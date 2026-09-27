// Package profilederive builds immutable Profile dictionary/guidance artifacts
// from the full ordered requirements and one validated concept snapshot.
package profilederive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
)

const (
	BootstrapPromptVersion   = "profile-bootstrap-v1"
	ManualPromptVersion      = "profile-manual-v1"
	CompileAutoPromptVersion = "profile-compile-auto-v1"
)

type Requirement struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type RequirementAccounting struct {
	ID          string `json:"id"`
	Disposition string `json:"disposition"`
	Explanation string `json:"explanation"`
}

type Preview struct {
	DictionaryDiff string
	GuidanceDiff   string
	Requirements   []RequirementAccounting
}

type BootstrapOutput struct {
	CompileGuidance string                  `json:"compile_guidance"`
	GuidanceDiff    string                  `json:"guidance_diff"`
	Requirements    []RequirementAccounting `json:"requirements"`
}

type ManualOutput struct {
	Tags            []profileartifacts.Tag  `json:"tags"`
	CompileGuidance string                  `json:"compile_guidance"`
	DictionaryDiff  string                  `json:"dictionary_diff"`
	GuidanceDiff    string                  `json:"guidance_diff"`
	Requirements    []RequirementAccounting `json:"requirements"`
}

type DictionaryOutput struct {
	Tags           []profileartifacts.Tag  `json:"tags"`
	DictionaryDiff string                  `json:"dictionary_diff"`
	Requirements   []RequirementAccounting `json:"requirements"`
}

type Concept struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Title       string          `json:"title"`
	Frontmatter json.RawMessage `json:"frontmatter"`
	Row         string          `json:"row"`
}

// ChatClient is the existing configured LLM transport. Returned model
// provenance comes only from the current provider response, never a fallback.
type ChatClient interface {
	ChatWithMetadata(context.Context, string, string) (string, string, error)
}

type Provider struct{ client ChatClient }

func NewProvider(client ChatClient) *Provider { return &Provider{client: client} }

type BootstrapResult struct {
	Data    []byte
	Ref     profileartifacts.BootstrapGuidanceRef
	Preview Preview
}

type ManualResult struct {
	DictionaryData []byte
	GuidanceData   []byte
	DictionaryRef  profileartifacts.DerivedRef
	GuidanceRef    profileartifacts.DerivedRef
	Preview        Preview
}

type CompileAutoResult struct {
	DictionaryData []byte
	DictionaryRef  profileartifacts.DerivedRef
	Preview        Preview
}

func (p *Provider) DeriveBootstrap(ctx context.Context, revision int64, requirements []Requirement) (BootstrapResult, error) {
	if p == nil || p.client == nil || revision < 1 {
		return BootstrapResult{}, errors.New("Profile bootstrap provider unavailable")
	}
	if err := validateRequirements(requirements); err != nil {
		return BootstrapResult{}, err
	}
	inputDigest := RequirementsDigest(requirements)
	request := struct {
		ProfileRevision int64         `json:"profile_revision"`
		InputDigest     string        `json:"input_digest"`
		Requirements    []Requirement `json:"requirements"`
	}{revision, inputDigest, requirements}
	userMessage, err := json.Marshal(request)
	if err != nil {
		return BootstrapResult{}, err
	}
	text, model, err := p.client.ChatWithMetadata(ctx, bootstrapSystemPrompt, string(userMessage))
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap provider call: %w", err)
	}
	if !validReturnedModel(model) {
		return BootstrapResult{}, errors.New("provider_model_unreported")
	}
	var output BootstrapOutput
	if err := decodeModelJSON([]byte(text), &output); err != nil {
		return BootstrapResult{}, fmt.Errorf("invalid bootstrap derivation output: %w", err)
	}
	if err := validateAccounting(output.Requirements, requirements, false, output.CompileGuidance, nil); err != nil {
		return BootstrapResult{}, err
	}
	envelope := profileartifacts.BootstrapGuidanceEnvelope{
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: revision, InputDigest: inputDigest,
		ModelVersion: model, PromptVersion: BootstrapPromptVersion, CompileGuidance: output.CompileGuidance,
	}
	data, ref, err := profileartifacts.EncodeBootstrapGuidance(envelope)
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{Data: data, Ref: ref, Preview: Preview{GuidanceDiff: output.GuidanceDiff, Requirements: output.Requirements}}, nil
}

