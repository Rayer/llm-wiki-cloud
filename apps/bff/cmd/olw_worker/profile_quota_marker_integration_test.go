//go:build byokprofilefixture

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedSyntoHealthCheckDoesNotBlockSharedQuotaSafety(t *testing.T) {
	python := os.Getenv("SYNTO_TEST_PYTHON")
	if python == "" || !filepath.IsAbs(python) {
		t.Skip("SYNTO_TEST_PYTHON must point to the pinned Synto 0.7.0 interpreter")
	}
	pythonPath, err := exec.LookPath(python)
	if err != nil {
		t.Fatalf("resolve pinned Synto interpreter: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(pythonPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_HEALTH_ROLE", "heavy")

	t.Run("first generation quota falls back locally after healthcheck", func(t *testing.T) {
		receipt, err := runPinnedSyntoProfileQuotaFixture(t, false, "0", 1, false)
		if err != nil {
			t.Fatalf("local first-call quota fallback: %v", err)
		}
		counts := readProfileFixtureCallCounts(t, receipt)
		t.Logf("captured local provider request counts: %v", counts)
		assertProfileFixtureCallCount(t, counts, "openai_models_healthcheck", "fixture-model-a", "/v1/models", 1)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-a", "/v1/chat/completions", 1)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions", "fixture-model-a", "/v1/chat/completions", 0)
		assertProfileFixtureCallCount(t, counts, "openai_models_healthcheck", "fixture-model-b", "/v1/models", 1)
		if counts[profileFixtureCallKey("openai_chat_completions", "fixture-model-b", "/v1/chat/completions")] == 0 {
			t.Fatalf("fallback profile B received no generation request: %v", counts)
		}
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-b", "/v1/chat/completions", 0)
	})

	t.Run("successful generation then quota stops cloud replay", func(t *testing.T) {
		receipt, err := runPinnedSyntoProfileQuotaFixture(t, true, "1", 2, false)
		if err == nil || !strings.Contains(err.Error(), "no-replay safety was not proven") {
			t.Fatalf("cloud result error=%v, want fail-closed after successful generation", err)
		}
		counts := readProfileFixtureCallCounts(t, receipt)
		t.Logf("captured cloud provider request counts: %v", counts)
		assertProfileFixtureCallCount(t, counts, "openai_models_healthcheck", "fixture-model-a", "/v1/models", 1)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions", "fixture-model-a", "/v1/chat/completions", 1)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-a", "/v1/chat/completions", 1)
		assertProfileFixtureCallCount(t, counts, "openai_models_healthcheck", "fixture-model-b", "/v1/models", 0)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions", "fixture-model-b", "/v1/chat/completions", 0)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-b", "/v1/chat/completions", 0)
	})

	t.Run("Fast success then Heavy account quota cannot enter candidate B", func(t *testing.T) {
		receipt, err := runPinnedSyntoProfileQuotaFixture(t, false, "0", 1, true)
		if err == nil || !strings.Contains(err.Error(), "no-replay safety was not proven") {
			t.Fatalf("Worker barrier error=%v, want no replay after Fast produced work", err)
		}
		counts := readProfileFixtureCallCounts(t, receipt)
		t.Logf("captured local provider request counts: %v", counts)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions", "fixture-fast-model-a", "/v1/chat/completions", 1)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-a", "/v1/chat/completions", 1)
		assertProfileFixtureCallCount(t, counts, "openai_models_healthcheck", "fixture-model-b", "/v1/models", 0)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions", "fixture-model-b", "/v1/chat/completions", 0)
		assertProfileFixtureCallCount(t, counts, "openai_chat_completions_quota", "fixture-model-b", "/v1/chat/completions", 0)
	})
}

