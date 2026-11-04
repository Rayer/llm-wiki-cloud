package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type mcpContentAdmissionObservation struct {
	active        bool
	projectTokens int64
}

type mcpContentStageMemorySample struct {
	stage      string
	totalAlloc uint64
	heapAlloc  uint64
	rssPeak    int64
}

func TestMCPSourceSnapshotErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		code       string
		retryAfter int
	}{
		{name: "historical generation has no snapshot", err: gcs.ErrSourceSnapshotUnavailable, code: "raw_snapshot_unavailable"},
		{name: "declared inventory missing", err: storage.ErrDeclaredObjectUnavailable, code: "content_unavailable", retryAfter: 1000},
		{name: "corrupt inventory", err: gcs.ErrSourceSnapshotInvalid, code: "content_unavailable", retryAfter: 1000},
		{name: "transient storage failure", err: fmt.Errorf("read inventory: %w", context.DeadlineExceeded), code: "content_unavailable", retryAfter: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, retryAfter := mcpSourceSnapshotError(tc.err)
			if code != tc.code || retryAfter != tc.retryAfter {
				t.Fatalf("classification=(%q,%d), want (%q,%d)", code, retryAfter, tc.code, tc.retryAfter)
			}
		})
	}
}

type mcpContentAdmissionProbeRoot struct {
	storage.RootStore
	fs      *firestore.Client
	content *gcs.Client
	mu      sync.Mutex
	seen    []mcpContentAdmissionObservation
}

func (r *mcpContentAdmissionProbeRoot) Scope(userID, projectID string) storage.Store {
	r.observeAdmission()
	if r.content != nil {
		return r.content.WithScope(userID, projectID)
	}
	return r.RootStore.Scope(userID, projectID)
}

func (r *mcpContentAdmissionProbeRoot) observeAdmission() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	docs, err := internalfirestore.Collection(r.fs, "mcp_admission_v1").Documents(ctx).GetAll()
	observation := mcpContentAdmissionObservation{}
	if err == nil {
		for _, doc := range docs {
			data := doc.Data()
			if data["class"] != "content" || data["dimension"] != "account_project" {
				continue
			}
			if value, ok := data["tokens_microunits"].(int64); ok {
				observation.projectTokens = value
			}
			if leases, ok := data["leases"].([]interface{}); ok && len(leases) > 0 {
				observation.active = true
			}
		}
	}
	r.mu.Lock()
	r.seen = append(r.seen, observation)
	r.mu.Unlock()
}

func (r *mcpContentAdmissionProbeRoot) reset() {
	r.mu.Lock()
	r.seen = nil
	r.mu.Unlock()
}

func (r *mcpContentAdmissionProbeRoot) observations() []mcpContentAdmissionObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcpContentAdmissionObservation(nil), r.seen...)
}

type mcpStaticBearerTransport struct {
	next      http.RoundTripper
	token     string
	projectID string
}

func (t mcpStaticBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	if t.projectID != "" {
		cloned.Header.Set("X-Project-ID", t.projectID)
	}
	return t.next.RoundTrip(cloned)
}

