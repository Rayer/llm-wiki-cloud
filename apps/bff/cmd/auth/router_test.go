package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	firestoreclient "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func TestProductionRouterExposesOnlyAuthPublicSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true})

	want := map[string]bool{
		http.MethodPost + " /api/v1/auth/register": true,
		http.MethodPost + " /api/v1/auth/login":    true,
		http.MethodPost + " /api/v1/auth/refresh":  true,
		http.MethodPost + " /api/v1/auth/logout":   true,
		http.MethodGet + " /api/v1/public/healthz": true,
		http.MethodGet + " /api/v1/public/version": true,
	}

	got := make(map[string]bool)
	for _, route := range router.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	if len(got) != len(want) {
		t.Fatalf("Auth production routes = %#v, want exactly %#v", got, want)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("Auth production router is missing %s", route)
		}
	}
	for route := range got {
		if !want[route] {
			t.Errorf("Auth production router exposed unapproved route %s", route)
		}
	}
}

func TestProductionRouterWiresGoogleAuthRoutesWhenConfigured(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	client, err := firestoreclient.NewClientWithDatabase("lwc-315-test", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
		GoogleClientID: "client", GoogleClientSecret: "secret", GoogleIssuer: "https://accounts.google.com",
		GoogleJWKSURL: "https://www.googleapis.com/oauth2/v3/certs", GoogleTokenURL: "https://oauth2.googleapis.com/token",
		GoogleLoginRedirectURL: "https://auth.example.test/api/v1/auth/google/login/callback",
		GoogleLinkRedirectURL:  "https://auth.example.test/api/v1/auth/google/link/callback",
		GoogleCompletionURL:    "https://frontend.example/login",
	}, false, client, &syssettings.FakeStore{Enabled: true})
	want := map[string]bool{
		"POST /api/v1/auth/google/start": true, "GET /api/v1/auth/google/start": true,
		"POST /api/v1/auth/google/login/start": true, "GET /api/v1/auth/google/login/start": true, "POST /api/v1/auth/google/link/start": true,
		"GET /api/v1/auth/google/callback": true, "GET /api/v1/auth/google/login/callback": true,
		"GET /api/v1/auth/google/link/callback": true, "POST /api/v1/auth/google/link/confirm": true,
		"POST /api/v1/auth/google/link/cancel": true, "GET /api/v1/auth/google/link/complete": true,
		"GET /api/v1/auth/google/complete": true, "GET /api/v1/auth/google/identity": true,
	}
	got := make(map[string]bool)
	for _, route := range router.Routes() {
		if strings.Contains(route.Path, "/google/") || strings.HasSuffix(route.Path, "/google/start") {
			got[route.Method+" "+route.Path] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Google auth routes=%#v want=%#v", got, want)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("missing Google auth route %s", route)
		}
	}
	// This is the consumer shape of Continue with Google: a normal browser
	// navigation uses GET and must reach the registered login-start handler.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://auth.example.test/api/v1/auth/google/login/start", nil)
	request.Header.Set("Origin", "https://frontend.example")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound || !strings.HasPrefix(recorder.Header().Get("Location"), "https://accounts.google.com/") {
		t.Fatalf("browser login start status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
}

func TestProductionRouterWiresLocalAuthHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", bytes.NewBufferString(`{"email":"demo@llm-wiki.dev","password":"demo123456"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("local login status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var loginResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode local login response: %v", err)
	}
	if loginResponse.AccessToken == "" {
		t.Fatal("local login response did not contain an access token")
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/register", bytes.NewBufferString(`{"email":"new@example.com","password":"password123"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("local register status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestProductionRouterUsesHostOnlyRefreshCookiePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/logout", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("deployed logout status = %d, want %d", recorder.Code, http.StatusOK)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("deployed logout returned %d cookies, want one", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-lwc_refresh" || cookie.Domain != "" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != -1 {
		t.Fatalf("deployed logout cookie attributes: name=%q domain=%q path=%q secure=%v httpOnly=%v sameSite=%v maxAge=%d", cookie.Name, cookie.Domain, cookie.Path, cookie.Secure, cookie.HttpOnly, cookie.SameSite, cookie.MaxAge)
	}
}

