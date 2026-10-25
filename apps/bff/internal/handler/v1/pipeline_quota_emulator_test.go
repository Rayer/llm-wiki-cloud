package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/search"
	storeapi "github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This is the Cloud Run counterpart to the native subprocess emulator
// acceptance test. Cloud Run itself is a loopback fixture; Firestore and GCS
// use their real loopback emulators.
func TestPipelineQuotaCloudRunHandlerEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") || !requireLoopbackEmulator(t, "STORAGE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore and Storage emulators")
	}

	const (
		project  = "llm-wiki-cloud"
		bucket   = "llm-wiki-cloud-local"
		database = "llm-wiki-cloud-local"
		userID   = "lwc371-quota-user"
	)
	scope := fmt.Sprintf("lwc371-quota-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	t.Setenv("FIRESTORE_DATABASE_ID", database)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	storageClient, err := cloudstorage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create loopback Storage client: %v", err)
	}
	fsClient, err := scopedfirestore.NewClientWithDatabase(project, database, "", "")
	if err != nil {
		_ = storageClient.Close()
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	quotaClient, err := gcs.NewClient(bucket)
	if err != nil {
		_ = fsClient.Close()
		_ = storageClient.Close()
		t.Fatalf("create scoped GCS client: %v", err)
	}
	bucketAvailable := false
	workerBucket := "lwc371-cloudrun-producer-" + fmt.Sprintf("%d", time.Now().UnixNano())
	workerBucketAvailable := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("delete/read back Firestore test scope: %v", err)
		}
		if bucketAvailable {
			if err := deletePipelineQuotaObjectScope(cleanupCtx, storageClient, bucket, scope); err != nil {
				t.Errorf("delete/read back Storage test scope: %v", err)
			}
		}
		if workerBucketAvailable {
			if err := deleteQuotaFixtureBucket(cleanupCtx, storageClient, workerBucket); err != nil {
				t.Errorf("delete/read back Cloud Run worker fixture bucket: %v", err)
			}
		}
		if err := quotaClient.Close(); err != nil {
			t.Errorf("close scoped GCS client: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close Firestore client: %v", err)
		}
		if err := storageClient.Close(); err != nil {
			t.Errorf("close Storage client: %v", err)
		}
	})

	bucketHandle := storageClient.Bucket(bucket)
	if _, err := bucketHandle.Attrs(ctx); errors.Is(err, cloudstorage.ErrBucketNotExist) {
		if err := bucketHandle.Create(ctx, project, nil); err != nil {
			t.Fatalf("create disposable emulator bucket: %v", err)
		}
		bucketAvailable = true
	} else if err != nil {
		t.Fatalf("read emulator bucket: %v", err)
	} else {
		bucketAvailable = true
	}

	fixture := newQuotaCloudRunFixture()
	h := New(quotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.metadataTokenURL = "http://run.test/token"
	h.SetPipelineJobURL("http://run.test/job:run")
	h.SetPipelineQuotaConfig(5, 1, 1, nil)
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	gin.SetMode(gin.TestMode)

	// A successful Cloud Run execution stays charged after background
	// reconciliation, without relying on user status polling.
	successProject := "success-control"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, successProject)
	if _, err := h.getMetadataAccessToken(ctx); err != nil {
		t.Fatalf("fixture metadata token: %v", err)
	}
	if _, _, _, err := h.pendingWorkForProject(ctx, userID, successProject); err != nil {
		t.Fatalf("fixture pending work: %v", err)
	}
	if _, err := h.isPipelineRunning(ctx, userID, successProject); err != nil {
		t.Fatalf("fixture activity lookup: %v", err)
	}
	successID := triggerQuotaPipeline(t, h, userID, successProject, http.StatusAccepted)
	successReservation := fixture.reservationID(successID)
	assertQuotaState(t, ctx, fsClient, userID, successProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	fixture.setStatus(successID, "SUCCEEDED")
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile successful execution: %v", err)
	}
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat successful reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, successReservation, "charged")

	// Unknown state and a temporary provider outage both retain the debit;
	// once FAILED is confirmed, the same input can be retried immediately.
	failedProject := "failed-child"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, failedProject)
	failedID := triggerQuotaPipeline(t, h, userID, failedProject, http.StatusAccepted)
	failedReservation := fixture.reservationID(failedID)
	fixture.setStatus(failedID, "UNKNOWN")
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile unknown execution: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, failedReservation, "pending")
	assertQuotaState(t, ctx, fsClient, userID, failedProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	fixture.setExecutionUnavailable(failedID, true)
	if err := h.ReconcilePipelineQuota(ctx); err == nil {
		t.Fatal("temporary execution-status outage returned nil")
	}
	assertQuotaState(t, ctx, fsClient, userID, failedProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	fixture.setExecutionUnavailable(failedID, false)
	fixture.setStatus(failedID, "FAILED")
	failureReceipt, err := json.Marshal(struct {
		Version    int    `json:"version"`
		Status     string `json:"status"`
		Stage      string `json:"stage"`
		ErrorClass string `json:"error_class"`
		Execution  string `json:"execution"`
	}{Version: 1, Status: "failed", Stage: "synto_run", ErrorClass: "child_exit", Execution: failedID})
	if err != nil {
		t.Fatalf("marshal confirmed pre-publication failure receipt: %v", err)
	}
	if _, err := quotaClient.WithScope(userID, failedProject).WriteBytes(ctx, failureReceipt, "cache/pipeline-"+failedID+".failure.json"); err != nil {
		t.Fatalf("write confirmed pre-publication failure receipt: %v", err)
	}
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile confirmed failure: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, failedReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, failedProject, 0, pipelinequota.DayKeyUTC(time.Now()))
	assertQuotaPipelineStatus(t, h, userID, failedProject, failedID, "FAILED", "", "refunded", 0)
	retryID := triggerQuotaPipeline(t, h, userID, failedProject, http.StatusAccepted)
	fixture.setStatus(retryID, "SUCCEEDED")
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("settle same-input retry: %v", err)
	}
	assertQuotaState(t, ctx, fsClient, userID, failedProject, 1, pipelinequota.DayKeyUTC(time.Now()))

	// A provider-confirmed failed invoke is a definite failure and refunds its
	// reservation in the accepted intent path, with the failure persisted for
	// status polling and the shared project claim released.
	invokeProject := "invoke-failure"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, invokeProject)
	failedIntent := triggerQuotaPipelineIntent(t, h, userID, invokeProject)
	if failedIntent.IntentStatus != "failed" || failedIntent.IntentID == "" || failedIntent.ExecutionID != "" || failedIntent.Message == "" {
		t.Fatalf("definite invoke failure intent=%+v; want actionable failed intent without execution", failedIntent)
	}
	assertQuotaState(t, ctx, fsClient, userID, invokeProject, 0, pipelinequota.DayKeyUTC(time.Now()))
	assertQuotaProjectReservation(t, ctx, fsClient, invokeProject, "released")
	failedIntentReservation, exists, err := fsClient.GetQuotaReservation(ctx, failedIntent.IntentID)
	if err != nil || !exists || failedIntentReservation.IntentStatus != "failed" || failedIntentReservation.SettlementStatus != "released" || failedIntentReservation.IntentErrorMessage != failedIntent.Message {
		t.Fatalf("definite invoke failure was not durably settled: reservation=%+v exists=%t err=%v", failedIntentReservation, exists, err)
	}

	// A definite 403 still records no-execution evidence when the first
	// settlement consumes its entire recovery deadline. The real GCS emulator
	// honors the expired context, so the marker requires an independent bound.
	const definiteInvokeProject = "definite-invoke-recovery"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, definiteInvokeProject)
	failedSettlementStore := &expireFirstQuotaSettlement{pipelineQuotaStore: fsClient}
	definiteInvokeHandler := New(quotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	definiteInvokeHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	definiteInvokeHandler.SetPipelineQuotaStore(failedSettlementStore)
	definiteInvokeHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	definiteInvokeHandler.metadataTokenURL = "http://run.test/token"
	definiteInvokeHandler.SetPipelineJobURL("http://run.test/job:run")
	definite403Observed := false
	definiteInvokeHandler.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/token":
			return testHTTPResponse(http.StatusOK, `{"access_token":"test-token"}`), nil
		case "/job/executions":
			return testHTTPResponse(http.StatusOK, `{"executions":[]}`), nil
		case "/job:run":
			definite403Observed = true
			return testHTTPResponse(http.StatusForbidden, `{"error":"synthetic permission denied"}`), nil
		default:
			return testHTTPResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	requestRecorder := httptest.NewRecorder()
	definiteInvokeContext, _ := gin.CreateTestContext(requestRecorder)
	definiteInvokeContext.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil).WithContext(ctx)
	definiteInvokeContext.Set("userID", userID)
	definiteInvokeContext.Set("projectID", definiteInvokeProject)
	definiteInvokeHandler.PipelineRun(definiteInvokeContext)
	if requestRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("deadline-expired definite invocation status=%d want 500: %s", requestRecorder.Code, requestRecorder.Body.String())
	}
	if !definite403Observed {
		t.Fatal("definite invoke fixture did not exercise the HTTP 403 response")
	}
	if !errors.Is(failedSettlementStore.contextErr, context.DeadlineExceeded) {
		t.Fatalf("first settlement context error=%v, want deadline exceeded", failedSettlementStore.contextErr)
	}
	definiteInvokeReservation := findQuotaReservation(t, ctx, fsClient, userID, definiteInvokeProject)
	if definiteInvokeReservation.ExecutionID != "" || definiteInvokeReservation.SettlementStatus != "pending" {
		t.Fatalf("deadline-expired invoke reservation=%+v, want pending with no execution before restart", definiteInvokeReservation)
	}
	marker, err := quotaClient.WithScope(userID, definiteInvokeProject).ReadFileLimited(ctx, "cache/pipeline-"+definiteInvokeReservation.ID+".failure.json", 4<<10+1)
	if err != nil || len(marker) == 0 {
		t.Fatalf("deadline-expired invoke recovery marker size=%d err=%v, want durable evidence", len(marker), err)
	}
	restartedInvokeHandler := New(quotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	restartedInvokeHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	restartedInvokeHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	restartedInvokeHandler.metadataTokenURL = h.metadataTokenURL
	restartedInvokeHandler.SetPipelineJobURL("http://run.test/job:run")
	restartedInvokeHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	if err := restartedInvokeHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("restart recovery of deadline-expired no-execution invocation: %v", err)
	}
	if err := restartedInvokeHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat deadline-expired no-execution recovery: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, definiteInvokeReservation.ID, "released")
	assertQuotaState(t, ctx, fsClient, userID, definiteInvokeProject, 0, pipelinequota.DayKeyUTC(time.Now()))
	retryDefiniteInvokeID := triggerQuotaPipeline(t, restartedInvokeHandler, userID, definiteInvokeProject, http.StatusAccepted)
	if fixture.reservationID(retryDefiniteInvokeID) == "" {
		t.Fatal("same-input retry after confirmed no-invocation release has no reservation")
	}
	assertQuotaState(t, ctx, fsClient, userID, definiteInvokeProject, 1, pipelinequota.DayKeyUTC(time.Now()))

	unenforcedProject := "unenforced-run"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, unenforcedProject)
	unenforcedHandler := New(quotaClient, nil, search.NewIndex(), conceptcache.New(), nil, nil)
	unenforcedHandler.metadataTokenURL = h.metadataTokenURL
	unenforcedHandler.SetPipelineJobURL("http://run.test/job:run")
	unenforcedHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	unenforcedHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	triggerQuotaPipeline(t, unenforcedHandler, userID, unenforcedProject, http.StatusAccepted)
	assertQuotaState(t, ctx, fsClient, userID, unenforcedProject, 0, "")
	assertQuotaProjectHasNoReservation(t, ctx, fsClient, unenforcedProject)

	adminUser, adminProject := "lwc371-admin-owner", "admin-trigger"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, adminUser, adminProject)
	h.projectExists = func(context.Context, string) error { return nil }
	h.adminProjectRecordLoader = func(context.Context, string) (adminProjectRecord, error) {
		return adminProjectRecord{id: adminUser + "_" + adminProject, userID: adminUser, projectID: adminProject}, nil
	}
	adminRecorder := httptest.NewRecorder()
	adminContext, _ := gin.CreateTestContext(adminRecorder)
	adminContext.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/"+adminUser+"_"+adminProject+"/pipeline", nil)
	adminContext.Params = gin.Params{{Key: "id", Value: adminUser + "_" + adminProject}}
	h.AdminPipelineTrigger(adminContext)
	if adminRecorder.Code != http.StatusAccepted {
		t.Fatalf("admin pipeline trigger returned %d, want 202: %s", adminRecorder.Code, adminRecorder.Body.String())
	}
	assertQuotaState(t, ctx, fsClient, adminUser, adminProject, 0, "")
	assertAdminPipelineReservation(t, ctx, fsClient, adminProject)

	// A valid execution publication receipt is authoritative even when Cloud
	// Run reports a cleanup exit failure. Build and run the actual deployed-mode
	// worker against a disposable GCS emulator bucket, then let the actual quota
	// reconciler consume its execution-owned manifest and receipt.
	committedProject := "published-cleanup-failure"
	if err := storageClient.Bucket(workerBucket).Create(ctx, project, nil); err != nil {
		t.Fatalf("create isolated Cloud Run worker emulator bucket: %v", err)
	}
	workerBucketAvailable = true
	deployedPipelineConfig := []byte("[pipeline]\nauto_approve = true\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\nrun_timeout_seconds = 15\n")
	writeDeployedPipelineConfig := func() error {
		configWriter := storageClient.Bucket(workerBucket).Object("pipeline-config/synto.toml").NewWriter(ctx)
		if _, err := configWriter.Write(deployedPipelineConfig); err != nil {
			_ = configWriter.Close()
			return err
		}
		return configWriter.Close()
	}
	if err := writeDeployedPipelineConfig(); err != nil {
		t.Fatalf("commit synthetic deployed Pipeline config: %v", err)
	}
	t.Setenv("LOCAL_CLOUD_SCOPE", "")
	storageEndpoint := strings.TrimSpace(os.Getenv("STORAGE_EMULATOR_HOST"))
	deployedQuotaClient, err := gcs.NewClient(workerBucket)
	if err != nil {
		t.Fatalf("create unscoped deployed-mode GCS reader: %v", err)
	}
	t.Cleanup(func() {
		if err := deployedQuotaClient.Close(); err != nil {
			t.Errorf("close deployed-mode GCS reader: %v", err)
		}
	})
	t.Setenv("STORAGE_EMULATOR_HOST", storageEndpoint)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	if _, err := deployedQuotaClient.WithScope(userID, committedProject).WriteBytes(ctx, []byte("synthetic deployed worker input"), "raw/source.md"); err != nil {
		t.Fatalf("seed deployed-mode worker input: %v", err)
	}
	deployedHandler := New(deployedQuotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	deployedHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	deployedHandler.metadataTokenURL = h.metadataTokenURL
	deployedHandler.SetPipelineJobURL("http://run.test/job:run")
	deployedHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	deployedHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	committedID := triggerQuotaPipeline(t, deployedHandler, userID, committedProject, http.StatusAccepted)
	committedReservation := fixture.reservationID(committedID)
	worker := buildLocalCloudPipelineFixtureWorker(t, ctx)
	runConfirmedWorkerFailure := func(projectID, executionID string) {
		t.Helper()
		projectStore := deployedQuotaClient.WithScope(userID, projectID)
		if _, err := projectStore.WriteBytes(ctx, []byte("synthetic child-failure input"), "raw/fixture-fail.md"); err != nil {
			t.Fatalf("seed deployed worker failure input for %s: %v", projectID, err)
		}
		output, err := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, projectID, executionID, storageEndpoint)
		if err == nil || !strings.Contains(string(output), "fixture compile failure") {
			t.Fatalf("run deployed worker failure for %s: output=%q err=%v", projectID, output, err)
		}
		data, err := projectStore.ReadFileLimited(ctx, "cache/pipeline-"+executionID+".failure.json", 4<<10+1)
		var diagnostic struct {
			Status    string `json:"status"`
			Stage     string `json:"stage"`
			Error     string `json:"error_class"`
			Execution string `json:"execution"`
		}
		if err != nil || json.Unmarshal(data, &diagnostic) != nil || diagnostic.Status != "failed" || diagnostic.Stage != "synto_run" || diagnostic.Error != "unknown" || diagnostic.Execution != executionID {
			t.Fatalf("deployed worker failure evidence for %s: diagnostic=%+v bytes=%q err=%v", projectID, diagnostic, data, err)
		}
	}
	workerCommand := exec.CommandContext(ctx, worker,
		"--bucket", workerBucket, "--user-id", userID, "--project-id", committedProject,
		"--execution-id", committedID, "run", `[["run","--auto-approve"]]`)
	workerCommand.Env = []string{
		"GOOGLE_CLOUD_PROJECT=" + project,
		"GCP_PROJECT=" + project,
		"STORAGE_EMULATOR_HOST=" + os.Getenv("STORAGE_EMULATOR_HOST"),
		"LOCAL_CLOUD_SCOPE=",
		"TMPDIR=" + os.TempDir(),
	}
	workerOutput, err := workerCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("run deployed-mode Cloud Run worker fixture: %v\n%s", err, workerOutput)
	}
	manifest, _, exists, err := deployedQuotaClient.WithScope(userID, committedProject).CurrentManifest(ctx)
	if err != nil || !exists || manifest.LocalExecutionID != committedID {
		t.Fatalf("deployed worker manifest exists=%v execution=%q err=%v, want execution-owned commit", exists, manifest.LocalExecutionID, err)
	}
	if _, err := deployedQuotaClient.WithScope(userID, committedProject).ReadLocalPublicationReceipt(ctx, committedID); err != nil {
		t.Fatalf("deployed worker publication receipt readback: %v", err)
	}
	storageProxy := newQuotaStorageProxy(t, storageEndpoint)
	t.Setenv("STORAGE_EMULATOR_HOST", storageProxy.URL())
	t.Setenv("LOCAL_CLOUD_SCOPE", "")
	proxyQuotaClient, err := gcs.NewClient(workerBucket)
	if err != nil {
		t.Fatalf("create proxied deployed-mode GCS reader: %v", err)
	}
	t.Cleanup(func() {
		if err := proxyQuotaClient.Close(); err != nil {
			t.Errorf("close proxied deployed-mode GCS reader: %v", err)
		}
	})
	t.Setenv("STORAGE_EMULATOR_HOST", storageEndpoint)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	proxyManifest, _, proxyManifestExists, proxyManifestErr := proxyQuotaClient.WithScope(userID, committedProject).CurrentManifest(ctx)
	if proxyManifestErr != nil || !proxyManifestExists || proxyManifest.LocalExecutionID != committedID {
		t.Fatalf("proxied worker manifest exists=%v execution=%q err=%v, want execution-owned commit", proxyManifestExists, proxyManifest.LocalExecutionID, proxyManifestErr)
	}
	if _, err := proxyQuotaClient.WithScope(userID, committedProject).ReadLocalPublicationReceipt(ctx, committedID); err != nil {
		t.Fatalf("proxied worker publication receipt readback: %v", err)
	}
	proxyHandler := New(proxyQuotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	proxyHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	proxyHandler.metadataTokenURL = h.metadataTokenURL
	proxyHandler.SetPipelineJobURL("http://run.test/job:run")
	proxyHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	proxyHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}

	// A later real Cloud Run worker can fail before publication while the
	// current manifest still belongs to an earlier success. Its own diagnostic
	// resolves the failure without a status endpoint poll or manifest guessing.
	const prepublicationProject = "early-config-after-prior-publication"
	if _, err := deployedQuotaClient.WithScope(userID, prepublicationProject).WriteBytes(ctx, []byte("synthetic early-config input"), "raw/source.md"); err != nil {
		t.Fatalf("seed early-config worker input: %v", err)
	}
	firstPublishedID := triggerQuotaPipeline(t, deployedHandler, userID, prepublicationProject, http.StatusAccepted)
	firstPublishedReservation := fixture.reservationID(firstPublishedID)
	if output, err := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, prepublicationProject, firstPublishedID, storageEndpoint); err != nil {
		t.Fatalf("run first early-config project publication: %v\n%s", err, output)
	}
	fixture.setStatus(firstPublishedID, "SUCCEEDED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile first early-config project publication: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, firstPublishedReservation, "charged")
	time.Sleep(1100 * time.Millisecond)

	configObject := storageClient.Bucket(workerBucket).Object("pipeline-config/synto.toml")
	configAttrs, err := configObject.Attrs(ctx)
	if err != nil {
		t.Fatalf("read disposable worker config generation: %v", err)
	}
	if err := configObject.Generation(configAttrs.Generation).Delete(ctx); err != nil {
		t.Fatalf("remove only disposable worker config for early-failure fixture: %v", err)
	}
	earlyFailureID := triggerQuotaPipeline(t, deployedHandler, userID, prepublicationProject, http.StatusAccepted)
	earlyFailureReservation := fixture.reservationID(earlyFailureID)
	earlyFailureOutput, earlyFailureErr := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, prepublicationProject, earlyFailureID, storageEndpoint)
	if earlyFailureErr == nil || !strings.Contains(string(earlyFailureOutput), "read deployed Pipeline config") {
		t.Fatalf("early config worker output=%q err=%v, want deployed config read failure", earlyFailureOutput, earlyFailureErr)
	}
	if err := writeDeployedPipelineConfig(); err != nil {
		t.Fatalf("restore disposable worker config after early-failure fixture: %v", err)
	}
	var earlyFailureDiagnostic struct {
		Status      string `json:"status"`
		Stage       string `json:"stage"`
		ErrorClass  string `json:"error_class"`
		ExecutionID string `json:"execution"`
		Message     string `json:"message"`
	}
	diagnosticBytes, err := deployedQuotaClient.WithScope(userID, prepublicationProject).ReadFileLimited(ctx, "cache/pipeline-"+earlyFailureID+".failure.json", 4<<10+1)
	if err != nil || json.Unmarshal(diagnosticBytes, &earlyFailureDiagnostic) != nil ||
		earlyFailureDiagnostic.Status != "failed" || earlyFailureDiagnostic.Stage != "synto_config_validation" ||
		earlyFailureDiagnostic.ErrorClass != "io" || earlyFailureDiagnostic.ExecutionID != earlyFailureID || earlyFailureDiagnostic.Message == "" {
		t.Fatalf("early failure diagnostic=%+v bytes=%q err=%v, want bounded execution-owned config failure", earlyFailureDiagnostic, diagnosticBytes, err)
	}
	firstProjectManifest, _, manifestExists, err := deployedQuotaClient.WithScope(userID, prepublicationProject).CurrentManifest(ctx)
	if err != nil || !manifestExists || firstProjectManifest.LocalExecutionID != firstPublishedID {
		t.Fatalf("early failure changed prior manifest: exists=%v execution=%q err=%v", manifestExists, firstProjectManifest.LocalExecutionID, err)
	}
	fixture.setStatus(earlyFailureID, "FAILED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile early config failure: %v", err)
	}
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat early config failure reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, firstPublishedReservation, "charged")
	assertQuotaReservation(t, ctx, fsClient, earlyFailureReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, prepublicationProject, 1, pipelinequota.DayKeyUTC(time.Now()))

	// Retrying the unchanged input after config recovery publishes normally.
	retryEarlyFailureID := triggerQuotaPipeline(t, deployedHandler, userID, prepublicationProject, http.StatusAccepted)
	retryEarlyFailureReservation := fixture.reservationID(retryEarlyFailureID)
	if output, err := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, prepublicationProject, retryEarlyFailureID, storageEndpoint); err != nil {
		t.Fatalf("run same-input early-config retry: %v\n%s", err, output)
	}
	fixture.setStatus(retryEarlyFailureID, "SUCCEEDED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile same-input early-config retry: %v", err)
	}
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat same-input early-config retry reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, retryEarlyFailureReservation, "charged")
	assertQuotaState(t, ctx, fsClient, userID, prepublicationProject, 2, pipelinequota.DayKeyUTC(time.Now()))

	fixture.setStatus(committedID, "FAILED")
	storageProxy.SetBlocked(true)
	storageOutageCtx, storageOutageCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := proxyHandler.ReconcilePipelineQuota(storageOutageCtx); err != nil {
		storageOutageCancel()
		t.Fatalf("reconcile published execution during temporary GCS outage: %v", err)
	}
	storageOutageCancel()
	assertQuotaReservation(t, ctx, fsClient, committedReservation, "pending")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	storageProxy.SetBlocked(false)
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile published execution after GCS recovery: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, committedReservation, "charged")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	// Provider process status stays distinct from the independently verified
	// publication receipt that keeps this execution charged.
	assertQuotaPipelineStatus(t, proxyHandler, userID, committedProject, committedID, "FAILED", "", "charged", 1)

	// A later failed execution must be refunded from its own bounded diagnostic
	// even while the current manifest remains owned by the preceding success.
	if _, err := deployedQuotaClient.WithScope(userID, committedProject).WriteBytes(ctx, []byte("synthetic child-failure input"), "raw/fixture-fail.md"); err != nil {
		t.Fatalf("seed later child-failure input: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	failedAfterPriorID := triggerQuotaPipeline(t, deployedHandler, userID, committedProject, http.StatusAccepted)
	failedAfterPriorReservation := fixture.reservationID(failedAfterPriorID)
	failedOutput, failedErr := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, committedProject, failedAfterPriorID, storageEndpoint)
	if failedErr == nil || !strings.Contains(string(failedOutput), "fixture compile failure") {
		t.Fatalf("later deployed worker failure output=%q err=%v, want the synthetic child failure", failedOutput, failedErr)
	}
	fixture.setStatus(failedAfterPriorID, "FAILED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile definitive failure after earlier publication: %v", err)
	}
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat definitive failure reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, committedReservation, "charged")
	assertQuotaReservation(t, ctx, fsClient, failedAfterPriorReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 1, pipelinequota.DayKeyUTC(time.Now()))

	// The same failed input can be retried after that refund, with the same
	// execution-owned failure evidence and no status endpoint polling.
	retryAfterPriorID := triggerQuotaPipeline(t, deployedHandler, userID, committedProject, http.StatusAccepted)
	retryAfterPriorReservation := fixture.reservationID(retryAfterPriorID)
	retryOutput, retryErr := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, committedProject, retryAfterPriorID, storageEndpoint)
	if retryErr == nil || !strings.Contains(string(retryOutput), "fixture compile failure") {
		t.Fatalf("same-input deployed worker retry output=%q err=%v, want the synthetic child failure", retryOutput, retryErr)
	}
	fixture.setStatus(retryAfterPriorID, "FAILED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile same-input failed retry: %v", err)
	}
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat same-input failed retry reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, retryAfterPriorReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 1, pipelinequota.DayKeyUTC(time.Now()))

	// A committed execution remains charged from its own receipt after a later
	// actual worker publication overwrites the current manifest.
	rawFixtureFailure := storeapi.ProjectObjectPath(userID, committedProject, "raw/fixture-fail.md")
	if err := storageClient.Bucket(workerBucket).Object(rawFixtureFailure).Delete(ctx); err != nil {
		t.Fatalf("remove synthetic failure input for overwrite fixture: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	overwrittenID := triggerQuotaPipeline(t, deployedHandler, userID, committedProject, http.StatusAccepted)
	overwrittenReservation := fixture.reservationID(overwrittenID)
	if output, err := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, committedProject, overwrittenID, storageEndpoint); err != nil {
		t.Fatalf("run execution that will be overwritten: %v\n%s", err, output)
	}
	fixture.setStatus(overwrittenID, "FAILED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("settle committed execution before admitting the later overwrite: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, overwrittenReservation, "charged")
	time.Sleep(1100 * time.Millisecond)
	laterPublicationID := triggerQuotaPipeline(t, deployedHandler, userID, committedProject, http.StatusAccepted)
	laterPublicationReservation := fixture.reservationID(laterPublicationID)
	if output, err := runCloudRunQuotaFixtureWorker(t, ctx, worker, workerBucket, userID, committedProject, laterPublicationID, storageEndpoint); err != nil {
		t.Fatalf("run later overwriting worker publication: %v\n%s", err, output)
	}
	fixture.setStatus(laterPublicationID, "SUCCEEDED")
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile execution-owned receipt after manifest overwrite: %v", err)
	}
	if err := proxyHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat overwritten-publication reconciliation: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, overwrittenReservation, "charged")
	assertQuotaReservation(t, ctx, fsClient, laterPublicationReservation, "charged")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 3, pipelinequota.DayKeyUTC(time.Now()))

	// A restarted handler recovers a reservation whose execution link was not
	// persisted, and refuses to associate a different owner's execution.
	restartedHandler := New(deployedQuotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	restartedHandler.metadataTokenURL = h.metadataTokenURL
	restartedHandler.SetPipelineJobURL("http://run.test/job:run")
	restartedHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	recoveryProject := "restart-recovery"
	recoveryReservation := "lwc371-recovery-reservation"
	if _, reserved, err := fsClient.ReserveQuota(ctx, recoveryReservation, userID, recoveryProject, h.pipelineLimits(), time.Now().UTC(), pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve Cloud Run restart fixture: reserved=%v err=%v", reserved, err)
	}
	recoveryExecutionID := fixture.addExecution(userID, recoveryProject, recoveryReservation, "FAILED")
	runConfirmedWorkerFailure(recoveryProject, recoveryExecutionID)
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("recover unlinked failed execution after restart: %v", err)
	}
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("repeat restart recovery: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, recoveryReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, recoveryProject, 0, pipelinequota.DayKeyUTC(time.Now()))

	ownerProject := "owner-isolation"
	ownerReservation := "lwc371-owner-isolation-reservation"
	if _, reserved, err := fsClient.ReserveQuota(ctx, ownerReservation, userID, ownerProject, h.pipelineLimits(), time.Now().UTC(), pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve owner-isolation fixture: reserved=%v err=%v", reserved, err)
	}
	fixture.addExecution("different-user", "different-project", ownerReservation, "FAILED")
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile mismatched owner execution: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, ownerReservation, "pending")
	assertQuotaState(t, ctx, fsClient, userID, ownerProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	ownerExecutionID := fixture.addExecution(userID, ownerProject, ownerReservation, "FAILED")
	runConfirmedWorkerFailure(ownerProject, ownerExecutionID)
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile matching owner execution: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, ownerReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, ownerProject, 0, pipelinequota.DayKeyUTC(time.Now()))

	// A pending shared claim blocks a later reservation. After settling that
	// failed claim, a later run can start; replaying the old settlement must not
	// overwrite the later run's credit count or cooldown timestamp.
	quotaLimits := pipelinequota.Limits{DailyLimit: 5, Cooldown: time.Second, MinNewRaw: 1}
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	quotaProject := "later-run-preserved"
	if _, reserved, err := fsClient.ReserveQuota(ctx, "same-day-old", userID, quotaProject, quotaLimits, base, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve first same-day run: reserved=%v err=%v", reserved, err)
	}
	later := base.Add(2 * time.Second)
	if _, reserved, err := fsClient.ReserveQuota(ctx, "same-day-later", userID, quotaProject, quotaLimits, later, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || reserved {
		t.Fatalf("pending same-day reservation did not block later admission: reserved=%v err=%v", reserved, err)
	}
	if settlement, err := fsClient.SettleQuotaReservation(ctx, "same-day-old", "FAILED"); err != nil || settlement != "refunded" {
		t.Fatalf("refund failed earlier same-day run: %v", err)
	}
	if _, reserved, err := fsClient.ReserveQuota(ctx, "same-day-later", userID, quotaProject, quotaLimits, later, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve later same-day run after old failure settlement: reserved=%v err=%v", reserved, err)
	}
	if _, err := fsClient.SettleQuotaReservation(ctx, "same-day-old", "FAILED"); err != nil {
		t.Fatalf("repeat earlier same-day refund: %v", err)
	}
	assertQuotaSnapshot(t, ctx, fsClient, userID, quotaProject, 1, pipelinequota.DayKeyUTC(base), later)
	if _, err := fsClient.SettleQuotaReservation(ctx, "same-day-later", "SUCCEEDED"); err != nil {
		t.Fatalf("settle later same-day run: %v", err)
	}

	quotaProject = "cross-day-preserved"
	previousDay := time.Date(2026, 10, 7, 23, 59, 0, 0, time.UTC)
	nextDay := time.Date(2026, 10, 8, 0, 1, 0, 0, time.UTC)
	if _, reserved, err := fsClient.ReserveQuota(ctx, "cross-day-old", userID, quotaProject, quotaLimits, previousDay, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve previous-day run: reserved=%v err=%v", reserved, err)
	}
	if _, reserved, err := fsClient.ReserveQuota(ctx, "cross-day-later", userID, quotaProject, quotaLimits, nextDay, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || reserved {
		t.Fatalf("pending previous-day reservation did not block next-day admission: reserved=%v err=%v", reserved, err)
	}
	if settlement, err := fsClient.SettleQuotaReservation(ctx, "cross-day-old", "FAILED"); err != nil || settlement != "refunded" {
		t.Fatalf("refund previous-day run: %v", err)
	}
	if _, reserved, err := fsClient.ReserveQuota(ctx, "cross-day-later", userID, quotaProject, quotaLimits, nextDay, pipelinequota.ModeIncremental, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve next-day run after old failure settlement: reserved=%v err=%v", reserved, err)
	}
	if _, err := fsClient.SettleQuotaReservation(ctx, "cross-day-old", "FAILED"); err != nil {
		t.Fatalf("repeat previous-day refund after next-day run: %v", err)
	}
	assertQuotaSnapshot(t, ctx, fsClient, userID, quotaProject, 1, pipelinequota.DayKeyUTC(nextDay), nextDay)

	// Transport ambiguity remains pending and has no confirmed-failure marker.
	const unknownInvokeProject = "unknown-invoke-recovery"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, unknownInvokeProject)
	unknownInvokeHandler := New(quotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	unknownInvokeHandler.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	unknownInvokeHandler.SetPipelineQuotaConfig(5, 1, 1, nil)
	unknownInvokeHandler.metadataTokenURL = "http://run.test/token"
	unknownInvokeHandler.SetPipelineJobURL("http://run.test/job:run")
	transportFailureObserved := false
	unknownInvokeHandler.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/token":
			return testHTTPResponse(http.StatusOK, `{"access_token":"test-token"}`), nil
		case "/job/executions":
			return testHTTPResponse(http.StatusOK, `{"executions":[]}`), nil
		case "/job:run":
			transportFailureObserved = true
			return nil, errors.New("synthetic transport loss after invoke")
		default:
			return testHTTPResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	unknownInvokeRecorder := httptest.NewRecorder()
	unknownInvokeContext, _ := gin.CreateTestContext(unknownInvokeRecorder)
	unknownInvokeContext.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil).WithContext(ctx)
	unknownInvokeContext.Set("userID", userID)
	unknownInvokeContext.Set("projectID", unknownInvokeProject)
	unknownInvokeHandler.PipelineRun(unknownInvokeContext)
	if unknownInvokeRecorder.Code != http.StatusAccepted {
		t.Fatalf("unknown invocation status=%d want accepted intent: %s", unknownInvokeRecorder.Code, unknownInvokeRecorder.Body.String())
	}
	var unknownIntent struct {
		IntentID     string `json:"intent_id"`
		IntentStatus string `json:"intent_status"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(unknownInvokeRecorder.Body.Bytes(), &unknownIntent); err != nil || unknownIntent.IntentID == "" ||
		unknownIntent.IntentStatus != "invocation_unknown" || unknownIntent.Message == "" {
		t.Fatalf("unknown invocation response=%+v err=%v; want durable unknown status", unknownIntent, err)
	}
	if !transportFailureObserved {
		t.Fatal("transport-unknown fixture did not exercise invoke transport failure")
	}
	unknownInvokeReservation := findQuotaReservation(t, ctx, fsClient, userID, unknownInvokeProject)
	if unknownInvokeReservation.ID != unknownIntent.IntentID || unknownInvokeReservation.ExecutionID != "" ||
		unknownInvokeReservation.IntentStatus != "invocation_unknown" || unknownInvokeReservation.SettlementStatus != "pending" {
		t.Fatalf("transport-unknown reservation=%+v, want pending with no execution", unknownInvokeReservation)
	}
	if _, err := quotaClient.WithScope(userID, unknownInvokeProject).ReadFileLimited(ctx, "cache/pipeline-"+unknownInvokeReservation.ID+".failure.json", 4<<10+1); !errors.Is(err, storeapi.ErrObjectNotExist) {
		t.Fatalf("transport-unknown failure marker err=%v, want not found", err)
	}
	assertQuotaState(t, ctx, fsClient, userID, unknownInvokeProject, 1, pipelinequota.DayKeyUTC(time.Now()))
}

func TestFirestoreReserveQuotaFullModeAndIncrementalRemainDistinct(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	project := "lwc391-quota-mode"
	database := "(default)"
	scope := fmt.Sprintf("lwc391-quota-mode-%d", time.Now().UnixNano())
	userID := "owner-" + fmt.Sprintf("%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc("lwc391-full-reservation").Delete(ctx)
		_, _ = scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, "full-no-pending")).Delete(ctx)
		_, _ = scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, "incremental-no-pending")).Delete(ctx)
		if err := client.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})

	limits := pipelinequota.Limits{DailyLimit: 2, Cooldown: time.Hour, MinNewRaw: 1}
	fullProject := "full-no-pending"
	fullReservation := "lwc391-full-reservation"
	fullSnapshot, reserved, err := client.ReserveQuota(context.Background(), fullReservation, userID, fullProject, limits, time.Now().UTC(), pipelinequota.ModeFull, false, false, 0, 0, 0)
	if err != nil || !reserved || fullSnapshot.Allowed || fullSnapshot.Reason != pipelinequota.ReasonCooldown || fullSnapshot.NewRawFiles != 0 || fullSnapshot.PendingWork != 0 {
		t.Fatalf("full transaction snapshot=%+v reserved=%v err=%v; want accepted reservation, post-reserve cooldown, and real zero counts", fullSnapshot, reserved, err)
	}
	incrementalProject := "incremental-no-pending"
	incrementalSnapshot, reserved, err := client.ReserveQuota(context.Background(), "lwc391-incremental-reservation", userID, incrementalProject, limits, time.Now().UTC(), pipelinequota.ModeIncremental, false, false, 0, 0, 0)
	if err != nil || reserved || incrementalSnapshot.Allowed || incrementalSnapshot.Reason != pipelinequota.ReasonNoNewRaw || incrementalSnapshot.NewRawFiles != 0 {
		t.Fatalf("incremental transaction snapshot=%+v reserved=%v err=%v; want no-new-raw block and zero counts", incrementalSnapshot, reserved, err)
	}
	if runs, _, _, err := client.LoadQuotaState(context.Background(), userID, incrementalProject); err != nil || runs != 0 {
		t.Fatalf("incremental no-work request mutated quota runs=%d err=%v", runs, err)
	}
}

func TestPipelineIntentFreezesInputAndRetryUsesSameSnapshot(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "llm-wiki-cloud-local"
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, projectID := "lwc415-intent-user-"+unique, "project-"+unique
	scope := "lwc415-intent-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	firstID, retryID := "pr-"+unique, "pr-retry-"+unique
	profileRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(userID + "_" + projectID).Collection("profile").Doc("state")
	t.Cleanup(func() {
		for _, ref := range []*cloudfirestore.DocumentRef{
			profileRef,
			scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, projectID)),
			scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(firstID),
			scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(retryID),
		} {
			if _, err := ref.Delete(context.Background()); err != nil && status.Code(err) != codes.NotFound {
				t.Errorf("delete owned intent fixture %s: %v", ref.Path, err)
			}
		}
		if err := client.Close(); err != nil {
			t.Errorf("close Firestore client: %v", err)
		}
	})

	requirementsV1 := []ProfileRequirement{{ID: "r1", Text: "saved Profile V1"}}
	digestV1 := profileRequirementsDigest(requirementsV1)
	if _, err := profileRef.Set(ctx, map[string]interface{}{
		"project_id": projectID, "revision": int64(1),
		"requirements": []interface{}{map[string]interface{}{"id": "r1", "text": "saved Profile V1"}},
	}); err != nil {
		t.Fatalf("seed saved Profile V1: %v", err)
	}
	limits := pipelinequota.Limits{DailyLimit: 5, Cooldown: time.Hour, MinNewRaw: 1}
	_, reserved, err := client.ReserveQuotaForPipelineIntent(ctx, firstID, userID, projectID, limits, time.Now().UTC(), pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve first intent: reserved=%t err=%v", reserved, err)
	}
	first, exists, err := client.GetPipelineIntent(ctx, userID, projectID, firstID)
	if err != nil || !exists || first.IntentStatus != "preparing" || first.ProfileRevision != 1 || first.RequirementsDigest != digestV1 {
		t.Fatalf("first intent=%+v exists=%t err=%v; want preparing V1 snapshot", first, exists, err)
	}
	if _, err := profileRef.Set(ctx, map[string]interface{}{
		"project_id": projectID, "revision": int64(2),
		"requirements":       []interface{}{map[string]interface{}{"id": "r2", "text": "newer Profile V2"}},
		"bootstrap_guidance": map[string]interface{}{"status": "pending", "profile_revision": int64(2)},
	}); err != nil {
		t.Fatalf("save newer Profile V2: %v", err)
	}
	guidanceData, guidanceRef, err := profileartifacts.EncodeBootstrapGuidance(profileartifacts.BootstrapGuidanceEnvelope{
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: 1, InputDigest: digestV1,
		ModelVersion: "controlled-provider-model", PromptVersion: "profile.bootstrap.v1", CompileGuidance: "Prepared from saved V1.",
	})
	if err != nil || len(guidanceData) == 0 {
		t.Fatalf("encode V1 run-bound guidance: ref=%+v err=%v", guidanceRef, err)
	}
	completed, err := client.CompletePipelineIntentPreparation(ctx, firstID, firstID, userID, projectID, guidanceRef)
	if err != nil || !completed {
		t.Fatalf("complete first intent preparation: completed=%t err=%v", completed, err)
	}
	updated, exists, err := client.GetPipelineIntent(ctx, userID, projectID, firstID)
	if err != nil || !exists || updated.IntentStatus != "ready" || updated.ProfileRevision != 1 || updated.RequirementsDigest != digestV1 || updated.ConsumedBootstrapGuidance == nil || *updated.ConsumedBootstrapGuidance != guidanceRef {
		t.Fatalf("prepared intent=%+v exists=%t err=%v; want V1 guidance pinned after V2 save", updated, exists, err)
	}
	profileState, err := profileRef.Get(ctx)
	if err != nil {
		t.Fatalf("read Profile after run-bound preparation: %v", err)
	}
	profileData := profileState.Data()
	revision, hasRevision := profileData["revision"].(int64)
	bootstrap, hasBootstrap := profileData["bootstrap_guidance"].(map[string]interface{})
	if !hasRevision || revision != 2 || !hasBootstrap || bootstrap["status"] != "pending" {
		t.Fatalf("preparation overwrote newer Profile state: data=%v err=%v", profileState.Data(), err)
	}

	start := make(chan struct{})
	started := make(chan bool, 2)
	startErr := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, claimed, claimErr := client.BeginPipelineIntentInvocation(ctx, firstID, firstID, userID, projectID)
			started <- claimed
			startErr <- claimErr
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		if err := <-startErr; err != nil {
			t.Fatalf("claim intent invocation: %v", err)
		}
		if <-started {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent ready intent invocation winners=%d, want exactly one", winners)
	}
	if err := client.MarkPipelineIntentInvocationUnknown(ctx, firstID, firstID, userID, projectID); err != nil {
		t.Fatalf("record ambiguous invocation: %v", err)
	}
	if err := client.FailPipelineIntent(ctx, firstID, firstID, userID, projectID, "invocation_unknown", "pipeline_invocation", "confirmed_not_started", "not started"); err != nil {
		t.Fatalf("settle confirmed non-invocation: %v", err)
	}
	if err := client.FailPipelineIntent(ctx, firstID, firstID, userID, projectID, "invocation_unknown", "pipeline_invocation", "confirmed_not_started", "not started"); err != nil {
		t.Fatalf("repeat idempotent non-invocation settlement: %v", err)
	}
	failed, exists, err := client.GetPipelineIntent(ctx, userID, projectID, firstID)
	if err != nil || !exists || failed.IntentStatus != "failed" || failed.SettlementStatus != "released" {
		t.Fatalf("confirmed failed intent=%+v exists=%t err=%v", failed, exists, err)
	}
	_, reserved, err = client.ReserveQuotaForPipelineIntentRetry(ctx, retryID, firstID, userID, projectID, limits, time.Now().UTC(), pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve retry from frozen intent: reserved=%t err=%v", reserved, err)
	}
	retried, exists, err := client.GetPipelineIntent(ctx, userID, projectID, retryID)
	if err != nil || !exists || retried.AttemptID != retryID || retried.IntentStatus != "ready" || retried.ProfileRevision != 1 || retried.RequirementsDigest != digestV1 || retried.ConsumedBootstrapGuidance == nil || *retried.ConsumedBootstrapGuidance != guidanceRef {
		t.Fatalf("retry intent=%+v exists=%t err=%v; want distinct attempt with V1 snapshot and guidance", retried, exists, err)
	}
}

func TestLWC427FullIntentRepreparesOnlyStaleActiveGuidance(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "llm-wiki-cloud-local"
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, scope := "lwc427-owner-"+unique, "lwc427-mode-"+unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	limits := pipelinequota.Limits{DailyLimit: 5, Cooldown: time.Hour, MinNewRaw: 1}
	oldRequirements := []ProfileRequirement{{ID: "r1", Text: "saved V1"}}
	newRequirements := []ProfileRequirement{{ID: "r2", Text: "saved V2 in Traditional Chinese"}}
	oldDigest := profileRequirementsDigest(oldRequirements)
	oldArtifact, oldGuidance, err := profileartifacts.EncodeGuidance(profileartifacts.GenerationGuidanceEnvelope{
		SchemaVersion: profileartifacts.GuidanceSchema, InputDigest: oldDigest,
		SourceContentGeneration: "generation-v1", CanonicalConceptsDigest: strings.Repeat("a", 64),
		ModelVersion: "controlled-v1", PromptVersion: "guidance-v1", CompileGuidance: "Use V1 guidance.",
	})
	if err != nil || len(oldArtifact) == 0 {
		t.Fatalf("encode old active guidance: ref=%+v err=%v", oldGuidance, err)
	}
	type scenario struct {
		name, projectID       string
		mode                  pipelinequota.ExecutionMode
		saved                 []ProfileRequirement
		wantStatus            string
		wantKind              string
		missingSourceRevision bool
	}
	for _, tc := range []scenario{
		{name: "full saved V2 with active V1 prepares", projectID: "full-stale-" + unique, mode: pipelinequota.ModeFull, saved: newRequirements, wantStatus: "preparing", wantKind: ""},
		{name: "full saved V1 keeps matching active", projectID: "full-match-" + unique, mode: pipelinequota.ModeFull, saved: oldRequirements, wantStatus: "ready", wantKind: "active"},
		{name: "full matching active without source revision prepares", projectID: "full-missing-source-" + unique, mode: pipelinequota.ModeFull, saved: oldRequirements, wantStatus: "preparing", wantKind: "", missingSourceRevision: true},
		{name: "incremental keeps existing active behavior", projectID: "incremental-stale-" + unique, mode: pipelinequota.ModeIncremental, saved: newRequirements, wantStatus: "ready", wantKind: "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reservationID := "intent-" + tc.projectID
			stateRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(userID + "_" + tc.projectID).Collection("profile").Doc("state")
			candidateID := "candidate-v1"
			candidateRef := stateRef.Collection("candidates").Doc(candidateID)
			reservationRef := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(reservationID)
			quotaRef := scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, tc.projectID))
			t.Cleanup(func() {
				for _, ref := range []*cloudfirestore.DocumentRef{stateRef, candidateRef, reservationRef, quotaRef} {
					if _, err := ref.Delete(context.Background()); err != nil && status.Code(err) != codes.NotFound {
						t.Errorf("delete owned full-intent fixture %s: %v", ref.Path, err)
					}
				}
			})
			savedDigest := profileRequirementsDigest(tc.saved)
			requirementsWire := make([]map[string]interface{}, len(tc.saved))
			for i, requirement := range tc.saved {
				requirementsWire[i] = map[string]interface{}{"id": requirement.ID, "text": requirement.Text}
			}
			if _, err := stateRef.Set(ctx, map[string]interface{}{
				"project_id": tc.projectID, "revision": int64(2), "requirements": requirementsWire,
				"active": map[string]interface{}{"candidate_id": candidateID, "content_generation": "generation-v1", "guidance_revision": oldGuidance.Revision},
			}); err != nil {
				t.Fatalf("seed saved Profile and active V1: %v", err)
			}
			candidate := map[string]interface{}{
				"candidate_id": candidateID, "source": "manual", "content_generation": "generation-v1",
				"requirements_digest": oldDigest, "guidance": oldGuidance,
			}
			if !tc.missingSourceRevision {
				candidate["base_revision"] = int64(1)
			}
			if _, err := candidateRef.Set(ctx, candidate); err != nil {
				t.Fatalf("seed active V1 candidate: %v", err)
			}
			_, reserved, err := client.ReserveQuotaForPipelineIntent(ctx, reservationID, userID, tc.projectID, limits, time.Now().UTC(), tc.mode, false, false, 1, 0, 0)
			if err != nil || !reserved {
				t.Fatalf("reserve %s: reserved=%t err=%v", tc.mode, reserved, err)
			}
			intent, exists, err := client.GetPipelineIntent(ctx, userID, tc.projectID, reservationID)
			if err != nil || !exists || intent.IntentStatus != tc.wantStatus || intent.ConsumedGuidanceKind != tc.wantKind {
				t.Fatalf("reserved intent=%+v exists=%t err=%v; want status=%s kind=%q", intent, exists, err, tc.wantStatus, tc.wantKind)
			}
			if intent.ProfileRequirements == nil || lwc427QuotaRequirementsDigest(intent.ProfileRequirements) != savedDigest {
				t.Fatalf("intent requirements=%+v digest=%s; want frozen digest %s", intent.ProfileRequirements, intent.RequirementsDigest, savedDigest)
			}
			if tc.mode == pipelinequota.ModeFull && savedDigest != oldDigest && (intent.ConsumedActiveGuidance != nil || intent.ConsumedGuidanceRevision != "" || intent.ActiveCandidateID != "") {
				t.Fatalf("stale active V1 leaked into full V2 reservation: %+v", intent)
			}
			if tc.mode == pipelinequota.ModeFull && intent.IntentStatus == "preparing" {
				if intent.ConsumedActiveGuidance != nil || intent.ConsumedGuidanceRevision != "" || intent.ConsumedGuidanceSourceRevision != 0 || intent.ActiveCandidateID != "" {
					t.Fatalf("invalid or unmatched active ref remained on preparing full intent: %+v", intent)
				}
				if _, claimed, err := client.BeginPipelineIntentInvocation(ctx, reservationID, reservationID, userID, tc.projectID); err != nil || claimed {
					t.Fatalf("invalid/unmatched active guidance crossed invocation fence: claimed=%t err=%v", claimed, err)
				}
				thirdRequirements := []map[string]interface{}{{"id": "r3", "text": "later V3"}}
				if _, err := stateRef.Set(ctx, map[string]interface{}{
					"project_id": tc.projectID, "revision": int64(3), "requirements": thirdRequirements,
					"bootstrap_guidance": map[string]interface{}{"status": "pending", "profile_revision": int64(3)},
				}); err != nil {
					t.Fatalf("interleave later V3 save: %v", err)
				}
				_, prepared, err := profileartifacts.EncodeBootstrapGuidance(profileartifacts.BootstrapGuidanceEnvelope{
					SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: 2, InputDigest: savedDigest,
					ModelVersion: "controlled-v2", PromptVersion: "bootstrap-v1", CompileGuidance: "Prepared from frozen V2.",
				})
				if err != nil {
					t.Fatal(err)
				}
				completed, err := client.CompletePipelineIntentPreparation(ctx, reservationID, reservationID, userID, tc.projectID, prepared)
				if err != nil || !completed {
					t.Fatalf("complete frozen V2 preparation after V3 edit: completed=%t err=%v", completed, err)
				}
				intent, exists, err = client.GetPipelineIntent(ctx, userID, tc.projectID, reservationID)
				if err != nil || !exists || intent.IntentStatus != "ready" || intent.ConsumedGuidanceKind != "bootstrap" || intent.ProfileRevision != 2 || intent.RequirementsDigest != savedDigest || intent.ConsumedBootstrapGuidance == nil || *intent.ConsumedBootstrapGuidance != prepared {
					t.Fatalf("prepared frozen V2 intent=%+v exists=%t err=%v", intent, exists, err)
				}
				profile, err := stateRef.Get(ctx)
				if err != nil || profile.Data()["revision"] != int64(3) || profile.Data()["bootstrap_guidance"].(map[string]interface{})["status"] != "pending" {
					t.Fatalf("preparation changed newer Profile state=%v err=%v", profile.Data(), err)
				}
			}
		})
	}
}

func TestLWC427RetryRepreparesLegacyFullIntentWithMissingSourceRevision(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "llm-wiki-cloud-local"
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, projectID, scope := "lwc427-retry-"+unique, "project-"+unique, "lwc427-retry-"+unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	sourceID, retryID := "old-full-"+unique, "retry-full-"+unique
	newRequirements := []ProfileRequirement{{ID: "r2", Text: "frozen V2"}}
	newDigest := profileRequirementsDigest(newRequirements)
	stale := strings.Repeat("a", 64)
	prior := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(sourceID)
	retry := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(retryID)
	quota := scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, projectID))
	t.Cleanup(func() {
		for _, ref := range []*cloudfirestore.DocumentRef{prior, retry, quota} {
			if _, err := ref.Delete(context.Background()); err != nil && status.Code(err) != codes.NotFound {
				t.Errorf("delete retry fixture %s: %v", ref.Path, err)
			}
		}
	})
	if _, err := prior.Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "attempt_id": sourceID,
		"trigger": string(pipelinequota.TriggerManual), "budget_class": "manual", "settlement_status": "released",
		"execution_id": "", "intent_status": "failed", "intent_mode": string(pipelinequota.ModeFull),
		"profile_revision": int64(2), "profile_requirements": []map[string]interface{}{{"id": "r2", "text": "frozen V2"}},
		"requirements_digest": newDigest, "consumed_guidance_kind": "active", "consumed_guidance_status": "active",
		"consumed_guidance_revision": stale, "consumed_guidance_source_revision": int64(0),
		"consumed_guidance_input_digest": newDigest, "consumed_guidance_model_version": "legacy-model",
		"consumed_guidance_prompt_version": "legacy-prompt", "consumed_guidance_schema_version": profileartifacts.GuidanceSchema,
		"consumed_active_guidance": map[string]interface{}{"Revision": stale, "InputDigest": newDigest, "ModelVersion": "legacy-model", "PromptVersion": "legacy-prompt", "SchemaVersion": profileartifacts.GuidanceSchema},
		"active_candidate_id":      "legacy-candidate", "active_content_generation": "generation-v1",
	}); err != nil {
		t.Fatalf("seed legacy failed full intent: %v", err)
	}
	_, reserved, err := client.ReserveQuotaForPipelineIntentRetry(ctx, retryID, sourceID, userID, projectID,
		pipelinequota.Limits{DailyLimit: 5, Cooldown: time.Hour, MinNewRaw: 1}, time.Now().UTC(),
		pipelinequota.ModeFull, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve retry: reserved=%t err=%v", reserved, err)
	}
	intent, exists, err := client.GetPipelineIntent(ctx, userID, projectID, retryID)
	if err != nil || !exists || intent.IntentStatus != "preparing" || intent.ProfileRevision != 2 || intent.RequirementsDigest != newDigest ||
		lwc427QuotaRequirementsDigest(intent.ProfileRequirements) != newDigest || intent.ConsumedGuidanceKind != "" || intent.ConsumedActiveGuidance != nil || intent.ConsumedGuidanceRevision != "" {
		t.Fatalf("retry intent=%+v exists=%t err=%v; want preparing from legacy frozen V2 without stale V1", intent, exists, err)
	}
	if _, claimed, err := client.BeginPipelineIntentInvocation(ctx, retryID, retryID, userID, projectID); err != nil || claimed {
		t.Fatalf("legacy invalid-source retry crossed invocation fence: claimed=%t err=%v", claimed, err)
	}
}

func lwc427QuotaRequirementsDigest(requirements []scopedfirestore.PipelineProfileRequirement) string {
	converted := make([]ProfileRequirement, len(requirements))
	for i, requirement := range requirements {
		converted[i] = ProfileRequirement{ID: requirement.ID, Text: requirement.Text}
	}
	return profileRequirementsDigest(converted)
}

func TestScheduledPipelineSweepAdmissionAndReconciliationEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project  = "llm-wiki-cloud"
		database = "(default)"
		userID   = "lwc413-scheduled-owner"
	)
	scope := fmt.Sprintf("lwc413-scheduled-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean scheduled pipeline emulator scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})

	projectCollection := scopedfirestore.Collection(fsClient.Raw(), "projects")
	batch := fsClient.Raw().Batch()
	for index := 0; index < scheduledPipelinePageSize; index++ {
		batch.Set(projectCollection.Doc(fmt.Sprintf("a%03d", index)), map[string]interface{}{"status": "suspended"})
	}
	if _, err := batch.Commit(ctx); err != nil {
		t.Fatalf("seed first page of non-ready projects: %v", err)
	}
	projectIDs := []string{"a-source", "b-no-work", "c-unknown"}
	for _, projectID := range projectIDs {
		_, err := projectCollection.Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
			"user_id": userID, "project_id": projectID, "status": "ready",
		})
		if err != nil {
			t.Fatalf("seed ready project %s: %v", projectID, err)
		}
	}
	localStore := localfs.New(t.TempDir())
	for _, projectID := range []string{"a-source", "c-unknown"} {
		if _, err := localStore.WithScope(userID, projectID).WriteBytes(ctx, []byte("pending source"), "raw/source.md"); err != nil {
			t.Fatalf("seed local source %s: %v", projectID, err)
		}
	}
	now := time.Date(2026, 10, 20, 23, 55, 0, 0, time.UTC)
	_, err = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, "a-source")).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": "a-source", "runs_today": int64(2),
		"day_key": pipelinequota.DayKeyUTC(now), "last_run_at": now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed exhausted manual quota: %v", err)
	}

	fixture := newQuotaCloudRunFixture()
	fixture.projectsWithInvokeError = map[string]bool{}
	fixture.loseResponseProjects["c-unknown"] = true
	h := New(localStore, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineJobURL("http://run.test/job:run")
	h.SetPipelineQuotaConfig(2, 3600, 1, nil)
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}

	first, err := h.RunScheduledPipelineSweep(ctx, now)
	if err != nil || first.Scanned != scheduledPipelinePageSize+len(projectIDs) || first.Accepted != 1 || first.Skipped != 1 || first.Unavailable != 1 {
		t.Fatalf("first sweep=%+v err=%v; want all pages scanned, one accepted, one no-work skip, and one unknown invoke", first, err)
	}
	fixture.mu.Lock()
	invokeCount := fixture.next
	requests := append([]map[string]string(nil), fixture.requests...)
	fixture.mu.Unlock()
	if invokeCount != 2 || len(requests) != 2 || requests[0][pipelineStageEnvName] != pipelineStageFull || requests[0]["CLEAN_REBUILD"] != "" || requests[0]["PIPELINE_RESERVATION_ID"] == "" {
		t.Fatalf("Cloud Run invocation count/env = %d/%v; want two provider requests with incremental (no clean rebuild) reservation", invokeCount, requests)
	}
	manualRuns, manualDay, _, err := fsClient.LoadQuotaState(ctx, userID, "a-source")
	if err != nil || manualRuns != 2 || manualDay != pipelinequota.DayKeyUTC(now) {
		t.Fatalf("scheduled start changed manual daily bucket to %d/%q err=%v", manualRuns, manualDay, err)
	}
	statusNoWork, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, "b-no-work", now)
	if err != nil || statusNoWork.SkipReason != "no_work" || statusNoWork.BudgetUsed {
		t.Fatalf("no-work check=%+v err=%v; want no entitlement consumed", statusNoWork, err)
	}

	firstExecutionID := "fixture-execution-1"
	firstReservationID := fixture.reservationID(firstExecutionID)
	firstReservation, exists, err := fsClient.GetQuotaReservation(ctx, firstReservationID)
	if err != nil || !exists || firstReservation.Trigger != pipelinequota.TriggerScheduledDaily || firstReservation.BudgetClass != "scheduled_daily" || firstReservation.ExecutionID != firstExecutionID {
		t.Fatalf("scheduled reservation=%+v exists=%t err=%v", firstReservation, exists, err)
	}
	fixture.setStatus(firstExecutionID, "FAILED")
	settlement, err := fsClient.SettleQuotaReservation(ctx, firstReservationID, "FAILED")
	if err != nil || settlement != "charged" {
		t.Fatalf("scheduled failed attempt settlement=%q err=%v; want charged daily entitlement", settlement, err)
	}
	failedStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, "a-source", now)
	if err != nil || !failedStatus.BudgetUsed || failedStatus.LastResult != "FAILED" || failedStatus.LastExecutionID != firstExecutionID {
		t.Fatalf("failed scheduled status=%+v err=%v", failedStatus, err)
	}

	if _, err := localStore.WithScope(userID, "b-no-work").WriteBytes(ctx, []byte("now pending"), "raw/new.md"); err != nil {
		t.Fatalf("add raw after no-work skip: %v", err)
	}
	second, err := h.RunScheduledPipelineSweep(ctx, now.Add(3*time.Minute))
	if err != nil || second.Accepted != 1 || second.Skipped != 2 || second.Unavailable != 0 {
		t.Fatalf("second sweep=%+v err=%v; want only the newly eligible project invoked", second, err)
	}
	fixture.mu.Lock()
	invokeCount = fixture.next
	fixture.mu.Unlock()
	if invokeCount != 3 {
		t.Fatalf("same-day failed and unknown attempts were reinvoked; provider invocation count=%d, want 3 total", invokeCount)
	}
	failedStatus, err = fsClient.LoadScheduledPipelineStatus(ctx, userID, "a-source", now)
	if err != nil || failedStatus.SkipReason != "daily_entitlement_used" {
		t.Fatalf("same-day failed attempt status=%+v err=%v", failedStatus, err)
	}
	unknownReservationID := fixture.reservationID("fixture-execution-2")
	unknownReservation, exists, err := fsClient.GetQuotaReservation(ctx, unknownReservationID)
	if err != nil || !exists || unknownReservation.ExecutionID != "" || unknownReservation.SettlementStatus != "pending" {
		t.Fatalf("lost-response reservation=%+v exists=%t err=%v", unknownReservation, exists, err)
	}
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile accepted invocation with lost response: %v", err)
	}
	unknownReservation, exists, err = fsClient.GetQuotaReservation(ctx, unknownReservationID)
	if err != nil || !exists || unknownReservation.ExecutionID != "fixture-execution-2" || unknownReservation.SettlementStatus != "pending" {
		t.Fatalf("reconciled lost-response reservation=%+v exists=%t err=%v", unknownReservation, exists, err)
	}

	dayTwo := now.Add(24 * time.Hour)
	third, err := h.RunScheduledPipelineSweep(ctx, dayTwo)
	if err != nil || third.Accepted != 1 || third.Skipped != 2 {
		t.Fatalf("UTC rollover sweep=%+v err=%v; want one new-day attempt and two older running/unknown claims skipped", third, err)
	}
	fixture.mu.Lock()
	invokeCount = fixture.next
	fixture.mu.Unlock()
	if invokeCount != 4 {
		t.Fatalf("UTC rollover invoked %d times; want one current-day grant after prior failed/unknown attempts", invokeCount)
	}
	unknownStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, "c-unknown", dayTwo)
	if err != nil || unknownStatus.SkipReason != "busy" || unknownStatus.BudgetUsed {
		t.Fatalf("cross-midnight unknown claim status=%+v err=%v", unknownStatus, err)
	}
	manualRuns, _, _, err = fsClient.LoadQuotaState(ctx, userID, "a-source")
	if err != nil || manualRuns != 2 {
		t.Fatalf("scheduled reservations changed manual daily usage after rollover: %d err=%v", manualRuns, err)
	}

	raceProject := "d-admission-race"
	start := make(chan struct{})
	type reserveResult struct {
		snapshot pipelinequota.Snapshot
		reserved bool
		err      error
	}
	results := make(chan reserveResult, 2)
	go func() {
		<-start
		snapshot, reserved, err := fsClient.ReserveQuota(ctx, "manual-race-reservation", userID, raceProject, h.pipelineLimits(), dayTwo, pipelinequota.ModeFull, false, false, 1, 0, 0)
		results <- reserveResult{snapshot: snapshot, reserved: reserved, err: err}
	}()
	go func() {
		<-start
		snapshot, reserved, err := fsClient.ReserveScheduledQuota(ctx, scopedfirestore.ScheduledPipelineBudgetID(userID, raceProject, pipelinequota.DayKeyUTC(dayTwo)), userID, raceProject, h.pipelineLimits(), dayTwo, pipelinequota.ModeIncremental, false, false, 1, 0, 0)
		results <- reserveResult{snapshot: snapshot, reserved: reserved, err: err}
	}()
	close(start)
	firstRace, secondRace := <-results, <-results
	if firstRace.err != nil || secondRace.err != nil || firstRace.reserved == secondRace.reserved {
		t.Fatalf("manual/full versus scheduled transaction race: first=%+v second=%+v; want exactly one winner", firstRace, secondRace)
	}
	if active, err := fsClient.PipelineAdmissionActive(ctx, userID, raceProject); err != nil || !active {
		t.Fatalf("shared admission claim active=%t err=%v after exactly one race winner", active, err)
	}

	manualProject := "e-manual-refund-control"
	previousLastRun := dayTwo.Add(-3 * time.Hour)
	_, err = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, manualProject)).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": manualProject, "runs_today": int64(1),
		"day_key": pipelinequota.DayKeyUTC(dayTwo), "last_run_at": previousLastRun,
	})
	if err != nil {
		t.Fatalf("seed manual refund control: %v", err)
	}
	manualSnapshot, manualReserved, err := fsClient.ReserveQuota(ctx, "manual-refund-control", userID, manualProject, h.pipelineLimits(), dayTwo, pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !manualReserved {
		t.Fatalf("manual control reservation=%+v reserved=%t err=%v", manualSnapshot, manualReserved, err)
	}
	manualSettlement, err := fsClient.SettleQuotaReservation(ctx, "manual-refund-control", "FAILED")
	manualRuns, _, lastRunAt, loadErr := fsClient.LoadQuotaState(ctx, userID, manualProject)
	if err != nil || loadErr != nil || manualSettlement != "refunded" || manualRuns != 1 || !lastRunAt.Equal(previousLastRun) {
		t.Fatalf("manual failure regression settlement=%q runs=%d lastRun=%s err=%v loadErr=%v", manualSettlement, manualRuns, lastRunAt, err, loadErr)
	}
}

func TestScheduledPipelineEntrypointRejectsUnauthenticatedCaller(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	const audience = "https://bff.example"
	const serviceAccount = "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com"
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", audience, serviceAccount)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/schedule", strings.NewReader(`{"user_id":"attacker","trigger":"scheduled_daily"}`))
	h.ScheduledPipeline(ctx)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated scheduled call status=%d body=%s; want 401", recorder.Code, recorder.Body.String())
	}

	validatorCalled := false
	h.SetScheduledPipelineIdentityValidator(func(_ context.Context, _, gotAudience string) (*idtoken.Payload, error) {
		validatorCalled = true
		if gotAudience != audience {
			t.Errorf("token validator audience=%q, want %q", gotAudience, audience)
		}
		return &idtoken.Payload{Audience: audience, Claims: map[string]interface{}{
			"email": "untrusted@llm-wiki-cloud.iam.gserviceaccount.com", "email_verified": true,
		}}, nil
	})
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/schedule", nil)
	ctx.Request.Header.Set("Authorization", "Bearer fixture-token")
	h.ScheduledPipeline(ctx)
	if !validatorCalled || recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong scheduler identity called=%t status=%d body=%s; want verifier call and 401", validatorCalled, recorder.Code, recorder.Body.String())
	}
}

func requireLoopbackEmulator(t *testing.T, envName string) bool {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return false
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
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip == nil || !ip.IsLoopback() {
		t.Fatalf("%s must point to a loopback emulator for this test", envName)
	}
	return true
}

func seedQuotaRaw(t *testing.T, ctx context.Context, client *gcs.Client, rawClient *cloudstorage.Client, bucket, scope, userID, projectID string) {
	t.Helper()
	if _, err := client.WithScope(userID, projectID).WriteBytes(ctx, []byte("synthetic raw fixture"), "raw/source.md"); err != nil {
		t.Fatalf("seed raw fixture for %s: %v", projectID, err)
	}
	objectName := "local_scopes/" + scope + "/users/" + userID + "/projects/" + projectID + "/raw/source.md"
	reader, rawReadErr := rawClient.Bucket(bucket).Object(objectName).NewReader(ctx)
	if rawReadErr != nil {
		t.Fatalf("raw SDK read fixture for %s (%s): %v", projectID, objectName, rawReadErr)
	}
	if _, err := io.ReadAll(reader); err != nil {
		_ = reader.Close()
		t.Fatalf("read raw SDK fixture body for %s: %v", projectID, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close raw SDK fixture body for %s: %v", projectID, err)
	}
	if _, err := client.WithScope(userID, projectID).ReadRaw(ctx, "source.md"); err != nil {
		objects := rawClient.Bucket(bucket).Objects(ctx, &cloudstorage.Query{Prefix: "local_scopes/" + scope + "/"})
		var names []string
		for {
			attrs, listErr := objects.Next()
			if listErr == iterator.Done {
				break
			}
			if listErr != nil {
				names = append(names, listErr.Error())
				break
			}
			names = append(names, attrs.Name)
		}
		t.Fatalf("read raw fixture for %s: %v (scope objects=%v)", projectID, err, names)
	}
}

func buildLocalCloudPipelineFixtureWorker(t *testing.T, ctx context.Context) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve BFF module path for Cloud Run worker fixture")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../.."))
	worker := filepath.Join(t.TempDir(), "olw_worker")
	build := exec.CommandContext(ctx, "go", "build", "-tags", "lwc_local_pipeline_fixture", "-o", worker, "./cmd/olw_worker")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build deployed-mode Cloud Run worker fixture: %v\n%s", err, output)
	}
	return worker
}

func runCloudRunQuotaFixtureWorker(t *testing.T, ctx context.Context, worker, bucket, userID, projectID, executionID, storageEndpoint string) ([]byte, error) {
	t.Helper()
	command := exec.CommandContext(ctx, worker,
		"--bucket", bucket, "--user-id", userID, "--project-id", projectID,
		"--execution-id", executionID, "run", `[["run","--auto-approve"]]`)
	command.Env = []string{
		"GOOGLE_CLOUD_PROJECT=llm-wiki-cloud", "GCP_PROJECT=llm-wiki-cloud",
		"STORAGE_EMULATOR_HOST=" + storageEndpoint, "LOCAL_CLOUD_SCOPE=", "TMPDIR=" + os.TempDir(),
	}
	output, err := command.CombinedOutput()
	return output, err
}

func deleteQuotaFixtureBucket(ctx context.Context, client *cloudstorage.Client, bucket string) error {
	objects := client.Bucket(bucket).Objects(ctx, &cloudstorage.Query{})
	var failures []error
	for {
		attrs, err := objects.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := client.Bucket(bucket).Object(attrs.Name).Generation(attrs.Generation).Delete(ctx); err != nil && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
			failures = append(failures, err)
		}
	}
	if err := client.Bucket(bucket).Delete(ctx); err != nil && !errors.Is(err, cloudstorage.ErrBucketNotExist) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

type quotaStorageProxy struct {
	server  *httptest.Server
	proxy   *httputil.ReverseProxy
	mu      sync.RWMutex
	blocked bool
}

func newQuotaStorageProxy(t *testing.T, endpoint string) *quotaStorageProxy {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		t.Fatalf("parse loopback Storage emulator endpoint %q", endpoint)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	gate := &quotaStorageProxy{proxy: proxy}
	gate.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate.mu.RLock()
		blocked := gate.blocked
		gate.mu.RUnlock()
		if blocked {
			http.Error(w, "synthetic temporary Storage outage", http.StatusServiceUnavailable)
			return
		}
		gate.proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { gate.server.Close() })
	return gate
}

func (p *quotaStorageProxy) URL() string { return p.server.URL }

func (p *quotaStorageProxy) SetBlocked(blocked bool) {
	p.mu.Lock()
	p.blocked = blocked
	p.mu.Unlock()
}

func triggerQuotaPipeline(t *testing.T, h *Handler, userID, projectID string, wantStatus int) string {
	t.Helper()
	result := triggerQuotaPipelineIntent(t, h, userID, projectID)
	if wantStatus != http.StatusAccepted {
		t.Fatalf("triggerQuotaPipeline only accepts status 202, got expected %d", wantStatus)
	}
	if result.ExecutionID == "" {
		t.Fatalf("pipeline trigger for %s returned no execution ID: intent=%+v", projectID, result)
	}
	return result.ExecutionID
}

type pipelineTriggerResult struct {
	Status       string `json:"status"`
	ExecutionID  string `json:"execution_id"`
	IntentID     string `json:"intent_id"`
	IntentStatus string `json:"intent_status"`
	Message      string `json:"message"`
}

func triggerQuotaPipelineIntent(t *testing.T, h *Handler, userID, projectID string) pipelineTriggerResult {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil)
	c.Set("userID", userID)
	c.Set("projectID", projectID)
	h.PipelineRun(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("pipeline trigger for %s returned %d, want %d: %s", projectID, recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var payload pipelineTriggerResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode Pipeline trigger response for %s: %v (%s)", projectID, err, recorder.Body.String())
	}
	return payload
}

func assertQuotaPipelineStatus(t *testing.T, h *Handler, userID, projectID, executionID, wantState, wantReason, wantSettlement string, wantRuns int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/pipeline/status?execution_id="+url.QueryEscape(executionID), nil)
	c.Set("userID", userID)
	c.Set("projectID", projectID)
	h.PipelineStatus(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pipeline status for %s returned %d: %s", projectID, recorder.Code, recorder.Body.String())
	}
	var payload struct {
		LastExecution *handler.PipelineExecutionResponse `json:"last_execution"`
		Quota         *pipelinequota.Snapshot            `json:"quota"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode pipeline status: %v; body=%s", err, recorder.Body.String())
	}
	if payload.LastExecution == nil || payload.LastExecution.Status != wantState || payload.LastExecution.QuotaSettlement != wantSettlement {
		t.Fatalf("last execution=%+v; want state=%s settlement=%s", payload.LastExecution, wantState, wantSettlement)
	}
	if wantReason != "" && payload.LastExecution.FailureReason != wantReason {
		t.Fatalf("failure reason=%q, want %q; status body=%s", payload.LastExecution.FailureReason, wantReason, recorder.Body.String())
	}
	if wantState == "FAILED" && payload.LastExecution.DiagnosticState != "available" {
		if wantReason != "" || payload.LastExecution.DiagnosticState != "unavailable" {
			t.Fatalf("diagnostic state=%q, want unavailable when no failure artifact exists", payload.LastExecution.DiagnosticState)
		}
	}
	if payload.Quota == nil || payload.Quota.RunsToday != wantRuns {
		t.Fatalf("quota=%+v, want runs_today=%d", payload.Quota, wantRuns)
	}
}

