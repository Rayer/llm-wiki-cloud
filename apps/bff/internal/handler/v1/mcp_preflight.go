package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/mcpquota"
)

const (
	maxMCPProtectedIDEncoded = 4096
	maxMCPProtectedIDDecoded = 1024
	maxMCPProtectedMetaBytes = 16 << 10
	maxMCPProtectedParams    = 64 << 10
	maxMCPProtectedJSONDepth = 32
	defaultMCPControlSlots   = 16
)

type mcpWireEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpWireToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta"`
}

type mcpProtectedPreflight struct {
	protected bool
	operation string
	class     mcpquota.Class
	validArgs bool
	canAdmit  bool
	id        json.RawMessage
}

func (h *Handler) serveAuthenticatedMCPRequest(identity mcpProjectKeyIdentity, w http.ResponseWriter, r *http.Request) {
	releaseControl, ok := h.acquireMCPControlSlot()
	if !ok {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, `{"error":"request capacity unavailable"}`, http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentImportMCPRequestBytes)
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		releaseControl()
		writeMCPEnvelopeError(w, http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	preflight, reject := inspectMCPProtectedEnvelope(body, h.mcpContentReadPolicy())
	releaseControl()
	if reject {
		writeMCPEnvelopeError(w, http.StatusBadRequest)
		return
	}
	if preflight.protected && !preflight.canAdmit {
		writeMCPEnvelopeError(w, http.StatusBadRequest)
		return
	}
	var lease *mcpquota.Lease
	var outcome *mcpAdmissionOutcome
	if preflight.canAdmit && preflight.protected && hasMCPCapability(identity.capabilities, capabilityForMCPTool(preflight.operation)) {
		if identity.revalidate != nil && identity.revalidate(r.Context()) != nil {
			outcome = &mcpAdmissionOutcome{decision: mcpquota.Decision{Code: "scope_denied"}}
		} else if limiter, err := h.mcpAdmissionService(); err != nil {
			outcome = &mcpAdmissionOutcome{decision: mcpquota.Decision{Code: "admission_unavailable", RetryAfterMS: 1000}}
		} else if limiter != nil {
			var decision mcpquota.Decision
			lease, decision, err = limiter.Admit(r.Context(), mcpquota.Identity{Environment: limiter.Environment(), Subject: identity.userID, ProjectID: identity.projectID}, preflight.class)
			if err != nil || lease == nil {
				outcome = &mcpAdmissionOutcome{decision: decision}
			}
		}
	}
	requestCtx := r.Context()
	if preflight.protected {
		requestCtx = context.WithValue(requestCtx, mcpProtectedRequestContextKey{}, true)
		requestCtx = context.WithValue(requestCtx, mcpProtectedServerRequestIDContextKey{}, uuid.NewString())
		if len(preflight.id) > 0 {
			requestCtx = context.WithValue(requestCtx, mcpProtectedRequestIDContextKey{}, append(json.RawMessage(nil), preflight.id...))
		}
	}
	if lease != nil {
		var cancel context.CancelFunc
		requestCtx, cancel = lease.Context(requestCtx)
		defer cancel()
		requestCtx = context.WithValue(requestCtx, mcpAdmissionLeaseContextKey{}, lease)
	}
	if outcome != nil {
		requestCtx = context.WithValue(requestCtx, mcpAdmissionOutcomeContextKey{}, *outcome)
	}
	r = r.WithContext(requestCtx)
	includeProjectContext := !preflight.protected || h.mcpConfig == nil || h.mcpConfig.Protection == nil || preflight.operation == "query_project" && lease != nil
	sdk := h.mcpSDKHandlerWithProjectContext(requestCtx, identity, includeProjectContext)
	h.serveAuthenticatedMCP(sdk, identity, w, r)
	if lease != nil {
		if err := lease.Close(); err != nil {
			slog.WarnContext(r.Context(), "mcp.admission.release_failed", "class", preflight.class, "decision", "unavailable")
		}
	}
}

func (h *Handler) acquireMCPControlSlot() (func(), bool) {
	h.mcpControlOnce.Do(func() {
		limit := defaultMCPControlSlots
		if h.mcpConfig != nil && h.mcpConfig.Protection != nil && h.mcpConfig.Protection.ControlLocalMaxInflight > 0 {
			limit = h.mcpConfig.Protection.ControlLocalMaxInflight
		}
		h.mcpControlSlots = make(chan struct{}, limit)
	})
	select {
	case h.mcpControlSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-h.mcpControlSlots }) }, true
	default:
		return func() {}, false
	}
}

