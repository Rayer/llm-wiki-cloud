package gcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

type rawSyncPostCommitErrorBackend struct {
	*memoryBackend
	err error
}

func (b *rawSyncPostCommitErrorBackend) Write(ctx context.Context, name string, data []byte, contentType string, metadata map[string]string, condition writeCondition) (backendObject, error) {
	object, err := b.memoryBackend.Write(ctx, name, data, contentType, metadata, condition)
	if err != nil {
		return object, err
	}
	return object, b.err
}

func TestRawSyncBackendPostCommitErrorIsUncertain(t *testing.T) {
	client, backend := newMemoryClient()
	cause := errors.New("injected error after backend commit")
	client.backend = &rawSyncPostCommitErrorBackend{memoryBackend: backend, err: cause}
	project := client.WithScope("user", "project")
	data := []byte("committed before the adapter error")
	digest := sha256.Sum256(data)

	if _, err := project.WriteSyncRawFile(context.Background(), "nested/committed.bin", bytes.NewReader(data), "", hex.EncodeToString(digest[:])); !errors.Is(err, store.ErrRawSyncCommitUncertain) || !errors.Is(err, cause) {
		t.Fatalf("post-commit write error=%v, want uncertain outcome wrapping cause", err)
	}

	files, err := project.ListSyncRawFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].Path != "nested/committed.bin" {
		t.Fatalf("committed listing=%#v err=%v", files, err)
	}
	reader, size, err := project.OpenSyncRawFile(context.Background(), files[0].Path, files[0].Generation)
	if err != nil {
		t.Fatal(err)
	}
	readback, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || size != int64(len(data)) || !bytes.Equal(readback, data) {
		t.Fatalf("readback size=%d data=%q read=%v close=%v", size, readback, readErr, closeErr)
	}
}
