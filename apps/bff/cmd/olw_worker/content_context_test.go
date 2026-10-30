package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/llm"
)

type contentContextTestSummaryClient struct {
	profile pipelineLLMProfile
	result  llm.CompletionResult
	calls   int
}

func (c *contentContextTestSummaryClient) Complete(_ context.Context, system, input string) (llm.CompletionResult, error) {
	c.calls++
	if system != contentcontext.SummarySystemPrompt || input == "" {
		return llm.CompletionResult{}, errors.New("unexpected summary request")
	}
	return c.result, nil
}

func (*contentContextTestSummaryClient) Close() {}

func TestContentContextManifestIdentityBindsGenerationObjectAndDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	want := "generation-1:17:" + digest
	if got := contentContextManifestIdentity("generation-1", 17, digest); got != want {
		t.Fatalf("manifest identity=%q, want %q", got, want)
	}
	for _, invalid := range []struct {
		generation string
		objectGen  int64
		digest     string
	}{
		{generation: "", objectGen: 17, digest: digest},
		{generation: "generation-1", objectGen: 0, digest: digest},
		{generation: "generation-1", objectGen: 17, digest: "not-a-digest"},
	} {
		if got := contentContextManifestIdentity(invalid.generation, invalid.objectGen, invalid.digest); got != "" {
			t.Fatalf("invalid manifest identity input %+v produced %q", invalid, got)
		}
	}
}

func TestCloudContentContextAuthorityReadsOnlyFencedCurrentManifest(t *testing.T) {
	objects := newMemoryObjects()
	cfg := workerConfig{
		Bucket: "local-test", UserID: "owner", ProjectID: "project", ExecutionID: "olw-pipeline-content-context-test",
	}
	prefix := workerProjectObjectPrefix(cfg)
	firstManifestGeneration, firstDigest := seedContentContextManifest(t, objects, prefix, "generation-one", "published concepts")
	authority := newCloudContentContextAuthority(cfg, objects)
	ctx := context.Background()

	var first contentcontext.Publication
	if err := authority.WithProjectFence(ctx, cfg.UserID, cfg.ProjectID, func(fencedCtx context.Context) error {
		var err error
		first, err = authority.CurrentPublication(fencedCtx, cfg.UserID, cfg.ProjectID)
		return err
	}); err != nil {
		t.Fatalf("read first fenced publication: %v", err)
	}
	if first.Generation != "generation-one" || first.ConceptsDigest != firstDigest ||
		first.ManifestIdentity != contentContextManifestIdentity("generation-one", firstManifestGeneration, generation.Digest(mustReadContentContextManifest(t, objects, prefix))) {
		t.Fatalf("first current publication=%+v", first)
	}
	if _, err := authority.CurrentPublication(ctx, cfg.UserID, cfg.ProjectID); err == nil {
		t.Fatal("unfenced current manifest read was accepted")
	}

	secondManifestGeneration, secondDigest := seedContentContextManifest(t, objects, prefix, "generation-two", "published concepts")
	var second contentcontext.Publication
	if err := authority.WithProjectFence(ctx, cfg.UserID, cfg.ProjectID, func(fencedCtx context.Context) error {
		var err error
		second, err = authority.CurrentPublication(fencedCtx, cfg.UserID, cfg.ProjectID)
		return err
	}); err != nil {
		t.Fatalf("read carried-forward publication: %v", err)
	}
	if second.Generation != "generation-two" || second.ConceptsDigest != secondDigest || second.ConceptsDigest != first.ConceptsDigest ||
		second.ManifestIdentity == first.ManifestIdentity ||
		second.ManifestIdentity != contentContextManifestIdentity("generation-two", secondManifestGeneration, generation.Digest(mustReadContentContextManifest(t, objects, prefix))) {
		t.Fatalf("same-digest current publication=%+v previous=%+v", second, first)
	}
	leaseName := prefix + generation.LeasePath
	if _, _, err := objects.Read(ctx, leaseName, 0, 64<<10); err == nil {
		t.Fatal("short publisher fence left its lease behind")
	}
}

