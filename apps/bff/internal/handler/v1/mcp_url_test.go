package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

type mcpURLFetcherFunc func(context.Context, string) (urlintake.Page, error)

func (f mcpURLFetcherFunc) Fetch(ctx context.Context, sourceURL string) (urlintake.Page, error) {
	return f(ctx, sourceURL)
}

type mcpURLEvaluatorFunc func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error)

func (f mcpURLEvaluatorFunc) Evaluate(ctx context.Context, input urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
	return f(ctx, input)
}

type mcpURLResolver struct{}

type mcpURLProjectLLMClient struct{ calls atomic.Int32 }

func (c *mcpURLProjectLLMClient) Chat(_ context.Context, system, input string) (string, error) {
	if !strings.Contains(system, "Assess URL extraction usability") || !strings.Contains(input, "Readable guide") {
		return "", errors.New("unexpected Jev Project request")
	}
	c.calls.Add(1)
	return `{"access_shell":false,"readable_content":true}`, nil
}

func (*mcpURLProjectLLMClient) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: "custom", Model: "project-fixture-model"}, true
}

type mcpURLCommitUncertainStore struct {
	urlintake.ProjectStore
	written atomic.Bool
}

func (s *mcpURLCommitUncertainStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, expected int64) (int64, error) {
	generation, err := s.ProjectStore.WriteFileIfGeneration(ctx, data, path, expected)
	if err != nil || !strings.HasPrefix(path, "raw/") {
		return generation, err
	}
	s.written.Store(true)
	return generation, errors.New("storage response was lost after the raw write")
}

func (s *mcpURLCommitUncertainStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	if strings.HasPrefix(path, "raw/") && s.written.Load() {
		return nil, 0, errors.New("readback unavailable after the raw write")
	}
	return s.ProjectStore.ReadFileWithGeneration(ctx, path)
}

func (mcpURLResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func TestMCPImportURLUsesStrictScopedIntakeAndConfiguredRawLink(t *testing.T) {
	root := localfs.New(t.TempDir())
	var fetches atomic.Int32
	var evaluations atomic.Int32
	service := urlintake.NewService(
		mcpURLFetcherFunc(func(context.Context, string) (urlintake.Page, error) {
			fetches.Add(1)
			return urlintake.Page{Title: "Readable guide", Markdown: "# Readable guide\n\nA short, useful reference page.\n"}, nil
		}),
		mcpURLEvaluatorFunc(func(_ context.Context, input urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			evaluations.Add(1)
			if !input.RequireTitleBodyConsistency || input.Title != "Readable guide" || !strings.Contains(input.Markdown, "short, useful reference") {
				t.Fatalf("strict quality input = %#v", input)
			}
			return urlintake.QualityEvaluation{ShellProbability: .02, ContentProbability: .98}, nil
		}),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			return root.WithScope(scope.ActorID, scope.ProjectID), nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = mcpURLResolver{}
	h := New(root, nil, nil, nil, nil, nil)
	h.SetURLIntakeService(service)
	h.SetMCPFrontendOrigin("https://wiki.trusted.example")
	identity := mcpProjectKeyIdentity{
		userID: "bound-user", projectID: "bound-project", keyID: "write-grant",
		capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	}
	server := httptest.NewServer(h.mcpAuthenticatedHandler(identity))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "lwc416-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: &http.Client{Transport: mcpBearerTestTransport{next: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP SDK client: %v", err)
	}
	defer session.Close()

	discovered, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("MCP SDK tools/list: %v", err)
	}
	toolNames := make(map[string]bool, len(discovered.Tools))
	for _, tool := range discovered.Tools {
		toolNames[tool.Name] = true
	}
	if len(toolNames) != 3 || !toolNames["query_project"] || !toolNames["add_raw"] || !toolNames["import_url"] {
		t.Fatalf("MCP SDK discovered tools = %v", toolNames)
	}

	args := map[string]any{"url": "https://example.com/article?ref=qa", "tags": []string{"qa", "reference"}}
	first, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "import_url", Arguments: args})
	if err != nil || first == nil || first.IsError {
		t.Fatalf("MCP SDK import_url = %+v err=%v", first, err)
	}
	firstPayload, ok := first.StructuredContent.(map[string]any)
	if !ok || firstPayload["status"] != "stored" || firstPayload["processing_status"] != "not_started" || firstPayload["title"] != "Readable guide" || firstPayload["source_url"] != args["url"] {
		t.Fatalf("MCP SDK import_url payload = %#v", first.StructuredContent)
	}
	storagePayload, ok := firstPayload["storage"].(map[string]any)
	if !ok || storagePayload["status"] != "created" || storagePayload["sha256"] == "" {
		t.Fatalf("MCP SDK import_url storage = %#v", firstPayload["storage"])
	}
	path, _ := storagePayload["path"].(string)
	if !strings.HasPrefix(path, "raw/") {
		t.Fatalf("MCP SDK import_url path = %q", path)
	}
	rawURL, err := url.Parse(firstPayload["raw_url"].(string))
	if err != nil || rawURL.Scheme != "https" || rawURL.Host != "wiki.trusted.example" || rawURL.Path != "/raw" || rawURL.Query().Get("project") != identity.projectID || rawURL.Query().Get("file") != strings.TrimPrefix(path, "raw/") {
		t.Fatalf("MCP SDK import_url raw_url = %v", firstPayload["raw_url"])
	}
	stored, err := root.WithScope(identity.userID, identity.projectID).ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("read scoped raw article: %v", err)
	}
	frontmatter, body, err := parseFrontmatterJSON(string(stored))
	if err != nil || body != "# Readable guide\n\nA short, useful reference page.\n" || frontmatter["source"] != args["url"] || frontmatter["title"] != "Readable guide" {
		t.Fatalf("stored raw content = %#v %q err=%v", frontmatter, body, err)
	}
	digest := sha256.Sum256(stored)
	if storagePayload["sha256"] != hex.EncodeToString(digest[:]) || fetches.Load() != 1 || evaluations.Load() != 1 {
		t.Fatalf("digest=%v fetches=%d evaluations=%d", storagePayload["sha256"], fetches.Load(), evaluations.Load())
	}
	if otherProjectFiles, err := root.WithScope(identity.userID, "other-project").ListRawFiles(ctx); err != nil || len(otherProjectFiles) != 0 {
		t.Fatalf("import escaped authorized Project: files=%v err=%v", otherProjectFiles, err)
	}

	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "import_url", Arguments: args})
	if err != nil || second == nil || second.IsError {
		t.Fatalf("MCP SDK import_url retry = %+v err=%v", second, err)
	}
	secondPayload, ok := second.StructuredContent.(map[string]any)
	if !ok || secondPayload["status"] != "stored" || secondPayload["raw_url"] != firstPayload["raw_url"] {
		t.Fatalf("MCP SDK import_url retry payload = %#v", second.StructuredContent)
	}
	secondStorage, _ := secondPayload["storage"].(map[string]any)
	if secondStorage["status"] != "already_exists" || secondStorage["path"] != path || fetches.Load() != 2 || evaluations.Load() != 2 {
		t.Fatalf("retry storage=%#v fetches=%d evaluations=%d", secondStorage, fetches.Load(), evaluations.Load())
	}
}

