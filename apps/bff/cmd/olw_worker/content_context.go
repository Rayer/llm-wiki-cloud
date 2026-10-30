package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
)

var newContentContextGCSClient = gcs.NewClientWithScope
var newContentContextFirestoreClient = newProfileFirestoreClient
var warnContentContextReconcileFailure = func(class string) {
	log.Printf("WARNING project_context reconcile failed stage=project_context class=%s", class)
}

// pinnedSummaryInput loads exactly the archived generation recorded by the
// summary claim. It validates that archive's manifest identity and uses the
// canonical cache reader to verify the JSONL digest before selection.
func pinnedSummaryInput(ctx context.Context, cfg workerConfig, claim contentcontext.SummaryClaim) ([]byte, contentcontext.SummaryInputMetadata, error) {
	if cfg.Bucket == "" || cfg.UserID == "" || cfg.ProjectID == "" || claim.SourceGeneration == "" || claim.SourceManifestIdentity == "" {
		return nil, contentcontext.SummaryInputMetadata{}, errors.New("invalid published summary input identity")
	}
	client, err := newContentContextGCSClient(cfg.Bucket, cfg.LocalCloudScope)
	if err != nil {
		return nil, contentcontext.SummaryInputMetadata{}, fmt.Errorf("create pinned summary reader: %w", err)
	}
	defer client.Close()

	pinned, manifest, err := client.WithScope(cfg.UserID, cfg.ProjectID).PinGeneration(ctx, claim.SourceGeneration)
	if err != nil {
		return nil, contentcontext.SummaryInputMetadata{}, fmt.Errorf("pin published summary source: %w", err)
	}
	if pinned != nil {
		defer pinned.Close()
	}
	if manifest.Manifest.GenerationID != claim.SourceGeneration ||
		contentContextManifestIdentity(manifest.Manifest.GenerationID, manifest.ManifestGeneration, manifest.ManifestSHA256) != claim.SourceManifestIdentity {
		return nil, contentcontext.SummaryInputMetadata{}, errors.New("published summary source manifest changed")
	}
	snapshot, err := conceptcache.New().PinnedCanonicalSnapshot(ctx, pinned)
	if err != nil {
		return nil, contentcontext.SummaryInputMetadata{}, fmt.Errorf("read pinned published concepts: %w", err)
	}
	if snapshot.Identity.ProjectID != cfg.ProjectID || snapshot.Identity.GenerationID != claim.SourceGeneration {
		return nil, contentcontext.SummaryInputMetadata{}, errors.New("pinned published concepts identity mismatch")
	}
	digest, ok := contentcontext.NormalizeSHA256(snapshot.Identity.ConceptsDigest)
	claimDigest, claimDigestOK := contentcontext.NormalizeSHA256(claim.ConceptsSHA256)
	if !ok || !claimDigestOK || digest != claimDigest {
		return nil, contentcontext.SummaryInputMetadata{}, errors.New("pinned published concepts digest mismatch")
	}
	input, metadata, err := contentcontext.BuildSummaryInput(snapshot)
	if err != nil {
		return nil, metadata, err
	}
	return input, metadata, nil
}

func contentContextManifestIdentity(generationID string, manifestGeneration int64, manifestSHA256 string) string {
	if _, ok := contentcontext.NormalizeSHA256(manifestSHA256); generationID == "" || manifestGeneration <= 0 || !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", generationID, manifestGeneration, manifestSHA256)
}

type cloudContentContextAuthority struct {
	cfg     workerConfig
	objects objectStore
}

type cloudContentContextFenceKey struct{}

type cloudContentContextFence struct {
	authority *cloudContentContextAuthority
	lease     *cloudLease
}

func newCloudContentContextAuthority(cfg workerConfig, objects objectStore) *cloudContentContextAuthority {
	return &cloudContentContextAuthority{cfg: cfg, objects: objects}
}

// WithProjectFence reuses the existing owner-liveness cloud publisher lease for
// a short state operation. The summary model call must happen outside this
// method so the publisher lease never becomes the 120-second work lease.
func (a *cloudContentContextAuthority) WithProjectFence(ctx context.Context, ownerID, projectID string, operation func(context.Context) error) error {
	if ctx == nil || operation == nil || a == nil || a.objects == nil ||
		!auth.ValidPathSegment(ownerID) || !auth.ValidPathSegment(projectID) ||
		ownerID != a.cfg.UserID || projectID != a.cfg.ProjectID || a.cfg.Bucket == "" {
		return errors.New("invalid content-context Project fence")
	}
	prefix := workerProjectObjectPrefix(a.cfg)
	lease, err := acquireCloudLease(ctx, a.objects, prefix, a.cfg.ExecutionID)
	if err != nil {
		return err
	}
	fence := cloudContentContextFence{authority: a, lease: lease}
	fencedCtx := context.WithValue(ctx, cloudContentContextFenceKey{}, fence)
	operationErr := operation(fencedCtx)
	leaseErr := validateCloudContentContextLease(ctx, lease, a.cfg.ExecutionID)
	releaseCtx, cancel := cloudFailureRecordingContext(ctx)
	releaseErr := lease.Release(releaseCtx)
	cancel()
	return errors.Join(operationErr, leaseErr, releaseErr)
}

