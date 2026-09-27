package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
)

func TestProfileCompileSuccessCarriesOnlySuccessfulCommittedManifest(t *testing.T) {
	concepts := []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"01JAZ5N7Y3K8M2Q4R6T9VWXABC\"}}\n")
	idMap := []byte("{\"concept\":{\"01JAZ5N7Y3K8M2Q4R6T9VWXABC\":\"alpha\"}}")
	manifest := generation.Manifest{
		Version: generation.Version, GenerationID: "generation-1", CreatedAt: "2026-09-25T00:00:00Z", InputFingerprint: "fingerprint",
		Files: []generation.File{
			{Path: "cache/concepts.jsonl", Size: int64(len(concepts)), SHA256: profileartifacts.SHA256(concepts), Generation: 7},
			{Path: "cache/id_map.json", Size: int64(len(idMap)), SHA256: profileartifacts.SHA256(idMap), Generation: 8},
		},
	}
	bootstrapRef := &profileartifacts.BootstrapGuidanceRef{
		Revision: strings.Repeat("a", 64), ProfileRevision: 1, InputDigest: strings.Repeat("b", 64),
		ModelVersion: "fake-model", PromptVersion: "bootstrap-v1", SchemaVersion: profileartifacts.BootstrapGuidanceSchema,
	}
	success, err := NewProfileCompileSuccess(manifest, 9, nil, bootstrapRef)
	if err != nil || success.manifest.GenerationID != "generation-1" || success.manifestGeneration != 9 ||
		success.canonicalConceptsDigest != manifest.Files[0].SHA256 || success.consumedBootstrapGuidance == nil ||
		*success.consumedBootstrapGuidance != *bootstrapRef {
		t.Fatalf("compile success=%+v err=%v", success, err)
	}
	pinned := gcs.GenerationSnapshot{Manifest: manifest, ManifestGeneration: 9, ManifestSHA256: success.manifestSHA256}
	if !success.matchesPinnedGeneration(pinned) {
		t.Fatal("compile success did not match its committed generation snapshot")
	}
	pinned.ManifestGeneration++
	if success.matchesPinnedGeneration(pinned) {
		t.Fatal("compile success accepted a different manifest object generation")
	}
	pinned.ManifestGeneration--
	pinned.ManifestSHA256 = strings.Repeat("c", 64)
	if success.matchesPinnedGeneration(pinned) {
		t.Fatal("compile success accepted different committed manifest bytes")
	}
	pinned.ManifestSHA256 = success.manifestSHA256
	for i := range pinned.Manifest.Files {
		if pinned.Manifest.Files[i].Path == "cache/concepts.jsonl" {
			pinned.Manifest.Files[i].SHA256 = strings.Repeat("c", 64)
		}
	}
	if success.matchesPinnedGeneration(pinned) {
		t.Fatal("compile success accepted a different canonical concepts digest")
	}
	bootstrapRef.Revision = strings.Repeat("c", 64)
	if success.consumedBootstrapGuidance.Revision != strings.Repeat("a", 64) {
		t.Fatal("compile success retained a mutable worker pin pointer")
	}
	if _, err := NewProfileCompileSuccess(manifest, 9, errors.New("partial compile"), nil); err == nil {
		t.Fatal("compile failure produced success evidence")
	}
	incomplete := manifest
	incomplete.Files = []generation.File{manifest.Files[1]}
	if _, err := NewProfileCompileSuccess(incomplete, 9, nil, nil); err == nil {
		t.Fatal("partial generation manifest produced compile success evidence")
	}
	if err := (ProfileCompileSuccess{}).validate(); err == nil {
		t.Fatal("zero-value compile success evidence was accepted")
	}
}

func TestProfileBlankStateAndDebouncedSave(t *testing.T) {
	state := newProfileState("alpha")
	if state.ProjectID != "alpha" || state.Revision != 0 || state.Requirements == nil || len(state.Requirements) != 0 {
		t.Fatalf("blank state = %+v", state)
	}
	if state.DerivationStatus != nil || state.ScheduledFor != nil || state.Candidate != nil || state.BootstrapGuidance != nil || state.Active != nil || state.Job != nil {
		t.Fatalf("blank state has work fields set: %+v", state)
	}

	now := time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC)
	requirements := []ProfileRequirement{{ID: "r1", Text: "  preserve this  "}}
	saved, err := saveProfileState(state, 0, requirements, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.DerivationStatus == nil || *saved.DerivationStatus != profileDerivationPending {
		t.Fatalf("saved state = %+v", saved)
	}
	if saved.ScheduledFor == nil || *saved.ScheduledFor != now.Add(profileDebounce).Format(time.RFC3339Nano) {
		t.Fatalf("scheduled_for = %v, want %s", saved.ScheduledFor, now.Add(profileDebounce).Format(time.RFC3339Nano))
	}
	if saved.Requirements[0] != requirements[0] {
		t.Fatalf("requirement was normalized: got %+v want %+v", saved.Requirements[0], requirements[0])
	}
}

