package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordConfiguredKeepsExplicitOAuthUnsetState(t *testing.T) {
	generatedHash := "generated-but-not-user-set"
	if isPasswordConfigured(&UserRecord{PasswordHash: generatedHash, PasswordConfigured: boolPointer(false)}) {
		t.Fatal("an explicit unset flag must win over a generated hash")
	}
	if !isPasswordConfigured(&UserRecord{PasswordHash: generatedHash}) {
		t.Fatal("legacy non-empty password hashes must remain configured")
	}
	if isPasswordConfigured(&UserRecord{PasswordConfigured: boolPointer(false)}) {
		t.Fatal("empty-hash OAuth accounts must remain unset")
	}
}

func TestUpdateOwnPasswordPreservesExistingCredentials(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	secret := "password-self-service-test-secret"
	oldPassword := "old-password-431"
	newPassword := "new-password-431"
	oldHash := passwordTestHash(t, oldPassword)
	before := UserRecord{
		Email: "owner@example.test", EmailCanonical: "owner@example.test", Role: "user",
		PasswordHash: oldHash, PasswordConfigured: boolPointer(true), AuthVersion: 3,
		EmailVerified: true, DefaultProject: "synthetic-project", ProjectCount: 2,
	}
	if _, err := fs.Collection("users").Doc("owner-431").Set(ctx, before); err != nil {
		t.Fatal(err)
	}
	accessToken, err := GenerateAccessToken("owner-431", "user", secret, 3)
	if err != nil {
		t.Fatal(err)
	}
	refreshToken, err := GenerateRefreshToken("owner-431", "user", secret, 3)
	if err != nil {
		t.Fatal(err)
	}

	passwordRouter := gin.New()
	passwordRouter.POST("/password", JWTAuthWithAccountLookup(config.Config{JWTSecret: secret}, FirestoreAccountLookup(fs)), WebOnly(), ChangePasswordHandler(fs, "demo-431"))
	postChange := func(currentPassword string, includeCurrent bool) *httptest.ResponseRecorder {
		payload := map[string]any{
			"new_password": newPassword, "confirm_password": newPassword,
			// Client supplied state must not grant the OAuth first-setup exception.
			"password_configured": false,
		}
		if includeCurrent {
			payload["current_password"] = currentPassword
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/password", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+accessToken)
		response := httptest.NewRecorder()
		passwordRouter.ServeHTTP(response, request)
		return response
	}
	for _, tc := range []struct {
		name            string
		currentPassword string
		includeCurrent  bool
		code            string
	}{
		{name: "missing proof", code: "current_password_required"},
		{name: "wrong proof", currentPassword: "wrong-password-431", includeCurrent: true, code: "current_password_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := postChange(tc.currentPassword, tc.includeCurrent)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("proof rejection status=%d body=%s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["code"] != tc.code {
				t.Fatalf("proof rejection body=%v err=%v", body, err)
			}
		})
	}
	unchanged, err := GetUser(ctx, fs, "owner-431")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PasswordHash != oldHash || unchanged.AuthVersion != before.AuthVersion {
		t.Fatalf("missing/wrong proof mutated the account: before=%+v after=%+v", before, unchanged)
	}
	loginBeforeChange := gin.New()
	loginBeforeChange.POST("/login", LoginHandlerWithRepository(NewIdentityRepository(fs), secret, LocalRefreshCookiePolicy()))
	oldLoginRequest := httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(`{"email":"owner@example.test","password":"`+oldPassword+`"}`))
	oldLoginResponse := httptest.NewRecorder()
	loginBeforeChange.ServeHTTP(oldLoginResponse, oldLoginRequest)
	if oldLoginResponse.Code != http.StatusOK {
		t.Fatalf("old password login after rejected proofs status=%d body=%s", oldLoginResponse.Code, oldLoginResponse.Body.String())
	}
	if response := postChange(oldPassword, true); response.Code != http.StatusOK {
		t.Fatalf("correct current password change status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := GetUser(ctx, fs, "owner-431")
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthVersion != before.AuthVersion || !after.AuthInvalidBefore.Equal(before.AuthInvalidBefore) {
		t.Fatalf("password maintenance invalidated credentials: version %d -> %d, cutoff %v -> %v", before.AuthVersion, after.AuthVersion, before.AuthInvalidBefore, after.AuthInvalidBefore)
	}
	if after.Email != before.Email || after.Role != before.Role || after.EmailVerified != before.EmailVerified || after.DefaultProject != before.DefaultProject || after.ProjectCount != before.ProjectCount {
		t.Fatalf("password maintenance changed unrelated account data: before=%+v after=%+v", before, after)
	}
	if !isPasswordConfigured(after) || bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(newPassword)) != nil {
		t.Fatal("new password was not saved as configured")
	}
	if bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(oldPassword)) == nil {
		t.Fatal("old password still authenticates")
	}
	if passwordProofHash(oldHash) == passwordProofHash(after.PasswordHash) {
		t.Fatal("the old Google-link password proof remains valid")
	}

	protected := gin.New()
	protected.GET("/protected", JWTAuthWithAccountLookup(config.Config{JWTSecret: secret}, FirestoreAccountLookup(fs)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("original access credential status=%d body=%s", response.Code, response.Body.String())
	}

	refresh := gin.New()
	refresh.POST("/refresh", RefreshHandlerWithCookiePolicy(fs, secret, LocalRefreshCookiePolicy()))
	request = httptest.NewRequest(http.MethodPost, "/refresh", nil)
	request.AddCookie(&http.Cookie{Name: LocalRefreshCookiePolicy().Name, Value: refreshToken})
	response = httptest.NewRecorder()
	refresh.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("original refresh credential status=%d body=%s", response.Code, response.Body.String())
	}

	login := gin.New()
	login.POST("/login", LoginHandlerWithRepository(NewIdentityRepository(fs), secret, LocalRefreshCookiePolicy()))
	request = httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(`{"email":"owner@example.test","password":"`+newPassword+`"}`))
	response = httptest.NewRecorder()
	login.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("new password login status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(`{"email":"owner@example.test","password":"`+oldPassword+`"}`))
	response = httptest.NewRecorder()
	login.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old password login status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthFirstPasswordAndDemoExclusion(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	generatedInitialHash := passwordTestHash(t, "unknown-generated-oauth-password")
	if _, err := fs.Collection("users").Doc("oauth-431").Set(ctx, UserRecord{
		Email: "oauth@example.test", EmailCanonical: "oauth@example.test", Role: "user",
		PasswordHash: generatedInitialHash, PasswordConfigured: boolPointer(false), AuthVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "oauth-password-self-service-test-secret"
	accessToken, err := GenerateAccessToken("oauth-431", "user", secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	passwordRouter := gin.New()
	passwordRouter.POST("/password", JWTAuthWithAccountLookup(config.Config{JWTSecret: secret}, FirestoreAccountLookup(fs)), WebOnly(), ChangePasswordHandler(fs, "demo-431"))
	postChange := func(currentPassword string, includeCurrent bool, newPassword string) *httptest.ResponseRecorder {
		payload := map[string]any{
			"password_configured": false,
			"new_password":        newPassword,
			"confirm_password":    newPassword,
		}
		if includeCurrent {
			payload["current_password"] = currentPassword
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/password", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+accessToken)
		response := httptest.NewRecorder()
		passwordRouter.ServeHTTP(response, request)
		return response
	}
	if response := postChange("", false, "oauth-first-password"); response.Code != http.StatusOK {
		t.Fatalf("OAuth first password setup status=%d body=%s", response.Code, response.Body.String())
	}
	configured, err := GetUser(ctx, fs, "oauth-431")
	if err != nil {
		t.Fatal(err)
	}
	if !isPasswordConfigured(configured) || bcrypt.CompareHashAndPassword([]byte(configured.PasswordHash), []byte("oauth-first-password")) != nil {
		t.Fatal("OAuth first password was not saved")
	}
	if response := postChange("", false, "missing-proof-password"); response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"current_password_required"`)) {
		t.Fatalf("configured OAuth password change without proof status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postChange("wrong-current-password", true, "wrong-proof-password"); response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"current_password_invalid"`)) {
		t.Fatalf("configured OAuth password change with wrong proof status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postChange("oauth-first-password", true, "oauth-second-password"); response.Code != http.StatusOK {
		t.Fatalf("configured OAuth subsequent password change status=%d body=%s", response.Code, response.Body.String())
	}
	configured, err = GetUser(ctx, fs, "oauth-431")
	if err != nil || bcrypt.CompareHashAndPassword([]byte(configured.PasswordHash), []byte("oauth-second-password")) != nil {
		t.Fatalf("OAuth subsequent password change was not saved: err=%v", err)
	}
	if _, err := fs.Collection("users").Doc("oauth-empty-431").Set(ctx, UserRecord{
		Email: "oauth-empty@example.test", EmailCanonical: "oauth-empty@example.test", Role: "user",
		PasswordConfigured: boolPointer(false), AuthVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateOwnPassword(ctx, fs, "oauth-empty-431", 1, "demo-431", "", passwordTestHash(t, "oauth-empty-first-password")); err != nil {
		t.Fatalf("OAuth first password setup from empty legacy hash: %v", err)
	}

	demoHash := passwordTestHash(t, "demo-shared-password")
	if _, err := fs.Collection("users").Doc("demo-431").Set(ctx, UserRecord{
		Email: "demo@example.test", Role: "user", PasswordHash: demoHash,
		PasswordConfigured: boolPointer(true), AuthVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	err = UpdateOwnPassword(ctx, fs, "demo-431", 0, "demo-431", "", passwordTestHash(t, "replacement-password"))
	if !errors.Is(err, ErrPasswordSelfServiceUnavailable) {
		t.Fatalf("Demo password change error=%v", err)
	}
	unchanged, err := GetUser(ctx, fs, "demo-431")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PasswordHash != demoHash || !isPasswordConfigured(unchanged) {
		t.Fatal("Demo password attempt changed hash or configured state")
	}

	admin := UserRecord{Email: "admin@example.test", Role: "admin", AuthVersion: 0}
	if _, err := fs.Collection("users").Doc("admin-431").Set(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin-431", "oauth-431", nil, strptr(AccountSuspended)); err != nil {
		t.Fatal(err)
	}
	err = UpdateOwnPassword(ctx, fs, "oauth-431", 1, "demo-431", "oauth-second-password", passwordTestHash(t, "stale-session-password"))
	if !errors.Is(err, ErrPasswordSessionInvalid) {
		t.Fatalf("suspended account password change error=%v", err)
	}
}

func TestConcurrentPasswordChangeRejectsStaleProof(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	oldPassword := "concurrent-old-password"
	oldHash := passwordTestHash(t, oldPassword)
	if _, err := fs.Collection("users").Doc("concurrent-431").Set(ctx, UserRecord{
		Email: "concurrent@example.test", Role: "user", PasswordHash: oldHash,
		PasswordConfigured: boolPointer(true), AuthVersion: 4,
	}); err != nil {
		t.Fatal(err)
	}
	newHashes := []string{
		passwordTestHash(t, "concurrent-first-new-password"),
		passwordTestHash(t, "concurrent-second-new-password"),
	}
	start := make(chan struct{})
	results := make(chan error, len(newHashes))
	var workers sync.WaitGroup
	for _, hash := range newHashes {
		workers.Add(1)
		go func(hash string) {
			defer workers.Done()
			<-start
			results <- UpdateOwnPassword(ctx, fs, "concurrent-431", 4, "demo-431", oldPassword, hash)
		}(hash)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, staleProofs := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCurrentPasswordInvalid):
			staleProofs++
		default:
			t.Fatalf("concurrent password update error=%v", err)
		}
	}
	if successes != 1 || staleProofs != 1 {
		t.Fatalf("concurrent result successes=%d stale-proof-rejections=%d, want 1 each", successes, staleProofs)
	}
	final, err := GetUser(ctx, fs, "concurrent-431")
	if err != nil {
		t.Fatal(err)
	}
	if final.AuthVersion != 4 {
		t.Fatalf("password change altered auth version: got %d", final.AuthVersion)
	}
	validCandidates := 0
	for _, password := range []string{"concurrent-first-new-password", "concurrent-second-new-password"} {
		if bcrypt.CompareHashAndPassword([]byte(final.PasswordHash), []byte(password)) == nil {
			validCandidates++
		}
	}
	if validCandidates != 1 {
		t.Fatalf("final hash matched %d concurrent candidates", validCandidates)
	}
}

func TestPasswordStatusReturnsTrustedDemoQualification(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	for id, user := range map[string]UserRecord{
		"personal-431": {Email: "owner@example.test", Role: "user", PasswordConfigured: boolPointer(true)},
		"demo-431":     {Email: "demo@example.test", Role: "user", PasswordConfigured: boolPointer(true)},
	} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id      string
		allowed bool
	}{{"personal-431", true}, {"demo-431", false}} {
		router := gin.New()
		router.GET("/status", func(c *gin.Context) { c.Set("userID", tc.id); PasswordSelfServiceStatusHandler(fs, "demo-431")(c) })
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s password status=%d body=%s", tc.id, response.Code, response.Body.String())
		}
		var got PasswordSelfServiceStatus
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Allowed != tc.allowed || !got.Configured {
			t.Fatalf("%s password status=%+v", tc.id, got)
		}
	}
}

func TestPasswordDemoRejectsDirectAndRefreshedAccessWithoutMutation(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const demoID, secret = "demo-password-route-431", "password-route-test-secret"
	demoHash := passwordTestHash(t, "demo-existing-password")
	before := UserRecord{Email: "demo@example.test", Role: "member", Status: AccountActive,
		PasswordHash: demoHash, PasswordConfigured: boolPointer(true), AuthVersion: 0}
	if _, err := fs.Collection("users").Doc(demoID).Set(ctx, before); err != nil {
		t.Fatal(err)
	}
	accessToken, err := GenerateAccessToken(demoID, before.Role, secret, before.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewRefreshSessionAuthority(fs, "demo-password-route-431")
	refreshToken, err := sessions.Issue(ctx, demoID, before.Role, secret, before.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	passwordRouter := gin.New()
	passwordRouter.POST("/password", JWTAuthWithAccountLookup(config.Config{JWTSecret: secret}, FirestoreAccountLookup(fs)), ChangePasswordHandler(fs, demoID))
	changePassword := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/password", bytes.NewBufferString(`{"new_password":"attempted-demo-password","confirm_password":"attempted-demo-password"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		passwordRouter.ServeHTTP(response, req)
		return response
	}
	if got := changePassword(accessToken).Code; got != http.StatusForbidden {
		t.Fatalf("direct Demo password request status=%d, want 403", got)
	}

	refreshRouter := gin.New()
	refreshRouter.POST("/refresh", RefreshHandlerWithSessionAuthority(sessions, secret, LocalRefreshCookiePolicy()))
	refreshRequest := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	refreshRequest.AddCookie(&http.Cookie{Name: LocalRefreshCookiePolicy().Name, Value: refreshToken})
	refreshResponse := httptest.NewRecorder()
	refreshRouter.ServeHTTP(refreshResponse, refreshRequest)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("Demo refresh status=%d body=%s", refreshResponse.Code, refreshResponse.Body.String())
	}
	var refreshed RefreshResponse
	if err := json.Unmarshal(refreshResponse.Body.Bytes(), &refreshed); err != nil || refreshed.AccessToken == "" {
		t.Fatalf("Demo refresh response=%+v err=%v", refreshed, err)
	}
	if got := changePassword(refreshed.AccessToken).Code; got != http.StatusForbidden {
		t.Fatalf("post-refresh Demo password request status=%d, want 403", got)
	}
	after, err := GetUser(ctx, fs, demoID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != before.PasswordHash || isPasswordConfigured(after) != isPasswordConfigured(&before) ||
		after.AuthVersion != before.AuthVersion || !after.AuthInvalidBefore.Equal(before.AuthInvalidBefore) {
		t.Fatalf("Demo password requests mutated the account: before=%+v after=%+v", before, after)
	}
}

func passwordTestHash(t *testing.T, value string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}
