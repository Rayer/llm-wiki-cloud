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
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

func TestHTTPToRunnerPublishesDigestVerifiedCanonicalArchiveLocally(t *testing.T) {
	ctx := context.Background()
	fs := exportEmulator(t)
	capture := otlpfixture.New(t)
	qaProject := "synthetic-project"
	if otlpfixture.CloudMode() {
		qaProject = os.Getenv("LWC_OTLP_QA_PROJECT")
	}
	provider := capture.Provider(t, "lwc-qa-export-fixture", "local-fixture", qaProject)
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
	stateDB, stateSecretMarkers := exportStateFixture(t)
	stateBytes, err := os.ReadFile(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	sourceStateDigest := sha256.Sum256(stateBytes)
	sourceStateSnapshot := snapshotExportStateSQLite(t, stateBytes)
	for _, marker := range stateSecretMarkers {
		if !bytes.Contains(stateBytes, marker) {
			t.Fatalf("source state fixture does not contain marker %q", marker)
		}
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
	var stateObjectName, stateGenerationVersion string
	var stateGeneration int64
	for i, path := range paths {
		generationNumber := int64(i + 10)
		file, err := generation.NewFile(path, files[path], generationNumber)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, file)
		objectName := projectPrefix + manifest.ObjectPath(file)
		storageAPI.put(objectName, files[path], generationNumber)
		if path == ".synto/state.db" {
			stateObjectName = objectName
			stateGeneration = generationNumber
			stateGenerationVersion = manifest.GenerationID + "@" + fmt.Sprint(generationNumber)
		}
	}
	if stateObjectName == "" || stateGenerationVersion == "" || stateGeneration == 0 {
		t.Fatal("producer-shaped source state database was not assigned a generation")
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	storageAPI.put(projectPrefix+generation.ManifestPath, manifestBytes, 5)
	storageAPI.put(projectPrefix+"raw/original.md", []byte("raw source bytes"), 3)
	storageAPI.put(projectPrefix+"wiki/stale-top-level.md", []byte("must not be exported"), 4)
	storageAPI.put(projectPrefix+"vault-schema.md", []byte("schema from project storage"), 6)
	foreignProjectPath := "users/" + userID + "/projects/other-project/wiki/foreign-project-sentinel.md"
	foreignProjectMarker := []byte("LWC486_FOREIGN_PROJECT_SENTINEL_880303")
	foreignOwnerPath := "users/other-user/projects/" + projectID + "/wiki/foreign-owner-sentinel.md"
	foreignOwnerMarker := []byte("LWC486_FOREIGN_OWNER_SENTINEL_880304")
	storageAPI.put(foreignProjectPath, foreignProjectMarker, 7)
	storageAPI.put(foreignOwnerPath, foreignOwnerMarker, 8)
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
	router.Use(observability.TraceContextMiddleware())
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
	traceID, parentID, traceParent := otlpfixture.NewParent(t)
	createRequest.Header.Set("traceparent", traceParent)
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
		if _, exists := entries[file.Name]; exists {
			t.Fatalf("archive contains duplicate entry %q", file.Name)
		}
		entries[file.Name] = file
	}
	expectedEntryPaths := []string{"raw/original.md", "wiki/.drafts/draft.md", "wiki/session-notes.md", "wiki/tokenization.md", "wiki.toml", "synto.toml", ".synto/INDEX.json", ".synto/state.db", ".lwc/publish/current.json", "vault-schema.md", "project-metadata/project.json", "export-meta.json"}
	actualEntryPaths := make([]string, 0, len(entries))
	for path := range entries {
		actualEntryPaths = append(actualEntryPaths, path)
	}
	sort.Strings(expectedEntryPaths)
	sort.Strings(actualEntryPaths)
	if !reflect.DeepEqual(actualEntryPaths, expectedEntryPaths) {
		t.Fatalf("archive entry paths = %#v, want exact set %#v", actualEntryPaths, expectedEntryPaths)
	}
	for _, path := range expectedEntryPaths {
		if entries[path] == nil {
			t.Errorf("archive omitted expected path %q", path)
		}
	}
	for _, path := range []string{"wiki/stale-top-level.md"} {
		if entries[path] != nil {
			t.Errorf("archive included noncanonical or credential-bearing source %q", path)
		}
	}
	for _, path := range []string{"wiki/foreign-project-sentinel.md", "wiki/foreign-owner-sentinel.md", foreignProjectPath, foreignOwnerPath} {
		if entries[path] != nil {
			t.Errorf("archive included a foreign-owner or foreign-Project sentinel entry %q", path)
		}
	}
	for name, entry := range entries {
		if strings.Contains(name, "foreign-project-sentinel.md") || strings.Contains(name, "foreign-owner-sentinel.md") {
			t.Errorf("archive contains a foreign-owner or foreign-Project sentinel path %q", name)
		}
		contents := readZipEntry(t, entry)
		if bytes.Contains(contents, foreignProjectMarker) || bytes.Contains(contents, foreignOwnerMarker) {
			t.Errorf("archive entry %q contains foreign-owner or foreign-Project sentinel contents", name)
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
	if got := metadata.SourceVersions[".synto/state.db"]; got != stateGenerationVersion {
		t.Fatalf("archive state database source version = %q, want pinned generation %q", got, stateGenerationVersion)
	}
	expectedManifestPaths := make([]string, 0, len(expectedEntryPaths)-1)
	for _, path := range expectedEntryPaths {
		if path != "export-meta.json" {
			expectedManifestPaths = append(expectedManifestPaths, path)
		}
	}
	actualManifestPaths := make([]string, 0, len(metadata.Files))
	for _, digest := range metadata.Files {
		actualManifestPaths = append(actualManifestPaths, digest.Path)
	}
	sort.Strings(expectedManifestPaths)
	sort.Strings(actualManifestPaths)
	if !reflect.DeepEqual(actualManifestPaths, expectedManifestPaths) {
		t.Fatalf("manifest file paths = %#v, want exact payload set %#v", actualManifestPaths, expectedManifestPaths)
	}
	if !reflect.DeepEqual(metadata.RedactedFields["wiki.toml"], []string{"provider.api_key"}) || !reflect.DeepEqual(metadata.RedactedFields["synto.toml"], []string{"providers.default.api_key_env"}) {
		t.Fatalf("archive manifest redaction fields = %#v", metadata.RedactedFields)
	}
	if bytes.Contains(archiveBytes, stateMarker) || bytes.Contains(archiveBytes, envReference) {
		t.Fatal("ZIP bytes contain a provider secret marker or active auth environment reference")
	}
	for _, marker := range stateSecretMarkers {
		if bytes.Contains(archiveBytes, marker) {
			t.Fatalf("ZIP bytes retain a synthetic SQLite secret marker %q", marker)
		}
	}
	if !bytes.Contains(readZipEntry(t, entries["wiki.toml"]), []byte(`fast = "deepseek-chat"`)) || !bytes.Contains(readZipEntry(t, entries["synto.toml"]), []byte(`model = "deepseek-flash"`)) {
		t.Fatal("sanitized configs did not preserve their nonsecret model settings")
	}
	assertArchiveRawNotesState(t, readZipEntry(t, entries[".synto/state.db"]))
	stateArchiveBytes := readZipEntry(t, entries[".synto/state.db"])
	archiveStateSnapshot := snapshotExportStateSQLite(t, stateArchiveBytes)
	if !reflect.DeepEqual(archiveStateSnapshot.Schema, sourceStateSnapshot.Schema) {
		t.Fatalf("sanitized archive state schema/indexes changed: source=%#v archive=%#v", sourceStateSnapshot.Schema, archiveStateSnapshot.Schema)
	}
	if !reflect.DeepEqual(archiveStateSnapshot.RowCounts, sourceStateSnapshot.RowCounts) {
		t.Fatalf("sanitized archive state row counts changed: source=%#v archive=%#v", sourceStateSnapshot.RowCounts, archiveStateSnapshot.RowCounts)
	}
	assertArchiveSQLiteCacheState(t, stateArchiveBytes, stateSecretMarkers)
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
	stateBytesAfter, err := os.ReadFile(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	stateDigestAfter := sha256.Sum256(stateBytesAfter)
	if !bytes.Equal(stateBytesAfter, stateBytes) || stateDigestAfter != sourceStateDigest {
		t.Fatalf("closed source state fixture changed: before_sha256=%x after_sha256=%x", sourceStateDigest, stateDigestAfter)
	}
	if after := snapshotExportStateSQLite(t, stateBytesAfter); !reflect.DeepEqual(after, sourceStateSnapshot) {
		t.Fatalf("source state fixture logical schema/index/rows changed: before=%#v after=%#v", sourceStateSnapshot, after)
	}
	sourceStateObject, exists := storageAPI.getObject(stateObjectName)
	if !exists || !bytes.Equal(sourceStateObject.Data, stateBytes) || sourceStateObject.Generation != stateGeneration {
		t.Fatalf("source state object changed during Export: exists=%t generation=%d want=%s bytes_equal=%t", exists, sourceStateObject.Generation, stateGenerationVersion, bytes.Equal(sourceStateObject.Data, stateBytes))
	}
	archiveDigest := sha256.Sum256(archiveBytes)
	t.Logf("local provider simulation: archive bytes=%d sha256=%x; every ZIP entry matched export-meta.json digests", len(archiveBytes), archiveDigest)
	capture.Flush(t, provider)
	capture.LogIdentities(t, "UC6")
	t.Logf("OTLP_QA_CASE=UC6 trace_id=%s parent_span_id=%s expected_spans=export.request,export.start,export.worker", traceID, parentID)
	if otlpfixture.CloudMode() {
		return
	}
	var requestSpan, startSpan, workerSpan *tracev1.Span
	var encoded bytes.Buffer
	for _, batch := range capture.Requests() {
		data, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					switch span.Name {
					case "export.request":
						if hex.EncodeToString(span.TraceId) == traceID && hex.EncodeToString(span.ParentSpanId) == parentID {
							requestSpan = span
						}
					case "export.start":
						if hex.EncodeToString(span.TraceId) == traceID {
							startSpan = span
						}
					case "export.worker":
						if len(span.ParentSpanId) == 0 && len(span.Links) == 1 && hex.EncodeToString(span.Links[0].TraceId) == traceID {
							workerSpan = span
						}
					}
				}
			}
		}
	}
	if requestSpan == nil || hex.EncodeToString(requestSpan.TraceId) != traceID || hex.EncodeToString(requestSpan.ParentSpanId) != parentID ||
		startSpan == nil || !bytes.Equal(startSpan.ParentSpanId, requestSpan.SpanId) ||
		workerSpan == nil || len(workerSpan.ParentSpanId) != 0 || len(workerSpan.Links) != 1 ||
		!bytes.Equal(workerSpan.Links[0].TraceId, startSpan.TraceId) || !bytes.Equal(workerSpan.Links[0].SpanId, startSpan.SpanId) ||
		len(workerSpan.Links[0].Attributes) != 0 {
		t.Fatalf("export request/start/worker causal chain is wrong: request=%+v start=%+v worker=%+v", requestSpan, startSpan, workerSpan)
	}
	if bytes.Contains(encoded.Bytes(), stateMarker) || bytes.Contains(encoded.Bytes(), envReference) {
		t.Fatal("exporter protobuf retained a credential canary from the synthetic archive")
	}
}

func TestOTLPCaptureExportHTTPToRunnerLocalSyntheticObjectStore(t *testing.T) {
	ctx := context.Background()
	capture := otlpfixture.New(t)
	qaProject := "synthetic-project"
	if otlpfixture.CloudMode() {
		qaProject = os.Getenv("LWC_OTLP_QA_PROJECT")
	}
	provider := capture.Provider(t, "lwc-qa-export-fixture", "local-fixture", qaProject)
	storageAPI := newFakeStorageAPI()
	storageServer := httptest.NewServer(storageAPI)
	t.Cleanup(storageServer.Close)
	storageClient, err := storage.NewClient(ctx, option.WithEndpoint(storageServer.URL+"/storage/v1/"), option.WithoutAuthentication(), option.WithHTTPClient(storageServer.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storageClient.Close() })

	const bucket, userID, projectID = "lwc398-local-export", "synthetic-user", "synthetic-project"
	prefix := "users/" + userID + "/projects/" + projectID + "/"
	storageAPI.put(prefix+"raw/source.txt", []byte("synthetic source"), 1)
	storageAPI.put(prefix+"wiki/fixture.md", []byte("# Synthetic fixture"), 2)
	repo := newTestRepository()
	cloud := &CloudArchiveStore{client: storageClient, bucket: bucket}
	archives := fakePublishedArchiveStore{cloud: cloud}
	starter := &localRunExportStarter{repo: repo, cloud: cloud}
	handler := NewHTTPHandler(repo, testProjectVerifier{name: "Synthetic Fixture"}, archives, starter)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(observability.TraceContextMiddleware())
	api := router.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("projectID", projectID)
		c.Next()
	})
	handler.Register(api)
	traceID, parentID, traceParent := otlpfixture.NewParent(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"scope":"raw-full"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", traceParent)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || starter.err != nil {
		t.Fatalf("synthetic export create status=%d body=%s runner=%v", response.Code, response.Body.String(), starter.err)
	}
	job := repo.jobs["export-1"]
	archiveName, err := ReadyObject(userID, projectID, job.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes := storageAPI.get(archiveName)
	if len(archiveBytes) == 0 {
		t.Fatal("local export runner did not publish its synthetic archive")
	}
	capture.Flush(t, provider)
	capture.LogIdentities(t, "UC6")
	t.Logf("OTLP_QA_CASE=UC6 trace_id=%s parent_span_id=%s expected_http_status=202", traceID, parentID)
	for _, identity := range capture.Identities() {
		if strings.HasPrefix(identity.Scope, "cloud.google.com/go") {
			t.Fatalf("excluded Google SDK span crossed the production filter into QA identity capture: %s/%s", identity.Scope, identity.Name)
		}
	}
	for _, batch := range capture.Requests() {
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				if strings.HasPrefix(scope.Scope.Name, "cloud.google.com/go") {
					t.Fatalf("excluded Google SDK span crossed the production filter into OTLP: %s", scope.Scope.Name)
				}
			}
		}
	}
	if otlpfixture.CloudMode() {
		return
	}
	var requestSpan, startSpan, workerSpan *tracev1.Span
	var encoded bytes.Buffer
	for _, batch := range capture.Requests() {
		data, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					switch span.Name {
					case "export.request":
						requestSpan = span
					case "export.start":
						startSpan = span
					case "export.worker":
						workerSpan = span
					}
				}
			}
		}
	}
	if requestSpan == nil || hex.EncodeToString(requestSpan.TraceId) != traceID || hex.EncodeToString(requestSpan.ParentSpanId) != parentID ||
		startSpan == nil || !bytes.Equal(startSpan.ParentSpanId, requestSpan.SpanId) ||
		workerSpan == nil || len(workerSpan.ParentSpanId) != 0 || len(workerSpan.Links) != 1 ||
		!bytes.Equal(workerSpan.Links[0].TraceId, startSpan.TraceId) || !bytes.Equal(workerSpan.Links[0].SpanId, startSpan.SpanId) ||
		len(workerSpan.Links[0].Attributes) != 0 {
		t.Fatalf("export request/start/worker causal chain is wrong: request=%+v start=%+v worker=%+v", requestSpan, startSpan, workerSpan)
	}
	if bytes.Contains(encoded.Bytes(), []byte("Synthetic Fixture")) || bytes.Contains(encoded.Bytes(), []byte("synthetic source")) {
		t.Fatal("exporter protobuf contains synthetic project content")
	}
}

