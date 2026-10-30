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

	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
)

func TestMCPMetadataUsesCaCForModernAndLegacyWireMethods(t *testing.T) {
	metadata := &config.BFFMCP{
		Instructions: "After a confirmed successful import_url, return the exact raw_url from its tool result. After add_raw, report its returned status and saved path; do not invent a URL.",
		ToolDescriptions: config.BFFMCPToolDescriptions{
			QueryProject: "configured query description",
			AddRaw:       "configured add description",
			ImportURL:    "configured import description",
		},
		DiscoveryTTLMS: 12_345, ToolsListTTLMS: 23_456,
	}
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetMCPConfig(metadata)
	queryOnly := mcpProjectKeyIdentity{userID: "synthetic-user-a", projectID: "synthetic-project-a", keyID: "synthetic-query-key", capabilities: []string{mcpQueryCapability}}
	writer := mcpProjectKeyIdentity{userID: "synthetic-user-b", projectID: "synthetic-project-b", keyID: "synthetic-writer-key", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}

	modernDiscover := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(queryOnly),
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	modernResult := mcpMetadataResult(t, modernDiscover)
	if modernResult["instructions"] != metadata.Instructions || modernResult["ttlMs"] != float64(metadata.DiscoveryTTLMS) || modernResult["cacheScope"] != "private" {
		t.Fatalf("modern discover metadata = %#v", modernResult)
	}
	if got := modernResult["instructions"].(string); !strings.Contains(got, "import_url") ||
		!strings.Contains(got, "add_raw") || strings.Contains(got, "After a successful save, return the exact raw_url") {
		t.Fatalf("delivered instructions conflate import_url raw_url with add_raw path: %q", got)
	}
	modernCapabilities := modernResult["capabilities"].(map[string]any)
	if modernCapabilities["tools"].(map[string]any)["listChanged"] == true || modernCapabilities["logging"] == nil {
		t.Fatalf("server capabilities do not preserve logging and disable undelivered listChanged: %#v", modernCapabilities)
	}
	t.Logf("wire modern server/discover: %s", strings.TrimSpace(modernDiscover.Body.String()))

	modernList := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(queryOnly),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	modernListResult := mcpMetadataResult(t, modernList)
	if modernListResult["ttlMs"] != float64(metadata.ToolsListTTLMS) || modernListResult["cacheScope"] != "private" {
		t.Fatalf("modern tools/list cache hints = %#v", modernListResult)
	}
	querySchema := assertMCPMetadataTools(t, modernListResult, metadata, false)
	t.Logf("wire modern query-only tools/list: %s", strings.TrimSpace(modernList.Body.String()))

	writerList := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(writer),
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	writerListResult := mcpMetadataResult(t, writerList)
	writerSchema := assertMCPMetadataTools(t, writerListResult, metadata, true)
	if string(querySchema) != string(writerSchema) {
		t.Fatalf("tool input schema changed with authorization: query=%s writer=%s", querySchema, writerSchema)
	}
	t.Logf("wire modern writer tools/list: %s", strings.TrimSpace(writerList.Body.String()))

	legacyInitialize := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(queryOnly),
		`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture-client","version":"1"}}}`, "2025-11-25")
	legacyResult := mcpMetadataResult(t, legacyInitialize)
	legacyCapabilities := legacyResult["capabilities"].(map[string]any)
	if legacyResult["instructions"] != metadata.Instructions || legacyCapabilities["tools"].(map[string]any)["listChanged"] == true || legacyCapabilities["logging"] == nil {
		t.Fatalf("legacy initialize metadata/capabilities = %#v", legacyResult)
	}
	t.Logf("wire legacy initialize: %s", strings.TrimSpace(legacyInitialize.Body.String()))

	legacyList := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(writer),
		`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`, "2025-11-25")
	legacyListResult := mcpMetadataResult(t, legacyList)
	if legacyListResult["ttlMs"] != float64(metadata.ToolsListTTLMS) || legacyListResult["cacheScope"] != "private" {
		t.Fatalf("legacy tools/list cache hints = %#v", legacyListResult)
	}
	assertMCPMetadataTools(t, legacyListResult, metadata, true)
	t.Logf("wire legacy writer tools/list: %s", strings.TrimSpace(legacyList.Body.String()))

	badVersion := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(queryOnly),
		`{"jsonrpc":"2.0","id":6,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2025-11-25")
	if !mcpMetadataHasError(badVersion) {
		t.Fatalf("discover accepted the legacy protocol version: status=%d body=%s", badVersion.Code, badVersion.Body.String())
	}
	malformed := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(queryOnly), "{", "")
	if !mcpMetadataHasError(malformed) {
		t.Fatalf("malformed MCP request was not rejected: status=%d body=%s", malformed.Code, malformed.Body.String())
	}
}

func TestMCPMetadataPrivateCacheHintsWithControlledClockAndAuthSeparation(t *testing.T) {
	metadata := &config.BFFMCP{
		Instructions:     "cache-test instructions",
		ToolDescriptions: config.BFFMCPToolDescriptions{QueryProject: "query", AddRaw: "add", ImportURL: "import"},
		DiscoveryTTLMS:   1_000, ToolsListTTLMS: 1_000,
	}
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetMCPConfig(metadata)
	identities := map[string]mcpProjectKeyIdentity{
		"query-a":  {userID: "synthetic-user", projectID: "synthetic-project-a", keyID: "synthetic-query-a", capabilities: []string{mcpQueryCapability}},
		"query-b":  {userID: "synthetic-user", projectID: "synthetic-project-b", keyID: "synthetic-query-b", capabilities: []string{mcpQueryCapability}},
		"query-a2": {userID: "synthetic-user", projectID: "synthetic-project-a", keyID: "synthetic-query-a2", capabilities: []string{mcpQueryCapability}},
		"writer-a": {userID: "synthetic-user", projectID: "synthetic-project-a", keyID: "synthetic-writer-a", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}},
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		token, ok := mcpBearerToken(r.Header.Get("Authorization"))
		identity, known := identities[token]
		if !ok || !known || r.Header.Get("X-Project-ID") != identity.projectID {
			http.Error(w, "synthetic auth denied", http.StatusUnauthorized)
			return
		}
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, w, r)
	}))
	defer server.Close()

	clock := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	client := newMCPMetadataTestClient(server.Client(), func() time.Time { return clock })
	ctx := context.Background()
	params := `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"cache-fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`
	call := func(method, token, project string) map[string]any {
		t.Helper()
		result, err := client.call(ctx, server.URL, token, project, method, params)
		if err != nil {
			t.Fatalf("cached %s request: %v", method, err)
		}
		return result
	}

	if got := call("server/discover", "query-a", "synthetic-project-a"); got["cacheScope"] != "private" || got["ttlMs"] != float64(1_000) {
		t.Fatalf("discover cache hints = %#v", got)
	}
	call("server/discover", "query-a", "synthetic-project-a")
	clock = clock.Add(999 * time.Millisecond)
	call("server/discover", "query-a", "synthetic-project-a")
	if requests != 1 {
		t.Fatalf("fresh private cache made %d requests, want 1", requests)
	}
	clock = clock.Add(time.Millisecond)
	call("server/discover", "query-a", "synthetic-project-a")
	if requests != 2 {
		t.Fatalf("expired metadata cache made %d requests, want 2", requests)
	}

	queryList := call("tools/list", "query-a", "synthetic-project-a")
	assertMCPMetadataTools(t, queryList, metadata, false)
	call("tools/list", "query-a", "synthetic-project-a")
	if requests != 3 {
		t.Fatalf("fresh tools/list cache made %d requests, want 3", requests)
	}
	assertMCPMetadataTools(t, call("tools/list", "writer-a", "synthetic-project-a"), metadata, true)
	if requests != 4 {
		t.Fatalf("writer authorization context reused query-only metadata: requests=%d", requests)
	}
	assertMCPMetadataTools(t, call("tools/list", "query-a2", "synthetic-project-a"), metadata, false)
	if requests != 5 {
		t.Fatalf("different token reused a private metadata entry: requests=%d", requests)
	}
	assertMCPMetadataTools(t, call("tools/list", "query-b", "synthetic-project-b"), metadata, false)
	if requests != 6 {
		t.Fatalf("different Project reused a private metadata entry: requests=%d", requests)
	}
}

