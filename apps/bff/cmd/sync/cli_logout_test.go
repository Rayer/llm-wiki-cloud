package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRunAuthLogoutUsesCurrentSameSessionCredentialAfterRotation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirm   bool
		wantError bool
	}{
		{name: "confirmed retry", confirm: true},
		{name: "unconfirmed retry preserves credentials", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configRoot := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configRoot)
			store := newLocalAuthStoreAt(filepath.Join(configRoot, localAuthDirectoryName))
			old := cliLocalCredentials{AccessToken: "access-old", RefreshToken: "refresh-old", SessionID: "session-1", UserID: "user-1"}
			current := old
			current.AccessToken = "access-current"
			current.RefreshToken = "refresh-current"
			var mu sync.Mutex
			requests := make([]string, 0, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					RefreshToken string `json:"refresh_token"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				requests = append(requests, body.RefreshToken)
				count := len(requests)
				mu.Unlock()
				if body.RefreshToken == "refresh-old" {
					if err := store.withLock(func() error { return store.saveCredentials(current) }); err != nil {
						http.Error(w, "could not rotate fixture", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if body.RefreshToken == "refresh-current" && count == 2 && tc.confirm {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			old.AuthHost, current.AuthHost = server.URL, server.URL
			if err := store.withLock(func() error {
				if err := store.saveConfig(cliLocalConfig{AuthHost: server.URL}); err != nil {
					return err
				}
				return store.saveCredentials(old)
			}); err != nil {
				t.Fatal(err)
			}

			err := runAuthLogout([]string{"--host", server.URL})
			if (err != nil) != tc.wantError {
				t.Fatalf("logout error=%v, wantError=%v", err, tc.wantError)
			}
			if tc.wantError && !strings.Contains(err.Error(), "did not confirm") {
				t.Fatalf("unconfirmed logout error=%v", err)
			}
			mu.Lock()
			gotRequests := append([]string(nil), requests...)
			mu.Unlock()
			if !reflect.DeepEqual(gotRequests, []string{"refresh-old", "refresh-current"}) {
				t.Fatalf("logout refresh tokens=%v", gotRequests)
			}
			var stored cliLocalCredentials
			loadErr := store.withLock(func() error {
				var err error
				stored, err = store.loadCredentials()
				return err
			})
			if tc.confirm {
				if !errors.Is(loadErr, errCLILocalAuthNotFound) {
					t.Fatalf("confirmed logout left credentials=%#v error=%v", stored, loadErr)
				}
			} else if loadErr != nil || stored != current {
				t.Fatalf("unconfirmed logout lost rotated credentials=%#v error=%v", stored, loadErr)
			}
		})
	}
}

func TestWithStoredCredentialsPreservesReplacementSessionOnUnauthorized(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	var store *localAuthStore
	var replaced cliLocalCredentials
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/cli/status" {
			if err := store.withLock(func() error { return store.saveCredentials(replaced) }); err != nil {
				http.Error(w, "could not replace fixture session", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	store = newLocalAuthStoreAt(filepath.Join(configRoot, localAuthDirectoryName))
	old := cliLocalCredentials{AuthHost: server.URL, AccessToken: "access-old", RefreshToken: "refresh-old", SessionID: "session-old", UserID: "user-1"}
	replaced = cliLocalCredentials{AuthHost: server.URL, AccessToken: "access-new-session", RefreshToken: "refresh-new-session", SessionID: "session-new", UserID: "user-1"}
	if err := store.withLock(func() error {
		if err := store.saveConfig(cliLocalConfig{AuthHost: server.URL}); err != nil {
			return err
		}
		return store.saveCredentials(old)
	}); err != nil {
		t.Fatal(err)
	}
	err := withStoredCredentials(server.URL, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		return client.request(t.Context(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("replacement session error=%v", err)
	}
	var got cliLocalCredentials
	if err := store.withLock(func() error {
		var err error
		got, err = store.loadCredentials()
		return err
	}); err != nil || got != replaced {
		t.Fatalf("replacement session credentials=%#v error=%v", got, err)
	}
}
