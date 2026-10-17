package observability

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const cloudTraceContextHeader = "X-Cloud-Trace-Context"

var installPropagatorOnce sync.Once

type traceContextPropagator struct{ w3c propagation.TraceContext }

func (p traceContextPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		carrier.Set("traceparent", formatTraceParent(sc))
	}
}

func (p traceContextPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	w3cContext := p.w3c.Extract(context.Background(), carrier)
	if sc := trace.SpanContextFromContext(w3cContext); sc.IsValid() {
		return trace.ContextWithRemoteSpanContext(ctx, sc)
	}
	if sc, ok := parseCloudTraceContext(carrier.Get(cloudTraceContextHeader)); ok {
		return trace.ContextWithRemoteSpanContext(ctx, sc)
	}
	return ctx
}

func (traceContextPropagator) Fields() []string {
	return []string{"traceparent", cloudTraceContextHeader}
}

func InstallTracePropagator() {
	installPropagatorOnce.Do(func() {
		otel.SetTextMapPropagator(traceContextPropagator{w3c: propagation.TraceContext{}})
	})
}

func TraceContextMiddleware() gin.HandlerFunc {
	InstallTracePropagator()
	return func(c *gin.Context) {
		ctx := ExtractTraceContext(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func ExtractTraceContext(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	InstallTracePropagator()
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func InjectTraceContext(ctx context.Context, headers http.Header) {
	InstallTracePropagator()
	headers.Del("tracestate")
	headers.Del("baggage")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(headers))
}

func ExtractWorkerTraceContext(ctx context.Context, traceparent string) context.Context {
	sc, ok := parseTraceParent(traceparent)
	if !ok {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}

func TraceParentFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return formatTraceParent(sc)
}

func formatTraceParent(sc trace.SpanContext) string {
	return fmt.Sprintf("00-%s-%s-%02x", sc.TraceID(), sc.SpanID(), uint8(sc.TraceFlags()))
}

func parseTraceParent(value string) (trace.SpanContext, bool) {
	if strings.TrimSpace(value) == "" {
		return trace.SpanContext{}, false
	}
	ctx := (propagation.TraceContext{}).Extract(context.Background(), propagation.MapCarrier{"traceparent": value})
	sc := trace.SpanContextFromContext(ctx)
	return sc, sc.IsValid() && sc.IsRemote()
}

func parseCloudTraceContext(value string) (trace.SpanContext, bool) {
	parts := strings.Split(value, ";")
	if len(parts) > 2 {
		return trace.SpanContext{}, false
	}
	ids := strings.Split(parts[0], "/")
	if len(ids) != 2 {
		return trace.SpanContext{}, false
	}
	traceID, err := trace.TraceIDFromHex(ids[0])
	if err != nil {
		return trace.SpanContext{}, false
	}
	spanIDValue, err := strconv.ParseUint(ids[1], 10, 64)
	if err != nil || spanIDValue == 0 {
		return trace.SpanContext{}, false
	}
	spanID, err := trace.SpanIDFromHex(fmt.Sprintf("%016x", spanIDValue))
	if err != nil {
		return trace.SpanContext{}, false
	}
	flags := trace.TraceFlags(0)
	if len(parts) == 2 {
		option, ok := strings.CutPrefix(parts[1], "o=")
		if !ok {
			return trace.SpanContext{}, false
		}
		options, parseErr := strconv.ParseUint(option, 10, 64)
		if parseErr != nil {
			return trace.SpanContext{}, false
		}
		if options&1 == 1 {
			flags = trace.FlagsSampled
		}
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: flags, Remote: true,
	})
	return sc, sc.IsValid()
}

func StartWorkerAttemptSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return startLinkedRoot(ctx, workerInstrumentationScope, name)
}

func StartExportAttemptSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return startLinkedRoot(ctx, exportInstrumentationScope, name)
}

func StartProfileWorkSpan(ctx context.Context) (context.Context, trace.Span) {
	return startLinkedRoot(ctx, profileInstrumentationScope, "profile.work")
}

func startLinkedRoot(ctx context.Context, scope, name string) (context.Context, trace.Span) {
	parent := trace.SpanContextFromContext(ctx)
	root := WithoutTraceParent(ctx)
	options := []trace.SpanStartOption{trace.WithNewRoot()}
	if parent.IsValid() {
		options = append(options, trace.WithLinks(trace.Link{SpanContext: parent}))
	}
	root, span := otel.Tracer(scope).Start(root, name, options...)
	SetRequestIDAttribute(span, RequestIDFromContext(ctx))
	return root, span
}

var _ propagation.TextMapPropagator = traceContextPropagator{}
