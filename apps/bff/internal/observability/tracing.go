package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
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
	return InitTracesWithProject(service, revision, strings.TrimSpace(os.Getenv("GCP_PROJECT")))
}

func InitTracesWithProject(service, revision, project string) (*sdktrace.TracerProvider, error) {
	return InitTracesWithProjectAndProcessors(service, revision, project)
}

// InitTracesWithProjectAndProcessors is an internal QA hook for processors
// that must observe exactly the spans permitted by the shared export filter.
func InitTracesWithProjectAndProcessors(service, revision, project string, additional ...sdktrace.SpanProcessor) (*sdktrace.TracerProvider, error) {
	InstallTracePropagator()
	setRuntimeProjectID(project)
	provider, err := newRuntimeTraceProvider(os.Stdout, service, revision, project, additional...)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(provider)
	return provider, nil
}

// InitWorkerTraces installs the worker's offline-safe exporter on stderr so
// child stdout remains the existing exactly-once pipeline log stream.
func InitWorkerTraces(revision string) (*sdktrace.TracerProvider, error) {
	InstallTracePropagator()
	project := strings.TrimSpace(os.Getenv("GCP_PROJECT"))
	setRuntimeProjectID(project)
	provider, err := newRuntimeTraceProvider(os.Stderr, "lwc-worker", revision, project, workerFixtureTraceProcessors()...)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(provider)
	return provider, nil
}

// InitExportWorkerTraces writes bounded export-worker spans to stderr, keeping
// the existing stdout contract available to the worker's caller.
func InitExportWorkerTraces(revision string) (*sdktrace.TracerProvider, error) {
	InstallTracePropagator()
	project := strings.TrimSpace(os.Getenv("GCP_PROJECT"))
	setRuntimeProjectID(project)
	provider, err := newRuntimeTraceProvider(os.Stderr, "lwc-export-worker", revision, project)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(provider)
	return provider, nil
}

func newTraceProvider(writer io.Writer, service, revision string) (*sdktrace.TracerProvider, error) {
	return newTraceProviderWithOTLP(writer, service, revision, "", nil)
}

// NewFilteredTraceProvider constructs the same privacy-filtered dual-export
// pipeline used by runtime initialization. Internal test fixtures supply a
// loopback OTLP exporter here so they exercise the production filter and SDK
// pipeline without credentials or a provider call.
func NewFilteredTraceProvider(writer io.Writer, service, revision, project string, otlp sdktrace.SpanExporter, additional ...sdktrace.SpanProcessor) (*sdktrace.TracerProvider, error) {
	if otlp != nil {
		otlp = safeTraceExporter{SpanExporter: otlp}
	}
	return newTraceProviderWithOTLP(writer, service, revision, project, otlp, additional...)
}

func newRuntimeTraceProvider(writer io.Writer, service, revision, project string, additional ...sdktrace.SpanProcessor) (*sdktrace.TracerProvider, error) {
	exporter, err := newRuntimeCloudTraceExporter()
	if err != nil {
		// Export remains optional; never expose credential or endpoint details.
		io.WriteString(os.Stderr, "[observability] WARNING: trace exporter unavailable\n")
	}
	return newTraceProviderWithOTLP(writer, service, revision, project, exporter, additional...)
}

func newTraceProviderWithOTLP(writer io.Writer, service, revision, project string, otlp sdktrace.SpanExporter, additional ...sdktrace.SpanProcessor) (*sdktrace.TracerProvider, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(writer))
	if err != nil {
		return nil, err
	}
	processors := []sdktrace.SpanProcessor{
		sdktrace.NewBatchSpanProcessor(exporter,
			sdktrace.WithMaxQueueSize(512),
			sdktrace.WithMaxExportBatchSize(64),
			sdktrace.WithBatchTimeout(time.Second),
			sdktrace.WithExportTimeout(traceExportTimeout)),
	}
	if otlp != nil {
		processors = append(processors, sdktrace.NewBatchSpanProcessor(otlp,
			sdktrace.WithMaxQueueSize(512),
			sdktrace.WithMaxExportBatchSize(64),
			sdktrace.WithBatchTimeout(time.Second),
			sdktrace.WithExportTimeout(traceExportTimeout)))
	}
	processors = append(processors, additional...)
	attrs := []attribute.KeyValue{
		attribute.String("service.name", service),
		attribute.String("service.revision", revision),
	}
	if strings.TrimSpace(project) != "" {
		attrs = append(attrs, attribute.String("gcp.project_id", strings.TrimSpace(project)))
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithSpanProcessor(querySpanExportProcessor{next: spanProcessorFanout(processors)}),
		sdktrace.WithResource(resource.NewSchemaless(attrs...)),
	}
	if os.Getenv(devFullTraceSamplingEnv) == "1" {
		// DEV deliberately records every privacy-approved application span,
		// including children of valid remote parents whose sampled flag is unset.
		options = append(options, sdktrace.WithSampler(sdktrace.AlwaysSample()))
	}
	return sdktrace.NewTracerProvider(options...), nil
}

