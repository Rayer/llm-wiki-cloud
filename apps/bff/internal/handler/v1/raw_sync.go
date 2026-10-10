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
		if h.syncBindingAuthority == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "sync binding authority unavailable"})
			return
		}
		err := h.syncBindingAuthority.CheckSyncBinding(
			c.Request.Context(), c.GetString("userID"), c.GetString("projectID"),
			c.GetHeader("X-Sync-Binding-ID"), c.GetHeader("X-Wiki-ID"), h.syncBindingHost,
		)
		if errors.Is(err, auth.ErrSyncBindingUnauthorized) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "sync binding is not active for this Project"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "sync binding authority unavailable"})
			return
		}
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
	files, err := rawStore.ListSyncRawFiles(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list raw files"})
		return
	}
	snapshot := rawSyncSnapshot(files)
	offset := 0
	if token := c.Query("page_token"); token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page token"})
			return
		}
		parts := strings.Split(string(decoded), ":")
		if len(parts) != 2 || parts[0] != snapshot {
			c.JSON(http.StatusConflict, gin.H{"error": "raw listing changed; restart inventory"})
			return
		}
		value, err := strconv.Atoi(parts[1])
		if err != nil || value < 0 || value > len(files) || value%rawSyncPageSize != 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page token"})
			return
		}
		offset = value
	}
	end := min(offset+rawSyncPageSize, len(files))
	page := rawSyncListPage{Files: files[offset:end], TotalFiles: len(files), Snapshot: snapshot}
	if end < len(files) {
		page.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", snapshot, end)))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, page)
}

func (h *Handler) SyncRawDownload(c *gin.Context) {
	rawStore, ok := h.rawSyncStore(c)
	if !ok {
		return
	}
	path := c.Query("path")
	generation := c.Query("generation")
	if store.ValidateRawSyncPath(path) != nil || generation == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid raw path and generation are required"})
		return
	}
	reader, size, err := rawStore.OpenSyncRawFile(c.Request.Context(), path, generation)
	if err != nil {
		writeRawSyncStorageError(c, err)
		return
	}
	defer reader.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Raw-Generation", generation)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, reader); err != nil {
		// The client verifies the pinned digest before publishing its temp file.
		return
	}
}

func (h *Handler) SyncRawUpload(c *gin.Context) {
	rawStore, ok := h.rawSyncStore(c)
	if !ok {
		return
	}
	path := c.Query("path")
	sha256 := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Content-SHA256")))
	expectedGeneration := strings.TrimSpace(c.GetHeader("X-Expected-Generation"))
	if store.ValidateRawSyncPath(path) != nil || !store.ValidRawSyncSHA256(sha256) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid raw path and SHA-256 are required"})
		return
	}
	generation, err := rawStore.WriteSyncRawFile(c.Request.Context(), path, c.Request.Body, expectedGeneration, sha256)
	if err != nil {
		writeRawSyncStorageError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"generation": generation, "sha256": sha256})
}

func (h *Handler) rawSyncStore(c *gin.Context) (store.RawSyncStore, bool) {
	userID, projectID := strings.TrimSpace(c.GetString("userID")), strings.TrimSpace(c.GetString("projectID"))
	if h.store == nil || userID == "" || projectID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "raw sync storage unavailable"})
		return nil, false
	}
	// Raw objects are outside generated wiki snapshots, so scope the current
	// Project store directly instead of pinning a compiled generation.
	rawStore, ok := h.store.Scope(userID, projectID).(store.RawSyncStore)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "raw sync storage unavailable"})
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
	case errors.Is(err, store.ErrRawSyncConflict), errors.Is(err, store.ErrGenerationMismatch):
		c.JSON(http.StatusConflict, gin.H{"error": "raw file changed; restart inventory"})
	case errors.Is(err, store.ErrRawSyncCommitUncertain):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "raw file write outcome is uncertain"})
	case errors.Is(err, store.ErrObjectNotExist):
		c.JSON(http.StatusNotFound, gin.H{"error": "raw file does not exist"})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "raw sync request was interrupted"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "raw file could not be transferred"})
	}
}
