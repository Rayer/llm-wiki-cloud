package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/annotation"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localpipeline"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	storeapi "github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
	"google.golang.org/api/idtoken"
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
	firestoreProxy := newGatedTCPProxy(t, emulatorHostPort(t, firestoreEndpoint))
	t.Setenv("FIRESTORE_EMULATOR_HOST", firestoreProxy.Address())

	const (
		project  = "llm-wiki-cloud"
		bucket   = "llm-wiki-cloud-local"
		database = "llm-wiki-cloud-local"
		secret   = "test-only-local-pipeline-signing-secret"
	)
	userID := "lwc398-test-user-" + localPipelineTestNonce(t)
	scope := "lwc361-test-" + localPipelineTestNonce(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	capture := otlpfixture.New(t)
	qaProject := "synthetic-project"
	if otlpfixture.CloudMode() {
		qaProject = os.Getenv("LWC_OTLP_QA_PROJECT")
		t.Setenv("LWC_OTLP_TEST_ENDPOINT", "")
	} else {
		t.Setenv("LWC_OTLP_TEST_ENDPOINT", capture.Endpoint())
	}
	provider := capture.Provider(t, "lwc-qa-pipeline-fixture", "local-fixture", qaProject)
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
	var reconciliationFS *scopedfirestore.Client
	var raceStorageClient *gcs.Client
	var storageProxy *gatedStorageProxy
	sentinelSeeded := false
	t.Cleanup(func() {
		firestoreProxy.SetPaused(false)
		firestoreProxy.SetBlocked(false)
		if storageProxy != nil {
			storageProxy.SetBlocked(false)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if !managerClosed && manager != nil {
			if err := manager.Close(cleanupCtx); err != nil {
				t.Errorf("close native worker manager during cleanup: %v", err)
			}
		}
		if raceStorageClient != nil {
			if err := raceStorageClient.Close(); err != nil {
				t.Errorf("close proxy-backed native finish Storage client: %v", err)
			}
		}
		if storageProxy != nil {
			storageProxy.Close()
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
		if reconciliationFS != nil {
			if err := reconciliationFS.Close(); err != nil {
				t.Errorf("close direct reconciliation Firestore client: %v", err)
			}
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
	workerRevision := strings.TrimSpace(os.Getenv("LWC_OTLP_QA_REVISION"))
	if workerRevision == "" {
		workerRevision = "local-fixture"
	}
	build := exec.CommandContext(ctx, "go", "build", "-tags", "lwc_local_pipeline_fixture", "-ldflags", "-X main.buildNonce="+workerRevision, "-o", worker, "./cmd/olw_worker")
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
	traceID, parentID, traceParent := otlpfixture.NewParent(t)
	executionID, requestID := postLocalPipelineRunWithRequestID(t, ctx, server.Client(), server.URL, token, projectID, http.StatusAccepted, traceParent)
	if uuid.Validate(requestID) != nil {
		t.Fatalf("actual pipeline trigger returned invalid server request ID %q", requestID)
	}
	postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusConflict)
	state, reason := waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if state != "SUCCEEDED" || reason != "" {
		if err := workerStderr.Sync(); err != nil {
			t.Errorf("sync native worker stderr capture: %v", err)
		}
		stderr, readErr := os.ReadFile(workerStderrPath)
		t.Fatalf("successful worker status=%q reason=%q; actual worker stderr=%q (read error: %v)", state, reason, stderr, readErr)
	}
	capture.Flush(t, provider)
	var httpSpan, submitSpan otlpfixture.SpanIdentity
	for _, item := range capture.Identities() {
		if item.Name == "http.request" && item.TraceID == traceID && item.Attributes["request.id"] == requestID {
			httpSpan = item
		}
		if item.Name == "pipeline.submit" && item.TraceID == traceID {
			submitSpan = item
		}
	}
	if httpSpan.Name == "" || httpSpan.ParentSpanID != parentID {
		t.Fatal("actual BFF request span did not preserve the synthetic HTTP parent")
	}
	if submitSpan.Name == "" || submitSpan.ParentSpanID != httpSpan.SpanID || submitSpan.Attributes["request.id"] != requestID {
		t.Fatal("actual pipeline submission span did not inherit the request lifecycle span and ID")
	}
	var invokeSpan otlpfixture.SpanIdentity
	for _, item := range capture.Identities() {
		if item.Name == "pipeline.job.invoke" && item.TraceID == traceID && item.ParentSpanID == submitSpan.SpanID {
			invokeSpan = item
			break
		}
	}
	if invokeSpan.Name == "" || invokeSpan.Attributes["request.id"] != requestID {
		t.Fatal("actual Manager invocation span was not a child of the pipeline submission")
	}
	execution, err := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(executionID).Get(ctx)
	if err != nil || execution.Data()["request_id"] != requestID {
		t.Fatalf("actual execution did not persist its originating request ID: err=%v", err)
	}
	var workerSpan otlpfixture.SpanIdentity
	for _, item := range localPipelineWorkerTraceIdentities(t, workerStderrPath) {
		if item.Name == "pipeline.worker" {
			workerSpan = item
			break
		}
	}
	if workerSpan.Name == "" || workerSpan.ParentSpanID != "" || workerSpan.TraceID == traceID || workerSpan.Attributes["request.id"] != requestID || len(workerSpan.Links) != 1 ||
		workerSpan.Links[0].TraceID != traceID || workerSpan.Links[0].SpanID != invokeSpan.SpanID {
		t.Fatalf("actual native worker attempt did not form an independent linked root from the real Manager invocation: invocation=%+v worker=%+v", invokeSpan, workerSpan)
	}
	t.Logf("OTLP_QA_SPAN case=UC5 trace_id=%s span_id=%s parent_trace_id=%s parent_span_id=%s service_name=%s scope=%s name=%s links=",
		submitSpan.TraceID, submitSpan.SpanID, submitSpan.ParentTraceID, submitSpan.ParentSpanID, submitSpan.ServiceName, submitSpan.Scope, submitSpan.Name)
	t.Logf("OTLP_QA_SPAN case=UC5 trace_id=%s span_id=%s parent_trace_id=%s parent_span_id=%s service_name=%s scope=%s name=%s links=",
		invokeSpan.TraceID, invokeSpan.SpanID, invokeSpan.ParentTraceID, invokeSpan.ParentSpanID, invokeSpan.ServiceName, invokeSpan.Scope, invokeSpan.Name)
	links := make([]string, 0, len(workerSpan.Links))
	for _, link := range workerSpan.Links {
		links = append(links, link.TraceID+"/"+link.SpanID)
	}
	t.Logf("OTLP_QA_SPAN case=UC5 trace_id=%s span_id=%s parent_trace_id=%s parent_span_id=%s service_name=%s scope=%s name=%s links=%s",
		workerSpan.TraceID, workerSpan.SpanID, "", workerSpan.ParentSpanID, workerSpan.ServiceName, workerSpan.Scope, workerSpan.Name, strings.Join(links, ","))
	t.Logf("OTLP_QA_CASE=UC5 worker_attempt_trace_id=%s worker_link_trace_id=%s worker_link_span_id=%s submit_span_id=%s http_parent_span_id=%s expected_span=pipeline.worker outcome=success http_status=%d",
		workerSpan.TraceID, traceID, invokeSpan.SpanID, submitSpan.SpanID, parentID, http.StatusAccepted)
	if !otlpfixture.CloudMode() {
		foundWorkerExport := false
		for _, span := range capture.Spans() {
			if span.Name == "pipeline.worker" && hex.EncodeToString(span.TraceId) == workerSpan.TraceID {
				foundWorkerExport = len(span.ParentSpanId) == 0 && len(span.Links) == 1 &&
					hex.EncodeToString(span.Links[0].TraceId) == traceID && hex.EncodeToString(span.Links[0].SpanId) == invokeSpan.SpanID
			}
		}
		if !foundWorkerExport {
			t.Fatal("actual worker span/link did not cross the filtered OTLP protobuf exporter")
		}
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, projectID, executionID, 1, "charged")
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

	// Exercise the protected scheduled entrypoint from supported raw intake
	// through the actual worker subprocess, generation/receipt readback, status
	// API, and production Query executor with an intercepted model transport.
	const scheduledProject = "scheduled-causal-e2e"
	const scheduledAudience = "http://scheduler.fixture.local/internal/pipeline/schedule"
	const scheduledServiceAccount = "scheduler-fixture@llm-wiki-cloud.invalid"
	scheduledProjectDoc := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID + "_" + scheduledProject)
	if _, err := scheduledProjectDoc.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": scheduledProject, "status": "ready"}); err != nil {
		t.Fatalf("seed one ready scheduled Project: %v", err)
	}
	rawBody := []byte("LWC413_CAUSAL_E2E: rare blue lantern proves this raw article reached publication.")
	var uploadBody bytes.Buffer
	uploadWriter := multipart.NewWriter(&uploadBody)
	filePart, err := uploadWriter.CreateFormFile("file", "lwc413-e2e-source.md")
	if err != nil {
		t.Fatalf("create scheduled source intake part: %v", err)
	}
	if _, err := filePart.Write(rawBody); err != nil {
		t.Fatalf("write scheduled source intake body: %v", err)
	}
	if err := uploadWriter.Close(); err != nil {
		t.Fatalf("close scheduled source intake form: %v", err)
	}
	intakeRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/raw/upload", &uploadBody)
	if err != nil {
		t.Fatalf("create scheduled source intake request: %v", err)
	}
	intakeRequest.Header.Set("Authorization", "Bearer "+token)
	intakeRequest.Header.Set("X-Project-ID", scheduledProject)
	intakeRequest.Header.Set("Content-Type", uploadWriter.FormDataContentType())
	intakeResponse, err := server.Client().Do(intakeRequest)
	if err != nil {
		t.Fatalf("submit scheduled source through raw intake: %v", err)
	}
	intakeResult, _ := io.ReadAll(io.LimitReader(intakeResponse.Body, 1<<20))
	_ = intakeResponse.Body.Close()
	if intakeResponse.StatusCode != http.StatusCreated || !bytes.Contains(intakeResult, []byte(`"status":"created"`)) {
		t.Fatalf("scheduled raw intake status=%d body=%s, want created", intakeResponse.StatusCode, intakeResult)
	}
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", scheduledAudience, scheduledServiceAccount)
	h.SetScheduledPipelineIdentityValidator(func(_ context.Context, tokenValue, gotAudience string) (*idtoken.Payload, error) {
		if tokenValue != "fixture-scheduler-token" || gotAudience != scheduledAudience {
			return nil, errors.New("scheduled identity fixture mismatch")
		}
		return &idtoken.Payload{Audience: scheduledAudience, Claims: map[string]interface{}{
			"email": scheduledServiceAccount, "email_verified": true,
		}}, nil
	})
	scheduleRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/internal/pipeline/schedule", nil)
	if err != nil {
		t.Fatalf("create scheduled entrypoint request: %v", err)
	}
	scheduleRequest.Header.Set("Authorization", "Bearer fixture-scheduler-token")
	scheduleResponse, err := server.Client().Do(scheduleRequest)
	if err != nil {
		t.Fatalf("call actual scheduled entrypoint: %v", err)
	}
	var sweep struct {
		Scanned     int `json:"scanned"`
		Accepted    int `json:"accepted"`
		Skipped     int `json:"skipped"`
		Unavailable int `json:"unavailable"`
	}
	scheduleDecodeErr := json.NewDecoder(io.LimitReader(scheduleResponse.Body, 1<<20)).Decode(&sweep)
	_ = scheduleResponse.Body.Close()
	if scheduleResponse.StatusCode != http.StatusOK || scheduleDecodeErr != nil || sweep.Scanned != 1 || sweep.Accepted != 1 || sweep.Skipped != 0 || sweep.Unavailable != 0 {
		t.Fatalf("actual scheduled sweep status=%d result=%+v decodeErr=%v; want one accepted Project", scheduleResponse.StatusCode, sweep, scheduleDecodeErr)
	}
	scheduledStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, scheduledProject, time.Now().UTC())
	if err != nil || scheduledStatus.LastExecutionID == "" {
		t.Fatalf("scheduled execution link=%q err=%v", scheduledStatus.LastExecutionID, err)
	}
	scheduledExecutionID := scheduledStatus.LastExecutionID
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, scheduledProject, scheduledExecutionID)
	if state != "SUCCEEDED" || reason != "" {
		t.Fatalf("scheduled native worker status=%q reason=%q; worker stderr=%q", state, reason, readLocalPipelineWorkerStderr(t, workerStderr, workerStderrPath))
	}
	scheduledStorage := storageClient.WithScope(userID, scheduledProject)
	scheduledManifest, _, scheduledManifestExists, err := scheduledStorage.CurrentManifest(ctx)
	if err != nil || !scheduledManifestExists || scheduledManifest.LocalExecutionID != scheduledExecutionID {
		t.Fatalf("scheduled manifest exists=%v execution=%q err=%v; want exact worker publication", scheduledManifestExists, scheduledManifest.LocalExecutionID, err)
	}
	if _, ok := scheduledManifest.File("wiki/sources/lwc413-e2e-source.md"); !ok {
		t.Fatalf("scheduled manifest %q omitted the newly consumed source page", scheduledManifest.GenerationID)
	}
	if _, err := scheduledStorage.ReadLocalPublicationReceipt(ctx, scheduledExecutionID); err != nil {
		t.Fatalf("scheduled publication receipt readback: %v", err)
	}
	sourceSnapshot, err := scheduledStorage.ReadSourceSnapshot(ctx)
	if err != nil {
		t.Fatalf("scheduled source snapshot readback: %v", err)
	}
	wantRawDigest := fmt.Sprintf("%x", sha256.Sum256(rawBody))
	var sourceRow *generation.SourceSnapshotRow
	for i := range sourceSnapshot.Rows {
		if sourceSnapshot.Rows[i].RawPath == "raw/lwc413-e2e-source.md" {
			sourceRow = &sourceSnapshot.Rows[i]
			break
		}
	}
	if sourceRow == nil || sourceRow.ContentDigest != wantRawDigest {
		t.Fatalf("scheduled consumed-source snapshot row=%+v; want start-time raw digest %s", sourceRow, wantRawDigest)
	}
	sourceReceiptBytes, err := scheduledStorage.ReadFile(ctx, sourcestatus.Path)
	if err != nil {
		t.Fatalf("scheduled source receipt readback: %v", err)
	}
	sourceReceiptArtifact, err := sourcestatus.Decode(sourceReceiptBytes)
	if err != nil {
		t.Fatalf("decode scheduled source receipt: %v", err)
	}
	var receiptFound bool
	for _, receipt := range sourceReceiptArtifact.Sources {
		if receipt.RawPath == "raw/lwc413-e2e-source.md" && receipt.LastIngestedRawSHA256 == wantRawDigest && receipt.LastIngestFingerprint != "" {
			receiptFound = true
		}
	}
	if !receiptFound {
		t.Fatalf("scheduled source receipts did not mark only the consumed input: %+v", sourceReceiptArtifact.Sources)
	}
	manualRuns, _, _, err := fsClient.LoadQuotaState(ctx, userID, scheduledProject)
	if err != nil || manualRuns != 0 {
		t.Fatalf("scheduled worker changed manual daily count to %d err=%v", manualRuns, err)
	}
	scheduledReservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, scheduledProject, pipelinequota.DayKeyUTC(time.Now().UTC()))
	scheduledReservation, exists, err := fsClient.GetQuotaReservation(ctx, scheduledReservationID)
	if err != nil || !exists || scheduledReservation.ExecutionID != scheduledExecutionID || scheduledReservation.SettlementStatus != "charged" || scheduledReservation.BudgetClass != "scheduled_daily" {
		t.Fatalf("scheduled reservation=%+v exists=%t err=%v; want charged, linked daily entitlement", scheduledReservation, exists, err)
	}
	statusRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/pipeline/status", nil)
	if err != nil {
		t.Fatalf("create scheduled pipeline status request: %v", err)
	}
	statusRequest.Header.Set("Authorization", "Bearer "+token)
	statusRequest.Header.Set("X-Project-ID", scheduledProject)
	statusResponse, err := server.Client().Do(statusRequest)
	if err != nil {
		t.Fatalf("read scheduled pipeline status surface: %v", err)
	}
	var statusPayload struct {
		LastExecution     *handlerapi.PipelineExecutionResponse    `json:"last_execution"`
		ScheduledPipeline *scopedfirestore.ScheduledPipelineStatus `json:"scheduled_pipeline"`
	}
	statusDecodeErr := json.NewDecoder(io.LimitReader(statusResponse.Body, 1<<20)).Decode(&statusPayload)
	_ = statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK || statusDecodeErr != nil || statusPayload.LastExecution == nil || statusPayload.LastExecution.Status != "SUCCEEDED" || statusPayload.ScheduledPipeline == nil || !statusPayload.ScheduledPipeline.BudgetUsed {
		t.Fatalf("scheduled status API status=%d execution=%+v scheduled=%+v decodeErr=%v", statusResponse.StatusCode, statusPayload.LastExecution, statusPayload.ScheduledPipeline, statusDecodeErr)
	}

	queryCache := cache.New()
	queryExecutor, err := queryquality.NewStrictProductionExecutorWithQueryServiceConfig(queryCache, nil, nil, query.NewService(queryCache, nil, llm.NewClient("local-fixture-no-network")), queryquality.DefaultRetrievalProfile(), queryquality.StructuredPlanPromptID, queryquality.DefaultOptions(), query.RuntimeConfigIdentity{})
	if err != nil {
		t.Fatalf("configure production Query executor fixture: %v", err)
	}
	h.SetQueryExecutor(queryExecutor)
	priorDefaultTransport := http.DefaultTransport
	http.DefaultTransport = localPipelineCitationTransport{}
	queryRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/query", strings.NewReader(`{"q":"rare blue lantern","mode":"wiki"}`))
	if err != nil {
		http.DefaultTransport = priorDefaultTransport
		t.Fatalf("create scheduled source Query request: %v", err)
	}
	queryRequest.Header.Set("Authorization", "Bearer "+token)
	queryRequest.Header.Set("X-Project-ID", scheduledProject)
	queryRequest.Header.Set("Content-Type", "application/json")
	queryResponse, err := server.Client().Do(queryRequest)
	http.DefaultTransport = priorDefaultTransport
	if err != nil {
		t.Fatalf("query newly published scheduled source: %v", err)
	}
	var queryResult handlerapi.QueryResponse
	queryDecodeErr := json.NewDecoder(io.LimitReader(queryResponse.Body, 1<<20)).Decode(&queryResult)
	_ = queryResponse.Body.Close()
	if queryResponse.StatusCode != http.StatusOK || queryDecodeErr != nil {
		t.Fatalf("scheduled source Query status=%d decodeErr=%v body=%+v", queryResponse.StatusCode, queryDecodeErr, queryResult)
	}
	var citedScheduledContent bool
	for _, citation := range queryResult.Citations {
		if citation.Slug == "alpha" || citation.Slug == "lwc413-e2e-source" {
			citedScheduledContent = true
		}
	}
	if !citedScheduledContent {
		t.Fatalf("scheduled source Query did not cite the newly published generation: results=%+v citations=%+v answer=%q", queryResult.Results, queryResult.Citations, queryResult.AISynth)
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
	lastRunBeforeFailure := assertLocalPipelineQuota(t, ctx, fsClient, userID, projectID, executionID, 2, "charged")
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
	failedExecution := readLocalPipelineExecution(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if failedExecution.DiagnosticState != "available" || failedExecution.Diagnostic == nil ||
		failedExecution.Diagnostic.Stage != "synto_run" || failedExecution.Diagnostic.ErrorClass == "" ||
		!strings.Contains(failedExecution.Diagnostic.Message, "fixture compile failure") {
		t.Fatalf("native API diagnostic=%+v state=%q, want worker stage/class/message", failedExecution.Diagnostic, failedExecution.DiagnosticState)
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, projectID, executionID, 2, "refunded", lastRunBeforeFailure)
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

	// The same failed input can be retried after the reservation refund restores
	// its prior cooldown timestamp.
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, projectID, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, projectID, executionID)
	if state != "FAILED" || reason == "" {
		t.Fatalf("same-input retry status=%q reason=%q, want the fixture failure", state, reason)
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, projectID, executionID, 2, "refunded", lastRunBeforeFailure)

	// Force the native finish writer to hold a stale UNKNOWN result while the
	// background reconciler confirms the worker's execution-owned prepublish
	// diagnostic and refunds the reservation.
	const staleFinishProject = "stale-finish-race"
	staleFinishStorage := storageClient.WithScope(userID, staleFinishProject)
	if _, err := staleFinishStorage.WriteBytes(ctx, []byte("initial publication"), "raw/source.md"); err != nil {
		t.Fatalf("seed stale-finish initial source: %v", err)
	}
	initialID := postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, staleFinishProject, http.StatusAccepted)
	waitNativeExecutionState(t, ctx, fsClient, initialID, "SUCCEEDED")
	assertLocalPipelineQuota(t, ctx, fsClient, userID, staleFinishProject, initialID, 1, "charged")
	time.Sleep(1100 * time.Millisecond)
	storageProxy = newGatedStorageProxy(t, storageEndpoint)
	t.Setenv("STORAGE_EMULATOR_HOST", storageProxy.URL())
	raceStorageClient, err = gcs.NewClient(bucket)
	if err != nil {
		t.Fatalf("create proxy-backed native finish Storage client: %v", err)
	}
	t.Setenv("STORAGE_EMULATOR_HOST", storageEndpoint)
	manager, err = localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: raceStorageClient, Worker: worker, Stderr: workerStderr,
		Project: project, Bucket: bucket, Database: database, Scope: scope, WorkDir: t.TempDir(),
		PipelineConfigPath: generatedConfigPath, PipelineBindingsPath: privateBindingsPath,
	})
	if err != nil {
		t.Fatalf("configure proxy-backed native finish manager: %v", err)
	}
	managerClosed = false
	h.SetLocalPipelineExecutor(manager)
	if _, err := staleFinishStorage.WriteBytes(ctx, []byte("controlled failure input"), "raw/fixture-outage.md"); err != nil {
		t.Fatalf("seed stale-finish failure input: %v", err)
	}
	staleWorkspace := t.TempDir()
	t.Setenv("WORKSPACE_DIR", staleWorkspace)
	failedID := postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, staleFinishProject, http.StatusAccepted)
	readyPath := filepath.Join(staleWorkspace, "fixture-outage-ready")
	releasePath := filepath.Join(staleWorkspace, "fixture-outage-release")
	waitForLocalFixtureFile(t, ctx, readyPath)
	staleRawObject := localcloud.Scope(scope).ObjectPrefix() + storeapi.ProjectObjectPath(userID, staleFinishProject, "raw/fixture-outage.md")
	if err := cloudClient.Bucket(bucket).Object(staleRawObject).Delete(ctx); err != nil {
		t.Fatalf("remove failure marker after native materialization: %v", err)
	}
	storageProxy.SetBlocked(true)
	firestoreProxy.SetPaused(true)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release controlled native failure: %v", err)
	}
	waitForLocalPipelineDiagnostic(t, ctx, staleFinishStorage, failedID)
	if err := firestoreProxy.WaitPaused(ctx); err != nil {
		t.Fatalf("wait for actual native finish transaction barrier: %v", err)
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", emulatorHostPort(t, firestoreEndpoint))
	reconciliationFS, err = scopedfirestore.NewClientWithDatabase(project, database, "", "")
	if err != nil {
		t.Fatalf("create direct loopback Firestore reconciler client: %v", err)
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", firestoreProxy.Address())
	reconciler, err := localpipeline.New(localpipeline.Config{
		Firestore: reconciliationFS.Raw(), Storage: storageClient, Worker: worker,
		Project: project, Bucket: bucket, Database: database, Scope: scope, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("configure direct background reconciler: %v", err)
	}
	reconcilerCtx, cancelReconciler := context.WithCancel(ctx)
	reconcilerDone := make(chan struct{})
	go func() {
		defer close(reconcilerDone)
		reconciler.RunQuotaReconciler(reconcilerCtx)
	}()
	waitNativeExecutionState(t, ctx, reconciliationFS, failedID, "FAILED")
	cancelReconciler()
	<-reconcilerDone
	assertLocalPipelineQuota(t, ctx, reconciliationFS, userID, staleFinishProject, failedID, 1, "refunded")
	if running, err := reconciler.Running(ctx, userID, staleFinishProject); err != nil || running {
		t.Fatalf("reconciled native lock running=%v err=%v, want released", running, err)
	}
	failedSnapshot, err := scopedfirestore.Collection(reconciliationFS.Raw(), "executions").Doc(failedID).Get(ctx)
	if err != nil || failedSnapshot.Data()["status"] != "FAILED" {
		t.Fatalf("reconciled native execution status=%v err=%v, want FAILED", failedSnapshot.Data()["status"], err)
	}
	firestoreProxy.SetPaused(false)
	raceCloseCtx, raceCloseCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := manager.Close(raceCloseCtx); err != nil {
		raceCloseCancel()
		t.Fatalf("wait for stale native finish after background reconciliation: %v", err)
	}
	raceCloseCancel()
	managerClosed = true
	storageProxy.SetBlocked(false)
	failedSnapshot, err = scopedfirestore.Collection(reconciliationFS.Raw(), "executions").Doc(failedID).Get(ctx)
	if err != nil || failedSnapshot.Data()["status"] != "FAILED" {
		t.Fatalf("resumed stale finish changed reconciled execution status=%v err=%v, want FAILED", failedSnapshot.Data()["status"], err)
	}
	assertLocalPipelineQuota(t, ctx, reconciliationFS, userID, staleFinishProject, failedID, 1, "refunded")
	if running, err := reconciler.Running(ctx, userID, staleFinishProject); err != nil || running {
		t.Fatalf("resumed stale finish changed reconciled lock running=%v err=%v, want released", running, err)
	}
	manager, err = localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: worker, Stderr: workerStderr,
		Project: project, Bucket: bucket, Database: database, Scope: scope, WorkDir: t.TempDir(),
		PipelineConfigPath: generatedConfigPath, PipelineBindingsPath: privateBindingsPath,
	})
	if err != nil {
		t.Fatalf("restart native worker manager after stale-finish race: %v", err)
	}
	managerClosed = false
	h.SetLocalPipelineExecutor(manager)
	if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset retry readiness signal: %v", err)
	}
	if err := os.Remove(releasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset retry release signal: %v", err)
	}
	if _, err := staleFinishStorage.WriteBytes(ctx, []byte("same failure input"), "raw/fixture-outage.md"); err != nil {
		t.Fatalf("restore same failed input for retry: %v", err)
	}
	retryFailedID := postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, staleFinishProject, http.StatusAccepted)
	waitForLocalFixtureFile(t, ctx, readyPath)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release same-input retry failure: %v", err)
	}
	waitForLocalPipelineDiagnostic(t, ctx, staleFinishStorage, retryFailedID)
	waitNativeExecutionState(t, ctx, reconciliationFS, retryFailedID, "FAILED")
	assertLocalPipelineQuota(t, ctx, reconciliationFS, userID, staleFinishProject, retryFailedID, 1, "refunded")

	const configFailureProject = "missing-config-project"
	if _, err := storageClient.WithScope(userID, configFailureProject).WriteBytes(ctx, []byte("A local config failure fixture."), "raw/source.md"); err != nil {
		t.Fatalf("seed raw input for config failure: %v", err)
	}
	missingConfigPath := generatedConfigPath + ".missing"
	if err := os.Rename(generatedConfigPath, missingConfigPath); err != nil {
		t.Fatalf("temporarily remove synthetic Pipeline config: %v", err)
	}
	configRestored := false
	defer func() {
		if !configRestored {
			if err := os.Rename(missingConfigPath, generatedConfigPath); err != nil {
				t.Errorf("restore synthetic Pipeline config: %v", err)
			}
		}
	}()
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, configFailureProject, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, configFailureProject, executionID)
	if state != "FAILED" || reason == "" {
		t.Fatalf("missing-config status=%q reason=%q, want visible early failure", state, reason)
	}
	if err := os.Rename(missingConfigPath, generatedConfigPath); err != nil {
		t.Fatalf("restore synthetic Pipeline config: %v", err)
	}
	configRestored = true
	assertLocalPipelineQuota(t, ctx, fsClient, userID, configFailureProject, executionID, 0, "refunded")

	const timeoutProject = "timeout-project"
	if _, err := storageClient.WithScope(userID, timeoutProject).WriteBytes(ctx, []byte("A local timeout fixture."), "raw/fixture-timeout.md"); err != nil {
		t.Fatalf("seed raw input for timeout: %v", err)
	}
	timeoutConfig := []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\nrun_timeout_seconds = 3\n")
	if err := os.WriteFile(generatedConfigPath, timeoutConfig, 0o600); err != nil {
		t.Fatalf("configure synthetic worker timeout: %v", err)
	}
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, timeoutProject, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, timeoutProject, executionID)
	if state != "FAILED" || reason == "" {
		t.Fatalf("timeout worker status=%q reason=%q, want visible timeout", state, reason)
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, timeoutProject, executionID, 0, "refunded")
	if err := os.WriteFile(generatedConfigPath, generatedConfig, 0o600); err != nil {
		t.Fatalf("restore synthetic Pipeline timeout config: %v", err)
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
	assertLocalPipelineQuota(t, ctx, fsClient, userID, spawnFailureProject, "", 0, "refunded")

	const outageProject = "terminal-outage-recovery"
	if _, err := storageClient.WithScope(userID, outageProject).WriteBytes(ctx, []byte("controlled child-result outage"), "raw/fixture-outage.md"); err != nil {
		t.Fatalf("seed controlled native result outage: %v", err)
	}
	workspaceDir := t.TempDir()
	t.Setenv("WORKSPACE_DIR", workspaceDir)
	executionID = postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, outageProject, http.StatusAccepted)
	waitForLocalFixtureFile(t, ctx, filepath.Join(workspaceDir, "fixture-outage-ready"))
	firestoreProxy.SetBlocked(true)
	if err := os.WriteFile(filepath.Join(workspaceDir, "fixture-outage-release"), []byte("release"), 0o600); err != nil {
		t.Fatalf("release controlled native child failure: %v", err)
	}
	waitForLocalPipelineDiagnostic(t, ctx, storageClient.WithScope(userID, outageProject), executionID)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := manager.Close(closeCtx); err != nil {
		closeCancel()
		t.Fatalf("close native manager after controlled Firestore outage: %v", err)
	}
	closeCancel()
	managerClosed = true
	firestoreProxy.SetBlocked(false)
	manager, err = localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: worker, Stderr: workerStderr,
		Project: project, Bucket: bucket, Database: database,
		Scope: scope, WorkDir: t.TempDir(),
		PipelineConfigPath: generatedConfigPath, PipelineBindingsPath: privateBindingsPath,
	})
	if err != nil {
		t.Fatalf("restart native worker manager after Firestore outage: %v", err)
	}
	managerClosed = false
	h.SetLocalPipelineExecutor(manager)
	if err := manager.ReconcileInterrupted(ctx); err != nil {
		t.Fatalf("reconcile observed native result after restart: %v", err)
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, outageProject, executionID, 0, "refunded")
	if running, err := manager.Running(ctx, userID, outageProject); err != nil || running {
		t.Fatalf("recovered native project lock running=%v err=%v, want unlocked", running, err)
	}
	failedExecution = readLocalPipelineExecution(t, ctx, server.Client(), server.URL, token, outageProject, executionID)
	if failedExecution.DiagnosticState != "available" || failedExecution.Diagnostic == nil ||
		failedExecution.Diagnostic.Stage == "" || failedExecution.Diagnostic.ErrorClass == "" ||
		failedExecution.Diagnostic.Message == "" {
		t.Fatalf("recovered native API diagnostic=%+v state=%q, want bounded stage/class/message", failedExecution.Diagnostic, failedExecution.DiagnosticState)
	}
	// The refund permits the same failed input to start again without status polling.
	retryExecutionID := postLocalPipelineRun(t, ctx, server.Client(), server.URL, token, outageProject, http.StatusAccepted)
	state, reason = waitLocalPipelineStatus(t, ctx, server.Client(), server.URL, token, outageProject, retryExecutionID)
	if state != "FAILED" || reason == "" {
		t.Fatalf("same-input retry after outage state=%q reason=%q", state, reason)
	}
	assertLocalPipelineQuota(t, ctx, fsClient, userID, outageProject, retryExecutionID, 0, "refunded")

	if err := manager.Close(ctx); err != nil {
		t.Fatalf("close native worker manager: %v", err)
	}
	managerClosed = true
	if calls := cloudRunCalls.Load(); calls != 0 {
		t.Fatalf("native local pipeline made %d intercepted Cloud Run calls", calls)
	}
}

