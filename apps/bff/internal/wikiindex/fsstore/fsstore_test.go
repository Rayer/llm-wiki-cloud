package fsstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

func TestListMarkdownFilesListsDirectChildrenOnly(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "wiki", "beta.md"), []byte("beta"))
	mustWriteFile(t, filepath.Join(root, "wiki", "alpha.md"), []byte("alpha"))
	mustWriteFile(t, filepath.Join(root, "wiki", "notes.txt"), []byte("notes"))
	mustWriteFile(t, filepath.Join(root, "wiki", "nested", "gamma.md"), []byte("gamma"))

	files, err := New(root).ListMarkdownFiles(context.Background(), "wiki/")
	if err != nil {
		t.Fatalf("ListMarkdownFiles() error = %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2: %#v", len(files), files)
	}
	want := []wikiindex.MarkdownFile{
		{Slug: "alpha", Path: "wiki/alpha.md", Data: []byte("alpha")},
		{Slug: "beta", Path: "wiki/beta.md", Data: []byte("beta")},
	}
	for i := range want {
		if files[i].Slug != want[i].Slug || files[i].Path != want[i].Path || string(files[i].Data) != string(want[i].Data) {
			t.Fatalf("files[%d] = %#v, want %#v", i, files[i], want[i])
		}
	}
}

func TestListMarkdownFilesMissingDirectoryReturnsEmpty(t *testing.T) {
	files, err := New(t.TempDir()).ListMarkdownFiles(context.Background(), "wiki/missing")
	if err != nil {
		t.Fatalf("ListMarkdownFiles() error = %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("len(files) = %d, want 0", len(files))
	}
}

func TestReadFileMapsNotFound(t *testing.T) {
	_, err := New(t.TempDir()).ReadFile(context.Background(), "wiki/missing.md")
	if !errors.Is(err, wikiindex.ErrNotFound) {
		t.Fatalf("ReadFile() error = %v, want ErrNotFound", err)
	}
}

func TestWriteBytesAtomicReplacesFileAndReturnsSHA256(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	mustWriteFile(t, filepath.Join(root, "cache", "out.txt"), []byte("old"))

	data := []byte("replacement")
	digest, err := store.WriteBytesAtomic(context.Background(), data, "cache/out.txt.tmp", "cache/out.txt")
	if err != nil {
		t.Fatalf("WriteBytesAtomic() error = %v", err)
	}

	sum := sha256.Sum256(data)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q, want %q", digest, hex.EncodeToString(sum[:]))
	}
	got, err := os.ReadFile(filepath.Join(root, "cache", "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("final data = %q, want %q", got, data)
	}
	if _, err := os.Stat(filepath.Join(root, "cache", "out.txt.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file stat error = %v, want not exist", err)
	}
}

func TestRejectsPathsEscapingRoot(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	if _, err := store.ListMarkdownFiles(ctx, "../wiki"); err == nil {
		t.Fatal("ListMarkdownFiles() error = nil, want traversal rejection")
	}
	if _, err := store.ReadFile(ctx, "wiki/../../outside"); err == nil {
		t.Fatal("ReadFile() error = nil, want normalized traversal rejection")
	}
	if _, err := store.ReadFile(ctx, "/tmp/outside.md"); err == nil {
		t.Fatal("ReadFile() error = nil, want absolute path rejection")
	}
	if _, err := store.WriteBytesAtomic(ctx, []byte("data"), "cache/tmp", "../cache/out"); err == nil {
		t.Fatal("WriteBytesAtomic() error = nil, want traversal rejection")
	}
}

func TestRejectsSymlinkEscapesUnderRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.md"), []byte("secret"))
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "wiki", "secret.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	store := New(root)
	if _, err := store.ReadFile(context.Background(), "linked-dir/secret.md"); err == nil {
		t.Fatal("ReadFile() error = nil, want symlink dir escape rejection")
	}
	if _, err := store.ListMarkdownFiles(context.Background(), "linked-dir"); err == nil {
		t.Fatal("ListMarkdownFiles() error = nil, want symlink dir escape rejection")
	}
	if _, err := store.ListMarkdownFiles(context.Background(), "wiki"); err == nil {
		t.Fatal("ListMarkdownFiles() error = nil, want symlink file escape rejection")
	}
}

