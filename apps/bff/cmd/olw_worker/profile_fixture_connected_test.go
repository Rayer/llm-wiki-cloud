//go:build byokprofilefixture

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/byok"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

// This connected fixture uses the actual user-profile, Project-binding, Query,
// Pipeline resolver, worker, Synto and publication paths. Only identity context,
// Firestore and provider transport are disposable local fixtures.
func TestPersistedProjectProfileConnectsQueryAndPipelinePublication(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is required for the disposable connected BYOK fixture")
	}
	host, port, err := net.SplitHostPort(endpoint)
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if err != nil || port == "" || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
		t.Fatalf("connected BYOK fixture requires a loopback Firestore emulator, got %q", endpoint)
	}
	connection, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		t.Fatalf("local Firestore emulator unavailable: %v", err)
	}
	_ = connection.Close()
	python := os.Getenv("SYNTO_TEST_PYTHON")
	if python == "" || !filepath.IsAbs(python) {
		t.Skip("SYNTO_TEST_PYTHON must point to the pinned Synto 0.7.0 interpreter")
	}
	pythonPath, err := exec.LookPath(python)
	if err != nil {
		t.Fatalf("resolve pinned Synto interpreter: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(pythonPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	gotPython, err := exec.LookPath("python3")
	if err != nil || filepath.Clean(gotPython) != filepath.Clean(pythonPath) {
		t.Fatalf("worker python3=%q, want pinned interpreter %q (err=%v)", gotPython, pythonPath, err)
	}
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	t.Setenv("LOCAL_CLOUD_SCOPE", "")
	gin.SetMode(gin.TestMode)

	userID := fmt.Sprintf("byok-fixture-user-%d", time.Now().UnixNano())
	projectID := fmt.Sprintf("byok-fixture-project-%d", time.Now().UnixNano())
	scope := fmt.Sprintf("byok-fixture-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	fs, err := internalfirestore.NewClientWithDatabaseAndScope("lwc-byok-profile-fixture", "", userID, projectID, scope)
	if err != nil {
		t.Fatalf("create disposable Firestore client: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	projectRef := internalfirestore.Collection(fs.Raw(), "projects").Doc(userID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("seed disposable owner Project: %v", err)
	}

	root := localfs.New(t.TempDir())
	projectStore := root.Scope(userID, projectID)
	concepts, err := json.Marshal(cache.Entry{
		Slug: "byok-profile-routing", Title: "BYOK Profile Routing",
		Body:        "The Project uses the saved provider profile for Query and Pipeline calls.",
		Frontmatter: map[string]any{"id": "a3f7b2c01d9d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.WriteBytes(ctx, append(concepts, '\n'), cache.GCSPath); err != nil {
		t.Fatalf("seed disposable Query corpus: %v", err)
	}
	if _, err := projectStore.WriteBytes(ctx, []byte("---\nid: a3f7b2c01d9d\ntitle: BYOK Profile Routing\n---\nThe Project uses the saved provider profile for Query and Pipeline calls.\n"), "wiki/byok-profile-routing.md"); err != nil {
		t.Fatalf("seed disposable Query page: %v", err)
	}

	if _, err := projectStore.WriteBytes(ctx, []byte(`{"concept":{"a3f7b2c01d9d":"byok-profile-routing"}}`), "cache/id_map.json"); err != nil {
		t.Fatalf("seed disposable Query citation map: %v", err)
	}

	conceptCache := cache.New()
	baseLLM := llm.NewClient("synthetic-base-never-used")
	h := handlerv1.New(root, fs, search.NewIndex(), conceptCache, baseLLM, nil)
	box, err := byok.NewCredentialBox(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h.SetBYOKCredentialBox(box)
	h.SetBYOKCredentialEnvironment(scope)
	queryRuntime, err := queryruntime.NewExecutor(connectedQueryConfig(t), conceptCache, baseLLM, nil, connectedOfflineSynthesizer{})
	if err != nil {
		t.Fatalf("build production-shaped Query runtime: %v", err)
	}
	h.SetQueryExecutor(connectedQueryErrorObserver{Executor: queryRuntime, t: t})
	reservationID := "fixture-reservation"
	h.SetPipelineQuotaStore(connectedFixtureQuotaStore{reservation: internalfirestore.QuotaReservation{
		ID: reservationID, UserID: userID, ProjectID: projectID, SettlementStatus: "pending",
	}})
	h.SetLocalPipelineExecutor(connectedFixtureLocalPipeline{})

	router := gin.New()
	withOwner := func(c *gin.Context) { c.Set("userID", userID); c.Next() }
	withProject := func(c *gin.Context) { c.Set("userID", userID); c.Set("projectID", projectID); c.Next() }
	router.POST("/api/v1/llm/profiles", withOwner, h.CreateUserLLMProfile)
	router.GET("/api/v1/llm/profiles", withOwner, h.UserLLMProfiles)
	router.GET("/api/v1/projects/:pid/llm-binding", withOwner, h.ProjectLLMBinding)
	router.PUT("/api/v1/projects/:pid/llm-binding", withOwner, h.SetProjectLLMBinding)
	router.POST("/api/v1/query", withProject, h.Query)
	router.POST("/internal/pipeline/llm-profile", h.PipelineLLMProfile)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()

	profileBody := `{"name":"Disposable project model","provider":"custom","protocol":"openai_chat_completions","endpoint":"https://fixture.example/v1","model":"fixture-model","api_key":"synthetic-fixture-secret"}`
	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/llm/profiles", strings.NewReader(profileBody))
	createReq.Header.Set("Content-Type", "application/json")
	createResponse, err := client.Do(createReq)
	if err != nil {
		t.Fatalf("create profile through BFF API: %v", err)
	}
	var created struct {
		ID               string `json:"id"`
		Revision         int64  `json:"revision"`
		SecretConfigured bool   `json:"secret_configured"`
	}
	decodeErr := json.NewDecoder(createResponse.Body).Decode(&created)
	_ = createResponse.Body.Close()
	if createResponse.StatusCode != http.StatusCreated || decodeErr != nil || created.ID == "" || created.Revision < 1 || !created.SecretConfigured {
		t.Fatalf("profile create status=%d result=%+v decodeErr=%v", createResponse.StatusCode, created, decodeErr)
	}

	profilesResponse, err := client.Get(server.URL + "/api/v1/llm/profiles")
	if err != nil {
		t.Fatalf("read profile settings: %v", err)
	}
	profilesJSON, readErr := readAndClose(profilesResponse.Body)
	if profilesResponse.StatusCode != http.StatusOK || readErr != nil || strings.Contains(string(profilesJSON), "synthetic-fixture-secret") {
		t.Fatalf("write-only profile metadata response status=%d readErr=%v", profilesResponse.StatusCode, readErr)
	}

	bindingURL := server.URL + "/api/v1/projects/" + projectID + "/llm-binding"
	initialBindingResponse, err := client.Get(bindingURL)
	if err != nil {
		t.Fatalf("read initial Project binding: %v", err)
	}
	var initialBinding struct {
		Source   string `json:"source"`
		Revision int64  `json:"revision"`
	}
	decodeErr = json.NewDecoder(initialBindingResponse.Body).Decode(&initialBinding)
	_ = initialBindingResponse.Body.Close()
	if initialBindingResponse.StatusCode != http.StatusOK || decodeErr != nil || initialBinding.Source != "shared" {
		t.Fatalf("initial binding status=%d binding=%+v decodeErr=%v", initialBindingResponse.StatusCode, initialBinding, decodeErr)
	}
	bindBody, _ := json.Marshal(map[string]any{"source": "personal", "profile_id": created.ID, "revision": initialBinding.Revision})
	bindRequest, _ := http.NewRequest(http.MethodPut, bindingURL, bytes.NewReader(bindBody))
	bindRequest.Header.Set("Content-Type", "application/json")
	bindResponse, err := client.Do(bindRequest)
	if err != nil {
		t.Fatalf("save Project binding through BFF API: %v", err)
	}
	var savedBinding struct {
		Source          string `json:"source"`
		ProfileID       string `json:"profile_id"`
		ProfileRevision int64  `json:"profile_revision"`
		Revision        int64  `json:"revision"`
	}
	decodeErr = json.NewDecoder(bindResponse.Body).Decode(&savedBinding)
	_ = bindResponse.Body.Close()
	if bindResponse.StatusCode != http.StatusOK || decodeErr != nil || savedBinding.Source != "personal" || savedBinding.ProfileID != created.ID || savedBinding.ProfileRevision != created.Revision {
		t.Fatalf("saved binding status=%d binding=%+v decodeErr=%v", bindResponse.StatusCode, savedBinding, decodeErr)
	}

	queryReceipt := filepath.Join(t.TempDir(), "query-provider.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", queryReceipt)
	queryRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/query", strings.NewReader(`{"q":"BYOK profile routing","mode":"wiki"}`))
	queryRequest.Header.Set("Content-Type", "application/json")
	queryResponse, err := client.Do(queryRequest)
	if err != nil {
		t.Fatalf("connected Query request: %v", err)
	}
	queryBody, readErr := readAndClose(queryResponse.Body)
	var queryResult handlerapi.QueryResponse
	decodeErr = json.Unmarshal(queryBody, &queryResult)
	if queryResponse.StatusCode != http.StatusOK || decodeErr != nil || queryResult.Query != "BYOK profile routing" ||
		queryResponse.Header.Get("X-LLM-Profile-ID") != created.ID || queryResponse.Header.Get("X-LLM-Profile-Revision") != fmt.Sprint(created.Revision) ||
		queryResponse.Header.Get("X-LLM-Protocol") != string(llm.ProtocolOpenAIChatCompletions) || queryResponse.Header.Get("X-LLM-Model") != "fixture-model" {
		t.Fatalf("connected Query status=%d result=%+v body=%s profile=%q model=%q readErr=%v decodeErr=%v", queryResponse.StatusCode, queryResult, queryBody, queryResponse.Header.Get("X-LLM-Profile-ID"), queryResponse.Header.Get("X-LLM-Model"), readErr, decodeErr)
	}
	queryReceipts, err := os.ReadFile(queryReceipt)
	if err != nil || !strings.Contains(string(queryReceipts), "openai_chat_completions\tfixture-model\t/v1/chat/completions") || strings.Contains(string(queryReceipts), "synthetic-fixture-secret") {
		t.Fatalf("sanitized Query provider receipt=%q err=%v", queryReceipts, err)
	}

	profileFixtureReceipt := filepath.Join(t.TempDir(), "pipeline-provider.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", profileFixtureReceipt)
	t.Setenv("LWC_PROFILE_RESOLVER_URL", server.URL+"/internal/pipeline/llm-profile")
	setProfileResolverForTest := server.URL + "/internal/pipeline/llm-profile"
	vault := testSuggestedQueriesVault(t)
	resetPinnedSyntoState(t, pythonPath, vault)
	rawSource := filepath.Join(vault, "raw", "lwc-byok-disposable-source.md")
	if err := os.MkdirAll(filepath.Dir(rawSource), 0o700); err != nil {
		t.Fatalf("create disposable raw input directory: %v", err)
	}
	if err := os.WriteFile(rawSource, []byte("A new source explains the selected BYOK profile and Project routing."), 0o600); err != nil {
		t.Fatal(err)
	}
	workerCfg := workerConfig{
		VaultPath: vault, WorkspaceDir: t.TempDir(), UserID: userID, ProjectID: projectID, ExecutionID: "fixture-execution",
		PipelineReservationID: reservationID, ProfileResolverURL: setProfileResolverForTest, LocalCloudScope: scope,
		APIKey: "platform-fallback-must-not-run", apiKeySet: true, Postprocess: true, SuggestedQueries: true, StopOnError: true,
	}
	if err := runWorkerBatch(ctx, workerCfg, `[["run","--auto-approve"]]`); err != nil {
		t.Fatalf("connected Pipeline/Synto run and local publication: %v", err)
	}
	pipelineReceipts, err := os.ReadFile(profileFixtureReceipt)
	if err != nil || !strings.Contains(string(pipelineReceipts), "openai_chat_completions\tfixture-model\t/v1/chat/completions") || strings.Contains(string(pipelineReceipts), "synthetic-fixture-secret") {
		t.Fatalf("sanitized Pipeline/Synto provider receipt=%q err=%v", pipelineReceipts, err)
	}
	artifactBytes, err := os.ReadFile(filepath.Join(vault, suggestedqueries.Path))
	if err != nil {
		t.Fatalf("read published Pipeline suggested-query artifact: %v", err)
	}
	artifact, err := suggestedqueries.Decode(artifactBytes)
	if err != nil || len(artifact.Candidates) != suggestedqueries.RequiredQueries {
		t.Fatalf("decode published suggested-query artifact: candidates=%d err=%v", len(artifact.Candidates), err)
	}
	metadata := artifact.Candidates[0].Generation
	if metadata.ProfileID != created.ID || metadata.ProfileRevision != created.Revision || metadata.Model != "fixture-model" || metadata.Protocol != string(llm.ProtocolOpenAIChatCompletions) {
		t.Fatalf("published Pipeline artifact provenance=%+v, want persisted Project profile", metadata)
	}
	if _, err := os.Stat(filepath.Join(vault, "cache", "id_map.json")); err != nil {
		t.Fatalf("Synto postprocess outputs were not published to the disposable Project vault: %v", err)
	}
}

func resetPinnedSyntoState(t *testing.T, python, vault string) {
	t.Helper()
	statePath := filepath.Join(vault, ".synto", "state.db")
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove stale Synto state fixture: %v", err)
	}
	command := exec.Command(python, "-c", "from pathlib import Path; from synto.state import StateDB; db=StateDB(Path(__import__('sys').argv[1])); db.close()", statePath)
	command.Dir = vault
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("initialize disposable Synto 0.7.0 state: %v: %s", err, output)
	}
}

func readAndClose(reader io.ReadCloser) ([]byte, error) {
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	return data, errors.Join(err, closeErr)
}

type connectedQueryErrorObserver struct {
	query.Executor
	t *testing.T
}

func (o connectedQueryErrorObserver) Execute(ctx context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
	result, err := o.Executor.Execute(ctx, reader, request)
	if err != nil {
		o.t.Logf("connected Query executor cause: %T: %+v", err, err)
	}
	return result, err
}

func connectedQueryConfig(t *testing.T) queryconfig.Config {
	t.Helper()
	profile := queryquality.DefaultRetrievalProfile()
	digest, err := profile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	prompt, ok := queryquality.LookupPrompt(queryquality.StructuredPlanPromptID)
	if !ok {
		t.Fatal("missing default Query prompt")
	}
	config, err := queryconfig.Seal(queryconfig.Config{
		SchemaVersion: queryconfig.SchemaVersion, ConfigRevision: "byok-fixture", QueryServiceImplementation: queryconfig.QueryServiceImplementation,
		Stages: queryconfig.Stages{
			QueryExpander: queryconfig.QueryExpanderStage{
				Provider: queryconfig.ProviderDeepSeek, Implementation: queryconfig.QueryExpanderImplementation, Model: "deepseek-flash",
				Reasoning: "none", DefaultProfileID: profile.ID, DefaultProfileDigest: digest, DefaultPromptID: prompt.ID,
				DefaultPromptDigest: prompt.TemplateDigest, KeywordsPerAttempt: 24, Attempts: 3,
			},
			CandidateMatcher:  queryconfig.CandidateMatcherStage{Implementation: queryconfig.CandidateMatcherImplementation, EvidenceThreshold: 2, RareKeywordMaxDocumentFrequency: 1},
			ResultSelector:    queryconfig.ResultSelectorStage{Implementation: queryconfig.ResultSelectorImplementation, Limit: 10, ExplorationSlots: 1, SeedPolicy: queryconfig.SeedPolicy},
			AnswerSynthesizer: queryconfig.AnswerSynthesizerStage{Provider: queryconfig.ProviderDeepSeek, Implementation: queryconfig.AnswerSynthesizerImplementation, Model: "deepseek-flash", Reasoning: "none", NoEvidencePolicy: queryconfig.NoEvidencePolicy},
		},
		Profiles: []queryconfig.Profile{{ID: profile.ID, CriterionPolicy: profile.CriterionPolicy, ProfileDigest: digest}},
	})
	if err != nil {
		t.Fatalf("seal Query config: %v", err)
	}
	return config
}

type connectedOfflineSynthesizer struct{}

func (connectedOfflineSynthesizer) SynthesizeWithError(_ context.Context, _ cache.Reader, _ query.Request, result query.Result) (query.Result, error) {
	return result, nil
}
func (connectedOfflineSynthesizer) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: queryconfig.ProviderDeepSeek, Model: "deepseek-flash", Reasoning: "none"}, true
}

type connectedFixtureLocalPipeline struct{}

func (connectedFixtureLocalPipeline) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", errors.New("not used by the profile resolver fixture")
}
func (connectedFixtureLocalPipeline) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	return nil, errors.New("not used by the profile resolver fixture")
}
func (connectedFixtureLocalPipeline) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

