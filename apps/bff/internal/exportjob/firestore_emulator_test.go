package exportjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

func exportEmulator(t *testing.T) *firestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("local Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("export repository tests require a loopback Firestore emulator")
	}
	client, err := firestore.NewClient(context.Background(), fmt.Sprintf("demo-lwc346-export-%d", time.Now().UnixNano()), option.WithEndpoint("http://"+endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestFirestoreAdmitSerializesConcurrentBinding(t *testing.T) {
	fs := exportEmulator(t)
	repo := NewFirestoreRepository(fs)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	userID, projectID := "concurrent-user", "binding-project"
	now := time.Now().UTC()
	const callers = 8
	var wait sync.WaitGroup
	jobs := make(chan Job, callers)
	denials := make(chan error, callers)
	otherErrors := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			job, _, err := repo.Admit(ctx, userID, projectID, ScopeRawFull, fmt.Sprintf("request-%d", i), now)
			if err == nil {
				jobs <- job
				return
			}
			var denied *AdmissionError
			if errors.As(err, &denied) && denied.Reason == RejectInProgress {
				denials <- err
				return
			}
			otherErrors <- err
		}(i)
	}
	wait.Wait()
	close(jobs)
	close(denials)
	close(otherErrors)
	var admitted []Job
	for job := range jobs {
		admitted = append(admitted, job)
	}
	for err := range otherErrors {
		t.Errorf("unexpected concurrent admission error: %v", err)
	}
	if len(admitted) != 1 || len(denials) != callers-1 {
		t.Fatalf("concurrent admissions: admitted=%d rejected=%d, want one job and %d in-progress rejections", len(admitted), len(denials), callers-1)
	}
	snapshot, err := repo.Snapshot(ctx, userID, projectID)
	if err != nil || !snapshot.Active || snapshot.LatestJob == nil || snapshot.LatestJob.ExportID != admitted[0].ExportID {
		t.Fatalf("concurrent state=%#v error=%v", snapshot, err)
	}
}

func TestFirestoreAdmitTimesOutExpiredJobBeforeReplacement(t *testing.T) {
	fs := exportEmulator(t)
	repo := NewFirestoreRepository(fs)
	ctx := context.Background()
	now := time.Now().UTC()
	first, _, err := repo.Admit(ctx, "timeout-user", "timeout-project", ScopeRaw, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunning(ctx, "timeout-user", "timeout-project", first.ExportID); err != nil {
		t.Fatal(err)
	}
	replacement, _, err := repo.Admit(ctx, "timeout-user", "timeout-project", ScopeRawFull, "replacement", first.DeadlineAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := repo.Get(ctx, "timeout-user", "timeout-project", first.ExportID)
	if err != nil || failed.Status != StatusFailed || failed.ErrorCode == nil || *failed.ErrorCode != "job_timeout" {
		t.Fatalf("expired job=%#v error=%v", failed, err)
	}
	snapshot, err := repo.Snapshot(ctx, "timeout-user", "timeout-project")
	if err != nil || !snapshot.Active || snapshot.LatestJob == nil || snapshot.LatestJob.ExportID != replacement.ExportID {
		t.Fatalf("replacement state=%#v error=%v", snapshot, err)
	}
}

func TestFirestoreCompleteMaintainsCurrentPreviousAndDeadline(t *testing.T) {
	fs := exportEmulator(t)
	repo := NewFirestoreRepository(fs)
	ctx := context.Background()
	userID, projectID := "complete-user", "complete-project"
	start := time.Now().UTC()
	first, _, err := repo.Admit(ctx, userID, projectID, ScopeRaw, "first", start)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunning(ctx, userID, projectID, first.ExportID); err != nil {
		t.Fatal(err)
	}
	firstCompleted := start.Add(time.Minute)
	if err := repo.Complete(ctx, userID, projectID, first.ExportID, start, firstCompleted, 123); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, userID, projectID, first.ExportID, start, firstCompleted, 123); err == nil {
		t.Fatal("Complete accepted a terminal job")
	}
	secondAdmitAt := NextAllowedAt(firstCompleted)
	second, _, err := repo.Admit(ctx, userID, projectID, ScopeRawFull, "second", secondAdmitAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunning(ctx, userID, projectID, second.ExportID); err != nil {
		t.Fatal(err)
	}
	secondCompleted := secondAdmitAt.Add(time.Minute)
	if err := repo.Complete(ctx, userID, projectID, second.ExportID, secondAdmitAt, secondCompleted, 456); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.Snapshot(ctx, userID, projectID)
	if err != nil || snapshot.Active || snapshot.Current == nil || snapshot.Previous == nil || snapshot.LatestJob == nil {
		t.Fatalf("completed snapshot=%#v error=%v", snapshot, err)
	}
	if snapshot.Current.ExportID != second.ExportID || snapshot.Previous.ExportID != first.ExportID || snapshot.LatestJob.Status != StatusReady || snapshot.LatestJob.SizeBytes == nil || *snapshot.LatestJob.SizeBytes != 456 {
		t.Fatalf("completed archive state=%#v", snapshot)
	}
	deadlineJob, _, err := repo.Admit(ctx, userID, projectID, ScopeRawFullMetadata, "deadline", NextAllowedAt(secondCompleted))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, userID, projectID, deadlineJob.ExportID, deadlineJob.CreatedAt, deadlineJob.DeadlineAt, 789); !errors.Is(err, ErrJobDeadlineExceeded) {
		t.Fatalf("Complete at deadline error=%v, want ErrJobDeadlineExceeded", err)
	}
}
