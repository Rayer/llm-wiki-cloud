package observability

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InitTraces installs an offline-safe stdout exporter for request spans.
func InitTraces(service, revision string) (*sdktrace.TracerProvider, error) {
	provider, err := newTraceProvider(os.Stdout, service, revision)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(provider)
	return provider, nil
}

func newTraceProvider(writer io.Writer, service, revision string) (*sdktrace.TracerProvider, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(writer))
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(querySpanExportProcessor{next: sdktrace.NewBatchSpanProcessor(exporter)}),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", service),
			attribute.String("service.revision", revision),
		)),
	), nil
}

// querySpanExportProcessor keeps the stdout contract scoped to BFF-owned
// diagnostics. Google SDK spans can carry raw provider errors in attributes,
// events, and status descriptions, so they must not cross this export boundary.
type querySpanExportProcessor struct{ next sdktrace.SpanProcessor }

const queryInstrumentationScope = "llm-wiki-bff/query"

func (p querySpanExportProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	if span.InstrumentationScope().Name == queryInstrumentationScope {
		p.next.OnStart(ctx, span)
	}
}

func (p querySpanExportProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if span.InstrumentationScope().Name == queryInstrumentationScope {
		p.next.OnEnd(span)
	}
}

func (p querySpanExportProcessor) Shutdown(ctx context.Context) error {
	return p.next.Shutdown(ctx)
}

func (p querySpanExportProcessor) ForceFlush(ctx context.Context) error {
	return p.next.ForceFlush(ctx)
}

func ShutdownTraces(provider *sdktrace.TracerProvider) {
	if provider != nil {
		_ = provider.Shutdown(context.Background())
	}
}