func TestMCPImportURLUsesResolvedProjectProfileForJevQualityCall(t *testing.T) {
	root := localfs.New(t.TempDir())
	service := urlintake.NewService(
		mcpURLFetcherFunc(func(context.Context, string) (urlintake.Page, error) {
			return urlintake.Page{Title: "Readable guide", Markdown: "# Readable guide\n\nA short, useful reference page.\n"}, nil
		}),
		urlintake.NewJevEvaluator(""),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			return root.WithScope(scope.ActorID, scope.ProjectID), nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = mcpURLResolver{}
	h := New(root, nil, nil, nil, nil, nil)
	h.SetURLIntakeService(service)
	h.SetMCPFrontendOrigin("https://wiki.trusted.example")
	client := &mcpURLProjectLLMClient{}
	h.projectLLMClientResolver = func(_ context.Context, owner, project string) (llm.ProjectChatClient, string, error) {
		if owner != "bound-user" || project != "bound-project" {
			t.Fatalf("Project LLM resolver scope=(%q,%q)", owner, project)
		}
		return client, "personal", nil
	}
	identity := mcpProjectKeyIdentity{
		userID: "bound-user", projectID: "bound-project", keyID: "write-grant",
		capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	}
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, identity)
	result, _, err := h.callMCPImportURL(ctx, nil, importURLInput{URL: "https://example.com/article"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("callMCPImportURL() result=%+v err=%v", result, err)
	}
	var payload importURLResponse
	if err := json.Unmarshal(result.StructuredContent.(json.RawMessage), &payload); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if payload.Status != "stored" || payload.Title != "Readable guide" || client.calls.Load() != 1 {
		t.Fatalf("import result=%+v Project Jev calls=%d, want stored with one resolved Project-profile evaluation", payload, client.calls.Load())
	}
}

func TestMCPImportURLFailsClosedWithoutQualityOrTrustedOrigin(t *testing.T) {
	for _, test := range []struct {
		name   string
		origin string
		eval   urlintake.QualityEvaluator
		want   string
		stage  string
	}{
		{name: "missing evaluator", origin: "https://wiki.example", want: "unavailable", stage: "quality"},
		{name: "missing trusted origin", eval: mcpURLEvaluatorFunc(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			return urlintake.QualityEvaluation{ShellProbability: .01, ContentProbability: .99}, nil
		}), want: "unavailable", stage: "setup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := localfs.New(t.TempDir())
			service := urlintake.NewService(mcpURLFetcherFunc(func(context.Context, string) (urlintake.Page, error) {
				return urlintake.Page{Title: "Guide", Markdown: "Readable body"}, nil
			}), test.eval, func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
				return root.WithScope(scope.ActorID, scope.ProjectID), nil
			}, urlintake.DefaultQualityThresholds)
			service.Resolver = mcpURLResolver{}
			h := New(root, nil, nil, nil, nil, nil)
			h.SetURLIntakeService(service)
			h.SetMCPFrontendOrigin(test.origin)
			identity := mcpProjectKeyIdentity{userID: "user", projectID: "project", keyID: "write-grant", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
			response := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.com/article"}`))
			payload := mcpStructuredResult(t, response)
			if payload["status"] != test.want || payload["failure_stage"] != test.stage || payload["raw_url"] != nil {
				t.Fatalf("import_url payload = %#v", payload)
			}
			files, err := root.WithScope(identity.userID, identity.projectID).ListRawFiles(context.Background())
			if err != nil || len(files) != 0 {
				t.Fatalf("fail-closed import changed raw: files=%v err=%v", files, err)
			}
		})
	}
}

