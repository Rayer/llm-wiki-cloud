package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

func TestRuntimeTraceSamplingPolicyRecordsUnsampledParentsOnlyWhenExplicitlyEnabled(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	parentID, err := trace.SpanIDFromHex("2222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	remoteContext := func(sampled bool) context.Context {
		flags := trace.TraceFlags(0)
		if sampled {
			flags = trace.FlagsSampled
		}
		parent := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID, SpanID: parentID, TraceFlags: flags, Remote: true,
		})
		return trace.ContextWithRemoteSpanContext(context.Background(), parent)
	}

	t.Setenv("LWC_DEV_FULL_TRACE_SAMPLING", "1")
	devRecorder := tracetest.NewSpanRecorder()
	devProvider, err := newRuntimeTraceProvider(io.Discard, "lwc-bff-test", "revision-test", "synthetic-project", devRecorder)
	if err != nil {
		t.Fatal(err)
	}
	devCtx, devSpan := devProvider.Tracer(queryInstrumentationScope).Start(remoteContext(false), "query.request")
	devSpan.End()
	devEnded := devRecorder.Ended()
	if len(devEnded) != 1 || !devSpan.SpanContext().IsSampled() || devSpan.SpanContext().TraceID() != traceID ||
		devEnded[0].Parent().SpanID() != parentID || trace.SpanContextFromContext(devCtx).TraceID() != traceID {
		t.Fatalf("explicit DEV sampling lost the unsampled incoming trace/parent: span=%+v ended=%+v", devSpan.SpanContext(), devEnded)
	}
	if err := devProvider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LWC_DEV_FULL_TRACE_SAMPLING", "")
	defaultRecorder := tracetest.NewSpanRecorder()
	defaultProvider, err := newRuntimeTraceProvider(io.Discard, "lwc-bff-test", "revision-test", "synthetic-project", defaultRecorder)
	if err != nil {
		t.Fatal(err)
	}
	_, unsampled := defaultProvider.Tracer(queryInstrumentationScope).Start(remoteContext(false), "query.request")
	unsampled.End()
	if got := len(defaultRecorder.Ended()); got != 0 {
		t.Fatalf("default provider recorded an unsampled remote parent, got %d spans", got)
	}
	_, sampled := defaultProvider.Tracer(queryInstrumentationScope).Start(remoteContext(true), "query.request")
	sampled.End()
	if got := defaultRecorder.Ended(); len(got) != 1 || !got[0].SpanContext().IsSampled() || got[0].Parent().SpanID() != parentID {
		t.Fatalf("default provider changed sampled-parent behavior: %+v", got)
	}
	if err := defaultProvider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineScopesExportSafeSpansAndBlockUnrelatedSDKPayloads(t *testing.T) {
	const canary = "PRIVATE_PIPELINE_SDK_CANARY"
	var output strings.Builder
	provider, err := newTraceProvider(&output, "lwc-pipeline-test", "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		ShutdownTraces(provider)
	})

	ctx, submit := StartPipelineSpan(context.Background(), "pipeline.submit")
	_, invoke := StartPipelineSpan(ctx, "pipeline.job.invoke")
	FinishPipelineSpan(invoke, "accepted")
	FinishPipelineSpan(submit, "accepted")
	_, worker := StartWorkerSpan(context.Background(), "pipeline.worker")
	FinishPipelineSpan(worker, "success")
	_, query := provider.Tracer(queryInstrumentationScope).Start(context.Background(), "query.request")
	query.End()
	_, authSpan := StartAuthSpan(context.Background(), "auth.request")
	FinishAuthSpan(authSpan, "rejected")
	_, profileSpan := StartProfileSpan(context.Background(), "profile.dispatch")
	FinishProfileSpan(profileSpan, "success")
	_, exportSpan := StartExportSpan(context.Background(), "export.worker")
	FinishExportSpan(exportSpan, "success")
	_, unapprovedAuth := provider.Tracer(authInstrumentationScope).Start(context.Background(), canary,
		trace.WithAttributes(attribute.String("payload", canary)))
	unapprovedAuth.End()

	_, sdk := provider.Tracer("google.golang.org/storage").Start(context.Background(), "storage.request",
		trace.WithAttributes(attribute.String("object", canary)),
	)
	sdk.AddEvent("raw response", trace.WithAttributes(attribute.String("body", canary)))
	sdk.SetStatus(codes.Error, canary)
	sdk.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"pipeline.submit", "pipeline.job.invoke", "pipeline.worker", "query.request", "auth.request", "profile.dispatch", "export.worker"} {
		if !strings.Contains(output.String(), `"Name":"`+want+`"`) {
			t.Fatalf("stdout exporter omitted allowlisted span %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), canary) || strings.Contains(output.String(), "storage.request") {
		t.Fatalf("unrelated SDK span payload reached stdout: %s", output.String())
	}
	sawSDKCanary := false
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name == "google.golang.org/storage" &&
			(strings.Contains(span.Status().Description, canary) || len(span.Attributes()) > 0 || len(span.Events()) > 0) {
			sawSDKCanary = true
		}
	}
	if !sawSDKCanary {
		t.Fatalf("fixture did not capture the unrelated SDK canary: %+v", recorder.Ended())
	}
}