type spanProcessorFanout []sdktrace.SpanProcessor

func (f spanProcessorFanout) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	for _, processor := range f {
		processor.OnStart(ctx, span)
	}
}

func (f spanProcessorFanout) OnEnd(span sdktrace.ReadOnlySpan) {
	for _, processor := range f {
		processor.OnEnd(span)
	}
}

func (f spanProcessorFanout) Shutdown(ctx context.Context) error {
	var errs []error
	for _, processor := range f {
		if err := processor.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f spanProcessorFanout) ForceFlush(ctx context.Context) error {
	var errs []error
	for _, processor := range f {
		if err := processor.ForceFlush(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// querySpanExportProcessor keeps the stdout contract scoped to BFF-owned
// diagnostics. Google SDK spans can carry raw provider errors in attributes,
// events, and status descriptions, so they must not cross this export boundary.
type querySpanExportProcessor struct{ next sdktrace.SpanProcessor }

const queryInstrumentationScope = "llm-wiki-bff/query"
const pipelineInstrumentationScope = "llm-wiki-bff/pipeline"
const workerInstrumentationScope = "llm-wiki-worker/pipeline"
const authInstrumentationScope = "llm-wiki-auth/operations"
const profileInstrumentationScope = "llm-wiki-bff/profile"
const exportInstrumentationScope = "llm-wiki-export/operations"
const dataInstrumentationScope = "llm-wiki-bff/data"

// devFullTraceSamplingEnv is set only by the canonical development runtime
// config. Production and offline defaults retain the SDK's parent-based policy.
const devFullTraceSamplingEnv = "LWC_DEV_FULL_TRACE_SAMPLING"

var authSpanNames = stringSet(
	"auth.request", "auth.google.token_exchange", "auth.google.token_decode", "auth.google.jwks_fetch",
	"auth.google.claims_validate", "auth.google.identity_commit", "auth.cli.operation", "auth.mcp.operation",
	"auth.login", "auth.register", "auth.refresh", "auth.logout", "auth.demo",
	"auth.cli.refresh", "auth.cli.logout", "auth.cli.status", "auth.cli.projects", "auth.cli.sync_service",
	"auth.session", "auth.pairing", "auth.binding",
	"auth.google.start", "auth.google.login_start", "auth.google.link_start", "auth.google.callback",
	"auth.google.login_callback", "auth.google.link_callback", "auth.google.link_confirm", "auth.google.link_cancel",
	"auth.google.link_complete", "auth.google.complete", "auth.google.identity",
	"auth.mcp.metadata", "auth.mcp.register", "auth.mcp.authorize", "auth.mcp.token", "auth.mcp.consent", "auth.mcp.grants",
	"auth.mcp.resource", "auth.transaction",
)
var profileSpanNames = stringSet(
	"profile.dispatch", "profile.dispatch.sweep", "profile.work", "profile.claim", "profile.execute", "profile.finish",
	"profile.derive", "profile.derive.claim", "profile.derive.content_pin", "profile.derive.provider",
	"profile.derive.model", "profile.derive.output_decode",
	"profile.derive.accounting", "profile.derive.artifact_encode", "profile.derive.provider_call", "profile.derive.artifact_write", "profile.derive.commit", "profile.compile", "profile.compile.provider",
	"profile.compile.artifact_write", "profile.compile.commit", "profile.tag.build", "profile.tag.evaluate",
	"profile.tag.transport", "profile.tag.decode", "profile.tag.job", "profile.tag.inventory", "profile.tag.artifact_write", "profile.tag.commit",
)
var exportSpanNames = stringSet(
	"export.request", "export.admission", "export.start", "export.status", "export.download_url", "export.authorize",
	"export.worker", "export.worker.mark_running", "export.worker.inventory", "export.worker.archive",
	"export.worker.zip_close", "export.worker.upload_close", "export.worker.temp_verify", "export.worker.ready_copy",
	"export.worker.ready_verify", "export.worker.complete", "export.worker.fail",
	"export.worker.ready_attributes",
)

func stringSet(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

var pipelineSpanNames = map[string]struct{}{
	"pipeline.submit": {}, "pipeline.admission": {}, "pipeline.quota.evaluate": {}, "pipeline.quota.reserve": {},
	"pipeline.job.invoke": {}, "pipeline.reservation.link": {}, "pipeline.invoke.recovery": {},
	"pipeline.status": {}, "pipeline.status.lookup": {}, "pipeline.status.publication": {}, "pipeline.status.settlement": {},
	"pipeline.quota.reconcile": {}, "pipeline.quota.list": {}, "pipeline.quota.lookup": {}, "pipeline.quota.publication": {},
	"pipeline.quota.link": {}, "pipeline.quota.settle": {}, "pipeline.recovery": {}, "pipeline.recovery.result": {}, "pipeline.recovery.lookup": {},
	"pipeline.recovery.transaction": {}, "pipeline.worker": {}, "pipeline.worker.validate": {}, "pipeline.worker.lease": {},
	"pipeline.worker.workspace": {}, "pipeline.worker.materialize": {}, "pipeline.worker.child": {}, "pipeline.worker.postprocess": {},
	"pipeline.worker.suggested_queries": {}, "pipeline.worker.suggested_queries.provider": {},
	"pipeline.worker.suggested_queries.validate": {}, "pipeline.worker.suggested_queries.fallback": {},
	"pipeline.worker.export_validate": {}, "pipeline.worker.export": {},
	"pipeline.worker.publish": {}, "pipeline.worker.finalize": {}, "pipeline.worker.compile_receipt": {},
}

var pipelineOutcomeValues = map[string]struct{}{
	"success": {}, "failure": {}, "canceled": {}, "timeout": {}, "accepted": {}, "pending": {},
	"skipped": {}, "unknown": {}, "degraded": {}, "committed": {}, "not_committed": {}, "not_applicable": {}, "rejected": {}, "no_op": {},
	"confirmation_required": {}, "existing": {}, "waiting_manual": {}, "superseded": {}, "not_active": {},
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
	ctx, span := otel.Tracer(pipelineInstrumentationScope).Start(ctx, name)
	SetRequestIDAttribute(span, RequestIDFromContext(ctx))
	return ctx, span
}

// StartWorkerSpan starts one allowlisted worker operation or phase.
func StartWorkerSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	if _, ok := pipelineSpanNames[name]; !ok {
		return ctx, trace.SpanFromContext(context.Background())
	}
	ctx, span := otel.Tracer(workerInstrumentationScope).Start(ctx, name)
	SetRequestIDAttribute(span, RequestIDFromContext(ctx))
	return ctx, span
}

func startScopedSpan(ctx context.Context, scope string, allow map[string]struct{}, name string) (context.Context, trace.Span) {
	if _, ok := allow[name]; !ok {
		return ctx, trace.SpanFromContext(context.Background())
	}
	ctx, span := otel.Tracer(scope).Start(ctx, name)
	SetRequestIDAttribute(span, RequestIDFromContext(ctx))
	return ctx, span
}

// StartAuthSpan, StartProfileSpan and StartExportSpan accept fixed names only;
// request data must never be used as a span name or attribute value.
func StartAuthSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return startScopedSpan(ctx, authInstrumentationScope, authSpanNames, name)
}

func StartProfileSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	if _, detail := profileDetailSpanNames[name]; detail {
		if name == "profile.tag.evaluate" {
			if budget, ok := ctx.Value(profileDetailBudgetKey{}).(*ProfileDetailBudget); ok && budget != nil && !budget.take() {
				return context.WithValue(ctx, profileDetailSuppressedKey{}, true), trace.SpanFromContext(context.Background())
			}
		} else if suppressed, _ := ctx.Value(profileDetailSuppressedKey{}).(bool); suppressed {
			return ctx, trace.SpanFromContext(context.Background())
		}
	}
	return startScopedSpan(ctx, profileInstrumentationScope, profileSpanNames, name)
}

func StartExportSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return startScopedSpan(ctx, exportInstrumentationScope, exportSpanNames, name)
}

// WithoutTraceParent preserves context values and cancellation while making
// a separately scheduled work attempt its own trace root.
func WithoutTraceParent(ctx context.Context) context.Context {
	return trace.ContextWithSpanContext(ctx, trace.SpanContext{})
}

func finishScopedSpan(span trace.Span, key, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	if _, ok := pipelineOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	span.SetAttributes(attribute.String(key, outcome))
	if outcome == "failure" || outcome == "timeout" {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
}

func FinishAuthSpan(span trace.Span, outcome string) { finishScopedSpan(span, "auth.outcome", outcome) }
func FinishProfileSpan(span trace.Span, outcome string) {
	finishScopedSpan(span, "profile.outcome", outcome)
}
func FinishExportSpan(span trace.Span, outcome string) {
	finishScopedSpan(span, "export.outcome", outcome)
}

func SetAuthOperation(span trace.Span, operation string) {
	switch operation {
	case "login", "register", "refresh", "logout", "demo",
		"cli_refresh", "cli_logout", "cli_status", "cli_project_list", "cli_sync_service",
		"session_list", "session_revoke", "pairing_start", "pairing_poll", "pairing_decision",
		"binding_list", "binding_create", "binding_reauthorize", "binding_revoke",
		"google_start", "google_login_start", "google_link_start", "google_callback", "google_login_callback",
		"google_link_callback", "google_link_confirm", "google_link_cancel", "google_link_complete", "google_complete", "google_identity",
		"mcp_metadata", "mcp_register", "mcp_authorize", "mcp_token", "mcp_consent_read", "mcp_consent_decision",
		"mcp_grants_list", "mcp_grants_revoke", "mcp_resource", "other":
	default:
		operation = "other"
	}
	if span != nil && span.IsRecording() {
		span.SetAttributes(attribute.String("auth.operation", operation))
	}
}

func SetAuthGuardOutcome(span trace.Span, stage, outcome string) {
	switch stage {
	case "host", "cors", "body", "rate", "authentication", "authorization", "provider", "transaction", "other":
	default:
		stage = "other"
	}
	switch outcome {
	case "passed", "rejected", "failure", "skipped", "unknown":
	default:
		outcome = "unknown"
	}
	if span != nil && span.IsRecording() {
		span.SetAttributes(
			attribute.String("auth.guard.stage", stage),
			attribute.String("auth.guard.outcome", outcome),
			attribute.String("auth.guard."+stage+".outcome", outcome),
		)
	}
}

func SetProfileOutcome(span trace.Span, key, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	if _, ok := pipelineOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	switch key {
	case "profile.claim.outcome", "profile.execution.outcome", "profile.commit.outcome", "profile.provider.outcome", "profile.artifact.outcome",
		"profile.model.outcome", "profile.decode.outcome", "profile.validation.outcome", "profile.active.outcome", "profile.candidate.outcome":
		span.SetAttributes(attribute.String(key, outcome))
	}
}

func SetProfileStage(span trace.Span, stage, outcome string) {
	switch stage {
	case "transport", "decode", "model", "answers", "validation", "output_decode", "accounting", "artifact_encode":
	default:
		return
	}
	if _, ok := pipelineOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	if span != nil && span.IsRecording() {
		span.SetAttributes(attribute.String("profile.provider."+stage+".outcome", outcome))
	}
}

func SetProfileStatus(span trace.Span, status string) {
	switch status {
	case "pending", "scheduled", "running", "complete", "waiting_manual", "superseded", "exhausted", "retry_wait", "invalid", "incomplete", "ready", "failed", "unknown":
	default:
		status = "unknown"
	}
	if span != nil && span.IsRecording() {
		span.SetAttributes(attribute.String("profile.work.status", status))
	}
}

func SetProfileWorkKind(span trace.Span, kind string) {
	if span == nil || !span.IsRecording() {
		return
	}
	switch kind {
	case "derive", "tag", "compile":
		span.SetAttributes(attribute.String("profile.work.kind", kind))
	}
}

func SetProfileProviderHost(span trace.Span, provider, scheme string) {
	if span == nil || !span.IsRecording() {
		return
	}
	switch provider {
	case "deepseek", "typesafe_systemone":
	default:
		return
	}
	switch scheme {
	case "http", "https":
	default:
		scheme = "other"
	}
	span.SetAttributes(attribute.String("profile.provider", provider), attribute.String("profile.provider.scheme", scheme))
}

func SetProfileCount(span trace.Span, key string, count int) {
	if span == nil || !span.IsRecording() || count < 0 {
		return
	}
	switch key {
	case "profile.processed_count", "profile.claimed_count", "profile.attempt_number", "profile.provider_attempt_count", "profile.expected_count", "profile.completed_count", "profile.missing_count", "profile.suppressed_count", "profile.item_count", "profile.tag_count":
		span.SetAttributes(attribute.Int(key, count))
	}
}

func SetExportOutcome(span trace.Span, key, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	if _, ok := pipelineOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	switch key {
	case "export.publication.outcome", "export.job.outcome", "export.phase.outcome":
		span.SetAttributes(attribute.String(key, outcome))
	}
}

func SetExportCount(span trace.Span, key string, count int) {
	if span == nil || !span.IsRecording() || count < 0 {
		return
	}
	switch key {
	case "export.file_count", "export.source_bytes", "export.archive_bytes", "export.redacted_field_count":
		span.SetAttributes(attribute.Int(key, count))
	}
}

func SetExportUploadTarget(span trace.Span, target string) {
	if span == nil || !span.IsRecording() {
		return
	}
	switch target {
	case "temporary", "ready":
		span.SetAttributes(attribute.String("export.upload.target", target))
	}
}

type ProfileDetailBudget struct {
	mu                         sync.Mutex
	limit, started, suppressed int
}
type profileDetailBudgetKey struct{}
type profileDetailSuppressedKey struct{}

var profileDetailSpanNames = stringSet("profile.tag.evaluate", "profile.tag.transport", "profile.tag.decode")

func WithProfileDetailBudget(ctx context.Context, limit int) (context.Context, *ProfileDetailBudget) {
	if budget, ok := ctx.Value(profileDetailBudgetKey{}).(*ProfileDetailBudget); ok && budget != nil {
		return ctx, budget
	}
	if limit < 0 {
		limit = 0
	}
	budget := &ProfileDetailBudget{limit: limit}
	return context.WithValue(ctx, profileDetailBudgetKey{}, budget), budget
}

func (b *ProfileDetailBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started >= b.limit {
		b.suppressed++
		return false
	}
	b.started++
	return true
}

func (b *ProfileDetailBudget) Suppressed() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.suppressed
}

type authGuardStateKey struct{}
type authGuardState struct {
	mu             sync.Mutex
	recorded       bool
	outcome        string
	requestOutcome string
}

// RecordAuthGuard records a guard result only when the current request owns an
// Auth root. A rejection is retained if later middleware observes a pass.
func RecordAuthGuard(ctx context.Context, stage, outcome string) {
	state, ok := ctx.Value(authGuardStateKey{}).(*authGuardState)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if (state.outcome == "rejected" || state.outcome == "failure") && outcome == "passed" {
		return
	}
	SetAuthGuardOutcome(trace.SpanFromContext(ctx), stage, outcome)
	state.recorded = true
	state.outcome = outcome
}

// RecordAuthRequestOutcome keeps an explicit callback result on the owning
// request so redirects can report their logical result independently of HTTP.
func RecordAuthRequestOutcome(ctx context.Context, outcome string) {
	switch outcome {
	case "success", "failure", "rejected", "cancelled", "confirmation_required":
	default:
		outcome = "unknown"
	}
	state, ok := ctx.Value(authGuardStateKey{}).(*authGuardState)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	state.requestOutcome = outcome
	state.mu.Unlock()
}

// AuthRequestOutcome returns the finite callback result recorded on a request.
func AuthRequestOutcome(ctx context.Context) string {
	state, ok := ctx.Value(authGuardStateKey{}).(*authGuardState)
	if !ok || state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.requestOutcome
}

// AuthCORSObserver observes the standard CORS middleware's decision without
// changing its response or whether the downstream handler runs.
func AuthCORSObserver(cors gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		cors(c)
		if strings.TrimSpace(c.GetHeader("Origin")) == "" {
			return
		}
		if c.Writer.Header().Get("Access-Control-Allow-Origin") == "" {
			RecordAuthGuard(c.Request.Context(), "cors", "rejected")
		} else {
			RecordAuthGuard(c.Request.Context(), "cors", "passed")
		}
	}
}

