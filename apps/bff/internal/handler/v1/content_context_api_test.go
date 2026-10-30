package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestDecodeStoredContentContextStateSnakeCase(t *testing.T) {
	state, err := decodeStoredContentContextState(contentContextStateMap("generation-current", testContentContextDigest('a'), "failed", "provider_error", testContentContextDigest('b'), "persisted summary"))
	if err != nil {
		t.Fatalf("decode Firestore map: %v", err)
	}
	if state.SchemaVersion != contentcontext.StateSchemaVersion || state.Status != contentcontext.StatusFailed || state.ErrorClass != "provider_error" ||
		state.TargetConceptsSHA != testContentContextDigest('a') || state.LastGood == nil || state.LastGood.Text != "persisted summary" ||
		state.LatestPublication == nil || state.LatestPublication.ConceptsSHA256 != testContentContextDigest('a') ||
		state.SummaryWork == nil || state.SummaryWork.Status != contentcontext.StatusFailed {
		t.Fatalf("decoded state = %+v", state)
	}
}

func TestContentContextResponseUsesCurrentDigestAndCarriesSameDigestAcrossGenerations(t *testing.T) {
	identity := storage.QueryGenerationIdentity{
		ProjectID: "project", GenerationID: "generation-current", ConceptsDigest: testContentContextDigest('a'),
	}
	sameDigest, err := decodeStoredContentContextState(contentContextStateMap("generation-old", testContentContextDigest('a'), "ready", "", testContentContextDigest('a'), "carried summary"))
	if err != nil {
		t.Fatal(err)
	}
	response := contentContextResponseForState("project", identity, &sameDigest)
	if response.Status != contentcontext.StatusReady || response.CurrentGeneration == nil || *response.CurrentGeneration != "generation-current" || response.Stale ||
		response.Summary == nil || response.Summary.Text != "carried summary" || response.Summary.SourceGeneration != "generation-old" {
		t.Fatalf("same-digest carry-forward response = %+v", response)
	}

	differentDigest, err := decodeStoredContentContextState(contentContextStateMap("generation-old", testContentContextDigest('b'), "ready", "", testContentContextDigest('b'), "stale summary"))
	if err != nil {
		t.Fatal(err)
	}
	response = contentContextResponseForState("project", identity, &differentDigest)
	if response.Status != contentcontext.StatusMissing || !response.Stale || response.Summary == nil || response.Summary.Text != "stale summary" {
		t.Fatalf("different-digest response = %+v", response)
	}
	if response = contentContextResponseForState("project", identity, nil); response.Status != contentcontext.StatusMissing || response.Summary != nil || response.Stale {
		t.Fatalf("missing-state response = %+v", response)
	}
}

func testContentContextDigest(ch byte) string { return strings.Repeat(string(ch), 64) }

func contentContextStateMap(generation, digest, stateStatus, errorClass, lastGoodDigest, summary string) map[string]interface{} {
	generated := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	manifestIdentity := "manifest-" + generation
	attemptID := ""
	var attemptStartedAt interface{}
	var attemptFinishedAt interface{}
	var lease interface{}
	if stateStatus == "failed" {
		attemptID = "attempt"
		attemptStartedAt = generated.Add(-time.Minute)
		attemptFinishedAt = generated
		lease = generated.Add(time.Minute)
	}
	return map[string]interface{}{
		"schema_version":         1,
		"target_generation":      generation,
		"target_concepts_sha256": digest,
		"status":                 stateStatus,
		"attempt_id":             attemptID,
		"attempt_started_at":     attemptStartedAt,
		"attempt_finished_at":    attemptFinishedAt,
		"error_class":            errorClass,
		"last_good": map[string]interface{}{
			"text": summary, "source_generation": generation, "concepts_sha256": lastGoodDigest,
			"generated_at": generated, "prompt_revision": "prompt-v1", "profile_source": "personal", "profile_id": "profile",
			"profile_revision": int64(1), "role": "fast", "requested_model": "model-requested",
			"returned_model": "model-returned", "input_digest": testContentContextDigest('c'), "selection_version": "selection-v1",
		},
		"latest_publication": map[string]interface{}{
			"generation": generation, "concepts_sha256": digest, "manifest_identity": manifestIdentity,
		},
		"target_sequence": int64(2),
		"summary_work": map[string]interface{}{
			"work_id": "work-" + digest, "concepts_sha256": digest, "source_generation": generation,
			"source_manifest_identity": manifestIdentity, "attempt_id": attemptID, "lease": lease,
			"retry_count": 0, "status": stateStatus,
		},
	}
}

