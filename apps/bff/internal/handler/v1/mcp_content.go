package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/mcpquota"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

const (
	mcpContentKindConcept = "concept"
	mcpContentKindSource  = "source"
	mcpContentMaxCursor   = 2048
	mcpContentMaxName     = 512
	mcpContentMaxID       = 128
	mcpContentChunkBytes  = 16 << 10
)

type listContentInput struct {
	Kind   string `json:"kind"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type findContentInput struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type readContentInput struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Generation string `json:"generation,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
	RefsCursor string `json:"refs_cursor,omitempty"`
}

type readSourceRawInput struct {
	SourceID   string `json:"source_id"`
	Generation string `json:"generation,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
}

type mcpContentRef struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Slug           string `json:"slug"`
	CitationPath   string `json:"citation_path"`
	TitleSource    string `json:"title_source"`
	TitleTruncated bool   `json:"title_truncated"`
	exactTitle     string `json:"-"`
}

type mcpContentCursor struct {
	SchemaVersion   int    `json:"schema_version"`
	Operation       string `json:"operation"`
	Kind            string `json:"kind,omitempty"`
	ID              string `json:"id,omitempty"`
	Generation      string `json:"generation"`
	ManifestSHA     string `json:"manifest_sha256"`
	ScopeBinding    string `json:"scope_binding"`
	Name            string `json:"name,omitempty"`
	LastID          string `json:"last_id,omitempty"`
	Revision        string `json:"revision,omitempty"`
	Offset          int    `json:"offset,omitempty"`
	ReferenceIndex  int    `json:"reference_index,omitempty"`
	CandidateOffset int    `json:"candidate_offset,omitempty"`
}

type mcpContentSnapshot struct {
	client               *gcs.Client
	generation           string
	manifestSHA          string
	manifest             generation.Manifest
	identity             storage.QueryGenerationIdentity
	ids                  wikiindex.IDMap
	refs                 []mcpContentRef
	byID                 map[string]mcpContentRef
	indexByID            map[string]int
	bySlugAndKind        map[string][]mcpContentRef
	byCanonicalRoute     map[string]mcpContentRef
	sourcesByConceptSlug map[string][]string
	releases             []func()
}

func (s *mcpContentSnapshot) close() {
	if s == nil {
		return
	}
	for i := len(s.releases) - 1; i >= 0; i-- {
		s.releases[i]()
	}
	s.releases = nil
}

type mcpGenerationPinner interface {
	PinCurrentGenerationIfPublished(context.Context) (*gcs.Client, gcs.GenerationSnapshot, bool, error)
	PinGeneration(context.Context, string) (*gcs.Client, gcs.GenerationSnapshot, error)
}

var (
	mcpListContentSchema         = json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["concept","source"]},"limit":{"type":"integer","minimum":1,"maximum":50,"default":20},"cursor":{"type":"string","maxLength":2048}},"required":["kind"],"additionalProperties":false}`)
	mcpFindContentSchema         = json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["concept","source"]},"name":{"type":"string","minLength":1,"maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":50,"default":20},"cursor":{"type":"string","maxLength":2048}},"required":["kind","name"],"additionalProperties":false}`)
	mcpReadContentSchema         = json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["concept","source"]},"id":{"type":"string","minLength":1,"maxLength":128},"generation":{"type":"string","maxLength":128},"cursor":{"type":"string","maxLength":2048},"max_bytes":{"type":"integer","minimum":1,"maximum":16384,"default":16384},"refs_cursor":{"type":"string","maxLength":2048}},"required":["kind","id"],"additionalProperties":false}`)
	mcpReadSourceRawSchema       = json.RawMessage(`{"type":"object","properties":{"source_id":{"type":"string","minLength":1,"maxLength":128},"generation":{"type":"string","maxLength":128},"cursor":{"type":"string","maxLength":2048},"max_bytes":{"type":"integer","minimum":1,"maximum":16384,"default":16384}},"required":["source_id"],"additionalProperties":false}`)
	mcpContentRefOutputSchema    = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false}`)
	mcpResolvedRefOutputSchema   = json.RawMessage(`{"type":"object","properties":{"relation":{"type":"string","enum":["synthesis_source","mention"]},"reference_id":{"type":"string"},"reference_index":{"type":"integer","minimum":0},"candidate_offset":{"type":"integer","minimum":0},"candidates_complete":{"type":"boolean"},"target_text":{"type":"string"},"label":{"type":"string"},"kind":{"type":["string","null"],"enum":["concept","source",null]},"resolution":{"type":"string","enum":["resolved","ambiguous","unresolved","external"]},"id":{"type":["string","null"]},"title":{"type":["string","null"]},"citation_path":{"type":["string","null"]},"candidates":{"type":"array","items":{"$ref":"#/$defs/content_ref"}},"origin":{"type":"string","enum":["frontmatter.sources","markdown_link","wikilink"]},"byte_start":{"type":["integer","null"]},"byte_end":{"type":["integer","null"]}},"required":["relation","reference_id","reference_index","candidate_offset","candidates_complete","target_text","label","kind","resolution","id","title","citation_path","candidates","origin","byte_start","byte_end"],"additionalProperties":false,"$defs":{"content_ref":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false}}`)
	mcpListContentOutputSchema   = json.RawMessage(`{"type":"object","properties":{"schema_version":{"type":"integer","const":1},"kind":{"type":"string","enum":["concept","source"]},"generation":{"type":["string","null"]},"knowledge_status":{"type":"string","enum":["no_published_generation"]},"items":{"type":"array","items":{"$ref":"#/$defs/content_ref"}},"next_cursor":{"type":["string","null"]}},"required":["schema_version","kind","generation","items","next_cursor"],"additionalProperties":false,"$defs":{"content_ref":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false}}}`)
	mcpFindContentOutputSchema   = json.RawMessage(`{"type":"object","properties":{"schema_version":{"type":"integer","const":1},"kind":{"type":"string","enum":["concept","source"]},"generation":{"type":["string","null"]},"knowledge_status":{"type":"string","enum":["no_published_generation"]},"status":{"type":"string","enum":["unique","ambiguous","not_found"]},"candidates":{"type":"array","items":{"$ref":"#/$defs/content_ref"}},"next_cursor":{"type":["string","null"]}},"required":["schema_version","kind","generation","status","candidates","next_cursor"],"additionalProperties":false,"$defs":{"content_ref":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false}}}`)
	mcpReadContentOutputSchema   = json.RawMessage(`{"type":"object","properties":{"schema_version":{"type":"integer","const":1},"ref":{"$ref":"#/$defs/content_ref"},"requested_id":{"type":"string"},"generation":{"type":"string"},"revision":{"type":"string"},"content":{"type":"object","properties":{"text":{"type":"string"},"start_byte":{"type":"integer","minimum":0},"end_byte":{"type":"integer","minimum":0},"total_bytes":{"type":"integer","minimum":0},"complete":{"type":"boolean"},"next_cursor":{"type":["string","null"]}},"required":["text","start_byte","end_byte","total_bytes","complete","next_cursor"],"additionalProperties":false},"synthesis_sources":{"type":"array","items":{"$ref":"#/$defs/resolved_ref"}},"mentions":{"type":"array","items":{"$ref":"#/$defs/resolved_ref"}},"provenance_status":{"type":"string","enum":["available","unavailable"]},"refs_complete":{"type":"boolean"},"next_refs_cursor":{"type":["string","null"]}},"required":["schema_version","ref","generation","revision","content","synthesis_sources","mentions","provenance_status","refs_complete","next_refs_cursor"],"additionalProperties":false,"$defs":{"content_ref":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false},"resolved_ref":{"type":"object","properties":{"relation":{"type":"string","enum":["synthesis_source","mention"]},"reference_id":{"type":"string"},"reference_index":{"type":"integer","minimum":0},"candidate_offset":{"type":"integer","minimum":0},"candidates_complete":{"type":"boolean"},"target_text":{"type":"string"},"label":{"type":"string"},"kind":{"type":["string","null"],"enum":["concept","source",null]},"resolution":{"type":"string","enum":["resolved","ambiguous","unresolved","external"]},"id":{"type":["string","null"]},"title":{"type":["string","null"]},"citation_path":{"type":["string","null"]},"candidates":{"type":"array","items":{"$ref":"#/$defs/content_ref"}},"origin":{"type":"string","enum":["frontmatter.sources","markdown_link","wikilink"]},"byte_start":{"type":["integer","null"]},"byte_end":{"type":["integer","null"]}},"required":["relation","reference_id","reference_index","candidate_offset","candidates_complete","target_text","label","kind","resolution","id","title","citation_path","candidates","origin","byte_start","byte_end"],"additionalProperties":false}}}`)
	mcpReadSourceRawOutputSchema = json.RawMessage(`{"type":"object","properties":{"schema_version":{"type":"integer","const":1},"source":{"$ref":"#/$defs/content_ref"},"generation":{"type":"string"},"revision":{"type":"string"},"representation":{"type":"string","const":"retained_source_text"},"content":{"type":"object","properties":{"text":{"type":"string"},"start_byte":{"type":"integer","minimum":0},"end_byte":{"type":"integer","minimum":0},"total_bytes":{"type":"integer","minimum":0},"complete":{"type":"boolean"},"next_cursor":{"type":["string","null"]}},"required":["text","start_byte","end_byte","total_bytes","complete","next_cursor"],"additionalProperties":false}},"required":["schema_version","source","generation","revision","representation","content"],"additionalProperties":false,"$defs":{"content_ref":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["concept","source"]},"title":{"type":"string"},"slug":{"type":"string"},"citation_path":{"type":"string"},"title_source":{"type":"string","enum":["metadata","slug"]},"title_truncated":{"type":"boolean"}},"required":["id","kind","title","slug","citation_path","title_source","title_truncated"],"additionalProperties":false}}}`)
)

func (h *Handler) addMCPContentTools(server *mcp.Server, metadata *config.BFFMCP) {
	if metadata == nil || metadata.Protection == nil {
		return
	}
	descriptions := metadata.ToolDescriptions
	policy := h.mcpContentReadPolicy()
	listSchema := mcpContentInputSchema(mcpListContentSchema, map[string]any{"limit": map[string]any{"minimum": 1, "maximum": policy.ListMax, "default": policy.ListDefault}})
	findSchema := mcpContentInputSchema(mcpFindContentSchema, map[string]any{"limit": map[string]any{"minimum": 1, "maximum": policy.ListMax, "default": policy.ListDefault}})
	readSchema := mcpContentInputSchema(mcpReadContentSchema, map[string]any{"max_bytes": map[string]any{"minimum": 1, "maximum": policy.ChunkMaxBytes, "default": policy.ChunkMaxBytes}})
	rawSchema := mcpContentInputSchema(mcpReadSourceRawSchema, map[string]any{"max_bytes": map[string]any{"minimum": 1, "maximum": policy.ChunkMaxBytes, "default": policy.ChunkMaxBytes}})
	for _, tool := range []struct {
		name, title, description string
		input                    json.RawMessage
		register                 func()
	}{
		{"list_content", "List published content", descriptions.ListContent, listSchema, func() {
			mcp.AddTool[listContentInput, any](server, &mcp.Tool{Name: "list_content", Title: "List published content", Description: descriptions.ListContent, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolHint(false), OpenWorldHint: boolHint(false)}, InputSchema: listSchema, OutputSchema: mcpListContentOutputSchema}, h.callMCPListContent)
		}},
		{"find_content", "Find published content by exact name", descriptions.FindContent, findSchema, func() {
			mcp.AddTool[findContentInput, any](server, &mcp.Tool{Name: "find_content", Title: "Find published content by exact name", Description: descriptions.FindContent, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolHint(false), OpenWorldHint: boolHint(false)}, InputSchema: findSchema, OutputSchema: mcpFindContentOutputSchema}, h.callMCPFindContent)
		}},
		{"read_content", "Read published content", descriptions.ReadContent, readSchema, func() {
			mcp.AddTool[readContentInput, any](server, &mcp.Tool{Name: "read_content", Title: "Read published content", Description: descriptions.ReadContent, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolHint(false), OpenWorldHint: boolHint(false)}, InputSchema: readSchema, OutputSchema: mcpReadContentOutputSchema}, h.callMCPReadContent)
		}},
		{"read_source_raw", "Read retained source text", descriptions.ReadSourceRaw, rawSchema, func() {
			mcp.AddTool[readSourceRawInput, any](server, &mcp.Tool{Name: "read_source_raw", Title: "Read retained source text", Description: descriptions.ReadSourceRaw, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolHint(false), OpenWorldHint: boolHint(false)}, InputSchema: rawSchema, OutputSchema: mcpReadSourceRawOutputSchema}, h.callMCPReadSourceRaw)
		}},
	} {
		if strings.TrimSpace(tool.description) != "" {
			tool.register()
		}
	}
}

type mcpContentReadPolicy struct {
	ListDefault      int
	ListMax          int
	ChunkMaxBytes    int
	ReferencePageMax int
}

func (h *Handler) mcpContentReadPolicy() mcpContentReadPolicy {
	policy := mcpContentReadPolicy{ListDefault: 20, ListMax: 50, ChunkMaxBytes: mcpContentChunkBytes, ReferencePageMax: 50}
	if h != nil && h.mcpConfig != nil && h.mcpConfig.Protection != nil {
		configured := h.mcpConfig.Protection
		policy = mcpContentReadPolicy{ListDefault: configured.ListDefault, ListMax: configured.ListMax, ChunkMaxBytes: configured.ChunkMaxBytes, ReferencePageMax: configured.ReferencePageMax}
	}
	return policy
}

func mcpContentInputSchema(base json.RawMessage, properties map[string]any) json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(base, &schema) != nil {
		return base
	}
	shape, _ := schema["properties"].(map[string]any)
	for name, updates := range properties {
		current, _ := shape[name].(map[string]any)
		for key, value := range updates.(map[string]any) {
			current[key] = value
		}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return base
	}
	return encoded
}

func (h *Handler) callMCPListContent(ctx context.Context, _ *mcp.CallToolRequest, input listContentInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpContentReadCapability) {
		return mcpContentToolError("scope_denied", false, 0), nil, nil
	}
	limit := input.Limit
	policy := h.mcpContentReadPolicy()
	if limit == 0 {
		limit = policy.ListDefault
	}
	if !validMCPContentKind(input.Kind) || limit < 1 || limit > policy.ListMax || len(input.Cursor) > mcpContentMaxCursor {
		return mcpContentToolError("invalid_input", false, 0), nil, nil
	}
	snapshot, cursor, err := h.loadMCPContentSnapshot(ctx, identity, "list", input.Kind, "", input.Cursor, "")
	if err != nil {
		return mcpContentErrorFor(err), nil, nil
	}
	if snapshot == nil {
		return mcpContentSuccess(map[string]any{"schema_version": 1, "kind": input.Kind, "generation": nil, "knowledge_status": "no_published_generation", "items": []mcpContentRef{}, "next_cursor": nil}), nil, nil
	}
	items := make([]mcpContentRef, 0, limit)
	for _, ref := range snapshot.refs {
		if ref.Kind != input.Kind || ref.ID <= cursor.LastID {
			continue
		}
		items = append(items, ref)
		if len(items) == limit {
			break
		}
	}
	var next any
	if len(items) > 0 && hasMCPContentAfter(snapshot.refs, input.Kind, items[len(items)-1].ID) {
		next = encodeMCPContentCursor(mcpContentCursor{SchemaVersion: 1, Operation: "list", Kind: input.Kind, Generation: snapshot.generation, ManifestSHA: snapshot.manifestSHA, ScopeBinding: mcpContentScopeBinding(identity), LastID: items[len(items)-1].ID})
	}
	return mcpContentSuccess(map[string]any{"schema_version": 1, "kind": input.Kind, "generation": snapshot.generation, "items": items, "next_cursor": next}), nil, nil
}

func (h *Handler) callMCPFindContent(ctx context.Context, _ *mcp.CallToolRequest, input findContentInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpContentReadCapability) {
		return mcpContentToolError("scope_denied", false, 0), nil, nil
	}
	name := strings.TrimSpace(input.Name)
	limit := input.Limit
	policy := h.mcpContentReadPolicy()
	if limit == 0 {
		limit = policy.ListDefault
	}
	if !validMCPContentKind(input.Kind) || name == "" || len([]byte(name)) > mcpContentMaxName || limit < 1 || limit > policy.ListMax || len(input.Cursor) > mcpContentMaxCursor {
		return mcpContentToolError("invalid_input", false, 0), nil, nil
	}
	snapshot, cursor, err := h.loadMCPContentSnapshot(ctx, identity, "find", input.Kind, name, input.Cursor, "")
	if err != nil {
		return mcpContentErrorFor(err), nil, nil
	}
	if snapshot == nil {
		return mcpContentSuccess(map[string]any{"schema_version": 1, "kind": input.Kind, "generation": nil, "knowledge_status": "no_published_generation", "status": "not_found", "candidates": []mcpContentRef{}, "next_cursor": nil}), nil, nil
	}
	matches := make([]mcpContentRef, 0)
	for _, ref := range snapshot.refs {
		if ref.Kind == input.Kind && (ref.exactTitle == name || ref.Slug == name) {
			matches = append(matches, ref)
		}
	}
	status := "not_found"
	if len(matches) == 1 {
		status = "unique"
	} else if len(matches) > 1 {
		status = "ambiguous"
	}
	page := make([]mcpContentRef, 0, min(limit, len(matches)))
	for _, ref := range matches {
		if ref.ID > cursor.LastID {
			page = append(page, ref)
			if len(page) == limit {
				break
			}
		}
	}
	var next any
	if len(page) > 0 && page[len(page)-1].ID < matches[len(matches)-1].ID {
		next = encodeMCPContentCursor(mcpContentCursor{SchemaVersion: 1, Operation: "find", Kind: input.Kind, Name: name, Generation: snapshot.generation, ManifestSHA: snapshot.manifestSHA, ScopeBinding: mcpContentScopeBinding(identity), LastID: page[len(page)-1].ID})
	}
	return mcpContentSuccess(map[string]any{"schema_version": 1, "kind": input.Kind, "generation": snapshot.generation, "status": status, "candidates": page, "next_cursor": next}), nil, nil
}

func (h *Handler) callMCPReadContent(ctx context.Context, _ *mcp.CallToolRequest, input readContentInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpContentReadCapability) {
		return mcpContentToolError("scope_denied", false, 0), nil, nil
	}
	maxBytes := input.MaxBytes
	policy := h.mcpContentReadPolicy()
	if maxBytes == 0 {
		maxBytes = policy.ChunkMaxBytes
	}
	if !validMCPContentKind(input.Kind) || len(input.ID) == 0 || len(input.ID) > mcpContentMaxID || maxBytes < 1 || maxBytes > policy.ChunkMaxBytes || len(input.Cursor) > mcpContentMaxCursor || len(input.RefsCursor) > mcpContentMaxCursor {
		return mcpContentToolError("invalid_input", false, 0), nil, nil
	}
	contentGeneration := input.Generation
	var refsCursor mcpContentCursor
	var err error
	if input.RefsCursor != "" {
		refsCursor, err = decodeMCPContentCursor(input.RefsCursor)
		if err != nil || refsCursor.Operation != "refs" || refsCursor.Kind != input.Kind || refsCursor.ScopeBinding != mcpContentScopeBinding(identity) || input.Generation != "" && input.Generation != refsCursor.Generation {
			return mcpContentToolError("invalid_cursor", false, 0), nil, nil
		}
		contentGeneration = refsCursor.Generation
	}
	snapshot, cursor, err := h.loadMCPContentSnapshot(ctx, identity, "read", input.Kind, "", input.Cursor, contentGeneration)
	if err != nil {
		return mcpContentErrorFor(err), nil, nil
	}
	if snapshot == nil {
		return mcpContentToolError("knowledge_unavailable", false, 0), nil, nil
	}
	requestedID := input.ID
	id := input.ID
	if redirected, exists := snapshot.ids.IDRedirects[id]; exists {
		if input.Kind == mcpContentKindConcept {
			if _, active := snapshot.ids.Concept[redirected]; active {
				id = redirected
			}
		} else if _, active := snapshot.ids.Source[redirected]; active {
			id = redirected
		}
	}
	ref, exists := snapshot.byID[input.Kind+"\x00"+id]
	if !exists {
		return mcpContentToolError("not_found", false, 0), nil, nil
	}
	path := "wiki/" + ref.Slug + ".md"
	if input.Kind == mcpContentKindSource {
		path = "wiki/sources/" + ref.Slug + ".md"
	}
	data, _, ok := reserveMCPContentRead(ctx, snapshot.manifest, path)
	if !ok {
		return mcpContentToolError("capacity_limited", true, 250), nil, nil
	}
	data, err = snapshot.client.ReadFileBounded(ctx, path, generation.MaxFileBytes)
	if err != nil {
		return mcpContentErrorFor(err), nil, nil
	}
	revision := "sha256:" + generation.Digest(data)
	start := 0
	if input.Cursor != "" {
		if cursor.Revision != revision || cursor.ID != id {
			return mcpContentToolError("invalid_cursor", false, 0), nil, nil
		}
		start = cursor.Offset
	}
	if start < 0 || start > len(data) || !utf8.Valid(data) || start != len(data) && start > 0 && data[start]&0xc0 == 0x80 {
		return mcpContentToolError("invalid_cursor", false, 0), nil, nil
	}
	end := utf8ChunkEnd(data, start, maxBytes)
	if end < 0 {
		return mcpContentToolError("invalid_range", false, 0), nil, nil
	}
	var next any
	complete := end == len(data)
	if !complete {
		next = encodeMCPContentCursor(mcpContentCursor{SchemaVersion: 1, Operation: "read", Kind: input.Kind, ID: id, Generation: snapshot.generation, ManifestSHA: snapshot.manifestSHA, ScopeBinding: mcpContentScopeBinding(identity), Revision: revision, Offset: end})
	}
	result := map[string]any{
		"schema_version": 1, "ref": ref, "generation": snapshot.generation, "revision": revision,
		"content":           map[string]any{"text": string(data[start:end]), "start_byte": start, "end_byte": end, "total_bytes": len(data), "complete": complete, "next_cursor": next},
		"synthesis_sources": []mcpResolvedRef{}, "mentions": []mcpResolvedRef{}, "provenance_status": "unavailable", "refs_complete": true, "next_refs_cursor": nil,
	}
	if id != requestedID {
		result["requested_id"] = requestedID
	}
	if input.Kind == mcpContentKindSource {
		result["synthesis_sources"] = []mcpResolvedRef{}
		result["provenance_status"] = "available"
	}
	if input.RefsCursor != "" && (refsCursor.Generation != snapshot.generation || refsCursor.ManifestSHA != snapshot.manifestSHA || refsCursor.Revision != revision || refsCursor.ID != id && snapshot.ids.IDRedirects[input.ID] != refsCursor.ID) {
		return mcpContentToolError("invalid_cursor", false, 0), nil, nil
	}
	frontmatter := parseMCPContentFrontmatter(data)
	var synthesisSources []string
	provenanceStatus := "available"
	if !frontmatter.Available {
		provenanceStatus = "unavailable"
	} else if input.Kind == mcpContentKindConcept {
		if frontmatter.HasSources {
			synthesisSources = frontmatter.Sources
		} else if values, exists := snapshot.sourcesByConceptSlug[ref.Slug]; exists {
			synthesisSources = values
		} else {
			provenanceStatus = "unavailable"
		}
	}
	synthesisRefs, mentionRefs, nextRefsCursor, refsComplete, refErr := pageMCPReadReferences(
		ctx, snapshot, identity, ref, revision, data, frontmatter.BodyStart, synthesisSources, refsCursor, input.RefsCursor != "", policy.ReferencePageMax, result,
	)
	if refErr != nil {
		return mcpContentErrorFor(refErr), nil, nil
	}
	result["synthesis_sources"] = synthesisRefs
	result["mentions"] = mentionRefs
	result["provenance_status"] = provenanceStatus
	result["refs_complete"] = refsComplete
	result["next_refs_cursor"] = nextRefsCursor
	return mcpContentSuccess(result), nil, nil
}

func (h *Handler) callMCPReadSourceRaw(ctx context.Context, _ *mcp.CallToolRequest, input readSourceRawInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpContentReadCapability) {
		return mcpContentToolError("scope_denied", false, 0), nil, nil
	}
	maxBytes := input.MaxBytes
	policy := h.mcpContentReadPolicy()
	if maxBytes == 0 {
		maxBytes = policy.ChunkMaxBytes
	}
	if len(input.SourceID) == 0 || len(input.SourceID) > mcpContentMaxID || maxBytes < 1 || maxBytes > policy.ChunkMaxBytes || len(input.Cursor) > mcpContentMaxCursor {
		return mcpContentToolError("invalid_input", false, 0), nil, nil
	}
	snapshot, cursor, err := h.loadMCPContentSnapshot(ctx, identity, "raw", mcpContentKindSource, "", input.Cursor, input.Generation)
	if err != nil {
		return mcpContentErrorFor(err), nil, nil
	}
	if snapshot == nil {
		return mcpContentToolError("knowledge_unavailable", false, 0), nil, nil
	}
	ref, exists := snapshot.byID[mcpContentKindSource+"\x00"+input.SourceID]
	if !exists {
		return mcpContentToolError("not_found", false, 0), nil, nil
	}
	lease := mcpAdmissionLeaseFrom(ctx)
	if lease == nil {
		return mcpContentToolError("capacity_limited", true, 250), nil, nil
	}
	// The pinned generation manifest gives the inventory's exact encoded size.
	// Reserve three copies plus headroom for decoding and row indexes before its
	// body is read; this lets small inventories coexist with one legal large
	// source body while staying within the process working-set cap.
	inventorySize, err := snapshot.client.RetainedSourceSnapshotSize(ctx)
	if err != nil {
		code, retryAfter := mcpSourceSnapshotError(err)
		return mcpContentToolError(code, retryAfter > 0, retryAfter), nil, nil
	}
	_, reserved := lease.ReserveBytes(inventorySize*3 + 128<<10)
	if !reserved {
		return mcpContentToolError("capacity_limited", true, 250), nil, nil
	}
	inventory, err := snapshot.client.ReadSourceSnapshot(ctx)
	if err != nil {
		code, retryAfter := mcpSourceSnapshotError(err)
		return mcpContentToolError(code, retryAfter > 0, retryAfter), nil, nil
	}
	var sourceRow *generation.SourceSnapshotRow
	for i := range inventory.Rows {
		if inventory.Rows[i].StableID == input.SourceID {
			sourceRow = &inventory.Rows[i]
			break
		}
	}
	if sourceRow == nil {
		return mcpContentToolError("raw_mapping_unavailable", false, 0), nil, nil
	}
	meta := snapshot.ids.SourceMeta[input.SourceID]
	if meta.SourceFile != "" && meta.SourceFile != sourceRow.RawPath {
		return mcpContentToolError("raw_mapping_unavailable", false, 0), nil, nil
	}
	if !storage.SafeRawPath(sourceRow.RawPath) {
		return mcpContentToolError("raw_mapping_unavailable", false, 0), nil, nil
	}
	sourceSize, err := snapshot.client.RetainedSourceBytesSize(ctx, inventory, input.SourceID)
	if err != nil {
		code, retryAfter := mcpSourceSnapshotError(err)
		return mcpContentToolError(code, retryAfter > 0, retryAfter), nil, nil
	}
	_, reserved = lease.ReserveBytes(sourceSize + 1<<20)
	if !reserved {
		return mcpContentToolError("capacity_limited", true, 250), nil, nil
	}
	data, err := snapshot.client.ReadSourceBytesFromSnapshot(ctx, inventory, input.SourceID)
	if err != nil {
		code, retryAfter := mcpSourceSnapshotError(err)
		return mcpContentToolError(code, retryAfter > 0, retryAfter), nil, nil
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 || looksLikeBinarySource(data) {
		return mcpContentToolError("raw_representation_unavailable", false, 0), nil, nil
	}
	revision := "sha256:" + generation.Digest(data)
	start := 0
	if input.Cursor != "" {
		if cursor.Revision != revision || cursor.ID != input.SourceID {
			return mcpContentToolError("invalid_cursor", false, 0), nil, nil
		}
		start = cursor.Offset
	}
	if start < 0 || start > len(data) || start != len(data) && start > 0 && data[start]&0xc0 == 0x80 {
		return mcpContentToolError("invalid_cursor", false, 0), nil, nil
	}
	end := utf8ChunkEnd(data, start, maxBytes)
	if end < 0 {
		return mcpContentToolError("invalid_range", false, 0), nil, nil
	}
	var next any
	complete := end == len(data)
	if !complete {
		next = encodeMCPContentCursor(mcpContentCursor{SchemaVersion: 1, Operation: "raw", Kind: mcpContentKindSource, ID: input.SourceID, Generation: snapshot.generation, ManifestSHA: snapshot.manifestSHA, ScopeBinding: mcpContentScopeBinding(identity), Revision: revision, Offset: end})
	}
	return mcpContentSuccess(map[string]any{"schema_version": 1, "source": ref, "generation": snapshot.generation, "revision": revision, "representation": "retained_source_text", "content": map[string]any{"text": string(data[start:end]), "start_byte": start, "end_byte": end, "total_bytes": len(data), "complete": complete, "next_cursor": next}}), nil, nil
}

func (h *Handler) loadMCPContentSnapshot(ctx context.Context, identity mcpProjectKeyIdentity, operation, kind, name, encoded, explicitGeneration string) (*mcpContentSnapshot, mcpContentCursor, error) {
	if h.store == nil {
		return nil, mcpContentCursor{}, errors.New("content_unavailable")
	}
	var cursor mcpContentCursor
	var held []func()
	loaded := false
	defer func() {
		if !loaded {
			for i := len(held) - 1; i >= 0; i-- {
				held[i]()
			}
		}
	}()
	if encoded != "" {
		var err error
		cursor, err = decodeMCPContentCursor(encoded)
		if err != nil || cursor.Operation != operation || cursor.Kind != kind || cursor.ScopeBinding != mcpContentScopeBinding(identity) || cursor.Name != name {
			return nil, mcpContentCursor{}, errors.New("invalid_cursor")
		}
		if explicitGeneration != "" && explicitGeneration != cursor.Generation {
			return nil, mcpContentCursor{}, errors.New("invalid_cursor")
		}
		explicitGeneration = cursor.Generation
	}
	root, ok := h.store.Scope(identity.userID, identity.projectID).(mcpGenerationPinner)
	if !ok {
		return nil, mcpContentCursor{}, errors.New("content_unavailable")
	}
	lease := mcpAdmissionLeaseFrom(ctx)
	if lease == nil {
		return nil, mcpContentCursor{}, errors.New("capacity_limited")
	}
	manifestRelease, reserved := lease.ReserveBytes(int64(generation.MaxManifestBytes) + 1<<20)
	if !reserved {
		return nil, mcpContentCursor{}, errors.New("capacity_limited")
	}
	held = append(held, manifestRelease)
	var client *gcs.Client
	var pinned gcs.GenerationSnapshot
	if explicitGeneration != "" {
		var err error
		client, pinned, err = root.PinGeneration(ctx, explicitGeneration)
		if err != nil {
			return nil, mcpContentCursor{}, errors.New("generation_unavailable")
		}
	} else {
		exists := false
		var err error
		client, pinned, exists, err = root.PinCurrentGenerationIfPublished(ctx)
		if err != nil {
			return nil, mcpContentCursor{}, errors.New("content_unavailable")
		}
		if !exists {
			return nil, mcpContentCursor{}, nil
		}
	}
	genIdentity, err := client.QueryGenerationIdentity(ctx)
	if err != nil {
		return nil, mcpContentCursor{}, errors.New("metadata_unavailable")
	}
	if encoded != "" && (cursor.ManifestSHA != pinned.ManifestSHA256 || cursor.Generation != pinned.Manifest.GenerationID) {
		return nil, mcpContentCursor{}, errors.New("invalid_cursor")
	}
	if cursor.SchemaVersion != 0 && cursor.SchemaVersion != 1 {
		return nil, mcpContentCursor{}, errors.New("invalid_cursor")
	}
	idMapRelease := reserveMCPMetadata(ctx, pinned.Manifest, wikiindex.IDMapPath)
	if idMapRelease == nil {
		return nil, mcpContentCursor{}, errors.New("capacity_limited")
	}
	held = append(held, idMapRelease)
	idBytes, err := client.ReadFileBounded(ctx, wikiindex.IDMapPath, generation.MaxFileBytes)
	if err != nil {
		return nil, mcpContentCursor{}, errors.New("metadata_unavailable")
	}
	ids, err := wikiindex.DecodeIDMap(idBytes)
	if err != nil {
		return nil, mcpContentCursor{}, errors.New("metadata_unavailable")
	}
	conceptsRelease := reserveMCPMetadata(ctx, pinned.Manifest, wikiindex.ConceptsJSONLPath)
	if conceptsRelease == nil {
		return nil, mcpContentCursor{}, errors.New("capacity_limited")
	}
	held = append(held, conceptsRelease)
	conceptsBytes, err := client.ReadFileBounded(ctx, wikiindex.ConceptsJSONLPath, generation.MaxFileBytes)
	if err != nil {
		return nil, mcpContentCursor{}, errors.New("metadata_unavailable")
	}
	conceptPages, err := gcs.WikiPagesFromConceptsJSONL(conceptsBytes)
	if err != nil {
		return nil, mcpContentCursor{}, errors.New("metadata_unavailable")
	}
	snapshot := buildMCPContentSnapshot(client, pinned, genIdentity, ids, conceptPages)
	snapshot.releases = held
	held = nil
	loaded = true
	return snapshot, cursor, nil
}

func buildMCPContentSnapshot(client *gcs.Client, pinned gcs.GenerationSnapshot, identity storage.QueryGenerationIdentity, ids wikiindex.IDMap, concepts []gcs.WikiPage) *mcpContentSnapshot {
	snapshot := &mcpContentSnapshot{client: client, generation: pinned.Manifest.GenerationID, manifestSHA: pinned.ManifestSHA256, manifest: pinned.Manifest, identity: identity, ids: ids, byID: map[string]mcpContentRef{}, indexByID: map[string]int{}, bySlugAndKind: map[string][]mcpContentRef{}, byCanonicalRoute: map[string]mcpContentRef{}, sourcesByConceptSlug: map[string][]string{}}
	titles := make(map[string]gcs.WikiPage, len(concepts))
	for _, page := range concepts {
		titles[page.Slug] = page
		snapshot.sourcesByConceptSlug[page.Slug] = append([]string(nil), page.Sources...)
	}
	for id, slug := range ids.Concept {
		page, exists := titles[slug]
		title, source := slug, "slug"
		if exists && strings.TrimSpace(page.Title) != "" {
			title, source = page.Title, "metadata"
		}
		ref := makeMCPContentRef(id, mcpContentKindConcept, title, slug, source)
		snapshot.refs = append(snapshot.refs, ref)
		snapshot.byID[ref.Kind+"\x00"+ref.ID] = ref
		snapshot.bySlugAndKind[ref.Kind+"\x00"+ref.Slug] = append(snapshot.bySlugAndKind[ref.Kind+"\x00"+ref.Slug], ref)
		snapshot.byCanonicalRoute[ref.Kind+"\x00"+ref.ID+"-"+ref.Slug] = ref
	}
	for id, slug := range ids.Source {
		meta := ids.SourceMeta[id]
		title, source := strings.TrimSpace(meta.Title), "metadata"
		if title == "" {
			title, source = slug, "slug"
		}
		ref := makeMCPContentRef(id, mcpContentKindSource, title, slug, source)
		snapshot.refs = append(snapshot.refs, ref)
		snapshot.byID[ref.Kind+"\x00"+ref.ID] = ref
		snapshot.bySlugAndKind[ref.Kind+"\x00"+ref.Slug] = append(snapshot.bySlugAndKind[ref.Kind+"\x00"+ref.Slug], ref)
		snapshot.byCanonicalRoute[ref.Kind+"\x00"+ref.ID+"-"+ref.Slug] = ref
	}
	for redirect, target := range ids.IDRedirects {
		if ref, ok := snapshot.byID[mcpContentKindConcept+"\x00"+target]; ok {
			snapshot.byCanonicalRoute[mcpContentKindConcept+"\x00"+redirect+"-"+ref.Slug] = ref
		}
		if ref, ok := snapshot.byID[mcpContentKindSource+"\x00"+target]; ok {
			snapshot.byCanonicalRoute[mcpContentKindSource+"\x00"+redirect+"-"+ref.Slug] = ref
		}
	}
	sort.Slice(snapshot.refs, func(i, j int) bool {
		if snapshot.refs[i].Kind == snapshot.refs[j].Kind {
			return snapshot.refs[i].ID < snapshot.refs[j].ID
		}
		return snapshot.refs[i].Kind < snapshot.refs[j].Kind
	})
	for index, ref := range snapshot.refs {
		snapshot.indexByID[ref.Kind+"\x00"+ref.ID] = index
	}
	return snapshot
}

func makeMCPContentRef(id, kind, title, slug, titleSource string) mcpContentRef {
	exactTitle := title
	path := "/" + kind + "s/" + id + "-" + url.PathEscape(slug)
	truncated := len([]byte(title)) > 512
	if truncated {
		for len(title) > 0 && len([]byte(title)) > 509 {
			_, size := utf8.DecodeLastRuneInString(title)
			title = title[:len(title)-size]
		}
		title += "…"
	}
	return mcpContentRef{ID: id, Kind: kind, Title: title, Slug: slug, CitationPath: path, TitleSource: titleSource, TitleTruncated: truncated, exactTitle: exactTitle}
}

func (h *Handler) loadMCPContentSnapshotWithRead(ctx context.Context, identity mcpProjectKeyIdentity, operation, kind, name, encoded, explicitGeneration string) (*mcpContentSnapshot, mcpContentCursor, error) {
	return h.loadMCPContentSnapshot(ctx, identity, operation, kind, name, encoded, explicitGeneration)
}

func reserveMCPContentRead(ctx context.Context, manifest generation.Manifest, path string) ([]byte, func(), bool) {
	file, ok := manifest.File(path)
	if !ok || file.Size < 0 {
		return nil, func() {}, false
	}
	lease := mcpAdmissionLeaseFrom(ctx)
	if lease == nil {
		return nil, func() {}, false
	}
	// ReadFileBounded materializes one exact-size body. Parsing retains only
	// bounded reference state; final encoding is separately wire-limited.
	release, reserved := lease.ReserveBytes(file.Size + 1<<20)
	return nil, release, reserved
}

func reserveMCPMetadata(ctx context.Context, manifest generation.Manifest, path string) func() {
	file, ok := manifest.File(path)
	if !ok || file.Size < 0 || file.Size > generation.MaxFileBytes {
		return nil
	}
	lease := mcpAdmissionLeaseFrom(ctx)
	if lease == nil {
		return nil
	}
	reservation := mcpMetadataWorkingSetReservation(path, file.Size)
	if reservation <= 0 {
		return nil
	}
	release, reserved := lease.ReserveBytes(reservation)
	if !reserved {
		return nil
	}
	return release
}

// mcpMetadataWorkingSetReservation uses the actual decoder's logical bounds
// and the encoded object size to account for retained headers and container
// slots as well as the source bytes. It deliberately estimates working set,
// not RSS or object-size equality.
func mcpMetadataWorkingSetReservation(path string, size int64) int64 {
	if size < 0 || size > generation.MaxFileBytes {
		return 0
	}
	switch path {
	case wikiindex.IDMapPath:
		// Seven IDMap collections each hold at most MaxFiles entries. A string
		// map entry needs at least five encoded bytes ("":""), so size/5 bounds
		// aggregate map entries. Each decoder map keeps both a result and a
		// duplicate-detection map. On 64-bit Go, a string header is 16 bytes and
		// each 8-slot bucket carries key/value headers plus tophash/overflow
		// overhead; SourceMeta values are 48 bytes. 256 bytes per encoded entry
		// covers both maps, map growth/evacuation overlap, and slot slack.
		mapEntries := min(int64(7*generation.MaxFiles), size/5)
		activeRefs := min(int64(2*generation.MaxFiles), size/5)
		redirectItems := size / 2 // an empty JSON string still costs two bytes
		// Each active Concept/Source contributes an mcpContentRef to refs and
		// four indexes (byID, indexByID, bySlugAndKind, byCanonicalRoute). A
		// ref itself is roughly 120 bytes; byID and byCanonicalRoute each retain
		// one ref value, bySlugAndKind retains another slice value, and refs plus
		// its per-slug slice retain ref copies. Compound string keys and bucket
		// slots add more. 640 bytes per active row covers those overlapping
		// values and slice/map growth while size*3 accounts decoded key strings.
		return size*3 + mapEntries*256 + activeRefs*640 + redirectItems*32 + 64<<10
	case wikiindex.ConceptsJSONLPath:
		// WikiPagesFromConceptsJSONL retains at most MaxFiles rows. Its JSON
		// scanner copies each line and json.Unmarshal materializes arbitrary
		// nested frontmatter maps/slices. size/2 bounds decoded JSON nodes after
		// accounting for separators. A sources item can overlap as a 16-byte
		// interface slot, 16-byte string header in the temporary values slice,
		// duplicate-set bucket/key, and headers in normalized and snapshot-copied
		// string slices. 96 bytes per node covers those live slots, nested
		// container headers and ordinary growth slack; size*2 covers line/string
		// backing copies. The per-row allowance covers WikiPage and index state.
		rows := min(int64(generation.MaxFiles), size/12) // shortest nonempty slug row: {"slug":"x"}
		jsonNodes := size / 2
		return size*2 + jsonNodes*96 + rows*256 + 64<<10
	default:
		return 0
	}
}

func (s *mcpContentSnapshot) resolveID(kind, id string) (mcpContentRef, string, bool) {
	if ref, ok := s.byID[kind+"\x00"+id]; ok {
		return ref, id, true
	}
	target, ok := s.ids.IDRedirects[id]
	if !ok {
		return mcpContentRef{}, "", false
	}
	if ref, ok := s.byID[kind+"\x00"+target]; ok {
		return ref, target, true
	}
	return mcpContentRef{}, "", false
}

func validMCPContentKind(kind string) bool {
	return kind == mcpContentKindConcept || kind == mcpContentKindSource
}

func hasMCPContentAfter(refs []mcpContentRef, kind, last string) bool {
	for _, ref := range refs {
		if ref.Kind == kind && ref.ID > last {
			return true
		}
	}
	return false
}

func utf8ChunkEnd(data []byte, start, maxBytes int) int {
	if start == len(data) {
		return start
	}
	end := min(len(data), start+maxBytes)
	for end > start && end < len(data) && data[end]&0xc0 == 0x80 {
		end--
	}
	if end == start {
		return -1
	}
	return end
}

func encodeMCPContentCursor(cursor mcpContentCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeMCPContentCursor(encoded string) (mcpContentCursor, error) {
	if len(encoded) == 0 || len(encoded) > mcpContentMaxCursor {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) > 1536 {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cursor mcpContentCursor
	if decoder.Decode(&cursor) != nil || cursor.SchemaVersion != 1 || cursor.Generation == "" || len(cursor.Generation) > 128 || len(cursor.ManifestSHA) != 64 || len(cursor.ScopeBinding) != 64 || cursor.Offset < 0 || cursor.ReferenceIndex < 0 || cursor.ReferenceIndex > maxMCPContentReferences || cursor.CandidateOffset < 0 || cursor.CandidateOffset > maxMCPContentReferences || len(cursor.Name) > mcpContentMaxName || len(cursor.ID) > mcpContentMaxID || len(cursor.LastID) > mcpContentMaxID {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	if _, err := hex.DecodeString(cursor.ManifestSHA); err != nil {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	if _, err := hex.DecodeString(cursor.ScopeBinding); err != nil {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	if cursor.Revision != "" && !strings.HasPrefix(cursor.Revision, "sha256:") {
		return mcpContentCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func mcpContentScopeBinding(identity mcpProjectKeyIdentity) string {
	hash := sha256.New()
	for _, value := range []string{"mcp-content-scope-v1", identity.userID, identity.projectID} {
		var size [4]byte
		size[0], size[1], size[2], size[3] = byte(len(value)>>24), byte(len(value)>>16), byte(len(value)>>8), byte(len(value))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func mcpContentToolError(code string, retryable bool, retryAfter int) *mcp.CallToolResult {
	message := strings.ReplaceAll(code, "_", " ")
	value := map[string]any{"schema_version": 1, "code": code, "retryable": retryable, "retry_after_ms": retryAfter, "message": message}
	encoded, _ := json.Marshal(value)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}, StructuredContent: value, IsError: true}
}

func mcpContentSuccess(value any) *mcp.CallToolResult {
	encoded, _ := json.Marshal(value)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}, StructuredContent: json.RawMessage(encoded)}
}

func mcpContentErrorFor(err error) *mcp.CallToolResult {
	code := "content_unavailable"
	if err != nil {
		switch err.Error() {
		case "invalid_cursor", "generation_unavailable", "metadata_unavailable", "capacity_limited", "not_found":
			code = err.Error()
		}
	}
	retryAfter := 0
	if code == "content_unavailable" || code == "metadata_unavailable" {
		retryAfter = 1000
	} else if code == "capacity_limited" {
		retryAfter = 250
	}
	return mcpContentToolError(code, code == "content_unavailable" || code == "capacity_limited" || code == "metadata_unavailable", retryAfter)
}

func mcpSourceSnapshotError(err error) (string, int) {
	switch {
	case errors.Is(err, gcs.ErrSourceSnapshotUnavailable):
		return "raw_snapshot_unavailable", 0
	case errors.Is(err, gcs.ErrSourceSnapshotInvalid), errors.Is(err, storage.ErrDeclaredObjectUnavailable):
		return "content_unavailable", 1000
	default:
		return "content_unavailable", 1000
	}
}

func mcpAdmissionLeaseFrom(ctx context.Context) *mcpquota.Lease {
	lease, _ := ctx.Value(mcpAdmissionLeaseContextKey{}).(*mcpquota.Lease)
	return lease
}

func looksLikeBinarySource(data []byte) bool {
	return bytes.HasPrefix(data, []byte("%PDF-")) || bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("\xD0\xCF\x11\xE0"))
}
