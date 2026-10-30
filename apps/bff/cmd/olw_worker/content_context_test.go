package main

import (
	"context"
	"encoding/json"
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
)

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
