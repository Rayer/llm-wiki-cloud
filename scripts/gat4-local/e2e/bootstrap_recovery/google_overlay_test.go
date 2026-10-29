package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/gateauth/internal/firestore"
)

func TestGAT4BootstrapGoogleBusinessFlow(t *testing.T) {
	project := requiredGAT4Env(t, "GAT4_BOOTSTRAP_PROJECT")
	database := requiredGAT4Env(t, "GAT4_BOOTSTRAP_DATABASE")
	scope := requiredGAT4Env(t, "GAT4_BOOTSTRAP_SCOPE")
	bffOrigin := requiredGAT4Env(t, "GAT4_BOOTSTRAP_BFF_ORIGIN")
	jwtKeyPath := requiredGAT4Env(t, "GAT4_BOOTSTRAP_JWT_SECRET_FILE")
	reportPath := requiredGAT4Env(t, "GAT4_BOOTSTRAP_RESULT_FILE")
	nonce := requiredGAT4Env(t, "GAT4_BOOTSTRAP_EMAIL_NONCE")
	jwtKey, err := os.ReadFile(jwtKeyPath)
	if err != nil || strings.TrimSpace(string(jwtKey)) == "" {
		t.Fatal("synthetic local signing key unavailable")
	}

	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatal("create scoped Firestore emulator client")
	}
	defer client.Close()
	repo := NewIdentityRepository(client.Raw())
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	base := GoogleConfig{
		ClientID: "g2-client", ClientSecret: "synthetic-local-secret",
		Issuer: "https://issuer.example.test", JWKSURL: server.URL + "/keys",
		TokenURL: server.URL + "/token", AuthorizationURL: server.URL + "/authorize",
		LoginRedirectURL: "https://auth.example.test/api/v1/auth/google/login/callback",
		LinkRedirectURL:  "https://auth.example.test/api/v1/auth/google/link/callback",
		CompletionURL:    "https://wiki.example.test/login", AllowedOrigins: []string{"https://wiki.example.test"},
	}
	sessions := NewRefreshSessionAuthorityWithConfig(client.Raw(), SessionAuthorityConfig{
		Environment: "gat4-bootstrap-local", Migration: RefreshSessionMigrationDisabled,
	})
	gate := &fakeRegistrationGate{enabled: true}
	service := NewGoogleOAuthServiceWithSessionAuthority(base, client.Raw(), repo, gate, strings.TrimSpace(string(jwtKey)), sessions)
	service.client = server.Client()
	service.verifier = newGoogleOIDCVerifier(base, service.client)

	jitSubject := "gat4-jit-" + nonce
	jitEmail := "gat4-jit-" + nonce + "@example.test"
	jitPath := executeGoogleLoginForBootstrap(t, service, provider, repo, jitSubject, jitEmail,
		nonce+"-jit", project, database, scope, bffOrigin)
	if !jitPath["jit_provisioned"].(bool) {
		t.Fatal("new Google subject did not use the actual JIT identity producer")
	}

	existingSubject := "gat4-existing-" + nonce
	existingUserID := "gat4-google-user-" + nonce
	existingEmail := "gat4-existing-" + nonce + "@example.test"
	existing := ExternalUserProvisioning{
		UserID: existingUserID, DisplayEmail: existingEmail, CanonicalEmail: existingEmail,
		EmailVerified: true, Provider: googleProvider, Issuer: base.Issuer, Subject: existingSubject,
		ProviderEmail: existingEmail, ProviderEmailVerified: true, ProjectID: defaultProjectID,
	}
	if err := repo.ProvisionExternalUser(context.Background(), existing); err != nil {
		t.Fatal("seed existing Auth-only Google identity through the production identity repository")
	}
	if !restProductDocumentsAbsent(t, project, database, scope, existingUserID) {
		t.Fatal("existing Auth identity unexpectedly has product docs before BFF bootstrap")
	}
	existingGate := &fakeRegistrationGate{enabled: false}
	existingService := NewGoogleOAuthServiceWithSessionAuthority(base, client.Raw(), repo, existingGate,
		strings.TrimSpace(string(jwtKey)), sessions)
	existingService.client = server.Client()
	existingService.verifier = newGoogleOIDCVerifier(base, existingService.client)
	existingPath := executeGoogleLoginForBootstrap(t, existingService, provider, repo, existingSubject,
		existingEmail, nonce+"-existing", project, database, scope, bffOrigin)
	if existingPath["jit_provisioned"].(bool) || existingPath["user_id"] != existingUserID || existingGate.called {
		t.Fatal("existing Google identity did not sign in through the existing-identity branch")
	}

	result := map[string]interface{}{
		"provider_seam": "existing repository fake Google provider, production callback and identity repository, real Firestore emulator; no external Google OIDC/browser flow",
		"new_jit":       jitPath, "existing_login": existingPath,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal("encode nonsecret Google business-flow report")
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0700); err != nil {
		t.Fatal("create private result directory")
	}
	if err := os.WriteFile(reportPath, raw, 0600); err != nil {
		t.Fatal("write private Google business-flow result")
	}
	fmt.Printf("GAT4_GOOGLE_BOOTSTRAP_RESULT %s\n", raw)
}

