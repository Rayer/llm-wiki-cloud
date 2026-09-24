package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
)

func TestAccountAuthRejectsRevokedCLIAndDoesNotReachProjectHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Role: "member"}, nil
	})
	projectChecked := false
	h.SetCLISessionVerifier(func(context.Context, string, string, int64) error { return auth.ErrCLISessionRevoked })
	h.SetCLIProjectAuthorizer(func(context.Context, string, string) error { projectChecked = true; return nil })
	token, err := auth.GenerateCLIAccessToken("owner", "member", "cli-session", "test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/api/v1/projects", h.AccountAuth(config.Config{JWTSecret: "test-secret"}), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := performCLIBFFRequest(router, token, "project-1")
	if recorder.Code != http.StatusUnauthorized || projectChecked {
		t.Fatalf("revoked CLI request status=%d project_checked=%v", recorder.Code, projectChecked)
	}
}

func TestAccountAuthChecksCLISessionWithoutProjectAndOwnerWithProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Role: "member", AuthVersion: 2}, nil
	})
	sessionChecks, projectChecks := 0, 0
	h.SetCLISessionVerifier(func(_ context.Context, userID, sessionID string, version int64) error {
		sessionChecks++
		if userID != "owner" || sessionID != "cli-session" || version != 2 {
			t.Fatalf("session verifier args=(%q,%q,%d)", userID, sessionID, version)
		}
		return nil
	})
	h.SetCLIProjectAuthorizer(func(_ context.Context, userID, projectID string) error {
		projectChecks++
		if userID != "owner" || projectID != "project-1" {
			t.Fatalf("project authorizer args=(%q,%q)", userID, projectID)
		}
		return nil
	})
	token, err := auth.GenerateCLIAccessToken("owner", "member", "cli-session", "test-secret", 2)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	var contextSessionID string
	router.GET("/api/v1/projects", h.AccountAuth(config.Config{JWTSecret: "test-secret"}), func(c *gin.Context) {
		contextSessionID = c.GetString("sessionID")
		c.Status(http.StatusNoContent)
	})
	if recorder := performCLIBFFRequest(router, token, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("CLI list without project status=%d", recorder.Code)
	}
	if sessionChecks != 1 || projectChecks != 0 {
		t.Fatalf("without project: session checks=%d project checks=%d", sessionChecks, projectChecks)
	}
	if contextSessionID != "cli-session" {
		t.Fatalf("request session context=%q, want cli-session", contextSessionID)
	}
	if recorder := performCLIBFFRequest(router, token, "project-1"); recorder.Code != http.StatusNoContent {
		t.Fatalf("CLI project request status=%d", recorder.Code)
	}
	if sessionChecks != 2 || projectChecks != 1 {
		t.Fatalf("with project: session checks=%d project checks=%d", sessionChecks, projectChecks)
	}
}

func TestAccountAuthPreservesWebAndFailsClosedWithoutCLIAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) { return &auth.UserRecord{Role: "member"}, nil })
	webToken, err := auth.GenerateAccessToken("owner", "member", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	cliToken, err := auth.GenerateCLIAccessToken("owner", "member", "cli-session", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/api/v1/projects", h.AccountAuth(config.Config{JWTSecret: "test-secret"}), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	if recorder := performCLIBFFRequest(router, webToken, "project-1"); recorder.Code != http.StatusNoContent {
		t.Fatalf("existing Web token status=%d", recorder.Code)
	}
	if recorder := performCLIBFFRequest(router, cliToken, ""); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("CLI token without verifier status=%d, want fail-closed 503", recorder.Code)
	}
	if recorder := performCLIBFFRequest(router, cliToken, "project-1"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("CLI token without project authority status=%d, want fail-closed 503", recorder.Code)
	}
}

func performCLIBFFRequest(router http.Handler, token, projectID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if projectID != "" {
		request.Header.Set("X-Project-ID", projectID)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}
