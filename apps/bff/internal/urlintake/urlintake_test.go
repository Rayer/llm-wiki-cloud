package urlintake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/llm"
)

type profileJevTestClient struct {
	answer        string
	system, input string
}

func (c *profileJevTestClient) Chat(_ context.Context, system, input string) (string, error) {
	c.system, c.input = system, input
	return c.answer, nil
}
func (*profileJevTestClient) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: "custom", Model: "fixture-model"}, true
}

func TestJevEvaluatorUsesResolvedProjectProfileWhenPresent(t *testing.T) {
	client := &profileJevTestClient{answer: `{"access_shell":false,"readable_content":true}`}
	ctx := llm.WithProjectChatClient(context.Background(), client)
	evaluation, err := NewJevEvaluator("").Evaluate(ctx, QualityInput{URL: "https://example.test/page", Title: "Page", Markdown: "Readable text", Provider: "test"})
	if err != nil || evaluation.ShellProbability != 0 || evaluation.ContentProbability != 1 || evaluation.ReturnedModel != "fixture-model" {
		t.Fatalf("evaluation=%+v err=%v", evaluation, err)
	}
	if client.system == "" || !strings.Contains(client.input, "Readable text") || !strings.Contains(client.system, "untrusted data") {
		t.Fatalf("Project profile did not receive the bounded quality task: system=%q input=%q", client.system, client.input)
	}
}

type resolverStub []netip.Addr

func (r resolverStub) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r, nil
}

func TestValidatePublicURLRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name string
		url  string
		ips  resolverStub
	}{
		{name: "private literal", url: "http://127.0.0.1/"},
		{name: "credentials", url: "https://user:pass@example.com/", ips: resolverStub{netip.MustParseAddr("93.184.216.34")}},
		{name: "credential query", url: "https://example.com/?access_token=x", ips: resolverStub{netip.MustParseAddr("93.184.216.34")}},
		{name: "mixed public private DNS", url: "https://example.com/", ips: resolverStub{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.1.2.3")}},
		{name: "non-http scheme", url: "file:///etc/passwd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidatePublicURL(context.Background(), tc.url, tc.ips); err == nil {
				t.Fatal("ValidatePublicURL unexpectedly accepted unsafe target")
			}
		})
	}
}

func TestNormalizeURLPreservesContentIdentity(t *testing.T) {
	parsed, err := ValidatePublicURL(context.Background(), "HTTPS://WWW.Example.COM:443/Path/?q=one&utm_source=mail&b=%2F#route", resolverStub{netip.MustParseAddr("93.184.216.34")})
	if err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeURL(parsed)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://www.example.com/Path/?q=one&b=%2F#route"
	if got != want {
		t.Fatalf("NormalizeURL() = %q, want %q", got, want)
	}
}

func TestDeepSeekFetcherUsesDocumentedRequestAndCapturesExplicitTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fetch" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-test-key" {
			t.Fatal("provider authorization header missing")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["url"] != "https://example.com/article" {
			t.Fatalf("provider request body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"A public page","content":"# A public page\n\nReadable text.","truncated":true}`))
	}))
	defer server.Close()
	fetcher := NewDeepSeekFetcher("provider-test-key")
	fetcher.Endpoint = server.URL + "/fetch"
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	page, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "A public page" || page.Markdown != "# A public page\n\nReadable text." || page.Truncated == nil || !*page.Truncated {
		t.Fatalf("unexpected provider page: %#v", page)
	}
}

func TestDeepSeekFetcherSurfacesRateLimitWithoutRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	fetcher := NewDeepSeekFetcher("provider-test-key")
	fetcher.Endpoint = server.URL
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	failure, ok := err.(*ProviderFailure)
	if !ok || failure.Kind != FailureRateLimited || failure.RetryAfter.Seconds() != 12 {
		t.Fatalf("Fetch() error = %#v, want rate limit with Retry-After", err)
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want one bounded request", requests)
	}
}

