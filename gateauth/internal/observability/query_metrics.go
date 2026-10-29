package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var queryAIOperationFailures metric.Int64Counter

func init() {
	var err error
	queryAIOperationFailures, err = otel.Meter("llm-wiki-bff/query").Int64Counter(
		"lwc.query.ai_operation_failures",
		metric.WithDescription("Recovered or terminal optional AI query operation failures"),
	)
	if err != nil {
		queryAIOperationFailures, _ = noop.NewMeterProvider().Meter("").Int64Counter("")
	}
}

// RecordQueryAIOperationFailure records only allowlisted stage, class, and
// fallback labels; request and provider data never become metric dimensions.
func RecordQueryAIOperationFailure(ctx context.Context, stage, class, fallback string) {
	queryAIOperationFailures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", boundedLabel(stage, "query_expansion", "answer_synthesis")),
		attribute.String("failure_class", boundedLabel(class, "authentication", "rate_limited", "quota_exhausted", "billing", "timeout", "invalid_response", "output_truncated", "provider_unavailable", "provider_error", "context_unavailable", "canceled")),
		attribute.String("fallback_outcome", boundedLabel(fallback, "none", "raw_query", "deterministic_plan", "retrieval_only")),
	))
}
