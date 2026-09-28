package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
)

var errProfileBootstrapNotCurrent = errors.New("profile bootstrap guidance is not current")

type profileBootstrapConfirmRequest struct {
	ExpectedRevision *int64  `json:"expected_revision" binding:"required"`
	InputDigest      *string `json:"input_digest" binding:"required"`
}

type profileBootstrapGuidanceResponse struct {
	BootstrapGuidance *ProfileBootstrapGuidance `json:"bootstrap_guidance"`
}

// GetProfileBootstrapGuidance returns the generation-free bootstrap preview,
// if one exists for the current Profile revision.
//
//	@Summary		Get first-compile Profile guidance preview
//	@Description	Returns the current generation-free guidance preview before a Project has an active Profile generation.
//	@Tags			profile
//	@Produce		json
//	@Param			pid					path	string	true	"Project ID"
//	@Param			X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Success		200	{object}	profileBootstrapGuidanceResponse
//	@Failure		401	{object}	handler.ErrorResponse
//	@Failure		404	{object}	handler.ErrorResponse
//	@Failure		500	{object}	handler.ErrorResponse
//	@Security		DevUserAuth
//	@Security		ProjectHeader
//	@Router			/api/v1/projects/{pid}/profile/bootstrap-guidance [get]
func (h *Handler) GetProfileBootstrapGuidance(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	state, err := h.profileRepository.GetProfile(c.Request.Context(), userID, projectID)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	bootstrap := state.BootstrapGuidance
	if bootstrap != nil && (bootstrap.ProfileRevision != state.Revision || bootstrap.InputDigest != profileRequirementsDigest(state.Requirements)) {
		bootstrap = nil
	}
	c.JSON(http.StatusOK, profileBootstrapGuidanceResponse{BootstrapGuidance: bootstrap})
}

// ConfirmProfileBootstrapGuidance confirms the current generation-free
// guidance preview. The immutable artifact revision is part of the route key.
//
//	@Summary		Confirm first-compile Profile guidance
//	@Description	Confirms the current bootstrap guidance only when its immutable revision, Profile revision, and ordered requirements digest still match.
//	@Tags			profile
//	@Accept			json
//	@Produce		json
//	@Param			pid					path	string					true	"Project ID"
//	@Param			revision			path	string					true	"Immutable bootstrap artifact revision"
//	@Param			X-Project-ID	header	string					true	"Project ID; must match the URL"
//	@Param			request				body	profileBootstrapConfirmRequest	true	"Current Profile revision and requirements digest"
//	@Success		200					{object}	profileBootstrapGuidanceResponse
//	@Failure		400,401,404,500	{object}	handler.ErrorResponse
//	@Failure		409					{object}	profileConflictResponse
//	@Failure		428					{object}	handler.ErrorResponse
//	@Security		DevUserAuth
//	@Security		ProjectHeader
//	@Router			/api/v1/projects/{pid}/profile/bootstrap-guidance/{revision}/confirm [post]
func (h *Handler) ConfirmProfileBootstrapGuidance(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	revision := strings.TrimSpace(c.Param("revision"))
	if profileartifacts.BootstrapObjectPath(revision) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bootstrap guidance revision"})
		return
	}
	var request profileBootstrapConfirmRequest
	if err := decodeProfileJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	if request.ExpectedRevision == nil || request.InputDigest == nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "expected_revision and input_digest are required"})
		return
	}
	if *request.ExpectedRevision < 0 || !isProfileDigest(*request.InputDigest) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid expected_revision or input_digest"})
		return
	}
	state, err := h.profileRepository.ConfirmProfileBootstrapGuidance(c.Request.Context(), userID, projectID, revision, *request.InputDigest, *request.ExpectedRevision)
	if err != nil {
		if errors.Is(err, errProfileBootstrapNotCurrent) {
			state, readErr := h.profileRepository.GetProfile(c.Request.Context(), userID, projectID)
			if readErr != nil {
				h.writeProfileError(c, userID, projectID, readErr)
				return
			}
			c.JSON(http.StatusConflict, profileConflictResponse{Error: "profile bootstrap guidance is no longer current", Revision: state.Revision})
			return
		}
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, profileBootstrapGuidanceResponse{BootstrapGuidance: state.BootstrapGuidance})
}