func TestDeepSeekFetcherClassifiesProviderStatusWithoutRetainingErrorBody(t *testing.T) {
	for _, test := range []struct {
		status   int
		body     string
		category FailureCategory
	}{
		{http.StatusUnauthorized, `{"error":{"type":"authentication_error","code":"KEY_CANARY","message":"BODY_CANARY"}}`, FailureCategoryAuthentication},
		{http.StatusPaymentRequired, `{"error":{"type":"billing_error","code":"BALANCE_CANARY"}}`, FailureCategoryBalance},
		{http.StatusForbidden, `{"error":{"type":"security_error"}}`, FailureCategorySecurityPolicy},
		{http.StatusNotFound, `{"error":{"type":"content_error"}}`, FailureCategoryContentUnavailable},
		{http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`, FailureCategoryRateLimited},
		{http.StatusInternalServerError, `{"error":{"type":"server_error"}}`, FailureCategoryProviderError},
		{http.StatusGatewayTimeout, `{"error":{"type":"timeout"}}`, FailureCategoryProviderTimeout},
		{http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"invalid_request_error"}}`, FailureCategoryInvalidRequest},
		{http.StatusTeapot, `{"error":{"type":"UNKNOWN_CANARY"}}`, FailureCategoryUnexpectedStatus},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			fetcher := NewDeepSeekFetcher("API_KEY_CANARY")
			fetcher.Endpoint = server.URL
			fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
			_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
			var failure *ProviderFailure
			if !errors.As(err, &failure) || failure.StatusCode != test.status || failure.Category != test.category {
				t.Fatalf("Fetch() failure = %#v, want status=%d category=%s", err, test.status, test.category)
			}
			if strings.Contains(failure.Error(), "CANARY") || strings.Contains(failure.Error(), "BODY_CANARY") {
				t.Fatalf("provider error body escaped safe error: %q", failure.Error())
			}
		})
	}
}

type countingReadCloser struct {
	reader io.Reader
	read   int
}

func (body *countingReadCloser) Read(data []byte) (int, error) {
	count, err := body.reader.Read(data)
	body.read += count
	return count, err
}

func (*countingReadCloser) Close() error { return nil }

type urlintakeRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip urlintakeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestDeepSeekFetcherBoundsProviderErrorBodyRead(t *testing.T) {
	body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("x", maxProviderErrorBytes*4))}
	fetcher := NewDeepSeekFetcher("API_KEY_CANARY")
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	fetcher.Client = &http.Client{Transport: urlintakeRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: body, Request: request}, nil
	})}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure.Category != FailureCategoryUnexpectedStatus || body.read != maxProviderErrorBytes+1 {
		t.Fatalf("provider error bytes read=%d err=%v, want a read capped at %d", body.read, err, maxProviderErrorBytes+1)
	}
}

func TestFetchFailureDiagnosticPreservesWrappedCauseAndUnknowns(t *testing.T) {
	cause := context.DeadlineExceeded
	failure := &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport, Cause: cause}
	wrapped := fmt.Errorf("outer fetch context: %w", failure)
	var got *ProviderFailure
	if !errors.As(wrapped, &got) || got != failure || !errors.Is(wrapped, cause) {
		t.Fatalf("wrapped provider failure lost errors.As/Is: %v", wrapped)
	}
	result := failureResult(wrapped)
	if result.FailureDiagnostic == nil || result.FailureDiagnostic.Dependency != DeepSeekFetchProvider ||
		result.FailureDiagnostic.Category != FailureCategoryTransportTimeout || result.FailureDiagnostic.UpstreamStatus != 0 {
		t.Fatalf("timeout diagnostic = %#v", result.FailureDiagnostic)
	}
	unknown := failureResult(errors.New("PRIVATE_CAUSE_CANARY"))
	if unknown.FailureDiagnostic == nil || unknown.FailureDiagnostic.Dependency != "unknown" ||
		unknown.FailureDiagnostic.Category != FailureCategoryUnknown || strings.Contains(unknown.Message, "PRIVATE_CAUSE_CANARY") {
		t.Fatalf("unknown failure diagnostic = %#v", unknown)
	}
}

func TestDeepSeekFetcherBoundsRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"title":"late","content":"late"}`))
	}))
	defer server.Close()
	fetcher := NewDeepSeekFetcher("provider-test-key")
	fetcher.Endpoint = server.URL
	fetcher.Client = &http.Client{Timeout: 10 * time.Millisecond}
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	failure, ok := err.(*ProviderFailure)
	if !ok || failure.Kind != FailureTransport {
		t.Fatalf("Fetch() error = %#v, want bounded transport failure", err)
	}
	if diagnostic := fetchFailureDiagnostic(err, failure); diagnostic.Category != FailureCategoryTransportTimeout {
		t.Fatalf("timeout diagnostic = %#v", diagnostic)
	}
}

func TestDeepSeekFetcherClassifiesMalformedAndOversizedResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body func() string
		kind FailureKind
	}{
		{name: "malformed", body: func() string { return `{"title":` }, kind: FailureResponse},
		{name: "oversized", body: func() string { return strings.Repeat("x", MaxCandidateBytes+64*1024+1) }, kind: FailureTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body())
			}))
			defer server.Close()
			fetcher := NewDeepSeekFetcher("API_KEY_CANARY")
			fetcher.Endpoint = server.URL
			fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
			_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
			var failure *ProviderFailure
			if !errors.As(err, &failure) || failure.Kind != test.kind {
				t.Fatalf("Fetch() error = %#v, want kind=%s", err, test.kind)
			}
			diagnostic := fetchFailureDiagnostic(err, failure)
			want := FailureCategoryInvalidResponse
			if test.kind == FailureTooLarge {
				want = FailureCategoryContentTooLarge
			}
			if diagnostic.Category != want || diagnostic.UpstreamStatus != http.StatusOK {
				t.Fatalf("Fetch() diagnostic = %#v, want category=%s upstream_status=%d", diagnostic, want, http.StatusOK)
			}
		})
	}
}

func TestDeepSeekFetcherNotConfiguredMakesNoProviderCall(t *testing.T) {
	calls := 0
	fetcher := NewDeepSeekFetcher("")
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	fetcher.Client = &http.Client{Transport: urlintakeRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected provider call")
	})}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure.Kind != FailureNotConfigured || calls != 0 {
		t.Fatalf("Fetch() error=%#v calls=%d, want not configured with no provider call", err, calls)
	}
	result := failureResult(err)
	if result.FailureDiagnostic == nil || result.FailureDiagnostic.Category != FailureCategoryNotConfigured ||
		!strings.Contains(result.Message, "no provider request was made") {
		t.Fatalf("not-configured result = %#v", result)
	}
}

func TestDeepSeekFetcherPreservesContextCancellation(t *testing.T) {
	entered := make(chan struct{})
	fetcher := NewDeepSeekFetcher("API_KEY_CANARY")
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	fetcher.Client = &http.Client{Transport: urlintakeRoundTripper(func(request *http.Request) (*http.Response, error) {
		close(entered)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fetcher.Fetch(ctx, "https://example.com/article")
		done <- err
	}()
	<-entered
	cancel()
	err := <-done
	var failure *ProviderFailure
	if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch() error = %#v, want preserved context cancellation", err)
	}
	if diagnostic := fetchFailureDiagnostic(err, failure); diagnostic.Category != FailureCategoryTransportCancelled {
		t.Fatalf("cancel diagnostic = %#v", diagnostic)
	}
}

func TestDeepSeekFetcherDoesNotFollowProviderRedirect(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "http://127.0.0.1:1/private", http.StatusFound)
	}))
	defer server.Close()
	fetcher := NewDeepSeekFetcher("provider-test-key")
	fetcher.Endpoint = server.URL
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	failure, ok := err.(*ProviderFailure)
	if !ok || failure.StatusCode != http.StatusFound || requests != 1 {
		t.Fatalf("Fetch() error=%#v requests=%d, want one un-followed redirect", err, requests)
	}
}

func TestJevEvaluatorSendsTwoIndependentBoundedQuestions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer quality-test-key" {
			t.Fatal("quality provider authorization header missing")
		}
		var payload struct {
			Model     string       `json:"model"`
			State     QualityInput `json:"state"`
			Questions map[string]struct {
				Type         string            `json:"type"`
				Instructions string            `json:"instructions"`
				Criteria     map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "jev-latest" || len(payload.Questions) != 2 || payload.State.Markdown != "full supplied body" {
			t.Fatalf("unexpected Jev request: %#v", payload)
		}
		for _, key := range []string{"access_shell", "readable_content"} {
			if q, ok := payload.Questions[key]; !ok || q.Type != "noul" || q.Instructions == "" {
				t.Fatalf("missing independent question %q", key)
			}
		}
		if strings.Contains(payload.Questions["readable_content"].Instructions, "consistent with the supplied title") || strings.Contains(payload.Questions["readable_content"].Criteria["false"], "unrelated") {
			t.Fatal("ordinary Web quality assessment picked up the strict MCP title/body rule")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.14.0","answers":{"access_shell":{"type":"noul","noul":0.02},"readable_content":{"type":"noul","noul":0.97}},"usage":{"input_tokens":800,"output_tokens":9}}`))
	}))
	defer server.Close()
	evaluator := NewJevEvaluator("quality-test-key")
	evaluator.Endpoint = server.URL
	got, err := evaluator.Evaluate(context.Background(), QualityInput{
		URL: "https://example.com/article", Title: "Page", Markdown: "full supplied body", Provider: DeepSeekFetchProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PromptVersion != JevPromptVersion || got.ReturnedModel != "jev-1.14.0" || got.Usage.InputTokens != 800 || got.Usage.OutputTokens != 9 || got.ShellProbability != 0.02 || got.ContentProbability != 0.97 {
		t.Fatalf("unexpected quality evaluation: %#v", got)
	}
}

func TestJevEvaluatorStrictAssessmentChecksTitleBodyConsistency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Questions map[string]struct {
				Instructions string            `json:"instructions"`
				Criteria     map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		question := payload.Questions["readable_content"]
		if !strings.Contains(question.Instructions, "consistent with the supplied title") || !strings.Contains(question.Instructions, "cut-off extraction") || !strings.Contains(question.Criteria["false"], "unrelated") || !strings.Contains(question.Criteria["false"], "visibly ends") || !strings.Contains(question.Criteria["true"], "matches the title") {
			t.Fatalf("strict readable-content question = %#v", question)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.14.0","answers":{"access_shell":{"type":"noul","noul":0.01},"readable_content":{"type":"noul","noul":0.98}},"usage":{"input_tokens":800,"output_tokens":9}}`))
	}))
	defer server.Close()
	evaluator := NewJevEvaluator("quality-test-key")
	evaluator.Endpoint = server.URL
	_, err := evaluator.Evaluate(context.Background(), QualityInput{
		URL: "https://example.com/article", Title: "Page", Markdown: "full supplied body", Provider: DeepSeekFetchProvider,
		RequireTitleBodyConsistency: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJevEvaluatorReportsSafeHTTPFailureMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("sensitive provider response body"))
	}))
	defer server.Close()
	evaluator := NewJevEvaluator("quality-test-key")
	evaluator.Endpoint = server.URL
	_, err := evaluator.Evaluate(context.Background(), QualityInput{
		URL: "https://example.com/article", Title: "Page", Markdown: "body", Provider: DeepSeekFetchProvider,
	})
	failure, ok := err.(*QualityProviderFailure)
	if !ok || failure.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Evaluate() error = %#v, want sanitized HTTP status failure", err)
	}
	if strings.Contains(err.Error(), "sensitive provider response body") {
		t.Fatal("provider response body escaped in error")
	}
}

