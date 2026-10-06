package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/annotation"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localpipeline"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This exercises the actual browser-facing BFF trigger/status routes and a
// separately built native worker. The tagged worker replaces only Synto; it
// still runs cloud materialization, worker reconciliation, GCS generation
// publication, manifest readback, and Firestore execution status writes.
func TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure(t *testing.T) {
	storageEndpoint := loopbackEmulatorEndpoint(t, "STORAGE_EMULATOR_HOST")
	firestoreEndpoint := loopbackEmulatorEndpoint(t, "FIRESTORE_EMULATOR_HOST")
	if storageEndpoint == "" || firestoreEndpoint == "" {
		t.Skip("requires loopback STORAGE_EMULATOR_HOST and FIRESTORE_EMULATOR_HOST")
	}

	const (
		project  = "llm-wiki-cloud"
		bucket   = "llm-wiki-cloud-local"
		database = "llm-wiki-cloud-local"
		userID   = "lwc361-test-user"
		secret   = "test-only-local-pipeline-signing-secret"
	)
	scope := "lwc361-test-" + localPipelineTestNonce(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cloudClient, err := cloudstorage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create emulator Storage client: %v", err)
	}
	fsClient, err := scopedfirestore.NewClientWithDatabase(project, database, "", "")
	if err != nil {
		_ = cloudClient.Close()
		t.Fatalf("create emulator Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClient(bucket)
	if err != nil {
		_ = fsClient.Close()
		_ = cloudClient.Close()
		t.Fatalf("create scoped GCS client: %v", err)
	}
	otherScope := scope + "-retained"
	managerClosed := false
	var manager *localpipeline.Manager
	sentinelSeeded := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if !managerClosed && manager != nil {
			if err := manager.Close(cleanupCtx); err != nil {
				t.Errorf("close native worker manager during cleanup: %v", err)
			}
		}
		if err := deleteLocalPipelineFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("delete/read back Firestore local scope: %v", err)
		}
		if err := deleteLocalPipelineObjectScope(cleanupCtx, cloudClient, bucket, scope); err != nil {
			t.Errorf("delete/read back Storage local scope: %v", err)
		}
		if sentinelSeeded {
			if err := verifyLocalPipelineRetainedSentinels(cleanupCtx, fsClient.Raw(), cloudClient, bucket, otherScope); err != nil {
				t.Errorf("other-scope sentinel readback failed: %v", err)
			}
		}
		if err := storageClient.Close(); err != nil {
			t.Errorf("close scoped Storage client: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close Firestore client: %v", err)
		}
		if err := cloudClient.Close(); err != nil {
			t.Errorf("close Storage client: %v", err)
		}
	})

	bucketHandle := cloudClient.Bucket(bucket)
	if _, err := bucketHandle.Attrs(ctx); errors.Is(err, cloudstorage.ErrBucketNotExist) {
		if err := bucketHandle.Create(ctx, project, nil); err != nil {
			t.Fatalf("create disposable emulator bucket: %v", err)
		}
	} else if err != nil {
		t.Fatalf("read emulator bucket: %v", err)
	}
	if _, err := fsClient.Raw().Collection("local_scopes").Doc(otherScope).Collection("executions").Doc("sentinel").Set(ctx, map[string]any{"sentinel": "keep"}); err != nil {
		t.Fatalf("seed other-scope Firestore sentinel: %v", err)
	}
	sentinelWriter := cloudClient.Bucket(bucket).Object("local_scopes/" + otherScope + "/sentinel.txt").NewWriter(ctx)
	if _, err := sentinelWriter.Write([]byte("keep")); err != nil {
		_ = sentinelWriter.Close()
		t.Fatalf("seed other-scope Storage sentinel: %v", err)
	}
	if err := sentinelWriter.Close(); err != nil {
		t.Fatalf("commit other-scope Storage sentinel: %v", err)
	}
	sentinelSeeded = true

	metadataProbe := []byte("local pipeline metadata probe")
	metadataDigestBytes := sha256.Sum256(metadataProbe)
	metadataDigest := hex.EncodeToString(metadataDigestBytes[:])
	metadataName := localcloud.Scope(scope).ObjectPrefix() + "metadata-probe"
	metadataObject := bucketHandle.Object(metadataName)
	metadataWriter := metadataObject.NewWriter(ctx)
	metadataWriter.Metadata = map[string]string{"sha256": metadataDigest}
	if _, err := metadataWriter.Write(metadataProbe); err != nil {
		_ = metadataWriter.Close()
		t.Fatalf("write loopback metadata probe: %v", err)
	}
	if err := metadataWriter.Close(); err != nil {
		t.Fatalf("commit loopback metadata probe: %v", err)
	}
	writerAttrs := metadataWriter.Attrs()
	emulatorAttrs, err := metadataObject.Attrs(ctx)
	if err != nil {
		t.Fatalf("read loopback metadata probe attributes: %v", err)
	}
	t.Logf("loopback object metadata: writer=%q emulator Attrs=%q", writerAttrs.Metadata["sha256"], emulatorAttrs.Metadata["sha256"])
	if writerAttrs.Metadata["sha256"] != metadataDigest || emulatorAttrs.Metadata["sha256"] != writerAttrs.Metadata["sha256"] {
		t.Fatalf("loopback object metadata mismatch: writer=%q emulator Attrs=%q want=%q", writerAttrs.Metadata["sha256"], emulatorAttrs.Metadata["sha256"], metadataDigest)
	}

	worker := filepath.Join(t.TempDir(), "olw_worker")
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve BFF module path")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../.."))
	build := exec.CommandContext(ctx, "go", "build", "-tags", "lwc_local_pipeline_fixture", "-o", worker, "./cmd/olw_worker")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tagged local pipeline worker: %v\n%s", err, output)
	}
	workerStderrPath := filepath.Join(t.TempDir(), "worker.stderr")
	generatedConfigDir := filepath.Join(t.TempDir(), "rendered Pipeline config")
	if err := os.Mkdir(generatedConfigDir, 0o700); err != nil {
		t.Fatalf("create rendered Pipeline config fixture directory: %v", err)
	}
	generatedConfigPath := filepath.Join(generatedConfigDir, "synto.toml")
	generatedConfig := []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\nrun_timeout_seconds = 30\n")
	if err := os.WriteFile(generatedConfigPath, generatedConfig, 0o600); err != nil {
		t.Fatalf("write rendered Pipeline TOML fixture: %v", err)
	}
	privateBindingsPath := filepath.Join(generatedConfigDir, "private-bindings.json")
	if err := os.WriteFile(privateBindingsPath, []byte(`{"environment":"local"}`), 0o600); err != nil {
		t.Fatalf("write private Pipeline binding fixture: %v", err)
	}
	workerStderr, err := os.Create(workerStderrPath)
	if err != nil {
		t.Fatalf("create native worker stderr capture: %v", err)
	}
	defer workerStderr.Close()
	manager, err = localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: worker, Stderr: workerStderr,
		Project: project, Bucket: bucket, Database: database,
		Scope: scope, WorkDir: t.TempDir(),
		PipelineConfigPath: generatedConfigPath, PipelineBindingsPath: privateBindingsPath,
	})
	if err != nil {
		t.Fatalf("configure native worker manager: %v", err)
	}
	if err := manager.ReconcileInterrupted(ctx); err != nil {
		t.Fatalf("reconcile test scope: %v", err)
	}

	h := handlerv1.New(storageClient, fsClient, nil, cache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, 1, 1, nil)
	h.SetLocalPipelineExecutor(manager)
	var cloudRunCalls atomic.Int32
	cloudRunSpy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudRunCalls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(cloudRunSpy.Close)
	h.SetPipelineJobURL(cloudRunSpy.URL)
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: secret, AllowedOrigins: []string{"http://localhost:3000"},
		FirestoreDatabaseID: database, GCPProject: project, Bucket: bucket,
	}, true, storageClient, fsClient, h, &syssettings.FakeStore{Enabled: true}, nil)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	token, err := auth.GenerateAccessToken(userID, "member", secret)
	if err != nil {
		t.Fatalf("create local bearer token: %v", err)
	}

	const projectID = "success-project"
	projectStorage := storageClient.WithScope(userID, projectID)
	historicalRaw := []byte("Previously ingested local source.")
	if _, err := projectStorage.WriteBytes(ctx, historicalRaw, "raw/historical.md"); err != nil {
		t.Fatalf("seed historical raw source: %v", err)
	}
	if _, err := projectStorage.WriteBytes(ctx, []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\n"), "synto.toml"); err != nil {
		t.Fatalf("seed keyless fixture Synto config: %v", err)
	}
	rawDigestBytes := sha256.Sum256(historicalRaw)
	rawDigest := hex.EncodeToString(rawDigestBytes[:])
	annotationDigest := annotation.Digest("")
	historicalReceipt := sourcestatus.Receipt{
		RawPath: "raw/historical.md", LastIngestedRawSHA256: rawDigest,
		LastIngestedAnnSHA256: annotationDigest,
		LastIngestFingerprint: sourcestatus.Fingerprint(rawDigest, annotationDigest),
		LastSuccessAt:         "2025-01-02T03:04:05Z",
	}
	statusBytes, err := json.Marshal(sourcestatus.Artifact{Version: 1, Sources: map[string]sourcestatus.Receipt{"historical-source": historicalReceipt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectStorage.WriteBytes(ctx, statusBytes, sourcestatus.Path); err != nil {
		t.Fatalf("seed historical source receipt: %v", err)
	}
	if _, err := projectStorage.WriteBytes(ctx, []byte(`{"source":{"historical-source":"historical"},"source_meta":{"historical-source":{"slug":"historical","title":"Historical","source_file":"raw/historical.md"}}}`), "cache/id_map.json"); err != nil {
		t.Fatalf("seed historical source map: %v", err)
	}
	if _, err := projectStorage.WriteBytes(ctx, []byte("---\nid: historical-source\ntitle: Historical\nsource_file: raw/historical.md\n---\nHistorical source page.\n"), "wiki/sources/historical.md"); err != nil {
		t.Fatalf("seed historical source page: %v", err)
	}
	if _, err := projectStorage.WriteBytes(ctx, []byte("First local pipeline source."), "raw/source.md"); err != nil {
		t.Fatalf("seed first scoped raw input: %v", err)
	}
	executionID := postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusAccepted)
	postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusConflict)
	state, reason := waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if state != "SUCCEEDED" || reason != "" {
		if err := workerStderr.Sync(); err != nil {
			t.Errorf("sync native worker stderr capture: %v", err)
		}
		stderr, readErr := os.ReadFile(workerStderrPath)
		t.Fatalf("successful worker status=%q reason=%q; actual worker stderr=%q (read error: %v)", state, reason, stderr, readErr)
	}
	manifest1, _, exists, err := projectStorage.CurrentManifest(ctx)
	if err != nil || !exists || manifest1.GenerationID == "" {
		t.Fatalf("first GCS manifest readback exists=%v generation=%q err=%v", exists, manifest1.GenerationID, err)
	}
	if _, ok := manifest1.File("wiki/alpha.md"); !ok {
		t.Fatalf("committed manifest %q does not own fixture wiki page", manifest1.GenerationID)
	}
	if _, ok := manifest1.File("wiki/sources/historical.md"); !ok {
		t.Fatalf("first generation %q did not preserve the seeded historical source page", manifest1.GenerationID)
	}
	historicalRow1 := assertLocalPipelineHistoricalSource(t, ctx, projectStorage, manifest1.SourceSnapshotDigest, historicalRaw, rawDigest)
	page, err := projectStorage.ReadFile(ctx, "wiki/alpha.md")
	if err != nil || !bytes.Contains(page, []byte("Alpha fixture output")) {
		t.Fatalf("published page readback=%q err=%v", page, err)
	}

	time.Sleep(1100 * time.Millisecond) // satisfy the configured test cooldown
	if _, err := projectStorage.WriteBytes(ctx, []byte("Incremental local pipeline source."), "raw/incremental.md"); err != nil {
		t.Fatalf("seed incremental raw input: %v", err)
	}
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if state != "SUCCEEDED" || reason != "" {
		t.Fatalf("incremental worker status=%q reason=%q; actual worker stderr=%q", state, reason, readLocalPipelineWorkerStderr(t, workerStderr, workerStderrPath))
	}
	manifest2, _, exists, err := projectStorage.CurrentManifest(ctx)
	if err != nil || !exists || manifest2.GenerationID == "" || manifest2.GenerationID == manifest1.GenerationID {
		t.Fatalf("incremental manifest readback=%q exists=%v err=%v, before=%q", manifest2.GenerationID, exists, err, manifest1.GenerationID)
	}
	if _, ok := manifest2.File("wiki/sources/historical.md"); !ok {
		t.Fatalf("incremental generation %q did not carry the historical source page", manifest2.GenerationID)
	}
	historicalRow2 := assertLocalPipelineHistoricalSource(t, ctx, projectStorage, manifest2.SourceSnapshotDigest, historicalRaw, rawDigest)
	if historicalRow2.StableID != historicalRow1.StableID || historicalRow2.RawPath != historicalRow1.RawPath || historicalRow2.ContentDigest != historicalRow1.ContentDigest || historicalRow2.ObjectGeneration != historicalRow1.ObjectGeneration {
		t.Fatalf("historical source changed across generations: first=%+v second=%+v", historicalRow1, historicalRow2)
	}
	historicalPage, err := projectStorage.ReadFile(ctx, "wiki/sources/historical.md")
	if err != nil || !bytes.Contains(historicalPage, []byte("Historical source page.")) {
		t.Fatalf("historical source page carry-forward=%q err=%v", historicalPage, err)
	}

	time.Sleep(1100 * time.Millisecond) // the failed worker must preserve this current generation
	if _, err := projectStorage.WriteBytes(ctx, []byte("failure stimulus"), "raw/fixture-fail.md"); err != nil {
		t.Fatalf("seed worker failure input: %v", err)
	}
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if state != "FAILED" || reason == "" {
		t.Fatalf("child-failure status=%q reason=%q, want a visible failure; actual worker stderr=%q", state, reason, readLocalPipelineWorkerStderr(t, workerStderr, workerStderrPath))
	}
	if stderr := readLocalPipelineWorkerStderr(t, workerStderr, workerStderrPath); !strings.Contains(stderr, "fixture compile failure") {
		t.Fatalf("expected fixture failure was not preserved in native worker stderr: %q", stderr)
	}
	manifestAfterFailure, _, exists, err := projectStorage.CurrentManifest(ctx)
	if err != nil || !exists || manifestAfterFailure.GenerationID != manifest2.GenerationID {
		t.Fatalf("child failure changed current manifest: generation=%q exists=%v err=%v, want %q", manifestAfterFailure.GenerationID, exists, err, manifest2.GenerationID)
	}
	page, err = projectStorage.ReadFile(ctx, "wiki/alpha.md")
	if err != nil || !bytes.Contains(page, []byte("Alpha fixture output")) {
		t.Fatalf("prior published page unavailable after child failure: %q err=%v", page, err)
	}

	const spawnFailureProject = "spawn-failure-project"
	if _, err := storageClient.WithScope(userID, spawnFailureProject).WriteBytes(ctx, []byte("A local pipeline source."), "raw/source.md"); err != nil {
		t.Fatalf("seed scoped raw input for spawn failure: %v", err)
	}
	brokenManager, err := localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: filepath.Join(t.TempDir(), "missing-worker"),
		Project: project, Bucket: bucket, Database: database,
		Scope: scope, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("configure spawn-failure worker manager: %v", err)
	}
	defer func() {
		if err := brokenManager.Close(ctx); err != nil {
			t.Errorf("close spawn-failure manager: %v", err)
		}
	}()
	brokenHandler := handlerv1.New(storageClient, fsClient, nil, cache.New(), nil, nil)
	brokenHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	brokenHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	brokenHandler.SetLocalPipelineExecutor(brokenManager)
	brokenHandler.SetPipelineJobURL(cloudRunSpy.URL)
	brokenServer := httptest.NewServer(newProductionRouter(config.Config{
		JWTSecret: secret, AllowedOrigins: []string{"http://localhost:3000"},
		FirestoreDatabaseID: database, GCPProject: project, Bucket: bucket,
	}, true, storageClient, fsClient, brokenHandler, &syssettings.FakeStore{Enabled: true}, nil))
	t.Cleanup(brokenServer.Close)
	postLocalPipelineRun(t, ctx, brokenServer.Client(), brokenServer.URL, token, spawnFailureProject, http.StatusInternalServerError)
	spawnState, spawnReason := waitLocalPipelineStatus(t, ctx, brokenServer.Client(), brokenServer.URL, token, spawnFailureProject, "")
	if spawnState != "FAILED" || spawnReason == "" {
		t.Fatalf("spawn-failure status=%q reason=%q, want a visible failure", spawnState, spawnReason)
	}

	if err := manager.Close(ctx); err != nil {
		t.Fatalf("close native worker manager: %v", err)
	}
	managerClosed = true
	if calls := cloudRunCalls.Load(); calls != 0 {
		t.Fatalf("native local pipeline made %d intercepted Cloud Run calls", calls)
	}
}