type profileRepositoryStub struct {
	state                    ProfileState
	job                      ProfileJob
	getErr                   error
	saveErr                  error
	confirmErr               error
	confirmBootstrapErr      error
	retryTaggingErr          error
	retryDerivationErr       error
	getJobErr                error
	saveCalls                int
	retryDerivationCalls     int
	confirmBootstrapCalls    int
	confirmBootstrapRevision string
	confirmBootstrapDigest   string
	confirmBootstrapExpected int64
}

func (r *profileRepositoryStub) GetProfile(context.Context, string, string) (ProfileState, error) {
	return r.state, r.getErr
}

func (r *profileRepositoryStub) SaveProfile(_ context.Context, _, _ string, _ int64, _ []ProfileRequirement) (ProfileState, error) {
	r.saveCalls++
	return r.state, r.saveErr
}

func (r *profileRepositoryStub) ConfirmProfileCandidate(context.Context, string, string, string, int64) (ProfileState, error) {
	return r.state, r.confirmErr
}

func (r *profileRepositoryStub) ConfirmProfileBootstrapGuidance(_ context.Context, _, _ string, revision, digest string, expected int64) (ProfileState, error) {
	r.confirmBootstrapCalls++
	r.confirmBootstrapRevision = revision
	r.confirmBootstrapDigest = digest
	r.confirmBootstrapExpected = expected
	return r.state, r.confirmBootstrapErr
}

func (r *profileRepositoryStub) RetryProfileTagging(context.Context, string, string, string, int64) (ProfileState, error) {
	return r.state, r.retryTaggingErr
}

func (r *profileRepositoryStub) RetryProfileDerivation(context.Context, string, string, int64) (ProfileState, error) {
	r.retryDerivationCalls++
	return r.state, r.retryDerivationErr
}

func (r *profileRepositoryStub) GetProfileJob(context.Context, string, string, string) (ProfileJob, error) {
	return r.job, r.getJobErr
}

func TestProfileAPIExpectedRevisionStatusAndLatestRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &profileRepositoryStub{state: ProfileState{ProjectID: "alpha", Revision: 7}, saveErr: &profileRevisionConflict{Latest: 7}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo

	missing := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"requirements":[]}`, h.PutProfile)
	if missing.Code != http.StatusPreconditionRequired || repo.saveCalls != 0 {
		t.Fatalf("missing expected_revision: status=%d saveCalls=%d body=%s", missing.Code, repo.saveCalls, missing.Body.String())
	}
	negative := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"expected_revision":-1,"requirements":[]}`, h.PutProfile)
	if negative.Code != http.StatusBadRequest || repo.saveCalls != 0 {
		t.Fatalf("negative expected_revision: status=%d saveCalls=%d body=%s", negative.Code, repo.saveCalls, negative.Body.String())
	}
	conflict := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"expected_revision":6,"requirements":[]}`, h.PutProfile)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"revision":7`) || repo.saveCalls != 1 {
		t.Fatalf("conflict: status=%d saveCalls=%d body=%s", conflict.Code, repo.saveCalls, conflict.Body.String())
	}
}

func TestProfileAPIRequiresMatchingProjectAndHidesNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &profileRepositoryStub{state: ProfileState{ProjectID: "alpha"}, getErr: errProfileProjectNotFound}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo

	mismatch := invokeProfileHandler(t, h, http.MethodGet, "alpha", "owner", "foreign", "", h.GetProfile)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatched project header status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	unauthenticated := invokeProfileHandler(t, h, http.MethodGet, "alpha", "", "alpha", "", h.GetProfile)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	notFound := invokeProfileHandler(t, h, http.MethodGet, "alpha", "owner", "alpha", "", h.GetProfile)
	if notFound.Code != http.StatusNotFound || strings.Contains(notFound.Body.String(), "owner") || strings.Contains(notFound.Body.String(), "foreign") {
		t.Fatalf("not-found response status=%d body=%s", notFound.Code, notFound.Body.String())
	}
}

