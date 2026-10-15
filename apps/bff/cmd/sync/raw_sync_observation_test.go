package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestCLIStatusErrorRetainsOnlyValidatedDiagnosticMetadata(t *testing.T) {
	large := `{"diagnostic_id":"` + strings.Repeat("x", maxRemoteDiagnosticIDBytes+1) + `"}`
	tooLarge := `{"diagnostic_id":"valid-ID-123"}` + strings.Repeat(" ", maxCLIResponseBytes)
	cases := []struct {
		name        string
		body        string
		wantID      string
		wantStatus  bool
		wantMessage string
	}{
		{name: "valid", body: `{"diagnostic_id":"Diag_123-ABC","message":"body-token-canary"}`, wantID: "Diag_123-ABC", wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "missing", body: `{"message":"body-token-canary"}`, wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "invalid characters", body: `{"diagnostic_id":"invalid/id"}`, wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "control characters", body: `{"diagnostic_id":"line\nfeed_123"}`, wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "oversized id", body: large, wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "non JSON", body: "diagnostic_id=Diag_123-ABC body-token-canary", wantStatus: true, wantMessage: "Auth service returned HTTP 502"},
		{name: "oversized body", body: tooLarge, wantMessage: "Auth service response was unavailable or too large"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := newCLIAPIClient(server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
			if err == nil || err.Error() != test.wantMessage {
				t.Fatalf("request error=%v, want %q", err, test.wantMessage)
			}
			var statusError cliAPIStatusError
			gotStatus := errors.As(err, &statusError)
			if gotStatus != test.wantStatus {
				t.Fatalf("typed status error=%v, want %v", gotStatus, test.wantStatus)
			}
			if gotStatus && statusError.DiagnosticID() != test.wantID {
				t.Fatalf("diagnostic ID=%q, want %q", statusError.DiagnosticID(), test.wantID)
			}
			if strings.Contains(err.Error(), "body-token-canary") || strings.Contains(err.Error(), "Diag_123-ABC") {
				t.Fatalf("status error exposed body metadata: %v", err)
			}
		})
	}
}

func TestCLIStatusRetryUsesOnlyFinalResponseDiagnosticID(t *testing.T) {
	var mu sync.Mutex
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/cli/status":
			mu.Lock()
			statusCalls++
			call := statusCalls
			mu.Unlock()
			if call == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"diagnostic_id":"stale-id-123"}`)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"diagnostic_id":"final-id_123"}`)
		case "/api/v1/auth/cli/refresh":
			_ = json.NewEncoder(w).Encode(cliLocalCredentials{AccessToken: "access-2", RefreshToken: "refresh-2", SessionID: "session-1", UserID: "user-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	credentials := &cliLocalCredentials{AuthHost: server.URL, AccessToken: "access-1", RefreshToken: "refresh-1", SessionID: "session-1", UserID: "user-1"}
	client, err := newCLIAPIClient(server.URL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	err = client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, nil)
	var statusError cliAPIStatusError
	if !errors.As(err, &statusError) || statusError.DiagnosticID() != "final-id_123" || err.Error() != "Auth service returned HTTP 403" {
		t.Fatalf("retry returned error=%v diagnostic=%q, want final request only", err, statusError.DiagnosticID())
	}
}

func TestCLIStatusConcurrentDiagnosticIDsStayWithTheirResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/cli/status/")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, `{"diagnostic_id":"%s"}`, id)
	}))
	defer server.Close()
	client, err := newCLIAPIClient(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan string, 2)
	for _, id := range []string{"request_A123", "request_B456"} {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status/"+id, nil, nil)
			var statusError cliAPIStatusError
			if !errors.As(err, &statusError) {
				results <- "missing"
				return
			}
			results <- statusError.DiagnosticID()
		}()
	}
	wait.Wait()
	close(results)
	got := map[string]int{}
	for id := range results {
		got[id]++
	}
	if got["request_A123"] != 1 || got["request_B456"] != 1 || len(got) != 2 {
		t.Fatalf("concurrent response IDs crossed: %#v", got)
	}
}

