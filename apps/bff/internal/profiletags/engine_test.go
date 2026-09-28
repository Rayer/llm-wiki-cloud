package profiletags

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// All evaluator responses in this file are synthetic fakes, not provider evidence.
type memoryStore map[string][]byte

func (m memoryStore) Read(_ context.Context, p string, limit int) ([]byte, error) {
	b, ok := m[p]
	if !ok {
		return nil, errors.New("missing object")
	}
	if len(b) > limit {
		return nil, errors.New("over limit")
	}
	return append([]byte(nil), b...), nil
}
func (m memoryStore) Create(_ context.Context, p string, b []byte) error {
	if old, ok := m[p]; ok && !bytes.Equal(old, b) {
		return errors.New("create conflict")
	}
	m[p] = append([]byte(nil), b...)
	return nil
}

type countingStore struct {
	memoryStore
	reads int
}

func (s *countingStore) Read(ctx context.Context, p string, limit int) ([]byte, error) {
	s.reads++
	return s.memoryStore.Read(ctx, p, limit)
}

type fakeEvaluator struct {
	calls    []string
	fail     map[string]bool
	judgment map[string]Judgment
}

func (f *fakeEvaluator) Evaluate(_ context.Context, i Item, t Tag, _ ProviderPolicy) (Evaluation, error) {
	k := key(i.Kind, i.StableID, t.ID)
	f.calls = append(f.calls, k)
	if f.fail[k] {
		return Evaluation{}, errors.New("synthetic timeout")
	}
	j := f.judgment[k]
	if j == "" {
		j = Unknown
	}
	return Evaluation{Judgment: j, ReturnedModel: "provider-version-7"}, nil
}
func fixture() Input {
	h := strings.Repeat("a", 64)
	return Input{Inventory: Inventory{ContentGeneration: "G1", ConceptsDigest: h, IDMapDigest: h, SourceSnapshotDigest: h, Items: []Item{{Source, "same", []byte("raw")}, {Concept, "same", []byte("concept")}}}, Dictionary: Dictionary{Revision: h, Tags: []Tag{{ID: "place", Definition: "place", AppliesTo: []Kind{Source, Concept}, MatchRule: "yes", NonMatchRule: "no", UnknownRule: "unclear", QueryUse: "required_candidate"}}}, Provider: ProviderPolicy{ConfiguredModel: "provider-alias", AcceptedReturnedModels: []string{"provider-version-7"}, PromptVersion: "p1", SchemaVersion: "eval.v1"}, MaxAttempts: 3}
}
func TestCanonicalFixtureAndStrictDecode(t *testing.T) {
	d := Decision{SchemaVersion: DecisionSchema, Kind: Source, StableID: "s1", ContentDigest: strings.Repeat("a", 64), InputDigest: strings.Repeat("b", 64), RuleDigest: strings.Repeat("c", 64), TagID: "place", Judgment: Unknown, ConfiguredModel: "provider-alias", ReturnedModel: "provider-version-7", PromptVersion: "p1", Evidence: "", Confidence: nil}
	b, r, e := encode(d)
	if e != nil {
		t.Fatal(e)
	}
	const want = `{"schema_version":"profile.tag-decision.v1","kind":"source","stable_id":"s1","content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","rule_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","tag_id":"place","judgment":"unknown","configured_model":"provider-alias","returned_model":"provider-version-7","prompt_version":"p1","evidence":"","confidence":null}`
	if string(b) != want {
		t.Fatalf("canonical bytes changed: %s", b)
	}
	if r != "84becedccb67e8015316016b9d97ed819348f48a37435995763c0ad3448dffc0" {
		t.Fatalf("decision digest changed: %s", r)
	}
	for _, bad := range [][]byte{append(append([]byte(nil), b...), 0xff), []byte(`{"schema_version":"x","schema_version":"y"}`), []byte(`{"unknown":1}`), append(append([]byte(nil), b...), []byte(" {}")...)} {
		var got Decision
		if StrictDecode(bad, &got) == nil {
			t.Fatalf("accepted invalid %q", bad)
		}
	}
	if e := StrictDecode(bytes.Repeat([]byte(" "), MaxObjectBytes+1), new(Decision)); e == nil {
		t.Fatal("accepted oversize")
	}
}

