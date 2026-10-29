package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	mexporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric"
)

// InitMetrics sets up the OTel MeterProvider with GCP resource detection
// and the native Cloud Monitoring exporter (not generic OTLP).
func InitMetrics(ctx context.Context, serviceName, projectID string) (*sdkmetric.MeterProvider, error) {
	exporter, err := mexporter.New(
		mexporter.WithProjectID(projectID),
	)
	if err != nil {
		return nil, fmt.Errorf("metric exporter: %w", err)
	}

	// GCP resource detection, fall back to env
	res, err := resource.New(ctx,
		resource.WithDetectors(gcp.NewDetector()),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		log.Printf("[observability] resource detection failed, using env: %v", err)
		res = resource.Environment()
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(10*time.Second),
		)),
	)
	otel.SetMeterProvider(provider)
	log.Printf("[observability] OTel meter provider initialized for %s", serviceName)
	return provider, nil
}

// InitMetricsForMode keeps local emulator processes off ambient cloud metrics
// credentials while leaving the existing Cloud Monitoring setup for services.
func InitMetricsForMode(ctx context.Context, serviceName, projectID string, localMode bool) (*sdkmetric.MeterProvider, error) {
	if localMode {
		return InitLocalMetrics(serviceName), nil
	}
	return InitMetrics(ctx, serviceName, projectID)
}

// InitLocalMetrics installs an in-process meter provider without cloud export.
// Local emulator runs must never consult ambient Google credentials.
func InitLocalMetrics(serviceName string) *sdkmetric.MeterProvider {
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource.NewSchemaless(semconv.ServiceName(serviceName))),
	)
	otel.SetMeterProvider(provider)
	log.Printf("[observability] cloud metrics exporter disabled for local service %s", serviceName)
	return provider
}

// GetProjectID returns the GCP project ID from env or metadata.
func GetProjectID() string {
	if id := os.Getenv("GOOGLE_CLOUD_PROJECT"); id != "" {
		return id
	}
	if id := os.Getenv("GCP_PROJECT"); id != "" {
		return id
	}
	return ""
}