func TestMCPImportURLReportsMayHaveWrittenWhenCommitReadbackFails(t *testing.T) {
	root := localfs.New(t.TempDir())
	base := root.WithScope("user", "project")
	store := &mcpURLCommitUncertainStore{ProjectStore: base}
	service := urlintake.NewService(
		mcpURLFetcherFunc(func(context.Context, string) (urlintake.Page, error) {
			return urlintake.Page{Title: "Guide", Markdown: "# Guide\n\nReadable article body."}, nil
		}),
		mcpURLEvaluatorFunc(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			return urlintake.QualityEvaluation{ShellProbability: .01, ContentProbability: .99}, nil
		}),
		func(urlintake.ActorProject) (urlintake.ProjectStore, error) { return store, nil },
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = mcpURLResolver{}
	h := New(root, nil, nil, nil, nil, nil)
	h.SetURLIntakeService(service)
	h.SetMCPFrontendOrigin("https://wiki.trusted.example")
	identity := mcpProjectKeyIdentity{userID: "user", projectID: "project", keyID: "write-grant", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	response := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.com/uncertain"}`))
	payload := mcpStructuredResult(t, response)
	if payload["status"] != "uncertain" || payload["failure_stage"] != "commit" || payload["raw_url"] != nil || payload["storage"] != nil || !strings.Contains(payload["reason"].(string), "may have written") {
		t.Fatalf("uncertain import payload = %#v", payload)
	}
	files, err := base.ListRawFiles(context.Background())
	if err != nil || len(files) != 1 {
		t.Fatalf("underlying simulated write = files=%v err=%v", files, err)
	}
}

func TestMCPImportURLReturnsDeclaredSizeFailureStage(t *testing.T) {
	root := localfs.New(t.TempDir())
	service := urlintake.NewService(
		mcpURLFetcherFunc(func(context.Context, string) (urlintake.Page, error) {
			return urlintake.Page{}, &urlintake.ProviderFailure{Kind: urlintake.FailureTooLarge}
		}),
		mcpURLEvaluatorFunc(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			t.Fatal("size failure must stop before quality evaluation")
			return urlintake.QualityEvaluation{}, nil
		}),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			return root.WithScope(scope.ActorID, scope.ProjectID), nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = mcpURLResolver{}
	h := New(root, nil, nil, nil, nil, nil)
	h.SetURLIntakeService(service)
	h.SetMCPFrontendOrigin("https://wiki.trusted.example")
	identity := mcpProjectKeyIdentity{userID: "user", projectID: "project", keyID: "write-grant", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	response := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.com/too-large"}`))
	payload := mcpStructuredResult(t, response)
	if payload["status"] != "rejected" || payload["failure_stage"] != "size" || payload["raw_url"] != nil || payload["storage"] != nil {
		t.Fatalf("oversized import payload = %#v", payload)
	}
	files, err := root.WithScope(identity.userID, identity.projectID).ListRawFiles(context.Background())
	if err != nil || len(files) != 0 {
		t.Fatalf("oversized import changed raw: files=%v err=%v", files, err)
	}
}

func TestMCPImportURLOutputSchemaAndOriginAreConstrained(t *testing.T) {
	var inputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(importURLInputSchema, &inputSchema); err != nil {
		t.Fatal(err)
	}
	if len(inputSchema.Properties) != 2 || inputSchema.Properties["url"] == nil || inputSchema.Properties["tags"] == nil || len(inputSchema.Required) != 1 || inputSchema.Required[0] != "url" || inputSchema.Additional {
		t.Fatalf("import_url input schema = %#v", inputSchema)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(importURLOutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["raw_url"] == nil || schema.Properties["storage"] == nil || schema.Properties["failure_stage"] == nil || schema.Additional {
		t.Fatalf("import_url output schema = %#v", schema)
	}
	var failureStage struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(schema.Properties["failure_stage"], &failureStage); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(failureStage.Enum, "size") {
		t.Fatalf("import_url failure stage enum = %v", failureStage.Enum)
	}
	if !strings.Contains(mcpImportURLToolDescription, "not factual accuracy") || !strings.Contains(mcpImportURLToolDescription, "does not start") {
		t.Fatalf("import_url description overstates behavior: %q", mcpImportURLToolDescription)
	}
	if trustedFrontendOrigin("https://user:password@attacker.example") != "" || trustedFrontendOrigin("https://frontend.example/path") != "" {
		t.Fatal("invalid configured frontend origin was accepted")
	}
}
