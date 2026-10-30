package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type intakeTestResolver struct{}

func (intakeTestResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type intakeTestFetcher func(context.Context, string) (urlintake.Page, error)

func (f intakeTestFetcher) Fetch(ctx context.Context, rawURL string) (urlintake.Page, error) {
	return f(ctx, rawURL)
}

type intakeTestEvaluator func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error)

func (f intakeTestEvaluator) Evaluate(ctx context.Context, input urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
	return f(ctx, input)
}

func TestScrapeRawConfirmationUsesVerifiedProjectAndStoredCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	store := localfs.New(root).WithScope("actor-1", "project-1")
	var fetchCalls atomic.Int32
	service := urlintake.NewService(
		intakeTestFetcher(func(context.Context, string) (urlintake.Page, error) {
			fetchCalls.Add(1)
			return urlintake.Page{Title: "Example Article", Markdown: "# Example Article\n\nReadable article body.\n"}, nil
		}),
		intakeTestEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			return urlintake.QualityEvaluation{ShellProbability: .5, ContentProbability: .5}, nil
		}),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			if scope.ActorID != "actor-1" || (scope.ProjectID != "project-1" && scope.ProjectID != "project-2") {
				t.Fatalf("store scope = %#v", scope)
			}
			return localfs.New(root).WithScope(scope.ActorID, scope.ProjectID), nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = intakeTestResolver{}
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	h := NewWithURLIntake(nil, nil, service)

	previewResponse := callScrapeRaw(t, h, "actor-1", "project-1", `{"url":"https://example.com/article"}`)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview HTTP status = %d, body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview ScrapeResponse
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "preview" || preview.Preview == nil || !preview.Preview.QualityChecked || preview.Preview.QualityStatus != "needs_confirmation" || preview.CandidateID == "" || preview.CandidateRevision == "" {
		t.Fatalf("preview response = %#v", preview)
	}
	if preview.Preview.Markdown != "# Example Article\n\nReadable article body.\n" {
		t.Fatalf("candidate preview Markdown = %q", preview.Preview.Markdown)
	}
	files, err := store.ListRawFiles(context.Background())
	markdownFiles := 0
	for _, file := range files {
		if strings.HasSuffix(file.Name, ".md") {
			markdownFiles++
		}
	}
	if err != nil || markdownFiles != 0 {
		t.Fatalf("unconfirmed candidate wrote raw files: %#v, %v", files, err)
	}

	acceptBody := `{"candidate_id":"` + preview.CandidateID + `","candidate_revision":"` + preview.CandidateRevision + `","title":"Example Article"}`
	wrongProject := callScrapeAction(t, h, "actor-1", "project-2", acceptBody, h.AcceptScrape)
	if wrongProject.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong-project confirmation HTTP status = %d, body=%s", wrongProject.Code, wrongProject.Body.String())
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("fetch calls after wrong-project confirmation = %d, want 1", fetchCalls.Load())
	}

	confirmed := callScrapeAction(t, h, "actor-1", "project-1", acceptBody, h.AcceptScrape)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmation HTTP status = %d, body=%s", confirmed.Code, confirmed.Body.String())
	}
	var saved ScrapeResponse
	if err := json.Unmarshal(confirmed.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "stored" || saved.Storage == nil || saved.Storage.Path != "raw/Example-Article.md" {
		t.Fatalf("confirmation result = %#v", saved)
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("fetch calls after confirmation = %d, want no refetch", fetchCalls.Load())
	}
	data, err := store.ReadFile(context.Background(), saved.Storage.Path)
	if err != nil || !bytes.Contains(data, []byte("Readable article body.")) {
		t.Fatalf("confirmed raw content = %q, %v", data, err)
	}
}