// AuthHTTPMiddleware opens one root before Auth route guards (host, CORS,
// body-size and rate limits) and records only a finite operation/outcome.
func AuthHTTPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if !isAuthObservationPath(requestPath) {
			c.Next()
			return
		}
		route := c.FullPath()
		if route == "" {
			route = requestPath
		}
		spanName, operation := authRouteObservation(c.Request.Method, route)
		ctx, span := StartAuthSpan(c.Request.Context(), spanName)
		SetAuthOperation(span, operation)
		state := &authGuardState{}
		ctx = context.WithValue(ctx, authGuardStateKey{}, state)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		status := c.Writer.Status()
		state.mu.Lock()
		recorded := state.recorded
		previousOutcome := state.outcome
		requestOutcome := state.requestOutcome
		state.mu.Unlock()
		if !recorded || (status >= 400 && (previousOutcome == "passed" || previousOutcome == "skipped")) {
			outcome := "passed"
			stage := "other"
			if status >= 500 {
				outcome = "failure"
			} else if status >= 400 {
				outcome = "rejected"
				if status == http.StatusUnauthorized || status == http.StatusForbidden {
					stage = "authentication"
				}
			}
			RecordAuthGuard(ctx, stage, outcome)
		}
		outcome := requestOutcome
		if outcome == "" {
			outcome = "success"
		}
		if status >= 500 {
			outcome = "failure"
		} else if status >= 400 && requestOutcome == "" {
			outcome = "rejected"
		}
		FinishAuthSpan(span, outcome)
	}
}

func isAuthObservationPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/auth/") || path == "/api/v1/auth" ||
		strings.Contains(path, "/oauth-authorization-server") || strings.HasPrefix(path, "/mcp/projects/") ||
		strings.HasPrefix(path, "/.well-known/oauth-protected-resource/mcp/projects/")
}

type authRouteObservationInfo struct {
	spanName  string
	operation string
}

var authRouteObservations = map[string]authRouteObservationInfo{
	"POST /api/v1/auth/login":                                           {"auth.login", "login"},
	"POST /api/v1/auth/register":                                        {"auth.register", "register"},
	"POST /api/v1/auth/refresh":                                         {"auth.refresh", "refresh"},
	"POST /api/v1/auth/logout":                                          {"auth.logout", "logout"},
	"POST /api/v1/auth/demo":                                            {"auth.demo", "demo"},
	"POST /api/v1/auth/google/start":                                    {"auth.google.start", "google_start"},
	"GET /api/v1/auth/google/start":                                     {"auth.google.start", "google_start"},
	"POST /api/v1/auth/google/login/start":                              {"auth.google.login_start", "google_login_start"},
	"GET /api/v1/auth/google/login/start":                               {"auth.google.login_start", "google_login_start"},
	"POST /api/v1/auth/google/link/start":                               {"auth.google.link_start", "google_link_start"},
	"GET /api/v1/auth/google/callback":                                  {"auth.google.callback", "google_callback"},
	"GET /api/v1/auth/google/login/callback":                            {"auth.google.login_callback", "google_login_callback"},
	"GET /api/v1/auth/google/link/callback":                             {"auth.google.link_callback", "google_link_callback"},
	"POST /api/v1/auth/google/link/confirm":                             {"auth.google.link_confirm", "google_link_confirm"},
	"POST /api/v1/auth/google/link/cancel":                              {"auth.google.link_cancel", "google_link_cancel"},
	"GET /api/v1/auth/google/link/complete":                             {"auth.google.link_complete", "google_link_complete"},
	"GET /api/v1/auth/google/complete":                                  {"auth.google.complete", "google_complete"},
	"GET /api/v1/auth/google/identity":                                  {"auth.google.identity", "google_identity"},
	"POST /api/v1/auth/cli/pairing/start":                               {"auth.pairing", "pairing_start"},
	"POST /api/v1/auth/cli/pairing/poll":                                {"auth.pairing", "pairing_poll"},
	"POST /api/v1/auth/cli/pairing/decision":                            {"auth.pairing", "pairing_decision"},
	"POST /api/v1/auth/cli/refresh":                                     {"auth.cli.refresh", "cli_refresh"},
	"POST /api/v1/auth/cli/logout":                                      {"auth.cli.logout", "cli_logout"},
	"GET /api/v1/auth/cli/status":                                       {"auth.cli.status", "cli_status"},
	"GET /api/v1/auth/cli/projects":                                     {"auth.cli.projects", "cli_project_list"},
	"GET /api/v1/auth/cli/sync-service":                                 {"auth.cli.sync_service", "cli_sync_service"},
	"GET /api/v1/auth/cli/sessions":                                     {"auth.session", "session_list"},
	"DELETE /api/v1/auth/cli/sessions/:id":                              {"auth.session", "session_revoke"},
	"POST /api/v1/auth/cli/sessions/:id/revoke":                         {"auth.session", "session_revoke"},
	"GET /api/v1/auth/cli/bindings":                                     {"auth.binding", "binding_list"},
	"POST /api/v1/auth/cli/bindings":                                    {"auth.binding", "binding_create"},
	"POST /api/v1/auth/cli/bindings/:projectID/reauthorize":             {"auth.binding", "binding_reauthorize"},
	"DELETE /api/v1/auth/cli/bindings/:projectID/:bindingID":            {"auth.binding", "binding_revoke"},
	"POST /api/v1/auth/cli/bindings/:projectID/:bindingID/revoke":       {"auth.binding", "binding_revoke"},
	"GET /.well-known/oauth-authorization-server":                       {"auth.mcp.metadata", "mcp_metadata"},
	"POST /api/v1/auth/mcp/oauth/register":                              {"auth.mcp.register", "mcp_register"},
	"GET /api/v1/auth/mcp/oauth/authorize":                              {"auth.mcp.authorize", "mcp_authorize"},
	"POST /api/v1/auth/mcp/oauth/token":                                 {"auth.mcp.token", "mcp_token"},
	"GET /api/v1/auth/mcp/oauth/consent/:transactionID":                 {"auth.mcp.consent", "mcp_consent_read"},
	"POST /api/v1/auth/mcp/oauth/consent/:transactionID/decision":       {"auth.mcp.consent", "mcp_consent_decision"},
	"GET /api/v1/auth/mcp/oauth/grants":                                 {"auth.mcp.grants", "mcp_grants_list"},
	"DELETE /api/v1/auth/mcp/oauth/grants/:grantID":                     {"auth.mcp.grants", "mcp_grants_revoke"},
	"GET /.well-known/oauth-protected-resource/mcp/projects/:projectID": {"auth.mcp.resource", "mcp_resource"},
	"POST /mcp/projects/:projectID":                                     {"auth.mcp.resource", "mcp_resource"},
	"GET /mcp/projects/:projectID":                                      {"auth.mcp.resource", "mcp_resource"},
	"DELETE /mcp/projects/:projectID":                                   {"auth.mcp.resource", "mcp_resource"},
}

