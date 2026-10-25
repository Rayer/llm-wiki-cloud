package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localpipeline"
)

func TestLocalPipelineEventsBoundedHistoryAndExactIntentOverlay(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") || !requireLoopbackEmulator(t, "STORAGE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore and Storage emulators")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	project, scope := "lwc428-local-events-test", fmt.Sprintf("lwc428-local-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create isolated Firestore emulator client: %v", err)
	}
	storageClient, err := gcs.NewClientWithScope("lwc428-local-events-test", scope)
	if err != nil {
		_ = fsClient.Close()
		t.Fatalf("create loopback Storage client: %v", err)
	}
	manager, err := localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: "unused-worker",
		Project: project, Bucket: "lwc428-local-events-test", Database: "(default)", Scope: scope,
		WorkDir: t.TempDir(),
	})
	if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("create local Pipeline status reader: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := manager.Close(cleanupCtx); err != nil {
			t.Errorf("close local Pipeline status reader: %v", err)
		}
		if err := storageClient.Close(); err != nil {
			t.Errorf("close loopback Storage client: %v", err)
		}
		for index := 0; index < pipelineEventHistoryLimit+1; index++ {
			_, _ = scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(fmt.Sprintf("history-%02d", index)).Delete(cleanupCtx)
		}
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "executions").Doc("known-older-run").Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc("known-reservation").Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Delete(cleanupCtx)
		_ = fsClient.Close()
	})
	now := time.Now().UTC()
	for index := 0; index < pipelineEventHistoryLimit+1; index++ {
		runID := fmt.Sprintf("history-%02d", index)
		started := now.Add(-time.Duration(index+1) * time.Minute)
		if _, err := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(runID).Set(ctx, map[string]any{
			"user_id": "user-a", "project_id": "project-a", "execution_id": runID,
			"native": true, "task_type": "pipeline", "status": "RUNNING",
			"started_at": started.Add(-time.Second), "actual_started_at": started,
		}); err != nil {
			t.Fatalf("seed local history run %s: %v", runID, err)
		}
	}
	knownStarted := now.Add(-24 * time.Hour)
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc("known-older-run").Set(ctx, map[string]any{
		"user_id": "user-a", "project_id": "project-a", "execution_id": "known-older-run",
		"native": true, "task_type": "pipeline", "status": "RUNNING",
		"started_at": knownStarted.Add(-time.Second), "actual_started_at": knownStarted,
	}); err != nil {
		t.Fatalf("seed exact known local run: %v", err)
	}
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Set(ctx, map[string]any{
		"active_reservation_id": "known-reservation", "last_pipeline_intent_id": "known-reservation",
	}); err != nil {
		t.Fatalf("seed local active intent pointer: %v", err)
	}
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc("known-reservation").Set(ctx, map[string]any{
		"user_id": "user-a", "project_id": "project-a", "attempt_id": "known-reservation",
		"execution_id": "known-older-run", "intent_status": "compiling", "settlement_status": "pending", "trigger": "manual",
	}); err != nil {
		t.Fatalf("seed local active reservation: %v", err)
	}
	h := &Handler{firestore: fsClient, localPipeline: manager}
	snapshot, err := h.readLocalPipelineEventSnapshot(ctx, "user-a", "project-a")
	if err != nil {
		t.Fatalf("read bounded local Pipeline event snapshot: %v", err)
	}
	if snapshot.Coverage.State != "complete" || !snapshot.Coverage.HistoryTruncated || snapshot.Coverage.GlobalScanLimit != pipelineEventGlobalLimit || snapshot.Coverage.GlobalScanned != 0 {
		t.Fatalf("local bounded-query coverage=%+v", snapshot.Coverage)
	}
	if len(snapshot.Runs) != pipelineEventHistoryLimit || len(snapshot.KnownRuns) != 1 || snapshot.KnownRuns[0].RunID != "known-older-run" {
		t.Fatalf("bounded local history/exact overlay runs=%d known=%+v", len(snapshot.Runs), snapshot.KnownRuns)
	}
	if snapshot.OldestPosition == nil || snapshot.OldestPosition.RunID == "known-older-run" {
		t.Fatalf("exact local overlay widened history position: %+v", snapshot.OldestPosition)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := h.readLocalPipelineEventSnapshot(canceled, "user-a", "project-a"); err == nil {
		t.Fatal("local Firestore source failure returned an empty successful snapshot")
	}
}

