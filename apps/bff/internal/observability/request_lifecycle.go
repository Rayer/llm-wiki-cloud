package observability

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const requestIDHeader = "X-Request-ID"
const requestInstrumentationScope = "llm-wiki-http/request"

type requestIDContextKey struct{}
type requestObservationKey struct{}

type requestObservation struct {
	mu            sync.Mutex
	protocolError bool
}

// MarkRequestProtocolError marks a transport-level protocol failure when the
// HTTP transport itself completed with a successful status (for example an
// MCP JSON-RPC error response). It records no protocol body or message.
func MarkRequestProtocolError(ctx context.Context) {
	state, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.protocolError = true
	state.mu.Unlock()
}

// RequestIDFromContext returns only a server-issued request identifier.
func RequestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// WithRequestID carries a valid server-issued identifier across owned async work.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if uuid.Validate(requestID) != nil {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// WithoutRequestID clears an unrelated parent request ID before handling work
// whose durable origin metadata is absent.
func WithoutRequestID(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, "")
}

// SetRequestIDAttribute adds only the bounded operational identifier to a span.
func SetRequestIDAttribute(span trace.Span, requestID string) {
	if span == nil || !span.IsRecording() || uuid.Validate(requestID) != nil {
		return
	}
	span.SetAttributes(attribute.String("request.id", requestID))
}

func StartMCPRequestSpan(ctx context.Context, method, requestID, envelopeID string) (context.Context, trace.Span, string) {
	operation := "other"
	switch method {
	case "initialize", "ping", "tools/list", "tools/call", "resources/list", "resources/read", "prompts/list", "prompts/get",
		"notifications/initialized", "notifications/cancelled", "notifications/progress", "notifications/message":
		operation = method
	}
	ctx, span := otel.Tracer(requestInstrumentationScope).Start(ctx, "mcp.request",
		trace.WithAttributes(
			attribute.String("mcp.operation", operation),
			attribute.String("request.envelope_id", envelopeID),
		),
	)
	SetRequestIDAttribute(span, requestID)
	return ctx, span, operation
}

func FinishMCPRequestSpan(span trace.Span, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	switch outcome {
	case "success", "error", "rejected":
	default:
		outcome = "error"
	}
	span.SetAttributes(attribute.String("request.phase", "terminal"), attribute.String("request.outcome", outcome))
	if outcome == "error" {
		span.SetStatus(codes.Error, "error")
	}
	span.End()
}

// RequestLifecycleMiddleware extracts upstream trace context once, starts a
// finite ingress span before route guards, and emits one safe terminal record.
func RequestLifecycleMiddleware() gin.HandlerFunc {
	InstallTracePropagator()
	return func(c *gin.Context) {
		ctx := ExtractTraceContext(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		requestID := uuid.NewString()
		ctx = WithRequestID(ctx, requestID)
		observation := &requestObservation{}
		ctx = context.WithValue(ctx, requestObservationKey{}, observation)
		ctx, span := otel.Tracer(requestInstrumentationScope).Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
		SetRequestIDAttribute(span, requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Header(requestIDHeader, requestID)

		finalized := false
		finalize := func(status int, panicked bool) {
			if finalized {
				return
			}
			finalized = true
			if panicked && !c.Writer.Written() {
				// Gin's outer Recovery middleware will produce this status after
				// this middleware unwinds. Reporting it here keeps the request
				// observation finite without changing the recovery response.
				status = http.StatusInternalServerError
			}
			route := c.FullPath()
			if route == "" {
				switch {
				case c.Request.Method == http.MethodOptions:
					route = "control.preflight"
				case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
					route = "control.unmatched"
				default:
					route = "control.fallback"
				}
			}
			outcome := "success"
			switch {
			case panicked || status >= 500:
				outcome = "failure"
				span.SetStatus(codes.Error, "failure")
			case status >= 400:
				outcome = "rejected"
			default:
				observation.mu.Lock()
				protocolError := observation.protocolError
				observation.mu.Unlock()
				if protocolError {
					outcome = "protocol_error"
					span.SetStatus(codes.Error, "protocol_error")
				}
			}
			span.SetAttributes(
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status),
				attribute.String("request.phase", "terminal"),
				attribute.String("request.outcome", outcome),
			)
			spanContext := span.SpanContext()
			span.End()
			slog.InfoContext(ctx, "http.request.terminal",
				"request_id", requestID,
				"trace_id", spanContext.TraceID().String(),
				"span_id", spanContext.SpanID().String(),
				"route", route,
				"status_code", status,
				"phase", "terminal",
				"outcome", outcome,
			)
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				finalize(c.Writer.Status(), true)
				panic(recovered)
			}
			finalize(c.Writer.Status(), false)
		}()
		c.Next()
	}
}
