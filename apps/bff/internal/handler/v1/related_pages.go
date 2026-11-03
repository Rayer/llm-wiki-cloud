package v1

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	conceptsRelationCachePath = "cache/concepts.jsonl"
	sourcesRelationCachePath  = "cache/id_map.json"
	conceptsRelationMaxBytes  = int64(10 << 20)
	sourcesRelationMaxBytes   = int64(generation.MaxFileBytes)
	maxRelationCacheEntries   = 8
	maxRelationCacheBytes     = int64(64 << 20)
	maxRelationSnapshotBytes  = int64(16 << 20)
	maxRelationColdLoads      = 1
)

type relationConcept struct {
	id      string
	slug    string
	title   string
	sources []string
}

type relationSource struct {
	id      string
	slug    string
	title   string
	path    string
	rawPath string
}

type relationSnapshot struct {
	concepts         []relationConcept
	sources          []relationSource
	sourceByKey      map[string][]int
	conceptSourceIDs [][]int
	retainedBytes    int64
	releaseTransient func()
}

type wikiRelationResult struct {
	relatedConcepts []handler.WikiReference
	relatedSources  []handler.WikiReference
	available       bool
}

func (result wikiRelationResult) responseFields() (*[]handler.WikiReference, *[]handler.WikiReference) {
	if !result.available {
		return nil, nil
	}
	return &result.relatedConcepts, &result.relatedSources
}

type relationCacheKey struct {
	scope string
	view  string
}

type relationCacheEntry struct {
	value *relationSnapshot
	bytes int64
	elem  *list.Element
}

type relationCacheFlight struct {
	done   chan struct{}
	value  *relationSnapshot
	reason string
	err    error
}

type relationMetadataCache struct {
	mu        sync.Mutex
	entries   map[relationCacheKey]*relationCacheEntry
	order     list.List
	flights   map[relationCacheKey]*relationCacheFlight
	inflight  int
	transient int
	usedBytes int64
}

func relationCacheKeyFor(reader store.Store) (relationCacheKey, bool) {
	view, ok := reader.(store.ViewToken)
	if !ok {
		return relationCacheKey{}, false
	}
	token := view.ViewToken()
	scope := reader.Prefix()
	if token == "" || token == "legacy" || scope == "" {
		return relationCacheKey{}, false
	}
	return relationCacheKey{scope: scope, view: token}, true
}

func (cache *relationMetadataCache) get(ctx context.Context, key relationCacheKey, load func(context.Context) (*relationSnapshot, string, error)) (*relationSnapshot, string, error) {
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = make(map[relationCacheKey]*relationCacheEntry)
	}
	if cache.flights == nil {
		cache.flights = make(map[relationCacheKey]*relationCacheFlight)
	}
	if entry, ok := cache.entries[key]; ok {
		cache.order.MoveToFront(entry.elem)
		value := entry.value
		cache.mu.Unlock()
		return value, "", nil
	}
	if flight, ok := cache.flights[key]; ok {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-flight.done:
			return flight.value, flight.reason, flight.err
		}
	}
	if cache.inflight >= maxRelationColdLoads || cache.transient > 0 {
		cache.mu.Unlock()
		return nil, "load_saturated", nil
	}
	flight := &relationCacheFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.inflight++
	cache.mu.Unlock()

	value, reason, err := loadRelationSnapshotWithinBudget(ctx, load)

	cache.mu.Lock()
	delete(cache.flights, key)
	cache.inflight--
	if err == nil && reason == "" && value != nil {
		entryBytes := value.retainedBytes + int64(len(key.scope)+len(key.view)) + 128
		for cache.order.Len() > 0 && (cache.order.Len() >= maxRelationCacheEntries || cache.usedBytes+entryBytes > maxRelationCacheBytes) {
			cache.evictOldestLocked()
		}
		if entryBytes <= maxRelationCacheBytes {
			entry := &relationCacheEntry{value: value, bytes: entryBytes}
			entry.elem = cache.order.PushFront(key)
			cache.entries[key] = entry
			cache.usedBytes += entryBytes
		}
	}
	flight.value, flight.reason, flight.err = value, reason, err
	close(flight.done)
	cache.mu.Unlock()
	return value, reason, err
}

