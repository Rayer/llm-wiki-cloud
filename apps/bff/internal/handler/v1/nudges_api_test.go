package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestNudgeAttentionAPIRejectsDirectUrgentReadAndDismiss(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID := "nudge-api-owner"
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPipelineFailure)
	category := nudge.Category{SchemaVersion: nudge.SchemaVersion, Category: nudge.CategoryPipelineFailure,
		AttentionID: "urgent-revision", AttentionState: nudge.AttentionUnread,
		MaterialBySource: map[string]string{"source": "urgent"}, SeverityBySource: map[string]nudge.Severity{"source": nudge.SeverityUrgent},
		ActiveCount: 1, UrgentCount: 1, UpdatedAt: time.Now().UTC()}
	if _, err := categoryRef.Set(context.Background(), category); err != nil {
		t.Fatal(err)
	}

	handler := &Handler{firestore: fsClient}
	router := gin.New()
	router.PATCH("/api/v1/nudges/:category/attention", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.PatchNudgeAttention(c)
	})
	for _, action := range []string{"read", "dismiss"} {
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/nudges/pipeline-failure/attention", strings.NewReader(`{"attention_id":"urgent-revision","action":"`+action+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"attention_not_dismissible"`) {
			t.Fatalf("direct urgent %s: status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/nudges/pipeline-failure/attention", strings.NewReader(`{"attention_id":"urgent-revision","action":"unread"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"attention_not_changeable"`) {
		t.Fatalf("direct urgent unread: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNudgeAttentionAPIAllowsOrdinaryUnread(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID := "nudge-api-ordinary-owner"
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPurposeGap)
	category := nudge.Category{SchemaVersion: nudge.SchemaVersion, Category: nudge.CategoryPurposeGap,
		AttentionID: "ordinary-revision", AttentionState: nudge.AttentionRead,
		MaterialBySource: map[string]string{"source": "purpose"}, SeverityBySource: map[string]nudge.Severity{"source": nudge.SeverityAttention},
		ActiveCount: 1, UpdatedAt: time.Now().UTC()}
	if _, err := categoryRef.Set(context.Background(), category); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{firestore: fsClient}
	router := gin.New()
	router.PATCH("/api/v1/nudges/:category/attention", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.PatchNudgeAttention(c)
	})
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/nudges/content-purpose-gap/attention", strings.NewReader(`{"attention_id":"ordinary-revision","action":"unread"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("ordinary unread: status=%d body=%s", response.Code, response.Body.String())
	}
	snapshot, err := categoryRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state := snapshot.Data()["attention_state"]; state != string(nudge.AttentionUnread) {
		t.Fatalf("ordinary unread state=%v", state)
	}
}

