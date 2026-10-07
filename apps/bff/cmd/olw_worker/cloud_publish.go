package main

// Cloud publishing intentionally uses objects rather than a mounted filesystem.
// The narrow interface makes the commit protocol executable with an in-memory
// backend in tests and keeps GCS details at this boundary.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/annotation"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

type objectConditions struct {
	DoesNotExist    bool
	GenerationMatch int64
}
type objectAttrs struct {
	Name             string
	Generation, Size int64
	Metadata         map[string]string
}
type objectStore interface {
	Read(context.Context, string, int64, int64) ([]byte, objectAttrs, error)
	List(context.Context, string, int) ([]objectAttrs, error)
	Write(context.Context, string, []byte, map[string]string, objectConditions) (objectAttrs, error)
	Delete(context.Context, string, int64) error
	Close() error
}

var errObjectGenerationConflict = errors.New("object generation conflict")
var errObjectNotFound = errors.New("object not found")

const cloudManifestReadbackTimeout = 3 * time.Second
const cloudFailureRecordingTimeout = 5 * time.Second

var workerFailureLogMu sync.Mutex

// Tests replace this seam to assert the independent warning without capturing
// the process logger.
var warnCloudFailurePipelineLog = func(cfg workerConfig, failure error, secrets []string) {
	diagnostic := diagnosticForError(failure)
	redact := func(value string) string {
		return string(redactDiagnosticBytes([]byte(value), append(logSecrets(cfg), secrets...)))
	}
	log.Printf("WARNING cloud failure pipeline log recording failed execution_id=%q project_id=%q stage=%q detail_code=%q",
		redact(cfg.ExecutionID), redact(cfg.ProjectID), diagnostic.Stage, diagnostic.DetailCode)
}

var errManifestCommitOutcomeUnknown = errors.New("manifest commit outcome unknown")
var errCloudFailureRecording = errors.New("failure state recording failed")
var errCloudPipelineLogRecording = errors.New("pipeline log recording failed")
var errCloudSourceStatusRecording = errors.New("source status recording failed")
var errCloudPipelineExecution = errors.New("pipeline execution failed")
var errCloudPipelinePublish = errors.New("pipeline publish failed")
var errCloudMaterialization = errors.New("pipeline input materialization failed")
var errCloudCommittedReceipt = errors.New("pipeline committed but receipt recording failed")
var errCloudCleanup = errors.New("pipeline cleanup failed")
var errCloudCommittedCleanup = errors.New("pipeline committed but cleanup failed")
var errCloudLeaseUnavailable = errors.New("pipeline publish lease unavailable")
var errCloudLeaseHeld = errors.New("pipeline publish lease is held")
var errCloudWorkspaceUnavailable = errors.New("pipeline workspace unavailable")
var errCloudWorkerInputInvalid = errors.New("cloud worker input is invalid")
var errCloudWorkerConfigInvalid = errors.New("cloud worker configuration is invalid")
var errCloudObjectRead = errors.New("cloud object read failed")
var errCloudSourceStatusInvalid = errors.New("invalid source status")
var errCloudSourceReceiptInvalid = errors.New("invalid source receipt")
var errCloudSourceReceiptRead = errors.New("source receipt read failed")
var errCloudSourceReceiptWrite = errors.New("source receipt write failed")
var errCloudSourceReceiptConflict = errors.New("source receipt conflict")
var errCloudGenerationOutputRead = errors.New("generation output read failed")
var errCloudGenerationUpload = errors.New("immutable generation upload failed")

// annotatedError keeps a stable public boundary message while preserving the
// root cause for diagnostics and errors.Is/As via Unwrap.
type annotatedError struct {
	public error
	cause  error
}

func (e *annotatedError) Error() string {
	if e == nil || e.public == nil {
		return "annotated error"
	}
	return e.public.Error()
}
func (e *annotatedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func (e *annotatedError) Is(target error) bool {
	if e == nil {
		return false
	}
	return errors.Is(e.public, target)
}

func annotateError(public, cause error) error {
	if public == nil {
		return cause
	}
	if cause == nil {
		return public
	}
	return &annotatedError{public: public, cause: cause}
}

func cloudFailureRecordingContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), cloudFailureRecordingTimeout)
}

const cloudLeaseReleaseAttempts = 3

var cloudMkdirTemp = os.MkdirTemp
var cloudReconcileSources = reconcileWorkspaceSources
var cloudReconcileConcepts = reconcileWorkspaceConcepts

type cloudFailureRecordingError struct {
	pipelineLog       bool
	sourceStatus      bool
	failureDiagnostic bool
}

func (e cloudFailureRecordingError) Error() string { return errCloudFailureRecording.Error() }
func (e cloudFailureRecordingError) Is(target error) bool {
	return target == errCloudFailureRecording || target == errCloudPipelineLogRecording && e.pipelineLog || target == errCloudSourceStatusRecording && e.sourceStatus
}

func normalizeObjectPrecondition(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return annotateError(errObjectNotFound, err)
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 404:
			return annotateError(errObjectNotFound, err)
		case 412:
			return annotateError(errObjectGenerationConflict, err)
		}
	}
	return err
}

func isObjectNotFound(err error) bool {
	return errors.Is(err, errObjectNotFound) || errors.Is(err, cloudstorage.ErrObjectNotExist)
}

func isObjectGenerationConflict(err error) bool {
	return errors.Is(err, errObjectGenerationConflict)
}

type cloudObjectStore struct {
	bucketName string
	client     *cloudstorage.Client
	bucket     *cloudstorage.BucketHandle
}

