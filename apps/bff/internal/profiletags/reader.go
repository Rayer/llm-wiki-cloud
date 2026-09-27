package profiletags

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

var ErrUnsupportedRequired = errors.New("required Profile rule is unsupported")

// RequirementStatus returns requested tag IDs that must not be silently treated as preferences.
func (b Bundle) RequirementStatus(requested []string, kind Kind) []string {
	wanted := map[string]bool{}
	for _, id := range requested {
		wanted[id] = true
	}
	supported := map[string]bool{}
	var ids []string
	for _, r := range b.Rules.Rules {
		if wanted[r.TagID] && r.Kind == kind && r.HardGateEnabled {
			supported[r.TagID] = true
		}
	}
	for id := range wanted {
		if !supported[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// RequireSupported is the Query boundary check for explicit hard constraints.
func (b Bundle) RequireSupported(requested []string, kind Kind) error {
	if ids := b.RequirementStatus(requested, kind); len(ids) > 0 {
		return fmt.Errorf("%w: %v", ErrUnsupportedRequired, ids)
	}
	return nil
}

// ValidatePublication performs deep writer-side readback, including every decision object.
func ValidatePublication(ctx context.Context, s Store, ref ActiveRef, inventory Inventory, dictionary Dictionary, provider ProviderPolicy, quality QualityPolicy) (Bundle, error) {
	in := Input{Inventory: inventory, Dictionary: dictionary, Provider: provider, Quality: quality, MaxAttempts: 1}
	if e := validateInput(in); e != nil {
		return Bundle{}, e
	}
	if ref.ContentGeneration != inventory.ContentGeneration || ref.DictionaryRevision != dictionary.Revision {
		return Bundle{}, errors.New("active generation/dictionary mismatch")
	}
	set, e := load[TagSet](ctx, s, SetPath(ref.TagSetRevision), ref.TagSetRevision)
	if e != nil {
		return Bundle{}, e
	}
	if set.SchemaVersion != SetSchema || set.ContentGeneration != inventory.ContentGeneration || set.DictionaryRevision != dictionary.Revision || set.ConceptsDigest != inventory.ConceptsDigest || set.IDMapDigest != inventory.IDMapDigest || set.SourceSnapshotDigest != inventory.SourceSnapshotDigest {
		return Bundle{}, errors.New("tag set inventory mismatch")
	}
	expected := map[string]struct{ cd, id, rd string }{}
	for _, i := range inventory.Items {
		_, cd, id, err := inputFor(i)
		if err != nil {
			return Bundle{}, err
		}
		for _, t := range dictionary.Tags {
			if !applies(t, i.Kind) {
				continue
			}
			rd, err := RuleDigest(t, i.Kind, provider)
			if err != nil {
				return Bundle{}, err
			}
			expected[key(i.Kind, i.StableID, t.ID)] = struct{ cd, id, rd string }{cd, id, rd}
		}
	}
	if set.ExpectedCount != len(expected) || set.CompletedCount != len(set.Rows) || set.MissingCount != 0 || len(set.Rows) != len(expected) {
		return Bundle{}, errors.New("incomplete coverage")
	}
	result := Bundle{Set: set, Decisions: make([]Decision, 0, len(set.Rows))}
	seen := map[string]bool{}
	for n, row := range set.Rows {
		if n > 0 && !rowLess(set.Rows[n-1], row) {
			return Bundle{}, errors.New("unsorted/duplicate rows")
		}
		k := key(row.Kind, row.StableID, row.TagID)
		want, ok := expected[k]
		if !ok || seen[k] {
			return Bundle{}, errors.New("extra/duplicate row")
		}
		seen[k] = true
		if row.DictionaryRevision != dictionary.Revision || row.ContentDigest != want.cd || row.InputDigest != want.id || row.RuleDigest != want.rd || !validJudgment(row.Judgment) {
			return Bundle{}, errors.New("row key/digest mismatch")
		}
		d, err := load[Decision](ctx, s, decisionPath(row.DecisionRevision), row.DecisionRevision)
		if err != nil {
			return Bundle{}, err
		}
		if !validDecision(d, provider) || d.Kind != row.Kind || d.StableID != row.StableID || d.TagID != row.TagID || d.ContentDigest != row.ContentDigest || d.InputDigest != row.InputDigest || d.RuleDigest != row.RuleDigest || d.Judgment != row.Judgment {
			return Bundle{}, errors.New("decision cross-reference mismatch")
		}
		result.Decisions = append(result.Decisions, d)
	}
	rules, e := load[QueryRules](ctx, s, RulesPath(ref.QueryRuleRevision), ref.QueryRuleRevision)
	if e != nil {
		return Bundle{}, e
	}
	wantRules, e := makeRules(in, ref.TagSetRevision)
	if e != nil {
		return Bundle{}, e
	}
	if !reflect.DeepEqual(rules, wantRules) {
		return Bundle{}, errors.New("query rule mismatch")
	}
	result.Rules = rules
	return result, nil
}
