// Package otlpfixture provides a loopback OTLP collector for integration tests.
package otlpfixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const maxCaptureBytes = 1 << 20

type Capture struct {
	server *httptest.Server
	mu     sync.Mutex
	items  []*tracepb.ExportTraceServiceRequest
	err    string
	ids    []SpanIdentity
}

type LinkIdentity struct{ TraceID, SpanID string }

type SpanIdentity struct {
	Scope, Name, TraceID, SpanID, ParentTraceID, ParentSpanID string
	Links                                                     []LinkIdentity
	Attributes                                                map[string]string
}

func New(t testing.TB) *Capture {
	t.Helper()
	capture := &Capture{}
	capture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
			capture.fail("unexpected OTLP request method or path")
			http.Error(w, "invalid OTLP request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCaptureBytes+1))
		if err != nil || len(body) > maxCaptureBytes {
			capture.fail("OTLP request body was unreadable or over limit")
			http.Error(w, "invalid OTLP request", http.StatusBadRequest)
			return
		}
		request := &tracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, request); err != nil {
			capture.fail("OTLP request was not protobuf")
			http.Error(w, "invalid OTLP request", http.StatusBadRequest)
			return
		}
		capture.mu.Lock()
		capture.items = append(capture.items, request)
		capture.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(capture.server.Close)
	return capture
}

func (c *Capture) fail(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == "" {
		c.err = message
	}
}

func (c *Capture) Exporter() (sdktrace.SpanExporter, error) {
	return otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(c.server.URL+"/v1/traces"),
		otlptracehttp.WithHTTPClient(&http.Client{Timeout: 2 * time.Second}),
		otlptracehttp.WithMaxRequestSize(maxCaptureBytes),
		otlptracehttp.WithTimeout(2*time.Second),
	)
}

func (c *Capture) Provider(t testing.TB, service, revision, project string) *sdktrace.TracerProvider {
	t.Helper()
	previous := otel.GetTracerProvider()
	var provider *sdktrace.TracerProvider
	if CloudMode() {
		revision = strings.TrimSpace(os.Getenv("LWC_OTLP_QA_REVISION"))
		if os.Getenv(observability.OTLPEndpointEnv) != "https://telemetry.googleapis.com" || project == "" || revision == "" {
			t.Fatal("Cloud Trace QA requires the approved endpoint and explicit synthetic project/revision")
		}
		var err error
		provider, err = observability.InitTracesWithProjectAndProcessors(service, revision, project, c)
		if err != nil {
			t.Fatal("initialize Cloud Trace QA exporter")
		}
	} else {
		exporter, err := c.Exporter()
		if err != nil {
			t.Fatal("create loopback OTLP exporter")
		}
		provider, err = observability.NewFilteredTraceProvider(io.Discard, service, revision, project, exporter, c)
		if err != nil {
			t.Fatal("create filtered test trace provider")
		}
		otel.SetTracerProvider(provider)
	}
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		observability.ShutdownTraces(provider)
	})
	return provider
}

func CloudMode() bool { return os.Getenv("LWC_OTLP_CLOUD_QA") == "1" }

func NewParent(t testing.TB) (traceID, parentID, header string) {
	t.Helper()
	var tid trace.TraceID
	var sid trace.SpanID
	if _, err := rand.Read(tid[:]); err != nil {
		t.Fatal("generate synthetic Trace ID")
	}
	if _, err := rand.Read(sid[:]); err != nil {
		t.Fatal("generate synthetic parent Span ID")
	}
	if !tid.IsValid() || !sid.IsValid() {
		t.Fatal("synthetic Trace ID or parent Span ID was zero")
	}
	traceID, parentID = tid.String(), sid.String()
	return traceID, parentID, "00-" + hex.EncodeToString(tid[:]) + "-" + hex.EncodeToString(sid[:]) + "-01"
}

func (c *Capture) Requests() []*tracepb.ExportTraceServiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]*tracepb.ExportTraceServiceRequest, len(c.items))
	for i, request := range c.items {
		result[i] = proto.Clone(request).(*tracepb.ExportTraceServiceRequest)
	}
	return result
}

func (c *Capture) Endpoint() string { return c.server.URL + "/v1/traces" }

func (c *Capture) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (c *Capture) OnEnd(span sdktrace.ReadOnlySpan) {
	ctx, parent := span.SpanContext(), span.Parent()
	item := SpanIdentity{
		Scope: span.InstrumentationScope().Name, Name: span.Name(),
		TraceID: ctx.TraceID().String(), SpanID: ctx.SpanID().String(), Attributes: make(map[string]string),
	}
	if parent.IsValid() {
		item.ParentTraceID, item.ParentSpanID = parent.TraceID().String(), parent.SpanID().String()
	}
	for _, link := range span.Links() {
		item.Links = append(item.Links, LinkIdentity{TraceID: link.SpanContext.TraceID().String(), SpanID: link.SpanContext.SpanID().String()})
	}
	for _, attr := range span.Attributes() {
		switch string(attr.Key) {
		case "query.outcome", "query.business_outcome", "query.dependency_degraded", "query.degradation_reason", "query.stage", "http.response.status_code":
			item.Attributes[string(attr.Key)] = attr.Value.Emit()
		}
	}
	c.mu.Lock()
	c.ids = append(c.ids, item)
	c.mu.Unlock()
}

func (c *Capture) Shutdown(context.Context) error   { return nil }
func (c *Capture) ForceFlush(context.Context) error { return nil }

func (c *Capture) Identities() []SpanIdentity {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := append([]SpanIdentity(nil), c.ids...)
	for i := range result {
		result[i].Links = append([]LinkIdentity(nil), result[i].Links...)
		if result[i].Attributes != nil {
			attributes := make(map[string]string, len(result[i].Attributes))
			for key, value := range result[i].Attributes {
				attributes[key] = value
			}
			result[i].Attributes = attributes
		}
	}
	return result
}

func (c *Capture) LogIdentities(t testing.TB, caseID string) {
	t.Helper()
	for _, item := range c.Identities() {
		links := make([]string, 0, len(item.Links))
		for _, link := range item.Links {
			links = append(links, link.TraceID+"/"+link.SpanID)
		}
		t.Logf("OTLP_QA_SPAN case=%s trace_id=%s span_id=%s parent_trace_id=%s parent_span_id=%s scope=%s name=%s links=%s",
			caseID, item.TraceID, item.SpanID, item.ParentTraceID, item.ParentSpanID, item.Scope, item.Name, strings.Join(links, ","))
	}
}

func (c *Capture) Spans() []*tracev1.Span {
	var spans []*tracev1.Span
	for _, request := range c.Requests() {
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					spans = append(spans, span)
				}
			}
		}
	}
	return spans
}

func (c *Capture) Error() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Capture) Flush(t testing.TB, provider *sdktrace.TracerProvider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := provider.ForceFlush(ctx); err != nil {
		t.Fatalf("flush filtered OTLP fixture: %v", err)
	}
	if message := c.Error(); message != "" {
		t.Fatalf("loopback OTLP collector: %s", message)
	}
}