func newCloudObjectStore(bucket string) objectStore { return &cloudObjectStore{bucketName: bucket} }
func (s *cloudObjectStore) ensure(ctx context.Context) error {
	if s.bucket != nil {
		return nil
	}
	c, err := cloudstorage.NewClient(ctx)
	if err != nil {
		return err
	}
	s.client = c
	s.bucket = c.Bucket(s.bucketName)
	return nil
}
func (s *cloudObjectStore) Read(ctx context.Context, name string, objectGeneration, limit int64) ([]byte, objectAttrs, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, objectAttrs{}, err
	}
	if limit < 0 {
		return nil, objectAttrs{}, errors.New("object exceeds input limit")
	}
	o := s.bucket.Object(name)
	if objectGeneration > 0 {
		o = o.Generation(objectGeneration)
	}
	attrs, err := o.Attrs(ctx)
	if err != nil {
		return nil, objectAttrs{}, normalizeObjectPrecondition(err)
	}
	if attrs.Size < 0 || attrs.Size > limit {
		return nil, objectAttrs{}, errors.New("object exceeds input limit")
	}
	o = o.Generation(attrs.Generation)
	r, err := o.NewReader(ctx)
	if err != nil {
		return nil, objectAttrs{}, normalizeObjectPrecondition(err)
	}
	if r.Attrs.Generation != attrs.Generation || r.Attrs.Size != attrs.Size {
		_ = r.Close()
		return nil, objectAttrs{}, errors.New("object changed while reading")
	}
	if r.Attrs.Size < 0 || r.Attrs.Size > limit {
		_ = r.Close()
		return nil, objectAttrs{}, errors.New("object exceeds input limit")
	}
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		_ = r.Close()
		return nil, objectAttrs{}, err
	}
	if err := r.Close(); err != nil {
		return nil, objectAttrs{}, err
	}
	if int64(len(b)) != r.Attrs.Size || int64(len(b)) > limit {
		return nil, objectAttrs{}, errors.New("object exceeds input limit")
	}
	return b, objectAttrs{Name: name, Generation: attrs.Generation, Size: attrs.Size, Metadata: attrs.Metadata}, nil
}
func (s *cloudObjectStore) List(ctx context.Context, prefix string, max int) ([]objectAttrs, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	if max < 0 {
		return nil, errors.New("invalid object list limit")
	}
	it := s.bucket.Objects(ctx, &cloudstorage.Query{Prefix: prefix})
	out := make([]objectAttrs, 0, min(max, 32))
	var totalSize int64
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, normalizeObjectPrecondition(err)
		}
		if len(out) == max {
			return nil, errors.New("object list exceeds limit")
		}
		if a.Size < 0 || a.Size > generation.MaxTotalSize || totalSize > generation.MaxTotalSize-a.Size {
			return nil, errors.New("object list exceeds limit")
		}
		out = append(out, objectAttrs{Name: a.Name, Generation: a.Generation, Size: a.Size, Metadata: a.Metadata})
		totalSize += a.Size
	}
}
func (s *cloudObjectStore) Write(ctx context.Context, name string, data []byte, metadata map[string]string, condition objectConditions) (objectAttrs, error) {
	if err := s.ensure(ctx); err != nil {
		return objectAttrs{}, err
	}
	o := s.bucket.Object(name)
	if condition.DoesNotExist {
		o = o.If(cloudstorage.Conditions{DoesNotExist: true})
	} else if condition.GenerationMatch > 0 {
		o = o.If(cloudstorage.Conditions{GenerationMatch: condition.GenerationMatch})
	}
	w := o.NewWriter(ctx)
	w.Metadata = metadata
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return objectAttrs{}, normalizeObjectPrecondition(err)
	}
	if err := w.Close(); err != nil {
		return objectAttrs{}, normalizeObjectPrecondition(err)
	}
	a := w.Attrs()
	return objectAttrs{Name: name, Generation: a.Generation, Size: a.Size, Metadata: a.Metadata}, nil
}
func (s *cloudObjectStore) Delete(ctx context.Context, name string, generation int64) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	o := s.bucket.Object(name)
	if generation > 0 {
		o = o.If(cloudstorage.Conditions{GenerationMatch: generation})
	}
	return normalizeObjectPrecondition(o.Delete(ctx))
}
func (s *cloudObjectStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

type cloudLease struct {
	store      objectStore
	name       string
	generation int64
}

// acquireCloudLease lives in lease_liveness.go (LWC-222 owner-liveness reclaim).

func (l *cloudLease) Release(_ context.Context) error {
	if err := storage.RetryGenerationCleanup(l.generation, generation.LeaseReleaseTimeout, cloudLeaseReleaseAttempts, func(ctx context.Context, objectGeneration int64) error {
		return l.store.Delete(ctx, l.name, objectGeneration)
	}); err != nil {
		return annotateError(errCloudCleanup, err)
	}
	return nil
}

// runCloudSuggestedQueries materializes the current committed generation,
// regenerates cache/suggested_queries.json only, and publishes a complete new
// generation (all other artifacts carry-forwarded from the materialized workspace).
func runCloudSuggestedQueries(ctx context.Context, cfg workerConfig, objects objectStore) (result error) {
	cfg.cloudMode = true
	cfg.SuggestedQueries = true
	if cfg.Bucket == "" || cfg.UserID == "" || cfg.ProjectID == "" {
		return errCloudWorkerConfigInvalid
	}
	if err := validateWorkerConfigBounds(cfg); err != nil {
		return annotateError(errCloudWorkerInputInvalid, err)
	}
	defer objects.Close()
	prefix := workerProjectObjectPrefix(cfg)
	lease, err := acquireCloudLease(ctx, objects, prefix, cfg.ExecutionID)
	if err != nil {
		return annotateError(errCloudLeaseUnavailable, err)
	}
	committed := false
	workspace := ""
	defer func() {
		if cleanupErr := lease.Release(ctx); cleanupErr != nil {
			if result == nil {
				if committed {
					failure := newWorkerFailure(nil, failureStageLeaseCleanup, failureClassIO, "", cleanupErr)
					if recordErr := writeCloudFailureLogAndDiagnostic(ctx, objects, prefix, "", cfg, failure); recordErr != nil {
						result = errors.Join(annotateError(errCloudCommittedCleanup, cleanupErr), cloudFailureRecordingError{failureDiagnostic: true})
					} else {
						result = annotateError(errCloudCommittedCleanup, cleanupErr)
					}
				} else {
					result = cleanupErr
				}
			}
		}
	}()
	workspace, err = cloudMkdirTemp("/tmp", "olw-cloud-suggest-")
	if err != nil {
		failure := newWorkerFailure(ctx, failureStageInputMaterialization, failureClassIO, "", err)
		primary := annotateError(errCloudWorkspaceUnavailable, err)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, "", cfg, nil, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	defer os.RemoveAll(workspace)

	// Suggest-only always needs the current generation (never CLEAN_REBUILD).
	snapshots, manifestData, manifestAttrs, err := materializeCloudWorkspace(ctx, objects, prefix, workspace, false)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageInputMaterialization, failureClassUnknown)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	if manifestAttrs.Generation <= 0 || len(manifestData) == 0 {
		failure := newWorkerFailure(ctx, failureStageInputMaterialization, failureClassStateInvalid, "", errors.New("suggested-queries requires a committed generation"))
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}

	if err := runSuggestedQueriesStage(ctx, workspace, suggestedQueryProvider(cfg)); err != nil {
		failure := preserveWorkerFailure(err, failureStagePostprocess, failureClassUnknown)
		primary := annotateError(errCloudPipelineExecution, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure, diagnosticSecrets(cfg, nil)); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}

	publishedManifest, publishedGeneration, err := publishCloudGenerationFromStart(ctx, objects, prefix, workspace, snapshots, manifestData, manifestAttrs, true, true, localExecutionIDFor(cfg))
	if err != nil {
		if errors.Is(err, errManifestCommitOutcomeUnknown) {
			if recordErr := recordCloudAmbiguousManifestFailure(ctx, objects, prefix, workspace, cfg); recordErr != nil {
				return errors.Join(errManifestCommitOutcomeUnknown, recordErr)
			}
			return errManifestCommitOutcomeUnknown
		}
		failureClass := failureClassIO
		if errors.Is(err, errObjectGenerationConflict) {
			failureClass = failureClassPublishConflict
		}
		failure := preserveWorkerFailure(err, failureStageGenerationPublish, failureClass)
		primary := annotateError(errCloudPipelinePublish, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	committed = true
	if err := writeLocalPublicationReceipt(ctx, objects, prefix, cfg, publishedManifest, publishedGeneration); err != nil {
		return annotateError(errCloudCommittedReceipt, err)
	}
	if err := writeCloudReceipts(ctx, objects, prefix, workspace, cfg, snapshots); err != nil {
		failure := preserveWorkerFailure(err, failureStageReceiptRecording, failureClassRecordingFailure)
		if recordErr := writeCloudFailureLogAndDiagnostic(ctx, objects, prefix, workspace, cfg, failure); recordErr != nil {
			return errors.Join(annotateError(errCloudCommittedReceipt, failure), cloudFailureRecordingError{failureDiagnostic: true})
		}
		return annotateError(errCloudCommittedReceipt, failure)
	}
	return nil
}

func runCloudWorkerBatch(ctx context.Context, cfg workerConfig, commands [][]string, objects objectStore) (result error) {
	cfg.cloudMode = true
	if err := validateWorkerInput(cfg, commands); err != nil {
		return annotateError(errCloudWorkerInputInvalid, err)
	}
	if cfg.Bucket == "" || cfg.UserID == "" || cfg.ProjectID == "" || !cfg.Postprocess || !startsWithFullOLWRun(commands) {
		return errCloudWorkerConfigInvalid
	}
	defer objects.Close()
	deployedConfig, localAPIKey, runTimeoutSeconds, err := readCloudPipelineInputs(ctx, cfg, objects)
	if err != nil {
		return annotateError(errCloudWorkerConfigInvalid, err)
	}
	if len(localAPIKey) > 0 {
		cfg.APIKey = string(localAPIKey)
		cfg.apiKeySet = true
	}
	clear(localAPIKey)
	cfg.DeployedSynto = append([]byte(nil), deployedConfig...)
	runCtx, cancelRun := context.WithTimeout(ctx, time.Duration(runTimeoutSeconds)*time.Second)
	defer cancelRun()
	ctx = runCtx
	prefix := workerProjectObjectPrefix(cfg)
	lease, err := acquireCloudLease(ctx, objects, prefix, cfg.ExecutionID)
	if err != nil {
		return annotateError(errCloudLeaseUnavailable, err)
	}
	committed := false
	workspace := ""
	defer func() {
		releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), cloudFailureRecordingTimeout)
		defer cancelRelease()
		if cleanupErr := lease.Release(releaseCtx); cleanupErr != nil {
			if result == nil {
				if committed {
					failure := newWorkerFailure(nil, failureStageLeaseCleanup, failureClassIO, "", cleanupErr)
					if recordErr := writeCloudFailureLogAndDiagnostic(ctx, objects, prefix, "", cfg, failure); recordErr != nil {
						result = errors.Join(annotateError(errCloudCommittedCleanup, cleanupErr), cloudFailureRecordingError{failureDiagnostic: true})
					} else {
						result = annotateError(errCloudCommittedCleanup, cleanupErr)
					}
				} else {
					result = cleanupErr
				}
			}
		}
	}()
	cfg.pinnedProfileGuidance, err = resolveProfileGuidanceAtCompileStart(ctx, cfg, objects)
	if err != nil {
		failure := newWorkerFailure(ctx, failureStageSyntoConfigValidation, failureClassStateInvalid, "", err)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, "", cfg, nil, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	if cfg.pinnedProfileGuidance != nil {
		pin := *cfg.pinnedProfileGuidance
		if pin.BootstrapRef != nil {
			ref := *pin.BootstrapRef
			pin.BootstrapRef = &ref
		}
		cfg.pinnedProfileGuidance = &pin
		log.Printf("worker: pinned Profile guidance revision=%s", cfg.pinnedProfileGuidance.Revision)
	}
	// Cloud workers are deliberately detached from DATA_DIR, WORKSPACE and any
	// mount. Their private work area is always local /tmp.
	workspace, err = cloudMkdirTemp("/tmp", "olw-cloud-")
	if err != nil {
		failure := newWorkerFailure(ctx, failureStageInputMaterialization, failureClassIO, "", err)
		primary := annotateError(errCloudWorkspaceUnavailable, err)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, "", cfg, nil, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	defer os.RemoveAll(workspace)
	if cfg.CleanRebuild {
		log.Printf("worker: clean_rebuild=true; skipping prior generation materialization (raw/annotations retained)")
	}
	snapshots, manifestData, manifestAttrs, err := materializeCloudWorkspace(ctx, objects, prefix, workspace, cfg.CleanRebuild)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageInputMaterialization, failureClassUnknown)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	startRawInputs, err := captureCloudRawInputs(ctx, workspace, snapshots)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageInputMaterialization, failureClassUnknown)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	// Capture concept IDs from the immediately prior committed/materialized
	// workspace id_map before OLW regenerates transient concept identities.
	priorConcepts, err := snapshotConcepts(workspace, snapshots)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageInputMaterialization, failureClassUnknown)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	if err := materializeSnapshots(workspace, snapshots); err != nil {
		failure := preserveWorkerFailure(err, failureStageInputMaterialization, failureClassUnknown)
		primary := annotateError(errCloudMaterialization, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	cfg.VaultPath = workspace
	cfg.Workspace = false
	// Keep SuppressOutput as configured by the caller. Cloud mode always writes
	// a durable pipeline log; silencing console is optional and must not be the
	// default, or local cloud runs and Cloud Logging lose live Synto output.
	err = runWorkerBatchAtVault(ctx, cfg, commands, workspace)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageSyntoRun, failureClassUnknown)
		primary := annotateError(errCloudPipelineExecution, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	if err := cloudReconcileSources(workspace, snapshots); err != nil {
		failure := preserveWorkerFailure(err, failureStageSourceReconciliation, failureClassUnknown)
		primary := annotateError(errCloudPipelinePublish, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure, diagnosticSecrets(cfg, commands)); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	if err := cloudReconcileConcepts(workspace, priorConcepts, snapshots); err != nil {
		failure := preserveWorkerFailure(err, failureStageConceptReconciliation, failureClassUnknown)
		primary := annotateError(errCloudPipelinePublish, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure, diagnosticSecrets(cfg, commands)); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	snapshots, err = pinNewlyMappedCloudSources(workspace, snapshots, startRawInputs)
	if err != nil {
		failure := preserveWorkerFailure(err, failureStageSourceReconciliation, failureClassUnknown)
		primary := annotateError(errCloudPipelinePublish, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure, diagnosticSecrets(cfg, commands)); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	// Bind the captured raw digests to the successful worker receipt in this
	// private workspace before archiving its generation-bound source inventory.
	if err := recordSuccess(workspace, snapshots, time.Now().UTC()); err != nil {
		failure := preserveWorkerFailure(err, failureStageReceiptRecording, failureClassRecordingFailure)
		primary := annotateError(errCloudSourceStatusRecording, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	publishedManifest, publishedGeneration, err := publishCloudGenerationFromStart(ctx, objects, prefix, workspace, snapshots, manifestData, manifestAttrs, manifestAttrs.Generation > 0, false, localExecutionIDFor(cfg))
	if err != nil {
		if errors.Is(err, errManifestCommitOutcomeUnknown) {
			if recordErr := recordCloudAmbiguousManifestFailure(ctx, objects, prefix, workspace, cfg); recordErr != nil {
				return errors.Join(errManifestCommitOutcomeUnknown, recordErr)
			}
			return errManifestCommitOutcomeUnknown
		}
		failureClass := failureClassIO
		if errors.Is(err, errObjectGenerationConflict) {
			failureClass = failureClassPublishConflict
		}
		failure := preserveWorkerFailure(err, failureStageGenerationPublish, failureClass)
		primary := annotateError(errCloudPipelinePublish, failure)
		if recordErr := writeCloudFailureReceipts(ctx, objects, prefix, workspace, cfg, snapshots, failure); recordErr != nil {
			return errors.Join(primary, recordErr)
		}
		return primary
	}
	committed = true
	if err := writeLocalPublicationReceipt(ctx, objects, prefix, cfg, publishedManifest, publishedGeneration); err != nil {
		return annotateError(errCloudCommittedReceipt, err)
	}
	if err := writeCloudReceipts(ctx, objects, prefix, workspace, cfg, snapshots); err != nil {
		failure := preserveWorkerFailure(err, failureStageReceiptRecording, failureClassRecordingFailure)
		if recordErr := writeCloudFailureLogAndDiagnostic(ctx, objects, prefix, workspace, cfg, failure); recordErr != nil {
			return errors.Join(annotateError(errCloudCommittedReceipt, failure), cloudFailureRecordingError{failureDiagnostic: true})
		}
		return annotateError(errCloudCommittedReceipt, failure)
	}
	if err := recordProfileCompileReceipt(ctx, cfg, publishedManifest, publishedGeneration); err != nil {
		return annotateError(errCloudCommittedReceipt, err)
	}
	return nil
}

func materializeCloudWorkspace(ctx context.Context, objects objectStore, prefix, workspace string, cleanRebuild bool) ([]sourceSnapshot, []byte, objectAttrs, error) {
	budget := cloudMaterializationBudget{}
	snapshots, err := materializeCanonicalCloudInputs(ctx, objects, prefix, workspace, &budget)
	if err != nil {
		return snapshots, nil, objectAttrs{}, err
	}
	reservations := cloudTombstones(snapshots)
	manifestData, manifestAttrs, err := objects.Read(ctx, prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
	manifestExists := err == nil
	if err != nil && !isObjectNotFound(err) {
		return snapshots, nil, objectAttrs{}, err
	}
	// Always retain current manifest bytes/attrs for CAS publish even when
	// clean rebuild skips installing prior generation outputs into the workspace.
	if cleanRebuild {
		if manifestExists {
			if _, err := generation.Decode(manifestData); err != nil {
				return snapshots, nil, objectAttrs{}, err
			}
		}
	} else if manifestExists {
		m, err := generation.Decode(manifestData)
		if err != nil {
			return snapshots, nil, objectAttrs{}, err
		}
		for _, f := range m.Files {
			if f.Path == "synto.toml" {
				continue
			}
			b, a, err := readCloudMaterializedObject(ctx, objects, prefix+m.ObjectPath(f), f.Generation, f.Size, &budget)
			if err != nil {
				return snapshots, nil, objectAttrs{}, fmt.Errorf("generation object fails manifest validation: %w", err)
			}
			if a.Size != f.Size || digestBytes(b) != f.SHA256 {
				return snapshots, nil, objectAttrs{}, fmt.Errorf("generation object fails manifest validation: size/digest mismatch path=%s", f.Path)
			}
			if err := writeCloudFile(workspace, f.Path, b); err != nil {
				return snapshots, nil, objectAttrs{}, err
			}
		}
	} else if err := materializeLegacyCloudOutputs(ctx, objects, prefix, workspace, &budget); err != nil {
		return snapshots, nil, objectAttrs{}, err
	}
	err = nil
	if len(snapshots) == 0 {
		mapped, mapErr := snapshotSources(workspace)
		if mapErr == nil && len(mapped) > 0 {
			snapshots = mapped
		} else if mapErr != nil {
			err = mapErr
		}
	} else if mapped, mapErr := snapshotSources(workspace); mapErr != nil {
		err = mapErr
	} else if len(mapped) > 0 {
		snapshots = appendCloudReservations(mapped, reservations)
	}
	return snapshots, manifestData, manifestAttrs, err
}

func captureCloudRawInputs(ctx context.Context, workspace string, snapshots []sourceSnapshot) (map[string][]byte, error) {
	files, err := listVaultRawFiles(ctx, workspace)
	if err != nil {
		return nil, err
	}
	mappedPaths := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		mappedPaths[snapshot.RawPath] = true
	}
	inputs := make(map[string][]byte, len(files))
	for _, file := range files {
		if mappedPaths[file.Path] {
			continue
		}
		data, err := readRegularFileWithin(workspace, file.Path)
		if err != nil {
			return nil, err
		}
		if digestBytes(data) != file.SHA256 {
			return nil, fmt.Errorf("raw input changed while snapshotting %q", file.Path)
		}
		inputs[file.Path] = data
	}
	return inputs, nil
}

func pinNewlyMappedCloudSources(workspace string, snapshots []sourceSnapshot, startRawInputs map[string][]byte) ([]sourceSnapshot, error) {
	known := make(map[string]bool, len(snapshots))
	knownPaths := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		known[snapshot.SourceID] = true
		knownPaths[snapshot.RawPath] = true
	}
	mapped, err := snapshotSourcesExcept(workspace, known)
	if err != nil {
		return nil, err
	}
	for _, snapshot := range mapped {
		if snapshot.Tombstone {
			return nil, fmt.Errorf("new source %q has no start-time raw bytes", snapshot.SourceID)
		}
		if knownPaths[snapshot.RawPath] {
			return nil, fmt.Errorf("new source %q reuses a mapped raw path", snapshot.SourceID)
		}
		startBytes, ok := startRawInputs[snapshot.RawPath]
		if !ok || digestBytes(startBytes) != snapshot.RawSHA256 {
			return nil, fmt.Errorf("new source %q raw bytes differ from start-time input", snapshot.SourceID)
		}
		snapshot.RawBytes = startBytes
		snapshots = append(snapshots, snapshot)
		known[snapshot.SourceID] = true
	}
	return snapshots, nil
}

func cloudTombstones(snapshots []sourceSnapshot) []sourceSnapshot {
	reservations := make([]sourceSnapshot, 0)
	for _, snapshot := range snapshots {
		if snapshot.Tombstone {
			reservations = append(reservations, snapshot)
		}
	}
	return reservations
}

func appendCloudReservations(snapshots, reservations []sourceSnapshot) []sourceSnapshot {
	seen := make(map[string]struct{}, len(snapshots)+len(reservations))
	for _, snapshot := range snapshots {
		seen[snapshot.SourceID+"\x00"+snapshot.RawPath] = struct{}{}
	}
	for _, reservation := range reservations {
		key := reservation.SourceID + "\x00" + reservation.RawPath
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		snapshots = append(snapshots, reservation)
	}
	return snapshots
}

type cloudMaterializationBudget struct {
	objects int
	bytes   int64
}

func (b *cloudMaterializationBudget) reserve(size int64) error {
	if size < 0 || size > generation.MaxFileBytes || b.objects >= generation.MaxFiles || b.bytes > generation.MaxTotalSize-size {
		return errors.New("cloud materialization exceeds limit")
	}
	b.objects++
	b.bytes += size
	return nil
}

func readCloudMaterializedObject(ctx context.Context, objects objectStore, name string, generationID, expectedSize int64, budget *cloudMaterializationBudget) ([]byte, objectAttrs, error) {
	if err := budget.reserve(expectedSize); err != nil {
		return nil, objectAttrs{}, err
	}
	limit := expectedSize
	if limit > generation.MaxFileBytes {
		limit = generation.MaxFileBytes
	}
	b, attrs, err := objects.Read(ctx, name, generationID, limit)
	if err != nil {
		return nil, objectAttrs{}, annotateError(errCloudObjectRead, err)
	}
	if attrs.Size != expectedSize || int64(len(b)) != expectedSize {
		return nil, objectAttrs{}, annotateError(errCloudObjectRead, fmt.Errorf("size mismatch attrs=%d body=%d expected=%d", attrs.Size, len(b), expectedSize))
	}
	return b, attrs, nil
}

func materializeCloudObjects(ctx context.Context, objects objectStore, prefix, workspace, objectPrefix string, budget *cloudMaterializationBudget, keep func(string) bool) error {
	remaining := generation.MaxFiles - budget.objects
	attrs, err := objects.List(ctx, prefix+objectPrefix, remaining)
	if err != nil {
		return err
	}
	for _, attr := range attrs {
		rel := strings.TrimPrefix(attr.Name, prefix)
		if !strings.HasPrefix(attr.Name, prefix+objectPrefix) || !keep(rel) {
			continue
		}
		b, _, err := readCloudMaterializedObject(ctx, objects, attr.Name, 0, attr.Size, budget)
		if err != nil {
			return err
		}
		if err := writeCloudFile(workspace, rel, b); err != nil {
			return err
		}
	}
	return nil
}

func materializeCanonicalCloudInputs(ctx context.Context, objects objectStore, prefix, workspace string, budget *cloudMaterializationBudget) ([]sourceSnapshot, error) {
	if err := materializeCloudObjects(ctx, objects, prefix, workspace, "raw/", budget, func(rel string) bool { return storage.SafeRawPath(rel) }); err != nil {
		return nil, err
	}
	if err := materializeCloudObjects(ctx, objects, prefix, workspace, "cache/annotations/", budget, func(rel string) bool {
		name := strings.TrimSuffix(strings.TrimPrefix(rel, "cache/annotations/"), ".json")
		return strings.HasSuffix(rel, ".json") && annotation.ValidSourceID(name)
	}); err != nil {
		return nil, err
	}
	data, attrs, err := objects.Read(ctx, prefix+sourcestatus.Path, 0, generation.MaxFileBytes)
	if isObjectNotFound(err) {
		return []sourceSnapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := budget.reserve(attrs.Size); err != nil {
		return nil, annotateError(errCloudObjectRead, err)
	}
	if int64(len(data)) != attrs.Size {
		return nil, annotateError(errCloudObjectRead, fmt.Errorf("size mismatch body=%d attrs=%d", len(data), attrs.Size))
	}
	if err := writeCloudFile(workspace, sourcestatus.Path, data); err != nil {
		return nil, err
	}
	snapshots, err := snapshotCanonicalCloudSources(workspace, data)
	return snapshots, err
}

func snapshotCanonicalCloudSources(workspace string, data []byte) ([]sourceSnapshot, error) {
	artifact, err := sourcestatus.Decode(data)
	if err != nil {
		return nil, annotateError(errCloudSourceStatusInvalid, err)
	}
	if artifact.Version != 1 {
		return nil, annotateError(errCloudSourceStatusInvalid, fmt.Errorf("unsupported source status version %d", artifact.Version))
	}
	snapshots := make([]sourceSnapshot, 0, len(artifact.Sources))
	for sourceID, receipt := range artifact.Sources {
		if !validCloudReceipt(sourceID, receipt) {
			return nil, errors.New("invalid source status")
		}
		raw, err := readRegularFileWithin(workspace, receipt.RawPath)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, sourceSnapshot{SourceID: sourceID, RawPath: receipt.RawPath, Tombstone: true})
			continue
		}
		if err != nil {
			return nil, err
		}
		ann, err := readAnnotation(workspace, sourceID, receipt.RawPath)
		if err != nil {
			return nil, err
		}
		rawSum := sha256.Sum256(raw)
		rawSHA := fmt.Sprintf("%x", rawSum[:])
		fingerprint := sourcestatus.Fingerprint(rawSHA, ann.SHA256)
		snapshot := sourceSnapshot{
			SourceID: sourceID, RawPath: receipt.RawPath, RawBytes: raw, RawSHA256: rawSHA,
			AnnotationBody: ann.Body, AnnotationSHA: ann.SHA256, Fingerprint: fingerprint,
			Dirty: !sourcestatus.ValidReceipt(receipt, receipt.RawPath) || receipt.LastIngestFingerprint != fingerprint,
		}
		snapshot.SyntoContentHash = syntoSourceContentHash(snapshot)
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SourceID < snapshots[j].SourceID })
	return snapshots, nil
}

func materializeLegacyCloudOutputs(ctx context.Context, objects objectStore, prefix, workspace string, budget *cloudMaterializationBudget) error {
	for _, entry := range []struct {
		prefix string
		keep   func(string) bool
	}{
		{"wiki/", generation.GenerationOwned},
		{"cache/", func(rel string) bool { return generation.GenerationOwned(rel) && rel != sourcestatus.Path }},
		{".olw/", generation.GenerationOwned},
		{".synto/", generation.GenerationOwned},
	} {
		if err := materializeCloudObjects(ctx, objects, prefix, workspace, entry.prefix, budget, entry.keep); err != nil {
			return err
		}
	}
	for _, config := range []string{"wiki.toml"} {
		data, attrs, err := objects.Read(ctx, prefix+config, 0, generation.MaxFileBytes)
		if isObjectNotFound(err) {
			continue
		}
		if err != nil {
			return annotateError(errCloudObjectRead, err)
		}
		if reserveErr := budget.reserve(attrs.Size); reserveErr != nil {
			return annotateError(errCloudObjectRead, reserveErr)
		}
		if int64(len(data)) != attrs.Size {
			return annotateError(errCloudObjectRead, fmt.Errorf("size mismatch body=%d attrs=%d path=%s", len(data), attrs.Size, config))
		}
		if err := writeCloudFile(workspace, config, data); err != nil {
			return err
		}
	}
	return nil
}
func writeCloudFile(root, rel string, b []byte) error {
	if err := safeRelativePath(rel); err != nil {
		return err
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0644)
}

func publishCloudGeneration(ctx context.Context, objects objectStore, prefix, workspace string, snapshots []sourceSnapshot) (generation.Manifest, int64, error) {
	files, err := preflightGenerationOutputs(workspace)
	if err != nil {
		return generation.Manifest{}, 0, fmt.Errorf("generation output validation failed: %w", err)
	}
	oldData, oldAttrs, oldErr := objects.Read(ctx, prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
	if oldErr != nil && !isObjectNotFound(oldErr) {
		return generation.Manifest{}, 0, oldErr
	}
	return publishCloudGenerationWithFiles(ctx, objects, prefix, workspace, snapshots, files, oldData, oldAttrs, oldErr == nil, false, "")
}
func publishCloudGenerationFromStart(ctx context.Context, objects objectStore, prefix, workspace string, snapshots []sourceSnapshot, oldData []byte, oldAttrs objectAttrs, oldExists, preservePreviousSourceSnapshot bool, localExecutionID string) (generation.Manifest, int64, error) {
	files, err := preflightGenerationOutputs(workspace)
	if err != nil {
		return generation.Manifest{}, 0, fmt.Errorf("generation output validation failed: %w", err)
	}
	return publishCloudGenerationWithFiles(ctx, objects, prefix, workspace, snapshots, files, oldData, oldAttrs, oldExists, preservePreviousSourceSnapshot, localExecutionID)
}
func publishCloudGenerationWithFiles(ctx context.Context, objects objectStore, prefix, workspace string, snapshots []sourceSnapshot, files []generationOutput, oldData []byte, oldAttrs objectAttrs, oldExists, preservePreviousSourceSnapshot bool, localExecutionID string) (generation.Manifest, int64, error) {
	var old generation.Manifest
	if oldExists {
		var err error
		old, err = generation.Decode(oldData)
		if err != nil {
			return generation.Manifest{}, 0, err
		}
	}
	id, err := newGenerationID()
	if err != nil {
		return generation.Manifest{}, 0, err
	}
	m := generation.Manifest{Version: generation.Version, GenerationID: id, LocalExecutionID: localExecutionID, CreatedAt: time.Now().UTC().Format(time.RFC3339), InputFingerprint: snapshotFingerprint(snapshots)}
	if old.GenerationID != "" {
		m.PreviousGenerationID = old.GenerationID
	}
	for _, file := range files {
		b, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(file.path)))
		if err != nil {
			return generation.Manifest{}, 0, annotateError(errCloudGenerationOutputRead, err)
		}
		digest := digestBytes(b)
		if int64(len(b)) != file.size || digest != file.sha256 {
			return generation.Manifest{}, 0, errors.New("generation output changed after validation")
		}
		a, err := objects.Write(ctx, prefix+generation.Prefix+id+"/"+file.path, b, map[string]string{"sha256": digest}, objectConditions{DoesNotExist: true})
		if err != nil {
			return generation.Manifest{}, 0, annotateError(errCloudGenerationUpload, err)
		}
		if a.Size != file.size || a.Metadata["sha256"] != file.sha256 || a.Generation <= 0 {
			return generation.Manifest{}, 0, annotateError(errCloudGenerationUpload, fmt.Errorf("upload attrs mismatch path=%s size=%d gen=%d", file.path, a.Size, a.Generation))
		}
		f := generation.File{Path: file.path, Size: file.size, SHA256: file.sha256, Generation: a.Generation}
		m.Files = append(m.Files, f)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	digest, err := publishGenerationSourceSnapshot(ctx, objects, prefix, workspace, m, snapshots, old, oldExists, preservePreviousSourceSnapshot)
	if err != nil {
		return generation.Manifest{}, 0, err
	}
	m.SourceSnapshotDigest = digest
	if err := m.Validate(); err != nil {
		return generation.Manifest{}, 0, err
	}
	data, _ := json.Marshal(m)
	archivePath, err := generation.ArchivedManifestPath(m.GenerationID)
	if err != nil {
		return generation.Manifest{}, 0, err
	}
	if _, err := writeImmutableCloudObject(ctx, objects, prefix+archivePath, data, digestBytes(data)); err != nil {
		return generation.Manifest{}, 0, fmt.Errorf("generation manifest archive failed: %w", err)
	}
	condition := objectConditions{DoesNotExist: true}
	if oldExists {
		condition = objectConditions{GenerationMatch: oldAttrs.Generation}
	}
	a, err := objects.Write(ctx, prefix+generation.ManifestPath, data, map[string]string{"sha256": digestBytes(data)}, condition)
	if err != nil {
		if committed, attrs, outcome := confirmManifestCommit(objects, prefix, data, m); outcome == nil && committed {
			return m, attrs.Generation, nil
		} else if errors.Is(outcome, errManifestCommitOutcomeUnknown) {
			return generation.Manifest{}, 0, errManifestCommitOutcomeUnknown
		}
		return generation.Manifest{}, 0, fmt.Errorf("generation manifest commit conflicted: %w", errObjectGenerationConflict)
	}
	return m, a.Generation, nil
}

func publishGenerationSourceSnapshot(ctx context.Context, objects objectStore, prefix, workspace string, manifest generation.Manifest, snapshots []sourceSnapshot, previous generation.Manifest, hasPrevious, preservePrevious bool) (string, error) {
	if preservePrevious {
		return retainUnchangedGenerationSourceSnapshot(ctx, objects, prefix, workspace, manifest, previous, hasPrevious)
	}
	statusBytes, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(sourcestatus.Path)))
	if errors.Is(err, os.ErrNotExist) && len(snapshots) == 0 {
		// Compatibility for older direct callers that do not materialize source receipts.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read source snapshot receipt: %w", err)
	}
	status, err := sourcestatus.Decode(statusBytes)
	if err != nil || status.Version != 1 {
		return "", fmt.Errorf("invalid source snapshot receipt")
	}
	if len(statusBytes) > generation.MaxFileBytes {
		return "", errors.New("source snapshot receipt exceeds limit")
	}
	idMapBytes, err := os.ReadFile(filepath.Join(workspace, "cache", "id_map.json"))
	if err != nil {
		return "", fmt.Errorf("read source snapshot ID map: %w", err)
	}
	idMapFile, ok := manifest.File("cache/id_map.json")
	if !ok || int64(len(idMapBytes)) != idMapFile.Size || digestBytes(idMapBytes) != idMapFile.SHA256 {
		return "", errors.New("source snapshot ID map differs from generation manifest")
	}
	idMap, err := wikiindex.DecodeIDMap(idMapBytes)
	if err != nil {
		return "", fmt.Errorf("decode source snapshot ID map: %w", err)
	}
	hasSourceSnapshot := false
	for _, snapshot := range snapshots {
		hasSourceSnapshot = hasSourceSnapshot || !snapshot.Tombstone
	}
	if len(idMap.Source) == 0 && (len(idMap.SourceMeta) > 0 || hasSourceSnapshot) {
		// Old manifests may carry source metadata without the canonical source-ID map.
		// Keep baseline publication working, but do not claim Profile coverage.
		return "", nil
	}
	sourceStatusDigest := digestBytes(statusBytes)
	rowsByID := make(map[string]sourceSnapshot, len(snapshots))
	tombstones := make(map[string]bool)
	for _, snapshot := range snapshots {
		if !annotation.ValidSourceID(snapshot.SourceID) {
			return "", errors.New("source snapshot has invalid stable ID")
		}
		if snapshot.Tombstone {
			tombstones[snapshot.SourceID] = true
			continue
		}
		if _, exists := rowsByID[snapshot.SourceID]; exists {
			return "", fmt.Errorf("duplicate source snapshot ID %q", snapshot.SourceID)
		}
		if !storage.SafeRawPath(snapshot.RawPath) || digestBytes(snapshot.RawBytes) != snapshot.RawSHA256 {
			return "", fmt.Errorf("invalid pinned source snapshot %q", snapshot.SourceID)
		}
		rowsByID[snapshot.SourceID] = snapshot
	}
	previousRows := map[string]generation.SourceSnapshotRow{}
	if hasPrevious && previous.SourceSnapshotDigest != "" {
		path, err := generation.SourceSnapshotPath(previous.SourceSnapshotDigest)
		if err != nil {
			return "", err
		}
		data, attrs, err := objects.Read(ctx, prefix+path, 0, generation.MaxSourceSnapshotBytes)
		if err != nil {
			return "", fmt.Errorf("read previous source snapshot: %w", err)
		}
		if attrs.Size != int64(len(data)) || digestBytes(data) != previous.SourceSnapshotDigest {
			return "", errors.New("previous source snapshot digest mismatch")
		}
		prior, err := generation.DecodeSourceSnapshot(data)
		previousIDMap, hasIDMap := previous.File("cache/id_map.json")
		if err != nil || prior.ContentGeneration != previous.GenerationID || !hasIDMap || prior.IDMapDigest != previousIDMap.SHA256 {
			return "", errors.New("invalid previous source snapshot")
		}
		for _, row := range prior.Rows {
			previousRows[row.StableID] = row
		}
	}

	activeIDs := make([]string, 0, len(idMap.Source))
	for id := range idMap.Source {
		activeIDs = append(activeIDs, id)
	}
	sort.Strings(activeIDs)
	rows := make([]generation.SourceSnapshotRow, 0, len(activeIDs))
	for _, id := range activeIDs {
		if !annotation.ValidSourceID(id) {
			return "", errors.New("source ID map has invalid stable ID")
		}
		if tombstones[id] {
			return "", fmt.Errorf("active source %q is missing its pinned raw bytes", id)
		}
		meta := idMap.SourceMeta[id]
		path := meta.SourceFile
		if path != strings.TrimSpace(path) || path != "" && !storage.SafeRawPath(path) {
			return "", fmt.Errorf("invalid source path for %q", id)
		}
		if snapshot, exists := rowsByID[id]; exists {
			if path != "" && path != snapshot.RawPath {
				return "", fmt.Errorf("source path mismatch for %q", id)
			}
			path = snapshot.RawPath
			receipt, ok := status.Sources[id]
			if !ok || !sourcestatus.ValidReceipt(receipt, path) || receipt.LastIngestedRawSHA256 != snapshot.RawSHA256 {
				return "", fmt.Errorf("source receipt missing or mismatched for %q", id)
			}
			objectPath, err := generation.SourceBytesPath(snapshot.RawSHA256)
			if err != nil {
				return "", err
			}
			attrs, err := writeImmutableCloudObject(ctx, objects, prefix+objectPath, snapshot.RawBytes, snapshot.RawSHA256)
			if err != nil {
				return "", fmt.Errorf("source bytes publish failed for %q: %w", id, err)
			}
			rows = append(rows, generation.SourceSnapshotRow{StableID: id, RawPath: path, ContentDigest: snapshot.RawSHA256, ObjectGeneration: attrs.Generation})
			continue
		}
		prior, exists := previousRows[id]
		if !exists || previous.SourceSnapshotDigest == "" {
			return "", fmt.Errorf("historical source bytes unavailable for %q", id)
		}
		if path != "" && path != prior.RawPath {
			return "", fmt.Errorf("retained source path mismatch for %q", id)
		}
		path = prior.RawPath
		receipt, ok := status.Sources[id]
		if !ok || !sourcestatus.ValidReceipt(receipt, path) || receipt.LastIngestedRawSHA256 != prior.ContentDigest {
			return "", fmt.Errorf("retained source receipt missing or mismatched for %q", id)
		}
		bytesPath, err := generation.SourceBytesPath(prior.ContentDigest)
		if err != nil {
			return "", err
		}
		data, attrs, err := objects.Read(ctx, prefix+bytesPath, prior.ObjectGeneration, generation.MaxFileBytes)
		if err != nil || attrs.Size != int64(len(data)) || digestBytes(data) != prior.ContentDigest {
			return "", fmt.Errorf("retained source bytes unavailable or mismatched for %q", id)
		}
		prior.RawPath = path
		rows = append(rows, prior)
	}
	seenPaths := make(map[string]string, len(rows))
	for _, row := range rows {
		if prior, exists := seenPaths[row.RawPath]; exists {
			return "", fmt.Errorf("duplicate source path for %q and %q", prior, row.StableID)
		}
		seenPaths[row.RawPath] = row.StableID
	}

	inventory := generation.SourceSnapshotManifest{
		SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: manifest.GenerationID,
		IDMapDigest: idMapFile.SHA256, SourceStatusDigest: sourceStatusDigest, Rows: rows,
	}
	data, digest, err := generation.EncodeSourceSnapshot(inventory)
	if err != nil {
		return "", err
	}
	path, err := generation.SourceSnapshotPath(digest)
	if err != nil {
		return "", err
	}
	if _, err := writeImmutableCloudObject(ctx, objects, prefix+path, data, digest); err != nil {
		return "", fmt.Errorf("source snapshot publish failed: %w", err)
	}
	return digest, nil
}

func retainUnchangedGenerationSourceSnapshot(ctx context.Context, objects objectStore, prefix, workspace string, manifest, previous generation.Manifest, hasPrevious bool) (string, error) {
	if !hasPrevious || previous.SourceSnapshotDigest == "" {
		// Legacy generations without a Profile source snapshot keep baseline behavior.
		return "", nil
	}
	if !sameGenerationFilesExceptSuggestions(manifest.Files, previous.Files) {
		return "", errors.New("output-only generation changed content outside suggested queries")
	}
	idMapBytes, err := os.ReadFile(filepath.Join(workspace, "cache", "id_map.json"))
	if err != nil {
		return "", fmt.Errorf("read retained source ID map: %w", err)
	}
	idMapFile, ok := manifest.File("cache/id_map.json")
	previousIDMapFile, previousHasIDMap := previous.File("cache/id_map.json")
	if !ok || !previousHasIDMap || idMapFile.SHA256 != previousIDMapFile.SHA256 || int64(len(idMapBytes)) != idMapFile.Size || digestBytes(idMapBytes) != idMapFile.SHA256 {
		return "", errors.New("output-only generation changed source ID map")
	}
	idMap, err := wikiindex.DecodeIDMap(idMapBytes)
	if err != nil {
		return "", fmt.Errorf("decode retained source ID map: %w", err)
	}
	priorPath, err := generation.SourceSnapshotPath(previous.SourceSnapshotDigest)
	if err != nil {
		return "", err
	}
	priorBytes, priorAttrs, err := objects.Read(ctx, prefix+priorPath, 0, generation.MaxSourceSnapshotBytes)
	if err != nil || priorAttrs.Size != int64(len(priorBytes)) || digestBytes(priorBytes) != previous.SourceSnapshotDigest {
		return "", errors.New("retained source inventory is missing or mismatched")
	}
	priorInventory, err := generation.DecodeSourceSnapshot(priorBytes)
	if err != nil || priorInventory.ContentGeneration != previous.GenerationID || priorInventory.IDMapDigest != idMapFile.SHA256 {
		return "", errors.New("retained source inventory does not match previous generation")
	}
	previousRows := make(map[string]generation.SourceSnapshotRow, len(priorInventory.Rows))
	for _, row := range priorInventory.Rows {
		previousRows[row.StableID] = row
	}
	activeIDs := make([]string, 0, len(idMap.Source))
	for id := range idMap.Source {
		activeIDs = append(activeIDs, id)
	}
	sort.Strings(activeIDs)
	if len(activeIDs) != len(previousRows) {
		return "", errors.New("output-only generation changed source identity set")
	}
	rows := make([]generation.SourceSnapshotRow, 0, len(activeIDs))
	for _, id := range activeIDs {
		if !annotation.ValidSourceID(id) {
			return "", errors.New("retained source ID map has invalid stable ID")
		}
		row, ok := previousRows[id]
		if !ok {
			return "", fmt.Errorf("retained source inventory is missing %q", id)
		}
		if path := idMap.SourceMeta[id].SourceFile; path != "" && path != row.RawPath {
			return "", fmt.Errorf("retained source path mismatch for %q", id)
		}
		bytesPath, err := generation.SourceBytesPath(row.ContentDigest)
		if err != nil {
			return "", err
		}
		data, attrs, err := objects.Read(ctx, prefix+bytesPath, row.ObjectGeneration, generation.MaxFileBytes)
		if err != nil || attrs.Size != int64(len(data)) || digestBytes(data) != row.ContentDigest {
			return "", fmt.Errorf("retained source bytes unavailable or mismatched for %q", id)
		}
		rows = append(rows, row)
	}
	inventory := generation.SourceSnapshotManifest{
		SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: manifest.GenerationID,
		IDMapDigest: idMapFile.SHA256, SourceStatusDigest: priorInventory.SourceStatusDigest, Rows: rows,
	}
	data, digest, err := generation.EncodeSourceSnapshot(inventory)
	if err != nil {
		return "", err
	}
	path, err := generation.SourceSnapshotPath(digest)
	if err != nil {
		return "", err
	}
	if _, err := writeImmutableCloudObject(ctx, objects, prefix+path, data, digest); err != nil {
		return "", fmt.Errorf("retained source snapshot publish failed: %w", err)
	}
	return digest, nil
}

func sameGenerationFilesExceptSuggestions(current, previous []generation.File) bool {
	const changedPath = "cache/suggested_queries.json"
	if len(current) != len(previous) {
		return false
	}
	currentByPath := make(map[string]generation.File, len(current))
	for _, file := range current {
		currentByPath[file.Path] = file
	}
	for _, oldFile := range previous {
		newFile, ok := currentByPath[oldFile.Path]
		if !ok {
			return false
		}
		if oldFile.Path == changedPath {
			continue
		}
		if oldFile.Size != newFile.Size || oldFile.SHA256 != newFile.SHA256 {
			return false
		}
	}
	_, oldSuggestions := previousFile(previous, changedPath)
	_, newSuggestions := previousFile(current, changedPath)
	return oldSuggestions && newSuggestions
}

func previousFile(files []generation.File, path string) (generation.File, bool) {
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return generation.File{}, false
}

func writeImmutableCloudObject(ctx context.Context, objects objectStore, name string, data []byte, digest string) (objectAttrs, error) {
	attrs, err := objects.Write(ctx, name, data, map[string]string{"sha256": digest}, objectConditions{DoesNotExist: true})
	if err != nil && !errors.Is(err, errObjectGenerationConflict) {
		return objectAttrs{}, err
	}
	if errors.Is(err, errObjectGenerationConflict) {
		stored, existing, readErr := objects.Read(ctx, name, 0, generation.MaxFileBytes)
		if readErr != nil || existing.Size != int64(len(stored)) || !bytes.Equal(stored, data) || digestBytes(stored) != digest {
			return objectAttrs{}, errors.New("existing immutable object does not match")
		}
		attrs = existing
	} else if err != nil {
		return objectAttrs{}, err
	}
	if attrs.Size != int64(len(data)) || attrs.Generation <= 0 || attrs.Metadata["sha256"] != digest {
		return objectAttrs{}, errors.New("immutable object attributes mismatch")
	}
	readback, readAttrs, err := objects.Read(ctx, name, attrs.Generation, int64(len(data)))
	if err != nil || readAttrs.Generation != attrs.Generation || int64(len(readback)) != int64(len(data)) || digestBytes(readback) != digest || !bytes.Equal(readback, data) {
		return objectAttrs{}, errors.New("immutable object readback mismatch")
	}
	return attrs, nil
}

func confirmManifestCommit(objects objectStore, prefix string, proposed []byte, manifest generation.Manifest) (bool, objectAttrs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudManifestReadbackTimeout)
	defer cancel()
	data, attrs, err := objects.Read(ctx, prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
	if err != nil {
		if isObjectNotFound(err) {
			return false, objectAttrs{}, nil
		}
		return false, objectAttrs{}, errManifestCommitOutcomeUnknown
	}
	if bytes.Equal(data, proposed) {
		return true, attrs, nil
	}
	readback, err := generation.Decode(data)
	if err != nil || !reflect.DeepEqual(readback, manifest) {
		return false, objectAttrs{}, nil
	}
	return true, attrs, nil
}

type generationOutput struct {
	path   string
	size   int64
	sha256 string
}

var walkGenerationDir = filepath.WalkDir

type generationTraversalLimits struct {
	entries, directories, depth, symlinks, nonRegular int
	bytes                                             int64
}

const (
	maxGenerationWorkspaceEntries     = generation.MaxFiles + 128
	maxGenerationWorkspaceDirectories = generation.MaxFiles
	maxGenerationWorkspaceDepth       = 64
)

var defaultGenerationTraversalLimits = generationTraversalLimits{
	entries:     maxGenerationWorkspaceEntries,
	directories: maxGenerationWorkspaceDirectories,
	depth:       maxGenerationWorkspaceDepth,
	bytes:       generation.MaxTotalSize,
}

// preflightGenerationOutputs creates the complete bounded file table before
// the first immutable object is written. It deliberately never includes raw,
// annotations, status or diagnostics in the generation.
func preflightGenerationOutputs(root string) ([]generationOutput, error) {
	return preflightGenerationOutputsWithLimits(root, defaultGenerationTraversalLimits)
}

func preflightGenerationOutputsWithLimits(root string, limits generationTraversalLimits) ([]generationOutput, error) {
	var files []generationOutput
	seen := map[string]bool{}
	var total, traversedBytes int64
	entries, directories, symlinks, nonRegular := 0, 0, 0, 0
	err := walkGenerationDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		entries++
		if entries > limits.entries {
			return errors.New("workspace traversal exceeds entry limit")
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if depth := len(strings.Split(rel, "/")); depth > limits.depth {
			return errors.New("workspace traversal exceeds depth limit")
		}
		if err := safeRelativePath(rel); err != nil || !generation.GenerationOwned(rel) && !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			// Non-owned canonical inputs are permitted in the private workspace;
			// unsafe entries are not.
			if err != nil {
				return err
			}
		}
		if d.Type()&os.ModeSymlink != 0 {
			symlinks++
			if symlinks > limits.symlinks {
				return errors.New("workspace traversal contains too many symlinks")
			}
			return errors.New("generation contains symlink")
		}
		if d.IsDir() {
			directories++
			if directories > limits.directories {
				return errors.New("workspace traversal exceeds directory limit")
			}
			return nil
		}
		if !d.Type().IsRegular() {
			nonRegular++
			if nonRegular > limits.nonRegular {
				return errors.New("workspace traversal contains too many non-regular entries")
			}
			return errors.New("generation contains special file")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || traversedBytes > limits.bytes-info.Size() {
			return errors.New("workspace traversal exceeds byte limit")
		}
		traversedBytes += info.Size()
		if !generation.GenerationOwned(rel) {
			return nil
		}
		// Historical Synto TOMLs remain decodable from manifests, but new
		// generations no longer publish them as runtime configuration.
		if rel == "synto.toml" {
			return nil
		}
		if info.Size() > generation.MaxFileBytes {
			return errors.New("generation output too large")
		}
		// Stop at MaxFiles+1 before hashing or appending another output; a child
		// can otherwise force an unbounded in-memory file table.
		if len(files) >= generation.MaxFiles {
			return errors.New("too many generation files")
		}
		total += info.Size()
		if total > generation.MaxTotalSize {
			return errors.New("generation output too large")
		}
		digest, err := digestGenerationFile(p, info.Size())
		if err != nil {
			return err
		}
		files = append(files, generationOutput{path: rel, size: info.Size(), sha256: digest})
		seen[rel] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"cache/id_map.json", "cache/concepts.jsonl", "cache/dormant_concepts.jsonl", "cache/raw_status.json", "cache/suggested_queries.json", ".synto/state.db", ".synto/INDEX.json"} {
		if !seen[required] {
			return nil, errors.New("generation output is incomplete")
		}
	}
	legacyConfig, legacyState := seen["wiki.toml"], seen[".olw/state.db"]
	if legacyConfig != legacyState {
		return nil, errors.New("migrated generation rollback artifacts are incomplete")
	}
	for _, state := range []string{".synto/state.db", ".olw/state.db"} {
		if seen[state] {
			if err := validateSQLiteArtifact(root, state); err != nil {
				return nil, fmt.Errorf("invalid %s: %w", state, err)
			}
		}
	}
	if _, err := readSyntoIndexTruth(root); err != nil {
		return nil, fmt.Errorf("invalid .synto/INDEX.json: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}
func generationOutputFiles(root string) ([]string, error) {
	files, err := preflightGenerationOutputs(root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	return paths, nil
}
func digestGenerationFile(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", fmt.Errorf("generation output digest failed: %w", err)
	}
	if n != size {
		return "", fmt.Errorf("generation output digest failed: size mismatch copied=%d want=%d", n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func newGenerationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "g_" + hex.EncodeToString(b), nil
}
func digestBytes(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func snapshotFingerprint(s []sourceSnapshot) string {
	h := sha256.New()
	for _, x := range s {
		if x.Tombstone {
			continue
		}
		_, _ = io.WriteString(h, x.SourceID+"\x00"+x.Fingerprint+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
func writeCloudReceipts(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig, snapshots []sourceSnapshot) error {
	if err := writeCloudPipelineLog(ctx, objects, prefix, workspace, cfg); err != nil {
		warnCloudFailurePipelineLog(cfg, newWorkerFailure(ctx, failureStageReceiptRecording, failureClassRecordingFailure, "", err), nil)
	}
	return mergeCloudSuccess(ctx, objects, prefix, snapshots)
}

func writeLocalPublicationReceipt(ctx context.Context, objects objectStore, prefix string, cfg workerConfig, manifest generation.Manifest, manifestGeneration int64) error {
	receipt := localcloud.PublicationReceipt{
		ExecutionID: cfg.ExecutionID, GenerationID: manifest.GenerationID,
		ManifestGeneration: manifestGeneration,
	}
	data, err := localcloud.EncodePublicationReceipt(receipt)
	if err != nil {
		return err
	}
	relPath, err := localcloud.PublicationReceiptPath(cfg.ExecutionID)
	if err != nil {
		return err
	}
	_, err = objects.Write(ctx, prefix+relPath, data, map[string]string{"execution_id": cfg.ExecutionID, "generation_id": manifest.GenerationID}, objectConditions{DoesNotExist: true})
	return err
}

func localExecutionIDFor(cfg workerConfig) string {
	return cfg.ExecutionID
}
func writeCloudFailureReceipts(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig, snapshots []sourceSnapshot, failure error, secrets ...[]string) error {
	recordingCtx, cancel := cloudFailureRecordingContext(ctx)
	defer cancel()
	var failureSecrets []string
	if len(secrets) > 0 {
		failureSecrets = secrets[0]
	}
	_ = recordCloudFailurePipelineLog(recordingCtx, objects, prefix, workspace, cfg, failure, failureSecrets)
	statusErr := mergeCloudFailure(recordingCtx, objects, prefix, snapshots)
	diagnosticErr := writeCloudFailureDiagnosticWithContext(recordingCtx, objects, prefix, cfg, failure)
	if statusErr == nil && diagnosticErr == nil {
		return nil
	}
	return cloudFailureRecordingError{sourceStatus: statusErr != nil, failureDiagnostic: diagnosticErr != nil}
}

func writeCloudFailureLogAndDiagnostic(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig, failure error, secrets ...[]string) error {
	recordingCtx, cancel := cloudFailureRecordingContext(ctx)
	defer cancel()
	var failureSecrets []string
	if len(secrets) > 0 {
		failureSecrets = secrets[0]
	}
	_ = recordCloudFailurePipelineLog(recordingCtx, objects, prefix, workspace, cfg, failure, failureSecrets)
	diagnosticErr := writeCloudFailureDiagnosticWithContext(recordingCtx, objects, prefix, cfg, failure)
	if diagnosticErr == nil {
		return nil
	}
	return cloudFailureRecordingError{failureDiagnostic: true}
}

func recordCloudAmbiguousManifestFailure(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig) error {
	failure := newWorkerFailure(nil, failureStageGenerationPublish, failureClassIO, "", errManifestCommitOutcomeUnknown)
	recordingCtx, cancel := cloudFailureRecordingContext(ctx)
	defer cancel()
	_ = recordCloudFailurePipelineLog(recordingCtx, objects, prefix, workspace, cfg, failure, nil)
	if err := writeCloudFailureDiagnosticWithContext(recordingCtx, objects, prefix, cfg, failure); err != nil {
		return cloudFailureRecordingError{failureDiagnostic: true}
	}
	return nil
}

func recordCloudFailurePipelineLog(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig, failure error, secrets []string) (err error) {
	// ponytail: global lock; use per-log keys only if worker throughput makes this measurable.
	workerFailureLogMu.Lock()
	defer workerFailureLogMu.Unlock()
	defer func() {
		if err != nil {
			warnCloudFailurePipelineLog(cfg, failure, secrets)
		}
	}()
	return recordCloudFailurePipelineLogLocked(ctx, objects, prefix, workspace, cfg, failure, secrets)
}

func recordCloudFailurePipelineLogLocked(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig, failure error, secrets []string) error {
	if workspace != "" {
		if err := appendWorkerFailurePipelineLogLocked(workspace, cfg, failure, secrets); err != nil {
			return err
		}
		return writeCloudPipelineLog(ctx, objects, prefix, workspace, cfg)
	}
	path := prefix + "cache/pipeline-" + cfg.ExecutionID + ".log"
	data, _, err := objects.Read(ctx, path, 0, maxPipelineLog+1)
	exists := err == nil
	if err != nil && !isObjectNotFound(err) {
		return err
	}
	if exists && hasWorkerFailureEvent(data) {
		return nil
	}
	data, err = workerFailurePipelineLogData(data, exists, cfg, failure, secrets)
	if err != nil {
		return err
	}
	_, err = objects.Write(ctx, path, data, nil, objectConditions{})
	return err
}

func recordLocalWorkerFailureLog(workspace, vault string, cfg workerConfig, failure error, published bool) (err error) {
	workerFailureLogMu.Lock()
	defer workerFailureLogMu.Unlock()
	defer func() {
		if err != nil {
			warnCloudFailurePipelineLog(cfg, failure, nil)
		}
	}()

	if workspace != "" && !published {
		if err = appendWorkerFailurePipelineLogLocked(workspace, cfg, failure, nil); err != nil {
			return err
		}
		var alreadyRecorded bool
		alreadyRecorded, err = localVaultHasWorkerFailure(vault, cfg)
		if err != nil || alreadyRecorded {
			return err
		}
		return publishWorkspaceFailureLog(workspace, vault, cfg)
	}
	return appendWorkerFailurePipelineLogLocked(vault, cfg, failure, nil)
}

func localVaultHasWorkerFailure(vault string, cfg workerConfig) (bool, error) {
	path, err := pipelineLogPath(vault, cfg.ExecutionID)
	if err != nil {
		return false, err
	}
	name := filepath.ToSlash(filepath.Join("cache", filepath.Base(path)))
	root, err := os.OpenRoot(vault)
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("failure log %q is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(maxPipelineLog)+1))
	closeErr := file.Close()
	if readErr != nil {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return hasWorkerFailureEvent(data), nil
}

type workerFailureLogRecord struct {
	Event      string                     `json:"event"`
	Stage      failureStage               `json:"stage"`
	ErrorClass failureErrorClass          `json:"error_class"`
	DetailCode conceptReconcileDetailCode `json:"detail_code,omitempty"`
	Child      failureChildCommand        `json:"child_command,omitempty"`
	ExitCode   *int                       `json:"exit_code,omitempty"`
	Cause      string                     `json:"cause"`
}

type workerStartLogRecord struct {
	Event       string `json:"event"`
	UserID      string `json:"user_id"`
	ProjectID   string `json:"project_id"`
	ExecutionID string `json:"execution_id"`
}

func appendWorkerFailurePipelineLog(workspace string, cfg workerConfig, failure error, secrets []string) (err error) {
	workerFailureLogMu.Lock()
	defer workerFailureLogMu.Unlock()
	defer func() {
		if err != nil {
			warnCloudFailurePipelineLog(cfg, failure, secrets)
		}
	}()
	return appendWorkerFailurePipelineLogLocked(workspace, cfg, failure, secrets)
}

func appendWorkerFailurePipelineLogLocked(workspace string, cfg workerConfig, failure error, secrets []string) (err error) {
	path, err := pipelineLogPath(workspace, cfg.ExecutionID)
	if err != nil {
		return err
	}
	rel := filepath.ToSlash(filepath.Join("cache", filepath.Base(path)))
	if err := safeRelativePath(rel); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.FromSlash(rel))
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("pipeline log is not a regular file")
	}
	var data []byte
	if exists {
		file, err := root.Open(filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		data, err = io.ReadAll(io.LimitReader(file, int64(maxPipelineLog)+1))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if hasWorkerFailureEvent(data) {
		return nil
	}
	data, err = workerFailurePipelineLogData(data, exists, cfg, failure, secrets)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if exists {
		perm = info.Mode().Perm()
	}
	return atomicRootWrite(root, rel, data, perm)
}

func hasWorkerFailureEvent(data []byte) bool {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var record struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &record) == nil && record.Event == "worker_failure" {
			return true
		}
	}
	return false
}

func workerFailurePipelineLogData(data []byte, exists bool, cfg workerConfig, failure error, secrets []string) ([]byte, error) {
	diagnostic := diagnosticForError(failure)
	secrets = append(logSecrets(cfg), secrets...)
	if !exists {
		startup, err := json.Marshal(workerStartLogRecord{
			Event: "worker_start", UserID: cfg.UserID, ProjectID: cfg.ProjectID, ExecutionID: cfg.ExecutionID,
		})
		if err != nil {
			return nil, err
		}
		data = append(redactDiagnosticBytes(startup, secrets), '\n')
	}
	confirmedOverflow := len(data) > maxPipelineLog
	if confirmedOverflow {
		data = data[:maxPipelineLog]
	}
	marker := []byte(pipelineLogTruncationMarker)
	for bytes.HasSuffix(data, marker) {
		data = data[:len(data)-len(marker)]
		confirmedOverflow = true
	}
	cause := truncateDiagnostic(string(redactDiagnosticBytes([]byte(failure.Error()), secrets)), maxWorkerArgBytes)
	record, err := json.Marshal(workerFailureLogRecord{
		Event:      "worker_failure",
		Stage:      diagnostic.Stage,
		ErrorClass: diagnostic.ErrorClass,
		DetailCode: diagnostic.DetailCode,
		Child:      diagnostic.Child,
		ExitCode:   diagnostic.ExitCode,
		Cause:      cause,
	})
	if err != nil {
		return nil, err
	}
	record = append(redactDiagnosticBytes(record, secrets), '\n')
	data, err = composeWorkerFailurePipelineLog(data, record, confirmedOverflow)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func composeWorkerFailurePipelineLog(child, record []byte, confirmedOverflow bool) ([]byte, error) {
	marker := []byte(pipelineLogTruncationMarker)
	if len(record)+len(marker) > maxPipelineLog {
		return nil, fmt.Errorf("worker failure record cannot fit in pipeline log")
	}
	separator := 0
	if len(child) > 0 && !bytes.HasSuffix(child, []byte{'\n'}) {
		separator = 1
	}
	if !confirmedOverflow && len(child)+separator+len(record) <= maxPipelineLog {
		data := append([]byte(nil), child...)
		if separator != 0 {
			data = append(data, '\n')
		}
		return append(data, record...), nil
	}

	available := maxPipelineLog - len(record) - len(marker)
	keep := len(child)
	if keep > available {
		keep = available
	}
	prefix := append([]byte(nil), child[:keep]...)
	if len(prefix) > 0 && !bytes.HasSuffix(prefix, []byte{'\n'}) {
		if len(prefix) == available {
			prefix = prefix[:len(prefix)-1]
		}
		prefix = append(prefix, '\n')
	}
	data := append(prefix, record...)
	data = append(data, marker...)
	if len(data) > maxPipelineLog {
		return nil, fmt.Errorf("worker failure record cannot fit in pipeline log")
	}
	return data, nil
}

func writeCloudPipelineLog(ctx context.Context, objects objectStore, prefix, workspace string, cfg workerConfig) error {
	if !validPipelineExecutionID(cfg.ExecutionID) {
		return nil
	}
	data := []byte{}
	if workspace != "" {
		path, err := pipelineLogPath(workspace, cfg.ExecutionID)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(file, int64(maxPipelineLog)+1))
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			data = boundedPipelineLogData(data)
		}
	}
	_, err := objects.Write(ctx, prefix+"cache/pipeline-"+cfg.ExecutionID+".log", data, nil, objectConditions{})
	return err
}

func boundedPipelineLogData(data []byte) []byte {
	if len(data) <= maxPipelineLog {
		return data
	}
	limit := maxPipelineLog - len(pipelineLogTruncationMarker)
	return append(append([]byte(nil), data[:limit]...), []byte(pipelineLogTruncationMarker)...)
}

func writeCloudFailureDiagnostic(ctx context.Context, objects objectStore, prefix string, cfg workerConfig, failure error) error {
	recordingCtx, cancel := cloudFailureRecordingContext(ctx)
	defer cancel()
	return writeCloudFailureDiagnosticWithContext(recordingCtx, objects, prefix, cfg, failure)
}

func writeCloudFailureDiagnosticWithContext(ctx context.Context, objects objectStore, prefix string, cfg workerConfig, failure error) error {
	if !validPipelineExecutionID(cfg.ExecutionID) {
		return nil
	}
	data, err := marshalFailureDiagnosticMeta(failure, cfg.ExecutionID, logSecrets(cfg))
	if err != nil {
		return err
	}
	name := prefix + "cache/pipeline-" + cfg.ExecutionID + ".failure.json"
	_, err = objects.Write(ctx, name, data, nil, objectConditions{DoesNotExist: true})
	if err == nil {
		return nil
	}
	if !isObjectGenerationConflict(err) {
		return err
	}
	existing, _, readErr := objects.Read(ctx, name, 0, 4<<10)
	if readErr != nil {
		return err
	}
	if _, decodeErr := decodeFailureDiagnostic(existing); decodeErr != nil || !bytes.Equal(existing, data) {
		return err
	}
	return nil
}
func mergeCloudSuccess(ctx context.Context, objects objectStore, prefix string, snapshots []sourceSnapshot) error {
	return mergeCloudReceipts(ctx, objects, prefix, func(artifact *sourcestatus.Artifact) {
		for _, snapshot := range snapshots {
			if snapshot.Tombstone {
				continue
			}
			if !cloudSnapshotCurrent(ctx, objects, prefix, snapshot) {
				continue
			}
			artifact.Sources[snapshot.SourceID] = sourcestatus.Receipt{RawPath: snapshot.RawPath, LastIngestedRawSHA256: snapshot.RawSHA256, LastIngestedAnnSHA256: snapshot.AnnotationSHA, LastIngestFingerprint: snapshot.Fingerprint, LastSuccessAt: time.Now().UTC().Format(time.RFC3339)}
		}
	})
}

const cloudReceiptCASAttempts = 3

func mergeCloudFailure(ctx context.Context, objects objectStore, prefix string, snapshots []sourceSnapshot) error {
	return mergeCloudReceipts(ctx, objects, prefix, func(artifact *sourcestatus.Artifact) {
		for _, s := range snapshots {
			if s.Tombstone {
				continue
			}
			if !cloudSnapshotCurrent(ctx, objects, prefix, s) {
				continue
			}
			r := artifact.Sources[s.SourceID]
			r.RawPath, r.FailedFingerprint, r.Error = s.RawPath, s.Fingerprint, "pipeline failed"
			artifact.Sources[s.SourceID] = r
		}
	})
}

func mergeCloudReceipts(ctx context.Context, objects objectStore, prefix string, merge func(*sourcestatus.Artifact)) error {
	for attempt := 0; attempt < cloudReceiptCASAttempts; attempt++ {
		data, attrs, err := objects.Read(ctx, prefix+sourcestatus.Path, 0, generation.MaxFileBytes)
		artifact := sourcestatus.Artifact{Version: 1, Sources: map[string]sourcestatus.Receipt{}}
		if err == nil {
			decoded, decodeErr := sourcestatus.Decode(data)
			if decodeErr != nil {
				return annotateError(errCloudSourceReceiptInvalid, decodeErr)
			}
			artifact = decoded
			if normErr := normalizeCloudReceipts(&artifact); normErr != nil {
				return annotateError(errCloudSourceReceiptInvalid, normErr)
			}
		} else if !isObjectNotFound(err) {
			return annotateError(errCloudSourceReceiptRead, err)
		}
		merge(&artifact)
		out, marshalErr := json.Marshal(artifact)
		if marshalErr != nil {
			return annotateError(errCloudSourceReceiptWrite, marshalErr)
		}
		condition := objectConditions{DoesNotExist: true}
		if err == nil {
			condition = objectConditions{GenerationMatch: attrs.Generation}
		}
		if _, writeErr := objects.Write(ctx, prefix+sourcestatus.Path, out, nil, condition); writeErr == nil {
			return nil
		} else if !isObjectGenerationConflict(writeErr) {
			return annotateError(errCloudSourceReceiptWrite, writeErr)
		}
	}
	return errCloudSourceReceiptConflict
}

func normalizeCloudReceipts(artifact *sourcestatus.Artifact) error {
	if artifact.Version != 1 {
		return errCloudSourceReceiptInvalid
	}
	if artifact.Sources == nil {
		artifact.Sources = map[string]sourcestatus.Receipt{}
	}
	seenRaw := make(map[string]string, len(artifact.Sources))
	for sourceID, receipt := range artifact.Sources {
		if !validCloudReceipt(sourceID, receipt) {
			return errCloudSourceReceiptInvalid
		}
		if receipt.RawPath != "" {
			if prior, exists := seenRaw[receipt.RawPath]; exists && prior != sourceID {
				return errCloudSourceReceiptInvalid
			}
			seenRaw[receipt.RawPath] = sourceID
		}
	}
	return nil
}

func validCloudReceipt(sourceID string, receipt sourcestatus.Receipt) bool {
	if !annotation.ValidSourceID(sourceID) || len(sourceID) > maxWorkerIDBytes || !safeMappedRawPath(receipt.RawPath) {
		return false
	}
	for _, digest := range []string{receipt.LastIngestedRawSHA256, receipt.LastIngestedAnnSHA256, receipt.LastIngestFingerprint, receipt.FailedFingerprint} {
		if digest != "" && normalizeCloudDigest(digest) == "" {
			return false
		}
	}
	if receipt.LastSuccessAt != "" && normalizeCloudTimestamp(receipt.LastSuccessAt) == "" {
		return false
	}
	if sourcestatus.ValidReceipt(receipt, receipt.RawPath) {
		return receipt.Error == "" || receipt.Error == "pipeline failed"
	}
	return receipt.LastIngestedRawSHA256 == "" && receipt.LastIngestedAnnSHA256 == "" &&
		receipt.LastIngestFingerprint == "" && receipt.LastSuccessAt == "" &&
		receipt.Error == "pipeline failed" && normalizeCloudDigest(receipt.FailedFingerprint) != ""
}

func normalizeCloudRawPath(value string) string {
	if len(value) > generation.MaxPathBytes || !safeMappedRawPath(value) {
		return ""
	}
	return value
}

func normalizeCloudDigest(value string) string {
	if value == "" || len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return strings.ToLower(value)
}

func normalizeCloudTimestamp(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func cloudSnapshotCurrent(ctx context.Context, objects objectStore, prefix string, s sourceSnapshot) bool {
	raw, _, err := objects.Read(ctx, prefix+s.RawPath, 0, generation.MaxFileBytes)
	if err != nil || digestBytes(raw) != s.RawSHA256 {
		return false
	}
	b, _, err := objects.Read(ctx, prefix+annotation.Path(s.SourceID), 0, generation.MaxFileBytes)
	if isObjectNotFound(err) {
		return s.AnnotationSHA == annotation.Digest("")
	}
	if err != nil {
		return false
	}
	var a annotation.Object
	return json.Unmarshal(b, &a) == nil && a.Validate(s.SourceID, s.RawPath) == nil && a.SHA256 == s.AnnotationSHA
}
