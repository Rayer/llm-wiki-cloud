package main

import (
	"fmt"
	"strings"
	"testing"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestValidateRawSyncUnionCountsRetainedFilesAndReplacementBytes(t *testing.T) {
	countBoundary := make(map[string]store.RawSyncFile, store.MaxRawSyncFiles)
	for i := range store.MaxRawSyncFiles {
		name := fmt.Sprintf("remote-%05d.bin", i)
		countBoundary[name] = store.RawSyncFile{Path: name, Size: 0, SHA256: strings.Repeat("0", 64), Generation: "1"}
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "remote-00000.bin", Size: 0}}}, countBoundary); err != nil {
		t.Fatalf("same-path replacement at file-count boundary: %v", err)
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "new-local.bin", Size: 0}}}, countBoundary); err == nil || !strings.Contains(err.Error(), "file-count") {
		t.Fatalf("new local file crossing retained union limit error=%v", err)
	}

	byteBoundary := make(map[string]store.RawSyncFile, 512)
	for i := range 512 {
		name := fmt.Sprintf("remote-%03d.bin", i)
		byteBoundary[name] = store.RawSyncFile{Path: name, Size: 1 << 20, SHA256: strings.Repeat("0", 64), Generation: "1"}
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "remote-000.bin", Size: 0}}}, byteBoundary); err != nil {
		t.Fatalf("same-path shrink at byte boundary: %v", err)
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "new-local.bin", Size: 1}}}, byteBoundary); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("retained remote tree plus new file crossing byte limit error=%v", err)
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "remote-000.bin", Size: store.MaxRawSyncFileBytes}}}, byteBoundary); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("same-path growth crossing byte limit error=%v", err)
	}

	remoteOnly := map[string]store.RawSyncFile{
		"remote-only.bin": {Path: "remote-only.bin", Size: 3, SHA256: strings.Repeat("0", 64), Generation: "1"},
	}
	if err := validateRawSyncUnion([]localRawFile{{RawSyncFile: store.RawSyncFile{Path: "local-only.bin", Size: 4}}}, remoteOnly); err != nil {
		t.Fatalf("small local/remote-only union: %v", err)
	}
}
