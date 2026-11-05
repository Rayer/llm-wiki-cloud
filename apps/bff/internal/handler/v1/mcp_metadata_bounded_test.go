package v1

import (
	"bytes"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

func TestMCPMetadataMinimalDecodeKeepsOnlyPublishedFields(t *testing.T) {
	idMap := []byte(`{"concept":{"concept-1":"alpha"},"source":{"source-1":"paper"},"source_meta":{"source-1":{"slug":"paper","title":"Paper","source_file":"raw/paper.md","nested":{"a":[1,{"b":true}]}}},"id_redirects":{"old-concept":"concept-1"},"redirects":{"old-slug":["new-slug"]},"unused":{"deep":[{"field":"ignored"}]}}`)
	idShape, err := inspectMCPIDMap(idMap)
	if err != nil {
		t.Fatalf("inspect id_map: %v", err)
	}
	ids, err := decodeMCPIDMap(idMap, idShape)
	if err != nil {
		t.Fatalf("decode id_map: %v", err)
	}
	if len(ids.Concept) != 1 || ids.Concept[0] != (mcpIDPair{Key: "concept-1", Value: "alpha"}) || len(ids.Source) != 1 || ids.Source[0].Value != "paper" || len(ids.SourceMeta) != 1 || ids.SourceMeta[0].SourceFile != "raw/paper.md" || len(ids.IDRedirects) != 1 || ids.IDRedirects[0].Value != "concept-1" {
		t.Fatalf("minimal id_map decode = %+v", ids)
	}
	if idShape.RedirectEntries != 1 || idShape.RedirectItems != 1 {
		t.Fatalf("skipped redirect shape = %+v", idShape)
	}

	concepts := []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"concept-1\",\"sources\":[\"source-1\",\"source-1\",\"source-2\"],\"unknown\":{\"deep\":[1,{\"ignored\":true}]}}}\n")
	conceptShape, err := inspectMCPConcepts(concepts)
	if err != nil {
		t.Fatalf("inspect concepts: %v", err)
	}
	pages, err := decodeMCPConcepts(concepts, conceptShape)
	if err != nil {
		t.Fatalf("decode concepts: %v", err)
	}
	if len(pages) != 1 || pages[0].Slug != "alpha" || pages[0].Title != "Alpha" || len(pages[0].Sources) != 2 || pages[0].Sources[0] != "source-1" || pages[0].Sources[1] != "source-2" {
		t.Fatalf("minimal concepts decode = %+v", pages)
	}
	if conceptShape.SourceItems != 3 {
		t.Fatalf("raw source item count = %d, want duplicate-preserving 3", conceptShape.SourceItems)
	}
}

func TestMCPMetadataSliceDecoderMatchesPublisherReaders(t *testing.T) {
	_, idMapBytes, conceptsBytes := mcpMetadataHeavyPublishedFixture(t, "mapping-user", "mapping-project", 25)
	expectedIDs, err := wikiindex.DecodeIDMap(idMapBytes)
	if err != nil {
		t.Fatal(err)
	}
	expectedPages, err := gcs.WikiPagesFromConceptsJSONL(conceptsBytes)
	if err != nil {
		t.Fatal(err)
	}
	idShape, err := inspectMCPIDMap(idMapBytes)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs, err := decodeMCPIDMap(idMapBytes, idShape)
	if err != nil {
		t.Fatal(err)
	}
	for id, slug := range expectedIDs.Concept {
		if got, ok := findMCPIDPair(gotIDs.Concept, id); !ok || got.Value != slug {
			t.Errorf("concept mapping %q = %+v present=%t, want %q", id, got, ok, slug)
		}
	}
	for id, slug := range expectedIDs.Source {
		if got, ok := findMCPIDPair(gotIDs.Source, id); !ok || got.Value != slug {
			t.Errorf("source mapping %q = %+v present=%t, want %q", id, got, ok, slug)
		}
	}
	for id, expected := range expectedIDs.SourceMeta {
		got, ok := findMCPSourceMeta(gotIDs.SourceMeta, id)
		if !ok || got.Slug != expected.Slug || got.Title != expected.Title || got.SourceFile != expected.SourceFile {
			t.Errorf("source metadata %q = %+v present=%t, want %+v", id, got, ok, expected)
		}
	}
	for id, target := range expectedIDs.IDRedirects {
		index := sort.Search(len(gotIDs.IDRedirects), func(i int) bool { return gotIDs.IDRedirects[i].Key >= id })
		if index >= len(gotIDs.IDRedirects) || gotIDs.IDRedirects[index].Key != id || gotIDs.IDRedirects[index].Value != target {
			t.Errorf("ID redirect %q missing or wrong", id)
		}
	}
	shape, err := inspectMCPConcepts(conceptsBytes)
	if err != nil {
		t.Fatal(err)
	}
	gotPages, err := decodeMCPConcepts(conceptsBytes, shape)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPages) != len(expectedPages) {
		t.Fatalf("concept page count=%d, want %d", len(gotPages), len(expectedPages))
	}
	for i := range expectedPages {
		if gotPages[i].Slug != expectedPages[i].Slug || gotPages[i].Title != expectedPages[i].Title || !sameMCPStrings(gotPages[i].Sources, expectedPages[i].Sources) {
			t.Errorf("concept page %d = %+v, want slug/title/sources %q/%q/%v", i, gotPages[i], expectedPages[i].Slug, expectedPages[i].Title, expectedPages[i].Sources)
		}
	}
}

