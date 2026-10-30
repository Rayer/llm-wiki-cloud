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
	"time"

	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
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
		_, _ = w.Write([]byte(`{"schema_version":2,"snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role_map_version":1,"source":"bootstrap","profiles":[]}`))
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
	want := pipelineLLMProfileResolverRequest{ReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: true}
	if received != want {
		t.Fatalf("resolver request = %+v, want %+v", received, want)
	}
}

type resolverSuggestedQueryClient struct {
	profile pipelineLLMProfile
	raw     string
	err     error
	calls   *[]string
	closed  *int
}

func (c *resolverSuggestedQueryClient) Chat(context.Context, string, string) (string, error) {
	*c.calls = append(*c.calls, c.profile.ID)
	return c.raw, c.err
}

func (c *resolverSuggestedQueryClient) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: c.profile.Provider, Model: normalizePipelineLLMModels(c.profile).Fast}, true
}

func (c *resolverSuggestedQueryClient) ProfileIdentity() llm.ProfileIdentity {
	return llm.ProfileIdentity{ID: c.profile.ID, Revision: c.profile.Revision, Role: string(llm.ProfileRoleFast), Provider: c.profile.Provider, Protocol: string(c.profile.Protocol), Model: normalizePipelineLLMModels(c.profile).Fast}
}

func (c *resolverSuggestedQueryClient) Close() { *c.closed++ }

func testResolverProfile(id, model string, revision int64) pipelineLLMProfile {
	return pipelineLLMProfile{
		SchemaVersion: 2, SnapshotDigest: strings.Repeat("a", 64), RoleMapVersion: 1,
		ID: id, Revision: revision, Provider: "custom", Protocol: llm.ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Models: pipelineLLMModels{Fast: model, Heavy: model}, APIKey: []byte("synthetic-" + id),
	}
}

func writePipelineProfileWire(w http.ResponseWriter, source string, profiles []pipelineLLMProfile) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		SchemaVersion  int                  `json:"schema_version"`
		SnapshotDigest string               `json:"snapshot_digest"`
		RoleMapVersion int                  `json:"role_map_version"`
		Source         string               `json:"source"`
		Profiles       []pipelineLLMProfile `json:"profiles"`
	}{2, strings.Repeat("a", 64), 1, source, profiles})
}

type modelOnlySuggestedQueryProvider struct {
	raw string
}

func (p modelOnlySuggestedQueryProvider) Chat(context.Context, string, string) (string, error) {
	return p.raw, nil
}

func (p modelOnlySuggestedQueryProvider) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: "legacy-fixture", Model: "legacy-model"}, true
}

func TestWrappedBootstrapSuggestedQueryArtifactKeepsLegacyModelMetadata(t *testing.T) {
	provider := quotaAwareSuggestedQueryProvider{provider: modelOnlySuggestedQueryProvider{raw: twentySuggestedQueriesRaw("alpha-id")}}
	entries := []conceptcache.Entry{{
		Slug: "alpha", Title: "Alpha", Body: "Alpha evidence",
		Frontmatter: map[string]interface{}{"id": "alpha-id"},
	}}
	artifact, err := suggestedqueries.Generate(context.Background(), provider, "", entries, nil,
		suggestedQueryGenerationMetadata(provider), time.Now())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(artifact.Candidates) != suggestedqueries.RequiredQueries {
		t.Fatalf("candidate count=%d, want %d", len(artifact.Candidates), suggestedqueries.RequiredQueries)
	}
	metadata := artifact.Candidates[0].Generation
	if metadata.Model != "legacy-model" || metadata.Provider != "legacy-fixture" || metadata.ProfileID != "" || metadata.ProfileRevision != 0 || metadata.Protocol != "" {
		t.Fatalf("bootstrap artifact metadata=%+v, want legacy model identity and no shared profile", metadata)
	}
}