func assertQuotaState(t *testing.T, ctx context.Context, client *scopedfirestore.Client, userID, projectID string, wantRuns int, wantDay string) {
	t.Helper()
	runs, day, _, err := client.LoadQuotaState(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("load quota state for %s: %v", projectID, err)
	}
	if runs != wantRuns || day != wantDay {
		t.Fatalf("quota state for %s = runs:%d day:%q, want runs:%d day:%q", projectID, runs, day, wantRuns, wantDay)
	}
}

func assertQuotaSnapshot(t *testing.T, ctx context.Context, client *scopedfirestore.Client, userID, projectID string, wantRuns int, wantDay string, wantLastRun time.Time) {
	t.Helper()
	runs, day, lastRun, err := client.LoadQuotaState(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("load quota snapshot for %s: %v", projectID, err)
	}
	if runs != wantRuns || day != wantDay || !lastRun.Equal(wantLastRun) {
		t.Fatalf("quota snapshot for %s = runs:%d day:%q last:%s, want runs:%d day:%q last:%s", projectID, runs, day, lastRun, wantRuns, wantDay, wantLastRun)
	}
}

func assertQuotaReservation(t *testing.T, ctx context.Context, client *scopedfirestore.Client, reservationID, want string) {
	t.Helper()
	reservation, exists, err := client.GetQuotaReservation(ctx, reservationID)
	if err != nil || !exists {
		t.Fatalf("get reservation %s: exists=%v err=%v", reservationID, exists, err)
	}
	if reservation.SettlementStatus != want {
		t.Fatalf("reservation %s settlement=%q, want %q", reservationID, reservation.SettlementStatus, want)
	}
}

