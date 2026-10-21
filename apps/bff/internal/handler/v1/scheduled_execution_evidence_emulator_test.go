package v1

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
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
	_, reserved, err := fsClient.ReserveScheduledQuota(ctx, reservationID, userID, projectID, h.pipelineLimits(), now,
		pipelinequota.ModeIncremental, false, false, 1, 0, 0)
	if err != nil || !reserved {
		t.Fatalf("reserve scheduled evidence fixture: reserved=%t err=%v", reserved, err)
	}
	scheduledExecutionID := fixture.addExecution(userID, projectID, reservationID, "SUCCEEDED")
	if err := fsClient.LinkQuotaReservation(ctx, reservationID, scheduledExecutionID); err != nil {
		t.Fatalf("link scheduled execution: %v", err)
	}
	if err := fsClient.RecordScheduledPipelineAttempt(ctx, userID, projectID, dayKey, reservationID, scheduledExecutionID, "accepted", now); err != nil {
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