func (r *firestoreProfileRepository) ConfirmProfileBootstrapGuidance(ctx context.Context, userID, projectID, revision, inputDigest string, expected int64) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		bootstrap := current.BootstrapGuidance
		if bootstrap == nil || bootstrap.Revision != revision || bootstrap.InputDigest != inputDigest ||
			bootstrap.ProfileRevision != current.Revision || bootstrap.InputDigest != profileRequirementsDigest(current.Requirements) {
			return errProfileBootstrapNotCurrent
		}
		if bootstrap.Status == profileBootstrapConfirmed {
			result = current
			return nil
		}
		if bootstrap.Status != profileBootstrapPreviewReady {
			return errProfileBootstrapNotCurrent
		}
		bootstrap.Status = profileBootstrapConfirmed
		bootstrap.ConfirmedAt = stringPtr(r.now().Format(time.RFC3339Nano))
		current.BootstrapGuidance = bootstrap
		result = current
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

// CompleteProfileBootstrapDerivation persists a current no-generation preview
// after an executor has created and verified the immutable artifact object.
func (h *Handler) CompleteProfileBootstrapDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest string, guidance ProfileBootstrapGuidance) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ProfileBootstrapDerivationSucceeded(ctx, userID, projectID, revision, attemptID, requirementsDigest, guidance)
}

func (r *firestoreProfileRepository) ProfileBootstrapDerivationSucceeded(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest string, guidance ProfileBootstrapGuidance) (ProfileState, error) {
	if r == nil || r.client == nil {
		return ProfileState{}, errors.New("Firestore is not configured")
	}
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if intent.AttemptID != attemptID || intent.Revision != revision || intent.Status != "running" ||
			intent.RequirementsDigest != requirementsDigest || current.DerivationStatus == nil ||
			*current.DerivationStatus != profileDerivationPending || current.Candidate != nil || current.BootstrapGuidance != nil ||
			profileRequirementsDigest(current.Requirements) != requirementsDigest {
			return errProfileBootstrapNotCurrent
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, attemptID)
		if err != nil {
			return err
		}
		if history.Revision != revision || history.RequirementsDigest != requirementsDigest || history.Status != "running" {
			return errors.New("profile derivation attempt history status mismatch")
		}
		if err := validateProfileBootstrapGuidance(guidance, current.Revision, requirementsDigest, current.Requirements); err != nil {
			return err
		}
		guidance.Status = profileBootstrapPreviewReady
		guidance.ConfirmedAt = nil
		current.BootstrapGuidance = &guidance
		current.DerivationStatus = stringPtr(profileDerivationReady)
		current.ScheduledFor = nil
		current.DerivationErrorCode = nil
		result = current
		intent.Status = profileDerivationReady
		intent.ScheduledFor = nil
		intent.ErrorCode = nil
		history.Status = profileDerivationReady
		history.ScheduledFor = nil
		history.ErrorCode = nil
		if err := tx.Set(historyRef, history); err != nil {
			return err
		}
		if err := tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

func validateProfileBootstrapGuidance(guidance ProfileBootstrapGuidance, revision int64, digest string, requirements []ProfileRequirement) error {
	if !isProfileDigest(guidance.Revision) || guidance.InputDigest != digest || guidance.ProfileRevision != revision ||
		guidance.Status != profileBootstrapPreviewReady || guidance.ConfirmedAt != nil ||
		guidance.ModelVersion == "" || guidance.PromptVersion == "" || guidance.SchemaVersion != profileartifacts.BootstrapGuidanceSchema ||
		len(guidance.Preview.Requirements) != len(requirements) {
		return errProfileTransitionInvalid
	}
	for index, requirement := range requirements {
		accounting := guidance.Preview.Requirements[index]
		if accounting.ID != requirement.ID || accounting.Explanation == "" ||
			(accounting.Disposition != "compile_guidance" && accounting.Disposition != "dictionary_or_query" && accounting.Disposition != "both" && accounting.Disposition != "limitation") {
			return fmt.Errorf("invalid bootstrap requirement accounting for %q: %w", requirement.ID, errProfileTransitionInvalid)
		}
	}
	return nil
}

func isProfileDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
