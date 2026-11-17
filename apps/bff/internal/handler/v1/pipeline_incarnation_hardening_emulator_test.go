package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinediagnostic"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

func TestPipelineLLMProfileFencesScheduledAttemptBeforeResolutionEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "(default)"
	unique := fmt.Sprint(time.Now().UnixNano())
	userID, projectID := "lwc493-resolver-owner-"+unique, "resolver-project-"+unique
	scope := "lwc493-resolver-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean resolver fixture: %v", err)
		}
		_ = fsClient.Close()
	})
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(userID, projectID)).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "status": "ready",
	}); err != nil {
		t.Fatalf("seed owned Project: %v", err)
	}
	const reservationID, attemptA, attemptB = "daily-reservation", "attempt-a", "attempt-b"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": attemptB,
		"trigger": "scheduled_daily", "budget_class": "scheduled_daily", "intent_status": "ready", "intent_mode": "incremental",
		"mode": "incremental", "settlement_status": "pending",
	}); err != nil {
		t.Fatalf("seed current scheduled incarnation B: %v", err)
	}
	h := New(localfs.New(t.TempDir()), fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaStore(fsClient)
	h.SetPipelineWorkerIdentity("https://bff.example", "worker@example.invalid")
	h.SetPipelineWorkerIdentityValidator(func(_ context.Context, token, _ string) (string, error) {
		if token != "valid-worker-token" {
			return "", errors.New("invalid fixture token")
		}
		return "worker@example.invalid", nil
	})
	resolverCalls := 0
	h.pipelineProfileResolver = func(context.Context, string, string, string, bool) (string, []pipelineLLMProfile, llm.OperationProfileSnapshot, error) {
		resolverCalls++
		return "fixture", []pipelineLLMProfile{}, llm.OperationProfileSnapshot{}, nil
	}
	invoke := func(attemptID string, omitAttempt bool) *httptest.ResponseRecorder {
		t.Helper()
		request := pipelineLLMProfileRequest{ReservationID: reservationID, AttemptID: attemptID, UserID: userID, ProjectID: projectID}
		if omitAttempt {
			request.AttemptID = ""
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/llm-profile", bytes.NewReader(body))
		c.Request.Header.Set("Authorization", "Bearer valid-worker-token")
		h.PipelineLLMProfile(c)
		return recorder
	}
	gin.SetMode(gin.TestMode)
	if response := invoke(attemptB, false); response.Code != http.StatusOK || resolverCalls != 1 {
		t.Fatalf("current B resolver response=%d calls=%d body=%s; want 200 and one resolver call", response.Code, resolverCalls, response.Body.String())
	}
	for _, tc := range []struct {
		name      string
		attemptID string
		omit      bool
	}{{name: "stale A", attemptID: attemptA}, {name: "missing attempt", omit: true}} {
		t.Run(tc.name, func(t *testing.T) {
			response := invoke(tc.attemptID, tc.omit)
			if response.Code != http.StatusForbidden || resolverCalls != 1 {
				t.Fatalf("rejected request status=%d resolver_calls=%d body=%s; want 403 before resolution", response.Code, resolverCalls, response.Body.String())
			}
		})
	}
	manualID := "legacy-manual"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(manualID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": manualID, "trigger": "manual", "mode": "incremental", "settlement_status": "pending",
	}); err != nil {
		t.Fatalf("seed legacy/manual reservation: %v", err)
	}
	manualRequest := pipelineLLMProfileRequest{ReservationID: manualID, UserID: userID, ProjectID: projectID}
	manualBody, _ := json.Marshal(manualRequest)
	manualRecorder := httptest.NewRecorder()
	manualContext, _ := gin.CreateTestContext(manualRecorder)
	manualContext.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/llm-profile", bytes.NewReader(manualBody))
	manualContext.Request.Header.Set("Authorization", "Bearer valid-worker-token")
	h.PipelineLLMProfile(manualContext)
	if manualRecorder.Code != http.StatusOK || resolverCalls != 2 {
		t.Fatalf("legacy/manual omitted-attempt response=%d resolver_calls=%d body=%s; want compatibility", manualRecorder.Code, resolverCalls, manualRecorder.Body.String())
	}
}

