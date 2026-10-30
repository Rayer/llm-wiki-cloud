package contentcontext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	LocalPublicationDescriptorPath = ".olw/content-context-publication.json"
	LocalPublicationVersion        = 1
	LocalPublicationMaxBytes       = 192 << 10
)

// LocalPublicationDescriptor is the bounded local commit marker. SummaryInput
// is captured with the concepts file by the existing worker journal, so later
// summary attempts never rebuild input from mutable workspace files.
type LocalPublicationDescriptor struct {
	Version                 int                  `json:"version"`
	OwnerID                 string               `json:"owner_id"`
	ProjectID               string               `json:"project_id"`
	ExecutionID             string               `json:"execution_id"`
	TransactionID           string               `json:"transaction_id"`
	Generation              string               `json:"generation"`
	ConceptsDigest          string               `json:"concepts_sha256"`
	SummarySourceGeneration string               `json:"summary_source_generation"`
	SummarySourceIdentity   string               `json:"summary_source_identity"`
	SummaryInput            []byte               `json:"summary_input,omitempty"`
	SummaryMetadata         SummaryInputMetadata `json:"summary_metadata"`
	InputErrorClass         string               `json:"input_error_class,omitempty"`
}

// LocalPublicationIdentity returns the current local transaction identity.
// It is deliberately distinct from a cloud manifest or object generation.
func LocalPublicationIdentity(transactionID, digest string) string {
	return "local-publication:" + transactionID + ":" + digest
}

// LocalSummarySourceIdentity returns the identity stored on summary work for
// the local transaction from which input was selected. Same-digest publications
// preserve this value.
func LocalSummarySourceIdentity(transactionID, digest string) string {
	return LocalPublicationIdentity(transactionID, digest)
}

func EncodeLocalPublicationDescriptor(descriptor LocalPublicationDescriptor) ([]byte, error) {
	if err := ValidateLocalPublicationDescriptor(descriptor, descriptor.OwnerID, descriptor.ProjectID); err != nil {
		return nil, err
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return nil, err
	}
	if len(data) > LocalPublicationMaxBytes {
		return nil, errors.New("local content-context descriptor exceeds size limit")
	}
	return data, nil
}

