package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localpipeline"
	"github.com/rayer/llm-wiki-bff/internal/pipelinediagnostic"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

type definitePreinvokeFailure struct {
	startCalls             int
	acceptedExecutionCount int
}

func (p *definitePreinvokeFailure) Start(context.Context, string, string, string, bool, string) (string, error) {
	p.startCalls++
	return "", errors.New("fixture rejected before execution acceptance")
}

func (*definitePreinvokeFailure) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*definitePreinvokeFailure) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestPipelineIntentFailureAndScheduledMarkerRecoveryRemainFenced(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") || !requireLoopbackEmulator(t, "STORAGE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore and Storage emulators")
	}
	const project, database = "llm-wiki-cloud", "(default)"
	scope := fmt.Sprintf("lwc413-native-marker-%d", time.Now().UnixNano())
	bucket := "lwc413-native-marker-" + strings.ReplaceAll(scope, "_", "-")
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create scoped Firestore client: %v", err)
	}
	storageClient, err := cloudstorage.NewClient(ctx)
	if err != nil {
		_ = fsClient.Close()
		t.Fatalf("create emulator Storage client: %v", err)
	}
	bucketHandle := storageClient.Bucket(bucket)
	if _, err := bucketHandle.Attrs(ctx); errors.Is(err, cloudstorage.ErrBucketNotExist) {
		if err := bucketHandle.Create(ctx, project, nil); err != nil {
			_ = storageClient.Close()
			_ = fsClient.Close()
			t.Fatalf("create disposable emulator bucket: %v", err)
		}
	} else if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("read disposable emulator bucket: %v", err)
	}
	storeRoot, err := gcs.NewClientWithScope(bucket, scope)
	if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("create scoped GCS store: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean native marker Firestore scope: %v", err)
		}
		if err := deletePipelineQuotaObjectScope(cleanupCtx, storageClient, bucket, scope); err != nil {
			t.Errorf("clean native marker Storage scope: %v", err)
		}
		if err := bucketHandle.Delete(cleanupCtx); err != nil && !errors.Is(err, cloudstorage.ErrBucketNotExist) {
			t.Errorf("delete disposable emulator bucket: %v", err)
		}
		_ = storeRoot.Close()
		_ = storageClient.Close()
		_ = fsClient.Close()
	})

	const userID, projectID = "native-marker-owner", "manual-recovery"
	if _, err := storeRoot.WithScope(userID, projectID).WriteBytes(ctx, []byte("# Pending source\n\nFixture input."), "raw/source.md"); err != nil {
		t.Fatalf("write local raw fixture: %v", err)
	}
	pipeline := &definitePreinvokeFailure{}
	h := New(storeRoot, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(2, 60, 1, nil)
	h.SetPipelineQuotaStore(fsClient)
	h.SetLocalPipelineExecutor(pipeline)
	if h.localPipeline != pipeline {
		t.Fatal("handler local pipeline test seam was not configured")
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil)
	c.Set("userID", userID)
	c.Set("projectID", projectID)
	h.PipelineRun(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("pre-invoke failure status=%d body=%s; want accepted durable intent", recorder.Code, recorder.Body.String())
	}
	if pipeline.startCalls != 1 || pipeline.acceptedExecutionCount != 0 {
		t.Fatalf("native invocation counter start=%d accepted=%d; want one definite rejection and zero accepted executions", pipeline.startCalls, pipeline.acceptedExecutionCount)
	}
	var response struct {
		Status       string `json:"status"`
		IntentID     string `json:"intent_id"`
		IntentStatus string `json:"intent_status"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode accepted Pipeline intent: %v", err)
	}
	if response.Status != "accepted" || response.IntentID == "" || response.IntentStatus != "failed" || response.Message == "" {
		t.Fatalf("accepted Pipeline response=%+v; want a persisted actionable failure", response)
	}
	manualReservation, found, err := fsClient.GetQuotaReservation(ctx, response.IntentID)
	if err != nil || !found || manualReservation.UserID != userID || manualReservation.ProjectID != projectID ||
		manualReservation.ExecutionID != "" || manualReservation.IntentStatus != "failed" ||
		manualReservation.SettlementStatus != "released" || manualReservation.IntentErrorMessage == "" {
		t.Fatalf("persisted pre-invoke failure reservation=%+v found=%t err=%v", manualReservation, found, err)
	}
	runsToday, _, _, err := fsClient.LoadQuotaState(ctx, userID, projectID)
	if err != nil || runsToday != 0 {
		t.Fatalf("failed intent quota runs_today=%d err=%v; want release to zero", runsToday, err)
	}
	active, err := fsClient.PipelineAdmissionActive(ctx, userID, projectID)
	if err != nil || active {
		t.Fatalf("failed intent project claim active=%t err=%v; want released", active, err)
	}

	manager, err := localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storeRoot, Worker: "unused-after-rejection", Scope: scope,
	})
	if err != nil {
		t.Fatalf("create native manager: %v", err)
	}
	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("idempotent quota reconciliation after confirmed failure: %v", err)
	}
	if again, stillFound, err := fsClient.GetQuotaReservation(ctx, response.IntentID); err != nil || !stillFound || again.SettlementStatus != "released" || again.IntentStatus != "failed" {
		t.Fatalf("repeated reconciliation changed failed intent=%+v found=%t err=%v", again, stillFound, err)
	}

	const scheduledProject = "scheduled-stale-marker"
	reservationID := "lwc413-scheduled-stale-" + fmt.Sprint(time.Now().UnixNano())
	oldAttemptID, currentAttemptID := "attempt-old", "attempt-current"
	now := time.Now().UTC()
	quotaReservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	if _, err := quotaReservation.Set(ctx, map[string]any{
		"user_id": userID, "project_id": scheduledProject, "execution_id": "", "attempt_id": currentAttemptID,
		"reserved_at": now, "day_key": pipelinequota.DayKeyUTC(now), "trigger": string(pipelinequota.TriggerScheduledDaily),
		"budget_class": "scheduled_daily", "settlement_status": "pending", "outcome": "",
		"intent_status": "ready", "intent_mode": "incremental", "mode": "incremental",
	}); err != nil {
		t.Fatalf("seed current scheduled reservation: %v", err)
	}
	quotaDoc := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, scheduledProject))
	if _, err := quotaDoc.Set(ctx, map[string]any{
		"runs_today": 0, "day_key": pipelinequota.DayKeyUTC(now), "active_reservation_id": reservationID,
		"active_trigger": string(pipelinequota.TriggerScheduledDaily), "last_run_at": now,
	}); err != nil {
		t.Fatalf("seed scheduled shared claim: %v", err)
	}
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_daily").Doc(reservationID).Set(ctx, map[string]any{
		"used": true, "result": "pending", "updated_at": now,
	}); err != nil {
		t.Fatalf("seed scheduled entitlement: %v", err)
	}
	legacyMarker := func(attemptID string) []byte {
		t.Helper()
		data, err := json.Marshal(pipelinediagnostic.ConfirmedInvokeFailure{
			Version: 1, Status: "failed", AttemptID: attemptID, Stage: pipelinediagnostic.StageUnknown,
			ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: reservationID,
			Message: pipelinediagnostic.ConfirmedInvokeFailureMessage,
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := storeRoot.WithScope(userID, scheduledProject).WriteBytes(ctx, legacyMarker(oldAttemptID), "cache/pipeline-"+reservationID+".failure.json"); err != nil {
		t.Fatalf("seed pre-upgrade stable-path stale marker: %v", err)
	}
	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("native stale scheduled-marker recovery: %v", err)
	}
	current, found, err := fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || current.AttemptID != currentAttemptID || current.SettlementStatus != "pending" {
		t.Fatalf("stale marker changed current scheduled reservation=%+v found=%t err=%v", current, found, err)
	}
	active, err = fsClient.PipelineAdmissionActive(ctx, userID, scheduledProject)
	if err != nil || !active {
		t.Fatalf("stale marker cleared new scheduled claim active=%t err=%v", active, err)
	}

	const matchingProject = "scheduled-matching-marker"
	matchingReservationID := "lwc413-scheduled-match-" + fmt.Sprint(time.Now().UnixNano())
	matchingAttemptID := "attempt-matching"
	matchingReservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(matchingReservationID)
	if _, err := matchingReservation.Set(ctx, map[string]any{
		"user_id": userID, "project_id": matchingProject, "execution_id": "", "attempt_id": matchingAttemptID,
		"reserved_at": now, "previous_last_run_at": now.Add(-30 * time.Minute), "day_key": pipelinequota.DayKeyUTC(now), "trigger": string(pipelinequota.TriggerScheduledDaily),
		"budget_class": "scheduled_daily", "settlement_status": "pending", "outcome": "",
	}); err != nil {
		t.Fatalf("seed matching scheduled reservation: %v", err)
	}
	matchingQuota := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, matchingProject))
	if _, err := matchingQuota.Set(ctx, map[string]any{
		"runs_today": 0, "day_key": pipelinequota.DayKeyUTC(now), "active_reservation_id": matchingReservationID,
		"active_trigger": string(pipelinequota.TriggerScheduledDaily), "last_run_at": now,
	}); err != nil {
		t.Fatalf("seed matching scheduled shared claim: %v", err)
	}
	matchingBudget := scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_daily").Doc(matchingReservationID)
	if _, err := matchingBudget.Set(ctx, map[string]any{"used": true, "result": "pending", "updated_at": now}); err != nil {
		t.Fatalf("seed matching scheduled entitlement: %v", err)
	}
	matchingMarker := pipelinediagnostic.ConfirmedInvokeFailure{
		Version: 1, Status: "failed", AttemptID: matchingAttemptID, Stage: pipelinediagnostic.StageUnknown,
		ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: matchingReservationID,
		Message: pipelinediagnostic.ConfirmedInvokeFailureMessage,
	}
	matchingBytes, err := json.Marshal(matchingMarker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeRoot.WithScope(userID, matchingProject).WriteBytes(ctx, matchingBytes, "cache/pipeline-"+matchingReservationID+".failure.json"); err != nil {
		t.Fatalf("seed pre-upgrade stable-path confirmed marker: %v", err)
	}
	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("native legacy scheduled-marker recovery: %v", err)
	}
	released, found, err := fsClient.GetQuotaReservation(ctx, matchingReservationID)
	if err != nil || !found || released.AttemptID != matchingAttemptID || released.SettlementStatus != "released" || released.Outcome != "NOT_INVOKED" {
		t.Fatalf("legacy scheduled marker reservation=%+v found=%t err=%v; want exact-incarnation release", released, found, err)
	}
	active, err = fsClient.PipelineAdmissionActive(ctx, userID, matchingProject)
	if err != nil || active {
		t.Fatalf("legacy marker did not release scheduled claim active=%t err=%v", active, err)
	}
	quotaAfterRelease, err := matchingQuota.Get(ctx)
	if err != nil || !quotaAfterRelease.Data()["last_run_at"].(time.Time).Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("legacy marker did not restore prior quota timestamp data=%v err=%v", quotaAfterRelease.Data(), err)
	}
	budget, err := matchingBudget.Get(ctx)
	if err != nil || budget.Data()["used"] != false || budget.Data()["result"] != "not_invoked" {
		t.Fatalf("matching scheduled entitlement data=%v err=%v; want available after not-invoked release", func() any {
			if budget == nil {
				return nil
			}
			return budget.Data()
		}(), err)
	}

	const cloudLegacyProject = "scheduled-cloud-legacy"
	cloudLegacyID := "lwc413-cloud-legacy-" + fmt.Sprint(time.Now().UnixNano())
	cloudLegacyAttempt := "cloud-legacy-attempt"
	previousLastRun := now.Add(-30 * time.Minute)
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(cloudLegacyID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": cloudLegacyProject, "execution_id": "", "attempt_id": cloudLegacyAttempt,
		"reserved_at": now, "previous_last_run_at": previousLastRun, "day_key": pipelinequota.DayKeyUTC(now),
		"trigger": string(pipelinequota.TriggerScheduledDaily), "budget_class": "scheduled_daily", "settlement_status": "pending",
	}); err != nil {
		t.Fatalf("seed legacy Cloud Run reservation: %v", err)
	}
	cloudLegacyQuota := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, cloudLegacyProject))
	if _, err := cloudLegacyQuota.Set(ctx, map[string]any{
		"user_id": userID, "project_id": cloudLegacyProject, "runs_today": 0, "day_key": pipelinequota.DayKeyUTC(now),
		"active_reservation_id": cloudLegacyID, "active_trigger": string(pipelinequota.TriggerScheduledDaily), "last_run_at": now,
	}); err != nil {
		t.Fatalf("seed legacy Cloud Run shared quota claim: %v", err)
	}
	cloudLegacyBudget := scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_daily").Doc(cloudLegacyID)
	if _, err := cloudLegacyBudget.Set(ctx, map[string]any{"used": true, "result": "pending", "updated_at": now}); err != nil {
		t.Fatalf("seed legacy Cloud Run daily entitlement: %v", err)
	}
	cloudMarker := pipelinediagnostic.ConfirmedInvokeFailure{
		Version: 1, Status: "failed", AttemptID: cloudLegacyAttempt, Stage: pipelinediagnostic.StageUnknown,
		ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: cloudLegacyID,
		Message: pipelinediagnostic.ConfirmedInvokeFailureMessage,
	}
	cloudMarkerBytes, err := json.Marshal(cloudMarker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeRoot.WithScope(userID, cloudLegacyProject).WriteBytes(ctx, cloudMarkerBytes, "cache/pipeline-"+cloudLegacyID+".failure.json"); err != nil {
		t.Fatalf("seed stable-path legacy Cloud Run marker: %v", err)
	}
	cloudHandler := New(storeRoot, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	cloudHandler.SetPipelineQuotaStore(fsClient)
	cloudHandler.SetPipelineJobURL("http://run.test/job:run")
	cloudHandler.metadataTokenURL = "http://run.test/token"
	cloudHandler.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/token" {
			return testHTTPResponse(http.StatusOK, `{"access_token":"fixture-token"}`), nil
		}
		return testHTTPResponse(http.StatusOK, `{"executions":[]}`), nil
	})}
	if err := cloudHandler.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("Cloud Run legacy-marker reconciliation: %v", err)
	}
	cloudReleased, found, err := fsClient.GetQuotaReservation(ctx, cloudLegacyID)
	if err != nil || !found || cloudReleased.AttemptID != cloudLegacyAttempt || cloudReleased.SettlementStatus != "released" || cloudReleased.Outcome != "NOT_INVOKED" {
		t.Fatalf("Cloud Run legacy marker reservation=%+v found=%t err=%v", cloudReleased, found, err)
	}
	if active, err := fsClient.PipelineAdmissionActive(ctx, userID, cloudLegacyProject); err != nil || active {
		t.Fatalf("Cloud Run legacy marker did not release shared claim active=%t err=%v", active, err)
	}
	cloudQuotaAfterRelease, err := cloudLegacyQuota.Get(ctx)
	if err != nil || !cloudQuotaAfterRelease.Data()["last_run_at"].(time.Time).Equal(previousLastRun) {
		t.Fatalf("Cloud Run legacy marker did not restore quota timestamp data=%v err=%v", cloudQuotaAfterRelease.Data(), err)
	}
}