func TestProfileDispatcherDoesNotLetReleasedAttemptStartReplacementEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "(default)"
	unique := fmt.Sprint(time.Now().UnixNano())
	userID, projectID := "lwc493-intent-owner-"+unique, "intent-project-"+unique
	scope := "lwc493-intent-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean intent race fixture: %v", err)
		}
		_ = fsClient.Close()
	})
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(userID, projectID)).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "status": "ready",
	}); err != nil {
		t.Fatalf("seed owned Project: %v", err)
	}
	stateRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(userID, projectID)).Collection("profile").Doc("state")
	requirements := []ProfileRequirement{{ID: "r1", Text: "failing preparation input"}}
	digest := profileRequirementsDigest(requirements)
	if _, err := stateRef.Set(ctx, ProfileState{ProjectID: projectID, Revision: 1, Requirements: requirements,
		BootstrapGuidance: &ProfileBootstrapGuidance{Status: "pending", ProfileRevision: 1, InputDigest: digest}}); err != nil {
		t.Fatalf("seed pending Profile: %v", err)
	}
	now := time.Now().UTC()
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, pipelinequota.DayKeyUTC(now))
	const attemptA, attemptB = "attempt-a", "attempt-b"
	baseStore, entered, resume := newPipelineIntentSecondReadGate(fsClient)
	_, reserved, err := baseStore.ReserveScheduledQuotaWithAttempt(ctx, reservationID, attemptA, userID, projectID,
		pipelinequota.Limits{DailyLimit: 2, Cooldown: time.Hour, MinNewRaw: 1}, now, pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve A: reserved=%t err=%v", reserved, err)
	}
	starts := &attemptCountingPipeline{}
	h := New(localfs.New(t.TempDir()), fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaStore(baseStore)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetLocalPipelineExecutor(starts)
	profileProvider := &deterministicFakeProfileProvider{err: errors.New("fixture preparation failure")}
	dispatcher := &ProfileDispatcher{Handler: h, Provider: profilederive.NewProvider(profileProvider)}
	workA := profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "pipeline", ID: reservationID, AttemptID: attemptA}
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.executePipelineIntent(ctx, h.profileRepository.(*firestoreProfileRepository), workA)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("A did not reach its post-preparation read barrier")
	}
	failedA, found, err := fsClient.GetPipelineIntent(ctx, userID, projectID, reservationID)
	if err != nil || !found || failedA.AttemptID != attemptA || failedA.IntentStatus != "failed" || failedA.SettlementStatus != "released" {
		t.Fatalf("A must be failed and released before B admission: %+v found=%t err=%v", failedA, found, err)
	}
	if _, err := stateRef.Set(ctx, ProfileState{ProjectID: projectID, Revision: 2, Requirements: []ProfileRequirement{}}); err != nil {
		t.Fatalf("change Profile to no-requirements ready state: %v", err)
	}
	_, reserved, err = fsClient.ReserveScheduledQuotaWithAttempt(ctx, reservationID, attemptB, userID, projectID,
		pipelinequota.Limits{DailyLimit: 2, Cooldown: time.Hour, MinNewRaw: 1}, now.Add(time.Minute), pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve replacement B: reserved=%t err=%v", reserved, err)
	}
	currentB, found, err := fsClient.GetPipelineIntent(ctx, userID, projectID, reservationID)
	if err != nil || !found || currentB.AttemptID != attemptB || currentB.IntentStatus != "ready" {
		t.Fatalf("B must be ready before A second read: %+v found=%t err=%v", currentB, found, err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatalf("late A work returned error: %v", err)
	}
	if starts.calls.Load() != 0 {
		t.Fatalf("late A work started replacement B %d times", starts.calls.Load())
	}
	workB := profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "pipeline", ID: reservationID, AttemptID: attemptB}
	if err := dispatcher.executePipelineIntent(ctx, h.profileRepository.(*firestoreProfileRepository), workB); err != nil {
		t.Fatalf("B work invocation: %v", err)
	}
	if starts.calls.Load() != 1 || starts.attemptID != attemptB {
		latest, _, latestErr := fsClient.GetPipelineIntent(ctx, userID, projectID, reservationID)
		t.Fatalf("B work started calls=%d attempt=%q; latest=%+v read_count=%d latest_err=%v; want exactly one B invocation", starts.calls.Load(), starts.attemptID, latest, baseStore.reads.Load(), latestErr)
	}
}

