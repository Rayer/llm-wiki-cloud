package sourceautomation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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
}

func (p *journalEmulatorProject) ReadFileWithGeneration(_ context.Context, path string) ([]byte, int64, error) {
	if path != "raw/notes/a.md" {
		return nil, 0, storage.ErrObjectNotExist
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

}
