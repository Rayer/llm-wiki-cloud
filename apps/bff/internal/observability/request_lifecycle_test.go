package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRequestLifecycleUsesFreshServerIDAndFiniteIngressObservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exporter := tracetest.NewInMemoryExporter()
	provider, err := NewFilteredTraceProvider(io.Discard, "lwc-bff-test", "revision-test", "synthetic-project", nil,
		sdktrace.NewSimpleSpanProcessor(exporter))
	if err != nil {
		t.Fatal(err)
	}
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		ShutdownTraces(provider)
	})
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	router := gin.New()
	router.Use(RequestLifecycleMiddleware())
	router.GET("/api/v1/projects/:projectID", func(c *gin.Context) {
		requestID := RequestIDFromContext(c.Request.Context())
		if requestID == "" || requestID != c.Writer.Header().Get(requestIDHeader) {
			t.Errorf("handler context/header request IDs differ: context=%q header=%q", requestID, c.Writer.Header().Get(requestIDHeader))
		}
		c.Status(http.StatusTeapot)
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-secret", nil)
	request.Header.Set(requestIDHeader, "caller-controlled-id")
	request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	request.Header.Set("tracestate", "vendor=PRIVATE_TRACESTATE_CANARY")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTeapot {
		t.Fatalf("ingress changed business status: got %d", response.Code)
	}
	requestID := response.Header().Get(requestIDHeader)
	if uuid.Validate(requestID) != nil || requestID == "caller-controlled-id" {
		t.Fatalf("response did not contain a fresh server-owned UUID: %q", requestID)
	}
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "http.request" || spans[0].SpanKind != trace.SpanKindServer || spans[0].SpanContext.TraceID().String() != "11111111111111111111111111111111" || spans[0].Parent.SpanID().String() != "2222222222222222" {
		t.Fatalf("ingress span did not retain the real W3C parent: %+v", spans)
	}
	if spans[0].Parent.TraceState().String() != "" {
		t.Fatalf("filtered ingress span retained private tracestate: %+v", spans[0].Parent)
	}
	attrs := make(map[string]any, len(spans[0].Attributes))
	for _, attr := range spans[0].Attributes {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	if attrs["request.id"] != requestID || attrs["http.route"] != "/api/v1/projects/:projectID" || attrs["http.response.status_code"] != int64(http.StatusTeapot) || attrs["request.phase"] != "terminal" || attrs["request.outcome"] != "rejected" {
		t.Fatalf("ingress span lost its bounded terminal fields: %v", attrs)
	}
	if strings.Contains(logs.String(), "PRIVATE_TRACESTATE_CANARY") || strings.Contains(logs.String(), "project-secret") || strings.Contains(logs.String(), "caller-controlled-id") {
		t.Fatalf("private carrier, raw path, or caller ID leaked to terminal log: %s", logs.String())
	}
	var summary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &summary); err != nil {
		t.Fatalf("terminal record is not JSON: %v; %s", err, logs.String())
	}
	if summary["msg"] != "http.request.terminal" || summary["request_id"] != requestID || summary["trace_id"] != spans[0].SpanContext.TraceID().String() || summary["span_id"] != spans[0].SpanContext.SpanID().String() || summary["route"] != "/api/v1/projects/:projectID" || summary["status_code"] != float64(http.StatusTeapot) || summary["phase"] != "terminal" || summary["outcome"] != "rejected" {
		t.Fatalf("terminal record does not bridge the response ID to its actual span: %+v", summary)
	}

	logs.Reset()
	exporter.Reset()
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/private/fallback/path", nil))
	if response.Code != http.StatusNotFound || uuid.Validate(response.Header().Get(requestIDHeader)) != nil {
		t.Fatalf("unmatched route did not receive its response ID: status=%d headers=%v", response.Code, response.Header())
	}
	if strings.Contains(logs.String(), "fallback/path") || !strings.Contains(logs.String(), `"route":"control.unmatched"`) {
		t.Fatalf("fallback record was not finite and path-safe: %s", logs.String())
	}
}

func TestRequestLifecycleFinalizesBeforeGinRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousGinErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = io.Discard
	t.Cleanup(func() { gin.DefaultErrorWriter = previousGinErrorWriter })
	exporter := tracetest.NewInMemoryExporter()
	provider, err := NewFilteredTraceProvider(io.Discard, "lwc-bff-test", "revision-test", "synthetic-project", nil,
		sdktrace.NewSimpleSpanProcessor(exporter))
	if err != nil {
		t.Fatal(err)
	}
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		ShutdownTraces(provider)
	})
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(RequestLifecycleMiddleware())
	router.GET("/api/v1/panic", func(*gin.Context) { panic("PRIVATE_PANIC_CANARY") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil))
	if response.Code != http.StatusInternalServerError || uuid.Validate(response.Header().Get(requestIDHeader)) != nil {
		t.Fatalf("Gin recovery response changed or lost the request ID: status=%d headers=%v", response.Code, response.Header())
	}
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "http.request" {
		t.Fatalf("panic recovery lost or duplicated the HTTP root span: %+v", spans)
	}
	attrs := make(map[string]any, len(spans[0].Attributes))
	for _, attr := range spans[0].Attributes {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	if attrs["http.response.status_code"] != int64(http.StatusInternalServerError) || attrs["request.phase"] != "terminal" || attrs["request.outcome"] != "failure" {
		t.Fatalf("panic recovery terminal does not report the actual failure: %v", attrs)
	}
	if strings.Contains(logs.String(), "PRIVATE_PANIC_CANARY") || strings.Count(logs.String(), `"msg":"http.request.terminal"`) != 1 || !strings.Contains(logs.String(), `"status_code":500`) || !strings.Contains(logs.String(), `"outcome":"failure"`) {
		t.Fatalf("panic terminal was missing, duplicated, or leaked panic content: %s", logs.String())
	}
}
