package v1

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
)

func TestDecodePipelineAutomationJSONIsStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "valid", body: `{"enabled":true,"revision":4}`},
		{name: "duplicate", body: `{"enabled":true,"enabled":false,"revision":4}`, want: errors.New("duplicate")},
		{name: "unknown", body: `{"enabled":true,"revision":4,"extra":1}`, want: errors.New("unknown")},
		{name: "trailing value", body: `{"enabled":true,"revision":4} {}`, want: errors.New("multiple")},
		{name: "float revision", body: `{"enabled":true,"revision":4.0}`, want: errors.New("json")},
		{name: "wrong boolean type", body: `{"enabled":"true","revision":4}`, want: errors.New("json")},
		{name: "oversized", body: `{"enabled":true,"revision":4,"padding":"` + strings.Repeat("x", maxPipelineAutomationBody) + `"}`, want: errPipelineAutomationBodySize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("PUT", "/automation", strings.NewReader(test.body))
			var request projectPipelineAutomationRequest
			err := decodePipelineAutomationJSON(c, &request)
			if test.want == nil {
				if err != nil || request.Enabled == nil || !*request.Enabled || request.Revision == nil || *request.Revision != 4 {
					t.Fatalf("decoded request=%+v err=%v", request, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected request rejection")
			}
			if errors.Is(test.want, errPipelineAutomationBodySize) {
				if !errors.Is(err, errPipelineAutomationBodySize) {
					t.Fatalf("error=%v, want body-size error", err)
				}
				return
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want.Error())) {
				t.Fatalf("error=%v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestProjectAutomationMissingFieldsAreDisabledAndFailClosed(t *testing.T) {
	setting := projectPipelineAutomationFromData(map[string]interface{}{}, pipelineAutomationSetting{backlogStatus: "idle"})
	if setting.Enabled || setting.Revision != 0 || setting.EffectiveEnabled || setting.BlockedReason != "admin_disabled" {
		t.Fatalf("missing settings were not disabled: %+v", setting)
	}
	setting = projectPipelineAutomationFromData(map[string]interface{}{"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(2)}, pipelineAutomationSetting{allow: true})
	if !setting.Enabled || !setting.EffectiveEnabled || setting.Revision != 2 || setting.BlockedReason != "" {
		t.Fatalf("enabled settings were not projected: %+v", setting)
	}
}

func TestPipelineAutomationReservationIDChangesWhenEitherSwitchReopens(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	initial := pipelineAutomationReservationID(fingerprint, 1, 1)
	if initial == pipelineAutomationReservationID(fingerprint, 2, 1) {
		t.Fatal("Project reopen reused a previous reservation ID")
	}
	if initial == pipelineAutomationReservationID(fingerprint, 1, 3) {
		t.Fatal("Admin reopen reused a previous reservation ID")
	}
	if initial != pipelineAutomationReservationID(fingerprint, 1, 1) {
		t.Fatal("same source and switch revisions did not preserve reservation identity")
	}
}

func TestPipelineAutomationAdmissionRejectsStaleSourceAndAllowsReopen(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	fs := client.Raw()
	owner, projectID := "automation-admission-owner", "wiki"
	projectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocID(owner, projectID))
	adminRef := pipelineAutomationAdminRef(fs)
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": owner, "project_id": projectID, "status": "ready",
		"auto_pipeline_enabled": true, "auto_pipeline_revision": int64(1), "pipeline_source_revision": int64(1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRef.Set(ctx, map[string]interface{}{"allow_auto_pipeline": true, "revision": int64(1)}); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("b", 64)
	limits := pipelinequota.Limits{DailyLimit: 2, Cooldown: time.Hour, MinNewRaw: 1}
	reserve := func(id string, admission scopedfirestore.PipelineAutomationAdmission) (bool, error) {
		_, ok, err := client.ReserveQuotaForPipelineAutomation(ctx, id, owner, projectID, limits, time.Now().UTC(), false, false, 1, 0, 0, admission)
		return ok, err
	}
	staleID := pipelineAutomationReservationID(fingerprint, 1, 1)
	reserved, err := reserve(staleID, scopedfirestore.PipelineAutomationAdmission{
		SourceFingerprint: fingerprint, SourceRevision: 0, ProjectRevision: 1, AdminRevision: 1,
	})
	if err != nil || reserved {
		t.Fatalf("stale source admission reserved=%t err=%v, want no reservation", reserved, err)
	}
	if _, exists, err := scopedfirestore.GetQuotaReservationFromFirestore(ctx, fs, staleID); err != nil || exists {
		t.Fatalf("stale source created a reservation: exists=%t err=%v", exists, err)
	}

	firstID := pipelineAutomationReservationID(fingerprint, 1, 1)
	reserved, err = reserve(firstID, scopedfirestore.PipelineAutomationAdmission{
		SourceFingerprint: fingerprint, SourceRevision: 1, ProjectRevision: 1, AdminRevision: 1,
	})
	if err != nil || !reserved {
		t.Fatalf("current source admission reserved=%t err=%v", reserved, err)
	}
	if _, err := client.SettleQuotaReservation(ctx, firstID, "FAILED"); err != nil {
		t.Fatalf("settle first reservation: %v", err)
	}
	if _, err := projectRef.Update(ctx, []cloudfirestore.Update{
		{Path: "auto_pipeline_revision", Value: int64(2)},
		{Path: "pipeline_failure_blocked_fingerprint", Value: cloudfirestore.Delete},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRef.Update(ctx, []cloudfirestore.Update{{Path: "revision", Value: int64(3)}}); err != nil {
		t.Fatal(err)
	}
	reopenedID := pipelineAutomationReservationID(fingerprint, 2, 3)
	reserved, err = reserve(reopenedID, scopedfirestore.PipelineAutomationAdmission{
		SourceFingerprint: fingerprint, SourceRevision: 1, ProjectRevision: 2, AdminRevision: 3,
	})
	if err != nil || !reserved {
		t.Fatalf("reopened switches reused a prior attempt: reserved=%t err=%v", reserved, err)
	}
}

func TestPipelineAutomationSwitchesUseCASAndQueueBacklogWork(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	fs := client.Raw()
	owner, projectID := "automation-owner", "wiki"
	projectRef := scopedfirestore.Collection(fs, "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": owner, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatal(err)
	}

	project, err := readPipelineAutomationProject(ctx, fs, owner, projectID)
	if err != nil || project.Enabled || project.Revision != 0 || project.EffectiveEnabled {
		t.Fatalf("legacy project defaults=%+v err=%v", project, err)
	}
	project, err = updatePipelineAutomationProject(ctx, fs, owner, projectID, true, 0)
	if err != nil || !project.Enabled || project.Revision != 1 || project.AdminAllowed || project.EffectiveEnabled {
		t.Fatalf("project enable before admin=%+v err=%v", project, err)
	}

	admin, err := updatePipelineAutomationAdmin(ctx, fs, true, 0)
	if err != nil || !admin.allow || admin.revision != 1 || admin.backlogStatus != "pending" {
		t.Fatalf("admin enable=%+v err=%v", admin, err)
	}
	project, err = readPipelineAutomationProject(ctx, fs, owner, projectID)
	if err != nil || !project.EffectiveEnabled || project.BlockedReason != "" {
		t.Fatalf("effective project settings=%+v err=%v", project, err)
	}

	if _, err := updatePipelineAutomationProject(ctx, fs, owner, projectID, false, 0); !errors.Is(err, errPipelineAutomationRevision) {
		t.Fatalf("stale project revision error=%v, want conflict", err)
	}
	if _, err := updatePipelineAutomationAdmin(ctx, fs, false, 0); !errors.Is(err, errPipelineAutomationRevision) {
		t.Fatalf("stale admin revision error=%v, want conflict", err)
	}

	work, err := scopedfirestore.Collection(fs, "profile_runtime_work").Documents(ctx).GetAll()
	if err != nil || len(work) != 2 {
		t.Fatalf("queued backlog work count=%d err=%v, want project+admin work", len(work), err)
	}
	seen := map[string]bool{}
	for _, doc := range work {
		kind, _ := doc.Data()["kind"].(string)
		seen[kind] = true
	}
	if !seen["pipeline_automation"] || !seen["pipeline_automation_scan"] {
		t.Fatalf("queued work kinds=%v", seen)
	}
}

func TestProjectToggleAutomationWorkCompletesBacklogStatus(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	owner, projectID := "automation-backlog-owner", "wiki"
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(owner, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": owner, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineAutomationAdminRef(client.Raw()).Set(ctx, map[string]interface{}{
		"allow_auto_pipeline": true, "revision": int64(1),
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := updatePipelineAutomationProject(ctx, client.Raw(), owner, projectID, true, 0)
	if err != nil || settings.BacklogScanStatus != "pending" {
		t.Fatalf("enabled project backlog=%+v err=%v", settings, err)
	}

	h := New(localfs.New(t.TempDir()), client, nil, nil, nil, nil)
	repo := h.profileRepository.(*firestoreProfileRepository)
	work := profileruntime.Work{UserID: owner, ProjectID: projectID, Kind: "pipeline_automation", Revision: 1, ID: "project-toggle-1"}
	if err := (&ProfileDispatcher{Handler: h}).execute(ctx, repo, work); err != nil {
		t.Fatalf("consume project backlog work: %v", err)
	}
	project, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if project.Data()["pipeline_backlog_scan_status"] != "complete" {
		t.Fatalf("Project backlog status=%v, want complete", project.Data()["pipeline_backlog_scan_status"])
	}
}
