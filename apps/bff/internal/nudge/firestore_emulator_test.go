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
	clockNow := time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC)
	repo.SetClockForTest(func() time.Time { return clockNow })
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
	sourceRef, err := repo.slotRef(ownerID, CategoryPipelineFailure, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	sourceSnapshot, err := sourceRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := decodeSlot(sourceSnapshot.Data())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, attentionID, "read"); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, attentionID, "unread"); err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	categoryRef, err := repo.categoryRef(ownerID, CategoryPipelineFailure)
	if err != nil {
		t.Fatal(err)
	}
	categorySnapshot, err := categoryRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	categoryBeforeDuplicate, err := decodeCategory(categorySnapshot.Data())
	if err != nil {
		t.Fatal(err)
	}
	clockNow = clockNow.Add(time.Minute)
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, attentionID, "unread"); err != nil {
		t.Fatalf("repeat unread: %v", err)
	}
	categorySnapshot, err = categoryRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	categoryAfterDuplicate, err := decodeCategory(categorySnapshot.Data())
	if err != nil || !categoryAfterDuplicate.UpdatedAt.Equal(categoryBeforeDuplicate.UpdatedAt) {
		t.Fatalf("idempotent unread changed category timestamp: before=%+v after=%+v err=%v", categoryBeforeDuplicate, categoryAfterDuplicate, err)
	}
	view, err = repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 1 || view.Items[0].AttentionID != attentionID || view.Items[0].AttentionState != AttentionUnread {
		t.Fatalf("unread transition did not persist: view=%+v err=%v", view, err)
	}
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, attentionID, "read"); err != nil {
		t.Fatalf("mark read again: %v", err)
	}
	sourceSnapshot, err = sourceRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sourceAfter, err := decodeSlot(sourceSnapshot.Data())
	if err != nil || sourceAfter.SourceRevision != sourceBefore.SourceRevision || sourceAfter.ProducerWitness != sourceBefore.ProducerWitness ||
		sourceAfter.PayloadDigest != sourceBefore.PayloadDigest || sourceAfter.ObservedAt != sourceBefore.ObservedAt {
		t.Fatalf("attention transition changed source state: before=%+v after=%+v err=%v", sourceBefore, sourceAfter, err)
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

func TestBYOKReminderUsesCurrentAuthorizedBindingAndClearsQueryAndSequencedPipelineByRevision(t *testing.T) {
	client := newNudgeFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ownerID, otherOwner, profileID := "byok-owner", "byok-other-owner", "byok-profile"
	profileRef := scopedfirestore.Collection(client.Raw(), "llm_profiles").Doc(ownerID + "_" + profileID)
	if _, err := profileRef.Set(ctx, map[string]any{"owner_id": ownerID, "revision": int64(1), "enabled": true}); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = profileRef.Delete(context.Background()) }()

	repo := NewRepository(client.Raw())
	projects := []string{fmt.Sprintf("byok-wiki-a-%d", time.Now().UnixNano()), fmt.Sprintf("byok-wiki-b-%d", time.Now().UnixNano())}
	for index, projectID := range projects {
		projectName := fmt.Sprintf("BYOK Wiki %d", index+1)
		projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID)
		if _, err := projectRef.Set(ctx, map[string]any{"user_id": ownerID, "project_id": projectID, "name": projectName}); err != nil {
			t.Fatalf("seed Project %s: %v", projectID, err)
		}
		if _, err := projectRef.Collection("llm_profile_binding").Doc("current").Set(ctx, map[string]any{
			"source": "personal", "profile_id": profileID, "profile_revision": int64(1), "revision": int64(1),
		}); err != nil {
			t.Fatalf("seed binding %s: %v", projectID, err)
		}
		input := byokRevisionFailureInput(index, projectID, profileID, 1, 1, 1, "authentication", time.Now().UTC())
		input.Observation.ProjectName = projectName
		if applied, err := repo.ApplyBYOKObservation(ctx, ownerID, input); err != nil || !applied {
			t.Fatalf("create Project %s reminder applied=%t err=%v", projectID, applied, err)
		}
	}
	initialView, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(initialView.Items) != 1 || initialView.Items[0].Category != CategoryBYOKActionable ||
		len(initialView.Items[0].Observations) != 2 {
		t.Fatalf("both Wiki reminders must be visible before settings update: view=%+v err=%v", initialView, err)
	}
	visibleProjects := make(map[string]string, len(projects))
	for _, slot := range initialView.Items[0].Observations {
		if slot.Observation == nil || len(slot.Observation.Actions) != 1 || slot.Observation.Actions[0].Target == nil ||
			slot.Observation.Actions[0].Target.ProjectID != slot.Observation.ProjectID {
			t.Fatalf("Wiki reminder has no scoped settings action: %+v", slot)
		}
		visibleProjects[slot.Observation.ProjectID] = slot.Observation.ProjectName
	}
	if len(visibleProjects) != 2 {
		t.Fatalf("visible Wiki reminders=%v", visibleProjects)
	}
	projectName := "Renamed BYOK Wiki"
	renamedRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projects[0])
	if _, err := renamedRef.Update(ctx, []firestore.Update{{Path: "name", Value: projectName}}); err != nil {
		t.Fatal(err)
	}
	renamedView, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(renamedView.Items) != 1 || len(renamedView.Items[0].Observations) != 2 || len(renamedView.Notices) != 0 {
		t.Fatalf("Project rename hid a current BYOK reminder: view=%+v err=%v", renamedView, err)
	}
	for _, slot := range renamedView.Items[0].Observations {
		if slot.Observation.ProjectID == projects[0] && slot.Observation.ProjectName != projectName {
			t.Fatalf("renamed Project display name=%q, want %q", slot.Observation.ProjectName, projectName)
		}
	}

	// Updating the shared profile revision retracts every bound Wiki's old
	// reminder; the tombstone makes late revision-1 requests fail closed.
	if _, err := profileRef.Update(ctx, []firestore.Update{{Path: "revision", Value: int64(2)}}); err != nil {
		t.Fatal(err)
	}
	updatedView, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(updatedView.Items) != 0 {
		t.Fatalf("old revision remained visible after profile settings changed: view=%+v err=%v", updatedView, err)
	}
	for index, projectID := range projects {
		if cleared, err := repo.ClearBYOKForSettings(ctx, ownerID, projectID); err != nil || !cleared {
			t.Fatalf("clear Project %s reminder cleared=%t err=%v", projectID, cleared, err)
		}
		old := byokRevisionFailureInput(index, projectID, profileID, 1, 1, 1, "authentication", time.Now().UTC().Add(time.Second))
		old.ExpectedSourceRevision = 2
		if applied, err := repo.ApplyBYOKObservation(ctx, ownerID, old); applied || !errors.Is(err, ErrSourceRevisionConflict) {
			t.Fatalf("late old-profile request Project %s applied=%t err=%v", projectID, applied, err)
		}
		slot, err := repo.ReadSourceSlot(ctx, ownerID, CategoryBYOKActionable, "byok:"+projectID)
		if err != nil || slot.Active || slot.ProducerWitness.Kind != "byok-settings" || slot.ProducerWitness.ProfileRevision != 2 ||
			slot.ProducerWitness.Trigger != "" || slot.ProducerWitness.ExecutionID != "" ||
			slot.ProducerWitness.ProjectPipelineSequence != 0 || !slot.ProducerWitness.AttemptStartedAt.IsZero() {
			t.Fatalf("settings tombstone Project %s slot=%+v err=%v", projectID, slot, err)
		}
	}

	// A failure from the new saved revision can recreate each source.
	for index, projectID := range projects {
		newFailure := byokRevisionFailureInput(index, projectID, profileID, 2, 1, 2, "billing", time.Now().UTC().Add(2*time.Second))
		newFailure.ExpectedSourceRevision = 2
		if applied, err := repo.ApplyBYOKObservation(ctx, ownerID, newFailure); err != nil || !applied {
			t.Fatalf("new profile revision did not recreate Project %s reminder applied=%t err=%v", projectID, applied, err)
		}
	}

	// Changing each Project binding also clears the Query and sequenced
	// Pipeline reminders; only the current binding revision can recreate them.
	for index, projectID := range projects {
		projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID)
		bindingRef := projectRef.Collection("llm_profile_binding").Doc("current")
		if _, err := bindingRef.Update(ctx, []firestore.Update{{Path: "revision", Value: int64(2)}}); err != nil {
			t.Fatal(err)
		}
		if cleared, err := repo.ClearBYOKForSettings(ctx, ownerID, projectID); err != nil || !cleared {
			t.Fatalf("binding revision did not clear Project %s reminder cleared=%t err=%v", projectID, cleared, err)
		}
		settingsSlot, err := repo.ReadSourceSlot(ctx, ownerID, CategoryBYOKActionable, "byok:"+projectID)
		if err != nil || settingsSlot.Active || settingsSlot.ProducerWitness.Kind != "byok-settings" ||
			settingsSlot.ProducerWitness.BindingRevision != 2 || settingsSlot.ProducerWitness.ExecutionID != "" ||
			settingsSlot.ProducerWitness.ProjectPipelineSequence != 0 || !settingsSlot.ProducerWitness.AttemptStartedAt.IsZero() {
			t.Fatalf("binding settings clear did not persist a canonical tombstone for %s: slot=%+v err=%v", projectID, settingsSlot, err)
		}
		oldBindingFailure := byokRevisionFailureInput(index, projectID, profileID, 2, 1, 2, "billing", time.Now().UTC().Add(3*time.Second))
		oldBindingFailure.ExpectedSourceRevision, err = repo.ReadSourceRevision(ctx, ownerID, CategoryBYOKActionable, "byok:"+projectID)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := repo.ApplyBYOKObservation(ctx, ownerID, oldBindingFailure); applied || !errors.Is(err, ErrSourceRevisionConflict) {
			t.Fatalf("late old-binding request Project %s applied=%t err=%v", projectID, applied, err)
		}
		newBindingFailure := byokRevisionFailureInput(index, projectID, profileID, 2, 2, 3, "billing", time.Now().UTC().Add(4*time.Second))
		newBindingFailure.ExpectedSourceRevision, err = repo.ReadSourceRevision(ctx, ownerID, CategoryBYOKActionable, "byok:"+projectID)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := repo.ApplyBYOKObservation(ctx, ownerID, newBindingFailure); err != nil || !applied {
			t.Fatalf("new binding revision did not recreate Project %s reminder applied=%t err=%v", projectID, applied, err)
		}
	}

	// Neither Query nor Pipeline evidence can cross the Project owner boundary.
	for index, projectID := range projects {
		unauthorized := byokRevisionFailureInput(index, projectID, profileID, 2, 2, 3, "billing", time.Now().UTC().Add(5*time.Second))
		if applied, err := repo.ApplyBYOKObservation(ctx, otherOwner, unauthorized); applied || !errors.Is(err, ErrSourceRevisionConflict) {
			t.Fatalf("cross-owner write Project %s applied=%t err=%v", projectID, applied, err)
		}
	}
	otherView, err := repo.Read(ctx, otherOwner, nil, nil)
	if err != nil || len(otherView.Items) != 0 {
		t.Fatalf("cross-owner view=%+v err=%v", otherView, err)
	}
	ownerView, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(ownerView.Items) != 1 || len(ownerView.Items[0].Observations) != 2 {
		t.Fatalf("cross-owner attempts changed owner Query/Pipeline reminders: view=%+v err=%v", ownerView, err)
	}
}

