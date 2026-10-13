package v1

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
)

type recompileAllQuotaSpy struct{ calls atomic.Int32 }

func (s *recompileAllQuotaSpy) LoadQuotaState(context.Context, string, string) (int, string, time.Time, error) {
	s.calls.Add(1)
	return 0, pipelinequota.DayKeyUTC(time.Now()), time.Time{}, nil
}

func (s *recompileAllQuotaSpy) ReserveQuota(_ context.Context, _ string, _ string, _ string, limits pipelinequota.Limits, now time.Time, mode pipelinequota.ExecutionMode, isDemo, alreadyRunning bool, newRaw, rawDirty, annotationDirty int) (pipelinequota.Snapshot, bool, error) {
	s.calls.Add(1)
	pre := pipelinequota.Evaluate(pipelinequota.Input{
		Now: now, Limits: limits, Mode: mode, IsDemo: isDemo, AlreadyRunning: alreadyRunning,
		NewRawFiles: newRaw, RawDirtyFiles: rawDirty, AnnotationDirtyFiles: annotationDirty, Enforced: true,
	})
	if !pre.Allowed {
		return pre, false, nil
	}
	return pipelinequota.Evaluate(pipelinequota.Input{
		Now: now, Limits: limits, Mode: mode, IsDemo: isDemo, AlreadyRunning: alreadyRunning,
		RunsToday: 1, DayKey: pipelinequota.DayKeyUTC(now), LastRunAt: now,
		NewRawFiles: newRaw, RawDirtyFiles: rawDirty, AnnotationDirtyFiles: annotationDirty, Enforced: true,
	}), true, nil
}

func (s *recompileAllQuotaSpy) LinkQuotaReservation(context.Context, string, string) error {
	s.calls.Add(1)
	return nil
}

func (s *recompileAllQuotaSpy) GetQuotaReservation(context.Context, string) (internalfirestore.QuotaReservation, bool, error) {
	s.calls.Add(1)
	return internalfirestore.QuotaReservation{}, false, nil
}

func (s *recompileAllQuotaSpy) ListPendingQuotaReservations(context.Context) ([]internalfirestore.QuotaReservation, error) {
	s.calls.Add(1)
	return nil, nil
}

func (s *recompileAllQuotaSpy) SettleQuotaReservation(context.Context, string, string) (string, error) {
	s.calls.Add(1)
	return "not_applicable", nil
}

func TestFullExecutionStateKeepsPublicationEvidenceSeparate(t *testing.T) {
	execution := cloudRunExecution{
		Name:             "projects/p/locations/r/jobs/j/executions/full-execution",
		CompletionStatus: "FAILED",
		Template:         cloudRunExecutionTemplate{Containers: []cloudRunContainer{{Env: []cloudRunEnv{{Name: "CLEAN_REBUILD", Value: "true"}}}}},
	}
	response := newPipelineExecutionResponse(execution)
	if response.Status != "FAILED" || response.CompileMode != string(pipelinequota.ModeFull) {
		t.Fatalf("execution state=%q compile mode=%q, want failed full execution", response.Status, response.CompileMode)
	}
	h := &Handler{}
	h.attachPipelineExecutionEvidence(t.Context(), response, &pipelineExecutionOwner{userID: "owner", projectID: "project"}, "full-execution")
	if response.Status != "FAILED" || response.PublicationOutcome != "unknown" || response.ConsumedGuidance == nil || response.ConsumedGuidance.Status != "unavailable" {
		t.Fatalf("full execution evidence overwrote cloud status or fabricated evidence: %+v", response)
	}
}