func TestGetNudgesPreservesNestedSDKObservationForActionResolver(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ownerID := "nudge-wire-owner"
	projectID := fmt.Sprintf("nudge-wire-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Named Project"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	defer func() { _, _ = projectRef.Delete(context.Background()) }()

	repository := nudge.NewRepository(fsClient.Raw())
	input := nudge.ObservationInput{
		Category: nudge.CategoryPipelineFailure, SourceKey: "pipeline-fail:" + projectID,
		ExpectedSourceRevision: 0, PayloadDigest: strings.Repeat("a", 64), AttentionMaterialRevision: "failed-run",
		ProducerWitness: nudge.ProducerWitness{Kind: "pipeline-run", ProjectID: projectID, ProjectPipelineSequence: 1,
			ReservationID: "reservation-1", AttemptID: "attempt-1", ExecutionID: "execution-1", Trigger: "manual", Mode: "full", TerminalClass: "failed"},
		Observation: nudge.Observation{ProjectID: projectID, ProjectName: "Named Project", ExecutionID: "execution-1",
			Title: "Pipeline did not publish", Body: "The run ended before publication.", Severity: nudge.SeverityAttention,
			Coverage: nudge.Coverage{}, Actions: []nudge.Action{{ID: "copy-open-run", Kind: "copy-then-link", Label: "Copy and open run",
				CopyText: "execution-1", Target: &nudge.Target{Kind: "pipeline-run", ProjectID: projectID, ExecutionID: "execution-1"}}}},
	}
	if applied, err := repository.ApplyObservation(ctx, ownerID, input); err != nil || !applied {
		t.Fatalf("persist source observation=%t err=%v", applied, err)
	}

	handler := &Handler{firestore: fsClient}
	handler.pipelineEventSource = func(_ context.Context, _, currentProject string) (pipelineEventSnapshot, error) {
		snapshot := makePipelineEventSnapshot(currentProject, map[string]pipelineEventRunRecord{}, nil, nil, false, pipelineEventGlobalLimit, 0, false, nil, 0)
		snapshot.records = map[string]pipelineEventRunRecord{}
		return snapshot, nil
	}
	router := gin.New()
	router.GET("/api/v1/nudges", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.GetNudges(c)
	})
	router.POST("/api/v1/nudges/reconcile", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.ReconcileNudges(c)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/nudges", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/nudges status=%d body=%s", response.Code, response.Body.String())
	}
	var body nudge.View
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode actual Nudge HTTP JSON: %v", err)
	}
	var wireBody map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &wireBody); err != nil {
		t.Fatalf("decode raw Nudge HTTP JSON: %v", err)
	}
	items, _ := wireBody["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("actual HTTP JSON items = %#v", items)
	}
	firstItem, _ := items[0].(map[string]interface{})
	slots, _ := firstItem["observations"].([]interface{})
	if len(slots) != 1 {
		t.Fatalf("actual HTTP JSON observations = %#v", slots)
	}
	firstSlot, _ := slots[0].(map[string]interface{})
	wireObservation, _ := firstSlot["observation"].(map[string]interface{})
	coverage, _ := wireObservation["coverage"].(map[string]interface{})
	wireActions, _ := wireObservation["actions"].([]interface{})
	if len(wireActions) != 1 {
		t.Fatalf("actual HTTP JSON actions = %#v", wireActions)
	}
	wireAction, _ := wireActions[0].(map[string]interface{})
	wireTarget, _ := wireAction["target"].(map[string]interface{})
	if coverage["total_concepts"] != float64(0) || coverage["selected_concepts"] != float64(0) || coverage["sampled"] != false ||
		coverage["title_truncated_concepts"] != float64(0) || coverage["body_truncated_concepts"] != float64(0) ||
		wireObservation["project_name"] != "Named Project" || wireAction["copy_text"] != "execution-1" ||
		wireTarget["project_id"] != projectID || wireTarget["execution_id"] != "execution-1" {
		t.Fatalf("actual HTTP JSON lost names, zero coverage, copy text or target: %+v", wireBody)
	}
	if len(body.Items) != 1 || len(body.Items[0].Observations) != 1 {
		t.Fatalf("HTTP projection lost the persisted observation: %+v", body)
	}
	observation := body.Items[0].Observations[0].Observation
	if observation == nil || observation.ProjectID != projectID || observation.ProjectName != "Named Project" ||
		observation.Coverage != (nudge.Coverage{}) || len(observation.Actions) != 1 ||
		observation.Actions[0].CopyText != "execution-1" || observation.Actions[0].Target == nil ||
		observation.Actions[0].Target.ProjectID != projectID || observation.Actions[0].Target.ExecutionID != "execution-1" {
		t.Fatalf("HTTP JSON dropped name, zero coverage, copy text or target: %+v", observation)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/nudges/reconcile", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var reconciled nudge.View
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &reconciled) != nil || !reconciled.Partial ||
		len(reconciled.Failures) != 2 || len(reconciled.Items) != 1 {
		t.Fatalf("real reconcile HTTP response did not preserve source qualification and item: status=%d body=%s", response.Code, response.Body.String())
	}
	classes := map[string]bool{}
	for _, failure := range reconciled.Failures {
		classes[failure.SafeClass] = true
	}
	if !classes["pipeline_history_missing"] || !classes["release_source_unavailable"] {
		t.Fatalf("reconcile did not report unavailable current Pipeline evidence and missing Release source: %+v", reconciled.Failures)
	}
}

