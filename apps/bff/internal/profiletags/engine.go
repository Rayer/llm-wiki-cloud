package profiletags

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
)

// Store is a Project-scoped immutable object store. Create must reject a different existing body.
type Store interface {
	Read(context.Context, string, int) ([]byte, error)
	Create(context.Context, string, []byte) error
}
type Evaluation struct {
	Judgment      Judgment
	ReturnedModel string
	Evidence      string
	Confidence    *float64
}

// Evaluator is injectable; production provider selection is an integration decision.
type Evaluator interface {
	Evaluate(context.Context, Item, Tag, ProviderPolicy) (Evaluation, error)
}
type Input struct {
	Inventory  Inventory
	Dictionary Dictionary
	Provider   ProviderPolicy
	Quality    QualityPolicy
	// Prior decision revisions come from a verified prior set or durable staging checkpoint.
	PriorDecisionRevisions []string
	MaxAttempts            int
	// CheckpointID scopes immutable completed-key receipts to a job. Store.Read
	// must return fs.ErrNotExist for absent receipts when checkpoints are enabled.
	CheckpointID string
}
type Failure struct {
	Kind                  Kind
	StableID, TagID, Code string
}
type Result struct {
	SetRevision, RulesRevision string
	Set                        TagSet
	Rules                      QueryRules
	Completed                  []string
	Missing                    []Failure
}

func decisionPath(r string) string { return ".lwc/profile/tags/decisions/v1/" + r + ".json" }
func SetPath(r string) string      { return ".lwc/profile/tags/sets/v1/" + r + ".json" }
func RulesPath(r string) string    { return ".lwc/profile/query/rules/v1/" + r + ".json" }
func load[T any](ctx context.Context, s Store, path, revision string) (T, error) {
	var v T
	if !validDigest(revision) {
		return v, errors.New("invalid revision")
	}
	b, e := s.Read(ctx, path, MaxObjectBytes+1)
	if e != nil {
		return v, e
	}
	if len(b) > MaxObjectBytes {
		return v, errors.New("object too large")
	}
	if digest(b) != revision {
		return v, errors.New("object digest mismatch")
	}
	if e = StrictDecode(b, &v); e != nil {
		return v, e
	}
	canonical, _, e := encode(v)
	if e != nil || !bytes.Equal(canonical, b) {
		return v, errors.New("noncanonical object")
	}
	return v, nil
}
func save(ctx context.Context, s Store, path string, v any) (string, error) {
	b, r, e := encode(v)
	if e != nil {
		return "", e
	}
	if e = s.Create(ctx, path+r+".json", b); e != nil {
		return "", e
	}
	read, e := s.Read(ctx, path+r+".json", MaxObjectBytes+1)
	if e != nil || !bytes.Equal(read, b) {
		return "", errors.New("immutable object readback mismatch")
	}
	return r, nil
}