func TestCloudPipelineEventsBoundedGlobalScanAndExactRunOverlay(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	project, scope := "lwc428-event-source-test", fmt.Sprintf("lwc428-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create isolated emulator Firestore client: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range []string{"exact-run", "exact-list-run"} {
			_, _ = scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(id).Delete(cleanupCtx)
		}
		for _, id := range []string{"res-exact", "res-list"} {
			_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(id).Delete(cleanupCtx)
		}
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_scheduled_status").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Delete(cleanupCtx)
		_ = fsClient.Close()
	})

	includeOwnedInList := false
	wrongOwner := false
	var listCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture-token"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/executions/exact-run") || strings.HasSuffix(r.URL.Path, "/executions/exact-list-run") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			ownerProject := "project-a"
			if wrongOwner {
				ownerProject = "foreign-project"
			}
			writeCloudEventExecution(w, id, "user-a", ownerProject, "2026-10-25T01:00:00Z", "")
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/executions") {
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("pageToken")
		pageNumber := 1
		if page != "" {
			_, _ = fmt.Sscanf(page, "page-%d", &pageNumber)
		}
		listCalls++
		executions := make([]cloudRunExecution, 0, 20)
		for index := 0; index < 20; index++ {
			id := fmt.Sprintf("foreign-%d-%02d", pageNumber, index)
			executions = append(executions, cloudEventExecution(id, "other-user", "other-project", "2026-10-25T00:00:00Z", ""))
		}
		if includeOwnedInList && pageNumber == 1 {
			executions[0] = cloudEventExecution("exact-list-run", "user-a", "project-a", "2026-10-25T01:00:00Z", "")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cloudRunExecutionsResponse{Executions: executions, NextPageToken: fmt.Sprintf("page-%d", pageNumber+1)})
	}))
	t.Cleanup(provider.Close)

	newHandler := func() *Handler {
		h := &Handler{firestore: fsClient, metadataTokenURL: provider.URL + "/token", httpClient: provider.Client()}
		h.SetPipelineJobURL(provider.URL + "/jobs/pipeline:run")
		return h
	}
	seedCandidate := func(reservationID, executionID string) {
		if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Set(ctx, map[string]any{"active_reservation_id": reservationID, "last_pipeline_intent_id": reservationID}); err != nil {
			t.Fatalf("seed scoped quota pointer: %v", err)
		}
		if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID).Set(ctx, map[string]any{
			"user_id": "user-a", "project_id": "project-a", "attempt_id": "attempt-" + reservationID,
			"execution_id": executionID, "intent_status": "ready", "settlement_status": "pending", "trigger": "manual",
		}); err != nil {
			t.Fatalf("seed durable Project intent: %v", err)
		}
	}

	t.Run("outside global sample stays visible and reports partial history", func(t *testing.T) {
		seedCandidate("res-exact", "exact-run")
		listCalls = 0
		includeOwnedInList = false
		wrongOwner = false
		snapshot, err := newHandler().readCloudPipelineEventSnapshot(ctx, "user-a", "project-a")
		if err != nil {
			t.Fatalf("read bounded Cloud Run event snapshot: %v", err)
		}
		if listCalls != pipelineEventPageLimit || snapshot.Coverage.GlobalScanned != pipelineEventGlobalLimit || snapshot.Coverage.State != "partial" || !snapshot.Coverage.HistoryTruncated {
			t.Fatalf("global bound/coverage list_calls=%d coverage=%+v", listCalls, snapshot.Coverage)
		}
		if len(snapshot.Runs) != 0 || len(snapshot.KnownRuns) != 1 || snapshot.KnownRuns[0].RunID != "exact-run" || snapshot.OldestPosition != nil || snapshot.ObservedHighWatermark != nil {
			t.Fatalf("exact known execution outside sample lost or widened history: %+v", snapshot)
		}
		if snapshot.Coverage.KnownRunIDs[0] != "exact-run" || len(snapshot.Coverage.UnresolvedKnownRunIDs) != 0 {
			t.Fatalf("known exact identity coverage=%+v", snapshot.Coverage)
		}
	})

	t.Run("owned list row and exact identity deduplicate", func(t *testing.T) {
		seedCandidate("res-list", "exact-list-run")
		listCalls = 0
		includeOwnedInList = true
		wrongOwner = false
		snapshot, err := newHandler().readCloudPipelineEventSnapshot(ctx, "user-a", "project-a")
		if err != nil {
			t.Fatalf("read deduplicated Cloud Run event snapshot: %v", err)
		}
		if len(snapshot.Runs) != 1 || snapshot.Runs[0].RunID != "exact-list-run" || len(snapshot.KnownRuns) != 0 {
			t.Fatalf("list/exact source duplicated or misplaced run: runs=%+v known=%+v", snapshot.Runs, snapshot.KnownRuns)
		}
		if len(snapshot.Runs[0].Events) != 1 || snapshot.Runs[0].Events[0].EventID != "exact-list-run:started" {
			t.Fatalf("deduplicated run phases=%+v", snapshot.Runs[0].Events)
		}
	})

	t.Run("mismatched exact owner remains unresolved", func(t *testing.T) {
		seedCandidate("res-exact", "exact-run")
		listCalls = 0
		includeOwnedInList = false
		wrongOwner = true
		snapshot, err := newHandler().readCloudPipelineEventSnapshot(ctx, "user-a", "project-a")
		if err != nil {
			t.Fatalf("read unresolved Cloud Run event snapshot: %v", err)
		}
		if snapshot.Coverage.State != "partial" || len(snapshot.Coverage.UnresolvedKnownRunIDs) != 1 || snapshot.Coverage.UnresolvedKnownRunIDs[0] != "exact-run" || len(snapshot.KnownRuns) != 0 {
			t.Fatalf("mismatched exact identity was trusted or silently omitted: %+v", snapshot)
		}
	})

	t.Run("zero owned matches at cap is not a complete empty history", func(t *testing.T) {
		listCalls = 0
		includeOwnedInList = false
		wrongOwner = false
		_, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID("user-a", "project-a")).Delete(ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := newHandler().readCloudPipelineEventSnapshot(ctx, "user-a", "project-a")
		if err != nil {
			t.Fatalf("read empty capped Cloud Run event snapshot: %v", err)
		}
		if snapshot.Coverage.State != "partial" || !snapshot.Coverage.HistoryTruncated || len(snapshot.Runs) != 0 || snapshot.OldestPosition != nil || snapshot.ObservedHighWatermark != nil {
			t.Fatalf("capped empty read falsely claims complete/no-run: %+v", snapshot)
		}
	})
}

