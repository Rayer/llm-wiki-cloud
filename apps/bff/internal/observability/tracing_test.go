package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	cloudstorage "cloud.google.com/go/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/api/option"
)

func TestStdoutTraceExporterWritesCorrelatedStructuredSpans(t *testing.T) {
	var output strings.Builder
	provider, err := newTraceProvider(&output, "lwc-query-test", "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	defer ShutdownTraces(provider)

	tracer := provider.Tracer(queryInstrumentationScope)
	ctx, root := tracer.Start(context.Background(), "query.request")
	_, child := tracer.Start(ctx, "query.stage.profile")
	child.End()
	root.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var spans []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	for scanner.Scan() {
		var span map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &span); err != nil {
			t.Fatalf("stdout span is not JSON: %v", err)
		}
		spans = append(spans, span)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("exported %d spans, want root and child: %s", len(spans), output.String())
	}
	if !strings.Contains(output.String(), `"service.name"`) || !strings.Contains(output.String(), "lwc-query-test") || !strings.Contains(output.String(), "revision-test") {
		t.Fatalf("stdout spans omitted service resource attributes: %s", output.String())
	}

	byName := make(map[string]map[string]any, len(spans))
	for _, span := range spans {
		name, _ := span["Name"].(string)
		byName[name] = span
	}
	rootSpan, rootOK := byName["query.request"]
	childSpan, childOK := byName["query.stage.profile"]
	if !rootOK || !childOK {
		t.Fatalf("exported spans = %v, want query.request and its stage: %s", byName, output.String())
	}
	rootContext, _ := rootSpan["SpanContext"].(map[string]any)
	childContext, _ := childSpan["SpanContext"].(map[string]any)
	childParent, _ := childSpan["Parent"].(map[string]any)
	if rootContext["TraceID"] == "" || rootContext["TraceID"] != childContext["TraceID"] || rootContext["SpanID"] != childParent["SpanID"] {
		t.Fatalf("exported span relationship lost: root=%v child=%v", rootSpan, childSpan)
	}
}

type memoryCanaryRoundTripper struct{ body string }

func (rt memoryCanaryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Request:    r,
	}, nil
}

func TestActualStorageSDKFailureCannotReachStdoutExporter(t *testing.T) {
	const (
		objectCanary = "PRIVATE_STORAGE_OBJECT_CANARY"
		bodyCanary   = "PRIVATE_STORAGE_BODY_CANARY"
	)
	body := "<?xml version=\"1.0\"?><Error><Code>AccessDenied</Code><Message>" + bodyCanary + "</Message></Error>"
	var output strings.Builder
	provider, err := newTraceProvider(&output, "lwc-query-test", "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	defer ShutdownTraces(provider)
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	otel.SetTracerProvider(provider)

	client, err := cloudstorage.NewClient(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(&http.Client{Transport: memoryCanaryRoundTripper{body: body}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, root := otel.Tracer(queryInstrumentationScope).Start(context.Background(), "query.request")
	ctx, child := otel.Tracer(queryInstrumentationScope).Start(ctx, "query.storage.read")
	reader, readErr := client.Bucket("memory-only-bucket").Object(objectCanary).NewReader(ctx)
	if reader != nil {
		_ = reader.Close()
	}
	if readErr == nil {
		t.Fatal("memory storage transport unexpectedly succeeded")
	}
	child.End()
	root.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var sawSDKSpan, sawBodyCanary bool
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name == queryInstrumentationScope {
			continue
		}
		sawSDKSpan = true
		if strings.Contains(span.Status().Description, bodyCanary) {
			sawBodyCanary = true
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), bodyCanary) || strings.Contains(attr.Value.Emit(), objectCanary) {
				sawBodyCanary = sawBodyCanary || strings.Contains(attr.Value.Emit(), bodyCanary)
			}
		}
		for _, event := range span.Events() {
			if strings.Contains(event.Name, bodyCanary) {
				sawBodyCanary = true
			}
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.Emit(), bodyCanary) {
					sawBodyCanary = true
				}
			}
		}
	}
	if !sawSDKSpan || !sawBodyCanary {
		t.Fatalf("actual storage SDK span did not carry the canary error: sdkSpan=%v bodyCanary=%v err=%v spans=%+v", sawSDKSpan, sawBodyCanary, readErr, recorder.Ended())
	}
	if strings.Contains(output.String(), bodyCanary) || strings.Contains(output.String(), objectCanary) {
		t.Fatalf("raw SDK attributes/events/status reached stdout: %s", output.String())
	}

	var spans []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	for scanner.Scan() {
		var span map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &span); err != nil {
			t.Fatalf("stdout span is not JSON: %v", err)
		}
		spans = append(spans, span)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("exported %d spans, want Query root and child: %s", len(spans), output.String())
	}
	byName := make(map[string]map[string]any, len(spans))
	for _, span := range spans {
		name, _ := span["Name"].(string)
		byName[name] = span
	}
	queryRootSpan, rootOK := byName["query.request"]
	queryChildSpan, childOK := byName["query.storage.read"]
	if !rootOK || !childOK {
		t.Fatalf("stdout lost Query root/child correlation spans: %v", byName)
	}
	rootContext, _ := queryRootSpan["SpanContext"].(map[string]any)
	childContext, _ := queryChildSpan["SpanContext"].(map[string]any)
	childParent, _ := queryChildSpan["Parent"].(map[string]any)
	if rootContext["TraceID"] == "" || rootContext["TraceID"] != childContext["TraceID"] || rootContext["SpanID"] != childParent["SpanID"] {
		t.Fatalf("Query root/child relationship lost: root=%v child=%v", queryRootSpan, queryChildSpan)
	}
}
