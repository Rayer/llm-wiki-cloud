package queryquality_test

import (
	"context"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
)

type tagRankingMatcher struct{}

func (tagRankingMatcher) Match(context.Context, queryquality.MatchRequest) (queryquality.EligibilityResult, error) {
	return queryquality.EligibilityResult{Candidates: []queryquality.CandidateEvidence{
		{Slug: "a", Title: "A", Eligible: true, Qualified: true, Score: 1},
		{Slug: "b", Title: "B", Eligible: true, Qualified: true, Score: 1},
		{Slug: "c", Title: "C", Eligible: false, Qualified: false, Score: 100},
	}}, nil
}

func TestProfileTagPreferenceRanksWithoutChangingEligibility(t *testing.T) {
	plan, err := queryquality.DecodePlan(`{"raw_query":"coffee","required":[],"excluded":[],"preferred":[{"kind":"venue_type","value":"coffee","terms":["coffee"],"proof":"lexical"}],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`, "coffee")
	if err != nil {
		t.Fatal(err)
	}
	options := queryquality.DefaultOptions()
	options.SelectionLimit = 1
	options.ExplorationSlots = 0
	pipeline := queryquality.NewQueryRetrievalPipelineWithOptions(fixedQueryExpander{plan: plan}, tagRankingMatcher{}, queryquality.NewResultSelector(), nil, options)
	snapshot := &query.ProfileSnapshot{Bundle: profiletags.Bundle{Rules: profiletags.QueryRules{Rules: []profiletags.Rule{{Kind: profiletags.Concept, TagID: "preferred", QueryUse: "preferred"}}}}}
	for _, id := range []string{"a", "b", "c"} {
		snapshot.Corpus.Entries = append(snapshot.Corpus.Entries, cache.CanonicalEntry{StableID: id, Entry: cache.Entry{Slug: id}})
	}
	for _, id := range []string{"b", "c"} {
		snapshot.Bundle.Set.Rows = append(snapshot.Bundle.Set.Rows, profiletags.Row{Kind: profiletags.Concept, StableID: id, TagID: "preferred", Judgment: profiletags.Match})
	}
	request := query.Request{Query: "coffee", Profile: snapshot}
	for _, traced := range []bool{false, true} {
		var result query.Result
		if traced {
			result, _, err = pipeline.ExecuteWithTrace(context.Background(), nil, request)
		} else {
			result, err = pipeline.Execute(context.Background(), nil, request)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Results) != 1 || result.Results[0].Slug != "b" {
			t.Fatalf("traced=%v results=%v", traced, result.Results)
		}
	}
}