func TestQuotaReservationAttemptReaderUsesExactArchiveEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project = "llm-wiki-cloud"
	unique := fmt.Sprint(time.Now().UnixNano())
	scope := "lwc493-quota-archive-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean quota archive fixture: %v", err)
		}
		_ = fsClient.Close()
	})
	const reservationID, attemptA, attemptB = "daily", "attempt-a", "attempt-b"
	reservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	if _, err := reservation.Set(ctx, map[string]any{"user_id": "owner", "project_id": "project", "attempt_id": attemptB,
		"trigger": "scheduled_daily", "intent_status": "preparing", "settlement_status": "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.Collection("attempts").Doc(attemptA).Set(ctx, map[string]any{"user_id": "owner", "project_id": "project",
		"attempt_id": attemptA, "execution_id": "execution-a", "trigger": "scheduled_daily", "intent_status": "failed", "settlement_status": "released"}); err != nil {
		t.Fatal(err)
	}
	archived, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), reservationID, attemptA, "owner", "project")
	if err != nil || !found || archived.AttemptID != attemptA || archived.ExecutionID != "execution-a" || archived.SettlementStatus != "released" {
		t.Fatalf("exact A archive=%+v found=%t err=%v", archived, found, err)
	}
	live, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), reservationID, attemptB, "owner", "project")
	if err != nil || !found || live.AttemptID != attemptB || live.SettlementStatus != "pending" {
		t.Fatalf("exact B live reservation=%+v found=%t err=%v", live, found, err)
	}
	if _, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), reservationID, attemptA, "foreign", "project"); err != nil || found {
		t.Fatalf("foreign owner read found=%t err=%v; want unproven", found, err)
	}
	legacyManualID := "legacy-full-manual"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(legacyManualID).Set(ctx, map[string]any{
		"user_id": "owner", "project_id": "project", "attempt_id": legacyManualID, "trigger": "manual",
		"intent_status": "succeeded", "mode": "full", "settlement_status": "charged",
	}); err != nil {
		t.Fatal(err)
	}
	legacyManual, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), legacyManualID, "", "owner", "project")
	if err != nil || !found || legacyManual.AttemptID != legacyManualID || legacyManual.Mode != pipelinequota.ModeFull || legacyManual.SettlementStatus != "charged" {
		t.Fatalf("legacy full/manual reservation=%+v found=%t err=%v; want unique stable-ID match despite intent status", legacyManual, found, err)
	}
	missingManualAttemptID := "legacy-manual-without-incarnation"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(missingManualAttemptID).Set(ctx, map[string]any{
		"user_id": "owner", "project_id": "project", "trigger": "manual", "intent_status": "succeeded", "mode": "full", "settlement_status": "charged",
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), missingManualAttemptID, "", "owner", "project"); err != nil || found {
		t.Fatalf("non-unique manual reservation without attempt found=%t err=%v; want unproven", found, err)
	}
	if _, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), reservationID, "", "owner", "project"); err != nil || found {
		t.Fatalf("distinct scheduled reservation without execution attempt found=%t err=%v; want unproven", found, err)
	}
	if _, err := reservation.Collection("attempts").Doc("malformed").Set(ctx, map[string]any{"user_id": "owner", "project_id": "project", "attempt_id": attemptA}); err != nil {
		t.Fatal(err)
	}
	if _, found, _, err := scopedfirestore.GetQuotaReservationForAttemptSnapshotFromFirestore(ctx, fsClient.Raw(), reservationID, "malformed", "owner", "project"); err != nil || found {
		t.Fatalf("malformed archive found=%t err=%v; want unproven", found, err)
	}
}

