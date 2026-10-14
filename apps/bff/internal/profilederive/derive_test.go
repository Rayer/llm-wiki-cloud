package profilederive

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type fakeChat struct {
	text   string
	model  string
	err    error
	calls  int
	system string
	user   string
}

func (f *fakeChat) ChatWithMetadata(_ context.Context, system, user string) (string, string, error) {
	f.calls++
	f.system, f.user = system, user
	return f.text, f.model, f.err
}

func TestDeriveBootstrapPreservesAllRequirementsAndReturnedModel(t *testing.T) {
	fake := &fakeChat{model: "deepseek-flash-provider-response-v4", text: `{"compile_guidance":"Write concise standalone entries.","guidance_diff":"Added concise entry guidance.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied to generated notes."},{"id":"r2","disposition":"dictionary_or_query","explanation":"Reserved for later tagging and search."},{"id":"r3","disposition":"limitation","explanation":"Cannot interpret this without a unit."}]}`}
	provider := NewProvider(fake)
	requirements := []Requirement{{ID: "r1", Text: "  concise entries  "}, {ID: "r2", Text: "family friendly for search"}, {ID: "r3", Text: "nearby"}}
	result, err := provider.DeriveBootstrap(context.Background(), 5, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || !strings.Contains(fake.user, `"id":"r1","text":"  concise entries  "`) || !strings.Contains(fake.user, `"id":"r3","text":"nearby"`) {
		t.Fatalf("provider calls=%d request=%s", fake.calls, fake.user)
	}
	envelope, err := profileartifacts.ValidateBootstrapGuidance(result.Data, result.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.ProfileRevision != 5 || envelope.InputDigest != RequirementsDigest(requirements) || envelope.ModelVersion != fake.model || envelope.CompileGuidance != "Write concise standalone entries." {
		t.Fatalf("bootstrap envelope = %+v", envelope)
	}
	if len(result.Preview.Requirements) != len(requirements) || result.Preview.Requirements[1].Disposition != "dictionary_or_query" {
		t.Fatalf("preview did not account for ordered requirements: %+v", result.Preview)
	}
}

func TestMissingProviderReturnedModelFailsClosed(t *testing.T) {
	fake := &fakeChat{model: "", text: `{"compile_guidance":"g","guidance_diff":"g","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"write"}]}`}
	if _, err := NewProvider(fake).DeriveBootstrap(context.Background(), 1, []Requirement{{ID: "r1", Text: "write"}}); err == nil || !strings.Contains(err.Error(), "provider_model_unreported") {
		t.Fatalf("missing provider model error = %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want one", fake.calls)
	}
}

func TestManualDerivationEmitsOnlyValidatedSortedArtifacts(t *testing.T) {
	requirements := []Requirement{{ID: "r2", Text: "tag family-friendly places"}, {ID: "r1", Text: "write short entries"}}
	fake := &fakeChat{model: "provider-returned-version-9", text: `{"tags":[{"id":"family_friendly","definition":"Places suitable for families.","applies_to":["concept","source"],"match_rule":"Explicit family facilities are present.","non_match_rule":"Explicit evidence says family facilities are absent.","unknown_rule":"The available text is insufficient.","requirement_ids":["r2"],"query_use":"preferred"}],"compile_guidance":"Write short entries.","dictionary_diff":"Added family-friendly tag.","guidance_diff":"Kept concise note guidance.","requirements":[{"id":"r2","disposition":"dictionary_or_query","explanation":"Added a search preference tag."},{"id":"r1","disposition":"compile_guidance","explanation":"Applied to entry writing."}]}`}
	concepts := []Concept{{ID: "01JAZ5N7Y3K8M2Q4R6T9VWXABC", Slug: "place-one", Title: "Place One", Row: `{"slug":"place-one","title":"Place One","frontmatter":{"id":"01JAZ5N7Y3K8M2Q4R6T9VWXABC"}}`}}
	result, err := NewProvider(fake).DeriveManual(context.Background(), 2, requirements, "generation-2", strings.Repeat("a", 64), concepts)
	if err != nil {
		t.Fatal(err)
	}
	dictionary, err := profileartifacts.ValidateDictionary(result.DictionaryData, result.DictionaryRef, "generation-2", strings.Repeat("a", 64), []string{"r2", "r1"})
	if err != nil {
		t.Fatal(err)
	}
	guidance, err := profileartifacts.ValidateGuidance(result.GuidanceData, result.GuidanceRef)
	if err != nil {
		t.Fatal(err)
	}
	if dictionary.ModelVersion != fake.model || guidance.ModelVersion != fake.model || dictionary.Tags[0].AppliesTo[0] != "source" || dictionary.Tags[0].AppliesTo[1] != "concept" {
		t.Fatalf("dictionary/guidance = %+v %+v", dictionary, guidance)
	}
	if dictionary.Tags[0].RequirementIDs[0] != "r2" || len(result.Preview.Requirements) != 2 || fake.calls != 1 {
		t.Fatalf("unexpected manual result %+v calls=%d", result.Preview, fake.calls)
	}
	for _, contract := range []string{
		"shaped as {id: string, disposition: string, explanation: string}",
		"disposition must be exactly compile_guidance, dictionary_or_query, both, or limitation",
		"tags (array of tag objects), compile_guidance (string), dictionary_diff (string), guidance_diff (string), requirements (array of accounting rows)",
		"requirements (array of accounting rows)",
		"id (string), definition (string), applies_to (array containing source and/or concept), match_rule (string), non_match_rule (string), unknown_rule (string), requirement_ids (array of input requirement IDs), query_use (preferred or required_candidate)",
	} {
		if !strings.Contains(fake.system, contract) {
			t.Errorf("manual prompt omitted consumer contract %q", contract)
		}
	}
}

func TestManualDecodeFailuresExposeOnlyFiniteSafeReasonsAndRemainStrict(t *testing.T) {
	const validManualOutput = `{"tags":[],"compile_guidance":"Write concise entries.","dictionary_diff":"No tag changes.","guidance_diff":"Kept concise guidance.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied to entry writing."}]}`
	tests := []struct {
		name   string
		output string
		reason string
	}{
		{name: "invalid UTF-8", output: string(append([]byte(validManualOutput), 0xff)), reason: DecodeReasonInvalidUTF8},
		{name: "invalid JSON", output: `{"tags":`, reason: DecodeReasonInvalidJSON},
		{name: "duplicate key", output: strings.Replace(validManualOutput, `"tags":[]`, `"tags":[],"tags":[]`, 1), reason: DecodeReasonDuplicateKey},
		{name: "unknown private field", output: strings.Replace(validManualOutput, `"tags":[]`, `"tags":[],"private_model_field":"private model value"`, 1), reason: DecodeReasonUnknownField},
		{name: "wrong field type", output: strings.Replace(validManualOutput, `"tags":[]`, `"tags":"wrong type"`, 1), reason: DecodeReasonTypeMismatch},
		{name: "trailing JSON", output: validManualOutput + ` {"private_tail":"private tail value"}`, reason: DecodeReasonTrailingData},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := NewProvider(&fakeChat{model: "fake-manual-model-v1", text: test.output})
			_, err := provider.DeriveManual(context.Background(), 1, []Requirement{{ID: "r1", Text: "write concise"}}, "generation-1", strings.Repeat("a", 64), nil)
			if err == nil || err.Error() != "Profile derivation failed" {
				t.Fatalf("manual decode error = %v, want safe generic failure", err)
			}
			diagnostic, ok := FailureDiagnosticFromError(err)
			want := FailureDiagnostic{Stage: FailureStageManualDecode, Class: FailureClassModelOutputDecodeFailed, DecodeReason: test.reason}
			if !ok || diagnostic != want || !diagnostic.Valid() {
				t.Fatalf("manual decode diagnostic = %+v, ok=%v; want %+v", diagnostic, ok, want)
			}
			encoded, marshalErr := json.Marshal(diagnostic)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, privateValue := range []string{"private_model_field", "private model value", "private_tail", "private tail value"} {
				if strings.Contains(string(encoded), privateValue) || strings.Contains(err.Error(), privateValue) {
					t.Fatalf("private model output entered safe error/diagnostic: %s", encoded)
				}
			}
		})
	}
}

