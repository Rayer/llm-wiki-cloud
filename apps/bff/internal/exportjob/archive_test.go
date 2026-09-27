package exportjob

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveStreamsFilesAndIncludesManifest(t *testing.T) {
	files := []FileSource{
		{Path: "raw/input.txt", Version: "7", Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("raw body")), nil }},
		{Path: "wiki/.drafts/draft.md", Version: "9", Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("draft body")), nil
		}},
	}
	var archive bytes.Buffer
	snapshot := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	err := WriteArchive(context.Background(), &archive, ExportMeta{FormatVersion: 1, ExportID: "export-a", ProjectID: "project-a", SnapshotAt: snapshot, Scope: ScopeRawFull}, files)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 3 || reader.File[2].Name != "export-meta.json" {
		t.Fatalf("ZIP members = %#v; want two files then export-meta.json", zipMemberNames(reader))
	}
	for i, want := range []string{"raw/input.txt", "wiki/.drafts/draft.md"} {
		body, err := reader.File[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if reader.File[i].Name != want {
			t.Fatalf("ZIP member[%d] = %q, want %q", i, reader.File[i].Name, want)
		}
		if len(data) == 0 {
			t.Fatalf("ZIP member %q is empty", want)
		}
	}
	metaReader, err := reader.File[2].Open()
	if err != nil {
		t.Fatal(err)
	}
	var meta ExportMeta
	err = json.NewDecoder(metaReader).Decode(&meta)
	_ = metaReader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if meta.ExportID != "export-a" || meta.ProjectID != "project-a" || meta.Scope != ScopeRawFull || !meta.SnapshotAt.Equal(snapshot) {
		t.Fatalf("export metadata = %#v", meta)
	}
	if len(meta.Files) != 2 || meta.Files[0].Path != "raw/input.txt" || meta.Files[1].Path != "wiki/.drafts/draft.md" {
		t.Fatalf("manifest files = %#v", meta.Files)
	}
	wantDigest := sha256.Sum256([]byte("raw body"))
	if meta.Files[0].SHA256 != hex.EncodeToString(wantDigest[:]) || meta.Files[0].Size != int64(len("raw body")) {
		t.Fatalf("manifest digest = %#v", meta.Files[0])
	}
}

func TestWriteArchiveRejectsUnsafeOrDuplicatePaths(t *testing.T) {
	for _, paths := range [][]string{{"../secret"}, {".."}, {"C:/secret"}, {"raw/drive:secret"}, {"raw/control\x01"}, {"/absolute"}, {"a\\b"}, {"export-meta.json"}, {"raw/a", "raw/a"}} {
		t.Run(strings.Join(paths, ","), func(t *testing.T) {
			files := make([]FileSource, len(paths))
			for i, name := range paths {
				files[i] = FileSource{Path: name, Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }}
			}
			if err := WriteArchive(context.Background(), io.Discard, ExportMeta{ExportID: "e", ProjectID: "p", SnapshotAt: time.Now(), Scope: ScopeRaw}, files); err == nil {
				t.Fatalf("WriteArchive accepted paths %v", paths)
			}
		})
	}
}

func TestSafeArchivePathRejectsTraversalRootAndControls(t *testing.T) {
	for _, value := range []string{"..", "C:/secrets", "raw/control\x01", "raw/../secret"} {
		if safeArchivePath(value) {
			t.Errorf("safeArchivePath(%q) = true, want false", value)
		}
	}
}

func zipMemberNames(reader *zip.Reader) []string {
	result := make([]string, len(reader.File))
	for i, file := range reader.File {
		result[i] = file.Name
	}
	return result
}
