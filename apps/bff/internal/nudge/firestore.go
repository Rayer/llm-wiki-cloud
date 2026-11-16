package nudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Repository struct {
	client *firestore.Client
	now    func() time.Time
}

// PublicationVerifier confirms the externally pinned publication identity for
// a stored purpose observation. It is read-only and must not run comparison or
// source reconciliation work.
type PublicationVerifier func(context.Context, string, string, ProducerWitness) (bool, error)

func NewRepository(client *firestore.Client) *Repository {
	return &Repository{client: client, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Repository) SetClockForTest(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

func (r *Repository) ReadSourceRevision(ctx context.Context, userID, category, sourceKey string) (int64, error) {
	ref, err := r.slotRef(userID, category, sourceKey)
	if err != nil {
		return 0, err
	}
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	slot, err := decodeSlot(snapshot.Data())
	if err != nil {
		return 0, err
	}
	if slot.SourceKey != sourceKey || slot.SchemaVersion != SchemaVersion {
		return 0, errors.New("invalid nudge source slot")
	}
	return slot.SourceRevision, nil
}

func (r *Repository) ReadSourceSlot(ctx context.Context, userID, category, sourceKey string) (Slot, error) {
	ref, err := r.slotRef(userID, category, sourceKey)
	if err != nil {
		return Slot{}, err
	}
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Slot{SchemaVersion: SchemaVersion, SourceKey: sourceKey}, nil
	}
	if err != nil {
		return Slot{}, err
	}
	slot, err := decodeSlot(snapshot.Data())
	if err != nil {
		return Slot{}, err
	}
	if slot.SourceKey != sourceKey || slot.SchemaVersion != SchemaVersion || slot.SourceRevision < 0 {
		return Slot{}, errors.New("invalid nudge source slot")
	}
	return slot, nil
}

func (r *Repository) ApplyObservation(ctx context.Context, userID string, input ObservationInput) (bool, error) {
	if err := ValidateObservationInput(input); err != nil {
		return false, err
	}
	if input.Category == CategoryPurposeGap {
		return false, ErrInvalidInput
	}
	return r.apply(ctx, userID, input.Category, input.SourceKey, input.ExpectedSourceRevision, input.ProducerWitness, &input, nil, false)
}

// ApplyReleaseAliasTransfer preserves an existing Release attention only for
// the validated one-time 1.0.0 -> 2026.11.4 alias. The witness is captured
// before Notes I/O and compared again inside the transaction.
func (r *Repository) ApplyReleaseAliasTransfer(ctx context.Context, userID string, input ObservationInput,
	proof ReleaseAliasTransferProof, captured Slot) (bool, error) {
	if err := ValidateObservationInput(input); err != nil || input.Category != CategoryReleaseNotes ||
		proof.RequestedVersion != "1.0.0" || proof.CanonicalVersion != "2026.11.4" ||
		input.ProducerWitness.ProductVersion != proof.CanonicalVersion ||
		input.AttentionMaterialRevision != "version:"+proof.CanonicalVersion ||
		len(proof.AliasRecordSHA256) != 64 || !isHex(proof.AliasRecordSHA256) ||
		captured.SourceKey != input.SourceKey || captured.SchemaVersion != SchemaVersion ||
		!captured.Active || captured.SourceRevision != input.ExpectedSourceRevision ||
		captured.ProducerWitness.Kind != "release-notes" || captured.ProducerWitness.Environment != input.ProducerWitness.Environment ||
		captured.ProducerWitness.ProductVersion != proof.RequestedVersion ||
		captured.AttentionMaterialRevision != "version:"+proof.RequestedVersion {
		return false, ErrInvalidInput
	}
	slotRef, err := r.slotRef(userID, input.Category, input.SourceKey)
	if err != nil {
		return false, err
	}
	categoryRef, err := r.categoryRef(userID, input.Category)
	if err != nil {
		return false, err
	}
	accepted := false
	err = r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		accepted = false
		slotSnapshot, err := tx.Get(slotRef)
		if status.Code(err) == codes.NotFound {
			return ErrSourceRevisionConflict
		}
		if err != nil {
			return err
		}
		slot, err := decodeSlot(slotSnapshot.Data())
		if err != nil {
			return err
		}
		if slot.Active && slot.ProducerWitness == input.ProducerWitness &&
			slot.PayloadDigest == input.PayloadDigest && slot.AttentionMaterialRevision == input.AttentionMaterialRevision {
			accepted = true
			return nil
		}
		if slot.SchemaVersion != captured.SchemaVersion || slot.SourceKey != captured.SourceKey ||
			slot.SourceRevision != captured.SourceRevision || slot.ProducerWitness != captured.ProducerWitness ||
			slot.PayloadDigest != captured.PayloadDigest || slot.AttentionMaterialRevision != captured.AttentionMaterialRevision ||
			!slot.Active {
			return ErrSourceRevisionConflict
		}
		categorySnapshot, err := tx.Get(categoryRef)
		if err != nil {
			return err
		}
		category, err := decodeCategory(categorySnapshot.Data())
		if err != nil || category.SchemaVersion != SchemaVersion || category.Category != input.Category {
			return ErrInvalidInput
		}
		docID := sourceDocumentID(input.SourceKey)
		if category.MaterialBySource[docID] != captured.AttentionMaterialRevision {
			return ErrSourceRevisionConflict
		}
		if category.AttentionState == "" || category.AttentionID == "" {
			return ErrSourceRevisionConflict
		}
		now := r.now().UTC().Truncate(time.Microsecond)
		slot.SourceRevision++
		slot.ProducerWitness = input.ProducerWitness
		slot.ObservedAt = now
		slot.PayloadDigest = input.PayloadDigest
		slot.AttentionMaterialRevision = input.AttentionMaterialRevision
		observation := input.Observation
		observation.ObservedAt = &now
		slot.Observation = &observation
		category.MaterialBySource[docID] = input.AttentionMaterialRevision
		category.SeverityBySource[docID] = observation.Severity
		category.ActiveCount = len(category.MaterialBySource)
		category.UrgentCount = 0
		for _, severity := range category.SeverityBySource {
			if severity == SeverityUrgent {
				category.UrgentCount++
			}
		}
		category.MaterialDigest = categoryMaterialDigest(category.MaterialBySource, category.SeverityBySource)
		category.UpdatedAt = now
		if err := tx.Set(slotRef, slot); err != nil {
			return err
		}
		if err := tx.Set(categoryRef, category); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}

// ApplyPipelineLegacyBootstrap commits the first exact legacy failure and its
// quota sequence floor inside the same Firestore transaction as the common
// source slot and attention material. The fence must complete all of its reads
// before it stages its quota write.
func (r *Repository) ApplyPipelineLegacyBootstrap(ctx context.Context, userID string, input ObservationInput, fence func(context.Context, *firestore.Transaction) error) (bool, error) {
	if err := ValidateObservationInput(input); err != nil || input.Category != CategoryPipelineFailure ||
		input.ProducerWitness.BootstrapDigest == "" || input.ProducerWitness.ProjectPipelineSequence != input.ProducerWitness.BootstrapFloor || fence == nil {
		return false, ErrInvalidInput
	}
	return r.applyWithFence(ctx, userID, input.Category, input.SourceKey, input.ExpectedSourceRevision, input.ProducerWitness, &input, nil, false, fence, true)
}

func (r *Repository) ApplyClear(ctx context.Context, userID string, input ClearInput) (bool, error) {
	if err := ValidateClearInput(input); err != nil {
		return false, err
	}
	if input.Category == CategoryPurposeGap {
		return false, ErrInvalidInput
	}
	return r.apply(ctx, userID, input.Category, input.SourceKey, input.ExpectedSourceRevision, input.ProducerWitness, nil, &input, false)
}

// ApplyPurposeConfirmation accepts the exact worker-written current comparison
// document. The transaction rechecks owner, description revision, publication,
// LastGood, declaration, comparison, pair and pending delivery before applying.
func (r *Repository) ApplyPurposeConfirmation(ctx context.Context, userID string, upsert *ObservationInput, clear *ClearInput) (bool, error) {
	if (upsert == nil) == (clear == nil) {
		return false, ErrInvalidInput
	}
	var key string
	var witness ProducerWitness
	var expected int64
	if upsert != nil {
		if err := ValidateObservationInput(*upsert); err != nil {
			return false, err
		}
		if upsert.Category != CategoryPurposeGap || !upsert.ConflictedComparison {
			return false, ErrInvalidInput
		}
		key, witness, expected = upsert.SourceKey, upsert.ProducerWitness, upsert.ExpectedSourceRevision
	} else {
		if err := ValidateClearInput(*clear); err != nil {
			return false, err
		}
		if clear.Category != CategoryPurposeGap || clear.Proof != "confirmed-no-conflict" {
			return false, ErrInvalidInput
		}
		key, witness, expected = clear.SourceKey, clear.ProducerWitness, clear.ExpectedSourceRevision
	}
	return r.apply(ctx, userID, CategoryPurposeGap, key, expected, witness, upsert, clear, true)
}

func (r *Repository) apply(ctx context.Context, userID, category, sourceKey string, expected int64, witness ProducerWitness, upsert *ObservationInput, clear *ClearInput, requirePurposeEvidence bool) (bool, error) {
	return r.applyWithFence(ctx, userID, category, sourceKey, expected, witness, upsert, clear, requirePurposeEvidence, nil, false)
}

func (r *Repository) applyWithFence(ctx context.Context, userID, category, sourceKey string, expected int64, witness ProducerWitness, upsert *ObservationInput, clear *ClearInput, requirePurposeEvidence bool, fence func(context.Context, *firestore.Transaction) error, requireAbsent bool) (bool, error) {
	slotRef, err := r.slotRef(userID, category, sourceKey)
	if err != nil {
		return false, err
	}
	categoryRef, err := r.categoryRef(userID, category)
	if err != nil {
		return false, err
	}
	accepted := false
	err = r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		accepted = false
		slot := Slot{SchemaVersion: SchemaVersion, SourceKey: sourceKey}
		slotExists := false
		slotSnap, slotErr := tx.Get(slotRef)
		if slotErr == nil {
			slotExists = true
			slot, slotErr = decodeSlot(slotSnap.Data())
		} else if status.Code(slotErr) != codes.NotFound {
			return slotErr
		}
		if slotErr != nil && status.Code(slotErr) != codes.NotFound {
			return slotErr
		}
		if slot.SchemaVersion != SchemaVersion || slot.SourceKey != sourceKey || slot.SourceRevision < 0 {
			return errors.New("invalid nudge source slot")
		}
		if slot.LastAppliedConfirmationID != "" && upsert != nil && slot.LastAppliedConfirmationID == upsert.ConfirmationID {
			accepted = true
			return nil
		}
		if slot.LastAppliedConfirmationID != "" && clear != nil && clear.ConfirmationID != "" && slot.LastAppliedConfirmationID == clear.ConfirmationID {
			accepted = true
			return nil
		}
		if upsert != nil && witness.BootstrapDigest != "" && slot.Active && slot.ProducerWitness == witness && slot.PayloadDigest == upsert.PayloadDigest {
			accepted = true
			return nil
		}
		if requireAbsent && slotExists {
			return ErrSourceRevisionConflict
		}
		if slot.SourceRevision != expected {
			return ErrSourceRevisionConflict
		}
		if slot.ProducerWitness.Kind != "" && CompareWitness(category, witness, slot.ProducerWitness) < 0 {
			return ErrSourceRevisionConflict
		}
		if upsert != nil && category != CategoryPurposeGap && slot.Active && slot.ProducerWitness == witness &&
			slot.PayloadDigest == upsert.PayloadDigest && slot.AttentionMaterialRevision == upsert.AttentionMaterialRevision {
			accepted = true
			return nil
		}
		if clear != nil && !slot.Active && slot.ProducerWitness == witness &&
			(clear.ConfirmationID == "" || clear.ConfirmationID == slot.LastAppliedConfirmationID) {
			accepted = true
			return nil
		}
		cat := emptyCategory(category)
		catSnap, catErr := tx.Get(categoryRef)
		if catErr == nil {
			cat, catErr = decodeCategory(catSnap.Data())
		} else if status.Code(catErr) != codes.NotFound {
			return catErr
		}
		if catErr != nil && status.Code(catErr) != codes.NotFound {
			return catErr
		}
		if cat.SchemaVersion != SchemaVersion || cat.Category != category {
			return errors.New("invalid nudge category state")
		}
		if fence != nil {
			if err := fence(ctx, tx); err != nil {
				return err
			}
		}
		if requirePurposeEvidence {
			confirmationID := witness.ConfirmationID
			if upsert != nil {
				confirmationID = upsert.ConfirmationID
			}
			if clear != nil {
				confirmationID = clear.ConfirmationID
			}
			if err := verifyPurposeEvidence(ctx, tx, r.client, userID, witness, confirmationID, upsert, clear); err != nil {
				return err
			}
		}
		if cat.MaterialBySource == nil {
			cat.MaterialBySource = map[string]string{}
		}
		if cat.SeverityBySource == nil {
			cat.SeverityBySource = map[string]Severity{}
		}
		docID := sourceDocumentID(sourceKey)
		oldMaterialDigest := categoryMaterialDigest(cat.MaterialBySource, cat.SeverityBySource)
		if slot.Active {
			delete(cat.MaterialBySource, docID)
			delete(cat.SeverityBySource, docID)
		}
		now := r.now().UTC().Truncate(time.Microsecond)
		slot.SourceRevision++
		slot.ProducerWitness = witness
		slot.ObservedAt = now
		isPurposeConfirmation := category == CategoryPurposeGap
		confirmationID := ""
		startedAt := time.Time{}
		if upsert != nil {
			slot.Active = true
			slot.PayloadDigest = upsert.PayloadDigest
			slot.AttentionMaterialRevision = upsert.AttentionMaterialRevision
			observation := upsert.Observation
			observation.ObservedAt = &now
			slot.Observation = &observation
			cat.MaterialBySource[docID] = upsert.AttentionMaterialRevision
			cat.SeverityBySource[docID] = observation.Severity
			confirmationID = upsert.ConfirmationID
			if upsert.EligibilityStartedAt != nil {
				startedAt = *upsert.EligibilityStartedAt
			}
		} else {
			slot.Active = false
			if clear != nil && clear.ConfirmationID != "" {
				confirmationID = clear.ConfirmationID
				startedAt = witness.AttemptStartedAt
			}
		}
		if isPurposeConfirmation {
			slot.LastAppliedConfirmationID = confirmationID
			if !startedAt.IsZero() {
				value := startedAt.UTC().Truncate(time.Microsecond)
				slot.LastConfirmationStartedAt = &value
			}
		}
		newMaterialDigest := categoryMaterialDigest(cat.MaterialBySource, cat.SeverityBySource)
		materialChanged := oldMaterialDigest != newMaterialDigest
		cat.ActiveCount = len(cat.MaterialBySource)
		cat.UrgentCount = 0
		for _, severity := range cat.SeverityBySource {
			if severity == SeverityUrgent {
				cat.UrgentCount++
			}
		}
		cat.MaterialDigest = newMaterialDigest
		shouldNotifyPurpose := false
		if upsert != nil && isPurposeConfirmation && upsert.ConflictedComparison {
			shouldNotifyPurpose = purposeNotificationEligible(cat.LastNotifiedAt, startedAt, now)
		}
		if (category != CategoryPurposeGap && materialChanged && cat.ActiveCount > 0) || shouldNotifyPurpose {
			cat.AttentionID = uuid.NewString()
			cat.AttentionState = AttentionUnread
			if shouldNotifyPurpose {
				notified := now
				cat.LastNotifiedAt = &notified
			}
		}
		if cat.ActiveCount == 0 && slot.Active == false { /* retain state for audit-free recovery semantics */
		}
		cat.UpdatedAt = now
		if err := tx.Set(slotRef, slot); err != nil {
			return err
		}
		if err := tx.Set(categoryRef, cat); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}

func purposeNotificationEligible(lastNotifiedAt *time.Time, attemptStartedAt, completedAt time.Time) bool {
	if lastNotifiedAt == nil {
		return true
	}
	eligibleAt := lastNotifiedAt.Add(PurposeCooldown)
	return !attemptStartedAt.Before(eligibleAt) && !completedAt.Before(eligibleAt)
}

func (r *Repository) PatchAttention(ctx context.Context, userID, category, attentionID, action string) error {
	if !validCategory(category) || attentionID == "" || (action != "read" && action != "unread" && action != "dismiss") {
		return ErrInvalidInput
	}
	ref, err := r.categoryRef(userID, category)
	if err != nil {
		return err
	}
	return r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrAttentionConflict
		}
		if err != nil {
			return err
		}
		cat, err := decodeCategory(snapshot.Data())
		if err != nil {
			return err
		}
		if cat.SchemaVersion != SchemaVersion || cat.Category != category || cat.AttentionID != attentionID || cat.AttentionState == "" {
			return ErrAttentionConflict
		}
		if cat.UrgentCount > 0 {
			if action == "unread" {
				return ErrAttentionNotChangeable
			}
			return ErrAttentionNotDismissible
		}
		if cat.AttentionState == AttentionDismissed && action != "dismiss" {
			return ErrAttentionNotChangeable
		}
		nextState := AttentionDismissed
		switch action {
		case "read":
			nextState = AttentionRead
		case "unread":
			nextState = AttentionUnread
		}
		if cat.AttentionState == nextState {
			return nil
		}
		cat.AttentionState = nextState
		cat.UpdatedAt = r.now().UTC().Truncate(time.Microsecond)
		return tx.Set(ref, cat)
	})
}

