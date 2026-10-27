package v1

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

type importURLInput struct {
	URL  string   `json:"url"`
	Tags []string `json:"tags,omitempty"`
}

type importURLStorage struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Status string `json:"status"`
}

type importURLResponse struct {
	Status           string            `json:"status"`
	Title            string            `json:"title,omitempty"`
	SourceURL        string            `json:"source_url,omitempty"`
	Storage          *importURLStorage `json:"storage,omitempty"`
	RawURL           string            `json:"raw_url,omitempty"`
	ProcessingStatus string            `json:"processing_status"`
	FailureStage     string            `json:"failure_stage,omitempty"`
	Reason           string            `json:"reason,omitempty"`
}

var importURLInputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"url":{"type":"string","minLength":1,"maxLength":8192,"description":"Public HTTP or HTTPS source URL without credentials."},
		"tags":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":128}}
	},
	"required":["url"],
	"additionalProperties":false
}`)

var importURLOutputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"status":{"type":"string","enum":["stored","rejected","unavailable","conflict","uncertain"]},
		"title":{"type":"string"},
		"source_url":{"type":"string"},
		"storage":{"type":"object","properties":{
			"path":{"type":"string"},
			"sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"},
			"status":{"type":"string","enum":["created","updated","already_exists"]}
		},"required":["path","sha256","status"],"additionalProperties":false},
		"raw_url":{"type":"string","format":"uri"},
		"processing_status":{"type":"string","enum":["not_started"]},
		"failure_stage":{"type":"string","enum":["setup","validation","fetch","size","quality","storage","commit"]},
		"reason":{"type":"string"}
	},
	"required":["status","processing_status"],
	"additionalProperties":false
}`)

const mcpImportURLToolDescription = "Fetches a public URL and imports it into this Project only when the extraction is clearly readable and usable. Truncated, ambiguous, rejected, or unavailable quality checks do not write raw content; retry after a quality service outage. This checks extraction usability, not factual accuracy or Project relevance. A successful save returns the exact raw page link; saving does not start the Project pipeline."

func trustedFrontendOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
}

func (h *Handler) callMCPImportURL(ctx context.Context, _ *mcp.CallToolRequest, input importURLInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" {
		return importURLToolResult(importURLResponse{Status: "unavailable", ProcessingStatus: "not_started", FailureStage: "setup", Reason: "The authorized Project is unavailable; the URL was not imported."}, true), nil, nil
	}
	if !hasMCPCapability(identity.capabilities, mcpQueryCapability) || !hasMCPCapability(identity.capabilities, mcpRawWriteCapability) {
		return importURLToolResult(importURLResponse{Status: "rejected", ProcessingStatus: "not_started", FailureStage: "setup", Reason: "This grant does not include raw write permission; the URL was not imported."}, true), nil, nil
	}
	if h == nil || h.urlIntake == nil {
		return importURLToolResult(importURLResponse{Status: "unavailable", ProcessingStatus: "not_started", FailureStage: "setup", Reason: "URL import is not configured; the URL was not imported."}, true), nil, nil
	}
	if h.mcpFrontendOrigin == "" {
		return importURLToolResult(importURLResponse{Status: "unavailable", ProcessingStatus: "not_started", FailureStage: "setup", Reason: "The trusted Project raw page origin is not configured; the URL was not imported."}, true), nil, nil
	}

	resolveProjectLLMClient := h.projectLLMClient
	if h.projectLLMClientResolver != nil {
		resolveProjectLLMClient = h.projectLLMClientResolver
	}
	projectClient, _, profileErr := resolveProjectLLMClient(ctx, identity.userID, identity.projectID)
	if profileErr != nil {
		return importURLToolResult(importURLResponse{Status: "unavailable", ProcessingStatus: "not_started", FailureStage: "quality", Reason: "The Project LLM source is unavailable, so the URL was not imported."}, true), nil, nil
	}
	if projectClient != nil {
		defer func() {
			if closer, ok := projectClient.(interface{ Close() }); ok {
				closer.Close()
			}
		}()
		ctx = llm.WithProjectChatClient(ctx, projectClient)
	}
	result, err := h.urlIntake.ImportStrict(ctx, urlintake.ActorProject{ActorID: identity.userID, ProjectID: identity.projectID}, urlintake.ImportRequest{URL: input.URL, Tags: input.Tags})
	if err != nil {
		observability.RecordURLIntakeEvent(ctx, "handler", "dependency", "failure")
		return importURLToolResult(importURLResponse{Status: "unavailable", ProcessingStatus: "not_started", FailureStage: "commit", Reason: "The URL import could not be confirmed; check the Project raw content before retrying."}, true), nil, nil
	}

	response := importURLResponse{Status: result.Status, ProcessingStatus: "not_started"}
	if result.Preview != nil {
		response.Title = result.Preview.Title
		response.SourceURL = result.Preview.SourceURL
	}
	if result.Status == "stored" {
		if result.Storage == nil {
			response.Status = "uncertain"
			response.FailureStage = "commit"
			response.Reason = "The commit may have written the article, but the result could not be confirmed; no success link is available. Check the Project before retrying."
			return importURLToolResult(response, true), nil, nil
		}
		rawURL, ok := h.mcpRawURL(identity.projectID, result.Storage.Path)
		if !ok {
			response.Status = "uncertain"
			response.FailureStage = "commit"
			response.Reason = "The commit may have written the article, but its raw page link could not be confirmed; no success link is available. Check the Project before retrying."
			return importURLToolResult(response, true), nil, nil
		}
		storageStatus := result.Storage.Status
		if storageStatus == "replaced" {
			storageStatus = "updated"
		}
		response.Storage = &importURLStorage{Path: result.Storage.Path, SHA256: result.Storage.SHA256, Status: storageStatus}
		response.RawURL = rawURL
		return importURLToolResult(response, false), nil, nil
	}

	response.FailureStage = result.FailureStage
	if response.FailureStage == "" {
		switch response.Status {
		case "rejected":
			response.FailureStage = "validation"
		case "conflict", "uncertain":
			response.FailureStage = "commit"
		default:
			response.FailureStage = "setup"
		}
	}
	response.Reason = result.Message
	if response.Status == "uncertain" && response.FailureStage == "commit" {
		response.Reason = "The commit may have written the article, but the result could not be confirmed. Check the Project before retrying; no success link is available."
	}
	if response.Status != "rejected" && response.Status != "unavailable" && response.Status != "conflict" && response.Status != "uncertain" {
		response.Status = "unavailable"
		response.FailureStage = "setup"
		response.Reason = "The URL import returned an unsupported result; the save was not confirmed."
	}
	return importURLToolResult(response, true), nil, nil
}

func (h *Handler) mcpRawURL(projectID, rawPath string) (string, bool) {
	if h == nil || h.mcpFrontendOrigin == "" || !strings.HasPrefix(rawPath, "raw/") {
		return "", false
	}
	relativePath := strings.TrimPrefix(rawPath, "raw/")
	if relativePath == "" || storage.ValidateRawSyncPath(relativePath) != nil {
		return "", false
	}
	query := url.Values{"file": []string{relativePath}, "project": []string{projectID}}
	return h.mcpFrontendOrigin + "/raw?" + query.Encode(), true
}

func importURLToolResult(response importURLResponse, isError bool) *mcp.CallToolResult {
	data, _ := json.Marshal(response)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: json.RawMessage(data),
		IsError:           isError,
	}
}