func TestManualDecodeReasonIsOptionalAndBoundToManualDecoderFailures(t *testing.T) {
	legacy := FailureDiagnostic{Stage: FailureStageManualDecode, Class: FailureClassModelOutputDecodeFailed}
	if !legacy.Valid() {
		t.Fatal("legacy two-label manual decoder diagnostic must remain valid")
	}
	for _, diagnostic := range []FailureDiagnostic{
		{Stage: FailureStageManualDecode, Class: FailureClassModelOutputDecodeFailed, DecodeReason: "unapproved_reason"},
		{Stage: FailureStageBootstrapDecode, Class: FailureClassModelOutputDecodeFailed, DecodeReason: DecodeReasonInvalidJSON},
		{Stage: FailureStageManualValidate, Class: FailureClassModelAccountingInvalid, DecodeReason: DecodeReasonInvalidJSON},
	} {
		if diagnostic.Valid() {
			t.Errorf("unsafe decoder diagnostic accepted: %+v", diagnostic)
		}
	}
}

func TestCompileAutoCreatesDictionaryOnlyAndNeutralUsesNoProvider(t *testing.T) {
	fake := &fakeChat{model: "fake-compile-model", text: `{"tags":[],"dictionary_diff":"No tag changes.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Writing guidance remains pinned unchanged."}]}`}
	result, err := NewProvider(fake).DeriveCompileAuto(context.Background(), 3, []Requirement{{ID: "r1", Text: "write concise"}}, "g3", strings.Repeat("b", 64), []Concept{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DictionaryData) == 0 || result.DictionaryRef.InputDigest != RequirementsDigest([]Requirement{{ID: "r1", Text: "write concise"}}) || fake.calls != 1 {
		t.Fatalf("compile-auto result=%+v calls=%d", result, fake.calls)
	}
	for _, contract := range []string{
		"shaped as {id: string, disposition: string, explanation: string}",
		"disposition must be exactly compile_guidance, dictionary_or_query, both, or limitation",
		"tags (array of tag objects), dictionary_diff (string), requirements (array of accounting rows)",
		"id (string), definition (string), applies_to (array containing source and/or concept), match_rule (string), non_match_rule (string), unknown_rule (string), requirement_ids (array of input requirement IDs), query_use (preferred or required_candidate)",
	} {
		if !strings.Contains(fake.system, contract) {
			t.Errorf("compile-auto prompt omitted consumer contract %q", contract)
		}
	}
	neutral, err := DeriveNeutral(4, []Requirement{}, "g4", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(neutral.DictionaryRef.Revision) != 64 || len(neutral.GuidanceRef.Revision) != 64 || fake.calls != 1 {
		t.Fatalf("neutral result=%+v calls=%d", neutral, fake.calls)
	}
}

func TestProviderOutputMustAccountForAllRequirementsAndUseStrictJSON(t *testing.T) {
	for _, output := range []string{
		`{"compile_guidance":"g","guidance_diff":"g","requirements":[]}`,
		`{"compile_guidance":"g","guidance_diff":"g","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"x"}],"extra":1}`,
		`{"compile_guidance":"g","compile_guidance":"bad","guidance_diff":"g","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"x"}]}`,
		`{"compile_guidance":"g","guidance_diff":"g","requirements":[{"id":"r1","disposition":"dictionary_or_query","explanation":"tag"}]} {}`,
	} {
		fake := &fakeChat{model: "provider-model", text: output}
		if _, err := NewProvider(fake).DeriveBootstrap(context.Background(), 1, []Requirement{{ID: "r1", Text: "x"}}); err == nil {
			t.Errorf("invalid output accepted: %s", output)
		}
	}
	if _, err := NewProvider(nil).DeriveBootstrap(context.Background(), 1, []Requirement{{ID: "r1"}}); err == nil {
		t.Fatal("nil provider accepted")
	}
	if _, err := NewProvider(&fakeChat{err: errors.New("timeout")}).DeriveBootstrap(context.Background(), 1, []Requirement{{ID: "r1"}}); err == nil {
		t.Fatal("provider failure accepted")
	}
}

func TestValidateConceptSnapshotReadsExactRowsAndRejectsIdentityDrift(t *testing.T) {
	stableID := "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	valid := []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"" + stableID + "\"}}\n")
	concepts, digest, err := ValidateConceptSnapshot(valid, wikiindex.IDMap{Concept: map[string]string{stableID: "alpha"}})
	if err != nil || digest != profileartifacts.SHA256(valid) || len(concepts) != 1 || concepts[0].Row != strings.TrimSuffix(string(valid), "\n") {
		t.Fatalf("validate snapshot = %+v, digest=%s, err=%v", concepts, digest, err)
	}
	if empty, emptyDigest, err := ValidateConceptSnapshot(nil, wikiindex.IDMap{Concept: map[string]string{}}); err != nil || len(empty) != 0 || emptyDigest != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty snapshot = %v %s %v", empty, emptyDigest, err)
	}
	for _, test := range []struct {
		name string
		data []byte
		ids  map[string]string
	}{
		{name: "duplicate IDs", data: append(valid, valid...), ids: map[string]string{stableID: "alpha"}},
		{name: "missing active row", data: valid, ids: map[string]string{stableID: "alpha", "01JAZ5N7Y3K8M2Q4R6T9VWXABD": "beta"}},
		{name: "slug mismatch", data: valid, ids: map[string]string{stableID: "wrong"}},
		{name: "no stable ID", data: []byte(`{"slug":"alpha","title":"Alpha","frontmatter":{}}`), ids: map[string]string{stableID: "alpha"}},
		{name: "duplicate nested key", data: []byte(`{"slug":"alpha","title":"Alpha","frontmatter":{"id":"01JAZ5N7Y3K8M2Q4R6T9VWXABC","id":"01JAZ5N7Y3K8M2Q4R6T9VWXABC"}}`), ids: map[string]string{stableID: "alpha"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ValidateConceptSnapshot(test.data, wikiindex.IDMap{Concept: test.ids}); err == nil {
				t.Fatal("invalid concept snapshot accepted")
			}
		})
	}
}

