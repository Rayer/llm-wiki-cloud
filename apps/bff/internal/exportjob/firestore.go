package exportjob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	stateCollection = "export_state"
	jobCollection   = "export_jobs"
	idemCollection  = "export_idempotency"
)

type FirestoreRepository struct{ fs *firestore.Client }

func NewFirestoreRepository(fs *firestore.Client) *FirestoreRepository {
	return &FirestoreRepository{fs: fs}
}

type FirestoreProjectVerifier struct{ fs *firestore.Client }

func NewFirestoreProjectVerifier(fs *firestore.Client) *FirestoreProjectVerifier {
	return &FirestoreProjectVerifier{fs: fs}
}

// VerifyProject checks the existing owner-prefixed project record before export
// state or storage is accessed. ProjectMiddleware only validates the header.
func (v *FirestoreProjectVerifier) VerifyProject(ctx context.Context, userID, projectID string) (string, error) {
	if v == nil || v.fs == nil || !validSegment(userID) || !validSegment(projectID) {
		return "", ErrProjectMissing
	}
	snap, err := v.fs.Collection("projects").Doc(userID + "_" + projectID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", ErrProjectMissing
	}
	if err != nil {
		return "", err
	}
	data := snap.Data()
	if owner, ok := data["user_id"].(string); ok && owner != userID {
		return "", ErrProjectMissing
	}
	if id, ok := data["project_id"].(string); ok && id != projectID {
		return "", ErrProjectMissing
	}
	if id, ok := data["project_id"].(string); !ok || id == "" {
		return "", ErrProjectMissing
	}
	name, _ := data["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = projectID
	}
	return name, nil
}

func (r *FirestoreRepository) Admit(ctx context.Context, userID, projectID string, scope Scope, idempotencyKey string, now time.Time) (Job, bool, error) {
	if r == nil || r.fs == nil || !scope.Valid() {
		return Job{}, false, errors.New("export repository unavailable")
	}
	now = now.UTC()
	stateRef := r.fs.Collection(stateCollection).Doc(ownerID(userID, projectID))
	var idemRef *firestore.DocumentRef
	if idempotencyKey != "" {
		idemRef = r.fs.Collection(idemCollection).Doc(ownerID(userID, projectID) + "_" + hashID(idempotencyKey))
	}
	jobID, err := newExportID()
	if err != nil {
		return Job{}, false, err
	}
	newJob := Job{ExportID: jobID, Scope: scope, Status: StatusQueued, CreatedAt: now, DeadlineAt: now.Add(JobDeadline)}
	jobRef := r.jobRef(userID, projectID, jobID)
	var result Job
	replayed := false
	var denied *AdmissionError
	err = r.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result, replayed, denied = Job{}, false, nil
		if idemRef != nil {
			idem, readErr := tx.Get(idemRef)
			if readErr == nil && idem.Exists() {
				storedScope, _ := idem.Data()["scope"].(string)
				if storedScope != string(scope) {
					return ErrIdempotencyConflict
				}
				storedID, _ := idem.Data()["export_id"].(string)
				stored, getErr := tx.Get(r.jobRef(userID, projectID, storedID))
				if getErr != nil {
					return getErr
				}
				result = jobFromData(stored.Data())
				replayed = true
				return nil
			} else if readErr != nil && status.Code(readErr) != codes.NotFound {
				return readErr
			}
		}

		state := map[string]interface{}{}
		stateSnap, stateErr := tx.Get(stateRef)
		if stateErr == nil && stateSnap.Exists() {
			state = stateSnap.Data()
		} else if stateErr != nil && status.Code(stateErr) != codes.NotFound {
			return stateErr
		}
		activeID, _ := state["active_job_id"].(string)
		var activeJob *Job
		if activeID != "" {
			activeSnap, getErr := tx.Get(r.jobRef(userID, projectID, activeID))
			if getErr != nil && status.Code(getErr) != codes.NotFound {
				return getErr
			}
			if getErr == nil {
				active := jobFromData(activeSnap.Data())
				activeJob = &active
			}
		}
		var lastSuccess *time.Time
		if value, ok := timeValue(state["last_successful_at"]); ok {
			lastSuccess = &value
		}
		if admissionErr := AdmissionAllowed(activeJob, nil, now); admissionErr != nil {
			denied = admissionErr
			return nil
		}
		if activeJob != nil && (activeJob.Status == StatusQueued || activeJob.Status == StatusRunning) {
			if err := tx.Update(r.jobRef(userID, projectID, activeID), []firestore.Update{{Path: "status", Value: string(StatusFailed)}, {Path: "error_code", Value: "job_timeout"}, {Path: "error_message", Value: "Export job timed out. You can try again."}, {Path: "updated_at", Value: now}}); err != nil {
				return err
			}
			delete(state, "active_job_id")
		}
		if admissionErr := AdmissionAllowed(nil, lastSuccess, now); admissionErr != nil {
			denied = admissionErr
			return nil
		}

		if err := tx.Create(jobRef, jobData(userID, projectID, newJob)); err != nil {
			return err
		}
		state["user_id"] = userID
		state["project_id"] = projectID
		state["active_job_id"] = jobID
		state["latest_job_id"] = jobID
		state["updated_at"] = now
		if err := tx.Set(stateRef, state); err != nil {
			return err
		}
		if idemRef != nil {
			if err := tx.Create(idemRef, map[string]interface{}{"export_id": jobID, "scope": string(scope), "created_at": now}); err != nil {
				return err
			}
		}
		result = newJob
		return nil
	})
	if err == nil && denied != nil {
		return Job{}, false, denied
	}
	return result, replayed, err
}

