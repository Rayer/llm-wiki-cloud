package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestInternalEventsUseBoundedMetricDimensions(t *testing.T) {
	ctx := context.Background()
	previous := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(ctx)
	})

	RecordURLIntakeEvent(ctx, "quality", "configuration", "failure")
	RecordURLIntakeEvent(ctx, "quality", "configuration", "fallback")
	RecordURLIntakeEvent(ctx, "setup", "configuration", "failure")
	RecordURLIntakeEvent(ctx, "handler", "internal", "failure")
	RecordURLIntakeEvent(ctx, "https://private.example/path", "actor-123", "request-456")
	RecordQueryAIOperationFailure(ctx, "query_expansion", "authentication", "deterministic_plan")
	RecordQueryAIOperationFailure(ctx, "https://private.example/path", "secret-provider-body", "request-456")

	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	var points []metricdata.DataPoint[int64]
	var queryPoints []metricdata.DataPoint[int64]
	for _, scope := range data.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name == "lwc.url_intake.internal_events" || item.Name == "lwc.query.ai_operation_failures" {
				sum, ok := item.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric data = %T, want sum", item.Data)
				}
				if item.Name == "lwc.url_intake.internal_events" {
					points = append(points, sum.DataPoints...)
				} else {
					queryPoints = append(queryPoints, sum.DataPoints...)
				}
			}
		}
	}
	if len(points) != 5 {
		t.Fatalf("metric points = %d, want five bounded series", len(points))
	}
	seen := map[string]int64{}
	for _, point := range points {
		stage, _ := point.Attributes.Value(attribute.Key("stage"))
		class, _ := point.Attributes.Value(attribute.Key("error_class"))
		outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
		seen[stage.AsString()+"/"+class.AsString()+"/"+outcome.AsString()] = point.Value
	}
	if seen["quality/configuration/failure"] != 1 || seen["quality/configuration/fallback"] != 1 || seen["setup/configuration/failure"] != 1 || seen["handler/internal/failure"] != 1 || seen["other/other/other"] != 1 {
		t.Fatalf("metric series = %#v", seen)
	}
	if len(queryPoints) != 2 {
		t.Fatalf("query failure metric points = %d, want two bounded series", len(queryPoints))
	}
	querySeen := map[string]int64{}
	for _, point := range queryPoints {
		stage, _ := point.Attributes.Value(attribute.Key("stage"))
		class, _ := point.Attributes.Value(attribute.Key("failure_class"))
		fallback, _ := point.Attributes.Value(attribute.Key("fallback_outcome"))
		querySeen[stage.AsString()+"/"+class.AsString()+"/"+fallback.AsString()] = point.Value
	}
	if querySeen["query_expansion/authentication/deterministic_plan"] != 1 || querySeen["other/other/other"] != 1 {
		t.Fatalf("query failure metric series = %#v", querySeen)
	}
}
