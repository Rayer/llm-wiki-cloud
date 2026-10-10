package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type queryDiagnosticContextKey struct{}

type queryDiagnostic struct {
	mu           sync.Mutex
	id           string
	transport    string
	projectID    string
	generationID string
	stage        string
	reason       string
	code         string
	outcome      string
	business     string
	toolIsError  bool
	span         trace.Span
	receipt      *query.ReceiptRecorder
	finalized    bool
}

type queryStageError struct {
	stage string
	err   error
}

func (e *queryStageError) Error() string { return "query stage failed" }
func (e *queryStageError) Unwrap() error { return e.err }

func queryStageFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &queryStageError{stage: stage, err: err}
}

func startQueryStage(ctx context.Context, stage string) (context.Context, trace.Span) {
	return otel.Tracer("llm-wiki-bff/query").Start(ctx, "query."+stage,
		trace.WithAttributes(attribute.String("query.stage", stage)),
	)
}

func finishQueryStage(span trace.Span, err error) {
	if err != nil {
		span.SetAttributes(attribute.String("query.outcome", "failure"))
		span.SetStatus(codes.Error, "failure")
	} else {
		span.SetAttributes(attribute.String("query.outcome", "success"))
	}
	span.End()
}

func beginQueryDiagnostic(ctx context.Context, transport, projectID string) (context.Context, *queryDiagnostic) {
	build := buildinfo.Current()
	id := uuid.NewString()
	ctx, span := otel.Tracer("llm-wiki-bff/query").Start(ctx, "query.request",
		trace.WithAttributes(
			attribute.String("diagnostic_id", id),
			attribute.String("service", build.Service),
			attribute.String("revision", build.Revision),
			attribute.String("project_id", projectID),
			attribute.String("query.transport", transport),
		),
	)
	diagnostic := &queryDiagnostic{
		id: id, transport: transport, projectID: projectID, stage: "adapter",
		reason: "unknown", code: "query_unavailable", outcome: "error", business: "error", span: span,
	}
	ctx = context.WithValue(ctx, queryDiagnosticContextKey{}, diagnostic)
	receiptCtx, receipt := query.WithReceipt(ctx)
	diagnostic.receipt = receipt
	return receiptCtx, diagnostic
}

func queryDiagnosticFrom(ctx context.Context) *queryDiagnostic {
	diagnostic, _ := ctx.Value(queryDiagnosticContextKey{}).(*queryDiagnostic)
	return diagnostic
}

func (d *queryDiagnostic) setStage(stage string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.stage = stage
	d.mu.Unlock()
}

func (d *queryDiagnostic) setGeneration(id string) {
	if d == nil || id == "" {
		return
	}
	d.mu.Lock()
	d.generationID = id
	d.mu.Unlock()
	d.span.SetAttributes(attribute.String("generation_id", id))
}

func (d *queryDiagnostic) noteCurrentManifest(reader storage.Store) {
	if d == nil {
		return
	}
	client, ok := reader.(*gcs.Client)
	if !ok || client.ViewToken() != "legacy" {
		return
	}
	// GCS assigns this token only after this request's one current-manifest read
	// returned exists=false; an unpinned view has an empty token.
	d.span.SetAttributes(attribute.Bool("generation.manifest_exists", false))
}

func (d *queryDiagnostic) capturePinnedGeneration(ctx context.Context, reader storage.Store) {
	if d == nil {
		return
	}
	identityProvider, ok := reader.(storage.QueryGenerationIdentityProvider)
	if !ok {
		return
	}
	identity, err := identityProvider.QueryGenerationIdentity(ctx)
	if err == nil {
		d.setGeneration(identity.GenerationID)
	}
}

func (d *queryDiagnostic) fail(failure *queryFailure) {
	if d == nil || failure == nil {
		return
	}
	d.mu.Lock()
	d.stage, d.reason, d.code, d.outcome, d.business = failure.stage, failure.reason, failure.code, "error", "error"
	d.mu.Unlock()
	d.span.SetAttributes(
		attribute.String("query.stage", failure.stage),
		attribute.String("query.reason", failure.reason),
		attribute.String("query.code", failure.code),
		attribute.String("query.outcome", "error"),
	)
	d.span.SetStatus(codes.Error, failure.reason)
}

