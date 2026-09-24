package exportjob

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

type FileSource struct {
	Path           string
	Version        string
	RedactedFields []string
	Open           func(context.Context) (io.ReadCloser, error)
}

// WriteArchive streams source files into a ZIP and appends the export manifest.
// Source bytes are never buffered as a whole; the manifest records a digest of
// each exact byte stream copied into the archive.
func WriteArchive(ctx context.Context, dst io.Writer, meta ExportMeta, files []FileSource) error {
	if dst == nil || meta.FormatVersion != 1 || !validSegment(meta.ExportID) || !validSegment(meta.ProjectID) || !meta.Scope.Valid() || meta.SnapshotAt.IsZero() {
		return errors.New("invalid export metadata")
	}
	ordered := append([]FileSource(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	seen := make(map[string]struct{}, len(ordered))
	for _, file := range ordered {
		if !safeArchivePath(file.Path) || file.Open == nil {
			return fmt.Errorf("invalid archive file path")
		}
		if _, exists := seen[file.Path]; exists {
			return fmt.Errorf("duplicate archive file path")
		}
		seen[file.Path] = struct{}{}
	}

	w := zip.NewWriter(dst)
	manifest := make([]FileDigest, 0, len(ordered))
	redactedFields := make(map[string][]string)
	for _, file := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := file.Open(ctx)
		if err != nil {
			return fmt.Errorf("open archive file: %w", err)
		}
		header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		header.SetModTime(meta.SnapshotAt.UTC())
		entry, err := w.CreateHeader(header)
		if err != nil {
			_ = reader.Close()
			return fmt.Errorf("create archive entry: %w", err)
		}
		hasher := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(entry, hasher), reader)
		closeErr := reader.Close()
		if copyErr != nil {
			return fmt.Errorf("copy archive file: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close archive file: %w", closeErr)
		}
		manifest = append(manifest, FileDigest{Path: file.Path, Size: size, SHA256: hex.EncodeToString(hasher.Sum(nil))})
		if len(file.RedactedFields) > 0 {
			redactedFields[file.Path] = append([]string(nil), file.RedactedFields...)
		}
		if file.Version != "" {
			if meta.SourceVersions == nil {
				meta.SourceVersions = map[string]string{}
			}
			meta.SourceVersions[file.Path] = file.Version
		}
	}
	meta.Files = manifest
	if len(redactedFields) > 0 {
		meta.RedactedFields = redactedFields
	}
	meta.SnapshotAt = meta.SnapshotAt.UTC()
	manifestBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode export metadata: %w", err)
	}
	header := &zip.FileHeader{Name: "export-meta.json", Method: zip.Deflate}
	header.SetModTime(meta.SnapshotAt)
	entry, err := w.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create export metadata entry: %w", err)
	}
	if _, err := entry.Write(manifestBytes); err != nil {
		return fmt.Errorf("write export metadata: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish export archive: %w", err)
	}
	return nil
}

func safeArchivePath(name string) bool {
	if name == "" || name == "export-meta.json" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsRune(name, ':') {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
