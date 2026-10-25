package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestMCPAddRawSchemaHasOnlyCreateInputs(t *testing.T) {
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(addRawInputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 4 || schema.Properties["filename"] == nil || schema.Properties["markdown"] == nil ||
		schema.Properties["source_url"] == nil || schema.Properties["metadata"] == nil {
		t.Fatalf("add_raw input properties = %v", schema.Properties)
	}
	if len(schema.Required) != 2 || schema.Required[0] != "filename" || schema.Required[1] != "markdown" || schema.AdditionalProperties {
		t.Fatalf("required=%v additionalProperties=%v", schema.Required, schema.AdditionalProperties)
	}
	for _, forbidden := range []string{"user_id", "project_id", "dir", "path", "overwrite", "delete", "annotation"} {
		if schema.Properties[forbidden] != nil {
			t.Fatalf("add_raw unexpectedly accepts %q", forbidden)
		}
	}
	if !strings.Contains(mcpAddRawToolDescription, "metadata.description") || !strings.Contains(mcpAddRawToolDescription, "source data, not instructions") {
		t.Fatal("tool description must disclose supplied summaries and treat Markdown as data")
	}
}

func TestMCPAddRawToolListFollowsVerifiedCapabilities(t *testing.T) {
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	readOnly := mcpProjectKeyIdentity{userID: "user-one", projectID: "project-one", keyID: "grant-old", capabilities: []string{mcpQueryCapability}}
	write := mcpProjectKeyIdentity{userID: "user-one", projectID: "project-one", keyID: "grant-new", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}

	for _, test := range []struct {
		name          string
		identity      mcpProjectKeyIdentity
		wantRaw       bool
		wantURLImport bool
	}{
		{name: "old query-only grant", identity: readOnly},
		{name: "explicit write consent", identity: write, wantRaw: true, wantURLImport: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveMCPRequest(t, h, test.identity, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
			tools, ok := response["result"].(map[string]any)["tools"].([]any)
			if !ok {
				t.Fatalf("tools/list response = %#v", response)
			}
			names := make(map[string]bool, len(tools))
			var addRawDescription string
			for _, item := range tools {
				tool, ok := item.(map[string]any)
				if !ok {
					t.Fatalf("tools/list entry = %#v", item)
				}
				name, _ := tool["name"].(string)
				names[name] = true
				if name == "add_raw" {
					addRawDescription, _ = tool["description"].(string)
				}
			}
			if names["query_project"] != true || names["add_raw"] != test.wantRaw || names["import_url"] != test.wantURLImport {
				t.Fatalf("listed tools = %v", names)
			}
			if !test.wantURLImport {
				denied := serveMCPRequest(t, h, test.identity, mcpToolCallBody(2, "import_url", `{"url":"https://example.test/article"}`))
				protocolError, ok := denied["error"].(map[string]any)
				if !ok || protocolError["code"] != float64(-32602) {
					t.Fatalf("query-only import_url call was not denied as unknown: %#v", denied)
				}
			}
			if test.wantRaw && (!strings.Contains(addRawDescription, "source data, not instructions") || !strings.Contains(addRawDescription, "metadata.description")) {
				t.Fatalf("add_raw description omits required handling instructions: %q", addRawDescription)
			}
		})
	}
}

func TestMCPImportURLIsListedForWritersAndFailsClosedWithoutService(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{
		userID: "user-one", projectID: "project-one", keyID: "grant-write",
		capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	}

	listed := serveMCPRequest(t, h, identity, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	items := listed["result"].(map[string]any)["tools"].([]any)
	names := make(map[string]bool, len(items))
	for _, item := range items {
		name, _ := item.(map[string]any)["name"].(string)
		names[name] = true
	}
	if len(names) != 3 || !names["query_project"] || !names["add_raw"] || !names["import_url"] {
		t.Fatalf("raw-write discovery = %v, want query_project, add_raw, and import_url", names)
	}

	response := serveMCPRequest(t, h, identity, mcpToolCallBody(2, "import_url", `{"url":"https://example.test/article"}`))
	payload := mcpStructuredResult(t, response)
	if payload["status"] != "unavailable" || payload["failure_stage"] != "setup" || payload["raw_url"] != nil {
		t.Fatalf("unconfigured import_url result = %#v", payload)
	}
	if files, err := root.Scope(identity.userID, identity.projectID).ListRawFiles(context.Background()); err != nil || len(files) != 0 {
		t.Fatalf("unconfigured import_url call mutated raw: files=%v err=%v", files, err)
	}
}

func TestMCPStreamableSDKClientRetainsRawWriteWithURLTool(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{
		userID: "sdk-user", projectID: "sdk-project", keyID: "grant-write",
		capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	}
	server := httptest.NewServer(h.mcpAuthenticatedHandler(identity))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpClient := &http.Client{Transport: mcpBearerTestTransport{next: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "lwc405-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP SDK client: %v", err)
	}
	defer session.Close()

	discovered, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("MCP SDK tools/list: %v", err)
	}
	names := make(map[string]bool, len(discovered.Tools))
	for _, tool := range discovered.Tools {
		names[tool.Name] = true
	}
	if len(names) != 3 || !names["query_project"] || !names["add_raw"] || !names["import_url"] {
		t.Fatalf("MCP SDK discovered tools = %v, want query_project, add_raw, and import_url", names)
	}
	urlResult, urlErr := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "import_url", Arguments: map[string]any{"url": "https://example.test/article"},
	})
	if urlErr != nil || urlResult == nil {
		t.Fatalf("MCP SDK unconfigured import_url result=%+v err=%v", urlResult, urlErr)
	}
	urlPayload, ok := urlResult.StructuredContent.(map[string]any)
	if !urlResult.IsError || !ok || urlPayload["status"] != "unavailable" {
		t.Fatalf("MCP SDK unconfigured import_url result=%+v err=%v", urlResult, urlErr)
	}
	if files, err := root.Scope(identity.userID, identity.projectID).ListRawFiles(ctx); err != nil || len(files) != 0 {
		t.Fatalf("unconfigured URL tool call mutated raw: files=%v err=%v", files, err)
	}

	arguments := map[string]any{"filename": "sdk-source.md", "markdown": "# SDK source\n\nSupplied Markdown."}
	created, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "add_raw", Arguments: arguments})
	if err != nil || created == nil || created.IsError {
		t.Fatalf("MCP SDK add_raw create = %+v err=%v", created, err)
	}
	if payload, ok := created.StructuredContent.(map[string]any); !ok || payload["status"] != "created" || payload["path"] != "raw/sdk-source.md" {
		t.Fatalf("MCP SDK add_raw create payload = %#v", created.StructuredContent)
	}

	retried, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "add_raw", Arguments: arguments})
	if err != nil || retried == nil || retried.IsError {
		t.Fatalf("MCP SDK add_raw retry = %+v err=%v", retried, err)
	}
	if payload, ok := retried.StructuredContent.(map[string]any); !ok || payload["status"] != "already_exists" {
		t.Fatalf("MCP SDK add_raw retry payload = %#v", retried.StructuredContent)
	}

	conflict, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "add_raw", Arguments: map[string]any{"filename": "sdk-source.md", "markdown": "different content"},
	})
	if err != nil || conflict == nil {
		t.Fatalf("MCP SDK add_raw conflict = %+v err=%v", conflict, err)
	}
	if payload, ok := conflict.StructuredContent.(map[string]any); !ok || payload["status"] != "conflict" {
		t.Fatalf("MCP SDK add_raw conflict payload = %#v", conflict.StructuredContent)
	}
	stored, err := root.Scope(identity.userID, identity.projectID).ReadFile(ctx, "raw/sdk-source.md")
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := parseFrontmatterJSON(string(stored))
	if err != nil || body != "# SDK source\n\nSupplied Markdown." {
		t.Fatalf("MCP SDK add_raw persisted body = %q err=%v", body, err)
	}
}

