package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type CallRecorder interface {
	StartCall(stage, model, reasoning string) func(outcome string)
}

type HostCallRecorder interface {
	StartHostCall(stage, scheme, host string) func(string)
}

type callURLRecorder interface {
	StartCallAt(stage, model, reasoning, rawURL string) func(string)
}

type timedCallRecorder interface {
	StartCallAtTimed(stage, model, reasoning, rawURL string) func(string, time.Time)
}

type contextualCallRecorder interface {
	StartCallAtContext(context.Context, string, string, string, string) func(string, string, time.Time)
}

type recorderKey struct{}
type stageKey struct{}
type hostRecorderKey struct{}
type callPurposeKey struct{}
type callAttemptIndexKey struct{}
type callIdentityRecorderKey struct{}

// CallIdentityRecorder records the model/profile that completed one call.
// Implementations must be safe for concurrent Query expansion requests.
type CallIdentityRecorder interface {
	RecordCallIdentity(context.Context, ModelIdentity, ProfileIdentity)
}

func WithCallIdentityRecorder(ctx context.Context, recorder CallIdentityRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, callIdentityRecorderKey{}, recorder)
}

// RecordCallIdentity reports a successful provider call to an optional request
// recorder. Failed quota attempts do not call this function.
func RecordCallIdentity(ctx context.Context, model ModelIdentity, profile ProfileIdentity) {
	if recorder, ok := ctx.Value(callIdentityRecorderKey{}).(CallIdentityRecorder); ok && recorder != nil {
		recorder.RecordCallIdentity(ctx, model, profile)
	}
}

type ContextHostCallRecorder interface {
	StartHostCallContext(context.Context, string, string, string) func(string)
}

func WithCallRecorder(ctx context.Context, recorder CallRecorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, recorder)
}

func WithHostCallRecorder(ctx context.Context, recorder HostCallRecorder) context.Context {
	return context.WithValue(ctx, hostRecorderKey{}, recorder)
}

func HostCallRecorderFromContext(ctx context.Context) HostCallRecorder {
	recorder, _ := ctx.Value(hostRecorderKey{}).(HostCallRecorder)
	return recorder
}

func WithCallStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, stageKey{}, stage)
}

func CallStage(ctx context.Context) string {
	stage, _ := ctx.Value(stageKey{}).(string)
	return stage
}

func WithCallPurpose(ctx context.Context, purpose string) context.Context {
	switch purpose {
	case "expansion", "evidence_answer", "model_prior":
		return context.WithValue(ctx, callPurposeKey{}, purpose)
	default:
		return ctx
	}
}

func CallPurpose(ctx context.Context) string {
	purpose, _ := ctx.Value(callPurposeKey{}).(string)
	return purpose
}

func WithCallAttemptIndex(ctx context.Context, index int) context.Context {
	if index < 0 {
		return ctx
	}
	return context.WithValue(ctx, callAttemptIndexKey{}, index)
}

func CallAttemptIndex(ctx context.Context) (int, bool) {
	index, ok := ctx.Value(callAttemptIndexKey{}).(int)
	return index, ok
}

// Client calls the DeepSeek API (OpenAI-compatible endpoint).
type Client struct {
	apiKey      string
	baseURL     string
	client      *http.Client
	model       string
	temperature *float64
	reasoning   Reasoning
}

// ModelIdentity is the sanitized model identity used for sealed runtime
// validation. It intentionally excludes credentials and endpoint details.
type ModelIdentity struct {
	Provider    string
	Model       string
	Reasoning   string
	Temperature float64
}

// ProjectRoleClients keeps one Project operation on a single immutable Profile
// snapshot while allowing its Fast and Heavy calls to select distinct models.
type ProjectRoleClients struct {
	Fast  ProjectChatClient
	Heavy ProjectChatClient
}

type ModelIdentityProvider interface {
	ModelIdentity() (ModelIdentity, bool)
}

type ProjectChatClient interface {
	Chat(context.Context, string, string) (string, error)
	ModelIdentityProvider
}

type projectChatClientContextKey struct{}
type projectRoleClientsContextKey struct{}

func WithProjectChatClient(ctx context.Context, client ProjectChatClient) context.Context {
	return context.WithValue(ctx, projectChatClientContextKey{}, client)
}