func TestGetNudgesRevalidatesPurposeAgainstPinnedPublication(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ownerID, projectID := "purpose-http-owner", fmt.Sprintf("purpose-http-%d", time.Now().UnixNano())
	projectName, description, summary := "Purpose Project", "Manual purpose", "Published overview"
	generation, conceptsDigest, summaryInputDigest := "published-G1", strings.Repeat("a", 64), strings.Repeat("b", 64)
	summaryHash := sha256.Sum256([]byte(summary))
	summaryTextDigest := hex.EncodeToString(summaryHash[:])
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	contextRef := projectRef.Collection("content_context")
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": projectName,
		"description": description, "description_revision": int64(4)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = contextRef.Doc("state").Delete(context.Background())
		_, _ = contextRef.Doc("declaration").Delete(context.Background())
		_, _ = contextRef.Doc("comparison").Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()
	if _, err := contextRef.Doc("state").Set(ctx, map[string]interface{}{
		"latest_publication": map[string]interface{}{"generation": generation, "concepts_sha256": conceptsDigest},
		"last_good": map[string]interface{}{"source_generation": "summary-G1", "concepts_sha256": conceptsDigest,
			"input_digest": summaryInputDigest, "text": summary},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := contextRef.Doc("declaration").Set(ctx, map[string]interface{}{
		"owner_id": ownerID, "project_id": projectID, "declaration_id": "declaration-1", "pair_key": "pair-1",
		"publication_generation": generation, "concepts_sha256": conceptsDigest, "summary_source_generation": "summary-G1",
		"summary_input_digest": summaryInputDigest, "summary_text_digest": summaryTextDigest, "description": description,
		"description_revision": int64(4),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := contextRef.Doc("comparison").Set(ctx, map[string]interface{}{
		"owner_id": ownerID, "project_id": projectID, "declaration_id": "declaration-1", "pair_key": "pair-1",
		"publication_generation": generation, "concepts_digest": conceptsDigest, "summary_source_generation": "summary-G1",
		"summary_input_digest": summaryInputDigest, "summary_text_digest": summaryTextDigest, "description_revision": int64(4),
	}); err != nil {
		t.Fatal(err)
	}
	witness := nudge.ProducerWitness{Kind: "purpose-comparison", ProjectID: projectID, AttemptID: "attempt-1", ConfirmationID: "confirmation-1",
		PairKey: "pair-1", DescriptionRevision: 4, PublicationGeneration: generation, ConceptsDigest: conceptsDigest,
		SummaryInputDigest: summaryInputDigest, AttemptStartedAt: time.Now().UTC().Add(-time.Minute)}
	observation := nudge.Observation{ProjectID: projectID, ProjectName: projectName, Title: "Purpose and overview differ", Body: "Clear conflict",
		DeclaredPurpose: description, PublishedSummary: summary, Severity: nudge.SeverityAttention,
		Actions: []nudge.Action{{ID: "copy-description", Kind: "copy-then-link", Label: "Copy overview and open " + projectName + " description settings",
			CopyText: summary, Target: &nudge.Target{Kind: "project-description", ProjectID: projectID}}}}
	slot := nudge.Slot{SchemaVersion: nudge.SchemaVersion, SourceKey: "content-purpose-gap:" + projectID,
		SourceRevision: 1, ProducerWitness: witness, Active: true, PayloadDigest: strings.Repeat("c", 64),
		AttentionMaterialRevision: "purpose-conflict", Observation: &observation, ObservedAt: time.Now().UTC(),
		LastAppliedConfirmationID: witness.ConfirmationID}
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPurposeGap)
	category := nudge.Category{SchemaVersion: nudge.SchemaVersion, Category: nudge.CategoryPurposeGap, AttentionID: "attention-1",
		AttentionState: nudge.AttentionUnread, MaterialBySource: map[string]string{slot.SourceKey: slot.AttentionMaterialRevision},
		SeverityBySource: map[string]nudge.Severity{slot.SourceKey: nudge.SeverityAttention}, ActiveCount: 1, UpdatedAt: time.Now().UTC()}
	if _, err := categoryRef.Set(ctx, category); err != nil {
		t.Fatal(err)
	}
	slotDigest := sha256.Sum256([]byte(slot.SourceKey))
	slotRef := categoryRef.Collection("observations").Doc(hex.EncodeToString(slotDigest[:]))
	if _, err := slotRef.Set(ctx, slot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = slotRef.Delete(context.Background())
		_, _ = categoryRef.Delete(context.Background())
	}()

	root := newContentContextTestRoot(t, storage.QueryGenerationIdentity{
		ProjectID: projectID, GenerationID: generation, ConceptsDigest: conceptsDigest,
	}, nil)
	h := &Handler{store: root, firestore: fsClient}
	read := func() (nudge.View, int, string) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/nudges", nil)
		c.Set("userID", ownerID)
		h.GetNudges(c)
		var view nudge.View
		_ = json.Unmarshal(recorder.Body.Bytes(), &view)
		return view, recorder.Code, recorder.Body.String()
	}
	view, statusCode, body := read()
	if statusCode != http.StatusOK || len(view.Items) != 1 || len(view.Items[0].Observations) != 1 ||
		view.Items[0].Observations[0].Observation.Stale || len(view.Items[0].Observations[0].Observation.Actions) != 1 {
		t.Fatalf("current Project and publication were not projected: status=%d view=%+v body=%s", statusCode, view, body)
	}

	root.identity.GenerationID = "published-G2"
	view, statusCode, body = read()
	if statusCode != http.StatusOK || len(view.Items) != 1 || !view.Items[0].Observations[0].Observation.Stale ||
		len(view.Items[0].Observations[0].Observation.Actions) != 0 {
		t.Fatalf("changed publication retained current purpose action: status=%d view=%+v body=%s", statusCode, view, body)
	}
	root.identity.GenerationID = generation
	root.identityErr = errors.New("publication read unavailable")
	view, statusCode, body = read()
	if statusCode != http.StatusOK || !view.Partial || len(view.Items) != 1 || !view.Items[0].Observations[0].Observation.Stale ||
		len(view.Items[0].Observations[0].Observation.Actions) != 0 {
		t.Fatalf("publication read failure was misrepresented as current or recovered: status=%d view=%+v body=%s", statusCode, view, body)
	}
	root.identityErr = nil
	view, statusCode, body = read()
	if statusCode != http.StatusOK || view.Partial || len(view.Items) != 1 || view.Items[0].Observations[0].Observation.Stale ||
		len(view.Items[0].Observations[0].Observation.Actions) != 1 {
		t.Fatalf("read-only freshness probe mutated stored purpose state: status=%d view=%+v body=%s", statusCode, view, body)
	}
}
