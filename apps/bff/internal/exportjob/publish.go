package exportjob

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	"cloud.google.com/go/storage"
)

func (s *CloudArchiveStore) writeAndPublish(ctx context.Context, userID, projectID string, job Job, snapshotAt time.Time, files []FileSource) (time.Time, int64, error) {
	tmpPrefix, err := TempPrefix(userID, projectID, job.ExportID)
	if err != nil {
		return time.Time{}, 0, err
	}
	readyName, err := ReadyObject(userID, projectID, job.ExportID)
	if err != nil {
		return time.Time{}, 0, err
	}
	metadata := map[string]string{"user_id": userID, "project_id": projectID, "export_id": job.ExportID, "scope": string(job.Scope)}
	bucket := s.client.Bucket(s.bucket)
	tmp := bucket.Object(tmpPrefix + "archive.zip")
	if err := tmp.Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return time.Time{}, 0, err
	}
	writer := tmp.NewWriter(ctx)
	writer.ContentType = "application/zip"
	writer.Metadata = metadata
	meta := ExportMeta{FormatVersion: 1, ExportID: job.ExportID, ProjectID: projectID, SnapshotAt: snapshotAt, Scope: job.Scope}
	uploadChecksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	writeErr := WriteArchive(ctx, io.MultiWriter(writer, uploadChecksum), meta, files)
	closeErr := writer.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = tmp.Delete(context.WithoutCancel(ctx))
		return time.Time{}, 0, err
	}
	tmpAttrs, err := tmp.Attrs(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	if tmpAttrs.CRC32C != uploadChecksum.Sum32() {
		return time.Time{}, 0, errors.New("temporary archive checksum did not match its uploaded bytes")
	}
	if err := verifyUploadedArchive(ctx, tmp.Generation(tmpAttrs.Generation), tmpAttrs.Size, tmpAttrs.CRC32C); err != nil {
		return time.Time{}, 0, err
	}
	completedAt := time.Now().UTC()
	tmpAttrs, err = tmp.Update(ctx, storage.ObjectAttrsToUpdate{CustomTime: completedAt, Metadata: metadata})
	if err != nil {
		return time.Time{}, 0, err
	}
	tmpReader, err := tmp.Generation(tmpAttrs.Generation).NewReader(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	ready := bucket.Object(readyName).NewWriter(ctx)
	ready.ContentType = "application/zip"
	ready.ContentDisposition = `attachment; filename="` + formatFilename("project", job.Scope, &snapshotAt, job.ExportID) + `"`
	ready.CustomTime = completedAt
	ready.Metadata = metadata
	readyChecksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	_, copyErr := io.Copy(io.MultiWriter(ready, readyChecksum), tmpReader)
	readCloseErr := tmpReader.Close()
	readyCloseErr := ready.Close()
	if err := errors.Join(copyErr, readCloseErr, readyCloseErr); err != nil {
		return time.Time{}, 0, err
	}
	readyAttrs, err := bucket.Object(readyName).Attrs(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	if readyAttrs.Size != tmpAttrs.Size || readyAttrs.CRC32C != tmpAttrs.CRC32C || readyChecksum.Sum32() != tmpAttrs.CRC32C || readyAttrs.CustomTime.IsZero() {
		return time.Time{}, 0, errors.New("published export archive failed verification")
	}
	_ = tmp.Generation(tmpAttrs.Generation).Delete(ctx)
	return readyAttrs.CustomTime.UTC(), readyAttrs.Size, nil
}

func verifyUploadedArchive(ctx context.Context, object *storage.ObjectHandle, expectedSize int64, expectedChecksum uint32) error {
	reader, err := object.NewReader(ctx)
	if err != nil {
		return err
	}
	checksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	bytesWritten, copyErr := io.Copy(io.MultiWriter(io.Discard, checksum), reader)
	closeErr := reader.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("verify uploaded export bytes: %w", err)
	}
	if bytesWritten != expectedSize || bytesWritten == 0 || checksum.Sum32() != expectedChecksum {
		return errors.New("uploaded export checksum or size did not match the completed object")
	}
	// WriteArchive only returns after zip.Writer.Close writes the central
	// directory and metadata entry. Reading its exact GCS generation and
	// checking both length and CRC32C avoids buffering the ZIP in memory.
	return nil
}
