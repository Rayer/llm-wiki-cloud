package profilederive

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type Attempt struct {
	UserID    string
	ProjectID string
	Revision  int64
	AttemptID string
}

type ProfileSnapshot struct {
	Revision     int64
	Requirements []Requirement
	HasActive    bool
	Claimed      bool
}

type ProfileTransitions interface {
	ClaimProfileDerivation(context.Context, Attempt) (ProfileSnapshot, error)
	CompleteProfileBootstrap(context.Context, Attempt, string, profileartifacts.BootstrapGuidanceRef, Preview) error
	CompleteProfileDerivation(context.Context, Attempt, string, string, profileartifacts.DerivedRef, profileartifacts.DerivedRef, Preview) error
	FailProfileDerivation(context.Context, Attempt, string, string, FailureDiagnostic) error
}

type ContentSource interface {
	PinCurrentProfileContent(context.Context, Attempt, bool, bool) (PinnedContent, bool, error)
}

type ArtifactStoreFactory interface {
	ProfileArtifacts(context.Context, Attempt) (profileartifacts.ObjectStore, error)
}

type PinnedContent struct {
	Generation           string
	SourceSnapshotDigest string
	IDMap                []byte
	Concepts             []byte
}

type Result struct {
	Mode          string
	BootstrapRef  *profileartifacts.BootstrapGuidanceRef
	DictionaryRef *profileartifacts.DerivedRef
	GuidanceRef   *profileartifacts.DerivedRef
}

type Executor struct {
	Profiles  ProfileTransitions
	Content   ContentSource
	Artifacts ArtifactStoreFactory
	Provider  *Provider
}

