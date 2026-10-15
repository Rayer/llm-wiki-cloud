package observability

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var dataSpanNames = stringSet(
	"bff.data.operation", "bff.data.authorize", "bff.data.input", "bff.data.load", "bff.data.cache",
	"bff.data.validate", "bff.data.read", "bff.data.write", "bff.data.response", "bff.data.transfer",
	"bff.data.placeholder",
)

var dataOperations = map[string]string{
	"GET /api/v1/projects":               "project.list",
	"GET /api/v1/sources":                "source.list",
	"GET /api/v1/sources/:id":            "source.read",
	"GET /api/v1/concepts":               "concept.list",
	"GET /api/v1/concepts/:id":           "concept.read",
	"GET /api/v1/sources/:id/annotation": "annotation.read",
	"PUT /api/v1/sources/:id/annotation": "annotation.write",
	"GET /api/v1/raw":                    "raw.list",
	"GET /api/v1/raw/:filename":          "raw.preview",
	"POST /api/v1/raw/upload":            "raw.upload",
	"GET /api/v1/sync/raw":               "raw_sync.list",
	"GET /api/v1/sync/raw/file":          "raw_sync.read",
	"PUT /api/v1/sync/raw/file":          "raw_sync.write",
	"POST /api/v1/import":                "import.validate",
}

var dataOperationValues = stringSet(
	"project.list", "source.list", "source.read", "concept.list", "concept.read",
	"annotation.read", "annotation.write", "raw.list", "raw.preview", "raw.upload",
	"raw_sync.list", "raw_sync.read", "raw_sync.write", "import.validate",
)

var dataOutcomeValues = stringSet(
	"success", "failure", "rejected", "canceled", "timeout", "unknown", "committed",
	"not_committed", "no_op", "precondition_failed", "transfer_failure", "response_failure",
	"cache_hit", "cache_miss", "placeholder", "skipped", "redirected",
)

type dataOperationKey struct{}

type dataOperationState struct {
	mu        sync.Mutex
	operation string
	outcome   string
	root      trace.Span
}

// DataOperationMiddleware starts one operation only for the frozen mapped
// route templates. It runs before the BFF and project guards, so their rejects
// remain visible without recording raw URL data.
func DataOperationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		operation := DataOperationForRoute(c.Request.Method, c.FullPath())
		if operation == "" {
			c.Next()
			return
		}
		ctx, span := otel.Tracer(dataInstrumentationScope).Start(c.Request.Context(), "bff.data.operation")
		span.SetAttributes(attribute.String("bff.operation", operation))
		state := &dataOperationState{operation: operation, root: span}
		ctx = context.WithValue(ctx, dataOperationKey{}, state)
		c.Request = c.Request.WithContext(ctx)
		c.Next()

		state.mu.Lock()
		outcome := state.outcome
		state.mu.Unlock()
		if outcome == "" {
			outcome = dataHTTPOutcome(c.Writer.Status())
		}
		FinishDataSpan(span, outcome)
	}
}

// DataOperationForRoute returns the finite owner for a registered Gin route.
func DataOperationForRoute(method, route string) string {
	return dataOperations[method+" "+route]
}

func dataStateFromContext(ctx context.Context) *dataOperationState {
	state, _ := ctx.Value(dataOperationKey{}).(*dataOperationState)
	return state
}

func selectedMutationOperation(operation string) bool {
	switch operation {
	case "annotation.write", "raw.upload", "raw_sync.write":
		return true
	default:
		return false
	}
}

// WriteDataJSON records the actual JSON response work for selected mutations.
// It leaves Gin's status, body, and render errors untouched while safely
// distinguishing response failures from the logical mutation outcome.
func WriteDataJSON(c *gin.Context, status int, value any) {
	if c.Request == nil {
		c.JSON(status, value)
		return
	}
	state := dataStateFromContext(c.Request.Context())
	if state == nil || !selectedMutationOperation(state.operation) {
		c.JSON(status, value)
		return
	}
	_, responseSpan := StartDataPhase(c.Request.Context(), "response")
	renderErrorsBefore := len(c.Errors)
	c.JSON(status, value)
	outcome := "success"
	if len(c.Errors) > renderErrorsBefore {
		outcome = "response_failure"
	}
	FinishDataSpan(responseSpan, outcome)
}