func TestContentContextProjectReadAuthorizationHidesCrossOwner(t *testing.T) {
	project := map[string]interface{}{"project_id": "project", "user_id": "owner"}
	if err := authorizeProjectDocument("owner_project", project, "owner", "project", ProjectRead); err != nil {
		t.Fatalf("owner ProjectRead was denied: %v", err)
	}
	if err := authorizeProjectDocument("owner_project", project, "other-owner", "project", ProjectRead); !errors.Is(err, errProfileProjectNotFound) {
		t.Fatalf("cross-owner ProjectRead error = %v, want hidden not-found", err)
	}
}

func TestCurrentContentContextPublicationHandlesLegacyAndReadFailure(t *testing.T) {
	root := newContentContextTestRoot(t, storage.QueryGenerationIdentity{}, storage.ErrQueryGenerationUnpinned)
	root.viewToken = "legacy"
	_, missing, err := currentContentContextPublication(context.Background(), root, "owner", "project")
	if err != nil || !missing {
		t.Fatalf("legacy publication = missing:%t err:%v, want missing without error", missing, err)
	}
	if root.pinCalls != 1 || root.identityCalls != 1 {
		t.Fatalf("legacy publication calls pin=%d identity=%d, want one each", root.pinCalls, root.identityCalls)
	}

	root = newContentContextTestRoot(t, storage.QueryGenerationIdentity{}, errors.New("provider unavailable"))
	_, missing, err = currentContentContextPublication(context.Background(), root, "owner", "project")
	if err == nil || missing {
		t.Fatalf("provider failure = missing:%t err:%v, want a read failure", missing, err)
	}
}

func TestGetContentContextReadsOnlyPersistedStateAndPinnedIdentity(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "cc-owner", fmt.Sprintf("cc-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	stateRef := projectRef.Collection("content_context").Doc("state")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": owner, "project_id": projectID}); err != nil {
		t.Fatalf("seed owner project: %v", err)
	}
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()

	const currentDigest = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root := newContentContextTestRoot(t, storage.QueryGenerationIdentity{
		ProjectID: projectID, GenerationID: "generation-current", ConceptsDigest: currentDigest,
	}, nil)
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	executor := &contentContextCountingExecutor{}
	h.SetQueryExecutor(executor)
	gin.SetMode(gin.TestMode)

	if _, err := stateRef.Set(ctx, contentContextStateMap("generation-old", strings.TrimPrefix(currentDigest, "sha256:"), "ready", "", strings.TrimPrefix(currentDigest, "sha256:"), "same digest summary")); err != nil {
		t.Fatalf("seed snake_case content-context state: %v", err)
	}
	response := serveContentContext(h, owner, projectID, projectID)
	if response.Code != http.StatusOK {
		t.Fatalf("same-digest response status=%d body=%s", response.Code, response.Body.String())
	}
	var body contentContextResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ProjectID != projectID || body.Status != "ready" || body.CurrentGeneration == nil || *body.CurrentGeneration != "generation-current" ||
		body.CurrentConceptsSHA256 == nil || *body.CurrentConceptsSHA256 != strings.TrimPrefix(currentDigest, "sha256:") || body.Summary == nil || body.Summary.Text != "same digest summary" || body.Stale {
		t.Fatalf("same-digest carry-forward response = %+v", body)
	}
	if root.pinCalls != 1 || root.identityCalls != 1 || root.writeCalls != 0 || executor.calls != 0 {
		t.Fatalf("pin=%d identity=%d writes=%d query=%d; want 1, 1, 0, 0", root.pinCalls, root.identityCalls, root.writeCalls, executor.calls)
	}

	root.identity.ConceptsDigest = testContentContextDigest('b')
	response = serveContentContext(h, owner, projectID, projectID)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stale response: %v", err)
	}
	if response.Code != http.StatusOK || body.Status != "missing" || !body.Stale || body.Summary == nil || body.Summary.Text != "same digest summary" {
		t.Fatalf("different-digest response status=%d body=%+v", response.Code, body)
	}

	if _, err := stateRef.Delete(ctx); err != nil {
		t.Fatalf("delete state for missing case: %v", err)
	}
	response = serveContentContext(h, owner, projectID, projectID)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode missing response: %v", err)
	}
	if response.Code != http.StatusOK || body.Status != "missing" || body.Summary != nil || body.Stale || body.CurrentGeneration == nil || *body.CurrentGeneration != "generation-current" {
		t.Fatalf("missing-state response status=%d body=%+v", response.Code, body)
	}

	root.identityErr = errors.New("provider read failure")
	if got := serveContentContext(h, owner, projectID, projectID).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("provider read failure status=%d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := serveContentContext(h, "other-owner", projectID, projectID).Code; got != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d, want %d", got, http.StatusNotFound)
	}
	if got := serveContentContext(h, "", projectID, projectID).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want %d", got, http.StatusUnauthorized)
	}
	if got := serveContentContext(h, owner, projectID, "other-project").Code; got != http.StatusBadRequest {
		t.Fatalf("project header mismatch status=%d, want %d", got, http.StatusBadRequest)
	}
	if root.writeCalls != 0 || executor.calls != 0 {
		t.Fatalf("request triggered writes=%d or query calls=%d", root.writeCalls, executor.calls)
	}
}

