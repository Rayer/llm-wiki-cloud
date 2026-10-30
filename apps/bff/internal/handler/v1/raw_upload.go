package v1

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxRawUploadSize              = 5 << 20
	maxRawUploadRequestSize       = maxRawUploadSize + (1 << 20)
	rawUploadReadyStatus          = "ready"
	rawUploadMarkdownSuffix       = ".md"
	rawUploadMaxFilenameBytes     = 512
	rawUploadConflictErrorMessage = "filename already exists with different content"
)

var (
	rawUploadFilenamePattern = regexp.MustCompile(`^[^/\x00]+\.(md|txt|html?|csv|json|xml|ya?ml|toml|ini|cfg|log|rst|org|tex)$`)
	errRawUploadTooLarge     = errors.New("raw upload file too large")
	errRawUploadEmptyFile    = errors.New("raw upload file is empty")
)

type rawUploadResponse struct {
	Filename         string `json:"filename"`
	Path             string `json:"path"`
	Bytes            int64  `json:"bytes"`
	SHA256           string `json:"sha256"`
	Status           string `json:"status"`
	ProcessingStatus string `json:"processing_status"`
	SourceRevision   int64  `json:"source_revision,omitempty"`
	CommitStatus     string `json:"commit_status,omitempty"`
}

// RawUpload handles POST /api/v1/raw/upload. Set overwrite=true as a form
// field or query parameter to replace an existing file with different content.
func (h *Handler) RawUpload(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.GetString("projectID"))
	if userID == "" {
		observability.WriteDataJSON(c, http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	if projectID == "" {
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "project ID is required"})
		return
	}
	if h.isDemoUser(userID) {
		observability.WriteDataJSON(c, http.StatusForbidden, handler.ErrorResponse{Error: "demo users cannot upload raw files"})
		return
	}
	if rawUploadHasDestinationDirectory(c) {
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "raw upload destination directories are not supported"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "Firestore client is not configured"})
		return
	}

	loadCtx, loadSpan := observability.StartDataPhase(c.Request.Context(), "load")
	c.Request = c.Request.WithContext(loadCtx)
	ready, err := h.rawUploadProjectReady(c, userID, projectID)
	if err != nil {
		observability.FinishDataSpan(loadSpan, observability.DataErrorOutcome(err))
		if status.Code(err) == codes.NotFound {
			observability.WriteDataJSON(c, http.StatusNotFound, handler.ErrorResponse{Error: "project not found"})
			return
		}
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw upload unavailable"})
		return
	}
	observability.FinishDataSpan(loadSpan, "success")
	if !ready {
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project not ready"})
		return
	}

	inputCtx, inputSpan := observability.StartDataPhase(loadCtx, "input")
	c.Request = c.Request.WithContext(inputCtx)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRawUploadRequestSize)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			observability.FinishDataSpan(inputSpan, "rejected")
			observability.WriteDataJSON(c, http.StatusRequestEntityTooLarge, handler.ErrorResponse{Error: "file too large (max 5 MiB)"})
			return
		}
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "file field is required"})
		return
	}
	defer file.Close()
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}

	filename := ""
	if header != nil {
		filename = header.Filename
	}
	if err := validateRawUploadFilename(filename); err != nil {
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: err.Error()})
		return
	}
	relativePath := filename
	if err := store.ValidateRawSyncPath(relativePath); err != nil {
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "invalid raw-relative path"})
		return
	}

	data, size, _, err := readRawUploadBody(file)
	if err != nil {
		outcome := "rejected"
		if !errors.Is(err, errRawUploadTooLarge) && !errors.Is(err, errRawUploadEmptyFile) {
			outcome = observability.DataErrorOutcome(err)
		}
		observability.FinishDataSpan(inputSpan, outcome)
		switch {
		case errors.Is(err, errRawUploadTooLarge):
			observability.WriteDataJSON(c, http.StatusRequestEntityTooLarge, handler.ErrorResponse{Error: "file too large (max 5 MiB)"})
		case errors.Is(err, errRawUploadEmptyFile):
			observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: "empty file"})
		default:
			observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw upload unavailable"})
		}
		return
	}
	observability.SetDataCount(inputSpan, "bytes", size)
	observability.FinishDataSpan(inputSpan, "success")

	wikiStore, err := h.GetGCSClient(c)
	if err != nil {
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw upload unavailable"})
		return
	}
	conditionalWriter, ok := wikiStore.(store.ConditionalWriter)
	if !ok {
		observability.SetDataOperationOutcome(c.Request.Context(), "failure")
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "raw upload unavailable"})
		return
	}
	writeCtx, writeSpan := observability.StartDataPhase(c.Request.Context(), "write")
	journal, sourceRef, err := h.prepareSourceCommit(writeCtx, userID, projectID, "ui_upload", relativePath, data)
	if err != nil {
		observability.FinishDataSpan(writeSpan, "failure")
		observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "raw upload unavailable"})
		return
	}
	result, err := rawintake.Commit(writeCtx, conditionalWriter, relativePath, data, rawUploadOverwrite(c))
	if err != nil {
		if journal != nil {
			_, _ = journal.ReconcileSourceCommit(writeCtx, sourceRef)
		}
		if errors.Is(err, rawintake.ErrConflict) {
			observability.FinishDataSpan(writeSpan, "precondition_failed")
			observability.SetDataOperationOutcome(writeCtx, "precondition_failed")
			observability.WriteDataJSON(c, http.StatusConflict, handler.ErrorResponse{Error: rawUploadConflictErrorMessage})
			return
		}
		if errors.Is(err, rawintake.ErrCommitUncertain) {
			observability.FinishDataSpan(writeSpan, "unknown")
			observability.SetDataOperationOutcome(writeCtx, "unknown")
			observability.WriteDataJSON(c, http.StatusServiceUnavailable, handler.ErrorResponse{Error: "raw upload outcome is uncertain; refresh the raw list before retrying"})
			return
		}
		observability.FinishDataSpan(writeSpan, observability.DataErrorOutcome(err))
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw upload unavailable"})
		return
	}
	commitResult, confirmErr := confirmSourceCommit(writeCtx, journal, sourceRef, conditionalWriter, relativePath, string(result.Status))
	if confirmErr != nil || commitResult.State != "committed" {
		observability.FinishDataSpan(writeSpan, "unknown")
		observability.SetDataOperationOutcome(writeCtx, "unknown")
		response := newRawUploadResponse(filename, size, result)
		response.ProcessingStatus = "unknown"
		response.CommitStatus = "saved"
		observability.WriteDataJSON(c, http.StatusAccepted, response)
		return
	}
	statusCode := http.StatusCreated
	if result.Status != rawintake.StatusCreated {
		statusCode = http.StatusOK
	}
	if result.Status == rawintake.StatusAlreadyExists {
		observability.FinishDataSpan(writeSpan, "success")
		observability.SetDataOperationOutcome(writeCtx, "no_op")
	} else {
		observability.FinishDataSpan(writeSpan, "committed")
		observability.SetDataOperationOutcome(writeCtx, "committed")
	}

	response := newRawUploadResponse(filename, size, result)
	response.ProcessingStatus = commitResult.ProcessingStatus
	response.SourceRevision = commitResult.SourceRevision
	observability.WriteDataJSON(c, statusCode, response)
}

