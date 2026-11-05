package v1

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

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
	if key, target, ok := parseMCPReferenceDefinition(context.Background(), []byte("[Same]: /concepts/same")); !ok || key != "same" || target != "/concepts/same" {
		t.Fatalf("reference definition parse = %q %q %v", key, target, ok)
	}
	var got []mcpMarkdownReference
	if !scanMCPMarkdownMentions(context.Background(), data, fm.BodyStart, func(reference mcpMarkdownReference) bool {
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
	snapshot := buildTestMCPContentSnapshot(pinned, ids, concepts)
	identity := mcpProjectKeyIdentity{userID: "user-1", projectID: "project-1"}
	document, exists := snapshot.refByID(mcpContentKindConcept, "after")
	if !exists {
		t.Fatal("after concept missing from snapshot")
	}
	data := []byte("[[Duplicate]] then [[After]]")

	_, first, next, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), data, 0, nil, mcpContentCursor{}, false, 50, map[string]any{})
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
	_, second, finalCursor, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), data, 0, nil, cursor, true, 50, map[string]any{})
	if err != nil || !complete || finalCursor != nil || len(second) != 2 {
		t.Fatalf("second candidate page = refs:%+v next:%v complete:%v err:%v", second, finalCursor, complete, err)
	}
	if second[0].ReferenceID != first[0].ReferenceID || second[0].CandidateOffset != 50 || len(second[0].Candidates) != 1 || !second[0].CandidatesComplete || second[0].Resolution != "ambiguous" || second[1].ReferenceIndex != 1 || second[1].Resolution != "resolved" {
		t.Fatalf("candidate page lost ambiguity or next reference: %+v", second)
	}

	pageAtPadding := func(padding int) (map[string]any, []mcpResolvedRef, mcpContentCursor, error) {
		base := map[string]any{"padding": strings.Repeat("x", padding)}
		_, refs, next, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), []byte("[[Duplicate]]"), 0, nil, mcpContentCursor{}, false, 50, base)
		encoded, ok := next.(string)
		if err != nil || !ok || complete || len(refs) != 1 {
			return nil, nil, mcpContentCursor{}, fmt.Errorf("padding %d: refs=%+v next=%T complete=%t err=%v", padding, refs, next, complete, err)
		}
		cursor, err := decodeMCPContentCursor(encoded)
		if err != nil {
			return nil, nil, mcpContentCursor{}, err
		}
		return base, refs, cursor, nil
	}
	// Find the largest padding for which one candidate still fits. The bound
	// avoids a slow linear sweep while keeping the protected wire cap as the
	// measured decision point.
	low, high := 0, maxMCPProtectedResponseBytes
	for low+1 < high {
		middle := low + (high-low)/2
		_, refs, _, err := pageAtPadding(middle)
		if err == nil && len(refs[0].Candidates) > 0 {
			low = middle
		} else {
			high = middle
		}
	}
	boundaryBase, boundaryFirst, boundaryCursor, err := pageAtPadding(low)
	if err != nil || len(boundaryFirst[0].Candidates) != 1 || boundaryCursor.ReferenceIndex != 0 || boundaryCursor.CandidateOffset != 1 {
		t.Fatalf("could not construct a wire boundary where exactly one candidate fits: padding=%d refs=%+v cursor=%+v err=%v", low, boundaryFirst, boundaryCursor, err)
	}
	tooLargeBase := map[string]any{"padding": strings.Repeat("x", low+1)}
	_, tooLarge, _, _, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), []byte("[[Duplicate]]"), 0, nil, mcpContentCursor{}, false, 50, tooLargeBase)
	if err == nil || err.Error() != "response_limit" || len(tooLarge) != 0 {
		t.Fatalf("first candidate that cannot fit should fail without an empty fragment: refs=%+v err=%v", tooLarge, err)
	}
	_, boundaryNext, cursorValue, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), []byte("[[Duplicate]]"), 0, nil, boundaryCursor, true, 50, boundaryBase)
	if err != nil || complete || len(boundaryNext) != 1 || len(boundaryNext[0].Candidates) != 1 || boundaryNext[0].CandidateOffset != 1 {
		t.Fatalf("boundary retry failed to make one-candidate progress: first=%+v next=%+v cursor=%v complete=%v err=%v", boundaryFirst, boundaryNext, cursorValue, complete, err)
	}
	encodedNext, ok := cursorValue.(string)
	if !ok {
		t.Fatalf("boundary retry lost its next cursor: %T", cursorValue)
	}
	advanced, err := decodeMCPContentCursor(encodedNext)
	if err != nil || advanced.CandidateOffset != 2 {
		t.Fatalf("minimum-candidate retry cursor=%+v err=%v", advanced, err)
	}

	firstReferenceFits := func(padding int) bool {
		base := map[string]any{"padding": strings.Repeat("x", padding)}
		_, refs, _, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), []byte("[[After]]"), 0, nil, mcpContentCursor{}, false, 50, base)
		return err == nil && complete && len(refs) == 1 && len(refs[0].Candidates) == 1
	}
	low, high = 0, maxMCPProtectedResponseBytes
	for low+1 < high {
		middle := low + (high-low)/2
		if firstReferenceFits(middle) {
			low = middle
		} else {
			high = middle
		}
	}
	priorBase := map[string]any{"padding": strings.Repeat("x", low)}
	_, priorPage, priorCursorValue, priorComplete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, "sha256:"+strings.Repeat("b", 64), []byte("[[After]] [[Duplicate]]"), 0, nil, mcpContentCursor{}, false, 50, priorBase)
	priorCursorText, cursorOK := priorCursorValue.(string)
	priorCursor, cursorErr := decodeMCPContentCursor(priorCursorText)
	if err != nil || priorComplete || !cursorOK || cursorErr != nil || len(priorPage) != 1 || priorPage[0].ReferenceIndex != 0 || len(priorPage[0].Candidates) != 1 || priorCursor.ReferenceIndex != 1 || priorCursor.CandidateOffset != 0 {
		t.Fatalf("later oversized candidate should keep the cursor before its first unsent item: refs=%+v cursor=%+v complete=%t err=%v cursor_err=%v", priorPage, priorCursor, priorComplete, err, cursorErr)
	}
}

