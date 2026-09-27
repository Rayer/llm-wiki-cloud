package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

// ProfileCompileSuccess is the in-process proof passed from the successful
// compile publisher to Profile derivation. It has no JSON or HTTP projection.
type ProfileCompileSuccess struct {
	manifest                  generation.Manifest
	manifestGeneration        int64
	manifestSHA256            string
	canonicalConceptsDigest   string
	consumedBootstrapGuidance *profileartifacts.BootstrapGuidanceRef
}

// NewProfileCompileSuccess accepts only a fully successful publisher result.
// The concepts digest is taken from that committed manifest, and the typed
// bootstrap ref is copied from the worker's compile-start guidance pin.
func NewProfileCompileSuccess(manifest generation.Manifest, manifestGeneration int64, compileErr error, consumedBootstrapGuidance *profileartifacts.BootstrapGuidanceRef) (ProfileCompileSuccess, error) {
	if compileErr != nil {
		return ProfileCompileSuccess{}, errors.New("Profile compile did not complete successfully")
	}
	if err := manifest.Validate(); err != nil || manifestGeneration <= 0 {
		return ProfileCompileSuccess{}, errors.New("invalid committed Profile compile manifest")
	}
	concepts, conceptsListed := manifest.File("cache/concepts.jsonl")
	_, idMapListed := manifest.File("cache/id_map.json")
	if !conceptsListed || !idMapListed || !isLowerProfileDigest(concepts.SHA256) {
		return ProfileCompileSuccess{}, errors.New("committed Profile compile manifest is incomplete")
	}
	if consumedBootstrapGuidance != nil && (!isLowerProfileDigest(consumedBootstrapGuidance.Revision) ||
		!isLowerProfileDigest(consumedBootstrapGuidance.InputDigest) || consumedBootstrapGuidance.ProfileRevision < 1 ||
		strings.TrimSpace(consumedBootstrapGuidance.ModelVersion) == "" || strings.TrimSpace(consumedBootstrapGuidance.PromptVersion) == "" ||
		consumedBootstrapGuidance.SchemaVersion != profileartifacts.BootstrapGuidanceSchema) {
		return ProfileCompileSuccess{}, errors.New("invalid consumed Profile bootstrap guidance reference")
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return ProfileCompileSuccess{}, err
	}
	var consumedRef *profileartifacts.BootstrapGuidanceRef
	if consumedBootstrapGuidance != nil {
		copy := *consumedBootstrapGuidance
		consumedRef = &copy
	}
	success := ProfileCompileSuccess{
		manifest: manifest, manifestGeneration: manifestGeneration,
		manifestSHA256: profileartifacts.SHA256(manifestBytes), canonicalConceptsDigest: concepts.SHA256,
		consumedBootstrapGuidance: consumedRef,
	}
	if err := success.validate(); err != nil {
		return ProfileCompileSuccess{}, err
	}
	return success, nil
}

func (s ProfileCompileSuccess) validate() error {
	if s.manifest.Validate() != nil || s.manifestGeneration <= 0 || !isLowerProfileDigest(s.manifestSHA256) ||
		!isLowerProfileDigest(s.canonicalConceptsDigest) {
		return errors.New("invalid Profile compile success evidence")
	}
	concepts, conceptsListed := s.manifest.File("cache/concepts.jsonl")
	_, idMapListed := s.manifest.File("cache/id_map.json")
	manifestBytes, err := json.Marshal(s.manifest)
	if err != nil || !conceptsListed || !idMapListed || concepts.SHA256 != s.canonicalConceptsDigest ||
		profileartifacts.SHA256(manifestBytes) != s.manifestSHA256 {
		return errors.New("invalid Profile compile success evidence")
	}
	return nil
}

func (s ProfileCompileSuccess) matchesPinnedGeneration(snapshot gcs.GenerationSnapshot) bool {
	concepts, ok := snapshot.Manifest.File("cache/concepts.jsonl")
	return ok && snapshot.Manifest.GenerationID == s.manifest.GenerationID &&
		snapshot.ManifestGeneration == s.manifestGeneration && snapshot.ManifestSHA256 == s.manifestSHA256 &&
		concepts.SHA256 == s.canonicalConceptsDigest
}

