package profileartifacts

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBootstrapArtifactIsCanonicalAndRevisionBound(t *testing.T) {
	envelope := BootstrapGuidanceEnvelope{
		SchemaVersion:   BootstrapGuidanceSchema,
		ProfileRevision: 7,
		InputDigest:     strings.Repeat("a", 64),
		ModelVersion:    "fixture-model",
		PromptVersion:   "bootstrap-v1",
		CompileGuidance: "Write concise notes.",
	}
	data, ref, err := EncodeBootstrapGuidance(envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"profile.bootstrap-guidance.v1","profile_revision":7,"input_digest":"` + strings.Repeat("a", 64) + `","model_version":"fixture-model","prompt_version":"bootstrap-v1","compile_guidance":"Write concise notes."}`
	if string(data) != want {
		t.Fatalf("canonical bytes = %s, want %s", data, want)
	}
	decoded, err := ValidateBootstrapGuidance(data, ref)
	if err != nil || decoded != envelope {
		t.Fatalf("validate = %+v, %v", decoded, err)
	}
	if ref.Revision != SHA256(data) || ref.InputDigest != envelope.InputDigest || ref.ProfileRevision != envelope.ProfileRevision {
		t.Fatalf("reference = %+v", ref)
	}
}

func TestNeutralGenerationArtifactsMatchFrozenBytesAndHashes(t *testing.T) {
	inputDigest := "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	conceptsDigest := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	dictionary, dictionaryRef, err := EncodeDictionary(DictionaryEnvelope{
		SchemaVersion: DictionarySchema, InputDigest: inputDigest, ContentGeneration: "G2",
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: "neutral-v1", PromptVersion: "neutral-v1", Tags: []Tag{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schema_version":"profile.dictionary.v1","input_digest":"` + inputDigest + `","content_generation":"G2","canonical_concepts_digest":"` + conceptsDigest + `","model_version":"neutral-v1","prompt_version":"neutral-v1","tags":[]}`; string(dictionary) != want {
		t.Fatalf("neutral dictionary bytes = %s", dictionary)
	}
	if dictionaryRef.Revision != "b10fbd8bf835f41ee64b953a9871fc52c6bb9351048324455a269a98b2b4ef36" {
		t.Fatalf("neutral dictionary revision = %s", dictionaryRef.Revision)
	}
	guidance, guidanceRef, err := EncodeGuidance(GenerationGuidanceEnvelope{
		SchemaVersion: GuidanceSchema, InputDigest: inputDigest, SourceContentGeneration: "G2",
		CanonicalConceptsDigest: conceptsDigest, ModelVersion: "neutral-v1", PromptVersion: "neutral-v1", CompileGuidance: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schema_version":"profile.guidance.v1","input_digest":"` + inputDigest + `","source_content_generation":"G2","canonical_concepts_digest":"` + conceptsDigest + `","model_version":"neutral-v1","prompt_version":"neutral-v1","compile_guidance":""}`; string(guidance) != want {
		t.Fatalf("neutral guidance bytes = %s", guidance)
	}
	if guidanceRef.Revision != "69fa310a64bb0957d2a03f48d8eb530d53558244e4ab06227971c8ed94e13fb0" {
		t.Fatalf("neutral guidance revision = %s", guidanceRef.Revision)
	}
	if _, err := ValidateDictionary(dictionary, dictionaryRef, "G2", conceptsDigest, nil); err != nil {
		t.Fatalf("neutral dictionary validation: %v", err)
	}
	if _, err := ValidateGuidance(guidance, guidanceRef); err != nil {
		t.Fatalf("neutral guidance validation: %v", err)
	}
}

func TestBootstrapArtifactRejectsNonCanonicalOrMismatchedBytes(t *testing.T) {
	envelope := BootstrapGuidanceEnvelope{SchemaVersion: BootstrapGuidanceSchema, ProfileRevision: 1, InputDigest: strings.Repeat("b", 64), ModelVersion: "m", PromptVersion: "p"}
	data, ref, err := EncodeBootstrapGuidance(envelope)
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{
		append(append([]byte(nil), data...), '\n'),
		bytes.Replace(data, []byte(`"compile_guidance":""`), []byte(`"compile_guidance":"","extra":true`), 1),
		bytes.Replace(data, []byte(`"compile_guidance":""`), []byte(`"compile_guidance":"","compile_guidance":"x"`), 1),
		append(append([]byte(nil), data...), []byte(` {}`)...),
	}
	for i, candidate := range bad {
		if _, err := ValidateBootstrapGuidance(candidate, ref); err == nil {
			t.Errorf("candidate %d accepted", i)
		}
	}
	changed := append([]byte(nil), data...)
	changed[len(changed)-2] ^= 1
	if _, err := ValidateBootstrapGuidance(changed, ref); err == nil {
		t.Fatal("one-byte mutation accepted")
	}
	oversized := bytes.Repeat([]byte("x"), MaxArtifactBytes+1)
	if _, err := ValidateBootstrapGuidance(oversized, ref); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestCreateOnlyAcceptsOnlyExactExistingBytes(t *testing.T) {
	envelope := BootstrapGuidanceEnvelope{SchemaVersion: BootstrapGuidanceSchema, ProfileRevision: 1, InputDigest: strings.Repeat("c", 64), ModelVersion: "m", PromptVersion: "p"}
	data, ref, err := EncodeBootstrapGuidance(envelope)
	if err != nil {
		t.Fatal(err)
	}
	objects := &objectStoreStub{existing: map[string][]byte{}}
	if err := WriteBootstrapGuidance(context.Background(), objects, ref, data); err != nil {
		t.Fatal(err)
	}
	if objects.writes != 1 {
		t.Fatalf("writes = %d, want one", objects.writes)
	}
	objects.failCreate = true
	if err := WriteBootstrapGuidance(context.Background(), objects, ref, data); err != nil {
		t.Fatalf("byte-identical retry failed: %v", err)
	}
	mutated := append([]byte(nil), data...)
	mutated[10] ^= 1
	objects.existing[BootstrapObjectPath(ref.Revision)] = mutated
	if err := WriteBootstrapGuidance(context.Background(), objects, ref, data); err == nil {
		t.Fatal("different existing bytes accepted")
	}
}

type objectStoreStub struct {
	existing   map[string][]byte
	writes     int
	failCreate bool
}

func (s *objectStoreStub) ReadFileLimited(_ context.Context, path string, limit int64) ([]byte, error) {
	data, ok := s.existing[path]
	if !ok {
		return nil, errors.New("not found")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("too large")
	}
	return append([]byte(nil), data...), nil
}

func (s *objectStoreStub) StatFile(_ context.Context, path string) (int64, error) {
	data, ok := s.existing[path]
	if !ok {
		return 0, errors.New("not found")
	}
	return int64(len(data)), nil
}

func (s *objectStoreStub) WriteFileIfGeneration(_ context.Context, data []byte, path string, expected int64) (int64, error) {
	s.writes++
	if expected != 0 || s.failCreate || s.existing[path] != nil {
		return 0, errors.New("already exists")
	}
	s.existing[path] = append([]byte(nil), data...)
	return 1, nil
}
