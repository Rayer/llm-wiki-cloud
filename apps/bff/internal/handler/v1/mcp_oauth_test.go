package v1

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
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
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "OAuth fixture project", "status": "ready"}); err != nil {
		t.Fatalf("create emulator project: %v", err)
	}

	const environment = "lwc389-project-header-test"
	const resourceOrigin = "http://localhost:18080"
	service := auth.NewMCPOAuthServiceWithPolicy(fs.Raw(), environment, "local", "http://localhost:18081", resourceOrigin,
		"http://localhost:13000", "mcp-oauth-test-secret", nil)
	client, err := service.RegisterClient(ctx, "Hermes test", []string{"http://127.0.0.1:43123/callback"})
	if err != nil {
		t.Fatalf("register test client: %v", err)
	}
	verifier := strings.Repeat("a", 43)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	resource := service.ResourceURL(projectID)
	consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge,
		"S256", "fixture-state", resource, "mcp:query mcp:raw:write")
	if err != nil {
		t.Fatalf("begin authorization: %v", err)
	}
	parsedConsent, err := url.Parse(consentURL)
	if err != nil {
		t.Fatalf("parse consent URL: %v", err)
	}
	transactionID := parsedConsent.Query().Get("transaction_id")
	if _, err := service.ConsentDetails(ctx, userID, 0, transactionID); err != nil {
		t.Fatalf("read consent details: %v", err)
	}
	decision, err := service.DecideConsent(ctx, userID, 0, transactionID, true)
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

	root := localfs.New(t.TempDir())
	handler := New(root, fs, nil, nil, nil, nil)
	handler.SetConsumerVerifier(mcpOAuthTestConsumer{service: service, issuer: "http://localhost:18081"})
	handler.SetCLIProjectAuthorizer(auth.FirestoreProjectOwnerAuthorizer(fs.Raw()))
	handler.pipelineEventSource = func(_ context.Context, gotUserID, gotProjectID string) (pipelineEventSnapshot, error) {
		if gotUserID != userID || gotProjectID != projectID {
			return pipelineEventSnapshot{}, fmt.Errorf("OAuth Pipeline source scope mismatch: %s/%s", gotUserID, gotProjectID)
		}
		return testEventSnapshot(gotUserID, gotProjectID, "oauth-fixture-revision")
	}
	executor := &countingMCPQueryExecutor{}
	handler.SetQueryExecutor(executor)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/mcp/projects/:projectID", handler.MCPProjectOAuthHandler("local", resourceOrigin))
	oauthWireRequest := func(token, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2026-07-28")
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err == nil {
			request.Header.Set("MCP-Method", envelope.Method)
			if envelope.Method == "resources/read" {
				var params struct {
					Params struct {
						URI string `json:"uri"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(body), &params); err == nil {
					request.Header.Set("MCP-Name", params.Params.URI)
				}
			}
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
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

	wrongPathRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/other-project",
		strings.NewReader(`{"jsonrpc":"2.0","id":19,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"coffee"}}}`))
	wrongPathRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	wrongPathRequest.Header.Set("Content-Type", "application/json")
	wrongPathRequest.Header.Add("Accept", "application/json")
	wrongPathRequest.Header.Add("Accept", "text/event-stream")
	wrongPathRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	wrongPathResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongPathResponse, wrongPathRequest)
	if wrongPathResponse.Code != http.StatusForbidden || executor.calls != 2 {
		t.Fatalf("wrong Project resource path status=%d executor calls=%d body=%s", wrongPathResponse.Code, executor.calls, wrongPathResponse.Body.String())
	}

	addRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(mcpToolCallBody(20, "add_raw", `{"filename":"oauth-source.md","markdown":"# OAuth source"}`)))
	addRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	addRequest.Header.Set("Content-Type", "application/json")
	addRequest.Header.Add("Accept", "application/json")
	addRequest.Header.Add("Accept", "text/event-stream")
	addRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	addResponse := httptest.NewRecorder()
	router.ServeHTTP(addResponse, addRequest)
	if addResponse.Code != http.StatusOK {
		t.Fatalf("OAuth add_raw status=%d body=%s", addResponse.Code, addResponse.Body.String())
	}
	var addWire map[string]any
	if err := json.Unmarshal(addResponse.Body.Bytes(), &addWire); err != nil {
		t.Fatalf("decode OAuth add_raw response: %v", err)
	}
	if payload := mcpStructuredResult(t, addWire); payload["status"] != "created" || payload["path"] != "raw/oauth-source.md" {
		t.Fatalf("OAuth add_raw result=%#v", payload)
	}
	if data, err := root.Scope(userID, projectID).ReadFile(context.Background(), "raw/oauth-source.md"); err != nil {
		t.Fatalf("OAuth add_raw was not scoped to the grant Project: %v", err)
	} else if frontmatter, body, err := parseFrontmatterJSON(string(data)); err != nil || body != "# OAuth source" || frontmatter["created"] == nil {
		t.Fatalf("OAuth add_raw bytes/frontmatter = %#v %q err=%v", frontmatter, body, err)
	}
	beforeLegacy, err := root.Scope(userID, projectID).ReadFile(context.Background(), "raw/oauth-source.md")
	if err != nil {
		t.Fatalf("read raw bytes before legacy call: %v", err)
	}
	writerListRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(`{"jsonrpc":"2.0","id":25,"method":"tools/list","params":{}}`))
	writerListRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	writerListRequest.Header.Set("Content-Type", "application/json")
	writerListRequest.Header.Add("Accept", "application/json")
	writerListRequest.Header.Add("Accept", "text/event-stream")
	writerListRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	writerListResponse := httptest.NewRecorder()
	router.ServeHTTP(writerListResponse, writerListRequest)
	var writerListWire map[string]any
	if writerListResponse.Code != http.StatusOK || json.Unmarshal(writerListResponse.Body.Bytes(), &writerListWire) != nil {
		t.Fatalf("raw-write tools/list status=%d body=%s", writerListResponse.Code, writerListResponse.Body.String())
	}
	writerTools, ok := writerListWire["result"].(map[string]any)["tools"].([]any)
	if !ok {
		t.Fatalf("raw-write tools/list response=%#v", writerListWire)
	}
	writerNames := make(map[string]bool, len(writerTools))
	for _, item := range writerTools {
		writerNames[item.(map[string]any)["name"].(string)] = true
	}
	if len(writerNames) != 4 || !writerNames["query_project"] || !writerNames["pipeline_status"] || !writerNames["add_raw"] || !writerNames["import_url"] {
		t.Fatalf("OAuth raw-write discovery = %v, want query_project, pipeline_status, add_raw, and import_url", writerNames)
	}

	urlImportRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(mcpToolCallBody(26, "import_url", `{"url":"https://example.test/oauth-article"}`)))
	urlImportRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	urlImportRequest.Header.Set("Content-Type", "application/json")
	urlImportRequest.Header.Add("Accept", "application/json")
	urlImportRequest.Header.Add("Accept", "text/event-stream")
	urlImportRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	urlImportResponse := httptest.NewRecorder()
	router.ServeHTTP(urlImportResponse, urlImportRequest)
	var urlImportWire map[string]any
	if urlImportResponse.Code != http.StatusOK || json.Unmarshal(urlImportResponse.Body.Bytes(), &urlImportWire) != nil {
		t.Fatalf("OAuth import_url status=%d body=%s", urlImportResponse.Code, urlImportResponse.Body.String())
	}
	urlImportPayload := mcpStructuredResult(t, urlImportWire)
	if urlImportPayload["status"] != "unavailable" || urlImportPayload["failure_stage"] != "setup" || urlImportPayload["raw_url"] != nil {
		t.Fatalf("OAuth unconfigured import_url result=%#v", urlImportPayload)
	}
	if files, err := root.Scope(userID, projectID).ListRawFiles(context.Background()); err != nil || len(files) != 1 {
		t.Fatalf("OAuth unconfigured import_url call mutated raw: files=%v err=%v", files, err)
	}
	if data, err := root.Scope(userID, projectID).ReadFile(context.Background(), "raw/oauth-source.md"); err != nil || string(data) != string(beforeLegacy) {
		t.Fatalf("OAuth unconfigured import_url call changed add_raw bytes: %q err=%v", data, err)
	}

	readOnlyClient, err := service.RegisterClient(ctx, "Hermes query-only test", []string{"http://127.0.0.1:43124/callback"})
	if err != nil {
		t.Fatalf("register query-only client: %v", err)
	}
	readOnlyVerifier := strings.Repeat("b", 43)
	readOnlyChallengeBytes := sha256.Sum256([]byte(readOnlyVerifier))
	readOnlyChallenge := base64.RawURLEncoding.EncodeToString(readOnlyChallengeBytes[:])
	readOnlyConsentURL, err := service.BeginAuthorization(ctx, readOnlyClient.ClientID, readOnlyClient.RedirectURIs[0], "code",
		readOnlyChallenge, "S256", "query-only-state", resource, "mcp:query")
	if err != nil {
		t.Fatalf("begin query-only authorization: %v", err)
	}
	readOnlyConsent, err := url.Parse(readOnlyConsentURL)
	if err != nil {
		t.Fatalf("parse query-only consent URL: %v", err)
	}
	readOnlyTransactionID := readOnlyConsent.Query().Get("transaction_id")
	if _, err := service.ConsentDetails(ctx, userID, 0, readOnlyTransactionID); err != nil {
		t.Fatalf("read query-only consent details: %v", err)
	}
	readOnlyDecision, err := service.DecideConsent(ctx, userID, 0, readOnlyTransactionID, true)
	if err != nil {
		t.Fatalf("approve query-only consent: %v", err)
	}
	readOnlyCallback, err := url.Parse(readOnlyDecision.RedirectURL)
	if err != nil {
		t.Fatalf("parse query-only callback: %v", err)
	}
	readOnlyTokens, err := service.ExchangeCode(ctx, readOnlyClient.ClientID, readOnlyCallback.Query().Get("code"), readOnlyClient.RedirectURIs[0], readOnlyVerifier, resource)
	if err != nil {
		t.Fatalf("exchange query-only code: %v", err)
	}
	modernMeta := `_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"oauth-event-fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	resourceList := oauthWireRequest(readOnlyTokens.AccessToken, `{"jsonrpc":"2.0","id":41,"method":"resources/list","params":{"`+modernMeta+`}}`)
	resourceListResult := mcpMetadataResult(t, resourceList)
	resources, ok := resourceListResult["resources"].([]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("OAuth query-only resources/list=%#v", resourceListResult)
	}
	resourceEntry := resources[0].(map[string]any)
	wantEventURI := pipelineEventsURI(projectID)
	if resourceEntry["uri"] != wantEventURI || resourceEntry["name"] != "pipeline-events" || resourceEntry["title"] != "Project Pipeline events" || resourceEntry["mimeType"] != "application/json" ||
		!strings.Contains(resourceEntry["description"].(string), "wake-up") || !strings.Contains(resourceEntry["description"].(string), "reconnect") {
		t.Fatalf("OAuth Pipeline resource contract=%#v", resourceEntry)
	}
	if resourceListResult["cacheScope"] != "private" {
		t.Fatalf("OAuth resources/list cache scope=%#v, want private", resourceListResult["cacheScope"])
	}
	resourceRead := oauthWireRequest(readOnlyTokens.AccessToken, `{"jsonrpc":"2.0","id":42,"method":"resources/read","params":{"uri":"`+wantEventURI+`","`+modernMeta+`}}`)
	resourceReadResult := mcpMetadataResult(t, resourceRead)
	contents, ok := resourceReadResult["contents"].([]any)
	if !ok || len(contents) != 1 || !strings.Contains(contents[0].(map[string]any)["text"].(string), "oauth-fixture-revision") ||
		resourceReadResult["cacheScope"] != "private" || resourceReadResult["ttlMs"] != float64(0) {
		t.Fatalf("OAuth query-only Pipeline resource read=%#v", resourceReadResult)
	}
	foreignResourceRead := oauthWireRequest(readOnlyTokens.AccessToken, `{"jsonrpc":"2.0","id":43,"method":"resources/read","params":{"uri":"`+pipelineEventsURI("foreign-project")+`","`+modernMeta+`}}`)
	if !mcpMetadataHasError(foreignResourceRead) {
		t.Fatalf("OAuth query-only grant read a foreign Project resource: %s", foreignResourceRead.Body.String())
	}
	_, projectKey, _, err := handler.projectKeyService.Create(ctx, userID, projectID, "Pipeline event test key")
	if err != nil || projectKey == "" {
		t.Fatalf("create scoped Project key: err=%v", err)
	}
	keyWireRequest := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+projectKey)
		request.Header.Set("X-Project-ID", projectID)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2026-07-28")
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err == nil {
			request.Header.Set("MCP-Method", envelope.Method)
			if envelope.Method == "resources/read" {
				var params struct {
					Params struct {
						URI string `json:"uri"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(body), &params); err == nil {
					request.Header.Set("MCP-Name", params.Params.URI)
				}
			}
		}
		response := httptest.NewRecorder()
		handler.MCPHandler().ServeHTTP(response, request)
		return response
	}
	keyResourceList := keyWireRequest(`{"jsonrpc":"2.0","id":44,"method":"resources/list","params":{"` + modernMeta + `}}`)
	keyResourceResult := mcpMetadataResult(t, keyResourceList)
	keyResources, ok := keyResourceResult["resources"].([]any)
	if !ok || len(keyResources) != 1 || keyResources[0].(map[string]any)["uri"] != wantEventURI ||
		keyResources[0].(map[string]any)["name"] != "pipeline-events" || keyResources[0].(map[string]any)["title"] != "Project Pipeline events" ||
		keyResourceResult["cacheScope"] != "private" {
		t.Fatalf("Project-key resources/list=%#v", keyResourceResult)
	}
	keyRead := keyWireRequest(`{"jsonrpc":"2.0","id":46,"method":"resources/read","params":{"uri":"` + wantEventURI + `","` + modernMeta + `}}`)
	keyReadResult := mcpMetadataResult(t, keyRead)
	if keyReadResult["cacheScope"] != "private" || keyReadResult["ttlMs"] != float64(0) {
		t.Fatalf("Project-key resources/read cache metadata=%#v", keyReadResult)
	}
	keyForeignRead := keyWireRequest(`{"jsonrpc":"2.0","id":45,"method":"resources/read","params":{"uri":"` + pipelineEventsURI("foreign-project") + `","` + modernMeta + `}}`)
	if !mcpMetadataHasError(keyForeignRead) {
		t.Fatalf("Project key read a foreign Project resource: %s", keyForeignRead.Body.String())
	}
	listRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID, strings.NewReader(`{"jsonrpc":"2.0","id":21,"method":"tools/list","params":{}}`))
	listRequest.Header.Set("Authorization", "Bearer "+readOnlyTokens.AccessToken)
	listRequest.Header.Set("Content-Type", "application/json")
	listRequest.Header.Add("Accept", "application/json")
	listRequest.Header.Add("Accept", "text/event-stream")
	listRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, listRequest)
	var listWire map[string]any
	if listResponse.Code != http.StatusOK || json.Unmarshal(listResponse.Body.Bytes(), &listWire) != nil {
		t.Fatalf("query-only tools/list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	listed, ok := listWire["result"].(map[string]any)["tools"].([]any)
	if !ok {
		t.Fatalf("query-only tools/list response=%#v", listWire)
	}
	for _, item := range listed {
		name := item.(map[string]any)["name"]
		if name == "add_raw" || name == "import_url" {
			t.Fatalf("query-only grant discovered write tool %q", name)
		}
	}
	queryOnlyRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(`{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"coffee"}}}`))
	queryOnlyRequest.Header.Set("Authorization", "Bearer "+readOnlyTokens.AccessToken)
	queryOnlyRequest.Header.Set("Content-Type", "application/json")
	queryOnlyRequest.Header.Add("Accept", "application/json")
	queryOnlyRequest.Header.Add("Accept", "text/event-stream")
	queryOnlyRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	queryOnlyResponse := httptest.NewRecorder()
	router.ServeHTTP(queryOnlyResponse, queryOnlyRequest)
	if queryOnlyResponse.Code != http.StatusOK || executor.calls != 3 {
		t.Fatalf("query-only grant query status=%d executor calls=%d body=%s", queryOnlyResponse.Code, executor.calls, queryOnlyResponse.Body.String())
	}
	deniedRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(mcpToolCallBody(22, "add_raw", `{"filename":"denied.md","markdown":"must not write"}`)))
	deniedRequest.Header.Set("Authorization", "Bearer "+readOnlyTokens.AccessToken)
	deniedRequest.Header.Set("Content-Type", "application/json")
	deniedRequest.Header.Add("Accept", "application/json")
	deniedRequest.Header.Add("Accept", "text/event-stream")
	deniedRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	deniedResponse := httptest.NewRecorder()
	router.ServeHTTP(deniedResponse, deniedRequest)
	var deniedWire map[string]any
	if err := json.Unmarshal(deniedResponse.Body.Bytes(), &deniedWire); err != nil {
		t.Fatalf("decode query-only add_raw response: %v", err)
	}
	protocolError, denied := deniedWire["error"].(map[string]any)
	if deniedResponse.Code != http.StatusOK || !denied || protocolError["code"] != float64(-32602) {
		t.Fatalf("query-only add_raw call was not denied as unknown tool: status=%d response=%#v", deniedResponse.Code, deniedWire)
	}
	if _, err := root.Scope(userID, projectID).ReadFile(context.Background(), "raw/denied.md"); err == nil {
		t.Fatal("query-only grant wrote a raw source")
	}
	grants, err := service.ListGrants(ctx, userID, projectID)
	if err != nil || len(grants) != 2 {
		t.Fatalf("list grants before revoke: grants=%+v err=%v", grants, err)
	}
	var writeGrantID string
	for _, grant := range grants {
		if grant.Scope == "mcp:query mcp:raw:write" {
			writeGrantID = grant.ID
		}
	}
	if writeGrantID == "" {
		t.Fatal("write consent was not retained in Project grant metadata")
	}
	if err := service.RevokeGrant(ctx, userID, 0, writeGrantID); err != nil {
		t.Fatalf("revoke write grant: %v", err)
	}
	revokedRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(`{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"coffee"}}}`))
	revokedRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	revokedRequest.Header.Set("Content-Type", "application/json")
	revokedRequest.Header.Add("Accept", "application/json")
	revokedRequest.Header.Add("Accept", "text/event-stream")
	revokedRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	revokedResponse := httptest.NewRecorder()
	router.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked OAuth token status=%d body=%s, want unauthorized", revokedResponse.Code, revokedResponse.Body.String())
	}

	expiredService := auth.NewMCPOAuthService(fs.Raw(), environment, "http://localhost:18081", resourceOrigin,
		"http://localhost:13000", "mcp-oauth-test-secret")
	past := time.Now().UTC().Add(-15 * time.Minute)
	expiredService.SetClock(func() time.Time { return past })
	expiredClient, err := expiredService.RegisterClient(ctx, "Hermes expired-token test", []string{"http://127.0.0.1:43125/callback"})
	if err != nil {
		t.Fatalf("register expired-token client: %v", err)
	}
	expiredVerifier := strings.Repeat("c", 43)
	expiredChallengeBytes := sha256.Sum256([]byte(expiredVerifier))
	expiredChallenge := base64.RawURLEncoding.EncodeToString(expiredChallengeBytes[:])
	expiredConsentURL, err := expiredService.BeginAuthorization(ctx, expiredClient.ClientID, expiredClient.RedirectURIs[0], "code",
		expiredChallenge, "S256", "expired-state", resource, "mcp:query mcp:raw:write")
	if err != nil {
		t.Fatalf("begin expired-token authorization: %v", err)
	}
	expiredConsent, err := url.Parse(expiredConsentURL)
	if err != nil {
		t.Fatalf("parse expired-token consent URL: %v", err)
	}
	expiredTransactionID := expiredConsent.Query().Get("transaction_id")
	if _, err := expiredService.ConsentDetails(ctx, userID, 0, expiredTransactionID); err != nil {
		t.Fatalf("read expired-token consent details: %v", err)
	}
	expiredDecision, err := expiredService.DecideConsent(ctx, userID, 0, expiredTransactionID, true)
	if err != nil {
		t.Fatalf("approve expired-token consent: %v", err)
	}
	expiredCallback, err := url.Parse(expiredDecision.RedirectURL)
	if err != nil {
		t.Fatalf("parse expired-token callback: %v", err)
	}
	expiredTokens, err := expiredService.ExchangeCode(ctx, expiredClient.ClientID, expiredCallback.Query().Get("code"),
		expiredClient.RedirectURIs[0], expiredVerifier, resource)
	if err != nil {
		t.Fatalf("exchange expired-token code: %v", err)
	}
	expiredRequest := httptest.NewRequest(http.MethodPost, "/mcp/projects/"+projectID,
		strings.NewReader(mcpToolCallBody(25, "add_raw", `{"filename":"expired.md","markdown":"must not write"}`)))
	expiredRequest.Header.Set("Authorization", "Bearer "+expiredTokens.AccessToken)
	expiredRequest.Header.Set("Content-Type", "application/json")
	expiredRequest.Header.Add("Accept", "application/json")
	expiredRequest.Header.Add("Accept", "text/event-stream")
	expiredRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	expiredResponse := httptest.NewRecorder()
	router.ServeHTTP(expiredResponse, expiredRequest)
	if expiredResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expired OAuth token status=%d body=%s, want unauthorized", expiredResponse.Code, expiredResponse.Body.String())
	}
	if _, err := root.Scope(userID, projectID).ReadFile(context.Background(), "raw/expired.md"); err == nil {
		t.Fatal("expired grant wrote a raw source")
	}
}

type mcpOAuthTestConsumer struct {
	service *auth.MCPOAuthService
	issuer  string
}

func (v mcpOAuthTestConsumer) Verify(ctx context.Context, credential, projectID string) (authconsumer.Identity, error) {
	return v.VerifyResource(ctx, credential, v.service.ResourceURL(projectID), projectID)
}

func (v mcpOAuthTestConsumer) VerifyResource(ctx context.Context, credential, resource, projectID string) (authconsumer.Identity, error) {
	verified, err := v.service.VerifyAccessToken(ctx, credential)
	if err != nil {
		if errors.Is(err, auth.ErrMCPOAuthInvalid) || errors.Is(err, auth.ErrMCPOAuthExpired) {
			return authconsumer.Identity{}, authconsumer.ErrInvalidCredential
		}
		return authconsumer.Identity{}, authconsumer.ErrUnavailable
	}
	if verified.ProjectID != projectID || verified.Resource != resource {
		return authconsumer.Identity{}, authconsumer.ErrForbidden
	}
	return authconsumer.Identity{SchemaVersion: 1, Subject: verified.UserID, CredentialKind: "mcp-oauth",
		Environment: verified.Environment, Issuer: v.issuer, Resource: verified.Resource, ExpiresAt: verified.ExpiresAt.Unix(),
		AuthVersion: verified.AuthVersion, Scopes: verified.Scopes, ManagementScopes: []string{},
		GrantID: verified.GrantID, ClientID: verified.ClientID, ProjectID: verified.ProjectID}, nil
}

func (v mcpOAuthTestConsumer) CheckAccount(_ context.Context, userID string) (authconsumer.Account, error) {
	return authconsumer.Account{SchemaVersion: 1, Subject: userID, Active: true}, nil
}
