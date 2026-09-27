package v1

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
)

type profileGuidanceArtifactData struct {
	Revision        string `json:"revision"`
	InputDigest     string `json:"input_digest"`
	ModelVersion    string `json:"model_version"`
	PromptVersion   string `json:"prompt_version"`
	SchemaVersion   string `json:"schema_version"`
	CompileGuidance string `json:"compile_guidance"`
}

type profileGuidanceArtifactResponse struct {
	GuidanceArtifact profileGuidanceArtifactData `json:"guidance_artifact"`
}

// GetProfileGuidanceArtifact reads and validates the exact immutable guidance
// object referenced by the current bootstrap preview or Profile candidate.
//
//	@Summary		Get immutable Profile compile guidance
//	@Description	Returns the exact validated guidance text and version metadata for a current Profile reference.
//	@Tags			profile
//	@Produce		json
//	@Param			pid			path	string	true	"Project ID"
//	@Param			revision	path	string	true	"Immutable guidance artifact revision"
//	@Param			X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Success		200	{object}	profileGuidanceArtifactResponse
//	@Failure		400	{object}	handler.ErrorResponse
//	@Failure		401	{object}	handler.ErrorResponse
//	@Failure		404	{object}	handler.ErrorResponse
//	@Failure		500	{object}	handler.ErrorResponse
//	@Security		DevUserAuth
//	@Security		ProjectHeader
//	@Router		/api/v1/projects/{pid}/profile/guidance/{revision} [get]
func (h *Handler) GetProfileGuidanceArtifact(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	revision := strings.TrimSpace(c.Param("revision"))
	if !isLowerProfileDigest(revision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid Profile guidance revision"})
		return
	}
	state, err := h.profileRepository.GetProfile(c.Request.Context(), userID, projectID)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	if h.store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Profile guidance unavailable"})
		return
	}
	objects, ok := h.store.Scope(userID, projectID).(profileartifacts.ObjectStore)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Profile guidance unavailable"})
		return
	}

	if bootstrap := state.BootstrapGuidance; bootstrap != nil && bootstrap.Revision == revision {
		if bootstrap.ProfileRevision != state.Revision || bootstrap.InputDigest != profileRequirementsDigest(state.Requirements) {
			c.JSON(http.StatusNotFound, gin.H{"error": "guidance artifact not found"})
			return
		}
		ref := profileartifacts.BootstrapGuidanceRef{
			Revision: bootstrap.Revision, ProfileRevision: bootstrap.ProfileRevision, InputDigest: bootstrap.InputDigest,
			ModelVersion: bootstrap.ModelVersion, PromptVersion: bootstrap.PromptVersion, SchemaVersion: bootstrap.SchemaVersion,
		}
		_, envelope, readErr := profileartifacts.ReadBootstrapGuidance(c.Request.Context(), objects, ref)
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Profile guidance unavailable"})
			return
		}
		c.JSON(http.StatusOK, profileGuidanceArtifactResponse{GuidanceArtifact: profileGuidanceArtifactData{
			Revision: revision, InputDigest: envelope.InputDigest, ModelVersion: envelope.ModelVersion,
			PromptVersion: envelope.PromptVersion, SchemaVersion: envelope.SchemaVersion,
			CompileGuidance: envelope.CompileGuidance,
		}})
		return
	}

	if candidate := state.Candidate; candidate != nil && candidate.Guidance.Revision == revision {
		ref := profileartifacts.DerivedRef{
			Revision: candidate.Guidance.Revision, InputDigest: candidate.Guidance.InputDigest,
			ModelVersion: candidate.Guidance.ModelVersion, PromptVersion: candidate.Guidance.PromptVersion,
			SchemaVersion: candidate.Guidance.SchemaVersion,
		}
		envelope, readErr := profileartifacts.ReadGuidance(c.Request.Context(), objects, ref)
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Profile guidance unavailable"})
			return
		}
		c.JSON(http.StatusOK, profileGuidanceArtifactResponse{GuidanceArtifact: profileGuidanceArtifactData{
			Revision: revision, InputDigest: envelope.InputDigest, ModelVersion: envelope.ModelVersion,
			PromptVersion: envelope.PromptVersion, SchemaVersion: envelope.SchemaVersion,
			CompileGuidance: envelope.CompileGuidance,
		}})
		return
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "guidance artifact not found"})
}
