package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

func accountEmulator(t *testing.T) *firestore.Client {
	t.Helper()
	endpoint := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if endpoint == "" {
		t.Skip("local Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("account lifecycle tests require loopback emulator")
	}
	client, err := firestore.NewClient(context.Background(), fmt.Sprintf("lwc325-%d", time.Now().UnixNano()), option.WithEndpoint("http://"+endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func accountState(t *testing.T, fs *firestore.Client, id string) *UserRecord {
	t.Helper()
	user, err := GetUser(context.Background(), fs, id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}
func strptr(s string) *string { return &s }

func TestAccountSuspendRestorePreservesDataAndNeverRevivesSessions(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	seedAccountTestUser(t, fs, "admin", "admin")
	user := UserRecord{Email: "owner@example.test", Role: "user", PasswordHash: "retained", DefaultProject: "project", ProjectCount: 3}
	if _, err := fs.Collection("users").Doc("owner").Set(ctx, user); err != nil {
		t.Fatal(err)
	}
	project := fs.Collection("users").Doc("owner").Collection("projects").Doc("project")
	if _, err := project.Set(ctx, map[string]any{"name": "preserved"}); err != nil {
		t.Fatal(err)
	}
	sessions := NewRefreshSessionAuthority(fs, "test")
	old, err := sessions.Issue(ctx, "owner", "user", "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountSuspended)); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Rotate(ctx, old, "key"); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("suspended refresh: %v", err)
	}
	if _, err := sessions.Issue(ctx, "owner", "user", "key", 0); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("suspended issue: %v", err)
	}
	if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountActive)); err != nil {
		t.Fatal(err)
	}
	current := accountState(t, fs, "owner")
	if current.AuthVersion != 1 || current.AllowsVersion(0) || !current.AllowsVersion(1) {
		t.Fatalf("restored version: %+v", current)
	}
	if current.Role != user.Role || current.PasswordHash != user.PasswordHash || current.Email != user.Email || current.ProjectCount != 3 || current.DefaultProject != "project" {
		t.Fatal("restore changed retained fields")
	}
	if _, err := project.Get(ctx); err != nil {
		t.Fatal("project removed", err)
	}
	if _, err := sessions.Rotate(ctx, old, "key"); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("old refresh revived: %v", err)
	}
	if _, err := sessions.Issue(ctx, "owner", "user", "key", 0); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("stale login snapshot revived: %v", err)
	}
	fresh, err := sessions.Issue(ctx, "owner", "user", "key", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Rotate(ctx, fresh, "key"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAdminConcurrentSuspensionAndDemotionPreserveActiveAdmin(t *testing.T) {
	for _, mode := range []string{"suspend", "demote", "self-demote", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			fs := accountEmulator(t)
			ctx := context.Background()
			seedAccountTestUser(t, fs, "a", "admin")
			seedAccountTestUser(t, fs, "b", "admin")
			if err := UpdateAccount(ctx, fs, "a", "a", nil, strptr(AccountSuspended)); !errors.Is(err, ErrAccountSelfSuspension) {
				t.Fatalf("self suspension: %v", err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
				go func(i int, pair [2]string) {
					<-start
					if mode == "self-demote" {
						pair[1] = pair[0]
					}
					if mode == "demote" || mode == "self-demote" || (mode == "mixed" && i == 0) {
						results <- UpdateAccount(ctx, fs, pair[0], pair[1], strptr("user"), nil)
					} else {
						results <- UpdateAccount(ctx, fs, pair[0], pair[1], nil, strptr(AccountSuspended))
					}
				}(i, pair)
			}
			close(start)
			success := 0
			for range 2 {
				if err := <-results; err == nil {
					success++
				} else if !errors.Is(err, ErrAccountAdminRequired) && !errors.Is(err, ErrAccountLastAdmin) {
					t.Fatal(err)
				}
			}
			active := 0
			last := ""
			for _, id := range []string{"a", "b"} {
				u := accountState(t, fs, id)
				if u.Active() && u.Role == "admin" {
					active++
					last = id
				}
			}
			if active != 1 || success != 1 {
				t.Fatalf("active=%d successful=%d", active, success)
			}
			if err := UpdateAccount(ctx, fs, last, last, strptr("user"), nil); !errors.Is(err, ErrAccountLastAdmin) {
				t.Fatalf("last admin demotion: %v", err)
			}
		})
	}
}

func TestAccountSuspendRacesIssuanceAndRotation(t *testing.T) {
	for _, mode := range []string{"issue", "rotate"} {
		t.Run(mode, func(t *testing.T) {
			fs := accountEmulator(t)
			ctx := context.Background()
			seedAccountTestUser(t, fs, "admin", "admin")
			for attempt := 0; attempt < 6; attempt++ {
				id := fmt.Sprintf("owner-%d", attempt)
				seedAccountTestUser(t, fs, id, "user")
				sessions := NewRefreshSessionAuthority(fs, "race")
				old, err := sessions.Issue(ctx, id, "user", "key")
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				var token string
				var issueErr, suspendErr error
				go func() {
					defer wg.Done()
					<-start
					if mode == "issue" {
						token, issueErr = sessions.Issue(ctx, id, "user", "key", 0)
					} else {
						r, e := sessions.Rotate(ctx, old, "key")
						token, issueErr = r.Token, e
					}
				}()
				go func() {
					defer wg.Done()
					<-start
					suspendErr = UpdateAccount(ctx, fs, "admin", id, nil, strptr(AccountSuspended))
				}()
				close(start)
				wg.Wait()
				if suspendErr != nil {
					t.Fatal(suspendErr)
				}
				if issueErr != nil && !errors.Is(issueErr, ErrAccountUnavailable) {
					t.Fatal(issueErr)
				}
				if err := UpdateAccount(ctx, fs, "admin", id, nil, strptr(AccountActive)); err != nil {
					t.Fatal(err)
				}
				if token != "" {
					if _, err := sessions.Rotate(ctx, token, "key"); !errors.Is(err, ErrAccountUnavailable) {
						t.Fatalf("race token revived: %v", err)
					}
				}
				if _, err := sessions.Rotate(ctx, old, "key"); err == nil {
					t.Fatal("original token revived")
				}
			}
		})
	}
}

func TestSuspendedGoogleCallbackPreservesIdentityWithoutSession(t *testing.T) {
	googleCallbackAcrossSuspension(t, false)
}
func TestDelayedGoogleCallbackCannotCrossSuspendRestore(t *testing.T) {
	googleCallbackAcrossSuspension(t, true)
}
func googleCallbackAcrossSuspension(t *testing.T, restore bool) {
	fs := accountEmulator(t)
	ctx := context.Background()
	repo := NewIdentityRepository(fs)
	provider, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, fs, repo, &fakeRegistrationGate{enabled: false}, server)
	input := ExternalUserProvisioning{UserID: "google-owner", DisplayEmail: "owner@example.test", CanonicalEmail: "owner@example.test", EmailVerified: true, Provider: googleProvider, Issuer: service.cfg.Issuer, Subject: "subject", ProjectID: defaultProjectID}
	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatal(err)
	}
	seedAccountTestUser(t, fs, "admin", "admin")
	service.now = func() time.Time { return time.Now().Add(time.Hour) } // Deliberately skew runtime time; invalidation uses Firestore timestamps.
	transaction := seedOAuthTransaction(t, service, "suspend-state", "suspend-browser", OAuthFlowLogin, "", "")
	provider.mu.Lock()
	provider.tokens["suspend-code"] = fakeGoogleToken{nonce: transaction.Nonce, subject: input.Subject, email: input.DisplayEmail, emailVerified: true}
	provider.mu.Unlock()
	if err := UpdateAccount(ctx, fs, "admin", input.UserID, nil, strptr(AccountSuspended)); err != nil {
		t.Fatal(err)
	}
	expectedStatus := AccountSuspended
	if restore {
		if err := UpdateAccount(ctx, fs, "admin", input.UserID, nil, strptr(AccountActive)); err != nil {
			t.Fatal(err)
		}
		expectedStatus = AccountActive
	}
	recorder, _ := oauthCallbackRequest(t, service, "suspend-state", "suspend-browser", "suspend-code", OAuthFlowLogin)
	if recorder.Code != 302 {
		t.Fatalf("callback status=%d", recorder.Code)
	}
	if responseCookie(recorder, HostRefreshCookiePolicy().Name) != nil {
		t.Fatal("suspended callback issued refresh cookie")
	}
	sessions, err := service.sessions.collection().Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatal("suspended callback created session")
	}
	if u := accountState(t, fs, input.UserID); u.Status != expectedStatus || u.AuthVersion != 1 {
		t.Fatal("callback reactivated account")
	}
	identity, err := repo.GetExternalIdentity(ctx, input.Provider, input.Issuer, input.Subject)
	if err != nil || identity == nil || identity.UserID != input.UserID {
		t.Fatal("callback changed identity", err)
	}
	if restore {
		fresh := seedOAuthTransaction(t, service, "fresh-state", "fresh-browser", OAuthFlowLogin, "", "")
		provider.mu.Lock()
		provider.tokens["fresh-code"] = fakeGoogleToken{nonce: fresh.Nonce, subject: input.Subject, email: input.DisplayEmail, emailVerified: true}
		provider.mu.Unlock()
		result, _ := oauthCallbackRequest(t, service, "fresh-state", "fresh-browser", "fresh-code", OAuthFlowLogin)
		cookie := responseCookie(result, HostRefreshCookiePolicy().Name)
		if cookie == nil {
			t.Fatal("fresh Google login after restore did not issue refresh")
		}
		claims, err := parseRefreshToken(cookie.Value, service.jwtSecret)
		if err != nil || claims.AuthVersion != 1 {
			t.Fatalf("fresh Google version: %v", err)
		}
	}

}

