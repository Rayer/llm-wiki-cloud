package exportjob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	maxCreateBodyBytes = 1024
	signedURLLifetime  = 15 * time.Minute
)

var ErrArchiveMissing = errors.New("export archive not found")

type HTTPHandler struct {
	repo     Repository
	projects ProjectVerifier
	archives ArchiveStore
	starter  JobStarter
	now      func() time.Time
}

func NewHTTPHandler(repo Repository, projects ProjectVerifier, archives ArchiveStore, starter JobStarter) *HTTPHandler {
	return &HTTPHandler{repo: repo, projects: projects, archives: archives, starter: starter, now: time.Now}
}

func (h *HTTPHandler) Register(routes gin.IRoutes) {
	routes.POST("/exports", h.Create)
	routes.GET("/exports", h.List)
	routes.GET("/exports/:exportID/status", h.Status)
	routes.POST("/exports/:exportID/download", h.Download)
}

func (h *HTTPHandler) Create(c *gin.Context) {
	userID, projectID, _, ok := h.authorize(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateBodyBytes)
	var request CreateRequest
	if err := c.ShouldBindJSON(&request); err != nil || !request.Scope.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scope"})
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_idempotency_key"})
		return
	}
	if h.repo == nil || h.starter == nil || h.archives == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	job, replayed, err := h.repo.Admit(c.Request.Context(), userID, projectID, request.Scope, idempotencyKey, h.now().UTC())
	if err != nil {
		var denied *AdmissionError
		if errors.As(err, &denied) {
			c.JSON(http.StatusConflict, gin.H{"error": "export_rejected", "reason": denied.Reason, "existing_export_id": nullableString(denied.ExistingJobID), "next_allowed_at": denied.NextAllowedAt})
			return
		}
		if errors.Is(err, ErrIdempotencyConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "idempotency_conflict"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	if !replayed {
		if err := h.starter.Start(c.Request.Context(), userID, projectID, job); err != nil {
			_ = h.repo.Fail(context.WithoutCancel(c.Request.Context()), userID, projectID, job.ExportID, "job_start_failed", "Export could not be started. You can try again.")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
			return
		}
	}
	job = h.publicJob(job, userID, projectID, c.Request.Context())
	c.JSON(http.StatusAccepted, job)
}

func (h *HTTPHandler) List(c *gin.Context) {
	userID, projectID, _, ok := h.authorize(c)
	if !ok {
		return
	}
	if h.repo == nil || h.archives == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	snapshot, err := h.repo.Snapshot(c.Request.Context(), userID, projectID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	now := h.now().UTC()
	result := State{Eligible: true, NextAllowedAt: snapshot.NextAllowedAt}
	active := snapshot.Active
	if snapshot.LatestJob != nil {
		job := h.publicJob(*snapshot.LatestJob, userID, projectID, c.Request.Context())
		result.LatestJob = &job
		if active && job.Status != StatusQueued && job.Status != StatusRunning {
			active = false
		}
	}
	current := h.publicArchive(c.Request.Context(), userID, projectID, snapshot.Current, now)
	previous := h.publicArchive(c.Request.Context(), userID, projectID, snapshot.Previous, now)
	if current == nil && previous != nil {
		current, previous = previous, nil
	}
	result.Current, result.Previous = current, previous
	if active {
		result.Eligible = false
		reason := RejectInProgress
		result.RejectionReason = &reason
	} else if result.NextAllowedAt != nil && result.NextAllowedAt.After(now) {
		result.Eligible = false
		reason := RejectCooldown
		result.RejectionReason = &reason
	} else {
		result.NextAllowedAt = nil
	}
	c.JSON(http.StatusOK, result)
}

func (h *HTTPHandler) Status(c *gin.Context) {
	userID, projectID, _, ok := h.authorize(c)
	if !ok {
		return
	}
	if h.repo == nil || h.archives == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	job, err := h.repo.Get(c.Request.Context(), userID, projectID, c.Param("exportID"))
	if err != nil {
		h.jobReadError(c, err)
		return
	}
	job = h.publicJob(job, userID, projectID, c.Request.Context())
	c.JSON(http.StatusOK, job)
}

func (h *HTTPHandler) Download(c *gin.Context) {
	userID, projectID, projectName, ok := h.authorize(c)
	if !ok {
		return
	}
	job, err := h.repo.Get(c.Request.Context(), userID, projectID, c.Param("exportID"))
	if err != nil {
		h.jobReadError(c, err)
		return
	}
	if job.Status != StatusReady {
		if job.Status == StatusExpired {
			c.JSON(http.StatusGone, gin.H{"error": "export_expired"})
		} else {
			c.JSON(http.StatusConflict, gin.H{"error": "export_not_ready"})
		}
		return
	}
	if h.archives == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	info, err := h.archives.Stat(c.Request.Context(), userID, projectID, job.ExportID)
	if errors.Is(err, ErrArchiveMissing) || errors.Is(err, store.ErrObjectNotExist) {
		c.JSON(http.StatusGone, gin.H{"error": "export_expired"})
		return
	}
	if err != nil || info.CustomTime.IsZero() || job.SizeBytes == nil || *job.SizeBytes != info.Size {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	expiresAt := ExpiryFromCustomTime(info.CustomTime)
	now := h.now().UTC()
	if !expiresAt.After(now) {
		c.JSON(http.StatusGone, gin.H{"error": "export_expired"})
		return
	}
	urlExpiry := now.Add(signedURLLifetime)
	if expiresAt.Before(urlExpiry) {
		urlExpiry = expiresAt
	}
	filename := formatFilename(projectName, job.Scope, job.SnapshotAt, job.ExportID)
	signedURL, err := h.archives.Sign(c.Request.Context(), userID, projectID, job.ExportID, urlExpiry, filename)
	if err != nil || signedURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	c.JSON(http.StatusOK, Download{ExportID: job.ExportID, SignedURL: signedURL, ExpiresAt: urlExpiry, Filename: filename})
}

func (h *HTTPHandler) authorize(c *gin.Context) (string, string, string, bool) {
	userID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.GetString("projectID"))
	if userID == "" || projectID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return "", "", "", false
	}
	if h.projects == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return "", "", "", false
	}
	name, err := h.projects.VerifyProject(c.Request.Context(), userID, projectID)
	if errors.Is(err, ErrProjectMissing) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project_or_export_not_found"})
		return "", "", "", false
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return "", "", "", false
	}
	return userID, projectID, name, true
}

