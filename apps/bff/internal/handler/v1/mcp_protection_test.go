package v1

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reject := inspectMCPProtectedEnvelope([]byte(test.body))
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

func TestInspectMCPProtectedEnvelopeDepthAndBatch(t *testing.T) {
	args := strings.Repeat("[", maxMCPProtectedJSONDepth+1) + `0` + strings.Repeat("]", maxMCPProtectedJSONDepth+1)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"query_project","arguments":{"q":"q","extra":%s}}}`, args)
	if _, reject := inspectMCPProtectedEnvelope([]byte(body)); !reject {
		t.Fatal("over-depth protected input was accepted")
	}
	protectedBatch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_content","arguments":{"kind":"concept"}}}]`
	if _, reject := inspectMCPProtectedEnvelope([]byte(protectedBatch)); !reject {
		t.Fatal("protected batch was accepted")
	}
	legacyBatch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_raw","arguments":{}}}]`
	if got, reject := inspectMCPProtectedEnvelope([]byte(legacyBatch)); reject || got.protected {
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

	t.Run("overflow fallback", func(t *testing.T) {
		underlying := httptest.NewRecorder()
		underlying.Header().Set("X-Existing", "kept-until-complete")
		writer := newMCPFinalBoundedWriter(underlying, json.RawMessage(`"bounded-id"`), "server-id")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(500)
		_, _ = writer.Write([]byte(strings.Repeat("x", maxMCPProtectedResponseBytes)))
		_, _ = writer.Write([]byte("overflow"))
		if underlying.Body.Len() != 0 {
			t.Fatal("partial response escaped")
		}
		writer.Commit()
		if underlying.Code != 200 || underlying.Body.Len() > maxMCPProtectedResponseBytes {
			t.Fatalf("fallback status/size = %d/%d", underlying.Code, underlying.Body.Len())
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
