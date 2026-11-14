package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type pipelineNudgeEvidenceRoot struct {
	storage.RootStore
	diagnostics map[string][]byte
}

func (r *pipelineNudgeEvidenceRoot) Scope(userID, projectID string) storage.Store {
	return pipelineNudgeEvidenceStore{Store: r.RootStore.Scope(userID, projectID), diagnostics: r.diagnostics}
}

type pipelineNudgeEvidenceStore struct {
	storage.Store
	diagnostics map[string][]byte
}

func (s pipelineNudgeEvidenceStore) ReadLocalPublicationReceipt(context.Context, string) ([]byte, error) {
	return nil, storage.ErrObjectNotExist
}

func (s pipelineNudgeEvidenceStore) CurrentManifest(context.Context) (generation.Manifest, int64, bool, error) {
	return generation.Manifest{}, 0, false, nil
}

func (s pipelineNudgeEvidenceStore) ReadFileLimited(_ context.Context, relPath string, limit int64) ([]byte, error) {
	base := filepath.Base(relPath)
	if !strings.HasPrefix(base, "pipeline-") || !strings.HasSuffix(base, ".failure.json") {
		return nil, storage.ErrObjectNotExist
	}
	executionID := strings.TrimSuffix(strings.TrimPrefix(base, "pipeline-"), ".failure.json")
	data, ok := s.diagnostics[executionID]
	if !ok {
		return nil, storage.ErrObjectNotExist
	}
	if int64(len(data)) > limit {
		return nil, storage.ErrReadLimitExceeded
	}
	return append([]byte(nil), data...), nil
}

type pipelineNudgeLocalStatus struct {
	status *handlerapi.PipelineExecutionResponse
}

func (*pipelineNudgeLocalStatus) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", fmt.Errorf("unexpected local start in read-only observer test")
}

func (p *pipelineNudgeLocalStatus) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	copy := *p.status
	return &copy, nil
}