func (p *Provider) DeriveManual(ctx context.Context, revision int64, requirements []Requirement, generation, conceptsDigest string, concepts []Concept) (ManualResult, error) {
	if p == nil || p.client == nil {
		return ManualResult{}, errors.New("Profile derivation provider unavailable")
	}
	if err := validateRequirements(requirements); err != nil {
		return ManualResult{}, err
	}
	if revision < 1 || generation == "" || !profileartifacts.IsSHA256(conceptsDigest) {
		return ManualResult{}, errors.New("invalid Profile derivation identity")
	}
	if len(requirements) == 0 {
		return DeriveNeutral(revision, requirements, generation, conceptsDigest)
	}
	request := struct {
		ProfileRevision         int64         `json:"profile_revision"`
		InputDigest             string        `json:"input_digest"`
		ContentGeneration       string        `json:"content_generation"`
		CanonicalConceptsDigest string        `json:"canonical_concepts_digest"`
		Requirements            []Requirement `json:"requirements"`
		Concepts                []Concept     `json:"concepts"`
	}{revision, RequirementsDigest(requirements), generation, conceptsDigest, requirements, concepts}
	userMessage, err := json.Marshal(request)
	if err != nil {
		return ManualResult{}, err
	}
	text, model, err := p.client.ChatWithMetadata(ctx, manualSystemPrompt, string(userMessage))
	if err != nil {
		return ManualResult{}, fmt.Errorf("manual provider call: %w", err)
	}
	if !validReturnedModel(model) {
		return ManualResult{}, errors.New("provider_model_unreported")
	}
	var output ManualOutput
	if err := decodeModelJSON([]byte(text), &output); err != nil {
		return ManualResult{}, fmt.Errorf("invalid manual derivation output: %w", err)
	}
	if err := validateAccounting(output.Requirements, requirements, true, output.CompileGuidance, output.Tags); err != nil {
		return ManualResult{}, err
	}
	inputDigest := RequirementsDigest(requirements)
	sortTags(output.Tags, requirements)
	dictionary := profileartifacts.DictionaryEnvelope{
		SchemaVersion: profileartifacts.DictionarySchema, InputDigest: inputDigest, ContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: ManualPromptVersion,
		Tags: output.Tags,
	}
	dictionaryData, dictionaryRef, err := profileartifacts.EncodeDictionary(dictionary, requirementIDs(requirements))
	if err != nil {
		return ManualResult{}, err
	}
	guidance := profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: inputDigest, SourceContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: ManualPromptVersion,
		CompileGuidance: output.CompileGuidance,
	}
	guidanceData, guidanceRef, err := profileartifacts.EncodeGuidance(guidance)
	if err != nil {
		return ManualResult{}, err
	}
	preview := Preview{DictionaryDiff: output.DictionaryDiff, GuidanceDiff: output.GuidanceDiff, Requirements: output.Requirements}
	return ManualResult{DictionaryData: dictionaryData, GuidanceData: guidanceData, DictionaryRef: dictionaryRef, GuidanceRef: guidanceRef, Preview: preview}, nil
}

func (p *Provider) DeriveCompileAuto(ctx context.Context, revision int64, requirements []Requirement, generation, conceptsDigest string, concepts []Concept) (CompileAutoResult, error) {
	if p == nil || p.client == nil {
		return CompileAutoResult{}, errors.New("Profile derivation provider unavailable")
	}
	if err := validateRequirements(requirements); err != nil {
		return CompileAutoResult{}, err
	}
	if revision < 1 || generation == "" || !profileartifacts.IsSHA256(conceptsDigest) {
		return CompileAutoResult{}, errors.New("invalid Profile compile-auto identity")
	}
	if len(requirements) == 0 {
		neutral, err := DeriveNeutral(revision, requirements, generation, conceptsDigest)
		if err != nil {
			return CompileAutoResult{}, err
		}
		return CompileAutoResult{DictionaryData: neutral.DictionaryData, DictionaryRef: neutral.DictionaryRef, Preview: neutral.Preview}, nil
	}
	request := struct {
		ProfileRevision         int64         `json:"profile_revision"`
		InputDigest             string        `json:"input_digest"`
		ContentGeneration       string        `json:"content_generation"`
		CanonicalConceptsDigest string        `json:"canonical_concepts_digest"`
		Requirements            []Requirement `json:"requirements"`
		Concepts                []Concept     `json:"concepts"`
	}{revision, RequirementsDigest(requirements), generation, conceptsDigest, requirements, concepts}
	userMessage, err := json.Marshal(request)
	if err != nil {
		return CompileAutoResult{}, err
	}
	text, model, err := p.client.ChatWithMetadata(ctx, compileAutoSystemPrompt, string(userMessage))
	if err != nil {
		return CompileAutoResult{}, fmt.Errorf("compile-auto provider call: %w", err)
	}
	if !validReturnedModel(model) {
		return CompileAutoResult{}, errors.New("provider_model_unreported")
	}
	var output DictionaryOutput
	if err := decodeModelJSON([]byte(text), &output); err != nil {
		return CompileAutoResult{}, fmt.Errorf("invalid compile-auto derivation output: %w", err)
	}
	if err := validateAccounting(output.Requirements, requirements, true, "compile guidance is intentionally unchanged", output.Tags); err != nil {
		return CompileAutoResult{}, err
	}
	sortTags(output.Tags, requirements)
	envelope := profileartifacts.DictionaryEnvelope{
		SchemaVersion: profileartifacts.DictionarySchema, InputDigest: RequirementsDigest(requirements), ContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: CompileAutoPromptVersion, Tags: output.Tags,
	}
	data, ref, err := profileartifacts.EncodeDictionary(envelope, requirementIDs(requirements))
	if err != nil {
		return CompileAutoResult{}, err
	}
	return CompileAutoResult{DictionaryData: data, DictionaryRef: ref, Preview: Preview{DictionaryDiff: output.DictionaryDiff, Requirements: output.Requirements}}, nil
}

