package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/iterator"
)

type fakeGoogleToken struct {
	nonce         string
	subject       string
	email         string
	emailVerified bool
}

type fakeGoogleProvider struct {
	key *rsa.PrivateKey

	mu     sync.Mutex
	calls  int
	tokens map[string]fakeGoogleToken
}

func newFakeGoogleProvider(t *testing.T) (*fakeGoogleProvider, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeGoogleProvider{key: key, tokens: make(map[string]fakeGoogleToken)}
	return provider, httptest.NewServer(provider)
}

func (p *fakeGoogleProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/keys":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "g2-test",
			"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": "AQAB",
		}}})
	case "/token":
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxOAuthBodyBytes+1))
		values, err := url.ParseQuery(string(body))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		code := values.Get("code")
		p.mu.Lock()
		p.calls++
		token, ok := p.tokens[code]
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		claims := jwt.MapClaims{
			"iss": "https://issuer.example.test", "sub": token.subject, "aud": "g2-client",
			"exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": token.nonce,
			"email": token.email, "email_verified": token.emailVerified,
		}
		idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		idToken.Header["kid"] = "g2-test"
		raw, err := idToken.SignedString(p.key)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *fakeGoogleProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func oauthEmulatorService(t *testing.T, client *firestore.Client, repo *IdentityRepository, gate RegistrationGate, provider *httptest.Server) *GoogleOAuthService {
	t.Helper()
	cfg := GoogleConfig{
		ClientID: "g2-client", ClientSecret: "g2-secret", Issuer: "https://issuer.example.test",
		JWKSURL: provider.URL + "/keys", TokenURL: provider.URL + "/token",
		AuthorizationURL: provider.URL + "/authorize",
		LoginRedirectURL: "https://auth.example.test/api/v1/auth/google/login/callback",
		LinkRedirectURL:  "https://auth.example.test/api/v1/auth/google/link/callback",
		CompletionURL:    "https://wiki.example.test/login", AllowedOrigins: []string{"https://wiki.example.test"},
	}
	sessions := NewRefreshSessionAuthority(client, "lwc-320-oauth-flow")
	service := NewGoogleOAuthServiceWithSessionAuthority(cfg, client, repo, gate, "g2-key-320", sessions)
	service.client = provider.Client()
	service.verifier = newGoogleOIDCVerifier(cfg, service.client)
	return service
}

func seedOAuthTransaction(t *testing.T, service *GoogleOAuthService, state, browser string, kind OAuthFlowKind, userID, proofHash string) oauthTransaction {
	t.Helper()
	now := service.now().UTC()
	transaction := oauthTransaction{
		StateHash: hashOAuthValue(state), BrowserHash: hashOAuthValue(browser), FlowKind: string(kind),
		Nonce: "nonce-" + state, PKCEVerifier: "verifier-" + state, RedirectURL: service.redirectURL(kind),
		UserID: userID, PasswordProofHash: proofHash, Status: oauthStatusActive,
		CreatedAt: now, ExpiresAt: now.Add(oauthTransactionTTL),
	}
	if err := service.createTransaction(context.Background(), "txn-"+state, browser, transaction); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func oauthCallbackRequest(t *testing.T, service *GoogleOAuthService, state, browser, code string, kind OAuthFlowKind) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	router := gin.New()
	router.GET("/callback", service.CallbackHandler(kind))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	request.AddCookie(&http.Cookie{Name: oauthCookieName, Value: browser})
	router.ServeHTTP(recorder, request)
	return recorder, request
}

func transactionForState(t *testing.T, client *firestore.Client, state string) (string, oauthTransaction) {
	t.Helper()
	iter := client.Collection(oauthTransactionsCollection).Where("state_hash", "==", hashOAuthValue(state)).Limit(1).Documents(context.Background())
	defer iter.Stop()
	snapshot, err := iter.Next()
	if err != nil {
		t.Fatal(err)
	}
	var transaction oauthTransaction
	if err := snapshot.DataTo(&transaction); err != nil {
		t.Fatal(err)
	}
	return snapshot.Ref.ID, transaction
}

func countOAuthTransactions(t *testing.T, client *firestore.Client) int {
	t.Helper()
	iter := client.Collection(oauthTransactionsCollection).Documents(context.Background())
	defer iter.Stop()
	count := 0
	for {
		_, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				return count
			}
			t.Fatal(err)
		}
		count++
	}
}

