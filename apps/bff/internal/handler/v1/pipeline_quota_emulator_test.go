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
	"net/url"
	"os"
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
	// Run reports a cleanup exit failure.
	committedProject := "published-cleanup-failure"
	seedQuotaRaw(t, ctx, quotaClient, storageClient, bucket, scope, userID, committedProject)
	committedID := triggerQuotaPipeline(t, h, userID, committedProject, http.StatusAccepted)
	committedReservation := fixture.reservationID(committedID)
	seedQuotaPublicationReceipt(t, ctx, storageClient, bucket, scope, userID, committedProject, committedID)
	fixture.setStatus(committedID, "FAILED")
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile published execution with cleanup failure: %v", err)
	}
	assertQuotaReservation(t, ctx, fsClient, committedReservation, "charged")
	assertQuotaState(t, ctx, fsClient, userID, committedProject, 1, pipelinequota.DayKeyUTC(time.Now()))
	assertQuotaPipelineStatus(t, h, userID, committedProject, committedID, "SUCCEEDED", "", "charged", 1)

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

func seedQuotaPublicationReceipt(t *testing.T, ctx context.Context, client *cloudstorage.Client, bucket, scope, userID, projectID, executionID string) {
	t.Helper()
	data := fmt.Sprintf(`{"execution_id":%q,"generation_id":"fixture-generation","manifest_generation":1}`, executionID)
	if err := writeQuotaFixtureObject(ctx, client, bucket, scope, userID, projectID, "cache/local-pipeline-"+executionID+".commit.json", []byte(data)); err != nil {
		t.Fatalf("seed publication receipt for %s: %v", projectID, err)
	}
}

func writeQuotaFixtureObject(ctx context.Context, client *cloudstorage.Client, bucket, scope, userID, projectID, path string, data []byte) error {
	name := "local_scopes/" + scope + "/users/" + userID + "/projects/" + projectID + "/" + path
	writer := client.Bucket(bucket).Object(name).NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	attrs, err := client.Bucket(bucket).Object(name).Attrs(ctx)
	if err == nil && attrs.Name != name {
		return fmt.Errorf("fixture object name readback %q, want %q", attrs.Name, name)
	}
	return err
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
