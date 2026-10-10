package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type receiptTraceExecutor struct{}

func (receiptTraceExecutor) Execute(ctx context.Context, _ cache.Reader, request query.Request) (query.Result, error) {
	recorder := query.ReceiptRecorderFromContext(ctx)
	if recorder == nil {
		return query.Result{}, errors.New("receipt recorder missing")
	}
	stageCtx := recorder.StartStage(ctx, "synthetic", "synthetic-provider", "safe-model", "low")
	finishCall := recorder.StartHostCall("synthetic", "https", "provider.example.test")
	finishCall("success")
	query.FinishStage(stageCtx, "success")
	return query.Result{Query: request.Query, Mode: request.Mode}, nil
}

type failingTestSpanExporter struct{}

func (failingTestSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("controlled local span sink failure")
}

func (failingTestSpanExporter) Shutdown(context.Context) error   { return nil }
func (failingTestSpanExporter) ForceFlush(context.Context) error { return nil }

type generationSwitchRoot struct {
	storage.Store
	currentGeneration string
	pins              int
	manifestReads     int
	identityReads     int
	identityErr       error
}

func (r *generationSwitchRoot) Scope(string, string) storage.Store {
	return &generationSwitchScope{root: r}
}

type generationSwitchScope struct {
	storage.Store
	root *generationSwitchRoot
}

func (s *generationSwitchScope) Pin(context.Context) (storage.Store, error) {
	s.root.pins++
	s.root.manifestReads++
	pinned := s.root.currentGeneration
	s.root.currentGeneration = "generation-new"
	return &generationSwitchPinnedStore{root: s.root, generationID: pinned}, nil
}

type generationSwitchPinnedStore struct {
	storage.Store
	root         *generationSwitchRoot
	generationID string
}

func (s *generationSwitchPinnedStore) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	s.root.identityReads++
	if s.root.identityErr != nil {
		return storage.QueryGenerationIdentity{}, s.root.identityErr
	}
	return storage.QueryGenerationIdentity{ProjectID: "diagnostic-test-project", GenerationID: s.generationID}, nil
}

func (s *generationSwitchPinnedStore) PinnedGenerationID() (string, bool) {
	return s.generationID, s.generationID != ""
}

type identityCheckingFailureExecutor struct{}

func (identityCheckingFailureExecutor) Execute(ctx context.Context, reader cache.Reader, _ query.Request) (query.Result, error) {
	identityProvider, ok := reader.(storage.QueryGenerationIdentityProvider)
	if !ok {
		return query.Result{}, storage.ErrQueryGenerationIdentityUnavailable
	}
	if _, err := identityProvider.QueryGenerationIdentity(ctx); err != nil {
		return query.Result{}, err
	}
	return query.Result{}, errors.New("injected failure after query identity validation")
}

