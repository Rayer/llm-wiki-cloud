package documentimport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	FailureStageSourceSave = "source_save"
	conversionLease        = 4 * time.Minute
)

type Processor struct {
	Repository           *FirestoreRepository
	Objects              ObjectStore
	Converter            Converter
	Journal              sourceautomation.Journal
	DependencyLockDigest string
}

func (p *Processor) Process(ctx context.Context, ownerID, projectID, documentID, attemptID string) error {
	if p == nil || p.Repository == nil || p.Objects == nil || p.Converter == nil || p.Journal == nil {
		return errors.New("document import worker is not configured")
	}
	record, leaseToken, claimed, err := p.Repository.Claim(ctx, ownerID, projectID, documentID, attemptID, conversionLease)
	if err != nil || !claimed {
		return err
	}
	if record.ConversionState != ConversionSucceeded {
		if err := p.convertRecord(ctx, record, leaseToken); err != nil {
			var failure *ConversionFailure
			if !errors.As(err, &failure) {
				failure = &ConversionFailure{Class: "conversion_error", MessageKey: "conversion_error", Cause: err}
			}
			_, failErr := p.Repository.FailConversion(ctx, ownerID, projectID, documentID, leaseToken, conversionFailure(failure))
			return errors.Join(err, failErr)
		}
		record, err = p.Repository.Get(ctx, ownerID, projectID, documentID)
		if err != nil {
			return err
		}
	}
	return p.saveSource(ctx, record, leaseToken)
}

func (p *Processor) convertRecord(ctx context.Context, record Record, leaseToken string) error {
	original, generation, err := p.Objects.ReadFileWithGeneration(ctx, record.OriginalObjectPath)
	if err != nil {
		return &ConversionFailure{Class: "original_unavailable", MessageKey: "original_unavailable", Retryable: true, Cause: err}
	}
	if generation != record.OriginalGeneration || int64(len(original)) != record.Bytes || digestBytes(original) != record.SHA256 {
		return &ConversionFailure{Class: "original_changed", MessageKey: "original_changed", Cause: ErrConflict}
	}
	markdown, err := p.Converter.Convert(ctx, record.OriginalFilename, original)
	if err != nil {
		return err
	}
	if len(markdown) == 0 || len(markdown) > MaxMarkdownBytes || len(strings.TrimSpace(string(markdown))) == 0 {
		class := "no_readable_text"
		if len(markdown) > MaxMarkdownBytes {
			class = "resource_limit"
		}
		return &ConversionFailure{Class: class, MessageKey: class, Cause: ErrNoReadableText}
	}
	markdownPath := MarkdownPath(record.DocumentID, record.AttemptID)
	markdownGeneration, err := ensureStoredObject(ctx, p.Objects, markdownPath, markdown)
	if err != nil {
		return &ConversionFailure{Class: "conversion_storage_error", MessageKey: "conversion_storage_error", Retryable: true, Cause: err}
	}
	_, err = p.Repository.CompleteConversion(ctx, record.OwnerID, record.ProjectID, record.DocumentID, leaseToken,
		markdownPath, digestBytes(markdown), p.DependencyLockDigest, markdownGeneration)
	return err
}

func (p *Processor) saveSource(ctx context.Context, record Record, leaseToken string) error {
	markdown, generation, err := p.Objects.ReadFileWithGeneration(ctx, record.MarkdownObjectPath)
	if err != nil || generation != record.MarkdownGeneration || digestBytes(markdown) != record.MarkdownSHA256 {
		return p.finishSourceFailure(ctx, record, leaseToken, "source_content_unavailable", false, errors.Join(err, ErrConflict))
	}
	rawPath := RawSourcePath(record.DocumentID)
	operationID := record.AttemptID
	verifiedProject := sourceautomation.VerifiedProject{OwnerID: record.OwnerID, ProjectID: record.ProjectID}
	ref, err := p.Journal.PrepareSourceCommit(ctx, verifiedProject, sourceautomation.PrepareRequest{
		OperationID:    operationID,
		Entrypoint:     "document_import",
		RawPath:        rawPath,
		ExpectedSHA256: record.MarkdownSHA256,
		BatchID:        record.DocumentID,
	})
	if err != nil {
		return p.finishSourceFailure(ctx, record, leaseToken, "source_prepare_failed", false, err)
	}
	rawGeneration, status, err := ensureRawSource(ctx, p.Objects, rawPath, markdown)
	if err != nil {
		return p.reconcileSourceFailure(ctx, record, leaseToken, ref, err)
	}
	result, err := p.Journal.ConfirmSourceCommit(ctx, ref, sourceautomation.ConfirmRequest{
		Generation: fmt.Sprintf("%d", rawGeneration), Status: status,
	})
	if err != nil {
		return p.reconcileSourceFailure(ctx, record, leaseToken, ref, err)
	}
	if result.State != "committed" {
		return p.finishSourceFailure(ctx, record, leaseToken, "source_commit_unknown", true, sourceautomation.ErrSourceCommitUnconfirmed)
	}
	pipelineStatus := result.ProcessingStatus
	if pipelineStatus != "pending" && pipelineStatus != "preparing" && pipelineStatus != "running" &&
		pipelineStatus != "failed" && pipelineStatus != "published" && pipelineStatus != "unknown" &&
		pipelineStatus != "not_started_disabled" {
		pipelineStatus = "unknown"
	}
	pipelineRef := &PipelineRef{Status: pipelineStatus, SourceRevision: result.SourceRevision}
	sourceRef := &SourceRef{RawPath: rawPath, SHA256: record.MarkdownSHA256, ObjectGeneration: rawGeneration}
	_, err = p.Repository.SetSourceOutcome(ctx, record.OwnerID, record.ProjectID, record.DocumentID, leaseToken,
		SourceSaved, sourceRef, ref.OperationID, pipelineRef, nil)
	return err
}

