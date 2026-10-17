//go:build !lwc_local_pipeline_fixture

package observability

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func fixtureTraceExporterFromEnvironment(context.Context) (sdktrace.SpanExporter, bool, error) {
	return nil, false, nil
}

func workerFixtureTraceProcessors() []sdktrace.SpanProcessor { return nil }