func DecodeLocalPublicationDescriptor(data []byte, ownerID, projectID string) (LocalPublicationDescriptor, error) {
	if len(data) == 0 || len(data) > LocalPublicationMaxBytes {
		return LocalPublicationDescriptor{}, errors.New("local content-context descriptor size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var descriptor LocalPublicationDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return LocalPublicationDescriptor{}, fmt.Errorf("decode local content-context descriptor: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return LocalPublicationDescriptor{}, errors.New("local content-context descriptor has trailing data")
	}
	if err := ValidateLocalPublicationDescriptor(descriptor, ownerID, projectID); err != nil {
		return LocalPublicationDescriptor{}, err
	}
	return descriptor, nil
}

func ValidateLocalPublicationDescriptor(descriptor LocalPublicationDescriptor, ownerID, projectID string) error {
	digest, digestOK := NormalizeSHA256(descriptor.ConceptsDigest)
	metadataDigest, metadataDigestOK := NormalizeSHA256(descriptor.SummaryMetadata.ConceptsDigest)
	if descriptor.Version != LocalPublicationVersion || ownerID == "" || projectID == "" ||
		descriptor.OwnerID != ownerID || descriptor.ProjectID != projectID ||
		descriptor.ExecutionID == "" || descriptor.TransactionID == "" || descriptor.Generation != "local-"+descriptor.TransactionID ||
		!digestOK || !metadataDigestOK || digest != metadataDigest ||
		descriptor.SummarySourceGeneration == "" || descriptor.SummarySourceIdentity == "" ||
		descriptor.SummaryMetadata.SourceGeneration != descriptor.SummarySourceGeneration ||
		descriptor.SummaryMetadata.SelectionVersion != SummarySelectionVersion ||
		!validLocalSummarySourceIdentity(descriptor.SummarySourceIdentity, descriptor.SummarySourceGeneration, digest) {
		return errors.New("local content-context descriptor identity is invalid")
	}
	if descriptor.InputErrorClass != "" {
		if (descriptor.InputErrorClass != "empty_concepts" && descriptor.InputErrorClass != "input_invalid") ||
			len(descriptor.SummaryInput) != 0 || descriptor.SummaryMetadata.InputDigest != "" {
			return errors.New("local content-context descriptor input error is invalid")
		}
		if descriptor.InputErrorClass == "empty_concepts" && descriptor.SummaryMetadata.TotalConcepts != 0 {
			return errors.New("local empty content-context descriptor has concepts")
		}
		return nil
	}
	inputDigest := sha256.Sum256(descriptor.SummaryInput)
	metadataInputDigest, inputDigestOK := NormalizeSHA256(descriptor.SummaryMetadata.InputDigest)
	if len(descriptor.SummaryInput) == 0 || len(descriptor.SummaryInput)+len(SummarySystemPrompt)+1 > SummaryMaxInputBytes ||
		!inputDigestOK || hex.EncodeToString(inputDigest[:]) != metadataInputDigest ||
		descriptor.SummaryMetadata.TotalConcepts < 1 || descriptor.SummaryMetadata.SelectedConcepts < 1 {
		return errors.New("local content-context summary input is invalid")
	}
	var payload summaryInputPayload
	decoder := json.NewDecoder(bytes.NewReader(descriptor.SummaryInput))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return errors.New("local content-context summary input payload is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("local content-context summary input has trailing data")
	}
	payloadDigest, payloadDigestOK := NormalizeSHA256(payload.ConceptsDigest)
	if !payloadDigestOK || payloadDigest != digest || payload.SelectionVersion != SummarySelectionVersion ||
		payload.TotalConcepts != descriptor.SummaryMetadata.TotalConcepts ||
		payload.SelectedConcepts != descriptor.SummaryMetadata.SelectedConcepts ||
		payload.Sampled != descriptor.SummaryMetadata.Sampled ||
		payload.TitleTruncatedConcepts != descriptor.SummaryMetadata.TitleTruncatedConcepts ||
		payload.BodyTruncatedConcepts != descriptor.SummaryMetadata.BodyTruncatedConcepts ||
		payload.TitleCodepointsOmitted != descriptor.SummaryMetadata.TitleCodepointsOmitted ||
		payload.BodyCodepointsOmitted != descriptor.SummaryMetadata.BodyCodepointsOmitted ||
		len(payload.Concepts) != descriptor.SummaryMetadata.SelectedConcepts {
		return errors.New("local content-context summary metadata does not match its input")
	}
	return nil
}

// ValidateLocalPublicationConcepts verifies that the descriptor still names
// the committed JSONL bytes. The captured bounded input is validated from its
// self-contained digest and selection metadata above, without reparsing the
// full corpus during GET or while the local publication fence is held.
func ValidateLocalPublicationConcepts(descriptor LocalPublicationDescriptor, concepts []byte) error {
	digest := sha256.Sum256(concepts)
	want, ok := NormalizeSHA256(descriptor.ConceptsDigest)
	if !ok || hex.EncodeToString(digest[:]) != want {
		return errors.New("local content-context concepts digest mismatch")
	}
	return nil
}

func validLocalSummarySourceIdentity(identity, sourceGeneration, digest string) bool {
	parts := strings.Split(identity, ":")
	return len(parts) == 3 && parts[0] == "local-publication" && parts[1] != "" &&
		"local-"+parts[1] == sourceGeneration && parts[2] == digest
}

func (descriptor LocalPublicationDescriptor) Publication() Publication {
	digest, _ := NormalizeSHA256(descriptor.ConceptsDigest)
	return Publication{
		Generation: descriptor.Generation, ConceptsDigest: digest,
		ManifestIdentity: LocalPublicationIdentity(descriptor.TransactionID, digest),
	}
}
