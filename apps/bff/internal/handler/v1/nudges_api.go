package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
	"github.com/rayer/llm-wiki-bff/internal/releasenotes"
)

type nudgeAttentionRequest struct {
	AttentionID string `json:"attention_id"`
	Action      string `json:"action"`
}

// GetNudges is persisted-only. It never calls source adapters or models.
func (h *Handler) GetNudges(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	view, err := h.readNudgeView(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// ReconcileNudges is the account-scoped adapter entrypoint. Qualified source
// adapters are installed independently; absent adapters remain explicit and
// cannot be mistaken for recovery or an empty successful view.
func (h *Handler) ReconcileNudges(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	repository := nudge.NewRepository(h.firestore.Raw())
	failures, notices := h.reconcilePipelineFailureNudges(c.Request.Context(), userID, repository)
	releaseFailures, releaseNotices, releaseVisibility := h.reconcileReleaseNotesNudge(c.Request.Context(), userID, repository)
	failures = append(failures, releaseFailures...)
	notices = append(notices, releaseNotices...)
	view, err := repository.ReadWithPublicationVerifier(c.Request.Context(), userID, failures, notices,
		func(ctx context.Context, ownerID, projectID string, witness nudge.ProducerWitness) (bool, error) {
			identity, missing, err := currentContentContextPublication(ctx, h.store, ownerID, projectID)
			if err != nil {
				return false, err
			}
			return !missing && identity.GenerationID == witness.PublicationGeneration &&
				contentcontext.NormalizeDigest(identity.ConceptsDigest) == contentcontext.NormalizeDigest(witness.ConceptsDigest), nil
		})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	view = h.projectReleaseNotesNudgeVisibility(view, releaseVisibility)
	c.JSON(http.StatusOK, view)
}

func (h *Handler) readNudgeView(ctx context.Context, userID string) (nudge.View, error) {
	view, err := nudge.NewRepository(h.firestore.Raw()).ReadWithPublicationVerifier(ctx, userID, nil, nil,
		func(ctx context.Context, ownerID, projectID string, witness nudge.ProducerWitness) (bool, error) {
			identity, missing, err := currentContentContextPublication(ctx, h.store, ownerID, projectID)
			if err != nil {
				return false, err
			}
			return !missing && identity.GenerationID == witness.PublicationGeneration &&
				contentcontext.NormalizeDigest(identity.ConceptsDigest) == contentcontext.NormalizeDigest(witness.ConceptsDigest), nil
		})
	if err != nil {
		return view, err
	}
	visibility, _ := releasenotes.ReadVisibilitySnapshot(ctx, h.releaseNotesStore, h.releaseNotesRuntime)
	return h.projectReleaseNotesNudgeVisibility(view, visibility), nil
}

// reconcileReleaseNotesNudge is the Notes-owned adapter call on the common
// authenticated reconciliation path. It captures the common source revision
// before each Notes read, applies only the exact common wrapper, and reobserves
// once if another source update wins the common revision compare-and-swap.
func (h *Handler) reconcileReleaseNotesNudge(ctx context.Context, userID string, repository *nudge.Repository) ([]nudge.SourceFailure, []nudge.SourceNotice, *releasenotes.VisibilitySnapshot) {
	runtime := h.releaseNotesRuntime
	key := "release-notes:llm-wiki-cloud:" + runtime.Environment
	partial := func(safeClass string) ([]nudge.SourceFailure, []nudge.SourceNotice) {
		failures := []nudge.SourceFailure{{SafeClass: safeClass}}
		notices := []nudge.SourceNotice{}
		if runtime.Environment == "dev" || runtime.Environment == "prod" || runtime.Environment == "local" {
			notices = append(notices, nudge.SourceNotice{SourceKey: key, State: "stale", SafeClass: safeClass})
		}
		return failures, notices
	}
	if h.releaseNotesStore == nil {
		failures, notices := partial("release_source_unavailable")
		return failures, notices, nil
	}

	var visibility *releasenotes.VisibilitySnapshot
	for attempt := 0; attempt < 2; attempt++ {
		capturedSlot, err := repository.ReadSourceSlot(ctx, userID, nudge.CategoryReleaseNotes, key)
		if err != nil {
			failures, notices := partial("release_state_unavailable")
			return failures, notices, visibility
		}
		result, observedVisibility := releasenotes.ObserveSourceWithSnapshot(ctx, h.releaseNotesStore, runtime, capturedSlot.SourceRevision)
		visibility = observedVisibility
		failures := append([]nudge.SourceFailure(nil), result.Failures...)
		notices := append([]nudge.SourceNotice(nil), result.Notices...)
		if result.Status == "unavailable" {
			return failures, notices, visibility
		}
		if len(result.Upserts) > 1 || len(result.AuthoritativeClears) > 1 ||
			(len(result.Upserts) > 0 && len(result.AuthoritativeClears) > 0) {
			failed, noticed := partial("release_source_result_invalid")
			return failed, noticed, visibility
		}
		if len(result.Upserts) == 0 && len(result.AuthoritativeClears) == 0 {
			return failures, notices, visibility
		}

		if len(result.Upserts) == 1 {
			if result.ReleaseAliasTransfer != nil && capturedSlot.Active &&
				capturedSlot.AttentionMaterialRevision == "version:"+result.ReleaseAliasTransfer.RequestedVersion {
				_, err = repository.ApplyReleaseAliasTransfer(ctx, userID, result.Upserts[0],
					*result.ReleaseAliasTransfer, capturedSlot)
			} else {
				_, err = repository.ApplyObservation(ctx, userID, result.Upserts[0])
			}
		} else {
			_, err = repository.ApplyClear(ctx, userID, result.AuthoritativeClears[0])
		}
		if errors.Is(err, nudge.ErrSourceRevisionConflict) {
			if attempt == 0 {
				continue
			}
			failed, noticed := partial("release_source_revision_conflict")
			return failed, noticed, visibility
		}
		if err != nil {
			failed, noticed := partial("release_state_unavailable")
			return failed, noticed, visibility
		}
		return failures, notices, visibility
	}
	failed, noticed := partial("release_source_revision_conflict")
	return failed, noticed, visibility
}

// projectReleaseNotesNudgeVisibility is read-only. Old process instances keep
// their persisted slot and attention state, but a Release observation for a
// different trusted runtime version or environment is not shown as current.
func (h *Handler) projectReleaseNotesNudgeVisibility(view nudge.View,
	visibility *releasenotes.VisibilitySnapshot) nudge.View {
	runtime := h.releaseNotesRuntime
	key := "release-notes:llm-wiki-cloud:" + runtime.Environment
	marker := (*releasenotes.CurrentDeployment)(nil)
	if visibility != nil {
		marker = visibility.Index.CurrentDeployment
	}
	markerCurrent := releasenotes.MarkerMatches(marker, runtime)
	publicVersion := ""
	if marker != nil {
		publicVersion = marker.ProductVersion
		if marker.SchemaVersion == releasenotes.MarkerSchemaV2 {
			publicVersion = marker.ReleaseVersion
		}
	}
	items := make([]nudge.Item, 0, len(view.Items))
	for _, item := range view.Items {
		if item.Category != nudge.CategoryReleaseNotes {
			items = append(items, item)
			continue
		}
		currentObservations := make([]nudge.Slot, 0, 1)
		staleSource := false
		for _, slot := range item.Observations {
			if slot.SourceKey != key {
				continue
			}
			staleSource = true
			if markerCurrent && slot.Active && slot.ProducerWitness.Kind == "release-notes" &&
				slot.ProducerWitness.Environment == runtime.Environment &&
				slot.ProducerWitness.ProductVersion == publicVersion &&
				slot.ProducerWitness.IndexRevision == visibility.IndexRevision &&
				slot.ProducerWitness.IndexDigest == visibility.IndexDigest {
				currentObservations = append(currentObservations, slot)
			}
		}
		if len(currentObservations) > 0 {
			item.Observations = currentObservations
			items = append(items, item)
		} else if staleSource {
			view.Partial = true
			foundFailure := false
			for _, failure := range view.Failures {
				if failure.SafeClass == "release_currentness_unverified" && failure.ProjectID == "" {
					foundFailure = true
					break
				}
			}
			if !foundFailure {
				view.Failures = append(view.Failures, nudge.SourceFailure{SafeClass: "release_currentness_unverified"})
			}
			foundNotice := false
			for _, notice := range view.Notices {
				if notice.SourceKey == key && notice.State == "stale" {
					foundNotice = true
					break
				}
			}
			if !foundNotice {
				view.Notices = append(view.Notices, nudge.SourceNotice{
					SourceKey: key, State: "stale", SafeClass: "release_currentness_unverified"})
			}
		}
	}
	view.Items = items
	return view
}

// PatchNudgeAttention updates interaction state only, with an exact revision
// compare-and-swap. Source rows and source freshness fences are untouched.
func (h *Handler) PatchNudgeAttention(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	category := strings.TrimSpace(c.Param("category"))
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var body nudgeAttentionRequest
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
		return
	}
	repo := nudge.NewRepository(h.firestore.Raw())
	err := repo.PatchAttention(c.Request.Context(), userID, category, body.AttentionID, body.Action)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, nudge.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
	case errors.Is(err, nudge.ErrAttentionNotDismissible):
		c.JSON(http.StatusConflict, gin.H{"error": "attention_not_dismissible"})
	case errors.Is(err, nudge.ErrAttentionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "attention_revision_stale"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
	}
}
