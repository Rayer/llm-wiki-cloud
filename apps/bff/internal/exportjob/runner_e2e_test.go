package exportjob

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"google.golang.org/api/option"
	_ "modernc.org/sqlite"
)

func TestHTTPToRunnerPublishesDigestVerifiedCanonicalArchiveLocally(t *testing.T) {
	ctx := context.Background()
	fs := exportEmulator(t)
	storageAPI := newFakeStorageAPI()
	storageServer := httptest.NewServer(storageAPI)
	t.Cleanup(storageServer.Close)
	storageClient, err := storage.NewClient(ctx, option.WithEndpoint(storageServer.URL+"/storage/v1/"), option.WithoutAuthentication(), option.WithHTTPClient(storageServer.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storageClient.Close() })

	const bucket, userID, projectID = "lwc346-local-export", "e2e-user", "e2e-project"
	projectPrefix := "users/" + userID + "/projects/" + projectID + "/"
	stateDB := exportStateFixture(t)
	stateBytes, err := os.ReadFile(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	stateMarker := []byte("LWC346_E2E_PROVIDER_SECRET_880301")
	envReference := []byte("LWC346_E2E_ENV_REFERENCE_880302")
	files := map[string][]byte{
		".synto/INDEX.json":     []byte(`{"schema":1,"concepts":[]}`),
		".synto/state.db":       stateBytes,
		"wiki/.drafts/draft.md": []byte("# Unpublished manual draft\n"),
		"wiki/session-notes.md": []byte("# User session notes\n"),
		"wiki/tokenization.md":  []byte("# Tokenization lesson\n"),
		"wiki.toml":             []byte("[provider]\nname = \"custom\"\nurl = \"https://api.deepseek.com/v1\"\napi_key = \"LWC346_E2E_PROVIDER_SECRET_880301\"\n\n[models]\nfast = \"deepseek-chat\"\nheavy = \"deepseek-reasoner\"\n\n[pipeline]\nauto_approve = true\n"),
		"synto.toml":            []byte("[providers.default]\nname = \"deepseek\"\nurl = \"https://api.deepseek.com/v1\"\ntimeout = 600\napi_key_env = \"LWC346_E2E_ENV_REFERENCE_880302\"\n\n[models.fast]\nprovider = \"default\"\nmodel = \"deepseek-flash\"\nctx = 16384\n"),
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	manifest := generation.Manifest{Version: generation.Version, GenerationID: "generation-e2e-001", CreatedAt: time.Now().UTC().Format(time.RFC3339), InputFingerprint: "local-fixture"}
	for i, path := range paths {
		generationNumber := int64(i + 10)
		file, err := generation.NewFile(path, files[path], generationNumber)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, file)
		storageAPI.put(projectPrefix+manifest.ObjectPath(file), files[path], generationNumber)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	storageAPI.put(projectPrefix+generation.ManifestPath, manifestBytes, 5)
	storageAPI.put(projectPrefix+"raw/original.md", []byte("raw source bytes"), 3)
	storageAPI.put(projectPrefix+"wiki/stale-top-level.md", []byte("must not be exported"), 4)
	storageAPI.put(projectPrefix+"vault-schema.md", []byte("schema from project storage"), 6)
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "name": "Integration Project", "description": "local fixture",
		"profile":  map[string]any{"language": "zh-TW"},
		"settings": map[string]any{"timezone": "Asia/Taipei"},
	}); err != nil {
		t.Fatal(err)
	}

	repo := NewFirestoreRepository(fs)
	cloud := &CloudArchiveStore{client: storageClient, bucket: bucket}
	archives := fakePublishedArchiveStore{cloud: cloud}
	starter := &localRunExportStarter{repo: repo, cloud: cloud, fs: fs}
	handler := NewHTTPHandler(repo, NewFirestoreProjectVerifier(fs), archives, starter)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("projectID", projectID)
		c.Next()
	})
	handler.Register(api)
	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"scope":"raw-full-metadata"}`))
	createRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(create, createRequest)
	if create.Code != http.StatusAccepted {
		t.Fatalf("API create status=%d body=%s runner error=%v requests=%v", create.Code, create.Body.String(), starter.err, storageAPI.requests)
	}

	stateResponse := httptest.NewRecorder()
	router.ServeHTTP(stateResponse, httptest.NewRequest(http.MethodGet, "/api/v1/exports", nil))
	if stateResponse.Code != http.StatusOK {
		t.Fatalf("API list status=%d body=%s", stateResponse.Code, stateResponse.Body.String())
	}
	var state State
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Current == nil || !state.Current.DownloadAvailable || state.Current.ExpiresAt.Sub(state.Current.CompletedAt) != Retention {
		t.Fatalf("API did not expose the published archive contract: %#v", state)
	}
	archiveName, err := ReadyObject(userID, projectID, state.Current.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes := storageAPI.get(archiveName)
	zipReader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("published archive is not a complete ZIP: %v", err)
	}
	entries := make(map[string]*zip.File, len(zipReader.File))
	for _, file := range zipReader.File {
		entries[file.Name] = file
	}
	for _, path := range []string{"raw/original.md", "wiki/.drafts/draft.md", "wiki/session-notes.md", "wiki/tokenization.md", "wiki.toml", "synto.toml", ".synto/INDEX.json", ".synto/state.db", ".lwc/publish/current.json", "vault-schema.md", "project-metadata/project.json", "export-meta.json"} {
		if entries[path] == nil {
			t.Errorf("archive omitted expected path %q", path)
		}
	}
	for _, path := range []string{"wiki/stale-top-level.md"} {
		if entries[path] != nil {
			t.Errorf("archive included noncanonical or credential-bearing source %q", path)
		}
	}
	var metadata ExportMeta
	metaBytes := readZipEntry(t, entries["export-meta.json"])
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Scope != ScopeRawFullMetadata || metadata.ProjectID != projectID || metadata.SourceVersions["wiki/.drafts/draft.md"] == "" {
		t.Fatalf("archive metadata lacks project/snapshot identity: %#v", metadata)
	}
	if !reflect.DeepEqual(metadata.RedactedFields["wiki.toml"], []string{"provider.api_key"}) || !reflect.DeepEqual(metadata.RedactedFields["synto.toml"], []string{"providers.default.api_key_env"}) {
		t.Fatalf("archive manifest redaction fields = %#v", metadata.RedactedFields)
	}
	if bytes.Contains(archiveBytes, stateMarker) || bytes.Contains(archiveBytes, envReference) {
		t.Fatal("ZIP bytes contain a provider secret marker or active auth environment reference")
	}
	if !bytes.Contains(readZipEntry(t, entries["wiki.toml"]), []byte(`fast = "deepseek-chat"`)) || !bytes.Contains(readZipEntry(t, entries["synto.toml"]), []byte(`model = "deepseek-flash"`)) {
		t.Fatal("sanitized configs did not preserve their nonsecret model settings")
	}
	assertArchiveRawNotesState(t, readZipEntry(t, entries[".synto/state.db"]))
	for _, digest := range metadata.Files {
		data := readZipEntry(t, entries[digest.Path])
		sum := sha256.Sum256(data)
		if int64(len(data)) != digest.Size || hex.EncodeToString(sum[:]) != digest.SHA256 {
			t.Fatalf("ZIP digest mismatch for %q: size=%d digest=%x, metadata=%#v", digest.Path, len(data), sum, digest)
		}
		if bytes.Contains(data, stateMarker) {
			t.Fatalf("archive entry %q retains a redacted provider secret marker", digest.Path)
		}
	}
	archiveDigest := sha256.Sum256(archiveBytes)
	t.Logf("local provider simulation: archive bytes=%d sha256=%x; every ZIP entry matched export-meta.json digests", len(archiveBytes), archiveDigest)
}

func exportStateFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/state.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE raw_notes (
		path TEXT PRIMARY KEY,
		content_hash TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'new',
		ingested_at TEXT,
		error TEXT
	);
	INSERT INTO raw_notes(path, content_hash, status, ingested_at, error)
	VALUES ('raw/original.md', 'source-sha256', 'ingested', '2026-09-25T00:00:00Z', '')`)
	closeErr := db.Close()
	if err := errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertArchiveRawNotesState(t *testing.T, data []byte) {
	t.Helper()
	path := t.TempDir() + "/state.db"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sourcePath, hash, status, ingestedAt, message string
	if err := db.QueryRow("SELECT path, content_hash, status, ingested_at, error FROM raw_notes").Scan(&sourcePath, &hash, &status, &ingestedAt, &message); err != nil {
		t.Fatal(err)
	}
	if sourcePath != "raw/original.md" || hash != "source-sha256" || status != "ingested" || ingestedAt == "" || message != "" {
		t.Fatalf("exported OLW raw_notes row = (%q, %q, %q, %q, %q)", sourcePath, hash, status, ingestedAt, message)
	}
}