func (h *HTTPHandler) publicJob(job Job, userID, projectID string, ctx context.Context) Job {
	applyCompletedJob(&job)
	now := h.now().UTC()
	if (job.Status == StatusQueued || job.Status == StatusRunning) && !jobDeadline(job).After(now) {
		job.Status = StatusFailed
		code, message := "job_timeout", "Export job timed out. You can try again."
		job.ErrorCode, job.ErrorMessage = &code, &message
	}
	if job.Status != StatusReady {
		return job
	}
	if h.archives == nil {
		job.DownloadAvailable = false
		return job
	}
	info, err := h.archives.Stat(ctx, userID, projectID, job.ExportID)
	if err != nil || info.CustomTime.IsZero() || job.SizeBytes == nil || *job.SizeBytes != info.Size {
		job.DownloadAvailable = false
		return job
	}
	expires := ExpiryFromCustomTime(info.CustomTime)
	job.CompletedAt = timePtr(info.CustomTime.UTC())
	job.ExpiresAt = timePtr(expires)
	job.NextAllowedAt = timePtr(NextAllowedAt(info.CustomTime))
	if !expires.After(now) {
		job.Status = StatusExpired
		job.DownloadAvailable = false
		code, message := "export_expired", "Export expired. Create another export."
		job.ErrorCode, job.ErrorMessage = &code, &message
		return job
	}
	job.DownloadAvailable = true
	return job
}

func jobDeadline(job Job) time.Time {
	if !job.DeadlineAt.IsZero() {
		return job.DeadlineAt
	}
	return job.CreatedAt.Add(JobDeadline)
}

func (h *HTTPHandler) publicArchive(ctx context.Context, userID, projectID string, job *Job, now time.Time) *Archive {
	if job == nil || job.Status != StatusReady || h.archives == nil || job.SizeBytes == nil {
		return nil
	}
	info, err := h.archives.Stat(ctx, userID, projectID, job.ExportID)
	if err != nil || info.CustomTime.IsZero() || info.Size != *job.SizeBytes {
		return nil
	}
	expires := ExpiryFromCustomTime(info.CustomTime)
	if !expires.After(now) || job.SnapshotAt == nil {
		return nil
	}
	return &Archive{ExportID: job.ExportID, Scope: job.Scope, Status: StatusReady, SnapshotAt: job.SnapshotAt.UTC(), CompletedAt: info.CustomTime.UTC(), ExpiresAt: expires, SizeBytes: info.Size, DownloadAvailable: true}
}

func (h *HTTPHandler) jobReadError(c *gin.Context, err error) {
	if errors.Is(err, ErrJobMissing) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project_or_export_not_found"})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
}

func nullableString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func timePtr(value time.Time) *time.Time { value = value.UTC(); return &value }

func formatFilename(projectName string, scope Scope, snapshotAt *time.Time, exportID string) string {
	name := strings.Builder{}
	lastHyphen := false
	for _, r := range projectName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			name.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && name.Len() > 0 {
			name.WriteByte('-')
			lastHyphen = true
		}
	}
	base := strings.Trim(name.String(), "-")
	if base == "" {
		base = "project"
	}
	stamp := "unknown-time"
	if snapshotAt != nil && !snapshotAt.IsZero() {
		stamp = snapshotAt.UTC().Format("20060102T150405Z")
	}
	return fmt.Sprintf("%s_%s_%s_%s.zip", base, scope, stamp, exportID)
}
