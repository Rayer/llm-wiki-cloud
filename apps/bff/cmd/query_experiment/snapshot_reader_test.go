package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

func TestSnapshotReaderRejectsOversizedSupportedArtifactsAndPages(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		path string
		size int64
	}{
		{name: "ID map", path: "cache/id_map.json", size: generation.MaxFileBytes + 1},
		{name: "concepts", path: conceptsPath, size: generation.MaxFileBytes + 1},
		{name: "suggested queries", path: suggestedPath, size: suggestedqueries.MaxArtifactBytes + 1},
		{name: "page", path: "wiki/large.md", size: generation.MaxFileBytes + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, test.size); err != nil {
				t.Fatal(err)
			}
			reader := newSnapshotReader(root)
			var err error
			if test.name == "page" {
				_, _, err = reader.GetPage(context.Background(), "large", "concepts")
			} else {
				_, err = reader.ReadFile(context.Background(), test.path)
			}
			if err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("read error = %v, want deterministic size rejection", err)
			}
		})
	}
}

func TestPreflightFreezesConceptsBeforeCacheLoad(t *testing.T) {
	root := t.TempDir()
	original := `{"slug":"original","title":"Original"}` + "\n"
	replacement := `{"slug":"replacement","title":"Replacement"}` + "\n"
	writeTestFile(t, filepath.Join(root, conceptsPath), original)
	prepared, err := preflightSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, conceptsPath), replacement)
	entries, err := prepared.cache.All(context.Background(), prepared.reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Slug != "original" || prepared.digest == "" {
		t.Fatalf("entries=%#v digest=%q, want frozen original corpus", entries, prepared.digest)
	}
}

func TestSnapshotReaderPreservesMissingOptionalSuggestedArtifact(t *testing.T) {
	reader := newSnapshotReader(t.TempDir())
	_, err := reader.ReadFile(context.Background(), suggestedPath)
	if !errors.Is(err, errSnapshotPathNotFound) {
		t.Fatalf("missing suggested artifact error = %v, want path-not-found", err)
	}
}

func TestPreflightFreezesCitationMapWithCorpus(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, conceptsPath), `{"slug":"台北 café","title":"Display","body":"Evidence"}`+"\n")
			const original = `{"concept":{"01ARZ3NDEKTSV4RRFFQ69G5FAV":"台北 café"}}`
			if present {
				writeTestFile(t, filepath.Join(root, "cache/id_map.json"), original)
			}
			prepared, err := preflightSnapshot(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(root, "cache/id_map.json"), `{"concept":{"abcdef123456":"台北 café"}}`)
			ids, err := query.LoadCitationIDMap(context.Background(), prepared.reader)
			if !present {
				if err == nil {
					t.Fatal("adopted a map absent from original snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := query.ResolveCitationIdentity(search.Result{Slug: "台北 café", Type: "concept"}, ids)
			if err != nil || result.ID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
				t.Fatalf("mixed snapshot map: %+v %v", result, err)
			}
		})
	}
}

func TestSnapshotCitationArtifactsRejectSymlinkAndTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	writeTestFile(t, outside, "outside")
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "cache/id_map.json")); err != nil {
		t.Fatal(err)
	}
	reader := newSnapshotReader(root)
	if _, err := reader.ReadFile(context.Background(), "cache/id_map.json"); err == nil {
		t.Fatal("followed ID map symlink")
	}
	for _, slug := range []string{"../outside", `a\b`, "a/b"} {
		if _, _, err := reader.GetPage(context.Background(), slug, "sources"); err == nil {
			t.Fatal("accepted source traversal")
		}
	}
	writeTestFile(t, filepath.Join(root, "wiki/sources/台北 café.md"), "Source body")
	_, body, err := reader.GetPage(context.Background(), "台北 café", "sources")
	if err != nil || string(body) != "Source body" {
		t.Fatalf("source read: %q %v", body, err)
	}
}
