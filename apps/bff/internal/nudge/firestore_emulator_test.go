package nudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
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
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Test Wiki"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	defer func() { _, _ = projectRef.Delete(context.Background()) }()

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

func TestReadRevalidatesPurposePairOwnershipAndPublication(t *testing.T) {
	client := newNudgeFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ownerID := "purpose-projection-owner"
	projectA := fmt.Sprintf("purpose-a-%d", time.Now().UnixNano())
	projectB := projectA + "-b"
	projectRefs := []*firestore.DocumentRef{
		seedPurposeProjectionProject(t, ctx, client.Raw(), ownerID, projectA, "Project A"),
		seedPurposeProjectionProject(t, ctx, client.Raw(), ownerID, projectB, "Project B"),
	}
	defer func() {
		for _, ref := range projectRefs {
			_, _ = ref.Collection("content_context").Doc("state").Delete(context.Background())
			_, _ = ref.Collection("content_context").Doc("declaration").Delete(context.Background())
			_, _ = ref.Collection("content_context").Doc("comparison").Delete(context.Background())
			_, _ = ref.Delete(context.Background())
		}
	}()

	categoryRef := scopedfirestore.Collection(client.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(CategoryPurposeGap)
	category := Category{SchemaVersion: SchemaVersion, Category: CategoryPurposeGap, AttentionID: "purpose-attention", AttentionState: AttentionUnread,
		MaterialBySource: map[string]string{"content-purpose-gap:" + projectA: "a", "content-purpose-gap:" + projectB: "b"},
		SeverityBySource: map[string]Severity{"content-purpose-gap:" + projectA: SeverityAttention, "content-purpose-gap:" + projectB: SeverityAttention},
		ActiveCount:      2, UpdatedAt: time.Now().UTC()}
	if _, err := categoryRef.Set(ctx, category); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = categoryRef.Delete(context.Background()) }()
	repo := NewRepository(client.Raw())
	verifyCurrent := func(_ context.Context, _, _ string, _ ProducerWitness) (bool, error) { return true, nil }
	view, err := repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, verifyCurrent)
	if err != nil || len(view.Items) != 1 || len(view.Items[0].Observations) != 2 {
		t.Fatalf("initial projection = %+v err=%v", view, err)
	}

	if _, err := projectRefs[0].Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectA, "name": "Project A", "description": "edited", "description_revision": int64(2)}); err != nil {
		t.Fatal(err)
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, verifyCurrent)
	assertPurposeProjection(t, view, err, projectA, projectB, true, false)
	storedSlotRef := categoryRef.Collection("observations").Doc(sourceDocumentID("content-purpose-gap:" + projectA))
	storedSlotSnapshot, err := storedSlotRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storedSlot, err := decodeSlot(storedSlotSnapshot.Data())
	if err != nil || !storedSlot.Active || storedSlot.SourceRevision != 1 || storedSlot.Observation == nil ||
		storedSlot.Observation.Stale || len(storedSlot.Observation.Actions) != 1 {
		t.Fatalf("read projection mutated the source slot: slot=%+v err=%v", storedSlot, err)
	}
	if _, err := projectRefs[0].Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectA, "name": "Project A", "description": "purpose", "description_revision": int64(1)}); err != nil {
		t.Fatal(err)
	}

	publicationChanged := func(_ context.Context, _, projectID string, _ ProducerWitness) (bool, error) {
		return projectID != projectA, nil
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, publicationChanged)
	assertPurposeProjection(t, view, err, projectA, projectB, true, false)

	publicationUnavailable := func(_ context.Context, _, projectID string, _ ProducerWitness) (bool, error) {
		if projectID == projectA {
			return false, errors.New("publication read unavailable")
		}
		return true, nil
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, publicationUnavailable)
	assertPurposeProjection(t, view, err, projectA, projectB, true, true)

	projectAStateRef := projectRefs[0].Collection("content_context").Doc("state")
	if _, err := projectAStateRef.Set(ctx, map[string]interface{}{
		"latest_publication": map[string]interface{}{"generation": "generation-1", "concepts_sha256": strings.Repeat("a", 64)},
		"last_good": map[string]interface{}{"source_generation": "summary-generation-1", "concepts_sha256": strings.Repeat("a", 64),
			"input_digest": strings.Repeat("d", 64), "text": "Published overview"},
	}); err != nil {
		t.Fatal(err)
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, verifyCurrent)
	assertPurposeProjection(t, view, err, projectA, projectB, true, false)
	if _, err := projectAStateRef.Set(ctx, map[string]interface{}{
		"latest_publication": map[string]interface{}{"generation": "generation-1", "concepts_sha256": strings.Repeat("a", 64)},
		"last_good": map[string]interface{}{"source_generation": "summary-generation-1", "concepts_sha256": strings.Repeat("a", 64),
			"input_digest": strings.Repeat("b", 64), "text": "Published overview"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := projectRefs[0].Set(ctx, map[string]interface{}{"user_id": "another-owner", "project_id": projectA, "name": "Project A", "description": "purpose", "description_revision": int64(1)}); err != nil {
		t.Fatal(err)
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, verifyCurrent)
	if err != nil || !view.Partial || len(view.Items) != 1 || len(view.Items[0].Observations) != 1 ||
		view.Items[0].Observations[0].Observation.ProjectID != projectB {
		t.Fatalf("non-owned Project was projected or hid valid Project B: view=%+v err=%v", view, err)
	}
	if _, err := projectRefs[0].Delete(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = repo.ReadWithPublicationVerifier(ctx, ownerID, nil, nil, verifyCurrent)
	if err != nil || !view.Partial || len(view.Items) != 1 || len(view.Items[0].Observations) != 1 ||
		view.Items[0].Observations[0].Observation.ProjectID != projectB {
		t.Fatalf("deleted Project was projected or hid valid Project B: view=%+v err=%v", view, err)
	}
}

func seedPurposeProjectionProject(t *testing.T, ctx context.Context, client *firestore.Client, ownerID, projectID, projectName string) *firestore.DocumentRef {
	t.Helper()
	digest, inputDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	summary, description, generation, sourceGeneration := "Published overview", "Manual purpose", "generation-1", "summary-generation-1"
	summaryHash := sha256.Sum256([]byte(summary))
	summaryDigest := hex.EncodeToString(summaryHash[:])
	projectRef := scopedfirestore.Collection(client, "projects").Doc(ownerID + "_" + projectID)
	contextRef := projectRef.Collection("content_context")
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": projectName, "description": description, "description_revision": int64(1)}); err != nil {
		t.Fatalf("seed Project %s: %v", projectID, err)
	}
	state := map[string]interface{}{
		"latest_publication": map[string]interface{}{"generation": generation, "concepts_sha256": digest},
		"last_good":          map[string]interface{}{"source_generation": sourceGeneration, "concepts_sha256": digest, "input_digest": inputDigest, "text": summary},
	}
	declaration := map[string]interface{}{
		"owner_id": ownerID, "project_id": projectID, "declaration_id": "declaration-1", "pair_key": "pair-1",
		"publication_generation": generation, "concepts_sha256": digest, "summary_source_generation": sourceGeneration,
		"summary_input_digest": inputDigest, "summary_text_digest": summaryDigest, "description": description, "description_revision": int64(1),
	}
	comparison := map[string]interface{}{
		"owner_id": ownerID, "project_id": projectID, "declaration_id": "declaration-1", "pair_key": "pair-1",
		"publication_generation": generation, "concepts_digest": digest, "summary_source_generation": sourceGeneration,
		"summary_input_digest": inputDigest, "summary_text_digest": summaryDigest, "description_revision": int64(1),
		"attempt_id": "attempt-1", "confirmation_id": "confirmation-1", "state": "ready", "verdict": "clear_conflict", "delivery_status": "applied",
	}
	for id, value := range map[string]map[string]interface{}{"state": state, "declaration": declaration, "comparison": comparison} {
		if _, err := contextRef.Doc(id).Set(ctx, value); err != nil {
			t.Fatalf("seed %s for %s: %v", id, projectID, err)
		}
	}

	witness := ProducerWitness{Kind: "purpose-comparison", ProjectID: projectID, AttemptID: "attempt-1", ConfirmationID: "confirmation-1",
		PairKey: "pair-1", DescriptionRevision: 1, PublicationGeneration: generation, ConceptsDigest: digest,
		SummaryInputDigest: inputDigest, AttemptStartedAt: time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC)}
	observation := Observation{ProjectID: projectID, ProjectName: projectName, Title: "Purpose and overview differ", Body: "Clear mismatch",
		DeclaredPurpose: description, PublishedSummary: summary, Severity: SeverityAttention, Coverage: Coverage{},
		Actions: []Action{{ID: "copy-open-description", Kind: "copy-then-link", Label: "Copy overview and open " + projectName + " description settings",
			CopyText: summary, Target: &Target{Kind: "project-description", ProjectID: projectID}}}}
	slot := Slot{SchemaVersion: SchemaVersion, SourceKey: "content-purpose-gap:" + projectID, SourceRevision: 1, ProducerWitness: witness,
		Active: true, PayloadDigest: strings.Repeat("c", 64), AttentionMaterialRevision: "purpose-current", Observation: &observation,
		ObservedAt: time.Now().UTC(), LastAppliedConfirmationID: witness.ConfirmationID}
	categoryRef := scopedfirestore.Collection(client, "user_nudges").Doc(ownerID).Collection("categories").Doc(CategoryPurposeGap)
	if _, err := categoryRef.Collection("observations").Doc(sourceDocumentID(slot.SourceKey)).Set(ctx, slot); err != nil {
		t.Fatalf("seed Nudge slot for %s: %v", projectID, err)
	}
	return projectRef
}

func assertPurposeProjection(t *testing.T, view View, err error, projectA, projectB string, staleA, partial bool) {
	t.Helper()
	if err != nil || view.Partial != partial || len(view.Items) != 1 || len(view.Items[0].Observations) != 2 {
		t.Fatalf("projection shape partial=%t items=%+v err=%v", view.Partial, view.Items, err)
	}
	byProject := map[string]Slot{}
	for _, slot := range view.Items[0].Observations {
		if slot.Observation != nil {
			byProject[slot.Observation.ProjectID] = slot
		}
	}
	if len(byProject) != 2 || byProject[projectA].Observation == nil || byProject[projectA].Observation.Stale != staleA ||
		byProject[projectB].Observation == nil || byProject[projectB].Observation.Stale || len(byProject[projectB].Observation.Actions) != 1 {
		t.Fatalf("projection freshness for Projects A/B = %+v", byProject)
	}
	if staleA && len(byProject[projectA].Observation.Actions) != 0 {
		t.Fatalf("stale Project A retained actions: %+v", byProject[projectA].Observation.Actions)
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
