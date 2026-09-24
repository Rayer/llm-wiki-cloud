package exportjob

import (
	"context"
	"errors"
	"time"
)

var ErrProjectMissing = errors.New("project not found")
var ErrJobMissing = errors.New("export job not found")
var ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
var ErrJobNotClaimable = errors.New("export job is no longer queued")
var ErrJobDeadlineExceeded = errors.New("export job deadline exceeded")

type AdmissionError struct {
	Reason        RejectionReason
	ExistingJobID string
	NextAllowedAt *time.Time
}

func (e *AdmissionError) Error() string { return string(e.Reason) }

type Snapshot struct {
	LatestJob     *Job
	Current       *Job
	Previous      *Job
	Active        bool
	NextAllowedAt *time.Time
}

type Repository interface {
	Admit(context.Context, string, string, Scope, string, time.Time) (Job, bool, error)
	Get(context.Context, string, string, string) (Job, error)
	Snapshot(context.Context, string, string) (Snapshot, error)
	MarkRunning(context.Context, string, string, string) error
	Fail(context.Context, string, string, string, string, string) error
	Complete(context.Context, string, string, string, time.Time, time.Time, int64) error
}

type ProjectVerifier interface {
	VerifyProject(context.Context, string, string) (string, error)
}

type ArchiveInfo struct {
	Size       int64
	CustomTime time.Time
}

type ArchiveStore interface {
	Stat(context.Context, string, string, string) (ArchiveInfo, error)
	Sign(context.Context, string, string, string, time.Time, string) (string, error)
}

type JobStarter interface {
	Start(context.Context, string, string, Job) error
}

func NextAllowedAt(completedAt time.Time) time.Time {
	return completedAt.UTC().Add(Cooldown)
}

func AdmissionAllowed(active *Job, lastSuccess *time.Time, now time.Time) *AdmissionError {
	if active != nil && (active.Status == StatusQueued || active.Status == StatusRunning) {
		deadline := active.DeadlineAt
		if deadline.IsZero() {
			deadline = active.CreatedAt.Add(JobDeadline)
		}
		if deadline.After(now) {
			return &AdmissionError{Reason: RejectInProgress, ExistingJobID: active.ExportID}
		}
	}
	if lastSuccess != nil {
		next := NextAllowedAt(*lastSuccess)
		if next.After(now) {
			return &AdmissionError{Reason: RejectCooldown, NextAllowedAt: &next}
		}
	}
	return nil
}

func applyCompletedJob(job *Job) {
	if job.Status == StatusReady && job.CompletedAt != nil {
		next := NextAllowedAt(*job.CompletedAt)
		job.NextAllowedAt = &next
	}
}