func TestLocalPublicationFeedsPersistedGETAndAuthenticatedMCPContext(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "local-context-owner", fmt.Sprintf("local-context-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	stateRef := projectRef.Collection("content_context").Doc("state")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready", "name": "Local research",
		"description": "Published project notes and concepts", "description_revision": int64(1),
	}); err != nil {
		t.Fatalf("seed Project metadata: %v", err)
	}
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()

	concepts := []byte(`{"slug":"alpha","title":"Alpha","body":"Published local concept.","frontmatter":{"id":"id-alpha"},"sources":[]}` + "\n")
	snapshot, err := conceptcache.ParseCanonicalJSONLSnapshot(projectID, "local-local-transaction-1", concepts)
	if err != nil {
		t.Fatal(err)
	}
	input, metadata, err := contentcontext.BuildSummaryInput(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimPrefix(snapshot.Identity.ConceptsDigest, "sha256:")
	descriptor := contentcontext.LocalPublicationDescriptor{
		Version: contentcontext.LocalPublicationVersion, OwnerID: owner, ProjectID: projectID,
		ExecutionID: "local-execution-1", TransactionID: "local-transaction-1", Generation: "local-local-transaction-1",
		ConceptsDigest: digest, SummarySourceGeneration: "local-local-transaction-1",
		SummarySourceIdentity: contentcontext.LocalSummarySourceIdentity("local-transaction-1", digest),
		SummaryInput:          input, SummaryMetadata: metadata,
	}
	descriptorData, err := contentcontext.EncodeLocalPublicationDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	localRoot := localfs.New(t.TempDir())
	project := localRoot.WithScope(owner, projectID)
	if _, err := project.WriteBytes(ctx, concepts, "cache/concepts.jsonl"); err != nil {
		t.Fatalf("write local concepts: %v", err)
	}
	if _, err := project.WriteBytes(ctx, descriptorData, contentcontext.LocalPublicationDescriptorPath); err != nil {
		t.Fatalf("write journaled local publication descriptor: %v", err)
	}
	if _, err := stateRef.Set(ctx, contentContextStateMap(descriptor.Generation, digest, "ready", "", digest, "Local summary from committed concepts")); err != nil {
		t.Fatalf("seed persisted summary state: %v", err)
	}

	h := New(localRoot, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	if got := serveContentContext(h, owner, projectID, projectID); got.Code != http.StatusOK {
		t.Fatalf("local GET status=%d body=%s", got.Code, got.Body.String())
	} else {
		var body contentContextResponse
		if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil || body.Status != contentcontext.StatusReady ||
			body.CurrentGeneration == nil || *body.CurrentGeneration != descriptor.Generation || body.Summary == nil || body.Summary.Text != "Local summary from committed concepts" {
			t.Fatalf("local GET body=%+v err=%v", body, err)
		}
	}

	h.SetMCPConfig(&config.BFFMCP{Instructions: "Keep project instructions."})
	identity := mcpProjectKeyIdentity{userID: owner, projectID: projectID, keyID: "local-bound-key", capabilities: []string{mcpQueryCapability}}
	response := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	result := mcpMetadataResult(t, response)
	instructions, ok := result["instructions"].(string)
	if !ok || !strings.Contains(instructions, "Local research") || !strings.Contains(instructions, "Local summary from committed concepts") ||
		!strings.Contains(instructions, descriptor.Generation) {
		t.Fatalf("authenticated MCP context omitted the local publication: %#v", result)
	}
	response = mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	tools := mcpMetadataResult(t, response)["tools"].([]interface{})
	queryTool := tools[0].(map[string]interface{})
	if queryTool["name"] != "query_project" || !strings.Contains(queryTool["description"].(string), "Local research") ||
		!strings.Contains(queryTool["description"].(string), "Local summary from committed concepts") {
		t.Fatalf("authenticated MCP tool description omitted the local publication: %#v", queryTool)
	}
}

func TestFirestoreRepositoryProducerStateFlowsThroughContentContextAPI(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "cc-producer-owner", fmt.Sprintf("cc-producer-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	stateRef := projectRef.Collection("content_context").Doc("state")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": owner, "project_id": projectID}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()

	digest := testContentContextDigest('d')
	authority := &contentContextAPITestAuthority{publication: contentcontext.Publication{
		Generation: "published-G1", ConceptsDigest: "sha256:" + digest, ManifestIdentity: "manifest-G1",
	}}
	repository := contentcontext.NewFirestoreRepository(fsClient.Raw())
	if _, err := repository.ReconcileCurrent(ctx, owner, projectID, authority); err != nil {
		t.Fatalf("register confirmed publication: %v", err)
	}
	claim, err := repository.Claim(ctx, owner, projectID, authority)
	if err != nil || claim == nil {
		t.Fatalf("claim published summary work: claim=%+v err=%v", claim, err)
	}
	completion := contentcontext.SummaryCompletion{
		Text: "Producer-backed published summary", PromptRevision: contentcontext.SummaryPromptRevision,
		ProfileSource: "personal", ProfileID: "selected-profile", ProfileRevision: 4, Role: contentcontext.SummaryRole,
		RequestedModel: "fast-requested", ReturnedModel: "fast-returned",
		InputDigest: testContentContextDigest('e'), SelectionVersion: contentcontext.SummarySelectionVersion,
	}
	if saved, err := repository.Complete(ctx, owner, projectID, authority, *claim, completion); err != nil || !saved {
		t.Fatalf("persist producer summary: saved=%t err=%v", saved, err)
	}

	root := newContentContextTestRoot(t, storage.QueryGenerationIdentity{
		ProjectID: projectID, GenerationID: "published-G1", ConceptsDigest: "sha256:" + digest,
	}, nil)
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	response := serveContentContext(h, owner, projectID, projectID)
	if response.Code != http.StatusOK {
		t.Fatalf("read producer summary status=%d body=%s", response.Code, response.Body.String())
	}
	var body contentContextResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode producer summary: %v", err)
	}
	if body.Status != contentcontext.StatusReady || body.CurrentGeneration == nil || *body.CurrentGeneration != "published-G1" ||
		body.CurrentConceptsSHA256 == nil || *body.CurrentConceptsSHA256 != digest || body.Summary == nil ||
		body.Summary.Text != completion.Text || body.Summary.SourceGeneration != "published-G1" || body.Summary.Role != contentcontext.SummaryRole || body.Stale {
		t.Fatalf("producer Firestore state was not read faithfully: %+v", body)
	}
}

