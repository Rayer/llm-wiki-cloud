package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestQueryGenerationIdentityIsExplicitLegacyWithoutDescriptor(t *testing.T) {
	client := New(t.TempDir()).WithScope("user", "project")
	identity, err := client.QueryGenerationIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.ProjectID != "project" || identity.GenerationID != "legacy" || identity.ConceptsDigest != "" {
		t.Fatalf("identity=%+v", identity)
	}
	if _, err := client.ContentContextPublication(context.Background()); !errors.Is(err, storage.ErrQueryGenerationUnpinned) {
		t.Fatalf("missing local content-context descriptor error=%v", err)
	}
}

func TestContentContextPublicationRequiresDescriptorDigestMatch(t *testing.T) {
	ctx := context.Background()
	client := New(t.TempDir()).WithScope("user", "project")
	concepts := []byte{}
	digest := sha256.Sum256(concepts)
	digestText := hex.EncodeToString(digest[:])
	descriptor := contentcontext.LocalPublicationDescriptor{
		Version: contentcontext.LocalPublicationVersion, OwnerID: "user", ProjectID: "project",
		ExecutionID: "execution-1", TransactionID: "transaction-1", Generation: "local-transaction-1",
		ConceptsDigest: digestText, SummarySourceGeneration: "local-transaction-1",
		SummarySourceIdentity: contentcontext.LocalSummarySourceIdentity("transaction-1", digestText),
		InputErrorClass:       "empty_concepts",
		SummaryMetadata: contentcontext.SummaryInputMetadata{
			ConceptsDigest: digestText, SourceGeneration: "local-transaction-1",
			SelectionVersion: contentcontext.SummarySelectionVersion,
		},
	}
	descriptorData, err := contentcontext.EncodeLocalPublicationDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteBytes(ctx, concepts, "cache/concepts.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteBytes(ctx, descriptorData, contentcontext.LocalPublicationDescriptorPath); err != nil {
		t.Fatal(err)
	}
	identity, err := client.QueryGenerationIdentity(ctx)
	if err != nil || identity.ProjectID != "project" || identity.GenerationID != "legacy" || identity.ConceptsDigest != "" || client.ViewToken() != "legacy" {
		t.Fatalf("Query identity=%+v token=%q err=%v; local Query must remain legacy", identity, client.ViewToken(), err)
	}
	publication, err := client.ContentContextPublication(ctx)
	if err != nil || publication.Generation != descriptor.Generation || publication.ConceptsDigest != digestText {
		t.Fatalf("content-context publication=%+v err=%v", publication, err)
	}
	if _, err := client.WriteBytes(ctx, []byte("changed"), "cache/concepts.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ContentContextPublication(ctx); !errors.Is(err, storage.ErrQueryGenerationIdentityUnavailable) {
		t.Fatal("descriptor with stale concepts digest was accepted")
	}
}
