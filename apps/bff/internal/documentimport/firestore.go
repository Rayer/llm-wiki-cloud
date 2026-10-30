package documentimport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrCursorStale = errors.New("document import cursor is stale")

type FirestoreRepository struct {
	client *cloudfirestore.Client
}

type projectState struct {
	FailedCount           int64
	LatestFailureSequence int64
	Revision              int64
}

type listCursor struct {
	ProjectID  string    `json:"project_id"`
	Filter     string    `json:"filter"`
	Revision   int64     `json:"revision"`
	Phase      string    `json:"phase"`
	UpdatedAt  time.Time `json:"updated_at"`
	DocumentID string    `json:"document_id"`
}

func NewFirestoreRepository(client *cloudfirestore.Client) *FirestoreRepository {
	return &FirestoreRepository{client: client}
}

func (r *FirestoreRepository) Begin(ctx context.Context, record Record) (Record, bool, error) {
	if r == nil || r.client == nil || !safeSegment(record.OwnerID) || !safeSegment(record.ProjectID) ||
		record.ClientSubmissionID == "" || record.DocumentID != "" || record.SHA256 == "" {
		return Record{}, false, errors.New("document import repository is unavailable")
	}
	project := r.projectRef(record.OwnerID, record.ProjectID)
	documents := project.Collection("document_imports")
	recordRef := documents.NewDoc()
	keyRef := project.Collection("document_import_submissions").Doc(submissionKey(record.ClientSubmissionID))
	now := time.Now().UTC()
	record.DocumentID = recordRef.ID
	record.SchemaVersion = SchemaVersion
	record.OriginalObjectPath = OriginalPath(record.DocumentID)
	record.CreatedAt = now
	record.UpdatedAt = now
	record.Revision = 1
	record.ConversionState = ConversionReceiving
	record.SourceState = SourceNotStarted
	record.DeletionState = DeletionPresent
	record.DispatchState = "prepared"
	record.AttemptID = record.DocumentID + "-a1"

	created := false
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		created = false
		key, err := tx.Get(keyRef)
		if err == nil {
			id, _ := key.Data()["document_id"].(string)
			if !safeSegment(id) {
				return ErrConflict
			}
			existingSnap, err := tx.Get(documents.Doc(id))
			if err != nil {
				return err
			}
			var existing Record
			if err := existingSnap.DataTo(&existing); err != nil {
				return err
			}
			if existing.ClientSubmissionID != record.ClientSubmissionID || existing.OriginalFilename != record.OriginalFilename ||
				existing.Bytes != record.Bytes || existing.SHA256 != record.SHA256 {
				return ErrConflict
			}
			record = existing
			return nil
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
		if _, err := tx.Get(project); err != nil {
			return err
		}
		if err := tx.Create(recordRef, record); err != nil {
			return err
		}
		if err := tx.Create(keyRef, map[string]any{
			"document_id":                 record.DocumentID,
			"client_submission_id_sha256": submissionKey(record.ClientSubmissionID),
			"created_at":                  now,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return record, created, err
}

func (r *FirestoreRepository) Get(ctx context.Context, ownerID, projectID, documentID string) (Record, error) {
	if r == nil || r.client == nil || !safeSegment(ownerID) || !safeSegment(projectID) || !safeSegment(documentID) {
		return Record{}, ErrNotFound
	}
	snap, err := r.documentsRef(ownerID, projectID).Doc(documentID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := snap.DataTo(&record); err != nil {
		return Record{}, err
	}
	if record.OwnerID != ownerID || record.ProjectID != projectID || record.DocumentID != documentID {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (r *FirestoreRepository) BySubmission(ctx context.Context, ownerID, projectID, submissionID string) (Record, bool, error) {
	if r == nil || r.client == nil || !safeSegment(ownerID) || !safeSegment(projectID) || submissionID == "" {
		return Record{}, false, errors.New("document import repository is unavailable")
	}
	key, err := r.projectRef(ownerID, projectID).Collection("document_import_submissions").Doc(submissionKey(submissionID)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	id, _ := key.Data()["document_id"].(string)
	record, err := r.Get(ctx, ownerID, projectID, id)
	if err != nil {
		return Record{}, false, err
	}
	if record.ClientSubmissionID != submissionID {
		return Record{}, false, ErrNotFound
	}
	return record, true, nil
}

func (r *FirestoreRepository) CompleteAcceptance(ctx context.Context, ownerID, projectID, documentID string, generation int64) (Record, error) {
	if generation <= 0 {
		return Record{}, errors.New("original object generation is unavailable")
	}
	project := r.projectRef(ownerID, projectID)
	recordRef := r.documentsRef(ownerID, projectID).Doc(documentID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		recordSnap, err := tx.Get(recordRef)
		if err != nil {
			return err
		}
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		var record Record
		if err := recordSnap.DataTo(&record); err != nil {
			return err
		}
		if record.ConversionState == ConversionAccepted || record.ConversionState == ConversionQueued || record.ConversionState == ConversionConverting ||
			record.ConversionState == ConversionSucceeded || record.ConversionState == ConversionFailed {
			if record.OriginalGeneration != generation {
				return ErrConflict
			}
			return nil
		}
		if record.ConversionState != ConversionReceiving || record.DeletionState != DeletionPresent {
			return ErrStateConflict
		}
		record.OriginalGeneration = generation
		record.ConversionState = ConversionAccepted
		record.DispatchState = "pending"
		record.UpdatedAt = time.Now().UTC()
		record.Revision++
		state := readProjectState(projectSnap.Data())
		state.Revision++
		if err := tx.Set(recordRef, record); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{
			"document_import_failed_count":            state.FailedCount,
			"document_import_latest_failure_sequence": state.LatestFailureSequence,
			"document_import_revision":                state.Revision,
		}, cloudfirestore.MergeAll)
	})
	if err != nil {
		return Record{}, err
	}
	return r.Get(ctx, ownerID, projectID, documentID)
}

func (r *FirestoreRepository) SetDispatchState(ctx context.Context, ownerID, projectID, documentID, state, executionID string) error {
	project := r.projectRef(ownerID, projectID)
	recordRef := r.documentsRef(ownerID, projectID).Doc(documentID)
	return r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		recordSnap, err := tx.Get(recordRef)
		if err != nil {
			return err
		}
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		var record Record
		if err := recordSnap.DataTo(&record); err != nil {
			return err
		}
		if record.ConversionState == ConversionReceiving || record.DeletionState != DeletionPresent {
			return ErrStateConflict
		}
		record.DispatchState = state
		if executionID != "" {
			record.JobExecutionID = executionID
		}
		record.UpdatedAt = time.Now().UTC()
		record.Revision++
		projectRevision := readProjectState(projectSnap.Data()).Revision + 1
		if err := tx.Set(recordRef, record); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{"document_import_revision": projectRevision}, cloudfirestore.MergeAll)
	})
}

func (r *FirestoreRepository) FailConversion(ctx context.Context, ownerID, projectID, documentID string, failure Failure) (Record, error) {
	project := r.projectRef(ownerID, projectID)
	recordRef := r.documentsRef(ownerID, projectID).Doc(documentID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		recordSnap, err := tx.Get(recordRef)
		if err != nil {
			return err
		}
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		var record Record
		if err := recordSnap.DataTo(&record); err != nil {
			return err
		}
		if record.ConversionState == ConversionFailed {
			return nil
		}
		if record.ConversionState == ConversionSucceeded || record.DeletionState != DeletionPresent {
			return ErrStateConflict
		}
		state := readProjectState(projectSnap.Data())
		state.FailedCount++
		state.LatestFailureSequence++
		state.Revision++
		record.ConversionState = ConversionFailed
		record.Failure = &failure
		record.FailureSequence = state.LatestFailureSequence
		record.UpdatedAt = time.Now().UTC()
		record.Revision++
		if err := tx.Set(recordRef, record); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{
			"document_import_failed_count":            state.FailedCount,
			"document_import_latest_failure_sequence": state.LatestFailureSequence,
			"document_import_revision":                state.Revision,
		}, cloudfirestore.MergeAll)
	})
	if err != nil {
		return Record{}, err
	}
	return r.Get(ctx, ownerID, projectID, documentID)
}