func rawUploadOverwrite(c *gin.Context) bool {
	return c.PostForm("overwrite") == "true" || c.Query("overwrite") == "true"
}

func rawUploadHasDestinationDirectory(c *gin.Context) bool {
	if c.Request == nil || c.Request.URL == nil {
		return false
	}
	for _, directory := range c.Request.URL.Query()["dir"] {
		if directory != "" {
			return true
		}
	}
	return false
}

func (h *Handler) rawUploadProjectReady(c *gin.Context, userID, projectID string) (bool, error) {
	snap, err := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(userID, projectID)).Get(c.Request.Context())
	if err != nil {
		return false, err
	}
	statusValue, _ := snap.Data()["status"].(string)
	return statusValue == rawUploadReadyStatus, nil
}

func validateRawUploadFilename(filename string) error {
	switch {
	case filename == "":
		return fmt.Errorf("filename is empty")
	case len(filename) > rawUploadMaxFilenameBytes:
		return fmt.Errorf("filename too long (max %d bytes)", rawUploadMaxFilenameBytes)
	case !rawUploadFilenamePattern.MatchString(filename):
		return fmt.Errorf("unsupported file type (accepted: .md, .txt, .html, .csv, .json, .xml, .yaml, .toml)")
	case filename == rawUploadMarkdownSuffix:
		return fmt.Errorf("filename is only '.md'")
	default:
		return nil
	}
}

func readRawUploadBody(r io.Reader) ([]byte, int64, string, error) {
	var buf bytes.Buffer
	hasher := sha256.New()
	size, err := copyRawUploadBody(&buf, hasher, r)
	if err != nil {
		return nil, 0, "", err
	}
	if size == 0 {
		return nil, 0, "", errRawUploadEmptyFile
	}
	return buf.Bytes(), size, fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func copyRawUploadBody(dst *bytes.Buffer, hasher hash.Hash, src io.Reader) (int64, error) {
	var size int64
	chunk := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(chunk)
		if n > 0 {
			size += int64(n)
			if size > maxRawUploadSize {
				return 0, errRawUploadTooLarge
			}
			part := chunk[:n]
			if _, err := dst.Write(part); err != nil {
				return 0, err
			}
			if _, err := hasher.Write(part); err != nil {
				return 0, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return size, nil
			}
			return 0, readErr
		}
	}
}

func newRawUploadResponse(filename string, bytes int64, result rawintake.Result) rawUploadResponse {
	return rawUploadResponse{
		Filename:         filename,
		Path:             result.Path,
		Bytes:            bytes,
		SHA256:           result.SHA256,
		Status:           string(result.Status),
		ProcessingStatus: "not_started",
	}
}
