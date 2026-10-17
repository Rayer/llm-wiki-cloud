package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceContextPropagatorPrecedenceFallbackAndSampling(t *testing.T) {
	w3c := "00-11111111111111111111111111111111-2222222222222222-01"
	googleHeader := "33333333333333333333333333333333/3;o=1"
	p := traceContextPropagator{w3c: propagation.TraceContext{}}

	cases := []struct {
		name     string
		carrier  propagation.MapCarrier
		traceID  string
		parentID string
		sampled  bool
		valid    bool
	}{
		{name: "W3C wins conflict", carrier: propagation.MapCarrier{"traceparent": w3c, cloudTraceContextHeader: googleHeader}, traceID: "11111111111111111111111111111111", parentID: "2222222222222222", sampled: true, valid: true},
		{name: "Google fallback", carrier: propagation.MapCarrier{cloudTraceContextHeader: googleHeader}, traceID: "33333333333333333333333333333333", parentID: "0000000000000003", sampled: true, valid: true},
		{name: "malformed W3C uses Google", carrier: propagation.MapCarrier{"traceparent": "not-a-traceparent", cloudTraceContextHeader: googleHeader}, traceID: "33333333333333333333333333333333", parentID: "0000000000000003", sampled: true, valid: true},
		{name: "unsampled W3C", carrier: propagation.MapCarrier{"traceparent": "00-11111111111111111111111111111111-2222222222222222-00", cloudTraceContextHeader: googleHeader}, traceID: "11111111111111111111111111111111", parentID: "2222222222222222", sampled: false, valid: true},
		{name: "both invalid", carrier: propagation.MapCarrier{"traceparent": "bad", cloudTraceContextHeader: "bad"}, valid: false},
		{name: "missing", carrier: propagation.MapCarrier{}, valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := trace.SpanContextFromContext(p.Extract(context.Background(), tc.carrier))
			if sc.IsValid() != tc.valid {
				t.Fatalf("valid=%t, want %t", sc.IsValid(), tc.valid)
			}
			if !tc.valid {
				return
			}
			if sc.TraceID().String() != tc.traceID || sc.SpanID().String() != tc.parentID || sc.IsSampled() != tc.sampled || !sc.IsRemote() {
				t.Fatalf("extracted span context = %+v", sc)
			}
		})
	}
}

func TestTraceContextMiddlewareParentsAuthRootAndInjectsOnlyTraceparent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := sdktrace.NewTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	router := gin.New()
	router.Use(TraceContextMiddleware(), AuthHTTPMiddleware())
	router.GET("/api/v1/auth/google/identity", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/identity", nil)
	request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	request.Header.Set("tracestate", "vendor=PRIVATE_TRACESTATE_CANARY")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP status = %d", response.Code)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d", len(spans))
	}
	span := spans[0]
	if span.Name() != "auth.google.identity" || span.SpanContext().TraceID().String() != "11111111111111111111111111111111" ||
		span.Parent().SpanID().String() != "2222222222222222" || span.SpanContext().SpanID().String() == span.Parent().SpanID().String() {
		t.Fatalf("Auth span does not have incoming parent: %+v", span)
	}
	carrier := http.Header{
		"Tracestate": []string{"private-state"},
		"Baggage":    []string{"private-baggage"},
	}
	InjectTraceContext(trace.ContextWithSpanContext(context.Background(), span.SpanContext()), carrier)
	if carrier.Get("traceparent") == "" || carrier.Get("tracestate") != "" || carrier.Get("baggage") != "" {
		t.Fatalf("outbound carrier = %#v", carrier)
	}

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := ExtractTraceContext(request.Context(), propagation.HeaderCarrier(request.Header))
		_, child := otel.Tracer("test.http-peer").Start(ctx, "service-b.request")
		child.End()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer peer.Close()
	request, err := http.NewRequest(http.MethodGet, peer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	InjectTraceContext(trace.ContextWithSpanContext(context.Background(), span.SpanContext()), request.Header)
	peerResponse, err := peer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	peerResponse.Body.Close()
	if peerResponse.StatusCode != http.StatusNoContent || request.Header.Get("traceparent") == "" ||
		request.Header.Get("tracestate") != "" || request.Header.Get("baggage") != "" {
		t.Fatalf("outbound HTTP propagation status=%d headers=%v", peerResponse.StatusCode, request.Header)
	}
	ended := recorder.Ended()
	if len(ended) != 2 || ended[1].Name() != "service-b.request" ||
		ended[1].Parent().SpanID() != span.SpanContext().SpanID() ||
		ended[1].SpanContext().TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("TCP peer span did not receive the actual parent: %+v", ended)
	}
}

func TestWorkerAttemptCreatesIndependentRootWithRealLinkAndConcurrentIsolation(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	parent := "00-44444444444444444444444444444444-5555555555555555-01"
	ctx := ExtractWorkerTraceContext(context.Background(), parent)
	_, span := StartWorkerAttemptSpan(ctx, "pipeline.worker")
	span.End()
	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Parent().IsValid() || len(ended[0].Links()) != 1 ||
		ended[0].Links()[0].SpanContext.TraceID().String() != "44444444444444444444444444444444" ||
		ended[0].Links()[0].SpanContext.SpanID().String() != "5555555555555555" {
		t.Fatalf("worker attempt did not remain a root with the actual causal link: %+v", ended)
	}

	const count = 32
	var wg sync.WaitGroup
	for i := 1; i <= count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			traceID := fmt.Sprintf("%032x", index)
			spanID := fmt.Sprintf("%016x", index)
			carrier := propagation.MapCarrier{"traceparent": "00-" + traceID + "-" + spanID + "-01"}
			got := trace.SpanContextFromContext(ExtractTraceContext(context.Background(), carrier))
			if got.TraceID().String() != traceID || got.SpanID().String() != spanID {
				t.Errorf("request %d extracted context %s/%s", index, got.TraceID(), got.SpanID())
			}
		}(i)
	}
	wg.Wait()
}
