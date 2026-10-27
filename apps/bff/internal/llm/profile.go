package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxProfileResponseBytes = 1 << 20

type profileContextualCallRecorder interface {
	StartProfileCallAtContext(context.Context, ProfileConfig, string, string) func(string, string, time.Time)
}

type Protocol string

const (
	ProtocolOpenAIChatCompletions Protocol = "openai_chat_completions"
	ProtocolAnthropicMessages     Protocol = "anthropic_messages"
)

// ProfileConfig contains the nonsecret, immutable identity used for one call.
type ProfileConfig struct {
	ID       string
	Revision int64
	Provider string
	Protocol Protocol
	Endpoint string
	Model    string
}

type ProfileClientOptions struct {
	Timeout         time.Duration
	MaxOutputTokens int
	Temperature     *float64
}

type Usage struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CachedInputTokens        *int64 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	ReasoningTokens          *int64 `json:"reasoning_tokens,omitempty"`
}

type CompletionMetadata struct {
	ProfileID         string `json:"profile_id"`
	ProfileRevision   int64  `json:"profile_revision"`
	Provider          string `json:"provider"`
	Protocol          string `json:"protocol"`
	RequestedModel    string `json:"requested_model"`
	ReturnedModel     string `json:"returned_model,omitempty"`
	FinishReason      string `json:"finish_reason,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`
	Usage             Usage  `json:"usage"`
}

type CompletionResult struct {
	Text     string             `json:"text"`
	Metadata CompletionMetadata `json:"metadata"`
}

type ProfileIdentity struct {
	ID       string
	Revision int64
	Provider string
	Protocol string
	Model    string
}

type ProfileIdentityProvider interface {
	ProfileIdentity() ProfileIdentity
}

type ProfileClient struct {
	profile ProfileConfig
	apiKey  string
	client  *http.Client
	maxBody int64
	maxOut  int
	temp    *float64
}

type ProviderErrorKind string

const (
	ProviderAuthentication  ProviderErrorKind = "provider_authentication"
	ProviderRateLimited     ProviderErrorKind = "provider_rate_limited"
	ProviderTimeout         ProviderErrorKind = "provider_timeout"
	ProviderBilling         ProviderErrorKind = "provider_billing"
	ProviderFailure         ProviderErrorKind = "provider_failure"
	ProviderQuotaExhausted  ProviderErrorKind = "quota_exhausted"
	ProviderResponseInvalid ProviderErrorKind = "provider_response_invalid"
	ProviderOutputTruncated ProviderErrorKind = "provider_output_truncated"
)

// ProfileCallError contains only a safe category and status, never provider bodies or keys.
type ProfileCallError struct {
	Kind       ProviderErrorKind
	StatusCode int
}

func (e *ProfileCallError) Error() string {
	if e == nil {
		return "provider call failed"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Kind, e.StatusCode)
	}
	return string(e.Kind)
}

func IsQuotaExhausted(err error) bool {
	var callErr *ProfileCallError
	return errors.As(err, &callErr) && callErr.Kind == ProviderQuotaExhausted
}

func NewProfileClient(profile ProfileConfig, apiKey string, options ProfileClientOptions) (*ProfileClient, error) {
	return newProfileClient(profile, apiKey, options, false, profileFixtureTransport())
}

