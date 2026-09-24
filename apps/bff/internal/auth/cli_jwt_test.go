package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
)

func TestAccountMiddlewareValidatesCLIClientKindAndSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lookup := func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Role: "member", AuthVersion: 3}, nil
	}
	var verifiedUser, verifiedSession string
	var verifiedVersion int64
	verify := func(_ context.Context, userID, sessionID string, version int64) error {
		verifiedUser, verifiedSession, verifiedVersion = userID, sessionID, version
		return nil
	}
	webToken, err := GenerateAccessToken("user-1", "member", "test-secret", 3)
	if err != nil {
		t.Fatal(err)
	}
	cliToken, err := GenerateCLIAccessToken("user-1", "member", "cli-session-1", "test-secret", 3)
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/protected", JWTAuthWithAccountLookupAndSessionVerifier(config.Config{JWTSecret: "test-secret"}, lookup, verify), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	web := performBearerRequest(t, router, webToken)
	if web.Code != http.StatusNoContent {
		t.Fatalf("web token status = %d, want %d", web.Code, http.StatusNoContent)
	}
	if verifiedUser != "" {
		t.Fatal("web token unexpectedly invoked the CLI session verifier")
	}

	cli := performBearerRequest(t, router, cliToken)
	if cli.Code != http.StatusNoContent {
		t.Fatalf("valid CLI token status = %d, want %d", cli.Code, http.StatusNoContent)
	}
	if verifiedUser != "user-1" || verifiedSession != "cli-session-1" || verifiedVersion != 3 {
		t.Fatalf("verifier args = (%q, %q, %d)", verifiedUser, verifiedSession, verifiedVersion)
	}
}

func TestOrdinaryAccountMiddlewareRejectsCLIWithoutSessionAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token, err := GenerateCLIAccessToken("user-1", "member", "cli-session-1", "test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string) (*UserRecord, error) { return &UserRecord{Role: "member"}, nil }
	for name, middleware := range map[string]gin.HandlerFunc{
		"account lookup": JWTAuthWithAccountLookup(config.Config{JWTSecret: "test-secret"}, lookup),
		"JWT only":       JWTAuth(config.Config{JWTSecret: "test-secret"}),
	} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.GET("/protected", middleware, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			recorder := performBearerRequest(t, router, token)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("ordinary middleware accepted CLI token: status=%d", recorder.Code)
			}
		})
	}
}

func TestCLISessionVerificationFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token, err := GenerateCLIAccessToken("user-1", "member", "cli-session-1", "test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string) (*UserRecord, error) { return &UserRecord{Role: "member"}, nil }
	tests := []struct {
		name   string
		verify CLIAccessSessionVerifier
		want   int
	}{
		{name: "revoked", verify: func(context.Context, string, string, int64) error { return ErrCLISessionRevoked }, want: http.StatusUnauthorized},
		{name: "authority unavailable", verify: func(context.Context, string, string, int64) error { return ErrCLISessionUnavailable }, want: http.StatusServiceUnavailable},
		{name: "verifier missing", want: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/protected", JWTAuthWithAccountLookupAndSessionVerifier(config.Config{JWTSecret: "test-secret"}, lookup, tc.verify), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			recorder := performBearerRequest(t, router, token)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

func TestCLISessionVerifierErrorClassification(t *testing.T) {
	if cliSessionHTTPStatus(ErrCLISessionRevoked) != http.StatusUnauthorized {
		t.Fatal("revoked CLI session must be unauthorized")
	}
	if cliSessionHTTPStatus(errors.New("firestore unavailable")) != http.StatusServiceUnavailable {
		t.Fatal("unknown verifier errors must fail closed as unavailable")
	}
}

func TestCLIRetryTokenDerivationIsDeterministicAndDomainSeparated(t *testing.T) {
	first, err := deriveCLIRetryToken("test-secret", "session-1", "nonce-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := deriveCLIRetryToken("test-secret", "session-1", "nonce-1", 1)
	if err != nil || retry != first {
		t.Fatalf("same rotation inputs produced token=%q error=%v, want %q", retry, err, first)
	}
	for _, tc := range []struct {
		name       string
		secret     string
		sessionID  string
		nonce      string
		generation uint64
	}{
		{name: "secret", secret: "other-secret", sessionID: "session-1", nonce: "nonce-1", generation: 1},
		{name: "session", secret: "test-secret", sessionID: "session-2", nonce: "nonce-1", generation: 1},
		{name: "nonce", secret: "test-secret", sessionID: "session-1", nonce: "nonce-2", generation: 1},
		{name: "generation", secret: "test-secret", sessionID: "session-1", nonce: "nonce-1", generation: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveCLIRetryToken(tc.secret, tc.sessionID, tc.nonce, tc.generation)
			if err != nil || got == first {
				t.Fatalf("different rotation scope produced token=%q error=%v", got, err)
			}
		})
	}
	if sessionID, valid := cliRefreshSessionID(first); !valid || sessionID != "session-1" {
		t.Fatalf("derived token session=%q valid=%v", sessionID, valid)
	}
}

func TestCLIControlPlaneOriginRequiresHTTPSExceptLoopback(t *testing.T) {
	for _, tc := range []struct {
		origin string
		valid  bool
	}{
		{origin: "https://auth.example.test", valid: true},
		{origin: "http://localhost:8080", valid: true},
		{origin: "http://127.0.0.1:8080", valid: true},
		{origin: "http://auth.example.test", valid: false},
		{origin: "ftp://auth.example.test", valid: false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			normalized := normalizeBindingHost(tc.origin)
			if tc.valid && normalized == "" {
				t.Fatalf("valid control-plane origin %q was rejected", tc.origin)
			}
			if !tc.valid && normalized != "" {
				t.Fatalf("unsafe control-plane origin %q normalized to %q", tc.origin, normalized)
			}
		})
	}
}

func TestCLIAccessChecksProjectOwnerAfterSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token, err := GenerateCLIAccessToken("user-1", "member", "cli-session-1", "test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string) (*UserRecord, error) { return &UserRecord{Role: "member"}, nil }
	var checkedProject string
	verify := func(context.Context, string, string, int64) error { return nil }
	authorize := func(_ context.Context, userID, projectID string) error {
		if userID != "user-1" {
			t.Fatalf("project authorizer user = %q", userID)
		}
		checkedProject = projectID
		return ErrProjectPermissionDenied
	}
	router := gin.New()
	router.GET("/protected", JWTAuthWithAccountLookupAndSessionVerifier(config.Config{JWTSecret: "test-secret"}, lookup, verify, authorize), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", "project-1")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || checkedProject != "project-1" {
		t.Fatalf("status=%d checked_project=%q, want forbidden project-1", recorder.Code, checkedProject)
	}
}

func TestWebAndCLIOnlyMiddlewareKeepApprovalWebBound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lookup := AccountLookup(func(context.Context, string) (*UserRecord, error) { return &UserRecord{Role: "user"}, nil })
	verify := CLIAccessSessionVerifier(func(context.Context, string, string, int64) error { return nil })
	webToken, err := GenerateAccessToken("web-user", "user", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	cliToken, err := GenerateCLIAccessToken("cli-user", "user", "session-1", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/decision", JWTAuthWithAccountLookupAndSessionVerifier(config.Config{JWTSecret: "test-secret"}, lookup, verify), WebOnly(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/decision", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if response := request(webToken); response.Code != http.StatusNoContent {
		t.Fatalf("Web decision status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(cliToken); response.Code != http.StatusForbidden {
		t.Fatalf("CLI credential reached Web pairing decision: status=%d body=%s", response.Code, response.Body.String())
	}
}

func performBearerRequest(t *testing.T, router http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, request)
	return recorder
}
