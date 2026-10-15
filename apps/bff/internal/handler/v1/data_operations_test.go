package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const dataScopeName = "llm-wiki-bff/data"

func newDataSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

func dataSpanAttribute(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			return item.Value, true
		}
	}
	return attribute.Value{}, false
}

func dataOperationSpans(t *testing.T, spans []sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, map[string]sdktrace.ReadOnlySpan) {
	t.Helper()
	var root sdktrace.ReadOnlySpan
	phases := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range spans {
		if span.InstrumentationScope().Name != dataScopeName {
			t.Fatalf("unexpected instrumentation scope %q on selected route", span.InstrumentationScope().Name)
		}
		if span.Name() == "bff.data.operation" {
			if root != nil {
				t.Fatal("selected route created multiple operation roots")
			}
			root = span
			continue
		}
		if _, exists := phases[span.Name()]; !exists {
			phases[span.Name()] = span
		}
	}
	if root == nil {
		t.Fatal("selected route did not emit its operation root")
	}
	for name, phase := range phases {
		if phase.SpanContext().TraceID() != root.SpanContext().TraceID() || phase.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Errorf("phase %q is not a direct child of the operation root", name)
		}
	}
	return root, phases
}

func assertSpanPayloadOmits(t *testing.T, spans []sdktrace.ReadOnlySpan, canary string) {
	t.Helper()
	for _, span := range spans {
		if strings.Contains(span.Name(), canary) || strings.Contains(span.Status().Description, canary) {
			t.Fatalf("span name/status leaked canary %q: %+v", canary, span)
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), canary) {
				t.Fatalf("span attribute %q leaked canary %q", attr.Key, canary)
			}
		}
		for _, event := range span.Events() {
			if strings.Contains(event.Name, canary) {
				t.Fatalf("span event leaked canary %q", canary)
			}
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.Emit(), canary) {
					t.Fatalf("span event attribute leaked canary %q", canary)
				}
			}
		}
	}
}

