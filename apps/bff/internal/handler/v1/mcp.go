package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/google/uuid"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type mcpProjectKeyIdentityContextKey struct{}

type mcpProjectKeyIdentity struct {
	userID       string
	projectID    string
	keyID        string
	capabilities []string
	revalidate   func(context.Context) error
}

const (
	mcpQueryCapability    = "mcp:query"
	mcpRawWriteCapability = "mcp:raw:write"
)

const mcpQueryProjectToolDescription = "Read-only search of the single project bound to the supplied project key. The key grants query-only access; project and user are fixed by the credential."

type queryProjectInput struct {
	Query string `json:"q"`
	Mode  string `json:"mode,omitempty"`
}

var queryProjectInputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"q":{"type":"string","minLength":1,"description":"Search text; whitespace-only values are rejected."},
		"mode":{"type":"string","enum":["wiki","full"],"default":"wiki","description":"wiki returns matching Project evidence and may synthesize an answer from it; full can return a disclosed model_prior answer when relevant Project evidence is unavailable. Defaults to wiki."}
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
		identity := mcpProjectKeyIdentity{
			userID: record.UserID, projectID: record.ProjectID, keyID: record.KeyID,
			capabilities: []string{mcpQueryCapability},
		}
		identity.revalidate = func(ctx context.Context) error {
			fresh, err := h.projectKeyService.AuthenticateWithoutUsage(ctx, token, record.ProjectID)
			if err != nil {
				return err
			}
			if fresh.UserID != identity.userID || fresh.ProjectID != identity.projectID || fresh.KeyID != identity.keyID {
				return auth.ErrProjectKeyAccessDenied
			}
			return nil
		}
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, w, r)
	})
}

func (h *Handler) mcpSDKHandler(identity mcpProjectKeyIdentity) http.Handler {
	capabilities := identity.capabilities
	metadata := h.mcpConfig
	instructions := ""
	queryDescription := mcpQueryProjectToolDescription
	addRawDescription := mcpAddRawToolDescription
	importURLDescription := mcpImportURLToolDescription
	if metadata != nil {
		instructions = metadata.Instructions
		queryDescription = metadata.ToolDescriptions.QueryProject
		addRawDescription = metadata.ToolDescriptions.AddRaw
		importURLDescription = metadata.ToolDescriptions.ImportURL
	}
	serverOptions := &mcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}, Tools: &mcp.ToolCapabilities{}},
		SetCacheable: func(_ context.Context, request mcp.Request, cacheable *mcp.Cacheable) {
			switch request.(type) {
			case *mcp.ServerRequest[*mcp.DiscoverParams]:
				cacheable.CacheScope = "private"
				if metadata != nil {
					cacheable.TTLMs = metadata.DiscoveryTTLMS
				}
			case *mcp.ListToolsRequest:
				cacheable.CacheScope = "private"
				if metadata != nil {
					cacheable.TTLMs = metadata.ToolsListTTLMS
				}
			case *mcp.ListResourcesRequest:
				cacheable.CacheScope = "private"
			case *mcp.ReadResourceRequest:
				cacheable.CacheScope = "private"
				cacheable.TTLMs = 0
			}
		},
	}
	var server *mcp.Server
	if hasMCPCapability(capabilities, mcpQueryCapability) {
		serverOptions.Capabilities.Resources = &mcp.ResourceCapabilities{Subscribe: true}
		serverOptions.SubscribeHandler = func(ctx context.Context, request *mcp.SubscribeRequest) error {
			return h.subscribeMCPPipelineEvents(ctx, request, server)
		}
		serverOptions.UnsubscribeHandler = func(context.Context, *mcp.UnsubscribeRequest) error { return nil }
	}
	server = mcp.NewServer(&mcp.Implementation{
		Name:    "llm-wiki-bff",
		Version: buildinfo.Current().ProductVersion,
	}, serverOptions)
	server.AddReceivingMiddleware(mcpRequestObservationMiddleware)
	if hasMCPCapability(capabilities, mcpQueryCapability) {
		server.AddResource(&mcp.Resource{
			URI: pipelineEventsURI(identity.projectID), Name: "pipeline-events", Title: "Project Pipeline events", MIMEType: "application/json",
			Description: pipelineEventsResourceDescription,
		}, h.readMCPPipelineEvents)
	}
	mcp.AddTool[queryProjectInput, any](server, &mcp.Tool{
		Name:         "query_project",
		Title:        "Query this project",
		Description:  queryDescription,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema:  queryProjectInputSchema,
		OutputSchema: queryProjectOutputSchema,
	}, h.callMCPQuery)
	if hasMCPCapability(capabilities, mcpQueryCapability) && hasMCPCapability(capabilities, mcpRawWriteCapability) {
		mcp.AddTool[addRawInput, any](server, &mcp.Tool{
			Name:        "add_raw",
			Title:       "Add a raw Markdown source",
			Description: addRawDescription,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolHint(false), IdempotentHint: true, OpenWorldHint: boolHint(false)},
			InputSchema: addRawInputSchema, OutputSchema: addRawOutputSchema,
		}, h.callMCPAddRaw)
		mcp.AddTool[importURLInput, any](server, &mcp.Tool{
			Name:        "import_url",
			Title:       "Import a usable URL into this Project",
			Description: importURLDescription,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolHint(false), IdempotentHint: true, OpenWorldHint: boolHint(true)},
			InputSchema: importURLInputSchema, OutputSchema: importURLOutputSchema,
		}, h.callMCPImportURL)
	}

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
	sdkAuthenticated := h.mcpSDKHandler(identity)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveAuthenticatedMCP(sdkAuthenticated, identity, w, r)
	})
}

