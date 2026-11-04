package v1

import (
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

func TestParseMCPContentFrontmatterDistinguishesUnavailableAndEmptySources(t *testing.T) {
	parsed := parseMCPContentFrontmatter([]byte("---\ntitle: A\nsources: []\n---\nbody"))
	if !parsed.Available || !parsed.HasSources || len(parsed.Sources) != 0 || string([]byte("---\ntitle: A\nsources: []\n---\nbody")[parsed.BodyStart:]) != "body" {
		t.Fatalf("valid empty sources parsed as unavailable: %+v", parsed)
	}
	missing := parseMCPContentFrontmatter([]byte("---\ntitle: A\n---\nbody"))
	if !missing.Available || missing.HasSources {
		t.Fatalf("valid absent sources = %+v", missing)
	}
	invalid := parseMCPContentFrontmatter([]byte("---\nsources: [\n---\nbody"))
	if invalid.Available {
		t.Fatalf("malformed sources were reported available: %+v", invalid)
	}
}

func TestScanMCPMarkdownMentionsSkipsCodeAndResolvesReferenceLinks(t *testing.T) {
	data := []byte("---\ntitle: Demo\n---\n" + "`[inline code](Hidden)`\n\n```md\n[[fenced]]\n[hidden](Hidden)\n```\n\n[visible][same]\n[Same]: /concepts/same\n[[Other#anchor|shown]] ![image](Ignored) <!-- [comment](Hidden) -->\n<script>[hidden](Hidden)</script>\n")
	fm := parseMCPContentFrontmatter(data)
	if key, target, ok := parseMCPReferenceDefinition([]byte("[Same]: /concepts/same")); !ok || key != "same" || target != "/concepts/same" {
		t.Fatalf("reference definition parse = %q %q %v", key, target, ok)
	}
	var got []mcpMarkdownReference
	if !scanMCPMarkdownMentions(data, fm.BodyStart, func(reference mcpMarkdownReference) bool {
		got = append(got, reference)
		return true
	}) {
		t.Fatal("scanner stopped before visiting the document")
	}
	if len(got) != 2 {
		t.Fatalf("mentions = %+v", got)
	}
	if got[0].Target != "/concepts/same" || got[0].Label != "visible" || got[0].Origin != "markdown_link" || string(data[got[0].Start:got[0].End]) != "[visible][same]" {
		t.Fatalf("reference-style mention = %+v", got[0])
	}
	if got[1].Target != "Other" || got[1].Label != "shown" || got[1].Origin != "wikilink" || string(data[got[1].Start:got[1].End]) != "[[Other#anchor|shown]]" {
		t.Fatalf("wikilink mention = %+v", got[1])
	}
}

func TestPageMCPReadReferencesFragmentsAmbiguousCandidatesAcrossPages(t *testing.T) {
	ids := wikiindex.IDMap{Concept: map[string]string{}, Source: map[string]string{}, SourceMeta: map[string]wikiindex.SourceMeta{}, IDRedirects: map[string]string{}}
	concepts := make([]gcs.WikiPage, 0, 51)
	for i := 0; i < 30; i++ {
		id := strings.Repeat("c", 1) + twoDigits(i)
		slug := "concept-" + twoDigits(i)
		ids.Concept[id] = slug
		concepts = append(concepts, gcs.WikiPage{ID: id, Slug: slug, Title: "Duplicate"})
	}
	for i := 0; i < 21; i++ {
		id := "s" + twoDigits(i)
		slug := "source-" + twoDigits(i)
		ids.Source[id] = slug
		ids.SourceMeta[id] = wikiindex.SourceMeta{Slug: slug, Title: "Duplicate"}
	}
	ids.Concept["after"] = "after"
	concepts = append(concepts, gcs.WikiPage{ID: "after", Slug: "after", Title: "After"})
	pinned := gcs.GenerationSnapshot{ManifestSHA256: strings.Repeat("a", 64)}
	pinned.Manifest.GenerationID = "generation-1"
	snapshot := buildMCPContentSnapshot(nil, pinned, storage.QueryGenerationIdentity{}, ids, concepts)
	identity := mcpProjectKeyIdentity{userID: "user-1", projectID: "project-1"}
	document := snapshot.byID[mcpContentKindConcept+"\x00after"]
	data := []byte("[[Duplicate]] then [[After]]")

	_, first, next, complete, err := pageMCPReadReferences(snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), data, 0, nil, mcpContentCursor{}, false)
	if err != nil || complete || len(first) != 1 || first[0].Resolution != "ambiguous" || first[0].Kind != nil || len(first[0].Candidates) != 50 || first[0].CandidateOffset != 0 || first[0].CandidatesComplete {
		t.Fatalf("first candidate page = refs:%+v next:%v complete:%v err:%v", first, next, complete, err)
	}
	encoded, ok := next.(string)
	if !ok {
		t.Fatalf("next refs cursor type = %T", next)
	}
	cursor, err := decodeMCPContentCursor(encoded)
	if err != nil || cursor.ReferenceIndex != 0 || cursor.CandidateOffset != 50 {
		t.Fatalf("candidate cursor = %+v err=%v", cursor, err)
	}
	_, second, finalCursor, complete, err := pageMCPReadReferences(snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), data, 0, nil, cursor, true)
	if err != nil || !complete || finalCursor != nil || len(second) != 2 {
		t.Fatalf("second candidate page = refs:%+v next:%v complete:%v err:%v", second, finalCursor, complete, err)
	}
	if second[0].ReferenceID != first[0].ReferenceID || second[0].CandidateOffset != 50 || len(second[0].Candidates) != 1 || !second[0].CandidatesComplete || second[0].Resolution != "ambiguous" || second[1].ReferenceIndex != 1 || second[1].Resolution != "resolved" {
		t.Fatalf("candidate page lost ambiguity or next reference: %+v", second)
	}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