func TestMCPMetadataRejectsShapeAndDepthOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		id   bool
	}{
		{name: "concept nesting over limit", data: []byte(`{"slug":"x","unknown":` + nestedMCPArrays(mcpMetadataMaxDepth) + `}`)},
		{name: "id map nesting over limit", id: true, data: []byte(`{"extra":` + nestedMCPArrays(mcpMetadataMaxDepth) + `}`)},
		{name: "concept too many top fields", data: tooManyMCPObjectFields(mcpMetadataMaxTopFields + 1)},
		{name: "source meta too many fields", id: true, data: []byte(`{"source_meta":{"s":{"slug":"s","f0":0,"f1":0,"f2":0,"f3":0,"f4":0,"f5":0,"f6":0,"f7":0,"f8":0,"f9":0,"f10":0,"f11":0,"f12":0,"f13":0,"f14":0,"f15":0}}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.id {
				_, err = inspectMCPIDMap(tc.data)
			} else {
				_, err = inspectMCPConcepts(tc.data)
			}
			if err == nil {
				t.Fatal("overflowing metadata shape was accepted")
			}
		})
	}

	if _, err := inspectMCPConcepts([]byte(`{"slug":"x","unknown":` + nestedMCPArrays(mcpMetadataMaxDepth-1) + `}`)); err != nil {
		t.Fatalf("maximum legal nesting rejected: %v", err)
	}
}

func TestMCPMetadataRejectsMalformedJSONAndDuplicateMappings(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"concept":{},"concept":{}}`),
		[]byte(`{"unused":{"value":"bad\xescape"}}`),
		[]byte(`{"unused":{"value":01}}`),
		[]byte(`{"concept":{}} trailing`),
	} {
		if _, err := inspectMCPIDMap(data); err == nil {
			t.Errorf("malformed id_map accepted: %s", data)
		}
	}
	idMap := []byte(`{"concept":{"same":"first","same":"second"}}`)
	shape, err := inspectMCPIDMap(idMap)
	if err != nil {
		t.Fatalf("inspect duplicate mapping: %v", err)
	}
	if _, err := decodeMCPIDMap(idMap, shape); err == nil {
		t.Fatal("duplicate mapping survived bounded materialization")
	}
	for _, data := range [][]byte{
		[]byte(`{"slug":"first","slug":"second"}`),
		[]byte(`{"slug":"x","unused":{"value":"bad\xescape"}}`),
		[]byte(`{"slug":"x","unused":{"value":01}}`),
		[]byte(`{"slug":"x"} {"slug":"y"}`),
	} {
		if _, err := inspectMCPConcepts(data); err == nil {
			t.Errorf("malformed concept metadata accepted: %s", data)
		}
	}
}