type contentContextAPITestAuthority struct {
	mu          sync.Mutex
	publication contentcontext.Publication
	inFence     bool
}

func (a *contentContextAPITestAuthority) WithProjectFence(ctx context.Context, _, _ string, operation func(context.Context) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inFence = true
	defer func() { a.inFence = false }()
	return operation(ctx)
}

func (a *contentContextAPITestAuthority) CurrentPublication(context.Context, string, string) (contentcontext.Publication, error) {
	if !a.inFence {
		return contentcontext.Publication{}, errors.New("publication read escaped fence")
	}
	return a.publication, nil
}

func TestFirestoreRepositoryConcurrentClaimsHaveOneWinner(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "cc-race-owner", fmt.Sprintf("cc-race-%d", time.Now().UnixNano())
	stateRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(owner + "_" + projectID).Collection("content_context").Doc("state")
	defer func() { _, _ = stateRef.Delete(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	authority := &contentContextAPITestAuthority{publication: contentcontext.Publication{
		Generation: "published-G1", ConceptsDigest: testContentContextDigest('a'), ManifestIdentity: "manifest-G1",
	}}
	repository := contentcontext.NewFirestoreRepository(fsClient.Raw())
	if _, err := repository.ReconcileCurrent(ctx, owner, projectID, authority); err != nil {
		t.Fatalf("register publication: %v", err)
	}

	const contenders = 8
	claims := make(chan *contentcontext.SummaryClaim, contenders)
	errs := make(chan error, contenders)
	var wait sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := repository.Claim(ctx, owner, projectID, authority)
			claims <- claim
			errs <- err
		}()
	}
	wait.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim failed: %v", err)
		}
	}
	winners := 0
	for claim := range claims {
		if claim != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claim winners=%d, want exactly one", winners)
	}
}