func readLocalPipelineWorkerStderr(t *testing.T, file *os.File, path string) string {
	t.Helper()
	if err := file.Sync(); err != nil {
		t.Errorf("sync native worker stderr capture: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read native worker stderr capture: %v", err)
	}
	return string(data)
}

func postLocalPipelineRun(t *testing.T, ctx context.Context, client *http.Client, baseURL, token, projectID string, wantStatus int) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/pipeline/run", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", projectID)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("trigger local pipeline for %s: %v", projectID, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != wantStatus {
		t.Fatalf("pipeline trigger status=%d body=%s, want %d", response.StatusCode, data, wantStatus)
	}
	if wantStatus != http.StatusAccepted {
		return ""
	}
	var payload struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.ExecutionID == "" {
		t.Fatalf("pipeline trigger response has no execution ID: %s (%v)", data, err)
	}
	return payload.ExecutionID
}

func assertLocalPipelineHistoricalSource(t *testing.T, ctx context.Context, storage *gcs.Client, snapshotDigest string, wantRaw []byte, wantDigest string) generation.SourceSnapshotRow {
	t.Helper()
	snapshotPath, err := generation.SourceSnapshotPath(snapshotDigest)
	if err != nil {
		t.Fatalf("resolve historical source snapshot path: %v", err)
	}
	snapshotData, err := storage.ReadFile(ctx, snapshotPath)
	if err != nil {
		t.Fatalf("read historical source snapshot: %v", err)
	}
	snapshot, err := generation.DecodeSourceSnapshot(snapshotData)
	if err != nil || len(snapshot.Rows) != 1 {
		t.Fatalf("historical source inventory=%+v err=%v", snapshot, err)
	}
	row := snapshot.Rows[0]
	if row.StableID != "historical-source" || row.RawPath != "raw/historical.md" || row.ContentDigest != wantDigest {
		t.Fatalf("historical source row=%+v", row)
	}
	bytesPath, err := generation.SourceBytesPath(row.ContentDigest)
	if err != nil {
		t.Fatalf("resolve historical source bytes path: %v", err)
	}
	data, objectGeneration, err := storage.ReadFileWithGeneration(ctx, bytesPath)
	if err != nil || objectGeneration != row.ObjectGeneration || !bytes.Equal(data, wantRaw) {
		t.Fatalf("historical source bytes=%q generation=%d want_generation=%d err=%v", data, objectGeneration, row.ObjectGeneration, err)
	}
	return row
}

