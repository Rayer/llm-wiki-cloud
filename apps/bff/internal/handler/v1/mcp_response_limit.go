package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

const maxMCPProtectedResponseBytes = 256 << 10

type mcpProtectedRequestContextKey struct{}
type mcpProtectedRequestIDContextKey struct{}
type mcpProtectedServerRequestIDContextKey struct{}
type mcpSDKTestToolRegistrarContextKey struct{}

// mcpFinalBoundedWriter buffers the SDK's complete protected JSON response so
// no header or prefix escapes if the encoded envelope exceeds the wire limit.
// It intentionally has no Unwrap or ReaderFrom method.
type mcpFinalBoundedWriter struct {
	underlying http.ResponseWriter
	header     http.Header
	requestID  json.RawMessage
	serverID   string
	body       bytes.Buffer
	status     int
	overflow   bool
	finished   bool
}

func newMCPFinalBoundedWriter(underlying http.ResponseWriter, requestID json.RawMessage, serverID string) *mcpFinalBoundedWriter {
	header := make(http.Header, len(underlying.Header()))
	for key, values := range underlying.Header() {
		header[key] = append([]string(nil), values...)
	}
	return &mcpFinalBoundedWriter{underlying: underlying, header: header, requestID: append(json.RawMessage(nil), requestID...), serverID: serverID}
}

func (w *mcpFinalBoundedWriter) Header() http.Header { return w.header }

func (w *mcpFinalBoundedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *mcpFinalBoundedWriter) Write(data []byte) (int, error) {
	if w.finished {
		return 0, errors.New("protected MCP response is already committed")
	}
	if !w.overflow && w.body.Len()+len(data) <= maxMCPProtectedResponseBytes {
		_, _ = w.body.Write(data)
	} else {
		w.overflow = true
		w.body.Reset()
	}
	// The SDK must finish once; the wrapper decides whether any buffered bytes
	// are committed after ServeHTTP returns.
	return len(data), nil
}

func (w *mcpFinalBoundedWriter) Flush()            {}
func (w *mcpFinalBoundedWriter) FlushError() error { return nil }

func (w *mcpFinalBoundedWriter) Commit() {
	if w.finished {
		return
	}
	w.finished = true
	if w.overflow {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", "")
		w.copyHeadersToUnderlying()
		w.underlying.WriteHeader(http.StatusOK)
		id := w.requestID
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		value := map[string]any{"schema_version": 1, "code": "response_limit", "message": "response exceeds limit", "retryable": false, "retry_after_ms": 0, "request_id": w.serverID}
		text, _ := json.Marshal(value)
		result := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": value, "isError": true}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = w.underlying.Write(response)
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.copyHeadersToUnderlying()
	w.underlying.WriteHeader(w.status)
	if w.body.Len() > 0 {
		_, _ = w.underlying.Write(w.body.Bytes())
	}
}

func (w *mcpFinalBoundedWriter) copyHeadersToUnderlying() {
	underlying := w.underlying.Header()
	for key := range underlying {
		delete(underlying, key)
	}
	for key, values := range w.header {
		if key == "Content-Length" && len(values) == 1 && values[0] == "" {
			continue
		}
		underlying[key] = append([]string(nil), values...)
	}
}
