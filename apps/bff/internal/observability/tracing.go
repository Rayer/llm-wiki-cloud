package observability

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

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

// InitWorkerTraces installs the worker's offline-safe exporter on stderr so
// child stdout remains the existing exactly-once pipeline log stream.
func InitWorkerTraces(revision string) (*sdktrace.TracerProvider, error) {
	provider, err := newTraceProvider(os.Stderr, "lwc-worker", revision)
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
const pipelineInstrumentationScope = "llm-wiki-bff/pipeline"
const workerInstrumentationScope = "llm-wiki-worker/pipeline"

var pipelineSpanNames = map[string]struct{}{
	"pipeline.submit": {}, "pipeline.admission": {}, "pipeline.quota.evaluate": {}, "pipeline.quota.reserve": {},
	"pipeline.job.invoke": {}, "pipeline.reservation.link": {}, "pipeline.invoke.recovery": {},
	"pipeline.status": {}, "pipeline.status.lookup": {}, "pipeline.status.publication": {}, "pipeline.status.settlement": {},
	"pipeline.quota.reconcile": {}, "pipeline.quota.list": {}, "pipeline.quota.lookup": {}, "pipeline.quota.publication": {},
	"pipeline.quota.link": {}, "pipeline.quota.settle": {}, "pipeline.recovery": {}, "pipeline.recovery.lookup": {},
	"pipeline.recovery.transaction": {}, "pipeline.worker": {}, "pipeline.worker.validate": {}, "pipeline.worker.lease": {},
	"pipeline.worker.workspace": {}, "pipeline.worker.child": {}, "pipeline.worker.postprocess": {},
	"pipeline.worker.suggested_queries": {}, "pipeline.worker.suggested_queries.provider": {},
	"pipeline.worker.suggested_queries.validate": {}, "pipeline.worker.suggested_queries.fallback": {},
	"pipeline.worker.publish": {}, "pipeline.worker.finalize": {}, "pipeline.worker.compile_receipt": {},
}

var pipelineOutcomeValues = map[string]struct{}{
	"success": {}, "failure": {}, "canceled": {}, "timeout": {}, "accepted": {}, "pending": {},
	"skipped": {}, "unknown": {}, "degraded": {}, "committed": {}, "not_committed": {}, "not_applicable": {},
}

// StartPipelineSpan starts one allowlisted BFF Pipeline operation or phase.
// Unknown names are suppressed so callers cannot accidentally export dynamic data.
func StartPipelineSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	if _, ok := pipelineSpanNames[name]; !ok {
		return ctx, trace.SpanFromContext(context.Background())
	}
	if _, detail := pipelineDetailSpanNames[name]; detail {
		if budget, ok := ctx.Value(pipelineDetailBudgetKey{}).(*PipelineDetailBudget); ok && budget != nil {
			budget.mu.Lock()
			if budget.started >= budget.limit {
				budget.suppressed++
				budget.mu.Unlock()
				return ctx, trace.SpanFromContext(context.Background())
			}
			budget.started++
			budget.mu.Unlock()
		}
	}
	return otel.Tracer(pipelineInstrumentationScope).Start(ctx, name)
}

// StartWorkerSpan starts one allowlisted worker operation or phase.
func StartWorkerSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	if _, ok := pipelineSpanNames[name]; !ok {
		return ctx, trace.SpanFromContext(context.Background())
	}
	return otel.Tracer(workerInstrumentationScope).Start(ctx, name)
}

// FinishPipelineSpan records only a finite outcome. Raw errors belong to the
// caller's existing logs and never become span status descriptions.
func FinishPipelineSpan(span trace.Span, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	if _, ok := pipelineOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	span.SetAttributes(attribute.String("pipeline.outcome", outcome))
	if outcome == "failure" || outcome == "timeout" {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
}

// SetPipelineCount adds a nonnegative count under a fixed safe key.
func SetPipelineCount(span trace.Span, key string, count int) {
	if span == nil || !span.IsRecording() || count < 0 {
		return
	}
	switch key {
	case "pipeline.processed_count", "pipeline.suppressed_count", "pipeline.command_count":
		span.SetAttributes(attribute.Int(key, count))
	}
}

// SetWorkerCommandCount records the actual number of child command attempts
// observed under ctx, without exposing argv or command text.
func SetWorkerCommandCount(ctx context.Context, count int) {
	SetPipelineCount(trace.SpanFromContext(ctx), "pipeline.command_count", count)
}

// SetPipelinePublicationOutcome records the publication state independently
// from the worker process outcome.
func SetPipelinePublicationOutcome(span trace.Span, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	switch outcome {
	case "committed", "not_committed", "unknown":
		span.SetAttributes(attribute.String("pipeline.publication.outcome", outcome))
	}
}

type PipelineDetailBudget struct {
	mu         sync.Mutex
	limit      int
	started    int
	suppressed int
}

type pipelineDetailBudgetKey struct{}

var pipelineDetailSpanNames = map[string]struct{}{
	"pipeline.quota.lookup": {}, "pipeline.quota.publication": {}, "pipeline.quota.link": {}, "pipeline.quota.settle": {},
	"pipeline.recovery.transaction": {},
}

// WithPipelineDetailBudget limits reservation-level OTel details for one
// reconciliation sweep without changing the work performed by that sweep.
func WithPipelineDetailBudget(ctx context.Context, limit int) (context.Context, *PipelineDetailBudget) {
	if existing, ok := ctx.Value(pipelineDetailBudgetKey{}).(*PipelineDetailBudget); ok && existing != nil {
		return ctx, existing
	}
	if limit < 0 {
		limit = 0
	}
	budget := &PipelineDetailBudget{limit: limit}
	return context.WithValue(ctx, pipelineDetailBudgetKey{}, budget), budget
}

// Suppressed reports the number of reservation observations omitted by the
// OTel budget. Business lookups and settlements are unaffected.
func (b *PipelineDetailBudget) Suppressed() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.suppressed
}

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
	if allowedInstrumentationScope(span.InstrumentationScope().Name) {
		p.next.OnStart(ctx, span)
	}
}

func (p querySpanExportProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if allowedInstrumentationScope(span.InstrumentationScope().Name) {
		p.next.OnEnd(span)
	}
}

func (p querySpanExportProcessor) Shutdown(ctx context.Context) error {
	return p.next.Shutdown(ctx)
}

func (p querySpanExportProcessor) ForceFlush(ctx context.Context) error {
	return p.next.ForceFlush(ctx)
}

func allowedInstrumentationScope(name string) bool {
	return name == queryInstrumentationScope || name == pipelineInstrumentationScope || name == workerInstrumentationScope
}

func ShutdownTraces(provider *sdktrace.TracerProvider) {
	if provider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	}
}
