package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
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
	sdkAuthenticated := h.mcpSDKHandler()
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
		h.serveAuthenticatedMCP(sdkAuthenticated, identity, w, r)
	})
}

func (h *Handler) mcpSDKHandler() http.Handler {
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
	return sdkAuthenticated
}

func (h *Handler) mcpAuthenticatedHandler(identity mcpProjectKeyIdentity) http.Handler {
	sdkAuthenticated := h.mcpSDKHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveAuthenticatedMCP(sdkAuthenticated, identity, w, r)
	})
}

func (h *Handler) serveAuthenticatedMCP(sdkAuthenticated http.Handler, identity mcpProjectKeyIdentity, w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), mcpProjectKeyIdentityContextKey{}, identity)
	observation := &mcpQueryObservation{}
	ctx = context.WithValue(ctx, mcpQueryObservationKey{}, observation)
	if isMCPQueryProjectCall(r) {
		var diagnostic *queryDiagnostic
		ctx, diagnostic = beginQueryDiagnostic(ctx, "mcp", identity.projectID)
		observation.start(diagnostic)
		buffered := newMCPQueryResponseWriter()
		statusWriter := &queryStatusWriter{ResponseWriter: buffered}
		sdkAuthenticated.ServeHTTP(statusWriter, r.WithContext(ctx))
		status := statusWriter.status
		if status == 0 {
			status = http.StatusOK
		}
		if observation.finish(status) {
			buffered.body = bytes.NewBuffer(mcpValidationDiagnosticBody(buffered.body.Bytes(), diagnostic))
			buffered.header.Del("Content-Length")
		}
		buffered.writeTo(w)
		return
	}
	statusWriter := &queryStatusWriter{ResponseWriter: w}
	sdkAuthenticated.ServeHTTP(statusWriter, r.WithContext(ctx))
	status := statusWriter.status
	if status == 0 {
		status = http.StatusOK
	}
	observation.finish(status)
}

const maxMCPDiagnosticProbeBytes = mcp.DefaultMaxRequestBodyBytes

func isMCPQueryProjectCall(r *http.Request) bool {
	if r.Method != http.MethodPost || r.Body == nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPDiagnosticProbeBytes+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	if err != nil || len(body) > maxMCPDiagnosticProbeBytes {
		return false
	}
	var message struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	return json.Unmarshal(body, &message) == nil && message.Method == "tools/call" && message.Params.Name == "query_project"
}

type mcpQueryResponseWriter struct {
	header http.Header
	status int
	body   *bytes.Buffer
}

func newMCPQueryResponseWriter() *mcpQueryResponseWriter {
	return &mcpQueryResponseWriter{header: make(http.Header), body: new(bytes.Buffer)}
}

func (w *mcpQueryResponseWriter) Header() http.Header { return w.header }

func (w *mcpQueryResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *mcpQueryResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (w *mcpQueryResponseWriter) writeTo(dst http.ResponseWriter) {
	for key, values := range w.header {
		dst.Header()[key] = append([]string(nil), values...)
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(w.body.Bytes())
}

func mcpValidationDiagnosticBody(body []byte, diagnostic *queryDiagnostic) []byte {
	var message map[string]json.RawMessage
	if json.Unmarshal(body, &message) != nil {
		return body
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(message["result"], &result) != nil {
		return body
	}
	var isError bool
	if json.Unmarshal(result["isError"], &isError) != nil || !isError {
		return body
	}
	failure := queryFailureFor("request_validation", errors.New("MCP input schema validation failed"), nil)
	textContent, _ := json.Marshal([]map[string]string{{"type": "text", "text": failure.message + " Diagnostic ID: " + diagnostic.id}})
	structured, _ := json.Marshal(map[string]string{"error": failure.message, "code": failure.code, "diagnostic_id": diagnostic.id})
	result["content"] = textContent
	result["structuredContent"] = structured
	message["result"], _ = json.Marshal(result)
	encoded, err := json.Marshal(message)
	if err != nil {
		return body
	}
	return encoded
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
	identity, identityOK := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	diagnostic := queryDiagnosticFrom(ctx)
	if diagnostic == nil {
		ctx, diagnostic = beginQueryDiagnostic(ctx, "mcp", identity.projectID)
	}
	observation := mcpQueryObservationFrom(ctx)
	toolIsError := false
	if observation != nil {
		observation.set(diagnostic, false)
	} else {
		defer func() { diagnostic.finish(http.StatusOK, toolIsError) }()
	}
	fail := func(stage string, cause error, reader storage.Store) (*mcp.CallToolResult, any, error) {
		failure := queryFailureFor(stage, cause, reader)
		diagnostic.fail(failure)
		toolIsError = true
		if observation != nil {
			observation.set(diagnostic, true)
		}
		return mcpQueryToolError(failure.message, failure.code, diagnostic.id), nil, nil
	}
	queryText := strings.TrimSpace(input.Query)
	if queryText == "" {
		return fail("request_validation", errors.New("q field is required"), nil)
	}
	mode := input.Mode
	if mode == "" {
		mode = "wiki"
	}
	if mode != "wiki" && mode != "full" {
		return fail("request_validation", errors.New("mode must be wiki or full"), nil)
	}
	if !identityOK || identity.userID == "" || identity.projectID == "" {
		return fail("scope_resolution", auth.ErrProjectKeyUnavailable, nil)
	}
	diagnostic.setStage("generation_resolution")
	reader, profile, err := h.queryStoreFor(ctx, identity.userID, identity.projectID)
	if err != nil {
		return fail("profile_resolution", err, reader)
	}
	response, runtimeIdentity, failure := h.executeQuery(ctx, reader, profile, query.Request{Query: queryText, Mode: mode})
	if failure != nil {
		diagnostic.fail(failure)
		toolIsError = true
		if observation != nil {
			observation.set(diagnostic, true)
		}
		return mcpQueryToolError(failure.message, failure.code, diagnostic.id), nil, nil
	}
	data, err := json.Marshal(response)
	if err != nil {
		return fail("response_encode", err, reader)
	}
	if runtimeIdentity != nil {
		diagnostic.setGeneration(runtimeIdentity.GenerationID)
	}
	diagnostic.succeed(response.Status, response.Reason, diagnostic.receipt.Receipt())
	if observation != nil {
		observation.set(diagnostic, false)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: json.RawMessage(data),
	}, nil, nil
}

func mcpQueryToolError(message, code, diagnosticID string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": message, "code": code, "diagnostic_id": diagnosticID},
		IsError:           true,
	}
}
