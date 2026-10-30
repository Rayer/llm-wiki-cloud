package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestPipelineAutomationRawSyncToLocalConsumer(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	owner, projectID := "automation-e2e-owner", "wiki"
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1),
		"pipeline_source_revision": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineAutomationAdminRef(client.Raw()).Set(ctx, map[string]interface{}{
		"allow_auto_pipeline": true, "revision": int64(1),
	}); err != nil {
		t.Fatal(err)
	}

	root := localfs.New(t.TempDir())
	h := New(root, client, nil, nil, nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	local := &pipelineAutomationLocalExecutor{}
	h.SetLocalPipelineExecutor(local)

	body := []byte("# Local automation fixture\n\nNo model or cloud worker is used by this test.\n")
	digest := sha256.Sum256(body)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/raw-sync/upload?path=notes/fixture.md", strings.NewReader(string(body)))
	c.Request.Header.Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	c.Set("userID", owner)
	c.Set("projectID", projectID)
	h.SyncRawUpload(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("raw sync status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var upload struct {
		ProcessingStatus string `json:"processing_status"`
		SourceRevision   int64  `json:"source_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &upload); err != nil || upload.ProcessingStatus != "pending" || upload.SourceRevision != 1 {
		t.Fatalf("raw sync response=%+v decode=%v body=%s", upload, err, recorder.Body.String())
	}
	stored, err := root.Scope(owner, projectID).ReadFile(ctx, "raw/notes/fixture.md")
	if err != nil || string(stored) != string(body) {
		t.Fatalf("committed source=%q err=%v", stored, err)
	}

	repo := h.profileRepository.(*firestoreProfileRepository)
	dispatcher := &ProfileDispatcher{Handler: h}
	automationWork := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "source-1"}
	if err := dispatcher.execute(ctx, repo, automationWork); err != nil {
		t.Fatalf("consume source commit work: %v", err)
	}

	works, err := scopedfirestore.Collection(client.Raw(), profileruntime.WorkCollection).Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	var pipelineWork profileruntime.Work
	for _, snapshot := range works {
		var work profileruntime.Work
		if err := snapshot.DataTo(&work); err != nil {
			t.Fatal(err)
		}
		if work.UserID == owner && work.ProjectID == projectID && work.Kind == "pipeline" {
			pipelineWork = work
		}
	}
	if pipelineWork.ID == "" {
		t.Fatalf("source consumer did not enqueue a pipeline intent; works=%v", works)
	}
	if err := dispatcher.execute(ctx, repo, pipelineWork); err != nil {
		t.Fatalf("consume local Pipeline intent: %v", err)
	}
	if local.starts != 1 || !strings.HasPrefix(local.reservationID, "auto-") {
		t.Fatalf("local pipeline starts=%d reservation=%q", local.starts, local.reservationID)
	}

	reservation, err := scopedfirestore.Collection(client.Raw(), "pipeline_quota_reservations").Doc(local.reservationID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Data()["trigger"] != "source_commit" || reservation.Data()["budget_class"] != "none" || reservation.Data()["source_revision"] != int64(1) {
		t.Fatalf("automatic reservation=%v", reservation.Data())
	}
	project, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if project.Data()["pipeline_automation_status"] != "running" {
		t.Fatalf("Project processing state=%v", project.Data())
	}
	if _, ok := root.Scope(owner, projectID).(storage.RawSyncStore); !ok {
		t.Fatal("local Project storage did not retain its RawSyncStore contract")
	}
}

type pipelineAutomationLocalExecutor struct {
	starts        int
	reservationID string
}

func (p *pipelineAutomationLocalExecutor) Start(_ context.Context, _, _, _ string, _ bool, reservationID string) (string, error) {
	p.starts++
	p.reservationID = reservationID
	return "local-automation-execution", nil
}

func (*pipelineAutomationLocalExecutor) Status(context.Context, string, string, string) (*handler.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*pipelineAutomationLocalExecutor) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

var _ LocalPipelineExecutor = (*pipelineAutomationLocalExecutor)(nil)
