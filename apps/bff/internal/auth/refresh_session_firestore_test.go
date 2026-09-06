package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

func TestRefreshSessionAuthorityRotatesAtomicallyAndStoresOnlyHashes(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	authority := NewRefreshSessionAuthorityWithConfig(client, SessionAuthorityConfig{
		Environment: "lwc-320-concurrency",
		Migration:   RefreshSessionMigrationDisabled,
	})
	token, err := authority.Issue(ctx, "session-user", "member", "session-key-320")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parseRefreshToken(token, "session-key-320")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := authority.sessionRef(claims.SessionID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snapshot.Data()
	if data["environment"] != "lwc-320-concurrency" || data["session_id"] != claims.SessionID {
		t.Fatalf("session identity = %#v", data)
	}
	if data["token_hash"] == token || data["token_hash"] != hashRefreshToken(token) {
		t.Fatalf("refresh token was persisted instead of its hash: %#v", data["token_hash"])
	}

	results := make(chan error, 2)
	rotated := make(chan string, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := authority.Rotate(ctx, token, "session-key-320")
			if err == nil {
				rotated <- result.Token
			}
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	close(rotated)
	successes, replays := 0, 0
	var next string
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRefreshSessionReplay) {
			replays++
		} else {
			t.Fatalf("concurrent rotation error = %v", err)
		}
	}
	for value := range rotated {
		next = value
	}
	if successes != 1 || replays != 1 || next == "" || next == token {
		t.Fatalf("concurrent rotation successes=%d replays=%d next=%q", successes, replays, next)
	}
	if _, err := authority.Rotate(ctx, token, "session-key-320"); !errors.Is(err, ErrRefreshSessionReplay) {
		t.Fatalf("replayed rotation error = %v, want ErrRefreshSessionReplay", err)
	}
}

func TestRefreshSessionAuthorityPersistsAcrossInstancesAndRestart(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	first := NewRefreshSessionAuthority(client, "lwc-320-restart")
	token, err := first.Issue(ctx, "restart-user", "admin", "restart-key-320")
	if err != nil {
		t.Fatal(err)
	}
	second := NewRefreshSessionAuthority(client, "lwc-320-restart")
	rotation, err := second.Rotate(ctx, token, "restart-key-320")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	restartedClient, err := firestore.NewClient(ctx, "lwc-315-test", option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer restartedClient.Close()
	third := NewRefreshSessionAuthority(restartedClient, "lwc-320-restart")
	if _, err := third.Rotate(ctx, rotation.Token, "restart-key-320"); err != nil {
		t.Fatalf("rotation after authority restart = %v", err)
	}
	if _, err := NewRefreshSessionAuthority(client, "lwc-320-other").Rotate(ctx, rotation.Token, "restart-key-320"); !errors.Is(err, ErrRefreshSessionInvalid) {
		t.Fatalf("cross-environment rotation error = %v, want invalid", err)
	}
}

func TestRefreshSessionAuthorityRevocationCleanupAndAccountInvalidation(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	authority := NewRefreshSessionAuthority(client, "lwc-320-lifecycle")
	logoutToken, err := authority.Issue(ctx, "logout-user", "member", "lifecycle-key-320")
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Revoke(ctx, logoutToken, "lifecycle-key-320"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Rotate(ctx, logoutToken, "lifecycle-key-320"); !errors.Is(err, ErrRefreshSessionRevoked) {
		t.Fatalf("revoked rotation error = %v, want revoked", err)
	}
	for range 2 {
		if _, err := authority.Issue(ctx, "invalidate-user", "member", "lifecycle-key-320"); err != nil {
			t.Fatal(err)
		}
	}
	if revoked, err := authority.RevokeUserSessions(ctx, "invalidate-user"); err != nil || revoked != 2 {
		t.Fatalf("account invalidation revoked=%d error=%v, want 2", revoked, err)
	}

	expired, err := authority.Issue(ctx, "cleanup-user", "member", "lifecycle-key-320")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parseRefreshToken(expired, "lifecycle-key-320")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.sessionRef(claims.SessionID).Update(ctx, []firestore.Update{{Path: "expires_at", Value: time.Now().UTC().Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Rotate(ctx, expired, "lifecycle-key-320"); !errors.Is(err, ErrRefreshSessionExpired) {
		t.Fatalf("expired rotation error = %v, want expired", err)
	}
	if removed, err := authority.CleanupExpired(ctx, time.Now().UTC(), 10); err != nil || removed != 1 {
		t.Fatalf("cleanup removed=%d error=%v, want one expired session", removed, err)
	}
	if _, err := authority.replayRef(claims.SessionID, hashRefreshToken(expired)).Get(ctx); err == nil {
		t.Fatal("cleanup left expired replay marker")
	}
}

func TestRefreshSessionAuthorityLegacyMigrationAndRollbackMode(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	legacy, err := GenerateRefreshToken("legacy-user", "member", "migration-key-320")
	if err != nil {
		t.Fatal(err)
	}
	disabled := NewRefreshSessionAuthorityWithConfig(client, SessionAuthorityConfig{Environment: "lwc-320-migration", Migration: RefreshSessionMigrationDisabled})
	if _, err := disabled.Rotate(ctx, legacy, "migration-key-320"); !errors.Is(err, ErrRefreshSessionInvalid) {
		t.Fatalf("rollback-mode legacy rotation error = %v, want invalid", err)
	}
	migrating := NewRefreshSessionAuthorityWithConfig(client, SessionAuthorityConfig{Environment: "lwc-320-migration", Migration: RefreshSessionMigrationLegacyReadThrough})
	rotation, err := migrating.Rotate(ctx, legacy, "migration-key-320")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rotation.Token, legacy) {
		t.Fatal("rotated token unexpectedly contains legacy token")
	}
	claims, err := parseRefreshToken(rotation.Token, "migration-key-320")
	if err != nil {
		t.Fatal(err)
	}
	data, err := migrating.sessionRef(claims.SessionID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data.Data()["token_hash"].(string), legacy) {
		t.Fatal("durable migration record contains raw refresh token")
	}
}