func DeriveNeutral(revision int64, requirements []Requirement, generation, conceptsDigest string) (ManualResult, error) {
	if revision < 1 || len(requirements) != 0 || generation == "" || !profileartifacts.IsSHA256(conceptsDigest) {
		return ManualResult{}, errors.New("invalid neutral Profile derivation identity")
	}
	inputDigest := RequirementsDigest(requirements)
	dictionary := profileartifacts.DictionaryEnvelope{
		SchemaVersion: profileartifacts.DictionarySchema, InputDigest: inputDigest, ContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: "neutral-v1", PromptVersion: "neutral-v1", Tags: []profileartifacts.Tag{},
	}
	dictionaryData, dictionaryRef, err := profileartifacts.EncodeDictionary(dictionary, nil)
	if err != nil {
		return ManualResult{}, err
	}
	guidance := profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: inputDigest, SourceContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: "neutral-v1", PromptVersion: "neutral-v1", CompileGuidance: "",
	}
	guidanceData, guidanceRef, err := profileartifacts.EncodeGuidance(guidance)
	if err != nil {
		return ManualResult{}, err
	}
	return ManualResult{
		DictionaryData: dictionaryData, GuidanceData: guidanceData, DictionaryRef: dictionaryRef, GuidanceRef: guidanceRef,
		Preview: Preview{DictionaryDiff: "No dictionary rules; all saved requirements are explicitly cleared.", GuidanceDiff: "No Profile-specific compile guidance; base Synto guidance remains.", Requirements: []RequirementAccounting{}},
	}, nil
}

func RequirementsDigest(requirements []Requirement) string {
	data, _ := json.Marshal(requirements)
	return profileartifacts.SHA256(data)
}

func sortTags(tags []profileartifacts.Tag, requirements []Requirement) {
	order := make(map[string]int, len(requirements))
	for i, requirement := range requirements {
		order[requirement.ID] = i
	}
	for i := range tags {
		sort.Slice(tags[i].AppliesTo, func(a, b int) bool {
			return scopeOrder(tags[i].AppliesTo[a]) < scopeOrder(tags[i].AppliesTo[b])
		})
		sort.Slice(tags[i].RequirementIDs, func(a, b int) bool {
			return order[tags[i].RequirementIDs[a]] < order[tags[i].RequirementIDs[b]]
		})
	}
	sort.Slice(tags, func(a, b int) bool { return tags[a].ID < tags[b].ID })
}

func scopeOrder(scope string) int {
	if scope == "source" {
		return 0
	}
	if scope == "concept" {
		return 1
	}
	return 2
}

func requirementIDs(requirements []Requirement) []string {
	ids := make([]string, len(requirements))
	for i, requirement := range requirements {
		ids[i] = requirement.ID
	}
	return ids
}

func validateRequirements(requirements []Requirement) error {
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		if strings.TrimSpace(requirement.ID) == "" || !utf8.ValidString(requirement.ID) || !utf8.ValidString(requirement.Text) {
			return errors.New("invalid Profile requirement")
		}
		if _, ok := seen[requirement.ID]; ok {
			return errors.New("duplicate Profile requirement ID")
		}
		seen[requirement.ID] = struct{}{}
	}
	return nil
}