func (*pipelineNudgeLocalStatus) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestPipelineNudgeAllAcceptedProducerModesUseDiscoveredEvidence(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID := "nudge-mode-owner"
	root := &pipelineNudgeEvidenceRoot{RootStore: localfs.New(t.TempDir()), diagnostics: make(map[string][]byte)}
	executions := make(map[string]cloudRunExecution)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture-token"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/executions") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cloudRunExecutionsResponse{Executions: []cloudRunExecution{}})
			return
		}
		if strings.Contains(r.URL.Path, "/executions/") {
			runID := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if execution, ok := executions[runID]; ok {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(execution)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	quota := fsClient
	limits := pipelinequota.Limits{DailyLimit: 20, Cooldown: time.Hour, MinNewRaw: 1}
	baseNow := time.Now().UTC().Truncate(time.Microsecond)
	adminRef := scopedfirestore.Collection(fsClient.Raw(), "system").Doc("pipeline_automation")
	if _, err := adminRef.Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatalf("seed automation admin gate: %v", err)
	}
	defer func() { _, _ = adminRef.Delete(context.Background()) }()

	tests := []struct {
		name    string
		trigger pipelinequota.Trigger
		mode    pipelinequota.ExecutionMode
		local   bool
	}{
		{name: "manual incremental", trigger: pipelinequota.TriggerManual, mode: pipelinequota.ModeIncremental},
		{name: "manual full", trigger: pipelinequota.TriggerManual, mode: pipelinequota.ModeFull},
		{name: "scheduled", trigger: pipelinequota.TriggerScheduledDaily, mode: pipelinequota.ModeIncremental},
		{name: "source commit", trigger: pipelinequota.TriggerSourceCommit, mode: pipelinequota.ModeIncremental},
		{name: "local full", trigger: pipelinequota.TriggerManual, mode: pipelinequota.ModeFull, local: true},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectID := fmt.Sprintf("mode-%d-%d", index, time.Now().UnixNano())
			runID, reservationID := "run-"+projectID, "reservation-"+projectID
			now := baseNow.Add(time.Duration(index) * 3 * time.Hour)
			projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
			projectData := map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Mode Project"}
			if tc.trigger == pipelinequota.TriggerSourceCommit {
				projectData["status"], projectData["auto_pipeline_enabled"] = "ready", true
				projectData["auto_pipeline_revision"], projectData["pipeline_source_revision"] = int64(1), int64(1)
			}
			if _, err := projectRef.Set(ctx, projectData); err != nil {
				t.Fatalf("seed Project identity: %v", err)
			}
			var accepted bool
			var err error
			switch tc.trigger {
			case pipelinequota.TriggerManual:
				_, accepted, err = quota.ReserveQuota(ctx, reservationID, ownerID, projectID, limits, now, tc.mode, false, false, 1, 0, 0)
			case pipelinequota.TriggerScheduledDaily:
				_, accepted, err = quota.ReserveScheduledQuotaWithAttempt(ctx, reservationID, "attempt-"+projectID, ownerID, projectID, limits, now, tc.mode, false, false, 1, 0, 0)
			case pipelinequota.TriggerSourceCommit:
				_, accepted, err = quota.ReserveQuotaForPipelineAutomation(ctx, reservationID, ownerID, projectID, limits, now,
					false, false, 1, 0, 0, scopedfirestore.PipelineAutomationAdmission{
						SourceFingerprint: "fingerprint-" + projectID, SourceRevision: 1, ProjectRevision: 1, AdminRevision: 1,
					})
			default:
				t.Fatalf("unsupported fixture trigger %q", tc.trigger)
			}
			if err != nil || !accepted {
				t.Fatalf("reserve %s accepted=%t err=%v", tc.name, accepted, err)
			}
			if err := quota.LinkQuotaReservation(ctx, reservationID, runID); err != nil {
				t.Fatalf("link accepted reservation: %v", err)
			}
			if tc.trigger == pipelinequota.TriggerScheduledDaily {
				if err := quota.RecordScheduledPipelineAttempt(ctx, ownerID, projectID, pipelinequota.DayKeyUTC(now),
					reservationID, "attempt-"+projectID, runID, "accepted", now); err != nil {
					t.Fatalf("record exact scheduled attempt witness: %v", err)
				}
			}
			reservation, exists, err := quota.GetQuotaReservation(ctx, reservationID)
			if err != nil || !exists || reservation.Mode != tc.mode || reservation.Trigger != tc.trigger || !reservation.ProjectPipelineSequencePresent {
				t.Fatalf("read durable mode identity=%+v exists=%t err=%v", reservation, exists, err)
			}
			diagnostic, _ := json.Marshal(map[string]interface{}{
				"version": 1, "status": "failed", "stage": "postprocess", "error_class": "io",
				"execution": runID, "message": "safe diagnostic fixture",
			})
			root.diagnostics[runID] = diagnostic
			if tc.local {
				if _, err := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(runID).Set(ctx, map[string]interface{}{
					"user_id": ownerID, "project_id": projectID, "execution_id": runID,
					"native": true, "task_type": "pipeline", "stage": "full", "status": "FAILED",
					"started_at": now, "actual_started_at": now, "finished_at": now.Add(time.Minute),
					"quota_reservation_id": reservationID,
				}); err != nil {
					t.Fatalf("seed actual local execution source: %v", err)
				}
			}
			execution := cloudEventExecution(runID, ownerID, projectID, now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano))
			execution.RunningCount, execution.FailedCount, execution.CompletionStatus = 0, 1, "FAILED"
			execution.Template.Containers[0].Env = append(execution.Template.Containers[0].Env, cloudRunEnv{Name: pipelineReservationEnvName, Value: reservationID})
			if tc.mode == pipelinequota.ModeFull {
				execution.Template.Containers[0].Env = append(execution.Template.Containers[0].Env, cloudRunEnv{Name: "CLEAN_REBUILD", Value: "true"})
			}
			executions[runID] = execution

			h := &Handler{store: root, firestore: fsClient, metadataTokenURL: provider.URL + "/token", httpClient: provider.Client()}
			h.SetPipelineJobURL(provider.URL + "/jobs/pipeline:run")
			if tc.local {
				h.localPipeline = &pipelineNudgeLocalStatus{status: &handlerapi.PipelineExecutionResponse{Name: runID, Status: "FAILED", CompileMode: string(tc.mode)}}
			}
			var discovered pipelineEventSnapshot
			if tc.local {
				discovered, err = h.readLocalPipelineEventSnapshot(ctx, ownerID, projectID)
			} else {
				discovered, err = h.readCloudPipelineEventSnapshot(ctx, ownerID, projectID)
			}
			if err != nil {
				t.Fatalf("discover actual %s source evidence: %v", tc.name, err)
			}
			discoveredRecord, exists := discovered.records[runID]
			if discovered.Coverage.State != "complete" || !exists || discoveredRecord.status != "FAILED" ||
				discoveredRecord.publication != "confirmed_not_published" || !discoveredRecord.sequenceSet ||
				discoveredRecord.trigger != string(tc.trigger) || discoveredRecord.mode != string(tc.mode) {
				t.Fatalf("actual %s source evidence incomplete: coverage=%+v candidate=%+v record=%+v exists=%t", tc.name, discovered.Coverage, discovered.candidates, discoveredRecord, exists)
			}
			h.pipelineEventSource = func(context.Context, string, string) (pipelineEventSnapshot, error) { return discovered, nil }
			repository := nudge.NewRepository(fsClient.Raw())
			slot := nudge.Slot{SchemaVersion: nudge.SchemaVersion, SourceKey: "pipeline-fail:" + projectID}
			failures, _ := h.reconcilePipelineProjectFailure(ctx, repository, ownerID, projectID, "Mode Project", slot)
			stored, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
			if len(failures) != 0 || err != nil || !stored.Active || stored.ProducerWitness.ExecutionID != runID ||
				stored.ProducerWitness.Trigger != string(tc.trigger) || stored.ProducerWitness.Mode != string(tc.mode) ||
				stored.ProducerWitness.ProjectPipelineSequence != reservation.ProjectPipelineSequence {
				t.Fatalf("discovered %s producer did not reach common source: failures=%+v slot=%+v err=%v", tc.name, failures, stored, err)
			}
		})
	}
}

