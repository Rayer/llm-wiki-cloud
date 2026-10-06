package v1

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
)

const recompileAllDenialByokRequired = "byok_required"

type recompileAllCapabilityResponse struct {
	Allowed    bool   `json:"allowed"`
	DenialCode string `json:"denial_code"`
}

type recompileAllDenialResponse struct {
	Error      string `json:"error"`
	DenialCode string `json:"denial_code"`
}

// RecompileAllCapability reports whether a project can run the explicit
// Recompile all action. BYOK storage is not implemented yet, so capability
// remains fail-closed until that authority exists.
//
//	@Summary		Get Recompile all capability
//	@Description	Returns the project's current Recompile all capability. It is denied until project BYOK is available.
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
	if _, ok := h.authorizeRecompileAllProject(c, ProjectRead); !ok {
		return
	}
	c.JSON(http.StatusOK, recompileAllCapabilityResponse{
		Allowed:    false,
		DenialCode: recompileAllDenialByokRequired,
	})
}

// RecompileAll handles the explicit full-project recompile action. It does
// not reserve pipeline quota or start a worker while BYOK is unavailable.
// The existing incremental pipeline route and admin recovery route are separate.
//
//	@Summary		Recompile all project content
//	@Description	Rejects the full-project recompile before quota or worker admission until project BYOK is available.
//	@Tags			profile
//	@Produce		json
//	@Failure		401	{object}	handler.ErrorResponse
//	@Failure		403	{object}	recompileAllDenialResponse
//	@Failure		404	{object}	handler.ErrorResponse
//	@Failure		500	{object}	handler.ErrorResponse
//	@Security		BearerAuth
//	@Security		ProjectHeader
//	@Router		/api/v1/projects/{pid}/recompile-all [post]
func (h *Handler) RecompileAll(c *gin.Context) {
	if _, ok := h.authorizeRecompileAllProject(c, ProjectEdit); !ok {
		return
	}
	c.JSON(http.StatusForbidden, recompileAllDenialResponse{
		Error:      recompileAllDenialByokRequired,
		DenialCode: recompileAllDenialByokRequired,
	})
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
