package urlintake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

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
		if !strings.Contains(question.Instructions, "consistent with the supplied title") || !strings.Contains(question.Criteria["false"], "unrelated") || !strings.Contains(question.Criteria["true"], "matches the title") {
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