func (r *FirestoreRepository) Dismiss(ctx context.Context, ownerID, projectID, actorID string, sequence int64) (Summary, error) {
	project := r.projectRef(ownerID, projectID)
	notice := project.Collection("document_import_notices").Doc(actorKey(actorID))
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		noticeSnap, err := tx.Get(notice)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		state := readProjectState(projectSnap.Data())
		if sequence < 0 || sequence > state.LatestFailureSequence {
			return ErrConflict
		}
		current := int64(0)
		if err == nil {
			current = intField(noticeSnap.Data()["dismissed_through_sequence"])
		}
		if sequence > current {
			current = sequence
		}
		state.Revision++
		if err := tx.Set(notice, map[string]any{
			"owner_id":                   ownerID,
			"project_id":                 projectID,
			"actor_id_sha256":            actorKey(actorID),
			"dismissed_through_sequence": current,
			"updated_at":                 time.Now().UTC(),
		}); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{"document_import_revision": state.Revision}, cloudfirestore.MergeAll)
	})
	if err != nil {
		return Summary{}, err
	}
	return r.Summary(ctx, ownerID, projectID, actorID, "")
}

func (r *FirestoreRepository) Summary(ctx context.Context, ownerID, projectID, actorID, siteURL string) (Summary, error) {
	project := r.projectRef(ownerID, projectID)
	projectSnap, err := project.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Summary{}, ErrNotFound
	}
	if err != nil {
		return Summary{}, err
	}
	state := readProjectState(projectSnap.Data())
	dismissed := int64(0)
	if actorID != "" {
		notice, err := project.Collection("document_import_notices").Doc(actorKey(actorID)).Get(ctx)
		if err == nil {
			dismissed = intField(notice.Data()["dismissed_through_sequence"])
		} else if status.Code(err) != codes.NotFound {
			return Summary{}, err
		}
	}
	return Summary{
		FailedCount: state.FailedCount, LatestFailureSequence: state.LatestFailureSequence,
		ReminderVisible:          state.FailedCount > 0 && state.LatestFailureSequence > dismissed,
		DismissedThroughSequence: dismissed, SiteURL: siteURL, SnapshotRevision: state.Revision,
	}, nil
}

