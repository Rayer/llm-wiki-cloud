package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestRawUploadDecision(t *testing.T) {
	tests := []struct {
		name                   string
		exists, overwrite      bool
		existingDigest, digest string
		wantStatus             int
		wantUploadStatus       string
		wantWrite              bool
	}{
		{name: "create", digest: "new", wantStatus: http.StatusCreated, wantUploadStatus: rawUploadStatusCreated, wantWrite: true},
		{name: "already exists", exists: true, existingDigest: "same", digest: "same", wantStatus: http.StatusOK, wantUploadStatus: rawUploadStatusAlreadyExists},
		{name: "conflict without overwrite", exists: true, existingDigest: "old", digest: "new", wantStatus: http.StatusConflict},
		{name: "replace with overwrite", exists: true, overwrite: true, existingDigest: "old", digest: "new", wantStatus: http.StatusOK, wantUploadStatus: rawUploadStatusReplaced, wantWrite: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, uploadStatus, write := rawUploadDecision(tt.exists, tt.existingDigest, tt.digest, tt.overwrite)
			if status != tt.wantStatus || uploadStatus != tt.wantUploadStatus || write != tt.wantWrite {
				t.Fatalf("got status=%d uploadStatus=%q write=%v; want status=%d uploadStatus=%q write=%v", status, uploadStatus, write, tt.wantStatus, tt.wantUploadStatus, tt.wantWrite)
			}
		})
	}
}

func TestValidateRawUploadFilenameAcceptsSafeMarkdownFilename(t *testing.T) {
	tests := []string{
		"notes-2026_06.27.md",
		"陽明山親子公園.md",
		"新北 景點 推薦.md",
		"新北市 特色公園 指南(完整版).md",
		"data.csv",
		"config.toml",
		"index.html",
	}
	for _, name := range tests {
		if err := validateRawUploadFilename(name); err != nil {
			t.Fatalf("validateRawUploadFilename(%q) returned error: %v", name, err)
		}
	}
}

func TestValidateRawUploadFilenameRejectsUnsafeNames(t *testing.T) {
	tests := []string{
		"",
		"notes.exe",
		"notes.MD",
		"../notes.md",
		".md",
		strings.Repeat("a", 510) + ".md",
	}

	for _, filename := range tests {
		t.Run(filename, func(t *testing.T) {
			if err := validateRawUploadFilename(filename); err == nil {
				t.Fatal("validateRawUploadFilename returned nil error")
			}
		})
	}
}

func TestReadRawUploadBodyReturnsBytesSizeAndSHA256(t *testing.T) {
	data, size, digest, err := readRawUploadBody(strings.NewReader("# Hello\n"))
	if err != nil {
		t.Fatalf("readRawUploadBody returned error: %v", err)
	}

	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("# Hello\n")))
	if string(data) != "# Hello\n" || size != int64(len(data)) || digest != wantDigest {
		t.Fatalf("data=%q size=%d digest=%q, want data %q size %d digest %q", data, size, digest, "# Hello\n", len(data), wantDigest)
	}
}

func TestReadRawUploadBodyRejectsEmptyAndOversizeFiles(t *testing.T) {
	if _, _, _, err := readRawUploadBody(strings.NewReader("")); err != errRawUploadEmptyFile {
		t.Fatalf("empty error = %v, want errRawUploadEmptyFile", err)
	}

	oversize := strings.NewReader(strings.Repeat("a", maxRawUploadSize+1))
	if _, _, _, err := readRawUploadBody(oversize); err != errRawUploadTooLarge {
		t.Fatalf("oversize error = %v, want errRawUploadTooLarge", err)
	}
}

func TestRawUploadResponseUsesProjectScopedPath(t *testing.T) {
	resp := newRawUploadResponse("user-1", "project-1", "note.md", 12, "abc123", rawUploadStatusCreated)

	if resp.Filename != "note.md" {
		t.Fatalf("filename = %q, want note.md", resp.Filename)
	}
	if resp.Path != "users/user-1/projects/project-1/raw/note.md" {
		t.Fatalf("path = %q", resp.Path)
	}
	if resp.Bytes != 12 || resp.SHA256 != "abc123" {
		t.Fatalf("bytes=%d sha256=%q, want bytes=12 sha256=abc123", resp.Bytes, resp.SHA256)
	}
	if resp.Status != rawUploadStatusCreated {
		t.Fatalf("status = %q, want %q", resp.Status, rawUploadStatusCreated)
	}
}

