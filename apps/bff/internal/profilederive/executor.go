package profilederive

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	FailProfileDerivation(context.Context, Attempt, string, string) error
}

type ContentSource interface {
	PinCurrentProfileContent(context.Context, Attempt, bool) (PinnedContent, bool, error)
}

type ArtifactStoreFactory interface {
	ProfileArtifacts(context.Context, Attempt) (profileartifacts.ObjectStore, error)
}

type PinnedContent struct {
	Generation string
	IDMap      []byte
	Concepts   []byte
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

func (e *Executor) Run(ctx context.Context, attempt Attempt) (Result, error) {
	if e == nil || e.Profiles == nil || e.Content == nil || e.Artifacts == nil ||
		strings.TrimSpace(attempt.UserID) == "" || strings.TrimSpace(attempt.ProjectID) == "" || attempt.Revision < 1 || attempt.AttemptID == "" {
		return Result{}, errors.New("invalid Profile derivation executor input")
	}
	profile, err := e.Profiles.ClaimProfileDerivation(ctx, attempt)
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
	content, generationExists, err := e.Content.PinCurrentProfileContent(ctx, attempt, needsConcepts)
	if err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_unavailable", err)
	}
	if !generationExists {
		if len(profile.Requirements) == 0 || profile.HasActive {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_generation_unavailable", errors.New("no current content generation"))
		}
		if e.Provider == nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_provider_unavailable", errors.New("Profile derivation provider is not configured"))
		}
		derived, err := e.Provider.DeriveBootstrap(ctx, profile.Revision, profile.Requirements)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, failureCode(err), err)
		}
		objects, err := e.Artifacts.ProfileArtifacts(ctx, attempt)
		if err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_store_unavailable", err)
		}
		if err := profileartifacts.WriteBootstrapGuidance(ctx, objects, derived.Ref, derived.Data); err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", err)
		}
		if err := e.Profiles.CompleteProfileBootstrap(ctx, attempt, inputDigest, derived.Ref, derived.Preview); err != nil {
			return Result{}, err
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
		if err := profileartifacts.WriteDictionary(ctx, objects, derived.DictionaryRef, derived.DictionaryData); err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", err)
		}
		if err := profileartifacts.WriteGuidance(ctx, objects, derived.GuidanceRef, derived.GuidanceData); err != nil {
			return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", err)
		}
		if err := e.Profiles.CompleteProfileDerivation(ctx, attempt, inputDigest, content.Generation, derived.DictionaryRef, derived.GuidanceRef, derived.Preview); err != nil {
			return Result{}, err
		}
		return Result{Mode: "neutral_manual", DictionaryRef: &derived.DictionaryRef, GuidanceRef: &derived.GuidanceRef}, nil
	}
	if e.Provider == nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_provider_unavailable", errors.New("Profile derivation provider is not configured"))
	}
	derived, err := e.Provider.DeriveManual(ctx, profile.Revision, profile.Requirements, content.Generation, conceptsDigest, concepts)
	if err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, failureCode(err), err)
	}
	if err := profileartifacts.WriteDictionary(ctx, objects, derived.DictionaryRef, derived.DictionaryData); err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", err)
	}
	if err := profileartifacts.WriteGuidance(ctx, objects, derived.GuidanceRef, derived.GuidanceData); err != nil {
		return Result{}, e.fail(ctx, attempt, inputDigest, "profile_artifact_write_failed", err)
	}
	if err := e.Profiles.CompleteProfileDerivation(ctx, attempt, inputDigest, content.Generation, derived.DictionaryRef, derived.GuidanceRef, derived.Preview); err != nil {
		return Result{}, err
	}
	return Result{Mode: "manual", DictionaryRef: &derived.DictionaryRef, GuidanceRef: &derived.GuidanceRef}, nil
}

func (e *Executor) fail(ctx context.Context, attempt Attempt, requirementsDigest, code string, cause error) error {
	if err := e.Profiles.FailProfileDerivation(ctx, attempt, requirementsDigest, code); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func failureCode(err error) string {
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