func (h *Handler) mcpAdmissionService() (*mcpquota.Limiter, error) {
	if h.mcpConfig == nil || h.mcpConfig.Protection == nil {
		return nil, nil
	}
	h.mcpAdmissionMu.Lock()
	defer h.mcpAdmissionMu.Unlock()
	if h.mcpAdmission != nil || h.mcpAdmissionErr != nil {
		return h.mcpAdmission, h.mcpAdmissionErr
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		h.mcpAdmissionErr = mcpquota.ErrUnavailable
		return nil, h.mcpAdmissionErr
	}
	environment := h.mcpEnvironment
	if environment == "" {
		environment = "local"
	}
	h.mcpAdmission, h.mcpAdmissionErr = mcpquota.New(h.firestore.Raw(), environment, *h.mcpConfig.Protection)
	return h.mcpAdmission, h.mcpAdmissionErr
}

func inspectMCPProtectedEnvelope(body []byte, policy mcpContentReadPolicy) (mcpProtectedPreflight, bool) {
	var envelopes []json.RawMessage
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return mcpProtectedPreflight{}, false
	}
	if trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &envelopes) != nil {
			return mcpProtectedPreflight{}, false
		}
		var found *mcpProtectedPreflight
		for _, raw := range envelopes {
			current, _ := inspectMCPProtectedObject(raw, policy)
			if !current.protected {
				if found != nil {
					return mcpProtectedPreflight{}, true
				}
				continue
			}
			if found != nil {
				return mcpProtectedPreflight{}, true
			}
			copy := current
			found = &copy
		}
		if found != nil {
			// Protected batches have per-call admission semantics. Reject the
			// whole batch before any tool can run, including mixed batches.
			return mcpProtectedPreflight{}, true
		}
		return mcpProtectedPreflight{}, false
	}
	return inspectMCPProtectedObject(trimmed, policy)
}

func inspectMCPProtectedObject(raw []byte, policy mcpContentReadPolicy) (mcpProtectedPreflight, bool) {
	var envelope mcpWireEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return mcpProtectedPreflight{}, false
	}
	if envelope.Method != "tools/call" {
		return mcpProtectedPreflight{}, false
	}
	var params mcpWireToolCall
	if len(envelope.Params) == 0 || json.Unmarshal(envelope.Params, &params) != nil {
		return mcpProtectedPreflight{protected: true}, false
	}
	class, protected := protectedMCPTool(params.Name)
	if !protected {
		return mcpProtectedPreflight{}, false
	}
	preflight := mcpProtectedPreflight{protected: true, operation: params.Name, class: class, id: append(json.RawMessage(nil), envelope.ID...)}
	if !validProtectedMCPID(envelope.ID) || len(params.Meta) > maxMCPProtectedMetaBytes || len(envelope.Params) > maxMCPProtectedParams || jsonDepthExceeds(raw, maxMCPProtectedJSONDepth) || jsonHasDuplicateObjectKeys(raw) {
		return preflight, true
	}
	preflight.validArgs = validMCPProtectedArguments(params.Name, params.Arguments, policy)
	preflight.canAdmit = preflight.validArgs && len(envelope.ID) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.ID), []byte("null"))
	return preflight, !preflight.canAdmit
}

func protectedMCPTool(name string) (mcpquota.Class, bool) {
	switch name {
	case "query_project":
		return mcpquota.Query, true
	case "list_content", "find_content", "read_content", "read_source_raw":
		return mcpquota.Content, true
	default:
		return "", false
	}
}

func capabilityForMCPTool(name string) string {
	if name == "query_project" {
		return mcpQueryCapability
	}
	return mcpContentReadCapability
}

func validProtectedMCPID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxMCPProtectedIDEncoded || !json.Valid(raw) || !utf8.Valid(raw) {
		return false
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil || len(value) > maxMCPProtectedIDDecoded {
			return false
		}
		return true
	}
	number := string(raw)
	if number == "" || number == "-" || strings.ContainsAny(number, ".eE+") {
		return false
	}
	parsed, err := strconv.ParseInt(number, 10, 64)
	if err == nil && parsed >= -9007199254740991 && parsed <= 9007199254740991 {
		return true
	}
	return false
}

