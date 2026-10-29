package observability

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestInitMetricsForModeLocalSkipsCloudExporterAndCloudModeKeepsIt(t *testing.T) {
	const subprocessEnv = "GAT4_METRICS_MODE_TEST_SUBPROCESS"
	if os.Getenv(subprocessEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestInitMetricsForModeLocalSkipsCloudExporterAndCloudModeKeepsIt$")
		cmd.Env = append(os.Environ(), subprocessEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated metrics mode test failed: %v\n%s", err, output)
		}
		return
	}

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
