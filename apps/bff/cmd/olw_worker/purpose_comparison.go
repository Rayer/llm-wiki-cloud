package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

func runStoredPurposeComparison(ctx context.Context, cfg workerConfig, repository *contentcontext.FirestoreRepository, authority contentcontext.PublicationAuthority) {
	if err := runPurposeComparison(ctx, cfg, repository, authority, nil, contentContextFastIdentity{}); err != nil {
		reportContentContextReconcileFailure(err)
	}
}

// runPurposeComparison follows one existing successful/ready summary worker
// invocation. It can first recover one persisted pending delivery, then makes
// at most one new bounded comparison attempt for the exact current pair.
func runPurposeComparison(ctx context.Context, cfg workerConfig, repository *contentcontext.FirestoreRepository, authority contentcontext.PublicationAuthority, suppliedClient contentContextSummaryClient, suppliedIdentity contentContextFastIdentity) error {
	if repository == nil || repository.FirestoreClient() == nil || authority == nil {
		return purposeOperationError("purpose_capture", "storage_failure", "purpose comparison storage is unavailable", "")
	}
	client := repository.FirestoreClient()
	nudgeRepository := nudge.NewRepository(client)
	declaration, err := repository.CaptureDeclaredPurpose(ctx, cfg.UserID, cfg.ProjectID, authority)
	if err != nil {
		return purposeOperationError("purpose_capture", purposeErrorClass(err), err.Error(), "")
	}
	storedDeclaration, storedComparison, err := repository.ReadPurposeDocuments(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil {
		return purposeOperationError("purpose_read", "storage_failure", "purpose companion read failed", "")
	}
	if storedComparison != nil && storedComparison.State == "ready" && storedComparison.DeliveryStatus == "pending" {
		if storedDeclaration == nil || storedDeclaration.PairKey != declaration.PairKey || storedComparison.PairKey != declaration.PairKey {
			if err := repository.MarkPurposeDelivery(ctx, cfg.UserID, cfg.ProjectID, storedComparison.ConfirmationID, "stale"); err != nil {
				return purposeOperationError("purpose_recovery", "storage_failure", "stale purpose delivery marker could not be saved", storedComparison.AttemptID)
			}
		} else {
			state, readErr := repository.Read(ctx, cfg.UserID, cfg.ProjectID)
			if readErr != nil || state.LastGood == nil {
				return purposeOperationError("purpose_recovery", "storage_failure", "pending purpose comparison could not be revalidated", storedComparison.AttemptID)
			}
			deliverErr := deliverPurposeComparison(ctx, cfg, repository, authority, nudgeRepository, *storedDeclaration, *storedComparison, *state.LastGood)
			if errors.Is(deliverErr, nudge.ErrSourceRevisionConflict) || errors.Is(deliverErr, contentcontext.ErrAttemptRejected) || errors.Is(deliverErr, contentcontext.ErrPublicationChanged) {
				if err := repository.MarkPurposeDelivery(ctx, cfg.UserID, cfg.ProjectID, storedComparison.ConfirmationID, "stale"); err != nil {
					return purposeOperationError("purpose_recovery", "storage_failure", "stale purpose delivery marker could not be saved", storedComparison.AttemptID)
				}
			} else if deliverErr != nil {
				return purposeOperationError("purpose_recovery", "storage_failure", "pending purpose comparison delivery failed", storedComparison.AttemptID)
			}
		}
	}

	state, err := repository.Read(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil || state.LastGood == nil {
		return purposeOperationError("purpose_compare", "input_invalid", "current saved summary is unavailable", "")
	}
	sourceKey := "content-purpose-gap:" + cfg.ProjectID
	expectedSourceRevision, err := nudgeRepository.ReadSourceRevision(ctx, cfg.UserID, nudge.CategoryPurposeGap, sourceKey)
	if err != nil {
		return purposeOperationError("purpose_compare", "storage_failure", "purpose source revision could not be read", "")
	}
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	comparison, err := repository.BeginPurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, declaration, expectedSourceRevision, startedAt)
	if err != nil {
		return purposeOperationError("purpose_attempt", purposeErrorClass(err), "purpose comparison attempt could not be saved", "")
	}

	verdict, basis := "", ""
	lastGood := *state.LastGood
	switch {
	case strings.TrimSpace(declaration.Description) == "":
		verdict, basis = "insufficient_evidence", "No manual Project purpose is saved for comparison."
	case lastGood.TotalConcepts < 1 || lastGood.SelectedConcepts < 1:
		verdict, basis = "insufficient_evidence", "The saved overview has no verified concept coverage metadata."
	default:
		input, inputErr := contentcontext.PurposePairSummaryInput(declaration, lastGood)
		if inputErr != nil {
			_ = repository.FailPurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID, "input_invalid")
			return purposeOperationError("purpose_input", "input_invalid", "bounded purpose comparison input is invalid", comparison.AttemptID)
		}
		comparisonClient := suppliedClient
		identity := suppliedIdentity
		createdClient := false
		if comparisonClient == nil {
			comparisonClient, identity, err = newContentContextFastClient(ctx, cfg)
			if err != nil {
				_ = repository.FailPurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID, "provider_unavailable")
				return purposeOperationError("purpose_fast_client", "provider_unavailable", "purpose comparison provider is unavailable", comparison.AttemptID)
			}
			createdClient = true
		}
		if createdClient {
			defer comparisonClient.Close()
		}
		modelCtx, cancel := context.WithTimeout(ctx, contentContextModelTimeout)
		modelCtx = llm.WithCallStage(modelCtx, "project_purpose_compare")
		result, compareErr := comparisonClient.Complete(modelCtx, contentcontext.PurposeCompareSystemPrompt, string(input))
		cancel()
		if compareErr != nil {
			class := contentContextProviderErrorClass(compareErr)
			_ = repository.FailPurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID, class)
			return purposeOperationError("purpose_compare_model", class, "bounded purpose comparison did not complete", comparison.AttemptID)
		}
		parsed, parseErr := contentcontext.ParsePurposeCompareOutput([]byte(result.Text))
		if parseErr != nil || result.Metadata.ProfileID != identity.profileID || result.Metadata.ProfileRevision != identity.profileRevision ||
			result.Metadata.Provider != identity.provider || result.Metadata.Protocol != identity.protocol || result.Metadata.Role != string(llm.ProfileRoleFast) ||
			result.Metadata.RequestedModel != identity.model || result.Metadata.ReturnedModel == "" {
			_ = repository.FailPurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID, "invalid_response")
			return purposeOperationError("purpose_compare_validate", "invalid_response", "purpose comparison response did not match the pinned Fast identity", comparison.AttemptID)
		}
		verdict, basis = parsed.Verdict, parsed.Basis
	}

	ready, err := repository.CompletePurposeComparison(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID, verdict, basis, lastGood)
	if err != nil {
		_ = repository.MarkPurposeStale(ctx, cfg.UserID, cfg.ProjectID, authority, comparison.AttemptID)
		return purposeOperationError("purpose_compare_persist", purposeErrorClass(err), "purpose comparison result is stale or unavailable", comparison.AttemptID)
	}
	if err := deliverPurposeComparison(ctx, cfg, repository, authority, nudgeRepository, declaration, ready, lastGood); err != nil {
		if errors.Is(err, nudge.ErrSourceRevisionConflict) || errors.Is(err, contentcontext.ErrAttemptRejected) || errors.Is(err, contentcontext.ErrPublicationChanged) {
			_ = repository.MarkPurposeDelivery(ctx, cfg.UserID, cfg.ProjectID, ready.ConfirmationID, "stale")
			return purposeOperationError("purpose_source_fence", "stale_pair", "purpose comparison was superseded before delivery", ready.AttemptID)
		}
		return purposeOperationError("purpose_delivery", "storage_failure", "purpose comparison delivery is pending", ready.AttemptID)
	}
	if err := repository.MarkPurposeDelivery(ctx, cfg.UserID, cfg.ProjectID, ready.ConfirmationID, "applied"); err != nil {
		return purposeOperationError("purpose_delivery_marker", "storage_failure", "purpose delivery marker remains pending for recovery", ready.AttemptID)
	}
	return nil
}

