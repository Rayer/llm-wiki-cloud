package gcs

import (
	"context"
	"errors"
	"regexp"

	"cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	releaseNotesIndexPath    = "release-notes/v1/index.json"
	releaseNotesIndexLimit   = 1 << 20
	releaseNotesContentLimit = 64 << 10
)

var releaseNotesContentPathPattern = regexp.MustCompile(`^release-notes/v1/versions/(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?/(en|zh-TW)/[0-9a-f]{64}\.json$`)

// ReleaseNotesStore is a fixed root-bucket adapter. It never inherits a
// user/project scope and still honors the selected local-worktree prefix.
type ReleaseNotesStore struct{ client *Client }

func NewReleaseNotesStore(client *Client) *ReleaseNotesStore {
	return &ReleaseNotesStore{client: client}
}

func (s *ReleaseNotesStore) root() (string, error) {
	if s == nil || s.client == nil || (s.client.bucket == nil && s.client.backend == nil) || s.client.userID != "" || s.client.projectID != "" {
		return "", errors.New("release notes root store unavailable")
	}
	return s.client.localScope.ObjectPrefix(), nil
}

func (s *ReleaseNotesStore) ReadIndex(ctx context.Context) ([]byte, int64, error) {
	root, err := s.root()
	if err != nil {
		return nil, 0, err
	}
	object, err := s.client.readObject(ctx, root+releaseNotesIndexPath, 0, releaseNotesIndexLimit)
	if err != nil {
		if objectNotFound(err) {
			return nil, 0, storage.ErrObjectNotExist
		}
		return nil, 0, err
	}
	return object.Data, object.Generation, nil
}

func (s *ReleaseNotesStore) ReadContent(ctx context.Context, objectPath string) ([]byte, error) {
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	if !releaseNotesContentPathPattern.MatchString(objectPath) {
		return nil, errors.New("invalid release notes object path")
	}
	object, err := s.client.readObject(ctx, root+objectPath, 0, releaseNotesContentLimit)
	if err != nil {
		if objectNotFound(err) {
			return nil, storage.ErrObjectNotExist
		}
		return nil, err
	}
	return object.Data, nil
}

// CreateContent is create-only and idempotent for byte-identical content.
func (s *ReleaseNotesStore) CreateContent(ctx context.Context, objectPath string, data []byte) (int64, error) {
	root, err := s.root()
	if err != nil {
		return 0, err
	}
	if !releaseNotesContentPathPattern.MatchString(objectPath) || len(data) == 0 || len(data) > releaseNotesContentLimit {
		return 0, errors.New("invalid release notes content")
	}
	name := root + objectPath
	attrs, err := s.client.writeObject(ctx, name, data, "application/json; charset=utf-8", nil, writeCondition{DoesNotExist: true})
	if err == nil {
		return attrs.Generation, nil
	}
	if !errors.Is(conditionalWriteError(err), store.ErrGenerationMismatch) {
		return 0, conditionalWriteError(err)
	}
	existing, readErr := s.client.readObject(ctx, name, 0, releaseNotesContentLimit)
	if readErr != nil {
		return 0, readErr
	}
	if string(existing.Data) != string(data) {
		return 0, errors.New("release notes immutable content conflict")
	}
	return existing.Generation, nil
}

func (s *ReleaseNotesStore) CompareAndSwapIndex(ctx context.Context, data []byte, expectedGeneration int64) (int64, error) {
	root, err := s.root()
	if err != nil {
		return 0, err
	}
	if len(data) == 0 || len(data) > releaseNotesIndexLimit || expectedGeneration < 0 {
		return 0, errors.New("invalid release notes index")
	}
	attrs, err := s.client.writeObject(ctx, root+releaseNotesIndexPath, data, "application/json; charset=utf-8", nil, createOrGenerationCondition(expectedGeneration))
	if err != nil {
		return 0, conditionalWriteError(err)
	}
	return attrs.Generation, nil
}
