package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var newContentContextGCSClient = gcs.NewClientWithScope
var newContentContextFirestoreClient = newProfileFirestoreClient

type contentContextFailureEvent struct {
	operation string
	class     string
	cause     string
	attemptID string
}

var warnContentContextFailure = func(event contentContextFailureEvent) {
	log.Printf("WARNING project_context operation failed stage=project_context operation=%s class=%s cause=%s attempt_id=%s",
		event.operation, event.class, event.cause, event.attemptID)
}

const (
	contentContextInputTimeout    = 15 * time.Second
	contentContextModelTimeout    = 60 * time.Second
	contentContextRunTimeout      = 90 * time.Second
	contentContextMaxOutputTokens = 2048
)

type contentContextSummaryClient interface {
	Complete(context.Context, string, string) (llm.CompletionResult, error)
	Close()
}

type contentContextFastIdentity struct {
	source          string
	profileID       string
	profileRevision int64
	provider        string
	protocol        string
	model           string
}

type bootstrapContentContextSummaryClient struct {
	client *llm.Client
}

func (c *bootstrapContentContextSummaryClient) Complete(ctx context.Context, systemPrompt, input string) (llm.CompletionResult, error) {
	if c == nil || c.client == nil {
		return llm.CompletionResult{}, errors.New("platform Fast client is unavailable")
	}
	text, returnedModel, err := c.client.ChatWithMetadata(ctx, systemPrompt, input)
	if err != nil {
		return llm.CompletionResult{}, err
	}
	identity, ok := c.client.ModelIdentity()
	if !ok || identity.Provider == "" || identity.Model == "" || returnedModel == "" {
		return llm.CompletionResult{}, errors.New("platform Fast identity is unavailable")
	}
	return llm.CompletionResult{Text: text, Metadata: llm.CompletionMetadata{
		Provider: identity.Provider, Protocol: string(llm.ProtocolOpenAIChatCompletions),
		Role: string(llm.ProfileRoleFast), RequestedModel: identity.Model, ReturnedModel: returnedModel,
	}}, nil
}

func (c *bootstrapContentContextSummaryClient) Close() {
	if c != nil {
		c.client = nil
	}
}

var newContentContextSummaryClient = func(profile pipelineLLMProfile) (contentContextSummaryClient, error) {
	models := normalizePipelineLLMModels(profile)
	return llm.NewProfileClient(llm.ProfileConfig{
		ID: profile.ID, Revision: profile.Revision, Role: llm.ProfileRoleFast,
		Provider: profile.Provider, Protocol: profile.Protocol, Endpoint: profile.Endpoint, Model: models.Fast,
	}, string(profile.APIKey), llm.ProfileClientOptions{
		Timeout: contentContextModelTimeout, MaxOutputTokens: contentContextMaxOutputTokens,
	})
}

var newBootstrapContentContextSummaryClient = func(apiKey string) (contentContextSummaryClient, llm.ModelIdentity, error) {
	client := llm.NewClientWithOptions(apiKey, llm.ClientOptions{
		Model: suggestedQueryModel, RequestTimeoutSeconds: int(contentContextModelTimeout / time.Second),
	})
	if client == nil {
		return nil, llm.ModelIdentity{}, errors.New("platform Fast client is unavailable")
	}
	identity, ok := client.ModelIdentity()
	if !ok || identity.Provider == "" || identity.Model == "" {
		return nil, llm.ModelIdentity{}, errors.New("platform Fast identity is unavailable")
	}
	return &bootstrapContentContextSummaryClient{client: client}, identity, nil
}

var loadContentContextSummaryInput = pinnedSummaryInput

type contentContextOperationError struct {
	operation string
	attemptID string
	class     string
	err       error
}

func (e *contentContextOperationError) Error() string {
	return "project context operation failed: " + e.class
}

func (e *contentContextOperationError) Unwrap() error { return e.err }

