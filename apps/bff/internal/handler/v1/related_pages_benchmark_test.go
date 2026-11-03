package v1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
)

func relationBenchmarkSourceMap() []byte {
	return []byte(`{"source":{"` + relationSourceOneID + `":"source-one"},"source_meta":{"` + relationSourceOneID + `":{"slug":"source-one","title":"Source One","source_file":"raw/source-one.md"}}}`)
}

func relationBenchmarkConceptJSONL(count int) []byte {
	var data strings.Builder
	for i := 0; i < count; i++ {
		row := map[string]any{
			"slug":  fmt.Sprintf("concept-%03d", i),
			"title": fmt.Sprintf("Concept %03d", i),
			"frontmatter": map[string]any{
				"id":      fmt.Sprintf("concept-id-%03d", i),
				"sources": []string{"source-one"},
			},
			"body": "synthetic benchmark body",
		}
		encoded, _ := json.Marshal(row)
		data.Write(encoded)
		data.WriteByte('\n')
	}
	return []byte(data.String())
}

func relationBenchmarkNearLimitJSONL() []byte {
	const fixedOverhead = len(`{"slug":"near-limit","title":"Near Limit","frontmatter":{"id":"near-id","sources":["source-one"]},"body":""}`) + 1
	body := strings.Repeat("x", int(conceptsRelationMaxBytes)-fixedOverhead)
	for {
		row := map[string]any{
			"slug": "near-limit", "title": "Near Limit",
			"frontmatter": map[string]any{"id": "near-id", "sources": []string{"source-one"}},
			"body":        body,
		}
		encoded, _ := json.Marshal(row)
		encoded = append(encoded, '\n')
		if int64(len(encoded)) <= conceptsRelationMaxBytes {
			return encoded
		}
		body = body[:len(body)-int(int64(len(encoded))-conceptsRelationMaxBytes)]
	}
}

func benchmarkRelationSnapshot(b *testing.B, data []byte) {
	b.Helper()
	sourceData := relationBenchmarkSourceMap()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		concepts, err := gcs.WikiPagesFromConceptsJSONL(data)
		if err != nil {
			b.Fatal(err)
		}
		sources, err := gcs.WikiPagesFromSourceIDMapForDetailRelations(sourceData)
		if err != nil {
			b.Fatal(err)
		}
		snapshot := buildRelationSnapshot(concepts, sources)
		if snapshot == nil || snapshot.retainedBytes != 0 {
			// Retained-byte estimation is intentionally part of cold load, not the parser benchmark.
			b.Fatal("unexpected relation snapshot state")
		}
		_ = relationSnapshotRetainedBytes(snapshot)
	}
}

func BenchmarkDetailRelationIndex478(b *testing.B) {
	benchmarkRelationSnapshot(b, relationBenchmarkConceptJSONL(478))
}

func BenchmarkDetailRelationIndexNear10MiB(b *testing.B) {
	benchmarkRelationSnapshot(b, relationBenchmarkNearLimitJSONL())
}
