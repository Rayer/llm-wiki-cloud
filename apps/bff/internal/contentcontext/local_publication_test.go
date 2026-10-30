package contentcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestLocalPublicationDescriptorRoundTripAndIdentity(t *testing.T) {
	digest := hex.EncodeToString(make([]byte, sha256.Size))
	input, err := json.Marshal(summaryInputPayload{
		SelectionVersion: SummarySelectionVersion, ConceptsDigest: digest,
		TotalConcepts: 1, SelectedConcepts: 1,
		Concepts: []summaryConcept{{Slug: "alpha", Title: "Alpha", Body: "Body"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputDigest := sha256.Sum256(input)
	descriptor := LocalPublicationDescriptor{
		Version: LocalPublicationVersion, OwnerID: "owner", ProjectID: "project",
		ExecutionID: "execution-7", TransactionID: "txn-7", Generation: "local-txn-7",
		ConceptsDigest: digest, SummarySourceGeneration: "local-txn-7",
		SummarySourceIdentity: LocalSummarySourceIdentity("txn-7", digest), SummaryInput: input,
		SummaryMetadata: SummaryInputMetadata{
			ConceptsDigest: digest, SourceGeneration: "local-txn-7",
			InputDigest: hex.EncodeToString(inputDigest[:]), SelectionVersion: SummarySelectionVersion,
			TotalConcepts: 1, SelectedConcepts: 1,
		},
	}
	encoded, err := EncodeLocalPublicationDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLocalPublicationDescriptor(encoded, "owner", "project")
	if err != nil {
		t.Fatal(err)
	}
	publication := decoded.Publication()
	if publication.Generation != "local-txn-7" || publication.ConceptsDigest != digest ||
		publication.ManifestIdentity != LocalPublicationIdentity("txn-7", digest) {
		t.Fatalf("publication = %#v", publication)
	}
}

func TestLocalPublicationDescriptorRejectsUnboundInputAndCrossProject(t *testing.T) {
	digest := hex.EncodeToString(make([]byte, sha256.Size))
	descriptor := LocalPublicationDescriptor{
		Version: LocalPublicationVersion, OwnerID: "owner", ProjectID: "project",
		ExecutionID: "execution-7", TransactionID: "txn-7", Generation: "local-txn-7",
		ConceptsDigest: digest, SummarySourceGeneration: "local-txn-7",
		SummarySourceIdentity: LocalSummarySourceIdentity("txn-7", digest), SummaryInput: []byte("input"),
		SummaryMetadata: SummaryInputMetadata{
			ConceptsDigest: digest, SourceGeneration: "local-txn-7",
			InputDigest: hex.EncodeToString(make([]byte, sha256.Size)), SelectionVersion: SummarySelectionVersion,
			TotalConcepts: 1, SelectedConcepts: 1,
		},
	}
	if _, err := EncodeLocalPublicationDescriptor(descriptor); err == nil {
		t.Fatal("descriptor with an unbound summary input was accepted")
	}
	if err := ValidateLocalPublicationDescriptor(descriptor, "owner", "other-project"); err == nil {
		t.Fatal("cross-project descriptor was accepted")
	}
}

func TestLocalPublicationDescriptorAllowsBoundEmptyAndInvalidInputStates(t *testing.T) {
	digest := hex.EncodeToString(make([]byte, sha256.Size))
	for _, class := range []string{"empty_concepts", "input_invalid"} {
		descriptor := LocalPublicationDescriptor{
			Version: LocalPublicationVersion, OwnerID: "owner", ProjectID: "project",
			ExecutionID: "execution-7", TransactionID: "txn-7", Generation: "local-txn-7",
			ConceptsDigest: digest, SummarySourceGeneration: "local-txn-7",
			SummarySourceIdentity: LocalSummarySourceIdentity("txn-7", digest), InputErrorClass: class,
			SummaryMetadata: SummaryInputMetadata{
				ConceptsDigest: digest, SourceGeneration: "local-txn-7", SelectionVersion: SummarySelectionVersion,
			},
		}
		if _, err := EncodeLocalPublicationDescriptor(descriptor); err != nil {
			t.Fatalf("encode %s descriptor: %v", class, err)
		}
	}
}