func contentContextFailureCause(err error) string {
	causes := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	visited := 0
	var visit func(error, int)
	visit = func(current error, depth int) {
		if current == nil || depth > 8 || visited >= 16 || len(causes) >= 3 {
			return
		}
		visited++
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child, depth+1)
			}
			return
		}
		cause := contentContextFailureLeaf(current)
		if cause != "internal_error" {
			if _, ok := seen[cause]; !ok {
				seen[cause] = struct{}{}
				causes = append(causes, cause)
			}
			return
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			visit(wrapped.Unwrap(), depth+1)
			return
		}
		if _, ok := seen[cause]; !ok {
			seen[cause] = struct{}{}
			causes = append(causes, cause)
		}
	}
	visit(err, 0)
	if len(causes) == 0 {
		return "internal_error"
	}
	return strings.Join(causes, "+")
}

func contentContextFailureLeaf(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, contentcontext.ErrPublicationChanged):
		return "publication_changed"
	case errors.Is(err, contentcontext.ErrAttemptRejected):
		return "attempt_rejected"
	case errors.Is(err, contentcontext.ErrEmptyConcepts):
		return "empty_concepts"
	case isObjectNotFound(err):
		return "object_not_found"
	}
	var profileErr *llm.ProfileCallError
	if errors.As(err, &profileErr) {
		return llm.SafeErrorCategory(err)
	}
	var httpErr *llm.HTTPStatusError
	if errors.As(err, &httpErr) {
		return llm.SafeErrorCategory(err)
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code >= 100 && apiErr.Code <= 599 {
			return fmt.Sprintf("google_api_http_%d", apiErr.Code)
		}
		return "google_api_http_other"
	}
	if grpcStatus, ok := status.FromError(err); ok {
		switch grpcStatus.Code() {
		case codes.Canceled:
			return "grpc_canceled"
		case codes.DeadlineExceeded:
			return "grpc_deadline_exceeded"
		case codes.PermissionDenied:
			return "grpc_permission_denied"
		case codes.Unauthenticated:
			return "grpc_unauthenticated"
		case codes.ResourceExhausted:
			return "grpc_resource_exhausted"
		case codes.Unavailable:
			return "grpc_unavailable"
		case codes.NotFound:
			return "grpc_not_found"
		case codes.InvalidArgument:
			return "grpc_invalid_argument"
		case codes.FailedPrecondition:
			return "grpc_failed_precondition"
		case codes.Aborted:
			return "grpc_aborted"
		case codes.Internal:
			return "grpc_internal"
		default:
			return fmt.Sprintf("grpc_code_%d", grpcStatus.Code())
		}
	}
	return "internal_error"
}

func safeContentContextAttemptID(value string) string {
	if value == "" {
		return "none"
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return "invalid"
	}
	return id.String()
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
		!contentContextArchiveMatches(claim.SourceManifestIdentity, manifest.Manifest.GenerationID, manifest.ManifestSHA256) {
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
	digest, ok := contentcontext.NormalizeSHA256(manifestSHA256)
	if generationID == "" || manifestGeneration <= 0 || !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", generationID, manifestGeneration, digest)
}

func contentContextArchiveMatches(manifestIdentity, generationID, archiveSHA256 string) bool {
	parts := strings.Split(manifestIdentity, ":")
	if len(parts) != 3 || parts[0] != generationID {
		return false
	}
	objectGeneration, err := strconv.ParseInt(parts[1], 10, 64)
	archiveDigest, digestOK := contentcontext.NormalizeSHA256(archiveSHA256)
	identityDigest, identityOK := contentcontext.NormalizeSHA256(parts[2])
	return err == nil && objectGeneration > 0 && digestOK && identityOK && archiveDigest == identityDigest
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
		return &contentContextOperationError{operation: "worker_identity", class: "storage_failure", err: errors.New("invalid content-context worker identity")}
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: errors.New("content-context Firestore project identity unavailable")}
	}
	client, err := newContentContextFirestoreClient(ctx, cloudProject)
	if err != nil {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: fmt.Errorf("create content-context Firestore client: %w", err)}
	}
	if client == nil {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: errors.New("content-context Firestore client unavailable")}
	}
	defer client.Close()
	repository := contentcontext.NewFirestoreRepository(client)
	authority := newCloudContentContextAuthority(cfg, objects)
	_, err = repository.ReconcileCurrent(ctx, cfg.UserID, cfg.ProjectID, authority)
	if isObjectNotFound(err) {
		return nil
	}
	if err != nil {
		return &contentContextOperationError{operation: "reconcile", err: err}
	}
	return nil
}

