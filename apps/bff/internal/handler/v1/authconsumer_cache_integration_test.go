package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
)

func TestSharedVerifierCacheAcrossAuthenticatedAPIHandlersKeepsProjectAuthorization(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })
	var upstreamCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/api/v1/auth/consumers/lwc/verify-credential" || r.Header.Get("X-LWC-User-Credential") != "cli-session" {
			t.Errorf("unexpected verifier request path=%q credential=%q", r.URL.Path, r.Header.Get("X-LWC-User-Credential"))
		}
		var request struct {
			SchemaVersion int    `json:"schema_version"`
			Resource      string `json:"resource"`
			ProjectID     string `json:"project_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode verifier request: %v", err)
		}
		if request.SchemaVersion != 1 || request.Resource != "https://bff.example.test" || request.ProjectID == "" {
			t.Errorf("request was not bound to the selected API Project: %+v", request)
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(authconsumer.Identity{
			SchemaVersion: 1, Subject: "user-a", CredentialKind: "cli-access", Environment: "dev",
			Issuer: "https://auth.example.test", Resource: request.Resource, ExpiresAt: time.Now().Add(time.Minute).Unix(),
			AuthVersion: 1, Scopes: []string{}, ManagementScopes: []string{}, SessionID: "session-a",
		})
	}))
	defer server.Close()
	client, err := authconsumer.New(authconsumer.Config{BaseURL: server.URL, Environment: "dev", Issuer: "https://auth.example.test",
		Resource: "https://bff.example.test", Audience: "https://auth-runtime.example.test", Timeout: 5 * time.Second}, nil,
		func(context.Context, string) (string, error) { return "runtime", nil })
	if err != nil {
		t.Fatal(err)
	}
	handler := New(nil, nil, nil, nil, nil, nil)
	handler.SetConsumerVerifier(client)
	var projectChecks atomic.Int64
	handler.SetCLIProjectAuthorizer(func(_ context.Context, subject, projectID string) error {
		projectChecks.Add(1)
		if subject != "user-a" || (projectID != "project-a" && projectID != "project-b") {
			t.Errorf("project authorization received subject=%q project=%q", subject, projectID)
		}
		return nil
	})
	router := gin.New()
	guard := handler.consumerAuthMiddleware()
	endpoint := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user": c.GetString("userID"), "kind": c.GetString("clientKind")})
	}
	router.GET("/api/v1/concepts", guard, endpoint)
	router.GET("/api/v1/sources", guard, endpoint)

	for _, request := range []struct{ path, project string }{
		{path: "/api/v1/concepts", project: "project-a"},
		{path: "/api/v1/sources", project: "project-a"},
		{path: "/api/v1/sources", project: "project-b"},
	} {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, request.path, nil)
		req.Header.Set("Authorization", "Bearer cli-session")
		req.Header.Set("X-Project-ID", request.project)
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Body.String() == "" {
			t.Fatalf("%s Project=%s returned status=%d body=%s", request.path, request.project, response.Code, response.Body.String())
		}
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("actual Auth verifier calls=%d, want one shared hit for project-a and one isolated project-b verification", upstreamCalls.Load())
	}
	if projectChecks.Load() != 3 {
		t.Fatalf("per-request CLI Project authorization calls=%d, want 3", projectChecks.Load())
	}
}