func ProjectChatClientFromContext(ctx context.Context) (ProjectChatClient, bool) {
	client, ok := ctx.Value(projectChatClientContextKey{}).(ProjectChatClient)
	return client, ok && client != nil
}

func WithProjectRoleClients(ctx context.Context, clients ProjectRoleClients) context.Context {
	return context.WithValue(ctx, projectRoleClientsContextKey{}, clients)
}

func ProjectRoleClientsFromContext(ctx context.Context) (ProjectRoleClients, bool) {
	clients, ok := ctx.Value(projectRoleClientsContextKey{}).(ProjectRoleClients)
	return clients, ok && clients.Fast != nil && clients.Heavy != nil
}

func ProjectChatClientForRole(ctx context.Context, role ProfileRole) (ProjectChatClient, bool) {
	if clients, ok := ProjectRoleClientsFromContext(ctx); ok {
		switch role {
		case ProfileRoleFast:
			return clients.Fast, true
		case ProfileRoleHeavy:
			return clients.Heavy, true
		default:
			return nil, false
		}
	}
	return ProjectChatClientFromContext(ctx)
}

func (c *Client) ModelIdentity() (ModelIdentity, bool) {
	if c == nil {
		return ModelIdentity{}, false
	}
	temperature := 0.0
	if c.temperature != nil {
		temperature = *c.temperature
	}
	return ModelIdentity{Provider: "deepseek", Model: c.model, Reasoning: string(c.reasoning), Temperature: temperature}, true
}

func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}
func (c *Client) Reasoning() Reasoning {
	if c == nil {
		return ""
	}
	return c.reasoning
}