func (e *Executor) Run(ctx context.Context, attempt Attempt) (result Result, resultErr error) {
	ctx, runSpan := observability.StartProfileSpan(ctx, "profile.derive")
	defer func() {
		outcome := "success"
		if resultErr != nil {
			outcome = "failure"
		}
		observability.FinishProfileSpan(runSpan, outcome)
	}()
	if e == nil || e.Profiles == nil || e.Content == nil || e.Artifacts == nil ||
		strings.TrimSpace(attempt.UserID) == "" || strings.TrimSpace(attempt.ProjectID) == "" || attempt.Revision < 1 || attempt.AttemptID == "" {
		return Result{}, errors.New("invalid Profile derivation executor input")
	}
	claimCtx, claimSpan := observability.StartProfileSpan(ctx, "profile.derive.claim")
	profile, err := e.Profiles.ClaimProfileDerivation(claimCtx, attempt)
	claimOutcome := "committed"
	if err != nil {
		claimOutcome = "failure"
	} else if !profile.Claimed {
		claimOutcome = "no_op"
	}
	observability.SetProfileOutcome(claimSpan, "profile.claim.outcome", claimOutcome)
	observability.FinishProfileSpan(claimSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
	if err != nil {
		return Result{}, err
	}
	if !profile.Claimed {
		return Result{Mode: "already_running"}, nil
	}
	inputDigest := RequirementsDigest(profile.Requirements)
	if profile.Revision != attempt.Revision {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_revision_stale", errors.New("claimed Profile revision mismatch"))
	}
	if err := validateRequirements(profile.Requirements); err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_requirements_invalid", err)
	}
	needsConcepts := len(profile.Requirements) > 0 || profile.HasActive
	contentCtx, contentSpan := observability.StartProfileSpan(ctx, "profile.derive.content_pin")
	content, generationExists, err := e.Content.PinCurrentProfileContent(contentCtx, attempt, needsConcepts, profile.HasActive)
	observability.FinishProfileSpan(contentSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
	if err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_unavailable", err)
	}
	if generationExists && len(profile.Requirements) > 0 && !profile.HasActive && content.SourceSnapshotDigest == "" {
		generationExists = false
	}
	if !generationExists {
		if len(profile.Requirements) == 0 || profile.HasActive {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_unavailable", errors.New("no current content generation"))
		}
		if e.Provider == nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_provider_unavailable", errors.New("Profile derivation provider is not configured"))
		}
		providerCtx, providerSpan := observability.StartProfileSpan(ctx, "profile.derive.provider")
		derived, err := e.Provider.DeriveBootstrap(providerCtx, profile.Revision, profile.Requirements)
		observability.SetProfileOutcome(providerSpan, "profile.provider.outcome", map[bool]string{true: "failure", false: "success"}[err != nil])
		observability.FinishProfileSpan(providerSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, failureCode(err), err)
		}
		objects, err := e.Artifacts.ProfileArtifacts(ctx, attempt)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_store_unavailable", err)
		}
		artifactCtx, artifactSpan := observability.StartProfileSpan(ctx, "profile.derive.artifact_write")
		writeErr := profileartifacts.WriteBootstrapGuidance(artifactCtx, objects, derived.Ref, derived.Data)
		observability.SetProfileOutcome(artifactSpan, "profile.artifact.outcome", map[bool]string{true: "failure", false: "success"}[writeErr != nil])
		observability.FinishProfileSpan(artifactSpan, map[bool]string{true: "failure", false: "success"}[writeErr != nil])
		if writeErr != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", writeErr)
		}
		commitCtx, commitSpan := observability.StartProfileSpan(ctx, "profile.derive.commit")
		commitErr := e.Profiles.CompleteProfileBootstrap(commitCtx, attempt, inputDigest, derived.Ref, derived.Preview)
		commitOutcome := "committed"
		if commitErr != nil {
			commitOutcome = "unknown"
		}
		observability.SetProfileOutcome(commitSpan, "profile.commit.outcome", commitOutcome)
		observability.FinishProfileSpan(commitSpan, map[bool]string{true: "failure", false: "success"}[commitErr != nil])
		if commitErr != nil {
			return Result{}, commitErr
		}
		return Result{Mode: "bootstrap_guidance", BootstrapRef: &derived.Ref}, nil
	}
	if content.Generation == "" {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_invalid", errors.New("pinned generation ID is empty"))
	}
	conceptsDigest := profileartifacts.SHA256(nil)
	concepts := []Concept{}
	if needsConcepts {
		idMap, err := wikiindex.DecodeIDMap(content.IDMap)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_invalid", fmt.Errorf("decode generation ID map: %w", err))
		}
		concepts, conceptsDigest, err = ValidateConceptSnapshot(content.Concepts, idMap)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_invalid", err)
		}
	}
	objects, err := e.Artifacts.ProfileArtifacts(ctx, attempt)
	if err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_store_unavailable", err)
	}
	if len(profile.Requirements) == 0 {
		derived, err := DeriveNeutral(profile.Revision, profile.Requirements, content.Generation, conceptsDigest)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_derivation_failed", err)
		}
		artifactCtx, artifactSpan := observability.StartProfileSpan(ctx, "profile.derive.artifact_write")
		writeErr := profileartifacts.WriteDictionary(artifactCtx, objects, derived.DictionaryRef, derived.DictionaryData)
		if writeErr == nil {
			writeErr = profileartifacts.WriteGuidance(artifactCtx, objects, derived.GuidanceRef, derived.GuidanceData)
		}
		observability.SetProfileOutcome(artifactSpan, "profile.artifact.outcome", map[bool]string{true: "failure", false: "success"}[writeErr != nil])
		observability.FinishProfileSpan(artifactSpan, map[bool]string{true: "failure", false: "success"}[writeErr != nil])
		if writeErr != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", writeErr)
		}
		commitCtx, commitSpan := observability.StartProfileSpan(ctx, "profile.derive.commit")
		commitErr := e.Profiles.CompleteProfileDerivation(commitCtx, attempt, inputDigest, content.Generation, derived.DictionaryRef, derived.GuidanceRef, derived.Preview)
		commitOutcome := "committed"
		if commitErr != nil {
			commitOutcome = "unknown"
		}
		observability.SetProfileOutcome(commitSpan, "profile.commit.outcome", commitOutcome)
		observability.FinishProfileSpan(commitSpan, map[bool]string{true: "failure", false: "success"}[commitErr != nil])
		if commitErr != nil {
			return Result{}, commitErr
		}
		return Result{Mode: "neutral_manual", DictionaryRef: &derived.DictionaryRef, GuidanceRef: &derived.GuidanceRef}, nil
	}
	if e.Provider == nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_provider_unavailable", errors.New("Profile derivation provider is not configured"))
	}
	providerCtx, providerSpan := observability.StartProfileSpan(ctx, "profile.derive.provider")
	derived, err := e.Provider.DeriveManual(providerCtx, profile.Revision, profile.Requirements, content.Generation, conceptsDigest, concepts)
	observability.SetProfileOutcome(providerSpan, "profile.provider.outcome", map[bool]string{true: "failure", false: "success"}[err != nil])
	observability.FinishProfileSpan(providerSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
	if err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, failureCode(err), err)
	}
	artifactCtx, artifactSpan := observability.StartProfileSpan(ctx, "profile.derive.artifact_write")
	writeErr := profileartifacts.WriteDictionary(artifactCtx, objects, derived.DictionaryRef, derived.DictionaryData)
	if writeErr == nil {
		writeErr = profileartifacts.WriteGuidance(artifactCtx, objects, derived.GuidanceRef, derived.GuidanceData)
	}
	observability.SetProfileOutcome(artifactSpan, "profile.artifact.outcome", map[bool]string{true: "failure", false: "success"}[writeErr != nil])
	observability.FinishProfileSpan(artifactSpan, map[bool]string{true: "failure", false: "success"}[writeErr != nil])
	if writeErr != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", writeErr)
	}
	commitCtx, commitSpan := observability.StartProfileSpan(ctx, "profile.derive.commit")
	commitErr := e.Profiles.CompleteProfileDerivation(commitCtx, attempt, inputDigest, content.Generation, derived.DictionaryRef, derived.GuidanceRef, derived.Preview)
	commitOutcome := "committed"
	if commitErr != nil {
		commitOutcome = "unknown"
	}
	observability.SetProfileOutcome(commitSpan, "profile.commit.outcome", commitOutcome)
	observability.FinishProfileSpan(commitSpan, map[bool]string{true: "failure", false: "success"}[commitErr != nil])
	if commitErr != nil {
		return Result{}, commitErr
	}
	return Result{Mode: "manual", DictionaryRef: &derived.DictionaryRef, GuidanceRef: &derived.GuidanceRef}, nil
}

