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
	otherProjectID := projectID + "-other"
	otherProjectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocumentID(userID, otherProjectID))
	if _, err := userRef.Set(ctx, UserRecord{Email: "oauth@example.test", Status: AccountActive, AuthVersion: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "OAuth fixture project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := otherProjectRef.Set(ctx, map[string]any{"user_id": userID, "project_id": otherProjectID, "name": "Other OAuth fixture project"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = userRef.Delete(ctx)
		_, _ = projectRef.Delete(ctx)
		_, _ = otherProjectRef.Delete(ctx)
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
	details, err := service.ConsentDetails(ctx, userID, 0, transactionID)
	if err != nil || details.ProjectName != "OAuth fixture project" || details.Scope != writeScope || details.CanContinue {
		t.Fatalf("consent details = %+v, err=%v", details, err)
	}
	firstContinuation, err := service.ContinueConsent(ctx, userID, 0, transactionID)
	if err != nil || !firstContinuation.RequiresConsent || firstContinuation.RedirectURL != "" {
		t.Fatalf("first consent continuation=%+v err=%v, want interaction with no redirect", firstContinuation, err)
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
	initialGrants, err := service.ListGrants(ctx, userID, projectID)
	if err != nil || len(initialGrants) != 1 {
		t.Fatalf("initial grant list=%+v err=%v", initialGrants, err)
	}
	initialGrantID := initialGrants[0].ID
	if _, err := service.ExchangeCode(ctx, client.ClientID, code, client.RedirectURIs[0], verifier, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("authorization-code replay error = %v, want invalid", err)
	}

	beginConsent := func(consentClient mcpOAuthClientRecord, consentResource, scope, state string) (string, bool) {
		t.Helper()
		consentURL, err := service.BeginAuthorization(ctx, consentClient.ClientID, consentClient.RedirectURIs[0], "code", challenge, "S256", state, consentResource, scope)
		if err != nil {
			t.Fatalf("begin repeat authorization for %q: %v", scope, err)
		}
		parsed, err := url.Parse(consentURL)
		if err != nil {
			t.Fatalf("parse repeat consent URL: %v", err)
		}
		transactionID := parsed.Query().Get("transaction_id")
		details, err := service.ConsentDetails(ctx, userID, 0, transactionID)
		if err != nil {
			t.Fatalf("read repeat consent details: %v", err)
		}
		return transactionID, details.CanContinue
	}
	repeatTransaction, repeatEligible := beginConsent(client, resource, writeScope, "repeat-same-scope-state")
	if !repeatEligible {
		t.Fatal("existing same-scope grant was not reported eligible")
	}
	repeatDecision, err := service.ContinueConsent(ctx, userID, 0, repeatTransaction)
	if err != nil || repeatDecision.RequiresConsent {
		t.Fatalf("same-grant continuation=%+v err=%v", repeatDecision, err)
	}
	repeatCallback, err := url.Parse(repeatDecision.RedirectURL)
	if err != nil || repeatCallback.Query().Get("state") != "repeat-same-scope-state" {
		t.Fatalf("repeat callback did not preserve state: %q err=%v", repeatDecision.RedirectURL, err)
	}
	repeatTokens, err := service.ExchangeCode(ctx, client.ClientID, repeatCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil || repeatTokens.Scope != writeScope {
		t.Fatalf("repeat code exchange=%+v err=%v", repeatTokens, err)
	}

	subsetTransaction, subsetEligible := beginConsent(client, resource, mcpOAuthScope, "repeat-subset-state")
	if !subsetEligible {
		t.Fatal("broader grant was not reported eligible for the requested subset")
	}
	subsetDecision, err := service.ContinueConsent(ctx, userID, 0, subsetTransaction)
	if err != nil || subsetDecision.RequiresConsent {
		t.Fatalf("covered-scope continuation=%+v err=%v", subsetDecision, err)
	}
	subsetCallback, err := url.Parse(subsetDecision.RedirectURL)
	if err != nil || subsetCallback.Query().Get("state") != "repeat-subset-state" {
		t.Fatalf("subset callback did not preserve state: %q err=%v", subsetDecision.RedirectURL, err)
	}
	subsetTokens, err := service.ExchangeCode(ctx, client.ClientID, subsetCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil || subsetTokens.Scope != mcpOAuthScope {
		t.Fatalf("subset code exchange widened requested scope: response=%+v err=%v", subsetTokens, err)
	}
	subsetCredential, err := service.VerifyAccessToken(ctx, subsetTokens.AccessToken)
	if err != nil || strings.Join(subsetCredential.Scopes, " ") != mcpOAuthScope {
		t.Fatalf("subset token capabilities=%v err=%v", subsetCredential.Scopes, err)
	}

	narrowClient, err := service.RegisterClient(ctx, "Narrow OAuth client", []string{"http://127.0.0.1:43124/narrow-callback"})
	if err != nil {
		t.Fatalf("register narrow client: %v", err)
	}
	narrowInitialTransaction, narrowInitialEligible := beginConsent(narrowClient, resource, mcpOAuthScope, "narrow-initial-state")
	if narrowInitialEligible {
		t.Fatal("client without an existing grant was reported eligible")
	}
	narrowInitialDecision, err := service.DecideConsent(ctx, userID, 0, narrowInitialTransaction, true)
	if err != nil {
		t.Fatalf("approve narrow-client first consent: %v", err)
	}
	narrowCallback, err := url.Parse(narrowInitialDecision.RedirectURL)
	if err != nil {
		t.Fatalf("parse narrow-client callback: %v", err)
	}
	if _, err := service.ExchangeCode(ctx, narrowClient.ClientID, narrowCallback.Query().Get("code"), narrowClient.RedirectURIs[0], verifier, resource); err != nil {
		t.Fatalf("exchange narrow-client consent code: %v", err)
	}
	expandedTransaction, expandedEligible := beginConsent(narrowClient, resource, writeScope, "scope-expansion-state")
	if expandedEligible {
		t.Fatal("query-only grant was reported eligible for added raw-write scope")
	}
	expandedDecision, err := service.ContinueConsent(ctx, userID, 0, expandedTransaction)
	if err != nil || !expandedDecision.RequiresConsent || expandedDecision.RedirectURL != "" {
		t.Fatalf("added-scope continuation=%+v err=%v, want interaction with no code", expandedDecision, err)
	}

	otherResource := service.ResourceURL(otherProjectID)
	foreignResourceTransaction, foreignResourceEligible := beginConsent(client, otherResource, mcpOAuthScope, "foreign-resource-state")
	if foreignResourceEligible {
		t.Fatal("grant for another Project resource was reported eligible")
	}
	foreignResourceDecision, err := service.ContinueConsent(ctx, userID, 0, foreignResourceTransaction)
	if err != nil || !foreignResourceDecision.RequiresConsent || foreignResourceDecision.RedirectURL != "" {
		t.Fatalf("foreign-resource continuation=%+v err=%v", foreignResourceDecision, err)
	}

	otherClient, err := service.RegisterClient(ctx, "Other OAuth client", []string{"http://127.0.0.1:43125/other-callback"})
	if err != nil {
		t.Fatalf("register other client: %v", err)
	}
	foreignClientTransaction, foreignClientEligible := beginConsent(otherClient, resource, mcpOAuthScope, "foreign-client-state")
	if foreignClientEligible {
		t.Fatal("grant for another client was reported eligible")
	}
	foreignClientDecision, err := service.ContinueConsent(ctx, userID, 0, foreignClientTransaction)
	if err != nil || !foreignClientDecision.RequiresConsent || foreignClientDecision.RedirectURL != "" {
		t.Fatalf("foreign-client continuation=%+v err=%v", foreignClientDecision, err)
	}

	authority := NewMCPGrantAuthority(fs, environment, "https://bff.example.test")
	authority.SetClock(func() time.Time { return clock })
	identity, err := authority.AuthenticateAccessToken(ctx, tokens.AccessToken, projectID, resource)
	if err != nil || identity.UserID != userID || identity.ProjectID != projectID ||
		identity.Scope != writeScope || strings.Join(identity.Capabilities, " ") != writeScope {
		t.Fatalf("authenticate project access token: identity=%+v err=%v", identity, err)
	}
	if identity, err := authority.AuthenticateAccessToken(ctx, repeatTokens.AccessToken, projectID, resource); err != nil || identity.Scope != writeScope {
		t.Fatalf("authenticate continuation child before source revocation: identity=%+v err=%v", identity, err)
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
	postRotationTransaction, postRotationEligible := beginConsent(client, resource, writeScope, "post-rotation-continuation-state")
	if !postRotationEligible {
		t.Fatal("grant was not reusable after refresh rotation")
	}
	postRotationDecision, err := service.ContinueConsent(ctx, userID, 0, postRotationTransaction)
	if err != nil || postRotationDecision.RequiresConsent {
		t.Fatalf("post-rotation continuation=%+v err=%v", postRotationDecision, err)
	}
	postRotationCallback, err := url.Parse(postRotationDecision.RedirectURL)
	if err != nil || postRotationCallback.Query().Get("code") == "" {
		t.Fatalf("post-rotation continuation callback=%q err=%v", postRotationDecision.RedirectURL, err)
	}
	postRotationTokens, err := service.ExchangeCode(ctx, client.ClientID, postRotationCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil {
		t.Fatalf("exchange post-rotation continuation code: %v", err)
	}

	clock = clock.Add(mcpOAuthRetryWindow + time.Second)
	if _, err := service.Refresh(ctx, client.ClientID, tokens.RefreshToken, resource); !errors.Is(err, ErrMCPOAuthReplay) {
		t.Fatalf("out-of-window refresh replay error = %v, want replay", err)
	}
	if _, err := authority.AuthenticateAccessToken(ctx, rotated.AccessToken, projectID, resource); !errors.Is(err, ErrMCPOAuthDenied) {
		t.Fatalf("replayed refresh family remained usable: %v", err)
	}
	if _, err := authority.AuthenticateAccessToken(ctx, postRotationTokens.AccessToken, projectID, resource); !errors.Is(err, ErrMCPOAuthDenied) {
		t.Fatalf("continuation child survived source grant revocation: %v", err)
	}
	grants, err := service.ListGrants(ctx, userID, projectID)
	if err != nil || len(grants) != 5 {
		t.Fatalf("grant after replay = %+v, err=%v", grants, err)
	}
	initialGrantRevoked := false
	for _, grant := range grants {
		if grant.ID == initialGrantID {
			initialGrantRevoked = grant.Status == "revoked"
		} else if grant.ClientName == "Narrow OAuth client" {
			if grant.Status != "active" {
				t.Fatalf("independent first-consent grant changed status: %+v", grant)
			}
		} else if grant.Status != "revoked" {
			t.Fatalf("continuation grant did not inherit revoked origin status: %+v", grant)
		}
	}
	if !initialGrantRevoked {
		t.Fatalf("refresh replay did not revoke the initial grant %s: %+v", initialGrantID, grants)
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
	details, err := service.ConsentDetails(ctx, userID, 3, transactionID)
	if err != nil || details.ResourceKind != "fixed" || details.ResourceName != "Daily Brief" || details.ProjectID != "" || details.ProjectName != "" ||
		details.Resource != resource || details.Scope != "brief:manage" || details.CanContinue || len(details.ScopeDetails) != 1 || details.ScopeDetails[0].Name != "brief:manage" {
		t.Fatalf("fixed consent details = %+v err=%v", details, err)
	}
	if _, err := service.ConsentDetails(ctx, otherUserID, 0, transactionID); !errors.Is(err, ErrMCPOAuthDenied) {
		t.Fatalf("account switch read error=%v, want transaction identity rejection", err)
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
	grants, err := service.ListResourceGrants(ctx, userID, resource)
	if err != nil || len(grants) != 1 || grants[0].ProjectID != "" || grants[0].Resource != resource || grants[0].Scope != "brief:manage" {
		t.Fatalf("account-owned fixed grants=%+v err=%v", grants, err)
	}
	rootGrantID := grants[0].ID
	grantRef := mcpOAuthCollection(fs, databaseEnvironment, "grants").Doc(rootGrantID)
	grantDoc, err := grantRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grant mcpOAuthGrantRecord
	if mcpOAuthDecode(grantDoc.Data(), &grant) != nil || grant.ProjectID != "" {
		t.Fatalf("persisted fixed grant fabricated Project: %#v", grantDoc.Data())
	}
	nearExpiry := clock.Add(5 * time.Minute)
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: nearExpiry}}); err != nil {
		t.Fatalf("set source grant near expiry: %v", err)
	}
	gin.SetMode(gin.TestMode)
	webAccessToken, err := GenerateAccessToken(userID, "user", "fixture-jwt-secret", 3)
	if err != nil {
		t.Fatal(err)
	}
	consentRouter := gin.New()
	protectedConsent := consentRouter.Group("/")
	protectedConsent.Use(JWTAuthWithAccountLookup(config.Config{JWTSecret: "fixture-jwt-secret"}, FirestoreAccountLookup(fs)))
	protectedConsent.GET("/consent/:transactionID", MCPOAuthConsentDetailsHandler(service))
	protectedConsent.POST("/consent/:transactionID/decision", MCPOAuthConsentDecisionHandler(service, "fixture-jwt-secret"))
	serveConsent := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+webAccessToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		consentRouter.ServeHTTP(response, request)
		return response
	}
	repeatURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "brief-repeat-state", resource, "brief:manage")
	if err != nil {
		t.Fatalf("begin repeat fixed-resource authorization: %v", err)
	}
	repeatConsent, _ := url.Parse(repeatURL)
	repeatTransactionID := repeatConsent.Query().Get("transaction_id")
	detailsResponse := serveConsent(http.MethodGet, "/consent/"+repeatTransactionID, "")
	var repeatDetails MCPOAuthConsentDetails
	if detailsResponse.Code != http.StatusOK || json.Unmarshal(detailsResponse.Body.Bytes(), &repeatDetails) != nil || !repeatDetails.CanContinue {
		t.Fatalf("HTTP repeat consent details status=%d body=%s decoded=%+v", detailsResponse.Code, detailsResponse.Body.String(), repeatDetails)
	}
	repeatDecisionResponse := serveConsent(http.MethodPost, "/consent/"+repeatTransactionID+"/decision", `{"decision":"continue"}`)
	var repeatDecisionBody struct {
		RedirectURL     string `json:"redirect_url"`
		RequiresConsent bool   `json:"requires_consent"`
	}
	if repeatDecisionResponse.Code != http.StatusOK || json.Unmarshal(repeatDecisionResponse.Body.Bytes(), &repeatDecisionBody) != nil ||
		repeatDecisionBody.RequiresConsent || repeatDecisionBody.RedirectURL == "" {
		t.Fatalf("HTTP reusable-grant continuation status=%d body=%s decoded=%+v", repeatDecisionResponse.Code, repeatDecisionResponse.Body.String(), repeatDecisionBody)
	}
	repeatCallback, err := url.Parse(repeatDecisionBody.RedirectURL)
	if err != nil || repeatCallback.Query().Get("state") != "brief-repeat-state" || repeatCallback.Query().Get("code") == "" {
		t.Fatalf("HTTP continuation callback did not preserve state/PKCE code: %q err=%v", repeatDecisionBody.RedirectURL, err)
	}
	repeatTokens, err := service.ExchangeCode(ctx, client.ClientID, repeatCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
	if err != nil || repeatTokens.AccessToken == "" || repeatTokens.ExpiresIn != int(nearExpiry.Sub(clock).Seconds()) {
		t.Fatalf("near-expiry continuation exchange=%+v err=%v", repeatTokens, err)
	}
	if childCredential, err := service.VerifyAccessToken(ctx, repeatTokens.AccessToken); err != nil || childCredential.UserID != userID {
		t.Fatalf("near-expiry derived token should initially verify: credential=%+v err=%v", childCredential, err)
	}
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
	grants, err = service.ListResourceGrants(ctx, userID, resource)
	if err != nil || len(grants) != 2 {
		t.Fatalf("root and continuation grants=%+v err=%v", grants, err)
	}
	var childGrantID string
	for _, listed := range grants {
		if listed.ID != rootGrantID {
			childGrantID = listed.ID
		}
	}
	childDoc, err := mcpOAuthCollection(fs, databaseEnvironment, "grants").Doc(childGrantID).Get(ctx)
	if err != nil || childGrantID == "" {
		t.Fatalf("read continuation grant %q: %v", childGrantID, err)
	}
	var childGrant mcpOAuthGrantRecord
	if mcpOAuthDecode(childDoc.Data(), &childGrant) != nil || childGrant.ProjectID != "" ||
		childGrant.OriginGrantID != rootGrantID || !childGrant.ExpiresAt.Equal(nearExpiry) {
		t.Fatalf("continuation grant did not preserve source lineage/expiry: %#v", childDoc.Data())
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
	grantSnapshot, err := grantRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mcpOAuthDecode(grantSnapshot.Data(), &grant) != nil || !clock.Before(grant.ExpiresAt) {
		t.Fatalf("pre-expiry grant fixture is invalid: %#v", grant)
	}
	originalGrantExpiry := nearExpiry
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: clock.Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("expired grant with otherwise valid access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if response := verifyHTTP(repeatTokens.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("expired source grant allowed derived access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: originalGrantExpiry}}); err != nil {
		t.Fatal(err)
	}
	if response := verifyHTTP(rotated.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("restored active grant should accept the still-valid access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if response := verifyHTTP(repeatTokens.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("restored source grant should accept its still-valid derived access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	expiryCodeURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "source-expiry-after-issue", resource, "brief:manage")
	if err != nil {
		t.Fatalf("begin source-expiry code flow: %v", err)
	}
	expiryConsent, _ := url.Parse(expiryCodeURL)
	if details, err := service.ConsentDetails(ctx, userID, 3, expiryConsent.Query().Get("transaction_id")); err != nil || !details.CanContinue {
		t.Fatalf("source-expiry continuation eligibility=%+v err=%v", details, err)
	}
	expiryDecision, err := service.ContinueConsent(ctx, userID, 3, expiryConsent.Query().Get("transaction_id"))
	if err != nil {
		t.Fatalf("issue continuation code before source expiry: %v", err)
	}
	expiryCallback, err := url.Parse(expiryDecision.RedirectURL)
	if err != nil || expiryCallback.Query().Get("code") == "" {
		t.Fatalf("source-expiry continuation callback=%q err=%v", expiryDecision.RedirectURL, err)
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: clock.Add(-time.Second)}}); err != nil {
		t.Fatalf("expire source after code issue: %v", err)
	}
	if _, err := service.ExchangeCode(ctx, client.ClientID, expiryCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("code exchange after source expiry=%v, want invalid", err)
	}
	if _, err := service.Refresh(ctx, client.ClientID, repeatTokens.RefreshToken, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("derived refresh after source expiry=%v, want invalid", err)
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: originalGrantExpiry}}); err != nil {
		t.Fatalf("restore source grant after expiry control: %v", err)
	}

	raceURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "grant-race-state", resource, "brief:manage")
	if err != nil {
		t.Fatalf("begin grant-race authorization: %v", err)
	}
	raceConsent, _ := url.Parse(raceURL)
	raceTransactionID := raceConsent.Query().Get("transaction_id")
	raceDetailsResponse := serveConsent(http.MethodGet, "/consent/"+raceTransactionID, "")
	var raceDetails MCPOAuthConsentDetails
	if raceDetailsResponse.Code != http.StatusOK || json.Unmarshal(raceDetailsResponse.Body.Bytes(), &raceDetails) != nil || !raceDetails.CanContinue {
		t.Fatalf("pre-race eligibility HTTP details status=%d body=%s decoded=%+v", raceDetailsResponse.Code, raceDetailsResponse.Body.String(), raceDetails)
	}
	if _, err := userRef.Set(ctx, UserRecord{Email: "brief@example.test", Status: AccountSuspended, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContinueConsent(ctx, userID, 3, raceTransactionID); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("suspended-account continuation error=%v, want inactive", err)
	}
	if _, err := userRef.Set(ctx, UserRecord{Email: "brief@example.test", Status: AccountActive, AuthVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := userRef.Update(ctx, []firestore.Update{{Path: "auth_version", Value: int64(4)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContinueConsent(ctx, userID, 3, raceTransactionID); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("stale-version continuation error=%v, want inactive", err)
	}
	if _, err := userRef.Update(ctx, []firestore.Update{{Path: "auth_version", Value: int64(3)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: clock.Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	expiredContinuation, err := service.ContinueConsent(ctx, userID, 3, raceTransactionID)
	if err != nil || !expiredContinuation.RequiresConsent || expiredContinuation.RedirectURL != "" {
		t.Fatalf("expired-grant continuation=%+v err=%v, want interaction and no code", expiredContinuation, err)
	}
	if _, err := grantRef.Update(ctx, []firestore.Update{{Path: "expires_at", Value: originalGrantExpiry}}); err != nil {
		t.Fatal(err)
	}
	revokedCodeURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "source-revoked-after-issue", resource, "brief:manage")
	if err != nil {
		t.Fatalf("begin source-revocation code flow: %v", err)
	}
	revokedConsent, _ := url.Parse(revokedCodeURL)
	if details, err := service.ConsentDetails(ctx, userID, 3, revokedConsent.Query().Get("transaction_id")); err != nil || !details.CanContinue {
		t.Fatalf("source-revocation continuation eligibility=%+v err=%v", details, err)
	}
	revokedDecision, err := service.ContinueConsent(ctx, userID, 3, revokedConsent.Query().Get("transaction_id"))
	if err != nil {
		t.Fatalf("issue continuation code before source revocation: %v", err)
	}
	revokedCallback, err := url.Parse(revokedDecision.RedirectURL)
	if err != nil || revokedCallback.Query().Get("code") == "" {
		t.Fatalf("source-revocation continuation callback=%q err=%v", revokedDecision.RedirectURL, err)
	}
	if err := service.RevokeGrant(ctx, userID, 3, rootGrantID); err != nil {
		t.Fatalf("revoke source after code issue: %v", err)
	}
	if _, err := service.ExchangeCode(ctx, client.ClientID, revokedCallback.Query().Get("code"), client.RedirectURIs[0], verifier, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("code exchange after source revocation=%v, want invalid", err)
	}
	if response := verifyHTTP(repeatTokens.AccessToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked source grant allowed derived access token HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := service.Refresh(ctx, client.ClientID, repeatTokens.RefreshToken, resource); !errors.Is(err, ErrMCPOAuthInvalid) {
		t.Fatalf("derived refresh after source revocation=%v, want invalid", err)
	}
	if err := service.RevokeGrant(ctx, userID, 3, grants[0].ID); err != nil {
		t.Fatalf("owner revoke=%v", err)
	}
	revokedContinuationResponse := serveConsent(http.MethodPost, "/consent/"+raceTransactionID+"/decision", `{"decision":"continue"}`)
	var revokedContinuation struct {
		RedirectURL     string `json:"redirect_url"`
		RequiresConsent bool   `json:"requires_consent"`
	}
	if revokedContinuationResponse.Code != http.StatusOK || json.Unmarshal(revokedContinuationResponse.Body.Bytes(), &revokedContinuation) != nil ||
		!revokedContinuation.RequiresConsent || revokedContinuation.RedirectURL != "" {
		t.Fatalf("revoked-grant HTTP continuation status=%d body=%s decoded=%+v, want interaction and no code", revokedContinuationResponse.Code, revokedContinuationResponse.Body.String(), revokedContinuation)
	}
	if err := service.RevokeGrant(ctx, userID, 3, grants[0].ID); err != nil {
		t.Fatalf("idempotent owner revoke=%v", err)
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
