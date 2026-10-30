package v1

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type documentImportAcceptedResponse struct {
	documentimport.Record
	StatusURL string `json:"status_url"`
	SiteURL   string `json:"site_url"`
}

type documentImportDismissRequest struct {
	DismissedThroughSequence int64 `json:"dismissed_through_sequence"`
}

func (h *Handler) acceptDocumentImport(c *gin.Context, userID, projectID, submissionID, batchID, filename string, data []byte) {
	if h.documentImports == nil || h.documentImports.Starter == nil || h.firestore == nil || h.firestore.Raw() == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import worker is unavailable"})
		return
	}
	objects, err := h.documentImportObjects(c)
	if err != nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document storage is unavailable"})
		return
	}
	record, err := h.documentImports.Accept(c.Request.Context(), objects, userID, projectID, submissionID, batchID, filename, data)
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	statusURL := fmt.Sprintf("/api/v1/projects/%s/document-imports/%s", url.PathEscape(projectID), url.PathEscape(record.DocumentID))
	c.Header("Location", statusURL)
	siteURL := "/sources?project_id=" + url.QueryEscape(projectID) + "&conversion_status=pending&document_id=" + url.QueryEscape(record.DocumentID)
	observability.WriteDataJSON(c, http.StatusAccepted, documentImportAcceptedResponse{Record: record, StatusURL: statusURL, SiteURL: siteURL})
}

func (h *Handler) ListDocumentImports(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	filter := c.Query("conversion_status")
	if filter == "" {
		filter = c.Query("filter")
	}
	result, err := h.documentImports.Repository.List(c.Request.Context(), userID, projectID, filter, c.Query("cursor"), queryLimit(c))
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	observability.WriteDataJSON(c, http.StatusOK, result)
}

func (h *Handler) GetDocumentImport(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	record, err := h.documentImports.Repository.Get(c.Request.Context(), userID, projectID, c.Param("id"))
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	if record.DeletionState == documentimport.DeletionComplete {
		observability.WriteDataJSON(c, http.StatusGone, handler.ErrorResponse{Error: "document import is no longer available"})
		return
	}
	observability.WriteDataJSON(c, http.StatusOK, documentimport.PublicRecord(record))
}

func (h *Handler) GetDocumentImportBySubmission(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	record, found, err := h.documentImports.Repository.BySubmission(c.Request.Context(), userID, projectID, c.Param("submissionID"))
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	if !found {
		observability.WriteDataJSON(c, http.StatusNotFound, handler.ErrorResponse{Error: "document import not found"})
		return
	}
	statusURL := fmt.Sprintf("/api/v1/projects/%s/document-imports/%s", url.PathEscape(projectID), url.PathEscape(record.DocumentID))
	if record.ConversionState == documentimport.ConversionReceiving {
		observability.WriteDataJSON(c, http.StatusOK, gin.H{"acceptance_status": "confirming", "status_url": statusURL})
		return
	}
	siteURL := "/sources?project_id=" + url.QueryEscape(projectID) + "&document_id=" + url.QueryEscape(record.DocumentID)
	observability.WriteDataJSON(c, http.StatusOK, gin.H{"acceptance_status": "accepted", "document_id": record.DocumentID, "document": documentimport.PublicRecord(record), "status_url": statusURL, "site_url": siteURL})
}

func (h *Handler) GetDocumentImportFailureSummary(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	siteURL := "/sources?project_id=" + url.QueryEscape(projectID) + "&conversion_status=failed"
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	summary, err := h.documentImports.Repository.Summary(c.Request.Context(), userID, projectID, userID, siteURL)
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	observability.WriteDataJSON(c, http.StatusOK, summary)
}

func (h *Handler) DismissDocumentImportFailureReminder(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	var request documentImportDismissRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "dismissed_through_sequence is required"})
		return
	}
	summary, err := h.documentImports.Repository.Dismiss(c.Request.Context(), userID, projectID, userID, request.DismissedThroughSequence)
	if err != nil {
		writeDocumentImportError(c, err)
		return
	}
	summary.SiteURL = "/sources?project_id=" + url.QueryEscape(projectID) + "&conversion_status=failed"
	observability.WriteDataJSON(c, http.StatusOK, summary)
}

func (h *Handler) DownloadDocumentImportOriginal(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	record, err := h.documentImports.Repository.Get(c.Request.Context(), userID, projectID, c.Param("id"))
	if err != nil || record.DeletionState == documentimport.DeletionComplete {
		writeDocumentImportError(c, documentimport.ErrNotFound)
		return
	}
	objects, err := h.documentImportObjects(c)
	if err != nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "original download is unavailable"})
		return
	}
	data, generation, err := objects.ReadFileWithGeneration(c.Request.Context(), record.OriginalObjectPath)
	if err != nil || generation != record.OriginalGeneration || int64(len(data)) != record.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != record.SHA256 {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "original download is unavailable"})
		return
	}
	mediaType, _, validationErr := documentimport.ValidateUpload(record.OriginalFilename, data)
	if validationErr != nil || mediaType != record.MediaType {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "original download is unavailable"})
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": record.OriginalFilename})
	c.Header("Content-Disposition", disposition)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, record.MediaType, data)
}

