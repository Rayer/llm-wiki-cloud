package documentimport

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
)

func TestListPaginationBoundariesAndFailedPhaseCursor(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failed    int
		nonfailed int
	}{
		{name: "limit minus one", failed: 1, nonfailed: 1},
		{name: "limit", failed: 1, nonfailed: 2},
		{name: "limit plus one", failed: 1, nonfailed: 3},
		{name: "failed group exactly fills first page", failed: 3, nonfailed: 2},
		{name: "failed group has lookahead", failed: 4, nonfailed: 2},
		{name: "failed to nonfailed on second page", failed: 2, nonfailed: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			repo, closeClient := newDocumentImportRepository(t)
			t.Cleanup(closeClient)
			ownerID := "list-owner"
			projectID := fmt.Sprintf("list-project-%d", time.Now().UnixNano())
			projectRef := repo.projectRef(ownerID, projectID)
			if _, err := projectRef.Set(ctx, map[string]any{
				"document_import_failed_count":            int64(tc.failed),
				"document_import_latest_failure_sequence": int64(tc.failed),
				"document_import_revision":                int64(7),
			}); err != nil {
				t.Fatalf("seed Project: %v", err)
			}
			want := make(map[string]bool, tc.failed+tc.nonfailed)
			base := time.Now().UTC()
			for i := 0; i < tc.failed+tc.nonfailed; i++ {
				id := fmt.Sprintf("document-%03d", i)
				state := ConversionFailed
				if i >= tc.failed {
					state = ConversionSucceeded
				}
				record := Record{
					SchemaVersion:   SchemaVersion,
					OwnerID:         ownerID,
					ProjectID:       projectID,
					DocumentID:      id,
					UpdatedAt:       base.Add(-time.Duration(i) * time.Second),
					Revision:        1,
					ConversionState: state,
					SourceState:     SourceSaved,
					DeletionState:   DeletionPresent,
				}
				if _, err := repo.documentsRef(ownerID, projectID).Doc(id).Set(ctx, record); err != nil {
					t.Fatalf("seed document %s: %v", id, err)
				}
				want[id] = true
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				for id := range want {
					_, _ = repo.documentsRef(ownerID, projectID).Doc(id).Delete(cleanupCtx)
				}
				_, _ = projectRef.Delete(cleanupCtx)
			})

			seen := make(map[string]bool, len(want))
			cursor := ""
			for pageNumber := 0; pageNumber < 10; pageNumber++ {
				page, err := repo.List(ctx, ownerID, projectID, "all", cursor, 3)
				if err != nil {
					t.Fatalf("List page %d: %v", pageNumber+1, err)
				}
				if len(page.Items) > 3 {
					t.Fatalf("List page %d returned %d items, want at most 3", pageNumber+1, len(page.Items))
				}
				if page.FailedCount != int64(tc.failed) || page.SnapshotRevision != 7 {
					t.Fatalf("List page %d summary=(failed:%d revision:%d), want (%d, 7)", pageNumber+1, page.FailedCount, page.SnapshotRevision, tc.failed)
				}
				for _, record := range page.Items {
					if seen[record.DocumentID] {
						t.Fatalf("duplicate document %s", record.DocumentID)
					}
					seen[record.DocumentID] = true
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
				if pageNumber == 9 {
					t.Fatal("List pagination did not terminate")
				}
			}
			if len(seen) != len(want) {
				t.Fatalf("listed %d unique documents, want %d; seen=%v", len(seen), len(want), seen)
			}
			for id := range want {
				if !seen[id] {
					t.Errorf("document %s was lost between pages", id)
				}
			}
		})
	}
}

