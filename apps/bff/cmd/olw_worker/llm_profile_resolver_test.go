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

func TestResolvePipelineLLMProfileResponsePreservesEnvelope(t *testing.T) {
	const digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var received pipelineLLMProfileResolverRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":2,"snapshot_digest":"` + digest + `","role_map_version":1,"source":"bootstrap","profiles":[]}`))
	}))
	defer server.Close()
	cfg := workerConfig{
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-envelope", UserID: "user-fixture", ProjectID: "project-fixture",
	}

	response, err := resolvePipelineLLMProfileResponseWithCredentials(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("resolvePipelineLLMProfileResponseWithCredentials() error = %v", err)
	}
	if received != (pipelineLLMProfileResolverRequest{ReservationID: "reservation-envelope", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: false}) {
		t.Fatalf("resolver request = %+v, want same reservation and identity-only mode", received)
	}
	if response.SchemaVersion != 2 || response.SnapshotDigest != digest || response.RoleMapVersion != 1 || response.Source != "bootstrap" || len(response.Profiles) != 0 {
		t.Fatalf("typed resolver envelope=(version:%d digest:%q role_map:%d source:%q profiles:%d), want actual bootstrap envelope without a synthetic Profile",
			response.SchemaVersion, response.SnapshotDigest, response.RoleMapVersion, response.Source, len(response.Profiles))
	}
}

func TestResolvePipelineLLMProfileResponsePreservesSharedSource(t *testing.T) {
	const digest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":2,"snapshot_digest":"` + digest + `","role_map_version":1,"source":"shared","profiles":[{"schema_version":2,"snapshot_digest":"` + digest + `","role_map_version":1,"id":"shared-fixture","revision":6,"provider":"custom","protocol":"openai_chat_completions","endpoint":"https://fixture.example/v1","models":{"fast":"fast-model","heavy":"heavy-model"}}]}`))
	}))
	defer server.Close()
	response, err := resolvePipelineLLMProfileResponseWithCredentials(context.Background(), workerConfig{
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-shared", UserID: "user-fixture", ProjectID: "project-fixture",
	}, false)
	if err != nil {
		t.Fatalf("resolve shared Pipeline profile response: %v", err)
	}
	if response.Source != "shared" || response.SchemaVersion != 2 || response.SnapshotDigest != digest || response.RoleMapVersion != 1 || len(response.Profiles) != 1 {
		t.Fatalf("shared resolver envelope=(source:%q version:%d digest:%q role_map:%d profiles:%d), want actual shared identity",
			response.Source, response.SchemaVersion, response.SnapshotDigest, response.RoleMapVersion, len(response.Profiles))
	}
	if profile := response.Profiles[0]; profile.ID != "shared-fixture" || profile.Models != (pipelineLLMModels{Fast: "fast-model", Heavy: "heavy-model"}) || len(profile.APIKey) != 0 {
		t.Fatalf("shared Profile=(id:%q models:%+v key_present:%t), want profile identity without credentials",
			profile.ID, profile.Models, len(profile.APIKey) > 0)
	}
}