// runCloudContentContext reconciles and claims the current published digest,
// reads the exact archived generation, then makes one Fast-role summary call
// outside the publisher fence. All failures are returned for bounded warning
// logging; they never change the knowledge publication result.
func runCloudContentContext(ctx context.Context, cfg workerConfig, objects objectStore) error {
	if ctx == nil || objects == nil || cfg.Bucket == "" || !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) {
		return &contentContextOperationError{operation: "worker_identity", class: "storage_failure", err: errors.New("invalid content-context worker identity")}
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: errors.New("content-context Firestore project identity unavailable")}
	}
	client, err := newContentContextFirestoreClient(ctx, cloudProject)
	if err != nil {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: fmt.Errorf("create content-context Firestore client: %w", err)}
	}
	if client == nil {
		return &contentContextOperationError{operation: "firestore_client", class: "storage_failure", err: errors.New("content-context Firestore client unavailable")}
	}
	defer client.Close()
	repository := contentcontext.NewFirestoreRepository(client)
	authority := newCloudContentContextAuthority(cfg, objects)
	claim, err := repository.Claim(ctx, cfg.UserID, cfg.ProjectID, authority)
	if isObjectNotFound(err) {
		return nil
	}
	if err != nil {
		return &contentContextOperationError{operation: "claim", err: err}
	}
	if claim == nil {
		return nil
	}

	inputCtx, cancelInput := context.WithTimeout(ctx, contentContextInputTimeout)
	input, metadata, err := loadContentContextSummaryInput(inputCtx, cfg, *claim)
	cancelInput()
	return runClaimedContentContextSummary(ctx, cfg, repository, authority, *claim, input, metadata, err)
}

