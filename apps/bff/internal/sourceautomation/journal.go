package sourceautomation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrInvalidSourceCommit     = errors.New("invalid source commit")
	ErrSourceCommitConflict    = errors.New("source commit operation conflicts with a prior request")
	ErrSourceCommitUnconfirmed = errors.New("source commit could not be confirmed")
	ErrSourceCommitPending     = errors.New("source commit is waiting for the raw write")
)

const (
	sourceCommitReconcileDelay = time.Minute
	sourceCommitReconcileGrace = 15 * time.Minute
)

// VerifiedProject must be populated from the request's authenticated owner and
// Project authorizer before calling the journal.
type VerifiedProject struct {
	OwnerID   string
	ProjectID string
}

type PrepareRequest struct {
	OperationID               string
	Entrypoint                string
	RawPath                   string
	ExpectedSHA256            string
	ExpectedStorageGeneration string
	BatchID                   string
}

type Ref struct {
	OwnerID     string
	ProjectID   string
	OperationID string
}

type ConfirmRequest struct {
	Generation string
	Status     string
}

type CommitResult struct {
	State            string `json:"state"`
	ProcessingStatus string `json:"processing_status"`
	SourceRevision   int64  `json:"source_revision"`
}

// Journal is the shared producer contract for raw writers, including LWC-450.
// Prepare must precede the raw write; Confirm follows storage readback. Reconcile
// is used when a storage result is ambiguous.
type Journal interface {
	PrepareSourceCommit(context.Context, VerifiedProject, PrepareRequest) (Ref, error)
	ConfirmSourceCommit(context.Context, Ref, ConfirmRequest) (CommitResult, error)
	ReconcileSourceCommit(context.Context, Ref) (CommitResult, error)
}

type FirestoreJournal struct {
	fs    *cloudfirestore.Client
	store storage.RootStore
}

func NewJournal(fs *cloudfirestore.Client, root storage.RootStore) *FirestoreJournal {
	return &FirestoreJournal{fs: fs, store: root}
}

