package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

func TestProfileRequestUnsupportedOnlyWhenExplicit(t *testing.T) {
	p := &ProfileSnapshot{Bundle: profiletags.Bundle{Rules: profiletags.QueryRules{Rules: []profiletags.Rule{{Kind: profiletags.Concept, TagID: "taipei", QueryUse: "required_candidate", ReasonCode: "unsupported_required"}}}}}
	if err := ValidateProfileRequest(Request{Profile: p}); err != nil {
		t.Fatalf("ordinary query: %v", err)
	}
	for _, snapshot := range []*ProfileSnapshot{nil, p} {
		for _, id := range []string{"taipei", "shilin", "nearby", "unknown"} {
			if err := ValidateProfileRequest(Request{Profile: snapshot, RequiredTagIDs: []string{id}}); !errors.Is(err, ErrUnsupportedRequired) {
				t.Fatalf("%s: %v", id, err)
			}
		}
	}
}

func TestProfilePreferencesUseOnlyExactConceptMatch(t *testing.T) {
	p := &ProfileSnapshot{Corpus: cache.PinnedCanonicalSnapshot{Entries: []cache.CanonicalEntry{{StableID: "same", Entry: cache.Entry{Slug: "concept"}}}}, Bundle: profiletags.Bundle{
		Rules: profiletags.QueryRules{Rules: []profiletags.Rule{{Kind: profiletags.Concept, TagID: "preferred", QueryUse: "preferred"}, {Kind: profiletags.Concept, TagID: "required", QueryUse: "required_candidate"}, {Kind: profiletags.Source, TagID: "source_only", QueryUse: "preferred"}}},
		Set:   profiletags.TagSet{Rows: []profiletags.Row{{Kind: profiletags.Source, StableID: "same", TagID: "preferred", Judgment: profiletags.Match}, {Kind: profiletags.Concept, StableID: "same", TagID: "preferred", Judgment: profiletags.Unknown}, {Kind: profiletags.Concept, StableID: "same", TagID: "required", Judgment: profiletags.Match}, {Kind: profiletags.Source, StableID: "same", TagID: "source_only", Judgment: profiletags.Match}}}}}
	if got := p.PreferenceScores()["concept"]; got != 0 {
		t.Fatalf("source/unknown/required inherited: %d", got)
	}
	p.Bundle.Set.Rows[1].Judgment = profiletags.Match
	if got := p.PreferenceScores()["concept"]; got != 1 {
		t.Fatalf("exact concept preference = %d", got)
	}
}

func TestPinnedProfileCitationContextsNeverNeedReader(t *testing.T) {
	const id = "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	p := &ProfileSnapshot{IDMap: wikiindex.IDMap{Concept: map[string]string{id: "old"}}, Corpus: cache.PinnedCanonicalSnapshot{Entries: []cache.CanonicalEntry{{StableID: id, Entry: cache.Entry{Slug: "old", Body: "retained G1 body"}}}}}
	results := []search.Result{{Slug: "old", Title: "G1", Type: "concept"}}
	authority, err := search.NewCitationAuthority(results)
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := buildProfileContexts(context.Background(), results, authority, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || !strings.Contains(contexts[0], "retained G1 body") || results[0].ID != id {
		t.Fatalf("contexts=%v results=%v", contexts, results)
	}
	results[0].Slug = "new-G2"
	if _, err := buildProfileContexts(context.Background(), results, authority, p); err == nil {
		t.Fatal("accepted result outside snapshot")
	}
}
