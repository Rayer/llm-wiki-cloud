package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	firestoreclient "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func TestReviewerStartupConflictMustDisableDemo(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	if strings.Contains(endpoint, "://") {
		t.Fatal("FIRESTORE_EMULATOR_HOST must be a loopback host:port")
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatal("Demo startup regression requires a loopback Firestore emulator")
	}

	gin.SetMode(gin.TestMode)
	scope := fmt.Sprintf("lwc372-startup-%d", time.Now().UnixNano())
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := firestoreclient.NewClientWithDatabase("lwc-372-startup-test", "", "", "")
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	users := firestoreclient.Collection(client.Raw(), "users")
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("synthetic-startup-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name          string
		seedDemo      func(string, string) error
		storageError  bool
		wantDemoReady bool
	}{
		{
			name: "canonical mismatch",
			seedDemo: func(userID, email string) error {
				_, err := users.Doc(userID).Set(ctx, auth.UserRecord{Email: "mismatch-" + userID + "@example.test", Role: "member", Status: auth.AccountActive})
				return err
			},
		},
		{
			name: "reservation owned by another user",
			seedDemo: func(userID, email string) error {
				if _, err := users.Doc(userID).Set(ctx, auth.UserRecord{Email: email, EmailCanonical: auth.CanonicalizeEmail(email), Role: "member", Status: auth.AccountActive}); err != nil {
					return err
				}
				_, err := startupReservationRef(client.Raw(), email).Set(ctx, auth.EmailReservation{CanonicalEmail: auth.CanonicalizeEmail(email), UserID: "another-synthetic-owner", DisplayEmail: email})
				return err
			},
		},
		{
			name: "storage error",
			seedDemo: func(userID, email string) error {
				if _, err := users.Doc(userID).Set(ctx, auth.UserRecord{Email: email, EmailCanonical: auth.CanonicalizeEmail(email), Role: "member", Status: auth.AccountActive}); err != nil {
					return err
				}
				_, err := startupReservationRef(client.Raw(), email).Set(ctx, auth.EmailReservation{CanonicalEmail: auth.CanonicalizeEmail(email), UserID: userID, DisplayEmail: email})
				return err
			},
			storageError: true,
		},
		{
			name:          "valid missing identity",
			wantDemoReady: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			userID := strings.ReplaceAll(fmt.Sprintf("demo-%s-%d", test.name, time.Now().UnixNano()), " ", "-")
			email := fmt.Sprintf("%s@example.test", strings.ReplaceAll(userID, "-", "."))
			if test.seedDemo != nil {
				if err := test.seedDemo(userID, email); err != nil {
					t.Fatalf("seed Demo fixture: %v", err)
				}
			}

			normalID := "normal-" + userID
			normalEmail := normalID + "@example.test"
			if _, err := users.Doc(normalID).Set(ctx, auth.UserRecord{Email: normalEmail, EmailCanonical: auth.CanonicalizeEmail(normalEmail), PasswordHash: string(passwordHash), Role: "member", Status: auth.AccountActive}); err != nil {
				t.Fatalf("seed ordinary Auth user: %v", err)
			}
			if _, err := startupReservationRef(client.Raw(), normalEmail).Set(ctx, auth.EmailReservation{CanonicalEmail: auth.CanonicalizeEmail(normalEmail), UserID: normalID, DisplayEmail: normalEmail}); err != nil {
				t.Fatalf("seed ordinary Auth reservation: %v", err)
			}

			cfg := config.Config{
				JWTSecret: "synthetic-startup-test-signing-key", AuthSessionEnvironment: scope,
				AuthDemoUserID: userID, AuthDemoUserEmail: email, AuthDemoUserRole: "member",
				AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"},
			}
			ensureClient := client.Raw()
			var unavailable *firestore.Client
			if test.storageError {
				t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:1")
				unavailable, err = firestore.NewClient(ctx, "lwc-372-startup-unavailable", option.WithoutAuthentication())
				if err != nil {
					t.Fatalf("create unavailable synthetic Firestore client: %v", err)
				}
				if err := firestoreclient.RegisterLocalScope(unavailable, scope); err != nil {
					t.Fatal(err)
				}
				defer unavailable.Close()
				ensureClient = unavailable
			}
			ready := ensureDemoAccountAtStartup(cfg, ensureClient)
			if test.storageError {
				t.Setenv("FIRESTORE_EMULATOR_HOST", endpoint)
			}
			if ready != test.wantDemoReady {
				t.Fatalf("startup readiness=%t want %t", ready, test.wantDemoReady)
			}

			router := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true}, ready)
			demoResponse := httptest.NewRecorder()
			demoRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/demo", nil)
			router.ServeHTTP(demoResponse, demoRequest)
			if !ready {
				if demoResponse.Code != http.StatusServiceUnavailable || len(demoResponse.Result().Cookies()) != 0 {
					t.Fatalf("failed startup Demo status=%d cookies=%d body=%s; want 503 without cookie", demoResponse.Code, len(demoResponse.Result().Cookies()), demoResponse.Body.String())
				}
				if got := startupSessionCount(t, ctx, client.Raw(), userID); got != 0 {
					t.Fatalf("failed startup created %d durable Demo sessions", got)
				}
			} else if demoResponse.Code != http.StatusOK || len(demoResponse.Result().Cookies()) != 1 {
				t.Fatalf("valid Demo startup status=%d cookies=%d body=%s; want 200 with refresh cookie", demoResponse.Code, len(demoResponse.Result().Cookies()), demoResponse.Body.String())
			} else if got := startupSessionCount(t, ctx, client.Raw(), userID); got != 1 {
				t.Fatalf("valid Demo startup created %d durable sessions, want one", got)
			}

			loginResponse := httptest.NewRecorder()
			loginRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, normalEmail, "synthetic-startup-test-password")))
			loginRequest.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(loginResponse, loginRequest)
			if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
				t.Fatalf("ordinary Auth login status=%d cookies=%d body=%s; want 200 with refresh cookie", loginResponse.Code, len(loginResponse.Result().Cookies()), loginResponse.Body.String())
			}
		})
	}
}

func startupReservationRef(client *firestore.Client, email string) *firestore.DocumentRef {
	digest := sha256.Sum256([]byte("email\x00" + auth.CanonicalizeEmail(email)))
	return firestoreclient.Collection(client, auth.EmailReservationsCollection).Doc(hex.EncodeToString(digest[:]))
}

func startupSessionCount(t *testing.T, ctx context.Context, client *firestore.Client, userID string) int {
	t.Helper()
	iter := firestoreclient.Collection(client, "auth_refresh_sessions").Where("user_id", "==", userID).Documents(ctx)
	defer iter.Stop()
	count := 0
	for {
		_, err := iter.Next()
		if err != nil {
			if err == iterator.Done {
				return count
			}
			t.Fatalf("query durable sessions: %v", err)
		}
		count++
	}
}