func TestGooglePendingLinkCannotCrossSuspendRestore(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	seedAccountTestUser(t, fs, "admin", "admin")
	if _, err := fs.Collection("users").Doc("owner").Set(ctx, UserRecord{Email: "owner@example.test", Role: "user", PasswordHash: "retained"}); err != nil {
		t.Fatal(err)
	}
	service := &GoogleOAuthService{fs: fs, now: time.Now}
	id := "pending-link-before-suspension"
	pending := oauthPendingLink{UserID: "owner", Provider: googleProvider, Issuer: "issuer", Subject: "subject", PasswordProofHash: passwordProofHash("retained"), Status: oauthStatusActive, ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := fs.Collection(oauthPendingLinksCollection).Doc(id).Create(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountSuspended)); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountActive)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.confirmLink(ctx, "owner", id); !errors.Is(err, errOAuthPasswordChanged) {
		t.Fatalf("stale link confirmation: %v", err)
	}
	identities, err := fs.Collection(ExternalIdentitiesCollection).Documents(ctx).GetAll()
	if err != nil || len(identities) != 0 {
		t.Fatal("stale link wrote identity", err)
	}
}

func TestJITSessionCannotCrossProvisionThenSuspendRestore(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	repo := NewIdentityRepository(fs)
	_, server := newFakeGoogleProvider(t)
	defer server.Close()
	service := oauthEmulatorService(t, fs, repo, &fakeRegistrationGate{enabled: true}, server)
	seedAccountTestUser(t, fs, "admin", "admin")
	input := ExternalUserProvisioning{UserID: "jit-owner", DisplayEmail: "jit@example.test", CanonicalEmail: "jit@example.test", EmailVerified: true, Provider: googleProvider, Issuer: service.cfg.Issuer, Subject: "jit-subject", ProjectID: defaultProjectID}
	seedOAuthTransaction(t, service, "jit-before-suspend", "jit-browser", OAuthFlowLogin, "", "")
	transaction, err := fs.Collection(oauthTransactionsCollection).Doc("txn-jit-before-suspend").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin", input.UserID, nil, strptr(AccountSuspended)); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount(ctx, fs, "admin", input.UserID, nil, strptr(AccountActive)); err != nil {
		t.Fatal(err)
	}
	// The JIT caller now reads the restored user's NEW version. Session issuance
	// must still reject the OLD callback, rather than accepting that new version.
	user := accountState(t, fs, input.UserID)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/callback", nil)
	service.issueSession(c, "jit-browser", input.UserID, user, true, transaction.CreateTime)
	sessions, err := service.sessions.collection().Documents(ctx).GetAll()
	if err != nil || len(sessions) != 0 {
		t.Fatal("old JIT callback created session", err)
	}
}

