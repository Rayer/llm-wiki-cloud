package v1

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
)

type countingMCPQueryExecutor struct{ calls int }

func (e *countingMCPQueryExecutor) Execute(_ context.Context, _ cache.Reader, request query.Request) (query.Result, error) {
	e.calls++
	return query.Result{Query: request.Query, Mode: request.Mode}, nil
}

func TestMCPProjectOAuthUsesConfiguredResourceMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &Handler{}
	router.Any("/mcp/projects/:projectID", handler.MCPProjectOAuthHandler("local-fixture", "http://localhost:8080"))
	router.GET("/.well-known/oauth-protected-resource/mcp/projects/:projectID",
		MCPProjectResourceMetadataHandler("http://localhost:8080", "http://localhost:8081"))

	metadataResponse := httptest.NewRecorder()
	metadataRequest := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp/projects/project-1", nil)
	metadataRequest.Host = "attacker.example.test"
	router.ServeHTTP(metadataResponse, metadataRequest)
	if metadataResponse.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, body=%s", metadataResponse.Code, metadataResponse.Body.String())
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataResponse.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["resource"] != "http://localhost:8080/mcp/projects/project-1" ||
		metadata["authorization_servers"].([]any)[0] != "http://localhost:8081" {
		t.Fatalf("metadata used request Host or omitted the configured issuer: %#v", metadata)
	}
	scopes, ok := metadata["scopes_supported"].([]any)
	if !ok || len(scopes) != 2 || scopes[0] != mcpQueryCapability || scopes[1] != mcpRawWriteCapability {
		t.Fatalf("resource metadata scopes = %#v, want query and raw write", metadata["scopes_supported"])
	}

	challengeResponse := httptest.NewRecorder()
	challengeRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/project-1", nil)
	challengeRequest.Host = "attacker.example.test"
	router.ServeHTTP(challengeResponse, challengeRequest)
	if challengeResponse.Code != http.StatusUnauthorized ||
		challengeResponse.Header().Get("WWW-Authenticate") != `Bearer resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp/projects/project-1"` {
		t.Fatalf("resource challenge status=%d header=%q", challengeResponse.Code, challengeResponse.Header().Get("WWW-Authenticate"))
	}
}

func TestMCPProjectOAuthRejectsConflictingProjectHeaderBeforeSDK(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	fs, err := internalfirestore.NewClientWithDatabaseAndScope("lwc389-mcp-oauth-test", "", "", "", "")
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer fs.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, projectID := "mcp-oauth-user-"+suffix, "mcp-oauth-project-"+suffix
	userRef := internalfirestore.Collection(fs.Raw(), "users").Doc(userID)
	projectRef := internalfirestore.Collection(fs.Raw(), "projects").Doc(userID + "_" + projectID)
	if _, err := userRef.Set(ctx, auth.UserRecord{Email: userID + "@example.test", Status: auth.AccountActive, AuthVersion: 0}); err != nil {
		t.Fatalf("create emulator account: %v", err)
	}
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "OAuth fixture project"}); err != nil {
		t.Fatalf("create emulator project: %v", err)
	}

	const environment = "lwc389-project-header-test"
	const resourceOrigin = "http://localhost:18080"
	service := auth.NewMCPOAuthService(fs.Raw(), environment, "http://localhost:18081", resourceOrigin,
		"http://localhost:13000", "mcp-oauth-test-secret")
	client, err := service.RegisterClient(ctx, "Hermes test", []string{"http://127.0.0.1:43123/callback"})
	if err != nil {
		t.Fatalf("register test client: %v", err)
	}
	verifier := strings.Repeat("a", 43)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	resource := service.ResourceURL(projectID)
	consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge,
		"S256", "fixture-state", resource, "mcp:query")
	if err != nil {
		t.Fatalf("begin authorization: %v", err)
	}
	parsedConsent, err := url.Parse(consentURL)
	if err != nil {
		t.Fatalf("parse consent URL: %v", err)
	}
	decision, err := service.DecideConsent(ctx, userID, 0, parsedConsent.Query().Get("transaction_id"), true)
	if err != nil {
		t.Fatalf("approve consent: %v", err)
	}
	callback, err := url.Parse(decision.RedirectURL)
	if err != nil {
		t.Fatalf("parse callback URL: %v", err)
	}
	tokens, err := service.ExchangeCode(ctx, client.ClientID, callback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil {
		t.Fatalf("exchange authorization code: %v", err)
	}

	handler := New(localfs.New(t.TempDir()), fs, nil, nil, nil, nil)
	executor := &countingMCPQueryExecutor{}
	handler.SetQueryExecutor(executor)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/mcp/projects/:projectID", handler.MCPProjectOAuthHandler(environment, resourceOrigin))
	for _, test := range []struct {
		name       string
		projectID  string
		wantStatus int
		wantCalls  int
	}{
		{name: "header absent", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "matching header", projectID: projectID, wantStatus: http.StatusOK, wantCalls: 2},
		{name: "conflicting header", projectID: "other-project", wantStatus: http.StatusForbidden, wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"coffee"}}}`
			request := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Add("Accept", "application/json")
			request.Header.Add("Accept", "text/event-stream")
			request.Header.Set("MCP-Protocol-Version", "2025-11-25")
			if test.projectID != "" {
				request.Header.Set("X-Project-ID", test.projectID)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || executor.calls != test.wantCalls {
				t.Fatalf("status=%d want=%d executor calls=%d want=%d body=%s", response.Code, test.wantStatus,
					executor.calls, test.wantCalls, response.Body.String())
			}
		})
	}
}