func cleanupOAuthTransaction(t *testing.T, client *firestore.Client, transactionID, browser string) {
	t.Helper()
	ctx := context.Background()
	_, _ = client.Collection(oauthTransactionsCollection).Doc(transactionID).Delete(ctx)
	_, _ = client.Collection(oauthBrowsersCollection).Doc(hashOAuthValue(browser)).Delete(ctx)
}

func cleanupOAuthIdentity(t *testing.T, client *firestore.Client, issuer, subject string) {
	t.Helper()
	_, _ = client.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(googleProvider, issuer, subject)).Delete(context.Background())
}

func TestGoogleOAuthLoginCallbackJITProvisionAndReplayWithFakeProvider(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: true}, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	state, browser, code := "login-state-g2", "login-browser-g2", "login-code-g2"
	cleanupOAuthTransaction(t, client, "txn-"+state, browser)
	if existing, _ := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, "login-subject-g2"); existing != nil {
		cleanupExternalFixture(t, client, ExternalUserProvisioning{UserID: existing.UserID, CanonicalEmail: "jit.g2@example.test", Provider: googleProvider, Issuer: service.cfg.Issuer, Subject: "login-subject-g2"})
	}
	transaction := seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
	provider.mu.Lock()
	provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: "login-subject-g2", email: "jit.g2@example.test", emailVerified: true}
	provider.mu.Unlock()

	recorder, request := oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != service.cfg.CompletionURL {
		t.Fatalf("callback status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	refreshCookie := responseCookie(recorder, "__Host-lwc_refresh")
	if refreshCookie == nil {
		t.Fatal("login callback did not set refresh cookie")
	}
	refreshRouter := gin.New()
	refreshRouter.POST("/refresh", RefreshHandlerWithSessionAuthority(service.sessions, service.jwtSecret, HostRefreshCookiePolicy()))
	refresh := httptest.NewRecorder()
	refreshReq := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	refreshReq.AddCookie(refreshCookie)
	refreshRouter.ServeHTTP(refresh, refreshReq)
	var response RefreshResponse
	if refresh.Code != http.StatusOK || json.Unmarshal(refresh.Body.Bytes(), &response) != nil || response.AccessToken == "" || response.User.ID == "" || response.User.Email != "jit.g2@example.test" {
		t.Fatalf("refresh status=%d response=%+v body=%s", refresh.Code, response, refresh.Body.String())
	}
	if cookie := responseCookie(recorder, "__Host-lwc_refresh"); cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatalf("refresh cookie = %#v", cookie)
	}
	identity, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, "login-subject-g2")
	if err != nil || identity == nil || identity.UserID != response.User.ID {
		t.Fatalf("identity = %#v, error = %v", identity, err)
	}
	reservation, err := repo.GetCanonicalEmailReservation(context.Background(), "jit.g2@example.test")
	if err != nil || reservation == nil || reservation.UserID != response.User.ID {
		t.Fatalf("reservation = %#v, error = %v", reservation, err)
	}

	// The consumed transaction prevents a callback replay from reaching the provider.
	replay := httptest.NewRecorder()
	replayReq := request.Clone(request.Context())
	replayReq.Header = request.Header.Clone()
	replayRouter := gin.New()
	replayRouter.GET("/callback", service.CallbackHandler(OAuthFlowLogin))
	replayRouter.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusBadRequest || provider.callCount() != 1 {
		t.Fatalf("replay status=%d provider_calls=%d body=%s", replay.Code, provider.callCount(), replay.Body.String())
	}
}

func TestGoogleOAuthCrossSiteStartDoesNotWriteOrCallProvider(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: true}, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	before := countOAuthTransactions(t, client)
	router := gin.New()
	router.POST("/start", service.StartHandler(OAuthFlowLogin))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/start", bytes.NewBufferString(`{"flow_kind":"login"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example.test")
	router.ServeHTTP(recorder, request)
	if after := countOAuthTransactions(t, client); recorder.Code != http.StatusForbidden || after != before || provider.callCount() != 0 {
		t.Fatalf("cross-site status=%d transactions_before=%d transactions_after=%d provider_calls=%d body=%s", recorder.Code, before, after, provider.callCount(), recorder.Body.String())
	}
}

