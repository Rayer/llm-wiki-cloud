package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

func TestCLISessionAuthorityPersistsRotatesAndRevokesWithoutIdleExpiry(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, jwtSecret, environment = "cli-session-user", "cli-session-jwt-secret", "lwc-346-cli-session"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, map[string]interface{}{"status": AccountActive, "auth_version": int64(0), "role": "member"}); err != nil {
		t.Fatal(err)
	}
	authority := NewRefreshSessionAuthority(fs, environment)
	issued, err := authority.IssueCLISession(ctx, userID, "macbook", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ValidateToken(issued.AccessToken, jwtSecret)
	if err != nil || claims.ClientKind != cliClientKind || claims.SessionID != issued.SessionID || claims.AuthVersion != 0 {
		t.Fatalf("CLI access claims=%#v error=%v", claims, err)
	}
	if err := authority.VerifyCLIAccessSession(ctx, userID, issued.SessionID, 0); err != nil {
		t.Fatalf("verify issued session = %v", err)
	}

	snapshot, err := authority.sessionRef(issued.SessionID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snapshot.Data()
	if data["client_kind"] != cliClientKind || data["client_name"] != "macbook" {
		t.Fatalf("stored CLI metadata = %#v", data)
	}
	if _, exists := data["expires_at"]; exists {
		t.Fatalf("CLI session unexpectedly has an idle expiration: %#v", data["expires_at"])
	}
	if data["token_hash"] != hashRefreshToken(issued.RefreshToken) || data["token_hash"] == issued.RefreshToken {
		t.Fatalf("CLI refresh verifier is not a hash: %#v", data["token_hash"])
	}
	if removed, err := authority.CleanupExpired(ctx, time.Now().AddDate(10, 0, 0), 50); err != nil || removed != 0 {
		t.Fatalf("expired-session cleanup removed=%d error=%v, want live CLI session retained", removed, err)
	}

	webToken, err := authority.Issue(ctx, userID, "member", jwtSecret, 0)
	if err != nil {
		t.Fatal(err)
	}
	webClaims, err := parseRefreshToken(webToken, jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := authority.ListCLISessions(ctx, userID)
	if err != nil || len(listed) != 1 || listed[0].ID != issued.SessionID {
		t.Fatalf("CLI sessions = %#v error=%v; web session must be excluded", listed, err)
	}
	if err := authority.RevokeCLISession(ctx, userID, webClaims.SessionID); !errors.Is(err, ErrCLISessionNotFound) {
		t.Fatalf("revoke Web session error = %v, want not found", err)
	}

	rotated, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret)
	if err != nil || rotated.SessionID != issued.SessionID || rotated.RefreshToken == issued.RefreshToken {
		t.Fatalf("CLI refresh rotation=%#v error=%v", rotated, err)
	}
	retried, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret)
	if err != nil || retried.RefreshToken != rotated.RefreshToken {
		t.Fatalf("CLI refresh retry=%#v error=%v; retry must recover the same next token", retried, err)
	}
	if err := authority.VerifyCLIAccessSession(ctx, userID, rotated.SessionID, 0); err != nil {
		t.Fatalf("verify refreshed session = %v", err)
	}
	if err := authority.RevokeCLISession(ctx, userID, issued.SessionID); err != nil {
		t.Fatal(err)
	}
	audits, err := fs.Collection("auth_audit_events").Where("session_id", "==", issued.SessionID).Documents(ctx).GetAll()
	if err != nil || len(audits) != 1 {
		t.Fatalf("CLI session revoke audit count=%d error=%v", len(audits), err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "token_hash", "user_code", "polling_secret"} {
		if _, ok := audits[0].Data()[forbidden]; ok {
			t.Fatalf("CLI session audit stored secret field %q: %#v", forbidden, audits[0].Data())
		}
	}
	if err := authority.VerifyCLIAccessSession(ctx, userID, issued.SessionID, 0); !errors.Is(err, ErrCLISessionRevoked) {
		t.Fatalf("verify revoked access error = %v, want revoked", err)
	}
	if _, err := authority.RotateCLISession(ctx, rotated.RefreshToken, jwtSecret); !errors.Is(err, ErrCLISessionRevoked) {
		t.Fatalf("refresh revoked CLI session error = %v, want revoked", err)
	}
	listed, err = authority.ListCLISessions(ctx, userID)
	if err != nil || len(listed) != 1 || listed[0].Status != refreshSessionStatusRevoked {
		t.Fatalf("revoked CLI sessions = %#v error=%v", listed, err)
	}
}

func TestCLISessionRefreshConcurrentRetryReturnsSameGeneration(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, jwtSecret = "cli-session-race-user", "cli-session-race-secret"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, map[string]interface{}{"status": AccountActive, "auth_version": int64(0), "role": "member"}); err != nil {
		t.Fatal(err)
	}
	authority := NewRefreshSessionAuthority(fs, "lwc-346-cli-session-race")
	issued, err := authority.IssueCLISession(ctx, userID, "cli", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan CLISessionCredential, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			credential, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- credential
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	var first string
	successes := 0
	for credential := range results {
		successes++
		if first == "" {
			first = credential.RefreshToken
		} else if credential.RefreshToken != first {
			t.Fatalf("concurrent CLI rotations returned different generations: %q != %q", credential.RefreshToken, first)
		}
	}
	for err := range errorsFound {
		t.Fatalf("concurrent CLI rotation error = %v", err)
	}
	if successes != 2 || first == "" {
		t.Fatalf("concurrent CLI rotation successes=%d; both callers must recover one token", successes)
	}
}

func TestCLISessionRetryWindowAndRevocationBoundaries(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, jwtSecret = "cli-session-window-user", "cli-session-window-secret"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, map[string]interface{}{"status": AccountActive, "auth_version": int64(0), "role": "member"}); err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	authority := NewRefreshSessionAuthority(fs, "lwc-346-cli-session-window")
	authority.now = func() time.Time { return current }

	issued, err := authority.IssueCLISession(ctx, userID, "cli", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := authority.sessionRef(issued.SessionID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snapshot.Data()
	retryUntil, ok := data["retry_until"].(time.Time)
	if data["previous_token_hash"] != hashRefreshToken(issued.RefreshToken) || data["token_hash"] != hashRefreshToken(rotated.RefreshToken) || data["retry_token_nonce"] == "" || !ok || !retryUntil.Equal(current.Add(cliRefreshRetryWindow)) || data["rotation_generation"] != int64(1) {
		t.Fatalf("rotation retry metadata = %#v", data)
	}
	if strings.Contains(fmt.Sprint(data), issued.RefreshToken) || strings.Contains(fmt.Sprint(data), rotated.RefreshToken) {
		t.Fatalf("session document stored a raw refresh token: %#v", data)
	}
	retried, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret)
	if err != nil || retried.RefreshToken != rotated.RefreshToken {
		t.Fatalf("same-token retry=%#v error=%v", retried, err)
	}
	snapshot, err = authority.sessionRef(issued.SessionID).Get(ctx)
	retryUntil, ok = snapshot.Data()["retry_until"].(time.Time)
	if err != nil || !ok || !retryUntil.Equal(current.Add(cliRefreshRetryWindow)) {
		t.Fatalf("retry extended fixed window: data=%#v error=%v", snapshot.Data(), err)
	}

	current = current.Add(time.Second)
	second, err := authority.RotateCLISession(ctx, rotated.RefreshToken, jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RotateCLISession(ctx, issued.RefreshToken, jwtSecret); !errors.Is(err, ErrCLISessionReplay) {
		t.Fatalf("older generation retry error = %v, want replay", err)
	}
	if err := authority.RevokeCLISession(ctx, userID, issued.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RotateCLISession(ctx, rotated.RefreshToken, jwtSecret); !errors.Is(err, ErrCLISessionRevoked) {
		t.Fatalf("retry after revoke error = %v, want revoked", err)
	}
	if second.RefreshToken == rotated.RefreshToken {
		t.Fatal("next generation reused the current refresh token")
	}

	windowSession, err := authority.IssueCLISession(ctx, userID, "cli-window", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RotateCLISession(ctx, windowSession.RefreshToken, jwtSecret); err != nil {
		t.Fatal(err)
	}
	current = current.Add(cliRefreshRetryWindow)
	if _, err := authority.RotateCLISession(ctx, windowSession.RefreshToken, jwtSecret); !errors.Is(err, ErrCLISessionReplay) {
		t.Fatalf("expired retry window error = %v, want replay", err)
	}

	versionSession, err := authority.IssueCLISession(ctx, userID, "cli-version", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RotateCLISession(ctx, versionSession.RefreshToken, jwtSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("users").Doc(userID).Update(ctx, []firestore.Update{{Path: "auth_version", Value: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RotateCLISession(ctx, versionSession.RefreshToken, jwtSecret); !errors.Is(err, ErrCLISessionRevoked) {
		t.Fatalf("retry after auth-version change error = %v, want revoked", err)
	}
}
