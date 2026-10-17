package v1

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"google.golang.org/protobuf/proto"
)

func TestOTLPCaptureActualQuerySuccessAndLogicalFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldLogger := slog.Default()
	oldLogWriter, oldLogFlags := log.Writer(), log.Flags()
	var terminalLogs, standardLogs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&terminalLogs, nil)))
	log.SetOutput(&standardLogs)
	log.SetFlags(0)
	t.Cleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldLogWriter)
		log.SetFlags(oldLogFlags)
	})

	for _, tc := range []struct {
		name, caseID string
		malformed    bool
	}{
		{name: "success", caseID: "UC3"},
		{name: "HTTP 200 typed decode fallback", caseID: "UC4", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminalLogs.Reset()
			standardLogs.Reset()
			capture := otlpfixture.New(t)
			project := "synthetic-project"
			if otlpfixture.CloudMode() {
				project = os.Getenv("LWC_OTLP_QA_PROJECT")
			}
			provider := capture.Provider(t, "lwc-qa-query-fixture", "local-fixture", project)

			const queryCanary = "PRIVATE_QUERY_CANARY"
			const providerBodyCanary = "PRIVATE_LLM_BODY_CANARY"
			var providerCalls int
			llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls++
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("controlled LLM request did not use the actual chat completion route")
				}
				requestBody, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read controlled LLM request")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.malformed {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":17}}],"private_marker":"`+providerBodyCanary+`"}`)
					return
				}
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(requestBody, &request); err != nil {
					t.Errorf("decode controlled LLM request")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				citation := ""
				for _, message := range request.Messages {
					if message.Role != "user" {
						continue
					}
					start := strings.Index(message.Content, "[CITATION_REF_")
					if start >= 0 {
						if end := strings.IndexByte(message.Content[start:], ']'); end >= 0 {
							citation = message.Content[start : start+end+1]
						}
					}
				}
				if citation == "" {
					t.Errorf("production synthesis did not send a server-issued citation capability")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Synthetic answer `+citation+`"}}]}`)
			}))
			client := llm.NewClientWithOptions("synthetic-test-credential", llm.ClientOptions{
				Model: "deepseek-flash", BaseURL: llmServer.URL, RequestTimeoutSeconds: 2,
			})
			if client == nil {
				t.Fatal("construct controlled production LLM client")
			}
			t.Cleanup(llmServer.Close)

			generationStore, active := profileQueryFixture(t, "u/p", "generation-synthetic")
			scope := &profileQueryStore{
				retained: map[string]*profileQueryStore{"generation-synthetic": generationStore},
				latest:   generationStore,
			}
			rootStore := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": scope}}
			h := profileQueryHandlerWithClient(t, cache.New(), rootStore, active, client)
			router := gin.New()
			router.Use(observability.TraceContextMiddleware())
			router.POST("/api/v1/query", func(c *gin.Context) {
				c.Set("userID", "u")
				c.Set("projectID", "p")
				h.Query(c)
			})

			traceID, parentID, traceParent := otlpfixture.NewParent(t)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{"q":"coffee `+queryCanary+`","mode":"full"}`))
			request.Header.Set("traceparent", traceParent)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("actual Query handler status=%d, want 200", response.Code)
			}
			var body struct {
				Query   string `json:"query"`
				Status  string `json:"status"`
				Results []struct {
					Slug string `json:"slug"`
				} `json:"results"`
				AISynth   string `json:"ai_synth"`
				Citations []struct {
					Slug string `json:"slug"`
				} `json:"citations"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("actual Query handler did not return JSON: %v", err)
			}
			if body.Query != "coffee "+queryCanary || len(body.Results) != 1 || body.Results[0].Slug != "coffee" {
				t.Fatalf("actual Query fixture output did not preserve its synthetic retrieval result")
			}
			if tc.malformed {
				if body.AISynth != "" || len(body.Citations) != 0 {
					t.Fatal("HTTP 200 typed decode failure did not preserve the actual no-synthesis fallback")
				}
			} else if body.AISynth != "Synthetic answer [Coffee generation-synthetic]" || len(body.Citations) != 1 || body.Citations[0].Slug != "coffee" {
				t.Fatal("actual Query success did not preserve citation-authorized synthesis output")
			}
			if providerCalls != 1 {
				t.Fatalf("controlled HTTP LLM calls=%d, want one actual synthesis request", providerCalls)
			}

			capture.Flush(t, provider)
			capture.LogIdentities(t, tc.caseID)
			outcome := "success"
			if tc.malformed {
				outcome = "degraded"
			}
			t.Logf("OTLP_QA_CASE=%s trace_id=%s parent_span_id=%s expected_span=query.request outcome=%s http_status=%d", tc.caseID, traceID, parentID, outcome, response.Code)

			identities := capture.Identities()
			find := func(name string) (otlpfixture.SpanIdentity, bool) {
				for _, item := range identities {
					if item.Name == name && item.TraceID == traceID {
						return item, true
					}
				}
				return otlpfixture.SpanIdentity{}, false
			}
			root, ok := find("query.request")
			if !ok || root.ParentSpanID != parentID || root.SpanID == parentID {
				t.Fatalf("actual query request root did not preserve the incoming parent")
			}
			runtimeSpan, ok := find("query.runtime.execute")
			if !ok || runtimeSpan.ParentSpanID != root.SpanID {
				t.Fatalf("actual query runtime stage was not parented to the request root")
			}
			synthesis, ok := find("query.stage.answer_synthesis")
			if !ok || synthesis.ParentSpanID != runtimeSpan.SpanID {
				t.Fatalf("actual query synthesis stage was not parented to the production runtime stage")
			}
			if tc.malformed {
				if synthesis.Attributes["query.outcome"] != "degraded" || root.Attributes["query.dependency_degraded"] != "true" {
					t.Fatalf("typed HTTP 200 decode failure did not produce a separate dependency fallback")
				}
			} else if synthesis.Attributes["query.outcome"] != "success" || root.Attributes["query.business_outcome"] != "success" {
				t.Fatalf("successful Query stages did not report their real business outcome")
			}

			var terminal map[string]any
			terminalCount := 0
			for _, line := range strings.Split(strings.TrimSpace(terminalLogs.String()), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "query.terminal" {
					terminalCount++
					terminal = entry
				}
			}
			if terminalCount != 1 || terminal == nil || terminal["trace_id"] != traceID || terminal["span_id"] != root.SpanID {
				t.Fatalf("actual Query did not emit exactly one terminal correlated to its request span")
			}
			for _, logs := range []string{terminalLogs.String(), standardLogs.String()} {
				if strings.Contains(logs, queryCanary) || strings.Contains(logs, providerBodyCanary) {
					t.Fatal("private synthetic Query or LLM canary reached logs")
				}
			}
			if otlpfixture.CloudMode() {
				return
			}
			var encoded bytes.Buffer
			var foundRoot bool
			for _, export := range capture.Requests() {
				data, err := proto.Marshal(export)
				if err != nil {
					t.Fatal("marshal captured OTLP request")
				}
				encoded.Write(data)
				for _, resource := range export.ResourceSpans {
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							if span.Name == "query.request" && hex.EncodeToString(span.TraceId) == traceID {
								foundRoot = true
								if hex.EncodeToString(span.ParentSpanId) != parentID {
									t.Fatal("filtered OTLP root lost its actual remote parent")
								}
							}
						}
					}
				}
			}
			if !foundRoot || bytes.Contains(encoded.Bytes(), []byte(queryCanary)) || bytes.Contains(encoded.Bytes(), []byte(providerBodyCanary)) {
				t.Fatal("real Query OTLP spans were absent or contained a synthetic privacy canary")
			}
		})
	}
}
