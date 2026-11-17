package v1

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/search"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

type scheduledEvidenceRootStore struct {
	store.RootStore
	receipts map[string][]byte
}

func (s *scheduledEvidenceRootStore) Scope(userID, projectID string) store.Store {
	return scheduledEvidenceProjectStore{Store: s.RootStore.Scope(userID, projectID), receipts: s.receipts}
}

type scheduledEvidenceProjectStore struct {
	store.Store
	receipts map[string][]byte
}

type scheduledLocalIncarnationStatus struct {
	response      *handlerapi.PipelineExecutionResponse
	reservationID string
	attemptID     string
}

func (*scheduledLocalIncarnationStatus) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", fmt.Errorf("unexpected local Pipeline start in status test")
}

func (s *scheduledLocalIncarnationStatus) Status(_ context.Context, _, _, _ string) (*handlerapi.PipelineExecutionResponse, error) {
	copy := *s.response
	return &copy, nil
}

func (s *scheduledLocalIncarnationStatus) StatusWithIncarnation(_ context.Context, _, _, _ string) (*handlerapi.PipelineExecutionResponse, string, string, error) {
	copy := *s.response
	return &copy, s.reservationID, s.attemptID, nil
}

func (*scheduledLocalIncarnationStatus) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s scheduledEvidenceProjectStore) ReadLocalPublicationReceipt(_ context.Context, executionID string) ([]byte, error) {
	data, ok := s.receipts[executionID]
	if !ok {
		return nil, store.ErrObjectNotExist
	}
	return data, nil
}

func TestScheduledStatusReadsExactExecutionEvidenceAfterLaterManualRunEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project   = "llm-wiki-cloud"
		database  = "(default)"
		userID    = "lwc413-scheduled-evidence-owner"
		projectID = "evidence-project"
	)
	scope := fmt.Sprintf("lwc413-scheduled-evidence-%d", time.Now().UnixNano())
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
			t.Errorf("clean scheduled-evidence fixture scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	fixture := newQuotaCloudRunFixture()
	root := &scheduledEvidenceRootStore{RootStore: localfs.New(t.TempDir()), receipts: map[string][]byte{}}
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	h.SetPipelineJobURL("http://run.test/job:run")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	dayKey := pipelinequota.DayKeyUTC(now)
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, dayKey)
	const scheduledAttemptID = "scheduled-attempt-a"
	_, reserved, err := fsClient.ReserveScheduledQuotaWithAttempt(ctx, reservationID, scheduledAttemptID, userID, projectID, h.pipelineLimits(), now,
		pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve scheduled evidence fixture: reserved=%t err=%v", reserved, err)
	}
	scheduledExecutionID := fixture.addExecutionWithAttempt(userID, projectID, reservationID, scheduledAttemptID, "SUCCEEDED")
	if err := fsClient.LinkQuotaReservationForAttempt(ctx, reservationID, scheduledAttemptID, scheduledExecutionID); err != nil {
		t.Fatalf("link scheduled execution: %v", err)
	}
	if err := fsClient.RecordScheduledPipelineAttempt(ctx, userID, projectID, dayKey, reservationID, scheduledAttemptID, scheduledExecutionID, "accepted", now); err != nil {
		t.Fatalf("record scheduled execution: %v", err)
	}
	manualExecutionID := fixture.addExecution(userID, projectID, "manual-later-reservation", "RUNNING")

	const generationID = "g_abc123"
	activeRevision := strings.Repeat("c", 64)
	receipt := profileruntime.CompileReceipt{
		UserID: userID, ProjectID: projectID, ExecutionID: scheduledExecutionID,
		ContentGeneration: generationID, ManifestGeneration: 1,
		ManifestSHA256: strings.Repeat("a", 64), CanonicalConceptsDigest: strings.Repeat("b", 64),
		ConsumedGuidanceKind: "active", ConsumedGuidanceRevision: &activeRevision,
	}
	stateRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(userID, projectID)).Collection("profile").Doc("state")
	if _, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(generationID).Set(ctx, receipt); err != nil {
		t.Fatalf("seed scheduled execution receipt: %v", err)
	}
	publication, err := localcloud.EncodePublicationReceipt(localcloud.PublicationReceipt{
		ExecutionID: scheduledExecutionID, GenerationID: generationID, ManifestGeneration: 1,
	})
	if err != nil {
		t.Fatalf("encode scheduled publication evidence: %v", err)
	}
	root.receipts[scheduledExecutionID] = publication

	latestManual, err := h.pipelineExecutionStatusForOwner(ctx, manualExecutionID, userID, projectID)
	if err != nil || latestManual == nil || shortCloudRunExecutionName(latestManual.Name, true) != manualExecutionID {
		t.Fatalf("later manual status=%+v err=%v; want exact manual execution B", latestManual, err)
	}
	scheduledStatus, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, projectID, now)
	if err != nil {
		t.Fatalf("load scheduled status: %v", err)
	}
	scheduledExecution := h.scheduledPipelineExecutionStatus(ctx, userID, projectID, &scheduledStatus)
	if scheduledExecution == nil || shortCloudRunExecutionName(scheduledExecution.Name, true) != scheduledExecutionID ||
		scheduledExecution.PublicationOutcome != "confirmed_published" || scheduledExecution.ConsumedGuidance == nil ||
		scheduledExecution.ConsumedGuidance.Status != "available" || scheduledExecution.ConsumedGuidance.Kind != "active" ||
		scheduledExecution.ConsumedGuidance.Revision == nil || *scheduledExecution.ConsumedGuidance.Revision != activeRevision ||
		scheduledExecution.ConsumedGuidance.ContentGeneration != generationID {
		t.Fatalf("scheduled evidence=%+v; want exact execution A publication and consumed-guidance receipt after later manual B", scheduledExecution)
	}
	wrongProject := h.scheduledPipelineExecutionStatus(ctx, userID, "other-project", &scheduledStatus)
	if wrongProject != nil {
		t.Fatalf("cross-Project scheduled evidence=%+v; want no evidence", wrongProject)
	}
}

