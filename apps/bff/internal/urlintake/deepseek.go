package urlintake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DeepSeekFetchEndpoint = "https://api.deepseek.com/v1/tools/fetch"
	DeepSeekFetchProvider = "deepseek-fetch"
	MaxCandidateBytes     = 5 << 20
	maxProviderErrorBytes = 8 << 10
)

type FailureKind string

type FailureCategory string

const (
	FailureCategoryNotConfigured      FailureCategory = "not_configured"
	FailureCategoryInvalidURL         FailureCategory = "invalid_url"
	FailureCategoryAuthentication     FailureCategory = "authentication"
	FailureCategoryBalance            FailureCategory = "balance"
	FailureCategorySecurityPolicy     FailureCategory = "security_policy"
	FailureCategoryContentUnavailable FailureCategory = "content_unavailable"
	FailureCategoryRateLimited        FailureCategory = "rate_limited"
	FailureCategoryProviderTimeout    FailureCategory = "provider_timeout"
	FailureCategoryProviderError      FailureCategory = "provider_error"
	FailureCategoryInvalidRequest     FailureCategory = "invalid_request"
	FailureCategoryRequestTooLarge    FailureCategory = "request_too_large"
	FailureCategoryUnexpectedStatus   FailureCategory = "unexpected_status"
	FailureCategoryTransportTimeout   FailureCategory = "transport_timeout"
	FailureCategoryTransportCancelled FailureCategory = "transport_cancelled"
	FailureCategoryTransportError     FailureCategory = "transport_error"
	FailureCategoryInvalidResponse    FailureCategory = "invalid_response"
	FailureCategoryContentTooLarge    FailureCategory = "content_too_large"
	FailureCategoryUnknown            FailureCategory = "unknown"
)

const (
	FailureNotConfigured FailureKind = "not_configured"
	FailureInvalidURL    FailureKind = "invalid_url"
	FailureRateLimited   FailureKind = "rate_limited"
	FailureProvider      FailureKind = "provider_error"
	FailureTransport     FailureKind = "transport_error"
	FailureResponse      FailureKind = "invalid_response"
	FailureTooLarge      FailureKind = "content_too_large"
)

type ProviderFailure struct {
	Provider   string
	Kind       FailureKind
	Category   FailureCategory
	StatusCode int
	RetryAfter time.Duration
	Cause      error
}

func (e *ProviderFailure) Error() string {
	if e == nil {
		return "provider unavailable"
	}
	switch e.Kind {
	case FailureNotConfigured:
		return "Fetch provider is not configured"
	case FailureInvalidURL:
		return "URL is invalid or unsafe"
	case FailureRateLimited:
		return "Fetch provider is rate limited"
	case FailureTooLarge:
		return "Fetched content exceeds the supported size"
	case FailureResponse:
		return "Fetch provider returned an invalid response"
	default:
		return "Fetch provider is unavailable"
	}
}

func (e *ProviderFailure) Unwrap() error { return e.Cause }

type Page struct {
	Title     string
	Markdown  string
	Truncated *bool
}

type Fetcher interface {
	Fetch(context.Context, string) (Page, error)
}

type DeepSeekFetcher struct {
	APIKey   string
	Endpoint string
	Client   *http.Client
	Resolver IPResolver
}

func NewDeepSeekFetcher(apiKey string) *DeepSeekFetcher {
	return &DeepSeekFetcher{
		APIKey:   strings.TrimSpace(apiKey),
		Endpoint: DeepSeekFetchEndpoint,
		Client: &http.Client{
			Timeout: 45 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (f *DeepSeekFetcher) Fetch(ctx context.Context, rawURL string) (Page, error) {
	if f == nil || strings.TrimSpace(f.APIKey) == "" || f.Client == nil || strings.TrimSpace(f.Endpoint) == "" {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureNotConfigured}
	}
	if _, err := ValidatePublicURL(ctx, rawURL, f.Resolver); err != nil {
		if errors.Is(err, ErrUnresolvedURL) {
			return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport, Cause: err}
		}
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureInvalidURL, Cause: err}
	}
	payload, err := json.Marshal(struct {
		URL string `json:"url"`
	}{URL: rawURL})
	if err != nil {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureResponse, Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureNotConfigured, Cause: err}
	}
	request.Header.Set("Authorization", "Bearer "+f.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := f.Client.Do(request)
	if err != nil {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport, Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		kind := FailureProvider
		if response.StatusCode == http.StatusTooManyRequests {
			kind = FailureRateLimited
		}
		var errorType, errorCode string
		if response.StatusCode == http.StatusBadRequest {
			errorType, errorCode = readProviderErrorClassification(response.Body)
		}
		return Page{}, &ProviderFailure{
			Provider: DeepSeekFetchProvider, Kind: kind,
			Category:   classifyProviderFailure(response.StatusCode, errorType, errorCode),
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, MaxCandidateBytes+64*1024+1))
	if err != nil {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport, StatusCode: response.StatusCode, Cause: err}
	}
	if len(responseBytes) > MaxCandidateBytes+64*1024 {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTooLarge, StatusCode: response.StatusCode}
	}
	var decoded struct {
		Title     string `json:"title"`
		Content   string `json:"content"`
		Truncated *bool  `json:"truncated"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureResponse, StatusCode: response.StatusCode, Cause: err}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureResponse, StatusCode: response.StatusCode, Cause: err}
	}
	if !utf8.ValidString(decoded.Title) || !utf8.ValidString(decoded.Content) {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureResponse, StatusCode: response.StatusCode}
	}
	if len(decoded.Content) > MaxCandidateBytes {
		return Page{}, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTooLarge, StatusCode: response.StatusCode}
	}
	return Page{Title: strings.TrimSpace(decoded.Title), Markdown: decoded.Content, Truncated: decoded.Truncated}, nil
}

func readProviderErrorClassification(body io.Reader) (string, string) {
	data, err := io.ReadAll(io.LimitReader(body, maxProviderErrorBytes+1))
	if err != nil || len(data) > maxProviderErrorBytes {
		return "", ""
	}
	var response struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", ""
	}
	return response.Error.Type, response.Error.Code
}

func classifyProviderFailure(status int, errorType, errorCode string) FailureCategory {
	switch status {
	case http.StatusUnauthorized:
		return FailureCategoryAuthentication
	case http.StatusPaymentRequired:
		return FailureCategoryBalance
	case http.StatusForbidden:
		return FailureCategorySecurityPolicy
	case http.StatusNotFound:
		return FailureCategoryContentUnavailable
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return FailureCategoryProviderTimeout
	case http.StatusTooManyRequests:
		return FailureCategoryRateLimited
	case http.StatusRequestEntityTooLarge:
		return FailureCategoryRequestTooLarge
	case http.StatusBadRequest:
		if errorType == "invalid_request_error" || errorCode == "invalid_request_error" {
			return FailureCategoryInvalidRequest
		}
		return FailureCategoryUnexpectedStatus
	}
	if status >= 500 && status <= 599 {
		return FailureCategoryProviderError
	}
	return FailureCategoryUnexpectedStatus
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}
