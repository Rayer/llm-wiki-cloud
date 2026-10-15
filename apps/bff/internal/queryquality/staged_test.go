package queryquality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type stageReader struct{}

func (stageReader) Prefix() string { return "staged-test" }
func (stageReader) ReadFile(_ context.Context, path string) ([]byte, error) {
	if path == "cache/id_map.json" {
		return []byte(`{"concept":{"abcdef123456":"alice"}}`), nil
	}
	return []byte(`{"slug":"alice","title":"Alice","body":"Alice follows the White Rabbit"}` + "\n"), nil
}
func (stageReader) ListConcepts(context.Context, bool) ([]gcs.WikiPage, error) { return nil, nil }
func (stageReader) GetPage(context.Context, string, string) (*gcs.WikiPage, []byte, error) {
	return &gcs.WikiPage{Slug: "alice", Title: "Alice"}, []byte("Alice follows the White Rabbit."), nil
}

type stageProvider struct{ calls atomic.Int32 }

func (p *stageProvider) Chat(context.Context, string, string) (string, error) {
	p.calls.Add(1)
	return `{"raw_query":"Alice","preferred":[{"kind":"entity","value":"Alice","terms":["Alice"],"proof":"lexical"}],"required":[],"excluded":[],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`, nil
}

type stageMatcher struct {
	CandidateMatcher
	calls int
}

func (m *stageMatcher) Match(ctx context.Context, r MatchRequest) (EligibilityResult, error) {
	m.calls++
	return m.CandidateMatcher.Match(ctx, r)
}

type stageSelector struct {
	ResultSelector
	calls int
}

func (s *stageSelector) Select(ctx context.Context, r SelectionInput) (SelectionResult, error) {
	s.calls++
	return s.ResultSelector.Select(ctx, r)
}

type stageTransport struct {
	calls int
	t     *testing.T
}

