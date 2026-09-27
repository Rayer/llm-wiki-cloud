package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
)

// All fixtures below are synthetic local fakes; no provider or cloud calls.
type taggingMemoryStore map[string][]byte

func (s taggingMemoryStore) Read(_ context.Context, p string, limit int) ([]byte, error) {
	b, ok := s[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if len(b) > limit {
		return nil, errors.New("limit")
	}
	return append([]byte(nil), b...), nil
}
func (s taggingMemoryStore) Create(_ context.Context, p string, b []byte) error {
	if old, ok := s[p]; ok && !bytes.Equal(old, b) {
		return errors.New("immutable conflict")
	}
	s[p] = append([]byte(nil), b...)
	return nil
}

type taggingFakeEvaluator struct {
	calls       map[profiletags.Kind]int
	failConcept bool
}

func (f *taggingFakeEvaluator) Evaluate(_ context.Context, i profiletags.Item, _ profiletags.Tag, _ profiletags.ProviderPolicy) (profiletags.Evaluation, error) {
	f.calls[i.Kind]++
	if i.Kind == profiletags.Concept && f.failConcept {
		return profiletags.Evaluation{}, errors.New("synthetic failure")
	}
	judgment := profiletags.Match
	if i.Kind == profiletags.Concept {
		judgment = profiletags.Unknown
	}
	return profiletags.Evaluation{Judgment: judgment, ReturnedModel: "fake-v1"}, nil
}

type taggingFakeRepository struct {
	profileRepository
	profileWorkerRepository
	state           ProfileState
	claimErr        error
	conflict        bool
	ambiguousCommit bool
	transitions     []string
	coverage        bool
	tags, rules     string
}

func (r *taggingFakeRepository) GetProfile(context.Context, string, string) (ProfileState, error) {
	return r.state, nil
}

func (r *taggingFakeRepository) ClaimProfileJob(context.Context, string, string, int64, string, string) (ProfileState, error) {
	if r.claimErr != nil {
		return ProfileState{}, r.claimErr
	}
	r.state.Job.Status = profileJobRunning
	return r.state, nil
}
func (r *taggingFakeRepository) ProfileJobTransition(_ context.Context, _, _ string, _ int64, _, _, next string, missing int64, _, tags, rules string, coverage bool) (ProfileState, error) {
	r.transitions = append(r.transitions, next)
	r.coverage = coverage
	r.tags, r.rules = tags, rules
	r.state.Job.Status = next
	r.state.Job.MissingCount = missing
	if r.conflict {
		r.state.Job.Status = profileJobIncomplete
	}
	if r.ambiguousCommit && next == profileJobReady {
		c := r.state.Candidate
		r.state.Active = &ProfileActive{CandidateID: c.CandidateID, ContentGeneration: c.ContentGeneration, DictionaryRevision: c.Dictionary.Revision, TagSetRevision: tags, QueryRuleRevision: rules, GuidanceRevision: c.Guidance.Revision}
		return ProfileState{}, errors.New("ambiguous synthetic commit")
	}
	return r.state, nil
}

func TestRunProfileTaggingReadsBackAmbiguousCommit(t *testing.T) {
	h, repo, _, load, evaluator, policy := taggingRunnerFixture(t)
	repo.ambiguousCommit = true
	if err := h.runProfileTagging(context.Background(), "u", "p", 1, "candidate", "job", evaluator, policy, load); err != nil {
		t.Fatal(err)
	}
}

func TestRunProfileTaggingNeutralDictionaryPublishesZeroCoverage(t *testing.T) {
	h, repo, objects, load, evaluator, policy := taggingRunnerFixture(t)
	c := repo.state.Candidate
	var envelope profileartifacts.DictionaryEnvelope
	if err := json.Unmarshal(objects[profileartifacts.DictionaryObjectPath(c.Dictionary.Revision)], &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Tags = []profileartifacts.Tag{}
	data, ref, err := profileartifacts.EncodeDictionary(envelope, []string{"r1"})
	if err != nil {
		t.Fatal(err)
	}
	c.Dictionary = profileDerivedRefFromArtifact(ref)
	objects[profileartifacts.DictionaryObjectPath(ref.Revision)] = data
	if err := h.runProfileTagging(context.Background(), "u", "p", 1, "candidate", "job", evaluator, policy, load); err != nil {
		t.Fatal(err)
	}
	if len(evaluator.calls) != 0 || !repo.coverage || repo.tags == "" || repo.rules == "" {
		t.Fatalf("neutral result=%+v calls=%v", repo, evaluator.calls)
	}
}
func taggingRunnerFixture(t *testing.T) (*Handler, *taggingFakeRepository, taggingMemoryStore, profileTagInputLoader, *taggingFakeEvaluator, profiletags.ProviderPolicy) {
	t.Helper()
	requirements := []ProfileRequirement{{ID: "r1", Text: "places"}}
	digest := profileRequirementsDigest(requirements)
	hash := strings.Repeat("a", 64)
	data, ref, err := profileartifacts.EncodeDictionary(profileartifacts.DictionaryEnvelope{SchemaVersion: profileartifacts.DictionarySchema, InputDigest: digest, ContentGeneration: "generation-1", CanonicalConceptsDigest: hash, ModelVersion: "fake-v1", PromptVersion: "fake-p1", Tags: []profileartifacts.Tag{{ID: "place", Definition: "place", AppliesTo: []string{"source", "concept"}, MatchRule: "yes", NonMatchRule: "no", UnknownRule: "uncertain", RequirementIDs: []string{"r1"}, QueryUse: "preferred"}}}, []string{"r1"})
	if err != nil {
		t.Fatal(err)
	}
	repo := &taggingFakeRepository{state: ProfileState{Revision: 1, Requirements: requirements, Candidate: &ProfileCandidate{CandidateID: "candidate", BaseRevision: 1, ContentGeneration: "generation-1", RequirementsDigest: digest, Dictionary: profileDerivedRefFromArtifact(ref)}, Job: &ProfileJob{JobID: "job", CandidateID: "candidate", ContentGeneration: "generation-1"}}}
	objects := taggingMemoryStore{profileartifacts.DictionaryObjectPath(ref.Revision): data}
	inventory := profiletags.Inventory{ContentGeneration: "generation-1", ConceptsDigest: hash, IDMapDigest: hash, SourceSnapshotDigest: hash, Items: []profiletags.Item{{Kind: profiletags.Source, StableID: "same", Content: []byte("raw")}, {Kind: profiletags.Concept, StableID: "same", Content: []byte("canonical concept")}}}
	loader := func(_ context.Context, g string) (profiletags.Inventory, profiletags.Store, error) {
		if g != "generation-1" {
			t.Fatalf("pinned %s", g)
		}
		return inventory, objects, nil
	}
	evaluator := &taggingFakeEvaluator{calls: map[profiletags.Kind]int{}}
	policy := profiletags.ProviderPolicy{ConfiguredModel: "fake-alias", AcceptedReturnedModels: []string{"fake-v1"}, PromptVersion: "fake-p1", SchemaVersion: "eval.v1"}
	return &Handler{profileRepository: repo}, repo, objects, loader, evaluator, policy
}
func TestRunProfileTaggingRetryReusesIndependentCompletedKeys(t *testing.T) {
	h, repo, objects, load, evaluator, policy := taggingRunnerFixture(t)
	evaluator.failConcept = true
	run := func() error {
		return h.runProfileTagging(context.Background(), "u", "p", 1, "candidate", "job", evaluator, policy, load)
	}
	if err := run(); err == nil {
		t.Fatal("incomplete succeeded")
	}
	if repo.state.Job.Status != profileJobIncomplete || repo.state.Job.MissingCount != 1 || repo.coverage || repo.tags != "" || repo.state.Active != nil {
		t.Fatalf("incomplete publication: %+v", repo)
	}
	if evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 3 {
		t.Fatalf("calls=%v", evaluator.calls)
	}
	evaluator.failConcept = false
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 4 || !repo.coverage || repo.state.Job.Status != profileJobReady {
		t.Fatalf("retry=%+v calls=%v", repo, evaluator.calls)
	}
	var set profiletags.TagSet
	if err := json.Unmarshal(objects[profiletags.SetPath(repo.tags)], &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Rows) != 2 || set.Rows[0].Judgment != profiletags.Match || set.Rows[1].Judgment != profiletags.Unknown || set.Rows[0].DecisionRevision == set.Rows[1].DecisionRevision {
		t.Fatalf("source/concept inherited: %+v", set)
	}
	// A new job reuses the previous complete set, independent of its checkpoint.
	repo.state.Active = &ProfileActive{ContentGeneration: "generation-1", DictionaryRevision: repo.state.Candidate.Dictionary.Revision, TagSetRevision: repo.tags, QueryRuleRevision: repo.rules}
	repo.state.Job.JobID = "job2"
	if err := h.runProfileTagging(context.Background(), "u", "p", 1, "candidate", "job2", evaluator, policy, load); err != nil {
		t.Fatal(err)
	}
	if evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 4 {
		t.Fatalf("prior set not reused: %v", evaluator.calls)
	}
}
func TestRunProfileTaggingFailsClosed(t *testing.T) {
	for _, tc := range []string{"early claim", "dictionary bytes", "inventory generation", "stale candidate", "activation conflict", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			h, repo, objects, load, evaluator, policy := taggingRunnerFixture(t)
			ctx := context.Background()
			switch tc {
			case "early claim":
				repo.claimErr = errProfileTransitionInvalid
			case "dictionary bytes":
				objects[profileartifacts.DictionaryObjectPath(repo.state.Candidate.Dictionary.Revision)] = []byte("{}")
			case "inventory generation":
				base := load
				load = func(ctx context.Context, g string) (profiletags.Inventory, profiletags.Store, error) {
					i, s, e := base(ctx, g)
					i.ContentGeneration = "generation-2"
					return i, s, e
				}
			case "stale candidate":
				repo.state.Candidate.BaseRevision = 2
			case "activation conflict":
				repo.conflict = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := h.runProfileTagging(ctx, "u", "p", 1, "candidate", "job", evaluator, policy, load); err == nil {
				t.Fatal("expected failure")
			}
			if tc != "activation conflict" && repo.coverage {
				t.Fatal("invalid coverage reported")
			}
			if tc == "early claim" && len(repo.transitions) != 0 {
				t.Fatal("transition after failed claim")
			}
		})
	}
}

type taggingSnapshotFake struct {
	files       map[string][]byte
	generations map[string]int64
	reads       map[string]int
}

func (s *taggingSnapshotFake) ReadFileLimited(_ context.Context, p string, _ int64) ([]byte, error) {
	s.reads[p]++
	b, ok := s.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return b, nil
}
func (s *taggingSnapshotFake) ReadImmutableFile(ctx context.Context, p string, g, limit int64) ([]byte, error) {
	if s.generations[p] != g {
		return nil, errors.New("wrong object generation")
	}
	return s.ReadFileLimited(ctx, p, limit)
}
func taggingInventoryFixture(t *testing.T) (*taggingSnapshotFake, generation.Manifest) {
	t.Helper()
	id := "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	sourceID := "source-1"
	raw := []byte("retained source bytes")
	rawDigest := generation.Digest(raw)
	ann := strings.Repeat("a", 64)
	receipts, _ := json.Marshal(sourcestatus.Artifact{Version: 1, Sources: map[string]sourcestatus.Receipt{sourceID: {RawPath: "raw/source.md", LastIngestedRawSHA256: rawDigest, LastIngestedAnnSHA256: ann, LastIngestFingerprint: sourcestatus.Fingerprint(rawDigest, ann), LastSuccessAt: "2026-09-25T00:00:00Z"}}})
	files := map[string][]byte{"cache/id_map.json": []byte(`{"concept":{"` + id + `":"alpha"},"source":{"` + sourceID + `":"source"}}`), "cache/concepts.jsonl": []byte(`{"slug":"alpha","title":"Alpha","frontmatter":{"id":"` + id + `"}}` + "\n"), "cache/source_status.json": receipts}
	manifest := generation.Manifest{Version: generation.Version, GenerationID: "generation-1", CreatedAt: "2026-09-25T00:00:00Z", InputFingerprint: "fixture"}
	for _, p := range []string{"cache/concepts.jsonl", "cache/id_map.json"} {
		b := files[p]
		manifest.Files = append(manifest.Files, generation.File{Path: p, Size: int64(len(b)), SHA256: generation.Digest(b), Generation: 7})
	}
	snapshot, hash, err := generation.EncodeSourceSnapshot(generation.SourceSnapshotManifest{SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: "generation-1", IDMapDigest: generation.Digest(files["cache/id_map.json"]), SourceStatusDigest: generation.Digest(receipts), Rows: []generation.SourceSnapshotRow{{StableID: sourceID, RawPath: "raw/source.md", ContentDigest: rawDigest, ObjectGeneration: 19}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceSnapshotDigest = hash
	path, _ := generation.SourceSnapshotPath(hash)
	files[path] = snapshot
	rawPath, _ := generation.SourceBytesPath(rawDigest)
	files[rawPath] = raw
	return &taggingSnapshotFake{files: files, generations: map[string]int64{rawPath: 19}, reads: map[string]int{}}, manifest
}
func TestProfileTagInventoryUsesOnlyExactImmutableInputs(t *testing.T) {
	reader, manifest := taggingInventoryFixture(t)
	inventory, err := readProfileTagInventory(context.Background(), reader, manifest, "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Items) != 2 || inventory.Items[0].Kind != profiletags.Source || inventory.Items[1].Kind != profiletags.Concept {
		t.Fatalf("inventory=%+v", inventory)
	}
	for p, count := range reader.reads {
		if count != 1 || p == "cache/source_status.json" || strings.HasPrefix(p, "raw/") || strings.HasPrefix(p, "wiki/") {
			t.Fatalf("unexpected read %s=%d", p, count)
		}
	}
	for _, tc := range []string{"current mismatch", "manifest bytes", "duplicate concepts", "missing source snapshot", "missing source bytes", "wrong source version", "missing source map", "snapshot binding"} {
		t.Run(tc, func(t *testing.T) {
			r, m := taggingInventoryFixture(t)
			switch tc {
			case "current mismatch":
				m.GenerationID = "generation-2"
			case "manifest bytes":
				r.files["cache/concepts.jsonl"] = []byte("changed")
			case "duplicate concepts":
				r.files["cache/concepts.jsonl"] = append(r.files["cache/concepts.jsonl"], r.files["cache/concepts.jsonl"]...)
				for i := range m.Files {
					if m.Files[i].Path == "cache/concepts.jsonl" {
						m.Files[i].SHA256 = generation.Digest(r.files[m.Files[i].Path])
						m.Files[i].Size = int64(len(r.files[m.Files[i].Path]))
					}
				}
			case "missing source snapshot":
				path, _ := generation.SourceSnapshotPath(m.SourceSnapshotDigest)
				delete(r.files, path)
			case "snapshot binding":
				path, _ := generation.SourceSnapshotPath(m.SourceSnapshotDigest)
				snapshot, err := generation.DecodeSourceSnapshot(r.files[path])
				if err != nil {
					t.Fatal(err)
				}
				snapshot.ContentGeneration = "generation-other"
				data, digest, err := generation.EncodeSourceSnapshot(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				m.SourceSnapshotDigest = digest
				path, _ = generation.SourceSnapshotPath(digest)
				r.files[path] = data

			case "missing source bytes":
				for p := range r.generations {
					delete(r.files, p)
				}
			case "wrong source version":
				for p := range r.generations {
					r.generations[p]++
				}
			case "missing source map":
				r.files["cache/id_map.json"] = []byte(`{"concept":{}}`)
				for i := range m.Files {
					if m.Files[i].Path == "cache/id_map.json" {
						m.Files[i].SHA256 = generation.Digest(r.files["cache/id_map.json"])
						m.Files[i].Size = int64(len(r.files["cache/id_map.json"]))
					}
				}
			}
			if _, err := readProfileTagInventory(context.Background(), r, m, "generation-1"); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	if !reflect.DeepEqual(inventory.Items[0].Content, []byte("retained source bytes")) {
		t.Fatal("source bytes changed")
	}
}

func TestProfileTagInventoryRejectsLegacyGenerationWithoutPinnedSourceSnapshot(t *testing.T) {
	reader, manifest := taggingInventoryFixture(t)
	snapshotPath, err := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := generation.DecodeSourceSnapshot(reader.files[snapshotPath])
	if err != nil {
		t.Fatal(err)
	}
	delete(reader.files, snapshotPath)
	for _, row := range snapshot.Rows {
		path, err := generation.SourceBytesPath(row.ContentDigest)
		if err != nil {
			t.Fatal(err)
		}
		delete(reader.files, path)
		reader.files[row.RawPath] = []byte("mutable current source")
	}
	manifest.SourceSnapshotDigest = "" // pre-Profile publisher archives omitted this field.

	inventory, err := readProfileTagInventory(context.Background(), reader, manifest, manifest.GenerationID)
	if err == nil {
		t.Fatalf("legacy generation unexpectedly produced source inventory: %+v", inventory)
	}
	if len(inventory.Items) != 0 {
		t.Fatalf("legacy generation returned unpinned items after failure: %+v", inventory.Items)
	}
	for path := range reader.reads {
		if strings.HasPrefix(path, "raw/") || strings.HasPrefix(path, generation.SourceSnapshotPrefix) {
			t.Fatalf("legacy tagging fell back to mutable or absent source data: %s", path)
		}
	}
}

func TestProfileTagInventoryExplicitEmptySnapshot(t *testing.T) {
	r, m := taggingInventoryFixture(t)
	r.files["cache/id_map.json"] = []byte(`{"concept":{},"source":{}}`)
	r.files["cache/concepts.jsonl"] = []byte{}
	for i := range m.Files {
		f := &m.Files[i]
		f.SHA256 = generation.Digest(r.files[f.Path])
		f.Size = int64(len(r.files[f.Path]))
	}
	data, digest, err := generation.EncodeSourceSnapshot(generation.SourceSnapshotManifest{SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: m.GenerationID, IDMapDigest: generation.Digest(r.files["cache/id_map.json"]), SourceStatusDigest: generation.Digest([]byte(`{"version":1,"sources":{}}`)), Rows: []generation.SourceSnapshotRow{}})
	if err != nil {
		t.Fatal(err)
	}
	m.SourceSnapshotDigest = digest
	path, _ := generation.SourceSnapshotPath(digest)
	r.files[path] = data
	inventory, err := readProfileTagInventory(context.Background(), r, m, m.GenerationID)
	if err != nil || len(inventory.Items) != 0 || inventory.ConceptsDigest != generation.Digest(nil) || inventory.SourceSnapshotDigest != digest {
		t.Fatalf("empty inventory=%+v err=%v", inventory, err)
	}
}