func TestMCPReferenceScannerBoundsTextAndStopsForCancellation(t *testing.T) {
	overlong := []byte("[[" + strings.Repeat("x", maxMCPReferenceTargetBytes+maxMCPReferenceLabelBytes+2) + "]]")
	if scanMCPMarkdownMentions(context.Background(), overlong, 0, func(mcpMarkdownReference) bool { return true }) {
		t.Fatal("overlong wikilink was silently materialized")
	}
	destination := []byte("[label](<" + strings.Repeat("x", maxMCPReferenceTargetBytes+1) + ">)")
	if scanMCPMarkdownMentions(context.Background(), destination, 0, func(mcpMarkdownReference) bool { return true }) {
		t.Fatal("overlong Markdown destination was silently materialized")
	}

	adversarial := []byte(strings.Repeat("[", 128<<10))
	started := time.Now()
	if !scanMCPMarkdownMentions(context.Background(), adversarial, 0, func(mcpMarkdownReference) bool { return true }) {
		t.Fatal("unmatched delimiters stopped reference scanning")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("unmatched delimiters were not scanned linearly: %s", elapsed)
	}

	openTags := append(bytes.Repeat([]byte("<"), 128<<10), []byte("\n[visible](Concept)")...)
	var visible []mcpMarkdownReference
	started = time.Now()
	if !scanMCPMarkdownMentions(context.Background(), openTags, 0, func(reference mcpMarkdownReference) bool {
		visible = append(visible, reference)
		return true
	}) {
		t.Fatal("an unmatched HTML opener made reference scanning incomplete")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("repeated unmatched HTML openers were not scanned linearly: %s", elapsed)
	}
	if len(visible) != 1 || visible[0].Target != "Concept" {
		t.Fatalf("HTML delimiter scan lost the following Markdown link: %+v", visible)
	}

	unclosedScript := []byte("<script>[hidden](Hidden)" + strings.Repeat("</script", 16<<10) + " [also hidden](Hidden)")
	var hidden []mcpMarkdownReference
	started = time.Now()
	if !scanMCPMarkdownMentions(context.Background(), unclosedScript, 0, func(reference mcpMarkdownReference) bool {
		hidden = append(hidden, reference)
		return true
	}) || len(hidden) != 0 {
		t.Fatalf("unclosed script scan should complete while suppressing script text: complete_refs=%+v", hidden)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("repeated unclosed script delimiters were not scanned linearly: %s", elapsed)
	}

	var afterUnclosedWiki []mcpMarkdownReference
	if !scanMCPMarkdownMentions(context.Background(), []byte("[[unfinished\n[visible](Concept)"), 0, func(reference mcpMarkdownReference) bool {
		afterUnclosedWiki = append(afterUnclosedWiki, reference)
		return true
	}) || len(afterUnclosedWiki) != 1 || afterUnclosedWiki[0].Target != "Concept" {
		t.Fatalf("unclosed wikilink hid a later valid Markdown link: %+v", afterUnclosedWiki)
	}

	midScan, cancelMidScan := context.WithCancel(context.Background())
	var seenBeforeCancel int
	midScanBody := []byte("[first](Concept)\n" + strings.Repeat("x", 2<<20))
	if scanMCPMarkdownMentions(midScan, midScanBody, 0, func(mcpMarkdownReference) bool {
		seenBeforeCancel++
		cancelMidScan()
		return true
	}) {
		t.Fatal("scanner reported complete after cancellation during the emitting pass")
	}
	if seenBeforeCancel != 1 {
		t.Fatalf("mid-scan cancellation observed references=%d, want 1 before cancellation", seenBeforeCancel)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if scanMCPMarkdownMentions(canceled, bytes.Repeat([]byte("x"), 1<<20), 0, func(mcpMarkdownReference) bool { return true }) {
		t.Fatal("canceled reference scan continued")
	}
}

func TestMCPReferencePagesShrinkToWireSizeAndResolveCanonicalIDRoutes(t *testing.T) {
	ids := wikiindex.IDMap{Concept: map[string]string{}, Source: map[string]string{}, SourceMeta: map[string]wikiindex.SourceMeta{}, IDRedirects: map[string]string{}}
	concepts := make([]gcs.WikiPage, 0, 52)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("concept-%02d", i)
		slug := fmt.Sprintf("concept-%02d-%s", i, strings.Repeat("台", 160))
		ids.Concept[id] = slug
		concepts = append(concepts, gcs.WikiPage{ID: id, Slug: slug, Title: strings.Repeat("\"", 512)})
	}
	for i := 0; i < 21; i++ {
		id := fmt.Sprintf("source-%02d", i)
		slug := fmt.Sprintf("source-%02d-%s", i, strings.Repeat("台", 160))
		ids.Source[id] = slug
		ids.SourceMeta[id] = wikiindex.SourceMeta{Slug: slug, Title: strings.Repeat("\"", 512)}
	}
	ids.Concept["after"] = "after"
	concepts = append(concepts, gcs.WikiPage{ID: "after", Slug: "after", Title: "After"})
	ids.Concept["canonical"] = "local-development"
	concepts = append(concepts, gcs.WikiPage{ID: "canonical", Slug: "local-development", Title: "Canonical"})
	ids.IDRedirects["old-canonical"] = "canonical"
	pinned := gcs.GenerationSnapshot{ManifestSHA256: strings.Repeat("a", 64)}
	pinned.Manifest.GenerationID = "generation-wire"
	snapshot := buildTestMCPContentSnapshot(pinned, ids, concepts)
	identity := mcpProjectKeyIdentity{userID: "wire-user", projectID: "wire-project"}
	canonicalIndex, exists := snapshot.routeRefIndex(mcpContentKindConcept, "canonical-local-development")
	if !exists {
		t.Fatal("canonical route missing from snapshot")
	}
	canonicalTarget := snapshot.refs[canonicalIndex]
	if canonicalTarget.CitationPath != "/concepts/canonical-local-development" {
		t.Fatalf("canonical citation path = %q", canonicalTarget.CitationPath)
	}
	for target, wantID := range map[string]string{
		"/concepts/canonical-local-development":     "canonical",
		"/concepts/old-canonical-local-development": "canonical",
		"/concepts/local-development":               "canonical",
	} {
		_, set := snapshot.mentionCandidates(target)
		if set.Resolution != "resolved" || set.ID == nil || *set.ID != wantID {
			t.Errorf("target %q resolved to %#v", target, set)
		}
	}
	for _, target := range []string{"/sources/canonical-local-development", "/concepts/missing-local-development", "/concepts/canonical-local%2Fdevelopment"} {
		_, set := snapshot.mentionCandidates(target)
		if set.Resolution != "unresolved" {
			t.Errorf("unsafe or wrong-kind target %q resolved to %#v", target, set)
		}
	}

	document, exists := snapshot.refByID(mcpContentKindConcept, "after")
	if !exists {
		t.Fatal("after concept missing from snapshot")
	}
	target := strings.Repeat("\"", 512)
	label := strings.Repeat(`\"`, 1020)
	data := []byte("[[" + target + "|" + label + "]] and [[After]]")
	base := map[string]any{
		"schema_version": 1, "ref": document, "generation": snapshot.generation,
		"revision":          "sha256:" + strings.Repeat("b", 64),
		"content":           map[string]any{"text": strings.Repeat("x", 16<<10), "start_byte": 0, "end_byte": 16 << 10, "total_bytes": len(data), "complete": true, "next_cursor": nil},
		"synthesis_sources": []mcpResolvedRef{}, "mentions": []mcpResolvedRef{}, "provenance_status": "available", "refs_complete": true, "next_refs_cursor": nil,
	}
	revision := "sha256:" + strings.Repeat("b", 64)
	_, first, next, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, revision, data, 0, nil, mcpContentCursor{}, false, 50, base)
	if err != nil || len(first) != 1 {
		t.Fatalf("large escaped candidate page failed: refs=%d complete=%v next=%v err=%v", len(first), complete, next, err)
	}
	if complete || len(first[0].Candidates) == 0 || len(first[0].Candidates) >= 50 || first[0].CandidatesComplete {
		t.Fatalf("large escaped candidate page was not shrunk by wire size: candidates=%d complete=%v next=%v", len(first[0].Candidates), complete, next)
	}
	cursor, err := decodeMCPContentCursor(next.(string))
	if err != nil || cursor.ReferenceIndex != 0 || cursor.CandidateOffset != len(first[0].Candidates) {
		t.Fatalf("wire-size cursor skipped or repeated candidates: cursor=%+v err=%v", cursor, err)
	}
	_, second, finalCursor, complete, err := pageMCPReadReferences(context.Background(), snapshot, identity, document, revision, data, 0, nil, cursor, true, 50, base)
	if err != nil || !complete || finalCursor != nil || len(second) != 2 {
		t.Fatalf("second wire page did not finish candidates and next mention: refs=%+v next=%v complete=%v err=%v", second, finalCursor, complete, err)
	}
	seen := make(map[string]bool, 51)
	for _, page := range [][]mcpResolvedRef{first, second[:1]} {
		for _, fragment := range page {
			for _, candidate := range fragment.Candidates {
				if seen[candidate.ID] {
					t.Fatalf("candidate repeated across pages: %s", candidate.ID)
				}
				seen[candidate.ID] = true
			}
		}
	}
	if len(seen) != 51 || second[1].ReferenceIndex != 1 || second[1].TargetText != "After" {
		t.Fatalf("candidate or next-mention coverage = %d candidates, refs=%+v", len(seen), second)
	}
}

