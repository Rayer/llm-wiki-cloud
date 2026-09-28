package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type fakeReader struct {
	concepts     []gcs.WikiPage
	pages        map[string]string
	errs         map[string]error
	jsonl        string // pre-built JSONL for ReadFile
	getPageCalls atomic.Int64
	listCalls    atomic.Int64
	readCalls    atomic.Int64
	readErr      error
	allowEmpty   bool
}

func (f *fakeReader) ReadFile(_ context.Context, _ string) ([]byte, error) {
	f.readCalls.Add(1)
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.jsonl == "" && !f.allowEmpty {
		return nil, errors.New("not found")
	}
	return []byte(f.jsonl), nil
}

func (f *fakeReader) ListConcepts(_ context.Context, _ bool) ([]gcs.WikiPage, error) {
	f.listCalls.Add(1)
	return f.concepts, nil
}

func (f *fakeReader) GetPage(_ context.Context, slug, category string) (*gcs.WikiPage, []byte, error) {
	f.getPageCalls.Add(1)
	if category != "concepts" {
		return nil, nil, errors.New("unexpected category")
	}
	if err := f.errs[slug]; err != nil {
		return nil, nil, err
	}
	return &gcs.WikiPage{Slug: slug, Title: slug}, []byte(f.pages[slug]), nil
}

func (f *fakeReader) WriteBytes(_ context.Context, data []byte, _ string) (string, error) {
	f.jsonl = string(data)
	return "ok", nil
}

func makeJSONL(entries []Entry) string {
	var buf strings.Builder
	for _, e := range entries {
		b, _ := json.Marshal(e)
		buf.WriteString(string(b) + "\n")
	}
	return buf.String()
}

func TestUnmarshalJSONLRejectsLogicalEntryOverflow(t *testing.T) {
	data := makeJSONL([]Entry{{Slug: "seed", Title: "Seed"}})
	data += strings.Repeat(`{"slug":"overflow","title":"Overflow"}`+"\n", 10000)
	if _, err := unmarshalJSONL([]byte(data)); err == nil || err.Error() != "generated cache logical entry limit exceeded" {
		t.Fatalf("unmarshalJSONL() error = %v, want fixed logical-entry error", err)
	}
}

func TestQueryReadsJSONLAndReturnsRankedResults(t *testing.T) {
	entries := []Entry{
		{Slug: "alpha", Title: "Alpha", Body: "shared topic alpha"},
		{Slug: "beta", Title: "Beta shared", Body: "body"},
		{Slug: "gamma", Title: "Gamma", Body: "shared idea"},
	}
	reader := &fakeReader{jsonl: makeJSONL(entries)}

	results, err := New().Query(context.Background(), reader, "shared", 2)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if got, want := len(results), 2; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if results[0].Slug == results[1].Slug {
		t.Fatal("Query() returned duplicate slug")
	}
	for _, r := range results {
		if r.Type != "concept" {
			t.Fatalf("result.Type = %q, want concept", r.Type)
		}
		if r.Snippet == "" {
			t.Fatalf("result.Snippet is empty")
		}
	}
}

func TestPinnedGenerationTokenSeparatesProjectCaches(t *testing.T) {
	reader := &tokenReader{fakeReader: fakeReader{jsonl: makeJSONL([]Entry{{Slug: "from-a", Title: "A", Body: "shared"}})}, prefix: "users/u/projects/p", token: "manifest-7"}
	cache := New()
	first, err := cache.Search(context.Background(), reader, "shared", 10)
	if err != nil || len(first) != 1 || first[0].Slug != "from-a" {
		t.Fatalf("generation A search = %#v, %v", first, err)
	}
	reader.token = "manifest-8"
	reader.jsonl = makeJSONL([]Entry{{Slug: "from-b", Title: "B", Body: "shared"}})
	next, err := cache.Search(context.Background(), reader, "shared", 10)
	if err != nil || len(next) != 1 || next[0].Slug != "from-b" {
		t.Fatalf("generation B search reused A cache = %#v, %v", next, err)
	}
}

type tokenReader struct {
	fakeReader
	prefix string
	token  string
}