func TestScrapeRawReportsSafeFetchFailureAttribution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := localfs.New(t.TempDir())
	service := urlintake.NewService(
		intakeTestFetcher(func(context.Context, string) (urlintake.Page, error) {
			return urlintake.Page{}, &urlintake.ProviderFailure{
				Provider: urlintake.DeepSeekFetchProvider, Kind: urlintake.FailureProvider, StatusCode: http.StatusUnauthorized,
			}
		}),
		intakeTestEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			t.Fatal("Fetch failure must stop before quality evaluation")
			return urlintake.QualityEvaluation{}, nil
		}),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			return root.WithScope(scope.ActorID, scope.ProjectID), nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = intakeTestResolver{}
	h := NewWithURLIntake(nil, nil, service)

	response := callScrapeRaw(t, h, "actor-1", "project-1", `{"url":"https://example.com/article?query-canary=private"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("Web status = %d, want outer 503; body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	diagnostic, _ := payload["failure_diagnostic"].(map[string]any)
	if payload["status"] != "unavailable" || payload["failure_stage"] != "fetch" ||
		!strings.Contains(payload["message"].(string), "DeepSeek Fetch") ||
		diagnostic["dependency"] != urlintake.DeepSeekFetchProvider || diagnostic["operation"] != "fetch" ||
		diagnostic["category"] != "authentication" || diagnostic["upstream_status"] != float64(http.StatusUnauthorized) {
		t.Fatalf("Web fetch failure payload = %#v", payload)
	}
	files, err := root.WithScope("actor-1", "project-1").ListRawFiles(context.Background())
	if err != nil || len(files) != 0 {
		t.Fatalf("Fetch failure changed raw content: files=%v err=%v", files, err)
	}
}

func TestScrapeRawFailureLogAndFilteredTraceShareCorrelation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("LWC_OTLP_CLOUD_QA", "")
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	capture := otlpfixture.New(t)
	provider := capture.Provider(t, "lwc-bff-test", "fixture-revision", "")
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","code":"KEY_CANARY","message":"BODY_CANARY"}}`))
	}))
	defer endpoint.Close()

	root := localfs.New(t.TempDir())
	fetcher := urlintake.NewDeepSeekFetcher("API_KEY_CANARY")
	fetcher.Endpoint = endpoint.URL
	fetcher.Resolver = intakeTestResolver{}
	service := urlintake.NewService(fetcher, nil, func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
		return root.WithScope(scope.ActorID, scope.ProjectID), nil
	}, urlintake.DefaultQualityThresholds)
	service.Resolver = intakeTestResolver{}
	h := NewWithURLIntake(nil, nil, service)
	router := gin.New()
	router.Use(observability.RequestLifecycleMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("userID", "actor-1")
		c.Set("projectID", "project-1")
		c.Next()
	})
	router.POST("/api/v1/raw/scrape", h.ScrapeRaw)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/raw/scrape", strings.NewReader(`{"url":"https://example.com/article?query-canary=private"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("Web status = %d, want outer 503; body=%s", response.Code, response.Body.String())
	}
	requestID := response.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("Web failure omitted its request ID")
	}
	var payload ScrapeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FailureDiagnostic == nil || payload.FailureDiagnostic.Dependency != urlintake.DeepSeekFetchProvider ||
		payload.FailureDiagnostic.Category != urlintake.FailureCategoryAuthentication || payload.FailureDiagnostic.UpstreamStatus != http.StatusUnauthorized {
		t.Fatalf("Web failure diagnostic = %#v", payload.FailureDiagnostic)
	}

	var fetchLog, terminalLog map[string]any
	scanner := bufio.NewScanner(strings.NewReader(logs.String()))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode structured log: %v", err)
		}
		switch entry["msg"] {
		case "URL intake internal event":
			fetchLog = entry
		case "http.request.terminal":
			terminalLog = entry
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if fetchLog == nil || fetchLog["dependency"] != urlintake.DeepSeekFetchProvider || fetchLog["category"] != "authentication" ||
		fetchLog["upstream_status"] != float64(http.StatusUnauthorized) || fetchLog["request_id"] != requestID ||
		fetchLog["trace_id"] != "11111111111111111111111111111111" || fetchLog["span_id"] == "" {
		t.Fatalf("structured Fetch log = %#v", fetchLog)
	}
	if terminalLog == nil || terminalLog["request_id"] != requestID || terminalLog["trace_id"] != fetchLog["trace_id"] ||
		terminalLog["span_id"] != fetchLog["span_id"] || terminalLog["status_code"] != float64(http.StatusServiceUnavailable) || terminalLog["outcome"] != "failure" {
		t.Fatalf("terminal HTTP log = %#v", terminalLog)
	}

	capture.Flush(t, provider)
	exportRequests := capture.Requests()
	if len(exportRequests) == 0 {
		t.Fatal("filtered OTLP exporter made no export request")
	}
	var requestSpan *tracev1.Span
	for _, exportRequest := range exportRequests {
		for _, resource := range exportRequest.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					if span.Name == "http.request" {
						requestSpan = span
					}
				}
			}
		}
	}
	if requestSpan == nil || requestSpan.Status == nil || requestSpan.Status.Code != tracev1.Status_STATUS_CODE_ERROR ||
		fmt.Sprintf("%x", requestSpan.TraceId) != fetchLog["trace_id"] || fmt.Sprintf("%x", requestSpan.SpanId) != fetchLog["span_id"] {
		t.Fatalf("filtered HTTP failure span = %#v", requestSpan)
	}
	spanAttributes := make(map[string]any, len(requestSpan.Attributes))
	for _, attr := range requestSpan.Attributes {
		spanAttributes[attr.Key] = attr.Value.GetIntValue()
		if attr.Key == "request.id" {
			spanAttributes[attr.Key] = attr.Value.GetStringValue()
		}
	}
	if spanAttributes["request.id"] != requestID || spanAttributes["http.response.status_code"] != int64(http.StatusServiceUnavailable) {
		t.Fatalf("filtered HTTP span attributes = %#v", spanAttributes)
	}
	canaries := []string{"API_KEY_CANARY", "KEY_CANARY", "BODY_CANARY", "query-canary=private"}
	var exportedOTLP strings.Builder
	for _, exportRequest := range exportRequests {
		payload, err := protojson.Marshal(exportRequest)
		if err != nil {
			t.Fatalf("serialize captured OTLP export request: %v", err)
		}
		exportedOTLP.Write(payload)
	}
	containsCanary := func(payload string) bool {
		for _, canary := range canaries {
			if strings.Contains(payload, canary) {
				return true
			}
		}
		return false
	}
	combined := response.Body.String() + logs.String() + exportedOTLP.String()
	if containsCanary(combined) {
		for _, canary := range canaries {
			if strings.Contains(combined, canary) {
				t.Fatalf("private Fetch canary %q escaped response/log/actual OTLP export", canary)
			}
		}
	}
	negativeFixture := proto.Clone(exportRequests[0]).(*tracepb.ExportTraceServiceRequest)
	negativeSpan := negativeFixture.ResourceSpans[0].ScopeSpans[0].Spans[0]
	negativeSpan.Attributes = append(negativeSpan.Attributes,
		&commonv1.KeyValue{Key: "test.canary", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "BODY_CANARY"}}},
	)
	negativePayload, err := protojson.Marshal(negativeFixture)
	if err != nil {
		t.Fatalf("serialize canary-bearing OTLP fixture: %v", err)
	}
	if !containsCanary(string(negativePayload)) {
		t.Fatal("canary checker did not detect a canary in a canary-bearing OTLP export fixture")
	}
	fetchLogJSON, _ := json.Marshal(fetchLog)
	terminalLogJSON, _ := json.Marshal(terminalLog)
	t.Logf("LWC441_FETCH_FAILURE_LOG %s", fetchLogJSON)
	t.Logf("LWC441_HTTP_TERMINAL_LOG %s", terminalLogJSON)
	t.Logf("LWC441_LOCAL_OTEL_SPAN name=%s trace_id=%x span_id=%x status=%s request_id=%s http_status=%d", requestSpan.Name,
		requestSpan.TraceId, requestSpan.SpanId, requestSpan.Status.Code, spanAttributes["request.id"], spanAttributes["http.response.status_code"])
}

func callScrapeRaw(t *testing.T, h *Handler, actorID, projectID, body string) *httptest.ResponseRecorder {
	return callScrapeAction(t, h, actorID, projectID, body, h.ScrapeRaw)
}

func callScrapeAction(t *testing.T, h *Handler, actorID, projectID, body string, action func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/raw/scrape", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	context.Set("userID", actorID)
	context.Set("projectID", projectID)
	action(context)
	return recorder
}