func buildTestMCPContentSnapshot(pinned gcs.GenerationSnapshot, source wikiindex.IDMap, sourcePages []gcs.WikiPage) *mcpContentSnapshot {
	ids := mcpContentIDs{}
	for id, slug := range source.Concept {
		ids.Concept = append(ids.Concept, mcpIDPair{Key: id, Value: slug})
	}
	for id, slug := range source.Source {
		ids.Source = append(ids.Source, mcpIDPair{Key: id, Value: slug})
	}
	for id, meta := range source.SourceMeta {
		ids.SourceMeta = append(ids.SourceMeta, mcpSourceMetadata{ID: id, Slug: meta.Slug, Title: meta.Title, SourceFile: meta.SourceFile})
	}
	for id, target := range source.IDRedirects {
		ids.IDRedirects = append(ids.IDRedirects, mcpIDPair{Key: id, Value: target})
	}
	pages := make([]mcpConceptPage, 0, len(sourcePages))
	for ordinal, page := range sourcePages {
		pages = append(pages, mcpConceptPage{Slug: page.Slug, Title: page.Title, Sources: page.Sources, Ordinal: ordinal})
	}
	sort.Slice(ids.SourceMeta, func(i, j int) bool { return ids.SourceMeta[i].ID < ids.SourceMeta[j].ID })
	sort.Slice(ids.IDRedirects, func(i, j int) bool { return ids.IDRedirects[i].Key < ids.IDRedirects[j].Key })
	return buildMCPContentSnapshot(nil, pinned, storage.QueryGenerationIdentity{}, ids, pages)
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