func TestMCPMetadataLegacySchema2BehaviorStaysPrivate(t *testing.T) {
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetMCPConfig(nil)
	identity := mcpProjectKeyIdentity{userID: "synthetic-user", projectID: "synthetic-project", keyID: "synthetic-key", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	discover := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":7,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	discoverResult := mcpMetadataResult(t, discover)
	if discoverResult["instructions"] != nil || discoverResult["ttlMs"] != float64(0) || discoverResult["cacheScope"] != "private" {
		t.Fatalf("legacy schema2 discover behavior = %#v", discoverResult)
	}
	initialize := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":9,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture-client","version":"1"}}}`, "2025-11-25")
	if result := mcpMetadataResult(t, initialize); result["instructions"] != nil {
		t.Fatalf("legacy schema2 initialize unexpectedly included instructions: %#v", result)
	}
	list := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{}}`, "2025-11-25")
	listResult := mcpMetadataResult(t, list)
	if listResult["ttlMs"] != float64(0) || listResult["cacheScope"] != "private" {
		t.Fatalf("legacy schema2 tools/list cache hints = %#v", listResult)
	}
	items := listResult["tools"].([]any)
	for _, item := range items {
		tool := item.(map[string]any)
		want := map[string]string{
			"query_project":       mcpQueryProjectToolDescription,
			"get_document_import": mcpGetDocumentImportDescription,
			"add_raw":             mcpAddRawToolDescription,
			"import_url":          mcpImportURLToolDescription,
			"import_document":     mcpImportDocumentDescription,
		}[tool["name"].(string)]
		if tool["description"] != want {
			t.Fatalf("legacy schema2 description for %q = %v", tool["name"], tool["description"])
		}
	}
}