func TestDataTracedConceptListPreservesCachedResponseAndOneRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newDataSpanRecorder(t)
	root := t.TempDir()
	project := filepath.Join(root, "users", "user", "projects", "project", "cache")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "concepts.jsonl"), []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"concept-1\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "id_map.json"), []byte(`{"concept":{"concept-1":"alpha"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	h := New(localfs.New(root), nil, search.NewIndex(), conceptcache.New(), nil, nil)
	router := gin.New()
	router.Use(observability.DataOperationMiddleware())
	router.GET("/api/v1/concepts", func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		h.ListConcepts(c)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/concepts", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Concepts []struct {
			Slug string `json:"slug"`
		} `json:"concepts"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Concepts) != 1 || body.Concepts[0].Slug != "alpha" {
		t.Fatalf("response changed: %+v", body)
	}

	operation, phases := dataOperationSpans(t, recorder.Ended())
	if value, ok := dataSpanAttribute(operation, "bff.operation"); !ok || value.AsString() != "concept.list" {
		t.Fatalf("operation owner = %v, want concept.list", value)
	}
	if value, ok := dataSpanAttribute(operation, "bff.outcome"); !ok || value.AsString() != "success" {
		t.Fatalf("operation outcome = %v, want success", value)
	}
	cache := phases["bff.data.cache"]
	if cache == nil {
		t.Fatalf("cache phase missing: %v", phases)
	}
	if value, ok := dataSpanAttribute(cache, "bff.cache.hit"); !ok || !value.AsBool() {
		t.Fatalf("cache hit was not recorded: %v", cache.Attributes())
	}
	if phases["bff.data.read"] == nil || phases["bff.data.response"] == nil {
		t.Fatalf("expected ID-map read and response phases: %v", phases)
	}
	if value, ok := dataSpanAttribute(phases["bff.data.response"], "bff.count.items"); !ok || value.AsInt64() != 1 {
		t.Fatalf("response item count = %v, want 1", value)
	}
	if len(phases) != 4 || phases["bff.data.input"] == nil {
		t.Fatalf("phases=%v, want input/cache/read/response and no synthetic network phase", phases)
	}
}

func TestDataTracedImportValidationAndPlaceholderNeverRecordURLOrCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newDataSpanRecorder(t)
	router := gin.New()
	router.Use(observability.DataOperationMiddleware())
	h := New(nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/import", h.Import)

	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodPost, "/api/v1/import", strings.NewReader("{invalid")))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid import status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
	invalidRoot, invalidPhases := dataOperationSpans(t, recorder.Ended())
	if value, ok := dataSpanAttribute(invalidRoot, "bff.outcome"); !ok || value.AsString() != "rejected" {
		t.Fatalf("invalid import outcome=%v", value)
	}
	if len(invalidPhases) != 1 || invalidPhases["bff.data.input"] == nil || invalidPhases["bff.data.placeholder"] != nil {
		t.Fatalf("invalid import phases=%v", invalidPhases)
	}

	canary := "https://PRIVATE_IMPORT_URL_CANARY.invalid/path?token=PRIVATE_TOKEN_CANARY"
	before := len(recorder.Ended())
	validResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/import", strings.NewReader(fmt.Sprintf(`{"urls":[%q]}`, canary)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(validResponse, request)
	if validResponse.Code != http.StatusOK || !strings.Contains(validResponse.Body.String(), canary) {
		t.Fatalf("placeholder response parity changed: status=%d body=%s", validResponse.Code, validResponse.Body.String())
	}
	validSpans := recorder.Ended()[before:]
	validRoot, validPhases := dataOperationSpans(t, validSpans)
	if value, ok := dataSpanAttribute(validRoot, "bff.outcome"); !ok || value.AsString() != "placeholder" {
		t.Fatalf("placeholder outcome=%v", value)
	}
	if validPhases["bff.data.input"] == nil || validPhases["bff.data.placeholder"] == nil || validPhases["bff.data.read"] != nil || validPhases["bff.data.write"] != nil {
		t.Fatalf("placeholder phases imply an unperformed operation: %v", validPhases)
	}
	assertSpanPayloadOmits(t, validSpans, canary)
	assertSpanPayloadOmits(t, validSpans, "PRIVATE_TOKEN_CANARY")
}

func TestDataTracedAnnotationCommitNoopAndPreconditionOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newDataSpanRecorder(t)
	root := t.TempDir()
	writeAnnotationFixture(t, root, "user", "project")
	h := New(localfs.New(root), nil, search.NewIndex(), conceptcache.New(), nil, nil)
	router := gin.New()
	router.Use(observability.DataOperationMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		c.Next()
	})
	router.PUT("/api/v1/sources/:id/annotation", h.PutAnnotation)
	put := func(body, generation string) (*httptest.ResponseRecorder, []sdktrace.ReadOnlySpan) {
		before := len(recorder.Ended())
		payload, _ := json.Marshal(handler.AnnotationRequest{Body: body, ExpectedGeneration: generation})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/sources/s1/annotation", bytes.NewReader(payload)))
		return response, recorder.Ended()[before:]
	}
	createdResponse, createdSpans := put("note", "0")
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created handler.AnnotationResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	createdRoot, createdPhases := dataOperationSpans(t, createdSpans)
	if value, ok := dataSpanAttribute(createdRoot, "bff.outcome"); !ok || value.AsString() != "committed" || createdPhases["bff.data.write"] == nil {
		t.Fatalf("write commit not observable: root=%v phases=%v", createdRoot.Attributes(), createdPhases)
	}

	noopResponse, noopSpans := put("note", created.Generation)
	if noopResponse.Code != http.StatusOK {
		t.Fatalf("no-op status=%d body=%s", noopResponse.Code, noopResponse.Body.String())
	}
	var unchanged handler.AnnotationResponse
	if err := json.Unmarshal(noopResponse.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	noopRoot, noopPhases := dataOperationSpans(t, noopSpans)
	if value, ok := dataSpanAttribute(noopRoot, "bff.outcome"); !ok || value.AsString() != "no_op" || noopPhases["bff.data.write"] != nil {
		t.Fatalf("no-op emitted a write: root=%v phases=%v", noopRoot.Attributes(), noopPhases)
	}
	if unchanged.Generation != created.Generation || unchanged.UpdatedAt != created.UpdatedAt {
		t.Fatalf("no-op response changed: created=%+v unchanged=%+v", created, unchanged)
	}

	staleResponse, staleSpans := put("stale", "0")
	if staleResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale write status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	staleRoot, stalePhases := dataOperationSpans(t, staleSpans)
	if value, ok := dataSpanAttribute(staleRoot, "bff.outcome"); !ok || value.AsString() != "precondition_failed" || stalePhases["bff.data.write"] != nil {
		t.Fatalf("precondition outcome/phases incorrect: root=%v phases=%v", staleRoot.Attributes(), stalePhases)
	}

	failureHandler := New(annotationResponseFailureRoot{Client: localfs.New(root), err: errors.New("PRIVATE_RESPONSE_STORAGE_ERROR_CANARY")}, nil, search.NewIndex(), conceptcache.New(), nil, nil)
	failureRouter := gin.New()
	failureRouter.Use(observability.DataOperationMiddleware())
	failureRouter.Use(func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		c.Next()
	})
	failureRouter.PUT("/api/v1/sources/:id/annotation", failureHandler.PutAnnotation)
	before := len(recorder.Ended())
	failurePayload, _ := json.Marshal(handler.AnnotationRequest{Body: "committed before response failure", ExpectedGeneration: created.Generation})
	failureResponse := httptest.NewRecorder()
	failureRouter.ServeHTTP(failureResponse, httptest.NewRequest(http.MethodPut, "/api/v1/sources/s1/annotation", bytes.NewReader(failurePayload)))
	if failureResponse.Code != http.StatusInternalServerError || failureResponse.Body.String() != `{"error":"generated data unavailable"}` {
		t.Fatalf("response failure contract changed: status=%d body=%s", failureResponse.Code, failureResponse.Body.String())
	}
	failureRoot, failurePhases := dataOperationSpans(t, recorder.Ended()[before:])
	if value, ok := dataSpanAttribute(failureRoot, "bff.outcome"); !ok || value.AsString() != "response_failure" {
		t.Fatalf("post-commit response outcome = %v", value)
	}
	if value, ok := dataSpanAttribute(failurePhases["bff.data.write"], "bff.outcome"); !ok || value.AsString() != "committed" {
		t.Fatalf("write outcome = %v, want committed", value)
	}
	if value, ok := dataSpanAttribute(failurePhases["bff.data.response"], "bff.outcome"); !ok || value.AsString() != "response_failure" {
		t.Fatalf("response outcome = %v", value)
	}
	persisted, _, err := localfs.New(root).Scope("user", "project").(store.ConditionalWriter).ReadFileWithGeneration(context.Background(), "cache/annotations/s1.json")
	if err != nil || !bytes.Contains(persisted, []byte("committed before response failure")) {
		t.Fatalf("write was not durably committed before response projection failure: err=%v", err)
	}
}

func TestDataTracedConceptProjectionFailureFinalizesResponseAndOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newDataSpanRecorder(t)
	root := t.TempDir()
	conceptPath := filepath.Join(root, "users", "user", "projects", "project", "wiki", "alpha.md")
	if err := os.MkdirAll(filepath.Dir(conceptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conceptPath, []byte("---\ntitle: Alpha\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(localfs.New(root), nil, search.NewIndex(), conceptcache.New(), nil, nil)
	router := gin.New()
	router.Use(observability.DataOperationMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		c.Next()
	})
	router.GET("/api/v1/concepts/:id", h.GetConcept)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/concepts/alpha", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != `{"error":"id map not found"}` {
		t.Fatalf("response contract changed: status=%d body=%s", response.Code, response.Body.String())
	}
	operation, phases := dataOperationSpans(t, recorder.Ended())
	if value, ok := dataSpanAttribute(operation, "bff.outcome"); !ok || value.AsString() != "response_failure" {
		t.Fatalf("operation outcome = %v, want response_failure", value)
	}
	responsePhase := phases["bff.data.response"]
	if responsePhase == nil {
		t.Fatalf("response phase was not ended: %v", phases)
	}
	if value, ok := dataSpanAttribute(responsePhase, "bff.outcome"); !ok || value.AsString() != "response_failure" {
		t.Fatalf("response phase outcome = %v", value)
	}
}

func TestDataTracedRawSyncRejectCancelCommitAndTransferFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newDataSpanRecorder(t)
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(observability.DataOperationMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		c.Next()
	})
	router.GET("/api/v1/sync/raw/file", h.SyncRawDownload)
	router.PUT("/api/v1/sync/raw/file", h.SyncRawUpload)

	badPath := "../../PRIVATE_RAW_PATH_CANARY"
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodGet, "/api/v1/sync/raw/file?path="+url.QueryEscape(badPath)+"&generation=1", nil))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
	invalidRoot, invalidPhases := dataOperationSpans(t, recorder.Ended())
	if value, ok := dataSpanAttribute(invalidRoot, "bff.outcome"); !ok || value.AsString() != "rejected" || invalidPhases["bff.data.input"] == nil || invalidPhases["bff.data.read"] != nil {
		t.Fatalf("invalid path reached repository read: root=%v phases=%v", invalidRoot.Attributes(), invalidPhases)
	}
	assertSpanPayloadOmits(t, recorder.Ended(), badPath)

	payload := []byte("PRIVATE_RAW_BODY_CANARY")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	before := len(recorder.Ended())
	writeRequest := httptest.NewRequest(http.MethodPut, "/api/v1/sync/raw/file?path="+url.QueryEscape("nested/note.bin"), bytes.NewReader(payload))
	writeRequest.Header.Set("X-Content-SHA256", digest)
	writeResponse := httptest.NewRecorder()
	router.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("write status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
	var writeResult struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(writeResponse.Body.Bytes(), &writeResult); err != nil || writeResult.Generation == "" {
		t.Fatalf("write response=%q err=%v", writeResponse.Body.String(), err)
	}
	writeSpans := recorder.Ended()[before:]
	writeRoot, writePhases := dataOperationSpans(t, writeSpans)
	if value, ok := dataSpanAttribute(writeRoot, "bff.outcome"); !ok || value.AsString() != "committed" || writePhases["bff.data.write"] == nil {
		t.Fatalf("write commit not observable: root=%v phases=%v", writeRoot.Attributes(), writePhases)
	}
	assertSpanPayloadOmits(t, writeSpans, "nested/note.bin")
	assertSpanPayloadOmits(t, writeSpans, digest)
	assertSpanPayloadOmits(t, writeSpans, string(payload))

	before = len(recorder.Ended())
	readURL := "/api/v1/sync/raw/file?path=" + url.QueryEscape("nested/note.bin") + "&generation=" + url.QueryEscape(writeResult.Generation)
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, readURL, nil))
	if readResponse.Code != http.StatusOK || !bytes.Equal(readResponse.Body.Bytes(), payload) || readResponse.Header().Get("X-Raw-Generation") != writeResult.Generation {
		t.Fatalf("read response changed: status=%d body=%q headers=%v", readResponse.Code, readResponse.Body.Bytes(), readResponse.Header())
	}
	readSpans := recorder.Ended()[before:]
	readRoot, readPhases := dataOperationSpans(t, readSpans)
	if value, ok := dataSpanAttribute(readRoot, "bff.outcome"); !ok || value.AsString() != "success" || readPhases["bff.data.transfer"] == nil {
		t.Fatalf("read/transfer not observable: root=%v phases=%v", readRoot.Attributes(), readPhases)
	}
	if value, ok := dataSpanAttribute(readPhases["bff.data.transfer"], "bff.count.bytes"); !ok || value.AsInt64() != int64(len(payload)) {
		t.Fatalf("transfer byte count = %v, want %d", value, len(payload))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before = len(recorder.Ended())
	canceledResponse := httptest.NewRecorder()
	canceledRequest := httptest.NewRequest(http.MethodGet, readURL, nil).WithContext(canceled)
	router.ServeHTTP(canceledResponse, canceledRequest)
	if canceledResponse.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled read status=%d body=%s", canceledResponse.Code, canceledResponse.Body.String())
	}
	canceledRoot, canceledPhases := dataOperationSpans(t, recorder.Ended()[before:])
	if value, ok := dataSpanAttribute(canceledRoot, "bff.outcome"); !ok || value.AsString() != "canceled" || canceledPhases["bff.data.read"] == nil {
		t.Fatalf("canceled read outcome=%v phases=%v", value, canceledPhases)
	}

	failingRecorder := newDataSpanRecorder(t)
	failingRoot := &rawSyncReadFailureRoot{Client: root}
	failingHandler := New(failingRoot, nil, nil, nil, nil, nil)
	failingRouter := gin.New()
	failingRouter.Use(observability.DataOperationMiddleware())
	failingRouter.Use(func(c *gin.Context) {
		c.Set("userID", "user")
		c.Set("projectID", "project")
		c.Next()
	})
	failingRouter.GET("/api/v1/sync/raw/file", failingHandler.SyncRawDownload)
	failureResponse := httptest.NewRecorder()
	failingRouter.ServeHTTP(failureResponse, httptest.NewRequest(http.MethodGet, readURL, nil))
	failureRoot, failurePhases := dataOperationSpans(t, failingRecorder.Ended())
	if failureResponse.Code != http.StatusOK || !strings.Contains(failureResponse.Body.String(), "PRIVATE_TRANSFER_PREFIX_CANARY") {
		t.Fatalf("partial response contract changed: status=%d body=%q", failureResponse.Code, failureResponse.Body.String())
	}
	if value, ok := dataSpanAttribute(failureRoot, "bff.outcome"); !ok || value.AsString() != "transfer_failure" || failurePhases["bff.data.transfer"] == nil {
		t.Fatalf("transfer failure not observable: root=%v phases=%v", failureRoot.Attributes(), failurePhases)
	}
	assertSpanPayloadOmits(t, failingRecorder.Ended(), "PRIVATE_TRANSFER_PREFIX_CANARY")
	assertSpanPayloadOmits(t, failingRecorder.Ended(), "PRIVATE_RAW_PROVIDER_ERROR_CANARY")
}

type rawSyncReadFailureRoot struct{ *localfs.Client }

type annotationResponseFailureRoot struct {
	*localfs.Client
	err error
}

func (r annotationResponseFailureRoot) Scope(userID, projectID string) store.Store {
	return annotationResponseFailureStore{Store: r.Client.Scope(userID, projectID), err: r.err}
}

type annotationResponseFailureStore struct {
	store.Store
	err error
}

func (s annotationResponseFailureStore) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if path == sourcestatus.Path {
		return nil, s.err
	}
	return s.Store.ReadFile(ctx, path)
}

func (s annotationResponseFailureStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	return s.Store.(store.ConditionalWriter).ReadFileWithGeneration(ctx, path)
}

func (s annotationResponseFailureStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, generation int64) (int64, error) {
	return s.Store.(store.ConditionalWriter).WriteFileIfGeneration(ctx, data, path, generation)
}

func (r *rawSyncReadFailureRoot) Scope(userID, projectID string) store.Store {
	return rawSyncReadFailureStore{Store: r.Client.Scope(userID, projectID)}
}

type rawSyncReadFailureStore struct{ store.Store }

func (s rawSyncReadFailureStore) ListSyncRawFiles(ctx context.Context) ([]store.RawSyncFile, error) {
	return s.Store.(store.RawSyncStore).ListSyncRawFiles(ctx)
}

func (s rawSyncReadFailureStore) OpenSyncRawFile(context.Context, string, string) (io.ReadCloser, int64, error) {
	return &rawSyncPartialFailureReader{}, 32, nil
}

func (s rawSyncReadFailureStore) WriteSyncRawFile(ctx context.Context, path string, body io.Reader, generation, digest string) (string, error) {
	return s.Store.(store.RawSyncStore).WriteSyncRawFile(ctx, path, body, generation, digest)
}

type rawSyncPartialFailureReader struct{ returned bool }

func (r *rawSyncPartialFailureReader) Read(p []byte) (int, error) {
	if r.returned {
		return 0, errors.New("PRIVATE_RAW_PROVIDER_ERROR_CANARY")
	}
	r.returned = true
	return copy(p, "PRIVATE_TRANSFER_PREFIX_CANARY"), nil
}

func (r *rawSyncPartialFailureReader) Close() error { return nil }
