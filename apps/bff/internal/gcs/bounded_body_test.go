package gcs

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

type observedReader struct {
	reader   io.Reader
	consumed int64
	calls    int
}

func (r *observedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.calls++
	r.consumed += int64(n)
	return n, err
}

type failingReader struct {
	reader *bytes.Reader
	err    error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.reader.Len() == 0 {
		return 0, r.err
	}
	return r.reader.Read(p)
}

func TestReadBoundedBodyUsesActualReaderAtConceptBudget(t *testing.T) {
	const limit = 10 << 20
	prefix := []byte(`{"slug":"alpha","frontmatter":{"id":"detail-boundary"},"padding":"`)
	suffix := []byte(`"}` + "\n")
	padding := limit - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatal("JSONL prefix exceeds limit")
	}
	valid := make([]byte, 0, limit)
	valid = append(valid, prefix...)
	valid = append(valid, strings.Repeat("x", padding)...)
	valid = append(valid, suffix...)
	if len(valid) != limit {
		t.Fatalf("fixture length=%d want=%d", len(valid), limit)
	}
	pages, err := WikiPagesFromConceptsJSONL(valid)
	if err != nil || len(pages) != 1 || pages[0].Slug != "alpha" {
		t.Fatalf("budget-sized prefix is not valid JSONL: pages=%#v err=%v", pages, err)
	}

	exact := &observedReader{reader: bytes.NewReader(valid)}
	got, err := store.ReadBoundedBody(exact, limit)
	if err != nil || !bytes.Equal(got, valid) || exact.consumed != limit {
		t.Fatalf("exact bounded read bytes=%d err=%v consumed=%d", len(got), err, exact.consumed)
	}

	withTail := append(append([]byte(nil), valid...), '\n')
	over := &observedReader{reader: bytes.NewReader(withTail)}
	if got, err := store.ReadBoundedBody(over, limit); !errors.Is(err, store.ErrReadLimitExceeded) || got != nil {
		t.Fatalf("unknown-size over-budget body=%d err=%v; want nil and ErrReadLimitExceeded", len(got), err)
	}
	if over.consumed != limit+1 {
		t.Fatalf("actual Reader consumed=%d want exactly limit+1=%d", over.consumed, limit+1)
	}
	if over.calls < 2 {
		t.Fatalf("counting Reader calls=%d; want actual streaming reads", over.calls)
	}
}

func TestReadBoundedBodyPreservesUnderlyingReadError(t *testing.T) {
	cause := errors.New("controlled body read failure")
	observed := &observedReader{reader: &failingReader{reader: bytes.NewReader([]byte("partial")), err: cause}}
	if got, err := store.ReadBoundedBody(observed, 32); !errors.Is(err, cause) || got != nil {
		t.Fatalf("read-error body=%q err=%v; want nil and original error", got, err)
	}
	if observed.consumed != int64(len("partial")) {
		t.Fatalf("read-error bytes consumed=%d want=%d", observed.consumed, len("partial"))
	}
}