func (r *Repository) Read(ctx context.Context, userID string, failures []SourceFailure, notices []SourceNotice) (View, error) {
	return r.ReadWithPublicationVerifier(ctx, userID, failures, notices, nil)
}

// ReadWithPublicationVerifier returns persisted Nudge state while rechecking
// Project ownership and the source pair used by actions. It never mutates
// source rows or invokes a producer.
func (r *Repository) ReadWithPublicationVerifier(ctx context.Context, userID string, failures []SourceFailure, notices []SourceNotice, verifyPublication PublicationVerifier) (View, error) {
	view := View{SchemaVersion: SchemaVersion, Partial: len(failures) > 0, Failures: failures, Notices: notices, Items: []Item{}}
	if r == nil || r.client == nil || !auth.ValidPathSegment(userID) {
		return view, errors.New("nudge store unavailable")
	}
	categories, err := scopedfirestore.Collection(r.client, "user_nudges").Doc(userID).Collection("categories").Documents(ctx).GetAll()
	if err != nil {
		return view, err
	}
	for _, categorySnapshot := range categories {
		cat, err := decodeCategory(categorySnapshot.Data())
		if err != nil || !validCategory(cat.Category) || cat.Category != categorySnapshot.Ref.ID || cat.SchemaVersion != SchemaVersion {
			return view, errors.New("invalid nudge category state")
		}
		observations, err := categorySnapshot.Ref.Collection("observations").Documents(ctx).GetAll()
		if err != nil {
			return view, err
		}
		item := Item{Category: cat.Category, AttentionID: cat.AttentionID, AttentionState: cat.AttentionState, Severity: SeverityInfo, Observations: []Slot{}}
		for _, observationSnapshot := range observations {
			slot, err := decodeSlot(observationSnapshot.Data())
			if err != nil {
				return view, err
			}
			if slot.SchemaVersion != SchemaVersion || !validSourceKey(cat.Category, slot.SourceKey) || observationSnapshot.Ref.ID != sourceDocumentID(slot.SourceKey) {
				return view, errors.New("invalid nudge source slot")
			}
			if !slot.Active {
				continue
			}
			if slot.Observation == nil || validateWitness(cat.Category, slot.SourceKey, slot.ProducerWitness) != nil || validateObservation(*slot.Observation) != nil {
				return view, errors.New("invalid active nudge source slot")
			}
			projectID := slot.ProducerWitness.ProjectID
			if projectID == "" {
				projectID = slot.Observation.ProjectID
			}
			if projectID != "" {
				owned, current, err := r.projectProjectionCurrent(ctx, userID, cat.Category, projectID, slot)
				if err != nil {
					appendProjectionFailure(&view, "project_context_unavailable")
					continue
				}
				if !owned {
					appendProjectionFailure(&view, "project_unavailable")
					continue
				}
				if cat.Category == CategoryPurposeGap && verifyPublication != nil {
					published, err := verifyPublication(ctx, userID, projectID, slot.ProducerWitness)
					if err != nil {
						appendProjectionFailure(&view, "project_publication_unavailable")
						current = false
					} else if !published {
						current = false
					}
				}
				if !current {
					slot.Observation.Stale = true
					slot.Observation.Actions = []Action{}
					appendProjectionNotice(&view, slot.SourceKey, "project_context_changed")
				}
			}
			item.Observations = append(item.Observations, slot)
			if slot.Observation.Severity == SeverityUrgent {
				item.Severity = SeverityUrgent
			} else if slot.Observation.Severity == SeverityAttention && item.Severity != SeverityUrgent {
				item.Severity = SeverityAttention
			}
		}
		if len(item.Observations) == 0 {
			continue
		}
		if cat.AttentionID == "" {
			continue
		}
		if cat.AttentionState == AttentionDismissed && cat.UrgentCount == 0 {
			continue
		}
		sort.Slice(item.Observations, func(i, j int) bool { return item.Observations[i].SourceKey < item.Observations[j].SourceKey })
		view.Items = append(view.Items, item)
	}
	sort.Slice(view.Items, func(i, j int) bool { return view.Items[i].Category < view.Items[j].Category })
	return view, nil
}