// StartDataPhase records a fixed phase only when it belongs to a mapped I4
// request. The returned context preserves the operation parent for real SDK
// child spans.
func StartDataPhase(ctx context.Context, phase string) (context.Context, trace.Span) {
	state := dataStateFromContext(ctx)
	if state == nil {
		return ctx, trace.SpanFromContext(context.Background())
	}
	name := "bff.data." + phase
	if _, ok := dataSpanNames[name]; !ok || name == "bff.data.operation" {
		return ctx, trace.SpanFromContext(context.Background())
	}
	phaseCtx, span := otel.Tracer(dataInstrumentationScope).Start(trace.ContextWithSpan(ctx, state.root), name)
	span.SetAttributes(attribute.String("bff.operation", state.operation))
	return phaseCtx, span
}

// SetDataOperationOutcome records a fixed logical result that may differ from
// the HTTP status, such as a successful write followed by projection failure.
func SetDataOperationOutcome(ctx context.Context, outcome string) {
	state := dataStateFromContext(ctx)
	if state == nil {
		return
	}
	if _, ok := dataOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	state.mu.Lock()
	state.outcome = outcome
	state.mu.Unlock()
}

// SetDataCount records an existing aggregate count under a fixed safe key.
func SetDataCount(span trace.Span, key string, count int64) {
	if span == nil || !span.IsRecording() || count < 0 || count > 1<<40 {
		return
	}
	if key != "items" && key != "bytes" {
		return
	}
	span.SetAttributes(attribute.Int64("bff.count."+key, count))
}

func SetDataCacheHit(span trace.Span, hit bool) {
	if span != nil && span.IsRecording() {
		span.SetAttributes(attribute.Bool("bff.cache.hit", hit))
	}
}

func FinishDataSpan(span trace.Span, outcome string) {
	if span == nil || !span.IsRecording() {
		return
	}
	if _, ok := dataOutcomeValues[outcome]; !ok {
		outcome = "unknown"
	}
	span.SetAttributes(attribute.String("bff.outcome", outcome))
	if outcome == "failure" || outcome == "timeout" || outcome == "canceled" || outcome == "transfer_failure" || outcome == "response_failure" {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
}

func dataHTTPOutcome(status int) string {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return "success"
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		return "rejected"
	case status >= http.StatusInternalServerError:
		return "failure"
	default:
		return "unknown"
	}
}

func DataErrorOutcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "failure"
	}
}

type dataSafeSpan struct{ sdktrace.ReadOnlySpan }

func (s dataSafeSpan) Attributes() []attribute.KeyValue {
	attributes := s.ReadOnlySpan.Attributes()
	safe := make([]attribute.KeyValue, 0, len(attributes))
	for _, item := range attributes {
		switch string(item.Key) {
		case "bff.operation":
			if _, ok := dataOperationValues[item.Value.AsString()]; ok && item.Value.Type() == attribute.STRING {
				safe = append(safe, item)
			}
		case "bff.outcome":
			if _, ok := dataOutcomeValues[item.Value.AsString()]; ok && item.Value.Type() == attribute.STRING {
				safe = append(safe, item)
			}
		case "bff.count.items", "bff.count.bytes":
			if item.Value.Type() == attribute.INT64 && item.Value.AsInt64() >= 0 && item.Value.AsInt64() <= 1<<40 {
				safe = append(safe, item)
			}
		case "bff.cache.hit":
			if item.Value.Type() == attribute.BOOL {
				safe = append(safe, item)
			}
		}
	}
	return safe
}

func (s dataSafeSpan) Events() []sdktrace.Event { return nil }
func (s dataSafeSpan) Links() []sdktrace.Link   { return nil }
func (s dataSafeSpan) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	status.Description = ""
	return status
}