func TestRecompileAllUsesFullQuotaAndSharedCleanRebuildAdmission(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID := "lwc353-recompile-owner"
	projectID := "p" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	db, err := internalfirestore.NewClient("lwc353-recompile-test", userID, projectID)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer db.Close()
	userRef := db.Raw().Collection("users").Doc(userID)
	if _, err := userRef.Set(ctx, map[string]interface{}{"status": "active", "auth_version": 0}); err != nil {
		t.Fatalf("create active account fixture: %v", err)
	}
	projectRef := db.Raw().Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}

	gin.SetMode(gin.TestMode)
	h := New(nil, db, nil, nil, nil, nil)
	quota := &recompileAllQuotaSpy{}
	h.SetPipelineQuotaStore(quota)
	var jobCalls atomic.Int32
	var runBody string
	jobServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jobCalls.Add(1)
			data, _ := io.ReadAll(r.Body)
			runBody = string(data)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"metadata":{"execution":"projects/test/locations/test/jobs/job/executions/full-execution"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer jobServer.Close()
	h.SetPipelineJobURL(jobServer.URL)
	h.httpClient = jobServer.Client()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"local-test-token"}`))
	}))
	defer tokenServer.Close()
	h.metadataTokenURL = tokenServer.URL

	router := gin.New()
	router.GET("/projects/:pid/recompile-all/capability", func(c *gin.Context) {
		if principal := c.GetHeader("X-Test-Principal"); principal != "" {
			c.Set("userID", principal)
		}
		h.RecompileAllCapability(c)
	})
	router.POST("/projects/:pid/recompile-all", func(c *gin.Context) {
		if principal := c.GetHeader("X-Test-Principal"); principal != "" {
			c.Set("userID", principal)
		}
		h.RecompileAll(c)
	})
	request := func(method, path, principal, header string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		if principal != "" {
			req.Header.Set("X-Test-Principal", principal)
		}
		if header != "" {
			req.Header.Set("X-Project-ID", header)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}

	capability := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", userID, projectID)
	if capability.Code != http.StatusOK {
		t.Fatalf("capability status=%d body=%s, want 200", capability.Code, capability.Body.String())
	}
	var gotCapability recompileAllCapabilityResponse
	if err := json.Unmarshal(capability.Body.Bytes(), &gotCapability); err != nil {
		t.Fatalf("decode capability: %v", err)
	}
	if !gotCapability.Allowed || gotCapability.CompileMode != pipelinequota.ModeFull || gotCapability.Quota == nil || gotCapability.Quota.NewRawFiles != 0 {
		t.Fatalf("capability=%+v, want full-mode admission without pending raw files", gotCapability)
	}

	accepted := request(http.MethodPost, "/projects/"+projectID+"/recompile-all", userID, projectID)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("recompile status=%d body=%s, want 202", accepted.Code, accepted.Body.String())
	}
	var gotRun map[string]any
	if err := json.Unmarshal(accepted.Body.Bytes(), &gotRun); err != nil {
		t.Fatalf("decode accepted run: %v", err)
	}
	if gotRun["status"] != "accepted" || gotRun["compile_mode"] != "full" || gotRun["project_id"] != projectID || gotRun["execution_id"] != "full-execution" {
		t.Fatalf("accepted run=%+v, want scoped full execution identity", gotRun)
	}
	if calls := quota.calls.Load(); calls < 3 {
		t.Fatalf("full run did not evaluate and reserve quota, calls=%d", calls)
	}
	if calls := jobCalls.Load(); calls != 1 {
		t.Fatalf("full run started Cloud Run %d times, want 1", calls)
	}
	if !strings.Contains(runBody, `"name":"CLEAN_REBUILD","value":"true"`) || !strings.Contains(runBody, `"name":"PIPELINE_STAGE","value":"full"`) {
		t.Fatalf("full invocation overrides=%s, want clean rebuild and full stage", runBody)
	}

	stateRef := internalfirestore.Collection(db.Raw(), "projects").Doc(projectDocID(userID, projectID)).Collection("profile").Doc("state")
	writeReceipt := func(executionID, generationID string, kind string, revision *string) {
		t.Helper()
		receipt := profileruntime.CompileReceipt{
			UserID: userID, ProjectID: projectID, ExecutionID: executionID,
			ContentGeneration: generationID, ManifestGeneration: 1,
			ManifestSHA256: strings.Repeat("a", 64), CanonicalConceptsDigest: strings.Repeat("b", 64),
			ConsumedGuidanceKind: kind, ConsumedGuidanceRevision: revision,
		}
		if _, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(generationID).Set(ctx, receipt); err != nil {
			t.Fatalf("write consumed-guidance fixture: %v", err)
		}
	}
	activeRevision := strings.Repeat("c", 64)
	writeReceipt("full-execution", "g_abc123", "active", &activeRevision)
	activeEvidence := h.readConsumedGuidanceEvidence(ctx, &pipelineExecutionOwner{userID: userID, projectID: projectID}, "full-execution")
	if activeEvidence.Status != "available" || activeEvidence.Kind != "active" || activeEvidence.Revision == nil || *activeEvidence.Revision != activeRevision || activeEvidence.ContentGeneration != "g_abc123" {
		t.Fatalf("active guidance evidence=%+v", activeEvidence)
	}
	writeReceipt("none-execution", "g_abc124", "none", nil)
	noneEvidence := h.readConsumedGuidanceEvidence(ctx, &pipelineExecutionOwner{userID: userID, projectID: projectID}, "none-execution")
	if noneEvidence.Status != "available" || noneEvidence.Kind != "none" || noneEvidence.Revision != nil {
		t.Fatalf("explicit no-guidance evidence=%+v", noneEvidence)
	}
	writeReceipt("legacy-execution", "g_abc125", "", nil)
	legacyEvidence := h.readConsumedGuidanceEvidence(ctx, &pipelineExecutionOwner{userID: userID, projectID: projectID}, "legacy-execution")
	if legacyEvidence.Status != "unavailable" || legacyEvidence.Revision != nil {
		t.Fatalf("legacy receipt must remain unavailable, got %+v", legacyEvidence)
	}

	unauthorized := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", "", projectID)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal status=%d body=%s, want 401", unauthorized.Code, unauthorized.Body.String())
	}

	mismatch := request(http.MethodPost, "/projects/"+projectID+"/recompile-all", userID, "other-project")
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("project header mismatch status=%d body=%s, want 400", mismatch.Code, mismatch.Body.String())
	}

	foreign := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", "lwc353-attacker", projectID)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign Project capability status=%d body=%s, want 404", foreign.Code, foreign.Body.String())
	}
}
