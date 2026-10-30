package contentcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestBuildSummaryInputSortsAndSamplesPublishedConcepts(t *testing.T) {
	snapshot := summarySnapshot(450, "Title", "Body")
	data, metadata, err := BuildSummaryInput(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var payload summaryInputPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if metadata.TotalConcepts != 450 || metadata.SelectedConcepts != SummaryMaxConcepts || !metadata.Sampled || !payload.Sampled {
		t.Fatalf("metadata=%+v payload=%+v", metadata, payload)
	}
	if payload.Concepts[0].Slug != "concept-000" || payload.Concepts[len(payload.Concepts)-1].Slug != "concept-449" {
		t.Fatalf("sample does not cover sorted corpus ends: first=%q last=%q", payload.Concepts[0].Slug, payload.Concepts[len(payload.Concepts)-1].Slug)
	}
	for i := 1; i < len(payload.Concepts); i++ {
		if payload.Concepts[i-1].Slug >= payload.Concepts[i].Slug {
			t.Fatalf("sample is not strictly sorted at %d: %q then %q", i, payload.Concepts[i-1].Slug, payload.Concepts[i].Slug)
		}
	}
	if payload.SelectionVersion != SummarySelectionVersion || metadata.SourceGeneration != "generation-1" || metadata.ConceptsDigest != strings.Repeat("a", 64) {
		t.Fatalf("identity metadata=%+v payload=%+v", metadata, payload)
	}
	sum := sha256.Sum256(data)
	if metadata.InputDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("input_digest=%q want %x", metadata.InputDigest, sum)
	}
	again, againMetadata, err := BuildSummaryInput(snapshot)
	if err != nil || string(again) != string(data) || againMetadata != metadata {
		t.Fatalf("selection was not deterministic: equal=%t metadataEqual=%t err=%v", string(again) == string(data), againMetadata == metadata, err)
	}
}

func TestBuildSummaryInputEnforcesCodepointAndByteLimits(t *testing.T) {
	snapshot := summarySnapshot(SummaryMaxConcepts, strings.Repeat("界", 300), strings.Repeat("😀", 600))
	data, metadata, err := BuildSummaryInput(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)+len(SummarySystemPrompt)+1 > SummaryMaxInputBytes {
		t.Fatalf("input bytes=%d limit=%d", len(data)+len(SummarySystemPrompt)+1, SummaryMaxInputBytes)
	}
	var payload summaryInputPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if metadata.TitleTruncatedConcepts == 0 || metadata.BodyTruncatedConcepts == 0 || metadata.TitleCodepointsOmitted == 0 || metadata.BodyCodepointsOmitted == 0 {
		t.Fatalf("truncation coverage missing: %+v", metadata)
	}
	for _, concept := range payload.Concepts {
		if utf8.RuneCountInString(concept.Title) > SummaryMaxTitleRunes || utf8.RuneCountInString(concept.Body) > SummaryMaxBodyRunes {
			t.Fatalf("codepoint limits exceeded: title=%d body=%d", utf8.RuneCountInString(concept.Title), utf8.RuneCountInString(concept.Body))
		}
	}
}

func TestBuildSummaryInputRejectsEmptyOrInvalidSnapshot(t *testing.T) {
	empty := summarySnapshot(0, "", "")
	if _, metadata, err := BuildSummaryInput(empty); !errors.Is(err, ErrEmptyConcepts) || metadata.SourceGeneration != "generation-1" {
		t.Fatalf("empty snapshot metadata=%+v err=%v", metadata, err)
	}
	invalid := summarySnapshot(1, "Title", "Body")
	invalid.Identity.ConceptsDigest = "not-a-digest"
	if _, _, err := BuildSummaryInput(invalid); err == nil {
		t.Fatal("invalid published digest was accepted")
	}
	unprefixed := summarySnapshot(1, "Title", "Body")
	unprefixed.Identity.ConceptsDigest = strings.Repeat("a", 64)
	if _, metadata, err := BuildSummaryInput(unprefixed); err != nil || metadata.ConceptsDigest != strings.Repeat("a", 64) {
		t.Fatalf("unprefixed valid digest metadata=%+v err=%v", metadata, err)
	}
}

func summarySnapshot(count int, title, body string) cache.PinnedCanonicalSnapshot {
	entries := make([]cache.CanonicalEntry, 0, count)
	for i := count - 1; i >= 0; i-- {
		entries = append(entries, cache.CanonicalEntry{Entry: cache.Entry{
			Slug: fmt.Sprintf("concept-%03d", i), Title: title, Body: body,
		}})
	}
	return cache.PinnedCanonicalSnapshot{
		Identity: storage.QueryGenerationIdentity{
			ProjectID: "project-1", GenerationID: "generation-1",
			ConceptsDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Entries: entries,
	}
}