func readZipEntry(t *testing.T, file *zip.File) []byte {
	t.Helper()
	if file == nil {
		t.Fatal("ZIP entry is missing")
	}
	reader, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		t.Fatal(err)
	}
	return data
}

type localRunExportStarter struct {
	repo  Repository
	cloud *CloudArchiveStore
	fs    *firestore.Client
	err   error
}

func (s *localRunExportStarter) Start(ctx context.Context, userID, projectID string, job Job) error {
	s.err = RunExport(ctx, s.repo, s.cloud, userID, projectID, job, s.fs)
	return s.err
}

type fakePublishedArchiveStore struct{ cloud *CloudArchiveStore }

func (s fakePublishedArchiveStore) Stat(ctx context.Context, userID, projectID, exportID string) (ArchiveInfo, error) {
	return s.cloud.Stat(ctx, userID, projectID, exportID)
}

func (fakePublishedArchiveStore) Sign(context.Context, string, string, string, time.Time, string) (string, error) {
	return "https://local-provider-simulation.invalid/archive", nil
}

type fakeStorageObject struct {
	Name               string            `json:"name"`
	Data               []byte            `json:"-"`
	Generation         int64             `json:"generation"`
	CustomTime         time.Time         `json:"customTime,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	ContentType        string            `json:"contentType,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
}