func executeGoogleLoginForBootstrap(t *testing.T, service *GoogleOAuthService, provider *fakeGoogleProvider,
	repo *IdentityRepository, subject, email, stateSuffix, project, database, scope, bffOrigin string) map[string]interface{} {
	t.Helper()
	state, browser, code := "gat4-state-"+stateSuffix, "gat4-browser-"+stateSuffix, "gat4-code-"+stateSuffix
	transaction := seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
	provider.mu.Lock()
	provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: subject, email: email, emailVerified: true}
	provider.mu.Unlock()
	recorder, _ := oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
	if recorder.Code != http.StatusFound {
		t.Fatalf("Google callback handler failed with HTTP %d", recorder.Code)
	}
	refreshCookie := responseCookie(recorder, "__Host-lwc_refresh")
	if refreshCookie == nil {
		t.Fatal("Google callback handler omitted refresh session")
	}
	completionCookie := responseCookie(recorder, oauthCompletionCookieName)
	if completionCookie == nil {
		t.Fatal("Google callback handler omitted completion result")
	}
	completionRouter := gin.New()
	completionRouter.GET("/complete", service.CompletionHandler())
	completionRequest := httptest.NewRequest(http.MethodGet, "/complete", nil)
	completionRequest.AddCookie(completionCookie)
	completionRequest.AddCookie(&http.Cookie{Name: oauthCookieName, Value: browser})
	completionResponse := httptest.NewRecorder()
	completionRouter.ServeHTTP(completionResponse, completionRequest)
	var completion oauthCompletionResultResponse
	if completionResponse.Code != http.StatusOK || json.Unmarshal(completionResponse.Body.Bytes(), &completion) != nil ||
		completion.Status != "success" {
		t.Fatal("Google callback did not complete its Auth-owned result")
	}

	refreshRouter := gin.New()
	refreshRouter.POST("/refresh", RefreshHandlerWithSessionAuthority(service.sessions, service.jwtSecret, HostRefreshCookiePolicy()))
	refreshRequest := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	refreshRequest.AddCookie(refreshCookie)
	refreshResponse := httptest.NewRecorder()
	refreshRouter.ServeHTTP(refreshResponse, refreshRequest)
	var refreshed RefreshResponse
	if refreshResponse.Code != http.StatusOK || json.Unmarshal(refreshResponse.Body.Bytes(), &refreshed) != nil ||
		refreshed.AccessToken == "" || refreshed.User.ID == "" {
		t.Fatal("Auth refresh did not issue a real access token for the Google identity")
	}

	external, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, subject)
	if err != nil || external == nil || external.UserID != refreshed.User.ID {
		t.Fatal("Google identity repository did not persist the callback subject")
	}
	user, err := scopedfirestore.Collection(service.fs, "users").Doc(refreshed.User.ID).Get(context.Background())
	if err != nil || user.Data()["default_project"] != defaultProjectID {
		t.Fatal("Auth identity did not preserve the default Project compatibility pointer")
	}
	if !restProductDocumentsAbsent(t, project, database, scope, refreshed.User.ID) {
		t.Fatal("Auth Google identity producer created product docs before BFF bootstrap")
	}

	status, body := postBootstrapForGAT4(t, bffOrigin, refreshed.AccessToken)
	if status != http.StatusOK || body["default_project_id"] != defaultProjectID {
		t.Fatalf("actual BFF bootstrap failed after Google identity, HTTP %d", status)
	}
	listStatus, listedOnce := getDefaultProjectFromBFFForGAT4(t, bffOrigin, refreshed.AccessToken)
	if listStatus != http.StatusOK || !listedOnce {
		t.Fatal("actual BFF project list did not expose exactly the default Project")
	}
	return map[string]interface{}{
		"user_id": refreshed.User.ID, "default_project_id": defaultProjectID,
		"jit_provisioned": completion.JITProvisioned, "identity_only_before_bff": true,
		"bff_bootstrap_status": status, "bff_project_list_status": listStatus,
		"bff_project_listed_once": listedOnce,
	}
}