func runClaimedContentContextSummary(ctx context.Context, cfg workerConfig, repository *contentcontext.FirestoreRepository, authority contentcontext.PublicationAuthority, claim contentcontext.SummaryClaim, input []byte, metadata contentcontext.SummaryInputMetadata, err error) error {
	if errors.Is(err, contentcontext.ErrEmptyConcepts) {
		marked, markErr := repository.MarkEmpty(ctx, cfg.UserID, cfg.ProjectID, authority, claim)
		if markErr != nil {
			return &contentContextOperationError{operation: "mark_empty", attemptID: claim.AttemptID, class: "storage_failure", err: markErr}
		}
		if !marked {
			return contentcontext.ErrAttemptRejected
		}
		return nil
	}
	if err != nil {
		return failCloudContentContextSummary(ctx, repository, authority, cfg, claim, "load_input", "input_invalid", err)
	}
	if len(input) == 0 || len(input)+len(contentcontext.SummarySystemPrompt)+1 > contentcontext.SummaryMaxInputBytes ||
		metadata.SourceGeneration != claim.SourceGeneration || metadata.SelectionVersion != contentcontext.SummarySelectionVersion ||
		!validSummaryInputMetadata(metadata, claim) {
		err = errors.New("pinned summary input metadata is invalid")
		return failCloudContentContextSummary(ctx, repository, authority, cfg, claim, "validate_input", "input_invalid", err)
	}

	summaryClient, identity, err := newContentContextFastClient(ctx, cfg)
	if err != nil {
		return failCloudContentContextSummary(ctx, repository, authority, cfg, claim, "fast_client", "provider_unavailable", err)
	}
	defer summaryClient.Close()
	modelCtx, cancelModel := context.WithTimeout(ctx, contentContextModelTimeout)
	modelCtx = llm.WithCallStage(modelCtx, "project_context")
	result, err := summaryClient.Complete(modelCtx, contentcontext.SummarySystemPrompt, string(input))
	cancelModel()
	if err != nil {
		return failCloudContentContextSummary(ctx, repository, authority, cfg, claim, "fast_call", contentContextProviderErrorClass(err), err)
	}
	text, err := contentcontext.ParseSummaryOutput([]byte(result.Text))
	if err != nil || result.Metadata.ProfileID != identity.profileID || result.Metadata.ProfileRevision != identity.profileRevision ||
		result.Metadata.Provider != identity.provider || result.Metadata.Protocol != identity.protocol ||
		result.Metadata.Role != string(llm.ProfileRoleFast) || result.Metadata.RequestedModel != identity.model || result.Metadata.ReturnedModel == "" {
		if err == nil {
			err = errors.New("Fast summary response metadata is invalid")
		}
		return failCloudContentContextSummary(ctx, repository, authority, cfg, claim, "validate_response", "summary_invalid", err)
	}
	completion := contentcontext.SummaryCompletion{
		Text: text, PromptRevision: contentcontext.SummaryPromptRevision, ProfileSource: identity.source,
		ProfileID: result.Metadata.ProfileID, ProfileRevision: result.Metadata.ProfileRevision,
		Role: result.Metadata.Role, RequestedModel: result.Metadata.RequestedModel,
		ReturnedModel: result.Metadata.ReturnedModel, InputDigest: metadata.InputDigest,
		SelectionVersion: metadata.SelectionVersion,
	}
	completed, err := repository.Complete(ctx, cfg.UserID, cfg.ProjectID, authority, claim, completion)
	if err != nil {
		return &contentContextOperationError{operation: "complete", attemptID: claim.AttemptID, class: "storage_failure", err: fmt.Errorf("persist content-context summary: %w", err)}
	}
	if !completed {
		return &contentContextOperationError{operation: "complete", attemptID: claim.AttemptID, err: contentcontext.ErrAttemptRejected}
	}
	return nil
}

func validSummaryInputMetadata(metadata contentcontext.SummaryInputMetadata, claim contentcontext.SummaryClaim) bool {
	digest, digestOK := contentcontext.NormalizeSHA256(metadata.ConceptsDigest)
	claimDigest, claimOK := contentcontext.NormalizeSHA256(claim.ConceptsSHA256)
	_, inputDigestOK := contentcontext.NormalizeSHA256(metadata.InputDigest)
	return digestOK && claimOK && inputDigestOK && digest == claimDigest
}

func newContentContextFastClient(ctx context.Context, cfg workerConfig) (contentContextSummaryClient, contentContextFastIdentity, error) {
	switch cfg.PipelineProfileSource {
	case "personal", "shared":
		profile, err := contentContextFastProfile(ctx, cfg)
		if err != nil {
			return nil, contentContextFastIdentity{}, err
		}
		client, err := newContentContextSummaryClient(profile)
		if err != nil {
			return nil, contentContextFastIdentity{}, err
		}
		models := normalizePipelineLLMModels(profile)
		return client, contentContextFastIdentity{
			source: cfg.PipelineProfileSource, profileID: profile.ID, profileRevision: profile.Revision,
			provider: profile.Provider, protocol: string(profile.Protocol), model: models.Fast,
		}, nil
	case "bootstrap":
		if !cfg.BootstrapLLMSource || cfg.SharedProfilePool || len(cfg.ProfileCandidates) != 0 {
			return nil, contentContextFastIdentity{}, errors.New("Project bootstrap source identity is invalid")
		}
		client, model, err := newBootstrapContentContextSummaryClient(cfg.APIKey)
		if err != nil {
			return nil, contentContextFastIdentity{}, err
		}
		return client, contentContextFastIdentity{
			source: "bootstrap", provider: model.Provider,
			protocol: string(llm.ProtocolOpenAIChatCompletions), model: model.Model,
		}, nil
	default:
		return nil, contentContextFastIdentity{}, errors.New("Project LLM source is unavailable")
	}
}

