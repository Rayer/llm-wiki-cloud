package profilederive

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
)

type transitionsStub struct {
	snapshot       ProfileSnapshot
	claimed        int
	bootstrap      *profileartifacts.BootstrapGuidanceRef
	dictionary     *profileartifacts.DerivedRef
	guidance       *profileartifacts.DerivedRef
	mode           string
	failedDigest   string
	failureCode    string
	completeErr    error
	alreadyRunning bool
}

func (r *transitionsStub) ClaimProfileDerivation(_ context.Context, _ Attempt) (ProfileSnapshot, error) {
	r.claimed++
	snapshot := r.snapshot
	snapshot.Claimed = !r.alreadyRunning
	return snapshot, nil
}
func (r *transitionsStub) CompleteProfileBootstrap(_ context.Context, _ Attempt, _ string, ref profileartifacts.BootstrapGuidanceRef, _ Preview) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	r.mode, r.bootstrap = "bootstrap_guidance", &ref
	return nil
}
func (r *transitionsStub) CompleteProfileDerivation(_ context.Context, _ Attempt, _ string, generation string, dictionary, guidance profileartifacts.DerivedRef, _ Preview) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	r.mode, r.dictionary, r.guidance = "manual:"+generation, &dictionary, &guidance
	return nil
}
func (r *transitionsStub) FailProfileDerivation(_ context.Context, _ Attempt, digest, code string) error {
	r.failedDigest, r.failureCode = digest, code
	return nil
}

type contentStub struct {
	content   PinnedContent
	exists    bool
	err       error
	called    int
	needsRows bool
}

func (c *contentStub) PinCurrentProfileContent(_ context.Context, _ Attempt, needConcepts, hasActive bool) (PinnedContent, bool, error) {
	c.called++
	c.needsRows = c.exists && needConcepts && (hasActive || c.content.SourceSnapshotDigest != "")
	return c.content, c.exists, c.err
}

type artifactsStub struct{ store *memoryArtifacts }

func (a artifactsStub) ProfileArtifacts(context.Context, Attempt) (profileartifacts.ObjectStore, error) {
	return a.store, nil
}

type memoryArtifacts struct {
	files map[string][]byte
}

func newMemoryArtifacts() *memoryArtifacts { return &memoryArtifacts{files: map[string][]byte{}} }
func (m *memoryArtifacts) ReadFileLimited(_ context.Context, path string, limit int64) ([]byte, error) {
	data, ok := m.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("too large")
	}
	return append([]byte(nil), data...), nil
}
func (m *memoryArtifacts) StatFile(_ context.Context, path string) (int64, error) {
	data, ok := m.files[path]
	if !ok {
		return 0, errors.New("not found")
	}
	return int64(len(data)), nil
}
func (m *memoryArtifacts) WriteFileIfGeneration(_ context.Context, data []byte, path string, expected int64) (int64, error) {
	if expected != 0 || m.files[path] != nil {
		return 0, errors.New("already exists")
	}
	m.files[path] = append([]byte(nil), data...)
	return 1, nil
}

func TestExecutorBootstrapsBeforeLegacyGenerationWithoutSnapshot(t *testing.T) {
	requirements := []Requirement{{ID: "r1", Text: "write short"}, {ID: "r2", Text: "tag family-friendly"}}
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 1, Requirements: requirements}}
	content := &contentStub{exists: true, content: PinnedContent{Generation: "legacy-generation"}}
	objects := newMemoryArtifacts()
	provider := NewProvider(&fakeChat{model: "fake-http-model", text: `{"compile_guidance":"Write concise entries.","guidance_diff":"Added concise-entry rule.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied to note writing."},{"id":"r2","disposition":"dictionary_or_query","explanation":"Saved for later query tagging."}]}`})
	result, err := (&Executor{Profiles: repo, Content: content, Artifacts: artifactsStub{objects}, Provider: provider}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 1, AttemptID: "attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "bootstrap_guidance" || result.BootstrapRef == nil || repo.claimed != 1 || repo.bootstrap == nil || repo.mode != result.Mode {
		t.Fatalf("result=%+v transitions=%+v", result, repo)
	}
	if repo.dictionary != nil || repo.guidance != nil || content.called != 1 || content.needsRows || len(objects.files) != 1 {
		t.Fatalf("bootstrap wrote corpus artifacts or read concepts: transitions=%+v reads=%d need=%v objects=%d", repo, content.called, content.needsRows, len(objects.files))
	}
	data := objects.files[profileartifacts.BootstrapObjectPath(result.BootstrapRef.Revision)]
	envelope, err := profileartifacts.ValidateBootstrapGuidance(data, *result.BootstrapRef)
	if err != nil || envelope.ProfileRevision != 1 || envelope.InputDigest != RequirementsDigest(requirements) {
		t.Fatalf("bootstrap object envelope=%+v err=%v", envelope, err)
	}
}