func testSuggestedQueriesVault(t *testing.T) string {
	t.Helper()
	vault := t.TempDir()
	idMap := []byte(`{"concept":{"a3f7b2c01d9d":"alpha"},"source":{},"redirects":{},"concept_entity_id":{"a3f7b2c01d9d":"01JAZ5N7Y3K8M2Q4R6T9VWXABC"}}`)
	concepts := []byte(`{"id":"a3f7b2c01d9d","slug":"alpha","title":"Alpha","body":"Alpha","frontmatter":{"id":"a3f7b2c01d9d","title":"Alpha"}}` + "\n")
	mustWriteFile(t, filepath.Join(vault, "cache", "id_map.json"), idMap)
	mustWriteFile(t, filepath.Join(vault, "cache", "concepts.jsonl"), concepts)
	mustWriteFile(t, filepath.Join(vault, "wiki", "alpha.md"), []byte("---\nid: a3f7b2c01d9d\ntitle: Alpha\n---\nAlpha"))
	mustWriteFile(t, filepath.Join(vault, "cache", "dormant_concepts.jsonl"), nil)
	mustWriteFile(t, filepath.Join(vault, "cache", "raw_status.json"), []byte(`{"version":1,"files":{},"file_count":0}`))
	mustWriteFile(t, filepath.Join(vault, "cache", "suggested_queries.json"), []byte(`{"version":2,"queries":[],"candidates":[],"updated_at":"2026-07-01T00:00:00Z"}`))
	mustWriteFile(t, filepath.Join(vault, "synto.toml"), []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\n"))
	mustWriteFile(t, filepath.Join(vault, ".synto", "INDEX.json"), []byte(syntoIndexFixture("a3f7b2c01d9d", "01JAZ5N7Y3K8M2Q4R6T9VWXABC", "alpha", false)))
	writeValidSQLiteState(t, filepath.Join(vault, ".synto", "state.db"))
	return vault
}

func TestStandaloneSuggestedQueriesUsesPinnedProjectSourceForQuotaFallback(t *testing.T) {
	firstProfiles := []pipelineLLMProfile{testResolverProfile("profile-a", "model-a", 1), testResolverProfile("profile-b", "model-b", 2)}
	var resolverCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		if r.URL.Path != "/internal/pipeline/llm-profile" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		writePipelineProfileWire(w, "shared", firstProfiles)
	}))
	defer server.Close()

	oldFactory := newSuggestedQueryProfileClient
	var selected []string
	var closed int
	newSuggestedQueryProfileClient = func(profile pipelineLLMProfile) (suggestedQueryProfileClient, error) {
		client := &resolverSuggestedQueryClient{profile: profile, raw: twentySuggestedQueriesRaw("a3f7b2c01d9d"), calls: &selected, closed: &closed}
		if profile.ID == "profile-a" {
			client.err = &llm.ProfileCallError{Kind: llm.ProviderQuotaExhausted, StatusCode: http.StatusTooManyRequests}
		}
		return client, nil
	}
	t.Cleanup(func() { newSuggestedQueryProfileClient = oldFactory })

	vault := testSuggestedQueriesVault(t)
	err := runSuggestedQueriesCommand(context.Background(), workerConfig{
		VaultPath: vault, ExecutionID: "standalone-resolved-suggested", SuggestedQueries: true,
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		APIKey: "platform-fallback-must-not-run", apiKeySet: true,
	})
	if err != nil {
		t.Fatalf("runSuggestedQueriesCommand() error = %v", err)
	}
	if resolverCalls != 1 || strings.Join(selected, ",") != "profile-a,profile-b" || closed != 2 {
		t.Fatalf("resolver calls=%d selected=%v closed=%d, want pinned A quota -> B success", resolverCalls, selected, closed)
	}
	data, err := os.ReadFile(filepath.Join(vault, suggestedqueries.Path))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suggestedqueries.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Candidates) != suggestedqueries.RequiredQueries {
		t.Fatalf("candidate count=%d, want %d", len(artifact.Candidates), suggestedqueries.RequiredQueries)
	}
	metadata := artifact.Candidates[0].Generation
	if metadata.Model != "model-b" || metadata.ProfileID != "profile-b" || metadata.ProfileRevision != 2 || metadata.Provider != "custom" || metadata.Protocol != string(llm.ProtocolOpenAIChatCompletions) {
		t.Fatalf("artifact generation metadata=%+v, want successful pinned profile B", metadata)
	}
}

