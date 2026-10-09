package v1

import (
	"cloud.google.com/go/firestore"
	"context"
	"errors"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

// queryStore resolves active once before any current-generation pin.
func (h *Handler) queryStore(c *gin.Context) (storage.Store, *query.ProfileSnapshot, error) {
	store, snapshot, err := h.queryStoreFor(c.Request.Context(), c.GetString("userID"), c.GetString("projectID"))
	if err == nil && store != nil {
		c.Set(requestPinnedStoreKey, store)
	}
	return store, snapshot, err
}

// queryStoreFor is the transport-independent query scope resolver shared by
// HTTP and MCP. It always pins the same active profile generation before the
// executor runs.
func (h *Handler) queryStoreFor(ctx context.Context, userID, projectID string) (storage.Store, *query.ProfileSnapshot, error) {
	if h.profileRepository == nil {
		s, err := h.queryProjectStore(ctx, userID, projectID)
		return s, nil, err
	}
	if userID == "" || projectID == "" {
		return nil, nil, errors.New("query profile scope unavailable")
	}
	state, err := h.profileRepository.GetProfile(ctx, userID, projectID)
	if err != nil {
		return nil, nil, err
	}
	if state.Active == nil {
		s, err := h.queryProjectStore(ctx, userID, projectID)
		return s, nil, err
	}
	active := *state.Active
	candidates, ok := h.profileRepository.(interface {
		GetProfileCandidate(context.Context, string, string, string) (ProfileCandidate, error)
	})
	if !ok {
		return nil, nil, errors.New("historical profile candidate reader unavailable")
	}
	candidate, err := candidates.GetProfileCandidate(ctx, userID, projectID, active.CandidateID)
	if err != nil {
		return nil, nil, err
	}
	if candidate.CandidateID != active.CandidateID || candidate.ContentGeneration != active.ContentGeneration || candidate.Dictionary.Revision != active.DictionaryRevision {
		return nil, nil, errors.New("active historical candidate mismatch")
	}
	ref := candidate.Dictionary
	dictionaryRef := profileartifacts.DerivedRef{Revision: ref.Revision, InputDigest: ref.InputDigest, ModelVersion: ref.ModelVersion, PromptVersion: ref.PromptVersion, SchemaVersion: ref.SchemaVersion}
	requirementOrder := make([]string, 0, len(candidate.Preview.Requirements))
	for _, r := range candidate.Preview.Requirements {
		requirementOrder = append(requirementOrder, r.ID)
	}
	if h.store == nil {
		return nil, nil, errors.New("query storage unavailable")
	}
	scoped := h.store.Scope(userID, projectID)
	pinner, ok := scoped.(query.GenerationPinner)
	if !ok {
		return nil, nil, errors.New("retained generation pin unavailable")
	}
	pinned, manifest, err := pinner.PinQueryGeneration(ctx, active.ContentGeneration)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := query.LoadProfile(ctx, h.cache, pinned, manifest, profiletags.ActiveRef{ContentGeneration: active.ContentGeneration, DictionaryRevision: active.DictionaryRevision, TagSetRevision: active.TagSetRevision, QueryRuleRevision: active.QueryRuleRevision}, dictionaryRef, requirementOrder)
	if err != nil {
		return nil, nil, err
	}
	return pinned, snapshot, nil
}

func (h *Handler) queryProjectStore(ctx context.Context, userID, projectID string) (storage.Store, error) {
	if userID == "" && projectID == "" {
		return h.store, nil
	}
	if userID == "" || projectID == "" {
		return nil, errors.New("incomplete storage request scope")
	}
	if h.store == nil {
		return nil, errWikiStorageNotConfigured
	}
	return pinStore(ctx, h.store.Scope(userID, projectID))
}

// GetProfileCandidate loads the immutable historical candidate named by a
// captured active tuple. It deliberately does not reread the moving state.
func (r *firestoreProfileRepository) GetProfileCandidate(ctx context.Context, userID, projectID, candidateID string) (ProfileCandidate, error) {
	if candidateID == "" || strings.TrimSpace(candidateID) != candidateID || strings.ContainsAny(candidateID, "/\\") || candidateID == "." || candidateID == ".." {
		return ProfileCandidate{}, errors.New("invalid historical candidate ID")
	}
	var candidate ProfileCandidate
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectRead)
		if err != nil {
			return err
		}
		snapshot, err := tx.Get(stateRef.Collection("candidates").Doc(candidateID))
		if err != nil {
			return err
		}
		candidate, err = profileCandidateFromData(snapshot.Data())
		if err != nil {
			return err
		}
		if candidate.CandidateID != candidateID {
			return errors.New("historical candidate identity mismatch")
		}
		return nil
	})
	return candidate, err
}
