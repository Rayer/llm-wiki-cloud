package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func TestMCPOAuthScopeIsExplicitAndCapabilityBound(t *testing.T) {
	for _, test := range []struct {
		name         string
		requested    string
		wantScope    string
		wantCaps     []string
		wantAccepted bool
	}{
		{name: "default query scope", requested: "", wantScope: mcpOAuthScope, wantCaps: []string{mcpOAuthScope}, wantAccepted: true},
		{name: "legacy query scope", requested: mcpOAuthScope, wantScope: mcpOAuthScope, wantCaps: []string{mcpOAuthScope}, wantAccepted: true},
		{name: "query and write", requested: mcpOAuthScope + " " + mcpOAuthRawWriteScope, wantScope: mcpOAuthScope + " " + mcpOAuthRawWriteScope, wantCaps: []string{mcpOAuthScope, mcpOAuthRawWriteScope}, wantAccepted: true},
		{name: "normalizes requested order", requested: mcpOAuthRawWriteScope + " " + mcpOAuthScope, wantScope: mcpOAuthScope + " " + mcpOAuthRawWriteScope, wantCaps: []string{mcpOAuthScope, mcpOAuthRawWriteScope}, wantAccepted: true},
		{name: "write without query", requested: mcpOAuthRawWriteScope, wantAccepted: false},
		{name: "duplicate query", requested: mcpOAuthScope + " " + mcpOAuthScope, wantAccepted: false},
		{name: "unknown scope", requested: mcpOAuthScope + " mcp:admin", wantAccepted: false},
		{name: "noncanonical spacing", requested: mcpOAuthScope + "  " + mcpOAuthRawWriteScope, wantAccepted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotScope, gotCapabilities, ok := normalizeMCPOAuthScope(test.requested)
			if ok != test.wantAccepted {
				t.Fatalf("accepted=%t, want %t", ok, test.wantAccepted)
			}
			if !ok {
				return
			}
			if gotScope != test.wantScope || strings.Join(gotCapabilities, "|") != strings.Join(test.wantCaps, "|") {
				t.Fatalf("scope=%q capabilities=%v, want %q and %v", gotScope, gotCapabilities, test.wantScope, test.wantCaps)
			}
			if !isCanonicalMCPOAuthScope(gotScope) {
				t.Fatalf("normalized scope %q is not canonical", gotScope)
			}
		})
	}
}

func TestAuthOAuthPolicyKeepsProjectDefaultsAndRequiresExplicitFixedScope(t *testing.T) {
	resources := config.LegacyAuthOAuthResources("https://bff.example.test")
	brief := config.AuthOAuthResource{
		ID: "daily-brief", Kind: "fixed", Enabled: false, URI: "https://brief.rayer.idv.tw/mcp", Name: "Daily Brief",
		Scopes:           []config.AuthOAuthScope{{Name: "brief:manage", Description: "Read and manage Daily Brief reports and annotations in your private account space."}},
		AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{},
		VerifierAudience: "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app",
	}
	resources = append(resources, brief)
	service := NewMCPOAuthServiceWithPolicy(nil, "llm-wiki-cloud-dev", "dev", "https://auth.example.test", "https://bff.example.test", "https://wiki.example.test", "", resources)
	for requested, expected := range map[string]string{"": "mcp:query", "mcp:query": "mcp:query", "mcp:raw:write mcp:query": "mcp:query mcp:raw:write"} {
		got, ok := service.normalizeRegistrationScope(requested)
		if !ok || got != expected {
			t.Errorf("project registration scope %q = %q, %t; want %q", requested, got, ok, expected)
		}
	}
	if _, ok := service.normalizeRegistrationScope("mcp:raw:write"); ok {
		t.Fatal("accepted raw-write without query")
	}
	if _, ok := service.normalizeRegistrationScope("brief:manage"); ok {
		t.Fatal("registered disabled fixed-resource scope")
	}
	if _, _, _, _, ok := service.normalizeAuthorizationScope(brief.URI, ""); ok {
		t.Fatal("defaulted an omitted fixed-resource scope")
	}
	if _, _, ok := service.policyForResource(brief.URI + "?tenant=other"); ok {
		t.Fatal("accepted a query-altered fixed resource")
	}
	if _, err := service.ListResourceGrants(context.Background(), "valid-user", brief.URI); !errors.Is(err, ErrMCPOAuthBadRequest) {
		t.Fatalf("disabled fixed-resource grant listing error=%v, want bad request", err)
	}
	resources[1].Enabled = true
	service = NewMCPOAuthServiceWithPolicy(nil, "llm-wiki-cloud-dev", "dev", "https://auth.example.test", "https://bff.example.test", "https://wiki.example.test", "", resources)
	if got, ok := service.normalizeRegistrationScope("brief:manage"); !ok || got != "brief:manage" {
		t.Fatalf("explicit Brief scope = %q %t", got, ok)
	}
	if got, scopes, _, projectID, ok := service.normalizeAuthorizationScope(brief.URI, "brief:manage"); !ok || got != "brief:manage" || strings.Join(scopes, " ") != got || projectID != "" {
		t.Fatalf("fixed authorization policy = %q %v project=%q ok=%t", got, scopes, projectID, ok)
	}
	if _, _, _, _, ok := service.normalizeAuthorizationScope(brief.URI, ""); ok {
		t.Fatal("auto-expanded missing Brief consent scope")
	}
	if _, err := service.ListResourceGrants(context.Background(), "valid-user", brief.URI); !errors.Is(err, ErrMCPOAuthUnavailable) {
		t.Fatalf("enabled fixed-resource grant listing without Firestore error=%v, want unavailable", err)
	}
}

