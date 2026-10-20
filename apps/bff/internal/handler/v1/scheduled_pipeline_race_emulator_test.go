package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"google.golang.org/api/idtoken"
)

func TestConcurrentScheduledManualAndFullEntrypointsHaveOneProviderInvoke(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project   = "llm-wiki-cloud"
		database  = "(default)"
		userID    = "lwc413-race-owner"
		projectID = "same-project"
	)
	scope := fmt.Sprintf("lwc413-race-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean scheduled race emulator scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	_, err = scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "status": "ready",
	})
	if err != nil {
		t.Fatalf("seed ready Project: %v", err)
	}
	localStore := localfs.New(t.TempDir())
	if _, err := localStore.WithScope(userID, projectID).WriteBytes(ctx, []byte("race input"), "raw/source.md"); err != nil {
		t.Fatalf("seed pending raw input: %v", err)
	}
	fixture := newQuotaCloudRunFixture()
	fixture.projectsWithInvokeError = map[string]bool{}
	h := New(localStore, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	h.SetPipelineJobURL("http://run.test/job:run")
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com")
	h.SetScheduledPipelineIdentityValidator(func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "fixture-token" || audience != "https://bff.example" {
			return nil, fmt.Errorf("scheduled identity fixture mismatch")
		}
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Claims: map[string]interface{}{
			"email": "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com", "email_verified": true,
		}}, nil
	})
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	h.projectExists = func(context.Context, string) error { return nil }
	h.adminProjectRecordLoader = func(_ context.Context, docID string) (adminProjectRecord, error) {
		if docID != userID+"_"+projectID {
			return adminProjectRecord{}, fmt.Errorf("unexpected project document")
		}
		return adminProjectRecord{id: docID, userID: userID, projectID: projectID}, nil
	}
	gin.SetMode(gin.TestMode)
	start := make(chan struct{})
	type response struct {
		name   string
		status int
		body   string
	}
	responses := make(chan response, 3)
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		<-start
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/schedule", nil).WithContext(ctx)
		c.Request.Header.Set("Authorization", "Bearer fixture-token")
		h.ScheduledPipeline(c)
		responses <- response{name: "scheduled", status: recorder.Code, body: recorder.Body.String()}
	}()
	go func() {
		defer wait.Done()
		<-start
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/run", nil).WithContext(ctx)
		c.Set("userID", userID)
		c.Set("projectID", projectID)
		h.PipelineRun(c)
		responses <- response{name: "manual", status: recorder.Code, body: recorder.Body.String()}
	}()
	go func() {
		defer wait.Done()
		<-start
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/"+userID+"_"+projectID+"/pipeline", nil).WithContext(ctx)
		c.Params = gin.Params{{Key: "id", Value: userID + "_" + projectID}}
		h.AdminPipelineTrigger(c)
		responses <- response{name: "full", status: recorder.Code, body: recorder.Body.String()}
	}()
	close(start)
	wait.Wait()
	results := make(map[string]response, 3)
	for range 3 {
		result := <-responses
		results[result.name] = result
	}
	if results["scheduled"].status != http.StatusOK {
		t.Fatalf("scheduled entrypoint status=%d body=%s", results["scheduled"].status, results["scheduled"].body)
	}
	var sweep ScheduledPipelineSweepResult
	if err := json.Unmarshal([]byte(results["scheduled"].body), &sweep); err != nil {
		t.Fatalf("decode scheduled result %q: %v", results["scheduled"].body, err)
	}
	if results["manual"].status != http.StatusAccepted && results["manual"].status != http.StatusConflict {
		t.Fatalf("manual entrypoint status=%d body=%s", results["manual"].status, results["manual"].body)
	}
	if results["full"].status != http.StatusAccepted && results["full"].status != http.StatusConflict {
		t.Fatalf("full entrypoint status=%d body=%s", results["full"].status, results["full"].body)
	}
	winners := 0
	if sweep.Accepted == 1 {
		winners++
	} else if sweep.Accepted != 0 {
		t.Fatalf("scheduled accepted count=%d, want 0 or 1", sweep.Accepted)
	}
	for _, name := range []string{"manual", "full"} {
		if results[name].status == http.StatusAccepted {
			winners++
		}
	}
	fixture.mu.Lock()
	invokeCount, requestCount := fixture.next, len(fixture.requests)
	fixture.mu.Unlock()
	if winners != 1 || invokeCount != 1 || requestCount != 1 {
		t.Fatalf("scheduled/manual/full race winners=%d provider invocations=%d requests=%d responses=%+v scheduled=%+v; want exactly one actual provider invoke", winners, invokeCount, requestCount, results, sweep)
	}
	if active, err := fsClient.PipelineAdmissionActive(ctx, userID, projectID); err != nil || !active {
		t.Fatalf("shared admission active=%t err=%v after accepted contender", active, err)
	}
	reservations, err := fsClient.ListPendingQuotaReservations(ctx)
	if err != nil || len(reservations) != 1 || reservations[0].ExecutionID == "" {
		t.Fatalf("winning reservation=%+v err=%v; want one accepted and linked execution", reservations, err)
	}
}

