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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Connected local E2E: real dispatcher/repositories, loopback Firestore/GCS,
// and actual provider adapters with synthetic HTTP responses only.
func TestProfileRuntimeConnectedManualThenCompileTagging(t *testing.T) {
	f := newRuntimeFixture(t)
	reader, manifest := taggingInventoryFixture(t)
	reader.files["cache/concepts.jsonl"] = []byte(strings.Replace(string(reader.files["cache/concepts.jsonl"]), `"title":"Alpha"`, `"title":"Alpha","body":"Alpha retained body","sources":["abcdef123456"]`, 1))
	// Query citations require the production stable source ID format.
	reader.files["cache/id_map.json"] = []byte(strings.ReplaceAll(string(reader.files["cache/id_map.json"]), "source-1", "abcdef123456"))
	reader.files["cache/source_status.json"] = []byte(strings.ReplaceAll(string(reader.files["cache/source_status.json"]), "source-1", "abcdef123456"))
	initialSnapshotPath, _ := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	initialSnapshot, err := generation.DecodeSourceSnapshot(reader.files[initialSnapshotPath])
	if err != nil {
		t.Fatal(err)
	}
	initialSnapshot.Rows[0].StableID = "abcdef123456"
	initialSnapshot.IDMapDigest = generation.Digest(reader.files["cache/id_map.json"])
	initialSnapshot.SourceStatusDigest = generation.Digest(reader.files["cache/source_status.json"])
	initialSnapshotBytes, initialDigest, err := generation.EncodeSourceSnapshot(initialSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceSnapshotDigest = initialDigest
	initialSnapshotPath, _ = generation.SourceSnapshotPath(initialDigest)
	reader.files[initialSnapshotPath] = initialSnapshotBytes
	for i := range manifest.Files {
		file := &manifest.Files[i]
		file.Size = int64(len(reader.files[file.Path]))
		file.SHA256 = generation.Digest(reader.files[file.Path])
	}
	objects := map[string]taggingGCSObject{}
	prefix := "users/" + f.user + "/projects/" + f.project + "/"
	client, mu, _ := newTaggingGCSClient(t, objects)
	f.dispatcher.Handler.store = client
	publish := func(currentGeneration int64) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		for _, file := range manifest.Files {
			objects[prefix+manifest.ObjectPath(file)] = taggingGCSObject{reader.files[file.Path], file.Generation}
		}
		for path, b := range reader.files {
			if strings.HasPrefix(path, ".lwc/") {
				version := reader.generations[path]
				if version == 0 {
					version = 17
				}
				objects[prefix+path] = taggingGCSObject{b, version}
			}
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := generation.ArchivedManifestPath(manifest.GenerationID)
		if err != nil {
			t.Fatal(err)
		}
		objects[prefix+archive] = taggingGCSObject{data, currentGeneration + 1}
		objects[prefix+generation.ManifestPath] = taggingGCSObject{data, currentGeneration}
	}
	publish(21)
	// This fixture has a committed content generation before any Profile state,
	// matching first activation for an existing project after Profile ships.
	legacyState, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || legacyState.Revision != 0 || legacyState.BootstrapGuidance != nil || legacyState.Active != nil {
		t.Fatalf("legacy generation should begin without Profile bootstrap or active state: %+v err=%v", legacyState, err)
	}
	assertRuntimeQuery(t, f, false, "Alpha", 200)

	deriveCalls, tagCalls := 0, 0
	priorTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = priorTransport })
	http.DefaultTransport = runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.deepseek.com" && request.URL.Host != "api.typesafe.ai" {
			if strings.HasPrefix(request.URL.Host, "127.0.0.1:") {
				return priorTransport.RoundTrip(request)
			}
			return nil, fmt.Errorf("unexpected nonlocal host %s", request.URL.Host)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if request.Method != "POST" || request.Header.Get("Authorization") != "Bearer synthetic-e2e-key" {
			return nil, fmt.Errorf("unexpected fake provider request")
		}
		var response any
		if request.URL.Host == "api.typesafe.ai" {
			tagCalls++
			var payload struct {
				State []string `json:"state"`
			}
			if err := json.Unmarshal(body, &payload); err != nil || len(payload.State) != 4 {
				return nil, fmt.Errorf("invalid Jev independent input")
			}
			if payload.State[0] != "source" && payload.State[0] != "concept" {
				return nil, fmt.Errorf("invalid Jev item kind")
			}
			response = map[string]any{"model": "fake-jev-v1", "answers": map[string]any{"applicable": map[string]any{"type": "noul", "noul": 1}, "known": map[string]any{"type": "noul", "noul": 1}, "match": map[string]any{"type": "noul", "noul": 1}}}
		} else {
			deriveCalls++
			if !strings.Contains(string(body), "prefer places and concise notes") {
				return nil, fmt.Errorf("requirements absent from provider request")
			}
			tag := profileartifacts.Tag{ID: "place", Definition: "a place", AppliesTo: []string{"source", "concept"}, MatchRule: "explicit place", NonMatchRule: "explicit non-place", UnknownRule: "ambiguous", RequirementIDs: []string{"r1"}, QueryUse: "preferred"}
			var output any
			if deriveCalls <= 2 {
				output = profilederive.ManualOutput{Tags: []profileartifacts.Tag{tag}, CompileGuidance: "Write concise notes.", DictionaryDiff: "Added place.", GuidanceDiff: "Added concise notes.", Requirements: []profilederive.RequirementAccounting{{ID: "r1", Disposition: "both", Explanation: "Dictionary and writing guidance."}}}
			} else {
				output = profilederive.DictionaryOutput{Tags: []profileartifacts.Tag{tag}, DictionaryDiff: "Refreshed generation.", Requirements: []profilederive.RequirementAccounting{{ID: "r1", Disposition: "dictionary_or_query", Explanation: "Represented by place preference."}}}
			}
			content, err := json.Marshal(output)
			if err != nil {
				return nil, err
			}
			response = map[string]any{"model": "fake-derive-v1", "choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}}
		}
		data, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	f.dispatcher.Provider = profilederive.NewProvider(llm.NewClient("synthetic-e2e-key"))
	f.dispatcher.Evaluator = profiletags.NewJevEvaluator("synthetic-e2e-key")
	f.dispatcher.Policy = profiletags.ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"fake-jev-v1"}, PromptVersion: profiletags.JevPromptVersion, SchemaVersion: "eval.v1"}
	state, deriveRef := f.save(t, 0, "prefer places and concise notes")
	f.request(t, "POST", "invalid", 401, 0)
	f.request(t, "POST", "synthetic-valid-token", 200, 0)
	if deriveCalls != 0 || tagCalls != 0 {
		t.Fatal("provider ran before debounce")
	}
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	current, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if current.Candidate == nil || current.Candidate.Source != "manual" || current.Candidate.ContentGeneration != manifest.GenerationID ||
		current.BootstrapGuidance != nil || current.Active != nil || deriveCalls != 1 || f.work(t, deriveRef).Status != "complete" {
		t.Fatalf("manual derivation not staged: %+v calls=%d work=%+v", current, deriveCalls, f.work(t, deriveRef))
	}
	current, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, current.Candidate.CandidateID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	current, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if current.Active == nil || current.Active.ContentGeneration != manifest.GenerationID || current.BootstrapGuidance != nil || current.Job.Status != profileJobReady || tagCalls != 2 {
		t.Fatalf("G1 not active: %+v calls=%d", current, tagCalls)
	}
	// A compile starts from this revision while its worker pins G2. Before G2
	// publishes, the owner updates Profile and manually activates r+1 on G1.
	updated, manualRef := f.save(t, current.Revision, "prefer places and concise notes with neighborhood context")
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	manual, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || manual.Candidate == nil || manual.Candidate.Source != "manual" || manual.Candidate.ContentGeneration != manifest.GenerationID || manual.Revision != updated.Revision || f.work(t, manualRef).Status != "complete" {
		t.Fatalf("r+1 derivation was not pinned to G1: state=%+v err=%v work=%+v", manual, err, f.work(t, manualRef))
	}
	manual, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, manual.Candidate.CandidateID, manual.Revision)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	current, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || current.Active == nil || current.Active.ContentGeneration != manifest.GenerationID || current.Revision != updated.Revision || current.Job.Status != profileJobReady || tagCalls != 2 {
		t.Fatalf("r+1 manual Profile did not activate on G1: state=%+v err=%v tag calls=%d", current, err, tagCalls)
	}
	first := *current.Active
	f.request(t, "POST", "synthetic-valid-token", 200, 0)
	if tagCalls != 2 {
		t.Fatal("duplicate dispatcher tick repeated provider calls")
	}

	// Publish G2 with unchanged source and changed canonical concept, then seed
	// the publisher's actual receipt/outbox schema in one emulator transaction.
	oldSnapshotPath, _ := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	snapshot, err := generation.DecodeSourceSnapshot(reader.files[oldSnapshotPath])
	if err != nil {
		t.Fatal(err)
	}
	manifest.PreviousGenerationID = manifest.GenerationID
	manifest.GenerationID = "generation-2"
	reader.files["cache/concepts.jsonl"] = []byte(strings.ReplaceAll(string(reader.files["cache/concepts.jsonl"]), "Alpha", "Changed Alpha"))
	for i := range manifest.Files {
		file := &manifest.Files[i]
		file.SHA256 = generation.Digest(reader.files[file.Path])
		file.Size = int64(len(reader.files[file.Path]))
		file.Generation += 30
	}
	snapshot.ContentGeneration = manifest.GenerationID
	snapshotBytes, snapshotDigest, err := generation.EncodeSourceSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceSnapshotDigest = snapshotDigest
	snapshotPath, _ := generation.SourceSnapshotPath(snapshotDigest)
	reader.files[snapshotPath] = snapshotBytes
	publish(51)
	// Real runtime activation remains G1 while the published current pointer is G2.
	assertRuntimeQuery(t, f, true, "Alpha", 200)
	g1Snapshot := prefix + oldSnapshotPath
	mu.Lock()
	retainedSnapshot := objects[g1Snapshot]
	delete(objects, g1Snapshot)
	mu.Unlock()
	assertRuntimeQuery(t, f, true, "", 500)
	mu.Lock()
	objects[g1Snapshot] = taggingGCSObject{[]byte("corrupt inventory"), retainedSnapshot.gen}
	mu.Unlock()
	assertRuntimeQuery(t, f, true, "", 500)
	mu.Lock()
	objects[g1Snapshot] = retainedSnapshot
	mu.Unlock()
	assertRuntimeQuery(t, f, true, "Alpha", 200)

	enqueueCompile := func(profile ProfileState, manifestGeneration int64, consumedBootstrap *profileartifacts.BootstrapGuidanceRef) profileruntime.CompileReceipt {
		t.Helper()
		receiptBytes, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		_, stateRef := f.repo.profileRefs(f.user, f.project)
		var consumed *profileartifacts.BootstrapGuidanceRef
		if consumedBootstrap != nil {
			copy := *consumedBootstrap
			consumed = &copy
		}
		receipt := profileruntime.CompileReceipt{UserID: f.user, ProjectID: f.project, ExecutionID: "synthetic-compile", ProfileRevision: profile.Revision, RequirementsDigest: profileRequirementsDigest(profile.Requirements), ContentGeneration: manifest.GenerationID, ManifestSHA256: generation.Digest(receiptBytes), CanonicalConceptsDigest: generation.Digest(reader.files["cache/concepts.jsonl"]), ManifestGeneration: manifestGeneration, ConsumedBootstrapGuidance: consumed, CreatedAt: f.clock}
		work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: profile.Revision, ID: manifest.GenerationID, Due: f.clock}
		if err := f.repo.client.RunTransaction(f.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			if err := tx.Set(stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID), receipt); err != nil {
				return err
			}
			return profileruntime.Enqueue(tx, f.repo.client, work)
		}); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	var final ProfileState
	if !t.Run("old-revision-receipt-reconciles-newer-confirmed-intent", func(t *testing.T) {
		oldBootstrapEvidence := &profileartifacts.BootstrapGuidanceRef{
			Revision: strings.Repeat("a", 64), ProfileRevision: state.Revision,
			InputDigest: profileRequirementsDigest(state.Requirements), ModelVersion: "pinned-model",
			PromptVersion: "pinned-bootstrap", SchemaVersion: profileartifacts.BootstrapGuidanceSchema,
		}
		compileReceipt := enqueueCompile(state, 51, oldBootstrapEvidence)
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		staged, err := f.repo.GetProfile(f.ctx, f.user, f.project)
		if err != nil {
			t.Fatal(err)
		}
		if staged.Candidate == nil || staged.Candidate.Source != "compile_auto" || staged.Candidate.ContentGeneration != manifest.GenerationID || staged.Candidate.BaseRevision != updated.Revision || staged.Candidate.RequirementsDigest != profileRequirementsDigest(updated.Requirements) || staged.Active == nil || *staged.Active != first || staged.Candidate.Guidance.Revision != first.GuidanceRevision || staged.DerivationStatus == nil || *staged.DerivationStatus != profileDerivationReady || staged.CompileRetryGeneration != "" || deriveCalls != 3 {
			t.Fatalf("compile candidate not staged preserving G1: %+v calls=%d", staged, deriveCalls)
		}
		storedReceiptSnapshot, err := stateRef.Collection("compile_receipts").Doc(manifest.GenerationID).Get(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var storedReceipt profileruntime.CompileReceipt
		if err := storedReceiptSnapshot.DataTo(&storedReceipt); err != nil {
			t.Fatal(err)
		}
		if storedReceipt.ProfileRevision != compileReceipt.ProfileRevision || storedReceipt.RequirementsDigest != compileReceipt.RequirementsDigest || storedReceipt.ConsumedBootstrapGuidance == nil || *storedReceipt.ConsumedBootstrapGuidance != *oldBootstrapEvidence {
			t.Fatalf("compile receipt or bootstrap evidence changed: got=%+v want=%+v", storedReceipt, compileReceipt)
		}
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		final, err = f.repo.GetProfile(f.ctx, f.user, f.project)
		if err != nil {
			t.Fatal(err)
		}
		if final.Active == nil || final.Active.ContentGeneration != manifest.GenerationID || final.Active.CandidateID != staged.Candidate.CandidateID || final.Active.GuidanceRevision != first.GuidanceRevision || final.Active.DictionaryRevision == first.DictionaryRevision || final.Job.Status != profileJobReady || final.Candidate == nil || final.Candidate.CandidateID != final.Active.CandidateID || tagCalls != 3 {
			t.Fatalf("G2 activation/reuse failed: %+v tag calls=%d", final, tagCalls)
		}
		f.request(t, "POST", "synthetic-valid-token", 200, 0)
		if deriveCalls != 3 || tagCalls != 3 {
			t.Fatalf("duplicate work calls derive=%d tags=%d", deriveCalls, tagCalls)
		}
	}) {
		return
	}

	// A current-revision compile exhausts before reconcile creation while the
	// activated compile-auto Candidate remains retained beside Active.
	var recoveredFinal ProfileState
	if !t.Run("retained-candidate-exhaustion-retries-through-activation", func(t *testing.T) {
		manifest.PreviousGenerationID = manifest.GenerationID
		manifest.GenerationID = "generation-3"
		reader.files["cache/concepts.jsonl"] = []byte(strings.ReplaceAll(string(reader.files["cache/concepts.jsonl"]), "Changed Alpha", "Retried Alpha"))
		for i := range manifest.Files {
			file := &manifest.Files[i]
			file.SHA256 = generation.Digest(reader.files[file.Path])
			file.Size = int64(len(reader.files[file.Path]))
			file.Generation += 30
		}
		snapshot.ContentGeneration = manifest.GenerationID
		snapshotBytes, snapshotDigest, err = generation.EncodeSourceSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		manifest.SourceSnapshotDigest = snapshotDigest
		snapshotPath, _ = generation.SourceSnapshotPath(snapshotDigest)
		reader.files[snapshotPath] = snapshotBytes
		publish(71)
		recoveryReceipt := enqueueCompile(final, 71, nil)
		if final.Candidate == nil || final.Active == nil || final.Candidate.CandidateID != final.Active.CandidateID {
			t.Fatalf("recovery compile should retain the activated candidate: candidate=%+v active=%+v", final.Candidate, final.Active)
		}
		recoveryWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: final.Revision, ID: manifest.GenerationID}
		recoveryWorkRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(recoveryWork))
		recoveryWork = f.work(t, recoveryWorkRef)
		recoveryWork.Attempts = profileruntime.MaxAttempts - 1
		recoveryWork.Due = f.clock.Add(-time.Second)
		recoveryWork.Status = "retry_wait"
		if _, err := recoveryWorkRef.Set(f.ctx, recoveryWork); err != nil {
			t.Fatal(err)
		}
		f.dispatcher.Handler.store = nil
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		exhausted := f.work(t, recoveryWorkRef)
		exhaustedProfile, err := f.repo.GetProfile(f.ctx, f.user, f.project)
		if err != nil {
			t.Fatal(err)
		}
		if exhausted.Pending || exhausted.Status != "exhausted" || exhaustedProfile.DerivationStatus == nil ||
			*exhaustedProfile.DerivationStatus != profileDerivationFailed || exhaustedProfile.DerivationErrorCode == nil ||
			*exhaustedProfile.DerivationErrorCode != "runtime_retry_exhausted" || exhaustedProfile.CompileRetryGeneration != manifest.GenerationID ||
			exhaustedProfile.Active == nil || *exhaustedProfile.Active != *final.Active || exhaustedProfile.Candidate == nil ||
			exhaustedProfile.Candidate.CandidateID != final.Active.CandidateID {
			t.Fatalf("active Profile compile exhaustion not visible/preserving: work=%+v Profile=%+v receipt=%+v", exhausted, exhaustedProfile, recoveryReceipt)
		}
		if _, err := profileCompileReconcileRef(stateRef, manifest.GenerationID).Get(f.ctx); status.Code(err) != codes.NotFound {
			t.Fatalf("compile exhaustion should happen before reconcile creation: err=%v", err)
		}
		retried, err := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, final.Revision)
		if err != nil || retried.DerivationStatus == nil || *retried.DerivationStatus != profileDerivationPending || retried.Active == nil || *retried.Active != *final.Active || retried.Candidate == nil || retried.Candidate.CandidateID != final.Active.CandidateID {
			t.Fatalf("compile retry did not preserve Active and its retained candidate: state=%+v err=%v", retried, err)
		}
		firstRetryWork := f.work(t, recoveryWorkRef)
		if !firstRetryWork.Pending || firstRetryWork.Status != "pending" || firstRetryWork.Attempts != 0 || firstRetryWork.ID != manifest.GenerationID {
			t.Fatalf("compile retry did not requeue exact receipt work: %+v", firstRetryWork)
		}
		retriedAgain, err := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, final.Revision)
		secondRetryWork := f.work(t, recoveryWorkRef)
		if err != nil || retriedAgain.ScheduledFor == nil || *retriedAgain.ScheduledFor != *retried.ScheduledFor ||
			secondRetryWork.Attempts != firstRetryWork.Attempts || !secondRetryWork.Due.Equal(firstRetryWork.Due) || secondRetryWork.Status != firstRetryWork.Status {
			t.Fatalf("repeated compile retry changed or duplicated work: first=%+v second=%+v state=%+v err=%v", firstRetryWork, secondRetryWork, retriedAgain, err)
		}
		f.dispatcher.Handler.store = client
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		recoveryStaged, err := f.repo.GetProfile(f.ctx, f.user, f.project)
		if err != nil {
			t.Fatal(err)
		}
		if recoveryStaged.Candidate == nil || recoveryStaged.Candidate.Source != "compile_auto" || recoveryStaged.Candidate.ContentGeneration != manifest.GenerationID || recoveryStaged.Candidate.BaseRevision != final.Revision || recoveryStaged.Active == nil || *recoveryStaged.Active != *final.Active || recoveryStaged.DerivationStatus == nil || *recoveryStaged.DerivationStatus != profileDerivationReady || recoveryStaged.CompileRetryGeneration != "" || f.work(t, recoveryWorkRef).Status != "complete" || deriveCalls != 4 {
			t.Fatalf("retried compile candidate not staged preserving G2: %+v calls=%d", recoveryStaged, deriveCalls)
		}
		reconcileSnapshot, err := profileCompileReconcileRef(stateRef, manifest.GenerationID).Get(f.ctx)
		if err != nil || reconcileSnapshot.Data()["status"] != "candidate_ready" || reconcileSnapshot.Data()["candidate_id"] != recoveryStaged.Candidate.CandidateID {
			t.Fatalf("retried compile did not complete reconciliation: reconcile=%v err=%v", reconcileSnapshot.Data(), err)
		}
		storedRecoveryReceiptSnapshot, err := stateRef.Collection("compile_receipts").Doc(manifest.GenerationID).Get(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var storedRecoveryReceipt profileruntime.CompileReceipt
		if err := storedRecoveryReceiptSnapshot.DataTo(&storedRecoveryReceipt); err != nil {
			t.Fatal(err)
		}
		if storedRecoveryReceipt != recoveryReceipt || storedRecoveryReceipt.ProfileRevision != final.Revision || storedRecoveryReceipt.RequirementsDigest != profileRequirementsDigest(final.Requirements) {
			t.Fatalf("current compile receipt changed during retry: got=%+v want=%+v", storedRecoveryReceipt, recoveryReceipt)
		}
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		recoveredFinal, err = f.repo.GetProfile(f.ctx, f.user, f.project)
		if err != nil {
			t.Fatal(err)
		}
		if recoveredFinal.Active == nil || recoveredFinal.Active.ContentGeneration != manifest.GenerationID || recoveredFinal.Active.CandidateID != recoveryStaged.Candidate.CandidateID || recoveredFinal.Active.GuidanceRevision != final.Active.GuidanceRevision || recoveredFinal.Active.DictionaryRevision == final.Active.DictionaryRevision || recoveredFinal.Job.Status != profileJobReady || recoveredFinal.Candidate == nil || recoveredFinal.Candidate.CandidateID != recoveredFinal.Active.CandidateID || tagCalls != 4 {
			t.Fatalf("retried G3 activation/reuse failed: %+v tag calls=%d", recoveredFinal, tagCalls)
		}
		f.request(t, "POST", "synthetic-valid-token", 200, 0)
		if deriveCalls != 4 || tagCalls != 4 {
			t.Fatalf("duplicate work calls derive=%d tags=%d", deriveCalls, tagCalls)
		}
	}) {
		return
	}
	// Clearing an active profile publishes explicit empty coverage after the
	// same debounce and confirmation, without calling either provider.
	neutral, err := f.repo.SaveProfile(f.ctx, f.user, f.project, recoveredFinal.Revision, []ProfileRequirement{})
	if err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	neutral, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if neutral.Candidate == nil {
		t.Fatalf("neutral candidate absent: %+v", neutral)
	}
	neutral, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, neutral.Candidate.CandidateID, neutral.Revision)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	neutral, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if neutral.Active == nil || neutral.Active.GuidanceRevision == first.GuidanceRevision || neutral.Job.Status != profileJobReady || deriveCalls != 4 || tagCalls != 4 {
		t.Fatalf("neutral activation failed: %+v derive=%d tags=%d", neutral, deriveCalls, tagCalls)
	}
	mu.Lock()
	setBytes := append([]byte(nil), objects[prefix+profiletags.SetPath(neutral.Active.TagSetRevision)].data...)
	guidanceBytes := append([]byte(nil), objects[prefix+profileartifacts.GuidanceObjectPath(neutral.Active.GuidanceRevision)].data...)
	mu.Unlock()
	var emptySet profiletags.TagSet
	var emptyGuidance profileartifacts.GenerationGuidanceEnvelope
	if err := json.Unmarshal(setBytes, &emptySet); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(guidanceBytes, &emptyGuidance); err != nil {
		t.Fatal(err)
	}
	if emptySet.ExpectedCount != 0 || len(emptySet.Rows) != 0 || emptyGuidance.CompileGuidance != "" {
		t.Fatalf("neutral artifacts not empty: %+v %+v", emptySet, emptyGuidance)
	}

	manifest.PreviousGenerationID = manifest.GenerationID
	manifest.GenerationID = "generation-4"
	snapshot.ContentGeneration = manifest.GenerationID
	snapshotBytes, snapshotDigest, err = generation.EncodeSourceSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceSnapshotDigest = snapshotDigest
	snapshotPath, _ = generation.SourceSnapshotPath(snapshotDigest)
	reader.files[snapshotPath] = snapshotBytes
	publish(91)
	enqueueCompile(neutral, 91, nil)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	afterEmptyCompile, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if afterEmptyCompile.Active == nil || afterEmptyCompile.Active.ContentGeneration != manifest.GenerationID || afterEmptyCompile.Active.GuidanceRevision != neutral.Active.GuidanceRevision || afterEmptyCompile.Job.Status != profileJobReady || deriveCalls != 4 || tagCalls != 4 {
		t.Fatalf("empty-active compile failed: %+v derive=%d tags=%d", afterEmptyCompile, deriveCalls, tagCalls)
	}

}
