package v1

import (
	"context"
	"errors"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pipelineIntentQuotaStore interface {
	ReserveQuotaForPipelineIntent(context.Context, string, string, string, pipelinequota.Limits, time.Time, pipelinequota.ExecutionMode, bool, bool, int, int, int) (pipelinequota.Snapshot, bool, error)
	ReserveQuotaForPipelineIntentRetry(context.Context, string, string, string, string, pipelinequota.Limits, time.Time, pipelinequota.ExecutionMode, bool, bool, int, int, int) (pipelinequota.Snapshot, bool, error)
	GetPipelineIntent(context.Context, string, string, string) (scopedfirestore.QuotaReservation, bool, error)
	BeginPipelineIntentInvocation(context.Context, string, string, string, string) (scopedfirestore.QuotaReservation, bool, error)
	CompletePipelineIntentPreparation(context.Context, string, string, string, string, profileartifacts.BootstrapGuidanceRef) (bool, error)
	MarkPipelineIntentInvocationUnknown(context.Context, string, string, string, string) error
	FailPipelineIntent(context.Context, string, string, string, string, string, string, string, string) error
}

func (h *Handler) reservePipelineIntent(ctx context.Context, userID, projectID, reservationID string, mode pipelinequota.ExecutionMode, store pipelineIntentQuotaStore) (pipelinequota.Snapshot, bool, error) {
	alreadyRunning, err := h.isPipelineRunning(ctx, userID, projectID)
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	newRaw, rawDirty, annotationDirty, err := h.pendingWorkForProject(ctx, userID, projectID)
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	return store.ReserveQuotaForPipelineIntent(ctx, reservationID, userID, projectID, h.pipelineLimits(), time.Now().UTC(), mode,
		h.isDemoUser(userID), alreadyRunning, newRaw, rawDirty, annotationDirty)
}

func (h *Handler) reservePipelineIntentRetry(ctx context.Context, userID, projectID, reservationID, sourceReservationID string, mode pipelinequota.ExecutionMode, store pipelineIntentQuotaStore) (pipelinequota.Snapshot, bool, error) {
	alreadyRunning, err := h.isPipelineRunning(ctx, userID, projectID)
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	newRaw, rawDirty, annotationDirty, err := h.pendingWorkForProject(ctx, userID, projectID)
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	return store.ReserveQuotaForPipelineIntentRetry(ctx, reservationID, sourceReservationID, userID, projectID, h.pipelineLimits(), time.Now().UTC(), mode,
		h.isDemoUser(userID), alreadyRunning, newRaw, rawDirty, annotationDirty)
}

func (h *Handler) ensurePipelineIntentWork(ctx context.Context, reservation scopedfirestore.QuotaReservation) error {
	if h.firestore == nil || h.firestore.Raw() == nil || reservation.IntentStatus != "preparing" && reservation.IntentStatus != "ready" {
		return nil
	}
	client := h.firestore.Raw()
	work := profileruntime.Work{
		UserID: reservation.UserID, ProjectID: reservation.ProjectID, Kind: "pipeline",
		Revision: reservation.ProfileRevision, ID: reservation.ID, Due: time.Now().UTC(),
	}
	workRef := scopedfirestore.Collection(client, profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	reservationRef := scopedfirestore.Collection(client, "pipeline_quota_reservations").Doc(reservation.ID)
	return client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		currentSnapshot, err := tx.Get(reservationRef)
		if err != nil {
			return err
		}
		data := currentSnapshot.Data()
		if data["user_id"] != reservation.UserID || data["project_id"] != reservation.ProjectID || data["attempt_id"] != reservation.AttemptID {
			return errors.New("pipeline intent owner or attempt identity mismatch")
		}
		if data["settlement_status"] != "pending" || data["execution_id"] != "" || (data["intent_status"] != "preparing" && data["intent_status"] != "ready") {
			return nil
		}
		existing, err := tx.Get(workRef)
		if err == nil {
			var prior profileruntime.Work
			if err := existing.DataTo(&prior); err != nil {
				return err
			}
			if prior.Pending {
				return nil
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		work.Pending, work.Status = true, "pending"
		work.Attempts = 0
		work.Token = ""
		work.LeaseUntil = time.Time{}
		return tx.Set(workRef, work)
	})
}

func (d *ProfileDispatcher) executePipelineIntent(ctx context.Context, repo *firestoreProfileRepository, work profileruntime.Work) error {
	store, ok := d.Handler.effectiveQuotaStore().(pipelineIntentQuotaStore)
	if !ok {
		return errors.New("pipeline intent storage unavailable")
	}
	reservation, exists, err := store.GetPipelineIntent(ctx, work.UserID, work.ProjectID, work.ID)
	if err != nil || !exists {
		if err != nil {
			return err
		}
		return nil
	}
	if reservation.IntentStatus == "preparing" {
		if err := d.preparePipelineIntent(ctx, repo, store, reservation); err != nil {
			return err
		}
		reservation, exists, err = store.GetPipelineIntent(ctx, work.UserID, work.ProjectID, work.ID)
		if err != nil || !exists {
			return err
		}
	}
	if reservation.IntentStatus != "ready" {
		return nil
	}
	_, _, err = d.Handler.startPipelineIntent(ctx, store, reservation)
	return err
}

func (d *ProfileDispatcher) preparePipelineIntent(ctx context.Context, repo *firestoreProfileRepository, store pipelineIntentQuotaStore, reservation scopedfirestore.QuotaReservation) error {
	state, err := repo.GetProfile(ctx, reservation.UserID, reservation.ProjectID)
	if err != nil {
		return err
	}
	if state.Revision == reservation.ProfileRevision && profileRequirementsDigest(state.Requirements) == reservation.RequirementsDigest {
		if guidance := matchingBootstrapGuidance(state, reservation); guidance != nil {
			_, err := store.CompletePipelineIntentPreparation(ctx, reservation.ID, reservation.AttemptID, reservation.UserID, reservation.ProjectID, *guidance)
			return err
		}
		if state.DerivationStatus != nil && *state.DerivationStatus == profileDerivationPending {
			return errProfileRuntimeWaiting
		}
	}
	if d.Provider == nil || d.Handler.store == nil {
		return d.failPipelineIntentPreparation(ctx, store, reservation, errors.New("Profile derivation provider unavailable"))
	}
	requirements := make([]profilederive.Requirement, len(reservation.ProfileRequirements))
	for i, requirement := range reservation.ProfileRequirements {
		requirements[i] = profilederive.Requirement{ID: requirement.ID, Text: requirement.Text}
	}
	derived, err := d.Provider.DeriveBootstrap(ctx, reservation.ProfileRevision, requirements)
	if err != nil {
		return d.failPipelineIntentPreparation(ctx, store, reservation, err)
	}
	attempt := profilederive.Attempt{UserID: reservation.UserID, ProjectID: reservation.ProjectID, Revision: reservation.ProfileRevision, AttemptID: reservation.AttemptID}
	objects, err := (profileGCSArtifactFactory{root: d.Handler.store}).ProfileArtifacts(ctx, attempt)
	if err == nil {
		err = profileartifacts.WriteBootstrapGuidance(ctx, objects, derived.Ref, derived.Data)
	}
	if err != nil {
		return d.failPipelineIntentPreparation(ctx, store, reservation, err)
	}
	_, err = store.CompletePipelineIntentPreparation(ctx, reservation.ID, reservation.AttemptID, reservation.UserID, reservation.ProjectID, derived.Ref)
	return err
}

func matchingBootstrapGuidance(state ProfileState, reservation scopedfirestore.QuotaReservation) *profileartifacts.BootstrapGuidanceRef {
	guidance := state.BootstrapGuidance
	if guidance == nil || (guidance.Status != profileBootstrapPreviewReady && guidance.Status != profileBootstrapConfirmed) ||
		guidance.ProfileRevision != reservation.ProfileRevision || guidance.InputDigest != reservation.RequirementsDigest ||
		!profileartifacts.IsSHA256(guidance.Revision) || guidance.ModelVersion == "" || guidance.PromptVersion == "" ||
		guidance.SchemaVersion != profileartifacts.BootstrapGuidanceSchema {
		return nil
	}
	return &profileartifacts.BootstrapGuidanceRef{
		Revision: guidance.Revision, ProfileRevision: guidance.ProfileRevision, InputDigest: guidance.InputDigest,
		ModelVersion: guidance.ModelVersion, PromptVersion: guidance.PromptVersion, SchemaVersion: guidance.SchemaVersion,
	}
}

func (d *ProfileDispatcher) failPipelineIntentPreparation(ctx context.Context, store pipelineIntentQuotaStore, reservation scopedfirestore.QuotaReservation, cause error) error {
	diagnostic, ok := profilederive.FailureDiagnosticFromError(cause)
	stage, code := "profile_guidance_prepare", "profile_guidance_prepare_failed"
	if ok {
		stage, code = diagnostic.Stage, diagnostic.Class
	}
	if err := store.FailPipelineIntent(ctx, reservation.ID, reservation.AttemptID, reservation.UserID, reservation.ProjectID,
		"preparing", stage, code, "Could not prepare compile guidance for this saved Profile. Review the Profile and retry this run."); err != nil {
		return err
	}
	return nil
}

func (h *Handler) startPipelineIntent(ctx context.Context, store pipelineIntentQuotaStore, reservation scopedfirestore.QuotaReservation) (executionID, intentStatus string, resultErr error) {
	claimed, started, err := store.BeginPipelineIntentInvocation(ctx, reservation.ID, reservation.AttemptID, reservation.UserID, reservation.ProjectID)
	if err != nil {
		return "", "invocation_unknown", err
	}
	if !started {
		latest, exists, readErr := store.GetPipelineIntent(ctx, reservation.UserID, reservation.ProjectID, reservation.ID)
		if readErr != nil {
			return "", "invocation_unknown", readErr
		}
		if !exists {
			return "", "invocation_unknown", nil
		}
		return latest.ExecutionID, latest.IntentStatus, nil
	}
	executionID, err = h.invokePipelineJobStageWithReservation(ctx, claimed.UserID, claimed.ProjectID,
		claimed.CompileMode == pipelinequota.ModeFull, pipelineStageFull, claimed.ID)
	if err != nil {
		if errors.Is(err, errPipelineInvocationOutcomeUnknown) {
			_ = store.MarkPipelineIntentInvocationUnknown(context.WithoutCancel(ctx), claimed.ID, claimed.AttemptID, claimed.UserID, claimed.ProjectID)
			return "", "invocation_unknown", nil
		}
		message := "Pipeline could not start. Check service availability and retry this run."
		if errors.Is(err, auth.ErrAccountInactive) {
			message = "This account is inactive, so the Pipeline did not start."
		}
		failErr := store.FailPipelineIntent(context.WithoutCancel(ctx), claimed.ID, claimed.AttemptID, claimed.UserID, claimed.ProjectID,
			"invoking", "pipeline_invocation", "pipeline_invoke_failed", message)
		if failErr != nil {
			evidenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = h.writeConfirmedPipelineInvokeFailure(evidenceCtx, claimed.UserID, claimed.ProjectID, claimed.ID, claimed.AttemptID)
			cancel()
			_ = store.MarkPipelineIntentInvocationUnknown(context.WithoutCancel(ctx), claimed.ID, claimed.AttemptID, claimed.UserID, claimed.ProjectID)
			return "", "invocation_unknown", nil
		}
		return "", "failed", nil
	}
	if h.localPipeline == nil {
		if quotaStore := h.effectiveQuotaStore(); quotaStore != nil {
			if err := quotaStore.LinkQuotaReservation(ctx, claimed.ID, executionID); err != nil {
				_ = store.MarkPipelineIntentInvocationUnknown(context.WithoutCancel(ctx), claimed.ID, claimed.AttemptID, claimed.UserID, claimed.ProjectID)
				return executionID, "invocation_unknown", nil
			}
		}
	}
	return executionID, "compiling", nil
}

func isPipelineIntentPending(status string) bool {
	switch strings.TrimSpace(status) {
	case "preparing", "ready", "invoking", "invocation_unknown", "compiling":
		return true
	default:
		return false
	}
}

func pipelineIntentStatusDTO(reservation scopedfirestore.QuotaReservation) *handlerapi.PipelineIntentStatus {
	result := &handlerapi.PipelineIntentStatus{
		ID: reservation.ID, Status: reservation.IntentStatus, CompileMode: string(reservation.CompileMode),
		ExecutionID: reservation.ExecutionID, SavedProfileRevision: reservation.ProfileRevision,
		SavedProfileInputDigest: reservation.RequirementsDigest,
		ErrorStage:              reservation.IntentErrorStage, ErrorCode: reservation.IntentErrorCode,
		ErrorMessage: reservation.IntentErrorMessage,
	}
	if !reservation.CreatedAt.IsZero() {
		created := reservation.CreatedAt
		result.CreatedAt = &created
	}
	if !reservation.UpdatedAt.IsZero() {
		updated := reservation.UpdatedAt
		result.UpdatedAt = &updated
	}
	if reservation.ConsumedGuidanceKind != "" && reservation.ConsumedGuidanceKind != "none" {
		result.PreparedGuidance = &handlerapi.PipelinePreparedGuidance{
			Kind: reservation.ConsumedGuidanceKind, Status: reservation.ConsumedGuidanceStatus,
			Revision:              reservation.ConsumedGuidanceRevision,
			SourceProfileRevision: reservation.ConsumedGuidanceSourceRevision,
			SourceInputDigest:     reservation.ConsumedGuidanceInputDigest,
			ModelVersion:          reservation.ConsumedGuidanceModelVersion,
			PromptVersion:         reservation.ConsumedGuidancePromptVersion,
			SchemaVersion:         reservation.ConsumedGuidanceSchemaVersion,
			CandidateID:           reservation.ActiveCandidateID,
			ContentGeneration:     reservation.ActiveContentGeneration,
		}
	}
	return result
}
