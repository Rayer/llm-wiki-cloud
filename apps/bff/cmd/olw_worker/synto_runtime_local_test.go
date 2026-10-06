//go:build lwc_local_synto_runtime

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider(t *testing.T) {
	wantPython := os.Getenv("LOCAL_CLOUD_PYTHON")
	if wantPython == "" || !filepath.IsAbs(wantPython) {
		t.Fatal("LOCAL_CLOUD_PYTHON must identify the worktree Synto interpreter")
	}
	gotPython, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("resolve worker python3: %v", err)
	}
	if filepath.Clean(gotPython) != filepath.Clean(wantPython) {
		t.Fatalf("worker python3=%q, want worktree venv %q", gotPython, wantPython)
	}
	for _, key := range []string{"LLM_API_KEY", "DEEPSEEK_API_KEY", "SYNTO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "TYPESAFE_API_KEY", "TYPESAFE_JEV_API_KEY"} {
		if value := os.Getenv(key); value != "" {
			t.Fatalf("%s must be unset for this no-provider runtime regression", key)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := execOLWCommand(context.Background(), t.TempDir(), []string{"--version"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("run pinned Synto version through worker adapter: %v; stderr=%q", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "synto, version 0.7.0" {
		t.Fatalf("worker adapter reported %q, want pinned Synto 0.7.0", got)
	}

	stdout.Reset()
	stderr.Reset()
	err = execOLWCommand(context.Background(), t.TempDir(), []string{"lwc361-command-does-not-exist"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("unknown Synto command unexpectedly succeeded")
	}
	if output := stdout.String() + stderr.String(); !strings.Contains(output, "No such command") {
		t.Fatalf("unknown Synto command did not fail at the CLI boundary: %q", output)
	}
}