func TestStandaloneSuggestedQueriesUnavailableSourceFailsBeforeWorkspaceAndBootstrapIsExplicit(t *testing.T) {
	var resolverCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		w.Header().Set("Content-Type", "application/json")
		writePipelineProfileWire(w, "shared", nil)
	}))
	defer server.Close()
	vault := testSuggestedQueriesVault(t)
	prior, err := os.ReadFile(filepath.Join(vault, "cache", "suggested_queries.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = runSuggestedQueriesCommand(context.Background(), workerConfig{
		VaultPath: vault, ExecutionID: "unavailable-source", SuggestedQueries: true,
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		APIKey: "platform-fallback-must-not-run", apiKeySet: true,
	})
	if err == nil || resolverCalls != 1 {
		t.Fatalf("error=%v resolver calls=%d; want unavailable source to fail closed", err, resolverCalls)
	}
	got, readErr := os.ReadFile(filepath.Join(vault, "cache", "suggested_queries.json"))
	if readErr != nil || string(got) != string(prior) {
		t.Fatalf("suggested queries changed on unavailable source: got=%q err=%v", got, readErr)
	}
	cloudErr := runSuggestedQueriesCommand(context.Background(), workerConfig{
		Bucket: "worker-fixture", UserID: "user-fixture", ProjectID: "project-fixture", ExecutionID: "cloud-unavailable-source",
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-fixture", APIKey: "platform-fallback-must-not-run", apiKeySet: true,
	})
	if cloudErr == nil || resolverCalls != 2 {
		t.Fatalf("cloud error=%v resolver calls=%d; want cloud entrypoint to fail before object-store access", cloudErr, resolverCalls)
	}

	bootstrapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":2,"snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role_map_version":1,"source":"bootstrap","profiles":[]}`))
	}))
	defer bootstrapServer.Close()
	bootstrap, err := resolveSuggestedQueriesSource(context.Background(), workerConfig{
		LocalCloudScope: "test-scope", ProfileResolverURL: bootstrapServer.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-bootstrap", UserID: "user-fixture", ProjectID: "project-fixture",
	})
	if err != nil || !bootstrap.BootstrapLLMSource || bootstrap.SharedProfilePool || len(bootstrap.ProfileCandidates) != 0 {
		t.Fatalf("bootstrap resolution=%+v err=%v", bootstrap, err)
	}
	bootstrap.APIKey = "synthetic-bootstrap-key"
	bootstrap.SuggestedQueries = true
	provider, err := newStandaloneSuggestedQueryProvider(context.Background(), bootstrap)
	if err != nil {
		t.Fatalf("newStandaloneSuggestedQueryProvider(bootstrap) error = %v", err)
	}
	defer closeSuggestedQueryProvider(provider)
	identity, ok := provider.(llm.ModelIdentityProvider)
	if !ok {
		t.Fatalf("bootstrap provider type %T does not expose model identity", provider)
	}
	model, ok := identity.ModelIdentity()
	if !ok || model.Provider != "deepseek" || model.Model != "deepseek-flash" {
		t.Fatalf("bootstrap model identity=%+v valid=%t", model, ok)
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
		if _, err := os.Stat(cfg.ProfileCallSuccessMarkerPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("profile-call marker survived between attempts: stat err=%v", err)
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: marker,
		ProfileCallSuccessMarkerPath: filepath.Join(root, "profile-call-success.marker"),
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

func TestSharedProfileQuotaRetryStopsAfterSuccessfulCallOrMissingSafetyMarker(t *testing.T) {
	for _, tc := range []struct {
		name               string
		configureSuccess   bool
		writeSuccessMarker bool
	}{
		{name: "successful provider response", configureSuccess: true, writeSuccessMarker: true},
		{name: "missing success marker path", configureSuccess: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldHooks := localWorkspaceBatchHooks
			t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
			root := t.TempDir()
			firstWorkspace := filepath.Join(root, "workspace-1")
			if err := os.Mkdir(firstWorkspace, 0o700); err != nil {
				t.Fatal(err)
			}
			quotaMarker := filepath.Join(root, "quota.marker")
			successMarker := filepath.Join(root, "profile-call-success.marker")
			attempts, workspaceCreations := 0, 0
			hooks := oldHooks
			hooks.createWorkspace = func(_, _ string) (string, error) {
				workspaceCreations++
				return filepath.Join(root, "workspace-retry"), nil
			}
			hooks.runWorkerBatchAtVault = func(_ context.Context, _ workerConfig, _ [][]string, _ string) error {
				attempts++
				if err := os.WriteFile(quotaMarker, nil, 0o600); err != nil {
					return err
				}
				if tc.writeSuccessMarker {
					if err := os.WriteFile(successMarker, nil, 0o600); err != nil {
						return err
					}
				}
				return errors.New("fixture quota exhausted")
			}
			localWorkspaceBatchHooks = hooks
			cfg := workerConfig{
				WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: quotaMarker,
				ProfileCandidates: []pipelineLLMProfile{{ID: "shared-first"}, {ID: "shared-second"}},
			}
			if tc.configureSuccess {
				cfg.ProfileCallSuccessMarkerPath = successMarker
			}
			got, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, root, firstWorkspace, nil, false)
			if err == nil || !strings.Contains(err.Error(), "no-replay safety was not proven") || got != firstWorkspace {
				t.Fatalf("result=(%q,%v), want fail-closed result on original workspace", got, err)
			}
			if attempts != 1 || workspaceCreations != 0 {
				t.Fatalf("attempts=%d workspace creations=%d, want no replay", attempts, workspaceCreations)
			}
		})
	}
}