func newContentContextEmulator(t *testing.T) *scopedfirestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	project := fmt.Sprintf("content-context-%d", time.Now().UnixNano())
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	return client
}

func newContentContextTestRoot(t *testing.T, identity storage.QueryGenerationIdentity, identityErr error) *contentContextTestRoot {
	t.Helper()
	return &contentContextTestRoot{
		RootStore:   localfs.New(t.TempDir()),
		identity:    identity,
		identityErr: identityErr,
	}
}

type contentContextTestRoot struct {
	storage.RootStore
	identity      storage.QueryGenerationIdentity
	identityErr   error
	viewToken     string
	pinCalls      int
	identityCalls int
	writeCalls    int
}

func (r *contentContextTestRoot) Scope(userID, projectID string) storage.Store {
	return &contentContextTestStore{Store: r.RootStore.Scope(userID, projectID), root: r}
}

type contentContextTestStore struct {
	storage.Store
	root *contentContextTestRoot
}

func (s *contentContextTestStore) Pin(context.Context) (storage.Store, error) {
	s.root.pinCalls++
	return s, nil
}

func (s *contentContextTestStore) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	s.root.identityCalls++
	return s.root.identity, s.root.identityErr
}

func (s *contentContextTestStore) ViewToken() string { return s.root.viewToken }

func (s *contentContextTestStore) WriteBytes(context.Context, []byte, string) (string, error) {
	s.root.writeCalls++
	return "", errors.New("write not expected")
}

func (s *contentContextTestStore) WriteBytesAtomic(context.Context, []byte, string, string) (string, error) {
	s.root.writeCalls++
	return "", errors.New("write not expected")
}

type contentContextCountingExecutor struct{ calls int }

func (e *contentContextCountingExecutor) Execute(context.Context, conceptcache.Reader, query.Request) (query.Result, error) {
	e.calls++
	return query.Result{}, nil
}

func serveContentContext(h *Handler, userID, projectID, headerProjectID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/content-context", nil)
	c.Params = gin.Params{{Key: "pid", Value: projectID}}
	c.Request.Header.Set("X-Project-ID", headerProjectID)
	if userID != "" {
		c.Set("userID", userID)
	}
	h.GetContentContext(c)
	return recorder
}