func contentContextFastProfile(ctx context.Context, cfg workerConfig) (pipelineLLMProfile, error) {
	if cfg.BootstrapLLMSource {
		return pipelineLLMProfile{}, errors.New("bootstrap source has no Project profile")
	}
	switch cfg.PipelineProfileSource {
	case "personal":
		if cfg.SharedProfilePool || len(cfg.ProfileCandidates) != 1 {
			return pipelineLLMProfile{}, errors.New("Project personal Fast profile is unavailable")
		}
		return cfg.ProfileCandidates[0], nil
	case "shared":
		if !cfg.SharedProfilePool || len(cfg.ProfileCandidates) == 0 {
			return pipelineLLMProfile{}, errors.New("Project shared Fast profile is unavailable")
		}
		eligible, err := revalidatePipelineProfileCandidates(ctx, cfg, cfg.ProfileCandidates)
		if err != nil {
			return pipelineLLMProfile{}, fmt.Errorf("Project shared Fast eligibility could not be proven: %w", err)
		}
		if cfg.ActiveLLMProfile != nil {
			for _, candidate := range eligible {
				if samePipelineProfileIdentity(*cfg.ActiveLLMProfile, candidate) {
					return candidate, nil
				}
			}
		}
		return eligible[0], nil
	default:
		return pipelineLLMProfile{}, errors.New("Project profile source is unavailable")
	}
}

func contentContextProviderErrorClass(err error) string {
	var callErr *llm.ProfileCallError
	if errors.As(err, &callErr) {
		switch callErr.Kind {
		case llm.ProviderTimeout:
			return "provider_timeout"
		case llm.ProviderResponseInvalid, llm.ProviderOutputTruncated:
			return "invalid_response"
		default:
			return "provider_error"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "provider_timeout"
	}
	return "provider_error"
}

func failCloudContentContextSummary(ctx context.Context, repository *contentcontext.FirestoreRepository, authority contentcontext.PublicationAuthority, cfg workerConfig, claim contentcontext.SummaryClaim, operation, errorClass string, cause error) error {
	_, err := repository.Fail(ctx, cfg.UserID, cfg.ProjectID, authority, claim, errorClass)
	if err != nil {
		return &contentContextOperationError{operation: "record_failure", attemptID: claim.AttemptID, class: "storage_failure", err: errors.Join(cause, err)}
	}
	return &contentContextOperationError{operation: operation, attemptID: claim.AttemptID, class: errorClass, err: cause}
}

func reportContentContextReconcileFailure(err error) {
	if err == nil {
		return
	}
	class := "storage_failure"
	operation := "summary_run"
	attemptID := ""
	cause := err
	var operationErr *contentContextOperationError
	if errors.As(err, &operationErr) {
		if operationErr.class != "" {
			class = operationErr.class
		}
		if operationErr.operation != "" {
			operation = operationErr.operation
		}
		attemptID = operationErr.attemptID
		cause = operationErr.err
	}
	if operationErr == nil || operationErr.class == "" {
		switch {
		case errors.Is(err, contentcontext.ErrPublicationChanged):
			class = "publication_changed"
		case isObjectNotFound(err):
			class = "publication_unavailable"
		case errors.Is(err, contentcontext.ErrAttemptRejected):
			class = "attempt_rejected"
		}
	}
	warnContentContextFailure(contentContextFailureEvent{
		operation: operation, class: class, cause: contentContextFailureCause(cause), attemptID: safeContentContextAttemptID(attemptID),
	})
}
