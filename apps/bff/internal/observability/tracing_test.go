package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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
	ctx, finishRead := StartStorageRead(ctx, "other")
	reader, readErr := client.Bucket("memory-only-bucket").Object(objectCanary).NewReader(ctx)
	if reader != nil {
		_ = reader.Close()
	}
	if readErr == nil {
		t.Fatal("memory storage transport unexpectedly succeeded")
	}
	finishRead(readErr, "failure", "other")
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
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name != queryInstrumentationScope {
			continue
		}
		if strings.Contains(span.Status().Description, bodyCanary) || strings.Contains(span.Status().Description, objectCanary) {
			t.Fatalf("query span status leaked an SDK canary: %+v", span)
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), bodyCanary) || strings.Contains(attr.Value.Emit(), objectCanary) {
				t.Fatalf("query span attribute leaked an SDK canary: %+v", attr)
			}
		}
		for _, event := range span.Events() {
			if strings.Contains(event.Name, bodyCanary) || strings.Contains(event.Name, objectCanary) {
				t.Fatalf("query span event leaked an SDK canary: %+v", event)
			}
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.Emit(), bodyCanary) || strings.Contains(attr.Value.Emit(), objectCanary) {
					t.Fatalf("query span event attribute leaked an SDK canary: %+v", attr)
				}
			}
		}
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

func TestStorageReadBudgetKeepsCanonicalHostCallsOnLivePhase(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	rootCtx, root := otel.Tracer(queryInstrumentationScope).Start(context.Background(), "query.request")
	phaseCtx, phase := otel.Tracer(queryInstrumentationScope).Start(rootCtx, "query.stage.cache_load")
	phaseCtx = WithStorageReadBudget(phaseCtx)
	const reads = storageReadDetailLimit + 4
	var wg sync.WaitGroup
	for index := 0; index < reads; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			readCtx, finishRead := StartStorageRead(phaseCtx, "concept_page")
			_, host := otel.Tracer(queryInstrumentationScope).Start(readCtx, "query.host_call")
			time.Sleep(time.Duration(reads-index) * time.Millisecond)
			host.End()
			finishRead(nil, "success", "none")
		}()
	}
	wg.Wait()
	phase.End()
	root.End()

	spans := exporter.GetSpans()
	var phaseSpan tracetest.SpanStub
	var readSpans, hostSpans []tracetest.SpanStub
	for _, span := range spans {
		switch span.Name {
		case "query.stage.cache_load":
			phaseSpan = span
		case "query.storage.read":
			readSpans = append(readSpans, span)
		case "query.host_call":
			hostSpans = append(hostSpans, span)
		}
	}
	if len(readSpans) != storageReadDetailLimit || len(hostSpans) != reads {
		t.Fatalf("detail spans=%d host spans=%d, want %d and %d", len(readSpans), len(hostSpans), storageReadDetailLimit, reads)
	}
	var suppressed int
	for _, attr := range phaseSpan.Attributes {
		if attr.Key == attribute.Key("query.storage.read.suppressed") {
			suppressed = int(attr.Value.AsInt64())
		}
	}
	if suppressed != reads-storageReadDetailLimit {
		t.Fatalf("suppressed=%d, want %d: %+v", suppressed, reads-storageReadDetailLimit, phaseSpan.Attributes)
	}
	phaseID := phaseSpan.SpanContext.SpanID()
	readIDs := make(map[trace.SpanID]bool, len(readSpans))
	for _, span := range readSpans {
		readIDs[span.SpanContext.SpanID()] = true
	}
	phaseParents, detailParents := 0, 0
	for _, span := range hostSpans {
		parentID := span.Parent.SpanID()
		switch {
		case parentID == phaseID:
			phaseParents++
		case readIDs[parentID]:
			detailParents++
		default:
			t.Fatalf("host span parent %s is neither live phase nor its storage detail: %+v", parentID, span)
		}
	}
	if phaseParents != suppressed || detailParents != storageReadDetailLimit {
		t.Fatalf("host parents phase=%d detail=%d, want %d and %d", phaseParents, detailParents, suppressed, storageReadDetailLimit)
	}
}
