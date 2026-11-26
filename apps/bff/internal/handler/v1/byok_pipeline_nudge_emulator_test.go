package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

func TestPipelineBYOKFailureSurvivesLaterPipelineStateAndProjectsToNudgeAPI(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := newContentContextEmulator(t)
	defer client.Close()

	owner, profileID := "byok-pipeline-owner", "byok-pipeline-profile"
	projectID := fmt.Sprintf("byok-pipeline-%d", time.Now().UnixNano())
	seedBYOKTestProject(t, ctx, client.Raw(), owner, projectID, "Pipeline Wiki", profileID)
	queryProjectID := projectID + "-query"
	seedBYOKTestProject(t, ctx, client.Raw(), owner, queryProjectID, "Query Wiki", profileID)
	repository := nudge.NewRepository(client.Raw())
	handler := &Handler{firestore: client}
	var snapshot pipelineEventSnapshot
	handler.pipelineEventSource = func(_ context.Context, _, currentProject string) (pipelineEventSnapshot, error) {
		if currentProject != projectID {
			return pipelineEventSnapshot{}, fmt.Errorf("unexpected test Project")
		}
		return snapshot, nil
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	byokFailure := byokPipelineRecord("byok-run-1", 1, "FAILED", "confirmed_not_published", started, "authentication")
	ordinaryFailure := byokPipelineRecord("ordinary-run-2", 2, "FAILED", "confirmed_not_published", started.Add(time.Second), "")
	refresh := func(records ...pipelineEventRunRecord) {
		t.Helper()
		byID := make(map[string]pipelineEventRunRecord, len(records))
		for _, record := range records {
			byID[record.id] = record
		}
		snapshot = pipelineEventSnapshot{ProjectID: projectID, Coverage: pipelineEventCoverage{State: "complete"},
			records: byID, candidates: scopedfirestore.PipelineEventCandidateSet{}}
		slot, err := repository.ReadSourceSlot(ctx, owner, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
		if err != nil {
			t.Fatalf("read Pipeline source slot: %v", err)
		}
		failures, notices := handler.reconcilePipelineProjectFailure(ctx, repository, owner, projectID, "Pipeline Wiki", slot)
		if len(failures) != 0 || len(notices) != 0 {
			t.Fatalf("complete Pipeline snapshot produced failures=%+v notices=%+v", failures, notices)
		}
	}

	// A later generic failure owns the Pipeline slot, while the earlier BYOK
	// event remains independently visible in the BYOK slot.
	refresh(byokFailure, ordinaryFailure)
	byokSlot, err := repository.ReadSourceSlot(ctx, owner, nudge.CategoryBYOKActionable, "byok:"+projectID)
	if err != nil || !byokSlot.Active || byokSlot.ProducerWitness.ExecutionID != byokFailure.id || byokSlot.ProducerWitness.ProjectPipelineSequence != 1 {
		t.Fatalf("earlier actionable Pipeline event was lost: slot=%+v err=%v", byokSlot, err)
	}
	pipelineSlot, err := repository.ReadSourceSlot(ctx, owner, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !pipelineSlot.Active || pipelineSlot.ProducerWitness.ExecutionID != ordinaryFailure.id {
		t.Fatalf("later generic Pipeline failure was not preserved: slot=%+v err=%v", pipelineSlot, err)
	}

	// A later confirmed publication clears only the Pipeline reminder.
	published := byokPipelineRecord("published-run-3", 3, "SUCCEEDED", "confirmed_published", started.Add(2*time.Second), "")
	refresh(byokFailure, ordinaryFailure, published)
	byokSlot, err = repository.ReadSourceSlot(ctx, owner, nudge.CategoryBYOKActionable, "byok:"+projectID)
	pipelineSlot, pipelineErr := repository.ReadSourceSlot(ctx, owner, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || pipelineErr != nil || !byokSlot.Active || pipelineSlot.Active || pipelineSlot.ProducerWitness.ExecutionID != published.id {
		t.Fatalf("publication changed independent BYOK state: BYOK=%+v Pipeline=%+v errors=%v/%v", byokSlot, pipelineSlot, err, pipelineErr)
	}

	// A failed BYOK persistence attempt must not prevent the normal Pipeline
	// reminder from recording its later failure.
	newBYOKFailure := byokPipelineRecord("byok-run-4", 4, "FAILED", "confirmed_not_published", started.Add(3*time.Second), "billing")
	newOrdinaryFailure := byokPipelineRecord("ordinary-run-5", 5, "FAILED", "confirmed_not_published", started.Add(4*time.Second), "")
	handler.firestore = nil
	refresh(newBYOKFailure, newOrdinaryFailure)
	pipelineSlot, err = repository.ReadSourceSlot(ctx, owner, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !pipelineSlot.Active || pipelineSlot.ProducerWitness.ExecutionID != newOrdinaryFailure.id {
		t.Fatalf("BYOK persistence failure interrupted Pipeline reminder: slot=%+v err=%v", pipelineSlot, err)
	}
	// Restore the test client for persisted API projection below.
	handler.firestore = client
	queryStarted := started.Add(5 * time.Second)
	if err := handler.recordBYOKActionableFailure(ctx, owner, queryProjectID, profileID, 1, 1, 0,
		"quota", "query", "", queryStarted); err != nil {
		t.Fatalf("persist synthetic actionable Query evidence: %v", err)
	}

	router := gin.New()
	router.GET("/api/v1/nudges", func(c *gin.Context) {
		c.Set("userID", owner)
		handler.GetNudges(c)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/nudges", nil))
	var view nudge.View
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatalf("GET Nudge UI view status=%d body=%s", response.Code, response.Body.String())
	}
	byProject := make(map[string]nudge.Slot)
	for _, item := range view.Items {
		if item.Category == nudge.CategoryBYOKActionable {
			for _, slot := range item.Observations {
				byProject[slot.Observation.ProjectID] = slot
			}
		}
	}
	if len(byProject) != 2 || byProject[projectID].Observation == nil || byProject[queryProjectID].Observation == nil {
		t.Fatalf("Query/Pipeline BYOK observations did not reach owner UI view independently: %+v", view)
	}
	for id, slot := range byProject {
		if len(slot.Observation.Actions) != 1 || slot.Observation.Actions[0].Target == nil ||
			slot.Observation.Actions[0].Target.ProjectID != id || slot.Observation.ProjectName == "" {
			t.Fatalf("UI action is not Project-scoped for %s: %+v", id, slot.Observation)
		}
	}
}

func seedBYOKTestProject(t *testing.T, ctx context.Context, client *firestore.Client, owner, projectID, projectName, profileID string) {
	t.Helper()
	projectRef := scopedfirestore.Collection(client, "projects").Doc(owner + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": owner, "project_id": projectID, "name": projectName}); err != nil {
		t.Fatalf("seed Project %s: %v", projectID, err)
	}
	profileRef := scopedfirestore.Collection(client, "llm_profiles").Doc(owner + "_" + profileID)
	if _, err := profileRef.Set(ctx, map[string]any{"owner_id": owner, "revision": int64(1), "enabled": true}); err != nil {
		t.Fatalf("seed personal Profile: %v", err)
	}
	bindingRef := projectRef.Collection("llm_profile_binding").Doc("current")
	if _, err := bindingRef.Set(ctx, map[string]any{"source": "personal", "profile_id": profileID, "profile_revision": int64(1), "revision": int64(1)}); err != nil {
		t.Fatalf("seed personal Profile binding: %v", err)
	}
}

func byokPipelineRecord(id string, sequence int64, status, publication string, started time.Time, reason string) pipelineEventRunRecord {
	var diagnostic *handlerapi.PipelineFailureDiagnostic
	if reason != "" {
		diagnostic = &handlerapi.PipelineFailureDiagnostic{Version: 1, Status: "failed", Stage: "synto_run",
			ErrorClass: "child_exit", Child: "run", BYOKFailureReason: reason}
	}
	response := &handlerapi.PipelineExecutionResponse{Name: id, Status: status, CompileMode: "full", Diagnostic: diagnostic}
	return pipelineEventRunRecord{id: id, status: status, startedAt: started, actualStarted: started,
		endedAt: started.Add(time.Minute), publication: publication, response: response,
		reservationID: fmt.Sprintf("reservation-%d", sequence), attemptID: fmt.Sprintf("attempt-%d", sequence),
		trigger: "manual", mode: "full", sequence: sequence, sequenceSet: true,
		profileID: "byok-pipeline-profile", profileRevision: 1, profileBindingRevision: 1}
}
