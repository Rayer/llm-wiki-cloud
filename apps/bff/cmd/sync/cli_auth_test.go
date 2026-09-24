package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalAuthStoreSeparatesConfigAndCredentialsWithPrivateModes(t *testing.T) {
	store := newLocalAuthStoreAt(t.TempDir())
	config := cliLocalConfig{AuthHost: "https://auth.example.test"}
	credentials := cliLocalCredentials{AuthHost: config.AuthHost, AccessToken: "access-secret", RefreshToken: "refresh-secret", SessionID: "session-1"}
	if err := store.saveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := store.saveCredentials(credentials); err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(store.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "access-secret") || strings.Contains(string(configBytes), "refresh-secret") {
		t.Fatalf("config contains credentials: %s", configBytes)
	}
	for _, path := range []string{store.dir, store.configPath(), store.credentialsPath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if path != store.dir {
			want = 0o600
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode(%s)=%#o, want %#o", filepath.Base(path), got, want)
		}
	}
	loaded, err := store.loadCredentials()
	if err != nil || loaded.RefreshToken != credentials.RefreshToken || loaded.AuthHost != credentials.AuthHost {
		t.Fatalf("load credentials=%#v error=%v", loaded, err)
	}
}

func TestLocalAuthStoreLockSerializesProcesses(t *testing.T) {
	store := newLocalAuthStoreAt(t.TempDir())
	const workers = 8
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := store.withLock(func() error {
				f, err := os.OpenFile(filepath.Join(store.dir, "counter"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = f.WriteString("x\n")
				return err
			}); err != nil {
				t.Errorf("withLock: %v", err)
			}
		}()
	}
	wait.Wait()
	data, err := os.ReadFile(filepath.Join(store.dir, "counter"))
	if err != nil || strings.Count(string(data), "x\n") != workers {
		t.Fatalf("locked writes=%q error=%v", data, err)
	}
}

func TestCLIAPIClientRefreshesAndRetriesWithoutFollowingRedirects(t *testing.T) {
	refreshes, statuses, externalHits := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statuses++
			if got := r.Header.Get("Authorization"); got != "Bearer access-2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user_id":"user-1","session_id":"session-1"}`))
		case "/api/v1/auth/cli/refresh":
			refreshes++
			var request struct {
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.RefreshToken != "refresh-1" {
				t.Errorf("refresh body=%#v error=%v", request, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","session_id":"session-1","user_id":"user-1","role":"user"}`))
		case "/api/v1/auth/cli/redirect":
			w.Header().Set("Location", "https://other.example.test/steal")
			w.WriteHeader(http.StatusFound)
		default:
			externalHits++
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	credentials := cliLocalCredentials{AuthHost: server.URL, AccessToken: "access-1", RefreshToken: "refresh-1", SessionID: "session-1"}
	client, err := newCLIAPIClient(server.URL, &credentials)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		UserID string `json:"user_id"`
	}
	if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, &status); err != nil {
		t.Fatal(err)
	}
	if statuses != 2 || refreshes != 1 || status.UserID != "user-1" || credentials.AccessToken != "access-2" || credentials.RefreshToken != "refresh-2" {
		t.Fatalf("requests status=%d refresh=%d body=%#v credentials=%#v", statuses, refreshes, status, credentials)
	}
	if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/redirect", nil, nil); err == nil {
		t.Fatal("redirect response unexpectedly treated as success")
	}
	if externalHits != 0 {
		t.Fatalf("client followed a redirect to another origin: external hits=%d", externalHits)
	}
	if err := validateAuthOrigin("https://user:pass@auth.example.test"); err == nil {
		t.Fatal("origin with credentials accepted")
	}
	if err := validateAuthOrigin("https://auth.example.test/path"); err == nil {
		t.Fatal("origin with path accepted")
	}
	if err := validateAuthOrigin("http://evil.example.test"); err == nil {
		t.Fatal("non-local insecure origin accepted")
	}
}

func TestCLIAPIClientReportsExpiredRefreshWithoutLeakingTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"access-secret refresh-secret"}`))
	}))
	defer server.Close()
	credentials := cliLocalCredentials{AuthHost: server.URL, AccessToken: "access-secret", RefreshToken: "refresh-secret"}
	client, err := newCLIAPIClient(server.URL, &credentials)
	if err != nil {
		t.Fatal(err)
	}
	err = client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
	if !errors.Is(err, errCLIReauthenticationRequired) || strings.Contains(err.Error(), credentials.AccessToken) || strings.Contains(err.Error(), credentials.RefreshToken) {
		t.Fatalf("error=%v, expected a non-secret reauthentication error", err)
	}
}
