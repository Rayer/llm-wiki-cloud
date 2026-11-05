package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/config"
)

func TestInspectMCPProtectedEnvelopeRequiresUnambiguousBoundedID(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		admit  bool
		reject bool
	}{
		{name: "safe integer", body: `{"jsonrpc":"2.0","id":9007199254740991,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, admit: true},
		{name: "string id", body: `{"jsonrpc":"2.0","id":"request","method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, admit: true},
		{name: "missing id", body: `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, reject: true},
		{name: "null id", body: `{"jsonrpc":"2.0","id":null,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, reject: true},
		{name: "fraction id", body: `{"jsonrpc":"2.0","id":1.5,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, reject: true},
		{name: "unsafe integer id", body: `{"jsonrpc":"2.0","id":9007199254740992,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}`, reject: true},
		{name: "duplicate protected name", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_project","name":"list_content","arguments":{"kind":"concept"}}}`, reject: true},
		{name: "bad protected args", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept","unexpected":true}}}`, reject: true},
	}
	policy := (&Handler{}).mcpContentReadPolicy()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reject := inspectMCPProtectedEnvelope([]byte(test.body), policy)
			if reject != test.reject || got.canAdmit != test.admit {
				t.Fatalf("inspect = protected:%v admit:%v reject:%v", got.protected, got.canAdmit, reject)
			}
		})
	}
}

func TestMCPContentSchemasAreValidJSON(t *testing.T) {
	for name, schema := range map[string]json.RawMessage{
		"list input": mcpListContentSchema, "list output": mcpListContentOutputSchema,
		"find input": mcpFindContentSchema, "find output": mcpFindContentOutputSchema,
		"read input": mcpReadContentSchema, "read output": mcpReadContentOutputSchema,
		"raw input": mcpReadSourceRawSchema, "raw output": mcpReadSourceRawOutputSchema,
	} {
		var decoded any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Errorf("%s schema is invalid JSON: %v", name, err)
		}
	}
}

func TestMCPContentPolicyDrivesSchemaAndProtectedPreflight(t *testing.T) {
	handler := &Handler{mcpConfig: &config.BFFMCP{Protection: &config.MCPProtection{ListDefault: 2, ListMax: 4, ChunkMaxBytes: 1024, ReferencePageMax: 3}}}
	policy := handler.mcpContentReadPolicy()
	if policy.ListDefault != 2 || policy.ListMax != 4 || policy.ChunkMaxBytes != 1024 || policy.ReferencePageMax != 3 {
		t.Fatalf("configured content policy was not retained: %+v", policy)
	}
	for name, raw := range map[string]json.RawMessage{
		"list": mcpContentInputSchema(mcpListContentSchema, map[string]any{"limit": map[string]any{"minimum": 1, "maximum": policy.ListMax, "default": policy.ListDefault}}),
		"read": mcpContentInputSchema(mcpReadContentSchema, map[string]any{"max_bytes": map[string]any{"minimum": 1, "maximum": policy.ChunkMaxBytes, "default": policy.ChunkMaxBytes}}),
	} {
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		property := "limit"
		wantMax, wantDefault := float64(4), float64(2)
		if name == "read" {
			property, wantMax, wantDefault = "max_bytes", 1024, 1024
		}
		got := schema.Properties[property]
		if got["type"] != "integer" || got["maximum"] != wantMax || got["default"] != wantDefault {
			t.Errorf("%s schema policy property = %#v", name, got)
		}
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept","limit":5}}}`,
		`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"read_content","arguments":{"kind":"concept","id":"c1","max_bytes":1025}}}`,
	} {
		if _, reject := inspectMCPProtectedEnvelope([]byte(body), policy); !reject {
			t.Errorf("protected preflight accepted value over configured policy: %s", body)
		}
	}
}