func assertQuotaProjectReservation(t *testing.T, ctx context.Context, client *scopedfirestore.Client, projectID, want string) {
	t.Helper()
	docs, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Where("project_id", "==", projectID).Documents(ctx).GetAll()
	if err != nil || len(docs) != 1 {
		t.Fatalf("reservations for %s: count=%d err=%v, want one", projectID, len(docs), err)
	}
	if got := docs[0].Data()["settlement_status"]; got != want {
		t.Fatalf("reservation for %s settlement=%v, want %q", projectID, got, want)
	}
}

func findQuotaReservation(t *testing.T, ctx context.Context, client *scopedfirestore.Client, userID, projectID string) scopedfirestore.QuotaReservation {
	t.Helper()
	reservations, err := client.ListPendingQuotaReservations(ctx)
	if err != nil {
		t.Fatalf("list pending quota reservations: %v", err)
	}
	for _, reservation := range reservations {
		if reservation.UserID == userID && reservation.ProjectID == projectID {
			return reservation
		}
	}
	t.Fatalf("no pending reservation for %s/%s", userID, projectID)
	return scopedfirestore.QuotaReservation{}
}

type expireFirstQuotaSettlement struct {
	pipelineQuotaStore
	contextErr error
}

func (s *expireFirstQuotaSettlement) SettleQuotaReservation(ctx context.Context, reservationID, executionStatus string) (string, error) {
	if s.contextErr == nil {
		<-ctx.Done()
		s.contextErr = ctx.Err()
		return "", s.contextErr
	}
	return s.pipelineQuotaStore.SettleQuotaReservation(ctx, reservationID, executionStatus)
}

