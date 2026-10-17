package observability

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/proto"
)

func TestOTLPCaptureUsesFilteredGateAndSanitizesPropagationPayloads(t *testing.T) {
	const traceStateCanary = "PRIVATE_TRACESTATE_CANARY"
	const linkCanary = "PRIVATE_LINK_ATTRIBUTE_CANARY"
	const eventCanary = "PRIVATE_EVENT_CANARY"
	const statusCanary = "PRIVATE_STATUS_CANARY"
	var encoded []byte
	var request tracepb.ExportTraceServiceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("OTLP path = %q", r.URL.Path)
		}
		var err error
		encoded, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OTLP body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := proto.Unmarshal(encoded, &request); err != nil {
			t.Errorf("decode OTLP protobuf: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := newOTLPHTTPExporter(context.Background(), server.URL+"/v1/traces", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal("create OTLP exporter")
	}
	provider, err := newTraceProviderWithOTLP(io.Discard, "lwc-qa-synthetic", "source-test", "synthetic-project", exporter)
	if err != nil {
		t.Fatal(err)
	}
	defer ShutdownTraces(provider)

	traceID, _ := trace.TraceIDFromHex("11111111111111111111111111111111")
	parentID, _ := trace.SpanIDFromHex("2222222222222222")
	linkID, _ := trace.SpanIDFromHex("3333333333333333")
	state, err := trace.ParseTraceState("vendor=" + traceStateCanary)
	if err != nil {
		t.Fatal(err)
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: parentID, TraceFlags: trace.FlagsSampled, TraceState: state, Remote: true})
	link := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: linkID, TraceFlags: trace.FlagsSampled, TraceState: state, Remote: true})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)
	_, span := provider.Tracer(queryInstrumentationScope).Start(ctx, "query.request", trace.WithLinks(trace.Link{
		SpanContext: link,
		Attributes:  []attribute.KeyValue{attribute.String("private", linkCanary)},
	}))
	span.AddEvent("unreviewed", trace.WithAttributes(attribute.String("private", eventCanary)))
	span.SetStatus(codes.Error, statusCanary)
	span.End()
	flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := provider.ForceFlush(flushCtx); err != nil {
		t.Fatalf("flush OTLP spans: %v", err)
	}
	for _, canary := range []string{traceStateCanary, linkCanary, eventCanary, statusCanary} {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("OTLP protobuf leaked canary %q", canary)
		}
	}
	if len(request.ResourceSpans) != 1 {
		t.Fatalf("resource spans = %d", len(request.ResourceSpans))
	}
	resource := request.ResourceSpans[0].Resource.Attributes
	resourceValues := map[string]string{}
	for _, item := range resource {
		resourceValues[item.Key] = item.Value.GetStringValue()
	}
	if resourceValues["service.name"] != "lwc-qa-synthetic" || resourceValues["service.revision"] != "source-test" || resourceValues["gcp.project_id"] != "synthetic-project" {
		t.Fatalf("resource identity = %#v", resourceValues)
	}
	spans := request.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d", len(spans))
	}
	got := spans[0]
	if got.Name != "query.request" || !bytes.Equal(got.ParentSpanId, parentID[:]) || !bytes.Equal(got.TraceId, traceID[:]) {
		t.Fatalf("span identity or parent changed: %+v", got)
	}
	if got.TraceState != "" || len(got.Events) != 0 || got.Status.Message != "error" {
		t.Fatalf("span propagation/event/status were not sanitized: %+v", got)
	}
	if len(got.Links) != 1 || !bytes.Equal(got.Links[0].SpanId, linkID[:]) || len(got.Links[0].Attributes) != 0 || got.Links[0].TraceState != "" {
		t.Fatalf("causal link identity or privacy changed: %+v", got.Links)
	}
}

