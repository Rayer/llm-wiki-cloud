package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/generation"
)

type localContentContextFenceKey struct{}

type localContentContextFence struct {
	authority *localContentContextAuthority
	lease     *vaultLease
}

type localContentContextAuthority struct {
	root        string
	ownerID     string
	projectID   string
	executionID string
}

func newLocalContentContextAuthority(cfg workerConfig, vault string) *localContentContextAuthority {
	return &localContentContextAuthority{
		root: vault, ownerID: cfg.UserID, projectID: cfg.ProjectID, executionID: cfg.ExecutionID,
	}
}

func (a *localContentContextAuthority) WithProjectFence(ctx context.Context, ownerID, projectID string, operation func(context.Context) error) error {
	if ctx == nil || operation == nil || a == nil || a.root == "" ||
		!auth.ValidPathSegment(ownerID) || !auth.ValidPathSegment(projectID) ||
		ownerID != a.ownerID || projectID != a.projectID || a.executionID == "" {
		return errors.New("invalid local content-context Project fence")
	}
	lease, err := acquireVaultLease(a.root, a.executionID)
	if err != nil {
		return err
	}
	fence := localContentContextFence{authority: a, lease: lease}
	fencedCtx := context.WithValue(ctx, localContentContextFenceKey{}, fence)
	operationErr := operation(fencedCtx)
	leaseErr := validateLocalContentContextLease(ctx, lease, a.executionID)
	releaseErr := lease.Release()
	return errors.Join(operationErr, leaseErr, releaseErr)
}

func (a *localContentContextAuthority) CurrentPublication(ctx context.Context, ownerID, projectID string) (contentcontext.Publication, error) {
	if ctx == nil || a == nil || ownerID != a.ownerID || projectID != a.projectID {
		return contentcontext.Publication{}, errors.New("local content-context publication identity mismatch")
	}
	fence, ok := ctx.Value(localContentContextFenceKey{}).(localContentContextFence)
	if !ok || fence.authority != a || fence.lease == nil {
		return contentcontext.Publication{}, errors.New("local content-context read is not fenced")
	}
	if err := validateLocalContentContextLease(ctx, fence.lease, a.executionID); err != nil {
		return contentcontext.Publication{}, err
	}
	descriptor, err := readLocalContentContextDescriptor(a.root, a.ownerID, a.projectID)
	if err != nil {
		return contentcontext.Publication{}, err
	}
	root, err := os.OpenRoot(a.root)
	if err != nil {
		return contentcontext.Publication{}, err
	}
	concepts, err := readLocalContentContextFile(root, "cache/concepts.jsonl", generation.MaxFileBytes)
	closeErr := root.Close()
	if err != nil || closeErr != nil {
		return contentcontext.Publication{}, errors.Join(err, closeErr)
	}
	if err := contentcontext.ValidateLocalPublicationConcepts(descriptor, concepts); err != nil {
		return contentcontext.Publication{}, fmt.Errorf("validate local content-context input: %w", err)
	}
	if err := validateLocalContentContextLease(ctx, fence.lease, a.executionID); err != nil {
		return contentcontext.Publication{}, err
	}
	return descriptor.Publication(), nil
}