func connectMCPWithBearer(t *testing.T, ctx context.Context, endpoint, token, projectID string) *mcp.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: mcpStaticBearerTransport{next: http.DefaultTransport, token: token, projectID: projectID}}
	client := mcp.NewClient(&mcp.Implementation{Name: "lwc-content-endpoint-test", Version: "local"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect MCP SDK client to product endpoint: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func mcpContentEndpointTestConfig() *config.BFFMCP {
	bucket := config.MCPProtectionBucket{Capacity: 32, RefillMicrounitsPerSecond: 1_000_000, MaxInflight: 8}
	content := config.MCPProtectionClass{
		Project: bucket, Account: config.MCPProtectionBucket{Capacity: 48, RefillMicrounitsPerSecond: 4_000_000, MaxInflight: 16},
		FleetMaxInflight: 64, LocalMaxInflight: 32, WorkTimeoutMS: 20_000, LeaseMS: 60_000, WorkingBytes: 128 << 20,
	}
	query := config.MCPProtectionClass{
		Project:          config.MCPProtectionBucket{Capacity: 4, RefillMicrounitsPerSecond: 200_000, MaxInflight: 2},
		Account:          config.MCPProtectionBucket{Capacity: 8, RefillMicrounitsPerSecond: 400_000, MaxInflight: 4},
		FleetMaxInflight: 16, LocalMaxInflight: 4, WorkTimeoutMS: 120_000, LeaseMS: 150_000,
	}
	return &config.BFFMCP{
		Instructions: "Query and content fixtures are local.",
		ToolDescriptions: config.BFFMCPToolDescriptions{
			QueryProject: "Query this Project.", AddRaw: "Add a raw source.", ImportURL: "Import a URL.",
			ListContent: "List published content.", FindContent: "Find published content by exact name.",
			ReadContent: "Read published content.", ReadSourceRaw: "Read retained Source text.",
		},
		Protection: &config.MCPProtection{
			SchemaVersion: 1, PolicyRevision: "mcp-endpoint-test-v1", Content: content, Query: query,
			AdmissionTimeoutMS: 2_000, TransactionMaxAttempts: 3, ReleaseTimeoutMS: 2_000, ReleaseMaxAttempts: 3,
			ControlLocalMaxInflight: 16, ListDefault: 20, ListMax: 50, ChunkMaxBytes: 16_384,
			ResponseMaxBytes: 256 << 10, ReferencePageMax: 50,
		},
	}
}

func mcpContentOAuthResources(origin string) []config.AuthOAuthResource {
	return []config.AuthOAuthResource{{
		ID: "lwc-project", Kind: "project-template", Enabled: true,
		URI: strings.TrimRight(origin, "/") + "/mcp/projects/{project_id}", Name: "LWC Project",
		Scopes: []config.AuthOAuthScope{
			{Name: "mcp:query", Description: "Query project knowledge."},
			{Name: "mcp:content:read", Description: "List and read published Concepts, Sources, and retained Source text in this Project."},
			{Name: "mcp:raw:write", Description: "Add permitted raw sources."},
		},
		AllowedScopeSets: [][]string{
			{"mcp:query"}, {"mcp:query", "mcp:content:read"}, {"mcp:query", "mcp:raw:write"}, {"mcp:query", "mcp:content:read", "mcp:raw:write"},
		},
		DefaultScopeSet: []string{"mcp:query"},
	}}
}

func TestMCPContentReadScopesAndAdmissionThroughProjectKeyAndOAuthSDKEndpoints(t *testing.T) {
	emulatorHost := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulatorHost == "" {
		t.Skip("requires a loopback Firestore emulator")
	}
	parsedEndpoint, parseErr := url.Parse("http://" + strings.TrimPrefix(emulatorHost, "http://"))
	if parseErr != nil || parsedEndpoint.Host == "" {
		t.Fatalf("invalid Firestore emulator host")
	}
	if host, _, splitErr := net.SplitHostPort(parsedEndpoint.Host); splitErr != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsedEndpoint.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	localScope := "mcp-content-sdk-" + suffix
	fs, err := internalfirestore.NewClientWithDatabaseAndScope("lwc-mcp-content-"+suffix, "", "", "", localScope)
	if err != nil {
		t.Fatalf("create scoped emulator Firestore client: %v", err)
	}
	defer fs.Close()

	userID, projectID := "mcp-content-owner-"+suffix, "mcp-content-project-"+suffix
	if _, err := internalfirestore.Collection(fs.Raw(), "users").Doc(userID).Set(ctx, auth.UserRecord{Email: userID + "@example.test", Status: auth.AccountActive, AuthVersion: 0}); err != nil {
		t.Fatalf("create local emulator account: %v", err)
	}
	if _, err := internalfirestore.Collection(fs.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx,
		map[string]any{"user_id": userID, "project_id": projectID, "name": "MCP content endpoint fixture", "status": "ready"}); err != nil {
		t.Fatalf("create local emulator Project: %v", err)
	}

	var holdBigRead atomic.Bool
	readStarted := make(chan struct{}, 1)
	releaseRead := make(chan struct{})
	var holdSmallRead atomic.Bool
	smallReadStarted := make(chan struct{}, 6)
	releaseSmallRead := make(chan struct{})
	var releaseSmallOnce sync.Once
	releaseSmall := func() { releaseSmallOnce.Do(func() { close(releaseSmallRead) }) }
	var holdCanceledRead atomic.Bool
	var failSourceSnapshotRead atomic.Bool
	var failCanceledBodyRead atomic.Bool
	var sourceSnapshotFailureInjected atomic.Bool
	canceledReadStarted := make(chan struct{}, 1)
	canceledBodyReturned := make(chan struct{}, 1)
	releaseCanceledRead := make(chan struct{})
	var releaseCanceledOnce sync.Once
	releaseCanceled := func() { releaseCanceledOnce.Do(func() { close(releaseCanceledRead) }) }
	var releaseOnce sync.Once
	releaseBigRead := func() { releaseOnce.Do(func() { close(releaseRead) }) }
	fixtureObjects := mcpPublishedContentFixture(t, userID, projectID)
	archiveManifestPath, err := generation.ArchivedManifestPath("g_mcp_content_fixture")
	if err != nil {
		t.Fatal(err)
	}
	var captureMetadataStages atomic.Bool
	var stageSamplesMu sync.Mutex
	var stageSamples []mcpContentStageMemorySample
	recordStageSample := func(stage string) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		stageSamplesMu.Lock()
		stageSamples = append(stageSamples, mcpContentStageMemorySample{stage: stage, totalAlloc: memory.TotalAlloc, heapAlloc: memory.HeapAlloc, rssPeak: mcpProcessPeakRSSSampleBytes()})
		stageSamplesMu.Unlock()
	}
	contentClient, _, _ := newTaggingGCSClientWithReadFailure(t, fixtureObjects, func(name string) {
		if captureMetadataStages.Load() {
			switch {
			case strings.HasSuffix(name, "/"+archiveManifestPath):
				recordStageSample("before_pinned_archive_manifest_read")
			case strings.HasSuffix(name, "/"+wikiindex.IDMapPath):
				recordStageSample("after_manifest_before_id_map_read")
			case strings.HasSuffix(name, "/"+wikiindex.ConceptsJSONLPath):
				recordStageSample("after_id_map_decode_before_concepts_read")
			case strings.HasSuffix(name, "/wiki/alpha.md"):
				recordStageSample("after_concepts_decode_snapshot_build_before_page_read")
			}
		}
		if holdSmallRead.Load() && strings.HasSuffix(name, "/wiki/alpha.md") {
			select {
			case smallReadStarted <- struct{}{}:
			default:
			}
			<-releaseSmallRead
		}
		if strings.HasSuffix(name, "/wiki/big-one.md") && holdCanceledRead.CompareAndSwap(true, false) {
			select {
			case canceledReadStarted <- struct{}{}:
			default:
			}
			<-releaseCanceledRead
			canceledBodyReturned <- struct{}{}
		}
		if holdBigRead.Load() && strings.HasSuffix(name, "/wiki/big-one.md") {
			select {
			case readStarted <- struct{}{}:
			default:
			}
			<-releaseRead
		}
	}, func(name string) bool {
		if strings.Contains(name, generation.SourceSnapshotPrefix) && failSourceSnapshotRead.Load() {
			sourceSnapshotFailureInjected.Store(true)
			return true
		}
		// The canceled fixture body is 64 MiB; fail after its barrier instead of
		// making the test server write that body to the canceled client.
		if strings.HasSuffix(name, "/wiki/big-one.md") && failCanceledBodyRead.CompareAndSwap(true, false) {
			return true
		}
		return false
	})
	t.Cleanup(releaseBigRead)
	t.Cleanup(releaseSmall)
	t.Cleanup(releaseCanceled)
	probe := &mcpContentAdmissionProbeRoot{RootStore: localfs.New(t.TempDir()), fs: fs.Raw(), content: contentClient}
	handler := New(probe, fs, nil, nil, nil, nil)
	handler.SetMCPConfig(mcpContentEndpointTestConfig())
	handler.mcpEnvironment = "local"
	handler.profileRepository = nil
	handler.SetQueryExecutor(&mcpTestExecutor{result: query.Result{
		Query: "published source citation", Mode: "full", Status: "evidence_found", AnswerBasis: "project_evidence",
		Results:   []search.Result{{ID: "concept-one", Slug: "alpha", Title: "Alpha", Type: "concept"}},
		AISynth:   "The published Alpha concept supports this answer.",
		Citations: []search.Citation{{ID: "concept-one", Text: "Alpha", Slug: "alpha", Type: "concept", Path: "/concepts/concept-one-alpha"}},
	}})
	gin.SetMode(gin.TestMode)

	readKey, readKeyToken, _, err := handler.projectKeyService.CreateWithCapabilities(ctx, userID, projectID, "MCP content read fixture", []string{"query", "content:read"})
	if err != nil || readKeyToken == "" || len(readKey.Capabilities) != 2 || readKey.Capabilities[0] != "query" || readKey.Capabilities[1] != "content:read" {
		t.Fatalf("create read-scoped project key: metadata=%+v token=%t err=%v", readKey, readKeyToken != "", err)
	}
	queryKey, queryKeyToken, _, err := handler.projectKeyService.Create(ctx, userID, projectID, "MCP query-only fixture")
	if err != nil || queryKeyToken == "" || len(queryKey.Capabilities) != 1 || queryKey.Capabilities[0] != "query" {
		t.Fatalf("create query-only project key: metadata=%+v token=%t err=%v", queryKey, queryKeyToken != "", err)
	}

	const resourceOrigin = "http://localhost:18080"
	const issuer = "http://localhost:18081"
	service := auth.NewMCPOAuthServiceWithPolicy(fs.Raw(), "mcp-content-oauth-"+suffix, "local", issuer, resourceOrigin,
		"http://localhost:13000", "local-test-oauth-secret", mcpContentOAuthResources(resourceOrigin))
	handler.SetConsumerVerifier(mcpOAuthTestConsumer{service: service, issuer: issuer})
	handler.SetCLIProjectAuthorizer(auth.FirestoreProjectOwnerAuthorizer(fs.Raw()))
	makeOAuthToken := func(requestedScopes, clientName, callbackPath string) string {
		t.Helper()
		client, err := service.RegisterClient(ctx, clientName, []string{"http://127.0.0.1:43125" + callbackPath})
		if err != nil {
			t.Fatalf("register local OAuth client: %v", err)
		}
		verifier := strings.Repeat("b", 43)
		challengeBytes := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
		resource := service.ResourceURL(projectID)
		consentURL, err := service.BeginAuthorization(ctx, client.ClientID, client.RedirectURIs[0], "code", challenge, "S256", "state-"+clientName, resource, requestedScopes)
		if err != nil {
			t.Fatalf("begin local OAuth authorization: %v", err)
		}
		parsed, err := url.Parse(consentURL)
		if err != nil {
			t.Fatalf("parse local consent URL: %v", err)
		}
		transactionID := parsed.Query().Get("transaction_id")
		if _, err := service.ConsentDetails(ctx, userID, 0, transactionID); err != nil {
			t.Fatalf("read local consent details: %v", err)
		}
		decision, err := service.DecideConsent(ctx, userID, 0, transactionID, true)
		if err != nil {
			t.Fatalf("approve local consent: %v", err)
		}
		callback, err := url.Parse(decision.RedirectURL)
		if err != nil {
			t.Fatalf("parse local OAuth callback: %v", err)
		}
		tokens, err := service.ExchangeCode(ctx, client.ClientID, callback.Query().Get("code"), client.RedirectURIs[0], verifier, resource)
		if err != nil {
			t.Fatalf("exchange local OAuth authorization code: %v", err)
		}
		return tokens.AccessToken
	}
	readOAuthToken := makeOAuthToken("mcp:query mcp:content:read", "MCP content read fixture", "/callback")
	queryOAuthToken := makeOAuthToken("mcp:query", "MCP query-only fixture", "/query-callback")
	const metaCanary = "MCP_META_OVERFLOW_SENTINEL"
	const errorCanary = "MCP_ERROR_DATA_OVERFLOW_SENTINEL"
	var metaProducerCalls atomic.Int32
	var errorProducerCalls atomic.Int32
	registerMetaProbe := func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "list_content", InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"],"additionalProperties":false}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				metaProducerCalls.Add(1)
				return &mcp.CallToolResult{Meta: mcp.Meta{"probe": strings.Repeat(metaCanary, 24_000)}}, nil
			})
	}
	registerErrorProbe := func(server *mcp.Server) {
		data, _ := json.Marshal(map[string]any{"probe": strings.Repeat(errorCanary, 24_000)})
		server.AddTool(&mcp.Tool{Name: "list_content", InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"],"additionalProperties":false}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				errorProducerCalls.Add(1)
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "synthetic wire error", Data: data}
			})
	}

	router := gin.New()
	router.Any("/mcp", gin.WrapH(handler.MCPHandler()))
	router.Any("/mcp/projects/:projectID", handler.MCPProjectOAuthHandler("local", resourceOrigin))
	server := httptest.NewServer(router)
	defer server.Close()

	for _, endpoint := range []struct {
		name, path, token, projectHeader string
	}{
		{name: "project-key", path: "/mcp", token: readKeyToken, projectHeader: projectID},
		{name: "oauth", path: "/mcp/projects/" + projectID, token: readOAuthToken},
	} {
		postWireWithContext := func(body []byte, requestContext context.Context) *httptest.ResponseRecorder {
			t.Helper()
			request := httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewReader(body)).WithContext(requestContext)
			request.Header.Set("Authorization", "Bearer "+endpoint.token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if endpoint.projectHeader != "" {
				request.Header.Set("X-Project-ID", endpoint.projectHeader)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			return response
		}
		postWire := func(body []byte) *httptest.ResponseRecorder { return postWireWithContext(body, context.Background()) }
		assertRejectedNoEcho := func(name string, body []byte, sentinel string) {
			t.Helper()
			response := postWire(body)
			if response.Body.Len() > maxMCPProtectedResponseBytes || !strings.Contains(response.Body.String(), `"id":null`) ||
				strings.Contains(response.Body.String(), sentinel) {
				t.Fatalf("%s %s wire rejection status=%d bytes=%d body=%q", endpoint.name, name, response.Code, response.Body.Len(), response.Body.String())
			}
		}
		longID := strings.Repeat("L", maxMCPProtectedIDDecoded+1)
		longIDBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": longID, "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept"}}})
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedNoEcho("long ID", longIDBody, strings.Repeat("L", 64))
		largeMetaBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "large-meta", "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept"}, "_meta": strings.Repeat("M", maxMCPProtectedMetaBytes+1)}})
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedNoEcho("oversized _meta", largeMetaBody, strings.Repeat("M", 64))
		largeParamsBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "large-params", "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept", "unexpected": strings.Repeat("P", maxMCPProtectedParams)}}})
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedNoEcho("oversized params", largeParamsBody, strings.Repeat("P", 64))
		protocolErrorBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "protocol-meta", "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept"}, "_meta": "invalid-meta-shape"}})
		if err != nil {
			t.Fatal(err)
		}
		protocolError := postWire(protocolErrorBody)
		if protocolError.Body.Len() > maxMCPProtectedResponseBytes || !strings.Contains(protocolError.Body.String(), `"id":"protocol-meta"`) || !strings.Contains(protocolError.Body.String(), `"error"`) {
			t.Fatalf("%s SDK protocol error wire response status=%d bytes=%d body=%q", endpoint.name, protocolError.Code, protocolError.Body.Len(), protocolError.Body.String())
		}
		assertWireID := func(label, id string) {
			t.Helper()
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept", "limit": 1}}})
			if err != nil {
				t.Fatal(err)
			}
			response := postWire(body)
			var decoded struct {
				ID    any             `json:"id"`
				Error json.RawMessage `json:"error"`
			}
			if response.Code != http.StatusOK || response.Body.Len() > maxMCPProtectedResponseBytes ||
				json.Unmarshal(response.Body.Bytes(), &decoded) != nil || decoded.ID != id || len(decoded.Error) != 0 {
				t.Fatalf("%s %s wire response status=%d bytes=%d decoded=%+v body=%q", endpoint.name, label, response.Code, response.Body.Len(), decoded, response.Body.String())
			}
		}
		assertWireID("max decoded ID", strings.Repeat("B", maxMCPProtectedIDDecoded))
		assertWireID("escaped ID", "wire\"\\\\\nλ"+strings.Repeat("x", 40))
		encodedBoundaryID := `"` + strings.Repeat(`\u0078`, 682) + `\t"`
		if len(encodedBoundaryID) != maxMCPProtectedIDEncoded {
			t.Fatalf("constructed JSON string ID encoding has %d bytes, want exactly 4096", len(encodedBoundaryID))
		}
		boundaryBody := []byte(`{"jsonrpc":"2.0","id":` + encodedBoundaryID + `,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept","limit":1}}}`)
		boundaryResponse := postWire(boundaryBody)
		var boundaryDecoded struct {
			ID any `json:"id"`
		}
		if boundaryResponse.Code != http.StatusOK || boundaryResponse.Body.Len() > maxMCPProtectedResponseBytes || json.Unmarshal(boundaryResponse.Body.Bytes(), &boundaryDecoded) != nil || boundaryDecoded.ID != strings.Repeat("x", 682)+"\t" {
			t.Fatalf("%s exact encoded-ID boundary response status=%d bytes=%d decoded=%+v body=%q", endpoint.name, boundaryResponse.Code, boundaryResponse.Body.Len(), boundaryDecoded, boundaryResponse.Body.String())
		}
		overBoundaryID := `"` + strings.Repeat(`\u0078`, 682) + `\tx"`
		if len(overBoundaryID) != maxMCPProtectedIDEncoded+1 {
			t.Fatalf("constructed over-boundary JSON string ID encoding has %d bytes, want 4097", len(overBoundaryID))
		}
		overBoundaryBody := []byte(`{"jsonrpc":"2.0","id":` + overBoundaryID + `,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept","limit":1}}}`)
		assertRejectedNoEcho("encoded ID over 4096", overBoundaryBody, "xxxxxxxx")
		assertOverflowFallback := func(name, canary string, calls *atomic.Int32, register func(*mcp.Server)) {
			t.Helper()
			id := "overflow-" + endpoint.name + "-" + name
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "list_content", "arguments": map[string]any{"kind": "concept"}}})
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			wireProbeContext := context.WithValue(context.Background(), mcpSDKTestToolRegistrarContextKey{}, register)
			response := postWireWithContext(body, wireProbeContext)
			var decoded struct {
				ID     any `json:"id"`
				Result struct {
					Structured map[string]any `json:"structuredContent"`
					IsError    bool           `json:"isError"`
				} `json:"result"`
			}
			if response.Code != http.StatusOK || response.Body.Len() > maxMCPProtectedResponseBytes || json.Unmarshal(response.Body.Bytes(), &decoded) != nil ||
				decoded.ID != id || !decoded.Result.IsError || decoded.Result.Structured["code"] != "response_limit" || strings.Contains(response.Body.String(), canary) || calls.Load() != before+1 {
				t.Fatalf("%s %s final SDK overflow response status=%d bytes=%d decoded=%+v producer_calls=%d->%d body=%q", endpoint.name, name, response.Code, response.Body.Len(), decoded, before, calls.Load(), response.Body.String())
			}
		}
		assertOverflowFallback("result-meta", metaCanary, &metaProducerCalls, registerMetaProbe)
		assertOverflowFallback("protocol-error-data", errorCanary, &errorProducerCalls, registerErrorProbe)
		t.Logf("protected_wire endpoint=%s max_decoded_id=1024 encoded_id_4096_legal_4097_rejected=true escaped_id_roundtrip=true oversized_request_id_meta_params=id_null_no_echo sdk_result_meta_and_error_data_overflow=response_limit_no_echo single_execution=true protocol_error_bytes=%d", endpoint.name, protocolError.Body.Len())
	}

	readSession := connectMCPWithBearer(t, ctx, server.URL+"/mcp", readKeyToken, projectID)
	keyTools, err := readSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("Project-key SDK tools/list: %v", err)
	}
	keyToolNames := make(map[string]bool, len(keyTools.Tools))
	for _, tool := range keyTools.Tools {
		keyToolNames[tool.Name] = true
	}
	for _, name := range []string{"list_content", "find_content", "read_content", "read_source_raw"} {
		if !keyToolNames[name] {
			t.Fatalf("read key tools/list omitted %q: %v", name, keyToolNames)
		}
	}
	if keyToolNames["add_raw"] || keyToolNames["import_url"] || keyToolNames["import_document"] {
		t.Fatalf("read key exposed a write tool: %v", keyToolNames)
	}

	queryResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "query_project", Arguments: map[string]any{"q": "published source citation", "mode": "full"}})
	if err != nil || queryResult == nil || queryResult.IsError {
		t.Fatalf("SDK query_project citation response=%+v err=%v", queryResult, err)
	}
	queryPayload, ok := queryResult.StructuredContent.(map[string]any)
	citations, _ := queryPayload["citations"].([]any)
	if !ok || queryPayload["content_generation"] != "g_mcp_content_fixture" || len(citations) != 1 {
		t.Fatalf("SDK query citation did not expose a pinned generation and one candidate: %#v", queryResult.StructuredContent)
	}
	citation, _ := citations[0].(map[string]any)
	if citation["id"] != "concept-one" || citation["path"] != "/concepts/concept-one-alpha" {
		t.Fatalf("query citation does not identify the stable Concept route: %#v", citation)
	}
	fixtureSize := func(suffix string) int64 {
		for name, object := range fixtureObjects {
			if strings.HasSuffix(name, suffix) {
				return int64(len(object.data))
			}
		}
		t.Fatalf("published test fixture is missing %s", suffix)
		return 0
	}
	stageObjectSizes := map[string]int64{
		"manifest": fixtureSize("/" + archiveManifestPath),
		"id_map":   fixtureSize("/" + wikiindex.IDMapPath),
		"concepts": fixtureSize("/" + wikiindex.ConceptsJSONLPath),
		"markdown": fixtureSize("/wiki/alpha.md"),
	}
	stageSamples = nil
	recordStageSample("before_sdk_read_content")
	captureMetadataStages.Store(true)
	conceptResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": citation["id"], "generation": queryPayload["content_generation"]}})
	captureMetadataStages.Store(false)
	recordStageSample("after_sdk_read_content_response")
	if err != nil || conceptResult == nil || conceptResult.IsError {
		t.Fatalf("follow query citation into pinned Concept: result=%+v err=%v", conceptResult, err)
	}
	stageSamplesMu.Lock()
	measuredStages := append([]mcpContentStageMemorySample(nil), stageSamples...)
	stageSamplesMu.Unlock()
	if len(measuredStages) != 6 {
		t.Fatalf("SDK content stage profiler saw %d of six boundaries: %+v", len(measuredStages), measuredStages)
	}
	stageReservations := []int64{
		int64(generation.MaxManifestBytes) + 1<<20,
		stageObjectSizes["id_map"]*4 + 128<<10,
		stageObjectSizes["concepts"]*3 + 128<<10,
		stageObjectSizes["markdown"] + 1<<20,
	}
	previous := measuredStages[0]
	for i, sample := range measuredStages[1:] {
		allocated := sample.totalAlloc - previous.totalAlloc
		t.Logf("mcp_content_working_set_stage stage=%s allocated_delta_bytes=%d heap_alloc_bytes=%d process_rss_peak_bytes=%d next_reservation_bytes=%d id_map_bytes=%d concepts_bytes=%d markdown_bytes=%d", sample.stage, allocated, sample.heapAlloc, sample.rssPeak, stageReservations[min(i, len(stageReservations)-1)], stageObjectSizes["id_map"], stageObjectSizes["concepts"], stageObjectSizes["markdown"])
		previous = sample
	}
	conceptPayload, _ := conceptResult.StructuredContent.(map[string]any)
	conceptSynthesis, _ := conceptPayload["synthesis_sources"].([]any)
	if conceptPayload["generation"] != queryPayload["content_generation"] || len(conceptSynthesis) != 1 {
		t.Fatalf("Concept citation follow-up changed generation or omitted source provenance: %#v", conceptResult.StructuredContent)
	}
	sourceRef, _ := conceptSynthesis[0].(map[string]any)
	if sourceRef["id"] != "source-one" || sourceRef["relation"] != "synthesis_source" {
		t.Fatalf("Concept provenance does not lead to the stable Source ID: %#v", conceptSynthesis)
	}
	sourceResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "source", "id": sourceRef["id"], "generation": queryPayload["content_generation"]}})
	if err != nil || sourceResult == nil || sourceResult.IsError {
		t.Fatalf("follow Concept provenance into pinned Source: result=%+v err=%v", sourceResult, err)
	}
	rawFollowup, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_source_raw", Arguments: map[string]any{"source_id": sourceRef["id"], "generation": queryPayload["content_generation"]}})
	if err != nil || rawFollowup == nil || rawFollowup.IsError {
		t.Fatalf("follow Source into publisher-retained raw bytes: result=%+v err=%v", rawFollowup, err)
	}
	rawFollowupPayload, _ := rawFollowup.StructuredContent.(map[string]any)
	rawFollowupContent, _ := rawFollowupPayload["content"].(map[string]any)
	if rawFollowupPayload["generation"] != queryPayload["content_generation"] || rawFollowupContent["text"] != "Retained source text from the publisher.\n" {
		t.Fatalf("query→Concept→Source→Raw chain did not retain its generation and source bytes: %#v", rawFollowup.StructuredContent)
	}
	t.Logf("query_citation_followup generation=%s citation=%s source=%s raw_bytes=%d", queryPayload["content_generation"], citation["id"], sourceRef["id"], len(rawFollowupContent["text"].(string)))

	querySession := connectMCPWithBearer(t, ctx, server.URL+"/mcp", queryKeyToken, projectID)
	queryTools, err := querySession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("query-only SDK tools/list: %v", err)
	}
	for _, tool := range queryTools.Tools {
		if tool.Name == "list_content" || tool.Name == "find_content" || tool.Name == "read_content" || tool.Name == "read_source_raw" {
			t.Fatalf("query-only key discovered content tool %q", tool.Name)
		}
	}
	probe.reset()
	if result, err := querySession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}}); err == nil && (result == nil || !result.IsError) {
		t.Fatalf("direct content call bypassed query-only scope: result=%+v", result)
	}
	if observations := probe.observations(); len(observations) != 0 {
		t.Fatalf("query-only content denial reached storage: %+v", observations)
	}

	probe.reset()
	keyResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}})
	if err != nil || keyResult == nil || keyResult.IsError {
		t.Fatalf("read key list_content response=%+v err=%v storage=%+v", keyResult, err, probe.observations())
	}
	keyPayload, ok := keyResult.StructuredContent.(map[string]any)
	items, _ := keyPayload["items"].([]any)
	if !ok || keyPayload["generation"] != "g_mcp_content_fixture" || len(items) != 4 {
		t.Fatalf("read key list did not return published fixture: %#v", keyResult.StructuredContent)
	}
	if observations := probe.observations(); len(observations) != 1 || !observations[0].active || observations[0].projectTokens <= 0 || observations[0].projectTokens >= 31_000_000 {
		t.Fatalf("Project-key storage scope was not reached under its active admission lease: %+v", observations)
	}

	// Six body reads are held at a barrier after shared admission and local
	// reservation. All must coexist under the unchanged 128 MiB working-set cap.
	type smallResult struct {
		result *mcp.CallToolResult
		err    error
	}
	smallResults := make(chan smallResult, 6)
	holdSmallRead.Store(true)
	for i := 0; i < 6; i++ {
		go func() {
			result, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "concept-one"}})
			smallResults <- smallResult{result: result, err: err}
		}()
	}
	for i := 0; i < 6; i++ {
		select {
		case <-smallReadStarted:
		case <-time.After(10 * time.Second):
			releaseSmall()
			holdSmallRead.Store(false)
			var outcomes []string
			for j := 0; j < 6; j++ {
				select {
				case got := <-smallResults:
					var payload map[string]any
					if got.result != nil {
						payload, _ = got.result.StructuredContent.(map[string]any)
					}
					outcomes = append(outcomes, fmt.Sprintf("is_error=%t code=%v retry_after=%v err=%v", got.result != nil && got.result.IsError, payload["code"], payload["retry_after_ms"], got.err))
				case <-time.After(5 * time.Second):
					j = 6
				}
			}
			var contentRecords []string
			if docs, err := internalfirestore.Collection(fs.Raw(), "mcp_admission_v1").Documents(context.Background()).GetAll(); err == nil {
				for _, doc := range docs {
					data := doc.Data()
					if data["class"] == "content" {
						contentRecords = append(contentRecords, fmt.Sprintf("dimension=%v tokens=%v limits=%v leases=%v", data["dimension"], data["tokens_microunits"], data["limits"], data["leases"]))
					}
				}
			}
			t.Fatalf("only %d of six small reads reached the storage barrier; outcomes=%v probe=%+v records=%v", i, outcomes, probe.observations(), contentRecords)
		}
	}
	releaseSmall()
	holdSmallRead.Store(false)
	for i := 0; i < 6; i++ {
		select {
		case got := <-smallResults:
			if got.err != nil || got.result == nil || got.result.IsError {
				t.Fatalf("normal six-read burst failed: result=%+v err=%v", got.result, got.err)
			}
		case <-ctx.Done():
			t.Fatal("six-read burst did not settle after releasing the barrier")
		}
	}
	t.Logf("normal_small_read_burst admitted_and_settled=6/6 barrier=wiki/alpha.md")
	cancelCtx, cancelRead := context.WithCancel(ctx)
	type canceledOutcome struct {
		result *mcp.CallToolResult
		err    error
	}
	canceledRead := make(chan canceledOutcome, 1)
	holdCanceledRead.Store(true)
	go func() {
		result, err := callMCPToolWithBoundedAdmissionRetry(t, cancelCtx, readSession, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-one"}})
		canceledRead <- canceledOutcome{result: result, err: err}
	}()
	select {
	case <-canceledReadStarted:
	case outcome := <-canceledRead:
		releaseCanceled()
		var payload any
		if outcome.result != nil {
			payload = outcome.result.StructuredContent
		}
		t.Fatalf("cancellable SDK read settled before reaching its held storage body: result=%+v err=%v", payload, outcome.err)
	case <-time.After(10 * time.Second):
		releaseCanceled()
		t.Fatal("cancellable MCP read did not reach its held storage body within 10 seconds")
	}
	cancelRead()
	failCanceledBodyRead.Store(true)
	var cancelOutcome canceledOutcome
	select {
	case cancelOutcome = <-canceledRead:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled MCP SDK call did not settle while the product producer remained blocked")
	}
	if !errors.Is(cancelOutcome.err, context.Canceled) {
		t.Fatalf("canceled MCP SDK call error=%v, want context canceled", cancelOutcome.err)
	}
	competingRead, competingErr := callMCPToolWithBoundedAdmissionRetry(t, ctx, readSession, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-one"}})
	if competingErr != nil || competingRead == nil || !competingRead.IsError {
		t.Fatalf("competing SDK read while canceled producer remained blocked: result=%+v err=%v", competingRead, competingErr)
	}
	competingPayload, _ := competingRead.StructuredContent.(map[string]any)
	if competingPayload["code"] != "capacity_limited" || competingPayload["retry_after_ms"] != float64(250) {
		t.Fatalf("canceled producer released capacity before settling: %#v", competingRead.StructuredContent)
	}
	releaseCanceled()
	select {
	case <-canceledBodyReturned:
	case <-ctx.Done():
		t.Fatal("canceled storage producer did not resume after releasing the barrier")
	}
	cancelRead()
	recoveredRead, recoveredErr := callMCPToolUntilCapacityRecovers(t, ctx, readSession, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-one"}})
	if recoveredErr != nil || recoveredRead == nil || recoveredRead.IsError {
		t.Fatalf("capacity did not recover after canceled producer settled: result=%+v err=%v", recoveredRead, recoveredErr)
	}
	t.Logf("canceled_read client_canceled_while_producer_blocked=true competing_read=capacity_limited producer_released=true recovered_read=success")
	failSourceSnapshotRead.Store(true)
	transientRaw, transientErr := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_source_raw", Arguments: map[string]any{"source_id": "source-one"}})
	failSourceSnapshotRead.Store(false)
	if transientErr != nil || transientRaw == nil || !transientRaw.IsError {
		t.Fatalf("transient source inventory storage failure response=%+v err=%v", transientRaw, transientErr)
	}
	transientPayload, _ := transientRaw.StructuredContent.(map[string]any)
	if !sourceSnapshotFailureInjected.Load() {
		t.Fatal("transient source snapshot failure control did not intercept a retained snapshot body read")
	}
	if transientPayload["code"] != "content_unavailable" || transientPayload["retryable"] != true || transientPayload["retry_after_ms"] != float64(1000) {
		t.Fatalf("transient source inventory failure was not retryable content_unavailable: %#v", transientRaw.StructuredContent)
	}
	rawResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_source_raw", Arguments: map[string]any{"source_id": "source-one"}})
	if err != nil || rawResult == nil || rawResult.IsError {
		t.Fatalf("read key read_source_raw response=%+v err=%v", rawResult, err)
	}
	rawPayload, ok := rawResult.StructuredContent.(map[string]any)
	rawBody, _ := rawPayload["content"].(map[string]any)
	if !ok || rawPayload["representation"] != "retained_source_text" || rawBody["text"] != "Retained source text from the publisher.\n" {
		t.Fatalf("retained raw Source response=%#v", rawResult.StructuredContent)
	}

	findResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "find_content", Arguments: map[string]any{"kind": "concept", "name": "Alpha"}})
	if err != nil || findResult == nil || findResult.IsError {
		t.Fatalf("read key find_content response=%+v err=%v", findResult, err)
	}
	findPayload, ok := findResult.StructuredContent.(map[string]any)
	candidates, _ := findPayload["candidates"].([]any)
	if !ok || findPayload["status"] != "ambiguous" || len(candidates) != 2 {
		t.Fatalf("exact-name ambiguity was not returned through SDK: %#v", findResult.StructuredContent)
	}

	readResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "concept-one"}})
	if err != nil || readResult == nil || readResult.IsError {
		t.Fatalf("read key read_content response=%+v err=%v", readResult, err)
	}
	readPayload, ok := readResult.StructuredContent.(map[string]any)
	readBody, _ := readPayload["content"].(map[string]any)
	synthesis, _ := readPayload["synthesis_sources"].([]any)
	mentions, _ := readPayload["mentions"].([]any)
	if !ok || readBody["text"] != "---\nid: concept-one\ntitle: Alpha\nsources:\n  - source-one\n---\n# Alpha\n\nSee [[concept-two]] for comparison.\n" || len(synthesis) != 1 || len(mentions) != 1 {
		t.Fatalf("published Markdown/provenance/mentions response=%#v", readResult.StructuredContent)
	}
	synthesisRef, _ := synthesis[0].(map[string]any)
	mentionRef, _ := mentions[0].(map[string]any)
	if synthesisRef["relation"] != "synthesis_source" || synthesisRef["id"] != "source-one" || mentionRef["relation"] != "mention" || mentionRef["id"] != "concept-two" {
		t.Fatalf("provenance and Markdown mentions were conflated: synthesis=%#v mentions=%#v", synthesis, mentions)
	}
	readText := readResult.Content[0].(*mcp.TextContent).Text
	var textPayload map[string]any
	if err := json.Unmarshal([]byte(readText), &textPayload); err != nil {
		t.Fatalf("decode SDK text result: %v", err)
	}
	emptyResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "concept-two"}})
	if err != nil || emptyResult == nil || emptyResult.IsError {
		t.Fatalf("read empty provenance/mentions response=%+v err=%v", emptyResult, err)
	}
	emptyPayload, _ := emptyResult.StructuredContent.(map[string]any)
	emptySynthesis, synthesisOK := emptyPayload["synthesis_sources"].([]any)
	emptyMentions, mentionsOK := emptyPayload["mentions"].([]any)
	var emptyText map[string]any
	if err := json.Unmarshal([]byte(emptyResult.Content[0].(*mcp.TextContent).Text), &emptyText); err != nil {
		t.Fatalf("decode empty SDK text result: %v", err)
	}
	if !synthesisOK || !mentionsOK || len(emptySynthesis) != 0 || len(emptyMentions) != 0 || emptyText["synthesis_sources"] == nil || emptyText["mentions"] == nil {
		t.Fatalf("SDK empty provenance arrays were null or omitted: structured=%#v text=%#v", emptyPayload, emptyText)
	}

	mismatchedRaw, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_source_raw", Arguments: map[string]any{"source_id": "source-two"}})
	if err != nil || mismatchedRaw == nil || !mismatchedRaw.IsError {
		t.Fatalf("nonempty mismatched SourceFile metadata was accepted: result=%+v err=%v", mismatchedRaw, err)
	}
	mismatchedPayload, _ := mismatchedRaw.StructuredContent.(map[string]any)
	if mismatchedPayload["code"] != "raw_mapping_unavailable" {
		t.Fatalf("mismatched SourceFile error=%#v", mismatchedRaw.StructuredContent)
	}

	runtime.GC()
	var heapBefore, heapAfter runtime.MemStats
	runtime.ReadMemStats(&heapBefore)
	rssBefore := mcpProcessPeakRSSBytes(t)
	largeResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-one"}})
	if err != nil || largeResult == nil || largeResult.IsError {
		t.Fatalf("64 MiB published file read response=%+v err=%v", largeResult, err)
	}
	largePayload, ok := largeResult.StructuredContent.(map[string]any)
	largeBody, _ := largePayload["content"].(map[string]any)
	largeText, _ := largeBody["text"].(string)
	largeCursor, _ := largeBody["next_cursor"].(string)
	if !ok || largeBody["total_bytes"] != float64(generation.MaxFileBytes) || largeBody["end_byte"] != float64(16<<10) || largeBody["complete"] != false || largeCursor == "" || len(largeText) != 16<<10 {
		t.Fatalf("64 MiB read did not return a bounded successful chunk: %#v", largeResult.StructuredContent)
	}
	runtime.ReadMemStats(&heapAfter)
	rssAfter := mcpProcessPeakRSSBytes(t)
	t.Logf("large_single file_bytes=%d response_chunk_bytes=%d heap_alloc_before=%d heap_alloc_after=%d rss_peak_before=%d rss_peak_after=%d", generation.MaxFileBytes, len(largeText), heapBefore.Alloc, heapAfter.Alloc, rssBefore, rssAfter)

	// Keep one SDK response inside its final bounded HTTP writer. The active
	// request's working-set reservations must remain unavailable until Commit.
	blockedWriter := newMCPCommitBlockingWriter()
	t.Cleanup(blockedWriter.releaseWrite)
	blockedRequest := httptest.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"held-write","method":"tools/call","params":{"name":"read_content","arguments":{"kind":"concept","id":"big-one"}}}`))
	blockedRequest.Header.Set("Authorization", "Bearer "+readKeyToken)
	blockedRequest.Header.Set("X-Project-ID", projectID)
	blockedRequest.Header.Set("Content-Type", "application/json")
	blockedRequest.Header.Set("Accept", "application/json, text/event-stream")
	blockedDone := make(chan struct{})
	go func() {
		handler.MCPHandler().ServeHTTP(blockedWriter, blockedRequest)
		close(blockedDone)
	}()
	select {
	case <-blockedWriter.started:
	case <-ctx.Done():
		t.Fatal("SDK response did not reach the blocked final writer")
	}
	capacityRequest := httptest.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"capacity-check","method":"tools/call","params":{"name":"read_content","arguments":{"kind":"concept","id":"big-two"}}}`))
	capacityRequest.Header.Set("Authorization", "Bearer "+readKeyToken)
	capacityRequest.Header.Set("X-Project-ID", projectID)
	capacityRequest.Header.Set("Content-Type", "application/json")
	capacityRequest.Header.Set("Accept", "application/json, text/event-stream")
	capacityRecorder := httptest.NewRecorder()
	handler.MCPHandler().ServeHTTP(capacityRecorder, capacityRequest)
	var capacityEnvelope map[string]any
	if err := json.Unmarshal(capacityRecorder.Body.Bytes(), &capacityEnvelope); err != nil {
		t.Fatalf("decode capacity response while first Commit is blocked: %v body=%q", err, capacityRecorder.Body.String())
	}
	capacityResult, _ := capacityEnvelope["result"].(map[string]any)
	capacityPayload, _ := capacityResult["structuredContent"].(map[string]any)
	if capacityPayload["code"] != "capacity_limited" || capacityPayload["retry_after_ms"] != float64(250) {
		t.Fatalf("working set was reused before bounded Commit: status=%d body=%s", capacityRecorder.Code, capacityRecorder.Body.String())
	}
	t.Logf("blocked_final_commit first_request_held=true second_request=capacity_limited retry_after_ms=250")
	blockedWriter.releaseWrite()
	select {
	case <-blockedDone:
	case <-ctx.Done():
		t.Fatal("blocked SDK request did not settle after writer release")
	}

	secondLargeSession := connectMCPWithBearer(t, ctx, server.URL+"/mcp", readKeyToken, projectID)
	type largeCallResult struct {
		result *mcp.CallToolResult
		err    error
	}
	firstLargeCall := make(chan largeCallResult, 1)
	holdBigRead.Store(true)
	go func() {
		result, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-one"}})
		firstLargeCall <- largeCallResult{result: result, err: err}
	}()
	select {
	case <-readStarted:
	case <-ctx.Done():
		t.Fatal("first concurrent big read did not reach its held GCS body")
	}
	secondLarge, secondErr := secondLargeSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_content", Arguments: map[string]any{"kind": "concept", "id": "big-two"}})
	releaseBigRead()
	firstLarge := <-firstLargeCall
	if firstLarge.err != nil || firstLarge.result == nil || firstLarge.result.IsError {
		t.Fatalf("first concurrent big file did not complete: result=%+v err=%v", firstLarge.result, firstLarge.err)
	}
	holdBigRead.Store(false)
	if secondErr != nil || secondLarge == nil || !secondLarge.IsError {
		t.Fatalf("second concurrent big file was not honestly capacity-limited: result=%+v err=%v", secondLarge, secondErr)
	}
	secondPayload, _ := secondLarge.StructuredContent.(map[string]any)
	if secondPayload["code"] != "capacity_limited" {
		t.Fatalf("second concurrent big file code=%#v, want capacity_limited", secondPayload)
	}
	t.Logf("large_concurrent file_bytes_each=%d first=success second=%s work_bytes_cap=%d", generation.MaxFileBytes, secondPayload["code"], mcpContentEndpointTestConfig().Protection.Content.WorkingBytes)

	oauthServerURL := server.URL + "/mcp/projects/" + projectID
	oauthSession := connectMCPWithBearer(t, ctx, oauthServerURL, readOAuthToken, "")
	oauthTools, err := oauthSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("OAuth SDK tools/list: %v", err)
	}
	oauthToolNames := make(map[string]bool, len(oauthTools.Tools))
	for _, tool := range oauthTools.Tools {
		oauthToolNames[tool.Name] = true
	}
	for _, name := range []string{"list_content", "find_content", "read_content", "read_source_raw"} {
		if !oauthToolNames[name] {
			t.Fatalf("OAuth read scope tools/list omitted %q: %v", name, oauthToolNames)
		}
	}
	probe.reset()
	oauthResult, err := oauthSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}})
	if err != nil || oauthResult == nil || oauthResult.IsError {
		t.Fatalf("OAuth list_content response=%+v err=%v", oauthResult, err)
	}
	oauthPayload, ok := oauthResult.StructuredContent.(map[string]any)
	oauthItems, _ := oauthPayload["items"].([]any)
	if !ok || oauthPayload["generation"] != "g_mcp_content_fixture" || len(oauthItems) != 4 {
		t.Fatalf("OAuth content success did not return published fixture: %#v", oauthResult.StructuredContent)
	}
	if observations := probe.observations(); len(observations) != 1 || !observations[0].active || observations[0].projectTokens <= 0 || observations[0].projectTokens >= 32_000_000 {
		t.Fatalf("OAuth storage scope was not reached under its active admission lease: %+v", observations)
	}

	queryOAuthSession := connectMCPWithBearer(t, ctx, oauthServerURL, queryOAuthToken, "")
	queryOAuthTools, err := queryOAuthSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("OAuth query-only SDK tools/list: %v", err)
	}
	for _, tool := range queryOAuthTools.Tools {
		if tool.Name == "list_content" || tool.Name == "find_content" || tool.Name == "read_content" || tool.Name == "read_source_raw" {
			t.Fatalf("query-only OAuth grant discovered content tool %q", tool.Name)
		}
	}
	probe.reset()
	if result, err := queryOAuthSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}}); err == nil && (result == nil || !result.IsError) {
		t.Fatalf("direct content call bypassed query-only OAuth scope: result=%+v", result)
	}
	if observations := probe.observations(); len(observations) != 0 {
		t.Fatalf("query-only OAuth denial reached storage: %+v", observations)
	}

	probe.reset()
	var limited *mcp.CallToolResult
	for i := 0; i < 40; i++ {
		observationsBefore := len(probe.observations())
		result, callErr := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}})
		if callErr != nil || result == nil {
			t.Fatalf("content quota exhaustion request %d response=%+v err=%v", i, result, callErr)
		}
		if result.IsError {
			if len(probe.observations()) != observationsBefore {
				t.Fatalf("quota-rejected cold content request reached storage: %+v", probe.observations())
			}
			payload, _ := result.StructuredContent.(map[string]any)
			if payload["code"] != "rate_limited" {
				t.Fatalf("content quota denial response=%#v", result.StructuredContent)
			}
			limited = result
			break
		}
	}
	if limited == nil {
		t.Fatal("content quota did not deny after exhausting its 32-token test bucket")
	}
}