func (j *FirestoreJournal) PrepareSourceCommit(ctx context.Context, project VerifiedProject, request PrepareRequest) (Ref, error) {
	if j == nil || j.fs == nil || j.store == nil || !validProject(project) || !validPrepareRequest(&request) {
		return Ref{}, ErrInvalidSourceCommit
	}
	if request.OperationID == "" {
		id, err := newOperationID()
		if err != nil {
			return Ref{}, err
		}
		request.OperationID = id
	}
	ref := Ref{OwnerID: project.OwnerID, ProjectID: project.ProjectID, OperationID: request.OperationID}
	projectRef := projectDocument(j.fs, project)
	eventRef := projectRef.Collection("pipeline_source_events").Doc(request.OperationID)
	now := time.Now().UTC()
	err := j.fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		projectSnapshot, err := tx.Get(projectRef)
		if err != nil {
			return err
		}
		if !readyProject(projectSnapshot.Data(), project) {
			return ErrInvalidSourceCommit
		}
		existing, err := tx.Get(eventRef)
		if err == nil {
			if !samePrepare(existing.Data(), request) {
				return ErrSourceCommitConflict
			}
			state, _ := existing.Data()["commit_state"].(string)
			if state != "prepared" {
				return nil
			}
			createdAt, _ := existing.Data()["created_at"].(time.Time)
			if createdAt.IsZero() {
				createdAt = now
			}
			return enqueueSourceCommitReconcile(ctx, tx, j.fs, project, request.OperationID, createdAt.Add(sourceCommitReconcileDelay))
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
		if err := enqueueSourceCommitReconcile(ctx, tx, j.fs, project, request.OperationID, now.Add(sourceCommitReconcileDelay)); err != nil {
			return err
		}
		return tx.Create(eventRef, map[string]interface{}{
			"schema_version": 1, "operation_id": request.OperationID, "entrypoint": request.Entrypoint,
			"raw_path": request.RawPath, "expected_sha256": request.ExpectedSHA256,
			"batch_id": request.BatchID, "commit_state": "prepared", "created_at": now,
			"expected_storage_generation": request.ExpectedStorageGeneration,
			"reconcile_deadline":          now.Add(sourceCommitReconcileGrace),
			"updated_at":                  now,
		})
	})
	if err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func (j *FirestoreJournal) ConfirmSourceCommit(ctx context.Context, ref Ref, request ConfirmRequest) (CommitResult, error) {
	generation := strings.TrimSpace(request.Generation)
	if j == nil || j.fs == nil || j.store == nil || !validRef(ref) || !validGeneration(generation) ||
		(request.Status != "created" && request.Status != "replaced" && request.Status != "already_exists") {
		return CommitResult{}, ErrInvalidSourceCommit
	}
	eventRef := eventDocument(j.fs, ref)
	eventSnapshot, err := eventRef.Get(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	data := eventSnapshot.Data()
	path, _ := data["raw_path"].(string)
	expectedDigest, _ := data["expected_sha256"].(string)
	if storage.ValidateRawSyncPath(path) != nil || !storage.ValidRawSyncSHA256(expectedDigest) {
		return CommitResult{}, ErrInvalidSourceCommit
	}
	reader, ok := j.store.Scope(ref.OwnerID, ref.ProjectID).(storage.ConditionalWriter)
	if !ok {
		return CommitResult{}, ErrSourceCommitUnconfirmed
	}
	bytes, actualGeneration, err := reader.ReadFileWithGeneration(ctx, "raw/"+path)
	if err != nil {
		return CommitResult{}, err
	}
	actualDigest := digest(bytes)
	if actualDigest != expectedDigest || strconv.FormatInt(actualGeneration, 10) != generation {
		return CommitResult{State: "unknown", ProcessingStatus: "unknown"}, ErrSourceCommitUnconfirmed
	}

	currentRef := projectDocument(j.fs, VerifiedProject{OwnerID: ref.OwnerID, ProjectID: ref.ProjectID}).Collection("pipeline_source_current").Doc(pathDocumentID(path))
	projectRef := projectDocument(j.fs, VerifiedProject{OwnerID: ref.OwnerID, ProjectID: ref.ProjectID})
	now := time.Now().UTC()
	result := CommitResult{State: "committed", ProcessingStatus: "unknown"}
	err = j.fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		result.SourceRevision = 0
		currentSnapshot, err := tx.Get(currentRef)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		var current map[string]interface{}
		if currentSnapshot != nil && currentSnapshot.Exists() {
			current = currentSnapshot.Data()
		}
		storedGeneration, _ := current["generation"].(string)
		storedDigest, _ := current["sha256"].(string)
		latest := current == nil || generationAtLeast(generation, storedGeneration)
		changed := latest && request.Status != "already_exists" && (current == nil || storedDigest != actualDigest)
		projectSnapshot, err := tx.Get(projectRef)
		if err != nil {
			return err
		}
		projectData := projectSnapshot.Data()
		if !readyProject(projectData, VerifiedProject{OwnerID: ref.OwnerID, ProjectID: ref.ProjectID}) {
			return ErrInvalidSourceCommit
		}
		eventSnapshot, err := tx.Get(eventRef)
		if err != nil {
			return err
		}
		eventData := eventSnapshot.Data()
		if eventData["operation_id"] != ref.OperationID || eventData["raw_path"] != path || eventData["expected_sha256"] != expectedDigest {
			return ErrSourceCommitConflict
		}
		state, _ := eventData["commit_state"].(string)
		if state == "aborted" {
			return ErrSourceCommitConflict
		}
		if state == "committed" {
			result.SourceRevision, _ = eventData["source_revision"].(int64)
			result.ProcessingStatus, _ = eventData["processing_status"].(string)
			return nil
		}
		settingsRef := scopedfirestore.Collection(j.fs, "system").Doc("pipeline_automation")
		settingsSnapshot, settingsErr := tx.Get(settingsRef)
		if settingsErr != nil && status.Code(settingsErr) != codes.NotFound {
			return settingsErr
		}
		adminAllowed := false
		if settingsSnapshot != nil && settingsSnapshot.Exists() {
			adminAllowed, _ = settingsSnapshot.Data()["allow_auto_pipeline"].(bool)
		}
		projectEnabled, _ := projectData["auto_pipeline_enabled"].(bool)
		processingStatus := "not_started"
		if changed {
			processingStatus = "not_started_disabled"
			if adminAllowed && projectEnabled {
				processingStatus = "pending"
			}
		}
		revision, _ := projectData["pipeline_source_revision"].(int64)
		if changed {
			revision++
			if err := tx.Set(currentRef, map[string]interface{}{
				"raw_path": path, "sha256": actualDigest, "generation": generation, "updated_at": now,
			}, cloudfirestore.MergeAll); err != nil {
				return err
			}
			if err := tx.Update(projectRef, []cloudfirestore.Update{
				{Path: "pipeline_source_revision", Value: revision},
				{Path: "pipeline_pending_source_revision", Value: revision},
				{Path: "pipeline_automation_status", Value: processingStatus},
				{Path: "pipeline_last_source_path", Value: path},
				{Path: "pipeline_last_source_sha256", Value: actualDigest},
				{Path: "pipeline_last_source_generation", Value: generation},
				{Path: "updated_at", Value: now},
			}); err != nil {
				return err
			}
			if err := profileruntime.Enqueue(tx, j.fs, profileruntime.Work{
				UserID: ref.OwnerID, ProjectID: ref.ProjectID, Kind: "pipeline_automation",
				Revision: revision, ID: "source-" + strconv.FormatInt(revision, 10), Due: now,
			}); err != nil {
				return err
			}
		} else if current == nil {
			// A prepared event may be replayed after an event with a newer GCS
			// generation already advanced the path watermark.
			revision, _ = projectData["pipeline_source_revision"].(int64)
		}
		if err := tx.Update(eventRef, []cloudfirestore.Update{
			{Path: "commit_state", Value: "committed"}, {Path: "confirmed_sha256", Value: actualDigest},
			{Path: "object_generation", Value: generation}, {Path: "storage_status", Value: request.Status},
			{Path: "processing_status", Value: processingStatus},
			{Path: "source_revision", Value: revision}, {Path: "updated_at", Value: now},
		}); err != nil {
			return err
		}
		result.SourceRevision = revision
		result.ProcessingStatus = processingStatus
		return nil
	})
	return result, err
}