func runPinnedSyntoProfileQuotaFixture(t *testing.T, cloud bool, quotaAfter string, generations int, fastThenHeavy bool) (string, error) {
	t.Helper()
	receipt := filepath.Join(t.TempDir(), "provider-receipts.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", receipt)
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_QUOTA_MODEL", "fixture-model-a")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_QUOTA_AFTER", quotaAfter)
	profileA := testResolverProfile("profile-a", "fixture-model-a", 1)
	profileA.Models.Fast = "fixture-fast-model-a"
	profileB := testResolverProfile("profile-b", "fixture-model-b", 2)
	profileB.Models.Fast = "fixture-fast-model-b"
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request pipelineLLMProfileResolverRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.IncludeCredentials {
			t.Errorf("identity-only resolver request=%+v err=%v", request, err)
		}
		identityProfiles := []pipelineLLMProfile{profileA, profileB}
		for i := range identityProfiles {
			identityProfiles[i].APIKey = nil
		}
		writePipelineProfileWire(w, "shared", identityProfiles)
	}))
	defer resolver.Close()

	vault := testSuggestedQueriesVault(t)

	workspaceRoot := t.TempDir()
	cfg := workerConfig{
		WorkspaceDir: workspaceRoot, ExecutionID: "profile-quota-marker-fixture", APIKey: "platform-fallback-must-not-run",
		SharedProfilePool: true, StopOnError: true, SuppressOutput: true,
		QuotaMarkerPath:              filepath.Join(workspaceRoot, "quota.marker"),
		ProfileCallSuccessMarkerPath: filepath.Join(workspaceRoot, "profile-call-success.marker"),
		ProfileCallAttemptMarkerPath: filepath.Join(workspaceRoot, "profile-call-attempt.marker"),
		ProfileResolverURL:           resolver.URL + "/internal/pipeline/llm-profile", LocalCloudScope: "test-scope",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		ProfileCandidates: []pipelineLLMProfile{profileA, profileB},
	}
	oldHooks := localWorkspaceBatchHooks
	hooks := oldHooks
	hooks.runWorkerBatchAtVault = func(ctx context.Context, attemptCfg workerConfig, _ [][]string, workspace string) error {
		env, err := prepareOLWEnvironment(attemptCfg)
		if err != nil {
			return err
		}
		defer cleanupOLWEnvironment(env)
		probe := `import sys
exec(sys.stdin.read(), {"__name__": "lwc_profile_fixture"})
from pathlib import Path
from synto.client_factory import build_router
from synto.config import Config

config = Config(vault=Path(sys.argv[2]),
    providers={"default": {"name": "deepseek", "url": "https://api.deepseek.com", "api_key_env": "LWC_TEST_API_KEY"}},
    models={"fast": "deepseek-chat", "heavy": "deepseek-reasoner"})
router = build_router(config)
try:
    router.require_healthy()
    endpoint = router.endpoint("heavy")
    if sys.argv[3] == "true":
        fast = router.endpoint("fast")
        fast.client.generate("Synthetic Fast barrier probe", model=fast.model, options=fast.options)
        endpoint.client.generate("Synthetic Heavy barrier probe", model=endpoint.model, options=endpoint.options)
    else:
        for _ in range(int(sys.argv[1])):
            endpoint.client.generate("Synthetic profile quota probe", model=endpoint.model, options=endpoint.options)
finally:
    router.close()
		`
		cmd := exec.CommandContext(ctx, "python3", "-c", probe, fmt.Sprint(generations), workspace, fmt.Sprint(fastThenHeavy))
		cmd.Stdin = strings.NewReader(profileFixtureScript(syntoExecutionAdapter))
		cmd.Env = allowlistedSyntoEnvironment(env)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pinned Synto Project profile probe: %w: %s", err, output.String())
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })

	commands := [][]string{{"run", "--auto-approve"}}
	if cloud {
		result, err := runCloudWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, commands, vault)
		if result != "" && result != vault {
			t.Cleanup(func() { _ = os.RemoveAll(result) })
		}
		return receipt, err
	}
	workspace, err := createWorkspace(workspaceRoot, vault)
	if err != nil {
		t.Fatalf("create local worker workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	result, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, commands, vault, workspace, nil, false)
	if result != "" && result != vault && result != workspace {
		t.Cleanup(func() { _ = os.RemoveAll(result) })
	}
	return receipt, err
}

func readProfileFixtureCallCounts(t *testing.T, receipt string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("read synthetic provider receipts: %v", err)
	}
	counts := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Fatalf("invalid provider receipt %q", line)
		}
		counts[profileFixtureCallKey(fields[1], fields[2], fields[3])]++
	}
	return counts
}

func profileFixtureCallKey(kind, model, path string) string {
	return fmt.Sprintf("%s|%s|%s", kind, model, path)
}

func assertProfileFixtureCallCount(t *testing.T, counts map[string]int, kind, model, path string, want int) {
	t.Helper()
	key := profileFixtureCallKey(kind, model, path)
	if got := counts[key]; got != want {
		t.Fatalf("provider call %q count=%d, want %d; all calls=%v", key, got, want, counts)
	}
}
