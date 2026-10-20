package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

type acceptedResponseLostTransport struct{ next http.RoundTripper }

func (t acceptedResponseLostTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil || request.Method != http.MethodPost || request.URL.Path != "/job:run" || response.StatusCode != http.StatusOK {
		return response, err
	}
	_ = response.Body.Close()
	return testHTTPResponse(http.StatusServiceUnavailable, `{"error":"fixture lost accepted response"}`), nil
}

type scheduledPreinvokeStoreFailures struct {
	*scopedfirestore.Client
	mu          sync.Mutex
	failCheck   bool
	failRelease bool
}

func (s *scheduledPreinvokeStoreFailures) RecordScheduledPipelineCheck(ctx context.Context, status scopedfirestore.ScheduledPipelineStatus) error {
	s.mu.Lock()
	fail := s.failCheck
	s.failCheck = false
	s.mu.Unlock()
	if fail {
		return errors.New("fixture scheduled check write failure")
	}
	return s.Client.RecordScheduledPipelineCheck(ctx, status)
}

func (s *scheduledPreinvokeStoreFailures) ReleaseQuotaReservation(ctx context.Context, reservationID string) error {
	s.mu.Lock()
	fail := s.failRelease
	s.failRelease = false
	s.mu.Unlock()
	if fail {
		return errors.New("fixture preinvoke reservation release failure")
	}
	return s.Client.ReleaseQuotaReservation(ctx, reservationID)
}

func TestScheduledAcceptedInvocationWith503RemainsClaimedAndReconcilesEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project   = "llm-wiki-cloud"
		database  = "(default)"
		userID    = "lwc413-accepted-503-owner"
		projectID = "accepted-response-lost"
	)
	scope := fmt.Sprintf("lwc413-accepted-503-%d", time.Now().UnixNano())
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
			t.Errorf("clean accepted-503 fixture scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})

	_, err = scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "status": "ready",
	})
	if err != nil {
		t.Fatalf("seed ready Project: %v", err)
	}
	localStore := localfs.New(t.TempDir())
	if _, err := localStore.WithScope(userID, projectID).WriteBytes(ctx, []byte("scheduled content"), "raw/source.md"); err != nil {
		t.Fatalf("seed pending raw: %v", err)
	}
	fixture := newQuotaCloudRunFixture()
	inner := roundTripFunc(fixture.roundTrip)
	h := New(localStore, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com")
	h.SetPipelineJobURL("http://run.test/job:run")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: acceptedResponseLostTransport{next: inner}}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	first, err := h.RunScheduledPipelineSweep(ctx, now)
	if err != nil || first.Accepted != 0 || first.Unavailable != 1 {
		t.Fatalf("first sweep=%+v err=%v; want accepted response classified unknown/unavailable", first, err)
	}
	fixture.mu.Lock()
	invocations := fixture.next
	fixture.mu.Unlock()
	if invocations != 1 {
		t.Fatalf("provider invocation count=%d; want actual accepted execution count 1", invocations)
	}
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, pipelinequota.DayKeyUTC(now))
	reservation, found, err := fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.ExecutionID != "" || reservation.SettlementStatus != "pending" {
		t.Fatalf("ambiguous reservation=%+v found=%t err=%v; want unresolved claim held", reservation, found, err)
	}

	for _, mode := range []pipelinequota.ExecutionMode{pipelinequota.ModeIncremental, pipelinequota.ModeFull} {
		_, reserved, err := fsClient.ReserveQuota(ctx, "manual-after-503-"+string(mode), userID, projectID,
			h.pipelineLimits(), now, mode, mode == pipelinequota.ModeFull, false, 1, 0, 0)
		if err != nil || reserved {
			t.Fatalf("manual %s reservation=%t err=%v; unresolved scheduled claim must exclude it", mode, reserved, err)
		}
	}
	second, err := h.RunScheduledPipelineSweep(ctx, now.Add(5*time.Minute))
	if err != nil || second.Accepted != 0 {
		t.Fatalf("same-day replay sweep=%+v err=%v; want no second accepted invocation", second, err)
	}
	fixture.mu.Lock()
	invocations = fixture.next
	fixture.mu.Unlock()
	if invocations != 1 {
		t.Fatalf("provider invocation count after replay/manual/full=%d; want exactly 1", invocations)
	}

	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile execution created before lost 503 response: %v", err)
	}
	reservation, found, err = fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.ExecutionID != "fixture-execution-1" || reservation.SettlementStatus != "pending" {
		t.Fatalf("reconciled reservation=%+v found=%t err=%v; want exact accepted execution linked", reservation, found, err)
	}
	_, manualDay, _, err := fsClient.LoadQuotaState(ctx, userID, projectID)
	if err != nil || manualDay != "" {
		t.Fatalf("scheduled unknown attempt changed manual quota day=%q err=%v", manualDay, err)
	}
	fixture.mu.Lock()
	invocations = fixture.next
	fixture.mu.Unlock()
	if invocations != 1 {
		t.Fatalf("provider invocation count after reconciliation=%d; want exactly 1", invocations)
	}
}