func (j *FirestoreJournal) ReconcileSourceCommit(ctx context.Context, ref Ref) (CommitResult, error) {
	if j == nil || j.fs == nil || j.store == nil || !validRef(ref) {
		return CommitResult{}, ErrInvalidSourceCommit
	}
	eventRef := eventDocument(j.fs, ref)
	snapshot, err := eventRef.Get(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	data := snapshot.Data()
	state, _ := data["commit_state"].(string)
	if state == "committed" || state == "aborted" {
		revision, _ := data["source_revision"].(int64)
		processing, _ := data["processing_status"].(string)
		return CommitResult{State: state, ProcessingStatus: processing, SourceRevision: revision}, nil
	}
	path, _ := data["raw_path"].(string)
	expectedDigest, _ := data["expected_sha256"].(string)
	if storage.ValidateRawSyncPath(path) != nil || !storage.ValidRawSyncSHA256(expectedDigest) {
		return CommitResult{}, ErrInvalidSourceCommit
	}
	reader, ok := j.store.Scope(ref.OwnerID, ref.ProjectID).(storage.ConditionalWriter)
	if !ok {
		return CommitResult{State: "unknown", ProcessingStatus: "unknown"}, ErrSourceCommitUnconfirmed
	}
	bytes, generation, err := reader.ReadFileWithGeneration(ctx, "raw/"+path)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			deadline := sourceCommitDeadline(data)
			if deadline.IsZero() || time.Now().UTC().Before(deadline) {
				return CommitResult{State: "pending", ProcessingStatus: "unknown"}, ErrSourceCommitPending
			}
			if err := j.markCommitAborted(ctx, ref, "raw_missing"); err != nil {
				return CommitResult{}, err
			}
			return CommitResult{State: "aborted", ProcessingStatus: "not_started"}, nil
		}
		return CommitResult{State: "unknown", ProcessingStatus: "unknown"}, err
	}
	actualDigest := digest(bytes)
	if actualDigest != expectedDigest {
		if entrypoint, _ := data["entrypoint"].(string); entrypoint == "cli_sync" {
			if expected, recorded := data["expected_storage_generation"].(string); recorded {
				expectedGeneration := int64(0)
				validPrecondition := expected == ""
				if expected != "" {
					expectedGeneration, err = strconv.ParseInt(expected, 10, 64)
					validPrecondition = err == nil && expectedGeneration > 0
				}
				if validPrecondition && generation != expectedGeneration {
					// Raw Sync publishes with this exact GCS generation precondition.
					// A different current generation proves the staged writer can no
					// longer commit; a matching generation may still be in progress.
					if err := j.markCommitAborted(ctx, ref, "raw_superseded"); err != nil {
						return CommitResult{}, err
					}
					return CommitResult{State: "aborted", ProcessingStatus: "pending"}, nil
				}
			}
		}
		if time.Now().UTC().Before(sourceCommitDeadline(data)) {
			return CommitResult{State: "pending", ProcessingStatus: "unknown"}, ErrSourceCommitPending
		}
		if err := j.markCommitAborted(ctx, ref, "raw_replaced"); err != nil {
			return CommitResult{}, err
		}
		return CommitResult{State: "aborted", ProcessingStatus: "pending"}, nil
	}
	return j.ConfirmSourceCommit(ctx, ref, ConfirmRequest{Generation: strconv.FormatInt(generation, 10), Status: "created"})
}