type mcpCommitBlockingWriter struct {
	header      http.Header
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	body        bytes.Buffer
	status      int
}

func newMCPCommitBlockingWriter() *mcpCommitBlockingWriter {
	return &mcpCommitBlockingWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
}

func (w *mcpCommitBlockingWriter) Header() http.Header { return w.header }

func (w *mcpCommitBlockingWriter) WriteHeader(status int) { w.status = status }

func (w *mcpCommitBlockingWriter) Write(data []byte) (int, error) {
	w.startedOnce.Do(func() { close(w.started) })
	<-w.release
	return w.body.Write(data)
}

func (w *mcpCommitBlockingWriter) releaseWrite() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func TestMCPMetadataHeavySDKReadMeasuresReservedWorkingSet(t *testing.T) {
	emulatorHost := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulatorHost == "" {
		t.Skip("requires a loopback Firestore emulator")
	}
	endpoint, err := url.Parse("http://" + strings.TrimPrefix(emulatorHost, "http://"))
	if err != nil {
		t.Fatalf("invalid Firestore emulator host: %v", err)
	}
	if host, _, err := net.SplitHostPort(endpoint.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", endpoint.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	scope := "mcp-meta-heavy-" + suffix
	fs, err := internalfirestore.NewClientWithDatabaseAndScope("lwc-mcp-meta-heavy-"+suffix, "", "", "", scope)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer fs.Close()
	userID, projectID := "mcp-meta-user-"+suffix, "mcp-meta-project-"+suffix
	if _, err := internalfirestore.Collection(fs.Raw(), "users").Doc(userID).Set(ctx, auth.UserRecord{Email: userID + "@example.test", Status: auth.AccountActive}); err != nil {
		t.Fatalf("seed metadata-heavy emulator user: %v", err)
	}
	if _, err := internalfirestore.Collection(fs.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "name": "metadata-heavy fixture", "status": "ready"}); err != nil {
		t.Fatalf("seed metadata-heavy emulator project: %v", err)
	}
	_, token, _, err := auth.NewProjectKeyService(fs.Raw()).CreateWithCapabilities(ctx, userID, projectID, "metadata-heavy SDK fixture", []string{"query", "content:read"})
	if err != nil || token == "" {
		t.Fatalf("create metadata-heavy content key: token_present=%t err=%v", token != "", err)
	}
	objects, idMapBytes, conceptsBytes := mcpMetadataHeavyPublishedFixture(t, userID, projectID, 2000)
	contentClient, readsMu, reads := newTaggingGCSClient(t, objects)
	probe := &mcpContentAdmissionProbeRoot{RootStore: localfs.New(t.TempDir()), fs: fs.Raw(), content: contentClient}
	handler := New(probe, fs, nil, nil, nil, nil)
	handler.SetMCPConfig(mcpContentEndpointTestConfig())
	handler.SetMCPEnvironment("local")
	router := gin.New()
	router.Any("/mcp", gin.WrapH(handler.MCPHandler()))
	server := httptest.NewServer(router)
	defer server.Close()
	session := connectMCPWithBearer(t, ctx, server.URL+"/mcp", token, projectID)
	defer session.Close()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	rssBefore := mcpProcessPeakRSSBytes(t)
	stopSampler := make(chan struct{})
	var sampleMu sync.Mutex
	var peakHeap uint64
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSampler:
				return
			case <-ticker.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				sampleMu.Lock()
				peakHeap = max(peakHeap, sample.HeapAlloc)
				sampleMu.Unlock()
			}
		}
	}()
	result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept", "limit": 1}})
	close(stopSampler)
	sampler.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	rssAfter := mcpProcessPeakRSSBytes(t)
	if callErr != nil || result == nil || result.IsError {
		readsMu.Lock()
		readCopy := make(map[string]int, len(reads))
		for name, count := range reads {
			readCopy[name] = count
		}
		readsMu.Unlock()
		t.Fatalf("metadata-heavy real SDK list failed: result=%+v err=%v storage_reads=%v", result, callErr, readCopy)
	}
	payload, _ := result.StructuredContent.(map[string]any)
	items, _ := payload["items"].([]any)
	if len(items) != 1 || payload["next_cursor"] == nil {
		t.Fatalf("metadata-heavy SDK list response=%#v", payload)
	}
	objectPrefix := "users/" + userID + "/projects/" + projectID + "/"
	contentObjectPrefix := objectPrefix + generation.Prefix + fmt.Sprint(payload["generation"]) + "/"
	readsMu.Lock()
	idMapReads, conceptsReads := reads[contentObjectPrefix+wikiindex.IDMapPath], reads[contentObjectPrefix+wikiindex.ConceptsJSONLPath]
	readCopy := make(map[string]int, len(reads))
	for name, count := range reads {
		readCopy[name] = count
	}
	readsMu.Unlock()
	if idMapReads == 0 || conceptsReads == 0 {
		t.Fatalf("metadata-heavy SDK storage reads: id_map=%d concepts=%d objects=%v", idMapReads, conceptsReads, readCopy)
	}
	sampleMu.Lock()
	peak := peakHeap
	sampleMu.Unlock()
	reservedIDMap := mcpMetadataWorkingSetReservation(wikiindex.IDMapPath, int64(len(idMapBytes)))
	reservedConcepts := mcpMetadataWorkingSetReservation(wikiindex.ConceptsJSONLPath, int64(len(conceptsBytes)))
	t.Logf("metadata_heavy_sdk generation=%s concepts=%d sources=%d id_map_bytes=%d concepts_bytes=%d id_map_reserved_bytes=%d concepts_reserved_bytes=%d total_alloc_delta=%d live_heap_delta=%d sampled_heap_peak_delta=%d rss_peak_before=%d rss_peak_after=%d storage_id_map_reads=%d storage_concepts_reads=%d", payload["generation"], 2000, 2000, len(idMapBytes), len(conceptsBytes), reservedIDMap, reservedConcepts, after.TotalAlloc-before.TotalAlloc, int64(after.HeapAlloc)-int64(before.HeapAlloc), int64(peak)-int64(before.HeapAlloc), rssBefore, rssAfter, idMapReads, conceptsReads)
}