const maxChatResponseBytes = 64 * 1024

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Temperature     *float64      `json:"temperature,omitempty"`
	Thinking        thinking      `json:"thinking"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type thinking struct {
	Type string `json:"type"`
}

type ClientOptions struct {
	Model                 string
	Temperature           *float64
	Reasoning             Reasoning
	BaseURL               string
	RequestTimeoutSeconds int
}

type Reasoning string

const (
	ReasoningNone Reasoning = "none"
	ReasoningLow  Reasoning = "low"
	ReasoningHigh Reasoning = "high"
	ReasoningMax  Reasoning = "max"
)

func (r Reasoning) Valid() bool {
	return r == ReasoningNone || r == ReasoningLow || r == ReasoningHigh || r == ReasoningMax
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatResponse struct {
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
}

// CanonicalDeepSeekModel accepts the migration aliases and rejects unknown models.
func CanonicalDeepSeekModel(model string) string {
	switch model {
	case "", "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat", "deepseek-reasoner":
		return "deepseek-flash"
	default:
		return ""
	}
}

// NewClient creates a DeepSeek API client. If apiKey is empty, returns nil.
func NewClient(apiKey string) *Client {
	return NewClientWithOptions(apiKey, ClientOptions{Model: "deepseek-flash"})
}

func NewClientWithOptions(apiKey string, options ClientOptions) *Client {
	if apiKey == "" {
		return nil
	}
	options.Model = CanonicalDeepSeekModel(options.Model)
	if options.Model == "" {
		return nil
	}
	if options.Reasoning == "" {
		options.Reasoning = ReasoningNone
	}
	if !options.Reasoning.Valid() {
		return nil
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	timeout := options.RequestTimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	return &Client{
		apiKey:      apiKey,
		baseURL:     baseURL,
		client:      &http.Client{Timeout: time.Duration(timeout) * time.Second},
		model:       options.Model,
		temperature: options.Temperature,
		reasoning:   options.Reasoning,
	}
}

// Chat sends a system + user message and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	text, _, err := c.ChatWithMetadata(ctx, systemPrompt, userMessage)
	return text, err
}

// ChatWithMetadata returns the assistant reply and the model identifier
// reported by the provider in this response. An omitted model stays empty;
// callers that require returned provenance must reject it explicitly.
func (c *Client) ChatWithMetadata(ctx context.Context, systemPrompt, userMessage string) (string, string, error) {
	var closeCall func(string)
	var closeCallAt func(string, time.Time)
	var closeContextCall func(string, string, time.Time)
	finish := func(outcome string) {
		if closeContextCall != nil {
			finish := closeContextCall
			closeContextCall = nil
			finish(outcome, outcome, time.Now())
			return
		}
		if closeCallAt != nil {
			f := closeCallAt
			closeCallAt = nil
			f(outcome, time.Now())
			return
		}
		if closeCall != nil {
			f := closeCall
			closeCall = nil
			f(outcome)
		}
	}
	finishObserved := func(receiptOutcome, transportOutcome string, at time.Time) {
		if closeContextCall != nil {
			f := closeContextCall
			closeContextCall = nil
			f(receiptOutcome, transportOutcome, at)
			return
		}
		if closeCallAt != nil {
			f := closeCallAt
			closeCallAt = nil
			f(receiptOutcome, at)
			return
		}
		finish(receiptOutcome)
	}
	defer func() { finish("success") }()
	body := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		Temperature: c.temperature,
		Thinking: thinking{Type: func() string {
			if c.reasoning == ReasoningNone {
				return "disabled"
			}
			return "enabled"
		}()},
	}
	if c.reasoning != ReasoningNone {
		body.ReasoningEffort = string(c.reasoning)
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if recorder, ok := ctx.Value(recorderKey{}).(CallRecorder); ok {
		stage := CallStage(ctx)
		if contextual, ok := recorder.(contextualCallRecorder); ok {
			closeContextCall = contextual.StartCallAtContext(ctx, stage, c.model, string(c.reasoning), req.URL.String())
		} else if timed, ok := recorder.(timedCallRecorder); ok {
			closeCallAt = timed.StartCallAtTimed(stage, c.model, string(c.reasoning), req.URL.String())
		} else if withURL, ok := recorder.(callURLRecorder); ok {
			closeCall = withURL.StartCallAt(stage, c.model, string(c.reasoning), req.URL.String())
		} else {
			closeCall = recorder.StartCall(stage, c.model, string(c.reasoning))
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		finish(callOutcome(ctx, err))
		return "", "", fmt.Errorf("api call: %w", &ProviderCallError{Err: err})
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, maxChatResponseBytes+1))
	networkFinished := time.Now()
	if err != nil {
		finishObserved(callOutcome(ctx, err), callOutcome(ctx, err), networkFinished)
		return "", "", fmt.Errorf("read response: %w", &ProviderCallError{Err: err})
	}
	if len(respData) > maxChatResponseBytes {
		transportOutcome := "success"
		if resp.StatusCode != http.StatusOK {
			transportOutcome = "provider_error"
		}
		finishObserved("decode_error", transportOutcome, networkFinished)
		return "", "", &ProviderCallError{Err: errors.New("provider response exceeds size limit")}
	}

	if resp.StatusCode != 200 {
		finishObserved("provider_error", "provider_error", networkFinished)
		return "", "", &ProviderCallError{Err: &HTTPStatusError{StatusCode: resp.StatusCode}}
	}

	var cr chatResponse
	if err := json.Unmarshal(respData, &cr); err != nil {
		finishObserved("decode_error", "success", networkFinished)
		return "", "", fmt.Errorf("unmarshal: %w", &ProviderCallError{Err: err})
	}
	if len(cr.Choices) == 0 {
		finishObserved("decode_error", "success", networkFinished)
		return "", "", &ProviderCallError{Err: errors.New("provider response has no choices")}
	}
	finishObserved("success", "success", networkFinished)
	modelIdentity, _ := c.ModelIdentity()
	RecordCallIdentity(ctx, modelIdentity, ProfileIdentity{})

	return cr.Choices[0].Message.Content, cr.Model, nil
}

func callOutcome(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "provider_error"
}

// HTTPStatusError retains only the status, never response bodies or endpoint credentials.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("api error %d", e.StatusCode) }

// ProviderCallError marks errors that came from an actual outbound provider call.
type ProviderCallError struct{ Err error }

func (e *ProviderCallError) Error() string { return "provider call failed" }
func (e *ProviderCallError) Unwrap() error { return e.Err }

// SafeErrorCategory is suitable for durable experiment receipts.
func SafeErrorCategory(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var profileErr *ProfileCallError
	if errors.As(err, &profileErr) {
		return string(profileErr.Kind)
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch {
		case status.StatusCode == 401 || status.StatusCode == 403:
			return "provider_auth_rejected"
		case status.StatusCode == 429:
			return "provider_rate_limited"
		case status.StatusCode >= 500:
			return "provider_server_error"
		default:
			return "provider_http_rejected"
		}
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return "provider_response_invalid_json"
	}
	return "provider_transport_or_response_error"
}
