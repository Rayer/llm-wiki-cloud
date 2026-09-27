// Package profileartifacts owns immutable, content-addressed Profile objects.
package profileartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaxArtifactBytes            = 1 << 20
	BootstrapGuidanceSchema     = "profile.bootstrap-guidance.v1"
	DictionarySchema            = "profile.dictionary.v1"
	GuidanceSchema              = "profile.guidance.v1"
	BootstrapGuidancePathPrefix = ".lwc/profile/bootstrap-guidance/v1/"
	DictionaryPathPrefix        = ".lwc/profile/derived/dictionary/v1/"
	GuidancePathPrefix          = ".lwc/profile/derived/guidance/v1/"
)

var tagIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// BootstrapGuidanceEnvelope is intentionally generation-free. Revision is
// the SHA-256 of its exact compact JSON bytes and is carried by the ref.
type BootstrapGuidanceEnvelope struct {
	SchemaVersion   string `json:"schema_version"`
	ProfileRevision int64  `json:"profile_revision"`
	InputDigest     string `json:"input_digest"`
	ModelVersion    string `json:"model_version"`
	PromptVersion   string `json:"prompt_version"`
	CompileGuidance string `json:"compile_guidance"`
}

type BootstrapGuidanceRef struct {
	Revision        string
	ProfileRevision int64
	InputDigest     string
	ModelVersion    string
	PromptVersion   string
	SchemaVersion   string
}

type DictionaryEnvelope struct {
	SchemaVersion           string `json:"schema_version"`
	InputDigest             string `json:"input_digest"`
	ContentGeneration       string `json:"content_generation"`
	CanonicalConceptsDigest string `json:"canonical_concepts_digest"`
	ModelVersion            string `json:"model_version"`
	PromptVersion           string `json:"prompt_version"`
	Tags                    []Tag  `json:"tags"`
}

type Tag struct {
	ID             string   `json:"id"`
	Definition     string   `json:"definition"`
	AppliesTo      []string `json:"applies_to"`
	MatchRule      string   `json:"match_rule"`
	NonMatchRule   string   `json:"non_match_rule"`
	UnknownRule    string   `json:"unknown_rule"`
	RequirementIDs []string `json:"requirement_ids"`
	QueryUse       string   `json:"query_use"`
}

type GenerationGuidanceEnvelope struct {
	SchemaVersion           string `json:"schema_version"`
	InputDigest             string `json:"input_digest"`
	SourceContentGeneration string `json:"source_content_generation"`
	CanonicalConceptsDigest string `json:"canonical_concepts_digest"`
	ModelVersion            string `json:"model_version"`
	PromptVersion           string `json:"prompt_version"`
	CompileGuidance         string `json:"compile_guidance"`
}

type DerivedRef struct {
	Revision      string
	InputDigest   string
	ModelVersion  string
	PromptVersion string
	SchemaVersion string
}

func IsSHA256(value string) bool { return validSHA256(value) }

// ObjectStore is the smallest part of a scoped Project store needed here.
type ObjectStore interface {
	ReadFileLimited(context.Context, string, int64) ([]byte, error)
	StatFile(context.Context, string) (int64, error)
	WriteFileIfGeneration(context.Context, []byte, string, int64) (int64, error)
}

func EncodeBootstrapGuidance(envelope BootstrapGuidanceEnvelope) ([]byte, BootstrapGuidanceRef, error) {
	if err := validateBootstrapEnvelope(envelope); err != nil {
		return nil, BootstrapGuidanceRef{}, err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, BootstrapGuidanceRef{}, err
	}
	if len(data) > MaxArtifactBytes {
		return nil, BootstrapGuidanceRef{}, errors.New("Profile artifact exceeds 1 MiB")
	}
	return data, BootstrapGuidanceRef{
		Revision: SHA256(data), ProfileRevision: envelope.ProfileRevision,
		InputDigest: envelope.InputDigest, ModelVersion: envelope.ModelVersion,
		PromptVersion: envelope.PromptVersion, SchemaVersion: envelope.SchemaVersion,
	}, nil
}

