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

func TestProductionRouterFailsClosedAndRegistersCLIControlPlanePaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
	}, false, nil, &syssettings.FakeStore{Enabled: true})
	want := []string{
		"POST /api/v1/auth/cli/pairing/start",
		"POST /api/v1/auth/cli/pairing/poll",
		"POST /api/v1/auth/cli/pairing/decision",
		"POST /api/v1/auth/cli/refresh",
		"POST /api/v1/auth/cli/logout",
		"GET /api/v1/auth/cli/status",
		"GET /api/v1/auth/cli/projects",
		"GET /api/v1/auth/cli/sessions",
		"DELETE /api/v1/auth/cli/sessions/:id",
		"POST /api/v1/auth/cli/sessions/:id/revoke",
		"GET /api/v1/auth/cli/bindings",
		"POST /api/v1/auth/cli/bindings",
		"POST /api/v1/auth/cli/bindings/:projectID/reauthorize",
		"DELETE /api/v1/auth/cli/bindings/:projectID/:bindingID",
		"POST /api/v1/auth/cli/bindings/:projectID/:bindingID/revoke",
	}
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("missing CLI auth route %s", route)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("CLI pairing route without Firestore status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for range 30 {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("CF-Connecting-IP", "192.0.2.30")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("poll request before rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("CF-Connecting-IP", "192.0.2.30")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("CLI poll rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCLISessionRevokePostPassesExistingCORSPreflightContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
	}, false, nil, &syssettings.FakeStore{Enabled: true})
	for _, path := range []string{
		"/api/v1/auth/cli/sessions/session-1/revoke",
		"/api/v1/auth/cli/bindings/project-1/binding-1/revoke",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodOptions, "http://auth.example.test"+path, nil)
		request.Header.Set("Origin", "https://frontend.example")
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Methods") != "GET,POST,OPTIONS" || recorder.Header().Get("Access-Control-Allow-Origin") != "https://frontend.example" {
			t.Fatalf("revoke preflight for %s status=%d headers=%#v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestProductionRouterCLIApprovalSessionProjectAndBindingLifecycle(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	gin.SetMode(gin.TestMode)
	client, err := firestoreclient.NewClientWithDatabase("lwc-346-auth-router", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := t.Context()
	userID := fmt.Sprintf("lwc-346-router-user-%d", time.Now().UnixNano())
	if _, err := client.Raw().Collection("users").Doc(userID).Set(ctx, map[string]interface{}{
		"email": "owner@example.test", "role": "user", "status": auth.AccountActive, "auth_version": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	const projectID = "cli-project"
	if _, err := client.Raw().Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "name": "CLI Project",
	}); err != nil {
		t.Fatal(err)
	}
	const jwtSecret = "lwc-346-router-jwt-secret"
	cfg := config.Config{
		JWTSecret: jwtSecret, FirestoreDatabaseID: "lwc-346-auth-router", AuthServiceURL: "https://auth.example.test",
		AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"},
	}
	router := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true})
	webToken, err := auth.GenerateAccessToken(userID, "user", jwtSecret, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "http://auth.example.test"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	start := request(http.MethodPost, "/api/v1/auth/cli/pairing/start", `{"client_name":"test-cli"}`, "")
	if start.Code != http.StatusCreated {
		t.Fatalf("pairing start status=%d body=%s", start.Code, start.Body.String())
	}
	var pairing auth.CLIStartPairingResponse
	if err := json.Unmarshal(start.Body.Bytes(), &pairing); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pairing.VerificationURL, "https://frontend.example/cli-pairing?user_code=") {
		t.Fatalf("verification URL=%q", pairing.VerificationURL)
	}
	if got := request(http.MethodGet, "/api/v1/auth/cli/pairing/decision?user_code="+pairing.UserCode, "", webToken); got.Code != http.StatusNotFound {
		t.Fatalf("GET verification unexpectedly had a decision route: %d", got.Code)
	}
	for range 30 {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/decision", strings.NewReader(`{"user_code":"00000000","decision":"approve"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+webToken)
		req.Header.Set("CF-Connecting-IP", "192.0.2.31")
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("guess request before rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	guessRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/decision", strings.NewReader(`{"user_code":"00000000","decision":"approve"}`))
	guessRequest.Header.Set("Content-Type", "application/json")
	guessRequest.Header.Set("Authorization", "Bearer "+webToken)
	guessRequest.Header.Set("CF-Connecting-IP", "192.0.2.31")
	router.ServeHTTP(recorder, guessRequest)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("CLI code guess rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	pending := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, pairing.PairingID, pairing.PollingSecret), "")
	if pending.Code != http.StatusAccepted {
		t.Fatalf("pairing poll before approval status=%d body=%s", pending.Code, pending.Body.String())
	}
	decisionBody := fmt.Sprintf(`{"user_code":%q,"decision":"approve"}`, pairing.UserCode)
	if decision := request(http.MethodPost, "/api/v1/auth/cli/pairing/decision", decisionBody, webToken); decision.Code != http.StatusOK {
		t.Fatalf("Web approval status=%d body=%s", decision.Code, decision.Body.String())
	}
	grant := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, pairing.PairingID, pairing.PollingSecret), "")
	if grant.Code != http.StatusOK {
		t.Fatalf("pairing grant status=%d body=%s", grant.Code, grant.Body.String())
	}
	var result auth.CLIPairingPollResult
	if err := json.Unmarshal(grant.Body.Bytes(), &result); err != nil || result.Credentials == nil {
		t.Fatalf("pairing grant=%#v error=%v", result, err)
	}
	cliToken := result.Credentials.AccessToken
	status := request(http.MethodGet, "/api/v1/auth/cli/status", "", cliToken)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), result.Credentials.SessionID) {
		t.Fatalf("CLI status=%d body=%s", status.Code, status.Body.String())
	}
	projects := request(http.MethodGet, "/api/v1/auth/cli/projects?page_size=1", "", cliToken)
	if projects.Code != http.StatusOK || !strings.Contains(projects.Body.String(), projectID) {
		t.Fatalf("CLI project list=%d body=%s", projects.Code, projects.Body.String())
	}
	createBinding := request(http.MethodPost, "/api/v1/auth/cli/bindings", fmt.Sprintf(`{"project_id":%q,"wiki_id":"wiki-1","host":"https://auth.example.test"}`, projectID), cliToken)
	if createBinding.Code != http.StatusCreated {
		t.Fatalf("create binding status=%d body=%s", createBinding.Code, createBinding.Body.String())
	}
	var binding auth.SyncBinding
	if err := json.Unmarshal(createBinding.Body.Bytes(), &binding); err != nil || binding.ID == "" {
		t.Fatalf("created binding=%#v error=%v", binding, err)
	}
	secondPairing := request(http.MethodPost, "/api/v1/auth/cli/pairing/start", `{"client_name":"second-cli"}`, "")
	if secondPairing.Code != http.StatusCreated {
		t.Fatalf("second pairing start status=%d body=%s", secondPairing.Code, secondPairing.Body.String())
	}
	var another auth.CLIStartPairingResponse
	if err := json.Unmarshal(secondPairing.Body.Bytes(), &another); err != nil {
		t.Fatal(err)
	}
	cliDecision := request(http.MethodPost, "/api/v1/auth/cli/pairing/decision", fmt.Sprintf(`{"user_code":%q,"decision":"approve"}`, another.UserCode), cliToken)
	if cliDecision.Code != http.StatusForbidden {
		t.Fatalf("CLI credential reached Web approval: status=%d body=%s", cliDecision.Code, cliDecision.Body.String())
	}
	secondPending := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, another.PairingID, another.PollingSecret), "")
	if secondPending.Code != http.StatusAccepted {
		t.Fatalf("CLI attempted approval changed pairing state: status=%d", secondPending.Code)
	}
	webBindings := request(http.MethodGet, "/api/v1/auth/cli/bindings", "", webToken)
	if webBindings.Code != http.StatusOK || !strings.Contains(webBindings.Body.String(), binding.ID) {
		t.Fatalf("Web binding list status=%d body=%s", webBindings.Code, webBindings.Body.String())
	}
	revokeBinding := request(http.MethodPost, "/api/v1/auth/cli/bindings/"+projectID+"/"+binding.ID+"/revoke", "", webToken)
	if revokeBinding.Code != http.StatusOK {
		t.Fatalf("Web binding revoke status=%d body=%s", revokeBinding.Code, revokeBinding.Body.String())
	}
	reauthorize := request(http.MethodPost, "/api/v1/auth/cli/bindings/"+projectID+"/reauthorize", fmt.Sprintf(`{"binding_id":%q,"wiki_id":"wiki-1","host":"https://auth.example.test"}`, binding.ID), webToken)
	if reauthorize.Code != http.StatusOK || strings.Contains(revokeBinding.Body.String(), "access_token") {
		t.Fatalf("Web binding reauthorize status=%d body=%s", reauthorize.Code, reauthorize.Body.String())
	}
	if revokeSession := request(http.MethodPost, "/api/v1/auth/cli/sessions/"+result.Credentials.SessionID+"/revoke", "", webToken); revokeSession.Code != http.StatusOK {
		t.Fatalf("Web session revoke status=%d body=%s", revokeSession.Code, revokeSession.Body.String())
	}
	if revoked := request(http.MethodGet, "/api/v1/auth/cli/status", "", cliToken); revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked CLI access status=%d body=%s", revoked.Code, revoked.Body.String())
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
