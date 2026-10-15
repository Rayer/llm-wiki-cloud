package exportjob

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	"cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/observability"
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
	writeErr := runExportPhase(ctx, "export.worker.archive", func(ctx context.Context) error {
		return writeObservedArchive(ctx, io.MultiWriter(writer, uploadChecksum), meta, files)
	})
	closeErr := runUploadClose(ctx, "temporary", writer.Close)
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = tmp.Delete(context.WithoutCancel(ctx))
		return time.Time{}, 0, err
	}
	var tmpAttrs *storage.ObjectAttrs
	if err := runExportPhase(ctx, "export.worker.temp_verify", func(ctx context.Context) error {
		var err error
		tmpAttrs, err = tmp.Attrs(ctx)
		if err != nil {
			return err
		}
		if tmpAttrs.CRC32C != uploadChecksum.Sum32() {
			return errors.New("temporary archive checksum did not match its uploaded bytes")
		}
		return verifyUploadedArchive(ctx, tmp.Generation(tmpAttrs.Generation), tmpAttrs.Size, tmpAttrs.CRC32C)
	}); err != nil {
		return time.Time{}, 0, err
	}
	completedAt := time.Now().UTC()
	if err := runExportPhase(ctx, "export.worker.ready_attributes", func(ctx context.Context) error {
		var err error
		tmpAttrs, err = tmp.Update(ctx, storage.ObjectAttrsToUpdate{CustomTime: completedAt, Metadata: metadata})
		return err
	}); err != nil {
		return time.Time{}, 0, err
	}
	readyChecksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	var ready *storage.Writer
	if err := runExportPhase(ctx, "export.worker.ready_copy", func(ctx context.Context) error {
		tmpReader, err := tmp.Generation(tmpAttrs.Generation).NewReader(ctx)
		if err != nil {
			return err
		}
		ready = bucket.Object(readyName).NewWriter(ctx)
		ready.ContentType = "application/zip"
		ready.ContentDisposition = `attachment; filename="` + formatFilename("project", job.Scope, &snapshotAt, job.ExportID) + `"`
		ready.CustomTime = completedAt
		ready.Metadata = metadata
		_, copyErr := io.Copy(io.MultiWriter(ready, readyChecksum), tmpReader)
		readCloseErr := tmpReader.Close()
		readyCloseErr := runUploadClose(ctx, "ready", ready.Close)
		return errors.Join(copyErr, readCloseErr, readyCloseErr)
	}); err != nil {
		return time.Time{}, 0, err
	}
	var readyAttrs *storage.ObjectAttrs
	if err := runExportPhase(ctx, "export.worker.ready_verify", func(ctx context.Context) error {
		var err error
		readyAttrs, err = bucket.Object(readyName).Attrs(ctx)
		if err != nil {
			return err
		}
		if readyAttrs.Size != tmpAttrs.Size || readyAttrs.CRC32C != tmpAttrs.CRC32C || readyChecksum.Sum32() != tmpAttrs.CRC32C || readyAttrs.CustomTime.IsZero() {
			return errors.New("published export archive failed verification")
		}
		return nil
	}); err != nil {
		return time.Time{}, 0, err
	}
	_ = tmp.Generation(tmpAttrs.Generation).Delete(ctx)
	return readyAttrs.CustomTime.UTC(), readyAttrs.Size, nil
}

func writeObservedArchive(ctx context.Context, dst io.Writer, meta ExportMeta, files []FileSource) error {
	return writeArchive(ctx, dst, meta, files, func(closeZip func() error) error {
		return runExportPhase(ctx, "export.worker.zip_close", func(context.Context) error { return closeZip() })
	})
}

func runUploadClose(ctx context.Context, target string, closeUpload func() error) error {
	_, span := observability.StartExportSpan(ctx, "export.worker.upload_close")
	observability.SetExportUploadTarget(span, target)
	err := closeUpload()
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	observability.SetExportOutcome(span, "export.phase.outcome", outcome)
	observability.FinishExportSpan(span, outcome)
	return err
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
