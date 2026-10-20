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

func (s *scheduledPreinvokeStoreFailures) ReleaseQuotaReservationIfAttempt(ctx context.Context, reservationID, attemptID string) (bool, error) {
	s.mu.Lock()
	fail := s.failRelease
	s.failRelease = false
	s.mu.Unlock()
	if fail {
		return false, errors.New("fixture preinvoke reservation release failure")
	}
	return s.Client.ReleaseQuotaReservationIfAttempt(ctx, reservationID, attemptID)
}

type scheduledAttemptReleaseResult struct {
	released bool
	err      error
}

type scheduledAttemptReleaseGate struct {
	*scopedfirestore.Client
	targetAttemptID string
	entered         chan struct{}
	resume          chan struct{}
	result          chan scheduledAttemptReleaseResult
	once            sync.Once
}

func (s *scheduledAttemptReleaseGate) ReleaseQuotaReservationIfAttempt(ctx context.Context, reservationID, attemptID string) (bool, error) {
	if attemptID == s.targetAttemptID {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.resume:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	released, err := s.Client.ReleaseQuotaReservationIfAttempt(ctx, reservationID, attemptID)
	if attemptID == s.targetAttemptID {
		s.result <- scheduledAttemptReleaseResult{released: released, err: err}
	}
	return released, err
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

func TestScheduledConfirmedNotInvokedMarkerCannotReleaseReusedDailyReservationEmulator(t *testing.T) {
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
	firstAttemptID := reservation.AttemptID
	if firstAttemptID == "" {
		t.Fatal("first scheduled reservation has no attempt incarnation")
	}
	confirmed, err := h.hasConfirmedPipelineInvokeFailure(ctx, reservation)
	if err != nil || !confirmed {
		t.Fatalf("confirmed-not-invoked marker=%t err=%v; want durable recovery evidence", confirmed, err)
	}
	gate := &scheduledAttemptReleaseGate{
		Client: fsClient, targetAttemptID: firstAttemptID,
		entered: make(chan struct{}), resume: make(chan struct{}),
		result: make(chan scheduledAttemptReleaseResult, 1),
	}
	h.SetPipelineQuotaStore(gate)
	staleReconcile := make(chan error, 1)
	go func() { staleReconcile <- h.ReconcilePipelineQuota(ctx) }()
	select {
	case <-gate.entered:
	case staleErr := <-staleReconcile:
		t.Fatalf("reconciler finished before entering attempt-bound release: %v", staleErr)
	case <-ctx.Done():
		t.Fatal("timed out waiting for stale reconciler to reach transactional release")
	}
	// A concurrent reconciler completes release for attempt A while the first
	// reconciler is paused after reading A's evidence but before its transaction.
	released, err := fsClient.ReleaseQuotaReservationIfAttempt(ctx, reservationID, firstAttemptID)
	if err != nil || !released {
		close(gate.resume)
		t.Fatalf("competing release of first attempt released=%t err=%v; want true", released, err)
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

	// Reuse the stable same-day reservation ID after the first attempt was
	// conclusively released, then lose the response after the provider creates
	// a real execution. The first attempt's marker must not release this claim.
	h.httpClient = &http.Client{Transport: acceptedResponseLostTransport{next: roundTripFunc(fixture.roundTrip)}}
	second, err := h.RunScheduledPipelineSweep(ctx, now.Add(5*time.Minute))
	if err != nil || second.Accepted != 0 || second.Unavailable != 1 {
		t.Fatalf("same-day second sweep=%+v err=%v; want accepted response classified unknown", second, err)
	}
	fixture.mu.Lock()
	invocations = fixture.next
	fixture.mu.Unlock()
	if invocations != 1 {
		t.Fatalf("provider invocation count after second attempt=%d; want one real execution", invocations)
	}
	reservation, found, err = fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.ExecutionID != "" || reservation.SettlementStatus != "pending" {
		t.Fatalf("second attempt reservation=%+v found=%t err=%v; want new unresolved claim", reservation, found, err)
	}
	secondAttemptID := reservation.AttemptID
	if secondAttemptID == "" || secondAttemptID == firstAttemptID {
		t.Fatalf("same-day retry attempt IDs first=%q second=%q; want distinct nonempty incarnations", firstAttemptID, secondAttemptID)
	}
	close(gate.resume)
	if err := <-staleReconcile; err != nil {
		t.Fatalf("stale reconciler returned error: %v", err)
	}
	staleRelease := <-gate.result
	if staleRelease.err != nil || staleRelease.released {
		t.Fatalf("stale attempt release result=%+v; want atomic no-op after reservation reuse", staleRelease)
	}
	reservation, found, err = fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.AttemptID != secondAttemptID || reservation.ExecutionID != "" || reservation.SettlementStatus != "pending" {
		t.Fatalf("reservation after stale reconciler=%+v found=%t err=%v; want second claim still pending", reservation, found, err)
	}

	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile second accepted response loss: %v", err)
	}
	reservation, found, err = fsClient.GetQuotaReservation(ctx, reservationID)
	if err != nil || !found || reservation.AttemptID != secondAttemptID || reservation.ExecutionID != "fixture-execution-1" || reservation.SettlementStatus != "pending" {
		t.Fatalf("second attempt reservation after reconcile=%+v found=%t err=%v; old confirmed-not-invoked marker must not release it", reservation, found, err)
	}
	for _, mode := range []pipelinequota.ExecutionMode{pipelinequota.ModeIncremental, pipelinequota.ModeFull} {
		_, reserved, err := fsClient.ReserveQuota(ctx, "manual-after-stale-marker-"+string(mode), userID, projectID,
			h.pipelineLimits(), now.Add(6*time.Minute), mode, mode == pipelinequota.ModeFull, false, 1, 0, 0)
		if err != nil || reserved {
			t.Fatalf("manual %s reservation=%t err=%v; recovered unknown scheduled claim must exclude it", mode, reserved, err)
		}
	}
	third, err := h.RunScheduledPipelineSweep(ctx, now.Add(6*time.Minute))
	if err != nil || third.Accepted != 0 {
		t.Fatalf("same-day third sweep=%+v err=%v; must not invoke after recovered unknown attempt", third, err)
	}
	fixture.mu.Lock()
	invocations = fixture.next
	fixture.mu.Unlock()
	if invocations != 1 {
		t.Fatalf("provider invocation count after replay/manual/full=%d; want exactly one", invocations)
	}
}
