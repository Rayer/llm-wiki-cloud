package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	OTLPEndpointEnv        = "OTEL_EXPORTER_OTLP_ENDPOINT"
	cloudTraceOTLPEndpoint = "https://telemetry.googleapis.com"
	traceExportTimeout     = 2 * time.Second
	maxOTLPRequestBytes    = 1 << 20
)

var (
	errTraceExportUnavailable = errors.New("trace exporter unavailable")
	errTraceExportFailed      = errors.New("trace export failed")
	runtimeProjectID          atomic.Value
)

func setRuntimeProjectID(project string) { runtimeProjectID.Store(strings.TrimSpace(project)) }

func RuntimeProjectID() string {
	project, _ := runtimeProjectID.Load().(string)
	return project
}

type tokenResult struct {
	token *oauth2.Token
	err   error
}

// boundedTokenSource limits token refresh even though oauth2.TokenSource has
// no context parameter. At most one refresh goroutine can outlive a timeout.
type boundedTokenSource struct {
	source  oauth2.TokenSource
	timeout time.Duration
	active  chan struct{}
}

func newBoundedTokenSource(source oauth2.TokenSource, timeout time.Duration) oauth2.TokenSource {
	return &boundedTokenSource{source: source, timeout: timeout, active: make(chan struct{}, 1)}
}

func (s *boundedTokenSource) Token() (*oauth2.Token, error) {
	if s.source == nil || s.timeout <= 0 {
		return nil, errTraceExportUnavailable
	}
	select {
	case s.active <- struct{}{}:
	default:
		return nil, errTraceExportUnavailable
	}
	result := make(chan tokenResult, 1)
	go func() {
		defer func() { <-s.active }()
		token, err := s.source.Token()
		result <- tokenResult{token: token, err: err}
	}()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case result := <-result:
		if result.err != nil || result.token == nil {
			return nil, errTraceExportUnavailable
		}
		return result.token, nil
	case <-timer.C:
		return nil, errTraceExportUnavailable
	}
}

func newOAuthHTTPClient(source oauth2.TokenSource) *http.Client {
	client := oauth2.NewClient(context.Background(), newBoundedTokenSource(source, traceExportTimeout))
	client.Timeout = traceExportTimeout
	return client
}

func newCloudTraceExporter(ctx context.Context, endpoint string, source oauth2.TokenSource, client *http.Client) (sdktrace.SpanExporter, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "telemetry.googleapis.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errTraceExportUnavailable
	}
	if client == nil {
		if source == nil {
			return nil, errTraceExportUnavailable
		}
		client = newOAuthHTTPClient(source)
	}
	return newOTLPHTTPExporter(ctx, cloudTraceOTLPEndpoint, client)
}

func newOTLPHTTPExporter(ctx context.Context, endpoint string, client *http.Client) (sdktrace.SpanExporter, error) {
	var endpointOption otlptracehttp.Option
	if endpoint == cloudTraceOTLPEndpoint {
		// WithEndpoint uses HTTPS and appends the protocol's /v1/traces path.
		endpointOption = otlptracehttp.WithEndpoint("telemetry.googleapis.com")
	} else {
		// Tests use a loopback server and supply its complete request path.
		endpointOption = otlptracehttp.WithEndpointURL(endpoint)
	}
	exporter, err := otlptracehttp.New(ctx,
		endpointOption,
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithHeaders(map[string]string{}),
		otlptracehttp.WithMaxRequestSize(maxOTLPRequestBytes),
		otlptracehttp.WithTimeout(traceExportTimeout),
	)
	if err != nil {
		return nil, errTraceExportUnavailable
	}
	return safeTraceExporter{SpanExporter: exporter}, nil
}

func newRuntimeCloudTraceExporter() (sdktrace.SpanExporter, error) {
	endpoint := strings.TrimSpace(os.Getenv(OTLPEndpointEnv))
	if endpoint == "" {
		return nil, nil
	}
	if endpoint != cloudTraceOTLPEndpoint {
		return nil, errTraceExportUnavailable
	}
	credentials, err := google.FindDefaultCredentials(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil || credentials == nil || credentials.TokenSource == nil {
		return nil, errTraceExportUnavailable
	}
	return newCloudTraceExporter(context.Background(), endpoint, credentials.TokenSource, nil)
}

// safeTraceExporter prevents exporter response bodies and auth errors from
// reaching the SDK's asynchronous otel.Handle path.
type safeTraceExporter struct{ sdktrace.SpanExporter }

func (e safeTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if err := e.SpanExporter.ExportSpans(ctx, spans); err != nil {
		return errTraceExportFailed
	}
	return nil
}

func (e safeTraceExporter) Shutdown(ctx context.Context) error {
	if err := e.SpanExporter.Shutdown(ctx); err != nil {
		return errTraceExportFailed
	}
	return nil
}