func authRouteObservation(method, route string) (spanName, operation string) {
	if route == "/mcp/projects/:projectID" || route == "/.well-known/oauth-protected-resource/mcp/projects/:projectID" {
		if observation, ok := authRouteObservations[method+" "+route]; ok {
			return observation.spanName, observation.operation
		}
		return "auth.mcp.resource", "mcp_resource"
	}
	if observation, ok := authRouteObservations[method+" "+route]; ok {
		return observation.spanName, observation.operation
	}
	return "auth.request", "other"
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

// SetPipelineSettlementOutcome records only a finite result from an actual
// quota settlement transaction. Transaction errors take precedence over any
// value observed inside a retried closure.
func SetPipelineSettlementOutcome(span trace.Span, settlement string, applied bool, err error) {
	if span == nil || !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.String("pipeline.settlement.outcome", PipelineSettlementOutcome(settlement, applied, err)))
}

// PipelineSettlementOutcome maps provider-independent settlement state to a
// finite label so recovery spans can share the result without exposing raw data.
func PipelineSettlementOutcome(settlement string, applied bool, err error) string {
	outcome := "unknown"
	switch {
	case err != nil:
		outcome = "failure"
	case applied:
		outcome = "committed"
	default:
		switch settlement {
		case "charged", "refunded":
			outcome = "retained"
		case "pending":
			outcome = "pending"
		case "not_applicable":
			outcome = "not_applicable"
		case "no_op":
			outcome = "no_op"
		}
	}
	return outcome
}