func assertQuotaProjectHasNoReservation(t *testing.T, ctx context.Context, client *scopedfirestore.Client, projectID string) {
	t.Helper()
	docs, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Where("project_id", "==", projectID).Documents(ctx).GetAll()
	if err != nil || len(docs) != 0 {
		t.Fatalf("reservations for %s: count=%d err=%v, want none", projectID, len(docs), err)
	}
}

func assertAdminPipelineReservation(t *testing.T, ctx context.Context, client *scopedfirestore.Client, projectID string) {
	t.Helper()
	docs, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Where("project_id", "==", projectID).Documents(ctx).GetAll()
	if err != nil || len(docs) != 1 {
		t.Fatalf("admin reservations for %s: count=%d err=%v, want one shared admission claim", projectID, len(docs), err)
	}
	data := docs[0].Data()
	if got := data["trigger"]; got != string(pipelinequota.TriggerAdmin) {
		t.Fatalf("admin reservation trigger=%v, want %q", got, pipelinequota.TriggerAdmin)
	}
	if got := data["budget_class"]; got != "none" {
		t.Fatalf("admin reservation budget_class=%v, want none", got)
	}
	if got, _ := data["execution_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatal("admin admission reservation has no linked execution ID")
	}
}

type quotaCloudRunFixture struct {
	mu                      sync.Mutex
	next                    int
	executions              map[string]*quotaCloudRunExecution
	requests                []map[string]string
	projectsWithInvokeError map[string]bool
	loseResponseProjects    map[string]bool
	executionUnavailable    map[string]bool
}