func TestJevEvaluatorMissingKeyIsTypedConfigurationFailure(t *testing.T) {
	evaluator := NewJevEvaluator("")
	_, err := evaluator.Evaluate(context.Background(), QualityInput{
		URL: "https://example.com/article", Title: "Page", Markdown: "body", Provider: DeepSeekFetchProvider,
	})
	failure, ok := err.(*QualityProviderFailure)
	if !ok || failure.Kind != "not configured" {
		t.Fatalf("Evaluate() error = %#v, want typed configuration failure", err)
	}
}

func TestJevEvaluatorClassifiesTimeoutAndInvalidResponses(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(40 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()
		evaluator := NewJevEvaluator("quality-test-key")
		evaluator.Endpoint = server.URL
		evaluator.Client.Timeout = 5 * time.Millisecond
		_, err := evaluator.Evaluate(context.Background(), QualityInput{
			URL: "https://example.com/article", Title: "Page", Markdown: "body", Provider: DeepSeekFetchProvider,
		})
		failure, ok := err.(*QualityProviderFailure)
		if !ok || failure.Kind != "timeout" {
			t.Fatalf("Evaluate() error = %#v, want typed timeout failure", err)
		}
	})

	t.Run("invalid response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"jev-unlisted-version","answers":{},"usage":{"input_tokens":0,"output_tokens":0},"private":"secret body"}`))
		}))
		defer server.Close()
		evaluator := NewJevEvaluator("quality-test-key")
		evaluator.Endpoint = server.URL
		_, err := evaluator.Evaluate(context.Background(), QualityInput{
			URL: "https://example.com/article", Title: "Page", Markdown: "body", Provider: DeepSeekFetchProvider,
		})
		failure, ok := err.(*QualityProviderFailure)
		if !ok || failure.Kind != "invalid_response" {
			t.Fatalf("Evaluate() error = %#v, want typed invalid-response failure", err)
		}
		if strings.Contains(err.Error(), "secret body") {
			t.Fatal("provider response body escaped in error")
		}
	})
}

func TestDecideQualityKeepsMixedResultsUncertain(t *testing.T) {
	thresholds := QualityThresholds{RejectShellAt: .9, RejectContentAtMost: .1, UsableShellAtMost: .1, UsableContentAt: .9}
	cases := []struct {
		name  string
		input QualityEvaluation
		want  QualityStatus
	}{
		{name: "invalid shell", input: QualityEvaluation{ShellProbability: .98, ContentProbability: .04}, want: QualityRejected},
		{name: "usable article", input: QualityEvaluation{ShellProbability: .02, ContentProbability: .97}, want: QualityUsable},
		{name: "mixed", input: QualityEvaluation{ShellProbability: .35, ContentProbability: .75}, want: QualityNeedsConfirmation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideQuality(tc.input, thresholds); got != tc.want {
				t.Fatalf("DecideQuality() = %q, want %q", got, tc.want)
			}
		})
	}
}
