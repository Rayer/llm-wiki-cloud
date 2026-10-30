package documentimport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	cloudstorage "cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

var ErrAcceptanceUnknown = errors.New("document acceptance outcome is unknown")

type ObjectStore interface {
	store.ConditionalWriter
	store.ObjectDeleter
}

type JobStarter interface {
	Start(context.Context, string, string, string, string) (string, error)
}

type Service struct {
	Repository *FirestoreRepository
	Starter    JobStarter
}

func (s *Service) BeginDelete(ctx context.Context, ownerID, projectID, documentID string, revision int64) (Record, error) {
	if s == nil || s.Repository == nil {
		return Record{}, errors.New("document import is unavailable")
	}
	record, err := s.Repository.MarkDeleting(ctx, ownerID, projectID, documentID, revision)
	return record, err
}

func (s *Service) FinishDelete(ctx context.Context, objects ObjectStore, record Record) (Record, error) {
	if s == nil || s.Repository == nil || objects == nil {
		return Record{}, errors.New("document import is unavailable")
	}
	var deleteErr error
	for _, item := range []struct {
		path       string
		generation int64
	}{
		{path: record.OriginalObjectPath, generation: record.OriginalGeneration},
		{path: record.MarkdownObjectPath, generation: record.MarkdownGeneration},
	} {
		if item.path == "" || item.generation <= 0 {
			continue
		}
		err := objects.DeleteFile(ctx, item.path, item.generation)
		if err != nil && !errors.Is(err, store.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
			deleteErr = errors.Join(deleteErr, err)
		}
	}
	finished, err := s.Repository.FinishDeletion(ctx, record.OwnerID, record.ProjectID, record.DocumentID, deleteErr == nil)
	if deleteErr != nil {
		return PublicRecord(finished), errors.Join(deleteErr, err)
	}
	return PublicRecord(finished), err
}

func (s *Service) Accept(ctx context.Context, objects ObjectStore, ownerID, projectID, actorSubmissionID, batchID, filename string, data []byte) (Record, error) {
	if s == nil || s.Repository == nil || objects == nil || ValidateSubmissionID(actorSubmissionID) != nil || batchID != "" && ValidateSubmissionID(batchID) != nil {
		return Record{}, errors.New("document import is unavailable")
	}
	mediaType, digest, err := ValidateUpload(filename, data)
	if err != nil {
		return Record{}, err
	}
	record, _, err := s.Repository.Begin(ctx, Record{
		OwnerID: ownerID, ProjectID: projectID, ClientSubmissionID: actorSubmissionID,
		BatchID: batchID, OriginalFilename: filename, MediaType: mediaType, Bytes: int64(len(data)), SHA256: digest,
		ConverterVersion: ConverterVersion, PipelineRef: nil,
	})
	if err != nil {
		return Record{}, err
	}
	if record.DeletionState == DeletionComplete {
		return PublicRecord(record), nil
	}
	wasReceiving := record.ConversionState == ConversionReceiving
	if !wasReceiving && record.OriginalGeneration <= 0 {
		return PublicRecord(record), nil
	}
	generation := record.OriginalGeneration
	if wasReceiving {
		generation, err = ensureOriginal(ctx, objects, record.OriginalObjectPath, data, digest)
		if err != nil {
			if !errors.Is(err, ErrConflict) {
				return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
			}
			return Record{}, err
		}
		record, err = s.Repository.CompleteAcceptance(ctx, ownerID, projectID, record.DocumentID, generation)
		if err != nil {
			return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
		}
	}
	readback, readGeneration, err := objects.ReadFileWithGeneration(ctx, record.OriginalObjectPath)
	if err != nil || readGeneration != record.OriginalGeneration || int64(len(readback)) != record.Bytes ||
		fmt.Sprintf("%x", sha256.Sum256(readback)) != record.SHA256 {
		if err == nil {
			err = errors.New("original object readback did not match accepted identity")
		}
		return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
	}
	conversionPending := record.ConversionState == ConversionAccepted || record.ConversionState == ConversionQueued || record.ConversionState == ConversionConverting
	sourceRecovery := record.ConversionState == ConversionSucceeded && (record.SourceState == SourceSaving || record.SourceState == SourceUnknown)
	if s.Starter != nil && (sourceRecovery || (conversionPending && record.DispatchState != "started")) {
		executionID, startErr := s.Starter.Start(ctx, ownerID, projectID, record.DocumentID, record.AttemptID)
		if startErr != nil {
			_ = s.Repository.SetDispatchState(ctx, ownerID, projectID, record.DocumentID, "unknown", "")
		} else {
			_ = s.Repository.SetDispatchState(ctx, ownerID, projectID, record.DocumentID, "started", executionID)
		}
		record, err = s.Repository.Get(ctx, ownerID, projectID, record.DocumentID)
		if err != nil {
			return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
		}
	}
	return PublicRecord(record), nil
}

func ensureOriginal(ctx context.Context, objects ObjectStore, objectPath string, data []byte, digest string) (int64, error) {
	verify := func() (int64, error) {
		stored, generation, err := objects.ReadFileWithGeneration(ctx, objectPath)
		if err != nil {
			return 0, err
		}
		if int64(len(stored)) != int64(len(data)) || fmt.Sprintf("%x", sha256.Sum256(stored)) != digest {
			return 0, ErrConflict
		}
		return generation, nil
	}
	if generation, err := verify(); err == nil {
		return generation, nil
	} else if !errors.Is(err, store.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return 0, err
	}
	_, err := objects.WriteFileIfGeneration(ctx, data, objectPath, 0)
	if err != nil && !errors.Is(err, store.ErrGenerationMismatch) {
		return 0, err
	}
	return verify()
}
