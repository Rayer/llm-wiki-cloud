package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOAuthTransactionsAreLatestWinsAndSingleUse(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	service := NewGoogleOAuthService(GoogleConfig{}, client, NewIdentityRepository(client), nil, "secret")
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	ctx := context.Background()
	browser := "browser-correlation"
	for _, id := range []string{"first-transaction", "second-transaction", "expired-transaction"} {
		_, _ = client.Collection(oauthTransactionsCollection).Doc(id).Delete(ctx)
	}
	_, _ = client.Collection(oauthBrowsersCollection).Doc(hashOAuthValue(browser)).Delete(ctx)
	first := oauthTransaction{StateHash: hashOAuthValue("first-state"), BrowserHash: hashOAuthValue(browser), FlowKind: string(OAuthFlowLogin), Status: oauthStatusActive, CreatedAt: service.now(), ExpiresAt: service.now().Add(oauthTransactionTTL), Nonce: "nonce", PKCEVerifier: "verifier", RedirectURL: "https://auth.example.test/callback"}
	if err := service.createTransaction(ctx, "first-transaction", browser, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.StateHash = hashOAuthValue("second-state")
	if err := service.createTransaction(ctx, "second-transaction", browser, second); err != nil {
		t.Fatal(err)
	}
	var firstStored oauthTransaction
	if snapshot, err := client.Collection(oauthTransactionsCollection).Doc("first-transaction").Get(ctx); err != nil {
		t.Fatal(err)
	} else if err := snapshot.DataTo(&firstStored); err != nil {
		t.Fatal(err)
	}
	if firstStored.Status != oauthStatusSuperseded {
		t.Fatalf("superseded transaction status = %q", firstStored.Status)
	}
	if _, _, err := service.consumeTransaction(ctx, "first-state", browser, OAuthFlowLogin); !errors.Is(err, errOAuthSuperseded) {
		t.Fatalf("superseded consume error = %v", err)
	}
	if _, _, err := service.consumeTransaction(ctx, "second-state", "wrong-browser", OAuthFlowLogin); !errors.Is(err, errOAuthWrongBrowser) {
		t.Fatalf("wrong-browser consume error = %v", err)
	}
	if _, _, err := service.consumeTransaction(ctx, "second-state", browser, OAuthFlowLink); !errors.Is(err, errOAuthWrongKind) {
		t.Fatalf("wrong-kind consume error = %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := service.consumeTransaction(ctx, "second-state", browser, OAuthFlowLogin)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var consumed, replayed int
	for err := range errs {
		if err == nil {
			consumed++
		} else if errors.Is(err, errOAuthReplay) {
			replayed++
		} else {
			t.Fatalf("concurrent consume error = %v", err)
		}
	}
	if consumed != 1 || replayed != 1 {
		t.Fatalf("concurrent consume consumed=%d replayed=%d, want one each", consumed, replayed)
	}
	lock, err := client.Collection(oauthBrowsersCollection).Doc(hashOAuthValue(browser)).Get(ctx)
	if err == nil && lock.Exists() {
		t.Fatal("consumed transaction left a browser lock")
	}
}

func TestOAuthExpiredTransactionCannotBeConsumed(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	service := NewGoogleOAuthService(GoogleConfig{}, client, NewIdentityRepository(client), nil, "secret")
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	ctx := context.Background()
	_, _ = client.Collection(oauthTransactionsCollection).Doc("expired-transaction").Delete(ctx)
	_, _ = client.Collection(oauthBrowsersCollection).Doc(hashOAuthValue("expired-browser")).Delete(ctx)
	transaction := oauthTransaction{StateHash: hashOAuthValue("expired-state"), BrowserHash: hashOAuthValue("expired-browser"), FlowKind: string(OAuthFlowLogin), Status: oauthStatusActive, CreatedAt: service.now().Add(-oauthTransactionTTL), ExpiresAt: service.now().Add(-time.Second)}
	if err := service.createTransaction(ctx, "expired-transaction", "expired-browser", transaction); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.consumeTransaction(ctx, "expired-state", "expired-browser", OAuthFlowLogin); !errors.Is(err, errOAuthExpired) {
		t.Fatalf("expired consume error = %v", err)
	}
	var stored oauthTransaction
	snapshot, err := client.Collection(oauthTransactionsCollection).Doc("expired-transaction").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.DataTo(&stored); err != nil || stored.Status != oauthStatusExpired {
		t.Fatalf("expired transaction status = %q, decode error = %v", stored.Status, err)
	}
}