func (h *Handler) serveAuthenticatedMCP(sdkAuthenticated http.Handler, identity mcpProjectKeyIdentity, w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), mcpProjectKeyIdentityContextKey{}, identity)
	control := newMCPListenControl(h.eventTiming())
	ctx = context.WithValue(ctx, mcpListenControlContextKey{}, control)
	observed := &mcpResponseObservationWriter{ResponseWriter: w, listen: control}
	sdkAuthenticated.ServeHTTP(observed, r.WithContext(ctx))
	if observed.hasProtocolError() {
		// The pinned SDK may turn a parameter decode failure into a JSON-RPC
		// error response before AddReceivingMiddleware runs. Classify only the
		// protocol envelope; never retain or log its message/data fields.
		observability.MarkRequestProtocolError(ctx)
	}
}

const maxMCPProtocolResponseObservation = 64 << 10

type mcpResponseObservationWriter struct {
	http.ResponseWriter
	listen   *mcpListenControl
	body     bytes.Buffer
	complete bool
}

func (w *mcpResponseObservationWriter) Write(p []byte) (int, error) {
	if w.listen != nil && w.listen.isActive() {
		if err := w.listen.setWriteDeadline(w.ResponseWriter); err != nil {
			return 0, err
		}
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil && w.listen != nil {
		w.listen.fail(err)
	}
	if w.listen != nil && err == nil {
		w.listen.observeWrite(p)
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if mediaTypeErr != nil || mediaType != "application/json" {
		w.complete = true
		w.body.Reset()
		return n, err
	}
	if !w.complete && w.body.Len()+n <= maxMCPProtocolResponseObservation {
		_, _ = w.body.Write(p[:n])
	} else {
		w.complete = true
		w.body.Reset()
	}
	return n, err
}

func (w *mcpResponseObservationWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *mcpResponseObservationWriter) FlushError() error {
	if w.listen != nil && w.listen.isActive() {
		if err := w.listen.setWriteDeadline(w.ResponseWriter); err != nil {
			return err
		}
	}
	err := flushResponseWriter(w.ResponseWriter)
	if err != nil {
		if w.listen != nil {
			w.listen.fail(err)
		}
		return err
	}
	if w.listen != nil {
		w.listen.acknowledgeAfterFlush()
	}
	return nil
}

func flushResponseWriter(writer http.ResponseWriter) error {
	var headers []interface{ WriteHeaderNow() }
	for current := writer; current != nil; {
		if flusher, ok := current.(interface{ FlushError() error }); ok {
			for _, header := range headers {
				header.WriteHeaderNow()
			}
			return flusher.FlushError()
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		if header, ok := current.(interface{ WriteHeaderNow() }); ok {
			headers = append(headers, header)
		}
		current = unwrapper.Unwrap()
	}
	return http.NewResponseController(writer).Flush()
}

func (w *mcpResponseObservationWriter) Flush() { _ = w.FlushError() }

func (w *mcpResponseObservationWriter) hasProtocolError() bool {
	if w.complete || w.body.Len() == 0 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	type protocolResponse struct {
		Error *struct{} `json:"error"`
	}
	var single protocolResponse
	if json.Unmarshal(w.body.Bytes(), &single) == nil && single.Error != nil {
		return true
	}
	var batch []protocolResponse
	if json.Unmarshal(w.body.Bytes(), &batch) != nil {
		return false
	}
	for _, item := range batch {
		if item.Error != nil {
			return true
		}
	}
	return false
}

func mcpRequestObservationMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method == "subscriptions/listen" {
			if control, ok := ctx.Value(mcpListenControlContextKey{}).(*mcpListenControl); ok && control != nil {
				var cancel context.CancelFunc
				ctx, cancel = control.activate(ctx)
				defer func() {
					cancel()
					control.deactivate()
				}()
			}
		}
		envelopeID := observability.RequestIDFromContext(ctx)
		requestID := uuid.NewString()
		if strings.HasPrefix(method, "notifications/") && envelopeID != "" {
			requestID = envelopeID
		}
		ctx = observability.WithRequestID(ctx, requestID)
		ctx, span, operation := observability.StartMCPRequestSpan(ctx, method, requestID, envelopeID)
		identity, _ := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)

		var diagnostic *queryDiagnostic
		var observation *mcpQueryObservation
		if call, ok := request.(*mcp.CallToolRequest); ok && call.Params.Name == "query_project" {
			ctx, diagnostic = beginQueryDiagnostic(ctx, "mcp", identity.projectID)
			observation = &mcpQueryObservation{}
			observation.start(diagnostic)
			ctx = context.WithValue(ctx, mcpQueryObservationKey{}, observation)
		}

		result, err := next(ctx, method, request)
		toolError := false
		callResult, isToolResult := result.(*mcp.CallToolResult)
		if isToolResult && callResult == nil {
			result = nil
			isToolResult = false
		}
		if isToolResult {
			toolError = callResult.IsError
		}
		if observation != nil {
			validationFailure := observation.finishResult(http.StatusOK, result != nil, toolError)
			if validationFailure && isToolResult {
				failure := queryFailureFor("request_validation", errors.New("MCP input schema validation failed"), nil)
				callResult.Content = []mcp.Content{&mcp.TextContent{Text: failure.message + " Diagnostic ID: " + diagnostic.id}}
				callResult.StructuredContent = map[string]any{"error": failure.message, "code": failure.code, "diagnostic_id": diagnostic.id}
			}
		}
		if !nilMCPResult(result) {
			metadata := result.GetMeta()
			withRequestID := make(map[string]any, len(metadata)+1)
			for key, value := range metadata {
				withRequestID[key] = value
			}
			withRequestID["request_id"] = requestID
			result.SetMeta(withRequestID)
		}

		outcome := "success"
		if err != nil || toolError {
			outcome = "error"
		}
		observability.FinishMCPRequestSpan(span, outcome)
		spanContext := span.SpanContext()
		if observation == nil {
			slog.InfoContext(ctx, "mcp.request.terminal",
				"request_id", requestID,
				"envelope_request_id", envelopeID,
				"trace_id", spanContext.TraceID().String(),
				"span_id", spanContext.SpanID().String(),
				"operation", operation,
				"phase", "terminal",
				"outcome", outcome,
			)
		}
		return result, err
	}
}

func nilMCPResult(result mcp.Result) bool {
	if result == nil {
		return true
	}
	value := reflect.ValueOf(result)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
		observation.set(diagnostic)
	} else {
		defer func() { diagnostic.finish(http.StatusOK, toolIsError) }()
	}
	fail := func(stage string, cause error, reader storage.Store) (*mcp.CallToolResult, any, error) {
		failure := queryFailureFor(stage, cause, reader)
		diagnostic.fail(failure)
		toolIsError = true
		if observation != nil {
			observation.set(diagnostic)
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
	response, runtimeIdentity, failure := h.executeQuery(ctx, reader, profile, query.Request{Query: queryText, Mode: mode}, identity.userID, identity.projectID)
	if failure != nil {
		diagnostic.fail(failure)
		toolIsError = true
		if observation != nil {
			observation.set(diagnostic)
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
		observation.set(diagnostic)
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
