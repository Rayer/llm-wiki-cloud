package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

// maxUploadSize limits raw file uploads to 10MB.
const maxUploadSize = 10 << 20

// ═══════════════ RAW UPLOAD ═══════════════

// RawUploadResponse is the response for POST /api/raw/upload.
type RawUploadResponse struct {
	Message  string `json:"message"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Digest   string `json:"digest"`
	Bytes    int64  `json:"bytes"`
}

// UploadRaw handles POST /api/raw/upload — multipart file upload to GCS raw/.
func (h *Handler) UploadRaw(c *gin.Context) {
	// Enforce request body size limit (10MB)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			c.JSON(http.StatusRequestEntityTooLarge, ErrorResponse{Error: "file too large (max 10MB)"})
			return
		}
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "file field is required (multipart form)"})
		return
	}
	defer file.Close()

	// Only accept .md files and sanitize filename to prevent path traversal
	safeFilename := filepath.Base(header.Filename)
	if !strings.HasSuffix(strings.ToLower(safeFilename), ".md") {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "only .md files are accepted"})
		return
	}
	if safeFilename == "." || safeFilename == ".." || safeFilename == ".md" {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid filename"})
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: fmt.Sprintf("read file: %v", err)})
		return
	}

	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "empty file"})
		return
	}

	ctx := context.Background()
	gcsRelPath := fmt.Sprintf("raw/%s", safeFilename)
	digest, err := h.gcs.WriteBytes(ctx, data, gcsRelPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: fmt.Sprintf("upload to GCS: %v", err)})
		return
	}

	log.Printf("Raw uploaded: %s (digest=%s, bytes=%d)", safeFilename, digest, len(data))

	c.JSON(http.StatusOK, RawUploadResponse{
		Message:  "File uploaded to raw/. It will be processed after a pipeline run is triggered.",
		Filename: safeFilename,
		Path:     gcsRelPath,
		Digest:   digest,
		Bytes:    int64(len(data)),
	})
}

// ═══════════════ RAW SCRAPE ═══════════════

// ScrapeRequest is the body for POST /api/v1/raw/scrape. A confirmation request
// carries only the opaque reference; the service commits its stored candidate.
type ScrapeRequest struct {
	URL             string   `json:"url,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	ConfirmationRef string   `json:"confirmation_ref,omitempty"`
}

// ScrapeResponse is the URL intake result returned by the raw scrape route.
type ScrapeResponse struct {
	Status            string                      `json:"status"`
	Message           string                      `json:"message"`
	ConfirmationRef   string                      `json:"confirmation_ref,omitempty"`
	ExpiresAt         string                      `json:"expires_at,omitempty"`
	RetryAfterSeconds int64                       `json:"retry_after_seconds,omitempty"`
	Preview           *urlintake.CandidatePreview `json:"preview,omitempty"`
	Storage           *urlintake.StorageOutcome   `json:"storage,omitempty"`
	ProcessingStatus  string                      `json:"processing_status,omitempty"`
}

// ScrapeRaw handles URL intake through the shared provider and quality service.
//
//	@Summary	Fetch and quality-check URL content into raw markdown
//	@Description	Fetches a public URL, checks the candidate, and saves only a usable page or an explicitly confirmed preview under the authenticated Project.
//	@Tags		raw
//	@Accept		json
//	@Produce	json
//	@Param		request	body		handler.ScrapeRequest	true	"URL and optional source tags, or a confirmation reference"
//	@Success	200		{object}	handler.ScrapeResponse
//	@Failure	400		{object}	handler.ErrorResponse
//	@Failure	401		{object}	handler.ErrorResponse
//	@Failure	409		{object}	handler.ScrapeResponse
//	@Failure	413		{object}	handler.ErrorResponse
//	@Failure	422		{object}	handler.ScrapeResponse
//	@Failure	500		{object}	handler.ErrorResponse
//	@Failure	503		{object}	handler.ScrapeResponse
//	@Security	BearerAuth || ProjectHeader
//	@Router		/api/v1/raw/scrape [post]
func (h *Handler) ScrapeRaw(c *gin.Context) {
	if h.urlIntake == nil {
		c.JSON(http.StatusServiceUnavailable, ScrapeResponse{Status: "unavailable", Message: "URL intake is not configured. Paste Markdown or clip the page and upload a supported file."})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var req ScrapeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			c.JSON(http.StatusRequestEntityTooLarge, ErrorResponse{Error: "request body is too large"})
			return
		}
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "provide a URL or confirmation_ref"})
		return
	}
	actor := urlintake.ActorProject{
		ActorID: strings.TrimSpace(c.GetString("userID")), ProjectID: strings.TrimSpace(c.GetString("projectID")),
	}
	if actor.ActorID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "user not authenticated"})
		return
	}
	if actor.ProjectID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "project ID is required"})
		return
	}
	var result urlintake.Result
	var err error
	if strings.TrimSpace(req.ConfirmationRef) != "" {
		if strings.TrimSpace(req.URL) != "" || len(req.Tags) != 0 {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: "confirmation_ref cannot be combined with a URL or tags"})
			return
		}
		result, err = h.urlIntake.Confirm(c.Request.Context(), actor, req.ConfirmationRef)
	} else if strings.TrimSpace(req.URL) != "" {
		result, err = h.urlIntake.Import(c.Request.Context(), actor, urlintake.ImportRequest{URL: req.URL, Tags: req.Tags})
	} else {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "provide a URL or confirmation_ref"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "URL intake failed"})
		return
	}
	if result.RetryAfterSeconds > 0 {
		c.Header("Retry-After", fmt.Sprint(result.RetryAfterSeconds))
	}
	status := http.StatusOK
	switch result.Status {
	case "rejected":
		status = http.StatusUnprocessableEntity
	case "conflict":
		status = http.StatusConflict
	case "unavailable", "uncertain":
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, ScrapeResponse{
		Status: result.Status, Message: result.Message, ConfirmationRef: result.ConfirmationRef,
		ExpiresAt: result.ExpiresAt, RetryAfterSeconds: result.RetryAfterSeconds,
		Preview: result.Preview, Storage: result.Storage, ProcessingStatus: result.ProcessingStatus,
	})
}

// ═══════════════ LLM TITLE GENERATION ═══════════════

// GenerateTitle handles POST /api/raw/generate-title — generates a wiki-style title from content.
func (h *Handler) GenerateTitle(c *gin.Context) {
	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "content field is required"})
		return
	}

	if h.llm == nil {
		c.JSON(http.StatusOK, gin.H{"title": "Untitled"})
		return
	}

	title, err := h.llm.Chat(
		c.Request.Context(),
		"Generate a concise, descriptive title for a wiki article based on its content. Return ONLY the title, nothing else. Use the same language as the content. Max 80 characters.",
		req.Content[:min(len(req.Content), 2000)],
	)

	if err != nil || title == "" {
		c.JSON(http.StatusOK, gin.H{"title": "Untitled"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"title": strings.TrimSpace(title)})
}
