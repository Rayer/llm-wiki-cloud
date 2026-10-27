package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func newPoolProfile(t *testing.T, id string, transport http.RoundTripper) *ProfileClient {
	t.Helper()
	profile, err := newProfileClient(ProfileConfig{ID: id, Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://provider.example/v1", Model: id + "-model"}, "key", ProfileClientOptions{}, true, transport)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func poolResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestProfilePoolAdvancesOnlyOnExplicitQuotaExhaustion(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	first := newPoolProfile(t, "first", profileRoundTripper(func(*http.Request) (*http.Response, error) {
		firstCalls.Add(1)
		return poolResponse(http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`), nil
	}))
	second := newPoolProfile(t, "second", profileRoundTripper(func(*http.Request) (*http.Response, error) {
		secondCalls.Add(1)
		return poolResponse(http.StatusOK, `{"model":"second-model","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`), nil
	}))
	pool, err := NewProfilePoolClient([]*ProfileClient{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	result, err := pool.Complete(context.Background(), "system", "input")
	if err != nil || result.Text != "ok" || result.Metadata.ProfileID != "second" || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=(%d,%d)", result, err, firstCalls.Load(), secondCalls.Load())
	}
}

func TestProfilePoolDoesNotTreatGenericRateLimitAsQuota(t *testing.T) {
	var secondCalls atomic.Int32
	first := newPoolProfile(t, "first", profileRoundTripper(func(*http.Request) (*http.Response, error) {
		return poolResponse(http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`), nil
	}))
	second := newPoolProfile(t, "second", profileRoundTripper(func(*http.Request) (*http.Response, error) {
		secondCalls.Add(1)
		return poolResponse(http.StatusOK, `{"model":"second-model","choices":[{"message":{"content":"unsafe replay"}}]}`), nil
	}))
	pool, err := NewProfilePoolClient([]*ProfileClient{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Complete(context.Background(), "system", "input"); err == nil || IsQuotaExhausted(err) || secondCalls.Load() != 0 {
		t.Fatalf("generic rate-limit error=%v second calls=%d", err, secondCalls.Load())
	}
}