func TestQueryDiagnosticsFinalizeSafelyAndExportActualSpanTree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(failingTestSpanExporter{})),
	)
	otel.SetTracerProvider(provider)
	var otelErrors bytes.Buffer
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		_, _ = fmt.Fprintln(&otelErrors, err.Error())
	}))

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(oldLogger)

	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetQueryExecutor(receiptTraceExecutor{})
	const queryCanary = "PRIVATE_QUERY_CANARY"
	malformed := performQueryDiagnosticsRequest(h, `{"q":"`+queryCanary+`",broken}`)
	problem := assertQueryProblem(t, malformed, http.StatusBadRequest, "invalid_query")
	if err := uuid.Validate(problem.DiagnosticID); err != nil {
		t.Fatalf("diagnostic ID is not a UUID: %q", problem.DiagnosticID)
	}
	if strings.Contains(malformed.Body.String(), queryCanary) || strings.Contains(logs.String(), queryCanary) {
		t.Fatalf("malformed query canary leaked to response or structured logs: %s %s", malformed.Body, logs.String())
	}
	if got := strings.Count(logs.String(), `"msg":"query.terminal"`); got != 1 {
		t.Fatalf("early failure wrote %d terminal summaries, want exactly one: %s", got, logs.String())
	}
	var earlySummary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &earlySummary); err != nil {
		t.Fatalf("early terminal summary is not JSON: %v; %s", err, logs.String())
	}
	if earlySummary["diagnostic_id"] != problem.DiagnosticID || earlySummary["reason"] != "invalid_request" || earlySummary["request_status"] != float64(http.StatusBadRequest) {
		t.Fatalf("early summary does not correlate with problem details: %+v", earlySummary)
	}
	earlySpans := exporter.GetSpans()
	if !hasSpan(earlySpans, "query.request") || !hasSpan(earlySpans, "query.generation.resolve_current") {
		t.Fatalf("early adapter failure did not export root and resolution child spans: %+v", earlySpans)
	}
	if len(earlySpans) != 2 {
		t.Fatalf("early failure fabricated spans: %+v", earlySpans)
	}
	if !strings.Contains(otelErrors.String(), "controlled local span sink failure") {
		t.Fatalf("controlled exporter failure did not exercise error handler: %q", otelErrors.String())
	}

	logs.Reset()
	exporter.Reset()
	const providerCanary = "PRIVATE_PROVIDER_BODY_CANARY"
	h.SetQueryExecutor(&mcpTestExecutor{err: errors.New("provider response contained " + providerCanary)})
	runtimeFailure := performQueryDiagnosticsRequest(h, `{"q":"ordinary local fixture"}`)
	assertQueryProblem(t, runtimeFailure, http.StatusInternalServerError, "query_runtime_unavailable")
	if strings.Contains(runtimeFailure.Body.String(), providerCanary) || strings.Contains(logs.String(), providerCanary) {
		t.Fatalf("raw runtime cause leaked to response or structured logs: %s %s", runtimeFailure.Body, logs.String())
	}
	if got := strings.Count(logs.String(), `"msg":"query.terminal"`); got != 1 {
		t.Fatalf("runtime failure wrote %d terminal summaries, want exactly one: %s", got, logs.String())
	}
	for _, span := range exporter.GetSpans() {
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), providerCanary) {
				t.Fatalf("raw runtime cause leaked to span %s attribute %s", span.Name, attr.Key)
			}
		}
	}

	logs.Reset()
	exporter.Reset()
	h.SetQueryExecutor(receiptTraceExecutor{})
	response := performQueryDiagnosticsRequest(h, `{"q":"ordinary local fixture"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("query failed while local span export was failing: status=%d body=%s", response.Code, response.Body)
	}
	if strings.Contains(logs.String(), "ordinary local fixture") {
		t.Fatalf("raw query leaked to structured logs: %s", logs.String())
	}
	if got := strings.Count(logs.String(), `"msg":"query.terminal"`); got != 1 {
		t.Fatalf("successful query wrote %d terminal summaries, want exactly one: %s", got, logs.String())
	}
	var summary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &summary); err != nil {
		t.Fatalf("success terminal summary is not JSON: %v; %s", err, logs.String())
	}
	spans := exporter.GetSpans()
	root, ok := findSpan(spans, "query.request")
	if !ok {
		t.Fatalf("query root span missing: %+v", spans)
	}
	stage, ok := findSpan(spans, "query.stage.synthetic")
	if !ok {
		t.Fatalf("receipt stage span missing: %+v", spans)
	}
	runtime, ok := findSpan(spans, "query.runtime.execute")
	if !ok {
		t.Fatalf("runtime stage span missing: %+v", spans)
	}
	hostCall, ok := findSpan(spans, "query.host_call")
	if !ok {
		t.Fatalf("host-call span missing: %+v", spans)
	}
	if runtime.Parent.SpanID() != root.SpanContext.SpanID() || stage.Parent.SpanID() != runtime.SpanContext.SpanID() || hostCall.Parent.SpanID() != stage.SpanContext.SpanID() {
		t.Fatalf("query spans are not parented root→runtime→receipt-stage→host: root=%s runtime=%s/%s stage=%s/%s host=%s/%s", root.SpanContext.SpanID(), runtime.Parent.SpanID(), runtime.SpanContext.SpanID(), stage.Parent.SpanID(), stage.SpanContext.SpanID(), hostCall.Parent.SpanID(), hostCall.SpanContext.SpanID())
	}
	if root.SpanContext.TraceID() != runtime.SpanContext.TraceID() || root.SpanContext.TraceID() != stage.SpanContext.TraceID() || root.SpanContext.TraceID() != hostCall.SpanContext.TraceID() {
		t.Fatalf("query spans do not share a trace: root=%s runtime=%s stage=%s host=%s", root.SpanContext.TraceID(), runtime.SpanContext.TraceID(), stage.SpanContext.TraceID(), hostCall.SpanContext.TraceID())
	}
	if summary["diagnostic_id"] == "" || summary["trace_id"] != root.SpanContext.TraceID().String() || summary["span_id"] != root.SpanContext.SpanID().String() {
		t.Fatalf("structured terminal summary does not correlate to OTel root: summary=%+v root=%+v", summary, root)
	}
	for _, span := range spans {
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), queryCanary) || strings.Contains(attr.Value.AsString(), "ordinary local fixture") {
				t.Fatalf("raw query leaked to span %s attribute %s", span.Name, attr.Key)
			}
		}
	}

	logs.Reset()
	exporter.Reset()
	switchedStore := &generationSwitchRoot{currentGeneration: "generation-pinned"}
	h = New(switchedStore, nil, nil, nil, nil, nil)
	h.SetQueryExecutor(&mcpTestExecutor{err: errors.New("failure after the current generation was pinned")})
	pinnedFailure := performQueryDiagnosticsRequest(h, `{"q":"local generation switch fixture"}`)
	assertQueryProblem(t, pinnedFailure, http.StatusInternalServerError, "query_runtime_unavailable")
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pinnedSummary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &pinnedSummary); err != nil {
		t.Fatalf("pin-switch terminal summary is not JSON: %v; %s", err, logs.String())
	}
	if pinnedSummary["generation_id"] != "generation-pinned" || strings.Contains(logs.String(), "generation-new") {
		t.Fatalf("failure diagnostics did not retain the same immutable pinned generation: %s", logs.String())
	}
	if switchedStore.currentGeneration != "generation-new" || switchedStore.pins != 1 || switchedStore.manifestReads != 1 || switchedStore.identityReads != 1 {
		t.Fatalf("pin-switch reads current=%q pins=%d manifests=%d identities=%d; want one pin and identity from the original immutable view", switchedStore.currentGeneration, switchedStore.pins, switchedStore.manifestReads, switchedStore.identityReads)
	}
	pinnedRoot, ok := findSpan(exporter.GetSpans(), "query.request")
	if !ok {
		t.Fatalf("pin-switch query root span missing: %+v", exporter.GetSpans())
	}
	foundPinnedGeneration := false
	for _, attr := range pinnedRoot.Attributes {
		if string(attr.Key) == "generation_id" && attr.Value.AsString() == "generation-pinned" {
			foundPinnedGeneration = true
		}
	}
	if !foundPinnedGeneration {
		t.Fatalf("pin-switch root span omitted pinned generation: %+v", pinnedRoot.Attributes)
	}

	logs.Reset()
	exporter.Reset()
	missingConceptsStore := &generationSwitchRoot{
		currentGeneration: "generation-pinned",
		identityErr:       storage.ErrQueryGenerationIdentityUnavailable,
	}
	h = New(missingConceptsStore, nil, nil, nil, nil, nil)
	h.SetQueryExecutor(identityCheckingFailureExecutor{})
	missingConceptsFailure := performQueryDiagnosticsRequest(h, `{"q":"local missing concepts fixture"}`)
	assertQueryProblem(t, missingConceptsFailure, http.StatusInternalServerError, "query_runtime_unavailable")
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var missingConceptsSummary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &missingConceptsSummary); err != nil {
		t.Fatalf("missing-concepts terminal summary is not JSON: %v; %s", err, logs.String())
	}
	if missingConceptsSummary["generation_id"] != "generation-pinned" || strings.Contains(logs.String(), "generation-new") {
		t.Fatalf("missing-concepts failure did not retain the original pinned generation: %s", logs.String())
	}
	if missingConceptsStore.currentGeneration != "generation-new" || missingConceptsStore.pins != 1 || missingConceptsStore.manifestReads != 1 || missingConceptsStore.identityReads != 2 {
		t.Fatalf("missing-concepts pin state current=%q pins=%d manifests=%d identities=%d; want one pin and strict identity rejection", missingConceptsStore.currentGeneration, missingConceptsStore.pins, missingConceptsStore.manifestReads, missingConceptsStore.identityReads)
	}
	missingConceptsRoot, ok := findSpan(exporter.GetSpans(), "query.request")
	if !ok {
		t.Fatalf("missing-concepts query root span missing: %+v", exporter.GetSpans())
	}
	foundMissingConceptsGeneration := false
	for _, attr := range missingConceptsRoot.Attributes {
		if string(attr.Key) == "generation_id" && attr.Value.AsString() == "generation-pinned" {
			foundMissingConceptsGeneration = true
		}
	}
	if !foundMissingConceptsGeneration {
		t.Fatalf("missing-concepts root span omitted the known pinned generation: %+v", missingConceptsRoot.Attributes)
	}

	testAuthenticatedMCPQuerySchemaDiagnostics(t, provider, exporter, &logs)
}

func TestQueryFailureClassifierRequiresProviderSeamAndManifestProvenance(t *testing.T) {
	cause := &llm.HTTPStatusError{StatusCode: http.StatusServiceUnavailable}
	storageFailure := queryFailureFor("runtime", cause, nil)
	providerFailure := queryFailureFor("runtime", &llm.ProviderCallError{Err: cause}, nil)
	if storageFailure.code != "query_runtime_unavailable" || storageFailure.reason == "provider_server_error" {
		t.Fatalf("same cause outside provider seam was misclassified: %+v", storageFailure)
	}
	if providerFailure.code != "query_provider_failed" || providerFailure.reason != "provider_server_error" {
		t.Fatalf("actual provider seam lost safe provider category: %+v", providerFailure)
	}
	unprovenanced := queryFailureFor("generation_resolution", fmt.Errorf("pin: %w", storage.ErrQueryGenerationIdentityUnavailable), localfs.New(t.TempDir()))
	if unprovenanced.code != "storage_unavailable" || unprovenanced.reason == "published_generation_missing" {
		t.Fatalf("unprovenanced identity failure was classified as missing publication: %+v", unprovenanced)
	}
	if isMissingPublishedGenerationFailure(storage.ErrQueryGenerationUnpinned, false) {
		t.Fatal("generic unpinned identity failure was classified as a missing current manifest")
	}
	if !isMissingPublishedGenerationFailure(fmt.Errorf("runtime identity: %w", storage.ErrQueryGenerationUnpinned), true) {
		t.Fatal("pinned legacy view plus its unpinned identity cause did not identify missing publication")
	}
}

func TestProviderClientFailureIsClassifiedAtRealOutboundSeam(t *testing.T) {
	const providerBodyCanary = "PRIVATE_PROVIDER_RESPONSE_CANARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(providerBodyCanary))
	}))
	defer server.Close()

	client := llm.NewClientWithOptions("synthetic-test-credential", llm.ClientOptions{BaseURL: server.URL})
	if client == nil {
		t.Fatal("controlled provider client was not constructed")
	}
	_, err := client.Chat(context.Background(), "private system prompt", "private query")
	var providerErr *llm.ProviderCallError
	if !errors.As(err, &providerErr) {
		t.Fatalf("actual outbound failure lacks provider seam marker: %T %v", err, err)
	}
	failure := queryFailureFor("runtime", err, nil)
	if failure.code != "query_provider_failed" || failure.reason != "provider_server_error" {
		t.Fatalf("actual outbound provider failure was not safely classified: %+v", failure)
	}
	if strings.Contains(err.Error(), providerBodyCanary) || strings.Contains(failure.message, providerBodyCanary) {
		t.Fatalf("provider response body leaked: err=%q detail=%q", err, failure.message)
	}
}

func TestMCPQueryFailureThroughSDKRemainsSafeToolError(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "query-test", Version: "test"}, nil)
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetQueryExecutor(&mcpTestExecutor{err: errors.New("provider body PRIVATE_MCP_CANARY")})
	mcp.AddTool[queryProjectInput, any](server, &mcp.Tool{Name: "query_project", InputSchema: queryProjectInputSchema, OutputSchema: queryProjectOutputSchema}, func(ctx context.Context, req *mcp.CallToolRequest, input queryProjectInput) (*mcp.CallToolResult, any, error) {
		ctx = context.WithValue(ctx, mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{userID: "user", projectID: "project", keyID: "fixture-key"})
		return h.callMCPQuery(ctx, req, input)
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "query-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "query_project", Arguments: map[string]any{"q": "PRIVATE_QUERY_CANARY"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("SDK tools/call response lost tool-error semantics: %+v", result)
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok || textContent.Text == "" || strings.Contains(textContent.Text, "PRIVATE_MCP_CANARY") || strings.Contains(textContent.Text, "PRIVATE_QUERY_CANARY") {
		t.Fatalf("SDK safe text error is invalid or leaked a canary: %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["error"] != textContent.Text || structured["code"] == "" || structured["diagnostic_id"] == "" {
		t.Fatalf("SDK structured failure diagnostics missing: %#v", result.StructuredContent)
	}
}

type queryProblemDetailsForTest struct {
	Detail       string `json:"detail"`
	Error        string `json:"error"`
	Code         string `json:"code"`
	DiagnosticID string `json:"diagnostic_id"`
}

func assertQueryProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) queryProblemDetailsForTest {
	t.Helper()
	if response.Code != status || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("Query problem response status/type = %d/%q, want %d/application/problem+json: %s", response.Code, response.Header().Get("Content-Type"), status, response.Body)
	}
	var problem queryProblemDetailsForTest
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("Query problem response is invalid JSON: %v: %s", err, response.Body)
	}
	if problem.Code != code || problem.Detail == "" || problem.Detail != problem.Error || problem.DiagnosticID == "" {
		t.Fatalf("Query problem details = %+v, want code=%q safe detail/error and diagnostic ID", problem, code)
	}
	return problem
}

func performQueryDiagnosticsRequest(h *Handler, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body))
	c.Set("userID", "diagnostic-test-user")
	c.Set("projectID", "diagnostic-test-project")
	h.Query(c)
	return recorder
}

func testAuthenticatedMCPQuerySchemaDiagnostics(t *testing.T, provider *sdktrace.TracerProvider, exporter *tracetest.InMemoryExporter, logs *bytes.Buffer) {
	t.Helper()
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{userID: "fixture-user", projectID: "fixture-project", keyID: "fixture-key"}
	mcpHandler := h.mcpAuthenticatedHandler(identity)
	const schemaCanary = "PRIVATE_MCP_SCHEMA_CANARY"
	requests := []struct {
		name      string
		arguments string
	}{
		{name: "empty q", arguments: `{"q":""}`},
		{name: "missing q", arguments: `{}`},
		{name: "wrong q type", arguments: `{"q":{"secret":"` + schemaCanary + `"}}`},
		{name: "invalid mode", arguments: `{"q":"ordinary fixture","mode":"PRIVATE_MODE_CANARY"}`},
	}
	for i, tc := range requests {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			exporter.Reset()
			body := `{"jsonrpc":"2.0","id":` + fmt.Sprint(i+1) + `,"method":"tools/call","params":{"name":"query_project","arguments":` + tc.arguments + `}}`
			response := serveAuthenticatedMCPRequest(mcpHandler, body, true)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("MCP schema response status/type=%d/%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body)
			}
			var wire struct {
				Result struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					StructuredContent map[string]string `json:"structuredContent"`
					IsError           bool              `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
				t.Fatalf("MCP schema response is not JSON: %v: %s", err, response.Body)
			}
			result := wire.Result
			if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
				t.Fatalf("SDK validation lost safe IsError text semantics: %+v", result)
			}
			diagnosticID := result.StructuredContent["diagnostic_id"]
			if err := uuid.Validate(diagnosticID); err != nil {
				t.Fatalf("safe diagnostic ID missing from structured content: %+v", result.StructuredContent)
			}
			if result.StructuredContent["code"] != "invalid_query" || !strings.Contains(result.Content[0].Text, "Diagnostic ID: "+diagnosticID) {
				t.Fatalf("MCP text/structured diagnostic mismatch: %+v", result)
			}
			if strings.Contains(response.Body.String(), schemaCanary) || strings.Contains(response.Body.String(), "PRIVATE_MODE_CANARY") || strings.Contains(logs.String(), schemaCanary) {
				t.Fatalf("raw invalid arguments leaked through MCP or terminal summary: response=%s logs=%s", response.Body, logs.String())
			}
			if got := strings.Count(logs.String(), `"msg":"query.terminal"`); got != 1 {
				t.Fatalf("SDK schema rejection emitted %d terminal summaries, want one: %s", got, logs.String())
			}
			var summary map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &summary); err != nil {
				t.Fatalf("MCP terminal summary is not JSON: %v: %s", err, logs.String())
			}
			if summary["diagnostic_id"] != diagnosticID || summary["stage"] != "request_validation" || summary["reason"] != "invalid_request" || summary["mcp_is_error"] != true || summary["request_status"] != float64(http.StatusOK) {
				t.Fatalf("MCP terminal does not correlate to SDK failure: %+v", summary)
			}
			if err := provider.ForceFlush(context.Background()); err != nil {
				t.Fatal(err)
			}
			spans := exporter.GetSpans()
			if len(spans) != 1 || spans[0].Name != "query.request" {
				t.Fatalf("schema failure fabricated runtime/provider spans or lost root: %+v", spans)
			}
			if spans[0].SpanContext.TraceID().String() != summary["trace_id"] {
				t.Fatalf("MCP root and terminal trace IDs differ: span=%s summary=%v", spans[0].SpanContext.TraceID(), summary)
			}
		})
	}

	logs.Reset()
	exporter.Reset()
	protocolRejected := serveAuthenticatedMCPRequestWithAccept(
		mcpHandler,
		`{"jsonrpc":"2.0","id":50,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"ordinary fixture"}}}`,
		true,
		"application/json",
	)
	if protocolRejected.Code != http.StatusBadRequest {
		t.Fatalf("SDK Accept rejection status=%d, want 400: %s", protocolRejected.Code, protocolRejected.Body)
	}
	if present, isError := mcpToolResultState(protocolRejected.Body.Bytes()); present || isError {
		t.Fatalf("SDK transport rejection unexpectedly contains a CallToolResult: %s", protocolRejected.Body)
	}
	if got := strings.Count(logs.String(), `"msg":"query.terminal"`); got != 1 {
		t.Fatalf("SDK protocol rejection emitted %d terminal summaries, want one: %s", got, logs.String())
	}
	var protocolSummary map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &protocolSummary); err != nil {
		t.Fatalf("SDK protocol rejection summary is not JSON: %v; %s", err, logs.String())
	}
	if protocolSummary["stage"] != "mcp_protocol_rejection" || protocolSummary["reason"] != "mcp_protocol_rejected" || protocolSummary["outcome"] != "error" || protocolSummary["mcp_is_error"] != false || protocolSummary["request_status"] != float64(http.StatusBadRequest) {
		t.Fatalf("SDK protocol rejection summary misreports the actual transport outcome: %+v", protocolSummary)
	}
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "query.request" || spans[0].SpanContext.TraceID().String() != protocolSummary["trace_id"] {
		t.Fatalf("SDK rejection fabricated runtime/provider spans or lost root correlation: summary=%+v spans=%+v", protocolSummary, spans)
	}

	for _, request := range []string{
		`{"jsonrpc":"2.0","id":90,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":91,"method":"tools/list","params":{}}`,
	} {
		logs.Reset()
		exporter.Reset()
		response := serveAuthenticatedMCPRequest(mcpHandler, request, true)
		if response.Code != http.StatusOK || strings.Contains(logs.String(), `"msg":"query.terminal"`) || len(exporter.GetSpans()) != 0 {
			t.Fatalf("non-Query MCP request created Query diagnostics: status=%d logs=%s spans=%+v", response.Code, logs.String(), exporter.GetSpans())
		}
	}
	logs.Reset()
	exporter.Reset()
	unauthorized := serveAuthenticatedMCPRequest(h.MCPHandler(), `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"query_project","arguments":{"q":""}}}`, false)
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(logs.String(), `"msg":"query.terminal"`) || len(exporter.GetSpans()) != 0 {
		t.Fatalf("MCP auth rejection entered Query diagnostics: status=%d logs=%s spans=%+v", unauthorized.Code, logs.String(), exporter.GetSpans())
	}
}

func serveAuthenticatedMCPRequest(handler http.Handler, body string, authenticated bool) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Accept", "application/json")
	request.Header.Add("Accept", "text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if authenticated {
		request.Header.Set("Authorization", "Bearer fixture-token")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func serveAuthenticatedMCPRequestWithAccept(handler http.Handler, body string, authenticated bool, accept string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if authenticated {
		request.Header.Set("Authorization", "Bearer fixture-token")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func hasSpan(spans tracetest.SpanStubs, name string) bool {
	_, ok := findSpan(spans, name)
	return ok
}

func findSpan(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, span := range spans {
		if span.Name == name {
			return span, true
		}
	}
	return tracetest.SpanStub{}, false
}