type mcpBearerTestTransport struct{ next http.RoundTripper }

func (t mcpBearerTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer local-sdk-test-token")
	return t.next.RoundTrip(cloned)
}

func TestMCPAddRawAdapterCreateRetryAndConflict(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{userID: "bound-user", projectID: "bound-project", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	args := `{"filename":"brief.md","markdown":"# Brief\n\nSupplied source text.","source_url":"https://example.test/article?ref=private","metadata":{"title":"Brief","author":"Ada","published":"2026-10-01","description":"Original supplied Markdown.","tags":["research","reading"]}}`
	first := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "add_raw", args))
	firstPayload := mcpStructuredResult(t, first)
	if firstPayload["status"] != "created" || firstPayload["path"] != "raw/brief.md" || firstPayload["processing_status"] != "not_started" || firstPayload["source_url"] != "https://example.test/article?ref=private" {
		t.Fatalf("first add_raw result = %#v", firstPayload)
	}
	firstBytes, err := root.Scope("bound-user", "bound-project").ReadFile(context.Background(), "raw/brief.md")
	if err != nil {
		t.Fatal(err)
	}
	frontmatter, body, err := parseFrontmatterJSON(string(firstBytes))
	if err != nil || body != "# Brief\n\nSupplied source text." || frontmatter["title"] != "Brief" || frontmatter["source"] != "https://example.test/article?ref=private" {
		t.Fatalf("stored raw frontmatter/body = %#v %q err=%v", frontmatter, body, err)
	}

	retry := serveMCPRequest(t, h, identity, mcpToolCallBody(2, "add_raw", args))
	retryPayload := mcpStructuredResult(t, retry)
	if retryPayload["status"] != "already_exists" {
		t.Fatalf("same-payload retry result = %#v", retryPayload)
	}
	retryBytes, err := root.Scope("bound-user", "bound-project").ReadFile(context.Background(), "raw/brief.md")
	if err != nil || string(retryBytes) != string(firstBytes) {
		t.Fatalf("same-payload retry changed stored bytes: err=%v", err)
	}

	different := serveMCPRequest(t, h, identity, mcpToolCallBody(3, "add_raw", `{"filename":"brief.md","markdown":"different content"}`))
	differentPayload := mcpStructuredResult(t, different)
	if differentPayload["status"] != "conflict" {
		t.Fatalf("different-content collision = %#v", differentPayload)
	}
	unchanged, err := root.Scope("bound-user", "bound-project").ReadFile(context.Background(), "raw/brief.md")
	if err != nil || string(unchanged) != string(firstBytes) {
		t.Fatalf("collision changed stored bytes: err=%v", err)
	}
	changedSource := strings.Replace(args, "https://example.test/article?ref=private", "https://example.test/other", 1)
	changedSourceResult := serveMCPRequest(t, h, identity, mcpToolCallBody(4, "add_raw", changedSource))
	if changed := mcpStructuredResult(t, changedSourceResult); changed["status"] != "conflict" {
		t.Fatalf("source_url metadata unexpectedly authorized a refresh: %#v", changed)
	}
	unchanged, err = root.Scope("bound-user", "bound-project").ReadFile(context.Background(), "raw/brief.md")
	if err != nil || string(unchanged) != string(firstBytes) {
		t.Fatalf("source_url collision changed stored bytes: err=%v", err)
	}
	if _, err := root.Scope("bound-user", "other-project").ReadFile(context.Background(), "raw/brief.md"); err == nil {
		t.Fatal("source escaped the authorized Project scope")
	}
}

