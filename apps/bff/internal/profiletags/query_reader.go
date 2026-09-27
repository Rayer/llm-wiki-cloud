package profiletags

import (
	"context"
	"errors"
)

// ItemRef is derived from the pinned canonical inventory/index, without raw content reads.
type ItemRef struct {
	Kind                    Kind
	StableID, ContentDigest string
}
type InventoryIndex struct {
	ContentGeneration, ConceptsDigest, IDMapDigest, SourceSnapshotDigest string
	Items                                                                []ItemRef
}

// ReadBundle is the bounded Query path: exactly one set and one rules object read.
// It never fetches source bytes, concept pages, or individual decisions.
func ReadBundle(ctx context.Context, s Store, ref ActiveRef, index InventoryIndex, dictionary Dictionary) (Bundle, error) {
	if s == nil || ref.ContentGeneration == "" || ref.ContentGeneration != index.ContentGeneration || ref.DictionaryRevision != dictionary.Revision || !validDigest(ref.DictionaryRevision) || !validDigest(index.ConceptsDigest) || !validDigest(index.IDMapDigest) || !validDigest(index.SourceSnapshotDigest) {
		return Bundle{}, errors.New("invalid pinned tuple/index")
	}
	set, e := load[TagSet](ctx, s, SetPath(ref.TagSetRevision), ref.TagSetRevision)
	if e != nil {
		return Bundle{}, e
	}
	rules, e := load[QueryRules](ctx, s, RulesPath(ref.QueryRuleRevision), ref.QueryRuleRevision)
	if e != nil {
		return Bundle{}, e
	}
	if set.SchemaVersion != SetSchema || set.ContentGeneration != ref.ContentGeneration || set.DictionaryRevision != ref.DictionaryRevision || set.ConceptsDigest != index.ConceptsDigest || set.IDMapDigest != index.IDMapDigest || set.SourceSnapshotDigest != index.SourceSnapshotDigest {
		return Bundle{}, errors.New("set pinned inventory mismatch")
	}
	if rules.SchemaVersion != RulesSchema || rules.ContentGeneration != ref.ContentGeneration || rules.DictionaryRevision != ref.DictionaryRevision || rules.TagSetRevision != ref.TagSetRevision || rules.QualityPolicyRevision == "" {
		return Bundle{}, errors.New("rules pinned tuple mismatch")
	}
	tags := map[string]Tag{}
	expectedRules := map[string]bool{}
	for _, t := range dictionary.Tags {
		if t.ID == "" || tags[t.ID].ID != "" || (t.QueryUse != "preferred" && t.QueryUse != "required_candidate") {
			return Bundle{}, errors.New("invalid dictionary tags")
		}
		tags[t.ID] = t
		for _, k := range t.AppliesTo {
			if !validKind(k) || expectedRules[key(k, "", t.ID)] {
				return Bundle{}, errors.New("invalid dictionary applies_to")
			}
			expectedRules[key(k, "", t.ID)] = true
		}
	}
	ruleByKey := map[string]Rule{}
	for n, r := range rules.Rules {
		if !validKind(r.Kind) || !validDigest(r.RuleDigest) {
			return Bundle{}, errors.New("invalid query rule")
		}
		if n > 0 {
			p := rules.Rules[n-1]
			if p.TagID > r.TagID || p.TagID == r.TagID && (p.Kind == r.Kind || p.Kind == Concept) {
				return Bundle{}, errors.New("unsorted/duplicate query rules")
			}
		}
		k := key(r.Kind, "", r.TagID)
		t, ok := tags[r.TagID]
		if !ok || !expectedRules[k] || r.QueryUse != t.QueryUse || r.HardGateEnabled || (r.QueryUse == "required_candidate" && r.ReasonCode != "unsupported_required") || (r.QueryUse == "preferred" && r.ReasonCode != "preference") {
			return Bundle{}, errors.New("query rule policy mismatch")
		}
		ruleByKey[k] = r
	}
	if len(ruleByKey) != len(expectedRules) {
		return Bundle{}, errors.New("missing query rule")
	}
	items := map[string]ItemRef{}
	expectedCount := 0
	for _, i := range index.Items {
		if !validKind(i.Kind) || i.StableID == "" || !validDigest(i.ContentDigest) {
			return Bundle{}, errors.New("invalid inventory ref")
		}
		k := key(i.Kind, i.StableID, "")
		if _, ok := items[k]; ok {
			return Bundle{}, errors.New("duplicate inventory ref")
		}
		items[k] = i
		for _, t := range dictionary.Tags {
			if applies(t, i.Kind) {
				expectedCount++
			}
		}
	}
	if set.ExpectedCount != expectedCount || set.CompletedCount != expectedCount || set.MissingCount != 0 || len(set.Rows) != expectedCount {
		return Bundle{}, errors.New("incomplete coverage")
	}
	seen := map[string]bool{}
	for n, row := range set.Rows {
		if n > 0 && !rowLess(set.Rows[n-1], row) {
			return Bundle{}, errors.New("unsorted/duplicate set rows")
		}
		item, ok := items[key(row.Kind, row.StableID, "")]
		if !ok || row.ContentDigest != item.ContentDigest || row.DictionaryRevision != dictionary.Revision || !validDigest(row.InputDigest) || !validDigest(row.DecisionRevision) || !validJudgment(row.Judgment) {
			return Bundle{}, errors.New("set row mismatch")
		}
		r, ok := ruleByKey[key(row.Kind, "", row.TagID)]
		if !ok || r.RuleDigest != row.RuleDigest {
			return Bundle{}, errors.New("set/rule cross-reference mismatch")
		}
		k := key(row.Kind, row.StableID, row.TagID)
		if seen[k] {
			return Bundle{}, errors.New("duplicate coverage row")
		}
		seen[k] = true
	}
	return Bundle{Set: set, Rules: rules}, nil
}