type mcpMetadataTestCacheEntry struct {
	result  map[string]any
	expires time.Time
}

type mcpMetadataTestClient struct {
	httpClient *http.Client
	now        func() time.Time
	cache      map[string]mcpMetadataTestCacheEntry
	nextID     int
}

func newMCPMetadataTestClient(httpClient *http.Client, now func() time.Time) *mcpMetadataTestClient {
	return &mcpMetadataTestClient{httpClient: httpClient, now: now, cache: make(map[string]mcpMetadataTestCacheEntry)}
}

func (c *mcpMetadataTestClient) call(ctx context.Context, endpoint, token, project, method, params string) (map[string]any, error) {
	var paramsValue any
	if err := json.Unmarshal([]byte(params), &paramsValue); err != nil {
		return nil, err
	}
	canonicalParams, err := json.Marshal(paramsValue)
	if err != nil {
		return nil, err
	}
	key := strings.Join([]string{endpoint, method, project, token, string(canonicalParams)}, "\x00")
	if entry, ok := c.cache[key]; ok && c.now().Before(entry.expires) {
		return entry.result, nil
	}
	c.nextID++
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": paramsValue})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Project-ID", project)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("MCP-Method", method)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var wire map[string]any
	if err := json.NewDecoder(response.Body).Decode(&wire); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata HTTP status %d", response.StatusCode)
	}
	result, ok := wire["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata response has no result")
	}
	ttl, _ := result["ttlMs"].(float64)
	if result["cacheScope"] == "private" && ttl > 0 {
		c.cache[key] = mcpMetadataTestCacheEntry{result: result, expires: c.now().Add(time.Duration(ttl) * time.Millisecond)}
	}
	return result, nil
}

func mcpMetadataWireRequest(t *testing.T, handler http.Handler, body, protocolVersion string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer synthetic-test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if protocolVersion != "" {
		request.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	if protocolVersion >= "2026-07-28" {
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err == nil && envelope.Method != "" {
			request.Header.Set("MCP-Method", envelope.Method)
		}
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func mcpMetadataResult(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("MCP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var wire map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode MCP wire response %q: %v", recorder.Body.String(), err)
	}
	result, ok := wire["result"].(map[string]any)
	if !ok {
		t.Fatalf("MCP response has no result: %#v", wire)
	}
	return result
}

func mcpMetadataHasError(recorder *httptest.ResponseRecorder) bool {
	if recorder.Code >= http.StatusBadRequest {
		return true
	}
	var wire map[string]any
	return json.Unmarshal(recorder.Body.Bytes(), &wire) == nil && wire["error"] != nil
}

func assertMCPMetadataTools(t *testing.T, result map[string]any, metadata *config.BFFMCP, writer bool) []byte {
	t.Helper()
	if result["ttlMs"] != float64(metadata.ToolsListTTLMS) || result["cacheScope"] != "private" {
		t.Fatalf("tools/list cache hints = %#v", result)
	}
	items, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", result)
	}
	wantCount := 2
	if writer {
		wantCount = 5
	}
	if len(items) != wantCount {
		t.Fatalf("tools/list returned %d tools, want %d", len(items), wantCount)
	}
	var querySchema []byte
	names := make(map[string]bool, len(items))
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tools/list entry = %#v", item)
		}
		name, _ := tool["name"].(string)
		names[name] = true
		wantDescription := map[string]string{
			"query_project":       metadata.ToolDescriptions.QueryProject,
			"get_document_import": mcpGetDocumentImportDescription,
			"add_raw":             metadata.ToolDescriptions.AddRaw,
			"import_url":          metadata.ToolDescriptions.ImportURL,
			"import_document":     mcpImportDocumentDescription,
		}[name]
		if wantDescription == "" || tool["description"] != wantDescription {
			t.Fatalf("tool %q description = %v, want configured text", name, tool["description"])
		}
		if name == "query_project" {
			querySchema, _ = json.Marshal(tool["inputSchema"])
		}
	}
	if !names["query_project"] || !names["get_document_import"] ||
		(writer && (!names["add_raw"] || !names["import_url"] || !names["import_document"])) ||
		(!writer && (names["add_raw"] || names["import_url"] || names["import_document"])) || len(querySchema) == 0 {
		t.Fatalf("tools/list names or code-owned schema = %v schema=%s", names, querySchema)
	}
	return querySchema
}