func TestInspectMCPProtectedEnvelopeDepthAndBatch(t *testing.T) {
	policy := (&Handler{}).mcpContentReadPolicy()
	args := strings.Repeat("[", maxMCPProtectedJSONDepth+1) + `0` + strings.Repeat("]", maxMCPProtectedJSONDepth+1)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"query_project","arguments":{"q":"q","extra":%s}}}`, args)
	if _, reject := inspectMCPProtectedEnvelope([]byte(body), policy); !reject {
		t.Fatal("over-depth protected input was accepted")
	}
	protectedBatch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}]`
	if _, reject := inspectMCPProtectedEnvelope([]byte(protectedBatch), policy); !reject {
		t.Fatal("protected batch was accepted")
	}
	legacyBatch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_raw","arguments":{}}}]`
	if got, reject := inspectMCPProtectedEnvelope([]byte(legacyBatch), policy); reject || got.protected {
		t.Fatalf("legacy batch changed: %#v reject=%v", got, reject)
	}
}

func TestMCPFinalBoundedWriterCommitsOnlyCompleteBoundedResponse(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		underlying := httptest.NewRecorder()
		writer := newMCPFinalBoundedWriter(underlying, json.RawMessage(`"id"`), "server-id")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(201)
		_, _ = writer.Write([]byte(`{"ok":true}`))
		writer.Flush()
		if underlying.Body.Len() != 0 || underlying.Code != 200 {
			t.Fatalf("response escaped before commit: %d %s", underlying.Code, underlying.Body.String())
		}
		writer.Commit()
		if underlying.Code != 201 || underlying.Body.String() != `{"ok":true}` {
			t.Fatalf("committed response = %d %q", underlying.Code, underlying.Body.String())
		}
	})

	large := strings.Repeat("sentinel-sensitive-output", maxMCPProtectedResponseBytes/24)
	metaResponse, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "bounded-id", "result": map[string]any{"_meta": map[string]any{"padding": large}}})
	errorResponse, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "bounded-id", "error": map[string]any{"code": -32603, "message": "failed", "data": map[string]any{"padding": large}}})
	for _, tc := range []struct {
		name     string
		response []byte
	}{
		{name: "generic overflow", response: append([]byte(strings.Repeat("x", maxMCPProtectedResponseBytes)), []byte("overflow")...)},
		{name: "encoded result metadata overflow", response: metaResponse},
		{name: "encoded protocol error data overflow", response: errorResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			underlying := httptest.NewRecorder()
			underlying.Header().Set("X-Existing", "kept-until-complete")
			writer := newMCPFinalBoundedWriter(underlying, json.RawMessage(`"bounded-id"`), "server-id")
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(500)
			half := len(tc.response) / 2
			_, _ = writer.Write(tc.response[:half])
			writer.Flush()
			_, _ = writer.Write(tc.response[half:])
			if underlying.Body.Len() != 0 {
				t.Fatal("partial response escaped")
			}
			if _, leaksUnwrap := any(writer).(interface{ Unwrap() http.ResponseWriter }); leaksUnwrap {
				t.Fatal("bounded writer exposes its underlying writer through Unwrap")
			}
			writer.Commit()
			if underlying.Code != http.StatusOK || underlying.Body.Len() > maxMCPProtectedResponseBytes || strings.Contains(underlying.Body.String(), "sentinel-sensitive-output") {
				t.Fatalf("fallback status/size/body = %d/%d/%q", underlying.Code, underlying.Body.Len(), underlying.Body.String())
			}
			var response struct {
				ID     string `json:"id"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					Structured map[string]any `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(underlying.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode fallback: %v", err)
			}
			if response.ID != "bounded-id" || !response.Result.IsError || len(response.Result.Content) != 1 || response.Result.Structured["code"] != "response_limit" || !strings.Contains(response.Result.Content[0].Text, `"code":"response_limit"`) || response.Result.Structured["request_id"] != "server-id" {
				t.Fatalf("fallback response is incomplete: %+v", response)
			}
			if underlying.Header().Get("X-Existing") != "kept-until-complete" {
				t.Fatalf("staged headers lost: %#v", underlying.Header())
			}
		})
	}
}