func assertLocalPipelineQuota(t *testing.T, ctx context.Context, client *scopedfirestore.Client, userID, projectID, executionID string, wantRuns int, wantSettlement string, wantLastRun ...time.Time) time.Time {
	t.Helper()
	runsToday, _, lastRunAt, err := client.LoadQuotaState(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("read local pipeline quota for %s: %v", projectID, err)
	}
	if runsToday != wantRuns {
		t.Fatalf("local pipeline quota runs_today for %s = %d, want %d", projectID, runsToday, wantRuns)
	}
	if len(wantLastRun) > 0 && !lastRunAt.Equal(wantLastRun[0]) {
		t.Fatalf("local pipeline quota last_run_at for %s = %s, want prior run %s", projectID, lastRunAt, wantLastRun[0])
	}
	query := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Where("project_id", "==", projectID)
	reservations, err := query.Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("read local quota reservations for %s: %v", projectID, err)
	}
	var matching map[string]any
	for _, reservation := range reservations {
		data := reservation.Data()
		if executionID == "" || data["execution_id"] == executionID {
			if matching != nil {
				t.Fatalf("multiple local quota reservations match project %s execution %s", projectID, executionID)
			}
			matching = data
		}
	}
	if matching == nil {
		t.Fatalf("local quota reservation missing for project %s execution %s", projectID, executionID)
	}
	if got := matching["settlement_status"]; got != wantSettlement {
		t.Fatalf("local quota reservation settlement for %s = %v, want %s", projectID, got, wantSettlement)
	}
	return lastRunAt
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

