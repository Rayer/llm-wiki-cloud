package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestGoogleRegistrationMethodsCallbackMatrix(t *testing.T) {
	for _, email := range []bool{false, true} {
		for _, google := range []bool{false, true} {
			t.Run(fmt.Sprintf("email=%t/google=%t", email, google), func(t *testing.T) {
				testGoogleRegistrationCallback(t, &methodRegistrationGate{email: email, google: google}, google)
			})
		}
	}
	t.Run("settings read failure", func(t *testing.T) {
		testGoogleRegistrationCallback(t, &methodRegistrationGate{email: true, google: true, err: errors.New("settings unavailable")}, false)
	})
}

func testGoogleRegistrationCallback(t *testing.T, gate *methodRegistrationGate, allowed bool) {
	t.Helper()
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, client, repo, gate, server)
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	unique := fmt.Sprintf("lwc324-%d", time.Now().UnixNano())
	email := unique + "@example.test"
	state, browser, code := unique+"-state", unique+"-browser", unique+"-code"
	transaction := seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
	provider.mu.Lock()
	provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: unique, email: email, emailVerified: true}
	provider.mu.Unlock()
	beforeUsers, err := client.Collection("users").Documents(context.Background()).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	beforeProjects, err := client.CollectionGroup("projects").Documents(context.Background()).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	beforeSessions, err := client.Collection(refreshSessionsCollection).Documents(context.Background()).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
	if rec.Code != http.StatusFound || len(gate.calls) != 1 || gate.calls[0] != "google" {
		t.Fatalf("callback=%d gate=%v", rec.Code, gate.calls)
	}
	identity, err := repo.GetExternalIdentity(context.Background(), googleProvider, service.cfg.Issuer, unique)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := repo.GetCanonicalEmailReservation(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		if identity == nil || reservation == nil || responseCookie(rec, "__Host-lwc_refresh") == nil {
			t.Fatal("allowed signup missing identity/reservation/session")
		}
		project, err := client.Collection("users").Doc(identity.UserID).Collection("projects").Doc(defaultProjectID).Get(context.Background())
		if err != nil || !project.Exists() {
			t.Fatalf("missing default project: %v", err)
		}
		// A bound subject keeps signing in even when configuration is unavailable.
		gate.err = errors.New("settings unavailable after signup")
		state, browser, code = unique+"-return-state", unique+"-return-browser", unique+"-return-code"
		transaction = seedOAuthTransaction(t, service, state, browser, OAuthFlowLogin, "", "")
		provider.mu.Lock()
		provider.tokens[code] = fakeGoogleToken{nonce: transaction.Nonce, subject: unique, email: email, emailVerified: true}
		provider.mu.Unlock()
		rec, _ = oauthCallbackRequest(t, service, state, browser, code, OAuthFlowLogin)
		if responseCookie(rec, "__Host-lwc_refresh") == nil || len(gate.calls) != 1 {
			t.Fatal("existing Google login consulted signup config or failed")
		}
	} else {
		afterUsers, err := client.Collection("users").Documents(context.Background()).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		afterProjects, err := client.CollectionGroup("projects").Documents(context.Background()).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		afterSessions, err := client.Collection(refreshSessionsCollection).Documents(context.Background()).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		if identity != nil || reservation != nil || responseCookie(rec, "__Host-lwc_refresh") != nil || len(afterUsers) != len(beforeUsers) || len(afterProjects) != len(beforeProjects) || len(afterSessions) != len(beforeSessions) {
			t.Fatal("denied signup created user/identity/reservation/session")
		}
	}
}
