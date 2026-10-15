package v1

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const rawSyncPageSize = 100

type syncBindingChecker interface {
	CheckSyncBinding(context.Context, string, string, string, string, string) error
}

type rawSyncListPage struct {
	Files         []store.RawSyncFile `json:"files"`
	TotalFiles    int                 `json:"total_files"`
	Snapshot      string              `json:"snapshot"`
	NextPageToken string              `json:"next_page_token,omitempty"`
}

func (h *Handler) SetSyncBindingAuthority(authority *auth.SyncBindingAuthority) {
	h.syncBindingAuthority = authority
}

// RawSyncBindingAuth rechecks the current binding for every raw sync request.
func (h *Handler) RawSyncBindingAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		phaseCtx, authorizeSpan := observability.StartDataPhase(c.Request.Context(), "authorize")
		c.Request = c.Request.WithContext(phaseCtx)
		if h.syncBindingAuthority == nil {
			observability.FinishDataSpan(authorizeSpan, "failure")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "sync binding authority unavailable"})
			return
		}
		err := h.syncBindingAuthority.CheckSyncBinding(
			phaseCtx, c.GetString("userID"), c.GetString("projectID"),
			c.GetHeader("X-Sync-Binding-ID"), c.GetHeader("X-Wiki-ID"), h.syncBindingHost,
		)
		if errors.Is(err, auth.ErrSyncBindingUnauthorized) {
			observability.FinishDataSpan(authorizeSpan, "rejected")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "sync binding is not active for this Project"})
			return
		}
		if err != nil {
			observability.FinishDataSpan(authorizeSpan, observability.DataErrorOutcome(err))
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "sync binding authority unavailable"})
			return
		}
		observability.FinishDataSpan(authorizeSpan, "success")
		c.Next()
	}
}

func (h *Handler) SetSyncBindingHost(host string) {
	h.syncBindingHost = strings.TrimSpace(host)
}

func (h *Handler) SyncRawList(c *gin.Context) {
	rawStore, ok := h.rawSyncStore(c)
	if !ok {
		return
	}
	readCtx, readSpan := observability.StartDataPhase(c.Request.Context(), "read")
	files, err := rawStore.ListSyncRawFiles(readCtx)
	if err != nil {
		outcome := observability.DataErrorOutcome(err)
		observability.FinishDataSpan(readSpan, outcome)
		observability.SetDataOperationOutcome(c.Request.Context(), outcome)
		observability.WriteDataJSON(c, http.StatusInternalServerError, gin.H{"error": "could not list raw files"})
		return
	}
	observability.SetDataCount(readSpan, "items", int64(len(files)))
	observability.FinishDataSpan(readSpan, "success")
	snapshot := rawSyncSnapshot(files)
	offset := 0
	_, inputSpan := observability.StartDataPhase(c.Request.Context(), "input")
	if token := c.Query("page_token"); token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			observability.FinishDataSpan(inputSpan, "rejected")
			observability.WriteDataJSON(c, http.StatusBadRequest, gin.H{"error": "invalid page token"})
			return
		}
		parts := strings.Split(string(decoded), ":")
		if len(parts) != 2 || parts[0] != snapshot {
			observability.FinishDataSpan(inputSpan, "precondition_failed")
			observability.SetDataOperationOutcome(c.Request.Context(), "precondition_failed")
			observability.WriteDataJSON(c, http.StatusConflict, gin.H{"error": "raw listing changed; restart inventory"})
			return
		}
		value, err := strconv.Atoi(parts[1])
		if err != nil || value < 0 || value > len(files) || value%rawSyncPageSize != 0 {
			observability.FinishDataSpan(inputSpan, "rejected")
			observability.WriteDataJSON(c, http.StatusBadRequest, gin.H{"error": "invalid page token"})
			return
		}
		offset = value
	}
	observability.FinishDataSpan(inputSpan, "success")
	end := min(offset+rawSyncPageSize, len(files))
	page := rawSyncListPage{Files: files[offset:end], TotalFiles: len(files), Snapshot: snapshot}
	if end < len(files) {
		page.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", snapshot, end)))
	}
	c.Header("Cache-Control", "no-store")
	observability.WriteDataJSON(c, http.StatusOK, page)
}