func validateAccounting(rows []RequirementAccounting, requirements []Requirement, includeTags bool, guidance string, tags []profileartifacts.Tag) error {
	if len(rows) != len(requirements) {
		return errors.New("derivation output does not account for every requirement")
	}
	tagRequirements := make(map[string]bool)
	for _, tag := range tags {
		for _, id := range tag.RequirementIDs {
			tagRequirements[id] = true
		}
	}
	for index, requirement := range requirements {
		row := rows[index]
		if row.ID != requirement.ID || strings.TrimSpace(row.Explanation) == "" {
			return fmt.Errorf("invalid accounting row for requirement %q", requirement.ID)
		}
		if !validDisposition(row.Disposition) {
			return fmt.Errorf("invalid disposition for requirement %q", requirement.ID)
		}
		usesGuidance := row.Disposition == "compile_guidance" || row.Disposition == "both"
		usesDictionary := row.Disposition == "dictionary_or_query" || row.Disposition == "both"
		if usesGuidance && strings.TrimSpace(guidance) == "" {
			return fmt.Errorf("requirement %q claims writing guidance but compile guidance is empty", requirement.ID)
		}
		if includeTags && usesDictionary && !tagRequirements[requirement.ID] {
			return fmt.Errorf("requirement %q claims dictionary/query coverage without a tag", requirement.ID)
		}
		if row.Disposition == "limitation" && !strings.Contains(strings.ToLower(row.Explanation), "limit") && !strings.Contains(strings.ToLower(row.Explanation), "cannot") && !strings.Contains(strings.ToLower(row.Explanation), "could not") {
			return fmt.Errorf("limitation for requirement %q is not explained as a limitation", requirement.ID)
		}
	}
	return nil
}

func validDisposition(value string) bool {
	return value == "compile_guidance" || value == "dictionary_or_query" || value == "both" || value == "limitation"
}

func validReturnedModel(model string) bool {
	if strings.TrimSpace(model) == "" || len(model) > 256 || !utf8.ValidString(model) {
		return false
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func decodeModelJSON(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("provider output is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("provider output has trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("provider output has trailing data")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("provider output object key is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate provider output key %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid provider output JSON delimiter")
	}
}

const bootstrapSystemPrompt = `You derive only compile-time writing guidance from project requirements. Do not invent or assume a content corpus. Consider every ordered requirement and preserve its intent in exactly one accounting row, in the same order: compile_guidance when it affects writing, dictionary_or_query when it is reserved for later tagging/search, both when it affects both, or limitation when it cannot be represented (explain why). Only copy writing or structure instructions into compile_guidance. Never copy Tag detection, query filters, ranking, or provider decision rules into compile_guidance. Return strict JSON with exactly: compile_guidance (string), guidance_diff (string), requirements (array of {id,disposition,explanation}). Include every input requirement ID exactly once. No Markdown fences.`

const manualSystemPrompt = `Derive a Profile dictionary and standalone compile writing guidance from the complete ordered requirements and the supplied canonical active concept context. Do not use prior artifacts. Every requirement must have exactly one ordered accounting row. Include compile_guidance only for writing/structure intent; never put Tag detection, query filtering, ranking, or provider rules in it. Tag/query requirements must be represented by dictionary tags or an explicit limitation. Tags use stable IDs matching [a-z][a-z0-9_]{0,63}, nonempty definition/match_rule/non_match_rule/unknown_rule, applies_to as source then concept, requirement_ids in input order, and query_use preferred or required_candidate. Return strict JSON with exactly: tags, compile_guidance, dictionary_diff, guidance_diff, requirements; each tag uses fields id,definition,applies_to,match_rule,non_match_rule,unknown_rule,requirement_ids,query_use. Include every input requirement ID exactly once in requirements. No Markdown fences.`

const compileAutoSystemPrompt = `Derive only a new dictionary for this successful content generation from the complete ordered requirements and supplied canonical active concept context. Writing guidance is intentionally unchanged. Do not reuse prior dictionary rules or invent concepts. Every requirement must have exactly one ordered accounting row; Tag/query intent must be represented by dictionary tags or an explicit limitation, and compile-only intent must remain accounted as compile_guidance without changing writing guidance. Tags use stable IDs matching [a-z][a-z0-9_]{0,63}, nonempty definition/match_rule/non_match_rule/unknown_rule, applies_to as source then concept, requirement_ids in input order, and query_use preferred or required_candidate. Return strict JSON with exactly: tags, dictionary_diff, requirements; each tag uses fields id,definition,applies_to,match_rule,non_match_rule,unknown_rule,requirement_ids,query_use. Include every input requirement ID exactly once in requirements. No Markdown fences.`