func mcpMetadataHeavyPublishedFixture(t *testing.T, userID, projectID string, count int) (map[string]taggingGCSObject, []byte, []byte) {
	t.Helper()
	const contentGeneration = "g_mcp_metadata_heavy"
	ids := wikiindex.IDMap{
		Concept: map[string]string{}, Source: map[string]string{}, SourceMeta: map[string]wikiindex.SourceMeta{},
		Redirects: map[string][]string{}, IDRedirects: map[string]string{},
	}
	var concepts bytes.Buffer
	files := map[string][]byte{}
	for i := 0; i < count; i++ {
		conceptID := fmt.Sprintf("concept-%05d", i)
		sourceID := fmt.Sprintf("source-%05d", i)
		slug := fmt.Sprintf("concept-%05d", i)
		sourceSlug := fmt.Sprintf("source-%05d", i)
		ids.Concept[conceptID] = slug
		ids.Source[sourceID] = sourceSlug
		ids.SourceMeta[sourceID] = wikiindex.SourceMeta{Slug: sourceSlug, Title: "Source " + sourceID}
		ids.Redirects["old-"+conceptID] = []string{conceptID}
		ids.IDRedirects["legacy-"+conceptID] = conceptID
		fmt.Fprintf(&concepts, "{\"slug\":%q,\"title\":%q,\"frontmatter\":{\"id\":%q,\"sources\":[%q]}}\n", slug, "Concept "+conceptID, conceptID, sourceID)
	}
	idMapBytes, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	conceptsBytes := concepts.Bytes()
	files[wikiindex.IDMapPath] = idMapBytes
	files[wikiindex.ConceptsJSONLPath] = conceptsBytes
	manifest := generation.Manifest{Version: generation.Version, GenerationID: contentGeneration, CreatedAt: "2026-11-05T00:00:00Z", InputFingerprint: "metadata-heavy-sdk-test", Files: make([]generation.File, 0, len(files))}
	objects := map[string]taggingGCSObject{}
	prefix := "users/" + userID + "/projects/" + projectID + "/"
	paths := []string{wikiindex.ConceptsJSONLPath, wikiindex.IDMapPath}
	for i, path := range paths {
		file, err := generation.NewFile(path, files[path], int64(100+i))
		if err != nil {
			t.Fatalf("create metadata-heavy manifest entry: %v", err)
		}
		manifest.Files = append(manifest.Files, file)
		objects[prefix+manifest.ObjectPath(file)] = taggingGCSObject{data: files[path], gen: file.Generation}
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate metadata-heavy generation manifest: %v", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath, err := generation.ArchivedManifestPath(contentGeneration)
	if err != nil {
		t.Fatal(err)
	}
	objects[prefix+archivePath] = taggingGCSObject{data: manifestBytes, gen: 201}
	objects[prefix+generation.ManifestPath] = taggingGCSObject{data: manifestBytes, gen: 202}
	return objects, idMapBytes, conceptsBytes
}

func mcpPublishedContentFixture(t *testing.T, userID, projectID string) map[string]taggingGCSObject {
	t.Helper()
	const contentGeneration = "g_mcp_content_fixture"
	conceptOne := []byte("---\nid: concept-one\ntitle: Alpha\nsources:\n  - source-one\n---\n# Alpha\n\nSee [[concept-two]] for comparison.\n")
	conceptTwo := []byte("---\nid: concept-two\ntitle: Alpha\n---\n# Alpha\n")
	largeBytes := bytes.Repeat([]byte("x"), int(generation.MaxFileBytes))
	sourcePage := []byte("---\nid: source-one\ntitle: Source One\n---\n# Source One\n")
	rawBytes := []byte("Retained source text from the publisher.\n")
	rawBytesTwo := []byte("Retained mismatch control text.\n")
	idMap, err := json.Marshal(wikiindex.IDMap{
		Concept:    map[string]string{"concept-one": "alpha", "concept-two": "beta", "big-one": "big-one", "big-two": "big-two"},
		Source:     map[string]string{"source-one": "source-one", "source-two": "source-two"},
		SourceMeta: map[string]wikiindex.SourceMeta{"source-one": {Slug: "source-one", Title: "Source One"}, "source-two": {Slug: "source-two", Title: "Source Two", SourceFile: "raw/declared-other.md"}},
		Redirects:  map[string][]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	concepts := []byte("{\"slug\":\"alpha\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"concept-one\",\"sources\":[\"source-one\"]}}\n" +
		"{\"slug\":\"beta\",\"title\":\"Alpha\",\"frontmatter\":{\"id\":\"concept-two\"}}\n" +
		"{\"slug\":\"big-one\",\"title\":\"Big One\",\"frontmatter\":{\"id\":\"big-one\"}}\n" +
		"{\"slug\":\"big-two\",\"title\":\"Big Two\",\"frontmatter\":{\"id\":\"big-two\"}}\n")
	fileData := map[string][]byte{
		"cache/concepts.jsonl":       concepts,
		"cache/id_map.json":          idMap,
		"wiki/alpha.md":              conceptOne,
		"wiki/beta.md":               conceptTwo,
		"wiki/big-one.md":            largeBytes,
		"wiki/big-two.md":            largeBytes,
		"wiki/sources/source-one.md": sourcePage,
		"wiki/sources/source-two.md": []byte("---\nid: source-two\ntitle: Source Two\n---\n# Source Two\n"),
	}
	paths := []string{"cache/concepts.jsonl", "cache/id_map.json", "wiki/alpha.md", "wiki/beta.md", "wiki/big-one.md", "wiki/big-two.md", "wiki/sources/source-one.md", "wiki/sources/source-two.md"}
	manifest := generation.Manifest{
		Version: generation.Version, GenerationID: contentGeneration, CreatedAt: "2026-11-05T00:00:00Z", InputFingerprint: "local-mcp-content-fixture",
		Files: make([]generation.File, 0, len(paths)),
	}
	objects := make(map[string]taggingGCSObject, len(paths)+4)
	projectPrefix := "users/" + userID + "/projects/" + projectID + "/"
	for i, relPath := range paths {
		file, err := generation.NewFile(relPath, fileData[relPath], int64(100+i))
		if err != nil {
			t.Fatalf("create published manifest row %q: %v", relPath, err)
		}
		manifest.Files = append(manifest.Files, file)
	}
	sourceSnapshot := generation.SourceSnapshotManifest{
		SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: contentGeneration,
		IDMapDigest: generation.Digest(idMap), SourceStatusDigest: generation.Digest([]byte("publisher-attested-status")),
		Rows: []generation.SourceSnapshotRow{
			{StableID: "source-one", RawPath: "raw/source.md", ContentDigest: generation.Digest(rawBytes), ObjectGeneration: 501},
			{StableID: "source-two", RawPath: "raw/source-two.md", ContentDigest: generation.Digest(rawBytesTwo), ObjectGeneration: 502},
		},
	}
	snapshotBytes, snapshotDigest, err := generation.EncodeSourceSnapshot(sourceSnapshot)
	if err != nil {
		t.Fatalf("encode retained source snapshot: %v", err)
	}
	manifest.SourceSnapshotDigest = snapshotDigest
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		objects[projectPrefix+manifest.ObjectPath(file)] = taggingGCSObject{data: fileData[file.Path], gen: file.Generation}
	}
	archivePath, err := generation.ArchivedManifestPath(contentGeneration)
	if err != nil {
		t.Fatal(err)
	}
	objects[projectPrefix+archivePath] = taggingGCSObject{data: manifestBytes, gen: 201}
	objects[projectPrefix+generation.ManifestPath] = taggingGCSObject{data: manifestBytes, gen: 202}
	snapshotPath, err := generation.SourceSnapshotPath(snapshotDigest)
	if err != nil {
		t.Fatal(err)
	}
	objects[projectPrefix+snapshotPath] = taggingGCSObject{data: snapshotBytes, gen: 301}
	bytesPath, err := generation.SourceBytesPath(generation.Digest(rawBytes))
	if err != nil {
		t.Fatal(err)
	}
	objects[projectPrefix+bytesPath] = taggingGCSObject{data: rawBytes, gen: 501}
	bytesPath, err = generation.SourceBytesPath(generation.Digest(rawBytesTwo))
	if err != nil {
		t.Fatal(err)
	}
	objects[projectPrefix+bytesPath] = taggingGCSObject{data: rawBytesTwo, gen: 502}
	return objects
}

func mcpProcessPeakRSSBytes(t *testing.T) int64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatalf("read process peak RSS: %v", err)
	}
	peak := int64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	return peak
}