func (r *tokenReader) Prefix() string    { return r.prefix }
func (r *tokenReader) ViewToken() string { return r.token }

type pinnedSnapshotReader struct {
	fakeReader
	prefix   string
	token    string
	identity storage.QueryGenerationIdentity
}

func (r *pinnedSnapshotReader) Prefix() string    { return r.prefix }
func (r *pinnedSnapshotReader) ViewToken() string { return r.token }
func (r *pinnedSnapshotReader) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	return r.identity, nil
}

func TestPinnedCanonicalSnapshotRequiresCanonicalJSONLWithoutFallback(t *testing.T) {
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{
			concepts: []gcs.WikiPage{{Slug: "fallback"}},
			pages:    map[string]string{"fallback": "page body"},
		},
		prefix: "users/u/projects/p", token: "manifest-7",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G1", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)},
	}

	if _, err := New().PinnedCanonicalSnapshot(context.Background(), reader); err == nil {
		t.Fatal("PinnedCanonicalSnapshot() error = nil for missing canonical JSONL")
	}
	if got := reader.readCalls.Load(); got != 1 {
		t.Fatalf("ReadFile calls = %d, want 1", got)
	}
	if got := reader.listCalls.Load(); got != 0 {
		t.Fatalf("ListConcepts calls = %d, want 0", got)
	}
	if got := reader.getPageCalls.Load(); got != 0 {
		t.Fatalf("GetPage calls = %d, want 0", got)
	}

	// The existing unprofiled path keeps its page-read fallback.
	entries, err := New().All(context.Background(), reader)
	if err != nil || len(entries) != 1 || entries[0].Slug != "fallback" {
		t.Fatalf("All() fallback = %#v, %v", entries, err)
	}
}

func TestPinnedCanonicalSnapshotReturnsExactRowDigestsFromOneRead(t *testing.T) {
	row := `{"slug":"alpha","title":"Alpha","body":"alpha body","frontmatter":{"id":"stable-alpha"}}`
	data := row + "\n"
	digest := sha256.Sum256([]byte(row))
	fileDigest := sha256.Sum256([]byte(data))
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{jsonl: data},
		prefix:     "users/u/projects/p", token: "manifest-7",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G1", ConceptsDigest: "sha256:" + hex.EncodeToString(fileDigest[:])},
	}
	cache := New()
	if entries, err := cache.All(context.Background(), reader); err != nil || len(entries) != 1 || entries[0].Body != "alpha body" {
		t.Fatalf("All() warm read = %#v, %v", entries, err)
	}
	if got := reader.readCalls.Load(); got != 1 {
		t.Fatalf("ReadFile calls after All() = %d, want 1", got)
	}

	got, err := cache.PinnedCanonicalSnapshot(context.Background(), reader)
	if err != nil {
		t.Fatalf("PinnedCanonicalSnapshot() error = %v", err)
	}
	if got.Identity.GenerationID != "G1" || got.Identity.ConceptsDigest != reader.identity.ConceptsDigest {
		t.Fatalf("snapshot identity = %#v", got.Identity)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("len(snapshot.Entries) = %d, want 1", len(got.Entries))
	}
	item := got.Entries[0]
	if item.StableID != "stable-alpha" || item.ContentDigest != hex.EncodeToString(digest[:]) || item.Entry.Body != "alpha body" {
		t.Fatalf("snapshot entry = %#v", item)
	}
	if got := reader.readCalls.Load(); got != 1 {
		t.Fatalf("ReadFile calls = %d, want 1", got)
	}
	if _, err := cache.PinnedCanonicalSnapshot(context.Background(), reader); err != nil {
		t.Fatalf("cached PinnedCanonicalSnapshot() error = %v", err)
	}
	if got := reader.readCalls.Load(); got != 1 {
		t.Fatalf("ReadFile calls after cache hit = %d, want 1", got)
	}
}