func (r *FirestoreRepository) MarkDeleting(ctx context.Context, ownerID, projectID, documentID string, revision int64) (Record, error) {
	project := r.projectRef(ownerID, projectID)
	recordRef := r.documentsRef(ownerID, projectID).Doc(documentID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		recordSnap, err := tx.Get(recordRef)
		if err != nil {
			return err
		}
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		var record Record
		if err := recordSnap.DataTo(&record); err != nil {
			return err
		}
		if record.DeletionState == DeletionComplete {
			return nil
		}
		if record.Revision != revision || (record.DeletionState != DeletionPresent && record.DeletionState != DeletionFailed) ||
			(!(record.ConversionState == ConversionFailed) && !(record.SourceState == SourceFailed && record.SourceRef == nil)) {
			return ErrStateConflict
		}
		record.DeletionState = DeletionInProgress
		record.LeaseToken = ""
		record.Revision++
		record.UpdatedAt = time.Now().UTC()
		state := readProjectState(projectSnap.Data())
		state.Revision++
		if err := tx.Set(recordRef, record); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{"document_import_revision": state.Revision}, cloudfirestore.MergeAll)
	})
	if err != nil {
		return Record{}, err
	}
	return r.Get(ctx, ownerID, projectID, documentID)
}

func (r *FirestoreRepository) FinishDeletion(ctx context.Context, ownerID, projectID, documentID string, success bool) (Record, error) {
	project := r.projectRef(ownerID, projectID)
	recordRef := r.documentsRef(ownerID, projectID).Doc(documentID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		recordSnap, err := tx.Get(recordRef)
		if err != nil {
			return err
		}
		projectSnap, err := tx.Get(project)
		if err != nil {
			return err
		}
		var record Record
		if err := recordSnap.DataTo(&record); err != nil {
			return err
		}
		if record.DeletionState == DeletionComplete {
			return nil
		}
		if record.DeletionState != DeletionInProgress && record.DeletionState != DeletionFailed {
			return ErrStateConflict
		}
		state := readProjectState(projectSnap.Data())
		if success {
			if FailedCounted(record) && state.FailedCount > 0 {
				state.FailedCount--
			}
			record.DeletionState = DeletionComplete
		} else {
			record.DeletionState = DeletionFailed
		}
		record.Revision++
		record.UpdatedAt = time.Now().UTC()
		state.Revision++
		if err := tx.Set(recordRef, record); err != nil {
			return err
		}
		return tx.Set(project, map[string]any{
			"document_import_failed_count": state.FailedCount,
			"document_import_revision":     state.Revision,
		}, cloudfirestore.MergeAll)
	})
	if err != nil {
		return Record{}, err
	}
	return r.Get(ctx, ownerID, projectID, documentID)
}

