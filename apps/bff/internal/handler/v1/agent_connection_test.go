package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestAgentConnectionOwnerEndpointsCreateReadReplaceAndScopeEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		return
	}
	project := fmt.Sprintf("agent-connection-api-%d", time.Now().UnixNano())
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer fsClient.Close()
	owner, projectID := "connection-owner", fmt.Sprintf("wiki-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": owner, "project_id": projectID, "status": "ready", "name": "Lifestyle Wiki"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	currentRef := projectRef.Collection("agent_connection").Doc("current")
	defer func() {
		_, _ = currentRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()
	h := New(nil, fsClient, nil, nil, nil, nil)

	created := serveAgentConnectionAPI(t, h, http.MethodPost, projectID, projectID, "")
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d cache=%q body=%s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	first := decodeAgentConnectionResponse(t, created)
	if first.ProjectID != projectID || first.Status != "waiting" || first.SetupID == "" ||
		len(first.ConfirmationToken) != 43 || first.CreatedAt == nil || first.ExpiresAt == nil || first.ConfirmedAt != nil {
		t.Fatalf("created setup has invalid receipt shape: %+v", first)
	}
	if delta := first.ExpiresAt.Sub(*first.CreatedAt); delta != agentConnectionSetupTTL {
		t.Fatalf("setup TTL=%s want %s", delta, agentConnectionSetupTTL)
	}

	read := serveAgentConnectionAPI(t, h, http.MethodGet, projectID, projectID, "")
	current := decodeAgentConnectionResponse(t, read)
	if read.Code != http.StatusOK || read.Header().Get("Cache-Control") != "no-store" || current.SetupID != first.SetupID ||
		current.Status != "waiting" || current.ConfirmationToken != first.ConfirmationToken {
		t.Fatalf("waiting readback status=%d cache=%q receipt=%+v", read.Code, read.Header().Get("Cache-Control"), current)
	}
	if mismatch := serveAgentConnectionAPI(t, h, http.MethodGet, projectID, "other-project", ""); mismatch.Code != http.StatusBadRequest {
		t.Fatalf("route/header project mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	if nonempty := serveAgentConnectionAPI(t, h, http.MethodPost, projectID, projectID, "{}"); nonempty.Code != http.StatusBadRequest {
		t.Fatalf("nonempty create body status=%d body=%s", nonempty.Code, nonempty.Body.String())
	}

	secondRecorder := serveAgentConnectionAPI(t, h, http.MethodPost, projectID, projectID, "")
	second := decodeAgentConnectionResponse(t, secondRecorder)
	if secondRecorder.Code != http.StatusCreated || second.SetupID == first.SetupID || second.ConfirmationToken == first.ConfirmationToken {
		t.Fatalf("replacement did not create a fresh setup: first=%+v second=%+v", first, second)
	}
	read = serveAgentConnectionAPI(t, h, http.MethodGet, projectID, projectID, "")
	current = decodeAgentConnectionResponse(t, read)
	if current.SetupID != second.SetupID || current.ConfirmationToken != second.ConfirmationToken {
		t.Fatalf("current read did not return replacement: %+v", current)
	}
}

func TestAgentConnectionActualMCPSDKRejectsWrongCrossProjectExpiredAndReplacedChallengesEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		return
	}
	project := fmt.Sprintf("agent-connection-mcp-%d", time.Now().UnixNano())
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer fsClient.Close()
	owner := "connection-owner"
	projectA, projectB := fmt.Sprintf("wiki-a-%d", time.Now().UnixNano()), fmt.Sprintf("wiki-b-%d", time.Now().UnixNano())
	projectARef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectA))
	projectBRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectB))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, item := range []struct {
		ref       *cloudfirestore.DocumentRef
		projectID string
	}{{projectARef, projectA}, {projectBRef, projectB}} {
		if _, err := item.ref.Set(ctx, map[string]any{"user_id": owner, "project_id": item.projectID, "status": "ready", "name": "Lifestyle Wiki"}); err != nil {
			t.Fatalf("seed Project %s: %v", item.projectID, err)
		}
	}
	currentA := projectARef.Collection("agent_connection").Doc("current")
	defer func() {
		_, _ = currentA.Delete(context.Background())
		_, _ = projectARef.Delete(context.Background())
		_, _ = projectBRef.Collection("agent_connection").Doc("current").Delete(context.Background())
		_, _ = projectBRef.Delete(context.Background())
	}()
	h := New(nil, fsClient, nil, nil, nil, nil)
	first := decodeAgentConnectionResponse(t, serveAgentConnectionAPI(t, h, http.MethodPost, projectA, projectA, ""))
	if first.SetupID == "" || first.ConfirmationToken == "" {
		t.Fatalf("create first challenge returned incomplete receipt: %+v", first)
	}

	identityA := mcpProjectKeyIdentity{userID: owner, projectID: projectA, keyID: "query-only-a", capabilities: []string{mcpQueryCapability}, revalidate: func(context.Context) error { return nil }}
	sessionA, closeA := connectAgentConnectionSDK(t, h, identityA)
	defer closeA()
	tools, err := sessionA.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("SDK tools/list: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["confirm_connection"] || names["add_raw"] {
		t.Fatalf("query-only SDK tools include unexpected connection/write capabilities: %v", names)
	}

	wrong, err := sessionA.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: map[string]any{
		"setup_id": first.SetupID, "confirmation_token": strings.Repeat("x", 43),
	}})
	if err != nil || wrong == nil || !wrong.IsError || mcpErrorText(wrong) != "invalid_setup" {
		t.Fatalf("wrong-token tool call result=%+v err=%v", wrong, err)
	}

	identityB := mcpProjectKeyIdentity{userID: owner, projectID: projectB, keyID: "query-only-b", capabilities: []string{mcpQueryCapability}, revalidate: func(context.Context) error { return nil }}
	sessionB, closeB := connectAgentConnectionSDK(t, h, identityB)
	defer closeB()
	crossProject, err := sessionB.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: map[string]any{
		"setup_id": first.SetupID, "confirmation_token": first.ConfirmationToken,
	}})
	if err != nil || crossProject == nil || !crossProject.IsError || mcpErrorText(crossProject) != "invalid_setup" {
		t.Fatalf("cross-Project tool call result=%+v err=%v", crossProject, err)
	}

	if _, err := currentA.Update(ctx, []cloudfirestore.Update{{Path: "expires_at", Value: time.Now().UTC().Add(-time.Minute)}}); err != nil {
		t.Fatalf("expire first challenge: %v", err)
	}
	expired, err := sessionA.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: map[string]any{
		"setup_id": first.SetupID, "confirmation_token": first.ConfirmationToken,
	}})
	if err != nil || expired == nil || !expired.IsError || mcpErrorText(expired) != "invalid_setup" {
		t.Fatalf("expired challenge tool call result=%+v err=%v", expired, err)
	}

	second := decodeAgentConnectionResponse(t, serveAgentConnectionAPI(t, h, http.MethodPost, projectA, projectA, ""))
	replaced, err := sessionA.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: map[string]any{
		"setup_id": first.SetupID, "confirmation_token": first.ConfirmationToken,
	}})
	if err != nil || replaced == nil || !replaced.IsError || mcpErrorText(replaced) != "invalid_setup" {
		t.Fatalf("replaced challenge tool call result=%+v err=%v", replaced, err)
	}

	args := map[string]any{"setup_id": second.SetupID, "confirmation_token": second.ConfirmationToken}
	confirmed, err := sessionA.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: args})
	if err != nil || confirmed == nil || confirmed.IsError {
		t.Fatalf("current challenge confirmation result=%+v err=%v", confirmed, err)
	}
	receipt := decodeAgentMCPReceipt(t, confirmed)
	if receipt["setup_id"] != second.SetupID || receipt["project_id"] != projectA || receipt["status"] != "confirmed" || receipt["confirmed_at"] == nil {
		t.Fatalf("confirmed receipt does not identify only the current setup: %#v", receipt)
	}
	if _, err := currentA.Update(ctx, []cloudfirestore.Update{{Path: "expires_at", Value: time.Now().UTC().Add(-time.Minute)}}); err != nil {
		t.Fatalf("expire already confirmed setup for replay check: %v", err)
	}
	replay, err := sessionA.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_connection", Arguments: args})
	if err != nil || replay == nil || replay.IsError {
		t.Fatalf("confirmed replay result=%+v err=%v", replay, err)
	}
	replayReceipt := decodeAgentMCPReceipt(t, replay)
	if replayReceipt["confirmed_at"] != receipt["confirmed_at"] {
		t.Fatalf("idempotent replay changed confirmation time: first=%#v replay=%#v", receipt, replayReceipt)
	}
	read := decodeAgentConnectionResponse(t, serveAgentConnectionAPI(t, h, http.MethodGet, projectA, projectA, ""))
	if read.Status != "confirmed" || read.SetupID != second.SetupID || read.ConfirmationToken != "" || read.ConfirmedAt == nil {
		t.Fatalf("owner readback did not return the same secret-free receipt: %+v", read)
	}
}

