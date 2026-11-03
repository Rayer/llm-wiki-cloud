package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestReadFileBoundedAcceptsExactLimitAndRejectsOversize(t *testing.T) {
	root := t.TempDir()
	client := New(root).WithScope("u", "p")
	path := filepath.Join(root, "users", "u", "projects", "p", "cache", "fixture")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := client.ReadFileBounded(context.Background(), "cache/fixture", 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("exact-limit bounded read=%q err=%v", data, err)
	}
	if _, err := client.ReadFileBounded(context.Background(), "cache/fixture", 4); !errors.Is(err, store.ErrReadLimitExceeded) {
		t.Fatalf("oversized bounded read err=%v, want ErrReadLimitExceeded", err)
	}
	if _, err := client.ReadFileBounded(context.Background(), "cache/missing", 5); !errors.Is(err, store.ErrObjectNotExist) {
		t.Fatalf("missing bounded read err=%v, want ErrObjectNotExist", err)
	}
}
