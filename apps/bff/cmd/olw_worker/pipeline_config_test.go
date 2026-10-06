package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestDeployedPipelineConfig(t *testing.T, vault string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(vault, "synto.toml"), []byte(testDeployedPipelineConfig))
}

func TestPipelineRunTimeoutRequiresPositiveBoundedTOMLValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want int
		bad  bool
	}{
		{name: "valid", data: "[pipeline]\nrun_timeout_seconds = 17\n", want: 17},
		{name: "missing", data: "[pipeline]\narticle_max_tokens = 32768\n", bad: true},
		{name: "zero", data: "[pipeline]\nrun_timeout_seconds = 0\n", bad: true},
		{name: "invalid TOML", data: "[pipeline\n", bad: true},
		{name: "overflow", data: "[pipeline]\nrun_timeout_seconds = 9223372036854775807\n", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pipelineRunTimeout([]byte(tc.data))
			if tc.bad && err == nil {
				t.Fatalf("pipelineRunTimeout() accepted %q", tc.data)
			}
			if !tc.bad && (err != nil || got != tc.want) {
				t.Fatalf("pipelineRunTimeout() = (%d, %v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestDeployedPipelineConfigIsReadOnce(t *testing.T) {
	store := &pipelineConfigReadOnceStore{objectStore: newMemoryObjects()}
	data, timeoutSeconds, err := readDeployedPipelineConfig(context.Background(), store)
	if err != nil || timeoutSeconds != 15 || !strings.Contains(string(data), "article_max_tokens = 32768") || store.reads != 1 {
		t.Fatalf("readDeployedPipelineConfig() timeout=%d reads=%d err=%v", timeoutSeconds, store.reads, err)
	}
}

func TestLocalCloudPipelineInputsUseRenderedWorktreeFiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "synto.toml")
	config := []byte("[pipeline]\nrun_timeout_seconds = 23\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	const localKey = "TEST_ONLY_LOCAL_PIPELINE_KEY"
	bindings, err := json.Marshal(struct {
		Environment string `json:"environment"`
		LocalAPIKey []byte `json:"localApiKey"`
	}{Environment: "local", LocalAPIKey: []byte(localKey)})
	if err != nil {
		t.Fatal(err)
	}
	bindingsPath := filepath.Join(dir, "private-bindings.json")
	if err := os.WriteFile(bindingsPath, bindings, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &pipelineConfigReadOnceStore{objectStore: newMemoryObjects()}
	data, apiKey, timeoutSeconds, err := readCloudPipelineInputs(context.Background(), workerConfig{
		LocalCloudScope: "worktree-local", PipelineConfigPath: configPath, PipelineBindingsPath: bindingsPath,
	}, store)
	if err != nil || string(data) != string(config) || string(apiKey) != localKey || timeoutSeconds != 23 || store.reads != 0 {
		t.Fatalf("local cloud config=%q key=%q timeout=%d GCS reads=%d err=%v", data, apiKey, timeoutSeconds, store.reads, err)
	}
	clear(apiKey)
}

func TestDeployedCloudPipelineInputsRejectLocalFilesBeforeGCSRead(t *testing.T) {
	store := &pipelineConfigReadOnceStore{objectStore: newMemoryObjects()}
	_, _, _, err := readCloudPipelineInputs(context.Background(), workerConfig{
		PipelineConfigPath: "/tmp/local/synto.toml", PipelineBindingsPath: "/tmp/local/private-bindings.json",
	}, store)
	if !errors.Is(err, errCloudWorkerConfigInvalid) || store.reads != 0 {
		t.Fatalf("deployed config error=%v GCS reads=%d, want fail before GCS read", err, store.reads)
	}
}

func TestCloudRunTimeoutCancelsWorkerChild(t *testing.T) {
	objects := newMemoryObjects()
	config := strings.ReplaceAll(testDeployedPipelineConfig, "run_timeout_seconds = 15", "run_timeout_seconds = 1")
	if _, err := objects.Write(context.Background(), deployedPipelineConfigObjectPath, []byte(config), nil, objectConditions{}); err != nil {
		t.Fatal(err)
	}
	store := &pipelineConfigReadOnceStore{objectStore: objects}
	started := make(chan struct{})
	oldExec := execOLW
	t.Cleanup(func() { execOLW = oldExec })
	execOLW = func(ctx context.Context, _ string, _ []string, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	err := runCloudWorkerBatch(context.Background(), cloudCfgFor("timeout-user", "timeout-project", "olw-pipeline-timeout-1"), [][]string{{"run"}}, store)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCloudWorkerBatch() error=%v, want timeout cancellation", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("Synto child did not start before timeout")
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("fixed run timeout elapsed=%s, want about one second", elapsed)
	}
	if store.reads != 1 {
		t.Fatalf("deployed synto.toml reads=%d, want one snapshot read", store.reads)
	}
}

func TestLocalWorkerRunConsumesRenderedPipelineConfig(t *testing.T) {
	config := strings.ReplaceAll(testDeployedPipelineConfig, "run_timeout_seconds = 15", "run_timeout_seconds = 1")
	configPath := filepath.Join(t.TempDir(), "syntoo.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	const localSecret = "TEST_ONLY_LOCAL_GSM_KEY"
	bindings, err := json.Marshal(struct {
		Environment string `json:"environment"`
		LocalAPIKey []byte `json:"localApiKey"`
	}{Environment: "local", LocalAPIKey: []byte(localSecret)})
	if err != nil {
		t.Fatal(err)
	}
	bindingsPath := filepath.Join(t.TempDir(), "private-bindings.json")
	if err := os.WriteFile(bindingsPath, bindings, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_API_KEY", "TEST_ONLY_OLD_ENV_KEY")
	vault := t.TempDir()
	workspaceDir := t.TempDir()
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	hooks := oldHooks
	hooks.runWorkerBatchAtVault = func(ctx context.Context, cfg workerConfig, _ [][]string, _ string) error {
		if string(cfg.DeployedSynto) != config {
			t.Fatalf("local worker received config %q, want generated config", cfg.DeployedSynto)
		}
		if cfg.APIKey != localSecret {
			t.Fatalf("local worker API key=%q, want selected private GSM value", cfg.APIKey)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second || time.Until(deadline) < 500*time.Millisecond {
			t.Fatalf("local worker context deadline=%v, hasDeadline=%t", deadline, ok)
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks

	cmd := newRootCommand()
	cmd.SetArgs([]string{"run", `[["run","--auto-approve"]]`, "--vault", vault,
		"--workspace-dir", workspaceDir, "--pipeline-config", configPath,
		"--pipeline-private-bindings", bindingsPath, "--no-postprocess"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker run with generated config: %v", err)
	}
}

func TestDeployedSyntoReplacesOldTOMLAndPreservesProjectState(t *testing.T) {
	deployed := []byte("[providers.default]\nname = \"deepseek\"\napi_key_env = \"DEEPSEEK_API_KEY\"\n\n[pipeline]\nauto_approve = true\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\narticle_max_tokens = 1234\nrun_timeout_seconds = 17\n")
	for _, migration := range []bool{false, true} {
		name := "existing Synto state"
		if migration {
			name = "legacy OLW migration"
		}
		t.Run(name, func(t *testing.T) {
			vault := t.TempDir()
			oldWiki := []byte("old project TOML")
			oldSynto := []byte("[pipeline]\nauto_commit = true\narticle_max_tokens = 9999\n")
			mustWriteFile(t, filepath.Join(vault, "wiki.toml"), oldWiki)
			if migration {
				writeValidSQLiteState(t, filepath.Join(vault, ".olw", "state.db"))
			} else {
				mustWriteFile(t, filepath.Join(vault, "synto.toml"), oldSynto)
				writeValidSQLiteState(t, filepath.Join(vault, ".synto", "state.db"))
			}
			statePath := filepath.Join(vault, ".synto", "state.db")
			if migration {
				statePath = filepath.Join(vault, ".olw", "state.db")
			}
			oldState, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			oldExec := execOLW
			t.Cleanup(func() { execOLW = oldExec })
			execOLW = func(_ context.Context, work string, _ []string, _ []string, _, _ io.Writer) error {
				mustWriteFile(t, filepath.Join(work, "synto.toml"), oldSynto)
				writeValidSQLiteState(t, filepath.Join(work, ".synto", "state.db"))
				return nil
			}
			if err := ensureSyntoVault(context.Background(), vault, workerConfig{APIKey: "test-only", DeployedSynto: deployed}, nil); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(vault, "synto.toml"))
			if err != nil || string(got) != string(deployed) {
				t.Fatalf("final synto.toml=%q err=%v, want deployed config", got, err)
			}
			if gotWiki, err := os.ReadFile(filepath.Join(vault, "wiki.toml")); err != nil || string(gotWiki) != string(oldWiki) {
				t.Fatalf("legacy project config changed: got=%q err=%v", gotWiki, err)
			}
			if _, err := os.Stat(statePath); err != nil {
				t.Fatalf("Synto state was not retained: %v", err)
			}
			if gotState, err := os.ReadFile(statePath); err != nil || string(gotState) != string(oldState) {
				t.Fatalf("project state changed: err=%v", err)
			}
			if migration {
				if _, err := os.Stat(filepath.Join(vault, ".synto", "state.db")); err != nil {
					t.Fatalf("legacy state migration did not produce Synto state: %v", err)
				}
			}
		})
	}
}

func TestGenerationOutputsExcludeConfigWhileReadingHistoricalManifestIsSupported(t *testing.T) {
	workspace := t.TempDir()
	writeFreshSyntoRequiredOutputs(t, workspace)
	files, err := preflightGenerationOutputs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.path == "synto.toml" || file.path == "wiki.toml" {
			t.Fatalf("project config %q remains a new generation output", file.path)
		}
	}
	if len(files) == 0 {
		t.Fatal("generation outputs unexpectedly omitted all valid Pipeline outputs")
	}
}

func TestDeployedConfigReadErrorNamesTheObjectWithoutLeakingPayload(t *testing.T) {
	objects := newMemoryObjects()
	objects.missingDeployedConfig = true
	_, _, err := readDeployedPipelineConfig(context.Background(), objects)
	if err == nil || !strings.Contains(err.Error(), "deployed Pipeline config") || strings.Contains(err.Error(), "TEST_ONLY") {
		t.Fatalf("missing deployed config error = %v", err)
	}
}