func TestAsyncExporterErrorIsOpaqueAtOtelHandle(t *testing.T) {
	const canary = "PRIVATE_EXPORTER_RESPONSE_CANARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, canary)
	}))
	defer server.Close()
	exporter, err := newOTLPHTTPExporter(context.Background(), server.URL+"/v1/traces", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal("create OTLP exporter")
	}
	provider, err := newTraceProviderWithOTLP(io.Discard, "lwc-qa-synthetic", "source-test", "synthetic-project", exporter)
	if err != nil {
		t.Fatal(err)
	}
	defer ShutdownTraces(provider)
	previous := otel.GetErrorHandler()
	defer otel.SetErrorHandler(previous)
	seen := make(chan string, 2)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { seen <- err.Error() }))
	_, span := provider.Tracer(queryInstrumentationScope).Start(context.Background(), "query.request")
	span.End()
	select {
	case got := <-seen:
		if strings.Contains(got, canary) {
			t.Fatalf("raw exporter response reached otel.Handle: %q", got)
		}
		if got != errTraceExportFailed.Error() {
			t.Fatalf("exporter error category = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("batch exporter error was not handled")
	}
}

func TestCloudTraceExporterPinsHTTPSAndTracePathAgainstAmbientOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{name: "traces endpoint", key: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", value: "http://unused.invalid/private-path"},
		{name: "insecure", key: "OTEL_EXPORTER_OTLP_INSECURE", value: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(OTLPEndpointEnv, cloudTraceOTLPEndpoint)
			t.Setenv(tc.key, tc.value)
			var gotURL, gotAuthorization string
			client := newOAuthHTTPClient(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "synthetic-review-token"}))
			client.Transport.(*oauth2.Transport).Base = ambientProbeTransport(func(request *http.Request) (*http.Response, error) {
				gotURL = request.URL.String()
				gotAuthorization = request.Header.Get("Authorization")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			exporter, err := newCloudTraceExporter(context.Background(), cloudTraceOTLPEndpoint, nil, client)
			if err != nil {
				t.Fatal("create Cloud Trace exporter")
			}
			defer exporter.Shutdown(context.Background())
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			_, span := provider.Tracer(queryInstrumentationScope).Start(context.Background(), "query.request")
			span.End()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := exporter.ExportSpans(ctx, recorder.Ended()); err != nil {
				t.Fatal("export synthetic span")
			}
			if gotURL != cloudTraceOTLPTraceURL {
				t.Errorf("ambient %s changed approved destination: %s", tc.key, gotURL)
			}
			if gotAuthorization != "Bearer synthetic-review-token" {
				t.Error("OAuth authorization was not attached to the pinned request")
			}
		})
	}
}

type ambientProbeTransport func(*http.Request) (*http.Response, error)

func (f ambientProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type testTokenSource struct {
	calls atomic.Int32
	block <-chan struct{}
}

func (s *testTokenSource) Token() (*oauth2.Token, error) {
	s.calls.Add(1)
	if s.block != nil {
		<-s.block
	}
	return &oauth2.Token{AccessToken: "synthetic-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
}

func TestOAuthHTTPClientOutlivesInitContextAndBoundedTokenRefresh(t *testing.T) {
	var authorized atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer synthetic-token" {
			authorized.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	initCtx, cancelInit := context.WithCancel(context.Background())
	source := &testTokenSource{}
	client := newOAuthHTTPClient(source)
	cancelInit()
	_ = initCtx // The long-lived OAuth client deliberately uses a process context.
	for range 2 {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request after init context cancellation: %v", err)
		}
		_ = response.Body.Close()
	}
	if source.calls.Load() == 0 || authorized.Load() != 2 {
		t.Fatalf("token refresh calls=%d authorized requests=%d", source.calls.Load(), authorized.Load())
	}

	block := make(chan struct{})
	blocked := newBoundedTokenSource(&testTokenSource{block: block}, 20*time.Millisecond)
	start := time.Now()
	if _, err := blocked.Token(); err == nil || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("blocked refresh was not bounded: err=%v elapsed=%s", err, time.Since(start))
	}
	close(block)
}