func validateLocalContentContextLease(ctx context.Context, lease *vaultLease, executionID string) error {
	if ctx == nil || lease == nil || lease.root == "" || lease.owner == "" || executionID == "" {
		return errors.New("local content-context publisher lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(lease.root)
	if err != nil {
		return err
	}
	data, err := root.ReadFile(leasePath)
	closeErr := root.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	var record vaultLeaseRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Owner != lease.owner || record.ExecutionID != executionID {
		return errors.New("local content-context publisher lease owner changed")
	}
	return nil
}

func prepareLocalContentContextDescriptor(workspace, vault string, cfg workerConfig) error {
	if !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) || strings.TrimSpace(cfg.ExecutionID) == "" {
		return errors.New("local content-context worker identity is invalid")
	}
	workspaceRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	concepts, err := readLocalContentContextFile(workspaceRoot, "cache/concepts.jsonl", generation.MaxFileBytes)
	if err != nil {
		_ = workspaceRoot.Close()
		return fmt.Errorf("read local concepts for content context: %w", err)
	}
	digest := localConceptsDigest(concepts)
	transactionID := uuid.NewString()
	generationID := "local-" + transactionID
	descriptor := contentcontext.LocalPublicationDescriptor{
		Version: contentcontext.LocalPublicationVersion, OwnerID: cfg.UserID, ProjectID: cfg.ProjectID,
		ExecutionID: cfg.ExecutionID, TransactionID: transactionID, Generation: generationID,
		ConceptsDigest: digest, SummarySourceGeneration: generationID,
		SummarySourceIdentity: contentcontext.LocalSummarySourceIdentity(transactionID, digest),
		SummaryMetadata: contentcontext.SummaryInputMetadata{
			ConceptsDigest: digest, SourceGeneration: generationID,
			SelectionVersion: contentcontext.SummarySelectionVersion,
		},
	}
	if previous, ok := committedLocalContentContextDescriptor(vault, cfg); ok && previous.ConceptsDigest == digest && committedLocalConceptsMatch(vault, cfg, digest) {
		descriptor.SummarySourceGeneration = previous.SummarySourceGeneration
		descriptor.SummarySourceIdentity = previous.SummarySourceIdentity
		descriptor.SummaryInput = append([]byte(nil), previous.SummaryInput...)
		descriptor.SummaryMetadata = previous.SummaryMetadata
		descriptor.InputErrorClass = previous.InputErrorClass
	} else {
		snapshot, snapshotErr := conceptcache.ParseCanonicalJSONLSnapshot(cfg.ProjectID, generationID, concepts)
		if snapshotErr != nil {
			descriptor.InputErrorClass = "input_invalid"
		} else {
			input, metadata, inputErr := contentcontext.BuildSummaryInput(snapshot)
			if metadata.ConceptsDigest == "" {
				metadata.ConceptsDigest = digest
			}
			if metadata.SourceGeneration == "" {
				metadata.SourceGeneration = generationID
			}
			if metadata.SelectionVersion == "" {
				metadata.SelectionVersion = contentcontext.SummarySelectionVersion
			}
			descriptor.SummaryMetadata = metadata
			switch {
			case errors.Is(inputErr, contentcontext.ErrEmptyConcepts):
				descriptor.InputErrorClass = "empty_concepts"
			case inputErr != nil:
				descriptor.InputErrorClass = "input_invalid"
			default:
				descriptor.SummaryInput = input
			}
		}
	}
	encoded, err := contentcontext.EncodeLocalPublicationDescriptor(descriptor)
	if err != nil {
		_ = workspaceRoot.Close()
		return err
	}
	if err := workspaceRoot.MkdirAll(".olw", 0o755); err != nil {
		_ = workspaceRoot.Close()
		return err
	}
	writeErr := atomicRootWrite(workspaceRoot, contentcontext.LocalPublicationDescriptorPath, encoded, 0o600)
	closeErr := workspaceRoot.Close()
	return errors.Join(writeErr, closeErr)
}

func committedLocalContentContextDescriptor(vault string, cfg workerConfig) (contentcontext.LocalPublicationDescriptor, bool) {
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	return descriptor, err == nil
}

// revalidateCommittedLocalContentContext confirms the descriptor and concept
// bytes after the worker reacquires its publisher lease following inference.
func revalidateCommittedLocalContentContext(vault string, cfg workerConfig) error {
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(vault)
	if err != nil {
		return err
	}
	concepts, readErr := readLocalContentContextFile(root, "cache/concepts.jsonl", generation.MaxFileBytes)
	closeErr := root.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	return contentcontext.ValidateLocalPublicationConcepts(descriptor, concepts)
}

// localContentContextRecoveryNeeded reports whether the confirmed local
// publication has not yet been reconciled or still has pending summary work.
// The caller holds the publisher lease while checking; inference happens only
// after that lease is released by the worker entry point.
func localContentContextRecoveryNeeded(ctx context.Context, cfg workerConfig, vault string) (bool, error) {
	if ctx == nil || !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) {
		return false, nil
	}
	descriptor, ok := committedLocalContentContextDescriptor(vault, cfg)
	if !ok || !committedLocalConceptsMatch(vault, cfg, descriptor.ConceptsDigest) {
		return false, nil
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return false, nil
	}
	client, err := newContentContextFirestoreClient(ctx, cloudProject)
	if err != nil {
		return false, err
	}
	if client == nil {
		return false, errors.New("local content-context Firestore client unavailable")
	}
	defer client.Close()
	state, err := contentcontext.NewFirestoreRepository(client).Read(ctx, cfg.UserID, cfg.ProjectID)
	if err != nil {
		return false, err
	}
	publication := descriptor.Publication()
	if state.LatestPublication == nil {
		return true, nil
	}
	latest := state.LatestPublication
	digest, digestOK := contentcontext.NormalizeSHA256(latest.ConceptsSHA256)
	if latest.Generation != publication.Generation || latest.ManifestIdentity != publication.ManifestIdentity ||
		!digestOK || digest != publication.ConceptsDigest {
		return true, nil
	}
	return state.Status == contentcontext.StatusPending, nil
}