func byokFailureInput(projectID, profileID string, profileRevision, bindingRevision int64, reason string, started time.Time) ObservationInput {
	witness := ProducerWitness{Kind: "byok-failure", ProjectID: projectID, ProfileID: profileID,
		ProfileRevision: profileRevision, BindingRevision: bindingRevision, FailureReason: reason,
		Trigger: "query", AttemptStartedAt: started.UTC().Truncate(time.Microsecond)}
	observation := Observation{ProjectID: projectID, Title: "Personal LLM needs attention", Body: "Review AI service settings.",
		Severity: SeverityAttention, Actions: []Action{{ID: "review-ai-settings", Kind: "link", Label: "Review AI settings",
			Target: &Target{Kind: "project-ai-service", ProjectID: projectID}}}}
	return ObservationInput{Category: CategoryBYOKActionable, SourceKey: "byok:" + projectID,
		PayloadDigest: strings.Repeat("a", 64), AttentionMaterialRevision: fmt.Sprintf("profile-revision:%d;binding-revision:%d;reason:%s", profileRevision, bindingRevision, reason),
		ProducerWitness: witness, Observation: observation}
}

func byokRevisionFailureInput(index int, projectID, profileID string, profileRevision, bindingRevision, pipelineSequence int64, reason string, started time.Time) ObservationInput {
	input := byokFailureInput(projectID, profileID, profileRevision, bindingRevision, reason, started)
	if index == 1 {
		input.ProducerWitness.Trigger = "pipeline"
		input.ProducerWitness.ExecutionID = fmt.Sprintf("review-run-%d", pipelineSequence)
		input.ProducerWitness.ProjectPipelineSequence = pipelineSequence
	}
	return input
}