func validateInput(in Input) error {
	v := in.Inventory
	if v.ContentGeneration == "" || !validDigest(v.ConceptsDigest) || !validDigest(v.IDMapDigest) || !validDigest(v.SourceSnapshotDigest) || !validDigest(in.Dictionary.Revision) {
		return errors.New("invalid generation or digest")
	}
	if e := validatePolicy(in.Provider); e != nil {
		return e
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 10 {
		return errors.New("invalid retry bound")
	}
	seen := map[string]bool{}
	for _, i := range v.Items {
		if _, _, _, e := inputFor(i); e != nil {
			return e
		}
		k := string(i.Kind) + "\x00" + i.StableID
		if seen[k] {
			return errors.New("duplicate inventory item")
		}
		seen[k] = true
	}
	seen = map[string]bool{}
	for _, t := range in.Dictionary.Tags {
		if t.ID == "" || seen[t.ID] || (t.QueryUse != "preferred" && t.QueryUse != "required_candidate") {
			return errors.New("invalid tag")
		}
		seen[t.ID] = true
		applies := map[Kind]bool{}
		for _, k := range t.AppliesTo {
			if !validKind(k) || applies[k] {
				return errors.New("invalid applies_to")
			}
			applies[k] = true
		}
		if len(applies) == 0 {
			return errors.New("empty applies_to")
		}
	}
	if in.Quality.Revision == "" {
		in.Quality.Revision = "none"
	}
	if in.Quality.Revision != "none" && !validDigest(in.Quality.Revision) {
		return errors.New("invalid quality policy revision")
	}
	for _, id := range in.Quality.ApprovedRequiredTags {
		if !seen[id] {
			return errors.New("approval references absent tag")
		}
	}
	if len(in.Quality.ApprovedRequiredTags) != 0 {
		return errors.New("hard gate approvals require separate quality and geo evidence integration")
	}
	return nil
}
func applies(t Tag, k Kind) bool {
	for _, x := range t.AppliesTo {
		if x == k {
			return true
		}
	}
	return false
}
func key(k Kind, id, tag string) string { return string(k) + "\x00" + id + "\x00" + tag }
func decisionKey(d Decision) string {
	return key(d.Kind, d.StableID, d.TagID) + "\x00" + d.ContentDigest + "\x00" + d.InputDigest + "\x00" + d.RuleDigest
}
func rowLess(a, b Row) bool {
	if a.Kind != b.Kind {
		return a.Kind == Source
	}
	if a.StableID != b.StableID {
		return a.StableID < b.StableID
	}
	return a.TagID < b.TagID
}
func accepted(p ProviderPolicy, returned string) bool {
	for _, s := range p.AcceptedReturnedModels {
		if s == returned {
			return true
		}
	}
	return false
}
func validDecision(d Decision, p ProviderPolicy) bool {
	return d.SchemaVersion == DecisionSchema && validKind(d.Kind) && d.StableID != "" && validDigest(d.ContentDigest) && validDigest(d.InputDigest) && validDigest(d.RuleDigest) && d.TagID != "" && validJudgment(d.Judgment) && d.ConfiguredModel == p.ConfiguredModel && accepted(p, d.ReturnedModel) && d.PromptVersion == p.PromptVersion && len(d.Evidence) <= MaxEvidenceBytes && (d.Confidence == nil || (*d.Confidence >= 0 && *d.Confidence <= 1))
}

// Build evaluates only keys lacking a validated prior decision. It writes no set on incomplete coverage.
func Build(ctx context.Context, s Store, e Evaluator, in Input) (Result, error) {
	if s == nil || e == nil {
		return Result{}, errors.New("store/evaluator required")
	}
	if err := validateInput(in); err != nil {
		return Result{}, err
	}
	prior := map[string]struct {
		d Decision
		r string
	}{}
	for _, r := range in.PriorDecisionRevisions {
		d, err := load[Decision](ctx, s, decisionPath(r), r)
		if err != nil {
			return Result{}, err
		}
		if !validDecision(d, in.Provider) {
			return Result{}, errors.New("invalid prior decision")
		}
		k := decisionKey(d)
		if prev, ok := prior[k]; ok && prev.r != r {
			return Result{}, errors.New("conflicting prior decision")
		}
		prior[k] = struct {
			d Decision
			r string
		}{d, r}
	}
	items := append([]Item(nil), in.Inventory.Items...)
	sort.Slice(items, func(a, b int) bool {
		if items[a].Kind != items[b].Kind {
			return items[a].Kind == Source
		}
		return items[a].StableID < items[b].StableID
	})
	tags := append([]Tag(nil), in.Dictionary.Tags...)
	sort.Slice(tags, func(a, b int) bool { return tags[a].ID < tags[b].ID })
	out := Result{Set: TagSet{SchemaVersion: SetSchema, ContentGeneration: in.Inventory.ContentGeneration, DictionaryRevision: in.Dictionary.Revision, ConceptsDigest: in.Inventory.ConceptsDigest, IDMapDigest: in.Inventory.IDMapDigest, SourceSnapshotDigest: in.Inventory.SourceSnapshotDigest, Rows: []Row{}}, Completed: []string{}, Missing: []Failure{}}
	for _, i := range items {
		_, cd, id, e1 := inputFor(i)
		if e1 != nil {
			return Result{}, e1
		}
		for _, t := range tags {
			if !applies(t, i.Kind) {
				continue
			}
			out.Set.ExpectedCount++
			rd, e1 := RuleDigest(t, i.Kind, in.Provider)
			if e1 != nil {
				return Result{}, e1
			}
			lookup := key(i.Kind, i.StableID, t.ID) + "\x00" + cd + "\x00" + id + "\x00" + rd
			var d Decision
			var revision string
			if in.CheckpointID != "" {
				d, revision, e1 = readCheckpoint(ctx, s, in.CheckpointID, lookup, in.Provider)
				if e1 != nil {
					return Result{}, e1
				}
			}
			if revision != "" {
				// A prior invocation completed this exact independent decision key.
			} else if p, ok := prior[lookup]; ok {
				d, revision = p.d, p.r
			} else {
				for attempt := 0; attempt < in.MaxAttempts; attempt++ {
					if err := ctx.Err(); err != nil {
						return Result{}, err
					}
					eval, err := e.Evaluate(ctx, i, t, in.Provider)
					if err != nil {
						if attempt == in.MaxAttempts-1 {
							out.Missing = append(out.Missing, Failure{i.Kind, i.StableID, t.ID, "provider_error"})
						}
						continue
					}
					d = Decision{DecisionSchema, i.Kind, i.StableID, cd, id, rd, t.ID, eval.Judgment, in.Provider.ConfiguredModel, eval.ReturnedModel, in.Provider.PromptVersion, eval.Evidence, eval.Confidence}
					if !validDecision(d, in.Provider) {
						out.Missing = append(out.Missing, Failure{i.Kind, i.StableID, t.ID, "invalid_provider_result"})
						break
					}
					revision, e1 = save(ctx, s, ".lwc/profile/tags/decisions/v1/", d)
					if e1 != nil {
						return Result{}, e1
					}
					break
				}
			}
			if revision == "" {
				continue
			}
			if in.CheckpointID != "" {
				if err := writeCheckpoint(ctx, s, in.CheckpointID, lookup, revision); err != nil {
					return Result{}, err
				}
			}
			out.Completed = append(out.Completed, revision)
			out.Set.Rows = append(out.Set.Rows, Row{i.Kind, i.StableID, cd, id, rd, t.ID, in.Dictionary.Revision, revision, d.Judgment})
		}
	}
	out.Set.CompletedCount = len(out.Set.Rows)
	out.Set.MissingCount = out.Set.ExpectedCount - out.Set.CompletedCount
	if out.Set.MissingCount != len(out.Missing) {
		return Result{}, errors.New("coverage accounting mismatch")
	}
	if out.Set.MissingCount > 0 {
		return out, nil
	}
	var err error
	out.SetRevision, err = save(ctx, s, ".lwc/profile/tags/sets/v1/", out.Set)
	if err != nil {
		return Result{}, err
	}
	out.Rules, err = makeRules(in, out.SetRevision)
	if err != nil {
		return Result{}, err
	}
	out.RulesRevision, err = save(ctx, s, ".lwc/profile/query/rules/v1/", out.Rules)
	if err != nil {
		return Result{}, err
	}
	if _, err = ValidatePublication(ctx, s, ActiveRef{in.Inventory.ContentGeneration, in.Dictionary.Revision, out.SetRevision, out.RulesRevision}, in.Inventory, in.Dictionary, in.Provider, in.Quality); err != nil {
		return Result{}, fmt.Errorf("readback: %w", err)
	}
	return out, nil
}
func makeRules(in Input, setRevision string) (QueryRules, error) {
	p := in.Quality
	if p.Revision == "" {
		p.Revision = "none"
	}
	r := QueryRules{SchemaVersion: RulesSchema, ContentGeneration: in.Inventory.ContentGeneration, DictionaryRevision: in.Dictionary.Revision, TagSetRevision: setRevision, QualityPolicyRevision: p.Revision, Rules: []Rule{}}
	tags := append([]Tag(nil), in.Dictionary.Tags...)
	sort.Slice(tags, func(i, j int) bool { return tags[i].ID < tags[j].ID })
	for _, t := range tags {
		for _, kind := range []Kind{Source, Concept} {
			if !applies(t, kind) {
				continue
			}
			d, e := RuleDigest(t, kind, in.Provider)
			if e != nil {
				return r, e
			}
			row := Rule{TagID: t.ID, Kind: kind, RuleDigest: d, QueryUse: t.QueryUse, ReasonCode: "preference"}
			if t.QueryUse == "required_candidate" {
				row.ReasonCode = "unsupported_required"
			}
			r.Rules = append(r.Rules, row)
		}
	}
	return r, nil
}