func TestMCPMetadataRejectsCollectionAndRowOverflowBeforeDecode(t *testing.T) {
	var idMap bytes.Buffer
	idMap.WriteString(`{"concept":{`)
	for i := 0; i <= 10_000; i++ {
		if i > 0 {
			idMap.WriteByte(',')
		}
		fmt.Fprintf(&idMap, `"id-%05d":"slug-%05d"`, i, i)
	}
	idMap.WriteString(`}}`)
	if _, err := inspectMCPIDMap(idMap.Bytes()); err == nil {
		t.Fatal("id_map collection over 10,000 entries was accepted")
	}

	var concepts bytes.Buffer
	for i := 0; i <= 10_000; i++ {
		fmt.Fprintf(&concepts, `{"slug":"slug-%05d"}`+"\n", i)
	}
	if _, err := inspectMCPConcepts(concepts.Bytes()); err == nil {
		t.Fatal("concept metadata over 10,000 rows was accepted")
	}
}

func TestMCPMetadataExactCollectionAndRowCapsUseSizedSlices(t *testing.T) {
	var idMap bytes.Buffer
	idMap.WriteString(`{"concept":{`)
	var concepts bytes.Buffer
	for i := 0; i < generation.MaxFiles; i++ {
		if i > 0 {
			idMap.WriteByte(',')
		}
		id, slug := fmt.Sprintf("concept-%05d", i), fmt.Sprintf("slug-%05d", i)
		fmt.Fprintf(&idMap, `%q:%q`, id, slug)
		fmt.Fprintf(&concepts, `{"slug":%q}`+"\n", slug)
	}
	idMap.WriteString(`}}`)
	idShape, err := inspectMCPIDMap(idMap.Bytes())
	if err != nil || idShape.ConceptEntries != generation.MaxFiles {
		t.Fatalf("exact maximum id_map shape=%+v err=%v", idShape, err)
	}
	ids, err := decodeMCPIDMap(idMap.Bytes(), idShape)
	if err != nil || len(ids.Concept) != generation.MaxFiles || cap(ids.Concept) != generation.MaxFiles {
		t.Fatalf("exact maximum id_map decode len=%d cap=%d err=%v", len(ids.Concept), cap(ids.Concept), err)
	}
	conceptShape, err := inspectMCPConcepts(concepts.Bytes())
	if err != nil || len(conceptShape.Rows) != generation.MaxFiles || conceptShape.IncludedRows != generation.MaxFiles {
		t.Fatalf("exact maximum concepts shape rows=%d included=%d err=%v", len(conceptShape.Rows), conceptShape.IncludedRows, err)
	}
	pages, err := decodeMCPConcepts(concepts.Bytes(), conceptShape)
	if err != nil || len(pages) != generation.MaxFiles || cap(pages) != generation.MaxFiles {
		t.Fatalf("exact maximum concepts decode len=%d cap=%d err=%v", len(pages), cap(pages), err)
	}
	pinned := gcs.GenerationSnapshot{ManifestSHA256: strings.Repeat("a", 64)}
	pinned.Manifest.GenerationID = "max-cardinality-generation"
	snapshot := buildMCPContentSnapshot(nil, pinned, storage.QueryGenerationIdentity{}, ids, pages)
	if len(snapshot.refs) != generation.MaxFiles || cap(snapshot.refs) != generation.MaxFiles || cap(snapshot.slugIndex) != generation.MaxFiles || cap(snapshot.routeIndex) != generation.MaxFiles {
		t.Fatalf("exact maximum snapshot refs len/cap=%d/%d slug cap=%d route cap=%d", len(snapshot.refs), cap(snapshot.refs), cap(snapshot.slugIndex), cap(snapshot.routeIndex))
	}
}

