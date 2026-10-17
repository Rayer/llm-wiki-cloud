package observability

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
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
	once          sync.Once
	protocolError bool
	requestID     string
	method        string
	ctx           context.Context
	span          trace.Span
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

// RequestLifecycleMiddleware reuses a lifecycle created at the HTTP boundary;
// direct Gin use still receives the same observation as a safe fallback.
func RequestLifecycleMiddleware() gin.HandlerFunc {
	InstallTracePropagator()
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		observation := requestObservationFromContext(ctx)
		if observation == nil {
			ctx, observation = startRequestObservation(ctx, c.Request.Method, c.Request.Header)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Header(requestIDHeader, observation.requestID)
		defer func() {
			if recovered := recover(); recovered != nil {
				status := c.Writer.Status()
				if !c.Writer.Written() {
					status = http.StatusInternalServerError
				}
				observation.finish(status, c.FullPath(), true)
				panic(recovered)
			}
			observation.finish(c.Writer.Status(), c.FullPath(), false)
		}()
		c.Next()
	}
}

func requestObservationFromContext(ctx context.Context) *requestObservation {
	observation, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	return observation
}

func startRequestObservation(ctx context.Context, method string, headers http.Header) (context.Context, *requestObservation) {
	ctx = ExtractTraceContext(ctx, propagation.HeaderCarrier(headers))
	observation := &requestObservation{requestID: uuid.NewString(), method: method}
	ctx = WithRequestID(ctx, observation.requestID)
	ctx = context.WithValue(ctx, requestObservationKey{}, observation)
	ctx, observation.span = otel.Tracer(requestInstrumentationScope).Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
	observation.ctx = ctx
	SetRequestIDAttribute(observation.span, observation.requestID)
	return ctx, observation
}

func (observation *requestObservation) finish(status int, route string, panicked bool) {
	if observation == nil {
		return
	}
	observation.once.Do(func() {
		if route == "" {
			route = controlRequestRoute(status, observation.method)
		}
		outcome := "success"
		switch {
		case panicked || status >= 500:
			outcome = "failure"
			observation.span.SetStatus(codes.Error, "failure")
		case status >= 400:
			outcome = "rejected"
		default:
			observation.mu.Lock()
			protocolError := observation.protocolError
			observation.mu.Unlock()
			if protocolError {
				outcome = "protocol_error"
				observation.span.SetStatus(codes.Error, "protocol_error")
			}
		}
		observation.span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
			attribute.String("request.phase", "terminal"),
			attribute.String("request.outcome", outcome),
		)
		spanContext := observation.span.SpanContext()
		observation.span.End()
		slog.InfoContext(observation.ctx, "http.request.terminal",
			"request_id", observation.requestID,
			"trace_id", spanContext.TraceID().String(),
			"span_id", spanContext.SpanID().String(),
			"route", route,
			"status_code", status,
			"phase", "terminal",
			"outcome", outcome,
		)
	})
}

func controlRequestRoute(status int, method string) string {
	switch {
	case method == http.MethodOptions:
		return "control.preflight"
	case status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect:
		return "control.redirect"
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return "control.unmatched"
	default:
		return "control.fallback"
	}
}

// RequestLifecycleHandler owns HTTP exchanges that Gin may answer before
// entering its middleware chain, including automatic redirects.
func RequestLifecycleHandler(next http.Handler) http.Handler {
	InstallTracePropagator()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if observation := requestObservationFromContext(request.Context()); observation != nil {
			writer.Header().Set(requestIDHeader, observation.requestID)
			next.ServeHTTP(writer, request)
			return
		}
		ctx, observation := startRequestObservation(request.Context(), request.Method, request.Header)
		writer.Header().Set(requestIDHeader, observation.requestID)
		tracked := &requestLifecycleResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				status := tracked.status
				if !tracked.wroteHeader {
					status = http.StatusInternalServerError
				}
				observation.finish(status, "", true)
				panic(recovered)
			}
			observation.finish(tracked.status, "", false)
		}()
		next.ServeHTTP(tracked, request.WithContext(ctx))
	})
}

type requestLifecycleResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (writer *requestLifecycleResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		writer.ResponseWriter.WriteHeader(status)
		return
	}
	if writer.wroteHeader {
		return
	}
	writer.status = status
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *requestLifecycleResponseWriter) Write(data []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *requestLifecycleResponseWriter) Flush() {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *requestLifecycleResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(writer.ResponseWriter).Hijack()
}

func (writer *requestLifecycleResponseWriter) CloseNotify() <-chan bool {
	if notifier, ok := writer.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return make(chan bool)
}

func (writer *requestLifecycleResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := writer.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (writer *requestLifecycleResponseWriter) ReadFrom(source io.Reader) (int64, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(source)
	}
	return io.Copy(writer.ResponseWriter, source)
}

func (writer *requestLifecycleResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

// RequestLifecycleRouter keeps Gin's route-inspection and middleware methods
// while placing the lifecycle handler before Gin's automatic routing paths.
type RequestLifecycleRouter struct {
	*gin.Engine
	handler http.Handler
}

func WrapRequestLifecycleRouter(engine *gin.Engine) *RequestLifecycleRouter {
	return &RequestLifecycleRouter{Engine: engine, handler: RequestLifecycleHandler(engine)}
}

func (router *RequestLifecycleRouter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	router.handler.ServeHTTP(writer, request)
}

func (router *RequestLifecycleRouter) Run(addr ...string) error {
	if len(addr) > 1 {
		panic("too many parameters")
	}
	address := os.Getenv("PORT")
	if address != "" {
		address = ":" + address
	} else {
		address = ":8080"
	}
	if len(addr) == 1 {
		address = addr[0]
	}
	return http.ListenAndServe(address, router)
}