func sourceCommitDeadline(data map[string]interface{}) time.Time {
	deadline, ok := data["reconcile_deadline"].(time.Time)
	if ok {
		return deadline
	}
	createdAt, _ := data["created_at"].(time.Time)
	return createdAt.Add(sourceCommitReconcileGrace)
}

func enqueueSourceCommitReconcile(ctx context.Context, tx *cloudfirestore.Transaction, fs *cloudfirestore.Client, project VerifiedProject, operationID string, due time.Time) error {
	work := profileruntime.Work{
		UserID: project.OwnerID, ProjectID: project.ProjectID, Kind: "source_commit_reconcile",
		Revision: 0, ID: operationID, Due: due.UTC(), Pending: true, Status: "pending",
	}
	workRef := scopedfirestore.Collection(fs, profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := tx.Get(workRef); err == nil {
		return nil
	} else if status.Code(err) != codes.NotFound {
		return err
	}
	return tx.Create(workRef, work)
}

func (j *FirestoreJournal) markCommitAborted(ctx context.Context, ref Ref, reason string) error {
	return j.fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		eventRef := eventDocument(j.fs, ref)
		snapshot, err := tx.Get(eventRef)
		if err != nil {
			return err
		}
		state, _ := snapshot.Data()["commit_state"].(string)
		if state != "prepared" && state != "unknown" {
			return nil
		}
		return tx.Update(eventRef, []cloudfirestore.Update{
			{Path: "commit_state", Value: "aborted"}, {Path: "abort_reason", Value: reason},
			{Path: "updated_at", Value: time.Now().UTC()},
		})
	})
}