func TestMCPMetadataWorkingSetFormulaCoversBoundedSliceDecode(t *testing.T) {
	_, idMapBytes, conceptsBytes := mcpMetadataHeavyPublishedFixture(t, "memory-user", "memory-project", 100)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var peakMu sync.Mutex
	peakHeap := before.HeapAlloc
	stopSampling := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSampling:
				return
			case <-ticker.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				peakMu.Lock()
				peakHeap = max(peakHeap, sample.HeapAlloc)
				peakMu.Unlock()
			}
		}
	}()
	idShape, err := inspectMCPIDMap(idMapBytes)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := decodeMCPIDMap(idMapBytes, idShape)
	if err != nil {
		t.Fatal(err)
	}
	conceptShape, err := inspectMCPConcepts(conceptsBytes)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := decodeMCPConcepts(conceptsBytes, conceptShape)
	if err != nil {
		t.Fatal(err)
	}
	pinned := gcs.GenerationSnapshot{ManifestSHA256: strings.Repeat("a", 64)}
	pinned.Manifest.GenerationID = "memory-generation"
	snapshot := buildMCPContentSnapshot(nil, pinned, storage.QueryGenerationIdentity{}, ids, pages)
	close(stopSampling)
	sampler.Wait()
	peakMu.Lock()
	peak := peakHeap
	peakMu.Unlock()
	ids, pages = mcpContentIDs{}, nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	idInputReserve := mcpMetadataInputReservation(wikiindex.IDMapPath, int64(len(idMapBytes)))
	idDecodeReserve := mcpIDMapDecodeReservation(int64(len(idMapBytes)), idShape)
	conceptsInputReserve := mcpMetadataInputReservation(wikiindex.ConceptsJSONLPath, int64(len(conceptsBytes)))
	conceptsDecodeReserve := mcpConceptDecodeReservation(int64(len(conceptsBytes)), conceptShape)
	snapshotReserve := mcpSnapshotWorkingSetReservation(int64(len(idMapBytes)), int64(len(conceptsBytes)), idShape, conceptShape)
	allocated := after.TotalAlloc - before.TotalAlloc
	peakReserve := max(idInputReserve+idDecodeReserve, idDecodeReserve+conceptsInputReserve, idDecodeReserve+conceptsInputReserve+conceptsDecodeReserve, idDecodeReserve+conceptsInputReserve+conceptsDecodeReserve+snapshotReserve)
	peakDelta := int64(peak) - int64(before.HeapAlloc)
	if peakDelta > peakReserve {
		t.Fatalf("sampled metadata decode/build peak exceeds staged reservation: peak=%d reserved=%d", peakDelta, peakReserve)
	}
	if len(snapshot.refs) != 200 || len(snapshot.sourceMeta) != 100 || len(snapshot.idRedirects) != 100 || len(snapshot.concepts) != 100 {
		t.Fatalf("snapshot shape refs=%d source_meta=%d id_redirects=%d concepts=%d", len(snapshot.refs), len(snapshot.sourceMeta), len(snapshot.idRedirects), len(snapshot.concepts))
	}
	t.Logf("bounded_metadata_bytes id_map=%d concepts=%d id_map_entries=%d source_meta=%d id_redirects=%d rows=%d source_items=%d id_map_input=%d id_map_decode=%d concepts_input=%d concepts_decode=%d snapshot=%d staged_peak_reserve=%d sampled_heap_peak_delta=%d total_alloc_delta=%d retained_heap_delta=%d", len(idMapBytes), len(conceptsBytes), idShape.ConceptEntries+idShape.SourceEntries, idShape.SourceMetaEntries, idShape.IDRedirectEntries, conceptShape.IncludedRows, conceptShape.SourceItems, idInputReserve, idDecodeReserve, conceptsInputReserve, conceptsDecodeReserve, snapshotReserve, peakReserve, peakDelta, allocated, int64(after.HeapAlloc)-int64(before.HeapAlloc))
}

func nestedMCPArrays(count int) string {
	return strings.Repeat("[", count) + "0" + strings.Repeat("]", count)
}

func tooManyMCPObjectFields(count int) []byte {
	var value bytes.Buffer
	value.WriteString(`{"slug":"x"`)
	for i := 1; i < count; i++ {
		fmt.Fprintf(&value, `,"field-%02d":null`, i)
	}
	value.WriteByte('}')
	return value.Bytes()
}

func findMCPIDPair(values []mcpIDPair, id string) (mcpIDPair, bool) {
	index := sort.Search(len(values), func(i int) bool { return values[i].Key >= id })
	if index < len(values) && values[index].Key == id {
		return values[index], true
	}
	return mcpIDPair{}, false
}

func sameMCPStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestMCPConceptSnapshotReservationCoversDenseSourceStrings(t *testing.T) {
	idMapBytes := []byte(`{"concept":{"c":"x"}}`)
	idShape, err := inspectMCPIDMap(idMapBytes)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		duplicate bool
	}{
		{name: "distinct"},
		{name: "duplicate", duplicate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := denseMCPConceptSourceFixture(test.duplicate, 200_000)
			shape, err := inspectMCPConcepts(data)
			if err != nil {
				t.Fatal(err)
			}
			if shape.SourceItems != 200_000 {
				t.Fatalf("source items = %d, want 200000", shape.SourceItems)
			}

			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			ids, err := decodeMCPIDMap(idMapBytes, idShape)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := decodeMCPConcepts(data, shape)
			if err != nil {
				t.Fatal(err)
			}
			pinned := gcs.GenerationSnapshot{ManifestSHA256: strings.Repeat("a", 64)}
			pinned.Manifest.GenerationID = "dense-source-memory"
			snapshot := buildMCPContentSnapshot(nil, pinned, storage.QueryGenerationIdentity{}, ids, pages)
			ids, pages = mcpContentIDs{}, nil
			runtime.GC()
			runtime.ReadMemStats(&after)
			reserve := mcpSnapshotWorkingSetReservation(int64(len(idMapBytes)), int64(len(data)), idShape, shape)
			retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			t.Logf("dense_source duplicate=%t input_bytes=%d source_items=%d retained_len=%d retained_cap=%d snapshot_reserve=%d retained_heap_delta=%d", test.duplicate, len(data), shape.SourceItems, len(snapshot.concepts[0].Sources), cap(snapshot.concepts[0].Sources), reserve, retained)
			if retained > reserve {
				t.Fatalf("retained snapshot exceeds reservation: retained=%d reserve=%d", retained, reserve)
			}
			if test.duplicate {
				sources := snapshot.concepts[0].Sources
				if len(sources) != 1 || cap(sources) != shape.SourceItems {
					t.Fatalf("deduplicated sources len=%d cap=%d, want 1 and %d", len(sources), cap(sources), shape.SourceItems)
				}
				retainedSources := sources[:cap(sources)]
				for i, source := range retainedSources[1:] {
					if source != "" {
						t.Fatalf("duplicate tail retained source at index %d", i+1)
					}
				}
			} else if len(snapshot.concepts[0].Sources) != shape.SourceItems {
				t.Fatalf("distinct source count = %d, want %d", len(snapshot.concepts[0].Sources), shape.SourceItems)
			}
			runtime.KeepAlive(data)
			runtime.KeepAlive(shape)
			snapshot.close()
		})
	}
}

func denseMCPConceptSourceFixture(duplicate bool, count int) []byte {
	var data bytes.Buffer
	data.Grow(20*count + 64)
	data.WriteString(`{"slug":"x","frontmatter":{"sources":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			data.WriteByte(',')
		}
		if duplicate {
			data.WriteString(`"abcdefghijklmnopq"`)
		} else {
			fmt.Fprintf(&data, `"source-%010d"`, i)
		}
	}
	data.WriteString(`]}}`)
	return data.Bytes()
}

func TestMCPContentSnapshotCloseRefundsOnceAndDropsRetainedMetadata(t *testing.T) {
	releases := 0
	snapshot := &mcpContentSnapshot{
		client:      new(gcs.Client),
		generation:  "generation",
		manifestSHA: strings.Repeat("a", 64),
		manifest:    generation.Manifest{GenerationID: "generation", Files: []generation.File{{Path: "wiki/x.md"}}},
		identity:    storage.QueryGenerationIdentity{ProjectID: "project", GenerationID: "generation"},
		refs:        []mcpContentRef{{ID: "id"}},
		slugIndex:   []mcpSlugIndex{{slug: "slug"}},
		routeIndex:  []mcpRouteIndex{{id: "id"}},
		concepts:    []mcpConceptPage{{Slug: "slug", Sources: []string{"source"}}},
		sourceMeta:  []mcpSourceMetadata{{ID: "source"}},
		idRedirects: []mcpIDPair{{Key: "old", Value: "new"}},
		releases: []func(){
			func() { releases++ },
			func() { releases++ },
		},
	}

	snapshot.close()
	snapshot.close()
	if releases != 2 {
		t.Fatalf("release callbacks = %d, want 2 exactly once", releases)
	}
	if snapshot.client != nil || snapshot.generation != "" || snapshot.manifestSHA != "" || snapshot.manifest.Files != nil || snapshot.identity != (storage.QueryGenerationIdentity{}) || snapshot.refs != nil || snapshot.slugIndex != nil || snapshot.routeIndex != nil || snapshot.concepts != nil || snapshot.sourceMeta != nil || snapshot.idRedirects != nil || snapshot.releases != nil {
		t.Fatalf("snapshot close retained metadata: %+v", snapshot)
	}
}