func TestProfileOnlyConfirmedRevisionDoesNotTriggerScheduledInvocation(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	const (
		project   = "llm-wiki-cloud"
		database  = "(default)"
		userID    = "lwc413-profile-owner"
		projectID = "profile-only"
	)
	scope := fmt.Sprintf("lwc413-profile-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := deletePipelineQuotaFirestoreScope(cleanupCtx, fsClient.Raw(), scope); err != nil {
			t.Errorf("clean profile-only emulator scope: %v", err)
		}
		if err := fsClient.Close(); err != nil {
			t.Errorf("close loopback Firestore client: %v", err)
		}
	})
	projectIDInFirestore := userID + "_" + projectID
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(projectIDInFirestore)
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "status": "ready",
	}); err != nil {
		t.Fatalf("seed ready Project: %v", err)
	}
	profileRef := projectRef.Collection("profile").Doc("state")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := profileRef.Delete(cleanupCtx); err != nil {
			t.Errorf("delete nested profile fixture: %v", err)
		}
	})
	if _, err := profileRef.Set(ctx, map[string]interface{}{
		"project_id": projectID, "revision": int64(1),
		"requirements":           []map[string]interface{}{{"id": "r1", "text": "Existing guidance"}},
		"confirmed_candidate_id": "candidate-v1",
	}); err != nil {
		t.Fatalf("seed confirmed Profile: %v", err)
	}
	localStore := localfs.New(t.TempDir())
	fixture := newQuotaCloudRunFixture()
	fixture.projectsWithInvokeError = map[string]bool{}
	h := New(localStore, fsClient, search.NewIndex(), conceptcache.New(), nil, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	h.SetPipelineQuotaConfig(5, int(time.Hour.Seconds()), 1, nil)
	h.SetPipelineJobURL("http://run.test/job:run")
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "lwc413-scheduler@llm-wiki-cloud.iam.gserviceaccount.com")
	h.metadataTokenURL = "http://run.test/token"
	h.httpClient = &http.Client{Transport: roundTripFunc(fixture.roundTrip)}
	first, err := h.RunScheduledPipelineSweep(ctx, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	if err != nil || first.Scanned != 1 || first.Accepted != 0 || first.Skipped != 1 {
		t.Fatalf("first no-raw sweep=%+v err=%v; want no-work skip", first, err)
	}
	if _, err := profileRef.Set(ctx, map[string]interface{}{
		"project_id": projectID, "revision": int64(2),
		"requirements":           []map[string]interface{}{{"id": "r1", "text": "Edited guidance"}},
		"confirmed_candidate_id": "candidate-v2",
	}); err != nil {
		t.Fatalf("edit and confirm Profile without changing sources: %v", err)
	}
	second, err := h.RunScheduledPipelineSweep(ctx, time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC))
	fixture.mu.Lock()
	invokeCount := fixture.next
	fixture.mu.Unlock()
	status, statusErr := fsClient.LoadScheduledPipelineStatus(ctx, userID, projectID, time.Now().UTC())
	profileSnapshot, profileErr := profileRef.Get(ctx)
	if err != nil || second.Scanned != 1 || second.Accepted != 0 || second.Skipped != 1 || invokeCount != 0 || statusErr != nil || status.SkipReason != "no_work" || profileErr != nil || profileSnapshot.Data()["revision"] != int64(2) {
		t.Fatalf("Profile-only sweep=%+v invokes=%d status=%+v statusErr=%v profile=%v profileErr=%v err=%v; want revision 2, no-work, and zero invocation", second, invokeCount, status, statusErr, profileSnapshot.Data(), profileErr, err)
	}
}
