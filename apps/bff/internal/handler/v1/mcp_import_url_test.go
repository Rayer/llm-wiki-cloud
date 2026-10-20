package v1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCPURLImportService struct {
	importActor   MCPURLImportActorProject
	importRequest MCPURLImportRequest
	importResult  MCPURLImportResult
	importErr     error
	importCalls   int

	confirmActor  MCPURLImportActorProject
	confirmRef    string
	confirmResult MCPURLImportResult
	confirmErr    error
	confirmCalls  int
}

func (s *fakeMCPURLImportService) Import(_ context.Context, actor MCPURLImportActorProject, request MCPURLImportRequest) (MCPURLImportResult, error) {
	s.importCalls++
	s.importActor = actor
	s.importRequest = request
	return s.importResult, s.importErr
}

func (s *fakeMCPURLImportService) Confirm(_ context.Context, actor MCPURLImportActorProject, ref string) (MCPURLImportResult, error) {
	s.confirmCalls++
	s.confirmActor = actor
	s.confirmRef = ref
	return s.confirmResult, s.confirmErr
}

func TestMCPURLImportSchemasExposeOnlyURLOrBoundConfirmation(t *testing.T) {
	var input struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		OneOf                []json.RawMessage          `json:"oneOf"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(mcpURLImportInputSchema, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Properties) != 3 || input.Properties["url"] == nil || input.Properties["tags"] == nil || input.Properties["confirmation_ref"] == nil {
		t.Fatalf("import_url input properties = %v", input.Properties)
	}
	if len(input.OneOf) != 2 || input.AdditionalProperties {
		t.Fatalf("oneOf=%v additionalProperties=%v", len(input.OneOf), input.AdditionalProperties)
	}
	for _, forbidden := range []string{"user_id", "project_id", "path", "filename", "overwrite", "delete", "annotation"} {
		if input.Properties[forbidden] != nil {
			t.Fatalf("import_url unexpectedly accepts %q", forbidden)
		}
	}
	if !strings.Contains(mcpImportURLToolDescription, "does not refetch") || !strings.Contains(mcpImportURLToolDescription, "untrusted source data") ||
		!strings.Contains(mcpImportURLToolDescription, "inspect Project raw sources before retrying") {
		t.Fatal("import_url description must disclose confirmation, data handling, and uncertain-write recovery")
	}

	var output struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(mcpURLImportOutputSchema, &output); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"created", "updated", "unchanged", "needs_confirmation", "rejected", "conflict", "unavailable"} {
		if !strings.Contains(string(output.Properties["status"]), `"`+status+`"`) {
			t.Fatalf("import_url output schema omits status %q", status)
		}
	}
	if !strings.Contains(string(output.Properties["commit_status"]), `"uncertain"`) {
		t.Fatal("import_url output schema omits uncertain commit status")
	}
}

func TestMCPURLImportToolListRequiresExplicitWriteConsent(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	identity := mcpProjectKeyIdentity{userID: "user-one", projectID: "project-one", keyID: "grant", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	list := func() map[string]bool {
		response := serveMCPRequest(t, h, identity, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		items := response["result"].(map[string]any)["tools"].([]any)
		names := map[string]bool{}
		for _, item := range items {
			name, _ := item.(map[string]any)["name"].(string)
			names[name] = true
		}
		return names
	}
	if names := list(); names["query_project"] != true || names["add_raw"] != true || names["import_url"] != true {
		t.Fatalf("tool list with explicit write consent = %v", names)
	}
	unavailable := serveMCPRequest(t, h, identity, mcpToolCallBody(2, "import_url", `{"url":"https://example.test/article"}`))
	if payload := mcpStructuredResult(t, unavailable); payload["status"] != "unavailable" {
		t.Fatalf("missing service did not return unavailable: %#v", payload)
	}
	h.SetMCPURLImportService(&fakeMCPURLImportService{})
	if names := list(); names["query_project"] != true || names["add_raw"] != true || names["import_url"] != true {
		t.Fatalf("tool list changed after service setup = %v", names)
	}
	readOnly := identity
	readOnly.capabilities = []string{mcpQueryCapability}
	response := serveMCPRequest(t, h, readOnly, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	items := response["result"].(map[string]any)["tools"].([]any)
	for _, item := range items {
		name, _ := item.(map[string]any)["name"].(string)
		if name == "add_raw" || name == "import_url" {
			t.Fatalf("query-only grant discovered write tool %q", name)
		}
	}
}

func TestMCPURLImportAdapterBindsActorAndConfirmsExactReference(t *testing.T) {
	service := &fakeMCPURLImportService{importResult: MCPURLImportResult{
		Status: "needs_confirmation", SourceURL: "https://example.test/article",
		Title: "Candidate title", Preview: "Candidate excerpt", ConfirmationRef: "opaque-confirmation-ref",
	}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetMCPURLImportService(service)
	identity := mcpProjectKeyIdentity{userID: "verified-user", projectID: "verified-project", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	first := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.test/article?tracking=private","tags":["reading","research"]}`))
	firstPayload := mcpStructuredResult(t, first)
	if firstPayload["status"] != "needs_confirmation" || firstPayload["confirmation_ref"] != "opaque-confirmation-ref" ||
		firstPayload["title"] != "Candidate title" || firstPayload["preview"] != "Candidate excerpt" || firstPayload["processing_status"] != "not_started" {
		t.Fatalf("uncertain import_url result = %#v", firstPayload)
	}
	if service.importCalls != 1 || service.importActor != (MCPURLImportActorProject{UserID: "verified-user", ProjectID: "verified-project"}) ||
		service.importRequest.URL != "https://example.test/article?tracking=private" || strings.Join(service.importRequest.Tags, ",") != "reading,research" {
		t.Fatalf("import service was not called with verified identity and request: actor=%+v request=%+v calls=%d", service.importActor, service.importRequest, service.importCalls)
	}

	service.confirmResult = MCPURLImportResult{Status: "updated", Path: "raw/article.md", SHA256: strings.Repeat("a", 64), SourceURL: "https://example.test/article"}
	confirmed := serveMCPRequest(t, h, identity, mcpToolCallBody(2, "import_url", `{"confirmation_ref":"opaque-confirmation-ref"}`))
	confirmedPayload := mcpStructuredResult(t, confirmed)
	if confirmedPayload["status"] != "updated" || confirmedPayload["path"] != "raw/article.md" ||
		confirmedPayload["sha256"] != strings.Repeat("a", 64) || confirmedPayload["processing_status"] != "not_started" {
		t.Fatalf("confirmed import_url result = %#v", confirmedPayload)
	}
	if service.importCalls != 1 || service.confirmCalls != 1 || service.confirmRef != "opaque-confirmation-ref" ||
		service.confirmActor != service.importActor {
		t.Fatalf("confirmation must use exact ref and same verified actor: service=%+v", service)
	}
}