func postLocalPipelineRun(t *testing.T, ctx context.Context, client *http.Client, baseURL, token, projectID string, wantStatus int, traceparent ...string) string {
	executionID, _ := postLocalPipelineRunWithRequestID(t, ctx, client, baseURL, token, projectID, wantStatus, traceparent...)
	return executionID
}

func postLocalPipelineRunWithRequestID(t *testing.T, ctx context.Context, client *http.Client, baseURL, token, projectID string, wantStatus int, traceparent ...string) (string, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/pipeline/run", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", projectID)
	request.Header.Set("Content-Type", "application/json")
	if len(traceparent) > 0 && traceparent[0] != "" {
		request.Header.Set("traceparent", traceparent[0])
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("trigger local pipeline for %s: %v", projectID, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != wantStatus {
		t.Fatalf("pipeline trigger status=%d body=%s, want %d", response.StatusCode, data, wantStatus)
	}
	requestID := response.Header.Get("X-Request-ID")
	if uuid.Validate(requestID) != nil {
		t.Fatalf("pipeline trigger returned invalid server request ID")
	}
	if wantStatus != http.StatusAccepted {
		return "", requestID
	}
	var payload struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.ExecutionID == "" {
		t.Fatalf("pipeline trigger response has no execution ID: %s (%v)", data, err)
	}
	return payload.ExecutionID, requestID
}

func localPipelineWorkerTraceIdentities(t *testing.T, path string) []otlpfixture.SpanIdentity {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bounded native-worker trace identity stderr: %v", err)
	}
	var identities []otlpfixture.SpanIdentity
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "OTLP_QA_SPAN case=UC5 ") {
			continue
		}
		fields := make(map[string]string, 8)
		for _, field := range strings.Fields(strings.TrimPrefix(line, "OTLP_QA_SPAN ")) {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key == "" {
				t.Fatalf("native-worker trace identity has malformed fixed fields")
			}
			if _, exists := fields[key]; exists {
				t.Fatalf("native-worker trace identity repeated a field")
			}
			fields[key] = value
		}
		if len(fields) != 10 || fields["case"] != "UC5" || fields["service_name"] != "lwc-worker" || fields["scope"] != "llm-wiki-worker/pipeline" {
			t.Fatalf("native-worker trace identity did not use the fixed test schema")
		}
		if fields["name"] != "pipeline.worker" {
			continue
		}
		traceID, spanID := fields["trace_id"], fields["span_id"]
		if decoded, err := hex.DecodeString(traceID); err != nil || len(decoded) != 16 {
			t.Fatalf("native-worker trace identity had an invalid trace ID")
		}
		if decoded, err := hex.DecodeString(spanID); err != nil || len(decoded) != 8 {
			t.Fatalf("native-worker trace identity had an invalid span ID")
		}
		identity := otlpfixture.SpanIdentity{
			TraceID: traceID, SpanID: spanID, ParentTraceID: fields["parent_trace_id"],
			ParentSpanID: fields["parent_span_id"], ServiceName: fields["service_name"], Scope: fields["scope"], Name: fields["name"],
			Attributes: map[string]string{"request.id": fields["request_id"]},
		}
		if parent := identity.ParentSpanID; parent != "" {
			if decoded, err := hex.DecodeString(parent); err != nil || len(decoded) != 8 {
				t.Fatalf("native-worker trace identity had an invalid parent span ID")
			}
		}
		if parent := identity.ParentTraceID; parent != "" {
			if decoded, err := hex.DecodeString(parent); err != nil || len(decoded) != 16 {
				t.Fatalf("native-worker trace identity had an invalid parent trace ID")
			}
		}
		if links := fields["links"]; links != "" {
			for _, raw := range strings.Split(links, ",") {
				linkTraceID, linkSpanID, ok := strings.Cut(raw, "/")
				if !ok {
					t.Fatalf("native-worker trace identity had an invalid link")
				}
				if decoded, err := hex.DecodeString(linkTraceID); err != nil || len(decoded) != 16 {
					t.Fatalf("native-worker trace identity had an invalid linked trace ID")
				}
				if decoded, err := hex.DecodeString(linkSpanID); err != nil || len(decoded) != 8 {
					t.Fatalf("native-worker trace identity had an invalid linked span ID")
				}
				identity.Links = append(identity.Links, otlpfixture.LinkIdentity{TraceID: linkTraceID, SpanID: linkSpanID})
			}
		}
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		t.Fatal("native-worker did not emit any fixed-schema trace identity on stderr")
	}
	return identities
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
				FailureReason  string `json:"failure_reason"`
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
				if reason == "" {
					reason = payload.LastExecution.FailureReason
				}
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