func TestWriteBytesAtomicDoesNotFollowSymlinkTempPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	mustWriteFile(t, outsideFile, []byte("outside"))
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "cache", "out.txt.tmp")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := New(root).WriteBytesAtomic(context.Background(), []byte("replacement"), "cache/out.txt.tmp", "cache/out.txt"); err != nil {
		t.Fatalf("WriteBytesAtomic() error = %v", err)
	}
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside file = %q, want untouched", got)
	}
}

func TestWriteBytesAtomicCleansUpTempFileOnCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := New(root).WriteBytesAtomic(ctx, []byte("data"), "cache/out.txt.tmp", "cache/out.txt"); err == nil {
		t.Fatal("WriteBytesAtomic() error = nil, want cancellation error")
	}
	entries, err := os.ReadDir(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache entries after canceled write = %d, want 0", len(entries))
	}
}

func TestRebuildOnRealFSStoreAcceptsManualEditAndPreservesThirdRun(t *testing.T) {
	const conceptID = "a3f7b2c01d9d"
	for _, unknownPriorTime := range []bool{false, true} {
		name := "known prior time"
		if unknownPriorTime {
			name = "unknown prior time"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			oldPage := []byte("---\nid: " + conceptID + "\ntitle: Alpha\n---\ninitial body\n")
			writeTestFile(t, filepath.Join(root, "wiki", "alpha.md"), oldPage)
			store := New(root)
			if _, err := wikiindex.Rebuild(context.Background(), store); err != nil {
				t.Fatalf("gen1 Rebuild(): %v", err)
			}
			first := readConceptRow(t, filepath.Join(root, "cache", "concepts.jsonl"))
			firstTime := stringField(first, "content_updated_at")
			if firstTime == "" || stringField(first, "content_sha256") == "" {
				t.Fatalf("gen1 row lacks recency: %#v", first)
			}
			if unknownPriorTime {
				delete(first, "content_updated_at")
				writeTestFile(t, filepath.Join(root, "cache", "concepts.jsonl"), encodeConceptRow(t, first))
				firstTime = ""
			}

			newPage := []byte("---\nid: " + conceptID + "\ntitle: Alpha\n---\nmanual user edit\n")
			writeTestFile(t, filepath.Join(root, "wiki", "alpha.md"), newPage)
			if _, err := wikiindex.Rebuild(context.Background(), store); err != nil {
				t.Fatalf("gen2 manual-edit Rebuild(): %v", err)
			}
			second := readConceptRow(t, filepath.Join(root, "cache", "concepts.jsonl"))
			secondTime := stringField(second, "content_updated_at")
			if secondTime == "" || secondTime == firstTime || stringField(second, "content_sha256") == stringField(first, "content_sha256") {
				t.Fatalf("gen2 failed to establish one changed-content time: first=%#v second=%#v", first, second)
			}
			if _, err := time.Parse(time.RFC3339Nano, secondTime); err != nil {
				t.Fatalf("gen2 content_updated_at=%q: %v", secondTime, err)
			}

			if _, err := wikiindex.Rebuild(context.Background(), store); err != nil {
				t.Fatalf("gen3 no-op Rebuild(): %v", err)
			}
			third := readConceptRow(t, filepath.Join(root, "cache", "concepts.jsonl"))
			if got := stringField(third, "content_updated_at"); got != secondTime {
				t.Fatalf("gen3 no-op time=%q, want exact gen2 value %q", got, secondTime)
			}
		})
	}
}