func mcpProcessPeakRSSSampleBytes() int64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return -1
	}
	peak := int64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	return peak
}

func callMCPToolWithBoundedAdmissionRetry(t *testing.T, ctx context.Context, session *mcp.ClientSession, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	t.Helper()
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := session.CallTool(ctx, params)
		if err != nil || result == nil || !result.IsError {
			return result, err
		}
		payload, _ := result.StructuredContent.(map[string]any)
		if payload["code"] != "admission_unavailable" || attempt == 3 {
			return result, err
		}
		retryAfter, _ := payload["retry_after_ms"].(float64)
		if retryAfter < 1 || retryAfter > 1000 {
			retryAfter = 1000
		}
		t.Logf("bounded_sdk_admission_retry tool=%s attempt=%d code=admission_unavailable retry_after_ms=%.0f", params.Name, attempt, retryAfter)
		timer := time.NewTimer(time.Duration(retryAfter) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("bounded MCP admission retry loop exhausted")
}

func callMCPToolUntilCapacityRecovers(t *testing.T, ctx context.Context, session *mcp.ClientSession, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	t.Helper()
	for attempt := 1; attempt <= 12; attempt++ {
		result, err := session.CallTool(ctx, params)
		if err != nil || result == nil || !result.IsError {
			return result, err
		}
		payload, _ := result.StructuredContent.(map[string]any)
		code, _ := payload["code"].(string)
		if code != "capacity_limited" && code != "concurrency_limited" && code != "admission_unavailable" || attempt == 12 {
			return result, err
		}
		retryAfter, _ := payload["retry_after_ms"].(float64)
		if retryAfter < 1 || retryAfter > 1000 {
			retryAfter = 250
		}
		t.Logf("bounded_sdk_capacity_recovery_retry tool=%s attempt=%d code=%s retry_after_ms=%.0f", params.Name, attempt, code, retryAfter)
		timer := time.NewTimer(time.Duration(retryAfter) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("bounded MCP capacity recovery retry loop exhausted")
}
