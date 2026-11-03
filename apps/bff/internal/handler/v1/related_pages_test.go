package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/search"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	relationAlphaID       = "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	relationBetaID        = "01JAZ5N7Y3K8M2Q4R6T9VWXABD"
	relationLonelyID      = "01JAZ5N7Y3K8M2Q4R6T9VWXABE"
	relationSourceOneID   = "01JAZ5N7Y3K8M2Q4R6T9VWXABF"
	relationSourceTwoID   = "01JAZ5N7Y3K8M2Q4R6T9VWXABG"
	relationTestUser      = "relation-user"
	relationTestProject   = "relation-project"
	relationTestViewToken = "manifest-test-1"
)

type relationTestStats struct {
	mu            sync.Mutex
	boundedCalls  map[string]int
	boundedLimits map[string]int64
	boundedBytes  map[string]int64
	readFailures  map[string]error
	readOverrides map[string][]byte
	conceptLists  int
	sourceLists   int
	pageReads     int
}

type relationTestRoot struct {
	store.RootStore
	stats *relationTestStats
	token string
}

type relationTestStore struct {
	store.Store
	stats         *relationTestStats
	token         string
	dataOverrides map[string][]byte
	failures      map[string]error
}

type relationReadProbe struct {
	mu         sync.Mutex
	active     int
	maxActive  int
	readCalls  int
	started    chan struct{}
	release    chan struct{}
	startedOne sync.Once
}

type relationProbedStore struct {
	store.Store
	token string
	probe *relationReadProbe
	block bool
}

func newRelationTestStats() *relationTestStats {
	return &relationTestStats{
		boundedCalls:  make(map[string]int),
		boundedLimits: make(map[string]int64),
		boundedBytes:  make(map[string]int64),
		readFailures:  make(map[string]error),
		readOverrides: make(map[string][]byte),
	}
}

func (root *relationTestRoot) Scope(userID, projectID string) store.Store {
	return &relationTestStore{Store: root.RootStore.Scope(userID, projectID), stats: root.stats, token: root.token}
}

func (s *relationTestStore) ViewToken() string { return s.token }

func (s *relationProbedStore) ViewToken() string { return s.token }

func (s *relationProbedStore) ReadFileBounded(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	s.probe.mu.Lock()
	s.probe.readCalls++
	s.probe.active++
	if s.probe.active > s.probe.maxActive {
		s.probe.maxActive = s.probe.active
	}
	s.probe.mu.Unlock()
	defer func() {
		s.probe.mu.Lock()
		s.probe.active--
		s.probe.mu.Unlock()
	}()
	if s.block {
		s.probe.startedOne.Do(func() { close(s.probe.started) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.probe.release:
		}
	}
	reader, ok := s.Store.(store.BoundedFileReader)
	if !ok {
		return nil, errors.New("underlying store has no bounded read")
	}
	return reader.ReadFileBounded(ctx, path, maxBytes)
}

func (s *relationTestStore) ReadFileBounded(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	s.stats.mu.Lock()
	s.stats.boundedCalls[path]++
	s.stats.boundedLimits[path] = maxBytes
	err := s.failures[path]
	if err == nil {
		err = s.stats.readFailures[path]
	}
	data, overridden := s.dataOverrides[path]
	if !overridden {
		data, overridden = s.stats.readOverrides[path]
	}
	if overridden {
		consumed := int64(len(data))
		if consumed > maxBytes {
			consumed = maxBytes + 1
		}
		s.stats.boundedBytes[path] += consumed
		s.stats.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, store.ErrReadLimitExceeded
		}
		return append([]byte(nil), data...), nil
	}
	s.stats.mu.Unlock()
	if err != nil {
		return nil, err
	}
	reader, ok := s.Store.(store.BoundedFileReader)
	if !ok {
		return nil, errors.New("underlying store has no bounded read")
	}
	data, err = reader.ReadFileBounded(ctx, path, maxBytes)
	s.stats.mu.Lock()
	if err == nil {
		s.stats.boundedBytes[path] += int64(len(data))
	}
	s.stats.mu.Unlock()
	return data, err
}

func (s *relationTestStore) ListConcepts(ctx context.Context, drafts bool) ([]store.WikiPage, error) {
	s.stats.mu.Lock()
	s.stats.conceptLists++
	s.stats.mu.Unlock()
	return s.Store.ListConcepts(ctx, drafts)
}

func (s *relationTestStore) ListSources(ctx context.Context) ([]store.WikiPage, error) {
	s.stats.mu.Lock()
	s.stats.sourceLists++
	s.stats.mu.Unlock()
	return s.Store.ListSources(ctx)
}

