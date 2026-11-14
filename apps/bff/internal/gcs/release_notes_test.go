package gcs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	storageerrors "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestReleaseNotesRootStoreUsesLocalScopeAndConditionalIndex(t *testing.T) {
	client, backend := newMemoryClient()
	root := *client
	root.userID, root.projectID = "", ""
	root.localScope = localcloud.Scope("notes-worktree")
	store := NewReleaseNotesStore(&root)
	ctx := context.Background()
	contentPath := "release-notes/v1/versions/1.2.3/en/" + strings.Repeat("a", 64) + ".json"
	content := []byte(`{"content":true}`)
	generation, err := store.CreateContent(ctx, contentPath, content)
	if err != nil || generation < 1 {
		t.Fatalf("CreateContent generation=%d err=%v", generation, err)
	}
	if replay, err := store.CreateContent(ctx, contentPath, content); err != nil || replay != generation {
		t.Fatalf("identical create replay generation=%d err=%v", replay, err)
	}
	if _, err := store.CreateContent(ctx, contentPath, []byte(`{"different":true}`)); err == nil {
		t.Fatal("different bytes replaced immutable content")
	}
	index := []byte(`{"schema_version":1,"versions":[]}`)
	indexGeneration, err := store.CompareAndSwapIndex(ctx, index, 0)
	if err != nil || indexGeneration <= generation {
		t.Fatalf("initial index generation=%d err=%v", indexGeneration, err)
	}
	read, readGeneration, err := store.ReadIndex(ctx)
	if err != nil || string(read) != string(index) || readGeneration != indexGeneration {
		t.Fatalf("ReadIndex data=%s generation=%d err=%v", read, readGeneration, err)
	}
	if _, err := store.CompareAndSwapIndex(ctx, []byte(`{"schema_version":1,"versions":[1]}`), 0); !errors.Is(err, storageerrors.ErrGenerationMismatch) {
		t.Fatalf("stale index create error=%v, want generation mismatch", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for name := range backend.objects {
		if strings.Contains(name, "/users/") || !strings.HasPrefix(name, "local_scopes/notes-worktree/release-notes/v1/") {
			t.Fatalf("release notes escaped fixed local root: %s", name)
		}
	}
}

func TestReleaseNotesRootStoreRejectsScopedAndCallerControlledPaths(t *testing.T) {
	client, _ := newMemoryClient()
	if _, _, err := NewReleaseNotesStore(client).ReadIndex(context.Background()); err == nil {
		t.Fatal("project-scoped client was accepted for root Notes access")
	}
	root := *client
	root.userID, root.projectID = "", ""
	store := NewReleaseNotesStore(&root)
	if _, err := store.ReadContent(context.Background(), "release-notes/v1/index.json"); err == nil {
		t.Fatal("arbitrary Notes object path was accepted")
	}
	if _, _, err := store.ReadIndex(context.Background()); !errors.Is(err, storage.ErrObjectNotExist) {
		t.Fatalf("missing index error=%v, want not found", err)
	}
}
