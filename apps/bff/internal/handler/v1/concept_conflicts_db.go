package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	cloudstorage "cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	_ "modernc.org/sqlite"
)

const maxConceptConflictStateDBBytes = 64 << 20

type conceptArticleIdentity struct {
	Exists      bool   `json:"exists"`
	Status      string `json:"status,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	ArticleID   string `json:"article_id,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
}

type conceptConflictDatabase struct {
	DBSHA256       string
	Rows           map[string]conceptArticleIdentity
	RunID          string
	NewConflictIDs map[string]struct{}
	MinConfidence  float64
	DB             *sql.DB
}

func readConceptConflictDatabase(ctx context.Context, scoped store.Store) (*conceptConflictDatabase, func(), error) {
	data, err := readConceptFile(ctx, scoped, ".synto/state.db", maxConceptConflictStateDBBytes)
	if err != nil {
		if errors.Is(err, store.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist) {
			return &conceptConflictDatabase{Rows: map[string]conceptArticleIdentity{}, NewConflictIDs: map[string]struct{}{}}, func() {}, nil
		}
		return nil, nil, err
	}
	if len(data) > maxConceptConflictStateDBBytes {
		return nil, nil, errConceptConflictTooLarge
	}
	dir, err := os.MkdirTemp("", "lwc341-state-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "state.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		cleanup()
		return nil, nil, err
	}
	state := &conceptConflictDatabase{
		DBSHA256:       conceptConflictHash(data),
		Rows:           map[string]conceptArticleIdentity{},
		NewConflictIDs: map[string]struct{}{},
		DB:             db,
	}
	if err := state.loadRunState(ctx); err != nil {
		_ = db.Close()
		cleanup()
		return nil, nil, err
	}
	return state, func() {
		_ = db.Close()
		cleanup()
	}, nil
}

func (db *conceptConflictDatabase) loadRunState(ctx context.Context) error {
	if db.DB == nil {
		return nil
	}
	var threshold float64
	var newIDs string
	err := db.DB.QueryRowContext(ctx, "SELECT run_id,new_conflicts_json,min_confidence FROM lwc341_run_state WHERE singleton=1").Scan(&db.RunID, &newIDs, &threshold)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	db.MinConfidence = threshold
	var ids []string
	if json.Unmarshal([]byte(newIDs), &ids) == nil {
		for _, id := range ids {
			db.NewConflictIDs[id] = struct{}{}
		}
	}
	return nil
}

func (db *conceptConflictDatabase) conflictFirstSeenRun(ctx context.Context, conflictID string) (string, bool, error) {
	if db == nil || db.DB == nil {
		return "", false, nil
	}
	var firstSeen, status string
	err := db.DB.QueryRowContext(ctx, "SELECT first_seen_run,status FROM lwc341_conflicts WHERE conflict_id=?", conflictID).Scan(&firstSeen, &status)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return firstSeen, status == "pending", nil
}

func (db *conceptConflictDatabase) articleIdentity(ctx context.Context, path string) (conceptArticleIdentity, error) {
	if db == nil || db.DB == nil {
		return conceptArticleIdentity{Exists: false}, nil
	}
	var identity conceptArticleIdentity
	identity.Exists = true
	err := db.DB.QueryRowContext(ctx,
		"SELECT status,content_hash,COALESCE(article_id,''),COALESCE(entity_id,'') FROM wiki_articles WHERE path=?",
		path,
	).Scan(&identity.Status, &identity.ContentHash, &identity.ArticleID, &identity.EntityID)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		return conceptArticleIdentity{Exists: false}, nil
	}
	if err != nil {
		return conceptArticleIdentity{}, err
	}
	return identity, nil
}

func (db *conceptConflictDatabase) operationResult(ctx context.Context, principalID, operationID, payloadSHA256 string) (map[string]any, bool, error) {
	if db == nil || db.DB == nil {
		return nil, false, nil
	}
	var encoded string
	err := db.DB.QueryRowContext(ctx,
		"SELECT result_json FROM lwc341_operations WHERE principal_id=? AND operation_id=? AND payload_sha256=?",
		principalID, operationID, payloadSHA256,
	).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		encoded = ""
	} else if err != nil {
		return nil, false, err
	}
	if encoded == "" {
		err = db.DB.QueryRowContext(ctx,
			"SELECT result_json FROM lwc341_operation_attempts WHERE principal_id=? AND operation_id=? AND payload_sha256=?",
			principalID, operationID, payloadSHA256,
		).Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	var result map[string]any
	if err := jsonUnmarshalConceptConflict(encoded, &result); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func (db *conceptConflictDatabase) hasOperationID(ctx context.Context, principalID, operationID string) (bool, error) {
	if db == nil || db.DB == nil {
		return false, nil
	}
	var found int
	err := db.DB.QueryRowContext(ctx, `SELECT 1 FROM (
		SELECT operation_id FROM lwc341_operations WHERE principal_id=? AND operation_id=?
		UNION ALL
		SELECT operation_id FROM lwc341_operation_attempts WHERE principal_id=? AND operation_id=?
	) LIMIT 1`, principalID, operationID, principalID, operationID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		return false, nil
	}
	return err == nil, err
}

func (db *conceptConflictDatabase) operationByID(ctx context.Context, principalID, operationID string) (map[string]any, bool, error) {
	if db == nil || db.DB == nil {
		return nil, false, nil
	}
	var encoded string
	err := db.DB.QueryRowContext(ctx, `SELECT result_json FROM (
		SELECT result_json,created_at FROM lwc341_operations WHERE principal_id=? AND operation_id=?
		UNION ALL
		SELECT result_json,created_at FROM lwc341_operation_attempts WHERE principal_id=? AND operation_id=?
	) ORDER BY created_at DESC LIMIT 1`, principalID, operationID, principalID, operationID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) || isConceptConflictMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var result map[string]any
	if err := jsonUnmarshalConceptConflict(encoded, &result); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func isConceptConflictMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func jsonUnmarshalConceptConflict(data string, target any) error {
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return fmt.Errorf("invalid concept conflict receipt")
	}
	return nil
}