func TestHistoricalCloudExecutionStatusAndEventUseArchivedAttemptEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "(default)"
	unique := fmt.Sprint(time.Now().UnixNano())
	userID, projectID := "lwc493-history-owner-"+unique, "history-project-"+unique
	scope := "lwc493-history-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean historical execution fixture: %v", err)
		}
		_ = fsClient.Close()
	})
	const reservationID, attemptA, attemptB = "daily-reservation", "attempt-a", "attempt-b"
	fixture := newQuotaCloudRunFixture()
	executionA := fixture.addExecutionWithAttempt(userID, projectID, reservationID, attemptA, "FAILED")
	executionB := fixture.addExecutionWithAttempt(userID, projectID, reservationID, attemptB, "RUNNING")
	reservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	if _, err := reservation.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "attempt_id": attemptB,
		"execution_id": executionB, "trigger": "scheduled_daily", "budget_class": "scheduled_daily", "intent_status": "compiling",
		"intent_mode": "incremental", "mode": "incremental", "settlement_status": "pending", "project_pipeline_sequence": int64(2)}); err != nil {
		t.Fatalf("seed live replacement B: %v", err)
	}
	if _, err := reservation.Collection("attempts").Doc(attemptA).Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "attempt_id": attemptA,
		"execution_id": executionA, "trigger": "scheduled_daily", "budget_class": "scheduled_daily", "intent_status": "failed",
		"mode": "incremental", "settlement_status": "refunded", "outcome": "FAILED", "project_pipeline_sequence": int64(1)}); err != nil {
		t.Fatalf("seed immutable attempt A archive: %v", err)
	}
	h := New(localfs.New(t.TempDir()), fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineJobURL("http://run.test/job:run")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	statusA, gotReservationA, gotAttemptA, err := h.pipelineExecutionStatusForOwnerWithIncarnation(ctx, executionA, userID, projectID)
	if err != nil || statusA == nil || gotReservationA != reservationID || gotAttemptA != attemptA || statusA.QuotaSettlement != "refunded" {
		t.Fatalf("historical A Cloud Run status=%+v reservation=%q attempt=%q err=%v; want archived refund", statusA, gotReservationA, gotAttemptA, err)
	}
	recordA := pipelineEventRunRecord{id: executionA, reservationID: reservationID, attemptID: attemptA}
	if err := h.attachPipelineReservation(ctx, userID, projectID, &recordA, nil); err != nil || recordA.reservationMissing || recordA.attemptID != attemptA || recordA.trigger != string(pipelinequota.TriggerScheduledDaily) {
		t.Fatalf("historical A event identity=%+v err=%v; want exact archived attempt", recordA, err)
	}
	statusB, gotReservationB, gotAttemptB, err := h.pipelineExecutionStatusForOwnerWithIncarnation(ctx, executionB, userID, projectID)
	if err != nil || statusB == nil || gotReservationB != reservationID || gotAttemptB != attemptB || statusB.QuotaSettlement != "pending" {
		t.Fatalf("current B Cloud Run status=%+v reservation=%q attempt=%q err=%v; want live pending settlement", statusB, gotReservationB, gotAttemptB, err)
	}

	const manualReservationID = "legacy-manual-full"
	manualExecutionID := fixture.addExecution(userID, projectID, manualReservationID, "SUCCEEDED")
	fixture.setFull(manualExecutionID)
	manualReservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(manualReservationID)
	if _, err := manualReservation.Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": manualReservationID, "execution_id": manualExecutionID,
		"trigger": "manual", "budget_class": "manual", "intent_status": "succeeded", "mode": "full", "settlement_status": "charged",
	}); err != nil {
		t.Fatalf("seed legacy full/manual reservation: %v", err)
	}
	manualStatus, gotManualReservation, gotManualAttempt, err := h.pipelineExecutionStatusForOwnerWithIncarnation(ctx, manualExecutionID, userID, projectID)
	if err != nil || manualStatus == nil || gotManualReservation != manualReservationID || gotManualAttempt != "" || manualStatus.QuotaSettlement != "charged" {
		t.Fatalf("legacy full/manual Cloud Run status=%+v reservation=%q attempt=%q err=%v; want unique legacy settlement", manualStatus, gotManualReservation, gotManualAttempt, err)
	}

	missingAttemptExecutionID := fixture.addExecution(userID, projectID, reservationID, "RUNNING")
	missingAttemptStatus, gotMissingReservation, gotMissingAttempt, err := h.pipelineExecutionStatusForOwnerWithIncarnation(ctx, missingAttemptExecutionID, userID, projectID)
	if err != nil || missingAttemptStatus == nil || gotMissingReservation != reservationID || gotMissingAttempt != "" || missingAttemptStatus.QuotaSettlement != "unknown" {
		t.Fatalf("distinct scheduled execution without attempt status=%+v reservation=%q attempt=%q err=%v; want unknown settlement", missingAttemptStatus, gotMissingReservation, gotMissingAttempt, err)
	}

	events, err := h.readCloudPipelineEventSnapshot(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("read Cloud Run event snapshot with legacy/manual and scheduled executions: %v", err)
	}
	manualEvent, ok := events.records[manualExecutionID]
	if !ok || manualEvent.reservationMissing || manualEvent.attemptID != manualReservationID || manualEvent.trigger != string(pipelinequota.TriggerManual) || manualEvent.mode != string(pipelinequota.ModeFull) || manualEvent.quota != "charged" {
		t.Fatalf("legacy full/manual event=%+v found=%t; want exact stable identity and charged settlement", manualEvent, ok)
	}
	missingAttemptEvent, ok := events.records[missingAttemptExecutionID]
	if !ok || !missingAttemptEvent.reservationMissing || missingAttemptEvent.attemptID != "" {
		t.Fatalf("distinct scheduled event without attempt=%+v found=%t; want identity unproven", missingAttemptEvent, ok)
	}
}