func seedContentContextManifest(t *testing.T, objects *memoryObjects, prefix, generationID, body string) (int64, string) {
	t.Helper()
	concepts := []byte(`{"slug":"project-context","title":"Project Context","body":"` + body + `"}` + "\n")
	file, err := generation.NewFile("cache/concepts.jsonl", concepts, 1)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generation.Manifest{
		Version: generation.Version, GenerationID: generationID,
		CreatedAt:        time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		InputFingerprint: "content-context-test", Files: []generation.File{file},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := objects.Write(context.Background(), prefix+generation.ManifestPath, data, nil, objectConditions{})
	if err != nil {
		t.Fatal(err)
	}
	return attrs.Generation, file.SHA256
}

func mustReadContentContextManifest(t *testing.T, objects *memoryObjects, prefix string) []byte {
	t.Helper()
	data, _, err := objects.Read(context.Background(), prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPinnedSummaryInputRejectsMissingClaimIdentityBeforeStorage(t *testing.T) {
	if _, _, err := pinnedSummaryInput(context.Background(), workerConfig{}, contentcontext.SummaryClaim{}); err == nil {
		t.Fatal("unbound summary input request was accepted")
	}
}

func TestContentContextManifestIdentityDoesNotAcceptUntrustedFormatting(t *testing.T) {
	digest := strings.Repeat("A", 64)
	if got := contentContextManifestIdentity("generation-1", 17, digest); got != "" {
		t.Fatalf("uppercase manifest digest produced identity %q", got)
	}
}

func TestContentContextArchiveMatchesPublicationManifestWithoutConfusingObjectGenerations(t *testing.T) {
	digest := strings.Repeat("a", 64)
	identity := contentContextManifestIdentity("generation-1", 17, digest)
	if !contentContextArchiveMatches(identity, "generation-1", digest) {
		t.Fatal("archived copy of the pinned manifest did not match the publication identity")
	}
	for _, invalid := range []struct {
		identity   string
		generation string
		digest     string
	}{
		{identity: identity, generation: "generation-2", digest: digest},
		{identity: identity, generation: "generation-1", digest: strings.Repeat("b", 64)},
		{identity: "generation-1:0:" + digest, generation: "generation-1", digest: digest},
	} {
		if contentContextArchiveMatches(invalid.identity, invalid.generation, invalid.digest) {
			t.Fatalf("invalid archive identity matched: %+v", invalid)
		}
	}
}

func TestCloudPublicationReconcilePersistsTheCurrentManifestInFirestore(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cloudProject := fmt.Sprintf("content-context-worker-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	cfg := workerConfig{
		Bucket: "local-test", UserID: "owner", ProjectID: "project", ExecutionID: "olw-pipeline-content-context-test",
	}
	objects := newMemoryObjects()
	prefix := workerProjectObjectPrefix(cfg)
	_, digest := seedContentContextManifest(t, objects, prefix, "published-current", "published project content")

	if err := reconcileCloudContentContext(ctx, cfg, objects); err != nil {
		t.Fatalf("persist reconciled current publication: %v", err)
	}

	client, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore readback client: %v", err)
	}
	defer client.Close()
	stateRef := scopedfirestore.Collection(client, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() { _, _ = stateRef.Delete(context.Background()) }()
	snapshot, err := stateRef.Get(ctx)
	if err != nil {
		t.Fatalf("read reconciled Firestore state: %v", err)
	}
	var state contentcontext.StateDocument
	if err := snapshot.DataTo(&state); err != nil {
		t.Fatalf("decode reconciled Firestore state: %v", err)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("validate reconciled Firestore state: %v", err)
	}
	if state.TargetGeneration != "published-current" || state.TargetConceptsSHA != digest || state.Status != contentcontext.StatusPending ||
		state.LatestPublication == nil || state.LatestPublication.ManifestIdentity == "" || state.SummaryWork == nil ||
		state.SummaryWork.SourceGeneration != "published-current" || state.SummaryWork.SourceManifestIdentity != state.LatestPublication.ManifestIdentity {
		t.Fatalf("reconciled worker state=%+v", state)
	}
}

func TestCloudContentContextUsesThePinnedFastProfileAndPersistsOneSummary(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cloudProject := fmt.Sprintf("content-context-fast-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	cfg := workerConfig{
		Bucket: "local-test", UserID: "owner", ProjectID: "project", ExecutionID: "olw-pipeline-content-context-fast-test",
		ProfileCandidates: []pipelineLLMProfile{{
			SchemaVersion: 2, SnapshotDigest: strings.Repeat("a", 64), RoleMapVersion: 1,
			ID: "fast-profile", Revision: 7, Provider: "custom", Protocol: llm.ProtocolOpenAIChatCompletions,
			Endpoint: "https://provider.invalid/v1", Models: pipelineLLMModels{Fast: "fast-model", Heavy: "heavy-model"}, APIKey: []byte("secret"),
		}},
	}
	objects := newMemoryObjects()
	prefix := workerProjectObjectPrefix(cfg)
	_, digest := seedContentContextManifest(t, objects, prefix, "published-fast", "published project content")
	stateClient, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore cleanup client: %v", err)
	}
	stateRef := scopedfirestore.Collection(stateClient, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_ = stateClient.Close()
	}()

	oldInputLoader, oldClientFactory := loadContentContextSummaryInput, newContentContextSummaryClient
	t.Cleanup(func() {
		loadContentContextSummaryInput, newContentContextSummaryClient = oldInputLoader, oldClientFactory
	})
	loadContentContextSummaryInput = func(_ context.Context, _ workerConfig, claim contentcontext.SummaryClaim) ([]byte, contentcontext.SummaryInputMetadata, error) {
		return []byte(`{"concepts":[{"slug":"project-context","title":"Project Context","body":"Published project content"}]}`), contentcontext.SummaryInputMetadata{
			ConceptsDigest: claim.ConceptsSHA256, SourceGeneration: claim.SourceGeneration,
			InputDigest: strings.Repeat("b", 64), SelectionVersion: contentcontext.SummarySelectionVersion,
		}, nil
	}
	testClient := &contentContextTestSummaryClient{profile: cfg.ProfileCandidates[0], result: llm.CompletionResult{
		Text: `{"summary":"This Project contains published project context."}`,
		Metadata: llm.CompletionMetadata{
			ProfileID: "fast-profile", ProfileRevision: 7, Role: string(llm.ProfileRoleFast),
			RequestedModel: "fast-model", ReturnedModel: "fast-model-v2",
		},
	}}
	newContentContextSummaryClient = func(profile pipelineLLMProfile) (contentContextSummaryClient, error) {
		if profile.ID != "fast-profile" || normalizePipelineLLMModels(profile).Fast != "fast-model" {
			t.Fatalf("summary client candidate id=%q fast_model=%q", profile.ID, normalizePipelineLLMModels(profile).Fast)
		}
		return testClient, nil
	}

	if err := runCloudContentContext(ctx, cfg, objects); err != nil {
		t.Fatalf("run cloud content-context summary: %v", err)
	}
	if testClient.calls != 1 {
		t.Fatalf("summary provider calls=%d, want one", testClient.calls)
	}
	snapshot, err := stateRef.Get(ctx)
	if err != nil {
		t.Fatalf("read summarized Firestore state: %v", err)
	}
	var state contentcontext.StateDocument
	if err := snapshot.DataTo(&state); err != nil {
		t.Fatalf("decode summarized state: %v", err)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("validate summarized state: %v", err)
	}
	if state.Status != contentcontext.StatusReady || state.LastGood == nil || state.LastGood.Text != "This Project contains published project context." ||
		state.LastGood.SourceGeneration != "published-fast" || state.LastGood.ConceptsSHA256 != digest ||
		state.LastGood.ProfileID != "fast-profile" || state.LastGood.Role != string(llm.ProfileRoleFast) ||
		state.LastGood.RequestedModel != "fast-model" || state.LastGood.ReturnedModel != "fast-model-v2" {
		t.Fatalf("unexpected persisted Fast summary state: %+v", state)
	}
	if testClient.profile.Models.Heavy != "heavy-model" {
		t.Fatalf("test profile Heavy model changed: %+v", testClient.profile.Models)
	}
}

func TestCloudContentContextMarksEmptyPublishedSnapshotWithoutCallingModel(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cloudProject := fmt.Sprintf("content-context-empty-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	cfg := workerConfig{Bucket: "local-test", UserID: "owner", ProjectID: "empty-project", ExecutionID: "olw-pipeline-content-context-empty-test"}
	objects := newMemoryObjects()
	prefix := workerProjectObjectPrefix(cfg)
	emptyConcepts, err := generation.NewFile("cache/concepts.jsonl", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generation.Manifest{
		Version: generation.Version, GenerationID: "empty-generation",
		CreatedAt: "2026-10-30T00:00:00Z", InputFingerprint: "empty-content-context-test",
		Files: []generation.File{emptyConcepts},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Write(ctx, prefix+generation.ManifestPath, manifestData, nil, objectConditions{}); err != nil {
		t.Fatal(err)
	}
	stateClient, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		t.Fatalf("create Firestore cleanup client: %v", err)
	}
	stateRef := scopedfirestore.Collection(stateClient, "projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("content_context").Doc("state")
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_ = stateClient.Close()
	}()
	oldInputLoader, oldClientFactory := loadContentContextSummaryInput, newContentContextSummaryClient
	t.Cleanup(func() {
		loadContentContextSummaryInput, newContentContextSummaryClient = oldInputLoader, oldClientFactory
	})
	loadContentContextSummaryInput = func(_ context.Context, _ workerConfig, claim contentcontext.SummaryClaim) ([]byte, contentcontext.SummaryInputMetadata, error) {
		return nil, contentcontext.SummaryInputMetadata{ConceptsDigest: claim.ConceptsSHA256, SourceGeneration: claim.SourceGeneration, SelectionVersion: contentcontext.SummarySelectionVersion}, contentcontext.ErrEmptyConcepts
	}
	newContentContextSummaryClient = func(pipelineLLMProfile) (contentContextSummaryClient, error) {
		t.Fatal("empty published snapshot attempted model inference")
		return nil, errors.New("unreachable")
	}

	if err := runCloudContentContext(ctx, cfg, objects); err != nil {
		t.Fatalf("process empty published snapshot: %v", err)
	}
	snapshot, err := stateRef.Get(ctx)
	if err != nil {
		t.Fatalf("read empty content-context state: %v", err)
	}
	var state contentcontext.StateDocument
	if err := snapshot.DataTo(&state); err != nil {
		t.Fatalf("decode empty content-context state: %v", err)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("validate empty content-context state: %v", err)
	}
	if state.Status != contentcontext.StatusMissing || state.LastGood != nil || state.SummaryWork == nil || state.SummaryWork.Status != contentcontext.StatusMissing {
		t.Fatalf("empty published snapshot state=%+v", state)
	}
}
