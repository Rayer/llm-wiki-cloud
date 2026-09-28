package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
)

// Synthetic local first-compile acceptance, using production dispatcher,
// provider HTTP clients, Firestore transitions and GCS immutable object client.
func TestProfileRuntimeConnectedBootstrapFirstCompile(t *testing.T) {
	f := newRuntimeFixture(t)
	objects := map[string]taggingGCSObject{}
	client, mu, reads := newTaggingGCSClient(t, objects)
	f.dispatcher.Handler.store = client
	reader, manifest := taggingInventoryFixture(t)
	prefix := "users/" + f.user + "/projects/" + f.project + "/"
	legacyManifest := manifest
	legacyManifest.GenerationID = "legacy-generation"
	legacyManifest.SourceSnapshotDigest = ""
	legacyManifestData, _ := json.Marshal(legacyManifest)
	mu.Lock()
	for _, file := range legacyManifest.Files {
		objects[prefix+legacyManifest.ObjectPath(file)] = taggingGCSObject{reader.files[file.Path], file.Generation}
	}
	legacyArchive, _ := generation.ArchivedManifestPath(legacyManifest.GenerationID)
	objects[prefix+legacyArchive] = taggingGCSObject{legacyManifestData, 21}
	objects[prefix+generation.ManifestPath] = taggingGCSObject{legacyManifestData, 20}
	objects[prefix+"raw/source.md"] = taggingGCSObject{[]byte("mutable legacy source"), 19}
	mu.Unlock()
	providerCalls, tagCalls := 0, 0
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = runtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Host, "127.0.0.1:") {
			return transport.RoundTrip(r)
		}
		if r.Header.Get("Authorization") != "Bearer bootstrap-local-key" {
			return nil, fmt.Errorf("unexpected nonlocal request: %s", r.URL.Host)
		}
		var response any
		switch r.URL.Host {
		case "api.deepseek.com":
			providerCalls++
			var out any
			if providerCalls == 1 {
				out = map[string]any{"compile_guidance": "Preserve explicit venue details.", "guidance_diff": "Add venue guidance", "requirements": []any{map[string]string{"id": "r1", "disposition": "both", "explanation": "Writing now and tags after first compile"}}}
			} else {
				out = map[string]any{"tags": []any{map[string]any{"id": "place", "definition": "a place", "applies_to": []string{"source", "concept"}, "match_rule": "explicit place", "non_match_rule": "explicitly not a place", "unknown_rule": "insufficient evidence", "requirement_ids": []string{"r1"}, "query_use": "preferred"}}, "dictionary_diff": "Add place tag", "requirements": []any{map[string]string{"id": "r1", "disposition": "both", "explanation": "Keep writing guidance and derive place tags"}}}
			}
			text, _ := json.Marshal(out)
			response = map[string]any{"model": "synthetic-derive", "choices": []any{map[string]any{"message": map[string]string{"content": string(text)}}}}
		case "api.typesafe.ai":
			tagCalls++
			answers := map[string]any{}
			for _, key := range []string{"applicable", "known", "match"} {
				answers[key] = map[string]any{"type": "noul", "noul": 0.95}
			}
			response = map[string]any{"model": "synthetic-jev", "answers": answers}
		default:
			return nil, fmt.Errorf("unconfigured host: %s", r.URL.Host)
		}
		b, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})
	f.dispatcher.Provider = profilederive.NewProvider(llm.NewClient("bootstrap-local-key"))
	f.dispatcher.Evaluator = profiletags.NewJevEvaluator("bootstrap-local-key")
	f.dispatcher.Policy = profiletags.ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"synthetic-jev"}, PromptVersion: profiletags.JevPromptVersion, SchemaVersion: profiletags.DecisionSchema}
	_, _ = f.save(t, 0, "Preserve venue details and prefer places")
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	state, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if state.BootstrapGuidance == nil || state.Active != nil || state.Candidate != nil || state.Job != nil || providerCalls != 1 {
		t.Fatalf("bootstrap manufactured generation state: %+v calls=%d", state, providerCalls)
	}
	if legacyManifest.SourceSnapshotDigest != "" {
		t.Fatal("legacy fixture unexpectedly has immutable Profile snapshot evidence")
	}
	mu.Lock()
	legacyReads := reads[prefix+legacyManifest.ObjectPath(legacyManifest.Files[0])] + reads[prefix+legacyManifest.ObjectPath(legacyManifest.Files[1])]
	mu.Unlock()
	if legacyReads != 0 {
		t.Fatalf("generation-free bootstrap read the legacy corpus: %d reads", legacyReads)
	}
	bootstrap := state.BootstrapGuidance
	state, err = f.repo.ConfirmProfileBootstrapGuidance(f.ctx, f.user, f.project, bootstrap.Revision, bootstrap.InputDigest, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	consumed := &profileartifacts.BootstrapGuidanceRef{Revision: bootstrap.Revision, ProfileRevision: state.Revision, InputDigest: bootstrap.InputDigest, ModelVersion: bootstrap.ModelVersion, PromptVersion: bootstrap.PromptVersion, SchemaVersion: bootstrap.SchemaVersion}
	legacyConcepts, _ := legacyManifest.File("cache/concepts.jsonl")
	legacyWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: legacyManifest.GenerationID, Due: f.clock}
	legacyReceipt := profileruntime.CompileReceipt{
		UserID: f.user, ProjectID: f.project, ExecutionID: "synthetic-legacy-compile", ProfileRevision: state.Revision,
		RequirementsDigest: bootstrap.InputDigest, ContentGeneration: legacyManifest.GenerationID,
		ManifestSHA256: generation.Digest(legacyManifestData), CanonicalConceptsDigest: legacyConcepts.SHA256,
		ManifestGeneration: 20, ConsumedBootstrapGuidance: consumed, CreatedAt: f.clock,
	}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if err = f.repo.client.RunTransaction(f.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if err := tx.Create(stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(legacyWork.ID), legacyReceipt); err != nil {
			return err
		}
		return profileruntime.Enqueue(tx, f.repo.client, legacyWork)
	}); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	legacyWorkRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(legacyWork))
	legacyWork = f.work(t, legacyWorkRef)
	state, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || !legacyWork.Pending || legacyWork.Status != "retry_wait" || state.Active != nil || state.Candidate != nil {
		t.Fatalf("legacy compile was promoted without immutable source evidence: work=%+v state=%+v err=%v", legacyWork, state, err)
	}
	// The new publisher output supplies the immutable snapshot needed before
	// the confirmed guidance can become a generation-bound candidate.
	manifestData, _ := json.Marshal(manifest)
	archive, _ := generation.ArchivedManifestPath(manifest.GenerationID)
	mu.Lock()
	for _, file := range manifest.Files {
		objects[prefix+manifest.ObjectPath(file)] = taggingGCSObject{reader.files[file.Path], file.Generation}
	}
	for path, data := range reader.files {
		if strings.HasPrefix(path, ".lwc/") {
			version := reader.generations[path]
			if version == 0 {
				version = 17
			}
			objects[prefix+path] = taggingGCSObject{data, version}
		}
	}
	objects[prefix+generation.ManifestPath] = taggingGCSObject{manifestData, 30}
	objects[prefix+archive] = taggingGCSObject{manifestData, 31}
	mu.Unlock()
	concepts, _ := manifest.File("cache/concepts.jsonl")
	receipt := profileruntime.CompileReceipt{UserID: f.user, ProjectID: f.project, ExecutionID: "synthetic-first-compile", ProfileRevision: state.Revision, RequirementsDigest: bootstrap.InputDigest, ContentGeneration: manifest.GenerationID, ManifestSHA256: generation.Digest(manifestData), CanonicalConceptsDigest: concepts.SHA256, ManifestGeneration: 30, ConsumedBootstrapGuidance: consumed, CreatedAt: f.clock}
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: manifest.GenerationID, Due: f.clock}
	if err = f.repo.client.RunTransaction(f.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if err := tx.Create(stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID), receipt); err != nil {
			return err
		}
		return profileruntime.Enqueue(tx, f.repo.client, work)
	}); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	state, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidate == nil || state.Active != nil || state.Candidate.Source != "compile_auto" || providerCalls != 2 {
		t.Fatalf("first compile not staged: %+v calls=%d", state, providerCalls)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	state, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active == nil || state.Active.ContentGeneration != manifest.GenerationID || state.Job.Status != profileJobReady || tagCalls != 2 {
		t.Fatalf("first compile inactive: %+v tags=%d", state, tagCalls)
	}
	data, err := client.WithScope(f.user, f.project).ReadFileLimited(f.ctx, profileartifacts.GuidanceObjectPath(state.Active.GuidanceRevision), profileartifacts.MaxArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	var guidance profileartifacts.GenerationGuidanceEnvelope
	if err = json.Unmarshal(data, &guidance); err != nil {
		t.Fatal(err)
	}
	if guidance.CompileGuidance != "Preserve explicit venue details." {
		t.Fatalf("consumed guidance not preserved: %+v", guidance)
	}
	mu.Lock()
	legacyBytes := string(objects[prefix+"raw/source.md"].data)
	mu.Unlock()
	if reads[prefix+"raw/source.md"] != 0 || legacyBytes != "mutable legacy source" {
		t.Fatalf("legacy mutable source was read or changed: reads=%d bytes=%q", reads[prefix+"raw/source.md"], legacyBytes)
	}
}