func (r *FirestoreRepository) Get(ctx context.Context, userID, projectID, exportID string) (Job, error) {
	if r == nil || r.fs == nil || !validSegment(exportID) {
		return Job{}, ErrJobMissing
	}
	snap, err := r.jobRef(userID, projectID, exportID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Job{}, ErrJobMissing
	}
	if err != nil {
		return Job{}, err
	}
	job := jobFromData(snap.Data())
	if job.ExportID != exportID {
		return Job{}, ErrJobMissing
	}
	applyCompletedJob(&job)
	return job, nil
}

func (r *FirestoreRepository) Snapshot(ctx context.Context, userID, projectID string) (Snapshot, error) {
	result := Snapshot{}
	if r == nil || r.fs == nil {
		return result, errors.New("export repository unavailable")
	}
	ref := r.fs.Collection(stateCollection).Doc(ownerID(userID, projectID))
	snap, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	data := snap.Data()
	activeID, _ := data["active_job_id"].(string)
	result.Active = activeID != ""
	result.LatestJob, err = r.optionalJob(ctx, userID, projectID, data["latest_job_id"])
	if err != nil {
		return Snapshot{}, err
	}
	result.Current, err = r.optionalJob(ctx, userID, projectID, data["current_archive_id"])
	if err != nil {
		return Snapshot{}, err
	}
	result.Previous, err = r.optionalJob(ctx, userID, projectID, data["previous_archive_id"])
	if err != nil {
		return Snapshot{}, err
	}
	if lastSuccess, ok := timeValue(data["last_successful_at"]); ok {
		next := NextAllowedAt(lastSuccess)
		result.NextAllowedAt = &next
	}
	return result, nil
}

func (r *FirestoreRepository) MarkRunning(ctx context.Context, userID, projectID, exportID string) error {
	ref := r.jobRef(userID, projectID, exportID)
	return r.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			return err
		}
		job := jobFromData(snap.Data())
		if err := claimError(job, time.Now().UTC()); err != nil {
			return err
		}
		return tx.Update(ref, []firestore.Update{{Path: "status", Value: string(StatusRunning)}, {Path: "started_at", Value: time.Now().UTC()}})
	})
}

func (r *FirestoreRepository) Fail(ctx context.Context, userID, projectID, exportID, errorCode, errorMessage string) error {
	stateRef := r.fs.Collection(stateCollection).Doc(ownerID(userID, projectID))
	jobRef := r.jobRef(userID, projectID, exportID)
	errorCode = safeErrorCode(errorCode)
	errorMessage = safeErrorMessage(errorMessage)
	return r.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		jobSnap, err := tx.Get(jobRef)
		if err != nil {
			return err
		}
		if jobFromData(jobSnap.Data()).Status == StatusReady || jobFromData(jobSnap.Data()).Status == StatusFailed {
			return nil
		}
		stateSnap, stateErr := tx.Get(stateRef)
		if stateErr != nil && status.Code(stateErr) != codes.NotFound {
			return stateErr
		}
		updates := []firestore.Update{{Path: "status", Value: string(StatusFailed)}, {Path: "error_code", Value: errorCode}, {Path: "error_message", Value: errorMessage}, {Path: "updated_at", Value: time.Now().UTC()}}
		if err := tx.Update(jobRef, updates); err != nil {
			return err
		}
		if stateErr == nil && stateSnap.Exists() && stateSnap.Data()["active_job_id"] == exportID {
			state := stateSnap.Data()
			delete(state, "active_job_id")
			state["latest_job_id"] = exportID
			state["updated_at"] = time.Now().UTC()
			return tx.Set(stateRef, state)
		}
		return nil
	})
}

