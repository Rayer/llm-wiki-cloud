package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/iterator"
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
	service := NewMCPOAuthService(fs, environment, "https://auth.example.test", "https://bff.example.test", "https://wiki.example.test", "fixture-jwt-secret")
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