type fakeStorageAPI struct {
	mu       sync.Mutex
	objects  map[string]fakeStorageObject
	nextGen  int64
	requests []string
}

func newFakeStorageAPI() *fakeStorageAPI {
	return &fakeStorageAPI{objects: make(map[string]fakeStorageObject), nextGen: 1000}
}

func (s *fakeStorageAPI) put(name string, data []byte, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[name] = fakeStorageObject{Name: name, Data: append([]byte(nil), data...), Generation: generation}
}

func (s *fakeStorageAPI) get(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.objects[name].Data...)
}

func (s *fakeStorageAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	s.mu.Unlock()
	if strings.Contains(r.URL.Path, "/upload/storage/v1/") && r.Method == http.MethodPost {
		s.upload(w, r)
		return
	}
	if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/o") || strings.HasSuffix(r.URL.Path, "/o/")) {
		s.list(w, r)
		return
	}
	name, err := fakeStorageObjectName(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "invalid object path "+r.URL.EscapedPath(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.read(w, r, name)
	case http.MethodPatch:
		s.update(w, r, name)
	case http.MethodDelete:
		s.delete(w, r, name)
	default:
		http.Error(w, "unsupported storage method", http.StatusMethodNotAllowed)
	}
}

func fakeStorageObjectName(escapedPath string) (string, error) {
	marker := "/o/"
	index := strings.Index(escapedPath, marker)
	if index >= 0 {
		name := strings.TrimSuffix(escapedPath[index+len(marker):], "/")
		return url.PathUnescape(name)
	}
	// The Storage client uses the XML media endpoint for object readers.
	trimmed := strings.TrimPrefix(escapedPath, "/")
	index = strings.IndexByte(trimmed, '/')
	if index < 0 {
		return "", fmt.Errorf("object marker missing")
	}
	name := strings.TrimSuffix(trimmed[index+1:], "/")
	return url.PathUnescape(name)
}

