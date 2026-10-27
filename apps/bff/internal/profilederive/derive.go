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

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/observability"
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
	text, model, err := chatWithProviderSpan(ctx, p.client, bootstrapSystemPrompt, string(userMessage))
	if err != nil {
		return BootstrapResult{}, withFailureDiagnostic(FailureStageBootstrapProvider, providerFailureClass(err), fmt.Errorf("bootstrap provider call: %w", err))
	}
	if err := observeProfilePhase(ctx, "profile.derive.model", func() error {
		if !validReturnedModel(model) {
			return errors.New("provider_model_unreported")
		}
		return nil
	}); err != nil {
		return BootstrapResult{}, withFailureDiagnostic(FailureStageBootstrapModel, FailureClassModelUnreported, errors.New("provider_model_unreported"))
	}
	var output BootstrapOutput
	if err := observeProfilePhase(ctx, "profile.derive.output_decode", func() error { return decodeModelJSON([]byte(text), &output) }); err != nil {
		return BootstrapResult{}, withFailureDiagnostic(FailureStageBootstrapDecode, FailureClassModelOutputDecodeFailed, fmt.Errorf("invalid bootstrap derivation output: %w", err))
	}
	if err := observeProfilePhase(ctx, "profile.derive.accounting", func() error {
		return validateAccounting(output.Requirements, requirements, false, output.CompileGuidance, nil)
	}); err != nil {
		return BootstrapResult{}, withFailureDiagnostic(FailureStageBootstrapValidate, FailureClassModelAccountingInvalid, err)
	}
	envelope := profileartifacts.BootstrapGuidanceEnvelope{
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: revision, InputDigest: inputDigest,
		ModelVersion: model, PromptVersion: BootstrapPromptVersion, CompileGuidance: output.CompileGuidance,
	}
	var data []byte
	var ref profileartifacts.BootstrapGuidanceRef
	err = observeProfilePhase(ctx, "profile.derive.artifact_encode", func() error {
		var encodeErr error
		data, ref, encodeErr = profileartifacts.EncodeBootstrapGuidance(envelope)
		return encodeErr
	})
	if err != nil {
		return BootstrapResult{}, withFailureDiagnostic(FailureStageBootstrapEncode, FailureClassArtifactEncodeFailed, err)
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
	text, model, err := chatWithProviderSpan(ctx, p.client, manualSystemPrompt, string(userMessage))
	if err != nil {
		return ManualResult{}, withFailureDiagnostic(FailureStageManualProvider, providerFailureClass(err), fmt.Errorf("manual provider call: %w", err))
	}
	if err := observeProfilePhase(ctx, "profile.derive.model", func() error {
		if !validReturnedModel(model) {
			return errors.New("provider_model_unreported")
		}
		return nil
	}); err != nil {
		return ManualResult{}, withFailureDiagnostic(FailureStageManualModel, FailureClassModelUnreported, errors.New("provider_model_unreported"))
	}
	var output ManualOutput
	if err := observeProfilePhase(ctx, "profile.derive.output_decode", func() error { return decodeModelJSON([]byte(text), &output) }); err != nil {
		return ManualResult{}, withManualDecodeFailure(err)
	}
	if err := observeProfilePhase(ctx, "profile.derive.accounting", func() error {
		return validateAccounting(output.Requirements, requirements, true, output.CompileGuidance, output.Tags)
	}); err != nil {
		return ManualResult{}, withFailureDiagnostic(FailureStageManualValidate, FailureClassModelAccountingInvalid, err)
	}
	inputDigest := RequirementsDigest(requirements)
	sortTags(output.Tags, requirements)
	dictionary := profileartifacts.DictionaryEnvelope{
		SchemaVersion: profileartifacts.DictionarySchema, InputDigest: inputDigest, ContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: ManualPromptVersion,
		Tags: output.Tags,
	}
	guidance := profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: inputDigest, SourceContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: ManualPromptVersion,
		CompileGuidance: output.CompileGuidance,
	}
	var dictionaryData, guidanceData []byte
	var dictionaryRef profileartifacts.DerivedRef
	var guidanceRef profileartifacts.DerivedRef
	err = observeProfilePhase(ctx, "profile.derive.artifact_encode", func() error {
		var encodeErr error
		dictionaryData, dictionaryRef, encodeErr = profileartifacts.EncodeDictionary(dictionary, requirementIDs(requirements))
		if encodeErr != nil {
			return encodeErr
		}
		guidanceData, guidanceRef, encodeErr = profileartifacts.EncodeGuidance(guidance)
		return encodeErr
	})
	if err != nil {
		return ManualResult{}, withFailureDiagnostic(FailureStageManualEncode, FailureClassArtifactEncodeFailed, err)
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
	text, model, err := chatWithProviderSpan(ctx, p.client, compileAutoSystemPrompt, string(userMessage))
	if err != nil {
		return CompileAutoResult{}, fmt.Errorf("compile-auto provider call: %w", err)
	}
	if err := observeProfilePhase(ctx, "profile.derive.model", func() error {
		if !validReturnedModel(model) {
			return errors.New("provider_model_unreported")
		}
		return nil
	}); err != nil {
		return CompileAutoResult{}, errors.New("provider_model_unreported")
	}
	var output DictionaryOutput
	if err := observeProfilePhase(ctx, "profile.derive.output_decode", func() error { return decodeModelJSON([]byte(text), &output) }); err != nil {
		return CompileAutoResult{}, fmt.Errorf("invalid compile-auto derivation output: %w", err)
	}
	if err := observeProfilePhase(ctx, "profile.derive.accounting", func() error {
		return validateAccounting(output.Requirements, requirements, true, "compile guidance is intentionally unchanged", output.Tags)
	}); err != nil {
		return CompileAutoResult{}, err
	}
	sortTags(output.Tags, requirements)
	envelope := profileartifacts.DictionaryEnvelope{
		SchemaVersion: profileartifacts.DictionarySchema, InputDigest: RequirementsDigest(requirements), ContentGeneration: generation,
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: model, PromptVersion: CompileAutoPromptVersion, Tags: output.Tags,
	}
	var data []byte
	var ref profileartifacts.DerivedRef
	err = observeProfilePhase(ctx, "profile.derive.artifact_encode", func() error {
		var encodeErr error
		data, ref, encodeErr = profileartifacts.EncodeDictionary(envelope, requirementIDs(requirements))
		return encodeErr
	})
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
		return &classifiedModelOutputDecodeError{
			reason: errModelOutputInvalidUTF8,
			cause:  errors.New("provider output is not valid UTF-8"),
		}
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
			err = errors.New("provider output has trailing data")
		}
		return &classifiedModelOutputDecodeError{reason: errModelOutputTrailingData, cause: err}
	}
	return nil
}