func ValidateBootstrapGuidance(data []byte, ref BootstrapGuidanceRef) (BootstrapGuidanceEnvelope, error) {
	var envelope BootstrapGuidanceEnvelope
	if err := decodeCanonical(data, &envelope); err != nil {
		return envelope, err
	}
	if err := validateBootstrapEnvelope(envelope); err != nil {
		return envelope, err
	}
	if !validSHA256(ref.Revision) || SHA256(data) != ref.Revision ||
		envelope.SchemaVersion != ref.SchemaVersion || envelope.ProfileRevision != ref.ProfileRevision ||
		envelope.InputDigest != ref.InputDigest || envelope.ModelVersion != ref.ModelVersion || envelope.PromptVersion != ref.PromptVersion {
		return envelope, errors.New("bootstrap guidance artifact reference mismatch")
	}
	return envelope, nil
}

func EncodeDictionary(envelope DictionaryEnvelope, requirementOrder []string) ([]byte, DerivedRef, error) {
	if err := validateDictionaryEnvelope(envelope, requirementOrder); err != nil {
		return nil, DerivedRef{}, err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, DerivedRef{}, err
	}
	if len(data) > MaxArtifactBytes {
		return nil, DerivedRef{}, errors.New("Profile artifact exceeds 1 MiB")
	}
	return data, DerivedRef{Revision: SHA256(data), InputDigest: envelope.InputDigest, ModelVersion: envelope.ModelVersion, PromptVersion: envelope.PromptVersion, SchemaVersion: envelope.SchemaVersion}, nil
}

func ValidateDictionary(data []byte, ref DerivedRef, contentGeneration, conceptsDigest string, requirementOrder []string) (DictionaryEnvelope, error) {
	var envelope DictionaryEnvelope
	if err := decodeCanonical(data, &envelope); err != nil {
		return envelope, err
	}
	if err := validateDictionaryEnvelope(envelope, requirementOrder); err != nil {
		return envelope, err
	}
	if !validDerivedRef(data, ref, DictionarySchema) || envelope.InputDigest != ref.InputDigest ||
		envelope.ModelVersion != ref.ModelVersion || envelope.PromptVersion != ref.PromptVersion ||
		envelope.ContentGeneration != contentGeneration || envelope.CanonicalConceptsDigest != conceptsDigest {
		return envelope, errors.New("dictionary artifact reference mismatch")
	}
	return envelope, nil
}

func EncodeGuidance(envelope GenerationGuidanceEnvelope) ([]byte, DerivedRef, error) {
	if err := validateGenerationGuidance(envelope); err != nil {
		return nil, DerivedRef{}, err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, DerivedRef{}, err
	}
	if len(data) > MaxArtifactBytes {
		return nil, DerivedRef{}, errors.New("Profile artifact exceeds 1 MiB")
	}
	return data, DerivedRef{Revision: SHA256(data), InputDigest: envelope.InputDigest, ModelVersion: envelope.ModelVersion, PromptVersion: envelope.PromptVersion, SchemaVersion: envelope.SchemaVersion}, nil
}

func ValidateGuidance(data []byte, ref DerivedRef) (GenerationGuidanceEnvelope, error) {
	var envelope GenerationGuidanceEnvelope
	if err := decodeCanonical(data, &envelope); err != nil {
		return envelope, err
	}
	if err := validateGenerationGuidance(envelope); err != nil {
		return envelope, err
	}
	if !validDerivedRef(data, ref, GuidanceSchema) || envelope.InputDigest != ref.InputDigest ||
		envelope.ModelVersion != ref.ModelVersion || envelope.PromptVersion != ref.PromptVersion {
		return envelope, errors.New("guidance artifact reference mismatch")
	}
	return envelope, nil
}