func restProductDocumentsAbsent(t *testing.T, project, database, scope, userID string) bool {
	t.Helper()
	host := requiredGAT4Env(t, "FIRESTORE_EMULATOR_HOST")
	root := fmt.Sprintf("http://%s/v1/projects/%s/databases/%s/documents", host, project, database)
	request, err := http.NewRequest(http.MethodGet, root+"/local_scopes/"+scope+"/projects?pageSize=1000", nil)
	if err != nil {
		t.Fatal("construct emulator REST read")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("read owned emulator Project registry")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("emulator Project registry read returned HTTP %d", response.StatusCode)
	}
	var listing struct {
		Documents []struct {
			Fields map[string]map[string]interface{} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		t.Fatal("decode owned emulator Project registry")
	}
	for _, document := range listing.Documents {
		field := document.Fields["user_id"]
		if field["stringValue"] == userID {
			return false
		}
	}
	metadataURL := root + "/local_scopes/" + scope + "/users/" + userID + "/projects/default"
	metadataRequest, err := http.NewRequest(http.MethodGet, metadataURL, nil)
	if err != nil {
		t.Fatal("construct metadata emulator REST read")
	}
	metadataResponse, err := http.DefaultClient.Do(metadataRequest)
	if err != nil {
		t.Fatal("read owned emulator account Project metadata")
	}
	defer metadataResponse.Body.Close()
	return metadataResponse.StatusCode == http.StatusNotFound
}

func postBootstrapForGAT4(t *testing.T, origin, token string) (int, map[string]interface{}) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/onboarding/default-project",
		strings.NewReader("{\"schema_version\":1}"))
	if err != nil {
		t.Fatal("construct BFF bootstrap request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("call actual local BFF bootstrap endpoint")
	}
	defer response.Body.Close()
	var body map[string]interface{}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.StatusCode, body
}

func getDefaultProjectFromBFFForGAT4(t *testing.T, origin, token string) (int, bool) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, origin+"/api/v1/projects", nil)
	if err != nil {
		t.Fatal("construct BFF projects request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("call actual local BFF projects endpoint")
	}
	defer response.Body.Close()
	var projects []map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&projects); err != nil {
		t.Fatal("decode actual BFF projects response")
	}
	count := 0
	for _, project := range projects {
		if project["id"] == defaultProjectID {
			count++
		}
	}
	return response.StatusCode, count == 1
}

func requiredGAT4Env(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("required local test input %s is missing", name)
	}
	return value
}
