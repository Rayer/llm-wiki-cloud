package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/exportjob"
)

type localExportAdmissionProbe struct{ calls int }

func (r *localExportAdmissionProbe) Admit(context.Context, string, string, exportjob.Scope, string, time.Time) (exportjob.Job, bool, error) {
	r.calls++
	return exportjob.Job{}, false, nil
}
func (*localExportAdmissionProbe) Get(context.Context, string, string, string) (exportjob.Job, error) {
	return exportjob.Job{}, nil
}
func (*localExportAdmissionProbe) Snapshot(context.Context, string, string) (exportjob.Snapshot, error) {
	return exportjob.Snapshot{}, nil
}
func (*localExportAdmissionProbe) MarkRunning(context.Context, string, string, string) error {
	return nil
}
func (*localExportAdmissionProbe) Fail(context.Context, string, string, string, string, string) error {
	return nil
}
func (*localExportAdmissionProbe) Complete(context.Context, string, string, string, time.Time, time.Time, int64) error {
	return nil
}

type localExportProjectProbe struct{ calls int }

func (p *localExportProjectProbe) VerifyProject(context.Context, string, string) (string, error) {
	p.calls++
	return "local test project", nil
}

func TestLocalExportRequestIsUnavailableBeforeAdmissionAndNeverCallsCloudRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cloudRunCalls := 0
	cloudRun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudRunCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer cloudRun.Close()
	repo := &localExportAdmissionProbe{}
	projects := &localExportProjectProbe{}
	handler := newExportHTTPHandler(true, config.Config{ExportJobURL: cloudRun.URL}, repo, projects, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", "local-user")
		c.Set("projectID", "local-project")
		c.Next()
	})
	handler.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/exports", strings.NewReader(`{"scope":"raw"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "export_unavailable") {
		t.Fatalf("local export status=%d body=%s; want unavailable", response.Code, response.Body.String())
	}
	if projects.calls != 1 || repo.calls != 0 || cloudRunCalls != 0 {
		t.Fatalf("project checks=%d admissions=%d Cloud Run calls=%d; want 1, 0, 0", projects.calls, repo.calls, cloudRunCalls)
	}
}