func WriteBootstrapGuidance(ctx context.Context, objects ObjectStore, ref BootstrapGuidanceRef, data []byte) error {
	if objects == nil {
		return errors.New("Profile artifact store unavailable")
	}
	if _, err := ValidateBootstrapGuidance(data, ref); err != nil {
		return err
	}
	return writeCreateOnly(ctx, objects, BootstrapObjectPath(ref.Revision), data)
}

func WriteDictionary(ctx context.Context, objects ObjectStore, ref DerivedRef, data []byte) error {
	if objects == nil || !validDerivedRef(data, ref, DictionarySchema) {
		return errors.New("invalid dictionary object reference")
	}
	return writeCreateOnly(ctx, objects, DictionaryObjectPath(ref.Revision), data)
}

func WriteGuidance(ctx context.Context, objects ObjectStore, ref DerivedRef, data []byte) error {
	if objects == nil || !validDerivedRef(data, ref, GuidanceSchema) {
		return errors.New("invalid guidance object reference")
	}
	return writeCreateOnly(ctx, objects, GuidanceObjectPath(ref.Revision), data)
}

func ReadBootstrapGuidance(ctx context.Context, objects ObjectStore, ref BootstrapGuidanceRef) ([]byte, BootstrapGuidanceEnvelope, error) {
	if objects == nil || BootstrapObjectPath(ref.Revision) == "" {
		return nil, BootstrapGuidanceEnvelope{}, errors.New("invalid bootstrap guidance reference")
	}
	data, err := readObjectLimited(ctx, objects, BootstrapObjectPath(ref.Revision))
	if err != nil {
		return nil, BootstrapGuidanceEnvelope{}, err
	}
	envelope, err := ValidateBootstrapGuidance(data, ref)
	return data, envelope, err
}

func BootstrapObjectPath(revision string) string {
	if !validSHA256(revision) {
		return ""
	}
	return BootstrapGuidancePathPrefix + revision + ".json"
}

func DictionaryObjectPath(revision string) string {
	if !validSHA256(revision) {
		return ""
	}
	return DictionaryPathPrefix + revision + ".json"
}

func GuidanceObjectPath(revision string) string {
	if !validSHA256(revision) {
		return ""
	}
	return GuidancePathPrefix + revision + ".json"
}

func SHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeCreateOnly(ctx context.Context, objects ObjectStore, path string, data []byte) error {
	if path == "" || len(data) > MaxArtifactBytes {
		return errors.New("invalid Profile artifact write")
	}
	if _, err := objects.WriteFileIfGeneration(ctx, data, path, 0); err == nil {
		return nil
	} else {
		existing, readErr := readObjectLimited(ctx, objects, path)
		if readErr == nil && bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("create-only Profile artifact write failed: %w", err)
	}
}

func readObjectLimited(ctx context.Context, objects ObjectStore, path string) ([]byte, error) {
	size, err := objects.StatFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > MaxArtifactBytes {
		return nil, errors.New("Profile artifact exceeds 1 MiB")
	}
	data, err := objects.ReadFileLimited(ctx, path, MaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errors.New("Profile artifact changed during read")
	}
	return data, nil
}

func validateBootstrapEnvelope(envelope BootstrapGuidanceEnvelope) error {
	if envelope.SchemaVersion != BootstrapGuidanceSchema || envelope.ProfileRevision < 1 || !validSHA256(envelope.InputDigest) ||
		strings.TrimSpace(envelope.ModelVersion) == "" || strings.TrimSpace(envelope.PromptVersion) == "" || !utf8.ValidString(envelope.CompileGuidance) {
		return errors.New("invalid bootstrap guidance envelope")
	}
	return nil
}

