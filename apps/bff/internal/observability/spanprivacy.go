package observability

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// privacySafeSpan removes propagation state and unreviewed link/event payloads
// while retaining the actual IDs, parent IDs, status code and causal links.
type privacySafeSpan struct{ sdktrace.ReadOnlySpan }

func (s privacySafeSpan) SpanContext() trace.SpanContext {
	return safeSpanContext(s.ReadOnlySpan.SpanContext())
}

func (s privacySafeSpan) Parent() trace.SpanContext {
	return safeSpanContext(s.ReadOnlySpan.Parent())
}

func (s privacySafeSpan) Links() []sdktrace.Link {
	links := s.ReadOnlySpan.Links()
	if len(links) == 0 {
		return nil
	}
	safe := make([]sdktrace.Link, 0, len(links))
	for _, link := range links {
		dropped := link.DroppedAttributeCount + len(link.Attributes)
		safe = append(safe, sdktrace.Link{
			SpanContext: safeSpanContext(link.SpanContext), DroppedAttributeCount: dropped,
		})
	}
	return safe
}

func (s privacySafeSpan) Events() []sdktrace.Event {
	for _, event := range s.ReadOnlySpan.Events() {
		if event.Name != "query.citation.validation" {
			continue
		}
		attrs := make([]attribute.KeyValue, 0, len(event.Attributes))
		for _, item := range event.Attributes {
			switch string(item.Key) {
			case "query.citation.outcome":
				if item.Value.Type() == attribute.STRING && (item.Value.AsString() == "none" || item.Value.AsString() == "resolved") {
					attrs = append(attrs, item)
				}
			case "query.citation.resolved_count", "query.citation.authorized_result_count":
				if item.Value.Type() == attribute.INT64 && item.Value.AsInt64() >= 0 && item.Value.AsInt64() <= 100000 {
					attrs = append(attrs, item)
				}
			}
		}
		return []sdktrace.Event{{Name: event.Name, Time: event.Time, Attributes: attrs}}
	}
	return nil
}

func (s privacySafeSpan) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	if status.Code != codes.Error {
		status.Description = ""
		return status
	}
	switch status.Description {
	case "failure", "timeout", "canceled", "provider_error", "error", "response_failure", "transfer_failure":
	default:
		status.Description = "error"
	}
	return status
}

func safeSpanContext(sc trace.SpanContext) trace.SpanContext {
	if !sc.IsValid() {
		return trace.SpanContext{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: sc.TraceID(), SpanID: sc.SpanID(), TraceFlags: sc.TraceFlags(), Remote: sc.IsRemote(),
	})
}
