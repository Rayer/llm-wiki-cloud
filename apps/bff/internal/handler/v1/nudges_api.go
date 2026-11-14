package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

type nudgeAttentionRequest struct {
	AttentionID string `json:"attention_id"`
	Action      string `json:"action"`
}

// GetNudges is persisted-only. It never calls source adapters or models.
func (h *Handler) GetNudges(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	view, err := h.readNudgeView(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// ReconcileNudges is the account-scoped adapter entrypoint. Qualified source
// adapters are installed independently; absent adapters remain explicit and
// cannot be mistaken for recovery or an empty successful view.
func (h *Handler) ReconcileNudges(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	repository := nudge.NewRepository(h.firestore.Raw())
	failures, notices := h.reconcilePipelineFailureNudges(c.Request.Context(), userID, repository)
	view, err := repository.ReadWithPublicationVerifier(c.Request.Context(), userID, failures, notices,
		func(ctx context.Context, ownerID, projectID string, witness nudge.ProducerWitness) (bool, error) {
			identity, missing, err := currentContentContextPublication(ctx, h.store, ownerID, projectID)
			if err != nil {
				return false, err
			}
			return !missing && identity.GenerationID == witness.PublicationGeneration &&
				contentcontext.NormalizeDigest(identity.ConceptsDigest) == contentcontext.NormalizeDigest(witness.ConceptsDigest), nil
		})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	view.Partial = true
	view.Failures = append(view.Failures, nudge.SourceFailure{SafeClass: "release_adapter_not_qualified"})
	c.JSON(http.StatusOK, view)
}

func (h *Handler) readNudgeView(ctx context.Context, userID string) (nudge.View, error) {
	return nudge.NewRepository(h.firestore.Raw()).ReadWithPublicationVerifier(ctx, userID, nil, nil,
		func(ctx context.Context, ownerID, projectID string, witness nudge.ProducerWitness) (bool, error) {
			identity, missing, err := currentContentContextPublication(ctx, h.store, ownerID, projectID)
			if err != nil {
				return false, err
			}
			return !missing && identity.GenerationID == witness.PublicationGeneration &&
				contentcontext.NormalizeDigest(identity.ConceptsDigest) == contentcontext.NormalizeDigest(witness.ConceptsDigest), nil
		})
}

// PatchNudgeAttention updates interaction state only, with an exact revision
// compare-and-swap. Source rows and source freshness fences are untouched.
func (h *Handler) PatchNudgeAttention(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	category := strings.TrimSpace(c.Param("category"))
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var body nudgeAttentionRequest
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
		return
	}
	repo := nudge.NewRepository(h.firestore.Raw())
	err := repo.PatchAttention(c.Request.Context(), userID, category, body.AttentionID, body.Action)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, nudge.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid nudge attention request"})
	case errors.Is(err, nudge.ErrAttentionNotDismissible):
		c.JSON(http.StatusConflict, gin.H{"error": "attention_not_dismissible"})
	case errors.Is(err, nudge.ErrAttentionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "attention_revision_stale"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nudge state unavailable"})
	}
}
