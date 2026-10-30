package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func (h *Handler) sourceCommitJournal() sourceautomation.Journal {
	if h == nil || h.firestore == nil || h.firestore.Raw() == nil || h.store == nil {
		return nil
	}
	return sourceautomation.NewJournal(h.firestore.Raw(), h.store)
}

func (h *Handler) prepareSourceCommit(ctx context.Context, ownerID, projectID, entrypoint, path string, data []byte) (sourceautomation.Journal, sourceautomation.Ref, error) {
	digest := sha256.Sum256(data)
	return h.prepareSourceCommitDigest(ctx, ownerID, projectID, entrypoint, path, hex.EncodeToString(digest[:]))
}

func (h *Handler) prepareSourceCommitDigest(ctx context.Context, ownerID, projectID, entrypoint, path, digest string) (sourceautomation.Journal, sourceautomation.Ref, error) {
	journal := h.sourceCommitJournal()
	if journal == nil {
		return nil, sourceautomation.Ref{}, nil
	}
	ref, err := journal.PrepareSourceCommit(ctx, sourceautomation.VerifiedProject{OwnerID: ownerID, ProjectID: projectID}, sourceautomation.PrepareRequest{
		Entrypoint: entrypoint, RawPath: path, ExpectedSHA256: digest,
	})
	return journal, ref, err
}

func confirmSourceCommit(ctx context.Context, journal sourceautomation.Journal, ref sourceautomation.Ref, writer storage.ConditionalWriter, rawPath, statusValue string) (sourceautomation.CommitResult, error) {
	if journal == nil {
		return sourceautomation.CommitResult{State: "committed", ProcessingStatus: "not_started"}, nil
	}
	if writer == nil {
		return sourceautomation.CommitResult{State: "unknown", ProcessingStatus: "unknown"}, sourceautomation.ErrSourceCommitUnconfirmed
	}
	if storage.ValidateRawSyncPath(rawPath) != nil {
		return sourceautomation.CommitResult{State: "unknown", ProcessingStatus: "unknown"}, sourceautomation.ErrInvalidSourceCommit
	}
	_, generation, err := writer.ReadFileWithGeneration(ctx, "raw/"+rawPath)
	if err != nil {
		return sourceautomation.CommitResult{State: "unknown", ProcessingStatus: "unknown"}, err
	}
	result, err := journal.ConfirmSourceCommit(ctx, ref, sourceautomation.ConfirmRequest{Generation: strconv.FormatInt(generation, 10), Status: statusValue})
	if err == nil {
		return result, nil
	}
	reconciled, reconcileErr := journal.ReconcileSourceCommit(ctx, ref)
	if reconcileErr == nil {
		return reconciled, nil
	}
	return sourceautomation.CommitResult{State: "unknown", ProcessingStatus: "unknown"}, err
}