func TestAcceptedJobKeepsWorkAndClaimRecoveryFencesStaleWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, closeClient := newDocumentImportRepository(t)
	t.Cleanup(closeClient)
	ownerID, projectID, documentID := "lease-owner", "lease-project", "document-1"
	projectRef := repo.projectRef(ownerID, projectID)
	recordRef := repo.documentsRef(ownerID, projectID).Doc(documentID)
	if _, err := projectRef.Set(ctx, map[string]any{"document_import_revision": int64(0)}); err != nil {
		t.Fatalf("seed Project: %v", err)
	}
	record := Record{
		SchemaVersion: SchemaVersion, OwnerID: ownerID, ProjectID: projectID, DocumentID: documentID,
		AttemptID: documentID + "-a1", ConversionState: ConversionAccepted, SourceState: SourceNotStarted,
		DeletionState: DeletionPresent, UpdatedAt: time.Now().UTC(), Revision: 1,
	}
	if _, err := recordRef.Set(ctx, record); err != nil {
		t.Fatalf("seed accepted document: %v", err)
	}
	work := profileruntime.Work{
		UserID: ownerID, ProjectID: projectID, Kind: "document_import", Revision: 1,
		ID: record.AttemptID, DocumentID: documentID, Due: time.Now().UTC(), Pending: true,
		Attempts: 1, Token: "dispatcher-lease", LeaseUntil: time.Now().UTC().Add(time.Minute), Status: "running",
	}
	workRef := scopedfirestore.Collection(repo.client, profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := workRef.Set(ctx, work); err != nil {
		t.Fatalf("seed claimed dispatch work: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = workRef.Delete(cleanupCtx)
		_, _ = recordRef.Delete(cleanupCtx)
		_, _ = projectRef.Delete(cleanupCtx)
	})

	if err := repo.SetDispatchState(ctx, ownerID, projectID, documentID, "started", "execution-1"); err != nil {
		t.Fatalf("record accepted Job execution: %v", err)
	}
	workSnapshot, err := workRef.Get(ctx)
	if err != nil {
		t.Fatalf("read durable dispatch work: %v", err)
	}
	var retained profileruntime.Work
	if err := workSnapshot.DataTo(&retained); err != nil {
		t.Fatalf("decode durable dispatch work: %v", err)
	}
	if !retained.Pending || retained.Status != "running" || retained.Token != "dispatcher-lease" {
		t.Fatalf("accepted Job consumed active dispatch lease: %+v", retained)
	}

	claimedRecord, staleToken, claimed, err := repo.Claim(ctx, ownerID, projectID, documentID, record.AttemptID, 5*time.Minute)
	if err != nil || !claimed || claimedRecord.AttemptCount != 1 {
		t.Fatalf("claim first worker: record=%+v claimed=%t err=%v", claimedRecord, claimed, err)
	}
	claimedRecord.LeaseUntil = time.Now().UTC().Add(-time.Second)
	if _, err := recordRef.Set(ctx, claimedRecord); err != nil {
		t.Fatalf("expire crashed worker lease: %v", err)
	}
	_, currentToken, claimed, err := repo.Claim(ctx, ownerID, projectID, documentID, record.AttemptID, 5*time.Minute)
	if err != nil || !claimed || currentToken == staleToken {
		t.Fatalf("recover after worker crash: claimed=%t tokenChanged=%t err=%v", claimed, currentToken != staleToken, err)
	}
	if _, err := repo.CompleteConversion(ctx, ownerID, projectID, documentID, staleToken, ".lwc/document-imports/document-1/source.md", strings.Repeat("a", 64), "lock", 1); err != ErrStateConflict {
		t.Fatalf("stale worker completion error = %v, want %v", err, ErrStateConflict)
	}
	completed, err := repo.CompleteConversion(ctx, ownerID, projectID, documentID, currentToken, ".lwc/document-imports/document-1/source.md", strings.Repeat("b", 64), "lock", 2)
	if err != nil || completed.ConversionState != ConversionSucceeded || completed.SourceState != SourceSaving {
		t.Fatalf("current worker completion = %+v, %v", completed, err)
	}
}

func newDocumentImportRepository(t *testing.T) (*FirestoreRepository, func()) {
	t.Helper()
	emulator := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulator == "" {
		t.Skip("requires loopback Firestore emulator")
	}
	host, _, err := net.SplitHostPort(emulator)
	if err != nil {
		t.Fatalf("FIRESTORE_EMULATOR_HOST must be host:port: %v", err)
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			t.Fatalf("FIRESTORE_EMULATOR_HOST must be a literal loopback host: %q", emulator)
		}
	}
	projectName := fmt.Sprintf("document-import-list-%d", time.Now().UnixNano())
	scope := fmt.Sprintf("document-import-list-%d", time.Now().UnixNano())
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(projectName, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	return NewFirestoreRepository(client.Raw()), func() { _ = client.Close() }
}