func TestResolvePipelineLLMProfilesDecodesExactBaseLegacyWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"personal","profiles":[{"id":"legacy-profile","revision":3,"provider":"custom","protocol":"openai_chat_completions","endpoint":"https://fixture.example/v1","model":"legacy-model","api_key":"c3ludGhldGljLWxlZ2FjeS1rZXk="}]}`))
	}))
	defer server.Close()
	cfg := workerConfig{
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-legacy", UserID: "user-fixture", ProjectID: "project-fixture",
	}
	response, err := resolvePipelineLLMProfileResponseWithCredentials(context.Background(), cfg, true)
	if err != nil || response.Source != "personal" || response.SchemaVersion != 0 || response.SnapshotDigest != "" || response.RoleMapVersion != 0 || len(response.Profiles) != 1 {
		t.Fatalf("legacy typed resolver envelope=(source:%q version:%d digest_present:%t role_map:%d profiles:%d) err=%v; want exact-base source with no invented metadata",
			response.Source, response.SchemaVersion, response.SnapshotDigest != "", response.RoleMapVersion, len(response.Profiles), err)
	}
	legacyProfile := response.Profiles[0]
	if legacyProfile.SchemaVersion != 0 || legacyProfile.SnapshotDigest != "" || legacyProfile.RoleMapVersion != 0 || legacyProfile.Model != "legacy-model" ||
		legacyProfile.Models != (pipelineLLMModels{Fast: "legacy-model", Heavy: "legacy-model"}) || string(legacyProfile.APIKey) != "synthetic-legacy-key" {
		t.Fatalf("legacy typed profile metadata=(version:%d digest_present:%t role_map:%d model:%q models:%+v key_present:%t), want exact base role normalization",
			legacyProfile.SchemaVersion, legacyProfile.SnapshotDigest != "", legacyProfile.RoleMapVersion, legacyProfile.Model, legacyProfile.Models, len(legacyProfile.APIKey) > 0)
	}
	clearPipelineProfileKeys(response.Profiles)

	profiles, shared, err := resolvePipelineLLMProfiles(context.Background(), cfg)
	if err != nil || shared || len(profiles) != 1 {
		t.Fatalf("legacy resolver result count=%d shared=%t err=%v", len(profiles), shared, err)
	}
	defer clearPipelineProfileKeys(profiles)
	profile := profiles[0]
	if profile.SchemaVersion != 0 || profile.SnapshotDigest != "" || profile.RoleMapVersion != 0 || profile.Model != "legacy-model" ||
		profile.Models != (pipelineLLMModels{Fast: "legacy-model", Heavy: "legacy-model"}) || string(profile.APIKey) != "synthetic-legacy-key" {
		t.Fatalf("legacy normalized identity metadata=(version:%d digest_present:%t role_map:%d model:%q models:%+v key_present:%t), want exact base shape with same-value role normalization",
			profile.SchemaVersion, profile.SnapshotDigest != "", profile.RoleMapVersion, profile.Model, profile.Models, len(profile.APIKey) > 0)
	}
}

func TestResolvePipelineLLMProfilesKeepsV2WireStrict(t *testing.T) {
	for name, response := range map[string]string{
		"unknown top-level field":    `{"schema_version":2,"snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role_map_version":1,"source":"bootstrap","profiles":[],"extra":true}`,
		"legacy model mixed into v2": `{"schema_version":2,"snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role_map_version":1,"source":"personal","profiles":[{"schema_version":2,"snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role_map_version":1,"id":"p","revision":1,"provider":"custom","protocol":"openai_chat_completions","endpoint":"https://fixture.example/v1","model":"legacy","models":{"fast":"fast","heavy":"heavy"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			profiles, _, err := resolvePipelineLLMProfiles(context.Background(), workerConfig{
				LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
				PipelineReservationID: "reservation-v2", UserID: "user-fixture", ProjectID: "project-fixture",
			})
			clearPipelineProfileKeys(profiles)
			if err == nil {
				t.Fatal("invalid v2 response unexpectedly accepted")
			}
		})
	}
}

