package v1

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/auth"
)

func TestPipelineAccountAdmissionRejectsSuspendedAndUnavailableOwners(t *testing.T) {
	for _, stage := range []string{pipelineStageFull, pipelineStageSuggestedQueries} {
		for _, lookupErr := range []error{nil, errors.New("offline")} {
			h := &Handler{accountLookup: func(context.Context, string) (*auth.UserRecord, error) {
				return &auth.UserRecord{Status: auth.AccountSuspended}, lookupErr
			}, httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected admission reached Cloud Run")
				return nil, nil
			})}}
			want := auth.ErrAccountInactive
			if lookupErr != nil {
				want = auth.ErrAccountUnavailable
			}
			if _, err := h.invokePipelineJobStage(context.Background(), "owner", "project", false, stage); !errors.Is(err, want) {
				t.Fatalf("stage %s error=%v", stage, err)
			}
		}
	}
}

func TestPipelineAdmittedBeforeSuspensionCompletesDispatch(t *testing.T) {
	state := auth.AccountActive
	lookups := 0
	runs := 0
	h := &Handler{metadataTokenURL: "http://metadata.test/token", cloudRunJobURL: "https://run.test/run"}
	h.accountLookup = func(context.Context, string) (*auth.UserRecord, error) {
		lookups++
		return &auth.UserRecord{Status: state}, nil
	}
	h.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/token" {
			state = auth.AccountSuspended
			return testHTTPResponse(200, `{"access_token":"test"}`), nil
		}
		runs++
		return testHTTPResponse(200, `{"metadata":{"execution":"projects/p/locations/l/jobs/j/executions/admitted"}}`), nil
	})}
	if id, err := h.invokePipelineJobStage(context.Background(), "owner", "project", false, pipelineStageFull); err != nil || id != "admitted" {
		t.Fatalf("prior admission id=%s err=%v", id, err)
	}
	if lookups != 1 || runs != 1 {
		t.Fatalf("lookups=%d runs=%d", lookups, runs)
	}
	if _, err := h.invokePipelineJobStage(context.Background(), "owner", "project", false, pipelineStageFull); !errors.Is(err, auth.ErrAccountInactive) {
		t.Fatalf("new admission err=%v", err)
	}
	if runs != 1 {
		t.Fatal("new job dispatched after suspension")
	}
}

func TestSuspendedOwnerPipelineRequestsReturnForbidden(t *testing.T) {
	for _, admin := range []bool{false, true} {
		h := &Handler{metadataTokenURL: "http://metadata.test/token", cloudRunJobURL: "https://run.test/job:run",
			accountLookup: func(context.Context, string) (*auth.UserRecord, error) {
				return &auth.UserRecord{Status: auth.AccountSuspended}, nil
			},
			adminProjectRecordLoader: func(context.Context, string) (adminProjectRecord, error) {
				return adminProjectRecord{id: "owner_project", userID: "owner", projectID: "project"}, nil
			},
		}
		h.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost {
				t.Fatal("policy denial dispatched job")
			}
			if r.URL.Path == "/token" {
				return testHTTPResponse(200, `{"access_token":"test"}`), nil
			}
			return testHTTPResponse(200, `{"executions":[]}`), nil
		})}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/pipeline", nil)
		c.Set("userID", "owner")
		c.Set("projectID", "project")
		c.Params = gin.Params{{Key: "id", Value: "owner_project"}}
		if admin {
			h.AdminPipelineTrigger(c)
		} else {
			h.PipelineRun(c)
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("admin=%v status=%d body=%s", admin, recorder.Code, recorder.Body.String())
		}
	}
}