// Test transport and loopback access stay package-private so product callers
// cannot bypass endpoint validation.
func newProfileClient(profile ProfileConfig, apiKey string, options ProfileClientOptions, allowLoopbackTest bool, testTransport http.RoundTripper) (*ProfileClient, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.Endpoint = strings.TrimSpace(profile.Endpoint)
	profile.Model = strings.TrimSpace(profile.Model)
	apiKey = strings.TrimSpace(apiKey)
	if profile.ID == "" || profile.Revision < 1 || profile.Model == "" || len(profile.Model) > 200 ||
		(apiKey == "" && profile.Provider != "custom") || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("invalid LLM profile configuration")
	}
	if profile.Protocol != ProtocolOpenAIChatCompletions && profile.Protocol != ProtocolAnthropicMessages {
		return nil, errors.New("unsupported LLM profile protocol")
	}
	switch profile.Provider {
	case "openai", "xai":
		if profile.Protocol != ProtocolOpenAIChatCompletions {
			return nil, errors.New("provider and protocol do not match")
		}
	case "anthropic":
		if profile.Protocol != ProtocolAnthropicMessages {
			return nil, errors.New("provider and protocol do not match")
		}
	case "custom":
	default:
		return nil, errors.New("unsupported LLM profile provider")
	}
	if profile.Provider == "openai" && profile.Endpoint != "https://api.openai.com/v1" {
		return nil, errors.New("invalid OpenAI endpoint")
	}
	if profile.Provider == "xai" && profile.Endpoint != "https://api.x.ai/v1" {
		return nil, errors.New("invalid xAI endpoint")
	}
	if profile.Provider == "anthropic" && profile.Endpoint != "https://api.anthropic.com" {
		return nil, errors.New("invalid Anthropic endpoint")
	}
	endpoint, err := validateEndpoint(profile.Endpoint, allowLoopbackTest)
	if err != nil {
		return nil, err
	}
	if options.MaxOutputTokens <= 0 {
		options.MaxOutputTokens = 4096
	}
	if options.MaxOutputTokens > 65536 {
		return nil, errors.New("invalid output token limit")
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > 2) {
		return nil, errors.New("invalid temperature")
	}
	if options.Timeout <= 0 {
		options.Timeout = 60 * time.Second
	}
	if options.Timeout > time.Hour {
		return nil, errors.New("invalid request timeout")
	}
	transport := testTransport
	if transport == nil {
		transport = safeProfileTransport(endpoint.Hostname(), allowLoopbackTest)
	}
	return &ProfileClient{
		profile: profile,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout:       options.Timeout,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxBody: maxProfileResponseBytes,
		maxOut:  options.MaxOutputTokens,
		temp:    options.Temperature,
	}, nil
}

func (c *ProfileClient) ModelIdentity() (ModelIdentity, bool) {
	if c == nil {
		return ModelIdentity{}, false
	}
	return ModelIdentity{Provider: c.profile.Provider, Model: c.profile.Model}, true
}

func (c *ProfileClient) ProfileIdentity() ProfileIdentity {
	if c == nil {
		return ProfileIdentity{}
	}
	return ProfileIdentity{ID: c.profile.ID, Revision: c.profile.Revision, Provider: c.profile.Provider, Protocol: string(c.profile.Protocol), Model: c.profile.Model}
}

func (c *ProfileClient) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	result, err := c.Complete(ctx, systemPrompt, userMessage)
	return result.Text, err
}

func (c *ProfileClient) ChatWithMetadata(ctx context.Context, systemPrompt, userMessage string) (string, string, error) {
	result, err := c.Complete(ctx, systemPrompt, userMessage)
	return result.Text, result.Metadata.ReturnedModel, err
}

