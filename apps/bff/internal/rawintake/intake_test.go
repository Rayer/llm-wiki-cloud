package rawintake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestDestination(t *testing.T) {
	for _, tc := range []struct {
		name, dir, filename, want string
	}{
		{name: "root", filename: "note.md", want: "note.md"},
		{name: "nested unicode", dir: "topic/公園", filename: "記事.md", want: "topic/公園/記事.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Destination(tc.dir, tc.filename)
			if err != nil || got != tc.want {
				t.Fatalf("Destination(%q, %q) = %q, %v; want %q", tc.dir, tc.filename, got, err, tc.want)
			}
		})
	}

	for _, tc := range []struct{ dir, filename string }{
		{dir: "../escape", filename: "note.md"},
		{dir: "/absolute", filename: "note.md"},
		{dir: `folder\\name`, filename: "note.md"},
		{dir: "folder\nname", filename: "note.md"},
		{dir: "folder//name", filename: "note.md"},
		{dir: string(make([]byte, store.MaxRawSyncPathBytes+1)), filename: "note.md"},
		{dir: "folder", filename: "../note.md"},
		{dir: "folder", filename: "nested/note.md"},
		{dir: "folder", filename: "bad\x7fname.md"},
	} {
		if got, err := Destination(tc.dir, tc.filename); err == nil {
			t.Errorf("Destination(%q, %q) = %q, nil error", tc.dir, tc.filename, got)
		}
	}
}

func TestCommitCreateNoopConflictAndOverwrite(t *testing.T) {
	project := localfs.New(t.TempDir()).WithScope("u", "p")
	writer := project
	ctx := context.Background()

	created, err := Commit(ctx, writer, "topic/note.md", []byte("first"), false)
	if err != nil || created.Status != StatusCreated || created.Path != "raw/topic/note.md" {
		t.Fatalf("create = %#v, %v", created, err)
	}
	data, err := project.ReadFile(ctx, created.Path)
	if err != nil || string(data) != "first" {
		t.Fatalf("stored data = %q, %v", data, err)
	}

	unchanged, err := Commit(ctx, writer, "topic/note.md", []byte("first"), false)
	if err != nil || unchanged.Status != StatusAlreadyExists || unchanged.SHA256 != created.SHA256 {
		t.Fatalf("same-content result = %#v, %v", unchanged, err)
	}
	if _, err := Commit(ctx, writer, "topic/note.md", []byte("second"), false); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}

	replaced, err := Commit(ctx, writer, "topic/note.md", []byte("second"), true)
	if err != nil || replaced.Status != StatusReplaced || replaced.SHA256 == created.SHA256 {
		t.Fatalf("overwrite = %#v, %v", replaced, err)
	}
	data, err = project.ReadFile(ctx, replaced.Path)
	if err != nil || string(data) != "second" {
		t.Fatalf("overwritten data = %q, %v", data, err)
	}
}

func TestCommitConcurrentCreateDoesNotOverwriteWinner(t *testing.T) {
	project := localfs.New(t.TempDir()).WithScope("u", "p")
	writer := project
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"first", "second"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			<-start
			_, err := Commit(context.Background(), writer, "same.md", []byte(content), false)
			results <- err
		}(content)
	}
	close(start)
	wg.Wait()
	close(results)

	created, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("concurrent commit error = %v", err)
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("concurrent outcomes: created=%d conflict=%d", created, conflict)
	}
	data, err := project.ReadFile(context.Background(), "raw/same.md")
	if err != nil || (string(data) != "first" && string(data) != "second") {
		t.Fatalf("concurrent winner = %q, %v", data, err)
	}
}

type failingConditionalWriter struct {
	store.ConditionalWriter
	readErr  error
	writeErr error
}

func (w failingConditionalWriter) ReadFileWithGeneration(context.Context, string) ([]byte, int64, error) {
	if w.readErr != nil {
		return nil, 0, w.readErr
	}
	return nil, 0, cloudstorage.ErrObjectNotExist
}

func (w failingConditionalWriter) WriteFileIfGeneration(context.Context, []byte, string, int64) (int64, error) {
	return 0, w.writeErr
}

func TestCommitPreservesReadErrorsAndMarksWriteErrorsUncertain(t *testing.T) {
	readFailure := errors.New("read failed")
	if _, err := Commit(context.Background(), failingConditionalWriter{readErr: readFailure}, "note.md", []byte("x"), false); !errors.Is(err, readFailure) {
		t.Fatalf("read error = %v", err)
	}

	writeFailure := errors.New("write response lost")
	_, err := Commit(context.Background(), failingConditionalWriter{writeErr: writeFailure}, "note.md", []byte("x"), false)
	if !errors.Is(err, ErrCommitUncertain) || !errors.Is(err, writeFailure) {
		t.Fatalf("write error = %v, want uncertain wrapping original", err)
	}
}

type concurrentConditionalWriter struct {
	store.ConditionalWriter
	path string
}

func (w concurrentConditionalWriter) WriteFileIfGeneration(ctx context.Context, data []byte, path string, expected int64) (int64, error) {
	if path != w.path {
		return 0, fmt.Errorf("unexpected path %q", path)
	}
	if _, err := w.ConditionalWriter.WriteFileIfGeneration(ctx, []byte("winner"), path, expected); err != nil {
		return 0, err
	}
	return w.ConditionalWriter.WriteFileIfGeneration(ctx, data, path, expected)
}

func TestCommitConcurrentChangeIsConflict(t *testing.T) {
	project := localfs.New(t.TempDir()).WithScope("u", "p")
	writer := project
	result, err := Commit(context.Background(), concurrentConditionalWriter{ConditionalWriter: writer, path: "raw/note.md"}, "note.md", []byte("loser"), false)
	if !errors.Is(err, ErrConflict) || result.Path != "" {
		t.Fatalf("commit = %#v, %v; want conflict", result, err)
	}
	data, err := project.ReadFile(context.Background(), "raw/note.md")
	if err != nil || string(data) != "winner" {
		t.Fatalf("concurrent winner = %q, %v", data, err)
	}
}
