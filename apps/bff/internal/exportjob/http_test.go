package exportjob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type testRepository struct {
	jobs       map[string]Job
	state      Snapshot
	keys       map[string]string
	admitCalls int
}

func newTestRepository() *testRepository {
	return &testRepository{jobs: map[string]Job{}, keys: map[string]string{}}
}

func (r *testRepository) Admit(_ context.Context, _, _ string, scope Scope, key string, now time.Time) (Job, bool, error) {
	r.admitCalls++
	if id := r.keys[key]; key != "" && id != "" {
		job := r.jobs[id]
		if job.Scope != scope {
			return Job{}, false, ErrIdempotencyConflict
		}
		return job, true, nil
	}
	if e := AdmissionAllowed(r.state.LatestJob, r.state.NextAllowedAt, now); e != nil {
		return Job{}, false, e
	}
	job := Job{ExportID: "export-1", Scope: scope, Status: StatusQueued, CreatedAt: now, DeadlineAt: now.Add(JobDeadline)}
	r.jobs[job.ExportID] = job
	r.keys[key] = job.ExportID
	r.state.LatestJob = &job
	r.state.Active = true
	return job, false, nil
}

func (r *testRepository) Get(_ context.Context, _, _, id string) (Job, error) {
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, ErrJobMissing
	}
	return job, nil
}
func (r *testRepository) Snapshot(context.Context, string, string) (Snapshot, error) {
	return r.state, nil
}
func (r *testRepository) MarkRunning(context.Context, string, string, string) error { return nil }
func (r *testRepository) Fail(_ context.Context, _, _, _, _, _ string) (FailResult, error) {
	return FailResult{Changed: true, Status: StatusFailed}, nil
}
func (r *testRepository) Complete(context.Context, string, string, string, time.Time, time.Time, int64) error {
	return nil
}

type testProjectVerifier struct {
	name string
	err  error
}

func (v testProjectVerifier) VerifyProject(context.Context, string, string) (string, error) {
	return v.name, v.err
}

type testArchiveStore struct {
	info       map[string]ArchiveInfo
	err        error
	signedTill time.Time
	filename   string
	signCalls  int
}

func (s *testArchiveStore) Stat(_ context.Context, _, _, id string) (ArchiveInfo, error) {
	if s.err != nil {
		return ArchiveInfo{}, s.err
	}
	info, ok := s.info[id]
	if !ok {
		return ArchiveInfo{}, ErrArchiveMissing
	}
	return info, nil
}
func (s *testArchiveStore) Sign(_ context.Context, _, _, _ string, expires time.Time, filename string) (string, error) {
	s.signCalls++
	s.signedTill, s.filename = expires, filename
	return "https://signed.example/archive", nil
}

type testStarter struct {
	calls int
	err   error
}

func (s *testStarter) Start(context.Context, string, string, Job) error {
	s.calls++
	return s.err
}

func newHTTPTestRouter(handler *HTTPHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("projectID", "project-1")
		c.Next()
	})
	handler.Register(api)
	return r
}

func TestCreateUsesOwnerVerifierAndStableJobContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	repo := newTestRepository()
	starter := &testStarter{}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "My Project"}, &testArchiveStore{}, starter)
	h.now = func() time.Time { return now }
	r := newHTTPTestRouter(h)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"scope":"raw-full"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "retry-1")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || starter.calls != 1 {
		t.Fatalf("create = %d %s, start calls=%d", recorder.Code, recorder.Body.String(), starter.calls)
	}
	var job Job
	if err := json.Unmarshal(recorder.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued || job.Scope != ScopeRawFull || job.SnapshotAt != nil || job.ExpiresAt != nil || job.SizeBytes != nil || job.ErrorCode != nil || job.ErrorMessage != nil {
		t.Fatalf("create job = %#v", job)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"scope":"raw-full"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "retry-1")
	recorder = httptest.NewRecorder()
	r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || starter.calls != 1 {
		t.Fatalf("idempotent create = %d %s, start calls=%d", recorder.Code, recorder.Body.String(), starter.calls)
	}
}