func TestScheduledConfirmedNotInvokedMarkerRecoversAfterWriteAndReleaseFailuresEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project   = "llm-wiki-cloud"
		database  = "(default)"
		userID    = "lwc413-preinvoke-recovery-owner"
		projectID = "preinvoke-check-write-failure"
	)
	scope := fmt.Sprintf("lwc413-preinvoke-recovery-%d", time.Now().UnixNano())
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
			t.Errorf("clean preinvoke-recovery fixture scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	_, err = scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "status": "ready",
	})
	if err != nil {
		t.Fatalf("seed ready Project: %v", err)
	}
	localStore := localfs.New(t.TempDir())
	if _, err := localStore.WithScope(userID, projectID).WriteBytes(ctx, []byte("pending source"), "raw/source.md"); err != nil {
		t.Fatalf("seed pending raw: %v", err)
	}
	fixture := newQuotaCloudRunFixture()
	wrappedStore := &scheduledPreinvokeStoreFailures{Client: fsClient, failCheck: true, failRelease: true}
	h := New(localStore, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaStore(wrappedStore)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com")
	h.SetPipelineJobURL("http://run.test/job:run")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	sweep, err := h.RunScheduledPipelineSweep(ctx, now)
	if err != nil || sweep.Unavailable != 1 || sweep.Accepted != 0 {
		t.Fatalf("preinvoke failure sweep=%+v err=%v; want isolated failure before provider invoke", sweep, err)
	}
	fixture.mu.Lock()
	invocations := fixture.next
	fixture.mu.Unlock()
	if invocations != 0 {
		t.Fatalf("provider invocation count=%d; want zero before recovery", invocations)
	}
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, pipelinequota.DayKeyUTC(now))
	reservation, found, err := fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.ExecutionID != "" || reservation.SettlementStatus != "pending" {
		t.Fatalf("pre-recovery reservation=%+v found=%t err=%v; want pending until durable evidence is reconciled", reservation, found, err)
	}
	confirmed, err := h.hasConfirmedPipelineInvokeFailure(ctx, userID, projectID, reservationID)
	if err != nil || !confirmed {
		t.Fatalf("confirmed-not-invoked marker=%t err=%v; want durable recovery evidence", confirmed, err)
	}
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile confirmed not-invoked evidence: %v", err)
	}
	reservation, found, err = fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.SettlementStatus != "released" || reservation.ExecutionID != "" {
		t.Fatalf("recovered reservation=%+v found=%t err=%v; want released with no execution", reservation, found, err)
	}
	fixture.mu.Lock()
	invocations = fixture.next
	fixture.mu.Unlock()
	if invocations != 0 {
		t.Fatalf("provider invocation count after recovery=%d; want zero", invocations)
	}
}