func (d *queryDiagnostic) succeed(responseStatus, responseReason string, receipt query.Receipt) {
	if d == nil {
		return
	}
	outcome, reason := "success", "completed"
	if responseStatus == "insufficient_evidence" && responseReason == "no_qualified_evidence" {
		outcome, reason = "no_evidence", "no_qualified_evidence"
	} else {
		for _, stage := range receipt.Stages {
			if stage.Outcome == "fallback" {
				outcome, reason = "degraded", "deterministic_fallback"
				break
			}
		}
	}
	d.mu.Lock()
	d.stage, d.reason, d.code, d.outcome, d.business = "complete", reason, reason, outcome, outcome
	d.mu.Unlock()
	d.span.SetAttributes(
		attribute.String("query.stage", "complete"),
		attribute.String("query.reason", reason),
		attribute.String("query.code", reason),
		attribute.String("query.outcome", outcome),
		attribute.String("query.business_outcome", outcome),
	)
}

func (d *queryDiagnostic) finish(requestStatus int, toolIsError bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.finalized {
		d.mu.Unlock()
		return
	}
	d.finalized = true
	d.toolIsError = toolIsError
	if toolIsError {
		d.outcome, d.business = "error", "error"
	}
	spanContext := d.span.SpanContext()
	build := buildinfo.Current()
	summary := query.TerminalSummary{
		DiagnosticID: d.id, TraceID: spanContext.TraceID().String(), SpanID: spanContext.SpanID().String(),
		Service: build.Service, Revision: build.Revision, ProjectID: d.projectID, GenerationID: d.generationID,
		Transport: d.transport, Stage: d.stage, Reason: d.reason, Outcome: d.outcome, Business: d.business,
		HTTPStatus: requestStatus, ToolIsError: toolIsError,
	}
	d.mu.Unlock()
	d.span.SetAttributes(attribute.Int("http.response.status_code", requestStatus), attribute.Bool("mcp.is_error", toolIsError))
	d.receipt.SetTerminal(summary)
	query.FinishReceipt(d.receipt)
	d.span.End()
}

type queryProblemDetails struct {
	Type         string `json:"type"`
	Title        string `json:"title"`
	Status       int    `json:"status"`
	Detail       string `json:"detail"`
	Code         string `json:"code"`
	DiagnosticID string `json:"diagnostic_id"`
	Error        string `json:"error"`
}

func writeQueryProblem(c *gin.Context, failure *queryFailure, diagnosticID string) {
	body, _ := json.Marshal(queryProblemDetails{
		Type: "urn:lwc:query:failure", Title: "Query failed", Status: failure.status,
		Detail: failure.message, Code: failure.code, DiagnosticID: diagnosticID, Error: failure.message,
	})
	c.Header("Content-Type", "application/problem+json")
	c.Status(failure.status)
	_, _ = c.Writer.Write(body)
}