func TestGoogleOAuthExpiredCallbackDoesNotCallProvider(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: true}, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	state, browser, code := "expired-callback-g2", "expired-browser-g2", "expired-code-g2"
	cleanupOAuthTransaction(t, client, "txn-"+state, browser)
	now := service.now().UTC()
	transaction := oauthTransaction{
		StateHash: hashOAuthValue(state), BrowserHash: hashOAuthValue(browser), FlowKind: string(OAuthFlowLogin),
		Nonce: "expired-nonce-g2", PKCEVerifier: "expired-verifier-g2", RedirectURL: service.redirectURL(OAuthFlowLogin),
		Status: oauthStatusActive, CreatedAt: now.Add(-oauthTransactionTTL), ExpiresAt: now.Add(-time.Second),
	}
	if err := service.createTransaction(context.Background(), "txn-"+state, browser, transaction); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: "expired-subject-g2", email: "expired.g2@example.test", emailVerified: true}
	provider.mu.Unlock()
	recorder, _ := oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
	if recorder.Code != http.StatusGone || provider.callCount() != 0 {
		t.Fatalf("expired callback status=%d provider_calls=%d body=%s", recorder.Code, provider.callCount(), recorder.Body.String())
	}
	var stored oauthTransaction
	snapshot, err := client.Collection(oauthTransactionsCollection).Doc("txn-" + state).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.DataTo(&stored); err != nil || stored.Status != oauthStatusExpired {
		t.Fatalf("expired stored status=%q decode_error=%v", stored.Status, err)
	}
}

func TestGoogleOAuthProviderCancellationConsumesWithoutWritesOrSession(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: true}, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	state, browser := "cancelled-callback-g2", "cancelled-browser-g2"
	cleanupOAuthTransaction(t, client, "txn-"+state, browser)
	seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
	router := gin.New()
	router.GET("/callback", service.CallbackHandler(OAuthFlowLogin))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/callback?state="+url.QueryEscape(state)+"&error=access_denied&error_description=provider-private-detail", nil)
	request.AddCookie(&http.Cookie{Name: oauthCookieName, Value: browser})
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || provider.callCount() != 0 {
		t.Fatalf("cancelled callback status=%d provider_calls=%d body=%s", recorder.Code, provider.callCount(), recorder.Body.String())
	}
	if cookie := responseCookie(recorder, "__Host-lwc_refresh"); cookie != nil {
		t.Fatalf("cancelled callback issued refresh cookie %#v", cookie)
	}
	if strings.Contains(recorder.Body.String(), "provider-private-detail") {
		t.Fatal("provider cancellation detail leaked in response")
	}
	_, transaction := transactionForState(t, client, state)
	if transaction.Status != oauthStatusConsumed {
		t.Fatalf("cancelled transaction status=%q, want consumed", transaction.Status)
	}
}

