package gcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	cloudstorage "cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/option"
)

type rawSyncPostCommitErrorBackend struct {
	*memoryBackend
	err error
}

type rawSyncRewriteTransport struct {
	mu                 sync.Mutex
	requests           []string
	rewrites           int
	commitThen503      bool
	destinationCreated bool
}

func (t *rawSyncRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
	t.mu.Unlock()
	status := http.StatusOK
	body := ""
	headers := make(http.Header)
	switch {
	case req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/upload/storage/v1/"):
		if req.URL.Query().Get("uploadType") == "multipart" {
			_, _ = io.Copy(io.Discard, req.Body)
			body = `{"kind":"storage#object","bucket":"bucket","name":"staging-object","generation":"101","size":"1"}`
		} else {
			headers.Set("Location", req.URL.Scheme+"://"+req.URL.Host+"/upload/storage/v1/b/bucket/o?upload_id=raw-sync-test-upload")
			headers.Set("X-GUploader-UploadID", "raw-sync-test-upload")
		}
	case req.Method == http.MethodPut && req.URL.Query().Get("upload_id") == "raw-sync-test-upload":
		_, _ = io.Copy(io.Discard, req.Body)
		body = `{"kind":"storage#object","bucket":"bucket","name":"staging-object","generation":"101","size":"1"}`
	case strings.Contains(req.URL.Path, "/rewriteTo/"):
		t.mu.Lock()
		defer t.mu.Unlock()
		t.rewrites++
		if t.commitThen503 && t.rewrites == 1 {
			t.destinationCreated = true
			status = http.StatusServiceUnavailable
			body = `{"error":{"code":503,"message":"injected response loss after destination commit"}}`
		} else {
			status = http.StatusPreconditionFailed
			body = `{"error":{"code":412,"message":"destination condition no longer holds"}}`
		}
	case req.Method == http.MethodDelete:
		status = http.StatusNoContent
	default:
		status = http.StatusNotFound
		body = `{"error":{"code":404,"message":"unexpected SDK request"}}`
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func TestRawSyncActualSDKDoesNotRetryAmbiguousConditionalRewrite(t *testing.T) {
	for _, test := range []struct {
		name            string
		commitThen503   bool
		wantUncertain   bool
		wantConflict    bool
		wantDestination bool
	}{
		{name: "commit then retryable response stays uncertain", commitThen503: true, wantUncertain: true, wantDestination: true},
		{name: "first precommit CAS rejection remains conflict", wantConflict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &rawSyncRewriteTransport{commitThen503: test.commitThen503}
			ctx := context.Background()
			provider, err := cloudstorage.NewClient(ctx,
				option.WithEndpoint("http://storage.googleapis.com/storage/v1/"),
				option.WithHTTPClient(&http.Client{Transport: transport}),
				option.WithoutAuthentication(),
			)
			if err != nil {
				t.Fatalf("create SDK client with injected transport: %v", err)
			}
			defer provider.Close()
			project := newClientWithStorageClient("bucket", "", provider).WithScope("user-1", "project-1")
			data := []byte("x")
			digest := sha256.Sum256(data)
			_, err = project.WriteSyncRawFile(ctx, "new.bin", bytes.NewReader(data), "", hex.EncodeToString(digest[:]))
			if errors.Is(err, store.ErrRawSyncCommitUncertain) != test.wantUncertain || errors.Is(err, store.ErrRawSyncConflict) != test.wantConflict {
				transport.mu.Lock()
				requests := append([]string(nil), transport.requests...)
				transport.mu.Unlock()
				t.Fatalf("WriteSyncRawFile error=%v; uncertain=%v conflict=%v requests=%v", err, errors.Is(err, store.ErrRawSyncCommitUncertain), errors.Is(err, store.ErrRawSyncConflict), requests)
			}
			transport.mu.Lock()
			rewrites, committed := transport.rewrites, transport.destinationCreated
			transport.mu.Unlock()
			if rewrites != 1 || committed != test.wantDestination {
				t.Fatalf("actual SDK rewrites=%d destinationCommitted=%v, want one rewrite and committed=%v", rewrites, committed, test.wantDestination)
			}
		})
	}
}

func (b *rawSyncPostCommitErrorBackend) Write(ctx context.Context, name string, data []byte, contentType string, metadata map[string]string, condition writeCondition) (backendObject, error) {
	object, err := b.memoryBackend.Write(ctx, name, data, contentType, metadata, condition)
	if err != nil {
		return object, err
	}
	return object, b.err
}

func TestRawSyncBackendPostCommitErrorIsUncertain(t *testing.T) {
	client, backend := newMemoryClient()
	cause := errors.New("injected error after backend commit")
	client.backend = &rawSyncPostCommitErrorBackend{memoryBackend: backend, err: cause}
	project := client.WithScope("user", "project")
	data := []byte("committed before the adapter error")
	digest := sha256.Sum256(data)

	if _, err := project.WriteSyncRawFile(context.Background(), "nested/committed.bin", bytes.NewReader(data), "", hex.EncodeToString(digest[:])); !errors.Is(err, store.ErrRawSyncCommitUncertain) || !errors.Is(err, cause) {
		t.Fatalf("post-commit write error=%v, want uncertain outcome wrapping cause", err)
	}

	files, err := project.ListSyncRawFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].Path != "nested/committed.bin" {
		t.Fatalf("committed listing=%#v err=%v", files, err)
	}
	reader, size, err := project.OpenSyncRawFile(context.Background(), files[0].Path, files[0].Generation)
	if err != nil {
		t.Fatal(err)
	}
	readback, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || size != int64(len(data)) || !bytes.Equal(readback, data) {
		t.Fatalf("readback size=%d data=%q read=%v close=%v", size, readback, readErr, closeErr)
	}
}