func TestRawSyncHandlersPageStableInventoryAndUseGenerationCAS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := localfs.New(t.TempDir())
	project := root.WithScope("user-1", "project-1")
	for i := 0; i < rawSyncPageSize+1; i++ {
		path := filepath.ToSlash(filepath.Join("raw", "nested", fmt.Sprintf("attachment-%03d.bin", i)))
		if _, err := project.WriteBytes(context.Background(), []byte(fmt.Sprintf("file-%03d", i)), path); err != nil {
			t.Fatal(err)
		}
	}
	h := New(root, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("projectID", "project-1")
		c.Next()
	})
	router.GET("/api/v1/sync/raw", h.SyncRawList)
	router.GET("/api/v1/sync/raw/file", h.SyncRawDownload)
	router.PUT("/api/v1/sync/raw/file", h.SyncRawUpload)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/sync/raw", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first list status=%d body=%s", first.Code, first.Body.String())
	}
	var page1 rawSyncListPage
	if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Files) != rawSyncPageSize || page1.TotalFiles != rawSyncPageSize+1 || page1.NextPageToken == "" {
		t.Fatalf("first page metadata = files %d total %d next %q", len(page1.Files), page1.TotalFiles, page1.NextPageToken)
	}

	secondURL := "/api/v1/sync/raw?page_token=" + url.QueryEscape(page1.NextPageToken)
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, secondURL, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second list status=%d body=%s", second.Code, second.Body.String())
	}
	var page2 rawSyncListPage
	if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Files) != 1 || page2.Snapshot != page1.Snapshot || page2.TotalFiles != page1.TotalFiles {
		t.Fatalf("second page metadata = %#v", page2)
	}

	if _, err := project.WriteBytes(context.Background(), []byte("changed"), "raw/nested/attachment-000.bin"); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	router.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, secondURL, nil))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale page token status=%d body=%s, want conflict", stale.Code, stale.Body.String())
	}

	payload := []byte("arbitrary nested attachment")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	uploadURL := "/api/v1/sync/raw/file?path=" + url.QueryEscape("incoming/deep/file.bin")
	upload := httptest.NewRecorder()
	uploadRequest := httptest.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(payload))
	uploadRequest.Header.Set("X-Content-SHA256", digest)
	router.ServeHTTP(upload, uploadRequest)
	if upload.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", upload.Code, upload.Body.String())
	}
	var writeResult struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &writeResult); err != nil || writeResult.Generation == "" {
		t.Fatalf("upload result=%q err=%v", upload.Body.String(), err)
	}

	downloadURL := "/api/v1/sync/raw/file?path=" + url.QueryEscape("incoming/deep/file.bin") + "&generation=" + writeResult.Generation
	download := httptest.NewRecorder()
	router.ServeHTTP(download, httptest.NewRequest(http.MethodGet, downloadURL, nil))
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), payload) || download.Header().Get("X-Raw-Generation") != writeResult.Generation {
		t.Fatalf("download status=%d body=%q headers=%v", download.Code, download.Body.Bytes(), download.Header())
	}

	staleUpload := httptest.NewRecorder()
	staleRequest := httptest.NewRequest(http.MethodPut, uploadURL, bytes.NewReader([]byte("stale")))
	staleRequest.Header.Set("X-Expected-Generation", "1")
	staleRequest.Header.Set("X-Content-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte("stale"))))
	router.ServeHTTP(staleUpload, staleRequest)
	if staleUpload.Code != http.StatusConflict {
		t.Fatalf("stale upload status=%d body=%s", staleUpload.Code, staleUpload.Body.String())
	}
}

type fakeSyncBindingChecker struct {
	err                                        error
	userID, projectID, bindingID, wikiID, host string
}

func (f *fakeSyncBindingChecker) CheckSyncBinding(_ context.Context, userID, projectID, bindingID, wikiID, host string) error {
	f.userID, f.projectID, f.bindingID, f.wikiID, f.host = userID, projectID, bindingID, wikiID, host
	return f.err
}