func deliverPurposeComparison(ctx context.Context, cfg workerConfig, repository *contentcontext.FirestoreRepository, authority contentcontext.PublicationAuthority, nudgeRepository *nudge.Repository, declaration contentcontext.DeclaredPurposeContext, comparison contentcontext.PurposeComparison, summary contentcontext.LastGood) error {
	if comparison.State != "ready" || comparison.PairKey != declaration.PairKey || comparison.SummaryInputDigest != summary.InputDigest ||
		comparison.SummarySourceGeneration != summary.SourceGeneration || comparison.SummaryTextDigest != contentcontext.PurposeTextDigest(summary.Text) ||
		declaration.SummaryTextDigest != contentcontext.PurposeTextDigest(summary.Text) || comparison.PublicationGeneration != declaration.PublicationGeneration ||
		comparison.ConfirmationID == "" {
		return contentcontext.ErrAttemptRejected
	}
	witness := nudge.ProducerWitness{
		Kind: "purpose-comparison", ProjectID: cfg.ProjectID, AttemptID: comparison.AttemptID,
		ConfirmationID: comparison.ConfirmationID, PairKey: comparison.PairKey,
		DescriptionRevision: comparison.DescriptionRevision, PublicationGeneration: comparison.PublicationGeneration,
		ConceptsDigest: comparison.ConceptsDigest, SummaryInputDigest: comparison.SummaryInputDigest,
		AttemptStartedAt: comparison.AttemptStartedAt,
	}
	var upsert *nudge.ObservationInput
	var clear *nudge.ClearInput
	if comparison.Verdict == "clear_conflict" {
		projectName := purposeProjectName(ctx, repository.FirestoreClient(), cfg.UserID, cfg.ProjectID)
		observation := nudge.Observation{
			ProjectID: cfg.ProjectID, ProjectName: projectName,
			Title: "Project purpose and published overview may not match", Body: comparison.Basis,
			DeclaredPurpose: declaration.Description, PublishedSummary: summary.Text,
			Coverage: nudge.Coverage{TotalConcepts: summary.TotalConcepts, SelectedConcepts: summary.SelectedConcepts,
				Sampled: summary.Sampled, TitleTruncatedConcepts: summary.TitleTruncatedConcepts, BodyTruncatedConcepts: summary.BodyTruncatedConcepts},
			Severity: nudge.SeverityAttention,
			Actions: []nudge.Action{{ID: "copy-open-description", Kind: "copy-then-link",
				Label: "Copy overview and open " + projectName + " description settings", CopyText: summary.Text,
				Target: &nudge.Target{Kind: "project-description", ProjectID: cfg.ProjectID}}},
		}
		upsert = &nudge.ObservationInput{
			Category: nudge.CategoryPurposeGap, SourceKey: "content-purpose-gap:" + cfg.ProjectID,
			ExpectedSourceRevision: comparison.ExpectedSourceRevision, ProducerWitness: witness,
			PayloadDigest:             contentcontext.PurposeObservationDigest(declaration, comparison),
			AttentionMaterialRevision: declaration.PairKey, Observation: observation,
			ConfirmationID: comparison.ConfirmationID, EligibilityStartedAt: &comparison.AttemptStartedAt, ConflictedComparison: true,
		}
	} else {
		clear = &nudge.ClearInput{
			Category: nudge.CategoryPurposeGap, SourceKey: "content-purpose-gap:" + cfg.ProjectID,
			ExpectedSourceRevision: comparison.ExpectedSourceRevision, ProducerWitness: witness,
			Proof: "confirmed-no-conflict", ConfirmationID: comparison.ConfirmationID,
		}
	}
	return authority.WithProjectFence(ctx, cfg.UserID, cfg.ProjectID, func(fencedCtx context.Context) error {
		current, err := authority.CurrentPublication(fencedCtx, cfg.UserID, cfg.ProjectID)
		if err != nil || current.Generation != comparison.PublicationGeneration || contentcontext.NormalizeDigest(current.ConceptsDigest) != comparison.ConceptsDigest {
			return contentcontext.ErrPublicationChanged
		}
		if _, err := nudgeRepository.ApplyPurposeConfirmation(fencedCtx, cfg.UserID, upsert, clear); err != nil {
			return err
		}
		verified, err := authority.CurrentPublication(fencedCtx, cfg.UserID, cfg.ProjectID)
		if err != nil || verified.Generation != comparison.PublicationGeneration || contentcontext.NormalizeDigest(verified.ConceptsDigest) != comparison.ConceptsDigest {
			return contentcontext.ErrPublicationChanged
		}
		return nil
	})
}

func purposeProjectName(ctx context.Context, client *firestore.Client, ownerID, projectID string) string {
	if client == nil {
		return projectID
	}
	snapshot, err := scopedfirestore.Collection(client, "projects").Doc(ownerID + "_" + projectID).Get(ctx)
	if err != nil {
		return projectID
	}
	data := snapshot.Data()
	if data["user_id"] != ownerID || data["project_id"] != projectID {
		return projectID
	}
	name, _ := data["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return projectID
	}
	return name
}

func purposeErrorClass(err error) string {
	switch {
	case errors.Is(err, contentcontext.ErrAttemptRejected):
		return "stale_pair"
	case errors.Is(err, contentcontext.ErrPublicationChanged):
		return "stale_pair"
	case errors.Is(err, contentcontext.ErrPurposeUnavailable):
		return "input_invalid"
	default:
		return "storage_failure"
	}
}

func purposeOperationError(operation, class, message, attemptID string) error {
	return &contentContextOperationError{operation: operation, class: class, attemptID: attemptID, err: fmt.Errorf("%s", message)}
}