func TestPipelineNudgeRejectsMismatchedCloudRunReservationIdentity(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	const ownerID = "pipeline-identity-owner"
	actualExecutions := make(map[string]cloudRunExecution)
	listedExecutions := make(map[string][]cloudRunExecution)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture-token"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/executions") {
			jobID := strings.TrimSuffix(r.URL.Path, "/executions")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cloudRunExecutionsResponse{Executions: listedExecutions[jobID]})
			return
		}
		if strings.Contains(r.URL.Path, "/executions/") {
			runID := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if execution, ok := actualExecutions[runID]; ok {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(execution)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()

	for _, tc := range []struct {
		name             string
		status           string
		publication      string
		candidatePending bool
		includeList      bool
		diagnosticStage  string
	}{
		{name: "known candidate cannot upsert failure with another execution reservation", status: "FAILED", publication: "confirmed_not_published", diagnosticStage: "postprocess"},
		{name: "listed pending candidate cannot clear failure with another execution reservation", status: "SUCCEEDED", publication: "confirmed_published", candidatePending: true, includeList: true, diagnosticStage: "lease_cleanup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID := fmt.Sprintf("identity-%d", time.Now().UnixNano())
			runID := "run-" + projectID
			reservationA, attemptA := "reservation-a-"+projectID, "attempt-a-"+projectID
			reservationB, attemptB := "reservation-b-"+projectID, "attempt-b-"+projectID
			projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
			if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Identity Project"}); err != nil {
				t.Fatalf("seed Project identity: %v", err)
			}
			defer func() { _, _ = projectRef.Delete(context.Background()) }()

			repository := nudge.NewRepository(fsClient.Raw())
			previous := pipelineEventRunRecord{id: "prior-" + projectID, status: "FAILED", publication: "confirmed_not_published",
				reservationID: "prior-reservation-" + projectID, attemptID: "prior-attempt-" + projectID,
				trigger: string(pipelinequota.TriggerManual), mode: string(pipelinequota.ModeFull), sequence: 1, sequenceSet: true}
			initial, err := pipelineFailureObservation(projectID, "Identity Project", previous, nudge.ProducerWitness{})
			if err != nil {
				t.Fatalf("build prior source observation: %v", err)
			}
			if applied, err := repository.ApplyObservation(ctx, ownerID, initial); err != nil || !applied {
				t.Fatalf("seed prior active source applied=%t err=%v", applied, err)
			}
			before, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
			if err != nil || !before.Active || before.SourceRevision != 1 || before.ProducerWitness.ExecutionID != previous.id {
				t.Fatalf("read seeded prior source slot=%+v err=%v", before, err)
			}

			quotaFields := map[string]interface{}{
				"user_id": ownerID, "project_id": projectID, "project_pipeline_sequence": int64(2),
				"last_pipeline_intent_id": reservationA,
			}
			reservationAExecution := runID
			reservationAStatus, reservationASettlement := "failed", "refunded"
			if tc.candidatePending {
				quotaFields["active_reservation_id"] = reservationA
				reservationAExecution = ""
				reservationAStatus, reservationASettlement = "invocation_unknown", "pending"
			}
			if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(ownerID, projectID)).Set(ctx, quotaFields); err != nil {
				t.Fatalf("seed scoped quota pointer: %v", err)
			}
			if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationA).Set(ctx, map[string]interface{}{
				"user_id": ownerID, "project_id": projectID, "attempt_id": attemptA, "execution_id": reservationAExecution,
				"intent_status": reservationAStatus, "settlement_status": reservationASettlement, "trigger": "manual",
				"mode": "full", "project_pipeline_sequence": int64(2),
			}); err != nil {
				t.Fatalf("seed candidate reservation A: %v", err)
			}
			// Reservation B belongs to the same owner and Project but points to a
			// different run. The exact Cloud Run template for runID names B.
			if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationB).Set(ctx, map[string]interface{}{
				"user_id": ownerID, "project_id": projectID, "attempt_id": attemptB, "execution_id": "other-" + projectID,
				"intent_status": "succeeded", "settlement_status": "charged", "trigger": "manual",
				"mode": "full", "project_pipeline_sequence": int64(3),
			}); err != nil {
				t.Fatalf("seed actual execution reservation B: %v", err)
			}
			actualReservation, exists, err := scopedfirestore.GetQuotaReservationFromFirestore(ctx, fsClient.Raw(), reservationB)
			if err != nil || !exists || actualReservation.AttemptID != attemptB || actualReservation.ExecutionID != "other-"+projectID {
				t.Fatalf("read actual reservation B identity=%+v exists=%t err=%v", actualReservation, exists, err)
			}

			start := time.Now().UTC().Truncate(time.Microsecond)
			end := start.Add(time.Minute)
			detail := cloudEventExecution(runID, ownerID, projectID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
			detail.RunningCount, detail.CompletionStatus = 0, tc.status
			if tc.status == "FAILED" {
				detail.FailedCount = 1
			} else {
				detail.SucceededCount = 1
			}
			detail.Template.Containers[0].Env = append(detail.Template.Containers[0].Env, cloudRunEnv{Name: pipelineReservationEnvName, Value: reservationB})
			actualExecutions[runID] = detail
			jobID := "/jobs/pipeline"
			if tc.includeList {
				listed := cloudEventExecution(runID, ownerID, projectID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
				listed.RunningCount, listed.CompletionStatus = 0, tc.status
				if tc.status == "FAILED" {
					listed.FailedCount = 1
				} else {
					listed.SucceededCount = 1
				}
				listed.Template.Containers[0].Env = append(listed.Template.Containers[0].Env, cloudRunEnv{Name: pipelineReservationEnvName, Value: reservationA})
				listedExecutions[jobID] = []cloudRunExecution{listed}
			}

			root := &pipelineNudgeEvidenceRoot{RootStore: localfs.New(t.TempDir()), diagnostics: map[string][]byte{
				runID: []byte(fmt.Sprintf(`{"version":1,"status":"failed","stage":%q,"error_class":"io","execution":%q,"message":"safe identity fixture"}`, tc.diagnosticStage, runID)),
			}}
			h := &Handler{store: root, firestore: fsClient, metadataTokenURL: provider.URL + "/token", httpClient: provider.Client()}
			h.SetPipelineJobURL(provider.URL + "/jobs/pipeline:run")

			snapshot, err := h.readCloudPipelineEventSnapshot(ctx, ownerID, projectID)
			if err != nil {
				t.Fatalf("discover exact Cloud Run reservation identity: %v", err)
			}
			record, exists := snapshot.records[runID]
			if snapshot.Coverage.State != "partial" || !exists || !record.reservationMissing || record.reservationID != reservationB ||
				record.status != tc.status || record.publication != tc.publication || record.attemptID != "" || record.sequenceSet {
				t.Fatalf("mismatched execution identity was accepted: coverage=%+v record=%+v exists=%t", snapshot.Coverage, record, exists)
			}
			if tc.candidatePending {
				if len(snapshot.candidates.Pending) != 1 || snapshot.candidates.Pending[0].ReservationID != reservationA || snapshot.candidates.Pending[0].AttemptID != attemptA {
					t.Fatalf("pending candidate A identity changed across exact discovery: %+v", snapshot.candidates.Pending)
				}
			} else if len(snapshot.candidates.Known) != 1 || snapshot.candidates.Known[0].ReservationID != reservationA ||
				snapshot.candidates.Known[0].AttemptID != attemptA || snapshot.candidates.Known[0].ExecutionID != runID {
				t.Fatalf("known candidate A identity changed across exact discovery: %+v", snapshot.candidates.Known)
			}

			failures, notices := h.reconcilePipelineProjectFailure(ctx, repository, ownerID, projectID, "Identity Project", before)
			if len(failures) != 1 || failures[0].SafeClass != "pipeline_history_partial" || len(notices) != 1 ||
				notices[0].State != "stale" || notices[0].SafeClass != "pipeline_history_partial" {
				t.Fatalf("mismatched execution identity was not surfaced as partial: failures=%+v notices=%+v", failures, notices)
			}
			after, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
			if err != nil || !after.Active || after.SourceRevision != before.SourceRevision || after.ProducerWitness != before.ProducerWitness ||
				after.PayloadDigest != before.PayloadDigest || after.AttentionMaterialRevision != before.AttentionMaterialRevision ||
				after.Observation == nil || after.Observation.ExecutionID != previous.id {
				t.Fatalf("identity mismatch changed the prior source slot: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}