func (c *ProfileClient) Complete(ctx context.Context, systemPrompt, userMessage string) (CompletionResult, error) {
	if c == nil || c.client == nil {
		return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
	}
	var body []byte
	var endpoint string
	if c.profile.Protocol == ProtocolAnthropicMessages {
		payload := struct {
			Model     string        `json:"model"`
			MaxTokens int           `json:"max_tokens"`
			System    string        `json:"system,omitempty"`
			Messages  []chatMessage `json:"messages"`
			Temp      *float64      `json:"temperature,omitempty"`
		}{c.profile.Model, c.maxOut, systemPrompt, []chatMessage{{Role: "user", Content: userMessage}}, c.temp}
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
		}
		endpoint = profileEndpoint(c.profile.Endpoint, ProtocolAnthropicMessages)
	} else {
		payload := struct {
			Model       string        `json:"model"`
			Messages    []chatMessage `json:"messages"`
			MaxTokens   int           `json:"max_tokens"`
			Temperature *float64      `json:"temperature,omitempty"`
		}{c.profile.Model, []chatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userMessage}}, c.maxOut, c.temp}
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
		}
		endpoint = profileEndpoint(c.profile.Endpoint, ProtocolOpenAIChatCompletions)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
	}
	request.Header.Set("Content-Type", "application/json")
	if c.profile.Protocol == ProtocolAnthropicMessages {
		request.Header.Set("x-api-key", c.apiKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	callOutcome := string(ProviderFailure)
	finishCall := func(string, string, time.Time) {}
	if recorder, ok := ctx.Value(recorderKey{}).(profileContextualCallRecorder); ok {
		finishCall = recorder.StartProfileCallAtContext(ctx, c.profile, CallStage(ctx), endpoint)
	}
	defer func() { finishCall(callOutcome, callOutcome, time.Now()) }()
	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			callOutcome = string(ProviderTimeout)
			return CompletionResult{}, &ProfileCallError{Kind: ProviderTimeout}
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			callOutcome = string(ProviderTimeout)
			return CompletionResult{}, &ProfileCallError{Kind: ProviderTimeout}
		}
		return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxBody+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			callOutcome = "canceled"
			return CompletionResult{}, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			callOutcome = "canceled"
			return CompletionResult{}, err
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			callOutcome = string(ProviderTimeout)
			return CompletionResult{}, &ProfileCallError{Kind: ProviderTimeout, StatusCode: response.StatusCode}
		}
		callOutcome = string(ProviderResponseInvalid)
		return CompletionResult{}, &ProfileCallError{Kind: ProviderResponseInvalid, StatusCode: response.StatusCode}
	}
	if int64(len(data)) > c.maxBody {
		callOutcome = string(ProviderResponseInvalid)
		return CompletionResult{}, &ProfileCallError{Kind: ProviderResponseInvalid, StatusCode: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		callErr := classifyProfileHTTPError(response.StatusCode, data)
		callOutcome = profileCallTraceOutcome(callErr)
		return CompletionResult{}, callErr
	}
	var result CompletionResult
	if c.profile.Protocol == ProtocolAnthropicMessages {
		var wire anthropicMessageResponse
		if err := json.Unmarshal(data, &wire); err != nil {
			callOutcome = string(ProviderResponseInvalid)
			return CompletionResult{}, &ProfileCallError{Kind: ProviderResponseInvalid, StatusCode: response.StatusCode}
		}
		for _, block := range wire.Content {
			if block.Type == "text" {
				result.Text += block.Text
			}
		}
		result.Metadata = CompletionMetadata{
			ReturnedModel: wire.Model, FinishReason: wire.StopReason, ProviderRequestID: wire.ID,
			Usage: Usage{InputTokens: wire.Usage.InputTokens, OutputTokens: wire.Usage.OutputTokens,
				CacheCreationInputTokens: wire.Usage.CacheCreationInputTokens, CachedInputTokens: wire.Usage.CacheReadInputTokens},
		}
	} else {
		var wire openAIProfileResponse
		if err := json.Unmarshal(data, &wire); err != nil || len(wire.Choices) != 1 {
			callOutcome = string(ProviderResponseInvalid)
			return CompletionResult{}, &ProfileCallError{Kind: ProviderResponseInvalid, StatusCode: response.StatusCode}
		}
		result.Text = wire.Choices[0].Message.Content
		result.Metadata = CompletionMetadata{
			ReturnedModel: wire.Model, FinishReason: wire.Choices[0].FinishReason, ProviderRequestID: wire.ID,
			Usage: Usage{InputTokens: wire.Usage.PromptTokens, OutputTokens: wire.Usage.CompletionTokens,
				CachedInputTokens: wire.Usage.PromptTokensDetails.CachedTokens, ReasoningTokens: wire.Usage.CompletionTokensDetails.ReasoningTokens},
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		callOutcome = string(ProviderResponseInvalid)
		return CompletionResult{}, &ProfileCallError{Kind: ProviderResponseInvalid, StatusCode: response.StatusCode}
	}
	if truncatedProfileFinishReason(result.Metadata.FinishReason) {
		callOutcome = string(ProviderOutputTruncated)
		return CompletionResult{}, &ProfileCallError{Kind: ProviderOutputTruncated, StatusCode: response.StatusCode}
	}
	callOutcome = "success"
	result.Metadata.ProfileID = c.profile.ID
	result.Metadata.ProfileRevision = c.profile.Revision
	result.Metadata.Provider = c.profile.Provider
	result.Metadata.Protocol = string(c.profile.Protocol)
	result.Metadata.RequestedModel = c.profile.Model
	RecordCallIdentity(ctx,
		ModelIdentity{Provider: c.profile.Provider, Model: c.profile.Model},
		ProfileIdentity{ID: c.profile.ID, Revision: c.profile.Revision, Provider: c.profile.Provider, Protocol: string(c.profile.Protocol), Model: c.profile.Model},
	)
	return result, nil
}

func truncatedProfileFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens":
		return true
	default:
		return false
	}
}