func TestSharedProfileQuotaRetryUsesPinnedNextCandidate(t *testing.T) {
	oldHooks := localWorkspaceBatchHooks
	t.Cleanup(func() { localWorkspaceBatchHooks = oldHooks })
	root := t.TempDir()
	firstWorkspace := filepath.Join(root, "workspace-1")
	if err := os.Mkdir(firstWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "quota.marker")
	var selected []string
	resolverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		writePipelineProfileWire(w, "shared", []pipelineLLMProfile{testResolverProfile("shared-current", "current-model", 3)})
	}))
	defer server.Close()
	hooks := oldHooks
	hooks.createWorkspace = func(_, _ string) (string, error) {
		path := filepath.Join(root, "workspace-2")
		if err := os.Mkdir(path, 0o700); err != nil {
			return "", err
		}
		return path, nil
	}
	hooks.removeWorkspace = os.RemoveAll
	hooks.runWorkerBatchAtVault = func(_ context.Context, cfg workerConfig, _ [][]string, _ string) error {
		selected = append(selected, cfg.ActiveLLMProfile.ID)
		if len(selected) == 1 {
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				return err
			}
			return errors.New("pinned provider quota exhausted")
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: marker,
		ProfileCallSuccessMarkerPath: filepath.Join(root, "profile-call-success.marker"),
		ProfileResolverURL:           server.URL + "/internal/pipeline/llm-profile", LocalCloudScope: "test-scope",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		ProfileCandidates: []pipelineLLMProfile{testResolverProfile("shared-first", "first-model", 1), testResolverProfile("removed-second", "second-model", 2)},
	}
	if _, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, root, firstWorkspace, nil, false); err != nil {
		t.Fatalf("runWorkerBatchAtVaultWithProfilePool() error = %v", err)
	}
	if resolverCalls != 0 || len(selected) != 2 || selected[0] != "shared-first" || selected[1] != "removed-second" {
		t.Fatalf("resolver calls=%d selected=%v, want the pinned candidate set without refresh", resolverCalls, selected)
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
			SchemaVersion: 2, SnapshotDigest: strings.Repeat("a", 64), RoleMapVersion: 1,
			ID: "personal-profile", Revision: 4, Provider: "custom", Protocol: "openai_chat_completions",
			Endpoint: "https://fixture.example/v1", Models: pipelineLLMModels{Fast: "fixture-fast", Heavy: "fixture-heavy"}, APIKey: []byte("synthetic-profile-key"),
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
		!strings.Contains(gotProfileConfig, `"models":{"fast":"fixture-fast","heavy":"fixture-heavy"}`) || gotProfileKey != "synthetic-profile-key" {
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
		ProfileCallSuccessMarkerPath: filepath.Join(root, "profile-call-success.marker"),
		ProfileCandidates:            []pipelineLLMProfile{{ID: "shared-first"}, {ID: "shared-second"}},
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
