package observability

import (
	"context"
	"testing"
	"time"
)

func TestInitMetricsForModeLocalSkipsCloudExporterAndCloudModeKeepsIt(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/dev/null")
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
	t.Setenv("GCE_METADATA_IP", "127.0.0.1:1")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())

	local, err := InitMetricsForMode(context.Background(), "test-local", "demo-gat4-local", true)
	if err != nil || local == nil {
		t.Fatalf("local metrics provider=%v err=%v", local, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := local.ForceFlush(ctx); err != nil {
		t.Fatalf("local provider tried to flush an exporter: %v", err)
	}
	if err := local.Shutdown(ctx); err != nil {
		t.Fatalf("local provider shutdown: %v", err)
	}

	cloud, err := InitMetricsForMode(ctx, "test-cloud", "demo-gat4-local", false)
	if cloud != nil {
		_ = cloud.Shutdown(ctx)
	}
	if err == nil {
		t.Fatal("cloud mode accepted an unreadable credential file; expected native exporter initialization")
	}
}
