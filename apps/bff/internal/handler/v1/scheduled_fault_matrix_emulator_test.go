package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

type scheduledLinkFailureStore struct {
	*scopedfirestore.Client
	mu       sync.Mutex
	failNext bool
}

func (s *scheduledLinkFailureStore) LinkQuotaReservation(ctx context.Context, reservationID, executionID string) error {
	s.mu.Lock()
	fail := s.failNext
	s.failNext = false
	s.mu.Unlock()
	if fail {
		return errors.New("synthetic reservation link write failure")
	}
	return s.Client.LinkQuotaReservation(ctx, reservationID, executionID)
}

func TestScheduledInvokeFaultIsolationAndLinkReconciliationEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project  = "llm-wiki-cloud"
		database = "(default)"
		userID   = "lwc413-scheduled-fault-owner"
	)
	scope := fmt.Sprintf("lwc413-scheduled-fault-%d", time.Now().UnixNano())
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
			t.Errorf("clean scheduled fault emulator scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})

	const (
		unavailableProject = "a-unavailable"
		preinvokeProject   = "b-preinvoke"
		linkFailureProject = "c-link-failure"
		unaffectedProject  = "d-unaffected"
	)
	projectIDs := []string{unavailableProject, preinvokeProject, linkFailureProject, unaffectedProject}
	projects := scopedfirestore.Collection(fsClient.Raw(), "projects")
	for _, projectID := range projectIDs {
		_, err := projects.Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
			"user_id": userID, "project_id": projectID, "status": "ready",
		})
		if err != nil {
			t.Fatalf("seed ready Project %s: %v", projectID, err)
		}
	}

	localRoot := t.TempDir()
	localStore := localfs.New(localRoot)
	for _, projectID := range projectIDs {
		if _, err := localStore.WithScope(userID, projectID).WriteBytes(ctx, []byte("scheduled source"), "raw/source.md"); err != nil {
			t.Fatalf("seed local raw for %s: %v", projectID, err)
		}
	}
	unavailableScopeRoot := filepath.Join(localRoot, "users", userID, "projects", unavailableProject)
	annotationDir := filepath.Join(unavailableScopeRoot, "cache", "annotations")
	if err := os.MkdirAll(filepath.Dir(annotationDir), 0o755); err != nil {
		t.Fatalf("create inaccessible annotation parent: %v", err)
	}
	if err := os.Symlink("/dev/null", annotationDir); err != nil {
		t.Fatalf("create inaccessible annotation metadata fixture: %v", err)
	}

	fixture := newQuotaCloudRunFixture()
	fixture.projectsWithInvokeError = map[string]bool{preinvokeProject: true}
	wrappedStore := &scheduledLinkFailureStore{Client: fsClient, failNext: true}
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

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	sweep, err := h.RunScheduledPipelineSweep(ctx, now)
	if err != nil || sweep.Scanned != 4 || sweep.Accepted != 2 || sweep.Unavailable != 2 || sweep.Skipped != 0 {
		t.Fatalf("fault-isolated sweep=%+v err=%v; want two accepted and two isolated unavailable Projects", sweep, err)
	}
	fixture.mu.Lock()
	actualInvocations, invokeRequests := fixture.next, append([]map[string]string(nil), fixture.requests...)
	fixture.mu.Unlock()
	if actualInvocations != 2 || len(invokeRequests) != 3 {
		t.Fatalf("Cloud Run fixture execution/request counts=%d/%d; want two accepted executions and three attempts including definite pre-invoke rejection", actualInvocations, len(invokeRequests))
	}
	if invokeRequests[1][pipelineReservationEnvName] != scopedfirestore.ScheduledPipelineBudgetID(userID, linkFailureProject, pipelinequota.DayKeyUTC(now)) {
		t.Fatalf("link-failure invoke reservation=%q; want stable Project/day reservation", invokeRequests[1][pipelineReservationEnvName])
	}
	if invokeRequests[1][pipelineStageEnvName] != pipelineStageFull || invokeRequests[1]["CLEAN_REBUILD"] != "" {
		t.Fatalf("scheduled worker override=%v; want incremental mode and no clean rebuild", invokeRequests[1])
	}

	preinvokeID := scopedfirestore.ScheduledPipelineBudgetID(userID, preinvokeProject, pipelinequota.DayKeyUTC(now))
	preinvokeReservation, exists, err := fsClient.GetQuotaReservation(ctx, preinvokeID)
	if err != nil || !exists || preinvokeReservation.SettlementStatus != "released" || preinvokeReservation.ExecutionID != "" {
		t.Fatalf("confirmed pre-invoke reservation=%+v exists=%v err=%v; want safely released", preinvokeReservation, exists, err)
	}
	preinvokeStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, preinvokeProject, now)
	if err != nil || preinvokeStatus.BudgetUsed {
		t.Fatalf("pre-invoke scheduled budget=%+v err=%v; want available", preinvokeStatus, err)
	}
	unavailableStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, unavailableProject, now)
	if err != nil || unavailableStatus.SkipReason != "unavailable" {
		t.Fatalf("unavailable Project status=%+v err=%v; want per-Project unavailable", unavailableStatus, err)
	}

	linkReservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, linkFailureProject, pipelinequota.DayKeyUTC(now))
	linkReservation, exists, err := fsClient.GetQuotaReservation(ctx, linkReservationID)
	if err != nil || !exists || linkReservation.ExecutionID != "" || linkReservation.SettlementStatus != "pending" {
		t.Fatalf("accepted link-write-failure reservation=%+v exists=%v err=%v; want unresolved accepted claim", linkReservation, exists, err)
	}
	if err := h.ReconcilePipelineQuota(ctx); err != nil {
		t.Fatalf("reconcile provider execution after link write failure: %v", err)
	}
	linkReservation, exists, err = fsClient.GetQuotaReservation(ctx, linkReservationID)
	if err != nil || !exists || linkReservation.ExecutionID == "" || linkReservation.SettlementStatus != "pending" {
		t.Fatalf("reconciled link reservation=%+v exists=%v err=%v; want exact running execution linked", linkReservation, exists, err)
	}
	fixture.mu.Lock()
	actualInvocationsAfterReconcile := fixture.next
	fixture.mu.Unlock()
	if actualInvocationsAfterReconcile != 2 {
		t.Fatalf("reconciliation caused another provider invocation: %d, want unchanged count 2", actualInvocationsAfterReconcile)
	}
}