func profileCallTraceOutcome(err error) string {
	var callErr *ProfileCallError
	if errors.As(err, &callErr) {
		return string(callErr.Kind)
	}
	return string(ProviderFailure)
}

func (c *ProfileClient) Close() {
	if c == nil || c.client == nil {
		return
	}
	if transport, ok := c.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (c *ProfileClient) ProfileConfig() ProfileConfig {
	if c == nil {
		return ProfileConfig{}
	}
	return c.profile
}

type anthropicMessageResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens              *int64 `json:"input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type openAIProfileResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        *int64 `json:"prompt_tokens"`
		CompletionTokens    *int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func classifyProfileHTTPError(statusCode int, data []byte) error {
	code := providerErrorCode(data)
	switch code {
	case "insufficient_quota", "quota_exhausted", "quota_exceeded", "insufficient_credits":
		return &ProfileCallError{Kind: ProviderQuotaExhausted, StatusCode: statusCode}
	case "billing_error":
		return &ProfileCallError{Kind: ProviderBilling, StatusCode: statusCode}
	}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &ProfileCallError{Kind: ProviderAuthentication, StatusCode: statusCode}
	case http.StatusTooManyRequests:
		return &ProfileCallError{Kind: ProviderRateLimited, StatusCode: statusCode}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return &ProfileCallError{Kind: ProviderTimeout, StatusCode: statusCode}
	default:
		return &ProfileCallError{Kind: ProviderFailure, StatusCode: statusCode}
	}
}

func providerErrorCode(data []byte) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	code := strings.ToLower(strings.TrimSpace(body.Error.Code))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(body.Error.Type))
	}
	return code
}

func validateEndpoint(raw string, allowLoopback bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(allowLoopback && u.Scheme == "http")) {
		return nil, errors.New("endpoint must be a public HTTPS URL without credentials or query")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") {
		return nil, errors.New("invalid provider endpoint host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !allowLoopback && !publicAddress(ip) {
			return nil, errors.New("provider endpoint resolves to a non-public address")
		}
	} else if !allowLoopback && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local")) {
		return nil, errors.New("provider endpoint must use a public host")
	}
	if p := strings.TrimSpace(u.Path); strings.Contains(p, "\\") || strings.Contains(p, "..") {
		return nil, errors.New("invalid provider endpoint path")
	}
	return u, nil
}

func profileEndpoint(raw string, protocol Protocol) string {
	u, _ := url.Parse(raw)
	base := strings.TrimRight(u.Path, "/")
	if protocol == ProtocolAnthropicMessages {
		if strings.HasSuffix(base, "/v1/messages") {
			return raw
		}
		if strings.HasSuffix(base, "/v1") {
			base += "/messages"
		} else {
			base += "/v1/messages"
		}
	} else {
		if strings.HasSuffix(base, "/chat/completions") {
			return raw
		}
		base += "/chat/completions"
	}
	u.Path = base
	return u.String()
}

func safeProfileTransport(host string, allowLoopback bool) *http.Transport {
	return &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, errors.New("invalid provider endpoint address")
			}
			target := host
			if target == "" {
				target, _, _ = net.SplitHostPort(address)
			}
			if ip, err := netip.ParseAddr(strings.Trim(target, "[]")); err == nil {
				if !allowLoopback && !publicAddress(ip) {
					return nil, errors.New("provider endpoint resolves to a non-public address")
				}
				return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", target)
			if err != nil || len(addresses) == 0 {
				return nil, errors.New("provider endpoint DNS lookup failed")
			}
			for _, ip := range addresses {
				if !allowLoopback && !publicAddress(ip) {
					return nil, errors.New("provider endpoint resolves to a non-public address")
				}
			}
			var lastErr error
			for _, ip := range addresses {
				conn, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.Is4() {
		for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
			if netip.MustParsePrefix(raw).Contains(ip) {
				return false
			}
		}
		return true
	}
	if !netip.MustParsePrefix("2000::/3").Contains(ip) || netip.MustParsePrefix("2001:db8::/32").Contains(ip) || netip.MustParsePrefix("3fff::/20").Contains(ip) {
		return false
	}
	return true
}