func TestScheduledInvokeFailureReaderSupportsOnlyExactLegacyMarker(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	tests := []struct {
		name, attemptID, intentStatus, path string
		marker                              pipelinediagnostic.ConfirmedInvokeFailure
		raw                                 []byte
		want                                bool
	}{
		{name: "exact legacy stable path", attemptID: "attempt-a", path: "cache/pipeline-daily.failure.json", marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", AttemptID: "attempt-a", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "daily", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}, want: true},
		{name: "new attempt scoped path", attemptID: "attempt-b", intentStatus: "ready", path: pipelinediagnostic.ConfirmedInvokeFailureObjectPath("daily", "attempt-b"), marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", AttemptID: "attempt-b", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "daily", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}, want: true},
		{name: "stale A body", attemptID: "attempt-b", path: "cache/pipeline-daily.failure.json", marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", AttemptID: "attempt-a", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "daily", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}},
		{name: "missing scheduled attempt", attemptID: "attempt-a", path: "cache/pipeline-daily.failure.json", marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "daily", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}},
		{name: "foreign execution", attemptID: "attempt-a", path: "cache/pipeline-daily.failure.json", marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", AttemptID: "attempt-a", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "foreign", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}},
		{name: "malformed", attemptID: "attempt-a", path: "cache/pipeline-daily.failure.json", raw: []byte(`{"version":1,"unknown":true}`)},
		{name: "oversize", attemptID: "attempt-a", path: "cache/pipeline-daily.failure.json", raw: bytes.Repeat([]byte("x"), maxPipelineDiagnosticBytes+1)},
		{name: "no fallback for current incarnation", attemptID: "attempt-b", intentStatus: "preparing", path: "cache/pipeline-daily.failure.json", marker: pipelinediagnostic.ConfirmedInvokeFailure{Version: 1, Status: "failed", AttemptID: "attempt-b", Stage: pipelinediagnostic.StageUnknown, ErrorClass: pipelinediagnostic.ErrorClassUnknown, Execution: "daily", Message: pipelinediagnostic.ConfirmedInvokeFailureMessage}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.raw
			if data == nil {
				var err error
				data, err = json.Marshal(tc.marker)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := root.Scope("owner", tc.name).WriteBytes(context.Background(), data, tc.path); err != nil {
				t.Fatalf("write marker fixture: %v", err)
			}
			reservation := scopedfirestore.QuotaReservation{ID: "daily", AttemptID: tc.attemptID, UserID: "owner", ProjectID: tc.name,
				Trigger: pipelinequota.TriggerScheduledDaily, IntentStatus: tc.intentStatus}
			confirmed, err := h.hasConfirmedPipelineInvokeFailure(context.Background(), reservation)
			if err != nil || confirmed != tc.want {
				t.Fatalf("confirmed=%t err=%v; want %t", confirmed, err, tc.want)
			}
		})
	}
}

type pipelineIntentSecondReadGate struct {
	*scopedfirestore.Client
	reads   atomic.Int32
	entered chan struct{}
	resume  chan struct{}
	start   sync.Once
}

func newPipelineIntentSecondReadGate(client *scopedfirestore.Client) (*pipelineIntentSecondReadGate, chan struct{}, chan struct{}) {
	entered, resume := make(chan struct{}), make(chan struct{})
	return &pipelineIntentSecondReadGate{Client: client, entered: entered, resume: resume}, entered, resume
}

func (g *pipelineIntentSecondReadGate) GetPipelineIntent(ctx context.Context, userID, projectID, reservationID string) (scopedfirestore.QuotaReservation, bool, error) {
	if g.reads.Add(1) == 2 {
		g.start.Do(func() { close(g.entered) })
		select {
		case <-g.resume:
		case <-ctx.Done():
			return scopedfirestore.QuotaReservation{}, false, ctx.Err()
		}
	}
	return g.Client.GetPipelineIntent(ctx, userID, projectID, reservationID)
}

type attemptCountingPipeline struct {
	calls     atomic.Int32
	attemptID string
	mu        sync.Mutex
}

func (p *attemptCountingPipeline) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "unexpected-legacy-start", nil
}

func (p *attemptCountingPipeline) StartWithAttempt(_ context.Context, _, _, _ string, _ bool, _, attemptID string) (string, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.attemptID = attemptID
	p.mu.Unlock()
	return "execution-" + attemptID, nil
}

func (*attemptCountingPipeline) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*attemptCountingPipeline) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

var _ interface {
	StartWithAttempt(context.Context, string, string, string, bool, string, string) (string, error)
} = (*attemptCountingPipeline)(nil)
