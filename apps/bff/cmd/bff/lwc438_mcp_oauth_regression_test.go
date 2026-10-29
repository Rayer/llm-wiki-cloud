package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	"github.com/rayer/llm-wiki-bff/internal/config"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func TestLWC438ProductionRouterUsesLoadedEnvironmentForProjectMCP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, environment := range []string{"dev", "prod", "local"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("PORT", "")
			const projectID = "lwc438-project"
			var resource, issuer, runtimeToken string
			var loadedEnvironment string
			authVerifierRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/auth/consumers/lwc/verify-credential" ||
					r.Header.Get("Authorization") != "Bearer "+runtimeToken {
					t.Errorf("synthetic Auth verifier received an unexpected request")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				var request struct {
					SchemaVersion int    `json:"schema_version"`
					Resource      string `json:"resource"`
					ProjectID     string `json:"project_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SchemaVersion != 1 ||
					request.Resource != resource || request.ProjectID != projectID {
					t.Errorf("synthetic Auth verifier received a request outside the loaded resource/project scope")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				credential := r.Header.Get("X-LWC-User-Credential")
				authVerifierRequests++
				if credential == "fixture-invalid" || credential == "fixture-expired" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"invalid"}`))
					return
				}
				identity := authconsumer.Identity{
					SchemaVersion: 1, Subject: "fixture-user", CredentialKind: "mcp-oauth",
					Environment: loadedEnvironment, Issuer: issuer, Resource: resource,
					ExpiresAt: time.Now().Add(time.Minute).Unix(), Scopes: []string{"mcp:query"},
					ManagementScopes: []string{}, GrantID: "fixture-grant", ClientID: "fixture-client", ProjectID: projectID,
				}
				switch credential {
				case "fixture-wrong-environment":
					identity.Environment = "other-environment"
				case "fixture-wrong-project":
					identity.ProjectID = "other-project"
				case "fixture-wrong-resource":
					identity.Resource = resource + "/other"
				}
				w.Header().Set("Cache-Control", "no-store")
				if err := json.NewEncoder(w).Encode(identity); err != nil {
					t.Errorf("encode synthetic Auth identity: %v", err)
				}
			}))
			defer server.Close()

			file := lwc438BFFFileForLWC438Test(environment, server.URL)
			issuer = file.AuthServiceURL
			loadedEnvironment = file.Environment
			resource = file.MCPResourceOrigin + "/mcp/projects/" + projectID
			runtimeToken = "fixture-runtime-identity"
			if environment == "local" {
				runtimeToken = "gateauth-local-fixture:oidc-v1"
			}
			data, err := json.Marshal(file)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "bff.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadBFFFile(path)
			if err != nil {
				t.Fatalf("load synthetic %s BFF config: %v", environment, err)
			}
			resource = cfg.MCPResourceOrigin + "/mcp/projects/" + projectID

			handler := handlerv1.New(nil, nil, nil, nil, nil, nil)
			projectChecks := 0
			handler.SetCLIProjectAuthorizer(func(_ context.Context, userID, gotProjectID string) error {
				projectChecks++
				if userID != "fixture-user" || gotProjectID != projectID {
					t.Errorf("production project authorization received an unexpected identity")
				}
				return nil
			})
			factory := func(_ context.Context, consumer authconsumer.Config, httpClient *http.Client) (*authconsumer.Client, error) {
				consumer.BaseURL = server.URL // Safe local Auth transport for this regression test.
				return authconsumer.New(consumer, httpClient, func(_ context.Context, audience string) (string, error) {
					if audience != consumer.Audience {
						t.Errorf("synthetic Auth runtime audience was not preserved")
					}
					return runtimeToken, nil
				})
			}
			if _, err := configureAuthConsumer(handler, cfg, environment == "local", factory); err != nil {
				t.Fatalf("configure BFF Auth verifier: %v", err)
			}
			router := newProductionRouter(cfg, environment == "local", nil, nil, handler,
				&syssettings.FakeStore{Enabled: true}, nil)

			for _, test := range []struct {
				name             string
				credential       string
				project          string
				wantStatus       int
				wantProjectCheck bool
			}{
				{name: "authenticated tools list", credential: "fixture-valid", wantStatus: http.StatusOK, wantProjectCheck: true},
				{name: "wrong environment", credential: "fixture-wrong-environment", wantStatus: http.StatusServiceUnavailable},
				{name: "wrong project", credential: "fixture-wrong-project", wantStatus: http.StatusServiceUnavailable},
				{name: "wrong resource", credential: "fixture-wrong-resource", wantStatus: http.StatusServiceUnavailable},
				{name: "conflicting project header", credential: "fixture-valid", project: "other-project", wantStatus: http.StatusForbidden, wantProjectCheck: true},
				{name: "invalid credential", credential: "fixture-invalid", wantStatus: http.StatusUnauthorized},
				{name: "expired credential", credential: "fixture-expired", wantStatus: http.StatusUnauthorized},
			} {
				t.Run(test.name, func(t *testing.T) {
					checksBefore := projectChecks
					verifierRequestsBefore := authVerifierRequests
					request := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
						strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
					request.Header.Set("Authorization", "Bearer "+test.credential)
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Accept", "application/json, text/event-stream")
					request.Header.Set("MCP-Protocol-Version", "2025-11-25")
					if test.project != "" {
						request.Header.Set("X-Project-ID", test.project)
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if authVerifierRequests != verifierRequestsBefore+1 {
						t.Fatalf("production route made %d synthetic Auth verifier requests, want 1", authVerifierRequests-verifierRequestsBefore)
					}
					if response.Code != test.wantStatus {
						t.Fatalf("loaded BFF environment %q returned status %d, want %d: %s",
							cfg.AuthEnvironment, response.Code, test.wantStatus, response.Body.String())
					}
					wantChecks := checksBefore
					if test.wantProjectCheck {
						wantChecks++
					}
					if projectChecks != wantChecks {
						t.Fatalf("production project authority calls=%d, want %d", projectChecks-checksBefore, wantChecks-checksBefore)
					}
					if test.name == "authenticated tools list" {
						var wire struct {
							Result struct {
								Tools []struct {
									Name string `json:"name"`
								} `json:"tools"`
							} `json:"result"`
						}
						if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
							t.Fatalf("decode MCP tools/list response: %v", err)
						}
						foundQueryTool := false
						for _, tool := range wire.Result.Tools {
							foundQueryTool = foundQueryTool || tool.Name == "query_project"
						}
						if !foundQueryTool {
							t.Fatalf("authenticated MCP tools/list omitted query_project: %s", response.Body.String())
						}
					}
				})
			}
		})
	}
}

