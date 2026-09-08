package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestGoogleOAuthConfigRejectsIncompleteProviderConfiguration(t *testing.T) {
	if err := (GoogleConfig{ClientID: "client"}).Validate(); err == nil {
		t.Fatal("incomplete Google configuration was accepted")
	}
}

func TestGoogleOIDCVerifierValidatesRS256ClaimsAndRefreshesUnknownKidOnce(t *testing.T) {
	keyA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		key := keyA
		kid := "a"
		if refreshes > 1 {
			key, kid = keyB, "b"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid, "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	}))
	defer server.Close()

	cfg := GoogleConfig{ClientID: "client", Issuer: "https://issuer.example.test", JWKSURL: server.URL, TokenURL: server.URL}
	verifier := newGoogleOIDCVerifier(cfg, server.Client())
	now := time.Now().UTC()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://issuer.example.test", "sub": "subject", "aud": "client", "exp": now.Add(time.Minute).Unix(),
		"iat": now.Unix(), "nonce": "nonce", "email": "display@example.test", "email_verified": true,
	})
	token.Header["kid"] = "b"
	tokenString, err := token.SignedString(keyB)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(t.Context(), tokenString, "nonce")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "subject" || !claims.EmailVerified || refreshes != 2 {
		t.Fatalf("claims=%+v refreshes=%d, want subject/verified and exactly one unknown-kid refresh", claims, refreshes)
	}
}

func TestGoogleOIDCVerifierRejectsAlgorithmIssuerAudienceAzpNonceAndTime(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "alg": "RS256", "kid": "k", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	}))
	defer server.Close()
	cfg := GoogleConfig{ClientID: "client", Issuer: "https://issuer.example.test", JWKSURL: server.URL, TokenURL: server.URL}
	now := time.Now().UTC()
	base := jwt.MapClaims{"iss": cfg.Issuer, "sub": "subject", "aud": "client", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce"}
	tests := []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"issuer", func(c jwt.MapClaims) { c["iss"] = "https://other.example.test" }},
		{"missing subject", func(c jwt.MapClaims) { delete(c, "sub") }},
		{"empty subject", func(c jwt.MapClaims) { c["sub"] = "" }},
		{"audience", func(c jwt.MapClaims) { c["aud"] = "other" }},
		{"azp", func(c jwt.MapClaims) { c["azp"] = "other" }},
		{"multi audience missing azp", func(c jwt.MapClaims) { c["aud"] = []string{"client", "other"}; delete(c, "azp") }},
		{"multi audience wrong azp", func(c jwt.MapClaims) { c["aud"] = []string{"client", "other"}; c["azp"] = "other" }},
		{"nonce", func(c jwt.MapClaims) { c["nonce"] = "other" }},
		{"future iat", func(c jwt.MapClaims) { c["iat"] = now.Add(121 * time.Second).Unix() }},
		{"expired", func(c jwt.MapClaims) { c["exp"] = now.Add(-121 * time.Second).Unix() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := jwt.MapClaims{}
			for key, value := range base {
				claims[key] = value
			}
			test.mutate(claims)
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "k"
			raw, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			verifier := newGoogleOIDCVerifier(cfg, server.Client())
			verifier.now = func() time.Time { return now }
			if _, err := verifier.Verify(t.Context(), raw, "nonce"); err == nil {
				t.Fatalf("Verify() accepted %s", test.name)
			}
		})
	}
}

