package auth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	projectKeyUsageCollection   = "project_key_usage"
	projectKeyUsageSchema       = 1
	projectKeyUsageWriteTimeout = 500 * time.Millisecond
	projectKeyUsageReadBatch    = 100

	projectKeyUsageAvailable   = "available"
	projectKeyUsageNoRecord    = "no_record"
	projectKeyUsageUnavailable = "unavailable"
)

var (
	errProjectKeyUsageUnavailable = errors.New("project key usage unavailable")
	projectKeyUsageAllowedFields  = map[string]struct{}{
		"schema_version": {}, "key_id": {}, "user_id": {}, "project_id": {}, "last_used_at": {},
	}
)

type projectKeyUsageRecord struct {
	SchemaVersion int       `json:"schema_version" firestore:"schema_version"`
	KeyID         string    `json:"key_id" firestore:"key_id"`
	UserID        string    `json:"user_id" firestore:"user_id"`
	ProjectID     string    `json:"project_id" firestore:"project_id"`
	LastUsedAt    time.Time `json:"last_used_at" firestore:"last_used_at"`
}

type projectKeyUsageStatus struct {
	LastUsedAt *time.Time
	Status     string
}

type projectKeyUsageStore interface {
	Record(context.Context, projectKeyUsageRecord) error
	List(context.Context, string, string, []string) (map[string]projectKeyUsageStatus, error)
}

type firestoreProjectKeyUsageStore struct{ fs *firestore.Client }

func (s firestoreProjectKeyUsageStore) collection() *firestore.CollectionRef {
	return scopedfirestore.Collection(s.fs, projectKeyUsageCollection)
}

func (s firestoreProjectKeyUsageStore) Record(ctx context.Context, record projectKeyUsageRecord) error {
	if s.fs == nil || !validProjectKeyUsageRecord(record) {
		return errProjectKeyUsageUnavailable
	}
	ref := s.collection().Doc(record.KeyID)
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return tx.Create(ref, map[string]interface{}{
				"schema_version": record.SchemaVersion,
				"key_id":         record.KeyID,
				"user_id":        record.UserID,
				"project_id":     record.ProjectID,
				"last_used_at":   record.LastUsedAt.UTC(),
			})
		}
		if err != nil {
			return err
		}
		stored, err := validateProjectKeyUsageRecord(snapshot.Ref.ID, snapshot.Data())
		if err != nil || stored.UserID != record.UserID || stored.ProjectID != record.ProjectID {
			return errProjectKeyUsageUnavailable
		}
		if !stored.LastUsedAt.Before(record.LastUsedAt) {
			return nil
		}
		return tx.Update(ref, []firestore.Update{{Path: "last_used_at", Value: record.LastUsedAt.UTC()}})
	})
	if err != nil {
		return errProjectKeyUsageUnavailable
	}
	return nil
}

func (s firestoreProjectKeyUsageStore) List(ctx context.Context, userID, projectID string, keyIDs []string) (map[string]projectKeyUsageStatus, error) {
	if s.fs == nil || !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return nil, errProjectKeyUsageUnavailable
	}
	statuses := make(map[string]projectKeyUsageStatus, len(keyIDs))
	for start := 0; start < len(keyIDs); start += projectKeyUsageReadBatch {
		end := start + projectKeyUsageReadBatch
		if end > len(keyIDs) {
			end = len(keyIDs)
		}
		refs := make([]*firestore.DocumentRef, 0, end-start)
		for _, keyID := range keyIDs[start:end] {
			if !validProjectKeyID(keyID) {
				return nil, errProjectKeyUsageUnavailable
			}
			refs = append(refs, s.collection().Doc(keyID))
		}
		documents, err := s.fs.GetAll(ctx, refs)
		if err != nil {
			return nil, errProjectKeyUsageUnavailable
		}
		for index, document := range documents {
			keyID := keyIDs[start+index]
			if !document.Exists() {
				statuses[keyID] = projectKeyUsageStatus{Status: projectKeyUsageNoRecord}
				continue
			}
			record, err := validateProjectKeyUsageRecord(document.Ref.ID, document.Data())
			if err != nil || record.UserID != userID || record.ProjectID != projectID {
				statuses[keyID] = projectKeyUsageStatus{Status: projectKeyUsageUnavailable}
				continue
			}
			lastUsedAt := record.LastUsedAt.UTC()
			statuses[keyID] = projectKeyUsageStatus{LastUsedAt: &lastUsedAt, Status: projectKeyUsageAvailable}
		}
	}
	return statuses, nil
}

func validProjectKeyUsageRecord(record projectKeyUsageRecord) bool {
	return record.SchemaVersion == projectKeyUsageSchema && validProjectKeyID(record.KeyID) &&
		ValidPathSegment(record.UserID) && ValidPathSegment(record.ProjectID) && !record.LastUsedAt.IsZero()
}

func validateProjectKeyUsageRecord(docID string, data map[string]interface{}) (projectKeyUsageRecord, error) {
	for field := range data {
		if _, ok := projectKeyUsageAllowedFields[field]; !ok {
			return projectKeyUsageRecord{}, errProjectKeyUsageUnavailable
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return projectKeyUsageRecord{}, errProjectKeyUsageUnavailable
	}
	var record projectKeyUsageRecord
	if err := json.Unmarshal(encoded, &record); err != nil || !validProjectKeyUsageRecord(record) || record.KeyID != docID {
		return projectKeyUsageRecord{}, errProjectKeyUsageUnavailable
	}
	return record, nil
}
