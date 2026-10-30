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

func (s *Service) Accept(ctx context.Context, objects ObjectStore, ownerID, projectID, actorSubmissionID, filename string, data []byte) (Record, error) {
	if s == nil || s.Repository == nil || objects == nil || actorSubmissionID == "" {
		return Record{}, errors.New("document import is unavailable")
	}
	mediaType, digest, err := ValidateUpload(filename, data)
	if err != nil {
		return Record{}, err
	}
	record, _, err := s.Repository.Begin(ctx, Record{
		OwnerID: ownerID, ProjectID: projectID, ClientSubmissionID: actorSubmissionID,
		OriginalFilename: filename, MediaType: mediaType, Bytes: int64(len(data)), SHA256: digest,
		ConverterVersion: ConverterVersion, PipelineRef: nil,
	})
	if err != nil {
		return Record{}, err
	}
	if record.DeletionState == DeletionComplete {
		return PublicRecord(record), nil
	}
	if record.ConversionState != ConversionReceiving {
		return PublicRecord(record), nil
	}

	generation, err := ensureOriginal(ctx, objects, record.OriginalObjectPath, data, digest)
	if err != nil {
		if !errors.Is(err, ErrConflict) {
			return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
		}
		return Record{}, err
	}
	accepted, err := s.Repository.CompleteAcceptance(ctx, ownerID, projectID, record.DocumentID, generation)
	if err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
	}
	readback, readGeneration, err := objects.ReadFileWithGeneration(ctx, accepted.OriginalObjectPath)
	if err != nil || readGeneration != accepted.OriginalGeneration || int64(len(readback)) != accepted.Bytes ||
		fmt.Sprintf("%x", sha256.Sum256(readback)) != accepted.SHA256 {
		if err == nil {
			err = errors.New("original object readback did not match accepted identity")
		}
		return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
	}
	if accepted.DispatchState != "started" && s.Starter != nil {
		executionID, startErr := s.Starter.Start(ctx, ownerID, projectID, accepted.DocumentID, accepted.AttemptID)
		if startErr != nil {
			_ = s.Repository.SetDispatchState(ctx, ownerID, projectID, accepted.DocumentID, "unknown", "")
		} else {
			_ = s.Repository.SetDispatchState(ctx, ownerID, projectID, accepted.DocumentID, "started", executionID)
		}
		accepted, err = s.Repository.Get(ctx, ownerID, projectID, accepted.DocumentID)
		if err != nil {
			return Record{}, fmt.Errorf("%w: %w", ErrAcceptanceUnknown, err)
		}
	}
	return PublicRecord(accepted), nil
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