func TestExecutorDerivesManualArtifactsFromOnePinnedEmptyConceptSnapshot(t *testing.T) {
	requirements := []Requirement{{ID: "r1", Text: "write concise"}}
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 2, Requirements: requirements}}
	content := &contentStub{exists: true, content: PinnedContent{Generation: "G2", SourceSnapshotDigest: strings.Repeat("a", 64), IDMap: []byte(`{"concept":{}}`), Concepts: []byte{}}}
	objects := newMemoryArtifacts()
	provider := NewProvider(&fakeChat{model: "fake-http-model", text: `{"tags":[],"compile_guidance":"Write concise entries.","dictionary_diff":"No Tags requested.","guidance_diff":"Added concise writing rule.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Used for future note generation."}]}`})
	result, err := (&Executor{Profiles: repo, Content: content, Artifacts: artifactsStub{objects}, Provider: provider}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 2, AttemptID: "attempt-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "manual" || result.DictionaryRef == nil || result.GuidanceRef == nil || content.called != 1 || !content.needsRows || len(objects.files) != 2 {
		t.Fatalf("result=%+v content=(%d,%t) objects=%d", result, content.called, content.needsRows, len(objects.files))
	}
	if repo.mode != "manual:G2" || repo.dictionary.Revision != result.DictionaryRef.Revision || repo.guidance.Revision != result.GuidanceRef.Revision {
		t.Fatalf("transition refs diverged: %+v", repo)
	}
	if result.DictionaryRef.InputDigest != RequirementsDigest(requirements) {
		t.Fatalf("dictionary digest = %s", result.DictionaryRef.InputDigest)
	}
	if result.DictionaryRef.Revision == result.GuidanceRef.Revision {
		t.Fatal("dictionary/guidance unexpectedly share content-addressed revision")
	}
}

func TestExecutorNeutralClearUsesEmptyContextAndNoProviderCall(t *testing.T) {
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 3, Requirements: []Requirement{}, HasActive: true}}
	conceptID := "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	concepts := []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"" + conceptID + "\"}}\n")
	content := &contentStub{exists: true, content: PinnedContent{
		Generation: "G3", IDMap: []byte("{\"concept\":{\"" + conceptID + "\":\"alpha\"}}"), Concepts: concepts,
	}}
	objects := newMemoryArtifacts()
	result, err := (&Executor{Profiles: repo, Content: content, Artifacts: artifactsStub{objects}}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 3, AttemptID: "attempt-3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "neutral_manual" || !content.needsRows || len(objects.files) != 2 || repo.dictionary == nil || repo.guidance == nil {
		t.Fatalf("result=%+v content=%+v objects=%d transitions=%+v", result, content, len(objects.files), repo)
	}
	if repo.dictionary.InputDigest != "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945" {
		t.Fatalf("neutral input digest=%s", repo.dictionary.InputDigest)
	}
	conceptsDigest := profileartifacts.SHA256(concepts)
	dictionaryData := objects.files[profileartifacts.DictionaryObjectPath(repo.dictionary.Revision)]
	dictionary, err := profileartifacts.ValidateDictionary(dictionaryData, *repo.dictionary, "G3", conceptsDigest, []string{})
	if err != nil || dictionary.CanonicalConceptsDigest != conceptsDigest {
		t.Fatalf("neutral dictionary did not bind the pinned concepts bytes: digest=%q envelope=%+v err=%v", conceptsDigest, dictionary, err)
	}
	guidanceData := objects.files[profileartifacts.GuidanceObjectPath(repo.guidance.Revision)]
	guidance, err := profileartifacts.ValidateGuidance(guidanceData, *repo.guidance)
	if err != nil || guidance.CanonicalConceptsDigest != conceptsDigest {
		t.Fatalf("neutral guidance did not bind the pinned concepts bytes: digest=%q envelope=%+v err=%v", conceptsDigest, guidance, err)
	}
}

