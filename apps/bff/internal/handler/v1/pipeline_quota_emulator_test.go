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
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/search"
	storeapi "github.com/rayer/llm-wiki-bff/internal/storage"
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
	// reservation in the request path.
	invokeProject := "invoke-failure"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, invokeProject)
	triggerQuotaPipeline(t, h, userID, invokeProject, http.StatusInternalServerError)
	assertQuotaState(t, ctx, fsClient, userID, invokeProject, 0, pipelinequota.DayKeyUTC(time.Now()))
	assertQuotaProjectReservation(t, ctx, fsClient, invokeProject, "refunded")

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
	assertQuotaReservation(t, ctx, fsClient, definiteInvokeReservation.ID, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, definiteInvokeProject, 0, pipelinequota.DayKeyUTC(time.Now()))
	retryDefiniteInvokeID := triggerQuotaPipeline(t, restartedInvokeHandler, userID, definiteInvokeProject, http.StatusAccepted)
	if fixture.reservationID(retryDefiniteInvokeID) == "" {
		t.Fatal("same-input retry after definite invocation refund has no reservation")
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
	assertQuotaProjectHasNoReservation(t, ctx, fsClient, adminProject)

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
	assertQuotaPipelineStatus(t, proxyHandler, userID, committedProject, committedID, "SUCCEEDED", "", "charged", 1)

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
	restartedHandler := New(quotaClient, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	restartedHandler.metadataTokenURL = h.metadataTokenURL
	restartedHandler.SetPipelineJobURL("http://run.test/job:run")
	restartedHandler.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	recoveryProject := "restart-recovery"
	recoveryReservation := "lwc371-recovery-reservation"
	if _, reserved, err := fsClient.ReserveQuota(ctx, recoveryReservation, userID, recoveryProject, h.pipelineLimits(), time.Now().UTC(), false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve Cloud Run restart fixture: reserved=%v err=%v", reserved, err)
	}
	fixture.addExecution(userID, recoveryProject, recoveryReservation, "FAILED")
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
	if _, reserved, err := fsClient.ReserveQuota(ctx, ownerReservation, userID, ownerProject, h.pipelineLimits(), time.Now().UTC(), false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve owner-isolation fixture: reserved=%v err=%v", reserved, err)
	}
	fixture.addExecution("different-user", "different-project", ownerReservation, "FAILED")
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile mismatched owner execution: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, ownerReservation, "pending")
	assertQuotaState(t, ctx, fsClient, userID, ownerProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	fixture.addExecution(userID, ownerProject, ownerReservation, "FAILED")
	if err := restartedHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile matching owner execution: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, ownerReservation, "refunded")
	assertQuotaState(t, ctx, fsClient, userID, ownerProject, 0, pipelinequota.DayKeyUTC(time.Now()))

	// A failed reservation settled after later same-day and cross-day runs must
	// never overwrite the later run's credit count or cooldown timestamp.
	quotaLimits := pipelinequota.Limits{DailyLimit: 5, Cooldown: time.Second, MinNewRaw: 1}
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	quotaProject := "later-run-preserved"
	if _, reserved, err := fsClient.ReserveQuota(ctx, "same-day-old", userID, quotaProject, quotaLimits, base, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve first same-day run: reserved=%v err=%v", reserved, err)
	}
	later := base.Add(2 * time.Second)
	if _, reserved, err := fsClient.ReserveQuota(ctx, "same-day-later", userID, quotaProject, quotaLimits, later, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve later same-day run: reserved=%v err=%v", reserved, err)
	}
	if _, err := fsClient.SettleQuotaReservation(ctx, "same-day-old", "FAILED"); err != nil {
		t.Fatalf("refund failed earlier same-day run: %v", err)
	}
	assertQuotaSnapshot(t, ctx, fsClient, userID, quotaProject, 1, pipelinequota.DayKeyUTC(base), later)
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
	if _, reserved, err := fsClient.ReserveQuota(ctx, "cross-day-old", userID, quotaProject, quotaLimits, previousDay, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve previous-day run: reserved=%v err=%v", reserved, err)
	}
	if _, reserved, err := fsClient.ReserveQuota(ctx, "cross-day-later", userID, quotaProject, quotaLimits, nextDay, false, false, 1, 0, 0); err != nil || !reserved {
		t.Fatalf("reserve next-day run: reserved=%v err=%v", reserved, err)
	}
	if _, err := fsClient.SettleQuotaReservation(ctx, "cross-day-old", "FAILED"); err != nil {
		t.Fatalf("refund previous-day run: %v", err)
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
	if unknownInvokeRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("unknown invocation status=%d want 500: %s", unknownInvokeRecorder.Code, unknownInvokeRecorder.Body.String())
	}
	if !transportFailureObserved {
		t.Fatal("transport-unknown fixture did not exercise invoke transport failure")
	}
	unknownInvokeReservation := findQuotaReservation(t, ctx, fsClient, userID, unknownInvokeProject)
	if unknownInvokeReservation.ExecutionID != "" || unknownInvokeReservation.SettlementStatus != "pending" {
		t.Fatalf("transport-unknown reservation=%+v, want pending with no execution", unknownInvokeReservation)
	}
	if _, err := quotaClient.WithScope(userID, unknownInvokeProject).ReadFileLimited(ctx, "cache/pipeline-"+unknownInvokeReservation.ID+".failure.json", 4<<10+1); !errors.Is(err, storeapi.ErrObjectNotExist) {
		t.Fatalf("transport-unknown failure marker err=%v, want not found", err)
	}
	assertQuotaState(t, ctx, fsClient, userID, unknownInvokeProject, 1, pipelinequota.DayKeyUTC(time.Now()))
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
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil)
	c.Set("userID", userID)
	c.Set("projectID", projectID)
	h.PipelineRun(c)
	if recorder.Code != wantStatus {
		t.Fatalf("pipeline trigger for %s returned %d, want %d: %s", projectID, recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantStatus != http.StatusAccepted {
		return ""
	}
	var payload struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.ExecutionID == "" {
		t.Fatalf("pipeline trigger for %s returned no execution ID: %s (%v)", projectID, recorder.Body.String(), err)
	}
	return payload.ExecutionID
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

type quotaCloudRunFixture struct {
	mu                      sync.Mutex
	next                    int
	executions              map[string]*quotaCloudRunExecution
	projectsWithInvokeError map[string]bool
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
		if f.projectsWithInvokeError[env["PROJECT_ID"]] {
			return testHTTPResponse(http.StatusForbidden, `{"error":"fixture invoke rejected"}`), nil
		}
		executionID := f.addExecution(env["USER_ID"], env["PROJECT_ID"], env["PIPELINE_RESERVATION_ID"], "RUNNING")
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
	collections := []string{"pipeline_quota", "pipeline_quota_reservations", "locks", "executions"}
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
