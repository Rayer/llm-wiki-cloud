//go:build lwc_local_pipeline_fixture

package observability

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const localFixtureOTLPEndpointEnv = "LWC_OTLP_TEST_ENDPOINT"

func fixtureTraceExporterFromEnvironment(ctx context.Context) (sdktrace.SpanExporter, bool, error) {
	endpoint := strings.TrimSpace(os.Getenv(localFixtureOTLPEndpointEnv))
	if endpoint == "" {
		return nil, false, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/v1/traces" {
		return nil, true, errTraceExportUnavailable
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Port() == "" || ip == nil || !ip.IsLoopback() {
		return nil, true, errTraceExportUnavailable
	}
	exporter, err := newOTLPHTTPExporter(ctx, endpoint, &http.Client{Timeout: traceExportTimeout})
	if err != nil {
		return nil, true, errTraceExportUnavailable
	}
	return exporter, true, nil
}

func workerFixtureTraceProcessors() []sdktrace.SpanProcessor {
	if strings.TrimSpace(os.Getenv(localFixtureOTLPEndpointEnv)) == "" && os.Getenv("LWC_OTLP_CLOUD_QA") != "1" {
		return nil
	}
	return []sdktrace.SpanProcessor{workerTraceIdentityLogger{}}
}

type workerTraceIdentityLogger struct{}

func (workerTraceIdentityLogger) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (workerTraceIdentityLogger) OnEnd(span sdktrace.ReadOnlySpan) {
	if !allowedSpan(span.InstrumentationScope().Name, span.Name()) {
		return
	}
	spanContext := span.SpanContext()
	parent := span.Parent()
	parentTraceID, parentSpanID := "", ""
	if parent.IsValid() {
		parentTraceID, parentSpanID = parent.TraceID().String(), parent.SpanID().String()
	}
	links := make([]string, 0, len(span.Links()))
	for _, link := range span.Links() {
		links = append(links, link.SpanContext.TraceID().String()+"/"+link.SpanContext.SpanID().String())
	}
	_, _ = fmt.Fprintf(os.Stderr, "OTLP_QA_SPAN case=UC5 trace_id=%s span_id=%s parent_trace_id=%s parent_span_id=%s service_name=lwc-worker scope=%s name=%s links=%s\n",
		spanContext.TraceID(), spanContext.SpanID(), parentTraceID, parentSpanID, span.InstrumentationScope().Name, span.Name(), strings.Join(links, ","))
}

func (workerTraceIdentityLogger) Shutdown(context.Context) error   { return nil }
func (workerTraceIdentityLogger) ForceFlush(context.Context) error { return nil }