// SetPipelineRecoveryTransactionOutcome records whether an outer recovery
// transaction actually updated its record after RunTransaction returned.
func SetPipelineRecoveryTransactionOutcome(span trace.Span, outcome string, err error) {
	if span == nil || !span.IsRecording() {
		return
	}
	if err != nil {
		outcome = "failure"
	}
	switch outcome {
	case "committed", "retained", "no_op", "pending", "not_applicable", "failure", "unknown":
	default:
		outcome = "unknown"
	}
	span.SetAttributes(attribute.String("pipeline.recovery.transaction.outcome", outcome))
}

// SetPipelineRecoveryStatus maps persisted terminal states to finite values.
func SetPipelineRecoveryStatus(span trace.Span, status string) {
	if span == nil || !span.IsRecording() {
		return
	}
	var outcome string
	switch status {
	case "SUCCEEDED":
		outcome = "success"
	case "FAILED":
		outcome = "failure"
	case "CANCELLED":
		outcome = "canceled"
	case "UNKNOWN":
		outcome = "unknown"
	case "RUNNING", "PENDING":
		outcome = "pending"
	default:
		outcome = "other"
	}
	span.SetAttributes(attribute.String("pipeline.recovery.status", outcome))
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
	"pipeline.recovery.result": {}, "pipeline.recovery.lookup": {}, "pipeline.recovery.transaction": {},
}