// RunProfileDerivation executes one server-scheduled derivation attempt.
// The caller must authenticate the Cloud Run/service claim before passing the
// scoped identity; this method then reauthorizes the actual Project and the
// Firestore transitions reauthorize it transactionally.
func (h *Handler) RunProfileDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID string, provider *profilederive.Provider) (profilederive.Result, error) {
	project, err := h.AuthorizeProject(ctx, userID, projectID, ProjectEdit)
	if err != nil {
		return profilederive.Result{}, err
	}
	if h.store == nil {
		return profilederive.Result{}, errors.New("Profile derivation storage unavailable")
	}
	transitions := profileDerivationTransitions{handler: h, userID: project.UserID, projectID: project.ProjectID}
	content := profileGCSContentSource{root: h.store}
	artifacts := profileGCSArtifactFactory{root: h.store}
	executor := profilederive.Executor{Profiles: transitions, Content: content, Artifacts: artifacts, Provider: provider}
	return executor.Run(ctx, profilederive.Attempt{UserID: project.UserID, ProjectID: project.ProjectID, Revision: revision, AttemptID: attemptID})
}

// RunProfileCompileDerivation handles one successful compile reconciliation.
// The caller supplies only evidence made from a successful publisher result
// and the worker's compile-start guidance pin; this is not an HTTP input.
// Existing active Profiles get a dictionary-only candidate; first-generation
// promotion requires the exact confirmed bootstrap ref consumed by the batch.
func (h *Handler) RunProfileCompileDerivation(ctx context.Context, userID, projectID string, success ProfileCompileSuccess, revision int64, requirementsDigest string, provider *profilederive.Provider) (ProfileState, error) {
	project, err := h.AuthorizeProject(ctx, userID, projectID, ProjectEdit)
	if err != nil {
		return ProfileState{}, err
	}
	if err := success.validate(); err != nil {
		return ProfileState{}, err
	}
	if h.store == nil {
		return ProfileState{}, errors.New("Profile derivation storage unavailable")
	}
	state, err := h.profileRepository.GetProfile(ctx, project.UserID, project.ProjectID)
	if err != nil {
		return ProfileState{}, err
	}
	currentDigest := profileRequirementsDigest(state.Requirements)
	if state.Revision != revision || currentDigest != requirementsDigest {
		return ProfileState{}, &profileRevisionConflict{Latest: state.Revision}
	}
	generationID := success.manifest.GenerationID
	needsConcepts := len(state.Requirements) > 0 || state.Active != nil
	pinned, exists, err := (profileGCSContentSource{root: h.store}).pinCurrentProfileGeneration(ctx,
		profilederive.Attempt{UserID: project.UserID, ProjectID: project.ProjectID, Revision: revision, AttemptID: "compile-auto:" + generationID}, needsConcepts)
	if err != nil {
		return ProfileState{}, err
	}
	if !exists || !success.matchesPinnedGeneration(pinned.snapshot) {
		return ProfileState{}, errors.New("successful compile result is not the current committed generation")
	}
	if c := state.Candidate; c != nil && c.Source == "compile_auto" && c.BaseRevision == revision && c.ContentGeneration == generationID && c.RequirementsDigest == currentDigest {
		return state, nil
	}
	if len(state.Requirements) == 0 {
		if success.consumedBootstrapGuidance != nil {
			return ProfileState{}, errProfileBootstrapNotCurrent
		}
		if state.Active == nil {
			return state, nil
		}
	}
	if state.Active == nil {
		if !profileBootstrapRefMatches(state.BootstrapGuidance, success.consumedBootstrapGuidance, revision, currentDigest) {
			return ProfileState{}, errProfileBootstrapNotCurrent
		}
	}
	if provider == nil && len(state.Requirements) > 0 {
		return ProfileState{}, errors.New("Profile derivation provider is not configured")
	}
	idMap, err := wikiindex.DecodeIDMap(pinned.idMap)
	if err != nil {
		return ProfileState{}, fmt.Errorf("decode generation ID map: %w", err)
	}
	concepts, conceptsDigest, err := profilederive.ValidateConceptSnapshot(pinned.concepts, idMap)
	if err != nil {
		return ProfileState{}, err
	}
	if conceptsDigest != success.canonicalConceptsDigest {
		return ProfileState{}, errors.New("committed Profile compile concepts digest mismatch")
	}
	requirements := make([]profilederive.Requirement, len(state.Requirements))
	for i, requirement := range state.Requirements {
		requirements[i] = profilederive.Requirement{ID: requirement.ID, Text: requirement.Text}
	}
	var derived profilederive.CompileAutoResult
	if len(requirements) == 0 {
		neutral, neutralErr := profilederive.DeriveNeutral(revision, requirements, generationID, conceptsDigest)
		err = neutralErr
		derived = profilederive.CompileAutoResult{DictionaryRef: neutral.DictionaryRef, DictionaryData: neutral.DictionaryData, Preview: neutral.Preview}
	} else {
		derived, err = provider.DeriveCompileAuto(ctx, revision, requirements, generationID, conceptsDigest, concepts)
	}
	if err != nil {
		return ProfileState{}, err
	}
	objects, ok := h.store.Scope(project.UserID, project.ProjectID).(profileartifacts.ObjectStore)
	if !ok {
		return ProfileState{}, errors.New("create-only Profile artifact store unavailable")
	}
	if err := profileartifacts.WriteDictionary(ctx, objects, derived.DictionaryRef, derived.DictionaryData); err != nil {
		return ProfileState{}, err
	}
	preview := ProfilePreview{DictionaryDiff: derived.Preview.DictionaryDiff, Requirements: profileAccountingFromDeriver(derived.Preview.Requirements)}
	if state.Active != nil {
		return h.CompleteProfileCompileCandidate(ctx, project.UserID, project.ProjectID, generationID, revision, currentDigest, profileDerivedRefFromArtifact(derived.DictionaryRef), preview)
	}
	bootstrapRef := *success.consumedBootstrapGuidance
	bootstrapData, err := objects.ReadFileLimited(ctx, profileartifacts.BootstrapObjectPath(bootstrapRef.Revision), profileartifacts.MaxArtifactBytes)
	if err != nil {
		return ProfileState{}, fmt.Errorf("read confirmed bootstrap guidance: %w", err)
	}
	bootstrapEnvelope, err := profileartifacts.ValidateBootstrapGuidance(bootstrapData, bootstrapRef)
	if err != nil {
		return ProfileState{}, fmt.Errorf("validate confirmed bootstrap guidance: %w", err)
	}
	guidanceData, guidanceRef, err := profileartifacts.EncodeGuidance(profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: currentDigest,
		SourceContentGeneration: generationID, CanonicalConceptsDigest: conceptsDigest,
		ModelVersion: bootstrapRef.ModelVersion, PromptVersion: bootstrapRef.PromptVersion,
		CompileGuidance: bootstrapEnvelope.CompileGuidance,
	})
	if err != nil {
		return ProfileState{}, err
	}
	if err := profileartifacts.WriteGuidance(ctx, objects, guidanceRef, guidanceData); err != nil {
		return ProfileState{}, err
	}
	return h.CompleteProfileBootstrapCompileCandidate(ctx, project.UserID, project.ProjectID, success, revision, currentDigest,
		profileDerivedRefFromArtifact(derived.DictionaryRef), profileDerivedRefFromArtifact(guidanceRef), preview)
}