func (r *FirestoreRepository) Complete(ctx context.Context, userID, projectID, exportID string, snapshotAt, completedAt time.Time, size int64) error {
	stateRef := r.fs.Collection(stateCollection).Doc(ownerID(userID, projectID))
	jobRef := r.jobRef(userID, projectID, exportID)
	completedAt = completedAt.UTC()
	snapshotAt = snapshotAt.UTC()
	return r.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		jobSnap, err := tx.Get(jobRef)
		if err != nil {
			return err
		}
		job := jobFromData(jobSnap.Data())
		jobStatus := job.Status
		if jobStatus == StatusReady || jobStatus == StatusFailed {
			return errors.New("export job is already terminal")
		}
		if jobStatus != StatusQueued && jobStatus != StatusRunning {
			return ErrJobNotClaimable
		}
		if !jobDeadline(job).After(completedAt) {
			return ErrJobDeadlineExceeded
		}
		stateSnap, err := tx.Get(stateRef)
		if err != nil {
			return err
		}
		state := stateSnap.Data()
		if state["active_job_id"] != exportID {
			return errors.New("export job is no longer active")
		}
		if err := tx.Update(jobRef, []firestore.Update{
			{Path: "status", Value: string(StatusReady)},
			{Path: "snapshot_at", Value: snapshotAt},
			{Path: "completed_at", Value: completedAt},
			{Path: "size_bytes", Value: size},
			{Path: "download_available", Value: true},
			{Path: "updated_at", Value: completedAt},
			{Path: "error_code", Value: nil},
			{Path: "error_message", Value: nil},
		}); err != nil {
			return err
		}
		if currentID, _ := state["current_archive_id"].(string); currentID != "" {
			state["previous_archive_id"] = currentID
		}
		state["current_archive_id"] = exportID
		state["latest_job_id"] = exportID
		state["last_successful_at"] = completedAt
		delete(state, "active_job_id")
		state["updated_at"] = completedAt
		return tx.Set(stateRef, state)
	})
}

func claimError(job Job, now time.Time) error {
	if job.Status != StatusQueued {
		return ErrJobNotClaimable
	}
	if !jobDeadline(job).After(now) {
		return ErrJobDeadlineExceeded
	}
	return nil
}

func (r *FirestoreRepository) optionalJob(ctx context.Context, userID, projectID string, value interface{}) (*Job, error) {
	id, _ := value.(string)
	if id == "" {
		return nil, nil
	}
	job, err := r.Get(ctx, userID, projectID, id)
	if errors.Is(err, ErrJobMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *FirestoreRepository) jobRef(userID, projectID, exportID string) *firestore.DocumentRef {
	return r.fs.Collection(jobCollection).Doc(ownerID(userID, projectID) + "_" + hashID(exportID))
}

func jobData(userID, projectID string, job Job) map[string]interface{} {
	return map[string]interface{}{
		"user_id": userID, "project_id": projectID, "export_id": job.ExportID, "scope": string(job.Scope),
		"status": string(job.Status), "created_at": job.CreatedAt, "snapshot_at": nil, "completed_at": nil,
		"deadline_at": job.DeadlineAt, "size_bytes": nil, "download_available": false, "error_code": nil, "error_message": nil,
	}
}

func jobFromData(data map[string]interface{}) Job {
	job := Job{}
	job.ExportID, _ = data["export_id"].(string)
	if scope, ok := data["scope"].(string); ok {
		job.Scope = Scope(scope)
	}
	if state, ok := data["status"].(string); ok {
		job.Status = Status(state)
	}
	job.CreatedAt, _ = timeValue(data["created_at"])
	job.DeadlineAt, _ = timeValue(data["deadline_at"])
	if value, ok := timeValue(data["snapshot_at"]); ok {
		job.SnapshotAt = &value
	}
	if value, ok := timeValue(data["completed_at"]); ok {
		job.CompletedAt = &value
	}
	if value, ok := intValue(data["size_bytes"]); ok {
		job.SizeBytes = &value
	}
	job.DownloadAvailable, _ = data["download_available"].(bool)
	if value, ok := data["error_code"].(string); ok && value != "" {
		job.ErrorCode = &value
	}
	if value, ok := data["error_message"].(string); ok && value != "" {
		job.ErrorMessage = &value
	}
	applyCompletedJob(&job)
	return job
}

func timeValue(value interface{}) (time.Time, bool) {
	switch t := value.(type) {
	case time.Time:
		return t.UTC(), true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	default:
		return time.Time{}, false
	}
}

func intValue(value interface{}) (int64, bool) {
	switch n := value.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func ownerID(userID, projectID string) string {
	return hashID(userID + "\x00" + projectID)
}

func hashID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func newExportID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate export ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func safeErrorCode(value string) string {
	switch value {
	case "worker_failure", "storage_unavailable", "job_start_failed", "job_timeout":
		return value
	default:
		return "worker_failure"
	}
}

func safeErrorMessage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Export could not be completed. You can try again."
	}
	if len(value) > 160 {
		value = value[:160]
	}
	// Provider errors often include resource paths or request metadata. The
	// caller should prefer stable messages; this final guard removes likely IDs.
	if strings.Contains(value, "projects/") || strings.Contains(value, "gs://") || strings.Contains(value, "serviceAccount") {
		return "Export could not be completed. You can try again."
	}
	return value
}
