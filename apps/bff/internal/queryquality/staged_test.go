package queryquality

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
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
