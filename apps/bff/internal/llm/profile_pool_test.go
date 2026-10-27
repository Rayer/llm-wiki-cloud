package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

type recordedProfileCallIdentity struct {
	purpose string
	model   ModelIdentity
	profile ProfileIdentity
}

type profileCallIdentityCapture struct {
	mu    sync.Mutex
	calls []recordedProfileCallIdentity
}

func (c *profileCallIdentityCapture) RecordCallIdentity(ctx context.Context, model ModelIdentity, profile ProfileIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, recordedProfileCallIdentity{purpose: CallPurpose(ctx), model: model, profile: profile})
}

func (c *profileCallIdentityCapture) snapshot() []recordedProfileCallIdentity {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recordedProfileCallIdentity(nil), c.calls...)
}

func TestProfilePoolRecordsActualExpansionAAndSynthesisBTransportIdentity(t *testing.T) {
	var firstCalls, secondCalls int
	assertRequest := func(request *http.Request, wantModel string) {
		t.Helper()
		if request.URL.String() != "https://provider.example/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("unexpected provider request: %s auth=%q", request.URL, request.Header.Get("Authorization"))
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if payload.Model != wantModel {
			t.Fatalf("request model=%q, want %q", payload.Model, wantModel)
		}
	}
	first := newPoolProfile(t, "first", profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		firstCalls++
		assertRequest(request, "first-model")
		if firstCalls == 2 {
			return poolResponse(http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`), nil
		}
		return poolResponse(http.StatusOK, `{"model":"first-model","choices":[{"message":{"content":"expanded"},"finish_reason":"stop"}]}`), nil
	}))
	second := newPoolProfile(t, "second", profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		secondCalls++
		assertRequest(request, "second-model")
		return poolResponse(http.StatusOK, `{"model":"second-model","choices":[{"message":{"content":"synthesized"},"finish_reason":"stop"}]}`), nil
	}))
	pool, err := NewProfilePoolClient([]*ProfileClient{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	capture := &profileCallIdentityCapture{}
	ctx := WithCallIdentityRecorder(context.Background(), capture)
	expansion, err := pool.Chat(WithCallPurpose(ctx, "expansion"), "expand", "query")
	if err != nil || expansion != "expanded" {
		t.Fatalf("expansion Chat()=(%q,%v)", expansion, err)
	}
	synthesis, err := pool.Chat(WithCallPurpose(ctx, "evidence_answer"), "answer", "evidence")
	if err != nil || synthesis != "synthesized" {
		t.Fatalf("synthesis Chat()=(%q,%v)", synthesis, err)
	}
	if firstCalls != 2 || secondCalls != 1 {
		t.Fatalf("provider transport calls=(A:%d,B:%d), want A expansion + quota then B synthesis", firstCalls, secondCalls)
	}
	calls := capture.snapshot()
	if len(calls) != 2 || calls[0].purpose != "expansion" || calls[0].profile.ID != "first" || calls[0].model.Model != "first-model" ||
		calls[1].purpose != "evidence_answer" || calls[1].profile.ID != "second" || calls[1].model.Model != "second-model" {
		t.Fatalf("successful transport identities=%+v, want expansion A then synthesis B; failed quota attempt must not be recorded", calls)
	}
}