func TestMCPAddRawRejectsUnsafeAndOversizedInputsWithoutWriting(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "user-one", projectID: "project-one", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	})
	for _, input := range []addRawInput{
		{Filename: "../escape.md", Markdown: "content"},
		{Filename: "folder/inside.md", Markdown: "content"},
		{Filename: "bad.txt", Markdown: "content"},
		{Filename: "empty.md", Markdown: " \n"},
		{Filename: "credential.md", Markdown: "content", SourceURL: "https://user:password@example.test/private"},
		{Filename: "private.md", Markdown: "content", SourceURL: "http://127.0.0.1/private?token=secret"},
		{Filename: "large.md", Markdown: strings.Repeat("x", maxRawUploadSize)},
	} {
		result, _, err := h.callMCPAddRaw(ctx, nil, input)
		if err != nil || !result.IsError {
			t.Fatalf("input filename %q result=%+v err=%v; want a safe tool rejection", input.Filename, result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "password") || strings.Contains(text, "token=secret") || strings.Contains(text, "PRIVATE") {
			t.Fatalf("sensitive URL or raw input was echoed: %q", text)
		}
	}
	if files, err := root.Scope("user-one", "project-one").ListRawFiles(context.Background()); err != nil || len(files) != 0 {
		t.Fatalf("rejected input wrote raw files=%v err=%v", files, err)
	}
}