// wikiindex's canonical writer serializes cache.Entry; body and sources must
// survive this reader unchanged, while arbitrary top-level fields remain invalid.
func TestValidateConceptSnapshotCanonicalWriterFields(t *testing.T) {
	const id = "01JAZ5N7Y3K8M2Q4R6T9VWXABC"
	entry := cache.Entry{Slug: "alpha", Title: "Alpha", Body: "Exact body.\nSecond line.", Sources: []string{"abcdef123456"}, Frontmatter: map[string]interface{}{"id": id, "updated_at": "2026-09-25T00:00:00Z"}}
	row, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte(nil), row...), '\n')
	ids := wikiindex.IDMap{Concept: map[string]string{id: "alpha"}}
	concepts, digest, err := ValidateConceptSnapshot(data, ids)
	if err != nil || len(concepts) != 1 || concepts[0].Row != string(row) || digest != profileartifacts.SHA256(data) {
		t.Fatalf("canonical writer row: %+v digest=%s err=%v", concepts, digest, err)
	}
	for _, suffix := range []string{`,"updated_at":"not-a-top-level-field"}`, `,"body":"duplicate"}`, `,"sources":["duplicate"]}`} {
		bad := append(append([]byte(nil), row[:len(row)-1]...), suffix...)
		if _, _, err := ValidateConceptSnapshot(bad, ids); err == nil {
			t.Fatalf("accepted invalid canonical row: %s", bad)
		}
	}
}
