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
	if err := h.writeConfirmedPipelineInvokeFailure(ctx, userID, scheduledProject, reservationID, oldAttemptID); err != nil {
		t.Fatalf("write stale scheduled marker with actual handler producer: %v", err)
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
		"reserved_at": now, "day_key": pipelinequota.DayKeyUTC(now), "trigger": string(pipelinequota.TriggerScheduledDaily),
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
	if err := h.writeConfirmedPipelineInvokeFailure(ctx, userID, matchingProject, matchingReservationID, matchingAttemptID); err != nil {
		t.Fatalf("write matching scheduled marker with actual handler producer: %v", err)
	}
	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("native matching scheduled-marker recovery: %v", err)
	}
	released, found, err := fsClient.GetQuotaReservation(ctx, matchingReservationID)
	if err != nil || !found || released.AttemptID != matchingAttemptID || released.SettlementStatus != "released" || released.Outcome != "NOT_INVOKED" {
		t.Fatalf("matching scheduled marker reservation=%+v found=%t err=%v; want exact-incarnation release", released, found, err)
	}
	active, err = fsClient.PipelineAdmissionActive(ctx, userID, matchingProject)
	if err != nil || active {
		t.Fatalf("matching marker did not release scheduled claim active=%t err=%v", active, err)
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
}