func (h *Handler) DeleteDocumentImport(c *gin.Context) {
	userID, projectID, ok := h.documentImportScope(c)
	if !ok {
		return
	}
	if h.isDemoUser(userID) {
		observability.WriteDataJSON(c, http.StatusForbidden, handler.ErrorResponse{Error: "demo users cannot delete documents"})
		return
	}
	revision, err := parseDocumentImportRevision(c.GetHeader("If-Match"))
	if err != nil {
		observability.WriteDataJSON(c, http.StatusPreconditionRequired, handler.ErrorResponse{Error: "If-Match revision is required"})
		return
	}
	if h.documentImports == nil || h.documentImports.Repository == nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is unavailable"})
		return
	}
	record, err := h.documentImports.BeginDelete(c.Request.Context(), userID, projectID, c.Param("id"), revision)
	if errors.Is(err, documentimport.ErrNotFound) || status.Code(err) == codes.NotFound {
		writeDocumentImportError(c, documentimport.ErrNotFound)
		return
	}
	if errors.Is(err, documentimport.ErrStateConflict) || errors.Is(err, documentimport.ErrConflict) {
		writeDocumentImportError(c, documentimport.ErrStateConflict)
		return
	}
	if err != nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document delete is unavailable"})
		return
	}
	if record.DeletionState == documentimport.DeletionComplete {
		c.Status(http.StatusNoContent)
		return
	}
	objects, err := h.documentImportObjects(c)
	if err != nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document delete is unavailable"})
		return
	}
	internalRecord, err := h.documentImports.Repository.Get(c.Request.Context(), userID, projectID, c.Param("id"))
	if err != nil {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document delete is unavailable"})
		return
	}
	go func(service *documentimport.Service, store documentimport.ObjectStore, record documentimport.Record) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if _, err := service.FinishDelete(ctx, store, record); err != nil {
			log.Printf("document import deletion cleanup failed")
		}
	}(h.documentImports, objects, internalRecord)
	observability.WriteDataJSON(c, http.StatusAccepted, documentimport.PublicRecord(internalRecord))
}

func (h *Handler) documentImportScope(c *gin.Context) (string, string, bool) {
	userID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.GetString("projectID"))
	if userID == "" {
		observability.WriteDataJSON(c, http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return "", "", false
	}
	if projectID == "" || projectID != strings.TrimSpace(c.Param("pid")) {
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "project ID does not match request scope"})
		return "", "", false
	}
	return userID, projectID, true
}

func (h *Handler) documentImportObjects(c *gin.Context) (documentimport.ObjectStore, error) {
	objects, err := h.GetStore(c)
	if err != nil {
		return nil, err
	}
	conditional, ok := objects.(documentimport.ObjectStore)
	if !ok {
		return nil, errors.New("conditional document storage is unavailable")
	}
	return conditional, nil
}

func readDocumentImportBody(file interface{ Read([]byte) (int, error) }) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, documentimport.MaxOriginalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > documentimport.MaxOriginalBytes {
		return nil, documentimport.ErrFileTooLarge
	}
	return data, nil
}

func validSubmissionID(value string) bool {
	return documentimport.ValidateSubmissionID(value) == nil
}

func parseDocumentImportRevision(value string) (int64, error) {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if value == "" || strings.HasPrefix(value, "W/") {
		return 0, errors.New("revision required")
	}
	return strconv.ParseInt(value, 10, 64)
}

func writeDocumentImportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, documentimport.ErrInvalidFilename), errors.Is(err, documentimport.ErrUnsupportedFile),
		errors.Is(err, documentimport.ErrInvalidContent), errors.Is(err, documentimport.ErrArchiveResourceLimit), errors.Is(err, documentimport.ErrEmptyFile), errors.Is(err, documentimport.ErrInvalidSubmissionID), errors.Is(err, documentimport.ErrConflict):
		statusCode := http.StatusBadRequest
		if errors.Is(err, documentimport.ErrConflict) {
			statusCode = http.StatusConflict
		}
		observability.WriteDataJSON(c, statusCode, handler.ErrorResponse{Error: err.Error()})
	case errors.Is(err, documentimport.ErrFileTooLarge):
		observability.WriteDataJSON(c, http.StatusRequestEntityTooLarge, handler.ErrorResponse{Error: "file too large (max 5 MiB)"})
	case errors.Is(err, documentimport.ErrNotFound):
		observability.WriteDataJSON(c, http.StatusNotFound, handler.ErrorResponse{Error: "document import not found"})
	case errors.Is(err, documentimport.ErrStateConflict), errors.Is(err, documentimport.ErrCursorStale):
		observability.WriteDataJSON(c, http.StatusConflict, handler.ErrorResponse{Error: "document import state changed; refresh and try again"})
	case errors.Is(err, documentimport.ErrAcceptanceUnknown):
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document acceptance outcome is unknown; check the submission status"})
	case status.Code(err) == codes.NotFound:
		observability.WriteDataJSON(c, http.StatusNotFound, handler.ErrorResponse{Error: "document import not found"})
	default:
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "document import is temporarily unavailable"})
	}
}

func queryLimit(c *gin.Context) int {
	value, _ := strconv.Atoi(c.Query("limit"))
	return value
}