func exportStateFixture(t *testing.T) (string, [][]byte) {
	t.Helper()
	path := t.TempDir() + "/state.db"
	nonceHash := sha256.Sum256([]byte(path))
	nonce := hex.EncodeToString(nonceHash[:6])
	secretMarkers := [][]byte{
		[]byte("LWC495_CACHE_ORDINARY_SECRET_" + nonce),
		[]byte("LWC495_CACHE_MALFORMED_SECRET_" + nonce),
		[]byte("LWC495_CACHE_VALID_SECRET_" + nonce),
		[]byte("LWC495_NULLABLE_MALFORMED_SECRET_" + nonce),
		[]byte("LWC495_NULLABLE_VALID_SECRET_" + nonce),
	}
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
	CREATE INDEX raw_notes_status_idx ON raw_notes(status);
	INSERT INTO raw_notes(path, content_hash, status, ingested_at, error)
	VALUES ('raw/original.md', 'source-sha256', 'ingested', '2026-09-25T00:00:00Z', '')`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE llm_cache (
		cache_key TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		response_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		last_hit_at TEXT,
		hit_count INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	cacheRows := []struct {
		key, response, createdAt string
		lastHit                  any
		hitCount                 any
	}{
		{key: "cache-ordinary", response: "ordinary response " + string(secretMarkers[0]), createdAt: "2026-10-01T00:00:00Z"},
		{key: "cache-malformed", response: fmt.Sprintf(`{"token":%q,broken`, string(secretMarkers[1])), createdAt: "2026-10-02T00:00:00Z", lastHit: "2026-11-02T00:00:00Z", hitCount: 4},
		{key: "cache-valid", response: fmt.Sprintf(`{"message":"ordinary response field","profile":{"refresh_token":%q,"label":"ordinary nested field"}}`, string(secretMarkers[2])), createdAt: "2026-10-03T00:00:00Z", hitCount: 7},
	}
	for _, row := range cacheRows {
		var err error
		if row.hitCount == nil {
			_, err = db.Exec(`INSERT INTO llm_cache(cache_key, model, response_json, created_at, last_hit_at)
				VALUES (?, 'synthetic-model', ?, ?, ?)`, row.key, row.response, row.createdAt, row.lastHit)
		} else {
			_, err = db.Exec(`INSERT INTO llm_cache(cache_key, model, response_json, created_at, last_hit_at, hit_count)
				VALUES (?, 'synthetic-model', ?, ?, ?, ?)`, row.key, row.response, row.createdAt, row.lastHit, row.hitCount)
		}
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`CREATE TABLE nullable_json_controls (
		id TEXT PRIMARY KEY,
		response_json TEXT,
		payload TEXT
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	nullableRows := []struct {
		id, response string
		payload      string
	}{
		{id: "nullable-malformed", response: fmt.Sprintf(`{"authorization":%q,broken`, string(secretMarkers[3])), payload: "opaque malformed payload"},
		{id: "nullable-existing-null", response: "", payload: "opaque null payload"},
		{id: "nullable-valid", response: fmt.Sprintf(`{"display":"nullable ordinary field","nested":{"api_key":%q,"title":"nullable ordinary nested field"}}`, string(secretMarkers[4])), payload: "opaque valid payload"},
	}
	for _, row := range nullableRows {
		var response any = row.response
		if row.id == "nullable-existing-null" {
			response = nil
		}
		if _, err := db.Exec(`INSERT INTO nullable_json_controls(id, response_json, payload) VALUES (?, ?, ?)`, row.id, response, row.payload); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, secretMarkers
}

type exportSQLiteSchemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

type exportSQLiteSnapshot struct {
	Schema    []exportSQLiteSchemaObject
	RowCounts map[string]int
	Rows      map[string][][]string
}

func snapshotExportStateSQLite(t *testing.T, data []byte) exportSQLiteSnapshot {
	t.Helper()
	db := openExportStateBytes(t, data)
	defer db.Close()

	snapshot := exportSQLiteSnapshot{RowCounts: make(map[string]int), Rows: make(map[string][][]string)}
	schemaRows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE (type = 'table' AND name NOT LIKE 'sqlite_%') OR (type = 'index' AND tbl_name NOT LIKE 'sqlite_%')
		ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	for schemaRows.Next() {
		var object exportSQLiteSchemaObject
		if err := schemaRows.Scan(&object.Type, &object.Name, &object.Table, &object.SQL); err != nil {
			_ = schemaRows.Close()
			t.Fatal(err)
		}
		snapshot.Schema = append(snapshot.Schema, object)
	}
	if err := errors.Join(schemaRows.Err(), schemaRows.Close()); err != nil {
		t.Fatal(err)
	}
	tableRows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for tableRows.Next() {
		var table string
		if err := tableRows.Scan(&table); err != nil {
			_ = tableRows.Close()
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := errors.Join(tableRows.Err(), tableRows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + quoteIdentifier(table) + " ORDER BY rowid")
		if err != nil {
			t.Fatalf("snapshot table %q: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatalf("snapshot row from %q: %v", table, err)
			}
			row := make([]string, len(values))
			for i, value := range values {
				row[i] = formatExportSQLiteCell(value)
			}
			snapshot.Rows[table] = append(snapshot.Rows[table], row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatalf("finish snapshot table %q: %v", table, err)
		}
		snapshot.RowCounts[table] = len(snapshot.Rows[table])
	}
	return snapshot
}

func formatExportSQLiteCell(value any) string {
	switch value := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return "BLOB:" + hex.EncodeToString(value)
	case string:
		return "TEXT:" + value
	case int64:
		return fmt.Sprintf("INTEGER:%d", value)
	case float64:
		return fmt.Sprintf("REAL:%g", value)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}

func openExportStateBytes(t *testing.T, data []byte) *sql.DB {
	t.Helper()
	path := t.TempDir() + "/state.db"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertArchiveSQLiteCacheState(t *testing.T, data []byte, secretMarkers [][]byte) {
	t.Helper()
	for _, marker := range secretMarkers {
		if bytes.Contains(data, marker) {
			t.Fatalf("sanitized SQLite bytes retain secret marker %q", marker)
		}
	}
	db := openExportStateBytes(t, data)
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("sanitized archive state quick_check = %q, %v", check, err)
	}

	var keys []string
	keyRows, err := db.Query("SELECT cache_key FROM llm_cache ORDER BY cache_key")
	if err != nil {
		t.Fatal(err)
	}
	for keyRows.Next() {
		var key string
		if err := keyRows.Scan(&key); err != nil {
			_ = keyRows.Close()
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := errors.Join(keyRows.Err(), keyRows.Close()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"cache-malformed", "cache-ordinary", "cache-valid"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("sanitized cache keys = %#v, want %#v", keys, want)
	}
	var cacheCount int
	if err := db.QueryRow("SELECT count(*) FROM llm_cache").Scan(&cacheCount); err != nil || cacheCount != 3 {
		t.Fatalf("sanitized cache row count = %d, %v; want 3", cacheCount, err)
	}
	for _, key := range []string{"cache-ordinary", "cache-malformed"} {
		var value string
		var isNull int
		var storageType string
		if err := db.QueryRow("SELECT response_json, response_json IS NULL, typeof(response_json) FROM llm_cache WHERE cache_key=?", key).Scan(&value, &isNull, &storageType); err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil || value != "null" || decoded != nil || isNull != 0 || storageType != "text" {
			t.Fatalf("sanitized NOT NULL response_json %q = (%q, decoded=%#v, IS NULL=%d, typeof=%q), err=%v; want bound TEXT null", key, value, decoded, isNull, storageType, err)
		}
	}
	var validResponse string
	if err := db.QueryRow("SELECT response_json FROM llm_cache WHERE cache_key='cache-valid'").Scan(&validResponse); err != nil {
		t.Fatal(err)
	}
	var validJSON map[string]any
	if err := json.Unmarshal([]byte(validResponse), &validJSON); err != nil {
		t.Fatalf("sanitized valid cache JSON %q: %v", validResponse, err)
	}
	profile, ok := validJSON["profile"].(map[string]any)
	if !ok || validJSON["message"] != "ordinary response field" || profile["label"] != "ordinary nested field" {
		t.Fatalf("sanitized valid cache JSON lost ordinary fields: %#v", validJSON)
	}
	if _, exists := profile["refresh_token"]; exists {
		t.Fatalf("sanitized valid cache JSON retained nested secret: %#v", validJSON)
	}

	type cacheMetadata struct {
		model, createdAt string
		lastHit          sql.NullString
		hitCount         int
	}
	metadataRows, err := db.Query("SELECT cache_key, model, created_at, last_hit_at, hit_count FROM llm_cache ORDER BY cache_key")
	if err != nil {
		t.Fatal(err)
	}
	metadata := make(map[string]cacheMetadata)
	for metadataRows.Next() {
		var key string
		var row cacheMetadata
		if err := metadataRows.Scan(&key, &row.model, &row.createdAt, &row.lastHit, &row.hitCount); err != nil {
			_ = metadataRows.Close()
			t.Fatal(err)
		}
		metadata[key] = row
	}
	if err := errors.Join(metadataRows.Err(), metadataRows.Close()); err != nil {
		t.Fatal(err)
	}
	wantMetadata := map[string]cacheMetadata{
		"cache-ordinary":  {model: "synthetic-model", createdAt: "2026-10-01T00:00:00Z", lastHit: sql.NullString{}, hitCount: 0},
		"cache-malformed": {model: "synthetic-model", createdAt: "2026-10-02T00:00:00Z", lastHit: sql.NullString{String: "2026-11-02T00:00:00Z", Valid: true}, hitCount: 4},
		"cache-valid":     {model: "synthetic-model", createdAt: "2026-10-03T00:00:00Z", lastHit: sql.NullString{}, hitCount: 7},
	}
	if !reflect.DeepEqual(metadata, wantMetadata) {
		t.Fatalf("sanitized cache metadata = %#v, want %#v", metadata, wantMetadata)
	}

	type columnInfo struct {
		name, kind       string
		notNull, primary int
		defaultValue     sql.NullString
	}
	var columns []columnInfo
	columnRows, err := db.Query(`PRAGMA table_info("llm_cache")`)
	if err != nil {
		t.Fatal(err)
	}
	for columnRows.Next() {
		var cid int
		var column columnInfo
		if err := columnRows.Scan(&cid, &column.name, &column.kind, &column.notNull, &column.defaultValue, &column.primary); err != nil {
			_ = columnRows.Close()
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := errors.Join(columnRows.Err(), columnRows.Close()); err != nil {
		t.Fatal(err)
	}
	wantColumns := []columnInfo{
		{name: "cache_key", kind: "TEXT", primary: 1},
		{name: "model", kind: "TEXT", notNull: 1},
		{name: "response_json", kind: "TEXT", notNull: 1},
		{name: "created_at", kind: "TEXT", notNull: 1},
		{name: "last_hit_at", kind: "TEXT"},
		{name: "hit_count", kind: "INTEGER", notNull: 1, defaultValue: sql.NullString{String: "0", Valid: true}},
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("sanitized llm_cache table_info = %#v, want %#v", columns, wantColumns)
	}
	indexRows, err := db.Query(`PRAGMA index_list("llm_cache")`)
	if err != nil {
		t.Fatal(err)
	}
	type indexInfo struct {
		name, origin    string
		unique, partial int
	}
	var indexes []indexInfo
	for indexRows.Next() {
		var sequence int
		var index indexInfo
		if err := indexRows.Scan(&sequence, &index.name, &index.unique, &index.origin, &index.partial); err != nil {
			_ = indexRows.Close()
			t.Fatal(err)
		}
		indexes = append(indexes, index)
	}
	if err := errors.Join(indexRows.Err(), indexRows.Close()); err != nil {
		t.Fatal(err)
	}
	if want := []indexInfo{{name: "sqlite_autoindex_llm_cache_1", origin: "pk", unique: 1}}; !reflect.DeepEqual(indexes, want) {
		t.Fatalf("sanitized llm_cache indexes = %#v, want %#v", indexes, want)
	}

	for _, id := range []string{"nullable-malformed", "nullable-existing-null"} {
		var value sql.NullString
		var isNull int
		var storageType string
		if err := db.QueryRow("SELECT response_json, response_json IS NULL, typeof(response_json) FROM nullable_json_controls WHERE id=?", id).Scan(&value, &isNull, &storageType); err != nil {
			t.Fatal(err)
		}
		if value.Valid || isNull != 1 || storageType != "null" {
			t.Fatalf("nullable response_json %q = (%#v, IS NULL=%d, typeof=%q), want SQL NULL", id, value, isNull, storageType)
		}
	}
	var nullableValid, opaquePayload string
	if err := db.QueryRow("SELECT response_json, payload FROM nullable_json_controls WHERE id='nullable-valid'").Scan(&nullableValid, &opaquePayload); err != nil {
		t.Fatal(err)
	}
	var nullableJSON map[string]any
	if err := json.Unmarshal([]byte(nullableValid), &nullableJSON); err != nil {
		t.Fatalf("sanitized nullable JSON %q: %v", nullableValid, err)
	}
	nested, ok := nullableJSON["nested"].(map[string]any)
	if !ok || nullableJSON["display"] != "nullable ordinary field" || nested["title"] != "nullable ordinary nested field" {
		t.Fatalf("sanitized nullable JSON lost ordinary fields: %#v", nullableJSON)
	}
	if _, exists := nested["api_key"]; exists {
		t.Fatalf("sanitized nullable JSON retained nested secret: %#v", nullableJSON)
	}
	if opaquePayload != "opaque valid payload" {
		t.Fatalf("non-json-named payload changed during sanitizer: %q", opaquePayload)
	}
	var malformedOpaquePayload string
	if err := db.QueryRow("SELECT payload FROM nullable_json_controls WHERE id='nullable-malformed'").Scan(&malformedOpaquePayload); err != nil {
		t.Fatal(err)
	}
	if malformedOpaquePayload != "opaque malformed payload" {
		t.Fatalf("non-json-named malformed payload = %q, want unchanged", malformedOpaquePayload)
	}
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
	object, _ := s.getObject(name)
	return object.Data
}

func (s *fakeStorageAPI) getObject(name string) (fakeStorageObject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, exists := s.objects[name]
	object.Data = append([]byte(nil), object.Data...)
	return object, exists
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
