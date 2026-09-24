package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type diagnosticTransport struct {
	status int
	body   string
}

func (d diagnosticTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: d.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(d.body))}, nil
}
func TestSafeProviderDiagnosticsFromRealClientWithoutNetwork(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "provider_auth_rejected"}, {403, "provider_auth_rejected"}, {429, "provider_rate_limited"},
		{503, "provider_server_error"}, {400, "provider_http_rejected"}, {200, "provider_response_invalid_json"},
	} {
		client := NewClient("explicit-mock-key")
		client.client.Transport = diagnosticTransport{tc.status, "private-provider-body"}
		_, err := client.Chat(context.Background(), "test", "test")
		if err == nil || SafeErrorCategory(err) != tc.want {
			t.Fatalf("status %d category %q", tc.status, SafeErrorCategory(err))
		}
		if strings.Contains(err.Error(), "private-provider-body") || strings.Contains(err.Error(), "explicit-mock-key") {
			t.Fatal("private text in error")
		}
	}
}
