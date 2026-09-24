package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func TestProductionRouterKeepsAuthCompatibilityLane(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newProductionRouter(
		config.Config{DevJWT: true, JWTSecret: "test-secret"},
		true,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		if !hasRoute(router, http.MethodPost, path) {
			t.Fatalf("BFF production router is missing compatibility route POST %s", path)
		}
	}

	request := func(path string, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"demo123456"}`).Code; got != http.StatusOK {
		t.Fatalf("local compatibility login status = %d, want %d", got, http.StatusOK)
	}
	if got := request("/api/v1/auth/register", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility register status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/refresh", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("local compatibility refresh status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := request("/api/v1/auth/logout", "").Code; got != http.StatusOK {
		t.Fatalf("local compatibility logout status = %d, want %d", got, http.StatusOK)
	}
	if got := request("/api/v1/auth/login", `{}`).Code; got != http.StatusBadRequest {
		t.Fatalf("small malformed local compatibility login status = %d, want %d", got, http.StatusBadRequest)
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"`+strings.Repeat("x", 64<<10)+`"}`).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized local compatibility login status = %d, want %d", got, http.StatusRequestEntityTooLarge)
	}

	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("CF-Connecting-IP", "203.0.113.258")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if i == 10 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("local compatibility login request %d status = %d, want %d", i+1, rec.Code, http.StatusTooManyRequests)
		}
	}

	unavailable := newProductionRouter(
		config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}},
		false,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/refresh"} {
		if got := servePost(unavailable, path).Code; got != http.StatusServiceUnavailable {
			t.Fatalf("unavailable compatibility %s status = %d, want %d", path, got, http.StatusServiceUnavailable)
		}
	}
	logout := servePost(unavailable, "/api/v1/auth/logout")
	if logout.Code != http.StatusOK {
		t.Fatalf("unavailable compatibility logout status = %d, want %d", logout.Code, http.StatusOK)
	}
	cookies := logout.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("legacy compatibility logout returned %d cookies, want one", len(cookies))
	}
	if cookies[0].Name != "refresh_token" || cookies[0].Domain != "rayer.idv.tw" || cookies[0].Path != "/" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != -1 {
		t.Fatalf("legacy compatibility logout cookie attributes: count=%d name=%q domain=%q path=%q secure=%v httpOnly=%v sameSite=%v maxAge=%d", len(cookies), cookies[0].Name, cookies[0].Domain, cookies[0].Path, cookies[0].Secure, cookies[0].HttpOnly, cookies[0].SameSite, cookies[0].MaxAge)
	}

	for _, path := range []string{"/api/v1/public/config", "/api/v1/public/version", "/api/v1/query/config"} {
		if !hasRoute(router, http.MethodGet, path) {
			t.Fatalf("BFF production router is missing GET %s", path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/exports"},
		{http.MethodGet, "/api/v1/exports"},
		{http.MethodGet, "/api/v1/exports/:exportID/status"},
		{http.MethodPost, "/api/v1/exports/:exportID/download"},
	} {
		if !hasRoute(router, route.method, route.path) {
			t.Fatalf("BFF router is missing export route %s %s", route.method, route.path)
		}
	}
	if got := serveGet(router, "/api/v1/query/config").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("legacy public query config status=%d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestProductionRouterUsesSharedCLIAndWebAuthAuthorities(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("local Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("production router auth test requires a loopback Firestore emulator")
	}
	gin.SetMode(gin.TestMode)
	projectName := fmt.Sprintf("demo-lwc346-router-%d", time.Now().UnixNano())
	fsClient, err := firestore.NewClientWithDatabase(projectName, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsClient.Raw().Close() })
	ctx := context.Background()
	userID := fmt.Sprintf("routeruser%d", time.Now().UnixNano())
	projectID := "owned"
	if _, err := fsClient.Raw().Collection("users").Doc(userID).Set(ctx, map[string]any{
		"status": auth.AccountActive, "auth_version": int64(0), "role": "member",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fsClient.Raw().Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "name": "owned project",
	}); err != nil {
		t.Fatal(err)
	}
	const jwtSecret, environment = "lwc346-router-secret", "lwc346-router-emulator"
	sessions := auth.NewRefreshSessionAuthority(fsClient.Raw(), environment)
	cli, err := sessions.IssueCLISession(ctx, userID, "test client", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	web, err := auth.GenerateToken(userID, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{JWTSecret: jwtSecret, AuthSessionEnvironment: environment, AllowedOrigins: []string{"https://wiki.example.test"}}
	router := newProductionRouter(cfg, false, nil, fsClient, handlerv1.New(nil, fsClient, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)
	request := func(token, pid string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Project-ID", pid)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if got := request(cli.AccessToken, projectID); got != http.StatusOK {
		t.Fatalf("valid CLI request status=%d, want %d", got, http.StatusOK)
	}
	if got := request(cli.AccessToken, "not-owned"); got != http.StatusForbidden {
		t.Fatalf("CLI request for unowned project status=%d, want %d", got, http.StatusForbidden)
	}
	if err := sessions.RevokeCLISession(ctx, userID, cli.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := request(cli.AccessToken, projectID); got != http.StatusUnauthorized {
		t.Fatalf("revoked CLI access status=%d, want %d", got, http.StatusUnauthorized)
	}
	if got := request(web, "not-owned"); got != http.StatusOK {
		t.Fatalf("valid Web request changed by CLI authority wiring: status=%d, want %d", got, http.StatusOK)
	}
}

func servePost(router *gin.Engine, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
	return recorder
}

func serveGet(router *gin.Engine, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func hasRoute(router *gin.Engine, method, path string) bool {
	for _, route := range router.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