func TestMCPOAuthFirestoreFlowBindsProjectAndRotatesRefreshTokens(t *testing.T) {
	_, fs := newIdentityEmulatorRepository(t)
	defer fs.Close()
	ctx := context.Background()
	userID := fmt.Sprintf("mcp-oauth-user-%d", time.Now().UnixNano())
	projectID := "mcp-oauth-project"
	environment := fmt.Sprintf("mcp-oauth-env-%d", time.Now().UnixNano())
	userRef := scopedfirestore.Collection(fs, "users").Doc(userID)
	projectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocumentID(userID, projectID))
	if _, err := userRef.Set(ctx, UserRecord{Email: "oauth@example.test", Status: AccountActive, AuthVersion: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "OAuth fixture project"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = userRef.Delete(ctx)
		_, _ = projectRef.Delete(ctx)
		for _, name := range []string{"clients", "transactions", "codes", "grants", "access", "refresh"} {
			iter := mcpOAuthCollection(fs, environment, name).Documents(ctx)
			for {
				doc, err := iter.Next()
				if errors.Is(err, iterator.Done) {
					break
				}
				if err != nil {
					break
				}
				_, _ = doc.Ref.Delete(ctx)
			}
			iter.Stop()
		}
	})

	clock := time.Now().UTC()
	service := NewMCPOAuthServiceWithPolicy(fs, environment, "dev", "https://auth.example.test", "https://bff.example.test", "https://wiki.example.test", "fixture-jwt-secret", config.LegacyAuthOAuthResources("https://bff.example.test"))
	service.SetClock(func() time.Time { return clock })
	client, err := service.RegisterClient(ctx, "Hermes Agent", []string{"http://127.0.0.1:43123/callback"})
	if err != nil {
		t.Fatalf("register public client: %v", err)
	}
	if _, err := service.RegisterClient(ctx, "bad client", []string{"http://example.test/callback"}); !errors.Is(err, ErrMCPOAuthBadRequest) {
		t.Fatalf("non-loopback HTTP redirect error = %v, want bad request", err)
	}

	verifier := strings.Repeat("a", 43)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	resource := service.ResourceURL(projectID)
	writeScope := mcpOAuthScope + " " + mcpOAuthRawWriteScope
	consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "fixture-state", resource, writeScope)
	if err != nil {
		t.Fatalf("begin authorization: %v", err)
	}
	parsedConsent, err := url.Parse(consentURL)
	if err != nil || parsedConsent.Host != "wiki.example.test" {
		t.Fatalf("consent destination = %q, err=%v", consentURL, err)
	}
	transactionID := parsedConsent.Query().Get("transaction_id")
	details, err := service.ConsentDetails(ctx, userID, transactionID)
	if err != nil || details.ProjectName != "OAuth fixture project" || details.Scope != writeScope {
		t.Fatalf("consent details = %+v, err=%v", details, err)
	}
	decision, err := service.DecideConsent(ctx, userID, 0, transactionID, true)
	if err != nil {
		t.Fatalf("approve consent: %v", err)
	}
	callback, err := url.Parse(decision.RedirectURL)
	if err != nil || callback.Query().Get("state") != "fixture-state" || callback.Query().Get("code") == "" {
		t.Fatalf("authorization callback = %q, err=%v", decision.RedirectURL, err)
	}
	code := callback.Query().Get("code")
	tokens, err := service.ExchangeCode(ctx, client.ClientID, code, client.RedirectURIs[0], verifier, resource)
	if err != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.Scope != writeScope {
		t.Fatalf("exchange authorization code: response=%+v err=%v", tokens, err)
	}
	verified, err := service.VerifyAccessToken(ctx, tokens.AccessToken)
	if err != nil || verified.UserID != userID || verified.ClientID != client.ClientID ||
		strings.Join(verified.Scopes, " ") != writeScope ||
		verified.Resource != resource || verified.Environment != "dev" {
		t.Fatalf("credential verifier must return the actual LWC grant attributes: credential=%+v err=%v", verified, err)
	}
	if _, err := service.ExchangeCode(ctx, client.ClientID, code, client.RedirectURIs[0], verifier, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("authorization-code replay error = %v, want invalid", err)
	}

	authority := NewMCPGrantAuthority(fs, environment, "https://bff.example.test")
	authority.SetClock(func() time.Time { return clock })
	identity, err := authority.AuthenticateAccessToken(ctx, tokens.AccessToken, projectID, resource)
	if err != nil || identity.UserID != userID || identity.ProjectID != projectID ||
		identity.Scope != writeScope || strings.Join(identity.Capabilities, " ") != writeScope {
		t.Fatalf("authenticate project access token: identity=%+v err=%v", identity, err)
	}
	clock = clock.Add(mcpOAuthAccessTTL + time.Second)
	if _, err := authority.AuthenticateAccessToken(ctx, tokens.AccessToken, projectID, resource); !errors.Is(err, ErrMCPOAuthExpired) {
		t.Fatalf("expired project access token error = %v, want expired", err)
	}

	rotated, err := service.Refresh(ctx, client.ClientID, tokens.RefreshToken, resource)
	if err != nil || rotated.Scope != writeScope {
		t.Fatalf("rotate refresh token: %v", err)
	}
	retried, err := service.Refresh(ctx, client.ClientID, tokens.RefreshToken, resource)
	if err != nil || retried.AccessToken != rotated.AccessToken || retried.RefreshToken != rotated.RefreshToken || retried.Scope != writeScope {
		t.Fatalf("same-token retry did not return same successor pair: first=%+v retry=%+v err=%v", rotated, retried, err)
	}

	clock = clock.Add(mcpOAuthRetryWindow + time.Second)
	if _, err := service.Refresh(ctx, client.ClientID, tokens.RefreshToken, resource); !errors.Is(err, ErrMCPOAuthReplay) {
		t.Fatalf("out-of-window refresh replay error = %v, want replay", err)
	}
	if _, err := authority.AuthenticateAccessToken(ctx, rotated.AccessToken, projectID, resource); !errors.Is(err, ErrMCPOAuthDenied) {
		t.Fatalf("replayed refresh family remained usable: %v", err)
	}
	grants, err := service.ListGrants(ctx, userID, projectID)
	if err != nil || len(grants) != 1 || grants[0].Status != "revoked" {
		t.Fatalf("grant after replay = %+v, err=%v", grants, err)
	}
}