func appendProjectionFailure(view *View, safeClass string) {
	for _, failure := range view.Failures {
		if failure.SafeClass == safeClass && failure.ProjectID == "" {
			view.Partial = true
			return
		}
	}
	view.Partial = true
	view.Failures = append(view.Failures, SourceFailure{SafeClass: safeClass})
}

func appendProjectionNotice(view *View, sourceKey, safeClass string) {
	for _, notice := range view.Notices {
		if notice.SourceKey == sourceKey && notice.State == "stale" {
			return
		}
	}
	view.Notices = append(view.Notices, SourceNotice{SourceKey: sourceKey, State: "stale", SafeClass: safeClass})
}

func (r *Repository) projectProjectionCurrent(ctx context.Context, userID, category, projectID string, slot Slot) (owned, current bool, err error) {
	if !auth.ValidPathSegment(projectID) {
		return false, false, nil
	}
	projectRef := scopedfirestore.Collection(r.client, "projects").Doc(userID + "_" + projectID)
	refs := []*firestore.DocumentRef{projectRef}
	if category == CategoryPurposeGap {
		contextRef := projectRef.Collection("content_context")
		refs = append(refs, contextRef.Doc("state"), contextRef.Doc("declaration"), contextRef.Doc("comparison"))
	}
	data := make([]map[string]interface{}, len(refs))
	found := make([]bool, len(refs))
	err = r.client.RunTransaction(ctx, func(txctx context.Context, tx *firestore.Transaction) error {
		for index, ref := range refs {
			snapshot, readErr := tx.Get(ref)
			if status.Code(readErr) == codes.NotFound {
				continue
			}
			if readErr != nil {
				return readErr
			}
			found[index] = true
			data[index] = snapshot.Data()
		}
		return nil
	}, firestore.ReadOnly)
	if err != nil {
		return false, false, err
	}
	if !found[0] {
		return false, false, nil
	}
	project := data[0]
	storedProjectID, _ := project["project_id"].(string)
	if storedProjectID == "" {
		storedProjectID = strings.TrimPrefix(projectRef.ID, userID+"_")
	}
	if project["user_id"] != userID || storedProjectID != projectID {
		return false, false, nil
	}
	owned, current = true, true
	projectName, _ := project["name"].(string)
	if strings.TrimSpace(projectName) == "" {
		projectName = projectID
	}
	observation := slot.Observation
	if observation == nil || observation.ProjectID != projectID || observation.ProjectName != projectName {
		current = false
	}
	if category == CategoryPurposeGap {
		current = current && found[1] && found[2] && found[3] && purposeProjectionMatches(userID, projectID, project, data[1], data[2], data[3], slot)
		return owned, current, nil
	}
	if category == CategoryPipelineFailure {
		witness := slot.ProducerWitness
		if witness.ProjectID != projectID || observation.ExecutionID == "" || observation.ExecutionID != witness.ExecutionID || len(observation.Actions) == 0 {
			current = false
		}
		for _, action := range observation.Actions {
			if action.Target == nil || ValidateTarget(*action.Target) != nil || action.Target.Kind != "pipeline-run" ||
				action.Target.ProjectID != projectID || action.Target.ExecutionID != observation.ExecutionID {
				current = false
			}
		}
	}
	return owned, current, nil
}

