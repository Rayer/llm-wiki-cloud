package v1

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
)

type recompileAllCapabilityResponse struct {
	Allowed     bool                        `json:"allowed"`
	DenialCode  string                      `json:"denial_code,omitempty"`
	CompileMode pipelinequota.ExecutionMode `json:"compile_mode"`
	Quota       *pipelinequota.Snapshot     `json:"quota,omitempty"`
}

type recompileAllDenialResponse struct {
	Error      string `json:"error"`
	DenialCode string `json:"denial_code"`
}

// RecompileAllCapability reports the project's full-mode pipeline quota.
//
//	@Summary		Get Recompile all capability
//	@Description	Returns the project's current full-mode pipeline capability and quota snapshot.
//	@Tags			profile
//	@Produce		json
//	@Success		200	{object}	recompileAllCapabilityResponse
//	@Failure		401	{object}	handler.ErrorResponse
//	@Failure		404	{object}	handler.ErrorResponse
//	@Failure		500	{object}	handler.ErrorResponse
//	@Security		BearerAuth
//	@Security		ProjectHeader
//	@Router		/api/v1/projects/{pid}/recompile-all/capability [get]
func (h *Handler) RecompileAllCapability(c *gin.Context) {
	project, ok := h.authorizeRecompileAllProject(c, ProjectRead)
	if !ok {
		return
	}
	response := recompileAllCapabilityResponse{CompileMode: pipelinequota.ModeFull}
	if h.localPipeline != nil {
		response.DenialCode = "unsupported_mode"
		c.JSON(http.StatusOK, response)
		return
	}
	snapshot, _, err := h.evaluateQuotaForMode(c.Request.Context(), project.UserID, project.ProjectID, "", pipelinequota.ModeFull)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pipeline status unavailable"})
		return
	}
	response.Allowed = snapshot.Allowed
	response.Quota = &snapshot
	if !snapshot.Allowed {
		response.DenialCode = string(snapshot.Reason)
	}
	c.JSON(http.StatusOK, response)
}

// RecompileAll starts a full clean rebuild through the shared pipeline admission path.
//
//	@Summary		Recompile all project content
//	@Description	Starts a quota-gated clean rebuild from raw sources through the existing pipeline worker.
//	@Tags			profile
//	@Produce		json
//	@Success		202	{object} map[string]any
//	@Failure		401	{object} handler.ErrorResponse
//	@Failure		403	{object}	recompileAllDenialResponse
//	@Failure		404	{object}	handler.ErrorResponse
//	@Failure		409	{object}	handler.ErrorResponse
//	@Failure		429	{object}	handler.ErrorResponse
//	@Failure		500	{object}	handler.ErrorResponse
//	@Security		BearerAuth
//	@Security		ProjectHeader
//	@Router		/api/v1/projects/{pid}/recompile-all [post]
func (h *Handler) RecompileAll(c *gin.Context) {
	project, ok := h.authorizeRecompileAllProject(c, ProjectEdit)
	if !ok {
		return
	}
	if h.localPipeline != nil {
		c.JSON(http.StatusConflict, recompileAllDenialResponse{Error: "full recompile is unavailable in local pipeline mode", DenialCode: "unsupported_mode"})
		return
	}
	h.pipelineRunForMode(c, project.UserID, project.ProjectID, pipelinequota.ModeFull)
}

func (h *Handler) authorizeRecompileAllProject(c *gin.Context, action ProjectAction) (AuthorizedProject, bool) {
	principal := strings.TrimSpace(c.GetString("userID"))
	if principal == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return AuthorizedProject{}, false
	}
	projectID := strings.TrimSpace(c.Param("pid"))
	if projectID == "" {
		projectID = strings.TrimSpace(c.Param("projectID"))
	}
	headerProjectID := strings.TrimSpace(c.GetHeader("X-Project-ID"))
	if !auth.ValidPathSegment(projectID) || !auth.ValidPathSegment(headerProjectID) || headerProjectID != projectID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project ID mismatch"})
		return AuthorizedProject{}, false
	}
	project, err := h.AuthorizeProject(c.Request.Context(), principal, projectID, action)
	if errors.Is(err, errProfileProjectNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return AuthorizedProject{}, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project authorization unavailable"})
		return AuthorizedProject{}, false
	}
	return project, true
}
