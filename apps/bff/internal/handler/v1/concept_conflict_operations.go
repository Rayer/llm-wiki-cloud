package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const maxConceptConflictOperationBytes = 8 << 10

type conceptConflictResolveRequest struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	ConflictID  string `json:"conflict_id"`
	Path        string `json:"path"`
	StorageView string `json:"storage_view"`
	PairSHA256  string `json:"pair_sha256"`
}

type conceptConflictOperationFingerprint struct {
	UserID      string `json:"user_id"`
	ProjectID   string `json:"project_id"`
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	ConflictID  string `json:"conflict_id"`
	Path        string `json:"path"`
	StorageView string `json:"storage_view"`
	PairSHA256  string `json:"pair_sha256"`
}

type conceptConflictWorkerOperation struct {
	OperationID   string         `json:"operation_id"`
	PayloadSHA256 string         `json:"payload_sha256"`
	Action        string         `json:"action"`
	ConflictID    string         `json:"conflict_id"`
	Path          string         `json:"path"`
	StorageView   string         `json:"storage_view"`
	Expected      map[string]any `json:"expected"`
}

type conceptConflictResolveResponse struct {
	OperationID   string `json:"operation_id"`
	PayloadSHA256 string `json:"payload_sha256"`
	ExecutionID   string `json:"execution_id,omitempty"`
	Status        string `json:"status"`
	Action        string `json:"action"`
}

type conceptConflictOperationStatusResponse struct {
	OperationID string                                `json:"operation_id"`
	Status      string                                `json:"status"`
	Result      map[string]any                        `json:"result,omitempty"`
	Execution   *handlerapi.PipelineExecutionResponse `json:"execution,omitempty"`
}

func decodeConceptConflictResolveRequest(reader io.Reader) (conceptConflictResolveRequest, error) {
	var request conceptConflictResolveRequest
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return request, errors.New("multiple JSON values")
		}
		return request, err
	}
	return request, nil
}

