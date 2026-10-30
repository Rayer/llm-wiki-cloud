package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type contentContextResponse struct {
	ProjectID             string                   `json:"project_id"`
	Status                contentcontext.Status    `json:"status"`
	CurrentGeneration     *string                  `json:"current_generation"`
	CurrentConceptsSHA256 *string                  `json:"current_concepts_sha256"`
	Summary               *contentcontext.LastGood `json:"summary"`
	Stale                 bool                     `json:"stale"`
	ErrorClass            string                   `json:"error_class,omitempty"`
}

func contentContextResponseForState(projectID string, identity storage.QueryGenerationIdentity, state *contentcontext.StateDocument) contentContextResponse {
	response := contentContextResponse{ProjectID: projectID, Status: contentcontext.StatusMissing}
	if identity.GenerationID == "" {
		return response
	}
	digest, ok := contentcontext.NormalizeSHA256(identity.ConceptsDigest)
	if !ok {
		return response
	}
	generation := identity.GenerationID
	response.CurrentGeneration = &generation
	response.CurrentConceptsSHA256 = &digest
	if state == nil {
		return response
	}
	response.Summary = state.LastGood
	if state.LastGood != nil {
		lastGoodDigest, valid := contentcontext.NormalizeSHA256(state.LastGood.ConceptsSHA256)
		response.Stale = !valid || lastGoodDigest != digest
	}
	stateDigest, valid := contentcontext.NormalizeSHA256(state.TargetConceptsSHA)
	if state.LatestPublication != nil && valid && stateDigest == digest {
		response.Status = state.Status
		if response.Status == contentcontext.StatusFailed && safeContentContextErrorClass(state.ErrorClass) {
			response.ErrorClass = state.ErrorClass
		}
	}
	return response
}

// GetContentContext returns persisted content-context work for the current
// pinned publication. It never starts reconciliation or invokes a model.
func (h *Handler) GetContentContext(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	projectID := strings.TrimSpace(c.Param("pid"))
	headerProjectID := strings.TrimSpace(c.GetHeader("X-Project-ID"))
	if !auth.ValidPathSegment(projectID) || !auth.ValidPathSegment(headerProjectID) || projectID != headerProjectID {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "project ID mismatch"})
		return
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), userID, projectID, ProjectRead); err != nil {
		if errors.Is(err, errProfileProjectNotFound) {
			c.JSON(http.StatusNotFound, handler.ErrorResponse{Error: "project not found"})
			return
		}
		writeContentContextUnavailable(c)
		return
	}
	identity, missing, err := currentContentContextPublication(c.Request.Context(), h.store, userID, projectID)
	if err != nil {
		writeContentContextUnavailable(c)
		return
	}
	if missing {
		c.JSON(http.StatusOK, contentContextResponseForState(projectID, storage.QueryGenerationIdentity{}, nil))
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		writeContentContextUnavailable(c)
		return
	}

	stateRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").
		Doc(projectDocID(userID, projectID)).Collection("content_context").Doc("state")
	snapshot, err := stateRef.Get(c.Request.Context())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			c.JSON(http.StatusOK, contentContextResponseForState(projectID, identity, nil))
			return
		}
		writeContentContextUnavailable(c)
		return
	}
	state, err := decodeStoredContentContextState(snapshot.Data())
	if err != nil {
		writeContentContextUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, contentContextResponseForState(projectID, identity, &state))
}

func hasLegacyPinnedView(pinned storage.Store) bool {
	view, ok := pinned.(storage.ViewToken)
	return ok && view.ViewToken() == "legacy"
}

func currentContentContextPublication(ctx context.Context, root storage.RootStore, userID, projectID string) (storage.QueryGenerationIdentity, bool, error) {
	if root == nil {
		return storage.QueryGenerationIdentity{}, false, errors.New("content-context storage unavailable")
	}
	pinned, err := pinStore(ctx, root.Scope(userID, projectID))
	if err != nil {
		return storage.QueryGenerationIdentity{}, false, err
	}
	var identity storage.QueryGenerationIdentity
	if publicationReader, ok := pinned.(contentcontext.PublicationReader); ok {
		publication, err := publicationReader.ContentContextPublication(ctx)
		if errors.Is(err, storage.ErrQueryGenerationUnpinned) && hasLegacyPinnedView(pinned) {
			return storage.QueryGenerationIdentity{}, true, nil
		}
		if err != nil {
			return storage.QueryGenerationIdentity{}, false, err
		}
		identity = storage.QueryGenerationIdentity{
			ProjectID: projectID, GenerationID: publication.Generation,
			ConceptsDigest: publication.ConceptsDigest,
		}
	} else {
		identityProvider, ok := pinned.(storage.QueryGenerationIdentityProvider)
		if !ok {
			return storage.QueryGenerationIdentity{}, false, errors.New("pinned generation identity unavailable")
		}
		var err error
		identity, err = identityProvider.QueryGenerationIdentity(ctx)
		if errors.Is(err, storage.ErrQueryGenerationUnpinned) && hasLegacyPinnedView(pinned) {
			return storage.QueryGenerationIdentity{}, true, nil
		}
		if err != nil {
			return storage.QueryGenerationIdentity{}, false, err
		}
	}
	if identity.ProjectID != projectID || identity.GenerationID == "" {
		return storage.QueryGenerationIdentity{}, false, errors.New("invalid pinned generation identity")
	}
	if _, valid := contentcontext.NormalizeSHA256(identity.ConceptsDigest); !valid {
		return storage.QueryGenerationIdentity{}, false, errors.New("invalid pinned generation digest")
	}
	return identity, false, nil
}

func decodeStoredContentContextState(data map[string]interface{}) (contentcontext.StateDocument, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return contentcontext.StateDocument{}, err
	}
	var state contentcontext.StateDocument
	if err := json.Unmarshal(encoded, &state); err != nil {
		return contentcontext.StateDocument{}, err
	}
	if err := state.Validate(); err != nil {
		return contentcontext.StateDocument{}, err
	}
	return state, nil
}

func safeContentContextErrorClass(value string) bool {
	switch value {
	case "provider_timeout", "invalid_response", "storage_failure", "provider_unavailable", "provider_error", "input_invalid", "lease_expired", "summary_invalid":
		return true
	default:
		return false
	}
}

func writeContentContextUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "content context unavailable"})
}