func (s *stageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	data, _ := io.ReadAll(req.Body)
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		s.t.Fatal(err)
	}
	var token string
	for _, message := range request.Messages {
		if start := strings.Index(message.Content, "[CITATION_REF_"); start >= 0 {
			rest := message.Content[start:]
			token = rest[:strings.Index(rest, "]")+1]
		}
	}
	if token == "" {
		s.t.Fatal("production synthesizer did not issue citation capability")
	}
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Alice follows the rabbit. " + token}}}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}
func TestStagedProductionReplayIsCausallyIsolatedAndEquivalent(t *testing.T) {
	transport := &stageTransport{t: t}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	provider := &stageProvider{}
	cc := cache.New()
	executor, err := NewStrictProductionExecutorWithQueryServiceConfig(cc, provider, nil, query.NewService(cc, nil, llm.NewClient("explicit-mocked-provider")), DefaultRetrievalProfile(), StructuredPlanPromptID, DefaultOptions(), query.RuntimeConfigIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	production := executor.(*ProductionExecutor)
	matcher := &stageMatcher{CandidateMatcher: production.queryRetrievalPipeline.candidateMatcher}
	selector := &stageSelector{ResultSelector: production.queryRetrievalPipeline.resultSelector}
	production.queryRetrievalPipeline.candidateMatcher = matcher
	production.queryRetrievalPipeline.resultSelector = selector
	request := query.Request{Query: "Alice", Mode: "wiki"}
	state := StageReplay{}
	var last query.Result
	for _, stage := range []int{70, 80, 90, 100} {
		state.Stage = stage
		ctx, err := WithStageReplay(context.Background(), &state)
		if err != nil {
			t.Fatal(err)
		}
		last, err = executor.Execute(ctx, stageReader{}, request)
		if err != nil {
			t.Fatal(err)
		}
		if provider.calls.Load() != 3 {
			t.Fatalf("stage %d reran expansion: %d", stage, provider.calls.Load())
		}
		wantMatcher, wantSelector, wantSynthesis := 0, 0, 0
		if stage >= 80 {
			wantMatcher = 1
		}
		if stage >= 90 {
			wantSelector = 1
		}
		if stage == 100 {
			wantSynthesis = 1
		}
		if matcher.calls != wantMatcher || selector.calls != wantSelector || transport.calls != wantSynthesis {
			t.Fatalf("stage %d called skipped work: match=%d selection=%d synthesis=%d", stage, matcher.calls, selector.calls, transport.calls)
		}
		// Real serialized replay, not retained in-memory pointers.
		data, _ := json.Marshal(state)
		state = StageReplay{}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
	}
	if last.AISynth == "" || len(last.Citations) != 1 || last.Citations[0].Slug != "alice" || strings.Contains(last.AISynth, "CITATION_REF_") {
		t.Fatalf("unresolved synthesis: %#v", last)
	}
	normal, err := executor.Execute(context.Background(), stageReader{}, request)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(normal)
	b, _ := json.Marshal(last)
	if string(a) != string(b) {
		t.Fatalf("staged execution differs from production: %s / %s", a, b)
	}
	// Synthesis-only replay performs a new synthesis and no retrieval inference.
	state.Stage = 100
	ctx, _ := WithStageReplay(context.Background(), &state)
	before := provider.calls.Load()
	m, s := matcher.calls, selector.calls
	if _, err := executor.Execute(ctx, stageReader{}, request); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != before || matcher.calls != m || selector.calls != s || transport.calls != 3 {
		t.Fatal("synthesis replay reran upstream")
	}
}
func TestStagedMissingDependenciesFailBeforeExecution(t *testing.T) {
	for _, stage := range []int{0, 60, 80, 90, 100} {
		if _, err := WithStageReplay(context.Background(), &StageReplay{Stage: stage}); err == nil {
			t.Fatalf("accepted missing stage %d dependencies", stage)
		}
	}
}

func TestExpansionDiagnosticsAreBoundedAndDoNotExposeProviderText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ExpansionError{Reason: "invalid_plan"}, "invalid_plan"},
		{&ExpansionError{Reason: "provider_error", Err: &llm.HTTPStatusError{StatusCode: 401}}, "provider_auth_rejected"},
		{&ExpansionError{Reason: "provider_error", Err: &llm.HTTPStatusError{StatusCode: 429}}, "provider_rate_limited"},
		{&ExpansionError{Reason: "provider_error", Err: &llm.HTTPStatusError{StatusCode: 503}}, "provider_server_error"},
		{&ExpansionError{Reason: "provider_error", Err: fmt.Errorf("private token and provider body")}, "provider_transport_or_response_error"},
		{context.DeadlineExceeded, "timeout"},
	} {
		if got := expansionDiagnostic(tc.err, ""); got != tc.want {
			t.Fatalf("category = %q, want %q", got, tc.want)
		}
	}
}

type failingStageProvider struct{ err error }

func (p failingStageProvider) Chat(context.Context, string, string) (string, error) {
	return "private-provider-body", p.err
}
func TestParallelExpansionPreservesSafeAttemptFailureCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "invalid_plan"},
		{&llm.HTTPStatusError{StatusCode: 429}, "provider_rate_limited"},
	} {
		expander, err := NewParallelMinimalStructuredPlanExpanderWithPrompt(failingStageProvider{tc.err}, NewDeterministicExpander(), DefaultOptions(), StructuredPlanPromptID)
		if err != nil {
			t.Fatal(err)
		}
		plan, info, err := expander.(TracedQueryExpander).ExpandWithTrace(context.Background(), ExpansionRequest{Query: "Alice"})
		if err != nil || !plan.Fallback || info.ProviderFailedAttempts != 3 {
			t.Fatal("unexpected fallback semantics", err)
		}
		for _, attempt := range info.AttemptOutcomes {
			if attempt.Diagnostic != tc.want {
				t.Fatalf("diagnostic %q", attempt.Diagnostic)
			}
		}
		data, _ := json.Marshal(info)
		if strings.Contains(string(data), "private-provider-body") {
			t.Fatal("provider text leaked")
		}
	}
}

type contextLLMProvider struct{ client *llm.Client }

func (p contextLLMProvider) Chat(ctx context.Context, system, user string) (string, error) {
	return p.client.Chat(ctx, system, user)
}

type delayedAttemptTransport struct {
	completed chan int
	starts    atomic.Int32
}