func (s *relationTestStore) GetPage(ctx context.Context, slug, category string) (*store.WikiPage, []byte, error) {
	s.stats.mu.Lock()
	s.stats.pageReads++
	s.stats.mu.Unlock()
	return s.Store.GetPage(ctx, slug, category)
}

func writeRelationFixture(t *testing.T, root string) {
	writeRelationFixtureFor(t, root, relationTestUser, relationTestProject)
}

func writeRelationFixtureFor(t *testing.T, root, userID, projectID string) {
	t.Helper()
	projectRoot := filepath.Join(root, "users", userID, "projects", projectID)
	for _, rel := range []string{"cache", "wiki", "wiki/sources", "raw"} {
		if err := os.MkdirAll(filepath.Join(projectRoot, rel), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	concepts := []map[string]any{
		{"slug": "alpha", "title": "Alpha", "frontmatter": map[string]any{"id": relationAlphaID, "sources": []string{"source-one"}}, "body": "alpha body"},
		{"slug": "beta", "title": "Beta", "frontmatter": map[string]any{"id": relationBetaID, "sources": []string{"source-one", "source-two"}}, "body": "beta body"},
		{"slug": "lonely", "title": "Lonely", "frontmatter": map[string]any{"id": relationLonelyID}, "body": "lonely body"},
	}
	var jsonl strings.Builder
	for _, concept := range concepts {
		row, err := json.Marshal(concept)
		if err != nil {
			t.Fatal(err)
		}
		jsonl.Write(row)
		jsonl.WriteByte('\n')
	}
	relationWriteFile(t, projectRoot, conceptsRelationCachePath, []byte(jsonl.String()))
	idMap := map[string]any{
		"concept": map[string]string{relationAlphaID: "alpha", relationBetaID: "beta", relationLonelyID: "lonely"},
		"source":  map[string]string{relationSourceOneID: "source-one", relationSourceTwoID: "source-two"},
		"source_meta": map[string]any{
			relationSourceOneID: map[string]string{"slug": "source-one", "title": "Source One", "source_file": "raw/source-one.md"},
			relationSourceTwoID: map[string]string{"slug": "source-two", "title": "Source Two", "source_file": "raw/source-two.md"},
		},
		"redirects":    map[string][]string{},
		"id_redirects": map[string]string{},
	}
	encodedIDMap, err := json.Marshal(idMap)
	if err != nil {
		t.Fatal(err)
	}
	relationWriteFile(t, projectRoot, sourcesRelationCachePath, encodedIDMap)
	for _, page := range []struct{ slug, id, title, raw string }{
		{"alpha", relationAlphaID, "Alpha", "source-one"},
		{"beta", relationBetaID, "Beta", "source-one"},
		{"lonely", relationLonelyID, "Lonely", ""},
	} {
		matter := fmt.Sprintf("---\nid: %s\ntitle: %s\nstatus: published\n", page.id, page.title)
		if page.raw != "" {
			matter += "sources:\n  - " + page.raw + "\n"
		}
		matter += "---\n" + page.title + " body\n"
		relationWriteFile(t, projectRoot, "wiki/"+page.slug+".md", []byte(matter))
	}
	for _, page := range []struct{ slug, id, title, raw string }{
		{"source-one", relationSourceOneID, "Source One", "raw/source-one.md"},
		{"source-two", relationSourceTwoID, "Source Two", "raw/source-two.md"},
	} {
		matter := fmt.Sprintf("---\nid: %s\ntitle: %s\nsource_file: %s\n---\n%s body\n", page.id, page.title, page.raw, page.title)
		relationWriteFile(t, projectRoot, "wiki/sources/"+page.slug+".md", []byte(matter))
		relationWriteFile(t, projectRoot, page.raw, []byte(page.title+" raw\n"))
	}
}

func relationWriteFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newRelationHandler(t *testing.T, token string, stats *relationTestStats) (*Handler, *relationTestRoot, string) {
	t.Helper()
	rootDir := t.TempDir()
	writeRelationFixture(t, rootDir)
	root := &relationTestRoot{RootStore: localfs.New(rootDir), stats: stats, token: token}
	return New(root, nil, search.NewIndex(), nil, nil, nil), root, rootDir
}

func invokeRelationDetail(h *Handler, kind, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/"+kind+"/"+path, nil)
	c.Params = gin.Params{{Key: "id", Value: path}}
	c.Set("userID", relationTestUser)
	c.Set("projectID", relationTestProject)
	if kind == "concepts" {
		h.GetConcept(c)
	} else {
		h.GetSource(c)
	}
	return recorder
}

func rawRelationSlugs(t *testing.T, body []byte, field string) []string {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response JSON: %v; body=%s", err, body)
	}
	raw, ok := response[field]
	if !ok {
		t.Fatalf("response omitted %q: %s", field, body)
	}
	var refs []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatalf("decode %s: %v; raw=%s", field, err, raw)
	}
	if len(refs) == 0 && strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty successful %s value=%s, want []", field, raw)
	}
	slugs := make([]string, 0, len(refs))
	for _, ref := range refs {
		slugs = append(slugs, ref.Slug)
	}
	return slugs
}

