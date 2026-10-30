package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
)

func TestLocalPublicationJournalCarriesSameDigestSummarySource(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "users", "owner", "projects", "project")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	concepts := localContentContextFixtureConcepts(t, "alpha")
	localContentContextFixtureWorkspace(t, workspace, concepts)
	cfg := workerConfig{UserID: "owner", ProjectID: "project", ExecutionID: "execution-1"}
	if err := prepareLocalContentContextDescriptor(workspace, vault, cfg); err != nil {
		t.Fatal(err)
	}
	first, err := readLocalContentContextDescriptor(workspace, cfg.UserID, cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncWorkspaceOutputs(workspace, vault, cfg.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if err := acquireAndReleaseTestVaultLease(t, vault, cfg.ExecutionID); err != nil {
		t.Fatal(err)
	}

	secondWorkspace := t.TempDir()
	localContentContextFixtureWorkspace(t, secondWorkspace, concepts)
	cfg.ExecutionID = "execution-2"
	if err := prepareLocalContentContextDescriptor(secondWorkspace, vault, cfg); err != nil {
		t.Fatal(err)
	}
	second, err := readLocalContentContextDescriptor(secondWorkspace, cfg.UserID, cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == second.Generation || first.TransactionID == second.TransactionID || first.ExecutionID == second.ExecutionID {
		t.Fatalf("local publication identity did not advance: first=%+v second=%+v", first, second)
	}
	if first.SummarySourceGeneration != second.SummarySourceGeneration || first.SummarySourceIdentity != second.SummarySourceIdentity ||
		string(first.SummaryInput) != string(second.SummaryInput) || first.SummaryMetadata != second.SummaryMetadata {
		t.Fatalf("same-digest publication changed captured summary source: first=%+v second=%+v", first, second)
	}

	changedWorkspace := t.TempDir()
	localContentContextFixtureWorkspace(t, changedWorkspace, localContentContextFixtureConcepts(t, "beta"))
	cfg.ExecutionID = "execution-3"
	if err := prepareLocalContentContextDescriptor(changedWorkspace, vault, cfg); err != nil {
		t.Fatal(err)
	}
	changed, err := readLocalContentContextDescriptor(changedWorkspace, cfg.UserID, cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConceptsDigest == second.ConceptsDigest || changed.SummarySourceGeneration != changed.Generation ||
		changed.SummarySourceIdentity == second.SummarySourceIdentity || string(changed.SummaryInput) == string(second.SummaryInput) {
		t.Fatalf("changed digest did not create a new captured summary source: old=%+v new=%+v", second, changed)
	}
}

func TestLocalPublicationJournalIsVisibleThroughLocalReader(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "users", "owner", "projects", "project")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	localContentContextFixtureWorkspace(t, workspace, localContentContextFixtureConcepts(t, "alpha"))
	cfg := workerConfig{UserID: "owner", ProjectID: "project", ExecutionID: "execution-1"}
	if err := prepareLocalContentContextDescriptor(workspace, vault, cfg); err != nil {
		t.Fatal(err)
	}
	if err := syncWorkspaceOutputs(workspace, vault, cfg.ExecutionID); err != nil {
		t.Fatal(err)
	}

	reader := localfs.New(root).WithScope(cfg.UserID, cfg.ProjectID)
	publication, err := reader.ContentContextPublication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := reader.QueryGenerationIdentity(context.Background())
	if err != nil || identity.ProjectID != cfg.ProjectID || identity.GenerationID != "legacy" || identity.ConceptsDigest != "" || reader.ViewToken() != "legacy" {
		t.Fatalf("local Query identity=%+v token=%q err=%v", identity, reader.ViewToken(), err)
	}
	if publication.Generation != descriptor.Generation || publication.ConceptsDigest != descriptor.ConceptsDigest {
		t.Fatalf("local content-context publication=%+v descriptor=%+v", publication, descriptor)
	}
}

func TestLocalPublicationAuthorityCarriesSameDigestAndFencesLateAttempt(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cloudProject := fmt.Sprintf("local-content-context-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	client, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	cfg := workerConfig{UserID: "local-owner", ProjectID: fmt.Sprintf("local-project-%d", time.Now().UnixNano()), ExecutionID: "local-summary-test"}
	stateRef := scopedfirestore.Collection(client, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() { _, _ = stateRef.Delete(context.Background()) }()
	vault := t.TempDir()
	commitLocalContentContextFixture(t, vault, cfg, "alpha")
	authority := newLocalContentContextAuthority(cfg, vault)
	repository := contentcontext.NewFirestoreRepository(client)
	if _, err := repository.ReconcileCurrent(ctx, cfg.UserID, cfg.ProjectID, authority); err != nil {
		t.Fatalf("reconcile initial local publication: %v", err)
	}
	firstClaim, err := repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
	if err != nil || firstClaim == nil {
		t.Fatalf("claim initial local publication: claim=%+v err=%v", firstClaim, err)
	}

	second := commitLocalContentContextFixture(t, vault, cfg, "alpha")
	if second.SummarySourceGeneration != firstClaim.SourceGeneration || second.SummarySourceIdentity != firstClaim.SourceManifestIdentity {
		t.Fatalf("same-digest local publication changed source identity: claim=%+v descriptor=%+v", firstClaim, second)
	}
	completion := contentcontext.SummaryCompletion{
		Text: "summary from first local publication", PromptRevision: contentcontext.SummaryPromptRevision,
		ProfileSource: "personal", ProfileID: "fast-profile", ProfileRevision: 1, Role: contentcontext.SummaryRole,
		RequestedModel: "fast-model", ReturnedModel: "fast-model-v2",
		InputDigest: strings.Repeat("b", 64), SelectionVersion: contentcontext.SummarySelectionVersion,
	}
	if saved, err := repository.Complete(ctx, cfg.UserID, cfg.ProjectID, authority, *firstClaim, completion); err != nil || !saved {
		t.Fatalf("complete carried local work: saved=%t err=%v", saved, err)
	}
	state, err := repository.Read(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil || state.TargetGeneration != second.Generation || state.LastGood == nil || state.LastGood.SourceGeneration != firstClaim.SourceGeneration {
		t.Fatalf("same-digest local state=%+v err=%v", state, err)
	}

	commitLocalContentContextFixture(t, vault, cfg, "beta")
	thirdClaim, err := repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
	if err != nil || thirdClaim == nil {
		t.Fatalf("claim changed local digest: claim=%+v err=%v", thirdClaim, err)
	}
	commitLocalContentContextFixture(t, vault, cfg, "gamma")
	if saved, err := repository.Complete(ctx, cfg.UserID, cfg.ProjectID, authority, *thirdClaim, completion); err != nil || saved {
		t.Fatalf("late changed-digest local attempt saved=%t err=%v", saved, err)
	}
	state, err = repository.Read(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil || state.Status != contentcontext.StatusPending || state.TargetConceptsSHA != localConceptsDigest(localContentContextFixtureConcepts(t, "gamma")) ||
		state.LastGood == nil || state.LastGood.Text != "summary from first local publication" {
		t.Fatalf("late local attempt changed current/last-good state: state=%+v err=%v", state, err)
	}
}

func TestLocalFirstClaimAfterSameDigestCarryForwardUsesCapturedSource(t *testing.T) {
	ctx := contentContextEmulatorContext(t, 30*time.Second)
	cloudProject := fmt.Sprintf("local-content-context-first-claim-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	client, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	cfg := workerConfig{UserID: "local-owner", ProjectID: fmt.Sprintf("local-first-claim-%d", time.Now().UnixNano()), ExecutionID: "local-summary-g1"}
	stateRef := scopedfirestore.Collection(client, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() { _, _ = stateRef.Delete(context.Background()) }()
	vault := t.TempDir()
	first := commitLocalContentContextFixture(t, vault, cfg, "alpha")
	cfg.ExecutionID = "local-summary-g2"
	second := commitLocalContentContextFixture(t, vault, cfg, "alpha")
	if first.Generation == second.Generation || first.SummarySourceGeneration != second.SummarySourceGeneration ||
		first.SummarySourceIdentity != second.SummarySourceIdentity {
		t.Fatalf("same-digest fixture did not retain G1 source: G1=%+v G2=%+v", first, second)
	}

	authority := newLocalContentContextAuthority(cfg, vault)
	repository := contentcontext.NewFirestoreRepository(client)
	claim, err := repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
	if err != nil || claim == nil || claim.SourceGeneration != first.Generation || claim.SourceManifestIdentity != first.SummarySourceIdentity {
		t.Fatalf("first claim after G2=%+v err=%v, want captured G1 source", claim, err)
	}
	input, metadata, err := loadLocalContentContextSummaryInput(ctx, cfg, vault, authority, *claim)
	if err != nil || len(input) == 0 || metadata.SourceGeneration != first.Generation {
		t.Fatalf("load captured G1 input after G2: bytes=%d metadata=%+v err=%v", len(input), metadata, err)
	}
	completion := contentcontext.SummaryCompletion{
		Text: "summary from the first committed local generation", PromptRevision: contentcontext.SummaryPromptRevision,
		ProfileSource: "bootstrap", Role: contentcontext.SummaryRole, RequestedModel: "fast-model", ReturnedModel: "fast-model-v2",
		InputDigest: metadata.InputDigest, SelectionVersion: contentcontext.SummarySelectionVersion,
	}
	if saved, err := repository.Complete(ctx, cfg.UserID, cfg.ProjectID, authority, *claim, completion); err != nil || !saved {
		t.Fatalf("complete G1 work against G2 publication: saved=%t err=%v", saved, err)
	}
	state, err := repository.Read(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil || state.Status != contentcontext.StatusReady || state.TargetGeneration != second.Generation ||
		state.LatestPublication == nil || state.LatestPublication.Generation != second.Generation ||
		state.LastGood == nil || state.LastGood.SourceGeneration != first.Generation || state.SummaryWork == nil ||
		state.SummaryWork.SourceGeneration != first.Generation || state.SummaryWork.RetryCount != 0 {
		t.Fatalf("GET state did not preserve current G2 and captured G1 identities: state=%+v err=%v", state, err)
	}
}

func TestLocalWorkerRecoversCommittedSummaryAndContinuesOrdinaryPipeline(t *testing.T) {
	for _, crashPoint := range []string{"before_first_state_registration", "after_claim_before_complete_expired", "after_claim_before_complete_active"} {
		t.Run(crashPoint, func(t *testing.T) {
			ctx := contentContextEmulatorContext(t, 30*time.Second)
			cloudProject := fmt.Sprintf("local-content-context-recovery-%d", time.Now().UnixNano())
			t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
			t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
			client, err := newProfileFirestoreClient(ctx, cloudProject)
			if err != nil {
				t.Fatalf("create Firestore emulator client: %v", err)
			}
			defer client.Close()

			cfg := workerConfig{
				UserID: "local-recovery-owner", ProjectID: fmt.Sprintf("local-recovery-%d", time.Now().UnixNano()),
				ExecutionID: "local-recovery-g1", Postprocess: true, WorkspaceDir: t.TempDir(),
				PipelineProfileSource: "bootstrap", BootstrapLLMSource: true, APIKey: "fixture-platform-key",
			}
			stateRef := scopedfirestore.Collection(client, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
			defer func() { _, _ = stateRef.Delete(context.Background()) }()
			vault := t.TempDir()
			published := commitLocalContentContextFixture(t, vault, cfg, "alpha")
			authority := newLocalContentContextAuthority(cfg, vault)
			repository := contentcontext.NewFirestoreRepository(client)
			var interruptedClaim *contentcontext.SummaryClaim
			if crashPoint != "before_first_state_registration" {
				interruptedClaim, err = repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
				if err != nil || interruptedClaim == nil {
					t.Fatalf("claim before simulated crash: claim=%+v err=%v", interruptedClaim, err)
				}
				if crashPoint == "after_claim_before_complete_expired" {
					if _, err := stateRef.Update(ctx, []firestore.Update{{Path: "summary_work.lease", Value: time.Now().UTC().Add(-time.Minute)}}); err != nil {
						t.Fatalf("expire interrupted claim lease: %v", err)
					}
				}
			}

			oldHooks := localWorkspaceBatchHooks
			t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
			oldBootstrapFactory := newBootstrapContentContextSummaryClient
			t.Cleanup(func() { newBootstrapContentContextSummaryClient = oldBootstrapFactory })
			summaryClient := &contentContextTestSummaryClient{result: llm.CompletionResult{
				Text: `{"summary":"Recovered published project context."}`,
				Metadata: llm.CompletionMetadata{
					Provider: "test-provider", Protocol: string(llm.ProtocolOpenAIChatCompletions),
					Role: string(llm.ProfileRoleFast), RequestedModel: "test-fast", ReturnedModel: "test-fast-v2",
				},
			}}
			newBootstrapContentContextSummaryClient = func(key string) (contentContextSummaryClient, llm.ModelIdentity, error) {
				if key != cfg.APIKey {
					t.Fatalf("recovery client received unexpected platform credential")
				}
				return summaryClient, llm.ModelIdentity{Provider: "test-provider", Model: "test-fast"}, nil
			}

			pipelineCalls, publishCalls := 0, 0
			raw := []byte("# new source\nNew pipeline input.\n")
			rawSHA := localConceptsDigest(raw)
			annotationSHA := localConceptsDigest(nil)
			snapshot := sourceSnapshot{
				SourceID: "source-new", RawPath: "raw/new.md", RawBytes: raw, RawSHA256: rawSHA,
				AnnotationSHA: annotationSHA, Fingerprint: sourcestatus.Fingerprint(rawSHA, annotationSHA), Dirty: true,
			}
			newConcepts := localContentContextFixtureConcepts(t, "beta")
			var stateBeforePipeline contentcontext.StateDocument
			hooks := oldHooks
			hooks.snapshotSources = func(string) ([]sourceSnapshot, error) {
				stateBeforePipeline, err = repository.Read(ctx, cfg.UserID, cfg.ProjectID)
				if err != nil {
					return nil, err
				}
				pipelineCalls++
				return []sourceSnapshot{snapshot}, nil
			}
			hooks.snapshotConcepts = func(string, ...[]sourceSnapshot) ([]conceptSnapshot, error) { return nil, nil }
			hooks.runWorkerBatchAtVault = func(_ context.Context, _ workerConfig, _ [][]string, workspace string) error {
				materialized, err := os.ReadFile(filepath.Join(workspace, snapshot.RawPath))
				if err != nil || string(materialized) != string(raw) {
						return fmt.Errorf("new source was not materialized: bytes=%q err=%v", materialized, err)
				}
				return os.WriteFile(filepath.Join(workspace, "cache", "concepts.jsonl"), newConcepts, 0o600)
			}
			hooks.reconcileWorkspaceSources = func(string, []sourceSnapshot) error { return nil }
			hooks.reconcileWorkspaceConcepts = func(string, []conceptSnapshot, ...[]sourceSnapshot) error { return nil }
			hooks.syncWorkspaceOutputs = func(workspace, vault, executionID string) error {
				publishCalls++
				return syncWorkspaceOutputs(workspace, vault, executionID)
			}
			localWorkspaceBatchHooks = hooks
			cfg.ExecutionID = "local-recovery-resume"
			if err := runWorkerBatchWorkspace(ctx, cfg, [][]string{{"run"}}, vault); err != nil {
				t.Fatalf("run recovered local workspace pipeline: %v", err)
			}
			wantRecoveryStatus := contentcontext.StatusReady
			wantSummaryCallsBeforePipeline := 1
			if crashPoint == "after_claim_before_complete_active" {
				wantRecoveryStatus = contentcontext.StatusPending
				wantSummaryCallsBeforePipeline = 0
			}
			if pipelineCalls != 1 || publishCalls != 1 || summaryClient.calls != wantSummaryCallsBeforePipeline+1 {
				t.Fatalf("worker calls summary=%d compile=%d publish=%d", summaryClient.calls, pipelineCalls, publishCalls)
			}
			if stateBeforePipeline.Status != wantRecoveryStatus || stateBeforePipeline.TargetGeneration != published.Generation ||
				stateBeforePipeline.LatestPublication == nil || stateBeforePipeline.LatestPublication.ManifestIdentity != published.Publication().ManifestIdentity {
				t.Fatalf("state before new pipeline input=%+v, want status=%s and committed G1=%s", stateBeforePipeline, wantRecoveryStatus, published.Generation)
			}
			wantRetryCount := 0
			if crashPoint == "after_claim_before_complete_expired" {
				wantRetryCount = 1
			}
			if stateBeforePipeline.SummaryWork == nil || stateBeforePipeline.SummaryWork.RetryCount != wantRetryCount {
				t.Fatalf("recovery changed claim budget: work=%+v want retry_count=%d", stateBeforePipeline.SummaryWork, wantRetryCount)
			}
			state, err := repository.Read(ctx, cfg.UserID, cfg.ProjectID)
			descriptor, descriptorErr := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
			status, statusErr := readSourceStatus(vault)
			receipt := status.Sources[snapshot.SourceID]
			if err != nil || descriptorErr != nil || statusErr != nil || state.Status != contentcontext.StatusReady ||
				state.TargetGeneration != descriptor.Generation || descriptor.Generation == published.Generation ||
				descriptor.ConceptsDigest != localConceptsDigest(newConcepts) ||
				state.LatestPublication == nil || state.LatestPublication.Generation != descriptor.Generation ||
				state.LastGood == nil || state.LastGood.SourceGeneration != descriptor.SummarySourceGeneration || state.SummaryWork == nil ||
				state.SummaryWork.SourceGeneration != descriptor.SummarySourceGeneration ||
				!sourcestatus.ValidReceipt(receipt, snapshot.RawPath) || receipt.LastIngestedRawSHA256 != snapshot.RawSHA256 {
				t.Fatalf("ordinary pipeline result state=%+v descriptor=%+v receipt=%+v status=%+v err=%v/%v/%v", state, descriptor, receipt, status, err, descriptorErr, statusErr)
			}
			if interruptedClaim != nil {
				completion := contentcontext.SummaryCompletion{
					Text: "stale result", PromptRevision: contentcontext.SummaryPromptRevision,
					ProfileSource: "bootstrap", Role: contentcontext.SummaryRole, RequestedModel: "test-fast", ReturnedModel: "test-fast-v2",
					InputDigest: strings.Repeat("a", 64), SelectionVersion: contentcontext.SummarySelectionVersion,
				}
				if saved, err := repository.Complete(ctx, cfg.UserID, cfg.ProjectID, authority, *interruptedClaim, completion); !errors.Is(err, contentcontext.ErrAttemptRejected) || saved {
					t.Fatalf("interrupted claim committed after new publication: saved=%t err=%v", saved, err)
				}
			}
		})
	}
}

func TestLocalWorkspaceWorkerPublishesAndRecordsEmptyContextWithoutInference(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cloudProject := fmt.Sprintf("local-empty-context-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	client, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	oldSummaryClient := newContentContextSummaryClient
	t.Cleanup(func() { newContentContextSummaryClient = oldSummaryClient })
	newContentContextSummaryClient = func(pipelineLLMProfile) (contentContextSummaryClient, error) {
		t.Fatal("empty local concepts attempted model inference")
		return nil, errors.New("unreachable")
	}

	cfg := workerConfig{
		UserID: "local-empty-owner", ProjectID: fmt.Sprintf("local-empty-%d", time.Now().UnixNano()),
		ExecutionID: "local-empty-worker", Postprocess: true, WorkspaceDir: t.TempDir(),
	}
	vault := t.TempDir()
	localContentContextFixtureWorkspace(t, vault, nil)
	stateRef := scopedfirestore.Collection(client, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() { _, _ = stateRef.Delete(context.Background()) }()

	hooks := oldHooks
	hooks.snapshotSources = func(string) ([]sourceSnapshot, error) { return nil, nil }
	hooks.snapshotConcepts = func(string, ...[]sourceSnapshot) ([]conceptSnapshot, error) { return nil, nil }
	hooks.runWorkerBatchAtVault = func(context.Context, workerConfig, [][]string, string) error { return nil }
	hooks.reconcileWorkspaceSources = func(string, []sourceSnapshot) error { return nil }
	hooks.reconcileWorkspaceConcepts = func(string, []conceptSnapshot, ...[]sourceSnapshot) error { return nil }
	hooks.recordSuccess = func(string, []sourceSnapshot, time.Time) error { return nil }
	localWorkspaceBatchHooks = hooks

	if err := runWorkerBatchWorkspace(ctx, cfg, [][]string{{"run"}}, vault); err != nil {
		t.Fatalf("run local workspace worker: %v", err)
	}
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	if err != nil || descriptor.InputErrorClass != "empty_concepts" {
		t.Fatalf("published local descriptor=%+v err=%v", descriptor, err)
	}
	snapshot, err := stateRef.Get(ctx)
	if err != nil {
		t.Fatalf("read local worker state: %v", err)
	}
	var state contentcontext.StateDocument
	if err := snapshot.DataTo(&state); err != nil {
		t.Fatalf("decode local worker state: %v", err)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("validate local worker state: %v", err)
	}
	if state.Status != contentcontext.StatusMissing || state.TargetGeneration != descriptor.Generation ||
		state.TargetConceptsSHA != descriptor.ConceptsDigest || state.LastGood != nil {
		t.Fatalf("empty local worker state=%+v descriptor=%+v", state, descriptor)
	}
}

func commitLocalContentContextFixture(t *testing.T, vault string, cfg workerConfig, slug string) contentcontext.LocalPublicationDescriptor {
	t.Helper()
	lease, err := acquireVaultLease(vault, cfg.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	}()
	workspace := t.TempDir()
	localContentContextFixtureWorkspace(t, workspace, localContentContextFixtureConcepts(t, slug))
	if err := prepareLocalContentContextDescriptor(workspace, vault, cfg); err != nil {
		t.Fatal(err)
	}
	if err := syncWorkspaceOutputs(workspace, vault, cfg.ExecutionID); err != nil {
		t.Fatal(err)
	}
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestLocalPrecommitFailureDoesNotStartContentContext(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	oldRename := publishRename
	t.Cleanup(func() { publishRename = oldRename })
	oldFirestore := newContentContextFirestoreClient
	t.Cleanup(func() { newContentContextFirestoreClient = oldFirestore })
	oldSummaryClient := newContentContextSummaryClient
	t.Cleanup(func() { newContentContextSummaryClient = oldSummaryClient })

	vault := workspaceVault(t, "raw")
	workspaceDir := t.TempDir()
	localContentContextFixtureWorkspace(t, vault, localContentContextFixtureConcepts(t, "alpha"))
	hooks := oldHooks
	hooks.recoverInterruptedPublish = func(string) error { return nil }
	hooks.snapshotSources = func(string) ([]sourceSnapshot, error) { return nil, nil }
	hooks.snapshotConcepts = func(string, ...[]sourceSnapshot) ([]conceptSnapshot, error) { return nil, nil }
	hooks.createWorkspace = createWorkspace
	hooks.materializeSnapshots = func(string, []sourceSnapshot) error { return nil }
	hooks.runWorkerBatchAtVault = func(context.Context, workerConfig, [][]string, string) error { return nil }
	hooks.reconcileWorkspaceSources = func(string, []sourceSnapshot) error { return nil }
	hooks.reconcileWorkspaceConcepts = func(string, []conceptSnapshot, ...[]sourceSnapshot) error { return nil }
	hooks.syncWorkspaceOutputs = syncWorkspaceOutputs
	hooks.recordSuccess = func(string, []sourceSnapshot, time.Time) error { return nil }
	localWorkspaceBatchHooks = hooks

	publishRename = func(root *os.Root, oldName, newName string) error {
		if newName == contentcontext.LocalPublicationDescriptorPath {
			return errors.New("injected precommit descriptor rename failure")
		}
		return oldRename(root, oldName, newName)
	}
	firestoreCalls, providerCalls := 0, 0
	newContentContextFirestoreClient = func(context.Context, string) (*firestore.Client, error) {
		firestoreCalls++
		return nil, errors.New("should not reach Firestore before commit")
	}
	newContentContextSummaryClient = func(pipelineLLMProfile) (contentContextSummaryClient, error) {
		providerCalls++
		return nil, errors.New("should not call provider before commit")
	}

	err := runWorkerBatchWorkspace(context.Background(), workerConfig{
		UserID: "owner", ProjectID: "project", ExecutionID: "precommit-failure", Postprocess: true, WorkspaceDir: workspaceDir,
	}, [][]string{{"run"}}, vault)
	if err == nil || !strings.Contains(err.Error(), "precommit descriptor rename failure") {
		t.Fatalf("runWorkerBatchWorkspace error=%v, want injected precommit failure", err)
	}
	if firestoreCalls != 0 || providerCalls != 0 {
		t.Fatalf("precommit failure started Firestore=%d provider=%d times", firestoreCalls, providerCalls)
	}
	if _, err := os.Stat(filepath.Join(vault, contentcontext.LocalPublicationDescriptorPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precommit failure published descriptor: %v", err)
	}
}

func acquireAndReleaseTestVaultLease(t *testing.T, vault, executionID string) error {
	t.Helper()
	lease, err := acquireVaultLease(vault, executionID)
	if err != nil {
		return err
	}
	return lease.Release()
}

func localContentContextFixtureConcepts(t *testing.T, slug string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"slug": slug, "title": strings.ToUpper(slug), "body": "A local published concept.",
		"frontmatter": map[string]any{"id": "id-" + slug}, "sources": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func localContentContextFixtureWorkspace(t *testing.T, root string, concepts []byte) {
	t.Helper()
	for path, data := range map[string][]byte{
		"cache/concepts.jsonl":         concepts,
		"cache/id_map.json":            []byte(`{"source_meta":{}}`),
		"cache/dormant_concepts.jsonl": []byte{},
		"cache/raw_status.json":        []byte(`{"version":1,"sources":{}}`),
		"cache/suggested_queries.json": []byte(`{"version":1,"queries":[]}`),
		"synto.toml":                   []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\n"),
		".synto/INDEX.json":            []byte(syntoIndexFixture("22af645d1859", "01JAZ5N7Y3K8M2Q4R6T9VWXAC8", "new", false)),
	} {
		writeLocalContentContextFixture(t, root, path, data)
	}
	writeValidSQLiteState(t, filepath.Join(root, ".synto", "state.db"))
}

func writeLocalContentContextFixture(t *testing.T, root, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
