package contentcontext

import (
	"bytes"
	"strings"
	"testing"
)

func TestPurposeCompareOutputIsStrictAndBounded(t *testing.T) {
	for _, raw := range []string{
		`{"verdict":"clear_conflict","basis":"The published topics focus on a different subject."}`,
	} {
		parsed, err := ParsePurposeCompareOutput([]byte(raw))
		if err != nil || parsed.Verdict != "clear_conflict" {
			t.Fatalf("valid comparison=%+v err=%v", parsed, err)
		}
	}
	for _, raw := range []string{
		`{"verdict":"unknown","basis":"No"}`,
		`{"verdict":"aligned","basis":"Okay","extra":true}`,
		`{"verdict":"aligned","basis":"Okay"}{"verdict":"clear_conflict","basis":"No"}`,
		`{"verdict":"clear_conflict","basis":"<script>"}`,
		`{"verdict":"aligned","basis":""}`,
	} {
		if _, err := ParsePurposeCompareOutput([]byte(raw)); err == nil {
			t.Fatalf("invalid comparison was accepted: %s", raw)
		}
	}
	long := []byte(`{"verdict":"aligned","basis":"` + strings.Repeat("x", PurposeMaxBasisRunes+1) + `"}`)
	if _, err := ParsePurposeCompareOutput(long); err == nil {
		t.Fatal("oversized basis was accepted")
	}
	if _, err := ParsePurposeCompareOutput(bytes.Repeat([]byte("x"), 8193)); err == nil {
		t.Fatal("oversized response was accepted")
	}
}

func TestPurposePairIncludesExactPublishedSummaryAndDescriptionRevision(t *testing.T) {
	publication := testPublication("G1", 'a', "manifest-G1")
	last := testSummaryCompletion("summary A")
	base := PurposePairKey("owner", "project", publication, LastGood{
		Text: last.Text, SourceGeneration: "source-G1", ConceptsSHA256: publication.ConceptsDigest,
		InputDigest: last.InputDigest, PromptRevision: last.PromptRevision, SelectionVersion: last.SelectionVersion,
	}, "manual purpose", 3)
	changedText := LastGood{Text: "summary B", SourceGeneration: "source-G1", ConceptsSHA256: publication.ConceptsDigest,
		InputDigest: last.InputDigest, PromptRevision: last.PromptRevision, SelectionVersion: last.SelectionVersion}
	if base == PurposePairKey("owner", "project", publication, changedText, "manual purpose", 3) {
		t.Fatal("different saved overview shared the same comparison pair")
	}
	if base == PurposePairKey("owner", "project", publication, changedText, "manual purpose", 4) {
		t.Fatal("new description revision shared the previous pair")
	}
}