func TestCaptureSnapshotKeepsVerifiedPageWithoutCacheRow(t *testing.T) {
	const conceptID = "a3f7b2c01d9d"
	oldPage := []byte("---\nid: " + conceptID + "\ntitle: Alpha\n---\nlegacy visible body\n")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cache", "id_map.json"), []byte(`{"concept":{"`+conceptID+`":"alpha"},"source":{},"redirects":{}}`))
	writeTestFile(t, filepath.Join(root, "cache", "concepts.jsonl"), nil)
	writeTestFile(t, filepath.Join(root, "wiki", "alpha.md"), oldPage)
	prior, err := wikiindex.CaptureConceptRecencySnapshot(context.Background(), New(root))
	if err != nil {
		t.Fatalf("CaptureConceptRecencySnapshot(): %v", err)
	}
	if !bytes.Equal(prior.ActivePages[conceptID], oldPage) {
		t.Fatalf("verified legacy page = %q, want %q", prior.ActivePages[conceptID], oldPage)
	}

	for _, tc := range []struct {
		name     string
		current  []byte
		wantTime bool
	}{
		{name: "unchanged legacy page remains unknown", current: oldPage, wantTime: false},
		{name: "changed legacy page gets first observed time", current: []byte("---\nid: " + conceptID + "\ntitle: Alpha\n---\nlegacy visible edit\n"), wantTime: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := []byte(`{"slug":"alpha","title":"Alpha","frontmatter":{"id":"` + conceptID + `"}}` + "\n")
			current := wikiindex.ConceptRecencySnapshot{
				IDMap:         wikiindex.IDMap{Concept: map[string]string{conceptID: "alpha"}, DormantConcept: map[string]string{}, IDRedirects: map[string]string{}},
				IDMapVerified: true, ActiveJSONL: row,
				ActivePages: map[string][]byte{conceptID: tc.current}, DormantPages: map[string][]byte{},
			}
			active, _, err := wikiindex.EnrichConceptRecency(current, prior, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("EnrichConceptRecency(): %v", err)
			}
			got := decodeConceptRow(t, active)
			if stringField(got, "content_sha256") == "" {
				t.Fatalf("legacy page fallback did not seed current hash: %#v", got)
			}
			if hasTime := stringField(got, "content_updated_at") != ""; hasTime != tc.wantTime {
				t.Fatalf("content_updated_at presence=%t, want %t: %#v", hasTime, tc.wantTime, got)
			}
		})
	}
}

func TestRebuildUsesVerifiedPageWhenPriorCacheRowIsMissing(t *testing.T) {
	const conceptID = "a3f7b2c01d9d"
	root := t.TempDir()
	page := []byte("---\nid: " + conceptID + "\ntitle: Alpha\n---\nlegacy visible body\n")
	writeTestFile(t, filepath.Join(root, "cache", "id_map.json"), []byte(`{"concept":{"`+conceptID+`":"alpha"},"source":{},"redirects":{}}`))
	writeTestFile(t, filepath.Join(root, "cache", "concepts.jsonl"), nil)
	writeTestFile(t, filepath.Join(root, "wiki", "alpha.md"), page)
	if _, err := wikiindex.Rebuild(context.Background(), New(root)); err != nil {
		t.Fatalf("Rebuild(): %v", err)
	}
	row := readConceptRow(t, filepath.Join(root, "cache", "concepts.jsonl"))
	if stringField(row, "content_sha256") == "" {
		t.Fatalf("legacy fallback did not seed hash: %#v", row)
	}
	if stringField(row, "content_updated_at") != "" {
		t.Fatalf("equal legacy page should not invent a birth time: %#v", row)
	}
}

func readConceptRow(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeConceptRow(t, data)
}

func decodeConceptRow(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var row map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &row); err != nil {
		t.Fatalf("decode concept row %q: %v", data, err)
	}
	return row
}

func encodeConceptRow(t *testing.T, row map[string]json.RawMessage) []byte {
	t.Helper()
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func stringField(row map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(row[key], &value)
	return value
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
