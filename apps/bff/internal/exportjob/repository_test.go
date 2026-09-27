package exportjob

import (
	"testing"
	"time"
)

func TestAdmissionSerializesActiveJobsAndOnlySuccessfulJobsCoolDown(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	active := &Job{ExportID: "e1", Status: StatusRunning, CreatedAt: now, DeadlineAt: now.Add(JobDeadline)}
	if got := AdmissionAllowed(active, nil, now); got == nil || got.Reason != RejectInProgress || got.ExistingJobID != "e1" {
		t.Fatalf("active admission = %#v, want in_progress", got)
	}

	failed := &Job{ExportID: "e2", Status: StatusFailed, CreatedAt: now}
	if got := AdmissionAllowed(failed, nil, now); got != nil {
		t.Fatalf("failed job blocked admission: %#v", got)
	}

	completed := now.Add(-time.Hour)
	if got := AdmissionAllowed(nil, &completed, now); got == nil || got.Reason != RejectCooldown || got.NextAllowedAt == nil || !got.NextAllowedAt.Equal(completed.Add(Cooldown)) {
		t.Fatalf("successful cooldown = %#v", got)
	}
	completed = now.Add(-Cooldown)
	if got := AdmissionAllowed(nil, &completed, now); got != nil {
		t.Fatalf("elapsed cooldown blocked admission: %#v", got)
	}
}

func TestExpiredJobDeadlineAllowsRecovery(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	active := &Job{ExportID: "e1", Status: StatusQueued, CreatedAt: now.Add(-JobDeadline), DeadlineAt: now}
	if got := AdmissionAllowed(active, nil, now); got != nil {
		t.Fatalf("timed out job blocked recovery: %#v", got)
	}
	if HardTimeout >= 24*time.Hour || JobDeadline >= 24*time.Hour || JobDeadline <= HardTimeout {
		t.Fatalf("timeout bounds: task=%s recovery=%s", HardTimeout, JobDeadline)
	}
}

func TestClaimErrorRequiresQueuedJobBeforeDeadline(t *testing.T) {
	now := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	job := Job{Status: StatusQueued, CreatedAt: now, DeadlineAt: now.Add(time.Minute)}
	if err := claimError(job, now); err != nil {
		t.Fatalf("queued job claim = %v", err)
	}
	if err := claimError(job, now.Add(time.Minute)); err != ErrJobDeadlineExceeded {
		t.Fatalf("expired queued job claim = %v", err)
	}
	job.Status = StatusRunning
	if err := claimError(job, now); err != ErrJobNotClaimable {
		t.Fatalf("duplicate claim = %v", err)
	}
}