func TestDataRouteInventoryIsFiniteAndExcludesOtherBFFFamilies(t *testing.T) {
	want := map[string]string{
		"GET /api/v1/projects":               "project.list",
		"GET /api/v1/sources":                "source.list",
		"GET /api/v1/sources/:id":            "source.read",
		"GET /api/v1/concepts":               "concept.list",
		"GET /api/v1/concepts/:id":           "concept.read",
		"GET /api/v1/sources/:id/annotation": "annotation.read",
		"PUT /api/v1/sources/:id/annotation": "annotation.write",
		"GET /api/v1/raw":                    "raw.list",
		"GET /api/v1/raw/*path":              "raw.preview",
		"POST /api/v1/raw/upload":            "raw.upload",
		"GET /api/v1/sync/raw":               "raw_sync.list",
		"GET /api/v1/sync/raw/file":          "raw_sync.read",
		"PUT /api/v1/sync/raw/file":          "raw_sync.write",
		"POST /api/v1/import":                "import.validate",
	}
	if len(dataOperations) != len(want) {
		t.Fatalf("mapped operation count = %d, want %d", len(dataOperations), len(want))
	}
	for route, operation := range want {
		if got := DataOperationForRoute(strings.SplitN(route, " ", 2)[0], strings.SplitN(route, " ", 2)[1]); got != operation {
			t.Errorf("DataOperationForRoute(%q) = %q, want %q", route, got, operation)
		}
	}
	for _, route := range []string{
		"GET /api/v1/health", "GET /api/v1/ready", "GET /api/v1/index", "GET /api/v1/status",
		"GET /api/v1/metrics", "POST /api/v1/query", "POST /api/v1/pipeline/run",
		"GET /api/v1/projects/:pid/profile", "GET /api/v1/projects/:pid/exports",
		"GET /api/v1/admin/projects", "POST /api/v1/raw/scrape",
	} {
		parts := strings.SplitN(route, " ", 2)
		if got := DataOperationForRoute(parts[0], parts[1]); got != "" {
			t.Errorf("out-of-scope route %q mapped to %q", route, got)
		}
	}
}

func TestDataOperationCapturesGuardRejectWithFiniteAttributes(t *testing.T) {
	const privatePath = "PRIVATE_DATA_SOURCE_ID_CANARY"
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := gin.New()
	router.Use(DataOperationMiddleware())
	router.GET("/api/v1/sources/:id", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusForbidden)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+privatePath, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("guard status = %d, want %d", response.Code, http.StatusForbidden)
	}
	var root sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name == dataInstrumentationScope && span.Name() == "bff.data.operation" {
			root = span
		}
	}
	if root == nil {
		t.Fatalf("mapped pre-handler guard reject was not recorded: %+v", recorder.Ended())
	}
	attrs := map[string]string{}
	for _, attr := range root.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	if attrs["bff.operation"] != "source.read" || attrs["bff.outcome"] != "rejected" {
		t.Fatalf("root attributes = %#v", attrs)
	}
	if strings.Contains(root.Name(), privatePath) || strings.Contains(strings.Join(attrsToStrings(root.Attributes()), " "), privatePath) {
		t.Fatalf("raw route path reached data span: %+v", root)
	}
}

func TestDataScopeSDKExportDropsPrivateAttributesEventsAndStatusText(t *testing.T) {
	const canary = "PRIVATE_DATA_SDK_CANARY"
	var output strings.Builder
	provider, err := newTraceProvider(&output, "lwc-data-test", "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := provider.Tracer(dataInstrumentationScope).Start(context.Background(), "bff.data.operation",
		trace.WithAttributes(
			attribute.String("bff.operation", "source.list"),
			attribute.String("bff.outcome", "success"),
			attribute.Int64("bff.count.items", 3),
			attribute.String("raw.path", canary),
		),
	)
	_ = ctx
	span.AddEvent(canary, trace.WithAttributes(attribute.String("body", canary)))
	span.SetStatus(codes.Error, canary)
	span.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	serialized := output.String()
	if strings.Contains(serialized, canary) || strings.Contains(serialized, "raw.path") {
		t.Fatalf("unsafe data-scope payload reached exporter: %s", serialized)
	}
	for _, safe := range []string{`"Name":"bff.data.operation"`, `"bff.operation"`, `"source.list"`, `"bff.outcome"`, `"bff.count.items"`} {
		if !strings.Contains(serialized, safe) {
			t.Fatalf("allowlisted data detail %q missing from exporter: %s", safe, serialized)
		}
	}
	if strings.Contains(serialized, `"Description":"`+canary) {
		t.Fatalf("raw status description reached exporter: %s", serialized)
	}
}

func attrsToStrings(attrs []attribute.KeyValue) []string {
	values := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		values = append(values, attr.Value.AsString())
	}
	return values
}