// ServerTimestamp is request time at millisecond precision, not commit order.
// Restore must preserve the suspended snapshot's exact commit boundary, including
// any intervening writes, and repeated restore must not move that boundary.
func TestRestorePersistsSuspendedCommitCutoff(t *testing.T) {
	for _, interveningWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(interveningWrite), func(t *testing.T) {
			fs := accountEmulator(t)
			ctx := t.Context()
			seedAccountTestUser(t, fs, "admin", "admin")
			seedAccountTestUser(t, fs, "owner", "user")
			if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountSuspended)); err != nil {
				t.Fatal(err)
			}
			initial, err := fs.Collection("users").Doc("owner").Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if interveningWrite {
				if err := UpdateAccount(ctx, fs, "admin", "owner", strptr("admin"), nil); err != nil {
					t.Fatal(err)
				}
			}
			suspended, err := fs.Collection("users").Doc("owner").Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if interveningWrite && !suspended.UpdateTime.After(initial.UpdateTime) {
				t.Fatal("intervening role change did not advance suspended commit")
			}
			for range 2 {
				if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountActive)); err != nil {
					t.Fatal(err)
				}
				user := accountState(t, fs, "owner")
				if !user.AuthInvalidBefore.Equal(suspended.UpdateTime) {
					t.Fatalf("restored cutoff=%s; suspended commit=%s", user.AuthInvalidBefore, suspended.UpdateTime)
				}
			}
		})
	}
}

