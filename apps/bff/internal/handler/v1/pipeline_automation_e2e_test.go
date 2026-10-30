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

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestPipelineAutomationRawSyncToLocalConsumer(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	owner, projectID := "automation-e2e-owner", "wiki"
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1),
		"pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineAutomationAdminRef(client.Raw()).Set(ctx, map[string]interface{}{
		"allow_auto_pipeline": true, "revision": int64(1),
	}); err != nil {
		t.Fatal(err)
	}

	root := localfs.New(t.TempDir())
	h := New(root, client, nil, nil, nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	local := &pipelineAutomationLocalExecutor{}
	h.SetLocalPipelineExecutor(local)

	body := []byte("# Local automation fixture\n\nNo model or cloud worker is used by this test.\n")
	digest := sha256.Sum256(body)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/raw-sync/upload?path=notes/fixture.md", strings.NewReader(string(body)))
	c.Request.Header.Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	c.Set("userID", owner)
	c.Set("projectID", projectID)
	h.SyncRawUpload(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("raw sync status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var upload struct {
		ProcessingStatus string `json:"processing_status"`
		SourceRevision   int64  `json:"source_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &upload); err != nil || upload.ProcessingStatus != "pending" || upload.SourceRevision != 1 {
		t.Fatalf("raw sync response=%+v decode=%v body=%s", upload, err, recorder.Body.String())
	}
	stored, err := root.Scope(owner, projectID).ReadFile(ctx, "raw/notes/fixture.md")
	if err != nil || string(stored) != string(body) {
		t.Fatalf("committed source=%q err=%v", stored, err)
	}

	repo := h.profileRepository.(*firestoreProfileRepository)
	dispatcher := &ProfileDispatcher{Handler: h}
	automationWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "source-1"}
	if err := dispatcher.execute(ctx, repo, automationWork); err != nil {
		t.Fatalf("consume source commit work: %v", err)
	}

	works, err := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	var pipelineWork profileruntime.Work
	for _, snapshot := range works {
		var work profileruntime.Work
		if err := snapshot.DataTo(&work); err != nil {
			t.Fatal(err)
		}
		if work.UserID == owner && work.ProjectID == projectID && work.Kind == "pipeline" {
			pipelineWork = work
		}
	}
	if pipelineWork.ID == "" {
		t.Fatalf("source consumer did not enqueue a pipeline intent; works=%v", works)
	}
	if err := dispatcher.execute(ctx, repo, pipelineWork); err != nil {
		t.Fatalf("consume local Pipeline intent: %v", err)
	}
	if local.starts != 1 || !strings.HasPrefix(local.reservationID, "auto-") {
		t.Fatalf("local pipeline starts=%d reservation=%q", local.starts, local.reservationID)
	}

	reservation, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(local.reservationID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Data()["trigger"] != "source_commit" || reservation.Data()["budget_class"] != "none" || reservation.Data()["source_revision"] != int64(1) {
		t.Fatalf("automatic reservation=%v", reservation.Data())
	}
	project, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if project.Data()["pipeline_automation_status"] != "running" {
		t.Fatalf("Project processing state=%v", project.Data())
	}
	if _, ok := root.Scope(owner, projectID).(storage.RawSyncStore); !ok {
		t.Fatal("local Project storage did not retain its RawSyncStore contract")
	}
}

type pipelineAutomationLocalExecutor struct {
	starts        int
	reservationID string
}

func (p *pipelineAutomationLocalExecutor) Start(_ context.Context, _, _, _ string, _ bool, reservationID string) (string, error) {
	p.starts++
	p.reservationID = reservationID
	return "local-automation-execution", nil
}

func (*pipelineAutomationLocalExecutor) Status(context.Context, string, string, string) (*handler.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*pipelineAutomationLocalExecutor) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

var _ LocalPipelineExecutor = (*pipelineAutomationLocalExecutor)(nil)

func TestPreparedSourceCommitRecoversThroughDurableDispatcher(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	owner, projectID := "automation-recovery-owner", "wiki"
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1),
		"pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineAutomationAdminRef(client.Raw()).Set(ctx, map[string]interface{}{
		"allow_auto_pipeline": true, "revision": int64(1),
	}); err != nil {
		t.Fatal(err)
	}

	root := localfs.New(t.TempDir())
	body := []byte("# Recovered source\n")
	digest := sha256.Sum256(body)
	operationID := "crash-before-confirm"
	verified := sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}
	journal := sourceautomation.NewJournal(client.Raw(), root)
	ref, err := journal.PrepareSourceCommit(ctx, verified, sourceautomation.PrepareRequest{
		OperationID: operationID, Entrypoint: "ui_upload", RawPath: "notes/recovered.md", ExpectedSHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("prepare source commit before simulated crash: %v", err)
	}
	eventRef := projectRef.Collection("pipeline_source_events").Doc(ref.OperationID)
	reconcileWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "source_commit_reconcile", ID: operationID}
	reconcileRef := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(reconcileWork))
	pipelineWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "source-1"}
	pipelineWorkRef := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(pipelineWork))
	pathDigest := sha256.Sum256([]byte("notes/recovered.md"))
	currentRef := projectRef.Collection("pipeline_source_current").Doc(hex.EncodeToString(pathDigest[:]))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = reconcileRef.Delete(cleanupCtx)
		_, _ = pipelineWorkRef.Delete(cleanupCtx)
		_, _ = eventRef.Delete(cleanupCtx)
		_, _ = currentRef.Delete(cleanupCtx)
		_, _ = pipelineAutomationAdminRef(client.Raw()).Delete(cleanupCtx)
		_, _ = projectRef.Delete(cleanupCtx)
	})

	// The first reconstructed dispatch sees no raw object and retains the work
	// without changing source revision or admitting a pipeline run.
	workSnapshot, err := reconcileRef.Get(ctx)
	if err != nil {
		t.Fatalf("read durable reconciliation work: %v", err)
	}
	var work profileruntime.Work
	if err := workSnapshot.DataTo(&work); err != nil {
		t.Fatal(err)
	}
	work.Due = time.Now().Add(-time.Second)
	if _, err := reconcileRef.Set(ctx, work); err != nil {
		t.Fatal(err)
	}
	firstHandler := New(root, client, nil, nil, nil, nil)
	firstDispatcher := &ProfileDispatcher{Handler: firstHandler}
	if processed, err := firstDispatcher.Dispatch(ctx, 8); err != nil || processed != 1 {
		t.Fatalf("missing raw recovery pass processed=%d err=%v", processed, err)
	}
	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projectSnapshot.Data()["pipeline_source_revision"] != int64(0) {
		t.Fatalf("missing raw object advanced source revision: %v", projectSnapshot.Data())
	}
	if _, err := pipelineWorkRef.Get(ctx); err == nil {
		t.Fatal("missing raw object admitted pipeline work")
	}

	writer := root.Scope(owner, projectID).(storage.ConditionalWriter)
	if _, err := writer.WriteFileIfGeneration(ctx, body, "raw/notes/recovered.md", 0); err != nil {
		t.Fatalf("persist raw object after simulated crash: %v", err)
	}
	workSnapshot, err = reconcileRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := workSnapshot.DataTo(&work); err != nil {
		t.Fatal(err)
	}
	work.Due = time.Now().Add(-time.Second)
	if _, err := reconcileRef.Set(ctx, work); err != nil {
		t.Fatal(err)
	}

	// A fresh Handler and dispatcher stand in for a restarted consumer. The
	// durable journal item alone must confirm the write and queue automation.
	restartedHandler := New(root, client, nil, nil, nil, nil)
	restartedDispatcher := &ProfileDispatcher{Handler: restartedHandler}
	if processed, err := restartedDispatcher.Dispatch(ctx, 8); err != nil || processed != 1 {
		t.Fatalf("recovery dispatch processed=%d err=%v", processed, err)
	}
	eventSnapshot, err := eventRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if eventSnapshot.Data()["commit_state"] != "committed" {
		t.Fatalf("source event state=%v", eventSnapshot.Data())
	}
	projectSnapshot, err = projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projectSnapshot.Data()["pipeline_source_revision"] != int64(1) {
		t.Fatalf("recovered source revision=%v", projectSnapshot.Data()["pipeline_source_revision"])
	}
	if _, err := pipelineWorkRef.Get(ctx); err != nil {
		t.Fatalf("recovery did not enqueue pipeline automation: %v", err)
	}
}

func TestAdminReopenRetriesFailedSourceWithoutProjectOrRawChanges(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	owner, projectID := "automation-admin-reopen-owner", "wiki"
	fs := client.Raw()
	projectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocID(owner, projectID))
	adminRef := pipelineAutomationAdminRef(fs)
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1),
		"pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRef.Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatal(err)
	}

	root := localfs.New(t.TempDir())
	h := New(root, client, nil, nil, nil, nil)
	local := &pipelineAutomationLocalExecutor{}
	h.SetLocalPipelineExecutor(local)
	body := []byte("# Admin reopen source\n")
	digest := sha256.Sum256(body)
	verified := sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}
	journal := sourceautomation.NewJournal(fs, root)
	ref, err := journal.PrepareSourceCommit(ctx, verified, sourceautomation.PrepareRequest{
		OperationID: "admin-reopen-source", Entrypoint: "ui_upload", RawPath: "notes/a.md", ExpectedSHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("prepare raw source: %v", err)
	}
	writer := root.Scope(owner, projectID).(storage.ConditionalWriter)
	generation, err := writer.WriteFileIfGeneration(ctx, body, "raw/notes/a.md", 0)
	if err != nil {
		t.Fatalf("write raw source: %v", err)
	}
	confirmed, err := journal.ConfirmSourceCommit(ctx, ref, sourceautomation.ConfirmRequest{Generation: fmt.Sprint(generation), Status: "created"})
	if err != nil || confirmed.SourceRevision != 1 {
		t.Fatalf("confirm raw source=%+v err=%v", confirmed, err)
	}
	rawStore := root.Scope(owner, projectID).(storage.RawSyncStore)
	fingerprint, err := sourceautomation.InventoryFingerprint(ctx, rawStore)
	if err != nil || fingerprint == "" {
		t.Fatalf("fingerprint raw inventory=%q err=%v", fingerprint, err)
	}
	rawBefore, generationBefore, err := writer.ReadFileWithGeneration(ctx, "raw/notes/a.md")
	if err != nil {
		t.Fatal(err)
	}

	firstID := pipelineAutomationReservationID(owner, projectID, fingerprint, 1, 1)
	_, reserved, err := client.ReserveQuotaForPipelineAutomation(ctx, firstID, owner, projectID, h.pipelineLimits(), time.Now().UTC(), false, false, 1, 0, 0,
		scopedfirestore.PipelineAutomationAdmission{SourceFingerprint: fingerprint, SourceRevision: 1, ProjectRevision: 1, AdminRevision: 1})
	if err != nil || !reserved {
		t.Fatalf("initial automatic reservation=%t err=%v", reserved, err)
	}
	if _, err := client.SettleQuotaReservation(ctx, firstID, "FAILED"); err != nil {
		t.Fatalf("settle initial failed reservation: %v", err)
	}

	// Use the same Admin update entrypoint as the PUT handler, then consume its
	// durable scan work before the queued Project admission.
	if _, err := updatePipelineAutomationAdmin(ctx, fs, false, 1); err != nil {
		t.Fatalf("disable Admin automation: %v", err)
	}
	if _, err := updatePipelineAutomationAdmin(ctx, fs, true, 2); err != nil {
		t.Fatalf("re-enable Admin automation: %v", err)
	}
	repo := h.profileRepository.(*firestoreProfileRepository)
	dispatcher := &ProfileDispatcher{Handler: h}
	if err := dispatcher.execute(ctx, repo, profileruntime.Work{
		UserID: "system", ProjectID: "automation", Kind: "pipeline_automation_scan", Revision: 3, ID: "scan-3",
	}); err != nil {
		t.Fatalf("execute Admin backlog scan: %v", err)
	}
	backlogWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "backlog-3-1"}
	if err := dispatcher.execute(ctx, repo, backlogWork); err != nil {
		t.Fatalf("execute scanned automatic admission: %v", err)
	}
	secondID := pipelineAutomationReservationID(owner, projectID, fingerprint, 1, 3)
	if _, exists, err := scopedfirestore.GetQuotaReservationFromFirestore(ctx, fs, secondID); err != nil || !exists {
		t.Fatalf("Admin reopen reservation exists=%t err=%v", exists, err)
	}
	if firstID == secondID {
		t.Fatal("Admin revision did not create a new automatic attempt identity")
	}
	if _, err := client.SettleQuotaReservation(ctx, secondID, "FAILED"); err != nil {
		t.Fatalf("settle reopened reservation as failed: %v", err)
	}
	_, retried, err := client.ReserveQuotaForPipelineAutomation(ctx, secondID, owner, projectID, h.pipelineLimits(), time.Now().UTC(), false, false, 1, 0, 0,
		scopedfirestore.PipelineAutomationAdmission{SourceFingerprint: fingerprint, SourceRevision: 1, ProjectRevision: 1, AdminRevision: 3})
	if err != nil || retried {
		t.Fatalf("same fingerprint retried again in the same Admin epoch: reserved=%t err=%v", retried, err)
	}

	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projectSnapshot.Data()["auto_pipeline_revision"] != int64(1) || projectSnapshot.Data()["pipeline_source_revision"] != int64(1) {
		t.Fatalf("Admin-only reopen changed Project/source revisions: %v", projectSnapshot.Data())
	}
	rawAfter, generationAfter, err := writer.ReadFileWithGeneration(ctx, "raw/notes/a.md")
	if err != nil || string(rawAfter) != string(rawBefore) || generationAfter != generationBefore {
		t.Fatalf("Admin-only reopen changed raw source: bytes=%q generation=%d err=%v", rawAfter, generationAfter, err)
	}
	fingerprintAfter, err := sourceautomation.InventoryFingerprint(ctx, rawStore)
	if err != nil || fingerprintAfter != fingerprint {
		t.Fatalf("Admin-only reopen changed raw inventory fingerprint: before=%q after=%q err=%v", fingerprint, fingerprintAfter, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminRef.Delete(cleanupCtx)
		_, _ = projectRef.Collection("pipeline_source_events").Doc(ref.OperationID).Delete(cleanupCtx)
		pathDigest := sha256.Sum256([]byte("notes/a.md"))
		_, _ = projectRef.Collection("pipeline_source_current").Doc(hex.EncodeToString(pathDigest[:])).Delete(cleanupCtx)
		_, _ = projectRef.Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fs, "pipeline_quota_reservations").Doc(firstID).Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fs, "pipeline_quota_reservations").Doc(secondID).Delete(cleanupCtx)
		works, _ := scopedfirestore.Collection(fs, profileruntime.WorkCollection).Documents(cleanupCtx).GetAll()
		for _, snapshot := range works {
			_, _ = snapshot.Ref.Delete(cleanupCtx)
		}
		_, _ = scopedfirestore.Collection(fs, pipelineAutomationScans).Doc("3").Delete(cleanupCtx)
		_, _ = scopedfirestore.Collection(fs, "pipeline_quota").Doc(scopedfirestore.QuotaDocID(owner, projectID)).Delete(cleanupCtx)
	})
}

func TestIdenticalSourcesInDifferentProjectsGetIndependentReservations(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	fs := client.Raw()
	owner := "automation-isolation-owner"
	projects := []string{"wiki-a", "wiki-b"}
	adminRef := pipelineAutomationAdminRef(fs)
	if _, err := adminRef.Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatal(err)
	}
	root := localfs.New(t.TempDir())
	h := New(root, client, nil, nil, nil, nil)
	h.SetLocalPipelineExecutor(&pipelineAutomationLocalExecutor{})
	journal := sourceautomation.NewJournal(fs, root)
	body := []byte("# Identical source in separate projects\n")
	digest := sha256.Sum256(body)
	references := make([]sourceautomation.Ref, 0, len(projects))
	projectRefs := make([]*cloudfirestore.DocumentRef, 0, len(projects))
	currentRefs := make([]*cloudfirestore.DocumentRef, 0, len(projects))
	fingerprints := make([]string, 0, len(projects))
	reservationIDs := make([]string, 0, len(projects))
	for _, projectID := range projects {
		projectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocID(owner, projectID))
		projectRefs = append(projectRefs, projectRef)
		if _, err := projectRef.Set(ctx, map[string]interface{}{
			"user_id": owner, "project_id": projectID, "status": "ready",
			"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1),
			"pipeline_source_revision": int64(0),
		}); err != nil {
			t.Fatal(err)
		}
		verified := sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}
		ref, err := journal.PrepareSourceCommit(ctx, verified, sourceautomation.PrepareRequest{
			OperationID: "identical-source", Entrypoint: "ui_upload", RawPath: "notes/shared.md", ExpectedSHA256: hex.EncodeToString(digest[:]),
		})
		if err != nil {
			t.Fatalf("prepare %s source: %v", projectID, err)
		}
		references = append(references, ref)
		writer := root.Scope(owner, projectID).(storage.ConditionalWriter)
		generation, err := writer.WriteFileIfGeneration(ctx, body, "raw/notes/shared.md", 0)
		if err != nil {
			t.Fatalf("write %s source: %v", projectID, err)
		}
		if result, err := journal.ConfirmSourceCommit(ctx, ref, sourceautomation.ConfirmRequest{Generation: fmt.Sprint(generation), Status: "created"}); err != nil || result.SourceRevision != 1 {
			t.Fatalf("confirm %s source=%+v err=%v", projectID, result, err)
		}
		fingerprint, err := sourceautomation.InventoryFingerprint(ctx, root.Scope(owner, projectID).(storage.RawSyncStore))
		if err != nil {
			t.Fatalf("fingerprint %s source: %v", projectID, err)
		}
		fingerprints = append(fingerprints, fingerprint)
		reservationIDs = append(reservationIDs, pipelineAutomationReservationID(owner, projectID, fingerprint, 1, 1))
		pathDigest := sha256.Sum256([]byte("notes/shared.md"))
		currentRefs = append(currentRefs, projectRef.Collection("pipeline_source_current").Doc(hex.EncodeToString(pathDigest[:])))
	}
	if fingerprints[0] == "" || fingerprints[0] != fingerprints[1] {
		t.Fatalf("identical source inventories differ: %v", fingerprints)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminRef.Delete(cleanupCtx)
		for i, ref := range references {
			_, _ = projectRefs[i].Collection("pipeline_source_events").Doc(ref.OperationID).Delete(cleanupCtx)
			_, _ = currentRefs[i].Delete(cleanupCtx)
			_, _ = projectRefs[i].Delete(cleanupCtx)
			_, _ = scopedfirestore.Collection(fs, "pipeline_quota_reservations").Doc(reservationIDs[i]).Delete(cleanupCtx)
			_, _ = scopedfirestore.Collection(fs, "pipeline_quota").Doc(scopedfirestore.QuotaDocID(owner, projects[i])).Delete(cleanupCtx)
		}
		works, _ := scopedfirestore.Collection(fs, profileruntime.WorkCollection).Documents(cleanupCtx).GetAll()
		for _, snapshot := range works {
			_, _ = snapshot.Ref.Delete(cleanupCtx)
		}
	})

	dispatcher := &ProfileDispatcher{Handler: h}
	if processed, err := dispatcher.Dispatch(ctx, 8); err != nil || processed != 2 {
		t.Fatalf("source-trigger dispatch processed=%d err=%v", processed, err)
	}
	for i, projectID := range projects {
		if _, exists, err := scopedfirestore.GetQuotaReservationFromFirestore(ctx, fs, reservationIDs[i]); err != nil || !exists {
			t.Fatalf("%s reservation exists=%t err=%v", projectID, exists, err)
		}
	}
	if reservationIDs[0] == reservationIDs[1] {
		t.Fatal("identical content in distinct Projects shared one reservation ID")
	}

	// A duplicate work item for the same Project sees its active reservation
	// and waits instead of entering quota or starting a second execution.
	repo := h.profileRepository.(*firestoreProfileRepository)
	duplicate := profileruntime.Work{UserID: owner, ProjectID: projects[0], Kind: "pipeline_automation", Revision: 1, ID: "duplicate-source-trigger"}
	if err := dispatcher.execute(ctx, repo, duplicate); !errors.Is(err, errProfileRuntimeWaiting) {
		t.Fatalf("duplicate same-Project automation work error=%v, want active reservation wait", err)
	}
	if _, exists, err := scopedfirestore.GetQuotaReservationFromFirestore(ctx, fs, reservationIDs[0]); err != nil || !exists {
		t.Fatalf("duplicate work lost original reservation: exists=%t err=%v", exists, err)
	}
}