func TestProductionRouterUsesDurableRefreshAuthorityAcrossRouterInstances(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	gin.SetMode(gin.TestMode)
	client, err := firestoreclient.NewClientWithDatabase("lwc-320-router", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	repo := auth.NewIdentityRepository(client.Raw())
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("passphrase-320"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("router-durable-user-320-%d", suffix)
	email := fmt.Sprintf("router-durable-320-%d@example.test", suffix)
	if err := repo.ProvisionPasswordUser(t.Context(), auth.PasswordUserProvisioning{
		UserID: userID, DisplayEmail: email, CanonicalEmail: email,
		PasswordHash: string(passwordHash), ProjectID: "default",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{JWTSecret: "router-key-320", FirestoreDatabaseID: "lwc-320-router", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}}
	first := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true})
	loginRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"passphrase-320"}`, email)))
	loginRequest.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	first.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("durable login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-lwc_refresh" || cookies[0].Value == "" {
		t.Fatalf("durable login cookies=%#v", cookies)
	}

	second := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true})
	refreshRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/refresh", nil)
	refreshRequest.AddCookie(cookies[0])
	refresh := httptest.NewRecorder()
	second.ServeHTTP(refresh, refreshRequest)
	if refresh.Code != http.StatusOK {
		t.Fatalf("durable refresh status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var rotated *http.Cookie
	for _, cookie := range refresh.Result().Cookies() {
		if cookie.Name == "__Host-lwc_refresh" {
			rotated = cookie
			break
		}
	}
	if rotated == nil || rotated.Value == cookies[0].Value {
		t.Fatalf("durable refresh cookie=%#v, want rotated cookie", rotated)
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(rotated)
	logout := httptest.NewRecorder()
	second.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("durable logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	revokedRefresh := httptest.NewRecorder()
	refreshRequest = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/refresh", nil)
	refreshRequest.AddCookie(rotated)
	second.ServeHTTP(revokedRefresh, refreshRequest)
	if revokedRefresh.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after durable logout status=%d body=%s", revokedRefresh.Code, revokedRefresh.Body.String())
	}
}

func TestAuthHostAllowlistRejectsBeforeRouteHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		DevJWT:         true,
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	for _, rawURL := range []string{"http://wrong.example.test/api/v1/public/healthz", "http://auth.example.test./api/v1/public/healthz"} {
		recorder = httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, rawURL, nil)
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("wrong Host %q status = %d, want %d", rawURL, recorder.Code, http.StatusBadRequest)
		}
		if strings.Contains(recorder.Body.String(), "listening") {
			t.Fatalf("wrong Host %q reached route handling", rawURL)
		}
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://auth.example.test/api/v1/public/healthz", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed Host status = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "http://auth.example.test/healthz", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("former public health path status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAuthLocalHostAllowlistAcceptsLocalhostOnlyInLocalMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localRouter := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true})
	for _, host := range []string{"localhost:8081", "127.0.0.1:8081"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v1/public/healthz", nil)
		localRouter.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("local Host %q status = %d, want %d", host, recorder.Code, http.StatusOK)
		}
	}

	productionRouter := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}}, false, nil, &syssettings.FakeStore{Enabled: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/public/healthz", nil)
	productionRouter.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("production localhost status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAuthCORSAllowsOnlyBaselineMethodsAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		DevJWT:         true,
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "http://auth.example.test/api/v1/auth/login", nil)
	request.Header.Set("Origin", "https://frontend.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("CORS preflight status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "GET,POST,OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "GET,POST,OPTIONS")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type,Authorization" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type,Authorization")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://frontend.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

func TestAuthRequestBodyLimitRejectsOversizedLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", strings.NewReader(`{"email":"demo@llm-wiki.dev","password":"`+strings.Repeat("x", 64<<10)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized login status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAuthRequestBodyLimitPreservesSmallMalformedLoginStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("small malformed login status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
