package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type profileRoundTripper func(*http.Request) (*http.Response, error)

func (f profileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type profileCallCapture struct {
	provider, stage, model, endpoint, outcome string
	profile                                   ProfileConfig
}

func (c *profileCallCapture) StartProfileCallAtContext(_ context.Context, profile ProfileConfig, stage, endpoint string) func(string, string, time.Time) {
	c.provider, c.stage, c.model, c.endpoint = profile.Provider, stage, profile.Model, endpoint
	c.profile = profile
	return func(outcome, _ string, _ time.Time) { c.outcome = outcome }
}

type profileReadFailureBody struct {
	ctx           context.Context
	err           error
	waitForCancel bool
	started       chan<- struct{}
}

func (b *profileReadFailureBody) Read([]byte) (int, error) {
	if b.started != nil {
		close(b.started)
	}
	if b.waitForCancel {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return 0, b.err
}

func (*profileReadFailureBody) Close() error { return nil }

type profileBodyTimeoutError struct{}

func (profileBodyTimeoutError) Error() string   { return "synthetic response body timeout" }
func (profileBodyTimeoutError) Timeout() bool   { return true }
func (profileBodyTimeoutError) Temporary() bool { return true }

func TestProfileClientClassifiesResponseBodyTimeoutAsProviderTimeout(t *testing.T) {
	requestCtx := context.Background()
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &profileReadFailureBody{ctx: request.Context(), err: profileBodyTimeoutError{}},
			Request:    request,
		}, nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p-timeout", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	capture := &profileCallCapture{}
	ctx := context.WithValue(requestCtx, recorderKey{}, capture)
	_, err = client.Complete(ctx, "rules", "question")
	var callErr *ProfileCallError
	if !errors.As(err, &callErr) || callErr.Kind != ProviderTimeout {
		t.Fatalf("Complete() error = %v, want typed timeout after HTTP 200 headers", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller context ended during provider timeout: %v", ctx.Err())
	}
	if capture.outcome != string(ProviderTimeout) {
		t.Fatalf("recorded provider outcome = %q, want %q", capture.outcome, ProviderTimeout)
	}
}

func TestProfileClientKeepsNonTimeoutResponseBodyReadAsInvalidResponse(t *testing.T) {
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &profileReadFailureBody{ctx: request.Context(), err: errors.New("synthetic body read failure")},
			Request:    request,
		}, nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p-invalid-body", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Complete(context.Background(), "rules", "question")
	var callErr *ProfileCallError
	if !errors.As(err, &callErr) || callErr.Kind != ProviderResponseInvalid {
		t.Fatalf("Complete() error = %v, want invalid response for a non-timeout body read error", err)
	}
	if strings.Contains(err.Error(), "synthetic body read failure") {
		t.Fatalf("body reader detail leaked in safe error: %v", err)
	}
}

func TestProfileClientKeepsCallerCancellationTerminalDuringResponseBodyRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &profileReadFailureBody{ctx: request.Context(), waitForCancel: true, started: started},
			Request:    request,
		}, nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p-canceled", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	capture := &profileCallCapture{}
	ctx = context.WithValue(ctx, recorderKey{}, capture)
	type completion struct {
		result CompletionResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, completeErr := client.Complete(ctx, "rules", "question")
		done <- completion{result: result, err: completeErr}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP 200 body was not read")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != context.Canceled || got.result.Text != "" {
			t.Fatalf("Complete() = (%+v, %v), want empty result and terminal caller cancellation", got.result, got.err)
		}
		if capture.outcome != "canceled" {
			t.Fatalf("recorded provider outcome = %q, want canceled", capture.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete() did not stop after caller cancellation")
	}
}