func validProject(project VerifiedProject) bool {
	return auth.ValidPathSegment(project.OwnerID) && auth.ValidPathSegment(project.ProjectID)
}

func validPrepareRequest(request *PrepareRequest) bool {
	if request == nil || storage.ValidateRawSyncPath(request.RawPath) != nil ||
		!storage.ValidRawSyncSHA256(strings.ToLower(request.ExpectedSHA256)) {
		return false
	}
	request.ExpectedSHA256 = strings.ToLower(request.ExpectedSHA256)
	if request.OperationID != "" && (!auth.ValidPathSegment(request.OperationID) || len(request.OperationID) > 128) {
		return false
	}
	if request.BatchID != "" && (!auth.ValidPathSegment(request.BatchID) || len(request.BatchID) > 128) {
		return false
	}
	if request.ExpectedStorageGeneration != "" {
		generation, err := strconv.ParseInt(request.ExpectedStorageGeneration, 10, 64)
		if request.Entrypoint != "cli_sync" || err != nil || generation <= 0 {
			return false
		}
	}
	switch request.Entrypoint {
	case "ui_upload", "url_accept", "mcp_add_raw", "mcp_import_url", "cli_sync", "document_import":
		return true
	default:
		return false
	}
}

func validRef(ref Ref) bool {
	return validProject(VerifiedProject{OwnerID: ref.OwnerID, ProjectID: ref.ProjectID}) && auth.ValidPathSegment(ref.OperationID)
}

func validGeneration(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func generationAtLeast(current, stored string) bool {
	currentValue, currentErr := strconv.ParseInt(current, 10, 64)
	storedValue, storedErr := strconv.ParseInt(stored, 10, 64)
	return currentErr == nil && (storedErr != nil || currentValue >= storedValue)
}

func samePrepare(data map[string]interface{}, request PrepareRequest) bool {
	expectedGeneration, _ := data["expected_storage_generation"].(string)
	return data["operation_id"] == request.OperationID && data["entrypoint"] == request.Entrypoint &&
		data["raw_path"] == request.RawPath && data["expected_sha256"] == request.ExpectedSHA256 &&
		expectedGeneration == request.ExpectedStorageGeneration && data["batch_id"] == request.BatchID
}

func readyProject(data map[string]interface{}, project VerifiedProject) bool {
	return data["user_id"] == project.OwnerID && data["project_id"] == project.ProjectID && data["status"] == "ready"
}

func projectDocument(fs *cloudfirestore.Client, project VerifiedProject) *cloudfirestore.DocumentRef {
	return scopedfirestore.Collection(fs, "projects").Doc(project.OwnerID + "_" + project.ProjectID)
}

func eventDocument(fs *cloudfirestore.Client, ref Ref) *cloudfirestore.DocumentRef {
	return projectDocument(fs, VerifiedProject{OwnerID: ref.OwnerID, ProjectID: ref.ProjectID}).Collection("pipeline_source_events").Doc(ref.OperationID)
}

func pathDocumentID(rawPath string) string {
	sum := sha256.Sum256([]byte(rawPath))
	return hex.EncodeToString(sum[:])
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InventoryFingerprint identifies source content only; generation changes and
// annotations do not count as new source content.
func InventoryFingerprint(ctx context.Context, raw storage.RawSyncStore) (string, error) {
	if raw == nil {
		return "", ErrSourceCommitUnconfirmed
	}
	files, err := raw.ListSyncRawFiles(ctx)
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, k int) bool { return files[i].Path < files[k].Path })
	hasher := sha256.New()
	for _, file := range files {
		if storage.ValidateRawSyncPath(file.Path) != nil || !storage.ValidRawSyncSHA256(file.SHA256) {
			return "", ErrSourceCommitUnconfirmed
		}
		_, _ = hasher.Write([]byte(file.Path + "\x00" + file.SHA256 + "\n"))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func newOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "src-" + hex.EncodeToString(value[:]), nil
}
