package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCLIPairingApprovalIssuesOneDurableCLIGrant(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, secret = "cli-pairing-user", "cli-pairing-jwt-secret"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, map[string]interface{}{"status": AccountActive, "auth_version": int64(0), "role": "member"}); err != nil {
		t.Fatal(err)
	}
	sessions := NewRefreshSessionAuthority(fs, "lwc-346-pairing")
	service := NewCLIAuthService(fs, sessions, secret, "https://wiki.example.test")
	current := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return current }

	start, err := service.StartPairing(ctx, "macbook")
	if err != nil {
		t.Fatal(err)
	}
	if len(start.UserCode) != 8 || start.PollIntervalSeconds != 3 || !strings.HasPrefix(start.VerificationURL, "https://wiki.example.test/cli-pairing?user_code=") {
		t.Fatalf("pairing response = %#v", start)
	}
	if !start.ExpiresAt.Equal(current.Add(10 * time.Minute)) {
		t.Fatalf("pairing expiry = %s", start.ExpiresAt)
	}
	snapshot, err := service.pairingRef(start.PairingID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprint(snapshot.Data())
	if strings.Contains(stored, start.UserCode) || strings.Contains(stored, start.PollingSecret) {
		t.Fatalf("pairing record persisted raw code or polling secret: %s", stored)
	}
	if snapshot.Data()["poll_secret_hash"] != hashRefreshToken(start.PollingSecret) {
		t.Fatalf("polling secret verifier not stored as hash: %#v", snapshot.Data()["poll_secret_hash"])
	}
	audits, err := fs.Collection("auth_audit_events").Where("pairing_id", "==", start.PairingID).Documents(ctx).GetAll()
	if err != nil || len(audits) != 1 {
		t.Fatalf("pairing start audit count=%d error=%v", len(audits), err)
	}
	for _, forbidden := range []string{"user_code", "polling_secret", "access_token", "refresh_token", "token_hash"} {
		if _, ok := audits[0].Data()[forbidden]; ok {
			t.Fatalf("pairing audit stored secret/detail field %q: %#v", forbidden, audits[0].Data())
		}
	}

	pending, err := service.PollPairing(ctx, start.PairingID, start.PollingSecret)
	if err != nil || pending.Status != cliPairingStatusPending || pending.Credentials != nil {
		t.Fatalf("pending poll=%#v error=%v", pending, err)
	}
	if err := service.DecidePairing(ctx, start.UserCode, "approve", userID); err != nil {
		t.Fatal(err)
	}
	approved, err := service.PollPairing(ctx, start.PairingID, start.PollingSecret)
	if err != nil || approved.Status != cliPairingStatusRedeemed || approved.Credentials == nil {
		t.Fatalf("approved poll=%#v error=%v", approved, err)
	}
	credential := approved.Credentials
	claims, err := ValidateToken(credential.AccessToken, secret)
	if err != nil || claims.ClientKind != cliClientKind || claims.SessionID != credential.SessionID || claims.Sub != userID {
		t.Fatalf("approved CLI access claims=%#v error=%v", claims, err)
	}
	if err := sessions.VerifyCLIAccessSession(ctx, userID, credential.SessionID, 0); err != nil {
		t.Fatalf("approved durable session verification = %v", err)
	}
	if _, err := service.PollPairing(ctx, start.PairingID, start.PollingSecret); !errors.Is(err, ErrCLIPairingRedeemed) {
		t.Fatalf("replayed pairing poll error = %v, want already redeemed", err)
	}
}

func TestCLIPairingDenyExpiryAndPollingSecretAttemptLimit(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, secret = "cli-pairing-limits-user", "cli-pairing-limits-secret"
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, map[string]interface{}{"status": AccountActive, "auth_version": int64(0), "role": "member"}); err != nil {
		t.Fatal(err)
	}
	service := NewCLIAuthService(fs, NewRefreshSessionAuthority(fs, "lwc-346-pairing-limits"), secret, "https://wiki.example.test")
	current := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return current }

	denied, err := service.StartPairing(ctx, "denied-device")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DecidePairing(ctx, denied.UserCode, "deny", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollPairing(ctx, denied.PairingID, denied.PollingSecret); !errors.Is(err, ErrCLIPairingDenied) {
		t.Fatalf("denied pairing poll error = %v, want denied", err)
	}

	expired, err := service.StartPairing(ctx, "expired-device")
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(11 * time.Minute)
	if _, err := service.PollPairing(ctx, expired.PairingID, expired.PollingSecret); !errors.Is(err, ErrCLIPairingExpired) {
		t.Fatalf("expired pairing poll error = %v, want expired", err)
	}

	locked, err := service.StartPairing(ctx, "locked-device")
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(-11 * time.Minute)
	for range cliPairingMaxPollSecretFailures {
		if _, err := service.PollPairing(ctx, locked.PairingID, "incorrect-private-secret"); !errors.Is(err, ErrCLIPairingInvalid) {
			t.Fatalf("bad polling secret error = %v, want invalid", err)
		}
	}
	if _, err := service.PollPairing(ctx, locked.PairingID, locked.PollingSecret); !errors.Is(err, ErrCLIPairingInvalid) {
		t.Fatalf("poll after secret failure limit error = %v, want invalid", err)
	}
}