func TestSyncListingDiagnosticIDUsesAlreadyReadErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"diagnostic_id":"remote_list_123","message":"private-body-canary"}`)
	}))
	defer server.Close()
	credentials := &cliLocalCredentials{AuthHost: server.URL, AccessToken: "synthetic-access"}
	authClient, err := newCLIAPIClient(server.URL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	observer := newRawSyncObservation()
	var stderr bytes.Buffer
	observer.writer = &stderr
	authClient.observer = observer
	dataClient, err := newSyncDataAPIClient(server.URL, authClient, vaultBindingConfig{ProjectID: "synthetic-project", WikiID: "synthetic-wiki", BindingID: "synthetic-binding"})
	if err != nil {
		t.Fatal(err)
	}
	dataClient.observer = observer
	observer.recordRemoteList(false)
	err = observer.measure(rawSyncStageRemote, func() error {
		_, listErr := dataClient.listAll(context.Background())
		return listErr
	})
	var statusError syncDataStatusError
	if !errors.As(err, &statusError) || statusError.Error() != "Sync data service returned HTTP 503" || statusError.DiagnosticID() != "remote_list_123" {
		t.Fatalf("listing error=%v diagnostic=%q", err, statusError.DiagnosticID())
	}
	observer.recordError(err)
	observer.finish(err)
	if !strings.Contains(stderr.String(), "diagnostic_id=remote_list_123") || !strings.Contains(stderr.String(), "sync_requests=1") || !strings.Contains(stderr.String(), "remote_list_pages=1") || strings.Contains(stderr.String(), "private-body-canary") {
		t.Fatalf("summary failed to reuse only safe listing metadata: %q", stderr.String())
	}
}

func TestRawSyncObservationDoesNotCarryDiagnosticIDAcrossErrors(t *testing.T) {
	tests := []struct {
		name       string
		currentErr error
		wantID     string
	}{
		{name: "current error has no ID", currentErr: errors.New("local verification failed")},
		{name: "current ID is invalid", currentErr: syncDataStatusError{status: http.StatusBadGateway, diagnosticID: "bad/id"}},
		{name: "current error owns its ID", currentErr: syncDataStatusError{status: http.StatusBadGateway, diagnosticID: "current-id_123"}, wantID: "current-id_123"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := newRawSyncObservation()
			var stderr bytes.Buffer
			observer.writer = &stderr
			observer.recordError(syncDataStatusError{status: http.StatusServiceUnavailable, diagnosticID: "previous-id_123"})
			observer.recordError(test.currentErr)
			observer.finish(test.currentErr)
			if test.wantID == "" {
				if strings.Contains(stderr.String(), "diagnostic_id=") {
					t.Fatalf("summary retained a stale or invalid ID: %q", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), "diagnostic_id="+test.wantID) || strings.Contains(stderr.String(), "previous-id_123") {
				t.Fatalf("summary did not retain only the current error ID: %q", stderr.String())
			}
		})
	}
}

type rawSyncRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rawSyncRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type rawSyncCountingBody struct {
	data  []byte
	reads int
}

func (b *rawSyncCountingBody) Read(p []byte) (int, error) {
	b.reads++
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (*rawSyncCountingBody) Close() error { return nil }

func TestSyncTransfersDoNotReadErrorBodiesAndKeepSuccessPayloadRaw(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "raw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "raw", "upload.bin"), []byte("synthetic upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := scanLocalRawFiles(context.Background(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.Close()

	uploadErrorBody := &rawSyncCountingBody{data: []byte(`{"diagnostic_id":"upload-error-id"}`)}
	downloadErrorBody := &rawSyncCountingBody{data: []byte(`{"diagnostic_id":"download-error-id"}`)}
	payload := []byte(`{"diagnostic_id":"this-is-raw-file-content"}`)
	successBody := &rawSyncCountingBody{data: append([]byte(nil), payload...)}
	calls := 0
	client, err := newCLIAPIClient("https://auth.example.test", &cliLocalCredentials{AuthHost: "https://auth.example.test", AccessToken: "synthetic-access"})
	if err != nil {
		t.Fatal(err)
	}
	dataClient := &syncDataAPIClient{
		origin: "https://sync.example.test", authClient: client, binding: vaultBindingConfig{ProjectID: "project", WikiID: "wiki", BindingID: "binding"},
		httpClient: &http.Client{Transport: rawSyncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			response := &http.Response{Header: make(http.Header), Request: request}
			switch calls {
			case 1:
				response.StatusCode, response.Body = http.StatusConflict, uploadErrorBody
			case 2:
				response.StatusCode, response.Body = http.StatusConflict, downloadErrorBody
			case 3:
				response.StatusCode, response.Body = http.StatusOK, successBody
				response.Header.Set("X-Raw-Generation", "7")
				response.Header.Set("Content-Length", fmt.Sprint(len(payload)))
			default:
				return nil, fmt.Errorf("unexpected request %d", calls)
			}
			return response, nil
		})},
	}
	file := inventory.Files[0]
	err = dataClient.uploadFile(context.Background(), inventory, file)
	var uploadStatus syncDataStatusError
	if !errors.As(err, &uploadStatus) || uploadStatus.Error() != "Sync data service returned HTTP 409" || uploadStatus.DiagnosticID() != "" {
		t.Fatalf("upload error=%v diagnostic=%q", err, uploadStatus.DiagnosticID())
	}
	err = dataClient.downloadFile(context.Background(), vault, store.RawSyncFile{Path: "download.bin", Size: int64(len(payload)), SHA256: hex.EncodeToString(sha256Sum(payload)), Generation: "7"})
	var downloadStatus syncDataStatusError
	if !errors.As(err, &downloadStatus) || downloadStatus.DiagnosticID() != "" {
		t.Fatalf("download error=%v diagnostic=%q", err, downloadStatus.DiagnosticID())
	}
	if uploadErrorBody.reads != 0 || downloadErrorBody.reads != 0 {
		t.Fatalf("transfer error bodies were read: upload=%d download=%d", uploadErrorBody.reads, downloadErrorBody.reads)
	}
	remote := store.RawSyncFile{Path: "download.bin", Size: int64(len(payload)), SHA256: hex.EncodeToString(sha256Sum(payload)), Generation: "7"}
	if err := dataClient.downloadFile(context.Background(), vault, remote); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "raw", "download.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("successful download bytes=%q err=%v, want raw payload %q", got, err, payload)
	}
	if successBody.reads == 0 || calls != 3 {
		t.Fatalf("success body reads=%d requests=%d, want existing raw transfer read and three requests", successBody.reads, calls)
	}
}

func sha256Sum(data []byte) []byte {
	digest := sha256.Sum256(data)
	return digest[:]
}

type rawSyncFailingWriter struct{}

func (rawSyncFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected observer output failure")
}

func TestRawSyncObservationSummaryIsBoundedAndWriterFailureIsInert(t *testing.T) {
	observer := newRawSyncObservation()
	var output bytes.Buffer
	observer.writer = &output
	for range 1000 {
		_ = observer.measure(rawSyncStageDiff, func() error {
			observer.recordComparedFile()
			return nil
		})
	}
	observer.setResult(rawPushResult{added: 3, failed: []string{"private/path"}})
	observer.finish(errors.New("partial"))
	if strings.Count(output.String(), "lwc-sync observation") != 1 || len(output.String()) > 2000 || !strings.Contains(output.String(), "compared_files=1000") || strings.Contains(output.String(), "private/path") {
		t.Fatalf("summary was not finite/bounded/private-safe: len=%d %q", output.Len(), output.String())
	}
	if !strings.Contains(output.String(), "outcome=partial") || !strings.Contains(output.String(), "failed=1") {
		t.Fatalf("summary did not preserve partial outcome: %q", output.String())
	}
	failingWriterObserver := newRawSyncObservation()
	failingWriterObserver.writer = rawSyncFailingWriter{}
	// finish is deliberately best-effort; a failed stderr write cannot replace a business result.
	failingWriterObserver.finish(nil)
}