func validateDictionaryEnvelope(envelope DictionaryEnvelope, requirementOrder []string) error {
	if envelope.SchemaVersion != DictionarySchema || !validSHA256(envelope.InputDigest) || !safeGeneration(envelope.ContentGeneration) ||
		!validSHA256(envelope.CanonicalConceptsDigest) || strings.TrimSpace(envelope.ModelVersion) == "" ||
		strings.TrimSpace(envelope.PromptVersion) == "" || envelope.Tags == nil {
		return errors.New("invalid dictionary envelope")
	}
	order := make(map[string]int, len(requirementOrder))
	for index, id := range requirementOrder {
		if id == "" {
			return errors.New("invalid dictionary requirement order")
		}
		if _, exists := order[id]; exists {
			return errors.New("duplicate dictionary requirement ID")
		}
		order[id] = index
	}
	seenTags := make(map[string]struct{}, len(envelope.Tags))
	lastTagID := ""
	for _, tag := range envelope.Tags {
		if !tagIDPattern.MatchString(tag.ID) || tag.ID <= lastTagID || strings.TrimSpace(tag.Definition) == "" ||
			strings.TrimSpace(tag.MatchRule) == "" || strings.TrimSpace(tag.NonMatchRule) == "" || strings.TrimSpace(tag.UnknownRule) == "" ||
			(tag.QueryUse != "preferred" && tag.QueryUse != "required_candidate") || len(tag.AppliesTo) == 0 || len(tag.RequirementIDs) == 0 {
			return errors.New("invalid dictionary tag")
		}
		if _, duplicate := seenTags[tag.ID]; duplicate {
			return errors.New("duplicate dictionary tag ID")
		}
		seenTags[tag.ID] = struct{}{}
		lastTagID = tag.ID
		lastScope := -1
		for _, scope := range tag.AppliesTo {
			scopeOrder := map[string]int{"source": 0, "concept": 1}[scope]
			if (scope != "source" && scope != "concept") || scopeOrder <= lastScope {
				return errors.New("invalid dictionary tag scope ordering")
			}
			lastScope = scopeOrder
		}
		lastRequirement := -1
		seenRequirements := make(map[string]struct{}, len(tag.RequirementIDs))
		for _, id := range tag.RequirementIDs {
			position, exists := order[id]
			if !exists || position <= lastRequirement {
				return errors.New("invalid dictionary tag requirement ordering")
			}
			if _, duplicate := seenRequirements[id]; duplicate {
				return errors.New("duplicate dictionary tag requirement")
			}
			seenRequirements[id] = struct{}{}
			lastRequirement = position
		}
	}
	return nil
}

func validateGenerationGuidance(envelope GenerationGuidanceEnvelope) error {
	if envelope.SchemaVersion != GuidanceSchema || !validSHA256(envelope.InputDigest) || !safeGeneration(envelope.SourceContentGeneration) ||
		!validSHA256(envelope.CanonicalConceptsDigest) || strings.TrimSpace(envelope.ModelVersion) == "" ||
		strings.TrimSpace(envelope.PromptVersion) == "" || !utf8.ValidString(envelope.CompileGuidance) {
		return errors.New("invalid generation guidance envelope")
	}
	return nil
}

func validDerivedRef(data []byte, ref DerivedRef, schema string) bool {
	return len(data) <= MaxArtifactBytes && validSHA256(ref.Revision) && SHA256(data) == ref.Revision &&
		validSHA256(ref.InputDigest) && strings.TrimSpace(ref.ModelVersion) != "" &&
		strings.TrimSpace(ref.PromptVersion) != "" && ref.SchemaVersion == schema
}

func safeGeneration(generation string) bool {
	return generation != "" && generation == strings.TrimSpace(generation) && !strings.ContainsAny(generation, `/\`) && generation != "." && generation != ".."
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeCanonical(data []byte, target any) error {
	if len(data) > MaxArtifactBytes {
		return errors.New("Profile artifact exceeds 1 MiB")
	}
	if !utf8.Valid(data) {
		return errors.New("Profile artifact is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return fmt.Errorf("invalid Profile artifact: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Profile artifact: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("Profile artifact is not canonical JSON")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureEOF(decoder)
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
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON key %q", name)
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
		return errors.New("unexpected JSON delimiter")
	}
}

func ensureEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("Profile artifact contains trailing data")
		}
		return fmt.Errorf("Profile artifact contains trailing data: %w", err)
	}
	return nil
}
