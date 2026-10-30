package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localpipeline"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type localDocumentImportProcessorStarter struct{ processor *documentimport.Processor }

func (s localDocumentImportProcessorStarter) Start(ctx context.Context, ownerID, projectID, documentID, attemptID string) (string, error) {
	if err := s.processor.Process(ctx, ownerID, projectID, documentID, attemptID); err != nil {
		return "", err
	}
	return "local-document-import-worker", nil
}

type acceptedDocumentImportJob struct{}

func (acceptedDocumentImportJob) Start(context.Context, string, string, string, string) (string, error) {
	return "local-document-import-job", nil
}

// This composes the public multipart producer, the real MarkItDown converter,
// durable Firestore/source journal, Profile dispatcher, native local Pipeline
// worker, publication receipt, status API and Query against loopback emulators.
func TestLocalDocumentImportDispatcherPublishesAndQueriesConvertedSource(t *testing.T) {
	if loopbackEmulatorEndpoint(t, "STORAGE_EMULATOR_HOST") == "" || loopbackEmulatorEndpoint(t, "FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires loopback Storage and Firestore emulators")
	}
	const (
		cloudProject = "llm-wiki-cloud"
		bucket       = "llm-wiki-cloud-local"
		secret       = "test-only-document-import-signing-secret"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	suffix := localPipelineTestNonce(t)
	scope := "lwc450-import-" + suffix
	database := "lwc450-import-" + strings.ReplaceAll(suffix, "-", "")
	userID, projectID := "lwc450-user-"+suffix, "document-import-e2e"
	t.Setenv("GOOGLE_CLOUD_PROJECT", cloudProject)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	t.Setenv("FIRESTORE_DATABASE_ID", database)

	cloudClient, err := cloudstorage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create loopback Storage client: %v", err)
	}
	fsClient, err := internalfirestore.NewClientWithDatabase(cloudProject, database, "", "")
	if err != nil {
		_ = cloudClient.Close()
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClient(bucket)
	if err != nil {
		_ = fsClient.Close()
		_ = cloudClient.Close()
		t.Fatalf("create scoped Storage client: %v", err)
	}
	var manager *localpipeline.Manager
	var workerStderr *os.File
	managerClosed := false
	adminRef := internalfirestore.Collection(fsClient.Raw(), "system").Doc("pipeline_automation")
	adminSnapshot, adminErr := adminRef.Get(ctx)
	priorAdminExists := adminErr == nil
	if adminErr != nil && status.Code(adminErr) != codes.NotFound {
		t.Fatalf("read local automation setting: %v", adminErr)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if !managerClosed && manager != nil {
			if err := manager.Close(cleanupCtx); err != nil {
				t.Errorf("close local Pipeline worker: %v", err)
			}
		}
		if workerStderr != nil {
			_ = workerStderr.Close()
		}
		if err := deleteLocalPipelineFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("delete scoped worker execution receipts: %v", err)
		}
		if err := deleteLocalPipelineObjectScope(cleanupCtx, cloudClient, bucket, scope); err != nil {
			t.Errorf("delete scoped Pipeline objects: %v", err)
		}
		projectRef := internalfirestore.Collection(fsClient.Raw(), "projects").Doc(userID + "_" + projectID)
		for _, child := range []string{"document_imports", "document_import_submissions", "pipeline_source_events", "pipeline_source_current", "profile"} {
			docs, err := projectRef.Collection(child).Documents(cleanupCtx).GetAll()
			if err != nil {
				t.Errorf("list disposable Project %s: %v", child, err)
				continue
			}
			for _, doc := range docs {
				if _, err := doc.Ref.Delete(cleanupCtx); err != nil {
					t.Errorf("delete disposable Project %s document: %v", child, err)
				}
			}
		}
		for _, collection := range []string{"profile_runtime_work", "pipeline_quota_reservations"} {
			docs, err := internalfirestore.Collection(fsClient.Raw(), collection).Where("user_id", "==", userID).Where("project_id", "==", projectID).Documents(cleanupCtx).GetAll()
			if err != nil {
				t.Errorf("list disposable %s: %v", collection, err)
				continue
			}
			for _, doc := range docs {
				if _, err := doc.Ref.Delete(cleanupCtx); err != nil {
					t.Errorf("delete disposable %s document: %v", collection, err)
				}
			}
		}
		quotaRef := internalfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(internalfirestore.QuotaDocID(userID, projectID))
		_, _ = quotaRef.Delete(cleanupCtx)
		if _, err := projectRef.Delete(cleanupCtx); err != nil && status.Code(err) != codes.NotFound {
			t.Errorf("delete disposable Project: %v", err)
		}
		if priorAdminExists {
			if _, err := adminRef.Set(cleanupCtx, adminSnapshot.Data()); err != nil {
				t.Errorf("restore local automation setting: %v", err)
			}
		} else if _, err := adminRef.Delete(cleanupCtx); err != nil && status.Code(err) != codes.NotFound {
			t.Errorf("delete local automation setting: %v", err)
		}
		if err := storageClient.Close(); err != nil {
			t.Errorf("close scoped Storage client: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close Firestore client: %v", err)
		}
		if err := cloudClient.Close(); err != nil {
			t.Errorf("close Storage client: %v", err)
		}
	})
	bucketHandle := cloudClient.Bucket(bucket)
	if _, err := bucketHandle.Attrs(ctx); errors.Is(err, cloudstorage.ErrBucketNotExist) {
		if err := bucketHandle.Create(ctx, cloudProject, nil); err != nil {
			t.Fatalf("create disposable emulator bucket: %v", err)
		}
	} else if err != nil {
		t.Fatalf("read emulator bucket: %v", err)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve local test source path")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../.."))
	repositoryRoot := filepath.Clean(filepath.Join(moduleRoot, "../.."))
	generatedConfigDir := t.TempDir()
	workerOverlay := documentImportWorkerFixtureOverlay(t, moduleRoot, generatedConfigDir)
	worker := filepath.Join(t.TempDir(), "olw_worker")
	build := exec.CommandContext(ctx, "go", "build", "-overlay", workerOverlay, "-tags", "lwc_local_pipeline_fixture", "-ldflags", "-X main.buildNonce=local-fixture", "-o", worker, "./cmd/olw_worker")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local Pipeline worker: %v\n%s", err, output)
	}
	workerWrapper := filepath.Join(t.TempDir(), "olw_worker_with_stdout_capture")
	t.Setenv("LWC_LOCAL_WORKER", worker)
	if err := os.WriteFile(workerWrapper, []byte("#!/bin/sh\nexec \"$LWC_LOCAL_WORKER\" \"$@\" >&2 2>&1\n"), 0o700); err != nil {
		t.Fatalf("write local Pipeline worker output wrapper: %v", err)
	}
	workerStderr, err = os.Create(filepath.Join(t.TempDir(), "worker.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(generatedConfigDir, "synto.toml")
	if err := os.WriteFile(configPath, []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\nrun_timeout_seconds = 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bindingsPath := filepath.Join(generatedConfigDir, "private-bindings.json")
	if err := os.WriteFile(bindingsPath, []byte(`{"environment":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	profileResolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"source":"bootstrap","profiles":[]}`))
	}))
	t.Cleanup(profileResolver.Close)
	manager, err = localpipeline.New(localpipeline.Config{
		Firestore: fsClient.Raw(), Storage: storageClient, Worker: workerWrapper, Stderr: workerStderr,
		Project: cloudProject, Bucket: bucket, Database: database, Scope: scope, WorkDir: t.TempDir(),
		PipelineConfigPath: configPath, PipelineBindingsPath: bindingsPath,
		ProfileResolverURL: profileResolver.URL + "/internal/pipeline/llm-profile",
	})
	if err != nil {
		t.Fatalf("configure native local worker: %v", err)
	}
	if err := manager.ReconcileInterrupted(ctx); err != nil {
		t.Fatalf("reconcile disposable local scope: %v", err)
	}

	h := handlerv1.New(storageClient, fsClient, nil, cache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, 1, 1, nil)
	h.SetLocalPipelineExecutor(manager)
	cloudRunSpy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	t.Cleanup(cloudRunSpy.Close)
	h.SetPipelineJobURL(cloudRunSpy.URL)
	projectRef := internalfirestore.Collection(fsClient.Raw(), "projects").Doc(userID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1), "pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatalf("seed disposable Project: %v", err)
	}
	if _, err := projectRef.Collection("profile").Doc("state").Set(ctx, map[string]any{
		"project_id": projectID, "revision": int64(1), "requirements": []map[string]any{},
	}); err != nil {
		t.Fatalf("seed empty Project Profile: %v", err)
	}
	if _, err := adminRef.Set(ctx, map[string]any{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatalf("enable local automatic Pipeline: %v", err)
	}

	lockDigest, err := documentimport.LockFileDigest(filepath.Join(repositoryRoot, "apps/document-import-worker/requirements.lock"))
	if err != nil {
		t.Fatal(err)
	}
	pythonPath := "/tmp/lwc450-document-import-converter/bin/python"
	if _, err := os.Stat(pythonPath); err != nil {
		t.Fatalf("local pinned converter runtime unavailable: %v", err)
	}
	projectObjects := storageClient.WithScope(userID, projectID)
	processor := &documentimport.Processor{
		Repository: documentimport.NewFirestoreRepository(fsClient.Raw()), Objects: projectObjects,
		Converter: documentimport.MarkItDownConverter{
			PythonPath: pythonPath, ScriptPath: filepath.Join(repositoryRoot, "apps/document-import-worker/convert.py"), Timeout: 30 * time.Second,
		},
		Journal: sourceautomation.NewJournal(fsClient.Raw(), storageClient), DependencyLockDigest: lockDigest,
	}
	h.SetDocumentImportStarter(acceptedDocumentImportJob{})

	queryCache := cache.New()
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := (localPipelineCitationTransport{}).RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(modelServer.Close)
	queryExecutor, err := queryquality.NewStrictProductionExecutorWithQueryServiceConfig(queryCache, nil, nil,
		query.NewService(queryCache, nil, llm.NewClientWithOptions("local-document-import-fixture", llm.ClientOptions{Model: "fixture", BaseURL: modelServer.URL})),
		queryquality.DefaultRetrievalProfile(), queryquality.StructuredPlanPromptID, queryquality.DefaultOptions(), query.RuntimeConfigIdentity{})
	if err != nil {
		t.Fatalf("configure local Query executor: %v", err)
	}
	h.SetQueryExecutor(queryExecutor)
	router := newProductionRouter(config.Config{
		JWTSecret: secret, AllowedOrigins: []string{"http://localhost:3000"}, FirestoreDatabaseID: database,
		GCPProject: cloudProject, Bucket: bucket,
	}, true, storageClient, fsClient, h, &syssettings.FakeStore{Enabled: true}, nil)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	token, err := auth.GenerateAccessToken(userID, "member", secret)
	if err != nil {
		t.Fatal(err)
	}

	originalDocument := documentImportDOCX(t, "LWC450_DOCUMENT_IMPORT_CAUSAL: rare violet lantern arrived through MarkItDown.")
	var uploadBody bytes.Buffer
	uploadWriter := multipart.NewWriter(&uploadBody)
	filePart, err := uploadWriter.CreateFormFile("file", "imported-e2e.docx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filePart.Write(originalDocument); err != nil {
		t.Fatal(err)
	}
	submissionID := "document-import-" + suffix
	if err := uploadWriter.WriteField("client_submission_id", submissionID); err != nil {
		t.Fatal(err)
	}
	if err := uploadWriter.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/raw/upload", &uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", projectID)
	request.Header.Set("Content-Type", uploadWriter.FormDataContentType())
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		DocumentID string `json:"document_id"`
		Conversion string `json:"conversion_state"`
		StatusURL  string `json:"status_url"`
	}
	responseBody, bodyErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	decodeErr := json.Unmarshal(responseBody, &accepted)
	if response.StatusCode != http.StatusAccepted || bodyErr != nil || decodeErr != nil || accepted.DocumentID == "" || accepted.Conversion != string(documentimport.ConversionAccepted) {
		t.Fatalf("document import producer status=%d result=%+v bodyErr=%v decodeErr=%v body=%s", response.StatusCode, accepted, bodyErr, decodeErr, responseBody)
	}

	h.SetDocumentImportStarter(localDocumentImportProcessorStarter{processor: processor})
	dispatcher := &handlerv1.ProfileDispatcher{Handler: h, Provider: profilederive.NewProvider(localPipelineBootstrapChatClient{})}
	processed, err := dispatcher.Dispatch(ctx, 8)
	if err != nil || processed == 0 {
		t.Fatalf("consume accepted document import: processed=%d err=%v", processed, err)
	}
	docStatus := func() (documentimport.Record, error) {
		statusRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+accepted.StatusURL, nil)
		if err != nil {
			return documentimport.Record{}, err
		}
		statusRequest.Header.Set("Authorization", "Bearer "+token)
		statusRequest.Header.Set("X-Project-ID", projectID)
		statusResponse, err := server.Client().Do(statusRequest)
		if err != nil {
			return documentimport.Record{}, err
		}
		defer statusResponse.Body.Close()
		var record documentimport.Record
		if statusResponse.StatusCode != http.StatusOK {
			return record, fmt.Errorf("document status API returned %d", statusResponse.StatusCode)
		}
		return record, json.NewDecoder(io.LimitReader(statusResponse.Body, 1<<20)).Decode(&record)
	}
	var record documentimport.Record
	for attempt := 0; attempt < 8; attempt++ {
		record, err = docStatus()
		if err == nil && record.ConversionState == documentimport.ConversionSucceeded && record.SourceState == documentimport.SourceSaved {
			break
		}
		if _, err := dispatcher.Dispatch(ctx, 8); err != nil {
			t.Fatalf("recover source-save work: %v", err)
		}
	}
	if err != nil || record.ConversionState != documentimport.ConversionSucceeded || record.SourceState != documentimport.SourceSaved ||
		record.SourceRef == nil || record.PipelineRef == nil || record.PipelineRef.SourceRevision <= 0 {
		t.Fatalf("converted and committed document status=%+v err=%v", record, err)
	}

	var reservation internalfirestore.QuotaReservation
	var reservationExists bool
	for attempt := 0; attempt < 12; attempt++ {
		if _, err := dispatcher.Dispatch(ctx, 8); err != nil {
			t.Fatalf("consume automatic Pipeline work: %v", err)
		}
		reservation, reservationExists, err = fsClient.GetPipelineIntent(ctx, userID, projectID, "")
		if err != nil {
			t.Fatalf("read source-commit Pipeline intent: %v", err)
		}
		if reservationExists && reservation.Trigger == pipelinequota.TriggerSourceCommit && reservation.ExecutionID != "" {
			break
		}
	}
	if !reservationExists || reservation.Trigger != pipelinequota.TriggerSourceCommit || reservation.ExecutionID == "" {
		t.Fatalf("document source was not admitted to a Pipeline: reservation=%+v exists=%t", reservation, reservationExists)
	}

	var pipelineStatus *handlerapi.PipelineExecutionResponse
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		pipelineStatus, err = manager.Status(ctx, userID, projectID, reservation.ExecutionID)
		if err == nil && pipelineStatus != nil && (pipelineStatus.Status == "SUCCEEDED" || pipelineStatus.Status == "FAILED" || pipelineStatus.Status == "CANCELLED") {
			break
		}
		if err != nil && !errors.Is(err, localpipeline.ErrExecutionNotFound) {
			t.Fatalf("read local Pipeline execution: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if pipelineStatus == nil || pipelineStatus.Status != "SUCCEEDED" {
		workerOutput, _ := os.ReadFile(workerStderr.Name())
		t.Fatalf("native local Pipeline status=%+v diagnostic=%+v err=%v worker_stderr=%s", pipelineStatus, pipelineStatus.Diagnostic, err, workerOutput)
	}
	projectStorage := storageClient.WithScope(userID, projectID)
	manifest, _, exists, err := projectStorage.CurrentManifest(ctx)
	if err != nil || !exists || manifest.LocalExecutionID != reservation.ExecutionID {
		t.Fatalf("publication manifest=%+v exists=%t err=%v", manifest, exists, err)
	}
	if _, err := projectStorage.ReadLocalPublicationReceipt(ctx, reservation.ExecutionID); err != nil {
		t.Fatalf("publication receipt readback: %v", err)
	}
	pinned, _, err := projectStorage.PinQueryGeneration(ctx, manifest.GenerationID)
	if err != nil {
		t.Fatalf("pin published generation: %v", err)
	}
	sourceSnapshotReader, ok := pinned.(interface {
		ReadSourceSnapshot(context.Context) (generation.SourceSnapshotManifest, error)
	})
	if !ok {
		t.Fatal("published generation does not expose its source snapshot")
	}
	sourceSnapshot, err := sourceSnapshotReader.ReadSourceSnapshot(ctx)
	if err != nil {
		t.Fatalf("read published source snapshot: %v", err)
	}
	var importedSourceFound bool
	for _, row := range sourceSnapshot.Rows {
		if row.RawPath == "raw/"+record.SourceRef.RawPath && row.ContentDigest == record.SourceRef.SHA256 {
			importedSourceFound = true
		}
	}
	if !importedSourceFound {
		t.Fatalf("published source snapshot omitted imported raw identity path=%q sha256=%s rows=%+v", record.SourceRef.RawPath, record.SourceRef.SHA256, sourceSnapshot.Rows)
	}

	statusRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+accepted.StatusURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	statusRequest.Header.Set("Authorization", "Bearer "+token)
	statusRequest.Header.Set("X-Project-ID", projectID)
	statusResponse, err := server.Client().Do(statusRequest)
	if err != nil {
		t.Fatal(err)
	}
	var publishedDocument documentimport.Record
	decodePublishedErr := json.NewDecoder(io.LimitReader(statusResponse.Body, 1<<20)).Decode(&publishedDocument)
	_ = statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK || decodePublishedErr != nil || publishedDocument.PipelineRef == nil ||
		publishedDocument.PipelineRef.Status != "published" || publishedDocument.PipelineRef.IntentID != reservation.ID ||
		publishedDocument.PipelineRef.ExecutionID != reservation.ExecutionID || publishedDocument.PipelineRef.ContentGeneration != manifest.GenerationID {
		t.Fatalf("published document projection status=%d doc=%+v decodeErr=%v", statusResponse.StatusCode, publishedDocument, decodePublishedErr)
	}

	queryRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/query", strings.NewReader(`{"q":"alpha","mode":"wiki"}`))
	if err != nil {
		t.Fatal(err)
	}
	queryRequest.Header.Set("Authorization", "Bearer "+token)
	queryRequest.Header.Set("X-Project-ID", projectID)
	queryRequest.Header.Set("Content-Type", "application/json")
	queryResponse, err := server.Client().Do(queryRequest)
	if err != nil {
		t.Fatal(err)
	}
	var queryResult handlerapi.QueryResponse
	queryDecodeErr := json.NewDecoder(io.LimitReader(queryResponse.Body, 1<<20)).Decode(&queryResult)
	_ = queryResponse.Body.Close()
	if queryResponse.StatusCode != http.StatusOK || queryDecodeErr != nil {
		t.Fatalf("Query published document generation status=%d decodeErr=%v result=%+v", queryResponse.StatusCode, queryDecodeErr, queryResult)
	}
	var alphaResult bool
	for _, result := range queryResult.Results {
		if result.Type == "concept" && result.Slug == "alpha" {
			alphaResult = true
		}
	}
	if !alphaResult {
		t.Fatalf("Query did not read the newly published generation: %+v", queryResult.Results)
	}

	t.Logf("DOCUMENT_IMPORT_CAUSAL_EVIDENCE doc=%s source_revision=%d source_sha256=%s intent=%s execution=%s generation=%s conversion=%s source=%s pipeline=%s query_status=%d result_count=%d", record.DocumentID, record.PipelineRef.SourceRevision, record.SourceRef.SHA256, reservation.ID, reservation.ExecutionID, manifest.GenerationID, record.ConversionState, record.SourceState, publishedDocument.PipelineRef.Status, queryResponse.StatusCode, len(queryResult.Results))
}

