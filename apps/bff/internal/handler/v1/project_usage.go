package v1

import (
	"context"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func (h *Handler) projectLastUsedAt(ctx context.Context, ownerID, projectID string) *string {
	if h == nil || h.firestore == nil || h.firestore.Raw() == nil {
		return nil
	}
	doc, err := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(ownerID + "_" + projectID).Get(ctx)
	if err != nil {
		return nil
	}
	return firestoreTimestampString(doc.Data()["last_used_at"])
}

// recordProjectUse advances the owner+Project activity watermark without
// recreating deleted projects or moving the timestamp backwards.
func (h *Handler) recordProjectUse(ctx context.Context, ownerID, projectID string, acceptedAt time.Time) error {
	if h == nil || h.firestore == nil || h.firestore.Raw() == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(projectID) == "" || acceptedAt.IsZero() {
		return nil
	}
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(ownerID + "_" + projectID)
	acceptedAt = acceptedAt.UTC()
	return h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		snapshot, err := tx.Get(projectRef)
		if err != nil {
			return err
		}
		data := snapshot.Data()
		if data["user_id"] != ownerID || data["project_id"] != projectID || data["status"] != "ready" {
			return nil
		}
		current, ok := data["last_used_at"].(time.Time)
		if ok && !current.Before(acceptedAt) {
			return nil
		}
		return tx.Update(projectRef, []cloudfirestore.Update{{Path: "last_used_at", Value: acceptedAt}})
	})
}

func maxProjectUseTimestamp(existing time.Time, hasExisting bool, acceptedAt time.Time) time.Time {
	acceptedAt = acceptedAt.UTC()
	if hasExisting && !existing.Before(acceptedAt) {
		return existing.UTC()
	}
	return acceptedAt
}
