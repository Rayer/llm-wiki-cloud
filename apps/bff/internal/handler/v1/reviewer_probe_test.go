package v1

import (
	"context"
	"encoding/json"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"testing"
)

func TestReviewerSourceInventoryClosesOnConceptMismatch(t *testing.T) {
	s, a := profileQueryFixture(t, "u/p", "G1-retained")
	var ids wikiindex.IDMap
	if err := json.Unmarshal(s.objects["cache/id_map.json"], &ids); err != nil {
		t.Fatal(err)
	}
	for k := range ids.Concept {
		ids.Concept[k] = "wrong-slug"
	}
	b, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	s.objects["cache/id_map.json"] = b
	for i := range s.manifest.Files {
		if s.manifest.Files[i].Path == "cache/id_map.json" {
			s.manifest.Files[i].SHA256 = generation.Digest(b)
			s.manifest.Files[i].Size = int64(len(b))
		}
	}
	s.snapshot.IDMapDigest = generation.Digest(b)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(old)
	_, err = query.LoadProfile(context.Background(), cache.New(), s, s.manifest, profiletags.ActiveRef{ContentGeneration: a.ContentGeneration, DictionaryRevision: a.DictionaryRevision, TagSetRevision: a.TagSetRevision, QueryRuleRevision: a.QueryRuleRevision}, s.dictionaryRef, []string{"r1"})
	if err == nil {
		t.Fatal("guard did not reject")
	}
	started, ended := 0, 0
	for _, sp := range rec.Started() {
		for _, a := range sp.Attributes() {
			if string(a.Key) == "query.object_role" && a.Value.AsString() == "source_inventory" {
				started++
			}
		}
	}
	for _, sp := range rec.Ended() {
		for _, a := range sp.Attributes() {
			if string(a.Key) == "query.object_role" && a.Value.AsString() == "source_inventory" {
				ended++
			}
		}
	}
	t.Logf("error=%v source_inventory started=%d ended=%d", err, started, ended)
	if started != ended {
		t.Fatal("source inventory span leaked")
	}
}
