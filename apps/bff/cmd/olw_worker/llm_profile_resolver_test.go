package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePipelineLLMProfilesSendsReservationIdentity(t *testing.T) {
	var received pipelineLLMProfileResolverRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/pipeline/llm-profile" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"bootstrap","profiles":[]}`))
	}))
	defer server.Close()

	cfg := workerConfig{
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
	}
	profiles, shared, err := resolvePipelineLLMProfiles(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolvePipelineLLMProfiles() error = %v", err)
	}
	if len(profiles) != 0 || shared {
		t.Fatalf("resolve result = (%d profiles, shared=%t), want empty bootstrap", len(profiles), shared)
	}
	want := pipelineLLMProfileResolverRequest{ReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture"}
	if received != want {
		t.Fatalf("resolver request = %+v, want %+v", received, want)
	}
}

func TestSharedProfileQuotaRetryStartsFromFreshWorkspace(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	root := t.TempDir()
	marker := filepath.Join(root, "quota.marker")
	firstWorkspace := filepath.Join(root, "workspace-1")
	if err := os.Mkdir(firstWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaces := []string{firstWorkspace}
	var selected []string
	hooks := oldHooks
	hooks.createWorkspace = func(_, _ string) (string, error) {
		path := filepath.Join(root, "workspace-2")
		if err := os.Mkdir(path, 0o700); err != nil {
			return "", err
		}
		workspaces = append(workspaces, path)
		return path, nil
	}
	hooks.removeWorkspace = os.RemoveAll
	hooks.runWorkerBatchAtVault = func(_ context.Context, cfg workerConfig, _ [][]string, workspace string) error {
		selected = append(selected, cfg.ActiveLLMProfile.ID)
		if len(selected) == 1 {
			if err := os.WriteFile(filepath.Join(workspace, "partial-output.md"), []byte("must be discarded"), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				return err
			}
			return errors.New("pinned provider quota exhausted")
		}
		if _, err := os.Stat(filepath.Join(workspace, "partial-output.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retry workspace contains failed attempt output: stat err=%v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("quota marker survived between attempts: stat err=%v", err)
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: marker,
		ProfileCandidates: []pipelineLLMProfile{
			{ID: "shared-first", Revision: 1},
			{ID: "shared-second", Revision: 1},
		},
	}

	got, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, root, firstWorkspace, nil, false)
	if err != nil {
		t.Fatalf("runWorkerBatchAtVaultWithProfilePool() error = %v", err)
	}
	if got != workspaces[1] || len(workspaces) != 2 {
		t.Fatalf("retry workspaces = %v, returned %q; want two clean attempts", workspaces, got)
	}
	if len(selected) != 2 || selected[0] != "shared-first" || selected[1] != "shared-second" {
		t.Fatalf("selected profiles = %v, want ordered quota fallback", selected)
	}
	if pipelineProfileQuotaMarkerExists(cfg) {
		t.Fatal("quota marker remained after a successful fallback")
	}
}

func TestCloudProfileRunnerAppliesResolvedPersonalProfile(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	root := t.TempDir()
	vault := filepath.Join(root, "materialized")
	if err := os.MkdirAll(filepath.Join(vault, "raw"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "raw", "source.md"), []byte("fixture source"), 0o600); err != nil {
		t.Fatal(err)
	}
	var selectedID string
	var gotProfileConfig, gotProfileKey string
	hooks := oldHooks
	hooks.runWorkerBatchAtVault = func(_ context.Context, cfg workerConfig, _ [][]string, workspace string) error {
		if _, err := os.Stat(filepath.Join(workspace, "raw", "source.md")); err != nil {
			return err
		}
		if cfg.ActiveLLMProfile == nil {
			t.Fatal("cloud worker did not select its resolved personal profile")
		}
		selectedID = cfg.ActiveLLMProfile.ID
		env, err := prepareOLWEnvironment(cfg)
		if err != nil {
			return err
		}
		defer cleanupOLWEnvironment(env)
		for _, item := range env {
			if key, value, ok := strings.Cut(item, "="); ok {
				switch key {
				case "LWC_PROFILE_CONFIG":
					gotProfileConfig = value
				case "LWC_PROFILE_API_KEY":
					gotProfileKey = value
				}
			}
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root,
		ProfileCandidates: []pipelineLLMProfile{{
			ID: "personal-profile", Revision: 4, Provider: "custom", Protocol: "openai_chat_completions",
			Endpoint: "https://fixture.example/v1", Model: "fixture-chat", APIKey: []byte("synthetic-profile-key"),
		}},
	}

	workspace, err := runCloudWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, vault)
	if err != nil {
		t.Fatalf("runCloudWorkerBatchAtVaultWithProfilePool() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	if selectedID != "personal-profile" || workspace == vault {
		t.Fatalf("selected=%q workspace=%q; want personal profile in an isolated copy", selectedID, workspace)
	}
	if !strings.Contains(gotProfileConfig, `"endpoint":"https://fixture.example/v1"`) ||
		!strings.Contains(gotProfileConfig, `"model":"fixture-chat"`) || gotProfileKey != "synthetic-profile-key" {
		t.Fatalf("Synto profile environment not applied (config present=%t, key present=%t)", gotProfileConfig != "", gotProfileKey != "")
	}
}

func TestCloudSharedProfileQuotaRetryStartsFromCleanSeed(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	root := t.TempDir()
	vault := filepath.Join(root, "materialized")
	if err := os.MkdirAll(filepath.Join(vault, "raw"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "raw", "source.md"), []byte("fixture source"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "quota.marker")
	var selected []string
	hooks := oldHooks
	hooks.runWorkerBatchAtVault = func(_ context.Context, cfg workerConfig, _ [][]string, workspace string) error {
		selected = append(selected, cfg.ActiveLLMProfile.ID)
		if len(selected) == 1 {
			if err := os.WriteFile(filepath.Join(workspace, "partial-output.md"), []byte("discard"), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				return err
			}
			return errors.New("fixture provider quota exhausted")
		}
		if _, err := os.Stat(filepath.Join(workspace, "partial-output.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("quota retry retained partial output: err=%v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("quota marker survived into retry: err=%v", err)
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: marker,
		ProfileCandidates: []pipelineLLMProfile{{ID: "shared-first"}, {ID: "shared-second"}},
	}

	workspace, err := runCloudWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, vault)
	if err != nil {
		t.Fatalf("runCloudWorkerBatchAtVaultWithProfilePool() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	if len(selected) != 2 || selected[0] != "shared-first" || selected[1] != "shared-second" {
		t.Fatalf("selected profiles = %v, want ordered explicit-quota fallback", selected)
	}
	if _, err := os.Stat(filepath.Join(workspace, "raw", "source.md")); err != nil {
		t.Fatalf("quota retry lost its materialized cloud seed: %v", err)
	}
}

func TestSharedProfileRetryStopsWithoutExplicitQuotaMarker(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	root := t.TempDir()
	attempts := 0
	hooks := oldHooks
	hooks.createWorkspace = func(_, _ string) (string, error) { return root, nil }
	hooks.runWorkerBatchAtVault = func(context.Context, workerConfig, [][]string, string) error {
		attempts++
		return errors.New("generic provider failure")
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: filepath.Join(root, "quota.marker"),
		ProfileCandidates: []pipelineLLMProfile{{ID: "shared-first", Revision: 1}, {ID: "shared-second", Revision: 1}},
	}
	if _, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, root, "", nil, false); err == nil {
		t.Fatal("generic provider failure unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one without an explicit quota marker", attempts)
	}
}

func TestPipelineProfileQuotaMarkerRequiresEmptyRegularFile(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "quota.marker")
	cfg := workerConfig{SharedProfilePool: true, QuotaMarkerPath: marker}
	if pipelineProfileQuotaMarkerExists(cfg) {
		t.Fatal("missing marker was accepted")
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !pipelineProfileQuotaMarkerExists(cfg) {
		t.Fatal("empty marker was rejected")
	}
	if err := os.WriteFile(marker, []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pipelineProfileQuotaMarkerExists(cfg) {
		t.Fatal("nonempty marker was accepted")
	}
}