func TestRawSyncBindingMiddlewareRechecksAndFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := &fakeSyncBindingChecker{}
	h := &Handler{syncBindingAuthority: checker, syncBindingHost: "https://auth.example.test"}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("projectID", "project-1")
		c.Next()
	})
	router.GET("/raw", h.RawSyncBindingAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/raw", nil)
		req.Header.Set("X-Sync-Binding-ID", "binding-1")
		req.Header.Set("X-Wiki-ID", "wiki-1")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if recorder := request(); recorder.Code != http.StatusNoContent {
		t.Fatalf("active binding status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if checker.userID != "user-1" || checker.projectID != "project-1" || checker.bindingID != "binding-1" || checker.wikiID != "wiki-1" || checker.host != "https://auth.example.test" {
		t.Fatalf("binding check arguments = %#v", checker)
	}
	checker.err = auth.ErrSyncBindingUnauthorized
	if recorder := request(); recorder.Code != http.StatusForbidden {
		t.Fatalf("rejected binding status=%d, want forbidden", recorder.Code)
	}
	checker.err = auth.ErrSyncBindingUnavailable
	if recorder := request(); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable authority status=%d, want unavailable", recorder.Code)
	}
	h.syncBindingAuthority = nil
	if recorder := request(); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing authority status=%d, want unavailable", recorder.Code)
	}
}

type fakeRawDigestStore struct {
	meta    map[string]string
	files   map[string][]byte
	metaErr error
	readErr error
}

func (f *fakeRawDigestStore) Prefix() string { return "users/u/projects/p" }
func (f *fakeRawDigestStore) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.files[relPath]
	if !ok {
		return nil, storage.ErrObjectNotExist
	}
	return data, nil
}
func (f *fakeRawDigestStore) WriteBytes(context.Context, []byte, string) (string, error) {
	return "", errors.New("not implemented")
}
func (f *fakeRawDigestStore) WriteBytesAtomic(context.Context, []byte, string, string) (string, error) {
	return "", errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListProjects(context.Context, string) ([]store.Project, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListConcepts(context.Context, bool) ([]store.WikiPage, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListSources(context.Context) ([]store.WikiPage, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListConceptsFromCache(context.Context) ([]store.WikiPage, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListSourcesFromCache(context.Context) ([]store.WikiPage, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) GetPage(context.Context, string, string) (*store.WikiPage, []byte, error) {
	return nil, nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListMarkdownFiles(context.Context, string) ([]store.MarkdownFile, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) ListRawFiles(context.Context) ([]store.RawFile, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRawDigestStore) BucketStats(context.Context) (int64, int64, error) {
	return 0, 0, errors.New("not implemented")
}
func (f *fakeRawDigestStore) GetMetaSHA256(_ context.Context, relPath string) (string, error) {
	if f.metaErr != nil {
		return "", f.metaErr
	}
	if f.meta == nil {
		return "", nil
	}
	return f.meta[relPath], nil
}

func TestResolveExistingRawDigestUsesMetadata(t *testing.T) {
	s := &fakeRawDigestStore{meta: map[string]string{"raw/note.md": "abc"}}
	digest, exists, err := resolveExistingRawDigest(context.Background(), s, "raw/note.md")
	if err != nil || !exists || digest != "abc" {
		t.Fatalf("got digest=%q exists=%v err=%v", digest, exists, err)
	}
}

func TestResolveExistingRawDigestFallsBackToRead(t *testing.T) {
	content := []byte("# hello\n")
	want := fmt.Sprintf("%x", sha256.Sum256(content))
	s := &fakeRawDigestStore{
		meta:  map[string]string{},
		files: map[string][]byte{"raw/note.md": content},
	}
	digest, exists, err := resolveExistingRawDigest(context.Background(), s, "raw/note.md")
	if err != nil || !exists || digest != want {
		t.Fatalf("got digest=%q exists=%v err=%v want %q", digest, exists, err, want)
	}
}

func TestResolveExistingRawDigestMissingFile(t *testing.T) {
	s := &fakeRawDigestStore{meta: map[string]string{}, files: map[string][]byte{}}
	digest, exists, err := resolveExistingRawDigest(context.Background(), s, "raw/note.md")
	if err != nil || exists || digest != "" {
		t.Fatalf("got digest=%q exists=%v err=%v", digest, exists, err)
	}
}
