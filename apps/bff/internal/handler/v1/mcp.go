package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/query"
)

type mcpProjectKeyIdentityContextKey struct{}

type mcpProjectKeyIdentity struct {
	userID    string
	projectID string
	keyID     string
}

type queryProjectInput struct {
	Query string `json:"q"`
	Mode  string `json:"mode,omitempty"`
}

var queryProjectInputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"q":{"type":"string","minLength":1,"description":"Search text; whitespace-only values are rejected."},
		"mode":{"type":"string","enum":["wiki","full"],"default":"wiki","description":"Use wiki for raw matches or full for synthesis; defaults to wiki."}
	},
	"required":["q"],
	"additionalProperties":false
}`)

var queryProjectOutputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"query":{"type":"string"},
		"mode":{"type":"string","enum":["wiki","full"]},
		"results":{"type":"array","items":{"type":"object"}},
		"expand":{"type":"object"},
		"ai_synth":{"type":"string"},
		"citations":{"type":"array","items":{"type":"object"}},
		"status":{"type":"string"},
		"reason":{"type":"string"},
		"answer_basis":{"type":"string"},
		"wiki_evidence_status":{"type":"string"},
		"disclosure_required":{"type":"boolean"}
	},
	"required":["query","mode","results"],
	"additionalProperties":false
}`)

// MCPHandler builds a stateless Streamable HTTP adapter. Every request is
// authenticated against current project-key authority before it reaches the
// SDK; the authenticated identity is then carried into that request's tool
// context. Stateless transport prevents a previous call's identity from being
// reused by a later key or session header.
func (h *Handler) MCPHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "llm-wiki-bff",
		Version: buildinfo.Current().ProductVersion,
	}, nil)
	mcp.AddTool[queryProjectInput, any](server, &mcp.Tool{
		Name:         "query_project",
		Title:        "Query this project",
		Description:  "Read-only search of the single project bound to the supplied project key. The key grants query-only access; project and user are fixed by the credential.",
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema:  queryProjectInputSchema,
		OutputSchema: queryProjectOutputSchema,
	}, h.callMCPQuery)

	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})

	// The SDK's session-hijack check consumes TokenInfo.UserID. Keep that value
	// bound to the opaque key ID if transport behavior changes later; request
	// authority itself always comes from the freshly authenticated context below.
	sdkAuthenticated := mcpauth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
		if !ok || identity.keyID == "" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: identity.keyID}, nil
	}, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(transport)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := mcpBearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeMCPProjectKeyError(w, auth.ErrProjectKeyInvalid)
			return
		}
		if h.projectKeyService == nil {
			writeMCPProjectKeyError(w, auth.ErrProjectKeyUnavailable)
			return
		}
		record, err := h.projectKeyService.Authenticate(r.Context(), token, r.Header.Get("X-Project-ID"))
		if err != nil {
			writeMCPProjectKeyError(w, err)
			return
		}
		identity := mcpProjectKeyIdentity{userID: record.UserID, projectID: record.ProjectID, keyID: record.KeyID}
		ctx := context.WithValue(r.Context(), mcpProjectKeyIdentityContextKey{}, identity)
		sdkAuthenticated.ServeHTTP(w, r.WithContext(ctx))
	})
}

func mcpBearerToken(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeMCPProjectKeyError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(auth.ProjectKeyErrorStatus(err))
	_ = json.NewEncoder(w).Encode(map[string]string{"error": auth.ProjectKeySafeError(err)})
}

func (h *Handler) callMCPQuery(ctx context.Context, _ *mcp.CallToolRequest, input queryProjectInput) (*mcp.CallToolResult, any, error) {
	queryText := strings.TrimSpace(input.Query)
	if queryText == "" {
		return mcpQueryToolError("q field is required"), nil, nil
	}
	mode := input.Mode
	if mode == "" {
		mode = "wiki"
	}
	if mode != "wiki" && mode != "full" {
		return mcpQueryToolError("mode must be wiki or full"), nil, nil
	}
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" {
		return mcpQueryToolError(auth.ErrProjectKeyUnavailable.Error()), nil, nil
	}
	reader, profile, err := h.queryStoreFor(ctx, identity.userID, identity.projectID)
	if err != nil {
		return mcpQueryToolError(queryStoreFailure(err).message), nil, nil
	}
	response, _, failure := h.executeQuery(ctx, reader, profile, query.Request{Query: queryText, Mode: mode})
	if failure != nil {
		return mcpQueryToolError(failure.message), nil, nil
	}
	data, err := json.Marshal(response)
	if err != nil {
		return mcpQueryToolError("generated data unavailable"), nil, nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: json.RawMessage(data),
	}, nil, nil
}

func mcpQueryToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
		IsError: true,
	}
}
