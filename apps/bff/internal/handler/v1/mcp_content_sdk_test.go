package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type mcpContentAdmissionObservation struct {
	active        bool
	projectTokens int64
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
	bucket := config.MCPProtectionBucket{Capacity: 8, RefillMicrounitsPerSecond: 1, MaxInflight: 8}
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

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	var releaseOnce sync.Once
	releaseBigRead := func() { releaseOnce.Do(func() { close(releaseRead) }) }
	contentClient, _, _ := newTaggingGCSClient(t, mcpPublishedContentFixture(t, userID, projectID), func(name string) {
		if holdBigRead.Load() && strings.HasSuffix(name, "/wiki/big-one.md") {
			select {
			case readStarted <- struct{}{}:
			default:
			}
			<-releaseRead
		}
	})
	t.Cleanup(releaseBigRead)
	probe := &mcpContentAdmissionProbeRoot{RootStore: localfs.New(t.TempDir()), fs: fs.Raw(), content: contentClient}
	handler := New(probe, fs, nil, nil, nil, nil)
	handler.SetMCPConfig(mcpContentEndpointTestConfig())
	handler.mcpEnvironment = "local"
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

	router := gin.New()
	router.Any("/mcp", gin.WrapH(handler.MCPHandler()))
	router.Any("/mcp/projects/:projectID", handler.MCPProjectOAuthHandler("local", resourceOrigin))
	server := httptest.NewServer(router)
	defer server.Close()

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
	if observations := probe.observations(); len(observations) != 1 || !observations[0].active || observations[0].projectTokens != 7_000_000 {
		t.Fatalf("Project-key storage scope was not reached under its active admission lease: %+v", observations)
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

	rawResult, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_source_raw", Arguments: map[string]any{"source_id": "source-one"}})
	if err != nil || rawResult == nil || rawResult.IsError {
		t.Fatalf("read key read_source_raw response=%+v err=%v", rawResult, err)
	}
	rawPayload, ok := rawResult.StructuredContent.(map[string]any)
	rawBody, _ := rawPayload["content"].(map[string]any)
	if !ok || rawPayload["representation"] != "retained_source_text" || rawBody["text"] != "Retained source text from the publisher.\n" {
		t.Fatalf("retained raw Source response=%#v", rawResult.StructuredContent)
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
	if observations := probe.observations(); len(observations) != 1 || !observations[0].active || observations[0].projectTokens >= 1_000_000 {
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
	limited, err := readSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_content", Arguments: map[string]any{"kind": "concept"}})
	if err != nil || limited == nil || !limited.IsError {
		t.Fatalf("exhausted content quota response=%+v err=%v", limited, err)
	}
	limitedPayload, ok := limited.StructuredContent.(map[string]any)
	if !ok || limitedPayload["code"] != "rate_limited" {
		t.Fatalf("exhausted content quota response=%#v", limited.StructuredContent)
	}
	if observations := probe.observations(); len(observations) != 0 {
		t.Fatalf("quota-rejected cold content request reached storage scope: %+v", observations)
	}

}

func mcpPublishedContentFixture(t *testing.T, userID, projectID string) map[string]taggingGCSObject {
	t.Helper()
	const contentGeneration = "g_mcp_content_fixture"
	conceptOne := []byte("---\nid: concept-one\ntitle: Alpha\nsources:\n  - source-one\n---\n# Alpha\n\nSee [[concept-two]] for comparison.\n")
	conceptTwo := []byte("---\nid: concept-two\ntitle: Alpha\n---\n# Alpha\n")
	largeBytes := bytes.Repeat([]byte("x"), int(generation.MaxFileBytes))
	sourcePage := []byte("---\nid: source-one\ntitle: Source One\n---\n# Source One\n")
	rawBytes := []byte("Retained source text from the publisher.\n")
	idMap, err := json.Marshal(wikiindex.IDMap{
		Concept:    map[string]string{"concept-one": "alpha", "concept-two": "beta", "big-one": "big-one", "big-two": "big-two"},
		Source:     map[string]string{"source-one": "source-one"},
		SourceMeta: map[string]wikiindex.SourceMeta{"source-one": {Slug: "source-one", Title: "Source One", SourceFile: "raw/source.md"}},
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
	}
	paths := []string{"cache/concepts.jsonl", "cache/id_map.json", "wiki/alpha.md", "wiki/beta.md", "wiki/big-one.md", "wiki/big-two.md", "wiki/sources/source-one.md"}
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
		Rows: []generation.SourceSnapshotRow{{StableID: "source-one", RawPath: "raw/source.md", ContentDigest: generation.Digest(rawBytes), ObjectGeneration: 501}},
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
