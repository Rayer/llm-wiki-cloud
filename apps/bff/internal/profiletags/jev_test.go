package profiletags

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestJevHTTPTransportUsesIndependentExactInputAndFailsClosed(t *testing.T) {
	exporter := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	response := `{"model":"jev-1.14.0","answers":{"applicable":{"type":"noul","noul":0.9},"known":{"type":"noul","noul":0.9},"match":{"type":"noul","noul":0.9}},"usage":{"input_tokens":1,"output_tokens":1}}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer explicit-fake-key" {
			t.Error("incorrect provider request")
		}
		var request struct {
			Model     string                     `json:"model"`
			State     []string                   `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "jev-latest" || len(request.State) != 4 || request.State[0] != "concept" || request.State[1] != "c1" || request.State[3] != "exact concept row" || len(request.Questions) != 3 {
			t.Errorf("incorrect independent input: %+v", request)
		}
		for _, raw := range request.Questions {
			var q struct {
				Instructions string `json:"instructions"`
			}
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			for _, exact := range []string{"domain", "positive", "negative", "ambiguous"} {
				if !strings.Contains(q.Instructions, exact) {
					t.Errorf("prompt omitted rule %q", exact)
				}
			}
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	e := NewJevEvaluator("explicit-fake-key")
	e.Endpoint = server.URL
	e.Client = server.Client()
	policy := ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: JevPromptVersion, SchemaVersion: DecisionSchema}
	item := Item{Kind: Concept, StableID: "c1", Content: []byte("exact concept row")}
	tag := Tag{ID: "test", Definition: "domain", MatchRule: "positive", NonMatchRule: "negative", UnknownRule: "ambiguous"}
	evalCtx, evalSpan := observability.StartProfileSpan(context.Background(), "profile.tag.evaluate")
	result, err := e.Evaluate(evalCtx, item, tag, policy)
	observability.FinishProfileSpan(evalSpan, "success")
	if err != nil || result.Judgment != Match || result.ReturnedModel != "jev-1.14.0" || result.Confidence != nil || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range exporter.Ended() {
		spans[span.Name()] = span
	}
	if spans["profile.tag.evaluate"] == nil || spans["profile.tag.transport"] == nil || spans["profile.tag.decode"] == nil {
		t.Fatalf("successful provider call omitted actual phase spans: %+v", spans)
	}
	for _, name := range []string{"profile.tag.transport", "profile.tag.decode"} {
		if spans[name].Parent().SpanID() != spans["profile.tag.evaluate"].SpanContext().SpanID() {
			t.Fatalf("%s span is not a child of evaluate: %+v", name, spans[name])
		}
	}
	original := response
	response = strings.Replace(original, `"known":{"type":"noul","noul":0.9}`, `"known":{"type":"noul","noul":0.1}`, 1)
	result, err = e.Evaluate(context.Background(), item, tag, policy)
	if err != nil || result.Judgment != Unknown {
		t.Fatalf("missing evidence = %+v %v", result, err)
	}
	for _, bad := range []string{`{}`, strings.Replace(original, `"noul":0.9`, `"noul":null`, 1), strings.Replace(original, `"model":`, `"model":"duplicate","model":`, 1), original + `{}`, strings.Repeat("x", 1048577)} {
		response = bad
		if _, err = e.Evaluate(context.Background(), item, tag, policy); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = e.Evaluate(ctx, item, tag, policy); err == nil {
		t.Fatal("canceled call accepted")
	}
}

func TestJevTransportDoesNotReturnProviderBodyOrKey(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, "secret-provider-body")
	}))
	defer s.Close()
	e := NewJevEvaluator("secret-test-key")
	e.Endpoint = s.URL
	e.Client = s.Client()
	_, err := e.Evaluate(context.Background(), Item{Kind: Source, StableID: "s", Content: []byte("s")}, Tag{}, ProviderPolicy{PromptVersion: JevPromptVersion})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestJevObservationSeparatesHTTPTransportFromLogicalDecode(t *testing.T) {
	const privatePayload = "PRIVATE_JEV_RESPONSE_CANARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, privatePayload)
	}))
	defer server.Close()
	exporter := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	evaluator := NewJevEvaluator("synthetic-key")
	evaluator.Endpoint = server.URL
	evaluator.Client = server.Client()
	ctx, span := observability.StartProfileSpan(context.Background(), "profile.tag.evaluate")
	_, err := evaluator.Evaluate(ctx, Item{Kind: Source, StableID: "private-id", Content: []byte("private record")}, Tag{ID: "private-tag"}, ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: JevPromptVersion})
	if err == nil || strings.Contains(err.Error(), privatePayload) {
		t.Fatalf("provider decode error = %v", err)
	}
	observability.FinishProfileSpan(span, "failure")
	spans := exporter.Ended()
	if len(spans) != 3 {
		t.Fatalf("unexpected provider spans: %+v", spans)
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range spans {
		byName[span.Name()] = span
		for _, attr := range span.Attributes() {
			if attr.Value.Type() == attribute.STRING && (strings.Contains(attr.Value.AsString(), privatePayload) || strings.Contains(attr.Value.AsString(), "private-id") || strings.Contains(attr.Value.AsString(), "private record")) {
				t.Fatal("private provider or record data reached the Profile span")
			}
		}
	}
	evaluate, transport, decode := byName["profile.tag.evaluate"], byName["profile.tag.transport"], byName["profile.tag.decode"]
	if evaluate == nil || transport == nil || decode == nil {
		t.Fatalf("missing evaluate/transport/decode spans: %+v", byName)
	}
	for _, child := range []sdktrace.ReadOnlySpan{transport, decode} {
		if child.Parent().SpanID() != evaluate.SpanContext().SpanID() {
			t.Fatalf("%s parent=%s, want evaluate span %s", child.Name(), child.Parent().SpanID(), evaluate.SpanContext().SpanID())
		}
	}
	transportOutcome, decodeOutcome := "", ""
	for _, attr := range transport.Attributes() {
		if attr.Key == "profile.provider.transport.outcome" {
			transportOutcome = attr.Value.AsString()
		}
	}
	for _, attr := range decode.Attributes() {
		if attr.Key == "profile.provider.decode.outcome" {
			decodeOutcome = attr.Value.AsString()
		}
	}
	if transportOutcome != "success" || decodeOutcome != "failure" {
		t.Fatalf("provider phase outcomes = transport:%q decode:%q", transportOutcome, decodeOutcome)
	}
}

