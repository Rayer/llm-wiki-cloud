package contentcontext

import (
	"context"
	"fmt"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestPurposeCompanionBindsExactSummaryAndDescriptionRevision(t *testing.T) {
	client := newContentContextFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ownerID, projectID := "purpose-owner", fmt.Sprintf("purpose-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID)
	defer func() { _, _ = projectRef.Delete(context.Background()) }()
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": ownerID, "project_id": projectID, "name": "Research archive",
		"description": "Published engineering research", "description_revision": int64(4),
	}); err != nil {
		t.Fatal(err)
	}

	publication := testPublication("purpose-G1", 'a', "purpose-manifest-G1")
	authority := &testPublicationAuthority{publication: publication}
	repository := NewFirestoreRepository(client.Raw())
	repository.SetClockForTest(func() time.Time { return time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC) })
	if _, err := repository.ReconcileCurrent(ctx, ownerID, projectID, authority); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || claim == nil {
		t.Fatalf("summary claim=%+v err=%v", claim, err)
	}
	if saved, err := repository.Complete(ctx, ownerID, projectID, authority, *claim, testSummaryCompletion("published overview A")); err != nil || !saved {
		t.Fatalf("summary completion=%t err=%v", saved, err)
	}
	state, err := repository.Read(ctx, ownerID, projectID)
	if err != nil || state.LastGood == nil {
		t.Fatalf("read LastGood=%+v err=%v", state.LastGood, err)
	}
	declaration, err := repository.CaptureDeclaredPurpose(ctx, ownerID, projectID, authority)
	if err != nil {
		t.Fatalf("capture declaration: %v", err)
	}
	if declaration.Description != "Published engineering research" || declaration.DescriptionRevision != 4 ||
		declaration.PublicationGeneration != publication.Generation || declaration.SummaryTextDigest != PurposeTextDigest(state.LastGood.Text) {
		t.Fatalf("declaration did not bind exact pair: %+v", declaration)
	}

	startedAt := time.Date(2026, 11, 14, 3, 1, 0, 0, time.UTC)
	comparison, err := repository.BeginPurposeComparison(ctx, ownerID, projectID, authority, declaration, 0, startedAt)
	if err != nil {
		t.Fatalf("begin comparison: %v", err)
	}
	changedSummary := *state.LastGood
	changedSummary.Text = "different output with the same model input digest"
	if _, err := repository.CompletePurposeComparison(ctx, ownerID, projectID, authority, comparison.AttemptID, "clear_conflict", "Different topics", changedSummary); err != ErrAttemptRejected {
		t.Fatalf("same input digest accepted a different persisted summary: %v", err)
	}
	ready, err := repository.CompletePurposeComparison(ctx, ownerID, projectID, authority, comparison.AttemptID, "clear_conflict", "Different topics", *state.LastGood)
	if err != nil || ready.State != "ready" || ready.SummaryTextDigest != declaration.SummaryTextDigest || ready.DeliveryStatus != "pending" {
		t.Fatalf("comparison confirmation=%+v err=%v", ready, err)
	}
	storedDeclaration, storedComparison, err := repository.ReadPurposeDocuments(ctx, ownerID, projectID)
	if err != nil || storedDeclaration == nil || storedComparison == nil || storedDeclaration.Description != declaration.Description || storedComparison.ConfirmationID != ready.ConfirmationID {
		t.Fatalf("separate persisted readback declaration=%+v comparison=%+v err=%v", storedDeclaration, storedComparison, err)
	}

	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": ownerID, "project_id": projectID, "name": "Research archive",
		"description": "A newly saved purpose", "description_revision": int64(5),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginPurposeComparison(ctx, ownerID, projectID, authority, declaration, 0, startedAt.Add(time.Second)); err != ErrPurposeUnavailable {
		t.Fatalf("old description snapshot started a new comparison: %v", err)
	}
}