func serveAgentConnectionAPI(t *testing.T, h *Handler, method, pathProject, scopeProject, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, "/projects/:pid/agent-connection-check", func(c *gin.Context) {
		c.Set("userID", "connection-owner")
		c.Set("projectID", scopeProject)
		if method == http.MethodPost {
			h.CreateAgentConnectionCheck(c)
		} else {
			h.GetAgentConnectionCheck(c)
		}
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/projects/"+pathProject+"/agent-connection-check", strings.NewReader(body))
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeAgentConnectionResponse(t *testing.T, recorder *httptest.ResponseRecorder) agentConnectionSetupResponse {
	t.Helper()
	var result agentConnectionSetupResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Agent connection response status=%d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}
	return result
}

func connectAgentConnectionSDK(t *testing.T, h *Handler, identity mcpProjectKeyIdentity) (*mcp.ClientSession, func()) {
	t.Helper()
	server := httptest.NewServer(h.mcpAuthenticatedHandler(identity))
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-connection-test-client", Version: "test"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: &http.Client{Transport: mcpBearerTestTransport{next: http.DefaultTransport}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("connect MCP SDK client: %v", err)
	}
	return session, func() {
		_ = session.Close()
		cancel()
		server.Close()
	}
}

func mcpErrorText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func decodeAgentMCPReceipt(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("encode Agent MCP receipt: %v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatalf("decode Agent MCP receipt %s: %v", encoded, err)
	}
	return receipt
}
