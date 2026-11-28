//go:build lwc_local_synto_runtime

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSyntoPublishGuardUsesDisposableFixtures(t *testing.T) {
	python := os.Getenv("LOCAL_CLOUD_PYTHON")
	if python == "" || !filepath.IsAbs(python) {
		t.Fatal("LOCAL_CLOUD_PYTHON must identify the worktree Synto interpreter")
	}
	root := t.TempDir()
	cmd := exec.Command(python, "testdata/lwc341_publish_safety_smoke.py")
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + root,
		"TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + root,
		"PYTHONDONTWRITEBYTECODE=1",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("disposable LWC-341 Synto fixture failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "LWC-341 disposable publication guard cases passed") {
		t.Fatalf("fixture did not report completion: %s", output)
	}
}