// loadUncached uses the same global admission and per-snapshot weight check as
// cacheable immutable views, but never retains the result after this request.
func (cache *relationMetadataCache) loadUncached(ctx context.Context, load func(context.Context) (*relationSnapshot, string, error)) (*relationSnapshot, string, error) {
	cache.mu.Lock()
	if cache.inflight >= maxRelationColdLoads || cache.transient > 0 {
		cache.mu.Unlock()
		return nil, "load_saturated", nil
	}
	cache.inflight++
	cache.mu.Unlock()

	value, reason, err := loadRelationSnapshotWithinBudget(ctx, load)
	cache.mu.Lock()
	cache.inflight--
	if err == nil && reason == "" && value != nil {
		cache.transient++
		var releaseOnce sync.Once
		value.releaseTransient = func() {
			releaseOnce.Do(func() {
				cache.mu.Lock()
				cache.transient--
				cache.mu.Unlock()
			})
		}
	}
	cache.mu.Unlock()
	return value, reason, err
}

func loadRelationSnapshotWithinBudget(ctx context.Context, load func(context.Context) (*relationSnapshot, string, error)) (*relationSnapshot, string, error) {
	value, reason, err := load(ctx)
	if err == nil && reason == "" && value == nil {
		reason = "metadata_unavailable"
	}
	if err == nil && reason == "" && value != nil {
		value.retainedBytes = relationSnapshotRetainedBytes(value)
		if value.retainedBytes > maxRelationSnapshotBytes {
			value = nil
			reason = "metadata_limit"
		}
	}
	return value, reason, err
}

func (cache *relationMetadataCache) evictOldestLocked() {
	elem := cache.order.Back()
	if elem == nil {
		return
	}
	key := elem.Value.(relationCacheKey)
	entry := cache.entries[key]
	delete(cache.entries, key)
	cache.order.Remove(elem)
	if entry != nil {
		cache.usedBytes -= entry.bytes
	}
}

func (cache *relationMetadataCache) limits() (entries int, bytes int64, inflight int) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.order.Len(), cache.usedBytes, cache.inflight
}

func (cache *relationMetadataCache) transientCount() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.transient
}

// relationSnapshotRetainedBytes conservatively accounts for struct, string,
// slice, map, and index storage; the cache uses it as a fixed byte budget.
func relationSnapshotRetainedBytes(snapshot *relationSnapshot) int64 {
	bytes := int64(unsafe.Sizeof(*snapshot)) + 256
	for _, concept := range snapshot.concepts {
		bytes += int64(unsafe.Sizeof(concept)) + 96
		bytes += 2 * int64(len(concept.id)+len(concept.slug)+len(concept.title))
		bytes += int64(cap(concept.sources)) * 48
		for _, source := range concept.sources {
			bytes += 2 * int64(len(source))
		}
	}
	for _, source := range snapshot.sources {
		bytes += int64(unsafe.Sizeof(source)) + 128
		bytes += 2 * int64(len(source.id)+len(source.slug)+len(source.title)+len(source.path)+len(source.rawPath))
	}
	for key, indices := range snapshot.sourceByKey {
		bytes += 128 + 2*int64(len(key)) + int64(cap(indices))*24
	}
	bytes += int64(cap(snapshot.conceptSourceIDs)) * 24
	for _, indices := range snapshot.conceptSourceIDs {
		bytes += int64(cap(indices)) * 24
	}
	return bytes
}

func (h *Handler) relationSnapshot(ctx context.Context, reader store.Store) (*relationSnapshot, string, error) {
	key, cacheable := relationCacheKeyFor(reader)
	load := func(loadCtx context.Context) (*relationSnapshot, string, error) {
		return loadRelationSnapshot(loadCtx, reader)
	}
	if !cacheable {
		return h.relationMetadata.loadUncached(ctx, load)
	}
	return h.relationMetadata.get(ctx, key, load)
}