func rawRelationTitles(t *testing.T, body []byte, field string) []string {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	var refs []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(response[field], &refs); err != nil {
		t.Fatalf("decode %s titles: %v", field, err)
	}
	titles := make([]string, 0, len(refs))
	for _, ref := range refs {
		titles = append(titles, ref.Title)
	}
	return titles
}

func assertRelationSlugs(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("relation slugs = %v, want %v", got, want)
	}
}

func TestDetailHandlersUsePinnedIndexesAndEmitThreeRelationStates(t *testing.T) {
	stats := newRelationTestStats()
	h, _, _ := newRelationHandler(t, relationTestViewToken, stats)

	concept := invokeRelationDetail(h, "concepts", relationAlphaID+"-alpha")
	if concept.Code != http.StatusOK {
		t.Fatalf("canonical concept status=%d body=%s", concept.Code, concept.Body.String())
	}
	assertRelationSlugs(t, rawRelationSlugs(t, concept.Body.Bytes(), "related_concepts"), []string{"beta"})
	assertRelationSlugs(t, rawRelationSlugs(t, concept.Body.Bytes(), "related_sources"), []string{"source-one", "source-two"})
	assertRelationSlugs(t, rawRelationTitles(t, concept.Body.Bytes(), "related_sources"), []string{"Source One", "Source Two"})

	source := invokeRelationDetail(h, "sources", relationSourceOneID+"-source-one")
	if source.Code != http.StatusOK {
		t.Fatalf("canonical source status=%d body=%s", source.Code, source.Body.String())
	}
	assertRelationSlugs(t, rawRelationSlugs(t, source.Body.Bytes(), "related_concepts"), []string{"alpha", "beta"})
	assertRelationSlugs(t, rawRelationSlugs(t, source.Body.Bytes(), "related_sources"), []string{"source-two"})

	empty := invokeRelationDetail(h, "concepts", "lonely")
	if empty.Code != http.StatusOK {
		t.Fatalf("unrelated concept status=%d body=%s", empty.Code, empty.Body.String())
	}
	assertRelationSlugs(t, rawRelationSlugs(t, empty.Body.Bytes(), "related_concepts"), []string{})
	assertRelationSlugs(t, rawRelationSlugs(t, empty.Body.Bytes(), "related_sources"), []string{})

	stats.mu.Lock()
	defer stats.mu.Unlock()
	if stats.conceptLists != 0 || stats.sourceLists != 0 {
		t.Fatalf("full library scans concept=%d source=%d", stats.conceptLists, stats.sourceLists)
	}
	if stats.boundedCalls[conceptsRelationCachePath] != 1 || stats.boundedCalls[sourcesRelationCachePath] != 1 {
		t.Fatalf("immutable view index reads = concepts:%d sources:%d", stats.boundedCalls[conceptsRelationCachePath], stats.boundedCalls[sourcesRelationCachePath])
	}
	if stats.boundedLimits[conceptsRelationCachePath] != conceptsRelationMaxBytes || stats.boundedLimits[sourcesRelationCachePath] != sourcesRelationMaxBytes {
		t.Fatalf("index read limits concepts=%d sources=%d", stats.boundedLimits[conceptsRelationCachePath], stats.boundedLimits[sourcesRelationCachePath])
	}
}