func TestJevMissingModelAndMalformedAnswersStayOnDecodePhase(t *testing.T) {
	const valid = `{"model":"jev-1.14.0","answers":{"applicable":{"type":"noul","noul":0.9},"known":{"type":"noul","noul":0.9},"match":{"type":"noul","noul":0.9}}}`
	for _, test := range []struct {
		name, body, attr string
	}{
		{name: "missing model", body: strings.Replace(valid, `"model":"jev-1.14.0"`, `"model":""`, 1), attr: "profile.provider.model.outcome"},
		{name: "answers", body: strings.Replace(valid, `"match":{"type":"noul","noul":0.9}`, `"match":{"type":"other","noul":0.9}`, 1), attr: "profile.provider.answers.outcome"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			exporter := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exporter))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(previous)
				_ = provider.Shutdown(context.Background())
			})
			evaluator := NewJevEvaluator("synthetic-key")
			evaluator.Endpoint, evaluator.Client = server.URL, server.Client()
			ctx, evalSpan := observability.StartProfileSpan(context.Background(), "profile.tag.evaluate")
			_, err := evaluator.Evaluate(ctx, Item{Kind: Source, StableID: "safe-id", Content: []byte("safe content")}, Tag{ID: "safe-tag"}, ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: JevPromptVersion})
			observability.FinishProfileSpan(evalSpan, "failure")
			if err == nil {
				t.Fatal("invalid provider result accepted")
			}
			var decodeSpan sdktrace.ReadOnlySpan
			for _, span := range exporter.Ended() {
				if span.Name() == "profile.tag.decode" {
					decodeSpan = span
				}
			}
			if decodeSpan == nil || decodeSpan.Parent().SpanID() != evalSpan.SpanContext().SpanID() {
				t.Fatalf("missing decode phase child: %+v", decodeSpan)
			}
			got := ""
			for _, attr := range decodeSpan.Attributes() {
				if string(attr.Key) == test.attr {
					got = attr.Value.AsString()
				}
			}
			if got != "failure" {
				t.Fatalf("%s outcome=%q, want failure", test.attr, got)
			}
		})
	}
}

func TestJevChildSpansShareFiniteBuildDetailBudget(t *testing.T) {
	const valid = `{"model":"jev-1.13.0","answers":{"applicable":{"type":"noul","noul":0.9},"known":{"type":"noul","noul":0.9},"match":{"type":"noul","noul":0.9}}}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, valid)
	}))
	defer server.Close()
	exporter := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	in := fixture()
	in.Inventory.Items = make([]Item, 24)
	for i := range in.Inventory.Items {
		in.Inventory.Items[i] = Item{Kind: Source, StableID: fmt.Sprintf("source-%02d", i), Content: []byte("synthetic")}
	}
	in.Provider = ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: JevPromptVersion, SchemaVersion: DecisionSchema}
	evaluator := NewJevEvaluator("synthetic-key")
	evaluator.Endpoint, evaluator.Client = server.URL, server.Client()
	result, err := Build(context.Background(), memoryStore{}, evaluator, in)
	if err != nil || calls != 24 || result.Set.CompletedCount != 24 {
		t.Fatalf("Build calls=%d completed=%d error=%v", calls, result.Set.CompletedCount, err)
	}
	counts := map[string]int{}
	suppressed := -1
	for _, span := range exporter.Ended() {
		counts[span.Name()]++
		if span.Name() == "profile.tag.build" {
			for _, attr := range span.Attributes() {
				if attr.Key == "profile.suppressed_count" {
					suppressed = int(attr.Value.AsInt64())
				}
			}
		}
	}
	if counts["profile.tag.evaluate"] != 16 || counts["profile.tag.transport"] != 16 || counts["profile.tag.decode"] != 16 || suppressed != 8 {
		t.Fatalf("budget details evaluate=%d transport=%d decode=%d suppressed=%d", counts["profile.tag.evaluate"], counts["profile.tag.transport"], counts["profile.tag.decode"], suppressed)
	}
}