func TestMCPURLImportRejectsAmbiguousAndQueryOnlyCallsWithoutServiceUse(t *testing.T) {
	service := &fakeMCPURLImportService{}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetMCPURLImportService(service)
	identity := mcpProjectKeyIdentity{userID: "verified-user", projectID: "verified-project", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	ambiguous := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.test/article","confirmation_ref":"opaque-ref"}`))
	if !mcpCallResultIsError(t, ambiguous) || service.importCalls != 0 || service.confirmCalls != 0 {
		t.Fatalf("ambiguous URL/confirmation request was not rejected before service use: response=%#v service=%+v", ambiguous, service)
	}

	readOnly := identity
	readOnly.capabilities = []string{mcpQueryCapability}
	denied := serveMCPRequest(t, h, readOnly, mcpToolCallBody(2, "import_url", `{"url":"https://example.test/article"}`))
	if !mcpCallResultIsError(t, denied) || service.importCalls != 0 || service.confirmCalls != 0 {
		t.Fatalf("query-only import_url call was not rejected before service use: response=%#v service=%+v", denied, service)
	}
}

func mcpCallResultIsError(t *testing.T, response map[string]any) bool {
	t.Helper()
	if protocolError, ok := response["error"].(map[string]any); ok {
		return protocolError["code"] != nil
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("MCP response has no result: %#v", response)
	}
	isError, _ := result["isError"].(bool)
	return isError
}

func TestMCPURLImportDoesNotExposeServiceErrorDetailsOrInvalidCommitPath(t *testing.T) {
	service := &fakeMCPURLImportService{importErr: errors.New("provider token=secret tracking=private")}
	h := New(nil, nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "verified-user", projectID: "verified-project", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability},
	})
	h.SetMCPURLImportService(service)
	result, _, err := h.callMCPURLImport(ctx, nil, mcpURLImportInput{URL: "https://example.test/article?tracking=private"})
	if err != nil || !result.IsError {
		t.Fatalf("service error result=%+v err=%v", result, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, "secret") || strings.Contains(text, "tracking=private") {
		t.Fatalf("service error details leaked: %q", text)
	}

	service.importErr = nil
	service.importResult = MCPURLImportResult{Status: "updated", Path: "raw/../private.md", SHA256: strings.Repeat("b", 64)}
	result, _, err = h.callMCPURLImport(ctx, nil, mcpURLImportInput{URL: "https://example.test/article"})
	if err != nil || !result.IsError {
		t.Fatalf("invalid service path result=%+v err=%v", result, err)
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok || payload["status"] != "unavailable" {
		t.Fatalf("invalid service path was presented as committed: %#v", payload)
	}

	service.importResult = MCPURLImportResult{
		Status: "uncertain", CommitStatus: "uncertain", SourceURL: "https://example.test/article?tracking=private",
		Title: "Candidate", Preview: "Bounded candidate preview",
	}
	result, _, err = h.callMCPURLImport(ctx, nil, mcpURLImportInput{URL: "https://example.test/article?tracking=private"})
	if err != nil || !result.IsError {
		t.Fatalf("uncertain service result=%+v err=%v", result, err)
	}
	text = result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"status":"unavailable"`) || !strings.Contains(text, `"commit_status":"uncertain"`) ||
		!strings.Contains(text, "Review the Project raw sources before retrying") || strings.Contains(text, "tracking=private") {
		t.Fatalf("uncertain save outcome was not represented safely: %q", text)
	}
}