func TestExecutorRejectsStaleCompletionAndDoesNotChangeProfile(t *testing.T) {
	requirements := []Requirement{{ID: "r1", Text: "write short"}}
	stale := errors.New("profile revision conflict")
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 4, Requirements: requirements}, completeErr: stale}
	objects := newMemoryArtifacts()
	provider := NewProvider(&fakeChat{model: "fake-http-model", text: `{"compile_guidance":"Write short.","guidance_diff":"Added writing rule.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied."}]}`})
	_, err := (&Executor{Profiles: repo, Content: &contentStub{}, Artifacts: artifactsStub{objects}, Provider: provider}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 4, AttemptID: "old-attempt"})
	if !errors.Is(err, stale) || repo.bootstrap != nil || repo.failureCode != "" {
		t.Fatalf("stale completion result err=%v transition=%+v", err, repo)
	}
	// Immutable orphan bytes may remain after a save races completion, but no
	// current Profile pointer is committed by the rejected transition.
	if len(objects.files) != 1 {
		t.Fatalf("bootstrap artifact count=%d, want one immutable unreferenced artifact", len(objects.files))
	}
}

func TestExecutorFailureStoresBoundedCodeNotRawProviderError(t *testing.T) {
	requirements := []Requirement{{ID: "r1", Text: "write"}}
	provider := NewProvider(&fakeChat{model: "", text: "ignored response"})
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 5, Requirements: requirements}}
	objects := newMemoryArtifacts()
	_, err := (&Executor{Profiles: repo, Content: &contentStub{}, Artifacts: artifactsStub{objects}, Provider: provider}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 5, AttemptID: "attempt-5"})
	if err == nil || repo.failureCode != "provider_model_unreported" || repo.failedDigest != RequirementsDigest(requirements) || len(objects.files) != 0 {
		t.Fatalf("failure err=%v code=%q digest=%s artifacts=%d", err, repo.failureCode, repo.failedDigest, len(objects.files))
	}
	if strings.Contains(repo.failureCode, "ignored") {
		t.Fatal("raw provider output was persisted as error code")
	}
}

func TestExecutorDuplicateRunningAttemptDoesNotCallProviderAgain(t *testing.T) {
	requirements := []Requirement{{ID: "r1", Text: "write concise"}}
	repo := &transitionsStub{snapshot: ProfileSnapshot{Revision: 6, Requirements: requirements}, alreadyRunning: true}
	providerClient := &fakeChat{model: "fake-model", text: `{"compile_guidance":"Write concise.","guidance_diff":"Updated.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied to writing."}]}`}
	objects := newMemoryArtifacts()
	content := &contentStub{}
	result, err := (&Executor{Profiles: repo, Content: content, Artifacts: artifactsStub{objects}, Provider: NewProvider(providerClient)}).Run(context.Background(), Attempt{UserID: "owner", ProjectID: "alpha", Revision: 6, AttemptID: "running-attempt"})
	if err != nil || result.Mode != "already_running" || providerClient.calls != 0 || content.called != 0 || len(objects.files) != 0 {
		t.Fatalf("duplicate execution result=%+v err=%v provider_calls=%d content_reads=%d objects=%d", result, err, providerClient.calls, content.called, len(objects.files))
	}
}
