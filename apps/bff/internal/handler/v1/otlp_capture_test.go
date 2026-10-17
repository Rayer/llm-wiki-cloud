package v1

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"google.golang.org/protobuf/proto"
)

func TestOTLPCaptureActualQuerySuccessAndLogicalFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		executor   query.Executor
		wantStatus string
		caseID     string
	}{
		{name: "success", executor: receiptTraceExecutor{}, caseID: "UC3"},
		{name: "logical fallback", executor: fallbackNoEvidenceExecutor{}, wantStatus: "insufficient_evidence", caseID: "UC4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := otlpfixture.New(t)
			project := "synthetic-project"
			if otlpfixture.CloudMode() {
				project = os.Getenv("LWC_OTLP_QA_PROJECT")
			}
			provider := capture.Provider(t, "lwc-qa-query-fixture", "local-fixture", project)
			const queryCanary = "PRIVATE_QUERY_CANARY"
			h := New(&generationSwitchRoot{currentGeneration: "generation-synthetic"}, nil, nil, nil, nil, nil)
			h.SetQueryExecutor(tc.executor)
			router := gin.New()
			router.Use(observability.TraceContextMiddleware())
			router.Use(func(c *gin.Context) {
				c.Set("userID", "synthetic-user")
				c.Set("projectID", "synthetic-project")
				c.Next()
			})
			router.POST("/api/v1/query", h.Query)
			traceID, parentID, traceParent := otlpfixture.NewParent(t)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{"q":"`+queryCanary+`"}`))
			request.Header.Set("traceparent", traceParent)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("query status=%d body=%s", response.Code, response.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("query response is invalid JSON: %v", err)
			}
			if tc.wantStatus != "" && body["status"] != tc.wantStatus {
				t.Fatalf("logical fallback status=%v want=%s", body["status"], tc.wantStatus)
			}
			capture.Flush(t, provider)
			capture.LogIdentities(t, tc.caseID)
			outcome := body["status"]
			if outcome == nil {
				outcome = "success"
			}
			t.Logf("OTLP_QA_CASE=%s trace_id=%s parent_span_id=%s expected_span=query.request outcome=%v http_status=%d", tc.caseID, traceID, parentID, outcome, response.Code)
			if otlpfixture.CloudMode() {
				return
			}
			encoded := bytes.Buffer{}
			var rootFound bool
			for _, request := range capture.Requests() {
				data, err := proto.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				encoded.Write(data)
				for _, resource := range request.ResourceSpans {
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							if span.Name != "query.request" {
								continue
							}
							rootFound = true
							if hex.EncodeToString(span.TraceId) != traceID || hex.EncodeToString(span.ParentSpanId) != parentID || hex.EncodeToString(span.SpanId) == parentID {
								t.Fatalf("query root parent identity changed: %+v", span)
							}
							attrs := make(map[string]string)
							businessOutcome, dependencyDegraded := "", false
							for _, attr := range span.Attributes {
								attrs[attr.Key] = attr.Value.GetStringValue()
								if attr.Key == "query.business_outcome" {
									businessOutcome = attr.Value.GetStringValue()
								}
								if attr.Key == "query.dependency_degraded" {
									dependencyDegraded = attr.Value.GetBoolValue()
								}
							}
							if tc.wantStatus != "" && (businessOutcome != "no_evidence" || !dependencyDegraded) {
								t.Fatalf("logical fallback tracing conflated business outcome and dependency status: attrs=%v", attrs)
							}
						}
					}
				}
			}
			if !rootFound {
				t.Fatal("actual Query handler root span was not exported through the filtered OTLP pipeline")
			}
			if bytes.Contains(encoded.Bytes(), []byte(queryCanary)) {
				t.Fatal("raw query reached OTLP protobuf")
			}
		})
	}
}
