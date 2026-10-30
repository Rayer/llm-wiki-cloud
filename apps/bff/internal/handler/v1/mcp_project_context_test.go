package v1

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestAuthenticatedMCPMetadataContainsBoundProjectDescriptionAndPublishedSummary(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "mcp-context-owner", fmt.Sprintf("mcp-context-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	stateRef := projectRef.Collection("content_context").Doc("state")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready", "name": "Paper archive",
		"description": "Published research papers and reading notes", "description_revision": int64(3),
	}); err != nil {
		t.Fatalf("seed project metadata: %v", err)
	}
	if _, err := stateRef.Set(ctx, contentContextStateMap("generation-1", testContentContextDigest('a'), "ready", "", testContentContextDigest('a'), "Published summary from generation 1")); err != nil {
		t.Fatalf("seed content context: %v", err)
	}
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()

	root := &contentContextTestRoot{
		RootStore: localfs.New(t.TempDir()),
		identity: storage.QueryGenerationIdentity{
			ProjectID: projectID, GenerationID: "generation-1", ConceptsDigest: "sha256:" + testContentContextDigest('a'),
		},
	}
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	metadata := &config.BFFMCP{Instructions: "CaC instructions"}
	metadata.ToolDescriptions.QueryProject = "Configured query description"
	metadata.ToolDescriptions.AddRaw = "Configured add_raw description"
	metadata.ToolDescriptions.ImportURL = "Configured import_url description"
	h.SetMCPConfig(metadata)
	identity := mcpProjectKeyIdentity{userID: owner, projectID: projectID, keyID: "bound-key", capabilities: []string{mcpQueryCapability}}

	discover := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	discoverResult := mcpMetadataResult(t, discover)
	instructions, ok := discoverResult["instructions"].(string)
	if !ok || !strings.Contains(instructions, "CaC instructions") || !strings.Contains(instructions, "Paper archive") ||
		!strings.Contains(instructions, "Published research papers and reading notes") ||
		!strings.Contains(instructions, "Published summary from generation 1") || !strings.Contains(instructions, "generation-1") {
		t.Fatalf("authenticated Project context missing from discover instructions: %#v", discoverResult)
	}

	list := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	tools := mcpMetadataResult(t, list)["tools"].([]interface{})
	query := tools[0].(map[string]interface{})
	queryDescription := query["description"].(string)
	if query["name"] != "query_project" || !strings.Contains(queryDescription, "untrusted advisory JSON") ||
		!strings.Contains(queryDescription, "Paper archive") || !strings.Contains(queryDescription, "Published summary from generation 1") {
		t.Fatalf("query tool description omitted the context hint: %#v", query)
	}
}

func TestFirestoreRepositorySummaryIsProjectedToAuthenticatedMCPMetadata(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	owner, projectID := "mcp-producer-owner", fmt.Sprintf("mcp-producer-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectDocID(owner, projectID))
	stateRef := projectRef.Collection("content_context").Doc("state")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready", "name": "Research archive",
		"description": "Published engineering and product research", "description_revision": int64(2),
	}); err != nil {
		t.Fatalf("seed authorized Project description: %v", err)
	}
	defer func() {
		_, _ = stateRef.Delete(context.Background())
		_, _ = projectRef.Delete(context.Background())
	}()

	digest := testContentContextDigest('f')
	authority := &contentContextAPITestAuthority{publication: contentcontext.Publication{
		Generation: "published-G1", ConceptsDigest: digest, ManifestIdentity: "manifest-G1",
	}}
	repository := contentcontext.NewFirestoreRepository(fsClient.Raw())
	if _, err := repository.ReconcileCurrent(ctx, owner, projectID, authority); err != nil {
		t.Fatalf("register source publication: %v", err)
	}
	claim, err := repository.Claim(ctx, owner, projectID, authority)
	if err != nil || claim == nil {
		t.Fatalf("claim source summary work: claim=%+v err=%v", claim, err)
	}
	authority.publication = contentcontext.Publication{
		Generation: "published-G2", ConceptsDigest: "sha256:" + digest, ManifestIdentity: "manifest-G2",
	}
	if _, err := repository.ReconcileCurrent(ctx, owner, projectID, authority); err != nil {
		t.Fatalf("carry summary work to current publication: %v", err)
	}
	completion := contentcontext.SummaryCompletion{
		Text: "Published engineering and product topics", PromptRevision: contentcontext.SummaryPromptRevision,
		ProfileID: "selected-profile", ProfileRevision: 5, Role: contentcontext.SummaryRole,
		RequestedModel: "fast-requested", ReturnedModel: "fast-returned",
		InputDigest: testContentContextDigest('e'), SelectionVersion: contentcontext.SummarySelectionVersion,
	}
	if saved, err := repository.Complete(ctx, owner, projectID, authority, *claim, completion); err != nil || !saved {
		t.Fatalf("save source summary after carry-forward: saved=%t err=%v", saved, err)
	}

	root := &contentContextTestRoot{
		RootStore: localfs.New(t.TempDir()),
		identity: storage.QueryGenerationIdentity{
			ProjectID: projectID, GenerationID: "published-G2", ConceptsDigest: "sha256:" + digest,
		},
	}
	h := New(root, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	metadata := &config.BFFMCP{Instructions: "Keep the existing project security rules."}
	metadata.ToolDescriptions.ImportURL = "Import a usable URL into this project."
	h.SetMCPConfig(metadata)
	identity := mcpProjectKeyIdentity{userID: owner, projectID: projectID, keyID: "bound-key", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	response := mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	result := mcpMetadataResult(t, response)
	instructions, ok := result["instructions"].(string)
	if !ok || !strings.Contains(instructions, "Research archive") || !strings.Contains(instructions, "Published engineering and product research") ||
		!strings.Contains(instructions, completion.Text) || !strings.Contains(instructions, "published-G2") || !strings.Contains(instructions, "published-G1") {
		t.Fatalf("producer-backed Project context lost its current/source identity: %#v", result)
	}

	response = mcpMetadataWireRequest(t, h.mcpAuthenticatedHandler(identity),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "2026-07-28")
	tools := mcpMetadataResult(t, response)["tools"].([]interface{})
	var importURL map[string]interface{}
	for _, candidate := range tools {
		tool, ok := candidate.(map[string]interface{})
		if ok && tool["name"] == "import_url" {
			importURL = tool
			break
		}
	}
	if importURL == nil {
		t.Fatalf("import_url missing from authenticated tools: %#v", tools)
	}
	toolDescription := importURL["description"].(string)
	if importURL["name"] != "import_url" || !strings.Contains(toolDescription, "Research archive") || !strings.Contains(toolDescription, completion.Text) {
		t.Fatalf("producer-backed summary missing from import tool description: %#v", importURL)
	}
}

func TestMCPToolContextCandidateUses300CodepointsAndMarksTruncation(t *testing.T) {
	value, truncated := truncateMCPCodepoints(strings.Repeat("😀", 400), 300)
	if !truncated || utf8.RuneCountInString(value) != 300 || !strings.HasSuffix(value, "…[truncated]") {
		t.Fatalf("truncated Project context candidate length=%d truncated=%t value=%q", utf8.RuneCountInString(value), truncated, value)
	}
}