func documentImportDOCX(t *testing.T, text string) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, entry := range []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`},
	} {
		file, err := archive.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func documentImportWorkerFixtureOverlay(t *testing.T, moduleRoot, tempDir string) string {
	t.Helper()
	// The Go overlay keeps document-import-specific corpus materialization in
	// this test without changing the shared LWC-442 worker fixture source.
	originalPath := filepath.Join(moduleRoot, "cmd/olw_worker/synto_executor_local_fixture.go")
	source, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read shared local Pipeline fixture: %v", err)
	}
	anchor := `if raw, err := os.ReadFile(filepath.Join(vault, "raw", "lwc413-e2e-source.md"));`
	if !strings.Contains(string(source), anchor) {
		t.Fatal("shared local Pipeline fixture insertion point changed")
	}
	importedSourceFixture := `if imported, err := filepath.Glob(filepath.Join(vault, "raw", "document-import-*.md")); err != nil {
		return fmt.Errorf("find imported document source fixture: %w", err)
	} else if len(imported) > 0 {
		raw, err := os.ReadFile(imported[0])
		if err != nil {
			return fmt.Errorf("read imported document source fixture: %w", err)
		}
		rawPath, err := filepath.Rel(vault, imported[0])
		if err != nil {
			return fmt.Errorf("resolve imported document source fixture: %w", err)
		}
		rawPath = filepath.ToSlash(rawPath)
		sourceID, slug := "d5d9e3f1a030", "imported-document"
		contentHash := fmt.Sprintf("%x", sha256.Sum256(raw))
		index := fmt.Sprintf(__DOCUMENT_IMPORT_INDEX_JSON__, sourceID, rawPath, contentHash)
		for path, content := range map[string][]byte{
			"wiki/alpha.md":                []byte(fmt.Sprintf("---\nid: c5d9e3f1a028\nsources:\n  - %s\n---\nAlpha fixture output\n\n%s\n", sourceID, string(raw))),
			"wiki/sources/" + slug + ".md": []byte(fmt.Sprintf("---\nid: %s\ntitle: Imported document\nsource_file: %s\n---\n%s\n", sourceID, rawPath, string(raw))),
			"cache/id_map.json":            []byte(fmt.Sprintf(__DOCUMENT_IMPORT_ID_MAP_JSON__, sourceID, slug, sourceID, slug, rawPath)),
			"cache/concepts.jsonl":         []byte(fmt.Sprintf(__DOCUMENT_IMPORT_CONCEPTS_JSON__+"\n", sourceID, sourceID)),
			".synto/INDEX.json":            []byte(index),
		} {
			if err := writeLocalFixtureFile(vault, path, content); err != nil {
				return err
			}
		}
	}`
	for placeholder, template := range map[string]string{
		"__DOCUMENT_IMPORT_INDEX_JSON__":    `{"schema_version":1,"pack":{"id":"fixture","name":"fixture","version":"0","language":["en"],"capabilities":["articles","concepts"]},"articles":[{"id":"article","entity_id":"01JAZ5N7Y3K8M2Q4R6T9VWXAC8","name":"alpha","path":"wiki/alpha.md","summary":null,"tags":[],"aliases":[],"confidence":"high"}],"terms":[],"papers":[],"sources":[{"id":"%s","title":"Imported document","source_type":"raw"}],"source_concepts":[{"source_path":"%s","content_hash":"%s","concepts":[{"name":"alpha","entity_id":"01JAZ5N7Y3K8M2Q4R6T9VWXAC8"}]}],"synthesis":[],"stats":{"article_count":1,"draft_count":0,"concept_count":1,"alias_count":0,"knowledge_item_count":0,"source_count":1,"source_segment_count":0,"failed_note_count":0,"failed_concept_count":0}}`,
		"__DOCUMENT_IMPORT_ID_MAP_JSON__":   `{"concept":{"c5d9e3f1a028":"alpha"},"source":{"%s":"%s"},"source_meta":{"%s":{"slug":"%s","title":"Imported document","source_file":"%s"}},"redirects":{}}`,
		"__DOCUMENT_IMPORT_CONCEPTS_JSON__": `{"slug":"alpha","frontmatter":{"id":"c5d9e3f1a028","sources":["%s"]},"sources":["%s"]}`,
	} {
		importedSourceFixture = strings.Replace(importedSourceFixture, placeholder, strconv.Quote(template), 1)
	}
	overlaid := strings.Replace(string(source), anchor, importedSourceFixture+"\n\t"+anchor, 1)
	fixturePath := filepath.Join(tempDir, "synto_executor_local_fixture.go")
	if err := os.WriteFile(fixturePath, []byte(overlaid), 0o600); err != nil {
		t.Fatalf("write document-import local fixture overlay: %v", err)
	}
	overlayData, err := json.Marshal(struct {
		Replace map[string]string `json:"Replace"`
	}{Replace: map[string]string{originalPath: fixturePath}})
	if err != nil {
		t.Fatalf("encode local worker Go overlay: %v", err)
	}
	overlayPath := filepath.Join(tempDir, "worker-overlay.json")
	if err := os.WriteFile(overlayPath, overlayData, 0o600); err != nil {
		t.Fatalf("write local worker Go overlay: %v", err)
	}
	return overlayPath
}
