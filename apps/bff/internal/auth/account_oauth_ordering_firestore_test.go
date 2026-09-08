package auth

import (
	"fmt"
	"testing"
	"time"
)

// Independent QA regression: require an actual overlap between coarse request
// time and the suspension commit. Zero observed overlaps cannot establish GREEN.
func TestIndependentQAOAuthConcurrentStartServerTimestampOrdering(t *testing.T) {
	fs := accountEmulator(t)
	repo := NewIdentityRepository(fs)
	p, provider := newFakeGoogleProvider(t)
	defer provider.Close()
	s := oauthEmulatorService(t, fs, repo, &fakeRegistrationGate{enabled: false}, provider)
	seedAccountTestUser(t, fs, "admin", "admin")
	input := ExternalUserProvisioning{UserID: "owner", DisplayEmail: "owner@example.test", CanonicalEmail: "owner@example.test", EmailVerified: true, Provider: googleProvider, Issuer: s.cfg.Issuer, Subject: "subject", ProjectID: defaultProjectID}
	if err := repo.ProvisionExternalUser(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	observed := 0
	for i := 0; i < 50; i++ {
		state, browser := fmt.Sprintf("race-%d", i), fmt.Sprintf("browser-%d", i)
		start := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-start
			value := AccountSuspended
			done <- UpdateAccount(t.Context(), fs, "admin", "owner", nil, &value)
		}()
		close(start)
		txn := seedOAuthTransaction(t, s, state, browser, OAuthFlowLogin, "", "")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		suspended, err := fs.Collection("users").Doc("owner").Get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		doc, err := fs.Collection(oauthTransactionsCollection).Doc("txn-" + state).Get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var user UserRecord
		suspended.DataTo(&user)
		value := AccountActive
		if err := UpdateAccount(t.Context(), fs, "admin", "owner", nil, &value); err != nil {
			t.Fatal(err)
		}
		if doc.CreateTime.After(user.AuthInvalidBefore) && doc.CreateTime.Before(suspended.UpdateTime) {
			observed++
			p.mu.Lock()
			p.tokens[state] = fakeGoogleToken{nonce: txn.Nonce, subject: input.Subject, email: input.DisplayEmail, emailVerified: true}
			p.mu.Unlock()
			w, _ := oauthCallbackRequest(t, s, state, browser, state, OAuthFlowLogin)
			t.Logf("start=%s suspend field=%s suspend commit=%s cookie=%v", doc.CreateTime.Format(time.RFC3339Nano), user.AuthInvalidBefore.Format(time.RFC3339Nano), suspended.UpdateTime.Format(time.RFC3339Nano), responseCookie(w, HostRefreshCookiePolicy().Name) != nil)
			if responseCookie(w, HostRefreshCookiePolicy().Name) != nil {
				cookie := responseCookie(w, HostRefreshCookiePolicy().Name)
				rotation, rotateErr := s.sessions.Rotate(t.Context(), cookie.Value, s.jwtSecret)
				t.Logf("revived callback refresh Rotate error=%v auth_version=%d", rotateErr, rotation.AuthVersion)
				if rotateErr != nil {
					t.Fatal("cookie was not usable", rotateErr)
				}
				t.Error("CONTRACT: callback created before suspension commit issued a usable session after restore")
				break
			}
		}
	}
	sessions, err := s.sessions.collection().Documents(t.Context()).GetAll()
	if err != nil || len(sessions) != 0 {
		t.Fatalf("rejected callbacks left durable sessions: count=%d err=%v", len(sessions), err)
	}
	t.Logf("observed starts between server field and suspension commit=%d; durable sessions=%d", observed, len(sessions))
	if observed == 0 {
		t.Fatal("ordering overlap not observed in 50 attempts; cannot claim GREEN")
	}
}