func readLocalPipelineExecution(t *testing.T, ctx context.Context, client *http.Client, baseURL, token, projectID, executionID string) *handlerapi.PipelineExecutionResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/pipeline/status?execution_id="+url.QueryEscape(executionID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", projectID)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("read native pipeline execution: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("native pipeline status response=%d err=%v body=%s", response.StatusCode, err, data)
	}
	var payload struct {
		LastExecution *handlerapi.PipelineExecutionResponse `json:"last_execution"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.LastExecution == nil {
		t.Fatalf("decode native pipeline status body=%s err=%v", data, err)
	}
	return payload.LastExecution
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

type gatedTCPProxy struct {
	listener      net.Listener
	target        string
	blocked       atomic.Bool
	paused        atomic.Bool
	mu            sync.Mutex
	conns         map[net.Conn]struct{}
	pauseObserved chan struct{}
	pauseResume   chan struct{}
}

type gatedStorageProxy struct {
	server  *httptest.Server
	proxy   *httputil.ReverseProxy
	blocked atomic.Bool
}

func newGatedStorageProxy(t *testing.T, endpoint string) *gatedStorageProxy {
	t.Helper()
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		t.Fatalf("parse loopback Storage emulator endpoint %q", endpoint)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
	}
	gate := &gatedStorageProxy{proxy: proxy}
	gate.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if gate.blocked.Load() {
			http.Error(w, "synthetic Storage read unavailable", http.StatusForbidden)
			return
		}
		gate.proxy.ServeHTTP(w, request)
	}))
	return gate
}

func (p *gatedStorageProxy) URL() string { return p.server.URL }

func (p *gatedStorageProxy) SetBlocked(blocked bool) { p.blocked.Store(blocked) }

func (p *gatedStorageProxy) Close() { p.server.Close() }

func newGatedTCPProxy(t *testing.T, target string) *gatedTCPProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for local Firestore outage proxy: %v", err)
	}
	proxy := &gatedTCPProxy{listener: listener, target: target, conns: make(map[net.Conn]struct{})}
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Errorf("close local Firestore outage proxy: %v", err)
		}
	})
	go proxy.accept()
	return proxy
}

func (p *gatedTCPProxy) Address() string { return p.listener.Addr().String() }

func (p *gatedTCPProxy) SetBlocked(blocked bool) {
	p.blocked.Store(blocked)
	if !blocked {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.conns {
		_ = conn.Close()
	}
}

func (p *gatedTCPProxy) SetPaused(paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if paused {
		if p.paused.Load() {
			return
		}
		p.pauseObserved = make(chan struct{}, 1)
		p.pauseResume = make(chan struct{})
		p.paused.Store(true)
		for conn := range p.conns {
			_ = conn.Close()
		}
		return
	}
	if p.paused.Swap(false) && p.pauseResume != nil {
		close(p.pauseResume)
	}
}

func (p *gatedTCPProxy) WaitPaused(ctx context.Context) error {
	p.mu.Lock()
	observed := p.pauseObserved
	p.mu.Unlock()
	if observed == nil {
		return errors.New("Firestore proxy pause was not armed")
	}
	select {
	case <-observed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *gatedTCPProxy) Close() error {
	p.SetPaused(false)
	err := p.listener.Close()
	p.mu.Lock()
	for conn := range p.conns {
		_ = conn.Close()
	}
	p.mu.Unlock()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (p *gatedTCPProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		if p.blocked.Load() {
			_ = client.Close()
			continue
		}
		if !p.awaitResume(client) {
			_ = client.Close()
			continue
		}
		upstream, err := net.DialTimeout("tcp", p.target, 2*time.Second)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.track(client, upstream)
		if p.blocked.Load() || p.paused.Load() {
			_ = client.Close()
			_ = upstream.Close()
		}
		go p.forward(client, upstream)
	}
}

func (p *gatedTCPProxy) awaitResume(client net.Conn) bool {
	p.mu.Lock()
	if !p.paused.Load() {
		p.mu.Unlock()
		return true
	}
	resume := p.pauseResume
	observed := p.pauseObserved
	p.conns[client] = struct{}{}
	p.mu.Unlock()
	select {
	case observed <- struct{}{}:
	default:
	}
	<-resume
	p.mu.Lock()
	delete(p.conns, client)
	p.mu.Unlock()
	return true
}

func (p *gatedTCPProxy) track(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range conns {
		p.conns[conn] = struct{}{}
	}
}

func (p *gatedTCPProxy) forward(client, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
	p.mu.Lock()
	delete(p.conns, client)
	delete(p.conns, upstream)
	p.mu.Unlock()
}

func emulatorHostPort(t *testing.T, endpoint string) string {
	t.Helper()
	hostPort := endpoint
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		hostPort = parsed.Host
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil || port == "" {
		t.Fatalf("local Firestore endpoint %q must contain a host and port", endpoint)
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort(host, port)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("local Firestore endpoint %q must point to loopback", endpoint)
	}
	return net.JoinHostPort(host, port)
}

func waitForLocalFixtureFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read local worker fixture signal: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("local worker fixture signal was not written before timeout: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForLocalPipelineDiagnostic(t *testing.T, ctx context.Context, storage *gcs.Client, executionID string) {
	t.Helper()
	path := "cache/pipeline-" + executionID + ".failure.json"
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := storage.ReadFileLimited(ctx, path, 4<<10+1)
		if err == nil && len(data) > 0 {
			return
		}
		if err != nil && !errors.Is(err, cloudstorage.ErrObjectNotExist) && !errors.Is(err, storeapi.ErrObjectNotExist) {
			t.Fatalf("read native failure diagnostic: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native failure diagnostic was not written before timeout: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitNativeExecutionState(t *testing.T, ctx context.Context, client *scopedfirestore.Client, executionID, want string) {
	t.Helper()
	executionRef := scopedfirestore.Collection(client.Raw(), "executions").Doc(executionID)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := executionRef.Get(ctx)
		if err == nil && snapshot.Data()["status"] == want {
			return
		}
		if err != nil && status.Code(err) != codes.NotFound {
			t.Fatalf("read native execution %s while waiting for %s: %v", executionID, want, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native execution %s did not reach %s before timeout: %v", executionID, want, ctx.Err())
		case <-ticker.C:
		}
	}
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
	for _, collection := range []string{"pipeline_quota", "pipeline_quota_reservations", "local_pipeline_locks", "executions"} {
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
	for _, collection := range []string{"pipeline_quota", "pipeline_quota_reservations", "local_pipeline_locks", "executions"} {
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

type localPipelineCitationTransport struct{}

func (localPipelineCitationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	requestBody, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		return nil, fmt.Errorf("decode local Query model request: %w", err)
	}
	var prompt string
	for _, message := range payload.Messages {
		if message.Role == "user" {
			prompt = message.Content
		}
	}
	start := strings.Index(prompt, "[CITATION_REF_")
	if start < 0 {
		return nil, errors.New("local Query model request has no server-issued citation")
	}
	end := strings.IndexByte(prompt[start:], ']')
	if end < 0 {
		return nil, errors.New("local Query model request has an invalid citation token")
	}
	token := prompt[start : start+end+1]
	responseBody, err := json.Marshal(map[string]any{"choices": []any{
		map[string]any{"message": map[string]string{"content": "The newly published raw article contains the requested detail " + token}},
	}})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    request,
	}, nil
}