func observeProfilePhase(ctx context.Context, name string, run func() error) error {
	_, span := observability.StartProfileSpan(ctx, name)
	err := run()
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	stage := "unknown"
	switch name {
	case "profile.derive.model":
		stage = "model"
	case "profile.derive.output_decode":
		stage = "output_decode"
	case "profile.derive.accounting":
		stage = "accounting"
	case "profile.derive.artifact_encode":
		stage = "artifact_encode"
	}
	observability.SetProfileStage(span, stage, outcome)
	observability.SetProfileOutcome(span, "profile.provider.outcome", outcome)
	observability.FinishProfileSpan(span, outcome)
	return err
}

func chatWithProviderSpan(ctx context.Context, client ChatClient, systemPrompt, userMessage string) (string, string, error) {
	callCtx, span := observability.StartProfileSpan(ctx, "profile.derive.provider_call")
	selected := client
	if projectClient, ok := llm.ProjectChatClientFromContext(callCtx); ok {
		if metadataClient, ok := projectClient.(ChatClient); ok {
			selected = metadataClient
		}
	}
	text, model, err := selected.ChatWithMetadata(callCtx, systemPrompt, userMessage)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	observability.SetProfileOutcome(span, "profile.provider.outcome", outcome)
	observability.FinishProfileSpan(span, outcome)
	return text, model, err
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("provider output has trailing data")
		}
		return &classifiedModelOutputDecodeError{reason: errModelOutputTrailingData, cause: err}
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
				return &classifiedModelOutputDecodeError{
					reason: errModelOutputDuplicateKey,
					cause:  fmt.Errorf("duplicate provider output key %q", name),
				}
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