func lwc438BFFFileForLWC438Test(environment, localAuthURL string) config.BFFFile {
	enabled := true
	file := config.BFFFile{
		SchemaVersion: 2, Environment: environment, Target: "bff",
		GCPProject: "llm-wiki-cloud", Bucket: "llm-wiki-data-" + environment,
		FirestoreDatabaseID: "llm-wiki-cloud-" + environment,
		AuthServiceURL:      "https://auth.dev.rayer.idv.tw", MCPResourceOrigin: "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app",
		PipelineJobURL: config.DefaultPipelineJobURL, AllowedOrigins: []string{"https://wiki.example.test"}, AllowedHosts: []string{},
		PipelineDailyLimit: 2, PipelineCooldownSeconds: 600, PipelineMinNewRaw: 1, PipelineDemoUserIDs: []string{},
		AuthSessionEnvironment: "development", AuthSessionMigration: "disabled", RegistrationEnabled: &enabled,
		JWTSecret: "fixture-jwt-secret", DeepSeekAPIKey: "fixture-deepseek-key",
		PipelineSchedule: config.BFFPipelineSchedule{Cron: "0 * * * *", Timezone: "UTC"},
		LLM:              config.BFFLLM{Provider: "deepseek", BaseURL: "https://api.deepseek.com", RequestTimeoutSeconds: 60, Model: "deepseek-flash"},
		Query:            config.BFFQuery{StageConfigPath: "/app/configs/query/dev/query.json"}, PortDefault: 8080,
	}
	if environment == "prod" {
		file.AuthServiceURL = "https://auth.rayer.idv.tw"
		file.MCPResourceOrigin = "https://llm-wiki-bff-580854833715.asia-east1.run.app"
		file.AuthSessionEnvironment = "production"
	}
	if environment == "local" {
		file.SchemaVersion = 3
		file.GCPProject, file.Bucket, file.FirestoreDatabaseID = "llm-wiki-cloud", "llm-wiki-cloud-local", "llm-wiki-cloud-local"
		file.AuthServiceURL, file.MCPResourceOrigin = localAuthURL, "http://localhost:8080"
		file.AllowedOrigins, file.AllowedHosts = []string{"http://localhost:3000"}, []string{"localhost", "127.0.0.1"}
		file.AuthSessionEnvironment, file.JWTSecret = "llm-wiki-cloud-local", ""
		file.Query = config.BFFQuery{StageConfigPath: "/tmp/query.json"}
		file.Local = &config.BFFLocal{Scope: "worktree-current", WorkerPath: "/tmp/worker", PipelineConfigPath: "/tmp/synto.toml", PipelineBindingsPath: "/tmp/bindings.json"}
		file.AuthConsumer = &config.BFFAuthConsumer{
			SchemaVersion: 1, Environment: "local", AuthURL: localAuthURL, Issuer: localAuthURL,
			RuntimeServiceAccount: "bff-local@localhost", VerifierAudience: localAuthURL,
			Resource: file.MCPResourceOrigin, VerifierPath: "/api/v1/auth/consumers/lwc/verify-credential",
			AccountCheckPath: "/api/v1/auth/consumers/lwc/accounts/check", CatalogPath: "/api/v1/auth/consumers/lwc/management/catalog",
			AuthorityModes: []string{"synthetic-local-fixture"}, TimeoutSeconds: 5,
		}
	}
	return file
}