func TestDetailRelationIndexFailureKeepsBodyAndOmitsBothFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		data []byte
	}{
		{name: "missing", err: store.ErrObjectNotExist},
		{name: "malformed", data: []byte("not-json\n")},
		{name: "oversized", data: []byte(strings.Repeat(" ", int(conceptsRelationMaxBytes+1)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := newRelationTestStats()
			h, _, _ := newRelationHandler(t, relationTestViewToken, stats)
			if tc.err != nil {
				stats.readFailures[conceptsRelationCachePath] = tc.err
			} else {
				stats.readOverrides[conceptsRelationCachePath] = tc.data
			}
			for _, route := range []struct{ kind, id string }{
				{"concepts", relationAlphaID + "-alpha"},
				{"sources", relationSourceOneID + "-source-one"},
			} {
				response := invokeRelationDetail(h, route.kind, route.id)
				if response.Code != http.StatusOK {
					t.Fatalf("%s response status=%d body=%s", route.kind, response.Code, response.Body.String())
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if _, ok := payload["body"]; !ok {
					t.Fatalf("%s response lost body: %s", route.kind, response.Body.String())
				}
				if _, ok := payload["related_concepts"]; ok {
					t.Fatalf("%s response claims available concepts: %s", route.kind, response.Body.String())
				}
				if _, ok := payload["related_sources"]; ok {
					t.Fatalf("%s response claims available sources: %s", route.kind, response.Body.String())
				}
			}
			stats.mu.Lock()
			defer stats.mu.Unlock()
			if stats.conceptLists != 0 || stats.sourceLists != 0 {
				t.Fatalf("failure triggered library scan: concept=%d source=%d", stats.conceptLists, stats.sourceLists)
			}
		})
	}
}

func TestSourceDetailIncompleteSourceMetadataDegradesWithoutChangingBody(t *testing.T) {
	stats := newRelationTestStats()
	h, _, _ := newRelationHandler(t, relationTestViewToken, stats)
	stats.readOverrides[sourcesRelationCachePath] = []byte(`{"source":{"` + relationSourceOneID + `":"source-one","` + relationSourceTwoID + `":"source-two"}}`)
	response := invokeRelationDetail(h, "sources", relationSourceOneID+"-source-one")
	if response.Code != http.StatusOK {
		t.Fatalf("source status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["body"]; !ok {
		t.Fatalf("source body missing: %s", response.Body.String())
	}
	if _, ok := payload["related_concepts"]; ok {
		t.Fatalf("incomplete source metadata claimed relationships: %s", response.Body.String())
	}
	if _, ok := payload["related_sources"]; ok {
		t.Fatalf("incomplete source metadata claimed relationships: %s", response.Body.String())
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if stats.conceptLists != 0 || stats.sourceLists != 0 {
		t.Fatalf("incomplete source metadata triggered full scans: concepts=%d sources=%d", stats.conceptLists, stats.sourceLists)
	}
}

func TestDetailRelationGenerationReadFailureRemainsFatal(t *testing.T) {
	stats := newRelationTestStats()
	h, _, _ := newRelationHandler(t, relationTestViewToken, stats)
	stats.readFailures[conceptsRelationCachePath] = store.ErrDeclaredObjectUnavailable
	response := invokeRelationDetail(h, "concepts", relationAlphaID+"-alpha")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("generation read failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoadRelationSnapshotAcceptsConceptIndexesAtAndBelowBudget(t *testing.T) {
	for _, size := range []int{int(conceptsRelationMaxBytes - 1), int(conceptsRelationMaxBytes)} {
		t.Run(fmt.Sprintf("exact_%d", size), func(t *testing.T) {
			stats := newRelationTestStats()
			_, _, rootDir := newRelationHandler(t, relationTestViewToken, stats)
			prefix := []byte(`{"slug":"alpha","frontmatter":{"id":"` + relationAlphaID + `"}}` + "\n")
			if len(prefix) > size {
				t.Fatal("fixture prefix exceeds limit")
			}
			stats.readOverrides[conceptsRelationCachePath] = append(prefix, []byte(strings.Repeat(" ", size-len(prefix)))...)
			reader := &relationTestStore{Store: localfs.New(rootDir).WithScope(relationTestUser, relationTestProject), stats: stats, token: "legacy"}
			snapshot, reason, err := loadRelationSnapshot(context.Background(), reader)
			if err != nil || reason != "" || snapshot == nil || len(snapshot.concepts) != 1 {
				t.Fatalf("boundary load snapshot=%v reason=%q err=%v", snapshot, reason, err)
			}
			if stats.boundedLimits[conceptsRelationCachePath] != conceptsRelationMaxBytes {
				t.Fatalf("bounded limit=%d want=%d", stats.boundedLimits[conceptsRelationCachePath], conceptsRelationMaxBytes)
			}
		})
	}
}

func TestRelationSnapshotCacheIsolatesProjectAndGenerationAndSkipsLegacyReuse(t *testing.T) {
	stats := newRelationTestStats()
	rootDir := t.TempDir()
	writeRelationFixture(t, rootDir)
	writeRelationFixtureFor(t, rootDir, relationTestUser, "other-project")
	h := New(localfs.New(rootDir), nil, search.NewIndex(), nil, nil, nil)
	base := localfs.New(rootDir)
	alphaIndex := []byte(`{"slug":"alpha","title":"Generation A","frontmatter":{"id":"` + relationAlphaID + `"}}` + "\n")
	betaIndex := []byte(`{"slug":"beta","title":"Generation B","frontmatter":{"id":"` + relationBetaID + `"}}` + "\n")
	alpha := &relationTestStore{Store: base.WithScope(relationTestUser, relationTestProject), stats: stats, token: "generation-a", dataOverrides: map[string][]byte{conceptsRelationCachePath: alphaIndex}}
	beta := &relationTestStore{Store: base.WithScope(relationTestUser, relationTestProject), stats: stats, token: "generation-b", dataOverrides: map[string][]byte{conceptsRelationCachePath: betaIndex}}
	otherProject := &relationTestStore{Store: base.WithScope(relationTestUser, "other-project"), stats: stats, token: "generation-a", dataOverrides: map[string][]byte{conceptsRelationCachePath: betaIndex}}
	for _, reader := range []*relationTestStore{alpha, beta, alpha, otherProject} {
		snapshot, reason, err := h.relationSnapshot(context.Background(), reader)
		if err != nil || reason != "" || snapshot == nil {
			t.Fatalf("relationSnapshot = %v, %q, %v", snapshot, reason, err)
		}
		if snapshot.releaseTransient != nil {
			snapshot.releaseTransient()
		}
	}
	if got := stats.boundedCalls[conceptsRelationCachePath]; got != 3 {
		t.Fatalf("concept index reads for A/B/A/other-project=%d, want 3", got)
	}
	if got := stats.boundedCalls[sourcesRelationCachePath]; got != 3 {
		t.Fatalf("source map reads for A/B/A/other-project=%d, want 3", got)
	}
	legacy := &relationTestStore{Store: base.WithScope(relationTestUser, relationTestProject), stats: stats, token: "legacy", dataOverrides: map[string][]byte{conceptsRelationCachePath: alphaIndex}}
	for range 2 {
		snapshot, reason, err := h.relationSnapshot(context.Background(), legacy)
		if err != nil || reason != "" || snapshot == nil {
			t.Fatalf("legacy relation load reason=%q err=%v", reason, err)
		}
		snapshot.releaseTransient()
	}
	if got := stats.boundedCalls[conceptsRelationCachePath]; got != 5 {
		t.Fatalf("legacy index reads=%d, want uncached reads at each request", got)
	}
}

func TestRelationMetadataCacheSharesInflightAndRejectsOtherColdLoadAtCapacity(t *testing.T) {
	cache := &relationMetadataCache{}
	keyA := relationCacheKey{scope: "scope-a", view: "view-a"}
	keyB := relationCacheKey{scope: "scope-b", view: "view-b"}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	loader := func(context.Context) (*relationSnapshot, string, error) {
		calls++
		close(entered)
		<-release
		return buildRelationSnapshot([]store.WikiPage{{Slug: "alpha", Title: "Alpha"}}, nil), "", nil
	}
	type cacheResult struct {
		value  *relationSnapshot
		reason string
		err    error
	}
	first := make(chan cacheResult, 1)
	go func() {
		value, reason, err := cache.get(context.Background(), keyA, loader)
		first <- cacheResult{value: value, reason: reason, err: err}
	}()
	<-entered
	joined := make(chan cacheResult, 1)
	go func() {
		value, reason, err := cache.get(context.Background(), keyA, func(context.Context) (*relationSnapshot, string, error) {
			calls++
			return nil, "unexpected_second_loader", nil
		})
		joined <- cacheResult{value: value, reason: reason, err: err}
	}()
	if value, reason, err := cache.get(context.Background(), keyB, func(context.Context) (*relationSnapshot, string, error) {
		t.Fatal("saturated cache started another loader")
		return nil, "", nil
	}); value != nil || reason != "load_saturated" || err != nil {
		t.Fatalf("saturation result = %v, %q, %v", value, reason, err)
	}
	close(release)
	firstResult, joinedResult := <-first, <-joined
	if firstResult.err != nil || joinedResult.err != nil || firstResult.value == nil || joinedResult.value != firstResult.value {
		t.Fatalf("singleflight results first=%+v joined=%+v", firstResult, joinedResult)
	}
	if calls != 1 {
		t.Fatalf("loader calls=%d, want one", calls)
	}
	entries, bytes, inflight := cache.limits()
	if entries != 1 || bytes <= 0 || bytes > maxRelationCacheBytes || inflight != 0 {
		t.Fatalf("cache limits entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
}

func TestRelationMetadataCacheCancellationReleasesFlightAndRecovers(t *testing.T) {
	cache := &relationMetadataCache{}
	key := relationCacheKey{scope: "cancel-scope", view: "cancel-view"}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := cache.get(ctx, key, func(loadCtx context.Context) (*relationSnapshot, string, error) {
			close(entered)
			<-loadCtx.Done()
			return nil, "", loadCtx.Err()
		})
		result <- err
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load err=%v", err)
	}
	if _, _, inflight := cache.limits(); inflight != 0 {
		t.Fatalf("cancelled flight retained: inflight=%d", inflight)
	}
	value, reason, err := cache.get(context.Background(), key, func(context.Context) (*relationSnapshot, string, error) {
		return buildRelationSnapshot([]store.WikiPage{{Slug: "recovered"}}, nil), "", nil
	})
	if err != nil || reason != "" || value == nil || value.concepts[0].slug != "recovered" {
		t.Fatalf("recovery value=%v reason=%q err=%v", value, reason, err)
	}
}

func TestRelationSnapshotAdmissionCoversLegacyAndImmutableBoundedReaders(t *testing.T) {
	for _, firstIsLegacy := range []bool{true, false} {
		name := "immutable-first"
		if firstIsLegacy {
			name = "legacy-first"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRelationFixture(t, root)
			h := New(localfs.New(root), nil, search.NewIndex(), nil, nil, nil)
			probe := &relationReadProbe{started: make(chan struct{}), release: make(chan struct{})}
			base := localfs.New(root).WithScope(relationTestUser, relationTestProject)
			legacy := &relationProbedStore{Store: base, token: "legacy", probe: probe}
			immutable := &relationProbedStore{Store: base, token: "manifest-admission-test", probe: probe}
			first, second := store.Store(legacy), store.Store(immutable)
			if firstIsLegacy {
				legacy.block = true
			} else {
				immutable.block = true
				first, second = immutable, legacy
			}

			firstCtx, cancelFirst := context.WithCancel(context.Background())
			type snapshotResult struct {
				value  *relationSnapshot
				reason string
				err    error
			}
			firstResult := make(chan snapshotResult, 1)
			go func() {
				value, reason, err := h.relationSnapshot(firstCtx, first)
				firstResult <- snapshotResult{value: value, reason: reason, err: err}
			}()
			select {
			case <-probe.started:
			case <-time.After(3 * time.Second):
				cancelFirst()
				t.Fatal("first detail load never reached its bounded reader")
			}

			if snapshot, reason, err := h.relationSnapshot(context.Background(), second); snapshot != nil || reason != "load_saturated" || err != nil {
				t.Fatalf("concurrent second view snapshot=%v reason=%q err=%v", snapshot, reason, err)
			}
			probe.mu.Lock()
			callsBeforeRelease, maxActiveBeforeRelease := probe.readCalls, probe.maxActive
			probe.mu.Unlock()
			if callsBeforeRelease != 1 || maxActiveBeforeRelease != 1 {
				t.Fatalf("bounded-reader seam calls=%d max-active=%d, want 1/1", callsBeforeRelease, maxActiveBeforeRelease)
			}
			if _, _, inflight := h.relationMetadata.limits(); inflight != 1 {
				t.Fatalf("blocked actual loader inflight=%d want=1", inflight)
			}

			close(probe.release)
			firstLoaded := <-firstResult
			if firstLoaded.err != nil || firstLoaded.reason != "" || firstLoaded.value == nil {
				t.Fatalf("first bounded load snapshot=%v reason=%q err=%v", firstLoaded.value, firstLoaded.reason, firstLoaded.err)
			}
			cancelFirst()
			probe.mu.Lock()
			maxActive, active := probe.maxActive, probe.active
			probe.mu.Unlock()
			entries, _, inflight := h.relationMetadata.limits()
			if maxActive != 1 || active != 0 || inflight != 0 {
				t.Fatalf("after completion max-active=%d active=%d inflight=%d", maxActive, active, inflight)
			}
			wantEntries := 1
			if firstIsLegacy {
				wantEntries = 0
			}
			if entries != wantEntries {
				t.Fatalf("persistent entries=%d want=%d (legacy result must not be retained)", entries, wantEntries)
			}
			wantTransient := 0
			if firstIsLegacy {
				wantTransient = 1
			}
			if got := h.relationMetadata.transientCount(); got != wantTransient {
				t.Fatalf("live uncached snapshots=%d want=%d", got, wantTransient)
			}
			if firstIsLegacy {
				if snapshot, reason, err := h.relationSnapshot(context.Background(), second); snapshot != nil || reason != "load_saturated" || err != nil {
					t.Fatalf("request during live legacy snapshot snapshot=%v reason=%q err=%v", snapshot, reason, err)
				}
			}
			if firstLoaded.value.releaseTransient != nil {
				firstLoaded.value.releaseTransient()
			}
			if snapshot, reason, err := h.relationSnapshot(context.Background(), second); err != nil || reason != "" || snapshot == nil {
				t.Fatalf("saturated view recovery snapshot=%v reason=%q err=%v", snapshot, reason, err)
			} else if snapshot.releaseTransient != nil {
				snapshot.releaseTransient()
			}
		})
	}
}

func TestRelationMetadataUncachedLoadUsesSnapshotBudgetWithoutRetention(t *testing.T) {
	cache := &relationMetadataCache{}
	largeTitle := strings.Repeat("m", int(maxRelationSnapshotBytes))
	value, reason, err := cache.loadUncached(context.Background(), func(context.Context) (*relationSnapshot, string, error) {
		return buildRelationSnapshot([]store.WikiPage{{Slug: "legacy-large", Title: largeTitle}}, nil), "", nil
	})
	if err != nil || value != nil || reason != "metadata_limit" {
		t.Fatalf("uncached snapshot budget value=%v reason=%q err=%v", value, reason, err)
	}
	entries, bytes, inflight := cache.limits()
	if entries != 0 || bytes != 0 || inflight != 0 {
		t.Fatalf("uncached oversized snapshot retained: entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
}

func TestRelationSnapshotLegacyCancellationReleasesLoaderAdmission(t *testing.T) {
	root := t.TempDir()
	writeRelationFixture(t, root)
	h := New(localfs.New(root), nil, search.NewIndex(), nil, nil, nil)
	probe := &relationReadProbe{started: make(chan struct{}), release: make(chan struct{})}
	reader := &relationProbedStore{
		Store: localfs.New(root).WithScope(relationTestUser, relationTestProject),
		token: "legacy", probe: probe, block: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := h.relationSnapshot(ctx, reader)
		result <- err
	}()
	select {
	case <-probe.started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("legacy detail load never reached its bounded reader")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled legacy load err=%v", err)
	}
	if entries, bytes, inflight := h.relationMetadata.limits(); entries != 0 || bytes != 0 || inflight != 0 {
		t.Fatalf("cancelled legacy loader retained resources: entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
	probe.mu.Lock()
	activeAfterCancel := probe.active
	probe.mu.Unlock()
	if activeAfterCancel != 0 {
		t.Fatalf("cancelled bounded reader remains active=%d", activeAfterCancel)
	}

	close(probe.release)
	if snapshot, reason, err := h.relationSnapshot(context.Background(), reader); err != nil || reason != "" || snapshot == nil {
		t.Fatalf("legacy recovery snapshot=%v reason=%q err=%v", snapshot, reason, err)
	} else if snapshot.releaseTransient != nil {
		snapshot.releaseTransient()
	}
	entries, bytes, inflight := h.relationMetadata.limits()
	if entries != 0 || bytes != 0 || inflight != 0 || h.relationMetadata.transientCount() != 0 {
		t.Fatalf("legacy recovery retained cache: entries=%d bytes=%d inflight=%d transient=%d", entries, bytes, inflight, h.relationMetadata.transientCount())
	}
}

func TestRelationMetadataCacheEvictsWithinEntryAndByteBudgets(t *testing.T) {
	cache := &relationMetadataCache{}
	loads := make(map[string]int)
	load := func(name, title string) {
		t.Helper()
		key := relationCacheKey{scope: "budget", view: name}
		_, reason, err := cache.get(context.Background(), key, func(context.Context) (*relationSnapshot, string, error) {
			loads[name]++
			return buildRelationSnapshot([]store.WikiPage{{Slug: name, Title: title}}, nil), "", nil
		})
		if err != nil || reason != "" {
			t.Fatalf("cache load %s reason=%q err=%v", name, reason, err)
		}
	}
	for i := range maxRelationCacheEntries + 2 {
		load(fmt.Sprintf("small-%d", i), "small")
	}
	entries, bytes, inflight := cache.limits()
	if entries > maxRelationCacheEntries || bytes > maxRelationCacheBytes || inflight != 0 {
		t.Fatalf("entry cache limits entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
	load("small-0", "small")
	if loads["small-0"] != 2 {
		t.Fatalf("oldest entry load count=%d, want eviction and reload", loads["small-0"])
	}

	largeCache := &relationMetadataCache{}
	largeTitleSize := 5 << 20
	for i := range maxRelationCacheEntries {
		name := fmt.Sprintf("large-%d", i)
		text := strings.Repeat(string(rune('a'+i)), largeTitleSize)
		key := relationCacheKey{scope: "large", view: name}
		_, reason, err := largeCache.get(context.Background(), key, func(context.Context) (*relationSnapshot, string, error) {
			return buildRelationSnapshot([]store.WikiPage{{Slug: name, Title: text}}, nil), "", nil
		})
		if err != nil || reason != "" {
			t.Fatalf("large cache load %s reason=%q err=%v", name, reason, err)
		}
	}
	entries, bytes, inflight = largeCache.limits()
	if entries >= maxRelationCacheEntries || bytes > maxRelationCacheBytes || inflight != 0 {
		t.Fatalf("byte cache limits entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
	oversizedCache := &relationMetadataCache{}
	tooLargeTitle := strings.Repeat("z", int(maxRelationSnapshotBytes))
	value, reason, err := oversizedCache.get(context.Background(), relationCacheKey{scope: "large", view: "single"}, func(context.Context) (*relationSnapshot, string, error) {
		return buildRelationSnapshot([]store.WikiPage{{Slug: "too-large", Title: tooLargeTitle}}, nil), "", nil
	})
	if err != nil || value != nil || reason != "metadata_limit" {
		t.Fatalf("per-snapshot metadata cap value=%v reason=%q err=%v", value, reason, err)
	}
	if entries, bytes, inflight := oversizedCache.limits(); entries != 0 || bytes != 0 || inflight != 0 {
		t.Fatalf("oversized snapshot retained: entries=%d bytes=%d inflight=%d", entries, bytes, inflight)
	}
}

func TestRelationOperationsStayBoundedFor478ConceptFixture(t *testing.T) {
	stats := newRelationTestStats()
	h, _, rootDir := newRelationHandler(t, relationTestViewToken, stats)
	var rows strings.Builder
	conceptIDMap := map[string]string{relationAlphaID: "alpha"}
	for i := 0; i < 478; i++ {
		slug := fmt.Sprintf("concept-%03d", i)
		id := fmt.Sprintf("concept-%03d-id", i)
		if i == 0 {
			slug, id = "alpha", relationAlphaID
		}
		conceptIDMap[id] = slug
		row := map[string]any{"slug": slug, "title": "Title " + slug, "frontmatter": map[string]any{"id": id, "sources": []string{"source-one"}}, "body": "synthetic"}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		rows.Write(encoded)
		rows.WriteByte('\n')
		if i > 0 {
			matter := fmt.Sprintf("---\nid: %s\ntitle: Title %s\nstatus: published\nsources:\n  - source-one\n---\nsynthetic\n", id, slug)
			relationWriteFile(t, filepath.Join(rootDir, "users", relationTestUser, "projects", relationTestProject), "wiki/"+slug+".md", []byte(matter))
		}
	}
	stats.readOverrides[conceptsRelationCachePath] = []byte(rows.String())
	mapPath := filepath.Join(rootDir, "users", relationTestUser, "projects", relationTestProject, sourcesRelationCachePath)
	idMapData, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	var idMap map[string]json.RawMessage
	if err := json.Unmarshal(idMapData, &idMap); err != nil {
		t.Fatal(err)
	}
	conceptIDs, err := json.Marshal(conceptIDMap)
	if err != nil {
		t.Fatal(err)
	}
	idMap["concept"] = conceptIDs
	idMapData, err = json.Marshal(idMap)
	if err != nil {
		t.Fatal(err)
	}
	relationWriteFile(t, filepath.Join(rootDir, "users", relationTestUser, "projects", relationTestProject), sourcesRelationCachePath, idMapData)

	concept := invokeRelationDetail(h, "concepts", "alpha")
	source := invokeRelationDetail(h, "sources", relationSourceOneID+"-source-one")
	if concept.Code != http.StatusOK || source.Code != http.StatusOK {
		t.Fatalf("synthetic detail statuses concept=%d source=%d", concept.Code, source.Code)
	}
	if got := len(rawRelationSlugs(t, source.Body.Bytes(), "related_concepts")); got != 478 {
		t.Fatalf("source related concept count=%d want=478", got)
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if stats.conceptLists != 0 || stats.sourceLists != 0 {
		t.Fatalf("478-page detail scanned concepts=%d sources=%d", stats.conceptLists, stats.sourceLists)
	}
	if stats.boundedCalls[conceptsRelationCachePath] != 1 || stats.boundedCalls[sourcesRelationCachePath] != 1 {
		t.Fatalf("478-page index operations concepts=%d sources=%d; want one each", stats.boundedCalls[conceptsRelationCachePath], stats.boundedCalls[sourcesRelationCachePath])
	}
	if stats.pageReads != 2 {
		t.Fatalf("478-page detail markdown reads=%d, want only two targets", stats.pageReads)
	}
	t.Logf("synthetic_concepts=478 detail_requests=2 target_markdown_reads=%d concept_index_reads=%d source_map_reads=%d full_concept_scans=%d full_source_scans=%d", stats.pageReads, stats.boundedCalls[conceptsRelationCachePath], stats.boundedCalls[sourcesRelationCachePath], stats.conceptLists, stats.sourceLists)
}