// WithPipelineDetailBudget limits reservation/result-level OTel details for
// one reconciliation sweep without changing the work performed by that sweep.
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
	if allowedSpan(span.InstrumentationScope().Name, span.Name()) {
		p.next.OnStart(ctx, span)
	}
}

func (p querySpanExportProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if allowedSpan(span.InstrumentationScope().Name, span.Name()) {
		safe := privacySafeSpan{ReadOnlySpan: span}
		if span.InstrumentationScope().Name == dataInstrumentationScope {
			p.next.OnEnd(dataSafeSpan{ReadOnlySpan: safe})
			return
		}
		p.next.OnEnd(safe)
	}
}

func (p querySpanExportProcessor) Shutdown(ctx context.Context) error {
	return p.next.Shutdown(ctx)
}

func (p querySpanExportProcessor) ForceFlush(ctx context.Context) error {
	return p.next.ForceFlush(ctx)
}

func allowedInstrumentationScope(name string) bool {
	return name == queryInstrumentationScope || name == pipelineInstrumentationScope || name == workerInstrumentationScope ||
		name == authInstrumentationScope || name == profileInstrumentationScope || name == exportInstrumentationScope ||
		name == dataInstrumentationScope || name == requestInstrumentationScope
}

func allowedSpan(scope, name string) bool {
	switch scope {
	case requestInstrumentationScope:
		return name == "http.request" || name == "mcp.request"
	case authInstrumentationScope:
		_, ok := authSpanNames[name]
		return ok
	case profileInstrumentationScope:
		_, ok := profileSpanNames[name]
		return ok
	case exportInstrumentationScope:
		_, ok := exportSpanNames[name]
		return ok
	case dataInstrumentationScope:
		_, ok := dataSpanNames[name]
		return ok
	default:
		return allowedInstrumentationScope(scope)
	}
}

func ShutdownTraces(provider *sdktrace.TracerProvider) {
	if provider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	}
}