func TestCloudPipelineEventOutcomeUsesOwnerDetailAndPublicationEvidenceEmulator(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") || !requireLoopbackEmulator(t, "STORAGE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore and Storage emulators")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const userID, projectID = "lwc428-cloud-event-owner", "pipeline-event-project"
	project, scope := "lwc428-cloud-event-publication", fmt.Sprintf("lwc428-cloud-events-%d", time.Now().UnixNano())
	bucketName := fmt.Sprintf("lwc428-event-publication-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	adminClient, err := cloudstorage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create loopback Storage admin client: %v", err)
	}
	if err := adminClient.Bucket(bucketName).Create(ctx, project, nil); err != nil {
		_ = adminClient.Close()
		t.Fatalf("create disposable Storage emulator bucket: %v", err)
	}
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		_ = adminClient.Bucket(bucketName).Delete(ctx)
		_ = adminClient.Close()
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClientWithScope(bucketName, scope)
	if err != nil {
		_ = fsClient.Close()
		_ = adminClient.Bucket(bucketName).Delete(ctx)
		_ = adminClient.Close()
		t.Fatalf("create loopback Storage client: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, reservationID := range []string{"published-reservation", "absent-reservation", "unknown-reservation"} {
			_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID).Delete(cleanupCtx)
		}
		_, _ = scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, projectID)).Delete(cleanupCtx)
		if _, err := storageClient.DeleteProjectPrefix(cleanupCtx, userID, projectID); err != nil {
			t.Errorf("delete loopback publication fixture scope: %v", err)
		}
		if err := adminClient.Bucket(bucketName).Delete(cleanupCtx); err != nil {
			t.Errorf("delete disposable Storage emulator bucket: %v", err)
		}
		if err := storageClient.Close(); err != nil {
			t.Errorf("close loopback Storage client: %v", err)
		}
		if err := adminClient.Close(); err != nil {
			t.Errorf("close loopback Storage admin client: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
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
			execution := cloudEventExecution(runID, userID, projectID, "2026-10-25T12:00:00Z", "2026-10-25T12:01:00Z")
			execution.RunningCount = 0
			execution.FailedCount = 1
			execution.CompletionStatus = "FAILED"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(execution)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(provider.Close)
	h := &Handler{store: storageClient, firestore: fsClient, metadataTokenURL: provider.URL + "/token", httpClient: provider.Client()}
	h.SetPipelineJobURL(provider.URL + "/jobs/pipeline:run")
	projectStore := storageClient.WithScope(userID, projectID)
	committed, err := localcloud.EncodePublicationReceipt(localcloud.PublicationReceipt{
		ExecutionID: "published-run", GenerationID: "g_abc123", ManifestGeneration: 1,
	})
	if err != nil {
		t.Fatalf("encode committed publication receipt: %v", err)
	}
	if _, err := projectStore.WriteBytes(ctx, committed, "cache/local-pipeline-published-run.commit.json"); err != nil {
		t.Fatalf("seed actual committed publication receipt: %v", err)
	}
	cleanupDiagnostic := []byte(`{"version":1,"status":"failed","stage":"lease_cleanup","error_class":"io","execution":"published-run","message":"lease cleanup failed after publication"}`)
	if _, err := projectStore.WriteBytes(ctx, cleanupDiagnostic, "cache/pipeline-published-run.failure.json"); err != nil {
		t.Fatalf("seed post-publication cleanup diagnostic: %v", err)
	}
	diagnostic := []byte(`{"version":1,"status":"failed","stage":"postprocess","error_class":"io","execution":"absent-run","message":"postprocess cleanup failed"}`)
	if _, err := projectStore.WriteBytes(ctx, diagnostic, "cache/pipeline-absent-run.failure.json"); err != nil {
		t.Fatalf("seed confirmed absence diagnostic: %v", err)
	}

	for _, tc := range []struct {
		reservationID, executionID, wantPublication, wantOutcome string
		storeUnavailable                                         bool
	}{
		{"published-reservation", "published-run", "confirmed_published", "succeeded", false},
		{"absent-reservation", "absent-run", "confirmed_not_published", "failed", false},
		{"unknown-reservation", "unknown-run", "unknown", "publication_pending", true},
	} {
		t.Run(tc.executionID, func(t *testing.T) {
			if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(userID, projectID)).Set(ctx, map[string]any{
				"active_reservation_id": tc.reservationID, "last_pipeline_intent_id": tc.reservationID,
			}); err != nil {
				t.Fatalf("seed bounded intent pointer: %v", err)
			}
			if _, err := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(tc.reservationID).Set(ctx, map[string]any{
				"user_id": userID, "project_id": projectID, "attempt_id": tc.reservationID,
				"execution_id": tc.executionID, "intent_status": "ready", "settlement_status": "pending", "trigger": "manual",
			}); err != nil {
				t.Fatalf("seed scoped durable execution identity: %v", err)
			}
			if tc.storeUnavailable {
				h.store = nil
			} else {
				h.store = storageClient
			}
			snapshot, err := h.readCloudPipelineEventSnapshot(ctx, userID, projectID)
			if err != nil {
				t.Fatalf("read Cloud Run event feed: %v", err)
			}
			if len(snapshot.KnownRuns) != 1 || snapshot.KnownRuns[0].RunID != tc.executionID || len(snapshot.KnownRuns[0].Events) != 2 {
				t.Fatalf("owner-checked exact terminal run missing: %+v", snapshot)
			}
			terminal := snapshot.KnownRuns[0].Events[1]
			if terminal.Publication != tc.wantPublication || terminal.Outcome != tc.wantOutcome || terminal.Result == nil || terminal.Result.Status != "FAILED" {
				t.Fatalf("provider status/publication semantics=%+v; want publication=%s outcome=%s with raw FAILED evidence", terminal, tc.wantPublication, tc.wantOutcome)
			}
			if tc.executionID == "absent-run" && (terminal.FailureReason != "postprocess cleanup failed" || terminal.DiagnosticState != "available") {
				t.Fatalf("confirmed non-publication lost the separate cleanup diagnostic: %+v", terminal)
			}
			if tc.executionID == "published-run" && (terminal.FailureReason != "lease cleanup failed after publication" || terminal.DiagnosticState != "available") {
				t.Fatalf("confirmed publication lost the post-publication cleanup diagnostic: %+v", terminal)
			}
		})
	}
}

func cloudEventExecution(id, userID, projectID, start, completion string) cloudRunExecution {
	return cloudRunExecution{
		Name:      "projects/test/locations/test/jobs/pipeline/executions/" + id,
		StartTime: start, CompletionTime: completion, RunningCount: 1,
		Template: cloudRunExecutionTemplate{Containers: []cloudRunContainer{{Env: []cloudRunEnv{
			{Name: "USER_ID", Value: userID}, {Name: "PROJECT_ID", Value: projectID}, {Name: "TASK_TYPE", Value: "pipeline"},
		}}}},
	}
}

func writeCloudEventExecution(w http.ResponseWriter, id, userID, projectID, start, completion string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cloudEventExecution(id, userID, projectID, start, completion))
}