func TestPinnedCanonicalSnapshotRejectsCorruptJSONLWithoutFallback(t *testing.T) {
	data := `{"slug":"alpha","frontmatter":{"id":"stable-alpha"}}` + "\n{" + "\n"
	fileDigest := sha256.Sum256([]byte(data))
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{jsonl: data, concepts: []gcs.WikiPage{{Slug: "fallback"}}},
		prefix:     "users/u/projects/p", token: "manifest-7",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G1", ConceptsDigest: "sha256:" + hex.EncodeToString(fileDigest[:])},
	}
	if _, err := New().PinnedCanonicalSnapshot(context.Background(), reader); err == nil {
		t.Fatal("PinnedCanonicalSnapshot() error = nil for corrupt JSONL")
	}
	if reader.listCalls.Load() != 0 || reader.getPageCalls.Load() != 0 {
		t.Fatalf("fallback calls: ListConcepts=%d GetPage=%d", reader.listCalls.Load(), reader.getPageCalls.Load())
	}
}

func TestPinnedCanonicalSnapshotRejectsManifestDigestMismatch(t *testing.T) {
	data := `{"slug":"alpha","frontmatter":{"id":"stable-alpha"}}` + "\n"
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{jsonl: data, concepts: []gcs.WikiPage{{Slug: "fallback"}}},
		prefix:     "users/u/projects/p", token: "manifest-7",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G1", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)},
	}
	if _, err := New().PinnedCanonicalSnapshot(context.Background(), reader); err == nil {
		t.Fatal("PinnedCanonicalSnapshot() error = nil for manifest digest mismatch")
	}
	if reader.readCalls.Load() != 1 || reader.listCalls.Load() != 0 || reader.getPageCalls.Load() != 0 {
		t.Fatalf("calls: ReadFile=%d ListConcepts=%d GetPage=%d", reader.readCalls.Load(), reader.listCalls.Load(), reader.getPageCalls.Load())
	}
}

func TestPinnedCanonicalSnapshotAcceptsValidEmptyJSONL(t *testing.T) {
	emptyDigest := sha256.Sum256(nil)
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{
			allowEmpty: true,
			concepts:   []gcs.WikiPage{{Slug: "fallback"}},
			pages:      map[string]string{"fallback": "page body"},
		},
		prefix: "users/u/projects/p", token: "manifest-empty",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G-empty", ConceptsDigest: "sha256:" + hex.EncodeToString(emptyDigest[:])},
	}
	cache := New()
	snapshot, err := cache.PinnedCanonicalSnapshot(context.Background(), reader)
	if err != nil {
		t.Fatalf("PinnedCanonicalSnapshot() error = %v", err)
	}
	if len(snapshot.Entries) != 0 || reader.readCalls.Load() != 1 {
		t.Fatalf("snapshot entries=%d ReadFile calls=%d", len(snapshot.Entries), reader.readCalls.Load())
	}
	entries, err := cache.All(context.Background(), reader)
	if err != nil || len(entries) != 1 || entries[0].Slug != "fallback" {
		t.Fatalf("All() fallback after strict empty snapshot = %#v, %v", entries, err)
	}
}

func TestPinnedCanonicalSnapshotSeparatesGenerations(t *testing.T) {
	cache := New()
	firstData := `{"slug":"alpha","title":"G1","body":"first","frontmatter":{"id":"stable-alpha"}}` + "\n"
	secondData := `{"slug":"alpha","title":"G2","body":"second","frontmatter":{"id":"stable-alpha"}}` + "\n"
	firstDigest := sha256.Sum256([]byte(firstData))
	secondDigest := sha256.Sum256([]byte(secondData))
	reader := &pinnedSnapshotReader{
		fakeReader: fakeReader{jsonl: firstData},
		prefix:     "users/u/projects/p", token: "manifest-G1",
		identity: storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: "G1", ConceptsDigest: "sha256:" + hex.EncodeToString(firstDigest[:])},
	}
	first, err := cache.PinnedCanonicalSnapshot(context.Background(), reader)
	if err != nil {
		t.Fatalf("G1 snapshot error = %v", err)
	}
	reader.token = "manifest-G2"
	reader.identity.GenerationID = "G2"
	reader.identity.ConceptsDigest = "sha256:" + hex.EncodeToString(secondDigest[:])
	reader.jsonl = secondData
	second, err := cache.PinnedCanonicalSnapshot(context.Background(), reader)
	if err != nil {
		t.Fatalf("G2 snapshot error = %v", err)
	}
	if first.Entries[0].Entry.Body != "first" || second.Entries[0].Entry.Body != "second" {
		t.Fatalf("snapshots mixed generations: G1=%#v G2=%#v", first.Entries[0].Entry, second.Entries[0].Entry)
	}
	if got := reader.readCalls.Load(); got != 2 {
		t.Fatalf("ReadFile calls across G1/G2 = %d, want 2", got)
	}
}