func purposeProjectionMatches(userID, projectID string, project, state, declaration, comparison map[string]interface{}, slot Slot) bool {
	witness, observation := slot.ProducerWitness, slot.Observation
	if observation == nil || witness.Kind != "purpose-comparison" || witness.ProjectID != projectID ||
		project["user_id"] != userID || projectDescriptionRevision(project) != witness.DescriptionRevision ||
		slot.LastAppliedConfirmationID == "" || slot.LastAppliedConfirmationID != witness.ConfirmationID {
		return false
	}
	publication, _ := state["latest_publication"].(map[string]interface{})
	lastGood, _ := state["last_good"].(map[string]interface{})
	summarySourceGeneration, _ := lastGood["source_generation"].(string)
	if publication == nil || lastGood == nil || publication["generation"] != witness.PublicationGeneration ||
		!sameDigestValue(publication["concepts_sha256"], witness.ConceptsDigest) ||
		!sameDigestValue(lastGood["concepts_sha256"], witness.ConceptsDigest) ||
		!sameDigestValue(lastGood["input_digest"], witness.SummaryInputDigest) ||
		summarySourceGeneration == "" {
		return false
	}
	summary, _ := lastGood["text"].(string)
	summaryHash := sha256.Sum256([]byte(summary))
	summaryDigest := hex.EncodeToString(summaryHash[:])
	if declaration["owner_id"] != userID || declaration["project_id"] != projectID || declaration["pair_key"] != witness.PairKey ||
		firestoreInt(declaration["description_revision"]) != witness.DescriptionRevision ||
		declaration["publication_generation"] != witness.PublicationGeneration ||
		declaration["summary_source_generation"] != summarySourceGeneration ||
		!sameDigestValue(declaration["concepts_sha256"], witness.ConceptsDigest) ||
		!sameDigestValue(declaration["summary_input_digest"], witness.SummaryInputDigest) ||
		declaration["summary_text_digest"] != summaryDigest || declaration["description"] != observation.DeclaredPurpose ||
		observation.PublishedSummary != summary {
		return false
	}
	comparisonDeclarationID, _ := declaration["declaration_id"].(string)
	if comparison["owner_id"] != userID || comparison["project_id"] != projectID || comparison["pair_key"] != witness.PairKey ||
		comparison["declaration_id"] != comparisonDeclarationID || comparison["publication_generation"] != witness.PublicationGeneration ||
		!sameDigestValue(comparison["concepts_digest"], witness.ConceptsDigest) ||
		!sameDigestValue(comparison["summary_input_digest"], witness.SummaryInputDigest) ||
		comparison["summary_source_generation"] != summarySourceGeneration ||
		comparison["summary_text_digest"] != summaryDigest || firestoreInt(comparison["description_revision"]) != witness.DescriptionRevision {
		return false
	}
	projectName, _ := project["name"].(string)
	if strings.TrimSpace(projectName) == "" {
		projectName = projectID
	}
	if observation.ProjectName != projectName || len(observation.Actions) != 1 {
		return false
	}
	action := observation.Actions[0]
	return action.Kind == "copy-then-link" && action.CopyText == summary &&
		action.Target != nil && action.Target.Kind == "project-description" && action.Target.ProjectID == projectID &&
		ValidateTarget(*action.Target) == nil && action.Label == "Copy overview and open "+projectName+" description settings"
}