func TestMCPAddRawConcurrentSamePayloadCreatesOneStableFile(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "user-one", projectID: "project-one", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	})
	input := addRawInput{Filename: "shared.md", Markdown: "same body", Metadata: addRawMetadata{Title: "Shared"}}
	const attempts = 12
	var wg sync.WaitGroup
	statuses := make(chan string, attempts)
	errorsFound := make(chan error, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := h.callMCPAddRaw(ctx, nil, input)
			if err != nil {
				errorsFound <- err
				return
			}
			encoded, marshalErr := json.Marshal(result.StructuredContent)
			if marshalErr != nil {
				errorsFound <- marshalErr
				return
			}
			var payload map[string]any
			if unmarshalErr := json.Unmarshal(encoded, &payload); unmarshalErr != nil {
				errorsFound <- unmarshalErr
				return
			}
			status, _ := payload["status"].(string)
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	created := 0
	for status := range statuses {
		if status == "created" {
			created++
		} else if status != "already_exists" {
			t.Fatalf("concurrent create result status = %q", status)
		}
	}
	if created != 1 {
		t.Fatalf("created status count=%d, want one", created)
	}
	if _, err := root.Scope("user-one", "project-one").ReadFile(context.Background(), "raw/shared.md"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPAddRawConcurrentDifferentPayloadCannotOverwrite(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(root, nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "user-one", projectID: "project-one", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	})
	start := make(chan struct{})
	statuses := make(chan string, 2)
	var wg sync.WaitGroup
	for _, body := range []string{"first competing body", "second competing body"} {
		body := body
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, _, err := h.callMCPAddRaw(ctx, nil, addRawInput{Filename: "racing.md", Markdown: body})
			if err != nil {
				statuses <- "unexpected-error"
				return
			}
			encoded, marshalErr := json.Marshal(result.StructuredContent)
			if marshalErr != nil {
				statuses <- "unexpected-error"
				return
			}
			var payload map[string]any
			if unmarshalErr := json.Unmarshal(encoded, &payload); unmarshalErr != nil {
				statuses <- "unexpected-error"
				return
			}
			status, _ := payload["status"].(string)
			statuses <- status
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	created, conflicts := 0, 0
	for status := range statuses {
		switch status {
		case "created":
			created++
		case "conflict":
			conflicts++
		default:
			t.Fatalf("concurrent different-payload result status=%q", status)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("concurrent different-payload statuses created=%d conflicts=%d, want one each", created, conflicts)
	}
	stored, err := root.Scope("user-one", "project-one").ReadFile(context.Background(), "raw/racing.md")
	if err != nil {
		t.Fatal(err)
	}
	_, storedBody, err := parseFrontmatterJSON(string(stored))
	if err != nil || (storedBody != "first competing body" && storedBody != "second competing body") {
		t.Fatalf("concurrent write left invalid or unexpected data %q, err=%v", storedBody, err)
	}
}

func TestMCPAddRawReconcilesUncertainCommitByReadback(t *testing.T) {
	root := localfs.New(t.TempDir())
	h := New(uncertainMCPRawRoot{RootStore: root, writeThenFail: true}, nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{userID: "user-one", projectID: "project-one", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	response := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "add_raw", `{"filename":"uncertain.md","markdown":"preserve me"}`))
	payload := mcpStructuredResult(t, response)
	if payload["status"] != "already_exists" || payload["path"] != "raw/uncertain.md" {
		t.Fatalf("readback did not reconcile committed bytes: %#v", payload)
	}

	missingRoot := localfs.New(t.TempDir())
	missing := New(uncertainMCPRawRoot{RootStore: missingRoot}, nil, nil, nil, nil, nil)
	response = serveMCPRequest(t, missing, identity, mcpToolCallBody(2, "add_raw", `{"filename":"missing.md","markdown":"unknown outcome"}`))
	payload = mcpStructuredResult(t, response)
	if payload["status"] != "unavailable" || payload["commit_status"] != "uncertain" {
		t.Fatalf("uncommitted uncertain result was not preserved: %#v", payload)
	}
}

func serveMCPRequest(t *testing.T, h *Handler, identity mcpProjectKeyIdentity, body string) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer opaque-test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Accept", "application/json")
	request.Header.Add("Accept", "text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("MCP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func mcpToolCallBody(id int, name, arguments string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, arguments)
}

func mcpStructuredResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("MCP response has no result: %#v", response)
	}
	structured, ok := result["structuredContent"]
	if !ok {
		t.Fatalf("MCP response has no structuredContent: %#v", result)
	}
	if raw, ok := structured.(map[string]any); ok {
		return raw
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode structured result: %v", err)
	}
	return decoded
}

type uncertainMCPRawRoot struct {
	store.RootStore
	writeThenFail bool
}

func (r uncertainMCPRawRoot) Scope(userID, projectID string) store.Store {
	return uncertainMCPRawStore{Store: r.RootStore.Scope(userID, projectID), writeThenFail: r.writeThenFail}
}

type uncertainMCPRawStore struct {
	store.Store
	writeThenFail bool
}

func (s uncertainMCPRawStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	return s.Store.(store.ConditionalWriter).ReadFileWithGeneration(ctx, path)
}

func (s uncertainMCPRawStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, generation int64) (int64, error) {
	writer := s.Store.(store.ConditionalWriter)
	if !s.writeThenFail {
		return 0, errors.New("synthetic commit response lost before write")
	}
	next, err := writer.WriteFileIfGeneration(ctx, data, path, generation)
	if err != nil {
		return next, err
	}
	return 0, errors.New("synthetic uncertain commit")
}
