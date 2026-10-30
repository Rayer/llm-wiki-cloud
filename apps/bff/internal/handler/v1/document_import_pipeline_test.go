package v1

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type documentImportPipelineRoot struct {
	storage.RootStore
	project storage.Store
}

func (r *documentImportPipelineRoot) Scope(string, string) storage.Store { return r.project }

type documentImportPipelineStore struct {
	storage.Store
	conditional storage.ConditionalWriter
	receipts    map[string][]byte
}

func (s *documentImportPipelineStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	return s.conditional.ReadFileWithGeneration(ctx, path)
}

func (s *documentImportPipelineStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, generation int64) (int64, error) {
	return s.conditional.WriteFileIfGeneration(ctx, data, path, generation)
}

func (s *documentImportPipelineStore) ReadLocalPublicationReceipt(_ context.Context, executionID string) ([]byte, error) {
	data, ok := s.receipts[executionID]
	if !ok {
		return nil, storage.ErrObjectNotExist
	}
	return append([]byte(nil), data...), nil
}

func (*documentImportPipelineStore) CurrentManifest(context.Context) (generation.Manifest, int64, bool, error) {
	return generation.Manifest{}, 0, false, nil
}

type documentImportPipelineStatus struct{ value string }

func (*documentImportPipelineStatus) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", errors.New("unexpected Pipeline start in status projection test")
}

func (p *documentImportPipelineStatus) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	return &handlerapi.PipelineExecutionResponse{Status: p.value}, nil
}