func TestProfileClientUsesOpenAIChatCompletionsProtocol(t *testing.T) {
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://fixture.example/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Fatalf("unexpected request target/auth: %s %q", request.URL, request.Header.Get("Authorization"))
		}
		if request.Header.Get("x-api-key") != "" || request.Header.Get("anthropic-version") != "" {
			t.Fatal("OpenAI protocol sent Anthropic headers")
		}
		var payload struct {
			Model               string        `json:"model"`
			Messages            []chatMessage `json:"messages"`
			Max                 int           `json:"max_tokens"`
			MaxCompletionTokens *int          `json:"max_completion_tokens"`
			Temperature         *float64      `json:"temperature"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "fixture-model" || payload.Max != 73 || payload.MaxCompletionTokens != nil || payload.Temperature != nil || len(payload.Messages) != 2 || payload.Messages[0] != (chatMessage{Role: "system", Content: "rules"}) || payload.Messages[1] != (chatMessage{Role: "user", Content: "question"}) {
			t.Fatalf("unexpected OpenAI payload: %+v", payload)
		}
		return profileResponse(http.StatusOK, `{"id":"req-1","model":"fixture-returned","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`), nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p1", Revision: 3, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions, Endpoint: "https://fixture.example/v1", Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{MaxOutputTokens: 73}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Complete(context.Background(), "rules", "question")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.Metadata.ProfileID != "p1" || result.Metadata.ProfileRevision != 3 || result.Metadata.ReturnedModel != "fixture-returned" || result.Metadata.FinishReason != "stop" || result.Metadata.ProviderRequestID != "req-1" || result.Metadata.Usage.InputTokens == nil || *result.Metadata.Usage.InputTokens != 12 || result.Metadata.Usage.CachedInputTokens == nil || *result.Metadata.Usage.CachedInputTokens != 3 || result.Metadata.Usage.ReasoningTokens == nil || *result.Metadata.Usage.ReasoningTokens != 2 {
		t.Fatalf("unexpected normalized result: %+v", result)
	}
}

func TestProfileModelDiscoveryUsesAuthenticatedReadOnlyAdapter(t *testing.T) {
	var calls int
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodGet || request.URL.String() != "https://fixture.example/v1/models" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Fatalf("discovery request method=%q url=%q authorization=%q", request.Method, request.URL, request.Header.Get("Authorization"))
		}
		return profileResponse(http.StatusOK, `{"object":"list","data":[{"id":"fast-a"},{"id":"heavy-b"},{"id":"fast-a"}]}`), nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p-discovery", Revision: 4, Role: ProfileRoleFast, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "lwc-discovery-only"}, "synthetic-key", ProfileClientOptions{}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	models, err := client.DiscoverModels(context.Background())
	if err != nil || strings.Join(models, ",") != "fast-a,heavy-b" || calls != 1 {
		t.Fatalf("models=%v calls=%d err=%v", models, calls, err)
	}
	client.discoveryOnly = true
	if _, err := client.Complete(context.Background(), "system", "message"); err == nil || calls != 1 {
		t.Fatalf("discovery-only client allowed generation: calls=%d err=%v", calls, err)
	}
}

