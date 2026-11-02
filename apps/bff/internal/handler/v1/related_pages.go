package v1

import (
	"context"
	"sort"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/handler"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

// resolveWikiRelations builds a request-local relation map only from canonical
// pages in the same storage view and explicit Concept source references.
func resolveWikiRelations(ctx context.Context, reader store.Store, currentType string, current store.WikiPage, frontmatter map[string]interface{}) ([]handler.WikiReference, []handler.WikiReference, error) {
	concepts, conceptErr := reader.ListConcepts(ctx, false)
	sources, sourceErr := reader.ListSources(ctx)
	if conceptErr != nil || sourceErr != nil { return nil, nil, nil }
	sort.Slice(concepts, func(i, j int) bool { return concepts[i].Slug < concepts[j].Slug })
	sort.Slice(sources, func(i, j int) bool { return sources[i].Slug < sources[j].Slug })
	lookup := make(map[string][]int)
	for i, source := range sources {
		for _, key := range sourceKeys(source) { lookup[key] = append(lookup[key], i) }
	}
	resolve := func(raw string) (int, bool) {
		indices := lookup[strings.TrimSpace(raw)]
		return func() (int, bool) { if len(indices) != 1 { return 0, false }; return indices[0], true }()
	}
	conceptSources := make([][]int, len(concepts))
	for i, concept := range concepts {
		for _, raw := range concept.Sources {
			if index, ok := resolve(raw); ok && !containsInt(conceptSources[i], index) { conceptSources[i] = append(conceptSources[i], index) }
		}
	}
	currentSources := make([]int, 0)
	if currentType == "concept" {
		refs := current.Sources
		if len(refs) == 0 { refs = referenceStrings(frontmatter["sources"]) }
		for _, raw := range refs { if index, ok := resolve(raw); ok && !containsInt(currentSources, index) { currentSources = append(currentSources, index) } }
	}
	currentSourceIndex := -1
	if currentType == "source" {
		for i, source := range sources { if samePageIdentity(source, current) { currentSourceIndex = i; break } }
	}
	relatedConcepts := make([]handler.WikiReference, 0)
	relatedSources := make([]handler.WikiReference, 0)
	conceptSeen, sourceSeen := map[string]bool{}, map[string]bool{}
	for i, refs := range conceptSources {
		matched := currentType == "source" && containsInt(refs, currentSourceIndex)
		if currentType == "concept" {
			for _, sourceIndex := range refs { if containsInt(currentSources, sourceIndex) { matched = true; break } }
		}
		if !matched { continue }
		concept := concepts[i]
		if currentType == "source" && !samePageIdentity(concept, current) { addReference(&relatedConcepts, conceptSeen, wikiReference(concept, "concept")) }
		for _, sourceIndex := range refs {
			if sourceIndex == currentSourceIndex || currentType == "concept" && containsInt(currentSources, sourceIndex) { continue }
			addReference(&relatedSources, sourceSeen, wikiReference(sources[sourceIndex], "source"))
		}
	}
	if currentType == "concept" {
		for i, sourcesForConcept := range conceptSources {
			if samePageIdentity(concepts[i], current) { continue }
			shared := false
			for _, sourceIndex := range sourcesForConcept { if containsInt(currentSources, sourceIndex) { shared = true; break } }
			if shared { addReference(&relatedConcepts, conceptSeen, wikiReference(concepts[i], "concept")) }
		}
		for _, sourceIndex := range currentSources { addReference(&relatedSources, sourceSeen, wikiReference(sources[sourceIndex], "source")) }
	}
	sort.Slice(relatedConcepts, func(i, j int) bool { return relatedConcepts[i].Slug < relatedConcepts[j].Slug })
	sort.Slice(relatedSources, func(i, j int) bool { return relatedSources[i].Slug < relatedSources[j].Slug })
	return relatedConcepts, relatedSources, nil
}

func sourceKeys(source store.WikiPage) []string {
	keys := []string{source.ID, source.Slug, source.Path, source.RawPath}
	result := make([]string, 0, len(keys))
	for _, key := range keys { if key = strings.TrimSpace(key); key != "" { result = append(result, key) } }
	return result
}

func referenceStrings(value any) []string {
	items, ok := value.([]interface{})
	if !ok { if values, typed := value.([]string); typed { return values }; return nil }
	result := make([]string, 0, len(items))
	for _, item := range items { if text, ok := item.(string); ok && strings.TrimSpace(text) != "" { result = append(result, strings.TrimSpace(text)) } }
	return result
}

func samePageIdentity(left, right store.WikiPage) bool {
	if left.ID != "" && right.ID != "" { return left.ID == right.ID }
	return left.Slug != "" && left.Slug == right.Slug
}

func wikiReference(page store.WikiPage, kind string) handler.WikiReference {
	return handler.WikiReference{ID: strings.TrimSpace(page.ID), Slug: strings.TrimSpace(page.Slug), Title: strings.TrimSpace(page.Title), Type: kind}
}

func addReference(target *[]handler.WikiReference, seen map[string]bool, reference handler.WikiReference) {
	if reference.Slug == "" { return }
	key := reference.Type + "\x00" + reference.Slug
	if seen[key] { return }
	seen[key] = true
	*target = append(*target, reference)
}

func containsInt(values []int, target int) bool {
	if target < 0 { return false }
	for _, value := range values { if value == target { return true } }
	return false
}