func (e *Executor) fail(ctx context.Context, attempt Attempt, requirementsDigest, code string, cause error) error {
	diagnostic, ok := FailureDiagnosticFromError(cause)
	if !ok {
		diagnostic = failureDiagnosticForCode(code)
	}
	commitCtx, commitSpan := observability.StartProfileSpan(ctx, "profile.derive.commit")
	err := e.Profiles.FailProfileDerivation(commitCtx, attempt, requirementsDigest, code, diagnostic)
	commitOutcome := "committed"
	if err != nil {
		commitOutcome = "unknown"
	}
	observability.SetProfileOutcome(commitSpan, "profile.commit.outcome", commitOutcome)
	observability.FinishProfileSpan(commitSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func failureCode(err error) string {
	var diagnosticError *failureDiagnosticError
	if errors.As(err, &diagnosticError) && diagnosticError.cause != nil {
		err = diagnosticError.cause
	}
	if strings.Contains(err.Error(), "provider_model_unreported") {
		return "provider_model_unreported"
	}
	if strings.Contains(err.Error(), "provider call") || strings.Contains(err.Error(), "provider transport") {
		return "profile_provider_failed"
	}
	if strings.Contains(err.Error(), "provider is not configured") {
		return "profile_provider_unavailable"
	}
	return "profile_derivation_failed"
}

func failureDiagnosticForCode(code string) FailureDiagnostic {
	diagnostic := FailureDiagnostic{Stage: FailureStageUnknown, Class: FailureClassUnknown}
	switch code {
	case "profile_revision_stale":
		diagnostic = FailureDiagnostic{Stage: FailureStageRevisionClaim, Class: FailureClassStaleRevision}
	case "profile_requirements_invalid":
		diagnostic = FailureDiagnostic{Stage: FailureStageRequirements, Class: FailureClassInputInvalid}
	case "profile_generation_unavailable":
		diagnostic = FailureDiagnostic{Stage: FailureStageContentPin, Class: FailureClassContentUnavailable}
	case "profile_generation_invalid":
		diagnostic = FailureDiagnostic{Stage: FailureStageGenerationValidate, Class: FailureClassGenerationInvalid}
	case "profile_provider_unavailable":
		diagnostic = FailureDiagnostic{Stage: FailureStageProviderConfig, Class: FailureClassProviderUnavailable}
	case "profile_artifact_store_unavailable":
		diagnostic = FailureDiagnostic{Stage: FailureStageArtifactStore, Class: FailureClassArtifactStoreUnavailable}
	case "profile_artifact_write_failed":
		diagnostic = FailureDiagnostic{Stage: FailureStageArtifactWrite, Class: FailureClassArtifactWriteFailed}
	}
	return diagnostic
}