func waitLocalPipelineStatus(t *testing.T, ctx context.Context, client *http.Client, baseURL, token, projectID, executionID string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		statusURL := baseURL + "/api/v1/pipeline/status"
		if executionID != "" {
			statusURL += "?execution_id=" + url.QueryEscape(executionID)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Project-ID", projectID)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("poll local pipeline status: %v", err)
		}
		var payload struct {
			LastExecution *struct {
				Status         string `json:"status"`
				LogStateReason string `json:"log_state_reason"`
				Diagnostic     *struct {
					DetailCode string `json:"detail_code"`
				} `json:"diagnostic"`
			} `json:"last_execution"`
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("pipeline status response=%d body=%s", response.StatusCode, data)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("decode pipeline status: %v; body=%s", err, data)
		}
		if payload.LastExecution != nil {
			state := strings.ToUpper(payload.LastExecution.Status)
			if state == "SUCCEEDED" || state == "FAILED" || state == "UNKNOWN" {
				reason := payload.LastExecution.LogStateReason
				if reason == "" && payload.LastExecution.Diagnostic != nil {
					reason = payload.LastExecution.Diagnostic.DetailCode
				}
				return state, reason
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pipeline did not finish before context expiry")
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("pipeline did not reach a terminal state within 90 seconds")
	return "", ""
}

func loopbackEmulatorEndpoint(t *testing.T, envName string) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return ""
	}
	host := raw
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			t.Fatalf("%s is not a valid emulator endpoint", envName)
		}
		host = parsed.Host
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	if strings.EqualFold(host, "localhost") {
		return raw
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip == nil || !ip.IsLoopback() {
		t.Fatalf("%s must point to a loopback emulator for this test", envName)
	}
	return raw
}

