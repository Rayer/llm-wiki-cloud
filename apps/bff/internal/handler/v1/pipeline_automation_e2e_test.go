package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestRawSyncOverwriteReconcilesWhileGCSStagingIsPaused(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") || !requireLoopbackEmulator(t, "STORAGE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore and Storage emulators")
	}

	client := newOnboardingFirestore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner := fmt.Sprintf("lwc442-overwrite-%d", time.Now().UnixNano())
	projectID := "wiki"
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready", "auto_pipeline_enabled": true,
		"auto_pipeline_revision": int64(1), "pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineAutomationAdminRef(client.Raw()).Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatal(err)
	}

	adminStorage, err := cloudstorage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create loopback Storage client: %v", err)
	}
	bucketName := fmt.Sprintf("lwc442-source-%d", time.Now().UnixNano())
	bucket := adminStorage.Bucket(bucketName)
	if err := bucket.Create(ctx, "lwc442-local", nil); err != nil {
		_ = adminStorage.Close()
		t.Fatalf("create disposable Storage emulator bucket: %v", err)
	}
	root, err := gcs.NewClientWithScope(bucketName, fmt.Sprintf("lwc442-%d", time.Now().UnixNano()))
	if err != nil {
		_ = bucket.Delete(ctx)
		_ = adminStorage.Close()
		t.Fatalf("create scoped GCS root: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		objects := bucket.Objects(cleanupCtx, nil)
		for {
			attrs, nextErr := objects.Next()
			if errors.Is(nextErr, iterator.Done) {
				break
			}
			if nextErr != nil {
				t.Errorf("list Storage emulator test objects: %v", nextErr)
				break
			}
			if deleteErr := bucket.Object(attrs.Name).Generation(attrs.Generation).Delete(cleanupCtx); deleteErr != nil && !errors.Is(deleteErr, cloudstorage.ErrObjectNotExist) {
				t.Errorf("delete Storage emulator test object: %v", deleteErr)
			}
		}
		if err := bucket.Delete(cleanupCtx); err != nil && !errors.Is(err, cloudstorage.ErrBucketNotExist) {
			t.Errorf("delete Storage emulator test bucket: %v", err)
		}
		if err := root.Close(); err != nil {
			t.Errorf("close scoped GCS root: %v", err)
		}
		if err := adminStorage.Close(); err != nil {
			t.Errorf("close Storage emulator client: %v", err)
		}
	})

	h := New(root, client, nil, nil, nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	local := &pipelineAutomationLocalExecutor{}
	h.SetLocalPipelineExecutor(local)
	gin.SetMode(gin.TestMode)
	upload := func(body io.Reader, digest, expectedGeneration string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/raw-sync/upload?path=notes/overwrite.md", body)
		c.Request.Header.Set("X-Content-SHA256", digest)
		if expectedGeneration != "" {
			c.Request.Header.Set("X-Expected-Generation", expectedGeneration)
		}
		c.Set("userID", owner)
		c.Set("projectID", projectID)
		h.SyncRawUpload(c)
		return recorder
	}

	oldBody := []byte("# Original raw content\n")
	oldDigest := sha256.Sum256(oldBody)
	first := upload(strings.NewReader(string(oldBody)), hex.EncodeToString(oldDigest[:]), "")
	if first.Code != http.StatusOK {
		t.Fatalf("initial raw sync status=%d body=%s", first.Code, first.Body.String())
	}
	var initial struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil || initial.Generation == "" {
		t.Fatalf("initial raw sync response=%s decode=%v", first.Body.String(), err)
	}
	journal := h.sourceCommitJournal()
	laggedBody := []byte("# Storage committed before journal confirmation\n")
	laggedDigest := sha256.Sum256(laggedBody)
	laggedRef, err := journal.PrepareSourceCommit(ctx, sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}, sourceautomation.PrepareRequest{
		OperationID: "prior-storage-confirm-delayed", Entrypoint: "cli_sync", RawPath: "notes/overwrite.md",
		ExpectedSHA256: hex.EncodeToString(laggedDigest[:]), ExpectedStorageGeneration: initial.Generation,
	})
	if err != nil {
		t.Fatalf("prepare delayed-confirm raw sync: %v", err)
	}
	rawStore, ok := root.Scope(owner, projectID).(storage.RawSyncStore)
	if !ok {
		t.Fatal("GCS scope does not expose the raw sync writer contract")
	}
	laggedGeneration, err := rawStore.WriteSyncRawFile(ctx, "notes/overwrite.md", bytes.NewReader(laggedBody), initial.Generation, hex.EncodeToString(laggedDigest[:]))
	if err != nil || laggedGeneration == "" {
		t.Fatalf("write raw before delayed journal confirmation: generation=%q err=%v", laggedGeneration, err)
	}

	newBody := bytes.Repeat([]byte("new staged raw content\n"), 80_000)
	newDigest := sha256.Sum256(newBody)
	release := make(chan struct{})
	paused := make(chan struct{})
	reader := &pausedSyncBody{prefix: newBody[:1<<20], rest: newBody[1<<20:], paused: paused, release: release}
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondDone <- upload(reader, hex.EncodeToString(newDigest[:]), laggedGeneration)
	}()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatalf("raw sync writer did not pause during GCS staging: %v", ctx.Err())
	}

	conditionalWriter, ok := root.Scope(owner, projectID).(storage.ConditionalWriter)
	if !ok {
		t.Fatal("GCS scope does not expose the conditional raw read contract")
	}
	stored, generation, err := conditionalWriter.ReadFileWithGeneration(ctx, "raw/notes/overwrite.md")
	if err != nil || string(stored) != string(laggedBody) || fmt.Sprint(generation) != laggedGeneration {
		t.Fatalf("destination changed before staged write completed: bytes=%d generation=%d err=%v", len(stored), generation, err)
	}
	events, err := projectRef.Collection("pipeline_source_events").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("read prepared source events: %v", err)
	}
	var overwriteEvent string
	for _, snapshot := range events {
		if snapshot.Data()["expected_sha256"] == hex.EncodeToString(newDigest[:]) {
			overwriteEvent = snapshot.Ref.ID
			break
		}
	}
	if overwriteEvent == "" {
		t.Fatalf("prepared overwrite event was not stored: %v", events)
	}
	priorCommit, err := journal.ConfirmSourceCommit(ctx, laggedRef, sourceautomation.ConfirmRequest{Generation: laggedGeneration, Status: "replaced"})
	if err != nil || priorCommit.State != "committed" || priorCommit.SourceRevision != 2 {
		t.Fatalf("confirm prior storage write after next writer prepared: result=%+v err=%v", priorCommit, err)
	}
	reconcileWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "source_commit_reconcile", ID: overwriteEvent}
	reconcileRef := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(reconcileWork))
	workSnapshot, err := reconcileRef.Get(ctx)
	if err != nil {
		t.Fatalf("read overwrite reconciliation work: %v", err)
	}
	var work profileruntime.Work
	if err := workSnapshot.DataTo(&work); err != nil {
		t.Fatal(err)
	}
	work.Due = time.Now().Add(-time.Hour)
	if _, err := reconcileRef.Set(ctx, work); err != nil {
		t.Fatalf("make reconciliation work due: %v", err)
	}
	dispatcher := &ProfileDispatcher{Handler: h}
	if processed, err := dispatcher.Dispatch(ctx, 1); err != nil || processed != 1 {
		t.Fatalf("reconcile old raw while new upload is staged: processed=%d err=%v", processed, err)
	}
	workSnapshot, err = reconcileRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := workSnapshot.DataTo(&work); err != nil {
		t.Fatal(err)
	}
	if !work.Pending || work.Status != "waiting_manual" {
		t.Fatalf("prepared overwrite was not retained as pending: %+v", work)
	}
	eventSnapshot, err := projectRef.Collection("pipeline_source_events").Doc(overwriteEvent).Get(ctx)
	if err != nil || eventSnapshot.Data()["commit_state"] != "prepared" {
		t.Fatalf("prepared overwrite changed state after old raw read: snapshot=%v err=%v", eventSnapshot, err)
	}

	close(release)
	var second *httptest.ResponseRecorder
	select {
	case second = <-secondDone:
	case <-ctx.Done():
		t.Fatalf("raw sync overwrite did not finish: %v", ctx.Err())
	}
	if second.Code != http.StatusOK {
		t.Fatalf("staged overwrite status=%d body=%s", second.Code, second.Body.String())
	}
	var overwritten struct {
		Generation     string `json:"generation"`
		SourceRevision int64  `json:"source_revision"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &overwritten); err != nil || overwritten.Generation == "" || overwritten.SourceRevision != 3 {
		t.Fatalf("overwrite response=%s decode=%v", second.Body.String(), err)
	}
	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil || projectSnapshot.Data()["pipeline_source_revision"] != int64(3) {
		t.Fatalf("overwrite source revision=%v err=%v", projectSnapshot, err)
	}
	automationWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 3, ID: "source-3"}
	automationRef := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(automationWork))
	automationSnapshot, err := automationRef.Get(ctx)
	if err != nil || !automationSnapshot.Exists() {
		t.Fatalf("source revision 3 did not enqueue one automation wake: snapshot=%v err=%v", automationSnapshot, err)
	}
	if err := dispatcher.execute(ctx, h.profileRepository.(*firestoreProfileRepository), automationWork); err != nil {
		t.Fatalf("consume source revision 3 automation wake: %v", err)
	}
	works, err := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	var pipelineWork *profileruntime.Work
	automationWakes := 0
	for _, snapshot := range works {
		var queued profileruntime.Work
		if err := snapshot.DataTo(&queued); err != nil {
			t.Fatal(err)
		}
		if queued.UserID != owner || queued.ProjectID != projectID {
			continue
		}
		if queued.Kind == "pipeline_automation" && queued.ID == "source-3" {
			automationWakes++
		}
		if queued.Kind == "pipeline" {
			pipelineWork = &queued
		}
	}
	if automationWakes != 1 || pipelineWork == nil {
		t.Fatalf("consumer rebuild for source-3 missing or duplicated: wakes=%d pipeline=%+v", automationWakes, pipelineWork)
	}
	if err := dispatcher.execute(ctx, h.profileRepository.(*firestoreProfileRepository), *pipelineWork); err != nil {
		t.Fatalf("consume rebuilt pipeline intent: %v", err)
	}
	if local.starts != 1 || !strings.HasPrefix(local.reservationID, "auto-") {
		t.Fatalf("overwritten source started %d pipeline runs with reservation %q", local.starts, local.reservationID)
	}

	supersededBody := []byte("# Prepared content that will be superseded\n")
	supersededDigest := sha256.Sum256(supersededBody)
	supersededRef, err := journal.PrepareSourceCommit(ctx, sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}, sourceautomation.PrepareRequest{
		OperationID: "later-version-negative", Entrypoint: "cli_sync", RawPath: "notes/overwrite.md",
		ExpectedSHA256: hex.EncodeToString(supersededDigest[:]), ExpectedStorageGeneration: overwritten.Generation,
	})
	if err != nil {
		t.Fatalf("prepare superseded content: %v", err)
	}
	laterBody := []byte("# Later confirmed content\n")
	laterDigest := sha256.Sum256(laterBody)
	later := upload(strings.NewReader(string(laterBody)), hex.EncodeToString(laterDigest[:]), overwritten.Generation)
	if later.Code != http.StatusOK {
		t.Fatalf("later raw sync status=%d body=%s", later.Code, later.Body.String())
	}
	var laterCommit struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(later.Body.Bytes(), &laterCommit); err != nil || laterCommit.Generation == "" {
		t.Fatalf("later raw sync response=%s decode=%v", later.Body.String(), err)
	}
	result, err := journal.ReconcileSourceCommit(ctx, supersededRef)
	if err != nil || result.State != "aborted" {
		t.Fatalf("reconcile superseded prepared content: result=%+v err=%v", result, err)
	}
	supersededEvent, err := projectRef.Collection("pipeline_source_events").Doc(supersededRef.OperationID).Get(ctx)
	if err != nil || supersededEvent.Data()["abort_reason"] != "raw_superseded" {
		t.Fatalf("superseded event state=%v err=%v", supersededEvent, err)
	}

	failedBody := []byte("# Failed staged content\n")
	failedDigest := sha256.Sum256(failedBody)
	failedRef, err := journal.PrepareSourceCommit(ctx, sourceautomation.VerifiedProject{OwnerID: owner, ProjectID: projectID}, sourceautomation.PrepareRequest{
		OperationID: "failed-writer-negative", Entrypoint: "cli_sync", RawPath: "notes/overwrite.md",
		ExpectedSHA256: hex.EncodeToString(failedDigest[:]), ExpectedStorageGeneration: laterCommit.Generation,
	})
	if err != nil {
		t.Fatalf("prepare failed content: %v", err)
	}
	if result, err := journal.ReconcileSourceCommit(ctx, failedRef); !errors.Is(err, sourceautomation.ErrSourceCommitPending) || result.State != "pending" {
		t.Fatalf("unconfirmed failed writer did not remain bounded pending: result=%+v err=%v", result, err)
	}
	failedEventRef := projectRef.Collection("pipeline_source_events").Doc(failedRef.OperationID)
	if _, err := failedEventRef.Update(ctx, []cloudfirestore.Update{{Path: "reconcile_deadline", Value: time.Now().Add(-time.Second)}}); err != nil {
		t.Fatalf("advance failed writer deadline: %v", err)
	}
	if result, err := journal.ReconcileSourceCommit(ctx, failedRef); err != nil || result.State != "aborted" {
		t.Fatalf("expire failed writer without committing it: result=%+v err=%v", result, err)
	}
	projectSnapshot, err = projectRef.Get(ctx)
	if err != nil || projectSnapshot.Data()["pipeline_source_revision"] != int64(4) {
		t.Fatalf("negative cases changed source revision: project=%v err=%v", projectSnapshot, err)
	}
	failedWake := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 5, ID: "source-5"}
	failedWakeRef := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(failedWake))
	if snapshot, err := failedWakeRef.Get(ctx); err == nil && snapshot.Exists() {
		t.Fatalf("aborted source commits emitted a revision-5 wake: %v", snapshot.Data())
	} else if err != nil && status.Code(err) != codes.NotFound {
		t.Fatalf("read aborted source wake: %v", err)
	}
}

type pausedSyncBody struct {
	prefix  []byte
	rest    []byte
	paused  chan struct{}
	release <-chan struct{}
	waiting bool
}

func (r *pausedSyncBody) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	if r.rest != nil {
		if !r.waiting {
			close(r.paused)
			r.waiting = true
			<-r.release
		}
		n := copy(p, r.rest)
		r.rest = r.rest[n:]
		if len(r.rest) == 0 {
			r.rest = nil
		}
		return n, nil
	}
	return 0, io.EOF
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