func queryFailureFor(stage string, cause error, reader storage.Store) *queryFailure {
	if cause == nil {
		cause = errors.New("unknown query failure")
	}
	if wrapped := new(queryStageError); errors.As(cause, &wrapped) {
		stage = wrapped.stage
	}
	for current := cause; current != nil; current = errors.Unwrap(current) {
		if wrapped, ok := current.(*queryStageError); ok {
			cause = wrapped.err
			break
		}
	}
	failure := &queryFailure{status: http.StatusInternalServerError, stage: stage, reason: "unknown", code: "query_unavailable", message: "The query could not be completed. Try again, and share the diagnostic ID if the problem continues.", cause: cause}
	switch {
	case errors.Is(cause, errProfileProjectNotFound):
		failure.status, failure.reason, failure.code, failure.message = http.StatusNotFound, "project_not_found", "project_not_found", "This project could not be found. Check the selected project and try again."
	case errors.Is(cause, context.Canceled):
		failure.reason, failure.code, failure.message = "canceled", "query_canceled", "The query was canceled. Submit it again when ready."
	case errors.Is(cause, context.DeadlineExceeded):
		failure.reason, failure.code, failure.message = "timeout", "query_timeout", "The query took too long. Try again."
	case isMissingPublishedGenerationFailure(cause, hasMissingCurrentManifest(reader)):
		failure.reason, failure.code, failure.message = "published_generation_missing", "published_generation_missing", "This project has no published wiki generation yet. Run the pipeline, then retry the search."
	case errors.Is(cause, query.ErrUnsupportedRequired):
		failure.status, failure.reason, failure.code, failure.message = http.StatusUnprocessableEntity, "unsupported_profile_condition", "unsupported_profile_condition", "A required profile condition is not supported by the query runtime. Update the profile requirements or contact support."
	case errors.Is(cause, query.ErrCacheNotConfigured):
		failure.reason, failure.code, failure.message = "query_index_unavailable", "query_index_unavailable", "The query index is temporarily unavailable. Try again later."
	case stage == "profile_state" || stage == "profile_candidate" || stage == "profile_snapshot" || stage == "profile_resolution":
		failure.reason, failure.code, failure.message = "profile_unavailable", "profile_unavailable", "The active project profile could not be loaded. Try again after the profile is available."
	case stage == "generation_pin" || stage == "generation_resolution":
		failure.reason, failure.code, failure.message = "storage_unavailable", "storage_unavailable", "The project's published wiki data is temporarily unavailable. Try again."
	case isProviderCallFailure(cause):
		failure.reason, failure.code, failure.message = llm.SafeErrorCategory(cause), "query_provider_failed", "The query provider could not complete this search. Try again later."
	case errors.Is(cause, queryruntime.ErrIdentityUnavailable) || errors.Is(cause, queryruntime.ErrIdentityProviderRequired):
		failure.reason, failure.code, failure.message = "runtime_identity_unavailable", "query_runtime_unavailable", "The query runtime could not be matched to this project's published data. Try again later."
	case stage == "runtime" || stage == "executor":
		failure.reason, failure.code, failure.message = "runtime_failure", "query_runtime_unavailable", "The query could not run with the current configuration. Try again later."
	case stage == "request_decode" || stage == "request_validation":
		failure.status, failure.reason, failure.code, failure.message = http.StatusBadRequest, "invalid_request", "invalid_query", "The search request is invalid. Check its format and try again."
	}
	return failure
}

func hasMissingCurrentManifest(reader storage.Store) bool {
	client, ok := reader.(*gcs.Client)
	return ok && client.ViewToken() == "legacy"
}

func isMissingPublishedGenerationFailure(cause error, manifestMissing bool) bool {
	return manifestMissing && (errors.Is(cause, storage.ErrQueryGenerationIdentityUnavailable) || errors.Is(cause, storage.ErrQueryGenerationUnpinned))
}

func isProviderCallFailure(err error) bool {
	var providerErr *llm.ProviderCallError
	return errors.As(err, &providerErr)
}

type mcpQueryObservationKey struct{}

type mcpQueryObservation struct {
	mu             sync.Mutex
	diagnostic     *queryDiagnostic
	toolError      bool
	handlerEntered bool
}

func (o *mcpQueryObservation) start(diagnostic *queryDiagnostic) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.diagnostic = diagnostic
	o.mu.Unlock()
}

func (o *mcpQueryObservation) set(diagnostic *queryDiagnostic, toolError bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.diagnostic, o.toolError, o.handlerEntered = diagnostic, toolError, true
	o.mu.Unlock()
}

func (o *mcpQueryObservation) finish(status int) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	diagnostic, toolError, handlerEntered := o.diagnostic, o.toolError, o.handlerEntered
	o.mu.Unlock()
	if diagnostic != nil {
		if !handlerEntered {
			diagnostic.fail(queryFailureFor("request_validation", errors.New("MCP input schema validation failed"), nil))
			toolError = true
		}
		diagnostic.finish(status, toolError)
	}
	return diagnostic != nil && !handlerEntered
}

func mcpQueryObservationFrom(ctx context.Context) *mcpQueryObservation {
	observation, _ := ctx.Value(mcpQueryObservationKey{}).(*mcpQueryObservation)
	return observation
}

type queryStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *queryStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *queryStatusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *queryStatusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *queryStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