func TestPatchAttentionRejectsUrgentReadDismissAndUnread(t *testing.T) {
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
	if err := repo.PatchAttention(ctx, ownerID, CategoryPipelineFailure, cat.AttentionID, "unread"); err != ErrAttentionNotChangeable {
		t.Fatalf("urgent unread returned %v", err)
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

func TestPatchAttentionCannotResurrectDismissedCategory(t *testing.T) {
	client := newNudgeFirestoreEmulator(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewRepository(client.Raw())
	ownerID := "dismissed-owner"
	ref, err := repo.categoryRef(ownerID, CategoryPurposeGap)
	if err != nil {
		t.Fatal(err)
	}
	cat := emptyCategory(CategoryPurposeGap)
	cat.AttentionID, cat.AttentionState, cat.ActiveCount = "dismissed-attention", AttentionDismissed, 1
	cat.MaterialBySource["purpose-source"] = "purpose"
	cat.SeverityBySource["purpose-source"] = SeverityAttention
	if _, err := ref.Set(ctx, cat); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"read", "unread"} {
		if err := repo.PatchAttention(ctx, ownerID, CategoryPurposeGap, cat.AttentionID, action); err != ErrAttentionNotChangeable {
			t.Fatalf("dismissed %s returned %v", action, err)
		}
	}
	if err := repo.PatchAttention(ctx, ownerID, CategoryPurposeGap, cat.AttentionID, "dismiss"); err != nil {
		t.Fatalf("repeated dismiss returned %v", err)
	}
	view, err := repo.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 0 {
		t.Fatalf("dismissed category became visible: view=%+v err=%v", view, err)
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