func (h *Handler) SyncRawDownload(c *gin.Context) {
	rawStore, ok := h.rawSyncStore(c)
	if !ok {
		return
	}
	_, inputSpan := observability.StartDataPhase(c.Request.Context(), "input")
	path := c.Query("path")
	generation := c.Query("generation")
	if store.ValidateRawSyncPath(path) != nil || generation == "" {
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, gin.H{"error": "a valid raw path and generation are required"})
		return
	}
	observability.FinishDataSpan(inputSpan, "success")
	readCtx, readSpan := observability.StartDataPhase(c.Request.Context(), "read")
	reader, size, err := rawStore.OpenSyncRawFile(readCtx, path, generation)
	if err != nil {
		outcome := observability.DataErrorOutcome(err)
		observability.FinishDataSpan(readSpan, outcome)
		observability.SetDataOperationOutcome(c.Request.Context(), outcome)
		writeRawSyncStorageError(c, err)
		return
	}
	observability.SetDataCount(readSpan, "bytes", size)
	observability.FinishDataSpan(readSpan, "success")
	defer reader.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Raw-Generation", generation)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	transferCtx, transferSpan := observability.StartDataPhase(c.Request.Context(), "transfer")
	_, err = io.Copy(c.Writer, reader)
	if err != nil {
		observability.FinishDataSpan(transferSpan, "transfer_failure")
		observability.SetDataOperationOutcome(transferCtx, "transfer_failure")
		// The client verifies the pinned digest before publishing its temp file.
		return
	}
	observability.SetDataCount(transferSpan, "bytes", size)
	observability.FinishDataSpan(transferSpan, "success")
}

func (h *Handler) SyncRawUpload(c *gin.Context) {
	rawStore, ok := h.rawSyncStore(c)
	if !ok {
		return
	}
	path := c.Query("path")
	sha256 := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Content-SHA256")))
	expectedGeneration := strings.TrimSpace(c.GetHeader("X-Expected-Generation"))
	_, inputSpan := observability.StartDataPhase(c.Request.Context(), "input")
	if store.ValidateRawSyncPath(path) != nil || !store.ValidRawSyncSHA256(sha256) {
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, gin.H{"error": "a valid raw path and SHA-256 are required"})
		return
	}
	observability.FinishDataSpan(inputSpan, "success")
	writeCtx, writeSpan := observability.StartDataPhase(c.Request.Context(), "write")
	generation, err := rawStore.WriteSyncRawFile(writeCtx, path, c.Request.Body, expectedGeneration, sha256)
	if err != nil {
		outcome := observability.DataErrorOutcome(err)
		if errors.Is(err, store.ErrRawSyncConflict) || errors.Is(err, store.ErrGenerationMismatch) {
			outcome = "precondition_failed"
		} else if errors.Is(err, store.ErrRawSyncCommitUncertain) {
			outcome = "unknown"
		}
		observability.FinishDataSpan(writeSpan, outcome)
		observability.SetDataOperationOutcome(c.Request.Context(), outcome)
		writeRawSyncStorageError(c, err)
		return
	}
	observability.FinishDataSpan(writeSpan, "committed")
	observability.SetDataOperationOutcome(c.Request.Context(), "committed")
	c.Header("Cache-Control", "no-store")
	observability.WriteDataJSON(c, http.StatusOK, gin.H{"generation": generation, "sha256": sha256})
}

func (h *Handler) rawSyncStore(c *gin.Context) (store.RawSyncStore, bool) {
	userID, projectID := strings.TrimSpace(c.GetString("userID")), strings.TrimSpace(c.GetString("projectID"))
	if h.store == nil || userID == "" || projectID == "" {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, gin.H{"error": "raw sync storage unavailable"})
		return nil, false
	}
	// Raw objects are outside generated wiki snapshots, so scope the current
	// Project store directly instead of pinning a compiled generation.
	rawStore, ok := h.store.Scope(userID, projectID).(store.RawSyncStore)
	if !ok {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, gin.H{"error": "raw sync storage unavailable"})
		return nil, false
	}
	return rawStore, true
}

func rawSyncSnapshot(files []store.RawSyncFile) string {
	hasher := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hasher, "%s\x00%d\x00%s\x00%s\n", file.Path, file.Size, file.SHA256, file.Generation)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func writeRawSyncStorageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrRawSyncCommitUncertain):
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, gin.H{"error": "raw file write outcome is uncertain"})
	case errors.Is(err, store.ErrRawSyncConflict), errors.Is(err, store.ErrGenerationMismatch):
		observability.WriteDataJSON(c, http.StatusConflict, gin.H{"error": "raw file changed; restart inventory"})
	case errors.Is(err, store.ErrObjectNotExist):
		observability.WriteDataJSON(c, http.StatusNotFound, gin.H{"error": "raw file does not exist"})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		observability.WriteDataJSON(c, http.StatusRequestTimeout, gin.H{"error": "raw sync request was interrupted"})
	default:
		observability.WriteDataJSON(c, http.StatusBadRequest, gin.H{"error": "raw file could not be transferred"})
	}
}