func conceptConflictPayloadSHA256(userID, projectID string, request conceptConflictResolveRequest) string {
	fingerprint := conceptConflictOperationFingerprint{
		UserID: userID, ProjectID: projectID, OperationID: request.OperationID,
		Action: request.Action, ConflictID: request.ConflictID, Path: request.Path,
		StorageView: request.StorageView, PairSHA256: request.PairSHA256,
	}
	encoded, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validConceptConflictOperationID(id string) bool {
	if len(id) < 8 || len(id) > 96 || !auth.ValidPathSegment(id) {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (h *Handler) authorizeConceptConflictWrite(c *gin.Context) (string, string, bool) {
	userID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.GetString("projectID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handlerapi.ErrorResponse{Error: "user not authenticated"})
		return "", "", false
	}
	if projectID == "" {
		c.JSON(http.StatusBadRequest, handlerapi.ErrorResponse{Error: "project is required"})
		return "", "", false
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), userID, projectID, ProjectEdit); err != nil {
		if errors.Is(err, errProfileProjectNotFound) {
			c.JSON(http.StatusNotFound, handlerapi.ErrorResponse{Error: "project not found"})
		} else {
			c.JSON(http.StatusServiceUnavailable, handlerapi.ErrorResponse{Error: "project is unavailable"})
		}
		return "", "", false
	}
	return userID, projectID, true
}

func writeConceptConflictOperationError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

// ResolveConceptConflict queues a single, authorized resolution against the
// exact comparison pair supplied by the caller. The worker rechecks that pair
// inside the backend's existing publication lease and commit boundary.
func (h *Handler) ResolveConceptConflict(c *gin.Context) {
	userID, projectID, ok := h.authorizeConceptConflictWrite(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxConceptConflictOperationBytes)
	request, err := decodeConceptConflictResolveRequest(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeConceptConflictOperationError(c, http.StatusRequestEntityTooLarge, "operation request is too large")
			return
		}
		writeConceptConflictOperationError(c, http.StatusBadRequest, "invalid operation request")
		return
	}
	if !validConceptConflictOperationID(request.OperationID) || request.Action != "keep_published" && request.Action != "publish_candidate" {
		writeConceptConflictOperationError(c, http.StatusBadRequest, "invalid operation identity or action")
		return
	}
	if _, valid := normalizePublishedConceptPath(request.Path); !valid || len(request.ConflictID) != 64 || len(request.PairSHA256) != 64 {
		writeConceptConflictOperationError(c, http.StatusBadRequest, "invalid conflict identity")
		return
	}
	if request.StorageView == "" || len(request.StorageView) > 256 {
		writeConceptConflictOperationError(c, http.StatusBadRequest, "invalid comparison snapshot")
		return
	}
	payloadSHA256 := conceptConflictPayloadSHA256(userID, projectID, request)
	ctx := c.Request.Context()
	scoped, err := h.GetStore(c)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	state, closeState, err := readConceptConflictDatabase(ctx, scoped)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	defer closeState()
	if result, exists, readErr := state.operationResult(ctx, userID, request.OperationID, payloadSHA256); readErr != nil {
		writeConceptConflictReadError(c, readErr)
		return
	} else if exists {
		c.JSON(http.StatusOK, result)
		return
	}
	if exists, checkErr := state.hasOperationID(ctx, userID, request.OperationID); checkErr != nil {
		writeConceptConflictReadError(c, checkErr)
		return
	} else if exists {
		writeConceptConflictOperationError(c, http.StatusConflict, "operation_id was already used with a different payload")
		return
	}
	// Resolve the comparison from one local lease-protected view. The earlier
	// database read above is only for durable receipt lookup and may predate a
	// worker commit that completed while this request was being authorized.
	comparison, err := compareConceptConflict(ctx, projectID, scoped, request.Path)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	if comparison.ConflictID != request.ConflictID || comparison.PairSHA256 != request.PairSHA256 || comparison.StorageView != request.StorageView {
		writeConceptConflictOperationError(c, http.StatusConflict, "comparison is stale; reload both versions")
		return
	}
	if comparison.Confidence < comparison.MinConfidence {
		writeConceptConflictOperationError(c, http.StatusConflict, "candidate is below the applicable confidence threshold")
		return
	}
	operation := conceptConflictWorkerOperation{
		OperationID: request.OperationID, PayloadSHA256: payloadSHA256,
		Action: request.Action, ConflictID: request.ConflictID, Path: request.Path,
		StorageView: request.StorageView,
		Expected: map[string]any{
			"conflict_id":        comparison.ConflictID,
			"published_sha256":   comparison.Published.SHA256,
			"candidate_sha256":   comparison.Candidate.SHA256,
			"published_identity": comparison.Published.Identity,
			"candidate_identity": comparison.Candidate.Identity,
			"index_sha256":       comparison.IndexSHA256,
			"min_confidence":     comparison.MinConfidence,
		},
	}
	operationJSON, err := json.Marshal(operation)
	if err != nil {
		writeConceptConflictOperationError(c, http.StatusInternalServerError, "operation could not be encoded")
		return
	}
	executionID, err := h.invokePipelineJobStageWithAttemptAndEnvironment(ctx, userID, projectID, false, pipelineStageFull, "", "", map[string]string{
		"LWC_CONCEPT_RESOLVE_JSON":  string(operationJSON),
		"LWC_CONFLICT_STORAGE_VIEW": request.StorageView,
	})
	if err != nil {
		writeConceptConflictOperationError(c, http.StatusServiceUnavailable, "operation could not be started")
		return
	}
	c.JSON(http.StatusAccepted, conceptConflictResolveResponse{
		OperationID: request.OperationID, PayloadSHA256: payloadSHA256,
		ExecutionID: executionID, Status: "pending", Action: request.Action,
	})
}

// GetConceptConflictOperation reads the durable result from the current
// committed project snapshot. It reauthorizes every poll before reading it.
func (h *Handler) GetConceptConflictOperation(c *gin.Context) {
	userID, projectID, ok := h.authorizeConceptConflictRead(c)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(c.Param("operationID"))
	if !validConceptConflictOperationID(operationID) {
		writeConceptConflictOperationError(c, http.StatusBadRequest, "invalid operation ID")
		return
	}
	readResult := func(scopedStore store.Store) (map[string]any, bool, error) {
		state, cleanup, readErr := readConceptConflictDatabase(c.Request.Context(), scopedStore)
		if readErr != nil {
			return nil, false, readErr
		}
		defer cleanup()
		return state.operationByID(c.Request.Context(), userID, operationID)
	}
	scoped, err := h.GetStore(c)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	result, found, err := readResult(scoped)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	if found {
		c.JSON(http.StatusOK, conceptConflictOperationStatusResponse{OperationID: operationID, Status: stringValue(result["status"]), Result: result})
		return
	}
	executionID, err := validatePipelineExecutionID(strings.TrimSpace(c.Query("execution_id")))
	if err != nil {
		writeConceptConflictOperationError(c, http.StatusBadRequest, "execution_id is required while operation is pending")
		return
	}
	execution, err := h.pipelineExecutionStatusForOwner(c.Request.Context(), executionID, userID, projectID)
	if err != nil {
		if errors.Is(err, errPipelineExecutionNotFound) {
			c.JSON(http.StatusNotFound, handlerapi.ErrorResponse{Error: "Pipeline execution not found"})
		} else {
			c.JSON(http.StatusServiceUnavailable, handlerapi.ErrorResponse{Error: "operation status unavailable"})
		}
		return
	}
	if execution.Status == "SUCCEEDED" || execution.Status == "FAILED" || execution.Status == "CANCELLED" {
		// A completed worker should have committed its receipt in the same
		// publication boundary; repin after observing terminal execution status.
		fresh := h.store.Scope(userID, projectID)
		fresh, err = pinStore(c.Request.Context(), fresh)
		if err == nil {
			result, found, err = readResult(fresh)
		}
		if err != nil {
			writeConceptConflictReadError(c, err)
			return
		}
		if found {
			c.JSON(http.StatusOK, conceptConflictOperationStatusResponse{OperationID: operationID, Status: stringValue(result["status"]), Result: result, Execution: execution})
			return
		}
		c.JSON(http.StatusOK, conceptConflictOperationStatusResponse{OperationID: operationID, Status: "failed_without_receipt", Execution: execution})
		return
	}
	c.JSON(http.StatusAccepted, conceptConflictOperationStatusResponse{OperationID: operationID, Status: "pending", Execution: execution})
}
