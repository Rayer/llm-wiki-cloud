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
		config.Config{JWTSecret: "test-secret"},
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
		req := httptest.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"demo123456"}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility login without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	badHost := httptest.NewRequest(http.MethodPost, "http://example.test/api/v1/auth/login", strings.NewReader(`{}`))
	badHostRecorder := httptest.NewRecorder()
	router.ServeHTTP(badHostRecorder, badHost)
	if badHostRecorder.Code != http.StatusBadRequest {
		t.Fatalf("local compatibility auth accepted Host %q: status=%d, want 400", badHost.Host, badHostRecorder.Code)
	}
	if got := request("/api/v1/auth/register", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility register status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/refresh", "").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility refresh without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/logout", "").Code; got != http.StatusOK {
		t.Fatalf("local compatibility logout status = %d, want %d", got, http.StatusOK)
	}
	if got := request("/api/v1/auth/login", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility login without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"`+strings.Repeat("x", 64<<10)+`"}`).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized local compatibility login status = %d, want %d", got, http.StatusRequestEntityTooLarge)
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
		{http.MethodGet, "/api/v1/projects/:pid/profile"},
		{http.MethodPut, "/api/v1/projects/:pid/profile"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/candidates/:candidateID/confirm"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/candidates/:candidateID/retry"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/derivation/retry"},
		{http.MethodGet, "/api/v1/projects/:pid/profile/jobs/:jobID"},
	} {
		if !hasRoute(router, route.method, route.path) {
			t.Fatalf("BFF production router is missing %s %s", route.method, route.path)
		}
	}
	if got := serveGet(router, "/api/v1/query/config").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("legacy public query config status=%d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestTrialDemoRestrictionCoversProfileAndExportForExactUserIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerv1.New(nil, nil, nil, nil, nil, nil)
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{"trial-user"})
	h.SetAccountLookup(func(_ context.Context, _ string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Role: "member"}, nil
	})
	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret"}, true, nil, nil, h,
		&syssettings.FakeStore{Enabled: true}, nil,
	)
	request := func(userID, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		token, err := auth.GenerateAccessToken(userID, "member", "test-secret")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Project-ID", "project-a")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/project-a/profile"},
		{http.MethodPost, "/api/v1/projects/project-a/profile/candidates/candidate-a/confirm"},
		{http.MethodGet, "/api/v1/exports"},
		{http.MethodPost, "/api/v1/exports"},
	} {
		if got := request("trial-user", route.method, route.path).Code; got != http.StatusForbidden {
			t.Errorf("trial user %s %s status = %d, want %d", route.method, route.path, got, http.StatusForbidden)
		}
	}

	if got := request("non-listed-user", http.MethodGet, "/api/v1/projects/project-a/profile").Code; got == http.StatusForbidden {
		t.Fatal("non-listed user was blocked from Profile")
	}
	if got := request("non-listed-user", http.MethodGet, "/api/v1/exports").Code; got == http.StatusForbidden {
		t.Fatal("non-listed user was blocked from Export")
	}
}

func TestProfileSavePreflightAllowsFrontendPutOnlyFromConfiguredOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const allowedOrigin = "https://wiki.dev.rayer.idv.tw"
	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{allowedOrigin}},
		false,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)
	preflight := func(origin string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects/project-1/profile", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-project-id")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	allowed := preflight(allowedOrigin)
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("allowed Profile PUT preflight status = %d, want %d", allowed.Code, http.StatusNoContent)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	methods := strings.Split(allowed.Header().Get("Access-Control-Allow-Methods"), ",")
	if !containsFold(methods, http.MethodPut) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PUT", allowed.Header().Get("Access-Control-Allow-Methods"))
	}
	for _, name := range []string{"authorization", "content-type", "x-project-id"} {
		if !containsFold(strings.Split(allowed.Header().Get("Access-Control-Allow-Headers"), ","), name) {
			t.Errorf("Access-Control-Allow-Headers = %q, missing %s", allowed.Header().Get("Access-Control-Allow-Headers"), name)
		}
	}

	rejected := preflight("https://unlisted.example")
	if got := rejected.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin received Access-Control-Allow-Origin %q", got)
	}
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
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
