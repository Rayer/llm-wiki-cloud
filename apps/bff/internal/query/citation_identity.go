package query

import (
	"context"
	"fmt"

	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type citationIdentityKey struct {
	kind, slug string
}

type citationIdentityEntry struct {
	id                     string
	ambiguous, conflicting bool
}

type citationIdentityResolver map[citationIdentityKey]citationIdentityEntry

func newCitationIdentityResolver(ids wikiindex.IDMap) (citationIdentityResolver, error) {
	resolver := make(citationIdentityResolver)
	// Validate all active rows once, including rows not selected in this request.
	for kind, entries := range map[string]map[string]string{"concept": ids.Concept, "source": ids.Source} {
		for id, slug := range entries {
			validID := wikiindex.ValidLegacyConceptID(id) || wikiindex.ValidSyntoEntityID(id)
			if !validID || !search.SafeCitationSlug(slug) {
				return nil, fmt.Errorf("citation identity: invalid active map entry")
			}
			key := citationIdentityKey{kind, slug}
			entry, exists := resolver[key]
			entry.id, entry.ambiguous = id, exists
			other := ids.Source
			if kind == "source" {
				other = ids.Concept
			}
			_, collision := other[id]
			entry.conflicting = entry.conflicting || collision
			resolver[key] = entry
		}
	}
	return resolver, nil
}

func (resolver citationIdentityResolver) resolve(result search.Result) (search.Result, error) {
	if result.Type != "concept" && result.Type != "source" {
		return search.Result{}, fmt.Errorf("citation identity: invalid type")
	}
	entry := resolver[citationIdentityKey{result.Type, result.Slug}]
	if entry.ambiguous {
		return search.Result{}, fmt.Errorf("citation identity: ambiguous mapping")
	}
	if entry.conflicting {
		return search.Result{}, fmt.Errorf("citation identity: conflicting type")
	}
	result.ID = entry.id
	if !search.SafeCitationResult(result) {
		return search.Result{}, fmt.Errorf("citation identity: missing mapping or unsafe route")
	}
	return result, nil
}

// ResolveCitationIdentity is the single-result helper for a supplied snapshot.
// Production context building reuses one resolver for the whole request.
func ResolveCitationIdentity(result search.Result, ids wikiindex.IDMap) (search.Result, error) {
	resolver, err := newCitationIdentityResolver(ids)
	if err != nil {
		return search.Result{}, err
	}
	return resolver.resolve(result)
}

func LoadCitationIDMap(ctx context.Context, reader any) (wikiindex.IDMap, error) {
	files, ok := reader.(interface {
		ReadFile(context.Context, string) ([]byte, error)
	})
	if !ok {
		return wikiindex.IDMap{}, fmt.Errorf("citation identity: snapshot cannot read ID map")
	}
	data, err := files.ReadFile(ctx, wikiindex.IDMapPath)
	if err != nil {
		return wikiindex.IDMap{}, fmt.Errorf("citation identity: read ID map: %w", err)
	}
	ids, err := wikiindex.DecodeIDMap(data)
	if err != nil {
		return wikiindex.IDMap{}, fmt.Errorf("citation identity: decode ID map: %w", err)
	}
	return ids, nil
}
