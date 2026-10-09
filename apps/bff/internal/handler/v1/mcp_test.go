package v1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

type mcpTestExecutor struct {
	result query.Result
	err    error
	got    query.Request
}

func (e *mcpTestExecutor) Execute(_ context.Context, _ cache.Reader, request query.Request) (query.Result, error) {
	e.got = request
	return e.result, e.err
}

func TestMCPQuerySchemaIsQueryOnly(t *testing.T) {
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(queryProjectInputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 2 || schema.Properties["q"] == nil || schema.Properties["mode"] == nil {
		t.Fatalf("input properties = %v, want q and mode only", schema.Properties)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "q" || schema.AdditionalProperties {
		t.Fatalf("input schema required=%v additionalProperties=%v", schema.Required, schema.AdditionalProperties)
	}
	var mode struct {
		Enum    []string `json:"enum"`
		Default string   `json:"default"`
	}
	if err := json.Unmarshal(schema.Properties["mode"], &mode); err != nil {
		t.Fatal(err)
	}
	if strings.Join(mode.Enum, ",") != "wiki,full" || mode.Default != "wiki" {
		t.Fatalf("mode enum/default = %v/%q", mode.Enum, mode.Default)
	}
	if !strings.Contains(string(queryProjectOutputSchema), `"citations"`) || !strings.Contains(string(queryProjectOutputSchema), `"disclosure_required"`) {
		t.Fatal("output schema omits QueryResponse fields")
	}
}

func TestMCPQueryDefaultsWikiAndUsesFixedIdentity(t *testing.T) {
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	executor := &mcpTestExecutor{result: query.Result{
		Query: "coffee", Mode: "wiki",
	}}
	h.SetQueryExecutor(executor)
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "fixture-user", projectID: "bound-project", keyID: "lwc_pk_fixture",
	})
	result, _, err := h.callMCPQuery(ctx, nil, queryProjectInput{Query: "  coffee  "})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("domain empty result marked as tool error: %+v", result)
	}
	if executor.got.Query != "coffee" || executor.got.Mode != "wiki" || executor.got.Profile != nil {
		t.Fatalf("executor request = %+v", executor.got)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want text", result.Content[0])
	}
	if !strings.Contains(text.Text, `"mode":"wiki"`) || strings.Contains(text.Text, `"citations"`) {
		t.Fatalf("default wiki response is not the QueryResponse compatibility shape: %s", text.Text)
	}
	structured, ok := result.StructuredContent.(json.RawMessage)
	if !ok || string(structured) != text.Text {
		t.Fatalf("structured content %T %s differs from text %s", result.StructuredContent, structured, text.Text)
	}
}

func TestMCPQueryPreservesModelPriorEmptyCitations(t *testing.T) {
	h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
	h.SetQueryExecutor(&mcpTestExecutor{result: query.Result{
		Query: "model prior", Mode: "full", Status: "insufficient_evidence", Reason: "no_qualified_evidence",
		AnswerBasis: "model_prior", WikiEvidenceStatus: "no_relevant_evidence", DisclosureRequired: true,
		AISynth: "Synthetic answer", Citations: []search.Citation{},
	}})
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "fixture-user", projectID: "bound-project", keyID: "lwc_pk_fixture",
	})
	result, _, err := h.callMCPQuery(ctx, nil, queryProjectInput{Query: "model prior", Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("model-prior domain result marked as tool error: %+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, `"citations":[]`) || !strings.Contains(text.Text, `"disclosure_required":true`) {
		t.Fatalf("wire response lost explicit model-prior contract: %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(json.RawMessage)
	if !ok || string(structured) != text.Text {
		t.Fatalf("structured content %T %s differs from text %s", result.StructuredContent, structured, text.Text)
	}
}

func TestSharedQueryExecutorRetainsHTTPProfileAndRequiredTags(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	executor := &mcpTestExecutor{}
	h.SetQueryExecutor(executor)
	profile := &query.ProfileSnapshot{}
	_, _, failure := h.executeQuery(context.Background(), nil, profile, query.Request{
		Query: "coffee", Mode: "wiki", RequiredTagIDs: []string{"required_place"},
	})
	if failure != nil {
		t.Fatalf("executeQuery failure = %+v", failure)
	}
	if executor.got.Profile != profile || strings.Join(executor.got.RequiredTagIDs, ",") != "required_place" {
		t.Fatalf("shared executor lost profile or required tag IDs: %+v", executor.got)
	}
}

func TestMCPQueryTurnsExecutorAndStoreFailuresIntoSafeToolErrors(t *testing.T) {
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "fixture-user", projectID: "bound-project", keyID: "lwc_pk_fixture",
	})
	tests := []struct {
		name string
		h    *Handler
	}{
		{
			name: "store failure",
			h:    New(nil, nil, nil, nil, nil, nil),
		},
		{
			name: "profile repository failure",
			h: func() *Handler {
				h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
				h.profileRepository = &profileRepositoryStub{getErr: errors.New("synthetic profile database credential")}
				return h
			}(),
		},
		{
			name: "executor failure",
			h: func() *Handler {
				h := New(localfs.New(t.TempDir()), nil, nil, nil, nil, nil)
				h.SetQueryExecutor(&mcpTestExecutor{err: errors.New("synthetic provider credential leaked if surfaced")})
				return h
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := tc.h.callMCPQuery(ctx, nil, queryProjectInput{Query: "coffee"})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("failure result = %+v", result)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != "generated data unavailable" {
				t.Fatalf("unsafe error content = %#v", result.Content)
			}
			if result.StructuredContent != nil {
				t.Fatalf("failure unexpectedly has structured output: %#v", result.StructuredContent)
			}
		})
	}
}
