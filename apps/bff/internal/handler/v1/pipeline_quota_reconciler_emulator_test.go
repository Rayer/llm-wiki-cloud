package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

func TestQuotaReconcilerIsolatesSettlementFailureWithFirestoreEmulator(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner := fmt.Sprintf("lwc484-isolation-%d", time.Now().UnixNano())
	ids := []string{"record-fails", "record-healthy"}
	executions := map[string]reconcileExecutionFixture{}
	for _, suffix := range ids {
		projectID := "project-" + suffix
		reservationID := fmt.Sprintf("lwc484-emulator-%d-%s", time.Now().UnixNano(), suffix)
		executionID := "execution-" + suffix
		now := time.Now().UTC()
		quotaID := scopedfirestore.QuotaDocID(owner, projectID)
		if _, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(quotaID).Set(ctx, map[string]interface{}{
			"user_id": owner, "project_id": projectID, "runs_today": int64(1), "day_key": pipelinequota.DayKeyUTC(now),
			"last_run_at": now, "active_reservation_id": reservationID, "active_trigger": string(pipelinequota.TriggerManual),
		}); err != nil {
			t.Fatalf("seed quota %s: %v", suffix, err)
		}
		if _, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(reservationID).Set(ctx, map[string]interface{}{
			"user_id": owner, "project_id": projectID, "day_key": pipelinequota.DayKeyUTC(now),
			"trigger": string(pipelinequota.TriggerManual), "budget_class": "manual", "settlement_status": "pending",
			"outcome": "", "execution_id": executionID, "reserved_at": now,
		}); err != nil {
			t.Fatalf("seed reservation %s: %v", suffix, err)
		}
		executions[executionID] = reconcileExecutionFixture{userID: owner, projectID: projectID, reservationID: reservationID, state: "SUCCEEDED"}
	}
	reservations, err := client.ListPendingQuotaReservations(ctx)
	if err != nil {
		t.Fatalf("list seeded reservations: %v", err)
	}
	var failingID, healthyID string
	for _, reservation := range reservations {
		if strings.HasSuffix(reservation.ProjectID, "record-fails") {
			failingID = reservation.ID
		}
		if strings.HasSuffix(reservation.ProjectID, "record-healthy") {
			healthyID = reservation.ID
		}
	}
	if failingID == "" || healthyID == "" {
		t.Fatalf("missing seeded reservation IDs: fail=%q healthy=%q", failingID, healthyID)
	}

	quotaStore := &failOneQuotaSettlementStore{pipelineQuotaStore: client, failID: failingID, err: errors.New("synthetic settlement failure")}
	h := New(localfs.New(t.TempDir()), client, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaStore(quotaStore)
	h.metadataTokenURL = "http://run.test/token"
	h.SetPipelineJobURL("http://run.test/job:run")
	h.httpClient = reconciliationExecutionHTTPClient(t, executions, http.StatusOK, "", nil)
	if err := h.ReconcilePipelineQuota(ctx); err == nil || !strings.Contains(err.Error(), "synthetic settlement failure") || !strings.Contains(err.Error(), failingID) {
		t.Fatalf("ReconcilePipelineQuota() error=%v; want the isolated failure with reservation correlation", err)
	}

	failed, exists, err := client.GetQuotaReservation(ctx, failingID)
	if err != nil || !exists || failed.SettlementStatus != "pending" {
		t.Fatalf("failed reservation=%+v exists=%t err=%v; want unchanged pending state", failed, exists, err)
	}
	healthy, exists, err := client.GetQuotaReservation(ctx, healthyID)
	if err != nil || !exists || healthy.SettlementStatus != "charged" || healthy.Outcome != "SUCCEEDED" {
		t.Fatalf("healthy reservation=%+v exists=%t err=%v; want settled despite earlier record failure", healthy, exists, err)
	}
	for _, reservationID := range []string{failingID, healthyID} {
		var ownerProject string
		for _, fixture := range executions {
			if fixture.reservationID == reservationID {
				ownerProject = fixture.projectID
				break
			}
		}
		quotaSnapshot, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(owner, ownerProject)).Get(ctx)
		if err != nil {
			t.Fatalf("read claim for %s: %v", reservationID, err)
		}
		active, _ := quotaSnapshot.Data()["active_reservation_id"].(string)
		if reservationID == healthyID && active == healthyID {
			t.Fatalf("healthy settlement retained active claim %q", active)
		}
		if reservationID == failingID && active != failingID {
			t.Fatalf("failed transaction changed old claim to %q", active)
		}
	}
}

type failOneQuotaSettlementStore struct {
	pipelineQuotaStore
	failID string
	err    error
}

func (s *failOneQuotaSettlementStore) SettleQuotaReservation(ctx context.Context, reservationID, executionStatus string) (string, error) {
	if reservationID == s.failID {
		return "", s.err
	}
	return s.pipelineQuotaStore.SettleQuotaReservation(ctx, reservationID, executionStatus)
}

func TestQuotaReconcilerSurfacesFirestoreIntentRequeueFailureAtLocalExit(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner := fmt.Sprintf("lwc484-requeue-%d", time.Now().UnixNano())
	projectID := "wiki"
	reservationID := fmt.Sprintf("lwc484-requeue-reservation-%d", time.Now().UnixNano())
	actualAttempt := "actual-attempt"
	reservationRef := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	if _, err := reservationRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "attempt_id": actualAttempt, "profile_revision": int64(1),
		"intent_status": "ready", "settlement_status": "pending", "execution_id": "",
	}); err != nil {
		t.Fatalf("seed durable ready intent: %v", err)
	}
	h := New(localfs.New(t.TempDir()), client, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaStore(&mismatchedAttemptQuotaStore{pipelineQuotaStore: client, reservationID: reservationID})
	h.SetLocalPipelineExecutor(&pipelineAutomationLocalExecutor{})
	if err := h.ReconcilePipelineQuota(ctx); err == nil || !strings.Contains(err.Error(), "owner or attempt identity mismatch") || !strings.Contains(err.Error(), reservationID) {
		t.Fatalf("ReconcilePipelineQuota() error=%v; want emulator transaction's persisted attempt mismatch", err)
	}
	work := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline", Revision: 1, ID: reservationID}
	if _, err := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(work)).Get(ctx); err == nil {
		t.Fatal("mismatched attempt created pipeline intent work")
	}
}

type mismatchedAttemptQuotaStore struct {
	pipelineQuotaStore
	reservationID string
}

func (s *mismatchedAttemptQuotaStore) ListPendingQuotaReservations(ctx context.Context) ([]scopedfirestore.QuotaReservation, error) {
	reservations, err := s.pipelineQuotaStore.ListPendingQuotaReservations(ctx)
	if err != nil {
		return nil, err
	}
	for i := range reservations {
		if reservations[i].ID == s.reservationID {
			reservations[i].AttemptID = "presented-stale-attempt"
		}
	}
	return reservations, nil
}

type selectedQuotaReservationStore struct {
	*scopedfirestore.Client
	allowed map[string]struct{}
}

func (s *selectedQuotaReservationStore) ListPendingQuotaReservations(ctx context.Context) ([]scopedfirestore.QuotaReservation, error) {
	reservations, err := s.Client.ListPendingQuotaReservations(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]scopedfirestore.QuotaReservation, 0, len(s.allowed))
	for _, reservation := range reservations {
		if _, ok := s.allowed[reservation.ID]; ok {
			filtered = append(filtered, reservation)
		}
	}
	return filtered, nil
}