func TestConcurrentRestoreAndSuspendPreserveOAuthCutoff(t *testing.T) {
	fs := accountEmulator(t)
	ctx := t.Context()
	seedAccountTestUser(t, fs, "admin", "admin")
	seedAccountTestUser(t, fs, "owner", "user")
	for attempt := 0; attempt < 10; attempt++ {
		if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountSuspended)); err != nil {
			t.Fatal(err)
		}
		suspended, err := fs.Collection("users").Doc("owner").Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, state := range []string{AccountActive, AccountSuspended} {
			go func(state string) {
				<-start
				results <- UpdateAccount(ctx, fs, "admin", "owner", nil, &state)
			}(state)
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		current, err := fs.Collection("users").Doc("owner").Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var user UserRecord
		if err := current.DataTo(&user); err != nil {
			t.Fatal(err)
		}
		if user.Active() {
			// Restore won last: it must retain at least the original suspension
			// commit, including when a repeated suspend wrote before restore.
			if user.AuthInvalidBefore.Before(suspended.UpdateTime) {
				t.Fatal("concurrent restore lost suspension commit cutoff")
			}
		} else {
			// Suspend won last: the next restore must retain THIS suspension.
			if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountActive)); err != nil {
				t.Fatal(err)
			}
			user = *accountState(t, fs, "owner")
			if !user.AuthInvalidBefore.Equal(current.UpdateTime) {
				t.Fatal("restore lost latest suspended commit cutoff")
			}
		}
		if err := UpdateAccount(ctx, fs, "admin", "owner", nil, strptr(AccountActive)); err != nil {
			t.Fatal(err)
		}
		if !accountState(t, fs, "owner").AuthInvalidBefore.Equal(user.AuthInvalidBefore) {
			t.Fatal("repeated restore changed cutoff")
		}
	}
}