type profileDerivationTransitions struct {
	handler   *Handler
	userID    string
	projectID string
}

func (t profileDerivationTransitions) ClaimProfileDerivation(ctx context.Context, attempt profilederive.Attempt) (profilederive.ProfileSnapshot, error) {
	state, err := t.handler.ClaimProfileDerivation(ctx, t.userID, t.projectID, attempt.Revision, attempt.AttemptID)
	if err != nil {
		return profilederive.ProfileSnapshot{}, err
	}
	requirements := make([]profilederive.Requirement, len(state.Requirements))
	for i, requirement := range state.Requirements {
		requirements[i] = profilederive.Requirement{ID: requirement.ID, Text: requirement.Text}
	}
	return profilederive.ProfileSnapshot{
		Revision: state.Revision, Requirements: requirements, HasActive: state.Active != nil,
		Claimed: state.derivationClaimAcquired,
	}, nil
}

func (t profileDerivationTransitions) CompleteProfileBootstrap(ctx context.Context, attempt profilederive.Attempt, digest string, ref profileartifacts.BootstrapGuidanceRef, preview profilederive.Preview) error {
	guidance := ProfileBootstrapGuidance{
		Revision: ref.Revision, InputDigest: ref.InputDigest, ProfileRevision: ref.ProfileRevision,
		Status: profileBootstrapPreviewReady, ModelVersion: ref.ModelVersion, PromptVersion: ref.PromptVersion,
		SchemaVersion: ref.SchemaVersion, Preview: ProfileBootstrapPreview{
			GuidanceDiff: preview.GuidanceDiff, Requirements: profileAccountingFromDeriver(preview.Requirements),
		},
	}
	_, err := t.handler.CompleteProfileBootstrapDerivation(ctx, t.userID, t.projectID, attempt.Revision, attempt.AttemptID, digest, guidance)
	return err
}

func (t profileDerivationTransitions) CompleteProfileDerivation(ctx context.Context, attempt profilederive.Attempt, digest, generationID string, dictionary, guidance profileartifacts.DerivedRef, preview profilederive.Preview) error {
	_, err := t.handler.CompleteProfileDerivation(ctx, t.userID, t.projectID, attempt.Revision, attempt.AttemptID, digest, generationID,
		profileDerivedRefFromArtifact(dictionary), profileDerivedRefFromArtifact(guidance), ProfilePreview{
			DictionaryDiff: preview.DictionaryDiff, GuidanceDiff: preview.GuidanceDiff,
			Requirements: profileAccountingFromDeriver(preview.Requirements),
		})
	return err
}