func TestProfileModelDiscoveryClassifiesErrorsWithoutProviderBody(t *testing.T) {
	client, err := newProfileClient(ProfileConfig{ID: "p-discovery-error", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "lwc-discovery-only"}, "synthetic-key", ProfileClientOptions{}, false,
		profileRoundTripper(func(*http.Request) (*http.Response, error) {
			return profileResponse(http.StatusUnauthorized, `{"error":{"message":"synthetic-secret-detail"}}`), nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.DiscoverModels(context.Background())
	var callErr *ProfileCallError
	if !errors.As(err, &callErr) || callErr.Kind != ProviderAuthentication || strings.Contains(err.Error(), "synthetic-secret-detail") {
		t.Fatalf("discovery error=%v, want safe authentication category", err)
	}
}

func TestProfileClientUsesProviderSpecificCompletionTokenLimit(t *testing.T) {
	for _, test := range []struct {
		name        string
		provider    string
		endpoint    string
		wantField   string
		absentField string
	}{
		{name: "official OpenAI", provider: "openai", endpoint: "https://api.openai.com/v1", wantField: "max_completion_tokens", absentField: "max_tokens"},
		{name: "xAI", provider: "xai", endpoint: "https://api.x.ai/v1", wantField: "max_tokens", absentField: "max_completion_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != test.endpoint+"/chat/completions" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Fatalf("unexpected request target/auth: %s %q", request.URL, request.Header.Get("Authorization"))
				}
				if request.Header.Get("x-api-key") != "" || request.Header.Get("anthropic-version") != "" {
					t.Fatal("OpenAI-compatible provider sent Anthropic headers")
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				var model string
				var messages []chatMessage
				var tokenLimit int
				if json.Unmarshal(payload["model"], &model) != nil || model != "fixture-model" ||
					json.Unmarshal(payload["messages"], &messages) != nil || len(messages) != 2 || messages[0] != (chatMessage{Role: "system", Content: "rules"}) || messages[1] != (chatMessage{Role: "user", Content: "question"}) {
					t.Fatalf("unexpected model/messages in request: model=%q messages=%+v", model, messages)
				}
				if _, ok := payload[test.absentField]; ok {
					t.Fatalf("request included sibling token field %q", test.absentField)
				}
				if _, ok := payload["temperature"]; ok {
					t.Fatal("nil temperature was serialized")
				}
				if raw, ok := payload[test.wantField]; !ok || json.Unmarshal(raw, &tokenLimit) != nil || tokenLimit != 73 {
					t.Fatalf("request %s = %s, want budget 73", test.wantField, raw)
				}
				return profileResponse(http.StatusOK, `{"id":"req-provider-token","model":"fixture-returned","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`), nil
			})
			client, err := newProfileClient(ProfileConfig{ID: "p-token", Revision: 1, Provider: test.provider, Protocol: ProtocolOpenAIChatCompletions,
				Endpoint: test.endpoint, Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{MaxOutputTokens: 73}, false, transport)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.Complete(context.Background(), "rules", "question")
			if err != nil || result.Text != "answer" {
				t.Fatalf("Complete() = (%+v, %v), want answer", result, err)
			}
		})
	}
}

func TestProfileClientRecordsItsProviderAndModel(t *testing.T) {
	transport := profileRoundTripper(func(*http.Request) (*http.Response, error) {
		return profileResponse(http.StatusOK, `{"id":"req-1","model":"fixture-model","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`), nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p1", Revision: 3, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions, Endpoint: "https://fixture.example/v1", Model: "fixture-model"}, "", ProfileClientOptions{}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	capture := &profileCallCapture{}
	ctx := WithCallStage(context.Background(), "query_expansion")
	ctx = context.WithValue(ctx, recorderKey{}, capture)
	if _, err := client.Complete(ctx, "rules", "question"); err != nil {
		t.Fatal(err)
	}
	if capture.provider != "custom" || capture.stage != "query_expansion" || capture.model != "fixture-model" || capture.endpoint != "https://fixture.example/v1/chat/completions" || capture.outcome != "success" || capture.profile.ID != "p1" || capture.profile.Revision != 3 || capture.profile.Protocol != ProtocolOpenAIChatCompletions {
		t.Fatalf("recorded profile call = %+v", capture)
	}
}

func TestProfileClientUsesAnthropicMessagesProtocol(t *testing.T) {
	transport := profileRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.anthropic.com/v1/messages" || request.Header.Get("x-api-key") != "synthetic-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("unexpected Anthropic request: %s headers=%v", request.URL, request.Header)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("Anthropic protocol sent Bearer authorization")
		}
		var payload struct {
			Model               string        `json:"model"`
			MaxTokens           int           `json:"max_tokens"`
			MaxCompletionTokens *int          `json:"max_completion_tokens"`
			System              string        `json:"system"`
			Messages            []chatMessage `json:"messages"`
			Temperature         *float64      `json:"temperature"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "claude-fixture" || payload.MaxTokens != 99 || payload.MaxCompletionTokens != nil || payload.System != "system rules" || payload.Temperature != nil || len(payload.Messages) != 1 || payload.Messages[0] != (chatMessage{Role: "user", Content: "question"}) {
			t.Fatalf("unexpected Anthropic payload: %+v", payload)
		}
		return profileResponse(http.StatusOK, `{"id":"msg-1","model":"claude-returned","stop_reason":"end_turn","content":[{"type":"thinking","text":"hidden"},{"type":"text","text":"one "},{"type":"text","text":"two"}],"usage":{"input_tokens":21,"output_tokens":8,"cache_read_input_tokens":6,"cache_creation_input_tokens":2}}`), nil
	})
	client, err := newProfileClient(ProfileConfig{ID: "p2", Revision: 1, Provider: "anthropic", Protocol: ProtocolAnthropicMessages, Endpoint: "https://api.anthropic.com", Model: "claude-fixture"}, "synthetic-key", ProfileClientOptions{MaxOutputTokens: 99}, false, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Complete(context.Background(), "system rules", "question")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "one two" || result.Metadata.ReturnedModel != "claude-returned" || result.Metadata.FinishReason != "end_turn" || result.Metadata.Usage.InputTokens == nil || *result.Metadata.Usage.InputTokens != 21 || result.Metadata.Usage.CachedInputTokens == nil || *result.Metadata.Usage.CachedInputTokens != 6 || result.Metadata.Usage.CacheCreationInputTokens == nil || *result.Metadata.Usage.CacheCreationInputTokens != 2 {
		t.Fatalf("unexpected normalized result: %+v", result)
	}
}

func TestProfileClientRejectsSyntacticallyValidTruncatedStructuredOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol Protocol
		endpoint string
		response string
	}{
		{
			name:     "OpenAI length",
			protocol: ProtocolOpenAIChatCompletions,
			endpoint: "https://fixture.example/v1",
			response: `{"id":"req-truncated","model":"fixture-model","choices":[{"message":{"role":"assistant","content":"{\"valid\":true}"},"finish_reason":"length"}]}`,
		},
		{
			name:     "Anthropic max_tokens",
			protocol: ProtocolAnthropicMessages,
			endpoint: "https://fixture.example",
			response: `{"id":"msg-truncated","model":"fixture-model","stop_reason":"max_tokens","content":[{"type":"text","text":"{\"valid\":true}"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := profileRoundTripper(func(*http.Request) (*http.Response, error) {
				return profileResponse(http.StatusOK, test.response), nil
			})
			client, err := newProfileClient(ProfileConfig{ID: "profile", Revision: 7, Provider: "custom", Protocol: test.protocol,
				Endpoint: test.endpoint, Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{}, false, transport)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			capture := &profileCallCapture{}
			ctx := context.WithValue(context.Background(), recorderKey{}, capture)
			text, err := client.Chat(ctx, "return JSON", `{"valid":true}`)
			var callErr *ProfileCallError
			if text != "" || !errors.As(err, &callErr) || callErr.Kind != ProviderOutputTruncated {
				t.Fatalf("Chat() = (%q, %v), want empty output and truncated error", text, err)
			}
			if capture.outcome != string(ProviderOutputTruncated) {
				t.Fatalf("call outcome = %q, want %q", capture.outcome, ProviderOutputTruncated)
			}
		})
	}
}

func TestProfileClientKeepsOpenAIParameterAndModelErrorsSafe(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		forbidden []string
	}{
		{name: "unsupported token field", status: http.StatusBadRequest, body: `{"error":{"code":"unsupported_parameter","param":"max_tokens"}}`, forbidden: []string{"unsupported_parameter", "max_tokens"}},
		{name: "model not found", status: http.StatusNotFound, body: `{"error":{"code":"model_not_found"}}`, forbidden: []string{"model_not_found"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := profileRoundTripper(func(*http.Request) (*http.Response, error) {
				return profileResponse(test.status, test.body), nil
			})
			client, err := newProfileClient(ProfileConfig{ID: "p-error", Revision: 1, Provider: "openai", Protocol: ProtocolOpenAIChatCompletions,
				Endpoint: "https://api.openai.com/v1", Model: "fixture-model"}, "synthetic-key", ProfileClientOptions{}, false, transport)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Complete(context.Background(), "rules", "question")
			var callErr *ProfileCallError
			if !errors.As(err, &callErr) || callErr.Kind != ProviderFailure || callErr.StatusCode != test.status || IsQuotaExhausted(err) {
				t.Fatalf("Complete() error = %v, want safe provider failure for HTTP %d", err, test.status)
			}
			for _, detail := range append(test.forbidden, "synthetic-key", "rules", "question") {
				if strings.Contains(err.Error(), detail) {
					t.Fatalf("provider detail leaked from safe error: %v", err)
				}
			}
		})
	}
}

func TestProfileClientOnlyFailsOverOnExplicitQuotaCode(t *testing.T) {
	for _, test := range []struct {
		name string
		code string
		want ProviderErrorKind
	}{
		{name: "explicit quota", code: `{"error":{"code":"insufficient_quota"}}`, want: ProviderQuotaExhausted},
		{name: "generic rate limit", code: `{"error":{"type":"rate_limit_error"}}`, want: ProviderRateLimited},
		{name: "anthropic billing", code: `{"error":{"type":"billing_error"}}`, want: ProviderBilling},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := profileRoundTripper(func(*http.Request) (*http.Response, error) {
				return profileResponse(http.StatusTooManyRequests, test.code), nil
			})
			client, err := newProfileClient(ProfileConfig{ID: "p", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions, Endpoint: "https://fixture.example/v1", Model: "m"}, "synthetic-key", ProfileClientOptions{}, false, transport)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Complete(context.Background(), "", "x")
			var callErr *ProfileCallError
			if !errors.As(err, &callErr) || callErr.Kind != test.want {
				t.Fatalf("error=%v want category %s", err, test.want)
			}
			if strings.Contains(err.Error(), test.code) || strings.Contains(err.Error(), "synthetic-key") {
				t.Fatalf("provider detail leaked: %v", err)
			}
			if IsQuotaExhausted(err) != (test.want == ProviderQuotaExhausted) {
				t.Fatalf("quota classification mismatch: %v", err)
			}
		})
	}
}

func TestProfileEndpointRejectsPrivateAndCredentialURLs(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/v1", "https://127.0.0.1/v1", "https://user:pass@example.com/v1", "https://169.254.169.254/latest/meta-data"} {
		if _, err := NewProfileClient(ProfileConfig{ID: "p", Revision: 1, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions, Endpoint: raw, Model: "m"}, "synthetic-key", ProfileClientOptions{}); err == nil {
			t.Errorf("endpoint accepted: %s", raw)
		}
	}
}

func profileResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
