package v1

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpPipelineStatusInput struct{}

const mcpPipelineStatusToolDescription = "Returns the Project's Admin and Project auto-pipeline switches, backlog state, latest processing state, source revision, and bounded failure code. Saved or queued content is not necessarily published or searchable."

type mcpPipelineStatusResponse struct {
	Status     string                             `json:"status"`
	Automation *pipelineAutomationProjectSettings `json:"automation,omitempty"`
	Message    string                             `json:"message,omitempty"`
}

var mcpPipelineStatusInputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
var mcpPipelineStatusOutputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"status":{"type":"string","enum":["available","unavailable"]},
		"automation":{"type":"object","properties":{
			"enabled":{"type":"boolean"},"revision":{"type":"integer","minimum":0},
			"admin_allowed":{"type":"boolean"},"effective_enabled":{"type":"boolean"},"blocked_reason":{"type":"string"},
			"backlog_scan_status":{"type":"string"},"pipeline_status":{"type":"string"},
			"source_revision":{"type":"integer","minimum":0},"processed_revision":{"type":"integer","minimum":0},
			"failure_stage":{"type":"string"},"failure_code":{"type":"string"}
		},"required":["enabled","revision","admin_allowed","effective_enabled","backlog_scan_status","pipeline_status","source_revision","processed_revision"],"additionalProperties":false},
		"message":{"type":"string"}
	},
	"required":["status"],"additionalProperties":false
}`)

func (h *Handler) callMCPPipelineStatus(ctx context.Context, _ *mcp.CallToolRequest, _ mcpPipelineStatusInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" || !hasMCPCapability(identity.capabilities, mcpQueryCapability) {
		return mcpPipelineStatusToolResult(mcpPipelineStatusResponse{Status: "unavailable", Message: "The authorized Project is unavailable."}, true), nil, nil
	}
	if h == nil || h.firestore == nil || h.firestore.Raw() == nil {
		return mcpPipelineStatusToolResult(mcpPipelineStatusResponse{Status: "unavailable", Message: "Pipeline status is unavailable."}, true), nil, nil
	}
	settings, err := readPipelineAutomationProject(ctx, h.firestore.Raw(), identity.userID, identity.projectID)
	if err != nil {
		return mcpPipelineStatusToolResult(mcpPipelineStatusResponse{Status: "unavailable", Message: "Pipeline status is unavailable."}, true), nil, nil
	}
	return mcpPipelineStatusToolResult(mcpPipelineStatusResponse{Status: "available", Automation: &settings}, false), nil, nil
}

func mcpPipelineStatusToolResult(response mcpPipelineStatusResponse, isError bool) *mcp.CallToolResult {
	data, _ := json.Marshal(response)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: json.RawMessage(data), IsError: isError}
}
