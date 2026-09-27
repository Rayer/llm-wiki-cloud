// Package cache provides an in-memory, project-scoped cache of wiki concepts.
package cache

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	fm "github.com/adrg/frontmatter"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

// GCSPath is the persisted JSONL concept cache written by the pipeline.
const GCSPath = "cache/concepts.jsonl"

// Entry is the cached representation of a concept page.
type Entry struct {
	Slug        string                 `json:"slug"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	Frontmatter map[string]interface{} `json:"frontmatter"`
	Sources     []string               `json:"sources"`
}

// CanonicalEntry pairs the parsed concept body with its stable identity and
// digest of the exact concepts.jsonl row bytes, excluding the line ending.
type CanonicalEntry struct {
	Entry         Entry
	StableID      string
	ContentDigest string
}

// PinnedCanonicalSnapshot is a manifest-verified concepts.jsonl corpus view.
// ContentDigest on each entry uses the same raw-row bytes hashed by Profile Tag
// inventory input; Identity.ConceptsDigest covers the complete JSONL file.
type PinnedCanonicalSnapshot struct {
	Identity storage.QueryGenerationIdentity
	Entries  []CanonicalEntry
}

type conceptReader interface {
	ListConcepts(ctx context.Context, includeDrafts bool) ([]gcs.WikiPage, error)
	GetPage(ctx context.Context, slug, category string) (*gcs.WikiPage, []byte, error)
}

// Reader is the subset of the GCS client used to build a project-scoped concept cache.
type Reader interface {
	conceptReader
	Prefix() string
}

type projectCache struct {
	entries       []Entry
	bySlug        map[string]Entry
	canonicalData []byte
}

// Cache stores independent concept sets for each user/project GCS prefix.
type Cache struct {
	mu sync.RWMutex
	// ponytail: serialize strict snapshot cache misses globally; use per-key locks if loads contend.
	snapshotMu      sync.Mutex
	projects        map[string]projectCache
	pinnedSnapshots map[string]PinnedCanonicalSnapshot
	random          *rand.Rand
}

// New creates an empty concept cache.
func New() *Cache {
	return &Cache{
		projects:        make(map[string]projectCache),
		pinnedSnapshots: make(map[string]PinnedCanonicalSnapshot),
		random:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Build loads the project concept cache, preferring the persisted JSONL file.
// When JSONL is unavailable it falls back to reading each concept page from
// storage. Individual page failures are skipped unless every listed concept
// fails.
func (c *Cache) Build(ctx context.Context, reader conceptReader) ([]Entry, error) {
	if reader == nil {
		return nil, fmt.Errorf("concept cache reader is nil")
	}

	if err := c.loadJSONL(ctx, reader); err == nil {
		if project, ok := c.project(reader); ok {
			return cloneEntries(project.entries), nil
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}

	return c.buildFromPages(ctx, reader)
}

func (c *Cache) buildFromPages(ctx context.Context, reader conceptReader) ([]Entry, error) {
	concepts, err := reader.ListConcepts(ctx, false)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("list concepts: %w", err)
	}

	type buildResult struct {
		entry Entry
		err   error
	}
	results := make(chan buildResult, len(concepts))
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup

	for _, concept := range concepts {
		concept := concept
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			page, data, err := reader.GetPage(ctx, concept.Slug, "concepts")
			if err != nil {
				results <- buildResult{err: err}
				return
			}
			titleFallback := concept.Title
			if page != nil && page.Title != "" {
				titleFallback = page.Title
			}
			results <- buildResult{entry: parseEntry(concept.Slug, titleFallback, string(data))}
		}()
	}

	wg.Wait()
	close(results)

	entries := make([]Entry, 0, len(concepts))
	for result := range results {
		if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			return nil, result.err
		}
		if result.err == nil {
			entries = append(entries, result.entry)
		}
	}
	if len(concepts) > 0 && len(entries) == 0 {
		return nil, fmt.Errorf("failed to read any of %d concepts", len(concepts))
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Slug < entries[j].Slug
	})
	bySlug := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		bySlug[entry.Slug] = entry
	}

	c.mu.Lock()
	c.projects[prefixForReader(reader)] = projectCache{entries: entries, bySlug: bySlug}
	c.mu.Unlock()

	if writer, ok := reader.(interface {
		WriteBytes(context.Context, []byte, string) (string, error)
	}); ok {
		_, _ = writer.WriteBytes(ctx, marshalJSONL(entries), GCSPath)
	}

	return cloneEntries(entries), nil
}

// IsReady reports whether the cache has a populated entry for the given prefix.
func (c *Cache) IsReady(prefix string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	project, ok := c.projects[prefix]
	return ok && len(project.entries) > 0
}

// Prefixes returns all cached project prefixes.
func (c *Cache) Prefixes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	prefixes := make([]string, 0, len(c.projects))
	for prefix := range c.projects {
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// Search finds matching concepts in a project cache. The project is loaded from
// JSONL or built on first use with a 10-second timeout, then results are
// sampled without replacement using match score as the weight.
func (c *Cache) Search(ctx context.Context, reader conceptReader, query string, limit int) ([]search.Result, error) {
	project, ok := c.project(reader)
	if !ok {
		if err := c.loadJSONL(ctx, reader); err != nil {
			buildCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if _, err := c.Build(buildCtx, reader); err != nil {
				return nil, fmt.Errorf("concept cache build for %q: %w", prefixForReader(reader), err)
			}
		}
		project, _ = c.project(reader)
	}

	if limit <= 0 {
		limit = 10
	}
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return []search.Result{}, nil
	}

	type candidate struct {
		result search.Result
		weight int
	}
	candidates := make([]candidate, 0, len(project.entries))
	for _, entry := range project.entries {
		score := entryScore(entry, words)
		if score == 0 {
			continue
		}
		candidates = append(candidates, candidate{
			weight: score,
			result: search.Result{
				Slug:    entry.Slug,
				Title:   entry.Title,
				Type:    "concept",
				Snippet: snippet(entry, words),
			},
		})
	}

	if len(candidates) <= limit {
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].weight > candidates[j].weight
		})
		results := make([]search.Result, len(candidates))
		for i, candidate := range candidates {
			results[i] = candidate.result
		}
		return results, nil
	}

	results := make([]search.Result, 0, limit)
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(results) < limit && len(candidates) > 0 {
		total := 0
		for _, candidate := range candidates {
			total += candidate.weight
		}
		pick := c.random.Intn(total)
		selected := 0
		for i, candidate := range candidates {
			pick -= candidate.weight
			if pick < 0 {
				selected = i
				break
			}
		}
		results = append(results, candidates[selected].result)
		candidates = append(candidates[:selected], candidates[selected+1:]...)
	}
	return results, nil
}

// Query loads a persisted JSONL cache when available, then searches it.
func (c *Cache) Query(ctx context.Context, reader conceptReader, query string, limit int) ([]search.Result, error) {
	if err := c.loadJSONL(ctx, reader); err != nil {
		if _, buildErr := c.Build(ctx, reader); buildErr != nil {
			return nil, buildErr
		}
	}
	return c.Search(ctx, reader, query, limit)
}

// All returns all cached entries, building or loading the cache on first use.
func (c *Cache) All(ctx context.Context, reader conceptReader) ([]Entry, error) {
	if project, ok := c.project(reader); ok {
		return cloneEntries(project.entries), nil
	}
	if err := c.loadJSONL(ctx, reader); err == nil {
		if project, ok := c.project(reader); ok {
			return cloneEntries(project.entries), nil
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return c.Build(ctx, reader)
}

// PinnedCanonicalSnapshot reads the canonical corpus only from a non-legacy,
// manifest-pinned reader. Unlike All, this path never falls back to page reads.
func (c *Cache) PinnedCanonicalSnapshot(ctx context.Context, reader Reader) (PinnedCanonicalSnapshot, error) {
	if reader == nil {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical snapshot reader is nil")
	}
	fileReader, ok := reader.(interface {
		ReadFile(context.Context, string) ([]byte, error)
	})
	if !ok {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical snapshot reader cannot read JSONL")
	}
	tokenized, ok := reader.(interface{ ViewToken() string })
	if !ok || tokenized.ViewToken() == "" || tokenized.ViewToken() == "legacy" {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical snapshot requires a pinned view")
	}
	identityReader, ok := reader.(storage.QueryGenerationIdentityProvider)
	if !ok {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical snapshot requires a manifest identity")
	}
	identity, err := identityReader.QueryGenerationIdentity(ctx)
	if err != nil {
		return PinnedCanonicalSnapshot{}, fmt.Errorf("read pinned canonical identity: %w", err)
	}
	if identity.ProjectID == "" || identity.GenerationID == "" || identity.GenerationID == "legacy" || !validManifestDigest(identity.ConceptsDigest) {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical snapshot identity is invalid")
	}

	key := pinnedSnapshotKey(reader, identity)
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	c.mu.RLock()
	if snapshot, ok := c.pinnedSnapshots[key]; ok {
		c.mu.RUnlock()
		return clonePinnedSnapshot(snapshot), nil
	}
	var data []byte
	if prefix := prefixForReader(reader); prefix != "" {
		if project, ok := c.projects[prefix]; ok && project.canonicalData != nil {
			data = append([]byte(nil), project.canonicalData...)
		}
	}
	c.mu.RUnlock()
	if data == nil {
		data, err = fileReader.ReadFile(ctx, GCSPath)
		if err != nil {
			return PinnedCanonicalSnapshot{}, fmt.Errorf("read pinned canonical concepts JSONL: %w", err)
		}
	}
	if got := manifestFileDigest(data); got != identity.ConceptsDigest {
		return PinnedCanonicalSnapshot{}, errors.New("pinned canonical concepts digest mismatch")
	}
	items, err := parseCanonicalEntries(data)
	if err != nil {
		return PinnedCanonicalSnapshot{}, fmt.Errorf("parse pinned canonical concepts JSONL: %w", err)
	}
	snapshot := PinnedCanonicalSnapshot{Identity: identity, Entries: items}
	entries := make([]Entry, len(items))
	bySlug := make(map[string]Entry, len(items))
	for i, item := range items {
		entries[i] = cloneEntry(item.Entry)
		bySlug[item.Entry.Slug] = cloneEntry(item.Entry)
	}
	c.mu.Lock()
	if c.pinnedSnapshots == nil {
		c.pinnedSnapshots = make(map[string]PinnedCanonicalSnapshot)
	}
	c.pinnedSnapshots[key] = clonePinnedSnapshot(snapshot)
	if prefix := prefixForReader(reader); prefix != "" && len(entries) > 0 {
		c.projects[prefix] = projectCache{entries: entries, bySlug: bySlug, canonicalData: append([]byte(nil), data...)}
	}
	c.mu.Unlock()
	return clonePinnedSnapshot(snapshot), nil
}

// Entry returns a cached concept for the requested project.
func (c *Cache) Entry(reader conceptReader, slug string) (Entry, bool) {
	project, ok := c.project(reader)
	if !ok {
		return Entry{}, false
	}
	entry, ok := project.bySlug[slug]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(entry), true
}

func (c *Cache) project(reader conceptReader) (projectCache, bool) {
	if reader == nil {
		return projectCache{}, false
	}
	c.mu.RLock()
	project, ok := c.projects[prefixForReader(reader)]
	c.mu.RUnlock()
	return project, ok
}

func (c *Cache) loadJSONL(ctx context.Context, reader conceptReader) error {
	fileReader, ok := reader.(interface {
		ReadFile(context.Context, string) ([]byte, error)
	})
	if !ok {
		return fmt.Errorf("concept cache reader cannot read JSONL")
	}
	data, err := fileReader.ReadFile(ctx, GCSPath)
	if err != nil {
		return err
	}
	entries, err := unmarshalJSONL(data)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("concept cache JSONL is empty")
	}
	bySlug := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		bySlug[entry.Slug] = entry
	}
	c.mu.Lock()
	c.projects[prefixForReader(reader)] = projectCache{entries: entries, bySlug: bySlug, canonicalData: append([]byte(nil), data...)}
	c.mu.Unlock()
	return nil
}

func pinnedSnapshotKey(reader conceptReader, identity storage.QueryGenerationIdentity) string {
	token := reader.(interface{ ViewToken() string }).ViewToken()
	return prefixForReader(reader) + "\x00" + token + "\x00" + identity.ProjectID + "\x00" + identity.GenerationID + "\x00" + identity.ConceptsDigest
}

func validManifestDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == digest
}

func manifestFileDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func parseCanonicalEntries(data []byte) ([]CanonicalEntry, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	items := make([]CanonicalEntry, 0)
	seenSlugs := make(map[string]bool)
	seenIDs := make(map[string]bool)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(items) >= generation.MaxFiles {
			return nil, generation.ErrLogicalEntryLimit
		}
		if !utf8.Valid(line) {
			return nil, errors.New("concepts JSONL row is not UTF-8")
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		stableID := frontmatterString(entry.Frontmatter["id"])
		if strings.TrimSpace(entry.Slug) == "" || strings.TrimSpace(stableID) == "" {
			return nil, errors.New("concepts JSONL row is missing slug or stable ID")
		}
		if seenSlugs[entry.Slug] || seenIDs[stableID] {
			return nil, errors.New("concepts JSONL has duplicate slug or stable ID")
		}
		seenSlugs[entry.Slug] = true
		seenIDs[stableID] = true
		digest := sha256.Sum256(line)
		items = append(items, CanonicalEntry{Entry: entry, StableID: stableID, ContentDigest: hex.EncodeToString(digest[:])})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func clonePinnedSnapshot(snapshot PinnedCanonicalSnapshot) PinnedCanonicalSnapshot {
	cloned := PinnedCanonicalSnapshot{Identity: snapshot.Identity, Entries: make([]CanonicalEntry, len(snapshot.Entries))}
	for i, item := range snapshot.Entries {
		item.Entry = cloneEntry(item.Entry)
		cloned.Entries[i] = item
	}
	return cloned
}

func prefixForReader(reader conceptReader) string {
	if prefixed, ok := reader.(interface{ Prefix() string }); ok {
		prefix := prefixed.Prefix()
		if tokenized, ok := reader.(interface{ ViewToken() string }); ok {
			if token := tokenized.ViewToken(); token != "" && token != "legacy" {
				return prefix + ":" + token
			}
		}
		return prefix
	}
	return ""
}

func parseEntry(slug, fallbackTitle, raw string) Entry {
	frontmatter, body := parseFrontmatter(raw)
	title := fallbackTitle
	if value := strings.TrimSpace(frontmatterString(frontmatter["title"])); value != "" {
		title = value
	}
	if title == "" {
		title = slug
	}
	return Entry{
		Slug:        slug,
		Title:       title,
		Body:        body,
		Frontmatter: frontmatter,
		Sources:     frontmatterSources(frontmatter),
	}
}

func parseFrontmatter(raw string) (map[string]interface{}, string) {
	matter := make(map[string]interface{})
	if !strings.HasPrefix(raw, "---\n") {
		return matter, raw
	}
	body, err := fm.MustParse(strings.NewReader(raw), &matter)
	if err != nil {
		return make(map[string]interface{}), raw
	}
	return matter, string(body)
}

func frontmatterSources(frontmatter map[string]interface{}) []string {
	for _, key := range []string{"sources", "source"} {
		switch value := frontmatter[key].(type) {
		case []string:
			return append([]string(nil), value...)
		case []interface{}:
			sources := make([]string, 0, len(value))
			for _, item := range value {
				if source := strings.TrimSpace(fmt.Sprint(item)); source != "" {
					sources = append(sources, source)
				}
			}
			return sources
		case string:
			if value != "" {
				return []string{value}
			}
		}
	}
	return []string{}
}

func frontmatterString(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func entryScore(entry Entry, words []string) int {
	title := strings.ToLower(entry.Title)
	body := strings.ToLower(entry.Body)
	sources := strings.ToLower(strings.Join(entry.Sources, " "))
	frontmatter := strings.ToLower(fmt.Sprint(entry.Frontmatter))
	score := 0
	for _, word := range words {
		if strings.Contains(title, word) {
			score += 5
		}
		if strings.Contains(sources, word) {
			score += 3
		}
		if strings.Contains(body, word) {
			score += 2
		}
		if strings.Contains(frontmatter, word) {
			score++
		}
	}
	return score
}

func snippet(entry Entry, words []string) string {
	text := strings.TrimSpace(entry.Body)
	if text == "" {
		text = entry.Title
	}
	lower := strings.ToLower(text)
	start := 0
	for _, word := range words {
		if index := strings.Index(lower, word); index >= 0 {
			start = max(0, index-80)
			break
		}
	}
	end := min(len(text), start+200)
	return strings.TrimSpace(text[start:end])
}

func cloneEntries(entries []Entry) []Entry {
	cloned := make([]Entry, len(entries))
	for i, entry := range entries {
		cloned[i] = cloneEntry(entry)
	}
	return cloned
}

func cloneEntry(entry Entry) Entry {
	frontmatter := make(map[string]interface{}, len(entry.Frontmatter))
	for key, value := range entry.Frontmatter {
		if values, ok := value.([]string); ok {
			frontmatter[key] = append([]string(nil), values...)
		} else {
			frontmatter[key] = value
		}
	}
	entry.Frontmatter = frontmatter
	entry.Sources = append([]string(nil), entry.Sources...)
	return entry
}

func marshalJSONL(entries []Entry) []byte {
	var builder strings.Builder
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		builder.Write(data)
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

func unmarshalJSONL(data []byte) ([]Entry, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	entries := make([]Entry, 0)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(entries) >= generation.MaxFiles {
			return nil, generation.ErrLogicalEntryLimit
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