func (r *FirestoreRepository) List(ctx context.Context, ownerID, projectID, filter, cursor string, limit int) (ListResult, error) {
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "pending" && filter != "succeeded" && filter != "failed" {
		return ListResult{}, ErrConflict
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	project := r.projectRef(ownerID, projectID)
	projectSnap, err := project.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return ListResult{}, ErrNotFound
	}
	if err != nil {
		return ListResult{}, err
	}
	state := readProjectState(projectSnap.Data())
	var current *listCursor
	if cursor != "" {
		decoded, err := decodeCursor(cursor)
		if err != nil || decoded.ProjectID != projectID || decoded.Filter != filter || decoded.Revision != state.Revision {
			return ListResult{}, ErrCursorStale
		}
		current = &decoded
	}
	items := make([]Record, 0, limit)
	var next *listCursor
	phase := ""
	if filter == "all" || filter == "failed" {
		phase = "failed"
	}
	if current != nil {
		phase = current.Phase
	}
	if phase == "failed" {
		start := current
		failed, more, err := r.queryPage(ctx, ownerID, projectID, "failed", start, limit+1)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, failed...)
		if more {
			items = items[:limit]
			next = makeCursor(projectID, filter, state.Revision, "failed", items[len(items)-1])
		} else if filter == "all" && len(items) < limit {
			nonfailed, nonfailedMore, err := r.queryPage(ctx, ownerID, projectID, "nonfailed", nil, limit-len(items)+1)
			if err != nil {
				return ListResult{}, err
			}
			items = append(items, nonfailed...)
			if len(items) > limit {
				items = items[:limit]
				next = makeCursor(projectID, filter, state.Revision, "nonfailed", items[len(items)-1])
			} else if nonfailedMore {
				next = makeCursor(projectID, filter, state.Revision, "nonfailed", items[len(items)-1])
			}
		}
	} else {
		phase = filter
		if filter == "all" {
			phase = "nonfailed"
		}
		page, more, err := r.queryPage(ctx, ownerID, projectID, phase, current, limit+1)
		if err != nil {
			return ListResult{}, err
		}
		items = page
		if more {
			items = items[:limit]
			next = makeCursor(projectID, filter, state.Revision, phase, items[len(items)-1])
		}
	}
	for i := range items {
		items[i] = PublicRecord(items[i])
	}
	if next != nil {
		encoded, err := encodeCursor(*next)
		if err != nil {
			return ListResult{}, err
		}
		return ListResult{Items: items, NextCursor: encoded, SnapshotRevision: state.Revision, FailedCount: state.FailedCount, LatestFailureSequence: state.LatestFailureSequence}, nil
	}
	return ListResult{Items: items, SnapshotRevision: state.Revision, FailedCount: state.FailedCount, LatestFailureSequence: state.LatestFailureSequence}, nil
}

func (r *FirestoreRepository) queryPage(ctx context.Context, ownerID, projectID, phase string, cursor *listCursor, limit int) ([]Record, bool, error) {
	query := r.documentsRef(ownerID, projectID).OrderBy("updated_at", cloudfirestore.Desc).OrderBy("document_id", cloudfirestore.Asc)
	switch phase {
	case "failed":
		query = query.Where("conversion_state", "==", string(ConversionFailed))
	case "pending":
		query = query.Where("conversion_state", "in", []string{string(ConversionAccepted), string(ConversionQueued), string(ConversionConverting)})
	case "succeeded":
		query = query.Where("conversion_state", "==", string(ConversionSucceeded))
	case "all", "nonfailed":
		query = query.Where("conversion_state", "in", []string{string(ConversionAccepted), string(ConversionQueued), string(ConversionConverting), string(ConversionSucceeded)})
	default:
		return nil, false, ErrConflict
	}
	if cursor != nil {
		query = query.StartAfter(cursor.UpdatedAt, cursor.DocumentID)
	}
	snapshots, err := query.Limit(limit).Documents(ctx).GetAll()
	if errors.Is(err, iterator.Done) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	more := len(snapshots) == limit
	items := make([]Record, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var record Record
		if err := snapshot.DataTo(&record); err != nil {
			return nil, false, err
		}
		if record.OwnerID != ownerID || record.ProjectID != projectID {
			continue
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].DocumentID < items[j].DocumentID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, more, nil
}

func (r *FirestoreRepository) projectRef(ownerID, projectID string) *cloudfirestore.DocumentRef {
	return scopedfirestore.Collection(r.client, "projects").Doc(ownerID + "_" + projectID)
}

func (r *FirestoreRepository) documentsRef(ownerID, projectID string) *cloudfirestore.CollectionRef {
	return r.projectRef(ownerID, projectID).Collection("document_imports")
}

func readProjectState(data map[string]any) projectState {
	return projectState{
		FailedCount:           intField(data["document_import_failed_count"]),
		LatestFailureSequence: intField(data["document_import_latest_failure_sequence"]),
		Revision:              intField(data["document_import_revision"]),
	}
}

func intField(value any) int64 {
	switch value := value.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func submissionKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func actorKey(value string) string { return submissionKey(value) }

func encodeCursor(cursor listCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeCursor(value string) (listCursor, error) {
	var cursor listCursor
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &cursor) != nil || cursor.DocumentID == "" || cursor.Phase == "" || cursor.Revision < 0 {
		return listCursor{}, ErrCursorStale
	}
	return cursor, nil
}

func makeCursor(projectID, filter string, revision int64, phase string, record Record) *listCursor {
	return &listCursor{ProjectID: projectID, Filter: filter, Revision: revision, Phase: phase, UpdatedAt: record.UpdatedAt, DocumentID: record.DocumentID}
}

func ensureSafePath(record Record) error {
	if record.OriginalObjectPath != OriginalPath(record.DocumentID) || record.DocumentID == "" || record.OriginalGeneration <= 0 {
		return fmt.Errorf("document original identity is invalid")
	}
	return nil
}
