package rawintake

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

var (
	ErrConflict        = errors.New("raw file already exists with different content")
	ErrCommitUncertain = errors.New("raw file commit outcome is uncertain")
)

type Status string

const (
	StatusCreated       Status = "created"
	StatusAlreadyExists Status = "already_exists"
	StatusReplaced      Status = "replaced"
)

type Result struct {
	Path   string
	SHA256 string
	Status Status
}

// Destination returns a validated path relative to raw/. It preserves the
// caller's exact directory text instead of cleaning or rewriting it.
func Destination(dir, filename string) (string, error) {
	if filename == "" || strings.Contains(filename, "/") {
		return "", errors.New("invalid raw-relative path")
	}
	if err := store.ValidateRawSyncPath(filename); err != nil {
		return "", err
	}
	if dir != "" {
		if err := store.ValidateRawSyncPath(dir); err != nil {
			return "", err
		}
		filename = dir + "/" + filename
	}
	if err := store.ValidateRawSyncPath(filename); err != nil {
		return "", err
	}
	return filename, nil
}

// Commit stores one file beneath raw/ using generation compare-and-swap. A
// non-precondition write error is uncertain because the backend may have
// committed before returning the error; callers must reconcile before retry.
func Commit(ctx context.Context, writer store.ConditionalWriter, relativePath string, data []byte, overwrite bool) (Result, error) {
	if err := store.ValidateRawSyncPath(relativePath); err != nil {
		return Result{}, err
	}
	if writer == nil {
		return Result{}, errors.New("raw conditional writer is unavailable")
	}
	objectPath := "raw/" + relativePath
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	existing, generation, err := writer.ReadFileWithGeneration(ctx, objectPath)
	status := StatusCreated
	switch {
	case errors.Is(err, storage.ErrObjectNotExist):
		generation = 0
	case err != nil:
		return Result{}, err
	case fmt.Sprintf("%x", sha256.Sum256(existing)) == digest:
		return Result{Path: objectPath, SHA256: digest, Status: StatusAlreadyExists}, nil
	case !overwrite:
		return Result{}, ErrConflict
	default:
		status = StatusReplaced
	}

	if _, err := writer.WriteFileIfGeneration(ctx, data, objectPath, generation); err != nil {
		if errors.Is(err, store.ErrGenerationMismatch) {
			return Result{}, ErrConflict
		}
		return Result{}, fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return Result{Path: objectPath, SHA256: digest, Status: status}, nil
}
