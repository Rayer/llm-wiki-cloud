package profiletags

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

const MaxObjectBytes = 64 << 20
const MaxEvidenceBytes = 2 << 10

const (
	DecisionSchema = "profile.tag-decision.v1"
	SetSchema      = "profile.tag-set.v1"
	RulesSchema    = "profile.query-rules.v1"
)

type Kind string

const (
	Source  Kind = "source"
	Concept Kind = "concept"
)

type Judgment string

const (
	Match         Judgment = "match"
	NoMatch       Judgment = "no_match"
	Unknown       Judgment = "unknown"
	NotApplicable Judgment = "not_applicable"
)

type Tag struct {
	ID           string `json:"id"`
	Definition   string `json:"definition"`
	AppliesTo    []Kind `json:"applies_to"`
	MatchRule    string `json:"match_rule"`
	NonMatchRule string `json:"non_match_rule"`
	UnknownRule  string `json:"unknown_rule"`
	QueryUse     string `json:"query_use"`
}
type Dictionary struct {
	Revision string
	Tags     []Tag
}
type Item struct {
	Kind     Kind
	StableID string
	Content  []byte
}

// Inventory must be supplied from one validated generation and retained source snapshots.
type Inventory struct {
	ContentGeneration    string
	ConceptsDigest       string
	IDMapDigest          string
	SourceSnapshotDigest string
	Items                []Item
}
type ProviderPolicy struct {
	ConfiguredModel        string
	AcceptedReturnedModels []string
	PromptVersion          string
	SchemaVersion          string
}
type QualityPolicy struct {
	Revision             string
	ApprovedRequiredTags []string
}

type Decision struct {
	SchemaVersion   string   `json:"schema_version"`
	Kind            Kind     `json:"kind"`
	StableID        string   `json:"stable_id"`
	ContentDigest   string   `json:"content_digest"`
	InputDigest     string   `json:"input_digest"`
	RuleDigest      string   `json:"rule_digest"`
	TagID           string   `json:"tag_id"`
	Judgment        Judgment `json:"judgment"`
	ConfiguredModel string   `json:"configured_model"`
	ReturnedModel   string   `json:"returned_model"`
	PromptVersion   string   `json:"prompt_version"`
	Evidence        string   `json:"evidence"`
	Confidence      *float64 `json:"confidence"`
}
type Row struct {
	Kind               Kind     `json:"kind"`
	StableID           string   `json:"stable_id"`
	ContentDigest      string   `json:"content_digest"`
	InputDigest        string   `json:"input_digest"`
	RuleDigest         string   `json:"rule_digest"`
	TagID              string   `json:"tag_id"`
	DictionaryRevision string   `json:"dictionary_revision"`
	DecisionRevision   string   `json:"decision_revision"`
	Judgment           Judgment `json:"judgment"`
}
type TagSet struct {
	SchemaVersion        string `json:"schema_version"`
	ContentGeneration    string `json:"content_generation"`
	DictionaryRevision   string `json:"dictionary_revision"`
	ConceptsDigest       string `json:"concepts_digest"`
	IDMapDigest          string `json:"id_map_digest"`
	SourceSnapshotDigest string `json:"source_snapshot_digest"`
	ExpectedCount        int    `json:"expected_count"`
	CompletedCount       int    `json:"completed_count"`
	MissingCount         int    `json:"missing_count"`
	Rows                 []Row  `json:"rows"`
}
type Rule struct {
	TagID           string `json:"tag_id"`
	Kind            Kind   `json:"kind"`
	RuleDigest      string `json:"rule_digest"`
	QueryUse        string `json:"query_use"`
	HardGateEnabled bool   `json:"hard_gate_enabled"`
	ReasonCode      string `json:"reason_code"`
}
type QueryRules struct {
	SchemaVersion         string `json:"schema_version"`
	ContentGeneration     string `json:"content_generation"`
	DictionaryRevision    string `json:"dictionary_revision"`
	TagSetRevision        string `json:"tag_set_revision"`
	QualityPolicyRevision string `json:"quality_policy_revision"`
	Rules                 []Rule `json:"rules"`
}
type ActiveRef struct{ ContentGeneration, DictionaryRevision, TagSetRevision, QueryRuleRevision string }
type Bundle struct {
	Set       TagSet
	Rules     QueryRules
	Decisions []Decision
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == hex.EncodeToString(b)
}
func validKind(k Kind) bool { return k == Source || k == Concept }
func validJudgment(j Judgment) bool {
	return j == Match || j == NoMatch || j == Unknown || j == NotApplicable
}
func encode(v any) ([]byte, string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, "", e
	}
	if len(b) > MaxObjectBytes {
		return nil, "", errors.New("profile tag object too large")
	}
	return b, digest(b), nil
}

// StrictDecode rejects duplicate/unknown keys, malformed UTF-8, and trailing data recursively.
func StrictDecode(data []byte, v any) error {
	if len(data) > MaxObjectBytes {
		return errors.New("profile tag object too large")
	}
	if !utf8.Valid(data) {
		return errors.New("invalid utf8")
	}
	if err := checkUnique(bytes.NewReader(data)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func checkUnique(r io.Reader) error {
	d := json.NewDecoder(r)
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok {
					return errors.New("invalid key")
				}
				if seen[s] {
					return fmt.Errorf("duplicate key %q", s)
				}
				seen[s] = true
				if e = walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case '[':
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
			_, e := d.Token()
			return e
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func validatePolicy(p ProviderPolicy) error {
	if p.ConfiguredModel == "" || p.PromptVersion == "" || p.SchemaVersion == "" || len(p.AcceptedReturnedModels) == 0 {
		return errors.New("incomplete provider policy")
	}
	seen := map[string]bool{}
	for _, s := range p.AcceptedReturnedModels {
		if s == "" || seen[s] {
			return errors.New("invalid returned model policy")
		}
		seen[s] = true
	}
	if !sort.StringsAreSorted(p.AcceptedReturnedModels) {
		return errors.New("returned model policy must be sorted")
	}
	return nil
}
func RuleDigest(t Tag, k Kind, p ProviderPolicy) (string, error) {
	if err := validatePolicy(p); err != nil {
		return "", err
	}
	if !validKind(k) || t.ID == "" || t.Definition == "" || t.MatchRule == "" || t.NonMatchRule == "" || t.UnknownRule == "" {
		return "", errors.New("invalid tag rule")
	}
	b, e := json.Marshal([]any{"profile.tag-rule.v1", t.ID, k, t.Definition, t.MatchRule, t.NonMatchRule, t.UnknownRule, p.ConfiguredModel, p.AcceptedReturnedModels, p.PromptVersion, p.SchemaVersion})
	if e != nil {
		return "", e
	}
	return digest(b), nil
}
func inputFor(i Item) ([]byte, string, string, error) {
	if !validKind(i.Kind) || i.StableID == "" || !utf8.Valid(i.Content) {
		return nil, "", "", errors.New("invalid inventory item")
	}
	cd := digest(i.Content)
	b, e := json.Marshal([]string{string(i.Kind), i.StableID, cd, string(i.Content)})
	if e != nil {
		return nil, "", "", e
	}
	return b, cd, digest(b), nil
}