type quotaCloudRunExecution struct {
	ID            string
	UserID        string
	ProjectID     string
	ReservationID string
	Status        string
}

func newQuotaCloudRunFixture() *quotaCloudRunFixture {
	return &quotaCloudRunFixture{
		executions:              make(map[string]*quotaCloudRunExecution),
		projectsWithInvokeError: map[string]bool{"invoke-failure": true},
		loseResponseProjects:    make(map[string]bool),
		executionUnavailable:    make(map[string]bool),
	}
}

func (f *quotaCloudRunFixture) reservationID(executionID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executions[executionID].ReservationID
}

func (f *quotaCloudRunFixture) setStatus(executionID, state string) {
	f.mu.Lock()
	f.executions[executionID].Status = state
	f.mu.Unlock()
}

func (f *quotaCloudRunFixture) setExecutionUnavailable(executionID string, unavailable bool) {
	f.mu.Lock()
	f.executionUnavailable[executionID] = unavailable
	f.mu.Unlock()
}

func (f *quotaCloudRunFixture) addExecution(userID, projectID, reservationID, state string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	executionID := fmt.Sprintf("fixture-execution-%d", f.next)
	f.executions[executionID] = &quotaCloudRunExecution{ID: executionID, UserID: userID, ProjectID: projectID, ReservationID: reservationID, Status: state}
	return executionID
}