var (
	errModelOutputInvalidUTF8  = errors.New("provider output is not valid UTF-8")
	errModelOutputDuplicateKey = errors.New("provider output contains a duplicate key")
	errModelOutputTrailingData = errors.New("provider output has trailing data")
)

type classifiedModelOutputDecodeError struct {
	reason error
	cause  error
}

func (e *classifiedModelOutputDecodeError) Error() string { return e.cause.Error() }
func (e *classifiedModelOutputDecodeError) Unwrap() error { return e.cause }
func (e *classifiedModelOutputDecodeError) Is(target error) bool {
	return target == e.reason
}

func withManualDecodeFailure(cause error) error {
	diagnostic := FailureDiagnostic{
		Stage:        FailureStageManualDecode,
		Class:        FailureClassModelOutputDecodeFailed,
		DecodeReason: manualDecodeReason(cause),
	}
	return withFailureDiagnosticValue(diagnostic, fmt.Errorf("invalid manual derivation output: %w", cause))
}

func manualDecodeReason(err error) string {
	switch {
	case errors.Is(err, errModelOutputInvalidUTF8):
		return DecodeReasonInvalidUTF8
	case errors.Is(err, errModelOutputDuplicateKey):
		return DecodeReasonDuplicateKey
	case errors.Is(err, errModelOutputTrailingData):
		return DecodeReasonTrailingData
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return DecodeReasonTypeMismatch
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return DecodeReasonInvalidJSON
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return DecodeReasonInvalidJSON
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return DecodeReasonUnknownField
	}
	return DecodeReasonOther
}

const manualSystemPrompt = `Derive a Profile dictionary and standalone compile writing guidance from the complete ordered requirements and the supplied canonical active concept context. Do not use prior artifacts. Every requirement must have exactly one accounting row in the same order, shaped as {id: string, disposition: string, explanation: string}; disposition must be exactly compile_guidance, dictionary_or_query, both, or limitation. Include compile_guidance only for writing/structure intent; never put Tag detection, query filtering, ranking, or provider rules in it. Tag/query requirements must be represented by dictionary tags or an explicit limitation. Tags use stable IDs matching [a-z][a-z0-9_]{0,63}, nonempty definition/match_rule/non_match_rule/unknown_rule, applies_to as source then concept, requirement_ids in input order, and query_use preferred or required_candidate. Return one strict JSON object with exactly these fields: tags (array of tag objects), compile_guidance (string), dictionary_diff (string), guidance_diff (string), requirements (array of accounting rows). Each tag object has exactly these fields and types: id (string), definition (string), applies_to (array containing source and/or concept), match_rule (string), non_match_rule (string), unknown_rule (string), requirement_ids (array of input requirement IDs), query_use (preferred or required_candidate). Include every input requirement ID exactly once in requirements. No Markdown fences.`

const compileAutoSystemPrompt = `Derive only a new dictionary for this successful content generation from the complete ordered requirements and supplied canonical active concept context. Writing guidance is intentionally unchanged. Do not reuse prior dictionary rules or invent concepts. Every requirement must have exactly one accounting row in the same order, shaped as {id: string, disposition: string, explanation: string}; disposition must be exactly compile_guidance, dictionary_or_query, both, or limitation. Tag/query intent must be represented by dictionary tags or an explicit limitation, and compile-only intent must remain accounted as compile_guidance without changing writing guidance. Tags use stable IDs matching [a-z][a-z0-9_]{0,63}, nonempty definition/match_rule/non_match_rule/unknown_rule, applies_to as source then concept, requirement_ids in input order, and query_use preferred or required_candidate. Return one strict JSON object with exactly these fields: tags (array of tag objects), dictionary_diff (string), requirements (array of accounting rows). Each tag object has exactly these fields and types: id (string), definition (string), applies_to (array containing source and/or concept), match_rule (string), non_match_rule (string), unknown_rule (string), requirement_ids (array of input requirement IDs), query_use (preferred or required_candidate). Include every input requirement ID exactly once in requirements. No Markdown fences.`