func loadRelationSnapshot(ctx context.Context, reader store.Store) (*relationSnapshot, string, error) {
	bounded, ok := reader.(store.BoundedFileReader)
	if !ok {
		return nil, "bounded_reader_unavailable", nil
	}
	conceptData, err := bounded.ReadFileBounded(ctx, conceptsRelationCachePath, conceptsRelationMaxBytes)
	if err != nil {
		if reason := optionalRelationReadFailure(err); reason != "" {
			return nil, reason, nil
		}
		return nil, "", err
	}
	if int64(len(conceptData)) > conceptsRelationMaxBytes {
		return nil, "oversized_index", nil
	}
	concepts, err := gcs.WikiPagesFromConceptsJSONL(conceptData)
	if err != nil {
		return nil, "malformed_index", nil
	}
	sourceData, err := bounded.ReadFileBounded(ctx, sourcesRelationCachePath, sourcesRelationMaxBytes)
	if err != nil {
		if reason := optionalRelationReadFailure(err); reason != "" {
			return nil, reason, nil
		}
		return nil, "", err
	}
	if int64(len(sourceData)) > sourcesRelationMaxBytes {
		return nil, "oversized_index", nil
	}
	sources, err := gcs.WikiPagesFromSourceIDMapForDetailRelations(sourceData)
	if err != nil {
		return nil, "malformed_index", nil
	}
	return buildRelationSnapshot(concepts, sources), "", nil
}

func optionalRelationReadFailure(err error) string {
	switch {
	case errors.Is(err, store.ErrReadLimitExceeded):
		return "oversized_index"
	case errors.Is(err, store.ErrObjectNotExist):
		return "missing_index"
	default:
		return ""
	}
}

func buildRelationSnapshot(concepts, sources []store.WikiPage) *relationSnapshot {
	sort.Slice(concepts, func(i, j int) bool { return concepts[i].Slug < concepts[j].Slug })
	sort.Slice(sources, func(i, j int) bool { return sources[i].Slug < sources[j].Slug })
	snapshot := &relationSnapshot{
		concepts:         make([]relationConcept, len(concepts)),
		sources:          make([]relationSource, len(sources)),
		sourceByKey:      make(map[string][]int),
		conceptSourceIDs: make([][]int, len(concepts)),
	}
	for i, source := range sources {
		snapshot.sources[i] = relationSource{id: source.ID, slug: source.Slug, title: source.Title, path: source.Path, rawPath: source.RawPath}
		for _, key := range relationSourceKeys(snapshot.sources[i]) {
			snapshot.sourceByKey[key] = append(snapshot.sourceByKey[key], i)
		}
	}
	for i, concept := range concepts {
		snapshot.concepts[i] = relationConcept{id: concept.ID, slug: concept.Slug, title: concept.Title, sources: concept.Sources}
		for _, raw := range concept.Sources {
			if index, ok := resolveRelationSource(snapshot.sourceByKey, raw); ok && !containsInt(snapshot.conceptSourceIDs[i], index) {
				snapshot.conceptSourceIDs[i] = append(snapshot.conceptSourceIDs[i], index)
			}
		}
	}
	return snapshot
}