func (p *Processor) reconcileSourceFailure(ctx context.Context, record Record, leaseToken string, ref sourceautomation.Ref, cause error) error {
	result, err := p.Journal.ReconcileSourceCommit(ctx, ref)
	if err == nil && result.State == "committed" {
		stored, storedGeneration, readErr := p.Objects.ReadFileWithGeneration(ctx, "raw/"+RawSourcePath(record.DocumentID))
		if readErr == nil && digestBytes(stored) == record.MarkdownSHA256 {
			pipelineStatus := result.ProcessingStatus
			if pipelineStatus == "" {
				pipelineStatus = "pending"
			}
			_, setErr := p.Repository.SetSourceOutcome(ctx, record.OwnerID, record.ProjectID, record.DocumentID, leaseToken,
				SourceSaved, &SourceRef{RawPath: RawSourcePath(record.DocumentID), SHA256: record.MarkdownSHA256, ObjectGeneration: storedGeneration},
				ref.OperationID, &PipelineRef{Status: pipelineStatus, SourceRevision: result.SourceRevision}, nil)
			return errors.Join(cause, setErr)
		}
		return p.finishSourceFailure(ctx, record, leaseToken, "source_readback_failed", true, errors.Join(readErr, cause))
	}
	if err == nil && result.State == "aborted" {
		return p.finishSourceFailure(ctx, record, leaseToken, "source_commit_aborted", false, cause)
	}
	return p.finishSourceFailure(ctx, record, leaseToken, "source_commit_unknown", true, errors.Join(err, cause))
}

func (p *Processor) finishSourceFailure(ctx context.Context, record Record, leaseToken, class string, unknown bool, cause error) error {
	state := SourceFailed
	if unknown {
		state = SourceUnknown
	}
	failure := &Failure{Stage: FailureStageSourceSave, Class: class, SafeMessageKey: class, DiagnosticID: diagnosticID(), Retryable: unknown}
	_, setErr := p.Repository.SetSourceOutcome(ctx, record.OwnerID, record.ProjectID, record.DocumentID, leaseToken,
		state, nil, record.AttemptID, nil, failure)
	if cause == nil {
		return setErr
	}
	return errors.Join(errors.New(class), setErr)
}

func ensureStoredObject(ctx context.Context, objects ObjectStore, objectPath string, data []byte) (int64, error) {
	digest := digestBytes(data)
	if existing, generation, err := objects.ReadFileWithGeneration(ctx, objectPath); err == nil {
		if digestBytes(existing) != digest {
			return 0, ErrConflict
		}
		return generation, nil
	} else if !errors.Is(err, store.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return 0, err
	}
	_, err := objects.WriteFileIfGeneration(ctx, data, objectPath, 0)
	if err != nil && !errors.Is(err, store.ErrGenerationMismatch) {
		return 0, err
	}
	stored, generation, err := objects.ReadFileWithGeneration(ctx, objectPath)
	if err != nil {
		return 0, err
	}
	if digestBytes(stored) != digest {
		return 0, ErrConflict
	}
	return generation, nil
}

func ensureRawSource(ctx context.Context, objects ObjectStore, rawPath string, markdown []byte) (int64, string, error) {
	objectPath := "raw/" + rawPath
	digest := digestBytes(markdown)
	if existing, generation, err := objects.ReadFileWithGeneration(ctx, objectPath); err == nil {
		if digestBytes(existing) != digest {
			return 0, "", ErrConflict
		}
		return generation, "already_exists", nil
	} else if !errors.Is(err, store.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return 0, "", err
	}
	_, err := objects.WriteFileIfGeneration(ctx, markdown, objectPath, 0)
	if err != nil && !errors.Is(err, store.ErrGenerationMismatch) {
		return 0, "", err
	}
	alreadyExisted := errors.Is(err, store.ErrGenerationMismatch)
	stored, generation, err := objects.ReadFileWithGeneration(ctx, objectPath)
	if err != nil {
		return 0, "", err
	}
	if digestBytes(stored) != digest {
		return 0, "", ErrConflict
	}
	if alreadyExisted {
		return generation, "already_exists", nil
	}
	return generation, "created", nil
}

func RawSourcePath(documentID string) string {
	return "document-import-" + documentID + ".md"
}

func digestBytes(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