func TestEmptySetAndRulesCanonicalBytes(t *testing.T) {
	h := strings.Repeat("a", 64)
	set := TagSet{SetSchema, "G0", h, h, h, h, 0, 0, 0, []Row{}}
	b, r, e := encode(set)
	if e != nil {
		t.Fatal(e)
	}
	const wantSet = `{"schema_version":"profile.tag-set.v1","content_generation":"G0","dictionary_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","concepts_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_map_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source_snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_count":0,"completed_count":0,"missing_count":0,"rows":[]}`
	if string(b) != wantSet || r != "a4397e5bed98ce5f154ad5416d4ebbc1208d6eb9580a3b896a339de7ceb578bd" {
		t.Fatalf("set bytes/hash: %s %s", b, r)
	}
	rules := QueryRules{RulesSchema, "G0", h, r, "none", []Rule{}}
	b, r, e = encode(rules)
	if e != nil {
		t.Fatal(e)
	}
	const wantRules = `{"schema_version":"profile.query-rules.v1","content_generation":"G0","dictionary_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tag_set_revision":"a4397e5bed98ce5f154ad5416d4ebbc1208d6eb9580a3b896a339de7ceb578bd","quality_policy_revision":"none","rules":[]}`
	if string(b) != wantRules || r != "8721d81dff03dd79b18bbd514d80a4d6d6d1fab1e9ecf51a13609b81e99fe95b" {
		t.Fatalf("rules bytes/hash: %s %s", b, r)
	}
}
func TestIncrementalCoverageAndRequiredGate(t *testing.T) {
	ctx := context.Background()
	s := memoryStore{}
	f := &fakeEvaluator{judgment: map[string]Judgment{key(Source, "same", "place"): NoMatch, key(Concept, "same", "place"): NotApplicable}}
	in := fixture()
	first, e := Build(ctx, s, f, in)
	if e != nil {
		t.Fatal(e)
	}
	if first.SetRevision != "59eca6bc0ffc1e429acde469a3e3a3233b6fc0158551bcee0a7923bc9599a417" || first.RulesRevision != "c520f3108e43bf6c95414789c387ad9263eaeb78883813c01b584f92a507bf50" {
		t.Fatalf("nonempty fixture revisions: %s %s", first.SetRevision, first.RulesRevision)
	}
	if first.SetRevision == "" || first.RulesRevision == "" || first.Set.ExpectedCount != 2 || len(f.calls) != 2 {
		t.Fatalf("first build: %+v calls=%v", first, f.calls)
	}
	b, e := ValidatePublication(ctx, s, ActiveRef{"G1", in.Dictionary.Revision, first.SetRevision, first.RulesRevision}, in.Inventory, in.Dictionary, in.Provider, in.Quality)
	if e != nil {
		t.Fatal(e)
	}
	if !errors.Is(b.RequireSupported([]string{"place"}, Source), ErrUnsupportedRequired) {
		t.Fatal("explicit required rule silently weakened")
	}
	if b.Decisions[0].Judgment != NoMatch || b.Decisions[1].Judgment != NotApplicable {
		t.Fatal("source/concept decisions merged")
	}
	f.calls = nil
	f.judgment[key(Concept, "same", "place")] = Match
	in.Inventory.ContentGeneration = "G2"
	in.Inventory.Items[1].Content = []byte("changed concept")
	in.PriorDecisionRevisions = first.Completed
	second, e := Build(ctx, s, f, in)
	if e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 1 || f.calls[0] != key(Concept, "same", "place") || second.Set.Rows[0].DecisionRevision != first.Set.Rows[0].DecisionRevision {
		t.Fatalf("incremental calls=%v", f.calls)
	}
	if second.Set.Rows[1].Judgment != Match {
		t.Fatal("match was not a completed decision")
	}
	f.calls = nil
	in.Dictionary.Tags = nil
	in.PriorDecisionRevisions = second.Completed
	empty, e := Build(ctx, s, f, in)
	if e != nil || len(f.calls) != 0 || empty.Set.ExpectedCount != 0 || len(empty.Set.Rows) != 0 || empty.SetRevision == "" {
		t.Fatalf("removed tag: %+v %v calls=%v", empty, e, f.calls)
	}
}
func TestMissingRetriesAndCrossReferences(t *testing.T) {
	ctx := context.Background()
	s := memoryStore{}
	in := fixture()
	f := &fakeEvaluator{fail: map[string]bool{key(Source, "same", "place"): true}, judgment: map[string]Judgment{key(Concept, "same", "place"): Unknown}}
	partial, e := Build(ctx, s, f, in)
	if e != nil {
		t.Fatal(e)
	}
	if partial.SetRevision != "" || partial.Set.MissingCount != 1 || len(partial.Completed) != 1 || len(f.calls) != 4 {
		t.Fatalf("partial: %+v calls=%v", partial, f.calls)
	}
	f.fail = nil
	f.calls = nil
	in.PriorDecisionRevisions = partial.Completed
	done, e := Build(ctx, s, f, in)
	if e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 1 || f.calls[0] != key(Source, "same", "place") {
		t.Fatalf("retry calls=%v", f.calls)
	}
	ref := ActiveRef{"G1", in.Dictionary.Revision, done.SetRevision, done.RulesRevision}
	for name, change := range map[string]func(*TagSet){"missing": func(x *TagSet) { x.Rows = x.Rows[:1]; x.CompletedCount = 1 }, "extra": func(x *TagSet) { x.Rows = append(x.Rows, x.Rows[1]) }, "duplicate": func(x *TagSet) { x.Rows[1] = x.Rows[0] }, "content": func(x *TagSet) { x.Rows[0].ContentDigest = strings.Repeat("f", 64) }} {
		x := done.Set
		x.Rows = append([]Row(nil), x.Rows...)
		change(&x)
		raw, rev, err := encode(x)
		if err != nil {
			t.Fatal(err)
		}
		s[SetPath(rev)] = raw
		bad := ref
		bad.TagSetRevision = rev
		if _, err = ValidatePublication(ctx, s, bad, in.Inventory, in.Dictionary, in.Provider, in.Quality); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	raw := append([]byte(nil), s[SetPath(ref.TagSetRevision)]...)
	raw[len(raw)-1] = ' '
	s[SetPath(ref.TagSetRevision)] = raw
	if _, e := ValidatePublication(ctx, s, ref, in.Inventory, in.Dictionary, in.Provider, in.Quality); e == nil {
		t.Fatal("accepted changed bytes")
	}
}
func TestPolicyChangesRuleDigest(t *testing.T) {
	in := fixture()
	tag := in.Dictionary.Tags[0]
	a, e := RuleDigest(tag, Source, in.Provider)
	if e != nil {
		t.Fatal(e)
	}
	in.Provider.AcceptedReturnedModels = []string{"provider-version-8"}
	b, e := RuleDigest(tag, Source, in.Provider)
	if e != nil || a == b {
		t.Fatal("policy change did not invalidate")
	}
	in.Provider.AcceptedReturnedModels = []string{"provider-version-7"}
	tag.MatchRule = "different"
	b, e = RuleDigest(tag, Source, in.Provider)
	if e != nil || a == b {
		t.Fatal("rule change did not invalidate")
	}
}

func TestUnacceptedReturnedIdentityIsMissing(t *testing.T) {
	ctx := context.Background()
	s := memoryStore{}
	in := fixture()
	in.Inventory.Items = in.Inventory.Items[:1]
	f := &fakeEvaluator{}
	in.Provider.AcceptedReturnedModels = []string{"different-version"}
	result, err := Build(ctx, s, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if result.SetRevision != "" || result.Set.MissingCount != 1 || result.Missing[0].Code != "invalid_provider_result" {
		t.Fatalf("identity accepted: %+v", result)
	}
}

func TestQueryReaderTwoObjectsAndScopedRequirement(t *testing.T) {
	ctx := context.Background()
	s := memoryStore{}
	in := fixture()
	f := &fakeEvaluator{}
	done, err := Build(ctx, s, f, in)
	if err != nil {
		t.Fatal(err)
	}
	index := InventoryIndex{ContentGeneration: "G1", ConceptsDigest: in.Inventory.ConceptsDigest, IDMapDigest: in.Inventory.IDMapDigest, SourceSnapshotDigest: in.Inventory.SourceSnapshotDigest}
	for _, i := range in.Inventory.Items {
		index.Items = append(index.Items, ItemRef{i.Kind, i.StableID, digest(i.Content)})
	}
	counted := &countingStore{memoryStore: s}
	ref := ActiveRef{"G1", in.Dictionary.Revision, done.SetRevision, done.RulesRevision}
	b, err := ReadBundle(ctx, counted, ref, index, in.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	if counted.reads != 2 || len(b.Decisions) != 0 {
		t.Fatalf("Query read %d objects; decisions=%d", counted.reads, len(b.Decisions))
	}
	if err = b.RequireSupported(nil, Source); err != nil {
		t.Fatal("unrequested hard rule blocked query", err)
	}
	if err = b.RequireSupported([]string{"unrelated"}, Source); !errors.Is(err, ErrUnsupportedRequired) {
		t.Fatal("unknown requested hard rule accepted")
	}
	if err = b.RequireSupported([]string{"place"}, Source); !errors.Is(err, ErrUnsupportedRequired) {
		t.Fatal("required rule silently weakened")
	}
	index.Items = index.Items[:1]
	counted.reads = 0
	if _, err = ReadBundle(ctx, counted, ref, index, in.Dictionary); err == nil {
		t.Fatal("accepted missing inventory item")
	}
	if counted.reads != 2 {
		t.Fatalf("Query reads=%d", counted.reads)
	}
}
