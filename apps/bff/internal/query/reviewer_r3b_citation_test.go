package query

import (
 "context"
 "net/http"
 "testing"
 "github.com/rayer/llm-wiki-bff/internal/llm"
 "go.opentelemetry.io/otel"
 sdktrace "go.opentelemetry.io/otel/sdk/trace"
 "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestReviewerR3BCitationValidationObservation(t *testing.T) {
 exp := tracetest.NewInMemoryExporter()
 tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
 old := otel.GetTracerProvider(); otel.SetTracerProvider(tp)
 defer func(){otel.SetTracerProvider(old); _ = tp.Shutdown(context.Background())}()
 oldTransport := http.DefaultTransport
 http.DefaultTransport = &queryLLMTransport{t:t}
 defer func(){http.DefaultTransport=oldTransport}()
 service, reader := serviceFixture(t, "{\"slug\":\"alpha-coffee\",\"title\":\"Alpha Coffee\",\"body\":\"coffee and espresso\"}\n")
 service.llm = llm.NewClient("test")
 ctx,root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(),"query.request")
 ctx,rec := WithReceipt(ctx)
 result,err := service.Execute(ctx,reader,Request{Query:"coffee",Mode:"full"})
 root.End()
 if err != nil || result.AISynth != "answer [Alpha Coffee]" || len(result.Citations)!=1 {t.Fatalf("fixture did not resolve citation: result=%+v err=%v",result,err)}
 events:=0
 for _,s:=range exp.GetSpans(){ events+=len(s.Events); t.Logf("span=%s events=%v",s.Name,s.Events) }
 t.Logf("resolved citations=%d host receipts=%d total events=%d",len(result.Citations),len(rec.Receipt().HostCalls),events)
 if events==0 {t.Fatal("actual citation resolution emitted no finite validation event (contract line 15)")}
}