func TestPipelineQuotaFallbackRevalidatesEligibilityWithoutReplacingPinnedIdentity(t *testing.T) {
	pinnedA := testResolverProfile("revoked-a", "pinned-a-model", 4)
	pinnedB := testResolverProfile("eligible-b", "pinned-b-model", 7)
	var received pipelineLLMProfileResolverRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		// B is still eligible, but its current editable record has since changed
		// model. The reservation resolver must return the pinned immutable model.
		currentB := testResolverProfile("eligible-b", "pinned-b-model", 7)
		currentB.APIKey = nil
		writePipelineProfileWire(w, "shared", []pipelineLLMProfile{currentB})
	}))
	defer server.Close()
	cfg := workerConfig{LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-same", UserID: "user-fixture", ProjectID: "project-fixture", SharedProfilePool: true}
	eligible, err := revalidatePipelineProfileCandidates(context.Background(), cfg, []pipelineLLMProfile{pinnedA, pinnedB})
	if err != nil {
		t.Fatalf("identity-only revalidation failed: %v", err)
	}
	if received != (pipelineLLMProfileResolverRequest{ReservationID: "reservation-same", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: false}) {
		t.Fatalf("resolver request=%+v, want same reservation with IncludeCredentials=false", received)
	}
	if len(eligible) != 1 {
		t.Fatalf("eligible candidate count=%d, want revoked A filtered and B retained", len(eligible))
	}
	if eligible[0].ID != "eligible-b" || eligible[0].Revision != 7 || eligible[0].Models.Fast != "pinned-b-model" ||
		eligible[0].APIKey == nil || string(eligible[0].APIKey) != "synthetic-eligible-b" {
		t.Fatalf("eligible candidate=(id:%q rev:%d fast:%q key_present:%t), want B's original immutable model and credential only",
			eligible[0].ID, eligible[0].Revision, eligible[0].Models.Fast, len(eligible[0].APIKey) > 0)
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
	var credentialModes []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		if r.URL.Path != "/internal/pipeline/llm-profile" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		var request pipelineLLMProfileResolverRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		credentialModes = append(credentialModes, request.IncludeCredentials)
		profiles := append([]pipelineLLMProfile(nil), firstProfiles...)
		if !request.IncludeCredentials {
			for i := range profiles {
				profiles[i].APIKey = nil
			}
		}
		writePipelineProfileWire(w, "shared", profiles)
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
	if resolverCalls != 2 || strings.Join(selected, ",") != "profile-a,profile-b" || closed != 2 || len(credentialModes) != 2 || !credentialModes[0] || credentialModes[1] {
		t.Fatalf("resolver calls=%d credential_modes=%v selected=%v closed=%d, want credential-bearing initial snapshot then identity-only A quota -> B success", resolverCalls, credentialModes, selected, closed)
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

func TestStandaloneSuggestedQueriesStopsWhenNextSharedProfileIsRevoked(t *testing.T) {
	pinned := []pipelineLLMProfile{testResolverProfile("profile-a", "model-a", 1), testResolverProfile("profile-b", "model-b", 2)}
	var resolverCalls int
	var credentialModes []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		var request pipelineLLMProfileResolverRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		credentialModes = append(credentialModes, request.IncludeCredentials)
		profiles := pinned
		if !request.IncludeCredentials {
			profiles = pinned[:1] // profile-b was revoked after the reservation snapshot was taken.
		}
		profiles = append([]pipelineLLMProfile(nil), profiles...)
		if !request.IncludeCredentials {
			for index := range profiles {
				profiles[index].APIKey = nil
			}
		}
		writePipelineProfileWire(w, "shared", profiles)
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

	err := runSuggestedQueriesCommand(context.Background(), workerConfig{
		VaultPath: testSuggestedQueriesVault(t), ExecutionID: "standalone-revoked-suggested", SuggestedQueries: true,
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-same", UserID: "user-fixture", ProjectID: "project-fixture",
		APIKey: "platform-fallback-must-not-run", apiKeySet: true,
	})
	if err == nil || !strings.Contains(err.Error(), "profile pool exhausted") || resolverCalls != 2 ||
		strings.Join(selected, ",") != "profile-a" || closed != 1 || len(credentialModes) != 2 ||
		!credentialModes[0] || credentialModes[1] {
		t.Fatalf("error=%v resolver_calls=%d credentials=%v selected=%v closed=%d, want identity-only revalidation to filter revoked B without replay", err, resolverCalls, credentialModes, selected, closed)
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
	first, second := testResolverProfile("shared-first", "first-model", 1), testResolverProfile("shared-second", "second-model", 2)
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request pipelineLLMProfileResolverRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.IncludeCredentials {
			t.Errorf("identity-only resolver request=%+v err=%v", request, err)
		}
		first.APIKey, second.APIKey = nil, nil
		writePipelineProfileWire(w, "shared", []pipelineLLMProfile{first, second})
	}))
	defer resolver.Close()
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
		for _, markerPath := range []string{cfg.ProfileCallSuccessMarkerPath, cfg.ProfileCallAttemptMarkerPath} {
			if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("profile-call marker survived between attempts: stat err=%v", err)
			}
		}
		return nil
	}
	localWorkspaceBatchHooks = hooks
	cfg := workerConfig{
		WorkspaceDir: root, SharedProfilePool: true, QuotaMarkerPath: marker,
		ProfileCallSuccessMarkerPath: filepath.Join(root, "profile-call-success.marker"),
		ProfileCallAttemptMarkerPath: filepath.Join(root, "profile-call-attempt.marker"),
		PipelineReservationID:        "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		ProfileResolverURL: resolver.URL + "/internal/pipeline/llm-profile", LocalCloudScope: "test-scope",
		ProfileCandidates: []pipelineLLMProfile{testResolverProfile("shared-first", "first-model", 1), testResolverProfile("shared-second", "second-model", 2)},
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

func TestSharedProfileQuotaRetryStopsWhenNextCandidateWasRevoked(t *testing.T) {
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
	var resolverRequest pipelineLLMProfileResolverRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverCalls++
		if err := json.NewDecoder(r.Body).Decode(&resolverRequest); err != nil {
			t.Errorf("decode identity-only resolver request: %v", err)
		}
		currentA := testResolverProfile("shared-first", "first-model", 1)
		currentA.APIKey = nil
		writePipelineProfileWire(w, "shared", []pipelineLLMProfile{currentA})
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
		ProfileCallAttemptMarkerPath: filepath.Join(root, "profile-call-attempt.marker"),
		ProfileResolverURL:           server.URL + "/internal/pipeline/llm-profile", LocalCloudScope: "test-scope",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		ProfileCandidates: []pipelineLLMProfile{testResolverProfile("shared-first", "first-model", 1), testResolverProfile("removed-second", "second-model", 2)},
	}
	if got, err := runWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, root, firstWorkspace, nil, false); err == nil ||
		!strings.Contains(err.Error(), "no untried currently eligible candidate") || got != firstWorkspace {
		t.Fatalf("runWorkerBatchAtVaultWithProfilePool()=(%q,%v), want fail closed when B is revoked", got, err)
	}
	if resolverCalls != 1 || len(selected) != 1 || selected[0] != "shared-first" ||
		resolverRequest != (pipelineLLMProfileResolverRequest{ReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: false}) {
		t.Fatalf("resolver calls=%d request=%+v selected=%v, want revoked B filtered on the same identity-only snapshot", resolverCalls, resolverRequest, selected)
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
	var resolverRequest pipelineLLMProfileResolverRequest
	first, second := testResolverProfile("shared-first", "first-model", 1), testResolverProfile("shared-second", "second-model", 2)
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&resolverRequest); err != nil {
			t.Errorf("decode identity-only resolver request: %v", err)
		}
		if resolverRequest.IncludeCredentials {
			t.Errorf("fallback resolver requested credentials: %+v", resolverRequest)
		}
		first.APIKey, second.APIKey = nil, nil
		writePipelineProfileWire(w, "shared", []pipelineLLMProfile{first, second})
	}))
	defer resolver.Close()
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
		ProfileCallAttemptMarkerPath: filepath.Join(root, "profile-call-attempt.marker"),
		PipelineReservationID:        "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		ProfileResolverURL: resolver.URL + "/internal/pipeline/llm-profile", LocalCloudScope: "test-scope",
		ProfileCandidates: []pipelineLLMProfile{testResolverProfile("shared-first", "first-model", 1), testResolverProfile("shared-second", "second-model", 2)},
	}

	workspace, err := runCloudWorkerBatchAtVaultWithProfilePool(context.Background(), cfg, [][]string{{"run"}}, vault)
	if err != nil {
		t.Fatalf("runCloudWorkerBatchAtVaultWithProfilePool() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	if len(selected) != 2 || selected[0] != "shared-first" || selected[1] != "shared-second" {
		t.Fatalf("selected profiles = %v, want ordered explicit-quota fallback", selected)
	}
	if resolverRequest != (pipelineLLMProfileResolverRequest{ReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: false}) {
		t.Fatalf("fallback resolver request=%+v, want same reservation identity only", resolverRequest)
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