func TestGetProfileGuidanceArtifactReturnsExactCurrentImmutableText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	inputDigest := profileRequirementsDigest([]ProfileRequirement{{ID: "req-1", Text: "write concise notes"}})
	bootstrapData, bootstrapRef, err := profileartifacts.EncodeBootstrapGuidance(profileartifacts.BootstrapGuidanceEnvelope{
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: 1, InputDigest: inputDigest,
		ModelVersion: "bootstrap-model-v1", PromptVersion: "bootstrap-prompt-v1", CompileGuidance: "Use the exact bootstrap text.",
	})
	if err != nil {
		t.Fatal(err)
	}
	guidanceData, guidanceRef, err := profileartifacts.EncodeGuidance(profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: inputDigest,
		SourceContentGeneration: "generation-1", CanonicalConceptsDigest: profileartifacts.SHA256([]byte("concepts")),
		ModelVersion: "candidate-model-v2", PromptVersion: "candidate-prompt-v3", CompileGuidance: "Use the exact candidate text.",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := newProfileState("alpha")
	state.Revision = 1
	state.Requirements = []ProfileRequirement{{ID: "req-1", Text: "write concise notes"}}
	state.BootstrapGuidance = &ProfileBootstrapGuidance{
		Revision: bootstrapRef.Revision, InputDigest: bootstrapRef.InputDigest, ProfileRevision: bootstrapRef.ProfileRevision,
		Status: profileBootstrapConfirmed, ModelVersion: bootstrapRef.ModelVersion, PromptVersion: bootstrapRef.PromptVersion,
		SchemaVersion: bootstrapRef.SchemaVersion,
	}
	state.Candidate = &ProfileCandidate{CandidateID: "candidate-1", Guidance: profileDerivedRefFromArtifact(guidanceRef)}
	objects := &profileDerivationIntegrationStore{objects: map[string][]byte{
		profileartifacts.BootstrapObjectPath(bootstrapRef.Revision): bootstrapData,
		profileartifacts.GuidanceObjectPath(guidanceRef.Revision):   guidanceData,
	}}
	h := New(&profileDerivationIntegrationRoot{RootStore: localfs.New(t.TempDir()), scoped: objects}, nil, nil, nil, nil, nil)
	h.profileRepository = &profileRepositoryStub{state: state}

	for _, tc := range []struct {
		name, revision, wantText, wantModel string
	}{
		{name: "bootstrap", revision: bootstrapRef.Revision, wantText: "Use the exact bootstrap text.", wantModel: "bootstrap-model-v1"},
		{name: "candidate", revision: guidanceRef.Revision, wantText: "Use the exact candidate text.", wantModel: "candidate-model-v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := invokeProfileGuidanceArtifact(t, h, tc.revision)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response profileGuidanceArtifactResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.GuidanceArtifact.Revision != tc.revision || response.GuidanceArtifact.CompileGuidance != tc.wantText ||
				response.GuidanceArtifact.ModelVersion != tc.wantModel || response.GuidanceArtifact.PromptVersion == "" ||
				response.GuidanceArtifact.SchemaVersion == "" || response.GuidanceArtifact.InputDigest != inputDigest {
				t.Fatalf("guidance artifact=%+v", response.GuidanceArtifact)
			}
		})
	}

	if recorder := invokeProfileGuidanceArtifact(t, h, strings.Repeat("f", 64)); recorder.Code != http.StatusNotFound {
		t.Fatalf("stale guidance reference status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func invokeProfileGuidanceArtifact(t *testing.T, h *Handler, revision string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/profile/guidance/"+revision, nil)
	c.Request.Header.Set("X-Project-ID", "alpha")
	c.Set("userID", "owner")
	c.Params = gin.Params{{Key: "pid", Value: "alpha"}, {Key: "revision", Value: revision}}
	h.GetProfileGuidanceArtifact(c)
	return recorder
}

func TestProfileDerivationRetryRequiresExpectedRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &profileRepositoryStub{state: newProfileState("alpha")}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo
	recorder := invokeProfileHandler(t, h, http.MethodPost, "alpha", "owner", "alpha", `{}`, h.RetryProfileDerivation)
	if recorder.Code != http.StatusPreconditionRequired || repo.retryDerivationCalls != 0 {
		t.Fatalf("retry missing revision: status=%d calls=%d body=%s", recorder.Code, repo.retryDerivationCalls, recorder.Body.String())
	}
}

func TestProfileInitialStateWireIncludesNullFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &profileRepositoryStub{state: newProfileState("alpha")}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo
	recorder := invokeProfileHandler(t, h, http.MethodGet, "alpha", "owner", "alpha", "", h.GetProfile)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, field := range []string{`"project_id":"alpha"`, `"revision":0`, `"requirements":[]`, `"derivation_status":null`, `"scheduled_for":null`, `"derivation_error_code":null`, `"candidate":null`, `"bootstrap_guidance":null`, `"confirmed_candidate_id":null`, `"active":null`, `"job":null`} {
		if !strings.Contains(recorder.Body.String(), field) {
			t.Fatalf("initial state omitted %s: %s", field, recorder.Body.String())
		}
	}
}

func TestProfileBootstrapGetAndConfirmUseRevisionAndDigest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requirements := []ProfileRequirement{{ID: "r1", Text: "write short"}, {ID: "r2", Text: "prefer useful tags"}}
	digest := profileRequirementsDigest(requirements)
	guidance := &ProfileBootstrapGuidance{
		Revision: strings.Repeat("a", 64), InputDigest: digest, ProfileRevision: 4, Status: profileBootstrapPreviewReady,
		ModelVersion: "fake-provider-model", PromptVersion: "bootstrap-v1", SchemaVersion: "profile.bootstrap-guidance.v1",
		Preview: ProfileBootstrapPreview{GuidanceDiff: "Writing guidance changed", Requirements: []ProfileRequirementAccounting{
			{ID: "r1", Disposition: "compile_guidance", Explanation: "Applied to compile writing."},
			{ID: "r2", Disposition: "dictionary_or_query", Explanation: "Reserved for dictionary and query derivation."},
		}},
	}
	repo := &profileRepositoryStub{state: ProfileState{ProjectID: "alpha", Revision: 4, Requirements: requirements, BootstrapGuidance: guidance}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo
	get := invokeProfileHandler(t, h, http.MethodGet, "alpha", "owner", "alpha", "", h.GetProfileBootstrapGuidance)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"bootstrap_guidance":{"revision":"`+strings.Repeat("a", 64)+`"`) {
		t.Fatalf("GET bootstrap guidance: status=%d body=%s", get.Code, get.Body.String())
	}
	confirm := invokeProfileHandler(t, h, http.MethodPost, "alpha", "owner", "alpha", `{"expected_revision":4,"input_digest":"`+digest+`"}`, h.ConfirmProfileBootstrapGuidance)
	if confirm.Code != http.StatusOK || repo.confirmBootstrapCalls != 1 || repo.confirmBootstrapRevision != guidance.Revision || repo.confirmBootstrapDigest != digest || repo.confirmBootstrapExpected != 4 {
		t.Fatalf("confirm bootstrap: status=%d request=(%s,%s,%d) body=%s", confirm.Code, repo.confirmBootstrapRevision, repo.confirmBootstrapDigest, repo.confirmBootstrapExpected, confirm.Body.String())
	}
	missingDigest := invokeProfileHandler(t, h, http.MethodPost, "alpha", "owner", "alpha", `{"expected_revision":4}`, h.ConfirmProfileBootstrapGuidance)
	if missingDigest.Code != http.StatusPreconditionRequired || repo.confirmBootstrapCalls != 1 {
		t.Fatalf("confirm without digest: status=%d calls=%d body=%s", missingDigest.Code, repo.confirmBootstrapCalls, missingDigest.Body.String())
	}
	stale := ProfileBootstrapGuidance{Revision: strings.Repeat("b", 64), InputDigest: digest, ProfileRevision: 3}
	repo.state.BootstrapGuidance = &stale
	getStale := invokeProfileHandler(t, h, http.MethodGet, "alpha", "owner", "alpha", "", h.GetProfileBootstrapGuidance)
	if getStale.Code != http.StatusOK || getStale.Body.String() != `{"bootstrap_guidance":null}` {
		t.Fatalf("GET stale preview did not suppress it: %d %s", getStale.Code, getStale.Body.String())
	}
}

func TestProfileAPIRejectsUnknownFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &profileRepositoryStub{state: newProfileState("alpha")}
	h := New(nil, nil, nil, nil, nil, nil)
	h.profileRepository = repo
	recorder := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"expected_revision":0,"requirements":[],"owner":"attacker"}`, h.PutProfile)
	if recorder.Code != http.StatusBadRequest || repo.saveCalls != 0 {
		t.Fatalf("unknown field: status=%d saveCalls=%d body=%s", recorder.Code, repo.saveCalls, recorder.Body.String())
	}
	missingText := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"expected_revision":0,"requirements":[{"id":"r1"}]}`, h.PutProfile)
	if missingText.Code != http.StatusBadRequest || repo.saveCalls != 0 {
		t.Fatalf("missing requirement text: status=%d saveCalls=%d body=%s", missingText.Code, repo.saveCalls, missingText.Body.String())
	}
	blankText := invokeProfileHandler(t, h, http.MethodPut, "alpha", "owner", "alpha", `{"expected_revision":0,"requirements":[{"id":"r1","text":""}]}`, h.PutProfile)
	if blankText.Code != http.StatusOK || repo.saveCalls != 1 {
		t.Fatalf("explicit blank requirement text: status=%d saveCalls=%d body=%s", blankText.Code, repo.saveCalls, blankText.Body.String())
	}
}

func invokeProfileHandler(t *testing.T, h *Handler, method, projectID, userID, headerProjectID, body string, handler func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/v1/projects/"+projectID+"/profile", strings.NewReader(body))
	c.Request.Header.Set("X-Project-ID", headerProjectID)
	c.Set("userID", userID)
	c.Params = gin.Params{{Key: "projectID", Value: projectID}, {Key: "pid", Value: projectID}, {Key: "revision", Value: strings.Repeat("a", 64)}}
	handler(c)
	return recorder
}

func TestProfileSaveConflictAndDuplicateRequirementIDs(t *testing.T) {
	state := newProfileState("alpha")
	state.Revision = 3
	_, err := saveProfileState(state, 2, []ProfileRequirement{}, time.Now())
	var conflict *profileRevisionConflict
	if !errors.As(err, &conflict) || conflict.Latest != 3 {
		t.Fatalf("stale save error = %#v, want conflict latest=3", err)
	}
	_, err = saveProfileState(state, 3, []ProfileRequirement{{ID: "same", Text: "one"}, {ID: "same", Text: "two"}}, time.Now())
	if !errors.Is(err, errInvalidProfileRequirements) {
		t.Fatalf("duplicate IDs error = %v, want invalid requirements", err)
	}
}

func TestEmptyProfileClearSchedulesNeutralDerivationAndPreservesActive(t *testing.T) {
	active := &ProfileActive{
		CandidateID: "old-candidate", ContentGeneration: "g1", DictionaryRevision: "d1",
		TagSetRevision: "t1", QueryRuleRevision: "q1", GuidanceRevision: "guide1",
	}
	state := newProfileState("alpha")
	state.Revision = 4
	state.Active = active
	now := time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC)
	saved, err := saveProfileState(state, 4, []ProfileRequirement{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 5 || len(saved.Requirements) != 0 || saved.DerivationStatus == nil || *saved.DerivationStatus != profileDerivationPending || saved.ScheduledFor == nil || saved.Job != nil {
		t.Fatalf("empty clear did not schedule neutral derivation: %+v", saved)
	}
	if *saved.ScheduledFor != now.Add(profileDebounce).Format(time.RFC3339Nano) {
		t.Fatalf("empty clear scheduled_for = %q, want %q", *saved.ScheduledFor, now.Add(profileDebounce).Format(time.RFC3339Nano))
	}
	if !sameProfileActive(saved.Active, active) {
		t.Fatalf("empty save changed active: got %+v want %+v", saved.Active, active)
	}
}

func TestProfileActivationIsAtomicAndIdempotent(t *testing.T) {
	oldActive := &ProfileActive{CandidateID: "old", ContentGeneration: "g1", DictionaryRevision: "d1", TagSetRevision: "t1", QueryRuleRevision: "q1", GuidanceRevision: "h1"}
	candidate := ProfileCandidate{
		CandidateID: "candidate", Source: "manual", BaseRevision: 2, RequirementsDigest: "digest", ContentGeneration: "g2",
		Dictionary: ProfileDerivedRef{Revision: "d2"}, Guidance: ProfileDerivedRef{Revision: "h1"},
	}
	state := ProfileState{Revision: 2, Requirements: []ProfileRequirement{{ID: "r1", Text: "x"}}, Candidate: &candidate, ConfirmedCandidateID: stringPtr(candidate.CandidateID), Active: cloneProfileActive(oldActive)}
	stateDigest := profileRequirementsDigest(state.Requirements)
	candidate.RequirementsDigest = stateDigest
	state.Candidate = &candidate
	job := storedProfileJob{
		ProfileJob:     ProfileJob{JobID: "job", CandidateID: candidate.CandidateID, ContentGeneration: candidate.ContentGeneration, Status: profileJobReady},
		TagSetRevision: "t2", QueryRuleRevision: "q2", CoverageComplete: true, ExpectedActive: cloneProfileActive(oldActive),
	}
	if !activateProfileState(&state, candidate, job) {
		t.Fatal("complete confirmed candidate did not activate")
	}
	wanted := &ProfileActive{CandidateID: "candidate", ContentGeneration: "g2", DictionaryRevision: "d2", TagSetRevision: "t2", QueryRuleRevision: "q2", GuidanceRevision: "h1"}
	if !sameProfileActive(state.Active, wanted) {
		t.Fatalf("active = %+v, want %+v", state.Active, wanted)
	}
	if !activateProfileState(&state, candidate, job) || !sameProfileActive(state.Active, wanted) {
		t.Fatal("duplicate ready/confirm transition regressed or rejected the already-active tuple")
	}
	job.CandidateID = "other"
	if activateProfileState(&state, candidate, job) {
		t.Fatal("job for another candidate activated")
	}
	job.CandidateID = candidate.CandidateID
	job.ContentGeneration = "wrong-generation"
	if activateProfileState(&state, candidate, job) {
		t.Fatal("job for another content generation activated")
	}
}

func TestProfileFailedDerivationRetryWithoutCandidate(t *testing.T) {
	state, err := saveProfileState(newProfileState("alpha"), 0, []ProfileRequirement{{ID: "r1", Text: "requirement"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	digest := profileRequirementsDigest(state.Requirements)
	failed, err := failProfileDerivation(state, state.Revision, digest, "derivation_unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Candidate != nil || failed.Active != nil || failed.DerivationStatus == nil || *failed.DerivationStatus != profileDerivationFailed {
		t.Fatalf("failed first derivation state = %+v", failed)
	}

	now := time.Date(2026, 9, 25, 1, 9, 0, 0, time.UTC)
	retried, err := retryFailedProfileDerivation(failed, failed.Revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Revision != failed.Revision || retried.Candidate != nil || retried.Active != nil || retried.DerivationStatus == nil || *retried.DerivationStatus != profileDerivationPending {
		t.Fatalf("retried state = %+v", retried)
	}
	if retried.ScheduledFor == nil || *retried.ScheduledFor != now.Format(time.RFC3339Nano) {
		t.Fatalf("retry scheduled_for = %v, want immediate server schedule %s", retried.ScheduledFor, now.Format(time.RFC3339Nano))
	}
	duplicate, err := retryFailedProfileDerivation(retried, retried.Revision, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if *duplicate.ScheduledFor != *retried.ScheduledFor {
		t.Fatalf("duplicate retry changed schedule from %s to %s", *retried.ScheduledFor, *duplicate.ScheduledFor)
	}
	var staleConflict *profileRevisionConflict
	if _, err := retryFailedProfileDerivation(failed, failed.Revision-1, now); !errors.As(err, &staleConflict) || staleConflict.Latest != failed.Revision {
		t.Fatalf("stale retry error = %v, want conflict latest=%d", err, failed.Revision)
	}
}

func TestProfileAuthorizationUsesRealProjectMetadata(t *testing.T) {
	tests := []struct {
		name  string
		docID string
		data  map[string]interface{}
		want  bool
	}{
		{name: "real project", docID: "owner_alpha", data: map[string]interface{}{"user_id": "owner", "project_id": "alpha", "status": "ready"}, want: true},
		{name: "legacy real project", docID: "owner_alpha", data: map[string]interface{}{"project_id": "alpha"}, want: true},
		{name: "foreign project", docID: "other_alpha", data: map[string]interface{}{"user_id": "other", "project_id": "alpha"}},
		{name: "idempotency marker", docID: "owner_key", data: map[string]interface{}{"user_id": "owner", "project_id": "alpha", "idempotency_key": "key"}},
		{name: "corrupt identity", docID: "owner_alpha", data: map[string]interface{}{"user_id": "owner", "project_id": "other"}},
		{name: "invalid marker", docID: "owner_alpha", data: map[string]interface{}{"user_id": "owner", "project_id": "alpha", "idempotency_key": "alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizeProjectDocument(tt.docID, tt.data, "owner", "alpha", ProjectRead)
			if got := err == nil; got != tt.want {
				t.Fatalf("authorize = %v, want allowed=%v (err=%v)", got, tt.want, err)
			}
		})
	}
}
