package localfs

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

type cancelRawSyncBodyAtEOF struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

func (r *cancelRawSyncBodyAtEOF) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		r.cancel()
	}
	return n, err
}

func TestRawSyncPostRenameInspectErrorIsUncertain(t *testing.T) {
	client := New(t.TempDir()).WithScope("user", "project")
	data := []byte("renamed before the canceled inspection")
	digest := sha256.Sum256(data)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := client.WriteSyncRawFile(ctx, "nested/committed.bin", &cancelRawSyncBodyAtEOF{reader: bytes.NewReader(data), cancel: cancel}, "", hex.EncodeToString(digest[:])); !errors.Is(err, store.ErrRawSyncCommitUncertain) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-rename write error=%v, want uncertain outcome wrapping cancellation", err)
	}

	files, err := client.ListSyncRawFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].Path != "nested/committed.bin" || files[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("committed listing=%#v err=%v", files, err)
	}
	reader, size, err := client.OpenSyncRawFile(context.Background(), files[0].Path, files[0].Generation)
	if err != nil {
		t.Fatal(err)
	}
	readback, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || size != int64(len(data)) || !bytes.Equal(readback, data) {
		t.Fatalf("readback size=%d data=%q read=%v close=%v", size, readback, readErr, closeErr)
	}
}