func (*documentImportPipelineStatus) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestDocumentImportPipelineAssociationAndPublicationProjection(t *testing.T) {
	f := newRuntimeFixture(t)
	userID, projectID := f.user, f.project
	base := localfs.New(t.TempDir())
	localProject := base.Scope(userID, projectID)
	conditional := localProject.(storage.ConditionalWriter)
	projectStore := &documentImportPipelineStore{Store: localProject, conditional: conditional, receipts: map[string][]byte{}}
	f.dispatcher.Handler.store = &documentImportPipelineRoot{RootStore: base, project: projectStore}

	matchingData := []byte("# imported note\n\nmatching source generation\n")
	matchingPath := "documents/imported.md"
	matchingGeneration, err := conditional.WriteFileIfGeneration(f.ctx, matchingData, "raw/"+matchingPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	staleData := []byte("old source generation\n")
	stalePath := "documents/replaced.md"
	staleGeneration, err := conditional.WriteFileIfGeneration(f.ctx, staleData, "raw/"+stalePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conditional.WriteFileIfGeneration(f.ctx, []byte("new source generation\n"), "raw/"+stalePath, staleGeneration); err != nil {
		t.Fatal(err)
	}
	writeRecord := func(id string, sourceRevision int64, path, digest string, objectGeneration int64) *firestore.DocumentRef {
		t.Helper()
		record := documentimport.Record{
			SchemaVersion: documentimport.SchemaVersion, OwnerID: userID, ProjectID: projectID, DocumentID: id,
			AttemptID: id + "-a1", OriginalFilename: "import.txt", Bytes: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("original"))),
			ConversionState: documentimport.ConversionSucceeded, SourceState: documentimport.SourceSaved, DeletionState: documentimport.DeletionPresent,
			SourceRef:   &documentimport.SourceRef{RawPath: path, SHA256: digest, ObjectGeneration: objectGeneration},
			PipelineRef: &documentimport.PipelineRef{Status: "pending", SourceRevision: sourceRevision}, Revision: 1,
		}
		ref := f.repo.client.Collection("projects").Doc(projectDocID(userID, projectID)).Collection("document_imports").Doc(id)
		if _, err := ref.Set(f.ctx, record); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	matchingRef := writeRecord("document-matching", 3, matchingPath, fmt.Sprintf("%x", sha256.Sum256(matchingData)), matchingGeneration)
	futureRef := writeRecord("document-future", 7, matchingPath, fmt.Sprintf("%x", sha256.Sum256(matchingData)), matchingGeneration)
	staleRef := writeRecord("document-replaced", 2, stalePath, fmt.Sprintf("%x", sha256.Sum256(staleData)), staleGeneration)
	latePath := "documents/confirmed-before-source-state.md"
	lateData := []byte("# Confirmed before SourceSaved\n\nrecover the same intent without resubmitting\n")
	lateGeneration, err := conditional.WriteFileIfGeneration(f.ctx, lateData, "raw/"+latePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	lateDocument := documentimport.Record{
		SchemaVersion: documentimport.SchemaVersion, OwnerID: userID, ProjectID: projectID, DocumentID: "document-late-source-state",
		AttemptID: "document-late-source-state-a1", OriginalFilename: "late.md", Bytes: 1,
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("original"))), ConversionState: documentimport.ConversionSucceeded,
		SourceState: documentimport.SourceSaving, LeaseToken: "document-worker-lease", LeaseUntil: time.Now().UTC().Add(time.Minute),
		DeletionState: documentimport.DeletionPresent, PipelineRef: &documentimport.PipelineRef{Status: "pending", SourceRevision: 6}, Revision: 1,
	}
	lateDocumentRef := f.repo.client.Collection("projects").Doc(projectDocID(userID, projectID)).Collection("document_imports").Doc(lateDocument.DocumentID)
	if _, err := lateDocumentRef.Set(f.ctx, lateDocument); err != nil {
		t.Fatal(err)
	}
	reservationID := "source-commit-document-test"
	if _, err := f.repo.client.Collection("pipeline_quota_reservations").Doc(reservationID).Set(f.ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": reservationID, "execution_id": "local-aaaaaaaaaaaaaaaaaaaaaaaa",
		"trigger": string(pipelinequota.TriggerSourceCommit), "source_revision": int64(6), "intent_status": "compiling",
		"settlement_status": "pending", "created_at": time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.client.Collection("pipeline_quota_reservations").Doc("manual-newer").Set(f.ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": "manual-newer", "execution_id": "local-bbbbbbbbbbbbbbbbbbbbbbbb",
		"trigger": string(pipelinequota.TriggerManual), "source_revision": int64(99), "intent_status": "compiling",
		"settlement_status": "pending", "created_at": time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.dispatcher.associateDocumentImportsWithPipeline(f.ctx, f.repo, userID, projectID); err != nil {
		t.Fatal(err)
	}
	var skippedLate documentimport.Record
	if snapshot, err := lateDocumentRef.Get(f.ctx); err != nil || snapshot.DataTo(&skippedLate) != nil {
		t.Fatalf("read document before SourceSaved barrier: %v", err)
	}
	if skippedLate.PipelineRef.IntentID != "" {
		t.Fatalf("pipeline automation associated a document before its source save was recorded: %+v", skippedLate.PipelineRef)
	}
	lateWork := profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "document_import", Revision: lateDocument.Revision,
		ID: lateDocument.AttemptID, DocumentID: lateDocument.DocumentID, Attempts: profileruntime.MaxAttempts, Status: "complete"}
	lateWorkRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(lateWork))
	if _, err := lateWorkRef.Set(f.ctx, lateWork); err != nil {
		t.Fatalf("seed consumed conversion attempts before association handoff: %v", err)
	}
	lateSourceRef := &documentimport.SourceRef{RawPath: latePath, SHA256: fmt.Sprintf("%x", sha256.Sum256(lateData)), ObjectGeneration: lateGeneration}
	if _, err := f.dispatcher.Handler.documentImports.Repository.SetSourceOutcome(f.ctx, userID, projectID, lateDocument.DocumentID,
		lateDocument.LeaseToken, documentimport.SourceSaved, lateSourceRef, lateDocument.AttemptID,
		&documentimport.PipelineRef{Status: "pending", SourceRevision: 6}, nil); err != nil {
		t.Fatalf("record confirmed source after automation skipped it: %v", err)
	}
	f.clock = time.Now().UTC().Add(time.Second)
	var queuedLateWork profileruntime.Work
	if snapshot, err := lateWorkRef.Get(f.ctx); err != nil || snapshot.DataTo(&queuedLateWork) != nil ||
		!queuedLateWork.Pending || queuedLateWork.Attempts != 0 || queuedLateWork.Status != "pipeline_association_wait" {
		t.Fatalf("SourceSaved did not reset consumed conversion attempts into association work: work=%+v err=%v", queuedLateWork, err)
	}
	if _, err := lateWorkRef.Update(f.ctx, []firestore.Update{{Path: "due", Value: f.clock}}); err != nil {
		t.Fatalf("wake durable document association recovery: %v", err)
	}
	f.dispatcher.Handler.SetDocumentImportStarter(documentImportStarterFunc(func(context.Context, string, string, string, string) (string, error) {
		return "", errors.New("saved source must associate without a new conversion Job")
	}))
	processed, err := f.dispatcher.Dispatch(f.ctx, 8)
	if err != nil || processed != 1 {
		t.Fatalf("recover document association after SourceSaved: processed=%d err=%v", processed, err)
	}
	var recoveredLate documentimport.Record
	if snapshot, err := lateDocumentRef.Get(f.ctx); err != nil || snapshot.DataTo(&recoveredLate) != nil {
		t.Fatalf("read recovered document association: %v", err)
	}
	if recoveredLate.PipelineRef.IntentID != reservationID || recoveredLate.PipelineRef.SourceRevision != 6 ||
		recoveredLate.SourceOperationID != lateDocument.AttemptID || recoveredLate.SourceRef == nil ||
		*recoveredLate.SourceRef != *lateSourceRef || recoveredLate.ConversionState != documentimport.ConversionSucceeded || recoveredLate.SourceState != documentimport.SourceSaved {
		t.Fatalf("recovery did not attach the same intent to the confirmed document: %+v", recoveredLate)
	}
	var completedLateWork profileruntime.Work
	if snapshot, err := lateWorkRef.Get(f.ctx); err != nil || snapshot.DataTo(&completedLateWork) != nil || completedLateWork.Pending || completedLateWork.Status != "complete" {
		t.Fatalf("document association recovery work did not complete: work=%+v err=%v", completedLateWork, err)
	}
	var matching documentimport.Record
	if snapshot, err := matchingRef.Get(f.ctx); err != nil || snapshot.DataTo(&matching) != nil {
		t.Fatalf("read matching document: %v", err)
	}
	if matching.PipelineRef.IntentID != reservationID || matching.PipelineRef.ExecutionID != "local-aaaaaaaaaaaaaaaaaaaaaaaa" ||
		matching.PipelineRef.SourceRevision != 3 || matching.PipelineRef.Status != "running" {
		t.Fatalf("matching document was not linked to the source commit: %+v", matching.PipelineRef)
	}
	for name, ref := range map[string]*firestore.DocumentRef{"newer source revision": futureRef, "replaced source generation": staleRef} {
		var record documentimport.Record
		snapshot, err := ref.Get(f.ctx)
		if err != nil || snapshot.DataTo(&record) != nil {
			t.Fatalf("read %s document: %v", name, err)
		}
		if record.PipelineRef.IntentID != "" {
			t.Errorf("%s was linked to older source commit: %+v", name, record.PipelineRef)
		}
	}

	status := &documentImportPipelineStatus{value: "RUNNING"}
	f.dispatcher.Handler.localPipeline = status
	projected, err := f.dispatcher.Handler.documentImports.Repository.Get(f.ctx, userID, projectID, matching.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	projected = f.dispatcher.Handler.projectDocumentImportPipeline(f.ctx, userID, projectID, projected)
	if projected.PipelineRef.Status != "running" || projected.ConversionState != documentimport.ConversionSucceeded {
		t.Fatalf("running Pipeline projection changed conversion outcome: pipeline=%+v conversion=%s", projected.PipelineRef, projected.ConversionState)
	}

	receipt, err := localcloud.EncodePublicationReceipt(localcloud.PublicationReceipt{
		ExecutionID: "local-aaaaaaaaaaaaaaaaaaaaaaaa", GenerationID: "g_document_generation", ManifestGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectStore.receipts["local-aaaaaaaaaaaaaaaaaaaaaaaa"] = receipt
	status.value = "SUCCEEDED"
	projected = f.dispatcher.Handler.projectDocumentImportPipeline(f.ctx, userID, projectID, projected)
	if projected.PipelineRef.Status != "published" || projected.PipelineRef.ContentGeneration != "g_document_generation" ||
		projected.ConversionState != documentimport.ConversionSucceeded {
		t.Fatalf("publication projection=%+v conversion=%s", projected.PipelineRef, projected.ConversionState)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(recorder)
	requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/document-imports/"+matching.DocumentID, nil)
	requestContext.Set("userID", userID)
	requestContext.Set("projectID", projectID)
	requestContext.Params = gin.Params{{Key: "pid", Value: projectID}, {Key: "id", Value: matching.DocumentID}}
	f.dispatcher.Handler.GetDocumentImport(requestContext)
	var restResponse struct {
		PipelineRef documentimport.PipelineRef `json:"pipeline_ref"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &restResponse) != nil ||
		restResponse.PipelineRef.Status != "published" || restResponse.PipelineRef.ContentGeneration != "g_document_generation" {
		t.Fatalf("REST document status=%d response=%+v body=%s", recorder.Code, restResponse, recorder.Body.String())
	}
	mcpContext := context.WithValue(f.ctx, mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: userID, projectID: projectID, capabilities: []string{mcpQueryCapability},
	})
	mcpResponse, _, err := f.dispatcher.Handler.callMCPGetDocumentImport(mcpContext, nil, mcpGetDocumentImportInput{DocumentID: matching.DocumentID})
	if err != nil {
		t.Fatal(err)
	}
	var mcpEnvelope struct {
		Document struct {
			PipelineRef documentimport.PipelineRef `json:"pipeline_ref"`
		} `json:"document"`
	}
	mcpData, marshalErr := json.Marshal(mcpResponse.StructuredContent)
	if err := json.Unmarshal(mcpData, &mcpEnvelope); marshalErr != nil || err != nil ||
		mcpEnvelope.Document.PipelineRef.Status != "published" || mcpEnvelope.Document.PipelineRef.ContentGeneration != "g_document_generation" {
		t.Fatalf("MCP document status=%+v marshalErr=%v err=%v content=%v", mcpEnvelope, marshalErr, err, mcpResponse.StructuredContent)
	}

	failedRef := &documentimport.PipelineRef{Status: "pending", IntentID: "source-commit-failed", SourceRevision: 4}
	failedRecord := projected
	failedRecord.PipelineRef = failedRef
	if _, err := f.repo.client.Collection("pipeline_quota_reservations").Doc(failedRef.IntentID).Set(f.ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "attempt_id": failedRef.IntentID,
		"trigger": string(pipelinequota.TriggerSourceCommit), "source_revision": int64(6), "intent_status": "failed",
		"intent_error_code": "pipeline_prepare_failed", "settlement_status": "released",
	}); err != nil {
		t.Fatal(err)
	}
	failedRecord = f.dispatcher.Handler.projectDocumentImportPipeline(f.ctx, userID, projectID, failedRecord)
	if failedRecord.PipelineRef.Status != "failed" || failedRecord.PipelineRef.Reason != "pipeline_prepare_failed" ||
		failedRecord.ConversionState != documentimport.ConversionSucceeded {
		t.Fatalf("Pipeline failure projection changed conversion outcome: pipeline=%+v conversion=%s", failedRecord.PipelineRef, failedRecord.ConversionState)
	}
}

func TestDocumentImportPipelineRefSourceRevisionWire(t *testing.T) {
	record := documentimport.Record{PipelineRef: &documentimport.PipelineRef{Status: "pending", SourceRevision: 42}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded documentimport.Record
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.PipelineRef == nil || decoded.PipelineRef.SourceRevision != 42 {
		t.Fatalf("PipelineRef source revision did not survive JSON: %+v err=%v", decoded.PipelineRef, err)
	}
}

func TestDocumentImportDeleteResumesFromDurableWorkAndExhaustsVisibly(t *testing.T) {
	f := newRuntimeFixture(t)
	base := localfs.New(t.TempDir())
	projectStore := base.Scope(f.user, f.project)
	f.dispatcher.Handler.store = &documentImportPipelineRoot{RootStore: base, project: projectStore}
	objects, ok := projectStore.(documentimport.ObjectStore)
	if !ok {
		t.Fatal("local project store does not satisfy document import storage")
	}
	projectRef := f.repo.client.Collection("projects").Doc(projectDocID(f.user, f.project))
	if _, err := projectRef.Set(f.ctx, map[string]any{"document_import_failed_count": int64(2)}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	docRepo := f.dispatcher.Handler.documentImports.Repository
	createFailedDocument := func(id, attemptID string, files bool) documentimport.Record {
		t.Helper()
		record := documentimport.Record{
			SchemaVersion: documentimport.SchemaVersion, OwnerID: f.user, ProjectID: f.project, DocumentID: id, AttemptID: attemptID,
			OriginalFilename: "failed.docx", Bytes: 1, SHA256: strings.Repeat("a", 64), ConversionState: documentimport.ConversionFailed,
			SourceState: documentimport.SourceNotStarted, DeletionState: documentimport.DeletionPresent, Revision: 1,
			Failure: &documentimport.Failure{Stage: "conversion", Class: "worker_unavailable", SafeMessageKey: "worker_unavailable"},
		}
		if files {
			originalPath := documentimport.OriginalPath(id)
			markdownPath := documentimport.MarkdownPath(id, attemptID)
			originalGeneration, err := objects.WriteFileIfGeneration(f.ctx, []byte("original"), originalPath, 0)
			if err != nil {
				t.Fatalf("write original fixture: %v", err)
			}
			markdownGeneration, err := objects.WriteFileIfGeneration(f.ctx, []byte("# converted"), markdownPath, 0)
			if err != nil {
				t.Fatalf("write Markdown fixture: %v", err)
			}
			record.OriginalObjectPath, record.OriginalGeneration = originalPath, originalGeneration
			record.MarkdownObjectPath, record.MarkdownGeneration = markdownPath, markdownGeneration
		}
		ref := f.repo.client.Collection("projects").Doc(projectDocID(f.user, f.project)).Collection("document_imports").Doc(id)
		if _, err := ref.Set(f.ctx, record); err != nil {
			t.Fatalf("seed failed document %s: %v", id, err)
		}
		return record
	}
	completed := createFailedDocument("document-delete-recover", "document-delete-recover-a1", true)
	exhausted := createFailedDocument("document-delete-exhaust", "document-delete-exhaust-a1", false)
	if _, err := docRepo.MarkDeleting(f.ctx, f.user, f.project, completed.DocumentID, completed.Revision); err != nil {
		t.Fatalf("durably mark cleanup before simulated crash: %v", err)
	}
	if _, err := docRepo.MarkDeleting(f.ctx, f.user, f.project, exhausted.DocumentID, exhausted.Revision); err != nil {
		t.Fatalf("durably queue cleanup exhaustion case: %v", err)
	}
	completedWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "document_import", ID: completed.AttemptID}
	completedWorkRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(completedWork))
	exhaustedWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "document_import", ID: exhausted.AttemptID}
	exhaustedWorkRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(exhaustedWork))
	f.clock = time.Now().UTC().Add(time.Second)
	if _, err := completedWorkRef.Update(f.ctx, []firestore.Update{{Path: "due", Value: f.clock}}); err != nil {
		t.Fatal(err)
	}
	if _, err := exhaustedWorkRef.Update(f.ctx, []firestore.Update{
		{Path: "due", Value: f.clock}, {Path: "attempts", Value: profileruntime.MaxAttempts},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatcher.Dispatch(f.ctx, 8); err != nil {
		t.Fatalf("resume cleanup work after process crash: %v", err)
	}
	for id, want := range map[string]documentimport.DeletionState{
		completed.DocumentID: documentimport.DeletionComplete,
		exhausted.DocumentID: documentimport.DeletionFailed,
	} {
		record, err := docRepo.Get(f.ctx, f.user, f.project, id)
		if err != nil || record.DeletionState != want {
			t.Errorf("document %s deletion state=%s err=%v want=%s", id, record.DeletionState, err, want)
		}
	}
	for _, path := range []string{completed.OriginalObjectPath, completed.MarkdownObjectPath} {
		if _, _, err := objects.ReadFileWithGeneration(f.ctx, path); err == nil ||
			(!errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist)) {
			t.Errorf("completed cleanup retained %s: err=%v", path, err)
		}
	}
	var recoveredWork, exhaustedWorkResult profileruntime.Work
	if snapshot, err := completedWorkRef.Get(f.ctx); err != nil || snapshot.DataTo(&recoveredWork) != nil || recoveredWork.Pending || recoveredWork.Status != "complete" {
		t.Fatalf("recovered cleanup work=%+v err=%v", recoveredWork, err)
	}
	if snapshot, err := exhaustedWorkRef.Get(f.ctx); err != nil || snapshot.DataTo(&exhaustedWorkResult) != nil || exhaustedWorkResult.Pending || exhaustedWorkResult.Status != "exhausted" {
		t.Fatalf("exhausted cleanup work=%+v err=%v", exhaustedWorkResult, err)
	}
	summary, err := docRepo.Summary(f.ctx, f.user, f.project, f.user, "")
	if err != nil || summary.FailedCount != 1 {
		t.Fatalf("failed badge after cleanup completion/exhaustion=%+v err=%v", summary, err)
	}
}