func TestQueryBuildsOnCacheMiss(t *testing.T) {
	reader := &fakeReader{
		concepts: []gcs.WikiPage{
			{Slug: "alpha", Title: "alpha"},
			{Slug: "broken", Title: "broken"},
		},
		pages: map[string]string{
			"alpha": "---\ntitle: Alpha Concept\nsources: [Source One, Source Two]\ntags: [one, two]\n---\nAlpha body text.",
		},
		errs: map[string]error{"broken": errors.New("read failed")},
	}

	results, err := New().Query(context.Background(), reader, "Alpha", 10)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 1 || results[0].Slug != "alpha" {
		t.Fatalf("results = %#v, want alpha only", results)
	}
	// Verify JSONL was persisted
	if reader.jsonl == "" {
		t.Fatal("JSONL was not persisted after build")
	}
}

func TestAllReturnsAllEntries(t *testing.T) {
	reader := &fakeReader{
		concepts: []gcs.WikiPage{{Slug: "a"}, {Slug: "b"}},
		pages: map[string]string{
			"a": "---\ntitle: A\ntags: [x]\n---\nbody A",
			"b": "---\ntitle: B\ntags: [y]\n---\nbody B",
		},
	}

	entries, err := New().All(context.Background(), reader)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	bySlug := map[string]Entry{}
	for _, e := range entries {
		bySlug[e.Slug] = e
	}
	if bySlug["a"].Title != "A" || bySlug["b"].Title != "B" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestBuildReadsJSONLWithoutGetPage(t *testing.T) {
	entries := []Entry{
		{Slug: "alpha", Title: "Alpha", Body: "alpha body"},
	}
	reader := &fakeReader{
		jsonl:    makeJSONL(entries),
		concepts: []gcs.WikiPage{{Slug: "alpha"}, {Slug: "beta"}},
		pages:    map[string]string{"alpha": "should not be read"},
	}

	got, err := New().Build(context.Background(), reader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(got) != 1 || got[0].Slug != "alpha" || got[0].Body != "alpha body" {
		t.Fatalf("entries = %#v", got)
	}
	if calls := reader.getPageCalls.Load(); calls != 0 {
		t.Fatalf("GetPage called %d times, want 0", calls)
	}
}

func TestSearchUsesJSONLOnColdStart(t *testing.T) {
	entries := []Entry{
		{Slug: "keyword-slug", Title: "Keyword Title", Body: "content with keyword"},
	}
	reader := &fakeReader{
		jsonl:    makeJSONL(entries),
		concepts: []gcs.WikiPage{{Slug: "keyword-slug"}},
		pages:    map[string]string{"keyword-slug": "should not be read"},
	}

	results, err := New().Search(context.Background(), reader, "keyword", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Slug != "keyword-slug" {
		t.Fatalf("results = %#v", results)
	}
	if calls := reader.getPageCalls.Load(); calls != 0 {
		t.Fatalf("GetPage called %d times, want 0", calls)
	}
}

func TestQueryEmptyJSONLFallsBackToBuild(t *testing.T) {
	reader := &fakeReader{
		jsonl:    "", // empty/missing
		concepts: []gcs.WikiPage{{Slug: "x"}},
		pages:    map[string]string{"x": "---\ntitle: X\n---\ncontent with keyword"},
	}

	results, err := New().Query(context.Background(), reader, "keyword", 10)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 1 || results[0].Slug != "x" {
		t.Fatalf("results = %#v, want x", results)
	}
}
