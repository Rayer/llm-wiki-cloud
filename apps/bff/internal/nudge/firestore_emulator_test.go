package nudge

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestRepositorySourceCASAttentionAndTombstoneRecovery(t *testing.T) {
	client := newNudgeFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ownerID := "nudge-owner"
	projectID := fmt.Sprintf("nudge-project-%d", time.Now().UnixNano())
	sourceKey := "pipeline-fail:" + projectID
	repo := NewRepository(client.Raw())
	repo.SetClockForTest(func() time.Time { return time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC) })

	failed := pipelineObservation(projectID, 1, "run-1", "a")
	if applied, err := repo.ApplyObservation(ctx, ownerID, failed); err != nil || !applied {
		t.Fatalf("initial source apply=%t err=%v", applied, err)
	}
	view, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 1 || view.Items[0].AttentionState != AttentionUnread {
		t.Fatalf("initial view=%+v err=%v", view, err)
	}
	attentionID := view.Items[0].AttentionID
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, attentionID, "read"); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	corrected := pipelineObservation(projectID, 2, "run-2", "a")
	corrected.ExpectedSourceRevision = 1
	if applied, err := repo.ApplyObservation(ctx, ownerID, corrected); err != nil || !applied {
		t.Fatalf("material-identical source correction=%t err=%v", applied, err)
	}
	view, err = repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 1 || view.Items[0].AttentionID != attentionID || view.Items[0].AttentionState != AttentionRead {
		t.Fatalf("source correction reset user interaction: view=%+v err=%v", view, err)
	}

	oldClear := pipelineClear(projectID, 1, "run-1")
	oldClear.ExpectedSourceRevision = 2
	if _, err := repo.ApplyClear(ctx, ownerID, oldClear); err != ErrSourceRevisionConflict {
		t.Fatalf("old recovery err=%v, want source conflict", err)
	}
	newClear := pipelineClear(projectID, 3, "run-3")
	newClear.ExpectedSourceRevision = 2
	if applied, err := repo.ApplyClear(ctx, ownerID, newClear); err != nil || !applied {
		t.Fatalf("confirmed recovery=%t err=%v", applied, err)
	}
	staleUpsert := failed
	staleUpsert.ExpectedSourceRevision = 2
	if _, err := repo.ApplyObservation(ctx, ownerID, staleUpsert); err != ErrSourceRevisionConflict {
		t.Fatalf("delayed failure resurrected tombstone: err=%v", err)
	}
	view, err = repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 0 {
		t.Fatalf("recovered source remained visible: view=%+v err=%v", view, err)
	}
	slotRef, err := repo.slotRef(ownerID, CategoryPipelineFailure, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := slotRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := decodeSlot(snapshot.Data())
	if err != nil || slot.Active || slot.SourceRevision != 3 || slot.ProducerWitness.ProjectPipelineSequence != 3 {
		t.Fatalf("recovery tombstone=%+v err=%v", slot, err)
	}
}

func TestPatchAttentionRejectsUrgentReadAndDismiss(t *testing.T) {
	client := newNudgeFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewRepository(client.Raw())
	ownerID := "urgent-owner"
	ref, err := repo.categoryRef(ownerID, CategoryPipelineFailure)
	if err != nil {
		t.Fatal(err)
	}
	cat := emptyCategory(CategoryPipelineFailure)
	cat.AttentionID, cat.AttentionState, cat.ActiveCount, cat.UrgentCount = "urgent-attention", AttentionUnread, 1, 1
	cat.MaterialBySource["urgent-source"] = "urgent"
	cat.SeverityBySource["urgent-source"] = SeverityUrgent
	cat.UpdatedAt = time.Now().UTC()
	if _, err := ref.Set(ctx, cat); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"read", "dismiss"} {
		if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, cat.AttentionID, action); err != ErrAttentionNotDismissible {
			t.Fatalf("urgent %s returned %v", action, err)
		}
	}
	stored, err := ref.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCategory(stored.Data())
	if err != nil || decoded.AttentionState != AttentionUnread || decoded.UrgentCount != 1 {
		t.Fatalf("urgent state changed by direct interaction: %+v err=%v", decoded, err)
	}
}

func pipelineObservation(projectID string, sequence int64, executionID, digestByte string) ObservationInput {
	witness := ProducerWitness{Kind: "pipeline-run", ProjectID: projectID, ProjectPipelineSequence: sequence,
		ReservationID: fmt.Sprintf("reservation-%d", sequence), AttemptID: fmt.Sprintf("attempt-%d", sequence),
		ExecutionID: executionID, Trigger: "manual", Mode: "full", TerminalClass: "failed"}
	return ObservationInput{Category: CategoryPipelineFailure, SourceKey: "pipeline-fail:" + projectID,
		ProducerWitness: witness, PayloadDigest: strings.Repeat(digestByte, 64), AttentionMaterialRevision: "current-failure",
		Observation: Observation{ProjectID: projectID, ProjectName: "Test Wiki", ExecutionID: executionID,
			Title: "Pipeline did not publish", Body: "The exact run ended before publication.", Severity: SeverityAttention,
			Actions: []Action{{ID: "open-run", Kind: "link", Label: "Open run", Target: &Target{Kind: "pipeline-run", ProjectID: projectID, ExecutionID: executionID}}}}}
}

func pipelineClear(projectID string, sequence int64, executionID string) ClearInput {
	return ClearInput{Category: CategoryPipelineFailure, SourceKey: "pipeline-fail:" + projectID,
		ProducerWitness: ProducerWitness{Kind: "pipeline-run", ProjectID: projectID, ProjectPipelineSequence: sequence,
			ReservationID: fmt.Sprintf("reservation-%d", sequence), AttemptID: fmt.Sprintf("attempt-%d", sequence),
			ExecutionID: executionID, Trigger: "manual", Mode: "full", TerminalClass: "recovered"},
		Proof: "confirmed-recovery"}
}

func newNudgeFirestoreEmulator(t *testing.T) *scopedfirestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(fmt.Sprintf("nudge-%d", time.Now().UnixNano()), "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create local Firestore emulator client: %v", err)
	}
	return client
}
