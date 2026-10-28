package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	"google.golang.org/api/idtoken"
)

func TestLWCConsumerVerifierRequiresTrustedCallerAndReturnsAccountAuthority(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	if _, err := fs.Collection("users").Doc("consumer-user").Set(ctx, UserRecord{Email: "user@example.test", Role: "admin", AuthVersion: 4}); err != nil {
		t.Fatal(err)
	}
	secret := "lwc-consumer-verifier-test-key"
	credential, err := GenerateAccessToken("consumer-user", "admin", secret, 4)
	if err != nil {
		t.Fatal(err)
	}
	policy := LWCConsumerPolicy{
		Enabled: true, Environment: "dev", Resource: "https://wiki.dev.example.test",
		CallerServiceAccount: "lwc-bff-dev@example.iam.gserviceaccount.com",
		VerifierAudience:     "https://auth-runtime.example.test", Issuer: "https://auth.dev.example.test",
		AllowedCredentialKinds: []string{"web-access", "cli-access"},
	}
	validator := func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "runtime-id-token" || audience != policy.VerifierAudience {
			t.Fatalf("caller verification received unexpected token/audience %q/%q", token, audience)
		}
		return &idtoken.Payload{
			Issuer: "https://accounts.google.com", Audience: policy.VerifierAudience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": policy.CallerServiceAccount, "email_verified": true},
		}, nil
	}
	router := gin.New()
	router.POST(LWCConsumerVerifyPath, LWCConsumerVerifyHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil))
	router.POST(LWCConsumerCheckPath, LWCConsumerAccountCheckHandler(fs, policy, validator))

	request := httptest.NewRequest(http.MethodPost, LWCConsumerVerifyPath, strings.NewReader(`{"schema_version":1,"resource":"https://wiki.dev.example.test"}`))
	request.Header.Set("Authorization", "Bearer runtime-id-token")
	request.Header.Set("X-LWC-User-Credential", credential)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("verify status/cache-control=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var identity LWCConsumerIdentity
	if err := json.Unmarshal(response.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "consumer-user" || identity.CredentialKind != "web-access" || identity.Environment != "dev" ||
		identity.Resource != policy.Resource || identity.AuthVersion != 4 || len(identity.ManagementScopes) != 0 {
		t.Fatalf("unexpected consumer identity: %+v", identity)
	}

	checkBody := `{"schema_version":1,"user_id":"consumer-user"}`
	request = httptest.NewRequest(http.MethodPost, LWCConsumerCheckPath, strings.NewReader(checkBody))
	request.Header.Set("Authorization", "Bearer runtime-id-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":true`) {
		t.Fatalf("account check status/body=%d/%s", response.Code, response.Body.String())
	}
}

func TestLWCConsumerVerifierFailsClosedForUntrustedCallerAndStrictResource(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	if _, err := fs.Collection("users").Doc("consumer-user").Set(ctx, UserRecord{Role: "admin", AuthVersion: 0}); err != nil {
		t.Fatal(err)
	}
	secret := "lwc-consumer-verifier-test-key"
	credential, err := GenerateAccessToken("consumer-user", "admin", secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy := LWCConsumerPolicy{
		Enabled: true, Environment: "dev", Resource: "https://wiki.dev.example.test",
		CallerServiceAccount: "lwc-bff-dev@example.iam.gserviceaccount.com",
		VerifierAudience:     "https://auth-runtime.example.test", Issuer: "https://auth.dev.example.test",
		AllowedCredentialKinds: []string{"web-access"},
	}
	trusted := false
	validator := func(_ context.Context, _, audience string) (*idtoken.Payload, error) {
		email := "wrong@example.iam.gserviceaccount.com"
		if trusted {
			email = policy.CallerServiceAccount
		}
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": email, "email_verified": true}}, nil
	}
	router := gin.New()
	router.POST(LWCConsumerVerifyPath, LWCConsumerVerifyHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil))
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{name: "untrusted runtime", body: `{"schema_version":1,"resource":"https://wiki.dev.example.test"}`, want: http.StatusForbidden},
		{name: "wrong resource", body: `{"schema_version":1,"resource":"https://wiki.prod.example.test"}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"schema_version":1,"resource":"https://wiki.dev.example.test","role":"admin"}`, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trusted = tc.name != "untrusted runtime"
			request := httptest.NewRequest(http.MethodPost, LWCConsumerVerifyPath, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer runtime-id-token")
			request.Header.Set("X-LWC-User-Credential", credential)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.want || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/cache-control=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
}

func TestLWCConsumerVerifierBindsOpaqueMCPCredentialToProjectResource(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	userID, projectID := "consumer-mcp-user-"+suffix, "consumer-mcp-project-"+suffix
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, UserRecord{Email: userID + "@example.test", Status: AccountActive, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "MCP fixture"}); err != nil {
		t.Fatal(err)
	}
	databaseEnvironment := "consumer-mcp-" + suffix
	const resourceOrigin = "https://bff.example.test"
	service := NewMCPOAuthServiceWithPolicy(fs, databaseEnvironment, "dev", "https://auth.example.test", resourceOrigin,
		"https://wiki.example.test", "fixture-jwt-secret", config.LegacyAuthOAuthResources(resourceOrigin))
	client, err := service.RegisterClient(ctx, "Hermes test", []string{"http://127.0.0.1:43123/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("a", 43)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	resource := service.ResourceURL(projectID)
	consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "fixture-state", resource, "mcp:query")
	if err != nil {
		t.Fatal(err)
	}
	consent, err := url.Parse(consentURL)
	if err != nil {
		t.Fatal(err)
	}
	transactionID := consent.Query().Get("transaction_id")
	if _, err := service.ConsentDetails(ctx, userID, 3, transactionID); err != nil {
		t.Fatal(err)
	}
	decision, err := service.DecideConsent(ctx, userID, 3, transactionID, true)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(decision.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := service.ExchangeCode(ctx, client.ClientID, callback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil {
		t.Fatal(err)
	}

	policy := LWCConsumerPolicy{Enabled: true, Environment: "dev", Resource: resourceOrigin,
		CallerServiceAccount: "lwc-bff-dev@example.iam.gserviceaccount.com", VerifierAudience: "https://auth-runtime.example.test",
		Issuer: "https://auth.example.test", AllowedCredentialKinds: []string{"web-access", "cli-access", "mcp-oauth"}}
	validator := func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "runtime-id-token" || audience != policy.VerifierAudience {
			t.Fatalf("caller verification received unexpected token/audience %q/%q", token, audience)
		}
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": policy.CallerServiceAccount, "email_verified": true}}, nil
	}
	router := gin.New()
	router.POST(LWCConsumerVerifyPath, LWCConsumerVerifyHandlerWithAuthorities(config.Config{}, fs, policy, validator, nil, nil, service))
	request := func(requestedResource, requestedProject string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(LWCConsumerVerifyRequest{SchemaVersion: 1, Resource: requestedResource, ProjectID: requestedProject})
		req := httptest.NewRequest(http.MethodPost, LWCConsumerVerifyPath, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer runtime-id-token")
		req.Header.Set("X-LWC-User-Credential", tokens.AccessToken)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request(resource, projectID)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("MCP verify status/cache-control=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var identity LWCConsumerIdentity
	if err := json.Unmarshal(response.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.CredentialKind != "mcp-oauth" || identity.Subject != userID || identity.ProjectID != projectID ||
		identity.Resource != resource || identity.GrantID == "" || identity.ClientID != client.ClientID ||
		strings.Join(identity.Scopes, " ") != "mcp:query" || len(identity.ManagementScopes) != 0 {
		t.Fatalf("unexpected MCP consumer identity: %+v", identity)
	}
	for _, tc := range []struct{ resource, project string }{
		{resource + "/other", projectID}, {resource, "other-project"},
	} {
		if got := request(tc.resource, tc.project).Code; got != http.StatusForbidden {
			t.Fatalf("MCP resource/project mismatch status=%d, want 403", got)
		}
	}
}

func TestLWCConsumerVerifierRevalidatesDurableCLISessionAndAccount(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, secret = "consumer-cli-user", "consumer-cli-signing-key"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, UserRecord{Email: "cli@example.test", Role: "member", Status: AccountActive, AuthVersion: 2}); err != nil {
		t.Fatal(err)
	}
	const environment = "consumer-cli-session-test"
	sessions := NewRefreshSessionAuthority(fs, environment)
	credential, err := sessions.IssueCLISession(ctx, userID, "test CLI", secret)
	if err != nil {
		t.Fatal(err)
	}
	policy := LWCConsumerPolicy{Enabled: true, Environment: "dev", Resource: "https://wiki.dev.example.test",
		CallerServiceAccount: "lwc-bff-dev@example.iam.gserviceaccount.com", VerifierAudience: "https://auth-runtime.example.test",
		Issuer: "https://auth.dev.example.test", AllowedCredentialKinds: []string{"cli-access"}}
	validator := func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": policy.CallerServiceAccount, "email_verified": true}}, nil
	}
	router := gin.New()
	router.POST(LWCConsumerVerifyPath, LWCConsumerVerifyHandler(config.Config{JWTSecret: secret}, fs, policy, validator, sessions.VerifyCLIAccessSession))
	verify := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, LWCConsumerVerifyPath, strings.NewReader(`{"schema_version":1,"resource":"https://wiki.dev.example.test"}`))
		req.Header.Set("Authorization", "Bearer runtime-id-token")
		req.Header.Set("X-LWC-User-Credential", token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := verify(credential.AccessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"credential_kind":"cli-access"`) ||
		!strings.Contains(response.Body.String(), `"session_id":"`+credential.SessionID+`"`) {
		t.Fatalf("CLI verify status/body=%d/%s", response.Code, response.Body.String())
	}
	if err := sessions.RevokeCLISession(ctx, userID, credential.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := verify(credential.AccessToken).Code; got != http.StatusUnauthorized {
		t.Fatalf("revoked CLI session verify status=%d, want 401", got)
	}
	second, err := sessions.IssueCLISession(ctx, userID, "second CLI", secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, UserRecord{Email: "cli@example.test", Status: AccountSuspended, AuthVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if got := verify(second.AccessToken).Code; got != http.StatusUnauthorized {
		t.Fatalf("suspended account verify status=%d, want 401", got)
	}
}