type cancellationAttemptTransport struct {
	started    chan int
	canceled   chan int
	startCount atomic.Int32
}

func (r *cancellationAttemptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	index, ok := llm.CallAttemptIndex(req.Context())
	if !ok {
		return nil, fmt.Errorf("missing attempt context")
	}
	r.startCount.Add(1)
	r.started <- index
	<-req.Context().Done()
	r.canceled <- index
	return nil, req.Context().Err()
}

func (r *delayedAttemptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	index, ok := llm.CallAttemptIndex(req.Context())
	if !ok {
		return nil, fmt.Errorf("missing attempt context")
	}
	r.starts.Add(1)
	if index == 0 {
		time.Sleep(45 * time.Millisecond)
	} else {
		time.Sleep(5 * time.Millisecond)
	}
	r.completed <- index
	const plan = `{"raw_query":"Alice","preferred":[{"kind":"entity","value":"Alice","terms":["Alice"],"proof":"lexical"}],"required":[],"excluded":[],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": plan}}}})
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func TestParallelLLMAttemptsKeepConcurrentParentsAndSeparateNetworkFromDecode(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	transport := &delayedAttemptTransport{completed: make(chan int, 4)}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	client := llm.NewClientWithOptions("local-fixture-key", llm.ClientOptions{BaseURL: "https://provider.example.test", Model: "deepseek-v4-flash", Reasoning: llm.ReasoningNone})
	prompt, ok := LookupPrompt(StructuredPlanPromptID)
	if !ok {
		t.Fatal("structured plan prompt missing")
	}
	structured := StructuredPlanExpander{
		provider: contextLLMProvider{client: client}, prompt: prompt,
		decodePlan: func(raw, queryText string) (QueryPlan, error) {
			time.Sleep(30 * time.Millisecond)
			return DecodePlan(raw, queryText)
		},
	}
	expander, err := NewParallelQueryExpander(structured, NewDeterministicExpander(), Options{ExpansionAttempts: 2, KeywordsPerAttempt: 24})
	if err != nil {
		t.Fatal(err)
	}

	type completedQuery struct {
		index   int
		info    ExpansionInfo
		receipt query.Receipt
		err     error
	}
	results := make(chan completedQuery, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			rootCtx, root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(), "query.request")
			ctx, recorder := query.WithReceipt(rootCtx)
			stageCtx := recorder.StartStage(ctx, "query_expansion", "deepseek", "safe-model", "none")
			_, info, err := expander.(TracedQueryExpander).ExpandWithTrace(stageCtx, ExpansionRequest{Query: "Alice"})
			if err != nil {
				query.FinishStage(stageCtx, "failure")
			} else {
				outcomes := make([]query.ExpansionAttemptReceipt, 0, len(info.AttemptOutcomes))
				for _, outcome := range info.AttemptOutcomes {
					outcomes = append(outcomes, query.ExpansionAttemptReceipt{AttemptIndex: outcome.AttemptIndex, Outcome: outcome.Outcome})
				}
				recorder.SetExpansionAttemptOutcomes(outcomes)
				query.FinishStage(stageCtx, "success")
			}
			receipt := recorder.Receipt()
			root.End()
			results <- completedQuery{index: index, info: info, receipt: receipt, err: err}
		}()
	}

	completionOrder := make([]int, 0, 4)
	for range 4 {
		completionOrder = append(completionOrder, <-transport.completed)
	}
	wg.Wait()
	completed := []completedQuery{<-results, <-results}
	if len(completionOrder) != 4 || completionOrder[0] != 1 || completionOrder[1] != 1 || completionOrder[2] != 0 || completionOrder[3] != 0 {
		t.Fatalf("transport completion order=%v, want indexes [1 1 0 0]", completionOrder)
	}
	if transport.starts.Load() != 4 {
		t.Fatalf("provider calls=%d, want the configured two attempts for each query", transport.starts.Load())
	}
	for _, result := range completed {
		if result.err != nil || result.info.RequestedAttempts != 2 || result.info.SuccessfulAttempts != 2 || len(result.info.AttemptOutcomes) != 2 {
			t.Fatalf("query %d expansion=%+v err=%v", result.index, result.info, result.err)
		}
		if len(result.receipt.HostCalls) != 2 || len(result.receipt.ExpansionAttemptOutcomes) != 2 {
			t.Fatalf("query %d receipt=%+v", result.index, result.receipt)
		}
		for i, attempt := range result.info.AttemptOutcomes {
			if attempt.AttemptIndex != i+1 || result.receipt.ExpansionAttemptOutcomes[i].AttemptIndex != i+1 {
				t.Fatalf("query %d legacy receipt attempt indexes changed: info=%+v receipt=%+v", result.index, result.info.AttemptOutcomes, result.receipt.ExpansionAttemptOutcomes)
			}
		}
	}

	spans := exporter.GetSpans()
	var roots, stages, attempts, hosts []tracetest.SpanStub
	for _, span := range spans {
		switch span.Name {
		case "query.request":
			roots = append(roots, span)
		case "query.stage.query_expansion":
			stages = append(stages, span)
		case "query.expansion.attempt":
			attempts = append(attempts, span)
		case "query.host_call":
			hosts = append(hosts, span)
		}
	}
	if len(roots) != 2 || len(stages) != 2 || len(attempts) != 4 || len(hosts) != 4 {
		t.Fatalf("root/stage/attempt/host spans=%d/%d/%d/%d", len(roots), len(stages), len(attempts), len(hosts))
	}
	rootIDs := make(map[string]bool, len(roots))
	stageIDs := make(map[string]bool, len(stages))
	attemptIDs := make(map[string]bool, len(attempts))
	for _, span := range roots {
		rootIDs[span.SpanContext.TraceID().String()] = true
	}
	for _, span := range stages {
		if !rootIDs[span.SpanContext.TraceID().String()] {
			t.Fatalf("stage has no query root: %+v", span)
		}
		stageIDs[span.SpanContext.SpanID().String()] = true
	}
	for _, span := range attempts {
		if !stageIDs[span.Parent.SpanID().String()] {
			t.Fatalf("attempt crossed query parents: %+v", span)
		}
		attemptIDs[span.SpanContext.SpanID().String()] = true
		var index int64 = -1
		var purpose string
		for _, attr := range span.Attributes {
			if attr.Key == attribute.Key("query.attempt_index") {
				index = attr.Value.AsInt64()
			}
			if attr.Key == attribute.Key("query.llm_purpose") {
				purpose = attr.Value.AsString()
			}
		}
		if index < 0 || index > 1 || purpose != "expansion" {
			t.Fatalf("attempt attrs index=%d purpose=%q: %+v", index, purpose, span.Attributes)
		}
	}
	for _, span := range hosts {
		if !attemptIDs[span.Parent.SpanID().String()] {
			t.Fatalf("host call crossed actual attempt context: %+v", span)
		}
		var index int64 = -1
		for _, attr := range span.Attributes {
			if attr.Key == attribute.Key("query.attempt_index") {
				index = attr.Value.AsInt64()
			}
		}
		if index < 0 || index > 1 || !span.EndTime.Before(findAttemptSpan(attempts, span.Parent.SpanID()).EndTime) {
			t.Fatalf("host transport/action timing or index invalid: host=%+v attempt=%+v", span, findAttemptSpan(attempts, span.Parent.SpanID()))
		}
	}
}

func TestCanceledParallelAttemptsRetainAttemptAndReceiptCounts(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	transport := &cancellationAttemptTransport{started: make(chan int, 2), canceled: make(chan int, 2)}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	client := llm.NewClientWithOptions("local-fixture-key", llm.ClientOptions{BaseURL: "https://provider.example.test", Model: "deepseek-v4-flash", Reasoning: llm.ReasoningNone})
	prompt, ok := LookupPrompt(StructuredPlanPromptID)
	if !ok {
		t.Fatal("structured plan prompt missing")
	}
	structured := StructuredPlanExpander{provider: contextLLMProvider{client: client}, prompt: prompt}
	expander, err := NewParallelQueryExpander(structured, NewDeterministicExpander(), Options{ExpansionAttempts: 2, KeywordsPerAttempt: 24})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := NewQueryRetrievalPipelineWithOptions(expander, NewLexicalMatcher(nil), NewResultSelector(), nil, Options{ExpansionAttempts: 2, KeywordsPerAttempt: 24})
	rootCtx, root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(), "query.request")
	ctx, recorder := query.WithReceipt(rootCtx)
	callCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan error, 1)
	go func() {
		_, err := pipeline.Execute(callCtx, stageReader{}, query.Request{Query: "Alice", Mode: "wiki"})
		resultCh <- err
	}()
	<-transport.started
	<-transport.started
	cancel()
	resultErr := <-resultCh
	if !errors.Is(resultErr, context.Canceled) {
		t.Fatalf("canceled pipeline error=%v, want context.Canceled", resultErr)
	}
	if transport.startCount.Load() != 2 {
		t.Fatalf("provider transport calls=%d, want exactly two actual attempts", transport.startCount.Load())
	}
	for range 2 {
		<-transport.canceled
	}
	receipt := recorder.Receipt()
	root.End()
	if len(receipt.HostCalls) != 2 {
		t.Fatalf("canonical host receipts=%d, want two canceled transport attempts: %+v", len(receipt.HostCalls), receipt.HostCalls)
	}

	attempts := make(map[int]tracetest.SpanStub)
	hosts := make([]tracetest.SpanStub, 0, 2)
	var stage tracetest.SpanStub
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "query.stage.query_expansion":
			stage = span
		case "query.expansion.attempt":
			index, found := intSpanAttribute(span.Attributes, "query.attempt_index")
			if !found {
				t.Fatalf("attempt index missing: %+v", span)
			}
			attempts[index] = span
		case "query.host_call":
			hosts = append(hosts, span)
		}
	}
	if len(attempts) != 2 || len(hosts) != 2 || !stage.SpanContext.SpanID().IsValid() {
		t.Fatalf("canceled attempt/host spans=%d/%d, want two each", len(attempts), len(hosts))
	}
	if stage.Parent.SpanID() != root.SpanContext().SpanID() {
		t.Fatalf("expansion stage parent=%s, want request %s", stage.Parent.SpanID(), root.SpanContext().SpanID())
	}
	if got, found := intSpanAttribute(stage.Attributes, "query.expansion.attempted_count"); !found || got != 2 {
		t.Fatalf("attempted count after cancellation=%d found=%v, want two actual attempts: %+v", got, found, stage.Attributes)
	}
	if got, found := intSpanAttribute(stage.Attributes, "query.expansion.failed_count"); !found || got != 2 {
		t.Fatalf("failed count after cancellation=%d found=%v, want two canceled attempts: %+v", got, found, stage.Attributes)
	}
	if got, found := intSpanAttribute(stage.Attributes, "query.expansion.fallback_count"); !found || got != 0 {
		t.Fatalf("fallback count after cancellation=%d found=%v, want zero: %+v", got, found, stage.Attributes)
	}
	for index, attempt := range attempts {
		if attempt.Parent.SpanID() != stage.SpanContext.SpanID() || spanStringAttribute(attempt.Attributes, "query.outcome") != "canceled" {
			t.Fatalf("attempt %d parent/outcome mismatch: %+v", index, attempt)
		}
		foundHost := false
		for _, host := range hosts {
			if host.Parent.SpanID() == attempt.SpanContext.SpanID() {
				foundHost = true
				if spanStringAttribute(host.Attributes, "query.outcome") != "canceled" || host.EndTime.After(attempt.EndTime) {
					t.Fatalf("attempt %d host transport outcome/time mismatch: host=%+v attempt=%+v", index, host, attempt)
				}
			}
		}
		if !foundHost {
			t.Fatalf("attempt %d has no actual host child: %+v", index, hosts)
		}
	}
}

func intSpanAttribute(attrs []attribute.KeyValue, key string) (int, bool) {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return int(attr.Value.AsInt64()), true
		}
	}
	return 0, false
}

func spanStringAttribute(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func findAttemptSpan(spans []tracetest.SpanStub, id trace.SpanID) tracetest.SpanStub {
	for _, span := range spans {
		if span.SpanContext.SpanID() == id {
			return span
		}
	}
	return tracetest.SpanStub{}
}
