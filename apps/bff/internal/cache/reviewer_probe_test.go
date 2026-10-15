package cache

import (
 "context"
 "fmt"
 "testing"
 "github.com/rayer/llm-wiki-bff/internal/gcs"
 "github.com/rayer/llm-wiki-bff/internal/observability"
 "go.opentelemetry.io/otel"
 sdktrace "go.opentelemetry.io/otel/sdk/trace"
 "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestReviewerRebuildBudget(t *testing.T) {
 exp:=tracetest.NewInMemoryExporter()
 tp:=sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
 old:=otel.GetTracerProvider(); otel.SetTracerProvider(tp); defer otel.SetTracerProvider(old)
 ctx,parent:=otel.Tracer("llm-wiki-bff/query").Start(context.Background(),"query.stage.cache_load")
 ctx=observability.WithStorageReadBudget(ctx)
 // An earlier JSONL read belongs to cache_load, not the new rebuild invocation.
 _,finish:=observability.StartStorageRead(ctx,"concept_cache"); finish(nil,"not_required","none")
 r:=&fakeReader{pages:map[string]string{}}
 for i:=0;i<20;i++ {s:=fmt.Sprint(i); r.concepts=append(r.concepts,gcs.WikiPage{Slug:s}); r.pages[s]="body"}
 if _,err:=New().buildFromPages(ctx,r);err!=nil {t.Fatal(err)}
 parent.End()
 for _,s:=range exp.GetSpans(){ if s.Name=="query.cache.rebuild" || s.Name=="query.stage.cache_load" {t.Logf("phase=%s attrs=%v",s.Name,s.Attributes)} }
 n:=0; for _,s:=range exp.GetSpans(){if s.Name=="query.storage.read" {n++}}
 t.Logf("total detail=%d actual page calls=%d",n,r.getPageCalls.Load())
}