func validMCPProtectedArguments(name string, raw json.RawMessage, policy mcpContentReadPolicy) bool {
	decode := func(target any) bool {
		if len(raw) == 0 || len(raw) > maxMCPProtectedParams {
			return false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(target) != nil {
			return false
		}
		var trailing any
		return decoder.Decode(&trailing) == io.EOF
	}
	switch name {
	case "query_project":
		var input queryProjectInput
		return decode(&input) && strings.TrimSpace(input.Query) != "" && (input.Mode == "" || input.Mode == "wiki" || input.Mode == "full")
	case "list_content":
		var input listContentInput
		return decode(&input) && validMCPContentKind(input.Kind) && (input.Limit == 0 || input.Limit >= 1 && input.Limit <= policy.ListMax) && len(input.Cursor) <= mcpContentMaxCursor
	case "find_content":
		var input findContentInput
		return decode(&input) && validMCPContentKind(input.Kind) && strings.TrimSpace(input.Name) != "" && len([]byte(strings.TrimSpace(input.Name))) <= mcpContentMaxName && (input.Limit == 0 || input.Limit >= 1 && input.Limit <= policy.ListMax) && len(input.Cursor) <= mcpContentMaxCursor
	case "read_content":
		var input readContentInput
		return decode(&input) && validMCPContentKind(input.Kind) && len(input.ID) > 0 && len(input.ID) <= mcpContentMaxID && (input.MaxBytes == 0 || input.MaxBytes >= 1 && input.MaxBytes <= policy.ChunkMaxBytes) && len(input.Generation) <= 128 && len(input.Cursor) <= mcpContentMaxCursor && len(input.RefsCursor) <= mcpContentMaxCursor
	case "read_source_raw":
		var input readSourceRawInput
		return decode(&input) && len(input.SourceID) > 0 && len(input.SourceID) <= mcpContentMaxID && (input.MaxBytes == 0 || input.MaxBytes >= 1 && input.MaxBytes <= policy.ChunkMaxBytes) && len(input.Generation) <= 128 && len(input.Cursor) <= mcpContentMaxCursor
	default:
		return false
	}
}

// jsonHasDuplicateObjectKeys preserves the SDK's last-wins behavior for
// unprotected legacy requests while allowing protected calls to fail closed
// whenever a second decoder could select a different envelope field.
func jsonHasDuplicateObjectKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var visit func() (bool, error)
	visit = func() (bool, error) {
		token, err := decoder.Token()
		if err != nil {
			return false, err
		}
		delim, isDelim := token.(json.Delim)
		if !isDelim {
			return false, nil
		}
		duplicate := false
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return true, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return true, errors.New("invalid object key")
				}
				if _, exists := seen[key]; exists {
					duplicate = true
				}
				seen[key] = struct{}{}
				childDuplicate, err := visit()
				if err != nil {
					return true, err
				}
				duplicate = duplicate || childDuplicate
			}
			_, err := decoder.Token()
			return duplicate, err
		case '[':
			for decoder.More() {
				childDuplicate, err := visit()
				if err != nil {
					return true, err
				}
				duplicate = duplicate || childDuplicate
			}
			_, err := decoder.Token()
			return duplicate, err
		default:
			return true, errors.New("invalid JSON delimiter")
		}
	}
	duplicate, err := visit()
	if err != nil {
		return true
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return true
	}
	return duplicate
}

func jsonDepthExceeds(data []byte, maxDepth int) bool {
	depth := 0
	inString, escaped := false, false
	for _, value := range data {
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return true
			}
		case '}', ']':
			depth--
		}
	}
	return false
}

func writeMCPEnvelopeError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Invalid Request"}}`)
}

func mcpAdmissionToolError(decision mcpquota.Decision, requestID string) *mcp.CallToolResult {
	code := decision.Code
	if code == "" || code == "admitted" {
		code = "admission_unavailable"
	}
	retryable := code == "rate_limited" || code == "concurrency_limited" || code == "capacity_limited" || code == "admission_unavailable"
	message := strings.ReplaceAll(code, "_", " ")
	value := map[string]any{"schema_version": 1, "code": code, "retryable": retryable, "retry_after_ms": decision.RetryAfterMS, "request_id": requestID, "message": message}
	encoded, _ := json.Marshal(value)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}, StructuredContent: value, IsError: true}
}