type connectedFixtureQuotaStore struct {
	reservation internalfirestore.QuotaReservation
}

func (s connectedFixtureQuotaStore) LoadQuotaState(context.Context, string, string) (int, string, time.Time, error) {
	return 0, "", time.Time{}, nil
}
func (s connectedFixtureQuotaStore) ReserveQuota(context.Context, string, string, string, pipelinequota.Limits, time.Time, pipelinequota.ExecutionMode, bool, bool, int, int, int) (pipelinequota.Snapshot, bool, error) {
	return pipelinequota.Snapshot{Enforced: true, Allowed: true}, true, nil
}
func (s connectedFixtureQuotaStore) LinkQuotaReservation(context.Context, string, string) error {
	return nil
}
func (s connectedFixtureQuotaStore) GetQuotaReservation(_ context.Context, reservationID string) (internalfirestore.QuotaReservation, bool, error) {
	if reservationID != s.reservation.ID {
		return internalfirestore.QuotaReservation{}, false, nil
	}
	return s.reservation, true, nil
}
func (connectedFixtureQuotaStore) ListPendingQuotaReservations(context.Context) ([]internalfirestore.QuotaReservation, error) {
	return nil, nil
}
func (s connectedFixtureQuotaStore) SettleQuotaReservation(context.Context, string, string) (string, error) {
	return s.reservation.SettlementStatus, nil
}