func (h *Handler) resolveWikiRelations(ctx context.Context, reader store.Store, currentType string, current store.WikiPage, frontmatter map[string]interface{}) (wikiRelationResult, error) {
	snapshot, reason, err := h.relationSnapshot(ctx, reader)
	if err != nil {
		return wikiRelationResult{}, err
	}
	if snapshot != nil && snapshot.releaseTransient != nil {
		defer snapshot.releaseTransient()
	}
	if reason != "" {
		slog.WarnContext(ctx, "detail relations unavailable", "reason", reason)
		return wikiRelationResult{}, nil
	}

	currentSources := make([]int, 0)
	if currentType == "concept" {
		references := current.Sources
		if len(references) == 0 {
			references = referenceStrings(frontmatter["sources"])
		}
		for _, raw := range references {
			if index, ok := resolveRelationSource(snapshot.sourceByKey, raw); ok && !containsInt(currentSources, index) {
				currentSources = append(currentSources, index)
			}
		}
	}
	currentSourceIndex := -1
	if currentType == "source" {
		for i, source := range snapshot.sources {
			if sameRelationIdentity(source.id, source.slug, current) {
				currentSourceIndex = i
				break
			}
		}
	}

	relatedConcepts := make([]handler.WikiReference, 0)
	relatedSources := make([]handler.WikiReference, 0)
	conceptSeen, sourceSeen := map[string]bool{}, map[string]bool{}
	for i, refs := range snapshot.conceptSourceIDs {
		matched := currentType == "source" && containsInt(refs, currentSourceIndex)
		if currentType == "concept" {
			for _, sourceIndex := range refs {
				if containsInt(currentSources, sourceIndex) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		concept := snapshot.concepts[i]
		if currentType == "source" && !sameRelationIdentity(concept.id, concept.slug, current) {
			addRelationReference(&relatedConcepts, conceptSeen, relationConceptReference(concept, "concept"))
		}
		for _, sourceIndex := range refs {
			if sourceIndex == currentSourceIndex || currentType == "concept" && containsInt(currentSources, sourceIndex) {
				continue
			}
			addRelationReference(&relatedSources, sourceSeen, relationSourceReference(snapshot.sources[sourceIndex], "source"))
		}
	}
	if currentType == "concept" {
		for i, sourcesForConcept := range snapshot.conceptSourceIDs {
			if sameRelationIdentity(snapshot.concepts[i].id, snapshot.concepts[i].slug, current) {
				continue
			}
			shared := false
			for _, sourceIndex := range sourcesForConcept {
				if containsInt(currentSources, sourceIndex) {
					shared = true
					break
				}
			}
			if shared {
				addRelationReference(&relatedConcepts, conceptSeen, relationConceptReference(snapshot.concepts[i], "concept"))
			}
		}
		for _, sourceIndex := range currentSources {
			addRelationReference(&relatedSources, sourceSeen, relationSourceReference(snapshot.sources[sourceIndex], "source"))
		}
	}
	sort.Slice(relatedConcepts, func(i, j int) bool { return relatedConcepts[i].Slug < relatedConcepts[j].Slug })
	sort.Slice(relatedSources, func(i, j int) bool { return relatedSources[i].Slug < relatedSources[j].Slug })
	return wikiRelationResult{relatedConcepts: relatedConcepts, relatedSources: relatedSources, available: true}, nil
}

func relationSourceKeys(source relationSource) []string {
	keys := []string{source.id, source.slug, source.path, source.rawPath}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			result = append(result, key)
		}
	}
	return result
}

func resolveRelationSource(lookup map[string][]int, raw string) (int, bool) {
	indices := lookup[strings.TrimSpace(raw)]
	if len(indices) != 1 {
		return 0, false
	}
	return indices[0], true
}

func referenceStrings(value any) []string {
	items, ok := value.([]interface{})
	if !ok {
		if values, typed := value.([]string); typed {
			return values
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func sameRelationIdentity(leftID, leftSlug string, right store.WikiPage) bool {
	if leftID != "" && right.ID != "" {
		return leftID == right.ID
	}
	return leftSlug != "" && leftSlug == right.Slug
}

func relationConceptReference(page relationConcept, kind string) handler.WikiReference {
	return handler.WikiReference{ID: strings.TrimSpace(page.id), Slug: strings.TrimSpace(page.slug), Title: strings.TrimSpace(page.title), Type: kind}
}

func relationSourceReference(page relationSource, kind string) handler.WikiReference {
	return handler.WikiReference{ID: strings.TrimSpace(page.id), Slug: strings.TrimSpace(page.slug), Title: strings.TrimSpace(page.title), Type: kind}
}

func addRelationReference(target *[]handler.WikiReference, seen map[string]bool, reference handler.WikiReference) {
	if reference.Slug == "" {
		return
	}
	key := reference.Type + "\x00" + reference.Slug
	if seen[key] {
		return
	}
	seen[key] = true
	*target = append(*target, reference)
}

func containsInt(values []int, target int) bool {
	if target < 0 {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