func committedLocalConceptsMatch(vault string, cfg workerConfig, expected string) bool {
	root, err := os.OpenRoot(vault)
	if err != nil {
		return false
	}
	data, err := readLocalContentContextFile(root, "cache/concepts.jsonl", generation.MaxFileBytes)
	closeErr := root.Close()
	if err != nil || closeErr != nil || localConceptsDigest(data) != expected {
		return false
	}
	descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
	return err == nil && contentcontext.ValidateLocalPublicationConcepts(descriptor, data) == nil
}

func readLocalContentContextDescriptor(vault, ownerID, projectID string) (contentcontext.LocalPublicationDescriptor, error) {
	root, err := os.OpenRoot(vault)
	if err != nil {
		return contentcontext.LocalPublicationDescriptor{}, err
	}
	data, err := readLocalContentContextFile(root, contentcontext.LocalPublicationDescriptorPath, contentcontext.LocalPublicationMaxBytes)
	closeErr := root.Close()
	if err != nil || closeErr != nil {
		return contentcontext.LocalPublicationDescriptor{}, errors.Join(err, closeErr)
	}
	return contentcontext.DecodeLocalPublicationDescriptor(data, ownerID, projectID)
}

func loadLocalContentContextSummaryInput(ctx context.Context, cfg workerConfig, vault string, authority *localContentContextAuthority, claim contentcontext.SummaryClaim) ([]byte, contentcontext.SummaryInputMetadata, error) {
	var input []byte
	var metadata contentcontext.SummaryInputMetadata
	err := authority.WithProjectFence(ctx, cfg.UserID, cfg.ProjectID, func(fencedCtx context.Context) error {
		publication, err := authority.CurrentPublication(fencedCtx, cfg.UserID, cfg.ProjectID)
		if err != nil {
			return err
		}
		claimDigest, claimDigestOK := contentcontext.NormalizeSHA256(claim.ConceptsSHA256)
		publicationDigest, publicationDigestOK := contentcontext.NormalizeSHA256(publication.ConceptsDigest)
		if !claimDigestOK || !publicationDigestOK || claimDigest != publicationDigest {
			return contentcontext.ErrAttemptRejected
		}
		descriptor, err := readLocalContentContextDescriptor(vault, cfg.UserID, cfg.ProjectID)
		if err != nil {
			return err
		}
		if descriptor.SummarySourceGeneration != claim.SourceGeneration || descriptor.SummarySourceIdentity != claim.SourceManifestIdentity {
			return contentcontext.ErrAttemptRejected
		}
		metadata = descriptor.SummaryMetadata
		if descriptor.InputErrorClass == "empty_concepts" {
			return contentcontext.ErrEmptyConcepts
		}
		if descriptor.InputErrorClass != "" {
			return errors.New("local published summary input is invalid")
		}
		input = append([]byte(nil), descriptor.SummaryInput...)
		return nil
	})
	return input, metadata, err
}

func runLocalContentContext(ctx context.Context, cfg workerConfig, vault string) error {
	if ctx == nil || !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) {
		return errors.New("invalid local content-context worker identity")
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return errors.New("content-context Firestore project identity unavailable")
	}
	client, err := newContentContextFirestoreClient(ctx, cloudProject)
	if err != nil {
		return fmt.Errorf("create local content-context Firestore client: %w", err)
	}
	if client == nil {
		return errors.New("local content-context Firestore client unavailable")
	}
	defer client.Close()
	repository := contentcontext.NewFirestoreRepository(client)
	authority := newLocalContentContextAuthority(cfg, vault)
	claim, err := repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
	if err != nil || claim == nil {
		return err
	}
	inputCtx, cancelInput := context.WithTimeout(ctx, contentContextInputTimeout)
	input, metadata, inputErr := loadLocalContentContextSummaryInput(inputCtx, cfg, vault, authority, *claim)
	cancelInput()
	if errors.Is(inputErr, contentcontext.ErrAttemptRejected) {
		return inputErr
	}
	return runClaimedContentContextSummary(ctx, cfg, repository, authority, *claim, input, metadata, inputErr)
}

func localConceptsDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readLocalContentContextFile(root *os.Root, name string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("invalid local content-context file limit")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("local content-context file is not a bounded regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("local content-context file exceeds size limit")
	}
	return data, nil
}
