package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	runtimeconfig "github.com/rayer/llm-wiki-bff/internal/config"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
	"google.golang.org/api/secretmanager/v1"
)

type fakeSecretReader struct {
	resource      string
	resources     []string
	resolved      string
	value         []byte
	err           error
	identity      projectIdentity
	identityErr   error
	identityCalls int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

type mcpCaCAdmissionBarrierRoot struct {
	storage.RootStore
	entered chan struct{}
	release chan struct{}
	active  atomic.Bool
	content *gcs.Client
}

func (r *mcpCaCAdmissionBarrierRoot) Scope(userID, projectID string) storage.Store {
	if r.active.Load() {
		r.entered <- struct{}{}
		<-r.release
	}
	if r.content != nil {
		return r.content.WithScope(userID, projectID)
	}
	return r.RootStore.Scope(userID, projectID)
}

type mcpCaCGCSObject struct {
	data       []byte
	generation int64
}

func newMcpCaCContentFixture(t *testing.T, scope, userID, projectID string) (*gcs.Client, *httptest.Server) {
	t.Helper()
	const contentGeneration = "g_mcp_cac_sdk_fixture"
	const targetConcepts, targetMentions = 50, 51
	conceptsByID := map[string]string{"alpha-id": "alpha"}
	pages := map[string][]byte{"wiki/sources/source-one.md": []byte("---\nid: source-one\ntitle: Source One\n---\n# Source One\n")}
	var concepts strings.Builder
	concepts.WriteString("{\"slug\":\"alpha\",\"title\":\"Alpha Guide\",\"frontmatter\":{\"id\":\"alpha-id\",\"sources\":[]}}\n")
	mentionTargets := make([]string, 0, targetMentions)
	for i := 1; i <= targetConcepts; i++ {
		id, slug := fmt.Sprintf("concept-%02d-id", i), fmt.Sprintf("concept-%02d", i)
		conceptsByID[id] = slug
		pages["wiki/"+slug+".md"] = []byte(fmt.Sprintf("---\nid: %s\ntitle: Concept %02d\n---\n# Concept %02d\n", id, i, i))
		fmt.Fprintf(&concepts, "{\"slug\":%q,\"title\":%q,\"frontmatter\":{\"id\":%q}}\n", slug, fmt.Sprintf("Concept %02d", i), id)
		mentionTargets = append(mentionTargets, "[["+id+"]]")
	}
	mentionTargets = append(mentionTargets, "[[source-one]]")
	alphaBody := "---\nid: alpha-id\ntitle: Alpha Guide\nsources: []\n---\n# Alpha\n\n" + strings.Repeat("fixture content ", 1100) + "\n" + strings.Join(mentionTargets, " ") + "\n"
	pages["wiki/alpha.md"] = []byte(alphaBody)
	if len(mentionTargets) != targetMentions {
		t.Fatalf("MCP CaC fixture mentions=%d, want %d", len(mentionTargets), targetMentions)
	}
	idMapBytes, err := json.Marshal(wikiindex.IDMap{
		Concept:     conceptsByID,
		Source:      map[string]string{"source-one": "source-one"},
		SourceMeta:  map[string]wikiindex.SourceMeta{"source-one": {Slug: "source-one", Title: "Source One"}},
		Redirects:   map[string][]string{},
		IDRedirects: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"cache/id_map.json": idMapBytes, "cache/concepts.jsonl": []byte(concepts.String())}
	for name, data := range pages {
		files[name] = data
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	manifest := generation.Manifest{Version: generation.Version, GenerationID: contentGeneration, CreatedAt: "2026-11-05T00:00:00Z", InputFingerprint: "local-mcp-cac-fixture", Files: make([]generation.File, 0, len(paths))}
	objects := map[string]mcpCaCGCSObject{}
	for i, name := range paths {
		file, err := generation.NewFile(name, files[name], int64(100+i))
		if err != nil {
			t.Fatalf("make CaC fixture manifest entry %q: %v", name, err)
		}
		manifest.Files = append(manifest.Files, file)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate CaC fixture generation manifest: %v", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "local_scopes/" + scope + "/users/" + userID + "/projects/" + projectID + "/"
	for _, file := range manifest.Files {
		objects[prefix+manifest.ObjectPath(file)] = mcpCaCGCSObject{data: files[file.Path], generation: file.Generation}
	}
	archivePath, err := generation.ArchivedManifestPath(contentGeneration)
	if err != nil {
		t.Fatal(err)
	}
	objects[prefix+archivePath] = mcpCaCGCSObject{data: manifestBytes, generation: 900}
	objects[prefix+generation.ManifestPath] = mcpCaCGCSObject{data: manifestBytes, generation: 901}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		marker := strings.LastIndex(escaped, "/o/")
		encodedName := ""
		if marker >= 0 {
			encodedName = escaped[marker+3:]
		} else if strings.HasPrefix(escaped, "/mcp-cac-content-fixture/") {
			encodedName = strings.TrimPrefix(escaped, "/mcp-cac-content-fixture/")
		}
		if encodedName == "" {
			http.NotFound(w, r)
			return
		}
		name, err := url.PathUnescape(encodedName)
		if err != nil {
			http.Error(w, "invalid object path", http.StatusBadRequest)
			return
		}
		object, ok := objects[name]
		if !ok || r.Method != http.MethodGet {
			t.Errorf("MCP CaC fixture received unexpected object request method=%s name=%q found=%t", r.Method, name, ok)
			http.NotFound(w, r)
			return
		}
		if requested := r.URL.Query().Get("generation"); requested != "" && requested != strconv.FormatInt(object.generation, 10) {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(r.URL.Path, "/storage/v1/") || r.URL.Query().Get("alt") == "media" {
			w.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
			w.Header().Set("X-Goog-Generation", strconv.FormatInt(object.generation, 10))
			_, _ = w.Write(object.data)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name": name, "bucket": "mcp-cac-content-fixture", "generation": strconv.FormatInt(object.generation, 10),
			"metageneration": "1", "size": strconv.Itoa(len(object.data)), "updated": "2026-11-05T00:00:00Z", "contentType": "application/octet-stream",
		})
	}))
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "mcp-cac-content-fixture")
	client, err := gcs.NewClientWithScope("mcp-cac-content-fixture", scope)
	if err != nil {
		server.Close()
		t.Fatalf("create local GCS client for CaC fixture: %v", err)
	}
	pinCtx, cancelPin := context.WithTimeout(context.Background(), 5*time.Second)
	_, pinned, exists, pinErr := client.WithScope(userID, projectID).PinCurrentGenerationIfPublished(pinCtx)
	cancelPin()
	if pinErr != nil || !exists || pinned.Manifest.GenerationID != contentGeneration {
		_ = client.Close()
		server.Close()
		t.Fatalf("pin current CaC fixture generation=%q exists=%t err=%v", pinned.Manifest.GenerationID, exists, pinErr)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, server
}

func newOwnedLoopbackTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:18631")
	if err != nil {
		t.Fatalf("listen on owned loopback port 18631: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type syntheticURLFetcher struct{}

func (syntheticURLFetcher) Fetch(context.Context, string) (urlintake.Page, error) {
	return urlintake.Page{Title: "Synthetic article", Markdown: "# Synthetic article\n\nReadable fixture body."}, nil
}

type syntheticPublicResolver struct{}

func (syntheticPublicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func assertGeneratedConfigUsesQualityConsumers(t *testing.T, cfg runtimeconfig.Config, environment, expectedKey string) {
	t.Helper()
	if cfg.TypeSafeAPIKey != expectedKey {
		t.Fatalf("runtime loader TypeSafe key mismatch for %s", environment)
	}
	profileEvaluator := profiletags.NewJevEvaluator(cfg.TypeSafeAPIKey)
	if profileEvaluator.APIKey != expectedKey {
		t.Fatalf("Profile Jev evaluator did not receive loaded CaC key for %s", environment)
	}

	var mu sync.Mutex
	var observedAuthorization string
	server := newOwnedLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		observedAuthorization = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"access_shell":{"type":"noul","noul":0.02},"readable_content":{"type":"noul","noul":0.97}},"usage":{"input_tokens":800,"output_tokens":9}}`))
	}))
	defer server.Close()
	evaluator := urlintake.NewJevEvaluator(cfg.TypeSafeAPIKey)
	evaluator.Endpoint = server.URL
	evaluator.Client = server.Client()
	store := localfs.New(t.TempDir())
	service := urlintake.NewService(syntheticURLFetcher{}, evaluator, func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
		return store.WithScope(scope.ActorID, scope.ProjectID), nil
	}, urlintake.DefaultQualityThresholds)
	service.Resolver = syntheticPublicResolver{}
	result, err := service.Prepare(context.Background(), urlintake.ActorProject{ActorID: "synthetic-actor", ProjectID: cfg.GCPProject}, urlintake.ImportRequest{URL: "https://example.com/article"})
	if err != nil || result.Status != "preview" || result.Preview == nil || !result.Preview.QualityChecked || result.Preview.QualityStatus != "usable" {
		t.Fatalf("generated %s config did not produce a normally checked preview: result=%#v err=%v", environment, result, err)
	}
	mu.Lock()
	authorization := observedAuthorization
	mu.Unlock()
	if authorization != "Bearer "+expectedKey {
		t.Fatalf("controlled Jev provider authorization for %s did not come from the loaded config", environment)
	}
}

func (r *fakeSecretReader) Access(_ context.Context, resource string) (string, []byte, error) {
	r.resource = resource
	r.resources = append(r.resources, resource)
	resolved := r.resolved
	if resolved == "" {
		resolved = resource
		if strings.HasSuffix(resource, "/latest") {
			resolved = strings.TrimSuffix(resource, "latest") + "1"
		}
	}
	value := r.value
	if strings.Contains(resource, "/byok-credential-encryption-key-") {
		value = []byte(strings.Repeat("a", 64))
	}
	return resolved, value, r.err
}

func TestRealPklBFFPrepareRendersSchema3ConsumerForLocalAndCloud(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for real BFF prepare acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}

	root := t.TempDir()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_LOCAL_LLM_KEY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TYPESAFE_JEV_API_KEY", "TEST_ONLY_LEGACY_JEV_DECOY")
	t.Setenv("TYPESAFE_API_KEY", "TEST_ONLY_LOCAL_TYPESAFE_API_KEY")

	localRoot := filepath.Join(root, "local")
	if err := os.MkdirAll(localRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	jwtPath := filepath.Join(localRoot, "synthetic-jwt-key")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc369-synthetic-acceptance")
	t.Setenv("LOCAL_CLOUD_WORKER_PATH", filepath.Join(localRoot, "olw_worker"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_CONFIG_PATH", filepath.Join(localRoot, "synthetic-synto.toml"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_BINDINGS_PATH", filepath.Join(localRoot, "synthetic-bindings.json"))
	t.Setenv("LOCAL_CLOUD_JWT_SECRET_FILE", jwtPath)
	t.Setenv("PIPELINE_DEMO_USER_IDS", "")
	t.Setenv("GCP_PROJECT", "llm-wiki-cloud")
	t.Setenv("BUCKET", "llm-wiki-cloud-local")
	t.Setenv("FIRESTORE_DATABASE_ID", "llm-wiki-cloud-local")
	t.Setenv("ALLOWED_HOSTS", "localhost,127.0.0.1")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:13000")
	t.Setenv("AUTH_SERVICE_URL", "http://localhost:18081")
	t.Setenv("DEV_JWT", "false")
	t.Setenv("LOCAL_DATA_DIR", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("PIPELINE_COOLDOWN_SECONDS", "")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")

	for _, tc := range []struct {
		environment       string
		cooldown          int
		typesafe          string
		documentImportURL string
		profileAudience   string
		profileService    string
		resources         []string
	}{
		{environment: "local", cooldown: 60, typesafe: "TEST_ONLY_LOCAL_TYPESAFE_API_KEY"},
		{environment: "dev", cooldown: 600, typesafe: "TEST_ONLY_SYNTHETIC_CLOUD_SECRET",
			documentImportURL: "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/document-import-job-dev:run",
			profileAudience:   "https://llm-wiki-bff-dev-a5nkmux6pq-de.a.run.app", profileService: "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com", resources: []string{
				"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
				"projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-dev/versions/1",
				"projects/llm-wiki-cloud/secrets/byok-credential-encryption-key-dev/versions/1",
			}},
		{environment: "prod", cooldown: 3600, typesafe: "TEST_ONLY_SYNTHETIC_CLOUD_SECRET",
			documentImportURL: "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/document-import-job:run",
			profileAudience:   "https://llm-wiki-bff-a5nkmux6pq-de.a.run.app", profileService: "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com", resources: []string{
				"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
				"projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-prod/versions/1",
				"projects/llm-wiki-cloud/secrets/byok-credential-encryption-key-prod/versions/1",
			}},
	} {
		t.Run(tc.environment, func(t *testing.T) {
			reader := &fakeSecretReader{value: []byte("TEST_ONLY_SYNTHETIC_CLOUD_SECRET")}
			output := filepath.Join(root, tc.environment+"-output")
			if err := runPrepareTarget(context.Background(), "bff", tc.environment, output,
				func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
				t.Fatalf("real Pkl BFF prepare failed: %v", err)
			}
			if len(reader.resources) != len(tc.resources) {
				t.Fatalf("selected secret reads=%d, want %d", len(reader.resources), len(tc.resources))
			}
			for i := range tc.resources {
				if reader.resources[i] != tc.resources[i] {
					t.Fatalf("secret read %d did not match the selected BFF reference", i)
				}
			}
			data, err := os.ReadFile(filepath.Join(output, "bff.json"))
			if err != nil {
				t.Fatalf("prepared BFF file is unavailable: %v", err)
			}
			generated, err := runtimeconfig.DecodeBFFFile(data)
			if err != nil {
				t.Fatalf("prepared BFF runtime schema failed validation: %v", err)
			}
			wantTTL := map[string]int{"local": 0, "dev": 60_000, "prod": 300_000}[tc.environment]
			wantBYOKKey := ""
			if tc.environment != "local" {
				wantBYOKKey = strings.Repeat("a", 64)
			}
			wantDeepSeekReference := "projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest"
			if tc.environment == "local" {
				wantDeepSeekReference = "environment:LLM_API_KEY"
			}
			wantSchema := 3
			if generated.SchemaVersion != wantSchema || generated.Environment != tc.environment || generated.Target != "bff" ||
				generated.PipelineCooldownSeconds != tc.cooldown || generated.TypeSafeAPIKey != tc.typesafe ||
				generated.DocumentImportJobURL != tc.documentImportURL ||
				generated.BYOKCredentialEncryptionKey != wantBYOKKey || generated.DeepSeekAPIKeyReference != wantDeepSeekReference ||
				generated.ProfileRuntimeAudience != tc.profileAudience || generated.ProfileRuntimeServiceAccount != tc.profileService ||
				generated.MCP == nil || generated.MCP.DiscoveryTTLMS != wantTTL || generated.MCP.ToolsListTTLMS != wantTTL {
				t.Fatalf("prepared BFF identity or cooldown is incorrect for %s", tc.environment)
			}
			if tc.environment == "local" && (generated.JWTSecret != "" || generated.AuthConsumer == nil ||
				generated.AuthConsumer.RuntimeServiceAccount != "bff-local@localhost" ||
				generated.AuthConsumer.VerifierAudience != generated.AuthServiceURL ||
				len(generated.AuthConsumer.AuthorityModes) != 1 || generated.AuthConsumer.AuthorityModes[0] != "synthetic-local-fixture") {
				t.Fatal("local schema-3 BFF config lost its isolated synthetic Auth consumer")
			}
			if tc.environment != "local" && (generated.JWTSecret != "" || generated.AuthConsumer == nil ||
				generated.AuthConsumer.Environment != tc.environment || generated.AuthConsumer.TimeoutSeconds != 5 ||
				generated.AuthConsumer.Resource != generated.MCPResourceOrigin) {
				t.Fatalf("cloud schema-3 BFF consumer projection is incorrect for %s", tc.environment)
			}
			if generated.PipelineSchedule.Cron != "0 * * * *" || generated.PipelineSchedule.Timezone != "UTC" ||
				generated.PipelineSchedule.Audience != tc.profileAudience || generated.PipelineSchedule.ServiceAccount != tc.profileService {
				t.Fatalf("prepared BFF schedule does not match the environment's hourly UTC identity for %s: %+v", tc.environment, generated.PipelineSchedule)
			}
			if generated.LLM.Provider != "deepseek" || generated.LLM.BaseURL != "https://api.deepseek.com" ||
				generated.LLM.RequestTimeoutSeconds != 60 || generated.LLM.Model != "deepseek-flash" {
				t.Fatalf("prepared BFF LLM connection options are incorrect for %s", tc.environment)
			}
			if tc.environment == "local" {
				if generated.PortDefault != 18080 || generated.AuthServiceURL != "http://localhost:18081" ||
					len(generated.AllowedOrigins) != 1 || generated.AllowedOrigins[0] != "http://localhost:13000" ||
					generated.Query.StageConfigPath != "" || generated.Query.Legacy == nil {
					t.Fatal("local BFF config lost selected ports or legacy query options")
				}
			} else if generated.PortDefault != 8080 || generated.Query.StageConfigPath != "/app/configs/query/dev/query-dev-2026-09-12.1.json" ||
				generated.Query.Legacy != nil {
				t.Fatal("cloud BFF config lost platform defaults or sealed query authority")
			}
			started, err := runtimeconfig.LoadBFFFile(filepath.Join(output, "bff.json"))
			if err != nil || started.GCPProject != generated.GCPProject ||
				started.Bucket != generated.Bucket || started.FirestoreDatabaseID != generated.FirestoreDatabaseID ||
				started.DocumentImportJobURL != tc.documentImportURL ||
				started.PipelineCooldownSeconds != tc.cooldown || started.TypeSafeAPIKey != tc.typesafe ||
				started.ProfileRuntimeAudience != tc.profileAudience || started.ProfileRuntimeServiceAccount != tc.profileService ||
				started.PipelineScheduleCron != generated.PipelineSchedule.Cron ||
				started.PipelineScheduleTimezone != generated.PipelineSchedule.Timezone ||
				started.ScheduledPipelineAudience != generated.PipelineSchedule.Audience ||
				started.ScheduledPipelineServiceAccount != generated.PipelineSchedule.ServiceAccount || started.MCP == nil ||
				started.MCP.Instructions != generated.MCP.Instructions || started.MCP.DiscoveryTTLMS != wantTTL ||
				started.MCP.ToolsListTTLMS != wantTTL {
				t.Fatalf("startup loader did not preserve generated runtime settings for %s: err=%v", tc.environment, err)
			}
			configured := tc.environment != "local"
			hV1 := handlerv1.New(nil, nil, nil, nil, nil, nil)
			hV1.SetScheduledPipelineConfig(started.PipelineScheduleCron, started.PipelineScheduleTimezone,
				started.ScheduledPipelineAudience, started.ScheduledPipelineServiceAccount)
			validatorCalls := 0
			hV1.SetScheduledPipelineIdentityValidator(func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
				validatorCalls++
				if token != "fixture-scheduler-token" || audience != tc.profileAudience {
					t.Errorf("loaded %s schedule identity token/audience = %q/%q", tc.environment, token, audience)
				}
				return &idtoken.Payload{
					Issuer: "https://accounts.google.com", Audience: audience,
					Claims: map[string]interface{}{"email": tc.profileService, "email_verified": true},
				}, nil
			})
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/schedule", nil)
			if configured {
				ctx.Request.Header.Set("Authorization", "Bearer fixture-scheduler-token")
			}
			hV1.ScheduledPipeline(ctx)
			wantRouteError := "scheduled pipeline identity is not configured"
			if configured {
				wantRouteError = "scheduled pipeline scan unavailable"
			}
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), wantRouteError) ||
				(configured && validatorCalls != 1) || (!configured && validatorCalls != 0) {
				t.Fatalf("loaded %s schedule consumer status=%d validatorCalls=%d body=%s", tc.environment, response.Code, validatorCalls, response.Body.String())
			}
			assertGeneratedConfigUsesQualityConsumers(t, started, tc.environment, tc.typesafe)
			if tc.environment == "local" {
				if generated.Local == nil || generated.Local.Scope != "lwc369-synthetic-acceptance" ||
					generated.JWTSecret != "" || generated.DeepSeekAPIKey != "TEST_ONLY_LOCAL_LLM_KEY" ||
					generated.ProfileRuntimeAudience != "" || generated.ProfileRuntimeServiceAccount != "" {
					t.Fatal("local BFF config did not use the isolated synthetic fixtures")
				}
				t.Run("missing optional Jev key remains loadable", func(t *testing.T) {
					t.Setenv("TYPESAFE_API_KEY", "")
					missingOutput := filepath.Join(root, "local-without-jev-output")
					if err := runPrepareTarget(context.Background(), "bff", "local", missingOutput,
						func(context.Context) (secretReader, error) { return &fakeSecretReader{}, nil }); err != nil {
						t.Fatalf("local BFF prepare without optional Jev key failed: %v", err)
					}
					missing, err := runtimeconfig.LoadBFFFile(filepath.Join(missingOutput, "bff.json"))
					if err != nil || missing.TypeSafeAPIKey != "" || missing.ProfileRuntimeAudience != "" {
						t.Fatalf("local BFF loader did not preserve optional Jev fallback config: keyPresent=%t err=%v", missing.TypeSafeAPIKey != "", err)
					}
				})
			} else if generated.Local != nil || generated.JWTSecret != "" || generated.AuthConsumer == nil ||
				generated.DeepSeekAPIKey != "TEST_ONLY_SYNTHETIC_CLOUD_SECRET" ||
				generated.TypeSafeAPIKey != "TEST_ONLY_SYNTHETIC_CLOUD_SECRET" {
				t.Fatalf("cloud BFF fixture binding mismatch local=%t jwt_empty=%t consumer_missing=%t deepseek_match=%t typesafe_match=%t",
					generated.Local != nil, generated.JWTSecret == "", generated.AuthConsumer == nil,
					generated.DeepSeekAPIKey == "TEST_ONLY_SYNTHETIC_CLOUD_SECRET",
					generated.TypeSafeAPIKey == "TEST_ONLY_SYNTHETIC_CLOUD_SECRET")
			}
			fileInfo, err := os.Stat(filepath.Join(output, "bff.json"))
			if err != nil || fileInfo.Mode().Perm() != 0o600 {
				t.Fatalf("BFF file permissions are not 0600: err=%v", err)
			}
			dirInfo, err := os.Stat(output)
			if err != nil || dirInfo.Mode().Perm() != 0o700 {
				t.Fatalf("BFF directory permissions are not 0700: err=%v", err)
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 1 || entries[0].Name() != "bff.json" {
				t.Fatalf("BFF prepare emitted unexpected artifacts: err=%v", err)
			}
		})
	}
}

func TestRealPklBFFLocalPrepareUsesSelectedSharedSecretVersion(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for BFF local binding acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	localRoot := filepath.Join(root, "local")
	if err := os.MkdirAll(localRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	jwtPath := filepath.Join(localRoot, "synthetic-jwt-key")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const resource = "projects/llm-wiki-cloud/secrets/lwc369-synthetic-local-selection/versions/17"
	const payload = "TEST_ONLY_LWC369_SYNTHETIC_SELECTED_PAYLOAD"
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "82800")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", resource)
	t.Setenv("LLM_API_KEY", "TEST_ONLY_ENV_FALLBACK_DECOY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "TEST_ONLY_LOCAL_TYPESAFE_API_KEY")
	t.Setenv("TYPESAFE_JEV_API_KEY", "TEST_ONLY_LEGACY_JEV_DECOY")
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc369-selected-secret-synthetic")
	t.Setenv("LOCAL_CLOUD_WORKER_PATH", filepath.Join(localRoot, "olw_worker"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_CONFIG_PATH", filepath.Join(localRoot, "synthetic-synto.toml"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_BINDINGS_PATH", filepath.Join(localRoot, "synthetic-bindings.json"))
	t.Setenv("LOCAL_CLOUD_JWT_SECRET_FILE", jwtPath)
	t.Setenv("PIPELINE_DEMO_USER_IDS", "")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")

	descriptorDir := filepath.Join(root, "descriptor")
	calledResolver := false
	descriptorLog, err := capturePipelinePrepareStdout(t, func() error {
		return runPrepareTargetMode(context.Background(), "bff", "local", descriptorDir, true,
			func(context.Context) (secretReader, error) {
				calledResolver = true
				return nil, errors.New("descriptor must not resolve secret payloads")
			})
	})
	if err != nil || calledResolver {
		t.Fatalf("BFF descriptor prepare initialized a resolver: err=%v called=%t", err, calledResolver)
	}
	descriptorBytes, err := os.ReadFile(filepath.Join(descriptorDir, "bff-inputs.json"))
	if err != nil {
		t.Fatalf("BFF descriptor was not generated: %v", err)
	}
	var descriptor bffSourceProjection
	if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
		t.Fatalf("BFF descriptor is malformed: %v", err)
	}
	if descriptor.DeepSeekAPIKeyReference.Source != "secret-manager" ||
		descriptor.DeepSeekAPIKeyReference.EnvName != "" ||
		descriptor.DeepSeekAPIKeyReference.Resource != resource {
		t.Fatalf("BFF descriptor did not preserve the exact selected shared binding: %#v", descriptor.DeepSeekAPIKeyReference)
	}
	if descriptor.MCPResourceOrigin != "http://localhost:18080" {
		t.Fatalf("BFF descriptor resource origin = %q, want the prepared canonical listener origin", descriptor.MCPResourceOrigin)
	}
	if descriptor.PipelineSchedule.Cron != "0 * * * *" || descriptor.PipelineSchedule.Timezone != "UTC" ||
		descriptor.PipelineSchedule.Audience != "" || descriptor.PipelineSchedule.ServiceAccount != "" {
		t.Fatalf("BFF descriptor schedule is not the frozen hourly UTC configuration: %+v", descriptor.PipelineSchedule)
	}
	if strings.Contains(string(descriptorBytes), payload) || strings.Contains(descriptorLog, payload) {
		t.Fatal("BFF descriptor or its log contains the synthetic secret payload")
	}
	if descriptor.MCP.Instructions == "" || descriptor.MCP.DiscoveryTTLMS != 0 || descriptor.MCP.ToolsListTTLMS != 0 {
		t.Fatalf("BFF descriptor omitted local CaC MCP metadata: %+v", descriptor.MCP)
	}

	bffReader := &fakeSecretReader{value: []byte(payload)}
	bffOutput := filepath.Join(root, "bff-runtime")
	bffLog, err := capturePipelinePrepareStdout(t, func() error {
		return runPrepareTargetMode(context.Background(), "bff", "local", bffOutput, false,
			func(context.Context) (secretReader, error) { return bffReader, nil })
	})
	if err != nil {
		t.Fatalf("BFF prepare using the selected synthetic GSM resource failed: %v", err)
	}
	if len(bffReader.resources) != 1 || bffReader.resources[0] != resource {
		t.Fatalf("BFF resolver did not receive the exact selected resource: %#v", bffReader.resources)
	}
	bffBytes, err := os.ReadFile(filepath.Join(bffOutput, "bff.json"))
	if err != nil {
		t.Fatalf("generated BFF runtime file is unavailable: %v", err)
	}
	generated, err := runtimeconfig.DecodeBFFFile(bffBytes)
	if err != nil || generated.DeepSeekAPIKey != payload {
		t.Fatalf("generated BFF runtime file did not use the selected synthetic payload: err=%v", err)
	}
	started, err := runtimeconfig.LoadBFFFile(filepath.Join(bffOutput, "bff.json"))
	if err != nil || started.MCP == nil || started.MCP.Instructions != descriptor.MCP.Instructions ||
		started.MCP.ToolDescriptions != descriptor.MCP.ToolDescriptions || started.MCP.DiscoveryTTLMS != descriptor.MCP.DiscoveryTTLMS ||
		started.MCP.ToolsListTTLMS != descriptor.MCP.ToolsListTTLMS {
		t.Fatalf("production composition consumer lost selected MCP metadata: err=%v", err)
	}
	if generated.MCPResourceOrigin != descriptor.MCPResourceOrigin {
		t.Fatalf("generated BFF runtime origin = %q, want %q", generated.MCPResourceOrigin, descriptor.MCPResourceOrigin)
	}
	if generated.PipelineSchedule != (runtimeconfig.BFFPipelineSchedule{
		Cron: "0 * * * *", Timezone: "UTC",
	}) {
		t.Fatalf("generated BFF runtime schedule = %+v, want the selected shared hourly UTC schedule", generated.PipelineSchedule)
	}
	if strings.Contains(bffLog, payload) {
		t.Fatal("BFF prepare log contains the synthetic secret payload")
	}

	pipelineReader := &fakeSecretReader{value: []byte(payload)}
	pipelineOutput := filepath.Join(root, "pipeline-runtime")
	pipelineLog, err := capturePipelinePrepareStdout(t, func() error {
		return runPrepareTarget(context.Background(), "pipeline", "local", pipelineOutput,
			func(context.Context) (secretReader, error) { return pipelineReader, nil })
	})
	if err != nil {
		t.Fatalf("Pipeline prepare using the selected synthetic GSM resource failed: %v", err)
	}
	if len(pipelineReader.resources) != 1 || pipelineReader.resources[0] != resource {
		t.Fatalf("Pipeline resolver did not receive the exact selected resource: %#v", pipelineReader.resources)
	}
	pipelineBytes, err := os.ReadFile(filepath.Join(pipelineOutput, "pipeline.json"))
	if err != nil || strings.Contains(string(pipelineBytes), payload) {
		t.Fatalf("Pipeline public config is unavailable or contains the synthetic payload: err=%v", err)
	}
	var pipeline struct {
		Secret secretBinding `json:"secret"`
	}
	if err := json.Unmarshal(pipelineBytes, &pipeline); err != nil || pipeline.Secret.Resource != descriptor.DeepSeekAPIKeyReference.Resource {
		t.Fatalf("Pipeline and BFF did not retain the same selected secret resource: Pipeline=%q err=%v", pipeline.Secret.Resource, err)
	}
	syntoBytes, err := os.ReadFile(filepath.Join(pipelineOutput, "synto.toml"))
	if err != nil || strings.Contains(string(syntoBytes), payload) || strings.Contains(pipelineLog, payload) {
		t.Fatal("Pipeline public artifacts or log contain the synthetic secret payload")
	}

	failingReader := &fakeSecretReader{err: errors.New(payload)}
	failingOutput := filepath.Join(root, "failed-bff")
	failureLog, failureErr := capturePipelinePrepareStdout(t, func() error {
		return runPrepareTargetMode(context.Background(), "bff", "local", failingOutput, false,
			func(context.Context) (secretReader, error) { return failingReader, nil })
	})
	if failureErr == nil || strings.Contains(failureErr.Error(), payload) || strings.Contains(failureLog, payload) {
		t.Fatalf("BFF preparation error or log leaked secret payload: err=%v", failureErr)
	}
	if _, err := os.Stat(filepath.Join(failingOutput, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed selected-secret prepare left a launchable BFF config: %v", err)
	}
}

func TestRealPklMCPProtectionReachesLoadedSDKConsumer(t *testing.T) {
	const expectedMentions = 51
	emulatorHost := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulatorHost == "" {
		t.Skip("requires a loopback Firestore emulator")
	}
	parsed, err := netip.ParseAddrPort(strings.TrimPrefix(emulatorHost, "http://"))
	if err != nil || !parsed.Addr().IsLoopback() {
		t.Fatalf("Firestore emulator must use loopback, got %q", emulatorHost)
	}
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for real MCP CaC acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}

	root := t.TempDir()
	cacDir := filepath.Join(root, "deploy", "cac")
	if err := os.MkdirAll(cacDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "cac", "ssot.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	const selectedQueryPath = "apps/bff/configs/query/dev/query-dev-2026-09-12.1.json"
	querySource, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(selectedQueryPath)))
	if err != nil {
		t.Fatal(err)
	}
	queryTarget := filepath.Join(root, filepath.FromSlash(selectedQueryPath))
	if err := os.MkdirAll(filepath.Dir(queryTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queryTarget, querySource, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"synto.pkl"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "cac", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cacDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mutated := strings.ReplaceAll(string(original), "list_default = 20", "list_default = 2")
	mutated = strings.ReplaceAll(mutated, "list_max = 50", "list_max = 4")
	mutated = strings.ReplaceAll(mutated, "chunk_max_bytes = 16384", "chunk_max_bytes = 1024")
	mutated = strings.ReplaceAll(mutated, "reference_page_max = 50", "reference_page_max = 3")
	mutated = strings.Replace(mutated, `policy_revision = "mcp-protection-v1"`, `policy_revision = "mcp-cac-lowered-v1"`, 1)
	mutated = strings.Replace(mutated, "project = new BffMcpRateBucket { capacity = 24; refill_microunits_per_second = 2000000; max_inflight = 8 }", "project = new BffMcpRateBucket { capacity = 24; refill_microunits_per_second = 2000000; max_inflight = 2 }", 1)
	if mutated == string(original) || !strings.Contains(mutated, "reference_page_max = 3") ||
		!strings.Contains(mutated, "project = new BffMcpRateBucket { capacity = 24; refill_microunits_per_second = 2000000; max_inflight = 2 }") {
		t.Fatal("synthetic policy lowering did not change only the intended project admission policy and content limits")
	}
	ssotPath := filepath.Join(cacDir, "ssot.pkl")
	if err := os.WriteFile(ssotPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/dev/null")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "82800")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "projects/llm-wiki-cloud/secrets/lwc369-mcp-cac-fixture/versions/1")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_LOCAL_LLM_KEY")
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc369-mcp-cac-synthetic")
	t.Setenv("LOCAL_CLOUD_WORKER_PATH", filepath.Join(root, "olw_worker"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_CONFIG_PATH", filepath.Join(root, "synthetic-synto.toml"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_BINDINGS_PATH", filepath.Join(root, "synthetic-bindings.json"))
	jwtPath := filepath.Join(root, "jwt-key")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_CLOUD_JWT_SECRET_FILE", jwtPath)
	t.Setenv("PIPELINE_DEMO_USER_IDS", "")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	fs, err := internalfirestore.NewClientWithDatabaseAndScope("lwc-mcp-cac-"+suffix, "", "", "", "mcp-cac-"+suffix)
	if err != nil {
		t.Fatalf("create scoped emulator Firestore client: %v", err)
	}
	defer fs.Close()
	userID, projectID := "mcp-cac-owner-"+suffix, "mcp-cac-project-"+suffix
	if _, err := internalfirestore.Collection(fs.Raw(), "users").Doc(userID).Set(ctx,
		auth.UserRecord{Email: userID + "@example.test", Status: auth.AccountActive, AuthVersion: 0}); err != nil {
		t.Fatalf("seed emulator account: %v", err)
	}
	if _, err := internalfirestore.Collection(fs.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx,
		map[string]any{"user_id": userID, "project_id": projectID, "name": "MCP CaC fixture", "status": "ready"}); err != nil {
		t.Fatalf("seed emulator Project: %v", err)
	}
	_, projectKey, _, err := auth.NewProjectKeyService(fs.Raw()).CreateWithCapabilities(ctx, userID, projectID,
		"CaC dynamic policy fixture", []string{"query", "content:read"})
	if err != nil || projectKey == "" {
		t.Fatalf("create content-read project key: token_present=%t err=%v", projectKey != "", err)
	}
	contentClient, contentServer := newMcpCaCContentFixture(t, "lwc369-mcp-cac-synthetic", userID, projectID)
	defer contentServer.Close()

	assertSDKPolicy := func(policyName string, wantDefault, wantListMax, wantChunk, wantRefs, wantProjectInflight, wantAccountInflight, wantFleetInflight, wantLocalInflight, concurrentReads int) {
		t.Helper()
		descriptorDir := filepath.Join(root, policyName+"-descriptor")
		if err := runPrepareTargetMode(ctx, "bff", "dev", descriptorDir, true,
			func(context.Context) (secretReader, error) {
				return nil, errors.New("descriptor cannot resolve secrets")
			}); err != nil {
			t.Fatalf("prepare %s BFF descriptor: %v", policyName, err)
		}
		descriptorBytes, err := os.ReadFile(filepath.Join(descriptorDir, "bff-inputs.json"))
		if err != nil {
			t.Fatalf("read %s descriptor: %v", policyName, err)
		}
		var descriptor bffSourceProjection
		if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
			t.Fatalf("decode %s descriptor: %v", policyName, err)
		}
		if descriptor.MCP.Protection == nil || descriptor.MCP.Protection.ListDefault != wantDefault ||
			descriptor.MCP.Protection.ListMax != wantListMax || descriptor.MCP.Protection.ChunkMaxBytes != wantChunk ||
			descriptor.MCP.Protection.ReferencePageMax != wantRefs || descriptor.MCP.Protection.Content.Project.MaxInflight != wantProjectInflight ||
			descriptor.MCP.Protection.Content.Account.MaxInflight != wantAccountInflight || descriptor.MCP.Protection.Content.FleetMaxInflight != wantFleetInflight ||
			descriptor.MCP.Protection.Content.LocalMaxInflight != wantLocalInflight {
			t.Fatalf("%s descriptor lost the CaC policy: %+v", policyName, descriptor.MCP.Protection)
		}
		normalize := exec.CommandContext(ctx, "go", "run", "./cmd/deploy_config", "--environment", "development", "--config", filepath.Join(repoRoot, "deploy/environments/development.yaml"), "--components", "bff", "--bff-inputs", filepath.Join(descriptorDir, "bff-inputs.json"))
		normalize.Dir = filepath.Join(repoRoot, "apps/bff")
		normalizedBytes, normalizeErr := normalize.CombinedOutput()
		if normalizeErr != nil {
			t.Fatalf("deploy_config normalize %s producer descriptor: %v output=%s", policyName, normalizeErr, normalizedBytes)
		}
		var normalizedPlan struct {
			BFF struct {
				RuntimeInputs json.RawMessage `json:"runtime_inputs"`
			} `json:"bff"`
		}
		if err := json.Unmarshal(normalizedBytes, &normalizedPlan); err != nil || len(normalizedPlan.BFF.RuntimeInputs) == 0 {
			t.Fatalf("decode deploy_config normalized %s plan: runtime_inputs=%s err=%v", policyName, normalizedPlan.BFF.RuntimeInputs, err)
		}
		var normalizedSource bffSourceProjection
		if err := json.Unmarshal(normalizedPlan.BFF.RuntimeInputs, &normalizedSource); err != nil || normalizedSource.MCP.Protection == nil ||
			normalizedSource.MCP.Protection.Content.Project.MaxInflight != wantProjectInflight ||
			normalizedSource.MCP.Protection.Content.Account.MaxInflight != wantAccountInflight ||
			normalizedSource.MCP.Protection.Content.FleetMaxInflight != wantFleetInflight ||
			normalizedSource.MCP.Protection.Content.LocalMaxInflight != wantLocalInflight {
			t.Fatalf("deploy_config normalization lost %s MCP admission policy: protection=%+v err=%v", policyName, normalizedSource.MCP.Protection, err)
		}
		t.Logf("cac_producer_normalized policy=%s revision=%s project_inflight=%d account_inflight=%d fleet_inflight=%d local_inflight=%d", policyName, normalizedSource.MCP.Protection.PolicyRevision, normalizedSource.MCP.Protection.Content.Project.MaxInflight, normalizedSource.MCP.Protection.Content.Account.MaxInflight, normalizedSource.MCP.Protection.Content.FleetMaxInflight, normalizedSource.MCP.Protection.Content.LocalMaxInflight)
		reader := &fakeSecretReader{value: []byte("TEST_ONLY_MCP_CAC_SECRET")}
		runtimeDir := filepath.Join(root, policyName+"-runtime")
		if err := runPrepareTargetMode(ctx, "bff", "dev", runtimeDir, false,
			func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
			t.Fatalf("materialize %s BFF config: %v", policyName, err)
		}
		loaded, err := runtimeconfig.LoadBFFFile(filepath.Join(runtimeDir, "bff.json"))
		if err != nil || loaded.MCP == nil || loaded.MCP.Protection == nil {
			t.Fatalf("load %s BFF runtime config: protection_present=%t err=%v", policyName,
				loaded.MCP != nil && loaded.MCP.Protection != nil, err)
		}
		if got := loaded.MCP.Protection; got.ListDefault != wantDefault || got.ListMax != wantListMax ||
			got.ChunkMaxBytes != wantChunk || got.ReferencePageMax != wantRefs || got.Content.LocalMaxInflight != wantLocalInflight ||
			got.Content.Project.MaxInflight != wantProjectInflight || got.Content.Account.MaxInflight != wantAccountInflight || got.Content.FleetMaxInflight != wantFleetInflight {
			t.Fatalf("%s runtime loader lost the CaC policy: %+v", policyName, got)
		}
		if normalizedSource.MCP.Protection.PolicyRevision != loaded.MCP.Protection.PolicyRevision ||
			normalizedSource.MCP.Protection.Content.LocalMaxInflight != loaded.MCP.Protection.Content.LocalMaxInflight {
			t.Fatalf("%s normalizer and runtime loader disagree: normalized=%+v loaded=%+v", policyName, normalizedSource.MCP.Protection, loaded.MCP.Protection)
		}
		t.Logf("cac_materialized_loaded policy=%s revision=%s", policyName, loaded.MCP.Protection.PolicyRevision)

		barrier := &mcpCaCAdmissionBarrierRoot{entered: make(chan struct{}, 3), release: make(chan struct{}), content: contentClient}
		var releaseOnce sync.Once
		releaseBarrier := func() { releaseOnce.Do(func() { close(barrier.release) }) }
		t.Cleanup(releaseBarrier)
		handler := handlerv1.New(barrier, fs, nil, nil, nil, nil)
		handler.SetMCPConfig(loaded.MCP)
		handler.SetMCPEnvironment("local")
		router := gin.New()
		router.Any("/mcp", gin.WrapH(handler.MCPHandler()))
		server := httptest.NewServer(router)
		defer server.Close()
		httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			cloned := request.Clone(request.Context())
			cloned.Header = request.Header.Clone()
			cloned.Header.Set("Authorization", "Bearer "+projectKey)
			cloned.Header.Set("X-Project-ID", projectID)
			return http.DefaultTransport.RoundTrip(cloned)
		})}
		client := mcp.NewClient(&mcp.Implementation{Name: "mcp-cac-policy-consumer", Version: "local"}, nil)
		connectCtx, cancelConnect := context.WithTimeout(ctx, 5*time.Second)
		session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
		cancelConnect()
		if err != nil {
			t.Fatalf("connect actual SDK client to %s BFF: %v", policyName, err)
		}
		defer session.Close()
		t.Logf("cac_sdk_connected policy=%s", policyName)
		listCtx, cancelList := context.WithTimeout(ctx, 5*time.Second)
		listed, err := session.ListTools(listCtx, nil)
		cancelList()
		if err != nil {
			t.Fatalf("actual SDK tools/list for %s: %v", policyName, err)
		}
		t.Logf("cac_sdk_tools_list policy=%s tools=%d", policyName, len(listed.Tools))
		byName := make(map[string]*mcp.Tool, len(listed.Tools))
		for _, tool := range listed.Tools {
			byName[tool.Name] = tool
		}
		for _, name := range []string{"list_content", "find_content", "read_content", "read_source_raw"} {
			if _, ok := byName[name]; !ok {
				t.Fatalf("%s actual SDK consumer omitted %s", policyName, name)
			}
		}
		assertSchemaNumber := func(toolName, property, key string, want int) {
			t.Helper()
			tool := byName[toolName]
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok {
				t.Fatalf("%s %s schema type is %T", policyName, toolName, tool.InputSchema)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s %s schema has no properties", policyName, toolName)
			}
			shape, ok := properties[property].(map[string]any)
			if !ok {
				t.Fatalf("%s %s schema has no %s property", policyName, toolName, property)
			}
			got, ok := shape[key].(float64)
			if !ok || int(got) != want {
				t.Fatalf("%s %s.%s.%s = %v, want %d", policyName, toolName, property, key, shape[key], want)
			}
		}
		assertSchemaNumber("list_content", "limit", "default", wantDefault)
		assertSchemaNumber("list_content", "limit", "maximum", wantListMax)
		assertSchemaNumber("find_content", "limit", "maximum", wantListMax)
		assertSchemaNumber("read_content", "max_bytes", "default", wantChunk)
		assertSchemaNumber("read_content", "max_bytes", "maximum", wantChunk)
		assertSchemaNumber("read_source_raw", "max_bytes", "maximum", wantChunk)
		listPage, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}})
		if err != nil || listPage == nil || listPage.IsError {
			t.Fatalf("%s readable SDK list_content failed: result=%+v err=%v", policyName, listPage, err)
		}
		listPayload, _ := listPage.StructuredContent.(map[string]any)
		listItems, _ := listPayload["items"].([]any)
		listCursor, _ := listPayload["next_cursor"].(string)
		if len(listItems) != wantDefault || listCursor == "" {
			t.Fatalf("%s SDK list page items=%d cursor=%q payload=%#v", policyName, len(listItems), listCursor, listPayload)
		}
		secondListPage, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept", "cursor": listCursor}})
		if err != nil || secondListPage == nil || secondListPage.IsError {
			t.Fatalf("%s readable SDK list_content cursor failed: result=%+v err=%v", policyName, secondListPage, err)
		}
		secondListPayload, _ := secondListPage.StructuredContent.(map[string]any)
		secondListItems, _ := secondListPayload["items"].([]any)
		secondListCursor, _ := secondListPayload["next_cursor"].(string)
		if len(secondListItems) != wantDefault || secondListPayload["generation"] != "g_mcp_cac_sdk_fixture" || secondListCursor == "" {
			t.Fatalf("%s SDK second list page items=%d payload=%#v", policyName, len(secondListItems), secondListPayload)
		}
		seenListIDs := map[string]struct{}{}
		for _, pageItems := range [][]any{listItems, secondListItems} {
			for _, item := range pageItems {
				ref, _ := item.(map[string]any)
				id, _ := ref["id"].(string)
				if id == "" {
					t.Fatalf("%s CaC list page has an empty item ID: %#v", policyName, item)
				}
				if _, exists := seenListIDs[id]; exists {
					t.Fatalf("%s CaC list pages duplicated ID %q", policyName, id)
				}
				seenListIDs[id] = struct{}{}
			}
		}
		readPage, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "alpha-id", "max_bytes": wantChunk}})
		if err != nil || readPage == nil || readPage.IsError {
			t.Fatalf("%s readable SDK read_content failed: result=%+v err=%v", policyName, readPage, err)
		}
		readPayload, _ := readPage.StructuredContent.(map[string]any)
		readBody, _ := readPayload["content"].(map[string]any)
		readText, _ := readBody["text"].(string)
		readCursor, _ := readPayload["next_refs_cursor"].(string)
		mentions, _ := readPayload["mentions"].([]any)
		contentCursor, _ := readBody["next_cursor"].(string)
		if len(readText) != wantChunk || contentCursor == "" || len(mentions) != wantRefs || readCursor == "" {
			t.Fatalf("%s SDK read chunk/refs page text=%d content_cursor=%q mentions=%d refs_cursor=%q payload=%#v", policyName, len(readText), contentCursor, len(mentions), readCursor, readPayload)
		}
		seenReferences := make(map[string]struct{}, expectedMentions)
		consumeReferences := func(items []any) {
			t.Helper()
			for _, item := range items {
				ref, _ := item.(map[string]any)
				referenceID, _ := ref["reference_id"].(string)
				if referenceID == "" {
					t.Fatalf("%s CaC reference page has an empty reference ID: %#v", policyName, item)
				}
				if _, exists := seenReferences[referenceID]; exists {
					t.Fatalf("%s CaC reference pages duplicated reference %q", policyName, referenceID)
				}
				seenReferences[referenceID] = struct{}{}
			}
		}
		consumeReferences(mentions)
		refPages := 1
		for readCursor != "" {
			nextRefsPage, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "alpha-id", "max_bytes": wantChunk, "refs_cursor": readCursor}})
			if err != nil || nextRefsPage == nil || nextRefsPage.IsError {
				t.Fatalf("%s readable SDK read_content refs cursor failed: result=%+v err=%v", policyName, nextRefsPage, err)
			}
			nextRefsPayload, _ := nextRefsPage.StructuredContent.(map[string]any)
			nextMentions, _ := nextRefsPayload["mentions"].([]any)
			if len(nextMentions) == 0 || len(nextMentions) > wantRefs {
				t.Fatalf("%s SDK reference cursor page size=%d, policy max=%d payload=%#v", policyName, len(nextMentions), wantRefs, nextRefsPayload)
			}
			consumeReferences(nextMentions)
			readCursor, _ = nextRefsPayload["next_refs_cursor"].(string)
			refPages++
			if refPages > expectedMentions {
				t.Fatalf("%s SDK reference pagination failed to advance: pages=%d", policyName, refPages)
			}
		}
		if len(seenReferences) != expectedMentions {
			t.Fatalf("%s SDK reference pagination returned %d distinct mentions across %d pages, want %d", policyName, len(seenReferences), refPages, expectedMentions)
		}
		t.Logf("cac_sdk_reference_pages policy=%s mentions=%d pages=%d restored_limit=%d", policyName, len(seenReferences), refPages, wantRefs)
		barrier.active.Store(true)

		type readOutcome struct {
			result *mcp.CallToolResult
			err    error
		}
		readDone := make(chan readOutcome, 3)
		for i := 0; i < concurrentReads; i++ {
			go func() {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "alpha-id"}})
				readDone <- readOutcome{result: result, err: err}
			}()
		}
		for i := 0; i < concurrentReads; i++ {
			select {
			case <-barrier.entered:
			case <-time.After(10 * time.Second):
				releaseBarrier()
				t.Fatalf("%s CaC-configured SDK reads reached storage barrier=%d, want %d before 10s timeout", policyName, i, concurrentReads)
			}
		}
		t.Logf("cac_sdk_reads_reached_storage policy=%s count=%d", policyName, concurrentReads)
		if concurrentReads == 2 {
			thirdDone := make(chan readOutcome, 1)
			go func() {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "alpha-id"}})
				thirdDone <- readOutcome{result: result, err: err}
			}()
			select {
			case third := <-thirdDone:
				if third.err != nil || third.result == nil || !third.result.IsError {
					releaseBarrier()
					t.Fatalf("lowered CaC policy third SDK read was not rejected: result=%+v err=%v", third.result, third.err)
				}
				payload, _ := third.result.StructuredContent.(map[string]any)
				retryAfter, _ := payload["retry_after_ms"].(float64)
				if payload["code"] != "concurrency_limited" || retryAfter < 250 {
					releaseBarrier()
					t.Fatalf("lowered CaC policy third SDK read response=%#v", third.result.StructuredContent)
				}
			case <-time.After(3 * time.Second):
				releaseBarrier()
				t.Fatal("lowered CaC policy third SDK read did not settle while first two were blocked")
			}
		}
		releaseBarrier()
		for i := 0; i < concurrentReads; i++ {
			select {
			case outcome := <-readDone:
				if outcome.err != nil || outcome.result == nil || outcome.result.IsError {
					t.Fatalf("%s admitted SDK read did not return readable content after release: result=%+v err=%v", policyName, outcome.result, outcome.err)
				}
				payload, _ := outcome.result.StructuredContent.(map[string]any)
				if payload["generation"] != "g_mcp_cac_sdk_fixture" {
					t.Fatalf("%s admitted SDK read returned wrong generation payload=%#v", policyName, payload)
				}
			case <-ctx.Done():
				t.Fatalf("%s admitted SDK reads did not settle after releasing the storage barrier: %v", policyName, ctx.Err())
			}
		}
		t.Logf("cac_sdk_admission policy=%s admitted_reads=%d third_blocked=%t restored=%t", policyName, concurrentReads, concurrentReads == 2, concurrentReads > 2)
	}

	assertSDKPolicy("lowered", 2, 4, 1024, 3, 2, 16, 64, 32, 2)
	if err := os.WriteFile(ssotPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	assertSDKPolicy("restored", 20, 50, 16_384, 50, 8, 16, 64, 32, 3)
}

func capturePipelinePrepareStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := run()
	_ = writer.Close()
	os.Stdout = original
	defer reader.Close()
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		return "", readErr
	}
	return string(output), runErr
}

func (r *fakeSecretReader) ProjectIdentity(_ context.Context, project string) (projectIdentity, error) {
	r.identityCalls++
	if r.identityErr != nil {
		return projectIdentity{}, r.identityErr
	}
	if !projectIdentityMatchesRequest(project, r.identity) {
		return projectIdentity{}, errProjectIdentityMismatch
	}
	return r.identity, nil
}

func TestEnvironmentBindingDoesNotInitializeSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_KEY", "TEST_ONLY_SECRET_PAYLOAD")
	called := false
	value, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, fmt.Errorf("unexpected Secret Manager initialization")
	})
	if err != nil || called || string(value) != "TEST_ONLY_SECRET_PAYLOAD" {
		t.Fatalf("resolveBinding() = (%q, %v), Secret Manager initialized=%t", value, err, called)
	}
}

func TestMissingLocalEnvironmentSecretFailsWithoutSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_MISSING_KEY", "")
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_MISSING_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, errors.New("unexpected Secret Manager initialization")
	})
	if err == nil || !strings.Contains(err.Error(), "required local Pipeline secret is missing") || called {
		t.Fatalf("missing local key error=%v Secret Manager initialized=%t", err, called)
	}
}

func TestSecretManagerBindingUsesOnlySelectedVersion(t *testing.T) {
	reader := &fakeSecretReader{value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const resource = "projects/test-project/secrets/test-secret/versions/7"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != resource || resolved != resource {
		t.Fatalf("selected Secret Manager version was not resolved: value=%q resource=%q err=%v", value, reader.resource, err)
	}
	clear(value)
	for _, b := range value {
		if b != 0 {
			t.Fatal("resolved synthetic secret buffer was not cleared")
		}
	}
}

func TestLatestSecretManagerBindingIsPinnedToResolvedNumericVersion(t *testing.T) {
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/19", value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const latest = "projects/test-project/secrets/test-secret/versions/latest"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: latest,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || resolved != reader.resolved || string(value) != "TEST_ONLY_GSM_PAYLOAD" {
		t.Fatalf("latest Secret Manager version was not pinned: resolved=%q err=%v", resolved, err)
	}
	clear(value)
}

func TestSecretManagerBindingAcceptsAuthoritativeNumericProjectAlias(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/latest"
	const numericResponse = "projects/580854833715/secrets/test-secret/versions/19"
	reader := &fakeSecretReader{
		resolved: numericResponse, value: []byte("TEST_ONLY_GSM_PAYLOAD"),
		identity: projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"},
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != requested ||
		reader.identityCalls != 1 || resolved != "projects/test-project/secrets/test-secret/versions/19" {
		t.Fatalf("numeric project alias was not authoritatively resolved: resource=%q version=%q identity_calls=%d err=%v",
			reader.resource, resolved, reader.identityCalls, err)
	}
	clear(value)
}

func TestSecretManagerNumericProjectAliasRejectsUnboundResponses(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	identity := projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"}
	for _, tc := range []struct {
		name           string
		resolved       string
		identity       projectIdentity
		identityErr    error
		emptyPayload   bool
		wantIdentity   int
		wantDiagnostic string
	}{
		{name: "wrong numeric project", resolved: "projects/999999999999/secrets/test-secret/versions/11", identity: identity, wantIdentity: 1},
		{name: "wrong secret", resolved: "projects/580854833715/secrets/other-secret/versions/11", identity: identity},
		{name: "wrong version", resolved: "projects/580854833715/secrets/test-secret/versions/12", identity: identity},
		{name: "malformed response", resolved: "projects/580854833715/secrets/test-secret/versions/latest", identity: identity},
		{name: "metadata identity mismatch", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: projectIdentity{ProjectID: "other-project", ProjectNumber: "580854833715"}, wantIdentity: 1,
			wantDiagnostic: "does not identify the selected project"},
		{name: "metadata unavailable", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, identityErr: errors.New("TEST_ONLY_METADATA_FAILURE"), wantIdentity: 1,
			wantDiagnostic: "project identity lookup failed"},
		{name: "empty payload precedes metadata lookup", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, emptyPayload: true, wantDiagnostic: "payload is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("TEST_ONLY_GSM_PAYLOAD")
			if tc.emptyPayload {
				payload = nil
			}
			reader := &fakeSecretReader{
				resolved: tc.resolved, value: payload,
				identity: tc.identity, identityErr: tc.identityErr,
			}
			_, _, err := resolveBinding(context.Background(), secretBinding{
				Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
			}, func(context.Context) (secretReader, error) { return reader, nil })
			if err == nil || strings.Contains(err.Error(), "TEST_ONLY_GSM_PAYLOAD") ||
				strings.Contains(err.Error(), "TEST_ONLY_METADATA_FAILURE") || reader.identityCalls != tc.wantIdentity {
				t.Fatalf("unbound response was accepted or leaked data: identity_calls=%d err=%v", reader.identityCalls, err)
			}
			if tc.wantDiagnostic != "" && !strings.Contains(err.Error(), tc.wantDiagnostic) {
				t.Fatalf("metadata failure lost its diagnostic class: %v", err)
			}
			for _, b := range reader.value {
				if b != 0 {
					t.Fatal("rejected synthetic secret buffer was not cleared")
				}
			}
		})
	}
}

func TestPreparedSecretReferenceUpdatesOnlyPublicAndPrivateBindings(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "pipeline.json")
	if err := os.WriteFile(path, []byte(`{"environment":"dev","secret":{"source":"secret-manager","target":"DEEPSEEK_API_KEY","envName":"","resource":"projects/test-project/secrets/test-secret/versions/latest"},"synto":{"ok":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const resolved = "projects/test-project/secrets/test-secret/versions/19"
	if err := bindResolvedSecretReference(temp, secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resolved}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	var binding secretBinding
	if err := json.Unmarshal(output["secret"], &binding); err != nil || binding.Resource != resolved {
		t.Fatalf("public prepared binding = %+v, err=%v", binding, err)
	}
	var synto map[string]bool
	if err := json.Unmarshal(output["synto"], &synto); err != nil || !synto["ok"] {
		t.Fatalf("unrelated public config changed: %s err=%v", output["synto"], err)
	}
}

func TestSecretManagerFailureIsExplicitAndDoesNotExposePayload(t *testing.T) {
	reader := &fakeSecretReader{err: fmt.Errorf("TEST_ONLY_SECRET_PAYLOAD denied")}
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY",
		Resource: "projects/test-project/secrets/test-secret/versions/3",
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "access failed") || strings.Contains(err.Error(), "TEST_ONLY_SECRET_PAYLOAD") {
		t.Fatalf("secret access failure was not explicit and redacted: %v", err)
	}
}

func TestInvalidSecretVersionFailsBeforeClientInitialization(t *testing.T) {
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Resource: "projects/test/secrets/test/versions/0",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "full secret version resource path") {
		t.Fatalf("invalid secret version was accepted: err=%v initialized=%t", err, called)
	}
}

func TestRunTimeoutMustBeExplicit(t *testing.T) {
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	if _, err := runTimeoutFromEnvironment(); err == nil || !strings.Contains(err.Error(), "verified Pipeline Job timeout") {
		t.Fatalf("missing timeout was not rejected clearly: %v", err)
	}
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "0")
	if _, err := runTimeoutFromEnvironment(); err == nil {
		t.Fatal("zero timeout was accepted")
	}
}

func TestDocumentImportJobURLIsEnvironmentScoped(t *testing.T) {
	dev := "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/document-import-job-dev:run"
	prod := "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/document-import-job:run"
	if !validateDocumentImportJobURL(dev, "dev") || !validateDocumentImportJobURL(prod, "prod") {
		t.Fatal("canonical DEV or Production document import target was rejected")
	}
	if validateDocumentImportJobURL(dev, "prod") || validateDocumentImportJobURL(prod, "dev") {
		t.Fatal("document import URL crossed its environment boundary")
	}
}

func TestRealPklDevBFFMaterializeUsesInjectedLoopbackSDKWithoutADC(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for real DEV BFF prepare acceptance: %v", err)
		}
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_LOCAL_LLM_KEY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TYPESAFE_JEV_API_KEY", "TEST_ONLY_LEGACY_JEV_DECOY")
	t.Setenv("TYPESAFE_API_KEY", "TEST_ONLY_LOCAL_TYPESAFE_API_KEY")
	t.Setenv("GCP_PROJECT", "llm-wiki-cloud")
	t.Setenv("BUCKET", "llm-wiki-cloud-local")
	t.Setenv("FIRESTORE_DATABASE_ID", "llm-wiki-cloud-local")
	t.Setenv("ALLOWED_HOSTS", "localhost,127.0.0.1")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:13000")
	t.Setenv("AUTH_SERVICE_URL", "http://localhost:18081")
	t.Setenv("DEV_JWT", "false")
	t.Setenv("LOCAL_DATA_DIR", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("PIPELINE_COOLDOWN_SECONDS", "")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")
	// An accidental default ADC lookup must fail locally; the materializer receives
	// its only reader through the existing injected SDK boundary below.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/dev/null")
	t.Setenv("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "/dev/null")
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")

	var mu sync.Mutex
	accesses := []string{}
	projectLookups := 0
	deniedResource := ""
	server := newOwnedLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && strings.HasSuffix(r.URL.Path, ":access") {
			resource := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":access")
			mu.Lock()
			accesses = append(accesses, resource)
			denied := resource == deniedResource
			mu.Unlock()
			if denied {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
					"code": http.StatusForbidden, "message": "TEST_ONLY_BFF_DENIED_MESSAGE",
					"details": []any{map[string]string{"message": "TEST_ONLY_BFF_RAW_BODY_MARKER"}},
				}})
				return
			}
			resolved := resource
			if strings.HasSuffix(resource, "/latest") {
				resolved = strings.TrimSuffix(resource, "/latest") + "/11"
				resolved = strings.Replace(resolved, "projects/llm-wiki-cloud/",
					"projects/580854833715/", 1)
			}
			value := "TEST_ONLY_SYNTHETIC_BFF_SECRET"
			if strings.Contains(resource, "byok-credential-encryption-key") {
				value = strings.Repeat("a", 64)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":    resolved,
				"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(value))},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v3/projects/") {
			mu.Lock()
			projectLookups++
			mu.Unlock()
			if r.URL.Path != "/v3/projects/llm-wiki-cloud" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"projects/580854833715","projectId":"llm-wiki-cloud","state":"ACTIVE"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	if !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
		t.Fatalf("synthetic SDK endpoint is not numeric loopback: %s", server.URL)
	}
	secretService, err := secretmanager.NewService(context.Background(), option.WithEndpoint(server.URL+"/"),
		option.WithHTTPClient(server.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	projectService, err := cloudresourcemanager.NewService(context.Background(), option.WithEndpoint(server.URL+"/"),
		option.WithHTTPClient(server.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	reader := googleSecretReader{service: secretService, projects: projectService}

	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runPrepareTargetMode(context.Background(), "bff", "dev", runtimeDir, false,
		func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
		t.Fatalf("actual DEV BFF materialization failed through loopback SDK: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runtimeDir, "bff.json"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := runtimeconfig.DecodeBFFFile(data)
	if err != nil {
		t.Fatalf("actual DEV BFF materialization output failed schema validation: %v", err)
	}
	if generated.DeepSeekAPIKey != "TEST_ONLY_SYNTHETIC_BFF_SECRET" ||
		generated.TypeSafeAPIKey != "TEST_ONLY_SYNTHETIC_BFF_SECRET" ||
		generated.BYOKCredentialEncryptionKey != strings.Repeat("a", 64) {
		t.Fatal("actual DEV BFF materializer did not use synthetic SDK payloads")
	}
	info, err := os.Stat(filepath.Join(runtimeDir, "bff.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("actual DEV BFF runtime config mode = %v, err=%v", info, err)
	}
	deniedRuntimeDir := filepath.Join(t.TempDir(), "denied-runtime")
	if err := os.Mkdir(deniedRuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	deniedResource = "projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest"
	mu.Unlock()
	err = runPrepareTargetMode(context.Background(), "bff", "dev", deniedRuntimeDir, false,
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "Google API HTTP 403") ||
		!strings.Contains(err.Error(), "TEST_ONLY_BFF_DENIED_MESSAGE") ||
		strings.Contains(err.Error(), "TEST_ONLY_BFF_RAW_BODY_MARKER") {
		t.Fatalf("actual DEV BFF materializer did not safely classify synthetic SDK denial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deniedRuntimeDir, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed actual BFF materialization wrote a runtime config: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantAccesses := []string{
		"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
		"projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-dev/versions/1",
		"projects/llm-wiki-cloud/secrets/byok-credential-encryption-key-dev/versions/1",
		"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
	}
	if len(accesses) != len(wantAccesses) || projectLookups != 1 {
		t.Fatalf("actual DEV BFF SDK path requested %d secret versions and %d project identities",
			len(accesses), projectLookups)
	}
	for i, resource := range wantAccesses {
		if accesses[i] != resource {
			t.Fatalf("actual DEV BFF SDK request %d did not match the source reference", i)
		}
	}
}

func TestRunPrepareBFFTargetDoesNotReadPipelineInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_DEEPSEEK_KEY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	jwtPath := filepath.Join(root, "jwt-secret")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_TEST_BFF_JSON", localBFFSourceJSON(root, jwtPath, 60))
	readerCalled := false
	output := filepath.Join(root, "out")
	if err := runPrepareTarget(context.Background(), "bff", "local", output,
		func(context.Context) (secretReader, error) {
			readerCalled = true
			return nil, errors.New("BFF target must not resolve a secret")
		}); err != nil {
		t.Fatalf("runPrepareTarget(bff) = %v", err)
	}
	if readerCalled {
		t.Fatal("BFF target initialized a Pipeline secret reader")
	}
	data, err := os.ReadFile(filepath.Join(output, "bff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generated runtimeconfig.BFFFile
	if err := json.Unmarshal(data, &generated); err != nil || generated.SchemaVersion != 2 ||
		generated.Environment != "local" || generated.Target != "bff" || generated.PipelineCooldownSeconds != 60 ||
		generated.JWTSecret != strings.Repeat("a", 64) || generated.DeepSeekAPIKey != "TEST_ONLY_DEEPSEEK_KEY" ||
		generated.LLM.BaseURL != "https://api.deepseek.com" || generated.Local == nil || generated.MCP == nil ||
		generated.MCP.Instructions != "configured shared instructions" || generated.MCP.DiscoveryTTLMS != 0 || generated.MCP.ToolsListTTLMS != 0 {
		t.Fatalf("BFF runtime config identity or values are invalid: schema=%d environment=%s target=%s cooldown=%d local=%t", generated.SchemaVersion, generated.Environment, generated.Target, generated.PipelineCooldownSeconds, generated.Local != nil)
	}
	started, err := runtimeconfig.LoadBFFFile(filepath.Join(output, "bff.json"))
	if err != nil || started.MCP == nil || started.MCP.Instructions != generated.MCP.Instructions ||
		started.MCP.ToolDescriptions != generated.MCP.ToolDescriptions {
		t.Fatalf("production composition consumer lost prepared MCP metadata: err=%v", err)
	}
	info, err := os.Stat(filepath.Join(output, "bff.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("BFF runtime config mode = %v, err=%v", info, err)
	}
	dirInfo, err := os.Stat(output)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("BFF runtime directory mode = %v, err=%v", dirInfo, err)
	}
	for _, name := range []string{"pipeline.json", "synto.toml", "private-bindings.json"} {
		if _, err := os.Stat(filepath.Join(output, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("BFF target emitted Pipeline artifact %s: err=%v", name, err)
		}
	}
}

func TestBFFDescriptorDoesNotResolveSecretsOrLeaveRuntimeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	jwtPath := filepath.Join(root, "jwt-secret")
	t.Setenv("LWC_TEST_BFF_JSON", localBFFSourceJSON(root, jwtPath, 60))
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "bff.json"), []byte("prior sensitive config"), 0o600); err != nil {
		t.Fatal(err)
	}
	readerCalled := false
	if err := runPrepareTargetMode(context.Background(), "bff", "local", output, true, func(context.Context) (secretReader, error) {
		readerCalled = true
		return nil, errors.New("descriptor mode must not resolve secrets")
	}); err != nil {
		t.Fatalf("descriptor prepare failed: %v", err)
	}
	if readerCalled {
		t.Fatal("descriptor mode initialized a Secret Manager reader")
	}
	if _, err := os.Stat(filepath.Join(output, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor mode left a launchable BFF file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "bff-inputs.json"))
	if err != nil || !strings.Contains(string(data), "jwt_secret_reference") || !strings.Contains(string(data), "deepseek_api_key_reference") {
		t.Fatalf("descriptor did not retain the selected reference metadata: err=%v", err)
	}
	var descriptor bffSourceProjection
	if err := json.Unmarshal(data, &descriptor); err != nil || descriptor.SchemaVersion != 2 ||
		descriptor.JWTSecretReference == nil || descriptor.JWTSecretReference.Source != "file" ||
		descriptor.JWTSecretReference.Resource != jwtPath || descriptor.AuthConsumer != nil {
		t.Fatalf("local schema-2 descriptor lost its bounded JWT file reference: err=%v descriptor=%+v", err, descriptor)
	}
}

func TestInvalidBFFAttemptInvalidatesPriorRuntimeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	invalid := localBFFSourceJSON(root, filepath.Join(root, "jwt-secret"), 0)
	t.Setenv("LWC_TEST_BFF_JSON", invalid)
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "bff.json"), []byte("prior config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPrepareTarget(context.Background(), "bff", "local", output, nil); err == nil {
		t.Fatal("invalid generated BFF config was accepted")
	}
	if _, err := os.Stat(filepath.Join(output, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed generation left a launchable prior BFF file: %v", err)
	}
}

func localBFFSourceJSON(root, jwtPath string, cooldown int) string {
	local := map[string]any{
		"scope": "worktree-0123456789abcdef01234567", "worker_path": filepath.Join(root, "olw_worker"),
		"pipeline_config_path": filepath.Join(root, "synto.toml"), "pipeline_bindings_path": filepath.Join(root, "private-bindings.json"),
	}
	value := map[string]any{
		"schema_version": 2, "environment": "local", "target": "bff", "gcp_project": "llm-wiki-cloud",
		"bucket": "llm-wiki-cloud-local", "firestore_database_id": "llm-wiki-cloud-local",
		"auth_service_url": "http://localhost:8081", "mcp_resource_origin": "http://localhost:18080",
		"mcp": map[string]any{
			"instructions":      "configured shared instructions",
			"tool_descriptions": map[string]any{"query_project": "configured query", "add_raw": "configured add", "import_url": "configured import"},
			"discovery_ttl_ms":  0, "tools_list_ttl_ms": 0,
		},
		"pipeline_job_url": "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline:run",
		"export_job_url":   "", "export_signing_service_account": "", "allowed_origins": []string{"http://localhost:3000"},
		"allowed_hosts": []string{"localhost", "127.0.0.1"}, "pipeline_daily_limit": 2, "pipeline_cooldown_seconds": cooldown,
		"pipeline_min_new_raw": 1, "pipeline_demo_user_ids": []string{}, "auth_session_environment": "llm-wiki-cloud-local",
		"auth_session_migration": "disabled", "registration_enabled": nil,
		"jwt_secret_reference":       map[string]any{"source": "file", "resource": jwtPath},
		"deepseek_api_key_reference": map[string]any{"source": "environment", "env_name": "LLM_API_KEY"},
		"profile_runtime_audience":   "", "profile_runtime_service_account": "",
		"pipeline_schedule": map[string]any{"cron": "0 * * * *", "timezone": "UTC", "audience": "", "service_account": ""},
		"llm":               map[string]any{"provider": "deepseek", "base_url": "https://api.deepseek.com/v1/", "request_timeout_seconds": 60, "model": "deepseek-flash"},
		"query": map[string]any{"stage_config_path": "", "legacy": map[string]any{
			"query_expansion_model": "deepseek-flash", "query_expansion_reasoning": "none", "answer_synthesis_model": "deepseek-flash",
			"answer_synthesis_reasoning": "none", "query_selection_limit": 10, "query_selection_exploration_slots": 1,
			"query_selection_evidence_threshold": 2, "query_expansion_keywords_per_attempt": 24, "query_expansion_attempts": 3,
			"query_matching_rare_keyword_max_document_frequency": 1,
		}},
		"local": local, "port_default": 8080, "config_secret_resource": "",
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func TestRunPrepareRendersOnlyThePreparedProjection(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("LWC_TEST_REAL_PKL"))
	if realPKL == "" {
		if found, err := exec.LookPath("pkl"); err == nil {
			realPKL = found
		} else if _, err := os.Stat("/Users/rayer/.local/bin/pkl"); err == nil {
			realPKL = "/Users/rayer/.local/bin/pkl"
		}
	}
	if realPKL == "" {
		t.Skip("Pkl is unavailable")
	}
	cacheDir := strings.TrimSpace(os.Getenv("PKL_CACHE_DIR"))
	if cacheDir == "" {
		if _, err := os.Stat("/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"); err == nil {
			cacheDir = "/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"
		}
	}
	if cacheDir == "" {
		t.Skip("locked Pkl package cache is unavailable")
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Skip("locked Pkl package cache is unavailable")
	}

	sourceDir := filepath.Join("..", "..", "..", "..", "deploy", "cac")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "ssot.pkl" {
			content := string(data)
			for _, replacement := range [][2]string{
				{`provider = "deepseek"`, `provider = "selected-provider"`},
				{`endpoint = "https://api.deepseek.com/v1"`, `endpoint = "https://selected.example.invalid/v1"`},
				{`model = "deepseek-flash"`, `model = "selected-model"`},
				{`requestTimeoutSeconds = 600`, `requestTimeoutSeconds = 437`},
				{`pipelineArticleMaxTokens: Int? = read?("prop:pipelineArticleMaxTokens")?.toInt()`,
					`pipelineArticleMaxTokens: Int? = 24680`},
			} {
				if !strings.Contains(content, replacement[0]) {
					t.Fatalf("SSOT fixture did not contain selected profile override %q", replacement[0])
				}
				content = strings.Replace(content, replacement[0], replacement[1], 1)
			}
			data = []byte(content)
		}
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := filepath.Join(root, "pkl-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nset -eu\nlast=\nfor arg do last=\"$arg\"; done\nif [ \"$last\" = \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\" ]; then\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\n  printf '%s\\n' 'module mutated' > \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\"\nelse\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\nfi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", wrapper)
	t.Setenv("LWC_TEST_REAL_PKL", realPKL)
	t.Setenv("PKL_CACHE_DIR", cacheDir)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "projects/test-project/secrets/test-secret/versions/latest")
	const payload = "TEST_ONLY_LOCAL_GSM_PAYLOAD"
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/7", value: []byte(payload)}
	if err := runPrepare(context.Background(), "local", output,
		func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
		t.Fatalf("runPrepare() with synthetic local GSM input: %v", err)
	}
	mutated, err := os.ReadFile(filepath.Join(root, "deploy", "cac", "ssot.pkl"))
	if err != nil || string(mutated) != "module mutated\n" {
		t.Fatalf("SSOT fixture was not mutated between evaluate and render: %q err=%v", mutated, err)
	}
	tomlBytes, err := os.ReadFile(filepath.Join(output, "synto.toml"))
	if err != nil || !strings.Contains(string(tomlBytes), "article_max_tokens = 24680") ||
		!strings.Contains(string(tomlBytes), "run_timeout_seconds = 23") || strings.Contains(string(tomlBytes), payload) {
		t.Fatalf("render did not use the prepared projection/override: %s err=%v", tomlBytes, err)
	}
	for _, selected := range []string{
		`name = "selected-provider"`, `url = "https://selected.example.invalid/v1"`,
		`timeout = 437`, `model = "selected-model"`, `article_max_tokens = 24680`,
	} {
		if !strings.Contains(string(tomlBytes), selected) {
			t.Fatalf("render did not use selected SSOT profile/override %q: %s", selected, tomlBytes)
		}
	}
	publicBytes, err := os.ReadFile(filepath.Join(output, "pipeline.json"))
	if err != nil || strings.Contains(string(publicBytes), payload) {
		t.Fatalf("public config contains local secret payload: err=%v", err)
	}
	for _, selected := range []string{"selected-provider", "https://selected.example.invalid/v1", "selected-model"} {
		if !strings.Contains(string(publicBytes), selected) {
			t.Fatalf("public Pipeline projection lost selected profile value %q", selected)
		}
	}
	privateInfo, err := os.Stat(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private bindings mode=%v, want 0600", privateInfo.Mode())
	}
	privateBytes, err := os.ReadFile(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var private privateBindings
	if err := json.Unmarshal(privateBytes, &private); err != nil || string(private.LocalAPIKey) != payload {
		t.Fatalf("private local binding payload=%q err=%v", private.LocalAPIKey, err)
	}
	clear(private.LocalAPIKey)
}

func TestRealPklLocalPipelinePrepareKeepsEnvironmentKeyPrivate(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for local Pipeline prepare acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const payload = "TEST_ONLY_LOCAL_ENV_PIPELINE_KEY"
	output := t.TempDir()
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", payload)
	t.Setenv("DEEPSEEK_API_KEY", "")
	if err := runPrepare(context.Background(), "local", output, func(context.Context) (secretReader, error) {
		return nil, errors.New("environment-backed local secret must not initialize Secret Manager")
	}); err != nil {
		t.Fatalf("runPrepare() with environment-backed local Pipeline key: %v", err)
	}
	for _, name := range []string{"pipeline.json", "synto.toml"} {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || strings.Contains(string(data), payload) {
			t.Fatalf("public %s contains the local key: err=%v", name, err)
		}
	}
	privateInfo, err := os.Stat(filepath.Join(output, "private-bindings.json"))
	if err != nil || privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private bindings permissions do not match 0600: err=%v", err)
	}
	privateBytes, err := os.ReadFile(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var private privateBindings
	if err := json.Unmarshal(privateBytes, &private); err != nil || private.Environment != "local" || string(private.LocalAPIKey) != payload {
		t.Fatalf("private local Pipeline binding env=%q key length=%d err=%v", private.Environment, len(private.LocalAPIKey), err)
	}
	clear(private.LocalAPIKey)
}

func TestGoogleSecretManagerSDKAccessPath(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":"%s","payload":{"data":"%s"}}`, resource, base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) {
		return googleSecretReader{service: service}, nil
	})
	if err != nil || string(value) != payload || resolved != resource {
		t.Fatalf("Google Secret Manager client response = %q version=%q, %v", value, resolved, err)
	}
	clear(value)
}

func TestGAT4SyntheticAuthDescriptorProducer(t *testing.T) {
	var requests []string
	server := newOwnedLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions":
			_, _ = w.Write([]byte(`{"versions":[{"name":"projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions/42","state":"ENABLED"}]}`))
		case "/v1/projects/llm-wiki-cloud/secrets/google-oauth-client-dev/versions/1":
			_, _ = w.Write([]byte(`{"name":"projects/llm-wiki-cloud/secrets/google-oauth-client-dev/versions/1","state":"ENABLED"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint(server.URL+"/"),
		option.WithHTTPClient(server.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_CACHE_DIR", "")
	sourceSHA := strings.TrimSpace(os.Getenv("GAT4_TEST_SOURCE_SHA"))
	if sourceSHA == "" {
		sourceSHA = strings.Repeat("c", 40)
	}
	output := t.TempDir()
	if err := runPrepareTargetModeAtSource(context.Background(), "auth", "dev", output, true, sourceSHA,
		func(context.Context) (secretReader, error) {
			return googleSecretReader{service: service}, nil
		}); err != nil {
		t.Fatalf("prepare synthetic Auth descriptor: %v", err)
	}
	descriptorPath := filepath.Join(output, "auth-inputs.json")
	descriptor, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot runtimeconfig.AuthInputSnapshot
	if err := json.Unmarshal(descriptor, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := runtimeconfig.ValidateAuthInputSnapshot(snapshot); err != nil {
		t.Fatalf("produced Auth descriptor is invalid: %v", err)
	}
	if snapshot.SourceSHA != sourceSHA || snapshot.JWTSecretVersion != "projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions/42" ||
		snapshot.Google.ClientSecretVersion != "projects/llm-wiki-cloud/secrets/google-oauth-client-dev/versions/1" {
		t.Fatalf("synthetic metadata was not pinned into the actual Auth descriptor: source=%q jwt=%q google=%q",
			snapshot.SourceSHA, snapshot.JWTSecretVersion, snapshot.Google.ClientSecretVersion)
	}
	if len(requests) != 2 || requests[0] != "GET /v1/projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions" ||
		requests[1] != "GET /v1/projects/llm-wiki-cloud/secrets/google-oauth-client-dev/versions/1" {
		t.Fatalf("unexpected loopback metadata requests: %v", requests)
	}
	if destination := strings.TrimSpace(os.Getenv("GAT4_TEST_AUTH_DESCRIPTOR_OUTPUT")); destination != "" {
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, descriptor, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGoogleSecretManagerSDKResolvesCanonicalNumericProjectName(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	const canonical = "projects/580854833715/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	accessCalls, projectCalls := 0, 0
	secretHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		accessCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+requested+":access" {
			return nil, fmt.Errorf("unexpected Secret Manager request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":%q,"payload":{"data":%q}}`, canonical,
			base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	projectHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		projectCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v3/projects/test-project" {
			return nil, fmt.Errorf("unexpected Cloud Resource Manager request %s %s", request.Method, request.URL.Path)
		}
		body := `{"name":"projects/580854833715","projectId":"test-project","state":"ACTIVE"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	secretService, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"),
		option.WithHTTPClient(secretHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	projectService, err := cloudresourcemanager.NewService(context.Background(), option.WithEndpoint("https://cloudresourcemanager.test/"),
		option.WithHTTPClient(projectHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	reader := googleSecretReader{service: secretService, projects: projectService}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != payload || accessCalls != 1 || projectCalls != 1 ||
		resolved != requested {
		t.Fatalf("SDK canonical project response was not safely pinned: version=%q access_calls=%d project_calls=%d err=%v",
			resolved, accessCalls, projectCalls, err)
	}
	clear(value)
}

func TestRunPreparePreservesBoundedSecretManagerSDKMessage(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const serverBodyMarker = "TEST_ONLY_RAW_SECRET_MANAGER_ERROR_BODY"
	message := "Permission denied for the selected Secret Manager version: " + strings.Repeat("x", 700)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code": 403, "message": message,
			"details": []any{map[string]any{"message": serverBodyMarker}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := secretManagerResponseReader(t, resource, http.StatusForbidden, string(body))
	root := prepareRootForSecretManager(t, resource)
	err = runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil {
		t.Fatal("runPrepare() accepted a Secret Manager access failure")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "Google API HTTP 403") ||
		!strings.Contains(diagnostic, "Permission denied for the selected Secret Manager version") ||
		!strings.Contains(diagnostic, "message_truncated=true") {
		t.Fatalf("runPrepare() lost the bounded SDK diagnostic: %q", diagnostic)
	}
	if strings.Contains(diagnostic, serverBodyMarker) || len(diagnostic) > 900 {
		t.Fatalf("runPrepare() exposed raw SDK body or an unbounded message: %q", diagnostic)
	}
}

func TestRunPrepareDistinguishesEmptySecretManagerPayload(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	body := fmt.Sprintf(`{"name":%q,"payload":{"data":""}}`, resource)
	reader := secretManagerResponseReader(t, resource, http.StatusOK, body)
	root := prepareRootForSecretManager(t, resource)
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "payload is empty") || strings.Contains(err.Error(), "access failed") {
		t.Fatalf("empty payload was not distinguished from access failure: %v", err)
	}
}

func TestRunPrepareDistinguishesSecretManagerClientInitializationFailure(t *testing.T) {
	root := prepareRootForSecretManager(t, "projects/test-project/secrets/test-secret/versions/11")
	const detail = "TEST_ONLY_INITIALIZATION_DETAIL"
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return nil, errors.New(detail) })
	if err == nil || !strings.Contains(err.Error(), "initialize Secret Manager resolver") ||
		!strings.Contains(err.Error(), "error type") || strings.Contains(err.Error(), detail) {
		t.Fatalf("client initialization failure was not safely classified: %v", err)
	}
}

func TestRunMaterializeAuthPreservesBoundedSecretManagerSDKDiagnostics(t *testing.T) {
	inputsPath := authInputsFileForMaterializerTest(t, true)
	inputsBytes, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs runtimeconfig.AuthInputSnapshot
	if err := json.Unmarshal(inputsBytes, &inputs); err != nil {
		t.Fatal(err)
	}

	const rawBodyMarker = "TEST_ONLY_RAW_SECRET_MANAGER_ERROR_BODY_TOKEN"
	longMessage := "Permission denied for the selected Secret Manager version: " + strings.Repeat("x", 700)
	for _, tc := range []struct {
		name       string
		status     int
		message    string
		want       string
		wantAbsent string
		body       string
	}{
		{name: "forbidden", status: http.StatusForbidden, message: longMessage,
			want:       "Google API HTTP 403: Permission denied for the selected Secret Manager version:",
			wantAbsent: rawBodyMarker},
		{name: "missing", status: http.StatusNotFound, message: "selected Secret Manager version does not exist",
			want:       "Google API HTTP 404: selected Secret Manager version does not exist",
			wantAbsent: rawBodyMarker},
		{name: "empty", status: http.StatusOK,
			want: "payload is empty", wantAbsent: "Google API HTTP",
			body: fmt.Sprintf(`{"name":%q,"payload":{"data":""}}`, inputs.JWTSecretVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				response, marshalErr := json.Marshal(map[string]any{
					"error": map[string]any{
						"code": tc.status, "message": tc.message,
						"details": []any{map[string]any{"message": rawBodyMarker}},
					},
				})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				body = string(response)
			}
			reader := secretManagerResponseReader(t, inputs.JWTSecretVersion, tc.status, body)
			output := filepath.Join(t.TempDir(), "auth.json")
			err := runMaterializeAuth(context.Background(), inputsPath, output,
				func(context.Context) (secretReader, error) { return reader, nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runMaterializeAuth() lost the selected SDK diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), tc.wantAbsent) || len(err.Error()) > 900 {
				t.Fatalf("runMaterializeAuth() exposed unbounded or raw SDK data: %q", err.Error())
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed materialization wrote an Auth config: %v", statErr)
			}
			if tc.name == "forbidden" && !strings.Contains(err.Error(), "message_truncated=true") {
				t.Fatalf("long SDK message was not bounded: %q", err.Error())
			}
		})
	}
}

func TestRunMaterializeAuthWritesPrivateFileAfterSuccessfulSDKAccess(t *testing.T) {
	inputsPath := authInputsFileForMaterializerTest(t, false)
	inputsBytes, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs runtimeconfig.AuthInputSnapshot
	if err := json.Unmarshal(inputsBytes, &inputs); err != nil {
		t.Fatal(err)
	}
	const syntheticPayload = "TEST_ONLY_AUTH_CREDENTIAL_PAYLOAD"
	body, err := json.Marshal(map[string]any{
		"name":    inputs.JWTSecretVersion,
		"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(syntheticPayload))},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := secretManagerResponseReader(t, inputs.JWTSecretVersion, http.StatusOK, string(body))
	output := filepath.Join(t.TempDir(), "auth.json")
	if err := runMaterializeAuth(context.Background(), inputsPath, output,
		func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
		t.Fatalf("runMaterializeAuth() rejected successful synthetic SDK access: %v", err)
	}
	fileBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	file, err := runtimeconfig.DecodeAuthFile(fileBytes)
	if err != nil || file.JWTSecret != syntheticPayload {
		t.Fatalf("successful SDK access did not produce the expected private test fixture: err=%v", err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized Auth file is not private: err=%v", err)
	}
}

func authInputsFileForMaterializerTest(t *testing.T, googleEnabled bool) string {
	t.Helper()
	projection := authProjectionForTest("dev")
	if !googleEnabled {
		projection.Google = authSourceGoogle{}
	}
	inputs, err := prepareAuthInputs(context.Background(), projection, strings.Repeat("d", 40),
		func(context.Context) (secretReader, error) { return &fakeAuthReader{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-inputs.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMaterializeAuthHelperProcess(t *testing.T) {
	if os.Getenv("LWC_TEST_AUTH_MATERIALIZER_HELPER") != "run" {
		return
	}
	inputsPath := os.Getenv("LWC_TEST_AUTH_INPUTS")
	output := os.Getenv("LWC_TEST_AUTH_OUTPUT")
	status, err := strconv.Atoi(os.Getenv("LWC_TEST_AUTH_HTTP_STATUS"))
	if err != nil || inputsPath == "" || output == "" {
		t.Fatal("synthetic materializer helper input is invalid")
	}
	inputBytes, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := runtimeconfig.DecodeAuthInputSnapshot(inputBytes)
	if err != nil {
		t.Fatal(err)
	}
	reader := secretManagerResponseReader(t, inputs.JWTSecretVersion, status,
		os.Getenv("LWC_TEST_AUTH_HTTP_BODY"))
	err = runMaterializeAuth(context.Background(), inputsPath, output,
		func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil {
		fmt.Printf("LWC_TEST_MATERIALIZE_ERROR:%s\n", err)
		return
	}
	fmt.Println("LWC_TEST_MATERIALIZE_OK")
}

func secretManagerResponseReader(t *testing.T, resource string, status int, body string) secretReader {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return googleSecretReader{service: service}
}

func prepareRootForSecretManager(t *testing.T, resource string) string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "deploy", "cac")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("offline fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := json.Marshal(pipelineConfig{
		Environment: "dev", RunTimeoutSeconds: 23,
		LLM: llmProfile{
			Provider: "deepseek", Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-flash",
			RequestTimeoutSeconds: 600,
			Secret:                secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkl := filepath.Join(root, "pkl-fixture")
	script := "#!/bin/sh\ncat <<'SSOT_JSON'\n" + string(config) + "\nSSOT_JSON\n"
	if err := os.WriteFile(pkl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	return root
}
