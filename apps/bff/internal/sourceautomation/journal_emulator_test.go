package sourceautomation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type journalEmulatorRoot struct {
	storage.RootStore
	project storage.Store
}

func (r journalEmulatorRoot) Scope(string, string) storage.Store { return r.project }

type journalEmulatorProject struct {
	storage.Store
	data       []byte
	generation int64
	readCount  int
	readError  error
	readHook   func(*journalEmulatorProject)
}

func (p *journalEmulatorProject) ReadFileWithGeneration(_ context.Context, path string) ([]byte, int64, error) {
	if path != "raw/notes/a.md" {
		return nil, 0, storage.ErrObjectNotExist
	}
	p.readCount++
	if p.readHook != nil {
		hook := p.readHook
		p.readHook = nil
		hook(p)
	}
	if p.readError != nil {
		return nil, 0, p.readError
	}
	return append([]byte(nil), p.data...), p.generation, nil
}

func (p *journalEmulatorProject) WriteFileIfGeneration(context.Context, []byte, string, int64) (int64, error) {
	return 0, fmt.Errorf("unexpected write in source journal test")
}

func TestConfirmSourceCommitAtomicallyQueuesChangedContentOnly(t *testing.T) {
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
			addresses, lookupErr := net.LookupIP(host)
			loopback := false
			for _, address := range addresses {
				loopback = loopback || address.IsLoopback()
			}
			if lookupErr != nil || !loopback {
				t.Fatalf("FIRESTORE_EMULATOR_HOST must resolve to loopback: %q", emulator)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const ownerID, projectID = "source-auto-test-user", "source-auto-test-project"
	projectName := "lwc442-source-test"
	scope := fmt.Sprintf("lwc442-source-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", projectName)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(projectName, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()
	fs := client.Raw()
	projectRef := scopedfirestore.Collection(fs, "projects").Doc(ownerID + "_" + projectID)
	adminRef := scopedfirestore.Collection(fs, "system").Doc("pipeline_automation")
	work := profileruntime.Work{UserID: ownerID, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "source-1"}
	workRef := scopedfirestore.Collection(fs, profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	currentRef := projectRef.Collection("pipeline_source_current").Doc(pathDocumentID("notes/a.md"))
	var eventIDs []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = workRef.Delete(cleanupCtx)
		_, _ = adminRef.Delete(cleanupCtx)
		for _, id := range eventIDs {
			_, _ = projectRef.Collection("pipeline_source_events").Doc(id).Delete(cleanupCtx)
		}
		_, _ = currentRef.Delete(cleanupCtx)
		_, _ = projectRef.Delete(cleanupCtx)
	})
	content := []byte("# Existing source\n")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": ownerID, "project_id": projectID, "status": "ready", "auto_pipeline_enabled": true,
		"pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatalf("seed ready Project: %v", err)
	}
	if _, err := adminRef.Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatalf("seed Admin automation setting: %v", err)
	}
	projectStore := &journalEmulatorProject{data: content, generation: 1}
	journal := NewJournal(fs, journalEmulatorRoot{project: projectStore})
	verified := VerifiedProject{OwnerID: ownerID, ProjectID: projectID}
	first, err := journal.PrepareSourceCommit(ctx, verified, PrepareRequest{
		Entrypoint: "ui_upload", RawPath: "notes/a.md", ExpectedSHA256: digest,
	})
	if err != nil {
		t.Fatalf("prepare source commit: %v", err)
	}
	eventIDs = append(eventIDs, first.OperationID)
	result, err := journal.ConfirmSourceCommit(ctx, first, ConfirmRequest{Generation: "1", Status: "created"})
	if err != nil || result.ProcessingStatus != "pending" || result.SourceRevision != 1 {
		t.Fatalf("confirm changed source = %+v, %v; want pending revision 1", result, err)
	}
	workSnapshot, err := workRef.Get(ctx)
	if err != nil {
		t.Fatalf("read transactional source wake: %v", err)
	}
	var queued profileruntime.Work
	if err := workSnapshot.DataTo(&queued); err != nil {
		t.Fatalf("decode transactional source wake: %v", err)
	}
	if !queued.Pending || queued.Kind != "pipeline_automation" || queued.Revision != 1 {
		t.Fatalf("transactional source wake=%+v", queued)
	}

	projectStore.generation = 2
	second, err := journal.PrepareSourceCommit(ctx, verified, PrepareRequest{
		Entrypoint: "ui_upload", RawPath: "notes/a.md", ExpectedSHA256: digest,
	})
	if err != nil {
		t.Fatalf("prepare unchanged source replay: %v", err)
	}
	eventIDs = append(eventIDs, second.OperationID)
	unchanged, err := journal.ConfirmSourceCommit(ctx, second, ConfirmRequest{Generation: "2", Status: "already_exists"})
	if err != nil || unchanged.ProcessingStatus != "not_started" || unchanged.SourceRevision != 1 {
		t.Fatalf("confirm unchanged source = %+v, %v; want not_started revision 1", unchanged, err)
	}
	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatalf("read Project source revision: %v", err)
	}
	if projectSnapshot.Data()["pipeline_source_revision"] != int64(1) {
		t.Fatalf("unchanged content advanced revision: %v", projectSnapshot.Data()["pipeline_source_revision"])
	}
	if _, err := workRef.Get(ctx); err != nil {
		t.Fatalf("source wake disappeared after unchanged replay: %v", err)
	}

	proposed := []byte("# Generated source with a newer server timestamp\n")
	proposedDigest := sha256.Sum256(proposed)
	payload := sha256.Sum256([]byte("normalized MCP add_raw payload"))
	prepareNoChange := func() Ref {
		ref, err := journal.PrepareSourceCommit(ctx, verified, PrepareRequest{
			Entrypoint: "mcp_add_raw", RawPath: "notes/a.md", ExpectedSHA256: hex.EncodeToString(proposedDigest[:]),
			UsePayloadSHA256: hex.EncodeToString(payload[:]),
		})
		if err != nil {
			t.Fatalf("prepare no-change receipt: %v", err)
		}
		eventIDs = append(eventIDs, ref.OperationID)
		return ref
	}
	requestFor := func(observed []byte, generation int64) NoChangeUseRequest {
		observedDigest := sha256.Sum256(observed)
		return NoChangeUseRequest{
			PayloadSHA256: hex.EncodeToString(payload[:]), ObservedSHA256: hex.EncodeToString(observedDigest[:]),
			ObservedGeneration: fmt.Sprint(generation), Equivalent: func(value []byte) bool { return string(value) == string(content) },
		}
	}

	noChangeRef := prepareNoChange()
	observed, observedGeneration, err := projectStore.ReadFileWithGeneration(ctx, "raw/notes/a.md")
	if err != nil {
		t.Fatalf("initial no-change observation: %v", err)
	}
	prepareSnapshot, err := eventDocument(fs, noChangeRef).Get(ctx)
	if err != nil {
		t.Fatalf("read no-change preparation: %v", err)
	}
	acceptedAt, ok := prepareSnapshot.Data()["created_at"].(time.Time)
	if !ok {
		t.Fatalf("no-change prepare has no server timestamp: %#v", prepareSnapshot.Data())
	}
	otherUseAt := acceptedAt.Add(time.Second)
	if _, err := projectRef.Update(ctx, []cloudfirestore.Update{{Path: "last_used_at", Value: otherUseAt}}); err != nil {
		t.Fatalf("seed later Project use: %v", err)
	}
	noChangeRequest := requestFor(observed, observedGeneration)
	receipt, err := journal.RecordNoChangeProjectUse(ctx, noChangeRef, noChangeRequest)
	if err != nil || !receipt.AcceptedAt.Equal(acceptedAt) || receipt.ObservedGeneration != fmt.Sprint(observedGeneration) {
		t.Fatalf("record verified no-change receipt=%+v err=%v", receipt, err)
	}
	projectSnapshot, err = projectRef.Get(ctx)
	if err != nil || !projectSnapshot.Data()["last_used_at"].(time.Time).Equal(otherUseAt) {
		t.Fatalf("late no-change receipt moved Project use backward: data=%#v err=%v", projectSnapshot.Data(), err)
	}
	noChangeEvent, err := eventDocument(fs, noChangeRef).Get(ctx)
	if err != nil || noChangeEvent.Data()["commit_state"] != "aborted" || noChangeEvent.Data()["use_state"] != "recorded_no_change" || noChangeEvent.Data()["processing_status"] != "not_started" {
		t.Fatalf("no-change receipt claimed a source commit: data=%#v err=%v", noChangeEvent.Data(), err)
	}
	readsAfterReceipt := projectStore.readCount
	projectStore.readError = storage.ErrObjectNotExist
	replayedReceipt, err := journal.RecordNoChangeProjectUse(ctx, noChangeRef, noChangeRequest)
	projectStore.readError = nil
	if err != nil || replayedReceipt != receipt || projectStore.readCount != readsAfterReceipt {
		t.Fatalf("durable receipt replay reread or changed its result: receipt=%+v err=%v reads=%d/%d", replayedReceipt, err, projectStore.readCount, readsAfterReceipt)
	}

	changedRef := prepareNoChange()
	changedObservation, changedGeneration, err := projectStore.ReadFileWithGeneration(ctx, "raw/notes/a.md")
	if err != nil {
		t.Fatalf("initial replacement observation: %v", err)
	}
	projectStore.readHook = func(store *journalEmulatorProject) {
		store.data = []byte("# Replaced between reads\n")
		store.generation++
	}
	if _, err := journal.RecordNoChangeProjectUse(ctx, changedRef, requestFor(changedObservation, changedGeneration)); !errors.Is(err, ErrSourceCommitUnconfirmed) {
		t.Fatalf("changed generation was accepted as no-change: %v", err)
	}
	projectStore.data, projectStore.generation = content, observedGeneration

	deletedRef := prepareNoChange()
	deletedObservation, deletedGeneration, err := projectStore.ReadFileWithGeneration(ctx, "raw/notes/a.md")
	if err != nil {
		t.Fatalf("initial deletion observation: %v", err)
	}
	projectStore.readHook = func(store *journalEmulatorProject) { store.readError = storage.ErrObjectNotExist }
	if _, err := journal.RecordNoChangeProjectUse(ctx, deletedRef, requestFor(deletedObservation, deletedGeneration)); !errors.Is(err, ErrSourceCommitUnconfirmed) {
		t.Fatalf("deleted raw object was accepted as no-change: %v", err)
	}
	projectStore.readError = nil

	for _, failedRef := range []Ref{changedRef, deletedRef} {
		failedEvent, err := eventDocument(fs, failedRef).Get(ctx)
		if err != nil || failedEvent.Data()["use_state"] != nil || failedEvent.Data()["commit_state"] != "prepared" {
			t.Errorf("failed readback wrote a receipt for %s: data=%#v err=%v", failedRef.OperationID, failedEvent.Data(), err)
		}
	}
	projectSnapshot, err = projectRef.Get(ctx)
	if err != nil || !projectSnapshot.Data()["last_used_at"].(time.Time).Equal(otherUseAt) || projectSnapshot.Data()["pipeline_source_revision"] != int64(1) {
		t.Fatalf("failed no-change readback touched usage or pipeline state: data=%#v err=%v", projectSnapshot.Data(), err)
	}

	documentAcceptanceAt := time.Now().UTC().Add(-2 * time.Minute)
	otherUseAfterDocumentAt := time.Now().UTC().Add(-time.Minute)
	if _, err := projectRef.Update(ctx, []cloudfirestore.Update{{Path: "last_used_at", Value: documentAcceptanceAt}}); err != nil {
		t.Fatalf("seed document acceptance use time: %v", err)
	}
	if _, err := projectRef.Update(ctx, []cloudfirestore.Update{{Path: "last_used_at", Value: otherUseAfterDocumentAt}}); err != nil {
		t.Fatalf("seed later explicit Project use: %v", err)
	}
	contentDigest := sha256.Sum256(content)
	documentSourceRef, err := journal.PrepareSourceCommit(ctx, verified, PrepareRequest{
		Entrypoint: "document_import", RawPath: "notes/a.md", ExpectedSHA256: hex.EncodeToString(contentDigest[:]),
	})
	if err != nil {
		t.Fatalf("prepare converted document source: %v", err)
	}
	eventIDs = append(eventIDs, documentSourceRef.OperationID)
	if _, err := journal.ConfirmSourceCommit(ctx, documentSourceRef, ConfirmRequest{
		Generation: fmt.Sprint(projectStore.generation), Status: "already_exists",
	}); err != nil {
		t.Fatalf("confirm converted document source: %v", err)
	}
	projectSnapshot, err = projectRef.Get(ctx)
	if err != nil || !projectSnapshot.Data()["last_used_at"].(time.Time).Equal(otherUseAfterDocumentAt) {
		t.Fatalf("background document source save advanced last_used_at: data=%#v err=%v", projectSnapshot.Data(), err)
	}
	if projectSnapshot.Data()["pipeline_source_revision"] != int64(1) {
		t.Fatalf("document source save advanced pipeline revision: %v", projectSnapshot.Data()["pipeline_source_revision"])
	}

}