func (t profileDerivationTransitions) FailProfileDerivation(ctx context.Context, attempt profilederive.Attempt, digest, errorCode string) error {
	_, err := t.handler.FailProfileDerivation(ctx, t.userID, t.projectID, attempt.Revision, attempt.AttemptID, digest, errorCode)
	return err
}

func profileDerivedRefFromArtifact(ref profileartifacts.DerivedRef) ProfileDerivedRef {
	return ProfileDerivedRef{Revision: ref.Revision, InputDigest: ref.InputDigest, ModelVersion: ref.ModelVersion, PromptVersion: ref.PromptVersion, SchemaVersion: ref.SchemaVersion}
}

func profileAccountingFromDeriver(rows []profilederive.RequirementAccounting) []ProfileRequirementAccounting {
	accounting := make([]ProfileRequirementAccounting, len(rows))
	for i, row := range rows {
		accounting[i] = ProfileRequirementAccounting{ID: row.ID, Disposition: row.Disposition, Explanation: row.Explanation}
	}
	return accounting
}

type profileGenerationStore interface {
	HasCurrentManifest(context.Context) (bool, error)
	PinCurrentGeneration(context.Context) (*gcs.Client, gcs.GenerationSnapshot, error)
}

type profileGCSContentSource struct{ root store.RootStore }

type profilePinnedGeneration struct {
	snapshot gcs.GenerationSnapshot
	idMap    []byte
	concepts []byte
}

func (s profileGCSContentSource) PinCurrentProfileContent(ctx context.Context, attempt profilederive.Attempt, needConcepts bool) (profilederive.PinnedContent, bool, error) {
	pinned, exists, err := s.pinCurrentProfileGeneration(ctx, attempt, needConcepts)
	if err != nil || !exists {
		return profilederive.PinnedContent{}, exists, err
	}
	return profilederive.PinnedContent{Generation: pinned.snapshot.Manifest.GenerationID, IDMap: pinned.idMap, Concepts: pinned.concepts}, true, nil
}

func (s profileGCSContentSource) pinCurrentProfileGeneration(ctx context.Context, attempt profilederive.Attempt, needConcepts bool) (profilePinnedGeneration, bool, error) {
	scoped, ok := s.root.Scope(attempt.UserID, attempt.ProjectID).(profileGenerationStore)
	if !ok {
		return profilePinnedGeneration{}, false, errors.New("generation-pinned Profile content reader unavailable")
	}
	exists, err := scoped.HasCurrentManifest(ctx)
	if err != nil {
		return profilePinnedGeneration{}, false, err
	}
	if !exists {
		return profilePinnedGeneration{}, false, nil
	}
	pinned, snapshot, err := scoped.PinCurrentGeneration(ctx)
	if err != nil {
		return profilePinnedGeneration{}, false, err
	}
	result := profilePinnedGeneration{snapshot: snapshot}
	if snapshot.Manifest.GenerationID == "" {
		return profilePinnedGeneration{}, false, errors.New("pinned generation manifest has no generation ID")
	}
	if !needConcepts {
		return result, true, nil
	}
	idMap, err := readProfileGenerationFile(ctx, pinned, snapshot.Manifest, "cache/id_map.json")
	if err != nil {
		return profilePinnedGeneration{}, false, err
	}
	concepts, err := readProfileGenerationFile(ctx, pinned, snapshot.Manifest, "cache/concepts.jsonl")
	if err != nil {
		return profilePinnedGeneration{}, false, err
	}
	result.idMap, result.concepts = idMap, concepts
	return result, true, nil
}

func readProfileGenerationFile(ctx context.Context, pinned *gcs.Client, manifest generation.Manifest, relPath string) ([]byte, error) {
	entry, ok := manifest.File(relPath)
	if !ok {
		return nil, fmt.Errorf("generation does not list %s", relPath)
	}
	if entry.Size > generation.MaxFileBytes {
		return nil, fmt.Errorf("generation file %s exceeds read limit", relPath)
	}
	data, err := pinned.ReadFileLimited(ctx, relPath, generation.MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read pinned generation file %s: %w", relPath, err)
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != entry.Size || hex.EncodeToString(digest[:]) != entry.SHA256 {
		return nil, fmt.Errorf("pinned generation file %s failed manifest validation", relPath)
	}
	return data, nil
}

type profileGCSArtifactFactory struct{ root store.RootStore }

func (f profileGCSArtifactFactory) ProfileArtifacts(_ context.Context, attempt profilederive.Attempt) (profileartifacts.ObjectStore, error) {
	objects, ok := f.root.Scope(attempt.UserID, attempt.ProjectID).(profileartifacts.ObjectStore)
	if !ok {
		return nil, errors.New("create-only Profile artifact store unavailable")
	}
	return objects, nil
}