func (r *Repository) slotRef(userID, category, key string) (*firestore.DocumentRef, error) {
	if r == nil || r.client == nil || !auth.ValidPathSegment(userID) || !validCategory(category) || !validSourceKey(category, key) {
		return nil, ErrInvalidInput
	}
	return scopedfirestore.Collection(r.client, "user_nudges").Doc(userID).Collection("categories").Doc(category).Collection("observations").Doc(sourceDocumentID(key)), nil
}

func (r *Repository) categoryRef(userID, category string) (*firestore.DocumentRef, error) {
	if r == nil || r.client == nil || !auth.ValidPathSegment(userID) || !validCategory(category) {
		return nil, ErrInvalidInput
	}
	return scopedfirestore.Collection(r.client, "user_nudges").Doc(userID).Collection("categories").Doc(category), nil
}

func emptyCategory(category string) Category {
	return Category{SchemaVersion: SchemaVersion, Category: category, AttentionState: AttentionUnread, MaterialBySource: map[string]string{}, SeverityBySource: map[string]Severity{}}
}

func categoryMaterialDigest(material map[string]string, severity map[string]Severity) string {
	keys := make([]string, 0, len(material))
	for key := range material {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(key + "\x00" + material[key] + "\x00" + string(severity[key]) + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func decodeSlot(data map[string]interface{}) (Slot, error) {
	var value Slot
	if err := decodeFirestore(data, &value); err != nil {
		return Slot{}, err
	}
	return value, nil
}
func decodeCategory(data map[string]interface{}) (Category, error) {
	var value Category
	if err := decodeFirestore(data, &value); err != nil {
		return Category{}, err
	}
	if value.MaterialBySource == nil {
		value.MaterialBySource = map[string]string{}
	}
	if value.SeverityBySource == nil {
		value.SeverityBySource = map[string]Severity{}
	}
	return value, nil
}
func decodeFirestore(data map[string]interface{}, dest interface{}) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dest)
}

func verifyPurposeEvidence(ctx context.Context, tx *firestore.Transaction, client *firestore.Client, userID string, witness ProducerWitness, confirmationID string, upsert *ObservationInput, clear *ClearInput) error {
	if client == nil || !auth.ValidPathSegment(userID) || !auth.ValidPathSegment(witness.ProjectID) || confirmationID == "" || confirmationID != witness.ConfirmationID {
		return ErrSourceRevisionConflict
	}
	projectRef := scopedfirestore.Collection(client, "projects").Doc(userID + "_" + witness.ProjectID)
	contextRef := projectRef.Collection("content_context")
	refs := []*firestore.DocumentRef{
		projectRef,
		contextRef.Doc("state"),
		contextRef.Doc("declaration"),
		contextRef.Doc("comparison"),
	}
	snapshots := make([]*firestore.DocumentSnapshot, len(refs))
	for index, ref := range refs {
		snapshot, err := tx.Get(ref)
		if err != nil {
			return ErrSourceRevisionConflict
		}
		snapshots[index] = snapshot
	}
	project := snapshots[0].Data()
	if project["user_id"] != userID || project["project_id"] != witness.ProjectID || projectDescriptionRevision(project) != witness.DescriptionRevision {
		return ErrSourceRevisionConflict
	}
	state := snapshots[1].Data()
	latestPublication, _ := state["latest_publication"].(map[string]interface{})
	lastGood, _ := state["last_good"].(map[string]interface{})
	if latestPublication == nil || lastGood == nil || latestPublication["generation"] != witness.PublicationGeneration ||
		!sameDigestValue(latestPublication["concepts_sha256"], witness.ConceptsDigest) ||
		!sameDigestValue(lastGood["concepts_sha256"], witness.ConceptsDigest) || !sameDigestValue(lastGood["input_digest"], witness.SummaryInputDigest) {
		return ErrSourceRevisionConflict
	}
	summaryText, _ := lastGood["text"].(string)
	summaryHash := sha256.Sum256([]byte(summaryText))
	summaryDigest := hex.EncodeToString(summaryHash[:])
	declaration := snapshots[2].Data()
	if declaration["owner_id"] != userID || declaration["project_id"] != witness.ProjectID || declaration["pair_key"] != witness.PairKey ||
		firestoreInt(declaration["description_revision"]) != witness.DescriptionRevision ||
		declaration["publication_generation"] != witness.PublicationGeneration ||
		declaration["summary_source_generation"] != lastGood["source_generation"] ||
		!sameDigestValue(declaration["concepts_sha256"], witness.ConceptsDigest) ||
		!sameDigestValue(declaration["summary_input_digest"], witness.SummaryInputDigest) ||
		!sameDigestValue(declaration["summary_text_digest"], summaryDigest) {
		return ErrSourceRevisionConflict
	}
	comparison := snapshots[3].Data()
	expectedSourceRevision := int64(-1)
	if upsert != nil {
		expectedSourceRevision = upsert.ExpectedSourceRevision
	} else if clear != nil {
		expectedSourceRevision = clear.ExpectedSourceRevision
	}
	if comparison["owner_id"] != userID || comparison["project_id"] != witness.ProjectID || comparison["pair_key"] != witness.PairKey ||
		comparison["attempt_id"] != witness.AttemptID || comparison["confirmation_id"] != confirmationID ||
		firestoreInt(comparison["expected_source_revision"]) != expectedSourceRevision ||
		comparison["declaration_id"] != declaration["declaration_id"] || comparison["summary_source_generation"] != lastGood["source_generation"] ||
		comparison["state"] != "ready" || comparison["delivery_status"] != "pending" ||
		projectDescriptionRevision(project) != witness.DescriptionRevision ||
		comparison["publication_generation"] != witness.PublicationGeneration ||
		!sameDigestValue(comparison["concepts_digest"], witness.ConceptsDigest) ||
		!sameDigestValue(comparison["summary_input_digest"], witness.SummaryInputDigest) ||
		!sameDigestValue(comparison["summary_text_digest"], summaryDigest) ||
		!firestoreTime(comparison["attempt_started_at"]).Equal(witness.AttemptStartedAt) {
		return ErrSourceRevisionConflict
	}
	verdict, _ := comparison["verdict"].(string)
	if upsert != nil {
		observation := upsert.Observation
		projectName, _ := project["name"].(string)
		if strings.TrimSpace(projectName) == "" {
			projectName = witness.ProjectID
		}
		if verdict != "clear_conflict" || observation.Severity != SeverityAttention || observation.ProjectID != witness.ProjectID ||
			observation.ProjectName != projectName || observation.DeclaredPurpose != declaration["description"] ||
			observation.PublishedSummary != summaryText || observation.Body != comparison["basis"] ||
			!sameCoverage(observation.Coverage, lastGood) || len(observation.Actions) != 1 {
			return ErrSourceRevisionConflict
		}
		action := observation.Actions[0]
		if action.Kind != "copy-then-link" || action.CopyText != summaryText || action.Target == nil ||
			action.Target.Kind != "project-description" || action.Target.ProjectID != witness.ProjectID ||
			action.Label != "Copy overview and open "+projectName+" description settings" {
			return ErrSourceRevisionConflict
		}
	} else if clear != nil && (verdict == "clear_conflict" || (verdict != "aligned" && verdict != "reasonable_extension" && verdict != "multi_topic_or_unclear" && verdict != "insufficient_evidence")) {
		return ErrSourceRevisionConflict
	}
	return nil
}

func sameCoverage(coverage Coverage, lastGood map[string]interface{}) bool {
	sampled, _ := lastGood["sampled"].(bool)
	return coverage.TotalConcepts == int(firestoreInt(lastGood["total_concepts"])) &&
		coverage.SelectedConcepts == int(firestoreInt(lastGood["selected_concepts"])) && coverage.Sampled == sampled &&
		coverage.TitleTruncatedConcepts == int(firestoreInt(lastGood["title_truncated_concepts"])) &&
		coverage.BodyTruncatedConcepts == int(firestoreInt(lastGood["body_truncated_concepts"]))
}

func firestoreInt(value interface{}) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return -1
	}
}

func projectDescriptionRevision(project map[string]interface{}) int64 {
	if _, exists := project["description_revision"]; !exists {
		return 0
	}
	return firestoreInt(project["description_revision"])
}

func firestoreTime(value interface{}) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Truncate(time.Microsecond)
	case *time.Time:
		if typed != nil {
			return typed.UTC().Truncate(time.Microsecond)
		}
	}
	return time.Time{}
}

func sameDigestValue(value interface{}, expected string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	if len(text) > 7 && text[:7] == "sha256:" {
		text = text[7:]
	}
	return text == expected
}
