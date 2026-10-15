package observability

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
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

const storageReadDetailLimit = 16

type storageReadBudgetKey struct{}

type storageReadBudget struct {
	mu         sync.Mutex
	count      int
	suppressed int
	phase      trace.Span
}

// WithStorageReadBudget starts a request-local detail budget for one actual
// phase invocation. Overflow is summarized on that live phase span.
func WithStorageReadBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, storageReadBudgetKey{}, &storageReadBudget{phase: trace.SpanFromContext(ctx)})
}

// StartStorageRead records one bounded logical storage operation. Suppressed
// detail leaves the caller context intact so real transport spans retain their
// actual live parent.
func StartStorageRead(ctx context.Context, role string) (context.Context, func(error, string, string)) {
	role = storageObjectRole(role)
	if budget, ok := ctx.Value(storageReadBudgetKey{}).(*storageReadBudget); ok && budget != nil {
		budget.mu.Lock()
		if budget.count >= storageReadDetailLimit {
			budget.suppressed++
			budget.phase.SetAttributes(attribute.Int("query.storage.read.suppressed", budget.suppressed))
			budget.mu.Unlock()
			return ctx, func(error, string, string) {}
		}
		budget.count++
		budget.mu.Unlock()
	}
	readCtx, span := otel.Tracer(queryInstrumentationScope).Start(ctx, "query.storage.read",
		trace.WithAttributes(attribute.String("query.object_role", role)),
	)
	return readCtx, func(err error, verification, reason string) {
		verification = storageVerification(verification)
		reason = storageValidationReason(reason)
		outcome := "success"
		if err != nil {
			outcome = "failure"
			if errors.Is(err, context.Canceled) {
				outcome = "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
		}
		span.SetAttributes(
			attribute.String("query.outcome", outcome),
			attribute.String("query.verification", verification),
			attribute.String("query.validation_reason", reason),
		)
		if err != nil {
			span.SetStatus(codes.Error, outcome)
		}
		span.End()
	}
}

func storageObjectRole(role string) string {
	switch role {
	case "current_manifest", "retained_manifest", "concept_cache", "id_map", "source_inventory", "dictionary", "tag_set", "query_rules", "concept_page":
		return role
	default:
		return "other"
	}
}

func storageVerification(value string) string {
	switch value {
	case "not_required", "success", "failure":
		return value
	default:
		return "not_required"
	}
}

func storageValidationReason(value string) string {
	switch value {
	case "none", "generation", "size", "digest", "decode", "identity", "coverage", "other":
		return value
	default:
		return "other"
	}
}

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