func TestGoogleOAuthLinkStartCallbackCancelAndConfirmWithFreshProof(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(" correct-password "), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	userID := "g2-link-user"
	email := "g2-link@example.test"
	cleanupIdentityFixtures(t, client, userID, "", email)
	cleanupOAuthIdentity(t, client, "https://issuer.example.test", "cancel-subject-g2")
	cleanupOAuthIdentity(t, client, "https://issuer.example.test", "confirm-subject-g2")
	if err := repo.ProvisionPasswordUser(context.Background(), PasswordUserProvisioning{UserID: userID, DisplayEmail: email, CanonicalEmail: email, PasswordHash: string(passwordHash)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupIdentityFixtures(t, client, userID, "", email) })
	service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: true}, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

	// Link starts are same-site, authenticated, password-protected, and chooser-forced.
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userID", userID); c.Next() })
	router.POST("/start", service.StartHandler(OAuthFlowLink))
	transactionsBeforeBadPassword := countOAuthTransactions(t, client)
	badPassword := httptest.NewRecorder()
	badPasswordReq := httptest.NewRequest(http.MethodPost, "/start", bytes.NewBufferString(`{"current_password":"wrong-password"}`))
	badPasswordReq.Header.Set("Content-Type", "application/json")
	badPasswordReq.Header.Set("Origin", "https://wiki.example.test")
	router.ServeHTTP(badPassword, badPasswordReq)
	transactionsAfterBadPassword := countOAuthTransactions(t, client)
	if badPassword.Code != http.StatusUnauthorized || transactionsAfterBadPassword != transactionsBeforeBadPassword {
		t.Fatalf("wrong-password start status=%d transactions_before=%d transactions_after=%d body=%s", badPassword.Code, transactionsBeforeBadPassword, transactionsAfterBadPassword, badPassword.Body.String())
	}
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/start", bytes.NewBufferString(`{"current_password":" correct-password "}`))
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("Origin", "https://wiki.example.test")
	router.ServeHTTP(start, startReq)
	if start.Code != http.StatusFound {
		t.Fatalf("link start status=%d body=%s", start.Code, start.Body.String())
	}
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("prompt") != "select_account" || location.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("link authorization query = %v", location.Query())
	}
	state := location.Query().Get("state")
	oauthCookie, err := http.ParseSetCookie(start.Header().Get("Set-Cookie"))
	if err != nil || oauthCookie == nil || oauthCookie.Name != oauthCookieName {
		t.Fatalf("oauth cookie = %#v error=%v", oauthCookie, err)
	}
	_, transaction := transactionForState(t, client, state)
	provider.mu.Lock()
	provider.tokens["cancel-code-g2"] = fakeGoogleToken{nonce: transaction.Nonce, subject: "cancel-subject-g2", email: "cancel.g2@example.test", emailVerified: true}
	provider.mu.Unlock()
	cancelCallback, _ := oauthCallbackRequest(t, service, state, oauthCookie.Value, "cancel-code-g2", OAuthFlowLink)
	if cancelCallback.Code != http.StatusFound || cancelCallback.Header().Get("Location") != service.cfg.CompletionURL {
		t.Fatalf("cancel callback status=%d body=%s", cancelCallback.Code, cancelCallback.Body.String())
	}
	pendingCookie := responseCookie(cancelCallback, oauthPendingCookieName)
	if pendingCookie == nil || pendingCookie.Value == "" {
		t.Fatalf("pending cookie=%#v", pendingCookie)
	}
	readRouter := gin.New()
	readRouter.Use(func(c *gin.Context) { c.Set("userID", userID); c.Next() })
	readRouter.GET("/complete", service.CompletionReadHandler())
	read := httptest.NewRecorder()
	readReq := httptest.NewRequest(http.MethodGet, "/complete", nil)
	readReq.AddCookie(pendingCookie)
	readRouter.ServeHTTP(read, readReq)
	var pending oauthCompletionResponse
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &pending) != nil || pending.Status != OAuthConfirmationRequired || pending.ConfirmationID != pendingCookie.Value || pending.ProviderEmail != "cancel.g2@example.test" || pending.CurrentEmail != email {
		t.Fatalf("pending read status=%d response=%+v body=%s", read.Code, pending, read.Body.String())
	}
	cancelRouter := gin.New()
	cancelRouter.Use(func(c *gin.Context) { c.Set("userID", userID); c.Next() })
	cancelRouter.POST("/cancel", service.CancelLinkHandler())
	cancel := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodPost, "/cancel", bytes.NewBufferString(`{"confirmation_id":"`+pending.ConfirmationID+`","confirmed":true}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRouter.ServeHTTP(cancel, cancelReq)
	if cancel.Code != http.StatusOK || !strings.Contains(cancel.Body.String(), oauthStatusCancelled) {
		t.Fatalf("cancel confirmation status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	if cookie := responseCookie(cancel, "__Host-lwc_refresh"); cookie != nil {
		t.Fatalf("cancel changed current refresh session %#v", cookie)
	}
	if identity, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, "cancel-subject-g2"); err != nil || identity != nil {
		t.Fatalf("cancel identity=%#v error=%v", identity, err)
	}

	// A separate pending link proves explicit confirmation creates the identity once.
	state = "confirm-state-g2"
	browser := "confirm-browser-g2"
	cleanupOAuthTransaction(t, client, "txn-"+state, browser)
	transaction = seedOAuthTransaction(t, service, state, browser, OAuthFlowLink, userID, passwordProofHash(string(passwordHash)))
	provider.mu.Lock()
	provider.tokens["confirm-code-g2"] = fakeGoogleToken{nonce: transaction.Nonce, subject: "confirm-subject-g2", email: "confirm.g2@example.test", emailVerified: true}
	provider.mu.Unlock()
	callback, _ := oauthCallbackRequest(t, service, state, browser, "confirm-code-g2", OAuthFlowLink)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != service.cfg.CompletionURL {
		t.Fatalf("confirm callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	pendingCookie = responseCookie(callback, oauthPendingCookieName)
	if pendingCookie == nil || pendingCookie.Value == "" {
		t.Fatalf("confirm pending cookie=%#v", pendingCookie)
	}
	confirmRouter := gin.New()
	confirmRouter.Use(func(c *gin.Context) { c.Set("userID", userID); c.Next() })
	confirmRouter.POST("/confirm", service.ConfirmLinkHandler())
	confirm := httptest.NewRecorder()
	confirmReq := httptest.NewRequest(http.MethodPost, "/confirm", bytes.NewBufferString(`{"confirmation_id":"`+pendingCookie.Value+`"}`))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmRouter.ServeHTTP(confirm, confirmReq)
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), `"status":"linked"`) {
		t.Fatalf("confirm status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	identity, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, "confirm-subject-g2")
	if err != nil || identity == nil || identity.UserID != userID {
		t.Fatalf("confirmed identity=%#v error=%v", identity, err)
	}
	replay := httptest.NewRecorder()
	replayReq := httptest.NewRequest(http.MethodPost, "/confirm", bytes.NewBufferString(`{"confirmation_id":"`+pendingCookie.Value+`"}`))
	replayReq.Header.Set("Content-Type", "application/json")
	confirmRouter.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusConflict {
		t.Fatalf("confirmation replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestGoogleOAuthLoginFailureBranchesWriteNoIdentityBoundary(t *testing.T) {
	tests := []struct {
		name          string
		gate          bool
		seedPassword  bool
		email         string
		emailVerified bool
		wantStatus    int
		providerSub   string
	}{
		{name: "registration disabled", gate: false, email: "disabled.g2@example.test", emailVerified: true, wantStatus: http.StatusForbidden, providerSub: "disabled-subject-g2"},
		{name: "canonical email conflict", gate: true, seedPassword: true, email: "conflict.g2@example.test", emailVerified: true, wantStatus: http.StatusConflict, providerSub: "conflict-subject-g2"},
		{name: "unverified email", gate: true, email: "unverified.g2@example.test", emailVerified: false, wantStatus: http.StatusForbidden, providerSub: "unverified-subject-g2"},
		{name: "invalid email", gate: true, email: "invalid-email-g2", emailVerified: true, wantStatus: http.StatusForbidden, providerSub: "invalid-email-subject-g2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, client := newIdentityEmulatorRepository(t)
			defer client.Close()
			provider, server := newFakeGoogleProvider(t)
			defer server.Close()
			userID := "g2-conflict-user"
			if test.seedPassword {
				cleanupIdentityFixtures(t, client, userID, "", test.email)
				hash, err := bcrypt.GenerateFromPassword([]byte("conflict-password"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.ProvisionPasswordUser(context.Background(), PasswordUserProvisioning{UserID: userID, DisplayEmail: test.email, CanonicalEmail: test.email, PasswordHash: string(hash)}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cleanupIdentityFixtures(t, client, userID, "", test.email) })
			}
			service := oauthEmulatorService(t, client, repo, &fakeRegistrationGate{enabled: test.gate}, server)
			service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
			state, browser, code := "failure-"+test.providerSub, "browser-"+test.providerSub, "code-"+test.providerSub
			cleanupOAuthTransaction(t, client, "txn-"+state, browser)
			transaction := seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
			provider.mu.Lock()
			provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: test.providerSub, email: test.email, emailVerified: test.emailVerified}
			provider.mu.Unlock()
			recorder, _ := oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
			// The fixed response intentionally does not contain the internal outcome.
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if cookie := responseCookie(recorder, "__Host-lwc_refresh"); cookie != nil {
				t.Fatalf("failure issued refresh cookie %#v", cookie)
			}
			identity, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, test.providerSub)
			if err != nil || identity != nil {
				t.Fatalf("failure identity=%#v error=%v", identity, err)
			}
			reservation, err := repo.GetCanonicalEmailReservation(context.Background(), test.email)
			if test.seedPassword {
				if err != nil || reservation == nil {
					t.Fatalf("seed reservation=%#v error=%v", reservation, err)
				}
			} else if err != nil || reservation != nil {
				t.Fatalf("failure reservation=%#v error=%v", reservation, err)
			}
		})
	}
}

func responseCookie(recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
