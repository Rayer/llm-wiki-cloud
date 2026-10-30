package v1

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
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
