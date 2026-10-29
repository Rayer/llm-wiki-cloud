package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var urlIntakeEvents metric.Int64Counter

func init() {
	var err error
	urlIntakeEvents, err = otel.Meter("llm-wiki-bff/url-intake").Int64Counter(
		"lwc.url_intake.internal_events",
		metric.WithDescription("Internal URL intake dependency and storage events, including recovered failures"),
	)
	if err != nil {
		urlIntakeEvents, _ = noop.NewMeterProvider().Meter("").Int64Counter("")
	}
}

// RecordURLIntakeEvent records a low-cardinality event; unknown labels collapse
// to "other" so request and content data cannot become metric dimensions.
func RecordURLIntakeEvent(ctx context.Context, stage, errorClass, outcome string) {
	urlIntakeEvents.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", boundedLabel(stage, "setup", "handler", "fetch", "quality", "storage", "candidate", "commit")),
		attribute.String("error_class", boundedLabel(errorClass, "configuration", "timeout", "transport", "provider", "invalid_response", "dependency", "storage", "internal")),
		attribute.String("outcome", boundedLabel(outcome, "failure", "fallback", "recovered", "uncertain")),
	))
}

func boundedLabel(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}