func TestGoogleOAuthStartRejectsCrossSiteInitiationWithoutCreatingTransaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := NewGoogleOAuthService(GoogleConfig{
		ClientID:         "client",
		ClientSecret:     "secret",
		Issuer:           "https://issuer.example.test",
		JWKSURL:          "https://issuer.example.test/keys",
		TokenURL:         "https://issuer.example.test/token",
		LoginRedirectURL: "https://auth.example.test/api/v1/auth/google/callback",
		LinkRedirectURL:  "https://auth.example.test/api/v1/auth/google/link/callback",
		CompletionURL:    "https://wiki.example.test/login",
	}, nil, nil, nil, "secret")
	router := gin.New()
	router.POST("/start", service.StartHandler(OAuthFlowLogin))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(`{"flow_kind":"login"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example.test")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site start status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if strings.Contains(recorder.Body.String(), "attacker.example.test") {
		t.Fatal("cross-site origin was reflected in the response")
	}
}

func TestGoogleOAuthFailureResponseDoesNotExposeSensitiveProviderValues(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeOAuthFailure(recorder, http.StatusBadRequest, oauthOutcomeInvalidRequest)
	body := recorder.Body.String()
	for _, secret := range []string{"code", "token", "secret", "email", "nonce", "state", "provider"} {
		if strings.Contains(strings.ToLower(body), secret) {
			t.Fatalf("failure body contains sensitive marker %q: %s", secret, body)
		}
	}
}

func TestGoogleOAuthAuthorizationURLUsesS256AndFixedRedirect(t *testing.T) {
	service := NewGoogleOAuthService(GoogleConfig{
		ClientID: "client", Issuer: "https://issuer.example.test", AuthorizationURL: "https://issuer.example.test/authorize",
		LoginRedirectURL: "https://auth.example.test/api/v1/auth/google/callback", LinkRedirectURL: "https://auth.example.test/api/v1/auth/google/link/callback",
		CompletionURL: "https://wiki.example.test/login",
	}, nil, nil, nil, "secret")
	raw, err := service.authorizationURL(OAuthFlowLink, "opaque-state", "opaque-nonce", "challenge")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") != "challenge" || query.Get("redirect_uri") != service.cfg.LinkRedirectURL || query.Get("prompt") != "select_account" {
		t.Fatalf("authorization query = %v", query)
	}
}

func TestGoogleOAuthTokenExchangeUsesStoredPKCEAndDoesNotRetry(t *testing.T) {
	calls := 0
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	service := NewGoogleOAuthService(GoogleConfig{ClientID: "client", ClientSecret: "secret", Issuer: "https://issuer.example.test", JWKSURL: "https://issuer.example.test/keys", TokenURL: server.URL, LoginRedirectURL: "https://auth.example.test/login", LinkRedirectURL: "https://auth.example.test/link", CompletionURL: "https://wiki.example.test/login"}, nil, nil, nil, "secret")
	service.client = server.Client()
	_, err := service.exchangeCode(t.Context(), oauthTransaction{PKCEVerifier: "verifier", RedirectURL: "https://auth.example.test/login"}, "code")
	if err == nil || calls != 1 {
		t.Fatalf("exchange error=%v calls=%d, want one bounded attempt", err, calls)
	}
	form, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("code_verifier") != "verifier" || form.Get("redirect_uri") != "https://auth.example.test/login" || form.Get("client_secret") != "secret" {
		t.Fatalf("token exchange form = %v", form)
	}
}

func TestGoogleOAuthProviderClientsRefuseRedirects(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()

	service := NewGoogleOAuthService(GoogleConfig{
		ClientID: "client", ClientSecret: "secret", Issuer: "https://issuer.example.test",
		JWKSURL: redirect.URL, TokenURL: redirect.URL,
		LoginRedirectURL: "https://auth.example.test/login", LinkRedirectURL: "https://auth.example.test/link",
		CompletionURL: "https://wiki.example.test/login",
	}, nil, nil, nil, "secret")
	if _, err := service.exchangeCode(t.Context(), oauthTransaction{PKCEVerifier: "verifier", RedirectURL: "https://auth.example.test/login"}, "code"); err == nil {
		t.Fatal("token exchange followed a provider redirect")
	}
	verifier := newGoogleOIDCVerifier(service.cfg, nil)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": service.cfg.Issuer, "sub": "subject", "aud": service.cfg.ClientID,
		"exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": "nonce",
	})
	token.Header["kid"] = "k"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), raw, "nonce"); err == nil {
		t.Fatal("JWKS fetch followed a provider redirect")
	}
	if redirected != 0 {
		t.Fatalf("redirect target received %d requests; provider secrets must not cross origins", redirected)
	}
}

func TestGoogleOIDCVerifierWarmCacheUnknownKidRefreshesOnce(t *testing.T) {
	keyA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[{"kty":"RSA","alg":"RS256","kid":"b","n":"`+base64.RawURLEncoding.EncodeToString(keyB.N.Bytes())+`","e":"AQAB"}]}`)
	}))
	defer server.Close()
	cfg := GoogleConfig{ClientID: "client", Issuer: "https://issuer.example.test", JWKSURL: server.URL, TokenURL: server.URL}
	verifier := newGoogleOIDCVerifier(cfg, server.Client())
	now := time.Now().UTC()
	verifier.now = func() time.Time { return now }
	verifier.keys = map[string]*rsa.PublicKey{"a": &keyA.PublicKey}
	verifier.fetchedAt = now
	verifier.freshUntil = now.Add(time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": cfg.Issuer, "sub": "subject", "aud": cfg.ClientID, "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce",
	})
	token.Header["kid"] = "b"
	raw, err := token.SignedString(keyB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), raw, "nonce"); err != nil {
		t.Fatalf("warm-cache Verify() error = %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("warm-cache unknown-kid refreshes=%d, want exactly one", refreshes)
	}
}

func TestGoogleOIDCVerifierRejectsWrongAlgorithmMalformedAndOversizedJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		body     string
		wrongAlg bool
	}{
		{name: "malformed keyset", body: `{"keys":[{"kty":"RSA","alg":"RS256","kid":"k","n":"%%%","e":"AQAB"}]}`},
		{name: "oversized modulus", body: `{"keys":[{"kty":"RSA","alg":"RS256","kid":"k","n":"` + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, maxRSAModulusBytes+1)) + `","e":"AQAB"}]}`},
		{name: "wrong algorithm", wrongAlg: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.wrongAlg {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			cfg := GoogleConfig{ClientID: "client", Issuer: "https://issuer.example.test", JWKSURL: server.URL, TokenURL: server.URL}
			verifier := newGoogleOIDCVerifier(cfg, server.Client())
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": cfg.Issuer, "sub": "subject", "aud": cfg.ClientID, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": "nonce"})
			if test.wrongAlg {
				token = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": cfg.Issuer, "sub": "subject", "aud": cfg.ClientID, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": "nonce"})
			}
			token.Header["kid"] = "k"
			var raw string
			if test.wrongAlg {
				raw, err = token.SignedString([]byte("wrong"))
			} else {
				raw, err = token.SignedString(key)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.Verify(t.Context(), raw, "nonce"); err == nil {
				t.Fatalf("Verify() accepted %s", test.name)
			}
		})
	}
}

func TestGoogleOAuthFailureEventsIncludeBoundedFlowKind(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	writeOAuthFailure(httptest.NewRecorder(), http.StatusBadRequest, oauthOutcomeCancelled, OAuthFlowLink)
	if !strings.Contains(output.String(), `"flow_kind":"link"`) || strings.Contains(output.String(), "nonce") {
		t.Fatalf("event log = %q", output.String())
	}
	output.Reset()
	writeOAuthFailure(httptest.NewRecorder(), http.StatusBadRequest, oauthOutcomeInvalidRequest)
	if !strings.Contains(output.String(), `"flow_kind":"unknown"`) {
		t.Fatalf("unknown event log = %q", output.String())
	}
}