func TestDailyBriefFixedResourceGrantIsAccountOwnedWithoutProject(t *testing.T) {
	_, fs := newIdentityEmulatorRepository(t)
	defer fs.Close()
	ctx := context.Background()
	userID := fmt.Sprintf("brief-oauth-user-%d", time.Now().UnixNano())
	databaseEnvironment := fmt.Sprintf("brief-oauth-env-%d", time.Now().UnixNano())
	userRef := scopedfirestore.Collection(fs, "users").Doc(userID)
	if _, err := userRef.Set(ctx, UserRecord{Email: "brief@example.test", Status: AccountActive, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	otherUserID := userID + "-other"
	otherUserRef := scopedfirestore.Collection(fs, "users").Doc(otherUserID)
	if _, err := otherUserRef.Set(ctx, UserRecord{Email: "other-brief@example.test", Status: AccountActive}); err != nil {
		t.Fatal(err)
	}
	resources := config.LegacyAuthOAuthResources("https://bff.example.test")
	resources = append(resources, config.AuthOAuthResource{
		ID: "daily-brief", Kind: "fixed", Enabled: true, URI: "https://brief.rayer.idv.tw/mcp", Name: "Daily Brief",
		Scopes:           []config.AuthOAuthScope{{Name: "brief:manage", Description: "Read and manage Daily Brief reports and annotations in your private account space."}},
		AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{},
		VerifierCallerServiceAccount: "brief-runtime@llm-wiki-cloud.iam.gserviceaccount.com",
		VerifierAudience:             "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app",
	})
	service := NewMCPOAuthServiceWithPolicy(fs, databaseEnvironment, "dev", "https://auth.dev.rayer.idv.tw", "https://bff.example.test", "https://wiki.example.test", "fixture-jwt-secret", resources)
	clock := time.Now().UTC()
	service.SetClock(func() time.Time { return clock })
	defer func() {
		_, _ = userRef.Delete(ctx)
		_, _ = otherUserRef.Delete(ctx)
		for _, name := range []string{"clients", "transactions", "codes", "grants", "access", "refresh"} {
			iter := mcpOAuthCollection(fs, databaseEnvironment, name).Documents(ctx)
			for {
				doc, err := iter.Next()
				if errors.Is(err, iterator.Done) {
					break
				}
				if err != nil {
					break
				}
				_, _ = doc.Ref.Delete(ctx)
			}
			iter.Stop()
		}
	}()
	client, err := service.RegisterClient(ctx, "Daily Brief MCP", []string{"http://127.0.0.1:43124/auth/callback"})
	if err != nil {
		t.Fatalf("register native client: %v", err)
	}
	verifier := strings.Repeat("b", 43)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	resource := "https://brief.rayer.idv.tw/mcp"
	consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "brief-state", resource, "brief:manage")
	if err != nil {
		t.Fatalf("begin fixed-resource authorization: %v", err)
	}
	consent, _ := url.Parse(consentURL)
	transactionID := consent.Query().Get("transaction_id")
	details, err := service.ConsentDetails(ctx, userID, transactionID)
	if err != nil || details.ResourceKind != "fixed" || details.ResourceName != "Daily Brief" || details.ProjectID != "" || details.ProjectName != "" ||
		details.Resource != resource || details.Scope != "brief:manage" || len(details.ScopeDetails) != 1 || details.ScopeDetails[0].Name != "brief:manage" {
		t.Fatalf("fixed consent details = %+v err=%v", details, err)
	}
	decision, err := service.DecideConsent(ctx, userID, 3, transactionID, true)
	if err != nil {
		t.Fatalf("approve fixed-resource consent without Project: %v", err)
	}
	callback, _ := url.Parse(decision.RedirectURL)
	tokens, err := service.ExchangeCode(ctx, client.ClientID, callback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil {
		t.Fatalf("exchange fixed-resource code without Project: %v", err)
	}
	verified, err := service.VerifyAccessToken(ctx, tokens.AccessToken)
	if err != nil || verified.UserID != userID || verified.ClientID != client.ClientID || verified.Resource != resource ||
		strings.Join(verified.Scopes, " ") != "brief:manage" || verified.Environment != "dev" {
		t.Fatalf("fixed credential verification=%+v err=%v", verified, err)
	}
	gin.SetMode(gin.TestMode)
	verifierConfig := config.Config{AuthOAuthResources: resources}
	validateCaller := func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "fixture-caller-id-token" || audience != resources[1].VerifierAudience {
			return nil, ErrMCPOAuthInvalid
		}
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": resources[1].VerifierCallerServiceAccount, "email_verified": true}}, nil
	}
	verifierRouter := gin.New()
	verifierRouter.POST("/verify", MCPOAuthCredentialVerifierHandlerWithValidator(service, verifierConfig, validateCaller))
	verifyHTTP := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/verify", nil)
		request.Header.Set("Authorization", "Bearer fixture-caller-id-token")
		request.Header.Set("X-LWC-User-Credential", token)
		response := httptest.NewRecorder()
		verifierRouter.ServeHTTP(response, request)
		return response
	}
	verifiedResponse := verifyHTTP(tokens.AccessToken)
	var verifiedBody MCPOAuthVerifiedCredential
	if verifiedResponse.Code != http.StatusOK || json.Unmarshal(verifiedResponse.Body.Bytes(), &verifiedBody) != nil ||
		verifiedBody.UserID != userID || verifiedBody.ClientID != client.ClientID || verifiedBody.Resource != resource ||
		strings.Join(verifiedBody.Scopes, " ") != "brief:manage" || verifiedBody.Environment != "dev" || verifiedBody.ExpiresAt.IsZero() {
		t.Fatalf("Firestore-backed HTTP verifier status=%d body=%s decoded=%+v", verifiedResponse.Code, verifiedResponse.Body.String(), verifiedBody)
	}
	grants, err := service.ListResourceGrants(ctx, userID, resource)
	if err != nil || len(grants) != 1 || grants[0].ProjectID != "" || grants[0].Resource != resource || grants[0].Scope != "brief:manage" {
		t.Fatalf("account-owned fixed grants=%+v err=%v", grants, err)
	}
	grantDoc, err := mcpOAuthCollection(fs, databaseEnvironment, "grants").Doc(grants[0].ID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grant mcpOAuthGrantRecord
	if mcpOAuthDecode(grantDoc.Data(), &grant) != nil || grant.ProjectID != "" {
		t.Fatalf("persisted fixed grant fabricated Project: %#v", grantDoc.Data())
	}
	rotated, err := service.Refresh(ctx, client.ClientID, tokens.RefreshToken, resource)
	if err != nil || rotated.RefreshToken == "" || rotated.RefreshToken == tokens.RefreshToken {
		t.Fatalf("fixed-resource refresh rotation response=%+v err=%v", rotated, err)
	}
	if rotatedVerified, err := service.VerifyAccessToken(ctx, rotated.AccessToken); err != nil || rotatedVerified.ClientID != client.ClientID {
		t.Fatalf("rotated fixed-resource access verification=%+v err=%v", rotatedVerified, err)
	}
	otherGrants, err := service.ListResourceGrants(ctx, otherUserID, resource)
	if err != nil || len(otherGrants) != 0 {
		t.Fatalf("other account grant list=%+v err=%v", otherGrants, err)
	}
	if err := service.RevokeGrant(ctx, otherUserID, 0, grants[0].ID); !errors.Is(err, ErrMCPOAuthNotFound) {
		t.Fatalf("other account revoke error=%v, want not found", err)
	}
	missingToken, err := generateMCPOpaque(mcpOAuthAccessPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(missingToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing Firestore access record HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	corruptToken, err := generateMCPOpaque(mcpOAuthAccessPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mcpOAuthCollection(fs, databaseEnvironment, "access").Doc(mcpOAuthHash(corruptToken)).Set(ctx, map[string]any{"schema_version": mcpOAuthRecordSchema, "unknown": "sentinel"}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(corruptToken); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt Firestore access record HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := userRef.Set(ctx, UserRecord{Email: "brief@example.test", Status: AccountSuspended, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("suspended account HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := userRef.Set(ctx, UserRecord{Email: "brief@example.test", Status: AccountActive, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("active account with matching authorization version HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := userRef.Update(ctx, []firestore.Update{{Path: "auth_version", Value: int64(4)}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("active account with stale authorization version HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := userRef.Update(ctx, []firestore.Update{{Path: "auth_version", Value: int64(3)}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("restored active account authorization version HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	issuedAt := clock
	clock = issuedAt.Add(mcpOAuthAccessTTL + time.Second)
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("expired Firestore access record HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	clock = issuedAt
	grantRef := mcpOAuthCollection(fs, databaseEnvironment, "grants").Doc(grants[0].ID)
	grantSnapshot, err := grantRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mcpOAuthDecode(grantSnapshot.Data(), &grant) != nil || !clock.Before(grant.ExpiresAt) {
		t.Fatalf("pre-expiry grant fixture is invalid: %#v", grant)
	}
	originalGrantExpiry := grant.ExpiresAt
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: clock.Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("expired grant with otherwise valid access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: originalGrantExpiry}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("restored active grant should accept the still-valid access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if err := service.RevokeGrant(ctx, userID, 3, grants[0].ID); err != nil {
		t.Fatalf("owner revoke=%v", err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("same still-valid access token after owner revoke HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := service.Refresh(ctx, client.ClientID, rotated.RefreshToken, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("revoked grant refresh=%v, want invalid grant", err)
	}
	// An unreachable Firestore endpoint must map to a transient verifier failure.
	emulatorHost, hadEmulatorHost := os.LookupEnv("FIRESTORE_EMULATOR_HOST")
	if hadEmulatorHost {
		if err := os.Unsetenv("FIRESTORE_EMULATOR_HOST"); err != nil {
			t.Fatal(err)
		}
	}
	restoreEmulatorHost := func() {
		if hadEmulatorHost {
			_ = os.Setenv("FIRESTORE_EMULATOR_HOST", emulatorHost)
		}
	}
	defer restoreEmulatorHost()
	unavailableClient, err := firestore.NewClient(ctx, "lwc-auth-unavailable-test",
		option.WithEndpoint("http://127.0.0.1:1"), option.WithoutAuthentication())
	restoreEmulatorHost()
	if err != nil {
		t.Fatal(err)
	}
	defer unavailableClient.Close()
	unavailableService := NewMCPOAuthServiceWithPolicy(unavailableClient, databaseEnvironment, "dev", "https://auth.dev.rayer.idv.tw", "https://bff.example.test", "https://wiki.example.test", "fixture-jwt-secret", resources)
	unavailableRouter := gin.New()
	unavailableRouter.POST("/verify", MCPOAuthCredentialVerifierHandlerWithValidator(unavailableService, verifierConfig, validateCaller))
	request := httptest.NewRequest(http.MethodPost, "/verify", nil)
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request = request.WithContext(requestContext)
	request.Header.Set("Authorization", "Bearer fixture-caller-id-token")
	request.Header.Set("X-LWC-User-Credential", tokens.AccessToken)
	response := httptest.NewRecorder()
	unavailableRouter.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable Firestore verifier HTTP status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMCPRedirectAndPKCEValidation(t *testing.T) {
	for _, test := range []struct {
		uri  string
		want bool
	}{
		{"http://127.0.0.1:43123/callback", true},
		{"http://localhost:43123/callback", true},
		{"http://[::1]:43123/callback", true},
		{"http://127.0.0.1/callback", false},
		{"http://example.test:43123/callback", false},
		{"http://127.0.0.1:43123/callback#fragment", false},
		{"http://127.0.0.1:43123/callback*", false},
		{"https://client.example.test/callback", true},
	} {
		if got := validMCPOAuthRedirectURI(test.uri); got != test.want {
			t.Errorf("validMCPOAuthRedirectURI(%q) = %t, want %t", test.uri, got, test.want)
		}
	}
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !validMCPCodeChallenge(challenge) || !verifyMCPCodeVerifier(challenge, verifier) ||
		verifyMCPCodeVerifier(challenge, strings.Repeat("b", 43)) {
		t.Fatal("S256 code challenge/verifier validation did not bind the verifier")
	}
}