func TestCreateRejectsHeaderOnlyProjectAndDoesNotTouchRepository(t *testing.T) {
	repo := newTestRepository()
	h := NewHTTPHandler(repo, testProjectVerifier{err: ErrProjectMissing}, &testArchiveStore{}, &testStarter{})
	r := newHTTPTestRouter(h)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"scope":"raw"}`)))
	if recorder.Code != http.StatusNotFound || repo.admitCalls != 0 {
		t.Fatalf("unauthorized project create = %d %s, admission calls=%d", recorder.Code, recorder.Body.String(), repo.admitCalls)
	}
}

func TestDownloadCapsSignedURLAtCustomTimeExpiry(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 55, 0, 0, time.UTC)
	completedAt := now.Add(-time.Hour)
	snapshotAt := now.Add(-2 * time.Hour)
	size := int64(400)
	repo := newTestRepository()
	repo.jobs["export-a"] = Job{ExportID: "export-a", Scope: ScopeRawFull, Status: StatusReady, CreatedAt: snapshotAt, SnapshotAt: &snapshotAt, CompletedAt: &completedAt, SizeBytes: &size}
	customTime := now.Add(-Retention + 5*time.Minute)
	archives := &testArchiveStore{info: map[string]ArchiveInfo{"export-a": {Size: size, CustomTime: customTime}}}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "My / Project"}, archives, &testStarter{})
	h.now = func() time.Time { return now }
	r := newHTTPTestRouter(h)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/exports/export-a/download", nil))
	if recorder.Code != http.StatusOK || archives.signCalls != 1 {
		t.Fatalf("download = %d %s, sign calls=%d", recorder.Code, recorder.Body.String(), archives.signCalls)
	}
	if want := customTime.Add(Retention); !archives.signedTill.Equal(want) {
		t.Fatalf("signed URL expiration = %s, want archive expiry %s", archives.signedTill, want)
	}
	if strings.ContainsAny(archives.filename, "/\\") {
		t.Fatalf("filename is not safe: %q", archives.filename)
	}
}

func TestDownloadReturnsSignedURLWithoutExportingItToSpans(t *testing.T) {
	const signedURL = "https://signed.example/archive"
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	now := time.Date(2026, 9, 26, 23, 55, 0, 0, time.UTC)
	snapshotAt := now.Add(-time.Hour)
	completedAt := now.Add(-30 * time.Minute)
	size := int64(12)
	repo := newTestRepository()
	repo.jobs["private-export-id"] = Job{ExportID: "private-export-id", Scope: ScopeRaw, Status: StatusReady, CreatedAt: snapshotAt, SnapshotAt: &snapshotAt, CompletedAt: &completedAt, SizeBytes: &size}
	archives := &testArchiveStore{info: map[string]ArchiveInfo{"private-export-id": {Size: size, CustomTime: completedAt}}}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "Project"}, archives, &testStarter{})
	h.now = func() time.Time { return now }
	r := newHTTPTestRouter(h)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/exports/private-export-id/download", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), signedURL) {
		t.Fatalf("download response = %d %s", response.Code, response.Body.String())
	}
	seenRoot, seenURLPhase := false, false
	for _, span := range exporter.GetSpans() {
		if span.Name == "export.request" {
			seenRoot = true
		}
		if span.Name == "export.download_url" {
			seenURLPhase = true
		}
		if strings.Contains(span.Name, signedURL) {
			t.Fatal("signed URL used as span name")
		}
		for _, attr := range span.Attributes {
			if attr.Value.Type() == attribute.STRING && strings.Contains(attr.Value.AsString(), signedURL) {
				t.Fatal("signed URL reached span attributes")
			}
		}
	}
	if !seenRoot || !seenURLPhase {
		t.Fatalf("missing request/download URL spans: root=%t url=%t", seenRoot, seenURLPhase)
	}
}

func TestExportHTTPPhasesRemainChildrenOfRequestAcrossEntrypoints(t *testing.T) {
	exporter := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(-time.Minute)
	snapshotAt := now.Add(-time.Hour)
	size := int64(16)
	repo := newTestRepository()
	repo.jobs["ready-a"] = Job{ExportID: "ready-a", Scope: ScopeRaw, Status: StatusReady, CreatedAt: snapshotAt, SnapshotAt: &snapshotAt, CompletedAt: &completedAt, SizeBytes: &size}
	archives := &testArchiveStore{info: map[string]ArchiveInfo{"ready-a": {Size: size, CustomTime: completedAt}}}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "Project"}, archives, &testStarter{})
	h.now = func() time.Time { return now }
	router := newHTTPTestRouter(h)
	cases := []struct {
		name, method, path, body string
		wantStatus               int
		phases                   []string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/exports", body: `{"scope":"raw"}`, wantStatus: http.StatusAccepted, phases: []string{"export.admission", "export.start"}},
		{name: "list", method: http.MethodGet, path: "/api/v1/exports", wantStatus: http.StatusOK, phases: []string{"export.status"}},
		{name: "status", method: http.MethodGet, path: "/api/v1/exports/ready-a/status", wantStatus: http.StatusOK, phases: []string{"export.status"}},
		{name: "download", method: http.MethodPost, path: "/api/v1/exports/ready-a/download", wantStatus: http.StatusOK, phases: []string{"export.status", "export.download_url"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := len(exporter.Ended())
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			spans := exporter.Ended()[before:]
			byName := map[string]sdktrace.ReadOnlySpan{}
			for _, span := range spans {
				byName[span.Name()] = span
			}
			root, authorize := byName["export.request"], byName["export.authorize"]
			if root == nil || authorize == nil || authorize.Parent().SpanID() != root.SpanContext().SpanID() {
				t.Fatalf("request/authorize span parent missing: %+v", byName)
			}
			if authorize.EndTime().After(root.EndTime()) {
				t.Fatalf("authorize ended after request: authorize=%s request=%s", authorize.EndTime(), root.EndTime())
			}
			for _, name := range test.phases {
				phase := byName[name]
				if phase == nil || phase.Parent().SpanID() != root.SpanContext().SpanID() {
					t.Fatalf("phase %s is not a request child: %+v", name, phase)
				}
				if phase.StartTime().Before(authorize.EndTime()) {
					t.Fatalf("phase %s started before authorize ended", name)
				}
			}
		})
	}
}

func TestDownloadDoesNotSignExpiredOrMissingArchive(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	snapshotAt := now.Add(-4 * 24 * time.Hour)
	completedAt := snapshotAt
	size := int64(3)
	repo := newTestRepository()
	repo.jobs["expired"] = Job{ExportID: "expired", Scope: ScopeRaw, Status: StatusReady, CreatedAt: snapshotAt, SnapshotAt: &snapshotAt, CompletedAt: &completedAt, SizeBytes: &size}
	archives := &testArchiveStore{info: map[string]ArchiveInfo{"expired": {Size: size, CustomTime: snapshotAt}}}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "Project"}, archives, &testStarter{})
	h.now = func() time.Time { return now }
	r := newHTTPTestRouter(h)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/exports/expired/download", nil))
	if recorder.Code != http.StatusGone || archives.signCalls != 0 {
		t.Fatalf("expired download = %d %s, sign calls=%d", recorder.Code, recorder.Body.String(), archives.signCalls)
	}
}

func TestListClearsTimedOutActiveJobForEligibility(t *testing.T) {
	now := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	created := now.Add(-JobDeadline - time.Minute)
	repo := newTestRepository()
	job := Job{ExportID: "timed-out", Scope: ScopeRaw, Status: StatusRunning, CreatedAt: created, DeadlineAt: created.Add(JobDeadline)}
	repo.state = Snapshot{LatestJob: &job, Active: true}
	h := NewHTTPHandler(repo, testProjectVerifier{name: "Project"}, &testArchiveStore{}, &testStarter{})
	h.now = func() time.Time { return now }
	recorder := httptest.NewRecorder()
	newHTTPTestRouter(h).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/exports", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list = %d %s", recorder.Code, recorder.Body.String())
	}
	var state State
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Eligible || state.RejectionReason != nil {
		t.Fatalf("list kept a timed-out job in progress: %#v", state)
	}
	// Timeouts are rendered as failed; that terminal public state releases the
	// admission lock without turning a side-effect-free GET into a write.
	if state.LatestJob == nil || state.LatestJob.Status != StatusFailed {
		t.Fatalf("latest job = %#v, want failed timeout", state.LatestJob)
	}
}