func (f *quotaCloudRunFixture) roundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/token" {
		return testHTTPResponse(http.StatusOK, `{"access_token":"loopback-test-token"}`), nil
	}
	if request.Method == http.MethodPost && request.URL.Path == "/job:run" {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Overrides struct {
				Containers []struct {
					Env []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containerOverrides"`
			} `json:"overrides"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || len(payload.Overrides.Containers) == 0 {
			return testHTTPResponse(http.StatusBadRequest, `{"error":"invalid fixture request"}`), nil
		}
		env := make(map[string]string)
		for _, variable := range payload.Overrides.Containers[0].Env {
			env[variable.Name] = variable.Value
		}
		f.mu.Lock()
		f.requests = append(f.requests, env)
		f.mu.Unlock()
		if f.projectsWithInvokeError[env["PROJECT_ID"]] {
			return testHTTPResponse(http.StatusForbidden, `{"error":"fixture invoke rejected"}`), nil
		}
		executionID := f.addExecution(env["USER_ID"], env["PROJECT_ID"], env["PIPELINE_RESERVATION_ID"], "RUNNING")
		if f.loseResponseProjects[env["PROJECT_ID"]] {
			return nil, errors.New("fixture accepted invocation but lost response")
		}
		return testHTTPResponse(http.StatusOK, fmt.Sprintf(`{"metadata":{"execution":"projects/test/locations/local/jobs/job/executions/%s"}}`, executionID)), nil
	}
	if request.Method == http.MethodGet && request.URL.Path == "/job/executions" {
		f.mu.Lock()
		defer f.mu.Unlock()
		items := make([]map[string]any, 0, len(f.executions))
		for _, execution := range f.executions {
			items = append(items, f.apiExecution(execution))
		}
		data, _ := json.Marshal(map[string]any{"executions": items})
		return testHTTPResponse(http.StatusOK, string(data)), nil
	}
	if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/job/executions/") {
		executionID := strings.TrimPrefix(request.URL.Path, "/job/executions/")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.executionUnavailable[executionID] {
			return testHTTPResponse(http.StatusServiceUnavailable, `{"error":"fixture status unavailable"}`), nil
		}
		execution := f.executions[executionID]
		if execution == nil {
			return testHTTPResponse(http.StatusNotFound, `{"error":"not found"}`), nil
		}
		data, _ := json.Marshal(f.apiExecution(execution))
		return testHTTPResponse(http.StatusOK, string(data)), nil
	}
	return testHTTPResponse(http.StatusNotFound, `{"error":"unknown fixture endpoint"}`), nil
}

func (f *quotaCloudRunFixture) apiExecution(execution *quotaCloudRunExecution) map[string]any {
	return map[string]any{
		"name":             "projects/test/locations/local/jobs/job/executions/" + execution.ID,
		"completionStatus": execution.Status,
		"template": map[string]any{"containers": []any{map[string]any{"env": []any{
			map[string]string{"name": "USER_ID", "value": execution.UserID},
			map[string]string{"name": "PROJECT_ID", "value": execution.ProjectID},
			map[string]string{"name": "TASK_TYPE", "value": "pipeline"},
			map[string]string{"name": "PIPELINE_RESERVATION_ID", "value": execution.ReservationID},
		}}}},
	}
}

func deletePipelineQuotaFirestoreScope(ctx context.Context, client *cloudfirestore.Client, scope string) error {
	root := client.Collection("local_scopes").Doc(scope)
	var failures []error
	collections := []string{"pipeline_quota", "pipeline_quota_reservations", "pipeline_scheduled_daily", "pipeline_scheduled_status", "projects", "locks", "executions"}
	for _, name := range collections {
		docs, err := root.Collection(name).Documents(ctx).GetAll()
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
	for _, name := range collections {
		docs, err := root.Collection(name).Documents(ctx).GetAll()
		if err != nil {
			failures = append(failures, err)
		} else if len(docs) != 0 {
			failures = append(failures, fmt.Errorf("Firestore scope retained documents in %s", name))
		}
	}
	return joinErrors(failures)
}

func deletePipelineQuotaObjectScope(ctx context.Context, client *cloudstorage.Client, bucket, scope string) error {
	objects := client.Bucket(bucket).Objects(ctx, &cloudstorage.Query{Prefix: "local_scopes/" + scope + "/"})
	var failures []error
	for {
		attrs, err := objects.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			failures = append(failures, err)
			break
		}
		if err := client.Bucket(bucket).Object(attrs.Name).Delete(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors(failures)
}

func joinErrors(failures []error) error {
	return errors.Join(failures...)
}
