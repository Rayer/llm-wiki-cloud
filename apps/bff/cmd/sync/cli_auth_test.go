package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestCLIAPIClientAdoptsConcurrentSameSessionRotationOnUnauthorized(t *testing.T) {
	old := cliLocalCredentials{AuthHost: "https://auth.example.test", AccessToken: "access-old", RefreshToken: "refresh-old", SessionID: "session-1", UserID: "user-1"}
	current := cliLocalCredentials{AuthHost: old.AuthHost, AccessToken: testCLIAccessToken(t, time.Now().Add(10*time.Minute)), RefreshToken: "refresh-current", SessionID: old.SessionID, UserID: old.UserID}
	var mu sync.Mutex
	statuses, refreshes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statuses++
			if r.Header.Get("Authorization") != "Bearer "+current.AccessToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"user_id":"user-1","session_id":"session-1"}`))
		case "/api/v1/auth/cli/refresh":
			refreshes++
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	old.AuthHost, current.AuthHost = server.URL, server.URL
	credentials := old
	client, err := newCLIAPIClient(server.URL, &credentials)
	if err != nil {
		t.Fatal(err)
	}
	client.loadCurrent = func(previous cliLocalCredentials) (cliLocalCredentials, error) {
		mu.Lock()
		defer mu.Unlock()
		if current.SessionID != previous.SessionID || current.UserID != previous.UserID {
			return cliLocalCredentials{}, errCLIReauthenticationRequired
		}
		return current, nil
	}
	if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil); err != nil {
		t.Fatalf("request using current same-session credentials: %v", err)
	}
	if statuses != 2 || refreshes != 0 || credentials.AccessToken != current.AccessToken || credentials.RefreshToken != "refresh-current" {
		t.Fatalf("status requests=%d refresh requests=%d credentials=%#v", statuses, refreshes, credentials)
	}
}

func TestCLIAPIClientRecoversSharedRotationAfterStaleRefreshUnauthorized(t *testing.T) {
	old := cliLocalCredentials{AuthHost: "https://auth.example.test", AccessToken: "access-old", RefreshToken: "refresh-old", SessionID: "session-1", UserID: "user-1"}
	current := old
	rotated := cliLocalCredentials{AuthHost: old.AuthHost, AccessToken: "access-current", RefreshToken: "refresh-current", SessionID: old.SessionID, UserID: old.UserID}
	var mu sync.Mutex
	reads, statuses, refreshes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statuses++
			if r.Header.Get("Authorization") != "Bearer access-current" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/v1/auth/cli/refresh":
			refreshes++
			mu.Lock()
			current = rotated
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	old.AuthHost, current.AuthHost, rotated.AuthHost = server.URL, server.URL, server.URL
	credentials := old
	client, err := newCLIAPIClient(server.URL, &credentials)
	if err != nil {
		t.Fatal(err)
	}
	client.loadCurrent = func(previous cliLocalCredentials) (cliLocalCredentials, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if current.SessionID != previous.SessionID || current.UserID != previous.UserID {
			return cliLocalCredentials{}, errCLIReauthenticationRequired
		}
		return current, nil
	}
	if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil); err != nil {
		t.Fatalf("request after shared refresh rotation: %v", err)
	}
	if statuses != 2 || refreshes != 1 || reads < 2 || credentials.AccessToken != "access-current" || credentials.RefreshToken != "refresh-current" {
		t.Fatalf("status requests=%d refresh requests=%d store reads=%d credentials=%#v", statuses, refreshes, reads, credentials)
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

func TestStoredCLIClientAdoptsNewerValidAccessWithoutRefreshing(t *testing.T) {
	oldAccess := testCLIAccessToken(t, time.Now().Add(-time.Minute))
	newAccess := testCLIAccessToken(t, time.Now().Add(10*time.Minute))
	var storeRef atomic.Pointer[localAuthStore]
	var sharedRef atomic.Pointer[cliLocalCredentials]
	var statusRequests, refreshRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statusRequests.Add(1)
			switch r.Header.Get("Authorization") {
			case "Bearer " + oldAccess:
				if err := saveStoredCLICredentials(storeRef.Load(), *sharedRef.Load()); err != nil {
					t.Errorf("save newer credentials: %v", err)
					http.Error(w, "store error", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer " + newAccess:
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/api/v1/auth/cli/refresh":
			refreshRequests.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	old := cliLocalCredentials{AuthHost: server.URL, AccessToken: oldAccess, RefreshToken: "refresh-old", SessionID: "session-1", UserID: "user-1"}
	shared := cliLocalCredentials{AuthHost: server.URL, AccessToken: newAccess, RefreshToken: "refresh-new", SessionID: old.SessionID, UserID: old.UserID}
	sharedRef.Store(&shared)
	store := newStoredCLIAuthStore(t, old)
	storeRef.Store(store)
	if err := withStoredCredentials(server.URL, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		return client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
	}); err != nil {
		t.Fatalf("request using newer valid shared access: %v", err)
	}
	if statusRequests.Load() != 2 || refreshRequests.Load() != 0 {
		t.Fatalf("status requests=%d refresh requests=%d, want 2 and 0", statusRequests.Load(), refreshRequests.Load())
	}
	if stored := loadStoredCLICredentials(t, store); stored != shared {
		t.Fatalf("stored credentials=%#v, want newer same-session credentials %#v", stored, shared)
	}
}

func TestStoredCLIClientRefreshesNewerExpiredAccess(t *testing.T) {
	oldAccess := testCLIAccessToken(t, time.Now().Add(-2*time.Minute))
	sharedAccess := testCLIAccessToken(t, time.Now().Add(-time.Minute))
	rotatedAccess := testCLIAccessToken(t, time.Now().Add(10*time.Minute))
	var storeRef atomic.Pointer[localAuthStore]
	var sharedRef atomic.Pointer[cliLocalCredentials]
	var statusRequests, refreshRequests atomic.Int32
	var refreshToken string
	var refreshMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statusRequests.Add(1)
			switch r.Header.Get("Authorization") {
			case "Bearer " + oldAccess:
				if err := saveStoredCLICredentials(storeRef.Load(), *sharedRef.Load()); err != nil {
					t.Errorf("save expired newer credentials: %v", err)
					http.Error(w, "store error", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer " + rotatedAccess:
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/api/v1/auth/cli/refresh":
			var input struct {
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Errorf("decode refresh request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			refreshMu.Lock()
			refreshToken = input.RefreshToken
			refreshMu.Unlock()
			refreshRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cliLocalCredentials{AccessToken: rotatedAccess, RefreshToken: "refresh-rotated", SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	old := cliLocalCredentials{AuthHost: server.URL, AccessToken: oldAccess, RefreshToken: "refresh-old", SessionID: "session-1", UserID: "user-1"}
	shared := cliLocalCredentials{AuthHost: server.URL, AccessToken: sharedAccess, RefreshToken: "refresh-current", SessionID: old.SessionID, UserID: old.UserID}
	sharedRef.Store(&shared)
	store := newStoredCLIAuthStore(t, old)
	storeRef.Store(store)
	if err := withStoredCredentials(server.URL, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		return client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
	}); err != nil {
		t.Fatalf("request after refreshing expired shared access: %v", err)
	}
	refreshMu.Lock()
	gotRefreshToken := refreshToken
	refreshMu.Unlock()
	if statusRequests.Load() != 2 || refreshRequests.Load() != 1 || gotRefreshToken != shared.RefreshToken {
		t.Fatalf("status=%d refresh=%d refresh token=%q, want 2, 1, %q", statusRequests.Load(), refreshRequests.Load(), gotRefreshToken, shared.RefreshToken)
	}
	rotated := shared
	rotated.AccessToken = rotatedAccess
	rotated.RefreshToken = "refresh-rotated"
	rotated.Role = "user"
	rotated.AuthVersion = 1
	if stored := loadStoredCLICredentials(t, store); stored != rotated {
		t.Fatalf("stored rotated credentials=%#v, want %#v", stored, rotated)
	}
}

func TestStoredCLIClientsSerializeNecessarySharedRefresh(t *testing.T) {
	oldAccess := testCLIAccessToken(t, time.Now().Add(-time.Minute))
	rotatedAccess := testCLIAccessToken(t, time.Now().Add(10*time.Minute))
	oldRefresh, rotatedRefresh := "refresh-old", "refresh-rotated"
	var statusRequests, oldStatusRequests atomic.Int32
	var refreshRequests, activeRefreshes, maxRefreshes atomic.Int32
	oldStatusesReady := make(chan struct{})
	secondRefresh := make(chan struct{})
	var secondRefreshOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			statusRequests.Add(1)
			if r.Header.Get("Authorization") == "Bearer "+oldAccess {
				if oldStatusRequests.Add(1) == 2 {
					close(oldStatusesReady)
				}
				select {
				case <-oldStatusesReady:
					w.WriteHeader(http.StatusUnauthorized)
				case <-r.Context().Done():
				}
				return
			}
			if r.Header.Get("Authorization") == "Bearer "+rotatedAccess {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/v1/auth/cli/refresh":
			n := refreshRequests.Add(1)
			active := activeRefreshes.Add(1)
			for {
				previous := maxRefreshes.Load()
				if active <= previous || maxRefreshes.CompareAndSwap(previous, active) {
					break
				}
			}
			defer activeRefreshes.Add(-1)
			if n == 1 {
				select {
				case <-secondRefresh:
				case <-time.After(250 * time.Millisecond):
				}
			} else {
				secondRefreshOnce.Do(func() { close(secondRefresh) })
			}
			var input struct {
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.RefreshToken != oldRefresh {
				t.Errorf("refresh token=%q error=%v, want initial shared token", input.RefreshToken, err)
				http.Error(w, "bad refresh token", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cliLocalCredentials{AccessToken: rotatedAccess, RefreshToken: rotatedRefresh, SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	initial := cliLocalCredentials{AuthHost: server.URL, AccessToken: oldAccess, RefreshToken: oldRefresh, SessionID: "session-1", UserID: "user-1"}
	store := newStoredCLIAuthStore(t, initial)
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- withStoredCredentials(server.URL, func(client *cliAPIClient, _ *cliLocalCredentials) error {
				return client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
			})
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("parallel request: %v", err)
		}
	}
	if statusRequests.Load() != 4 || oldStatusRequests.Load() != 2 || refreshRequests.Load() != 1 || maxRefreshes.Load() != 1 {
		t.Fatalf("status=%d old-status=%d refresh=%d max-concurrent-refresh=%d, want 4, 2, 1, 1", statusRequests.Load(), oldStatusRequests.Load(), refreshRequests.Load(), maxRefreshes.Load())
	}
	rotated := initial
	rotated.AccessToken = rotatedAccess
	rotated.RefreshToken = rotatedRefresh
	rotated.Role = "user"
	rotated.AuthVersion = 1
	if stored := loadStoredCLICredentials(t, store); stored != rotated {
		t.Fatalf("stored credentials=%#v, want %#v", stored, rotated)
	}
}

func newStoredCLIAuthStore(t *testing.T, credentials cliLocalCredentials) *localAuthStore {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := newLocalAuthStoreAt(filepath.Join(root, localAuthDirectoryName))
	if err := store.withLock(func() error {
		if err := store.saveConfig(cliLocalConfig{AuthHost: credentials.AuthHost}); err != nil {
			return err
		}
		return store.saveCredentials(credentials)
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func saveStoredCLICredentials(store *localAuthStore, credentials cliLocalCredentials) error {
	if store == nil {
		return errors.New("credential fixture store is unavailable")
	}
	return store.withLock(func() error { return store.saveCredentials(credentials) })
}

func loadStoredCLICredentials(t *testing.T, store *localAuthStore) cliLocalCredentials {
	t.Helper()
	var credentials cliLocalCredentials
	if err := store.withLock(func() error {
		var err error
		credentials, err = store.loadCredentials()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return credentials
}

func testCLIAccessToken(t *testing.T, expiry time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": expiry.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