func TestScheduledStatusReadsExactLocalExecutionIncarnationEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const project, database = "llm-wiki-cloud", "(default)"
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, projectID := "lwc493-local-status-"+unique, "local-status-"+unique
	scope := "lwc493-local-status-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create scoped loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := &scheduledEvidenceRootStore{RootStore: localfs.New(t.TempDir()), receipts: map[string][]byte{}}
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	statusReader := &scheduledLocalIncarnationStatus{
		response:      &handlerapi.PipelineExecutionResponse{Name: "local-exact-execution", Status: "SUCCEEDED"},
		reservationID: "", attemptID: "",
	}
	h.SetLocalPipelineExecutor(statusReader)
	day := time.Now().UTC()
	dayKey := pipelinequota.DayKeyUTC(day)
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, dayKey)
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, ref := range []*cloudfirestore.DocumentRef{
			scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID),
			scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_daily").Doc(reservationID),
			scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_status").Doc(scopedfirestore.QuotaDocID(userID, projectID)),
			scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, projectID)),
		} {
			_, _ = ref.Delete(cleanup)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close Firestore client: %v", err)
		}
	})
	attemptID, executionID := "local-attempt-"+unique, "local-exact-execution"
	_, reserved, err := fsClient.ReserveScheduledQuotaWithAttempt(ctx, reservationID, attemptID, userID, projectID,
		h.pipelineLimits(), day, pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve exact local scheduled attempt: reserved=%t err=%v", reserved, err)
	}
	if err := fsClient.LinkQuotaReservationForAttempt(ctx, reservationID, attemptID, executionID); err != nil {
		t.Fatalf("link exact local scheduled execution: %v", err)
	}
	if err := fsClient.RecordScheduledPipelineAttempt(ctx, userID, projectID, dayKey, reservationID, attemptID, executionID, "accepted", day); err != nil {
		t.Fatalf("record exact local scheduled attempt: %v", err)
	}
	statusReader.reservationID, statusReader.attemptID = reservationID, attemptID
	scheduled, err := fsClient.LoadScheduledPipelineStatus(ctx, userID, projectID, day)
	if err != nil {
		t.Fatalf("load exact local scheduled status: %v", err)
	}
	response := h.scheduledPipelineExecutionStatus(ctx, userID, projectID, &scheduled)
	if response == nil || response.Name != executionID || response.Status != "SUCCEEDED" {
		t.Fatalf("exact local scheduled execution=%+v; want current reservation and attempt", response)
	}
	statusReader.attemptID = "late-attempt-A"
	if response := h.scheduledPipelineExecutionStatus(ctx, userID, projectID, &scheduled); response != nil {
		t.Fatalf("late local execution from attempt A leaked into scheduled B status: %+v", response)
	}
}