func (a *cloudContentContextAuthority) CurrentPublication(ctx context.Context, ownerID, projectID string) (contentcontext.Publication, error) {
	if ctx == nil || a == nil || a.objects == nil || ownerID != a.cfg.UserID || projectID != a.cfg.ProjectID {
		return contentcontext.Publication{}, errors.New("content-context publication identity mismatch")
	}
	fence, ok := ctx.Value(cloudContentContextFenceKey{}).(cloudContentContextFence)
	if !ok || fence.authority != a || fence.lease == nil {
		return contentcontext.Publication{}, errors.New("content-context manifest read is not fenced")
	}
	if err := validateCloudContentContextLease(ctx, fence.lease, a.cfg.ExecutionID); err != nil {
		return contentcontext.Publication{}, err
	}
	manifestData, attrs, err := a.objects.Read(ctx, workerProjectObjectPrefix(a.cfg)+generation.ManifestPath, 0, generation.MaxManifestBytes)
	if err != nil {
		return contentcontext.Publication{}, fmt.Errorf("read current published manifest: %w", err)
	}
	if attrs.Generation <= 0 {
		return contentcontext.Publication{}, errors.New("current published manifest has no object generation")
	}
	manifest, err := generation.Decode(manifestData)
	if err != nil {
		return contentcontext.Publication{}, fmt.Errorf("decode current published manifest: %w", err)
	}
	conceptsFile, ok := manifest.File("cache/concepts.jsonl")
	if !ok {
		return contentcontext.Publication{}, errors.New("current manifest has no published concepts JSONL")
	}
	digest, ok := contentcontext.NormalizeSHA256(conceptsFile.SHA256)
	if !ok {
		return contentcontext.Publication{}, errors.New("current manifest has an invalid concepts digest")
	}
	publication := contentcontext.Publication{
		Generation: manifest.GenerationID, ConceptsDigest: digest,
		ManifestIdentity: contentContextManifestIdentity(manifest.GenerationID, attrs.Generation, generation.Digest(manifestData)),
	}
	if strings.TrimSpace(publication.ManifestIdentity) == "" {
		return contentcontext.Publication{}, errors.New("current manifest identity is invalid")
	}
	if err := validateCloudContentContextLease(ctx, fence.lease, a.cfg.ExecutionID); err != nil {
		return contentcontext.Publication{}, err
	}
	return publication, nil
}

func validateCloudContentContextLease(ctx context.Context, lease *cloudLease, expectedExecution string) error {
	if lease == nil || lease.store == nil || lease.name == "" || lease.generation <= 0 || expectedExecution == "" {
		return errors.New("content-context publisher lease is unavailable")
	}
	data, attrs, err := lease.store.Read(ctx, lease.name, lease.generation, 64<<10)
	if err != nil {
		return fmt.Errorf("read content-context publisher lease: %w", err)
	}
	if attrs.Generation != lease.generation {
		return errors.New("content-context publisher lease generation changed")
	}
	payload, err := parseLeasePayload(data)
	if err != nil || payload.Execution != expectedExecution {
		return errors.New("content-context publisher lease owner changed")
	}
	return nil
}

// reconcileCloudContentContext records only a manifest that the existing
// owner-liveness publisher fence can prove current. Errors are reported to the
// worker warning channel and never change the knowledge publication result.
func reconcileCloudContentContext(ctx context.Context, cfg workerConfig, objects objectStore) error {
	if ctx == nil || objects == nil || cfg.Bucket == "" || !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) {
		return errors.New("invalid content-context worker identity")
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return errors.New("content-context Firestore project identity unavailable")
	}
	client, err := newContentContextFirestoreClient(ctx, cloudProject)
	if err != nil {
		return fmt.Errorf("create content-context Firestore client: %w", err)
	}
	if client == nil {
		return errors.New("content-context Firestore client unavailable")
	}
	defer client.Close()
	repository := contentcontext.NewFirestoreRepository(client)
	authority := newCloudContentContextAuthority(cfg, objects)
	_, err = repository.ReconcileCurrent(ctx, cfg.UserID, cfg.ProjectID, authority)
	if isObjectNotFound(err) {
		return nil
	}
	return err
}

func reportContentContextReconcileFailure(err error) {
	if err == nil {
		return
	}
	class := "storage_failure"
	if errors.Is(err, contentcontext.ErrPublicationChanged) {
		class = "publication_changed"
	} else if errors.Is(err, errObjectNotFound) {
		class = "publication_unavailable"
	}
	warnContentContextReconcileFailure(class)
}
