package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
)

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

func TestRawUploadResponseUsesRelativeRawPath(t *testing.T) {
	resp := newRawUploadResponse("note.md", 12, rawintake.Result{
		Path: "raw/note.md", SHA256: "abc123", Status: rawintake.StatusCreated,
	})

	if resp.Filename != "note.md" {
		t.Fatalf("filename = %q, want note.md", resp.Filename)
	}
	if resp.Path != "raw/note.md" {
		t.Fatalf("path = %q", resp.Path)
	}
	if resp.Bytes != 12 || resp.SHA256 != "abc123" {
		t.Fatalf("bytes=%d sha256=%q, want bytes=12 sha256=abc123", resp.Bytes, resp.SHA256)
	}
	if resp.Status != string(rawintake.StatusCreated) {
		t.Fatalf("status = %q, want %q", resp.Status, rawintake.StatusCreated)
	}
}

func TestRawUploadRejectsNonEmptyDirectoryWithoutWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("projectID", "project-1")
		c.Next()
	})
	router.POST("/api/v1/raw/upload", h.RawUpload)

	for _, path := range []string{"?dir=topic", "?dir=&dir=topic"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/raw/upload"+path, nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "destination directories are not supported") {
			t.Fatalf("request %q status=%d body=%s; want directory rejection", path, recorder.Code, recorder.Body.String())
		}
		files, err := root.WithScope("user-1", "project-1").ListRawFiles(context.Background())
		if err != nil || len(files) != 0 {
			t.Fatalf("request %q stored raw files=%#v err=%v; want no root or nested write", path, files, err)
		}
	}

	unauthenticated := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(unauthenticated)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/raw/upload?dir=topic", nil)
	h.RawUpload(ctx)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity status=%d body=%s; want auth rejection before directory validation", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func TestRawUploadStoresRootFileBytesThroughRoute(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}

	const firestoreProject = "llm-wiki-cloud"
	scope := fmt.Sprintf("lwc126-raw-upload-%d", time.Now().UnixNano())
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(firestoreProject, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create local Firestore client: %v", err)
	}
	t.Cleanup(func() { _ = fsClient.Close() })

	const userID, projectID = "lwc126-root-owner", "root-only"
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(context.Background(), map[string]any{"user_id": userID, "project_id": projectID, "status": rawUploadReadyStatus}); err != nil {
		t.Fatalf("seed disposable ready Project: %v", err)
	}
	t.Cleanup(func() { _, _ = projectRef.Delete(context.Background()) })

	root := localfs.New(t.TempDir())
	h := New(root, fsClient, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("projectID", projectID)
		c.Next()
	})
	router.POST("/api/v1/raw/upload", h.RawUpload)

	want := []byte("# root upload bytes\nUnicode metadata: 台北\n")
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	filePart, err := form.CreateFormFile("file", "root.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filePart.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/raw/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("root upload status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response rawUploadResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response %q: %v", recorder.Body.String(), err)
	}
	if response.Path != "raw/root.md" || response.Filename != "root.md" || response.Bytes != int64(len(want)) || response.Status != string(rawintake.StatusCreated) {
		t.Fatalf("upload response = %+v; want root path, exact byte count and created status", response)
	}
	stored, err := root.WithScope(userID, projectID).ReadFile(context.Background(), response.Path)
	if err != nil || !bytes.Equal(stored, want) {
		t.Fatalf("stored root bytes=%q err=%v; want %q", stored, err, want)
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