func localPipelineTestNonce(t *testing.T) string {
	t.Helper()
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value[:])
}

func deleteLocalPipelineFirestoreScope(ctx context.Context, client *cloudfirestore.Client, scope string) error {
	var failures []error
	root := client.Collection("local_scopes").Doc(scope)
	for _, collection := range []string{"pipeline_quota", "local_pipeline_locks", "executions"} {
		docs, err := root.Collection(collection).Documents(ctx).GetAll()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, doc := range docs {
			if _, err := doc.Ref.Delete(ctx); err != nil && status.Code(err) != codes.NotFound {
				failures = append(failures, err)
			}
		}
	}
	if _, err := root.Delete(ctx); err != nil && status.Code(err) != codes.NotFound {
		failures = append(failures, err)
	}
	for _, collection := range []string{"pipeline_quota", "local_pipeline_locks", "executions"} {
		docs, err := root.Collection(collection).Documents(ctx).GetAll()
		if err != nil {
			failures = append(failures, err)
		} else if len(docs) != 0 {
			failures = append(failures, errors.New("Firestore scope retained documents in "+collection))
		}
	}
	return errors.Join(failures...)
}

func deleteLocalPipelineObjectScope(ctx context.Context, client *cloudstorage.Client, bucket, scope string) error {
	prefix := "local_scopes/" + scope + "/"
	objectIterator := client.Bucket(bucket).Objects(ctx, &cloudstorage.Query{Prefix: prefix})
	var failures []error
	for {
		attrs, err := objectIterator.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := client.Bucket(bucket).Object(attrs.Name).Generation(attrs.Generation).Delete(ctx); err != nil && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
			failures = append(failures, err)
		}
	}
	readback := client.Bucket(bucket).Objects(ctx, &cloudstorage.Query{Prefix: prefix})
	if attrs, err := readback.Next(); err == nil {
		failures = append(failures, errors.New("Storage scope retained object "+attrs.Name))
	} else if !errors.Is(err, iterator.Done) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func verifyLocalPipelineRetainedSentinels(ctx context.Context, fs *cloudfirestore.Client, storage *cloudstorage.Client, bucket, scope string) error {
	doc, err := fs.Collection("local_scopes").Doc(scope).Collection("executions").Doc("sentinel").Get(ctx)
	if err != nil {
		return err
	}
	if doc.Data()["sentinel"] != "keep" {
		return errors.New("other-scope Firestore sentinel changed")
	}
	reader, err := storage.Bucket(bucket).Object("local_scopes/" + scope + "/sentinel.txt").NewReader(ctx)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if string(data) != "keep" {
		return errors.New("other-scope Storage sentinel changed")
	}
	return nil
}
