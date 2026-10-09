package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

var projectKeyTokenPattern = regexp.MustCompile(`^lwc_pk_[0-9a-f]{32}\.[A-Za-z0-9_-]{43}$`)

type projectKeyTestExecutor func(context.Context, cache.Reader, query.Request) (query.Result, error)

func (fn projectKeyTestExecutor) Execute(ctx context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
	return fn(ctx, reader, request)
}

func TestProjectKeyManagementAndQueryRoutes(t *testing.T) {
	if !loopbackFirestoreEmulator(t) {
		t.Skip("local Firestore emulator required")
	}
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc377_project_key_routes")
	projectName := fmt.Sprintf("lwc377-project-key-%d", time.Now().UnixNano())
	fsClient, err := firestore.NewClientWithDatabase(projectName, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsClient.Raw().Close() })
	ctx := context.Background()
	userID := fmt.Sprintf("lwc377owner%d", time.Now().UnixNano())
	projectID := "research"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "users").Doc(userID).Set(ctx, map[string]any{
		"status": auth.AccountActive, "auth_version": int64(0), "role": "member",
	}); err != nil {
		t.Fatal(err)
	}
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "Research"}); err != nil {
		t.Fatal(err)
	}

	const jwtSecret, sessionEnvironment = "lwc377-project-key-router-secret", "lwc377-project-key-router-emulator"
	h := handlerv1.New(localfs.New(t.TempDir()), fsClient, nil, nil, nil, nil)
	queryCount := 0
	queryPrefixes := []string{}
	h.SetQueryExecutor(projectKeyTestExecutor(func(_ context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
		queryCount++
		got := reader.Prefix()
		queryPrefixes = append(queryPrefixes, got)
		if !strings.Contains(got, userID) {
			t.Errorf("Query reader escaped user scope: %q", got)
		}
		return query.Result{Query: request.Query, Mode: request.Mode}, nil
	}))
	lookupAvailable := true
	accountLookup := auth.FirestoreAccountLookup(fsClient.Raw())
	h.SetAccountLookup(func(ctx context.Context, id string) (*auth.UserRecord, error) {
		if !lookupAvailable {
			return nil, auth.ErrAccountUnavailable
		}
		return accountLookup(ctx, id)
	})
	cfg := config.Config{JWTSecret: jwtSecret, AuthSessionEnvironment: sessionEnvironment, AllowedOrigins: []string{"https://frontend.example.test"}}
	router := newProductionRouter(cfg, false, nil, fsClient, h, &syssettings.FakeStore{Enabled: true}, nil)
	webToken, err := auth.GenerateAccessToken(userID, "member", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewRefreshSessionAuthority(fsClient.Raw(), sessionEnvironment)
	cli, err := sessions.IssueCLISession(ctx, userID, "test CLI", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, body, projectHeader string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if projectHeader != "" {
			req.Header.Set("X-Project-ID", projectHeader)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}

	createdResponse := request(http.MethodPost, "/api/v1/projects/"+projectID+"/keys", webToken, `{"name":"Indexer"}`, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d, want %d", createdResponse.Code, http.StatusCreated)
	}
	var created struct {
		Key struct {
			KeyID        string   `json:"key_id"`
			Name         string   `json:"name"`
			ProjectID    string   `json:"project_id"`
			Capabilities []string `json:"capabilities"`
			State        string   `json:"state"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !projectKeyTokenPattern.MatchString(created.Secret) || len(created.Secret) != 83 {
		t.Fatalf("create returned non-canonical key format (len=%d)", len(created.Secret))
	}
	if created.Key.KeyID == "" || created.Key.Name != "Indexer" || created.Key.ProjectID != projectID || created.Key.State != "active" || strings.Join(created.Key.Capabilities, ",") != "query" {
		t.Fatalf("create returned incorrect metadata: %#v", created.Key)
	}

	listResponse := request(http.MethodGet, "/api/v1/projects/"+projectID+"/keys", webToken, "", "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	if strings.Contains(listResponse.Body.String(), created.Secret) || strings.Contains(listResponse.Body.String(), "secret_hash") || strings.Contains(listResponse.Body.String(), `"secret"`) {
		t.Fatal("list response exposed a key secret or hash")
	}
	if got := request(http.MethodGet, "/api/v1/projects/other/keys", webToken, "", "").Code; got != http.StatusNotFound {
		t.Fatalf("foreign project management status=%d, want 404", got)
	}
	if got := request(http.MethodGet, "/api/v1/projects/"+projectID+"/keys", cli.AccessToken, "", "").Code; got != http.StatusForbidden {
		t.Fatalf("CLI management status=%d, want 403", got)
	}
	if got := request(http.MethodGet, "/api/v1/projects/"+projectID+"/keys", "invalid", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid JWT management status=%d, want 401", got)
	}
	if got := request(http.MethodGet, "/api/v1/projects/"+projectID+"/keys", "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("missing JWT management status=%d, want 401", got)
	}
	for _, body := range []string{`{"name":""}`, `{"name":"x","capabilities":["admin"]}`, `{"name":"x"} {}`} {
		if got := request(http.MethodPost, "/api/v1/projects/"+projectID+"/keys", webToken, body, "").Code; got != http.StatusBadRequest {
			t.Errorf("invalid create body %q status=%d, want 400", body, got)
		}
	}
	lookupAvailable = false
	failedLookup := request(http.MethodPost, "/api/v1/projects/"+projectID+"/keys", webToken, `{"name":"must-not-write"}`, "")
	if failedLookup.Code != http.StatusServiceUnavailable {
		t.Fatalf("management lookup outage status=%d, want 503", failedLookup.Code)
	}
	lookupAvailable = true
	unchangedList := request(http.MethodGet, "/api/v1/projects/"+projectID+"/keys", webToken, "", "")
	if strings.Contains(unchangedList.Body.String(), "must-not-write") {
		t.Fatal("management account lookup outage mutated project-key storage")
	}

	keyQuery := func(projectHeader string) *httptest.ResponseRecorder {
		return request(http.MethodPost, "/api/v1/query", created.Secret, `{"q":"coffee","mode":"wiki"}`, projectHeader)
	}
	if got := keyQuery("").Code; got != http.StatusOK {
		t.Fatalf("bound-project query without X-Project-ID status=%d", got)
	}
	if got := keyQuery(projectID).Code; got != http.StatusOK {
		t.Fatalf("bound-project query with matching X-Project-ID status=%d", got)
	}
	if got := request(http.MethodPost, "/api/v1/query", webToken, `{"q":"coffee","mode":"wiki"}`, projectID).Code; got != http.StatusOK {
		t.Fatalf("existing Web JWT query status=%d", got)
	}
	if got := request(http.MethodPost, "/api/v1/query", cli.AccessToken, `{"q":"coffee","mode":"wiki"}`, projectID).Code; got != http.StatusOK {
		t.Fatalf("existing CLI JWT query status=%d", got)
	}
	if got := keyQuery("another-project").Code; got != http.StatusForbidden {
		t.Fatalf("cross-project query status=%d, want 403", got)
	}
	if queryCount != 4 {
		t.Fatalf("Query executor called %d times, want 4; cross-project request must be rejected first", queryCount)
	}
	for i, prefix := range queryPrefixes {
		if !strings.Contains(prefix, projectID) {
			t.Fatalf("existing HTTP Query %d did not retain project scope %q: %q", i+1, projectID, prefix)
		}
	}
	for _, path := range []string{"/api/v1/health", "/api/v1/projects"} {
		if got := request(http.MethodGet, path, created.Secret, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("project key on protected BFF route %s status=%d, want 401", path, got)
		}
	}
	publicLogout := request(http.MethodPost, "/api/v1/auth/logout", created.Secret, "", "")
	if publicLogout.Code != http.StatusOK {
		t.Fatalf("public Auth logout was intercepted by project-key rejection: %d", publicLogout.Code)
	}
	if got := request(http.MethodGet, "/api/v1/query/config", created.Secret, "", "").Code; got == http.StatusUnauthorized {
		t.Fatal("project key was intercepted on the public query config route")
	}

	mcpRequest := func(method, token, body, projectHeader, sessionID string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		if projectHeader != "" {
			req.Header.Set("X-Project-ID", projectHeader)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		for _, token := range []string{"", "invalid"} {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`
			if method != http.MethodPost {
				body = ""
			}
			if got := mcpRequest(method, token, body, "", "fixture-session").Code; got != http.StatusUnauthorized {
				t.Fatalf("MCP %s with token %q status=%d, want 401", method, token, got)
			}
		}
	}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`
	initialized := mcpRequest(http.MethodPost, created.Secret, initBody, "", "")
	if initialized.Code != http.StatusOK {
		t.Fatalf("MCP initialize status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	var initializeResult struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initialized.Body.Bytes(), &initializeResult); err != nil {
		t.Fatalf("MCP initialize response: %v: %s", err, initialized.Body.String())
	}
	if initializeResult.Result.ProtocolVersion == "" {
		t.Fatalf("MCP initialize omitted negotiated protocol version: %s", initialized.Body.String())
	}
	listed := mcpRequest(http.MethodPost, created.Secret, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "", "fixture-session")
	if listed.Code != http.StatusOK {
		t.Fatalf("MCP tools/list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var toolsResult struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &toolsResult); err != nil {
		t.Fatalf("MCP tools/list response: %v: %s", err, listed.Body.String())
	}
	if len(toolsResult.Result.Tools) != 1 || toolsResult.Result.Tools[0].Name != "query_project" {
		t.Fatalf("MCP exposed tools outside query-only contract: %+v", toolsResult.Result.Tools)
	}
	toolCall := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query_project","arguments":{"q":"coffee"}}}`
	called := mcpRequest(http.MethodPost, created.Secret, toolCall, "", "fixture-session")
	if called.Code != http.StatusOK || strings.Contains(called.Body.String(), `"isError":true`) || !strings.Contains(called.Body.String(), `"query":"coffee"`) {
		t.Fatalf("MCP query tool call status=%d body=%s", called.Code, called.Body.String())
	}
	if queryCount != 5 || len(queryPrefixes) != 5 || !strings.Contains(queryPrefixes[4], projectID) {
		t.Fatalf("first MCP call used wrong scope/count: calls=%d prefixes=%v", queryCount, queryPrefixes)
	}
	otherProject := "archive"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID+"_"+otherProject).Set(ctx, map[string]any{"user_id": userID, "project_id": otherProject, "name": "Archive"}); err != nil {
		t.Fatal(err)
	}
	_, otherProjectSecret, _, err := auth.NewProjectKeyService(fsClient.Raw()).Create(ctx, userID, otherProject, "Archive reader")
	if err != nil {
		t.Fatal(err)
	}
	crossKeyCall := mcpRequest(http.MethodPost, otherProjectSecret, toolCall, "", "fixture-session")
	if crossKeyCall.Code != http.StatusOK || strings.Contains(crossKeyCall.Body.String(), `"isError":true`) || !strings.Contains(crossKeyCall.Body.String(), `"query":"coffee"`) {
		t.Fatalf("MCP second-key tool call status=%d body=%s", crossKeyCall.Code, crossKeyCall.Body.String())
	}
	if queryCount != 6 || len(queryPrefixes) != 6 || !strings.Contains(queryPrefixes[5], otherProject) || strings.Contains(queryPrefixes[5], projectID) {
		t.Fatalf("MCP reused prior key scope: calls=%d prefixes=%v", queryCount, queryPrefixes)
	}
	if got := mcpRequest(http.MethodPost, otherProjectSecret, toolCall, projectID, "fixture-session").Code; got != http.StatusForbidden {
		t.Fatalf("MCP mismatched X-Project-ID status=%d, want 403", got)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if got := mcpRequest(method, created.Secret, "", "", "fixture-session").Code; got != http.StatusMethodNotAllowed {
			t.Fatalf("MCP authenticated %s status=%d, want stateless 405", method, got)
		}
	}

	revokePath := "/api/v1/projects/" + projectID + "/keys/" + created.Key.KeyID + "/revoke"
	for i := 0; i < 2; i++ {
		revoked := request(http.MethodPost, revokePath, webToken, "", "")
		if revoked.Code != http.StatusOK || !strings.Contains(revoked.Body.String(), `"state":"revoked"`) {
			t.Fatalf("revoke attempt %d status=%d body=%s", i+1, revoked.Code, revoked.Body.String())
		}
	}
	if got := keyQuery("").Code; got != http.StatusUnauthorized {
		t.Fatalf("revoked key query status=%d, want 401", got)
	}
	if got := mcpRequest(http.MethodPost, created.Secret, toolCall, "", "fixture-session").Code; got != http.StatusUnauthorized {
		t.Fatalf("revoked key reused an MCP session: status=%d, want 401", got)
	}

	_, resumedSecret, _, err := auth.NewProjectKeyService(fsClient.Raw()).Create(ctx, userID, projectID, "authority-check")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "users").Doc(userID).Update(ctx, []cloudfirestore.Update{{Path: "status", Value: auth.AccountSuspended}}); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, "/api/v1/query", resumedSecret, `{"q":"coffee","mode":"wiki"}`, "").Code; got != http.StatusForbidden {
		t.Fatalf("suspended-account query status=%d, want 403", got)
	}
	if got := mcpRequest(http.MethodPost, otherProjectSecret, toolCall, "", "fixture-session").Code; got != http.StatusForbidden {
		t.Fatalf("suspended-account MCP status=%d, want 403", got)
	}

	if _, err := projectRef.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "users").Doc(userID).Update(ctx, []cloudfirestore.Update{{Path: "status", Value: auth.AccountActive}}); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, "/api/v1/query", resumedSecret, `{"q":"coffee","mode":"wiki"}`, "").Code; got != http.StatusForbidden {
		t.Fatalf("lost-owner query status=%d, want 403", got)
	}
}

func loopbackFirestoreEmulator(t *testing.T) bool {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		return false
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("project-key test requires a loopback Firestore emulator")
	}
	return true
}