func (s *fakeStorageAPI) upload(w http.ResponseWriter, r *http.Request) {
	var data []byte
	var resource struct {
		Metadata           map[string]string `json:"metadata"`
		ContentType        string            `json:"contentType"`
		ContentDisposition string            `json:"contentDisposition"`
		CustomTime         time.Time         `json:"customTime"`
	}
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(r.Body, params["boundary"])
		resourcePart, err := reader.NextPart()
		if err != nil {
			http.Error(w, "missing upload metadata", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(resourcePart).Decode(&resource); err != nil {
			http.Error(w, "invalid upload metadata", http.StatusBadRequest)
			return
		}
		mediaPart, err := reader.NextPart()
		if err != nil {
			http.Error(w, "missing media bytes", http.StatusBadRequest)
			return
		}
		data, err = io.ReadAll(mediaPart)
		if err != nil {
			http.Error(w, "read media bytes", http.StatusBadRequest)
			return
		}
	} else {
		var err error
		data, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read media bytes", http.StatusBadRequest)
			return
		}
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = resource.Metadata["name"]
	}
	if name == "" {
		http.Error(w, "missing object name", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.nextGen++
	object := fakeStorageObject{Name: name, Data: data, Generation: s.nextGen, Metadata: resource.Metadata, ContentType: resource.ContentType, ContentDisposition: resource.ContentDisposition, CustomTime: resource.CustomTime}
	s.objects[name] = object
	s.mu.Unlock()
	s.writeAttrs(w, object, http.StatusOK)
}

func (s *fakeStorageAPI) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	s.mu.Lock()
	items := make([]map[string]any, 0)
	for name, object := range s.objects {
		if strings.HasPrefix(name, prefix) {
			items = append(items, s.attrs(object))
		}
	}
	s.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (s *fakeStorageAPI) read(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	object, ok := s.objects[name]
	s.mu.Unlock()
	if !ok || !fakeGenerationMatches(r, object.Generation) {
		s.writeNotFound(w)
		return
	}
	if r.URL.Query().Get("alt") == "media" || strings.Contains(r.URL.Path, "/download/") || !strings.Contains(r.URL.EscapedPath(), "/o/") {
		w.Header().Set("Content-Type", object.ContentType)
		w.Header().Set("Content-Length", fmt.Sprint(len(object.Data)))
		w.Header().Set("X-Goog-Generation", fmt.Sprint(object.Generation))
		w.Header().Set("X-Goog-Hash", "crc32c="+fakeCRC32C(object.Data))
		_, _ = w.Write(object.Data)
		return
	}
	s.writeAttrs(w, object, http.StatusOK)
}

func (s *fakeStorageAPI) update(w http.ResponseWriter, r *http.Request, name string) {
	var update struct {
		CustomTime time.Time         `json:"customTime"`
		Metadata   map[string]string `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid object update", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	object, ok := s.objects[name]
	if ok && fakeGenerationMatches(r, object.Generation) {
		if !update.CustomTime.IsZero() {
			object.CustomTime = update.CustomTime
		}
		if update.Metadata != nil {
			object.Metadata = update.Metadata
		}
		s.objects[name] = object
	}
	s.mu.Unlock()
	if !ok {
		s.writeNotFound(w)
		return
	}
	s.writeAttrs(w, object, http.StatusOK)
}

func (s *fakeStorageAPI) delete(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	object, ok := s.objects[name]
	if ok && fakeGenerationMatches(r, object.Generation) {
		delete(s.objects, name)
	}
	s.mu.Unlock()
	if !ok {
		s.writeNotFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func fakeGenerationMatches(r *http.Request, generation int64) bool {
	value := r.URL.Query().Get("generation")
	return value == "" || value == fmt.Sprint(generation)
}

func (s *fakeStorageAPI) attrs(object fakeStorageObject) map[string]any {
	attrs := map[string]any{
		"name": object.Name, "bucket": "lwc346-local-export", "generation": fmt.Sprint(object.Generation),
		"metageneration": "1", "size": fmt.Sprint(len(object.Data)), "crc32c": fakeCRC32C(object.Data),
	}
	if object.ContentType != "" {
		attrs["contentType"] = object.ContentType
	}
	if object.ContentDisposition != "" {
		attrs["contentDisposition"] = object.ContentDisposition
	}
	if !object.CustomTime.IsZero() {
		attrs["customTime"] = object.CustomTime.UTC().Format(time.RFC3339Nano)
	}
	if object.Metadata != nil {
		attrs["metadata"] = object.Metadata
	}
	return attrs
}

func fakeCRC32C(data []byte) string {
	checksum := crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], checksum)
	return base64.StdEncoding.EncodeToString(raw[:])
}

func (s *fakeStorageAPI) writeAttrs(w http.ResponseWriter, object fakeStorageObject, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(s.attrs(object))
}

func (s *fakeStorageAPI) writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
}