func TestAuthMiddlewareCapturesPreGuardRejectWithoutPathData(t *testing.T) {
	const projectID = "PRIVATE_PROJECT_ID_CANARY"
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := gin.New()
	router.Use(AuthHTTPMiddleware())
	router.GET("/mcp/projects/:projectID", func(c *gin.Context) { c.AbortWithStatus(http.StatusForbidden) })
	request := httptest.NewRequest(http.MethodGet, "/mcp/projects/"+projectID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("guard status = %d, want %d", response.Code, http.StatusForbidden)
	}
	var root sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name == authInstrumentationScope && span.Name() == "auth.mcp.resource" {
			root = span
		}
	}
	if root == nil {
		t.Fatal("early resource guard rejection was not captured")
	}
	attrs := map[string]string{}
	for _, attr := range root.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	if attrs["auth.operation"] != "mcp_resource" || attrs["auth.guard.stage"] != "authentication" || attrs["auth.guard.outcome"] != "rejected" {
		t.Fatalf("auth guard attributes = %#v", attrs)
	}
	if strings.Contains(root.Name(), projectID) {
		t.Fatal("raw project identifier reached the Auth span name")
	}
	for _, value := range attrs {
		if strings.Contains(value, projectID) {
			t.Fatal("raw project identifier reached the Auth span attributes")
		}
	}
}

func TestProfileAttemptStartsIndependentTraceAndPreservesContextValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	base := context.WithValue(context.Background(), struct{}{}, "lease-value")
	requestCtx, requestSpan := StartProfileSpan(base, "profile.dispatch")
	attemptCtx, attemptSpan := StartProfileWorkSpan(requestCtx)
	if got := attemptCtx.Value(struct{}{}); got != "lease-value" {
		t.Fatalf("attempt context lost dispatcher value: %v", got)
	}
	FinishProfileSpan(attemptSpan, "success")
	FinishProfileSpan(requestSpan, "success")
	var request, attempt sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "profile.dispatch":
			request = span
		case "profile.work":
			attempt = span
		}
	}
	if request == nil || attempt == nil || request.SpanContext().TraceID() == attempt.SpanContext().TraceID() || attempt.Parent().IsValid() ||
		len(attempt.Links()) != 1 || attempt.Links()[0].SpanContext.TraceID() != request.SpanContext().TraceID() ||
		attempt.Links()[0].SpanContext.SpanID() != request.SpanContext().SpanID() {
		t.Fatalf("attempt trace inherited dispatch parent: request=%v attempt=%v", request, attempt)
	}
}

func TestPipelineDetailBudgetSuppressesOnlyObservationDetails(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	ctx, root := StartPipelineSpan(context.Background(), "pipeline.quota.reconcile")
	ctx, budget := WithPipelineDetailBudget(ctx, 16)
	for index := 0; index < 21; index++ {
		name := "pipeline.quota.lookup"
		if index%3 == 1 {
			name = "pipeline.recovery.result"
		} else if index%3 == 2 {
			name = "pipeline.recovery.transaction"
		}
		_, detail := StartPipelineSpan(ctx, name)
		FinishPipelineSpan(detail, "success")
	}
	SetPipelineCount(root, "pipeline.processed_count", 21)
	SetPipelineCount(root, "pipeline.suppressed_count", budget.Suppressed())
	FinishPipelineSpan(root, "success")
	spans := exporter.GetSpans()
	if len(spans) != 17 || budget.Suppressed() != 5 {
		t.Fatalf("exported spans=%d suppressed=%d, want root + 16 details and 5 suppressed", len(spans), budget.Suppressed())
	}
	for _, span := range spans {
		if span.Name != "pipeline.quota.reconcile" {
			continue
		}
		attrs := map[string]int{}
		for _, attr := range span.Attributes {
			if attr.Value.Type() == attribute.INT64 {
				attrs[string(attr.Key)] = int(attr.Value.AsInt64())
			}
		}
		if attrs["pipeline.processed_count"] != 21 || attrs["pipeline.suppressed_count"] != 5 {
			t.Fatalf("reconcile count attributes = %#v", attrs)
		}
	}
}

func TestPipelineSettlementOutcomeUsesFiniteTransactionTruth(t *testing.T) {
	tests := []struct {
		settlement string
		applied    bool
		err        error
		want       string
	}{
		{settlement: "charged", applied: true, want: "committed"},
		{settlement: "refunded", applied: true, want: "committed"},
		{settlement: "charged", want: "retained"},
		{settlement: "refunded", want: "retained"},
		{settlement: "pending", want: "pending"},
		{settlement: "not_applicable", want: "not_applicable"},
		{settlement: "no_op", want: "no_op"},
		{settlement: "unrecognized-private-value", want: "unknown"},
		{settlement: "charged", applied: true, err: errors.New("private transaction detail"), want: "failure"},
	}
	for _, tt := range tests {
		if got := PipelineSettlementOutcome(tt.settlement, tt.applied, tt.err); got != tt.want {
			t.Errorf("PipelineSettlementOutcome(%q, %t, %v) = %q, want %q", tt.settlement, tt.applied, tt.err, got, tt.want)
		}
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
