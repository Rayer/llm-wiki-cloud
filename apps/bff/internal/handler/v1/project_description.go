package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxProjectDescriptionLength = 1000
	maxProjectDescriptionBytes  = 32 * 1024
)

var (
	errProjectDescriptionInvalid  = errors.New("invalid project description")
	errProjectDescriptionTooLarge = errors.New("project description request body too large")
	errProjectDescriptionConflict = errors.New("project description revision conflict")
	errProjectDescriptionCorrupt  = errors.New("corrupt project description")
)

type projectDescriptionRequest struct {
	Description *string `json:"description"`
	Revision    *int64  `json:"revision"`
}

type projectDescriptionResponse struct {
	ProjectID   string `json:"project_id"`
	Description string `json:"description"`
	Revision    int64  `json:"revision"`
}

func normalizeProjectDescription(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", errProjectDescriptionInvalid
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	if utf8.RuneCountInString(value) > maxProjectDescriptionLength {
		return "", errProjectDescriptionInvalid
	}
	return value, nil
}

func decodeInitProjectJSON(body io.Reader, destination *initProjectRequest) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: multiple JSON values")
		}
		return err
	}
	return nil
}

func optionalInitProjectDescription(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errProjectDescriptionInvalid
	}
	if !utf8.Valid(raw) || hasUnpairedJSONSurrogate(raw) {
		return "", errProjectDescriptionInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errProjectDescriptionInvalid
	}
	return normalizeProjectDescription(value)
}

func decodeProjectDescriptionJSON(body io.Reader, destination *projectDescriptionRequest) error {
	data, err := io.ReadAll(io.LimitReader(body, maxProjectDescriptionBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxProjectDescriptionBytes {
		return errProjectDescriptionTooLarge
	}
	if !utf8.Valid(data) || hasUnpairedJSONSurrogate(data) {
		return errProjectDescriptionInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("invalid JSON: expected object")
	}
	keys := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid JSON: expected object key")
		}
		if _, exists := keys[key]; exists {
			return errors.New("invalid JSON: duplicate object key")
		}
		keys[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return decodeStrictJSON(bytes.NewReader(data), destination)
}

func hasUnpairedJSONSurrogate(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if !inString {
			if data[i] == '"' {
				inString = true
			}
			continue
		}
		switch data[i] {
		case '"':
			inString = false
		case '\\':
			i++
			if i >= len(data) || data[i] != 'u' || i+4 >= len(data) {
				continue
			}
			code, ok := parseJSONHex4(data[i+1 : i+5])
			if !ok {
				continue
			}
			i += 4
			if code >= 0xDC00 && code <= 0xDFFF {
				return true
			}
			if code < 0xD800 || code > 0xDBFF {
				continue
			}
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return true
			}
			low, ok := parseJSONHex4(data[i+3 : i+7])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 6
		}
	}
	return false
}

func parseJSONHex4(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var result uint16
	for _, digit := range value {
		result <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			result += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			result += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			result += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func projectDescriptionValues(data map[string]interface{}) (string, int64, error) {
	description := ""
	if value, ok := data["description"]; ok {
		var valid bool
		description, valid = value.(string)
		if !valid || !utf8.ValidString(description) || strings.ContainsRune(description, '\x00') {
			return "", 0, errProjectDescriptionCorrupt
		}
	}

	revision := int64(0)
	if value, ok := data["description_revision"]; ok {
		switch number := value.(type) {
		case int:
			revision = int64(number)
		case int32:
			revision = int64(number)
		case int64:
			revision = number
		default:
			return "", 0, errProjectDescriptionCorrupt
		}
		if revision < 0 {
			return "", 0, errProjectDescriptionCorrupt
		}
	}
	return description, revision, nil
}

func projectDescriptionFromData(projectID string, data map[string]interface{}) (projectDescriptionResponse, error) {
	description, revision, err := projectDescriptionValues(data)
	if err != nil {
		return projectDescriptionResponse{}, err
	}
	return projectDescriptionResponse{ProjectID: projectID, Description: description, Revision: revision}, nil
}

// GetProjectDescription handles GET /api/v1/projects/:pid/description.
func (h *Handler) GetProjectDescription(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	projectID, ok := projectDescriptionTarget(c)
	if !ok {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid project ID"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project description unavailable"})
		return
	}

	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(userID, projectID))
	snapshot, err := projectRef.Get(c.Request.Context())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			c.JSON(http.StatusNotFound, handler.ErrorResponse{Error: "project not found"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project description unavailable"})
		return
	}
	if err := authorizeProjectDocument(projectRef.ID, snapshot.Data(), userID, projectID, ProjectRead); err != nil {
		c.JSON(http.StatusNotFound, handler.ErrorResponse{Error: "project not found"})
		return
	}
	response, err := projectDescriptionFromData(projectID, snapshot.Data())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project description unavailable"})
		return
	}
	c.JSON(http.StatusOK, response)
}

// PutProjectDescription handles PUT /api/v1/projects/:pid/description.
func (h *Handler) PutProjectDescription(c *gin.Context) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handler.ErrorResponse{Error: "user not authenticated"})
		return
	}
	projectID, ok := projectDescriptionTarget(c)
	if !ok {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid project ID"})
		return
	}
	var request projectDescriptionRequest
	if err := decodeProjectDescriptionJSON(c.Request.Body, &request); err != nil {
		if errors.Is(err, errProjectDescriptionTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, handler.ErrorResponse{Error: "request body too large"})
			return
		}
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "invalid description request"})
		return
	}
	if request.Description == nil || request.Revision == nil || *request.Revision < 0 {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "description and non-negative revision are required"})
		return
	}
	description, err := normalizeProjectDescription(*request.Description)
	if err != nil {
		c.JSON(http.StatusBadRequest, handler.ErrorResponse{Error: "description must be valid text of at most 1000 characters"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project description unavailable"})
		return
	}

	response, err := h.updateProjectDescription(c.Request.Context(), userID, projectID, description, *request.Revision)
	if err != nil {
		switch {
		case errors.Is(err, errProfileProjectNotFound):
			c.JSON(http.StatusNotFound, handler.ErrorResponse{Error: "project not found"})
		case errors.Is(err, errProjectDescriptionConflict):
			c.JSON(http.StatusConflict, handler.ErrorResponse{Error: "description revision conflict"})
		default:
			c.JSON(http.StatusServiceUnavailable, handler.ErrorResponse{Error: "project description unavailable"})
		}
		return
	}
	c.JSON(http.StatusOK, response)
}

func projectDescriptionTarget(c *gin.Context) (string, bool) {
	projectID := strings.TrimSpace(c.Param("pid"))
	if !auth.ValidPathSegment(projectID) {
		return "", false
	}
	contextProjectID := strings.TrimSpace(c.GetString("projectID"))
	headerProjectID := strings.TrimSpace(c.GetHeader("X-Project-ID"))
	if (contextProjectID != "" && contextProjectID != projectID) || (headerProjectID != "" && headerProjectID != projectID) {
		return "", false
	}
	return projectID, true
}

func (h *Handler) updateProjectDescription(ctx context.Context, userID, projectID, description string, expectedRevision int64) (projectDescriptionResponse, error) {
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(userID, projectID))
	var response projectDescriptionResponse
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(projectRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return errProfileProjectNotFound
			}
			return err
		}
		if err := authorizeProjectDocument(projectRef.ID, snapshot.Data(), userID, projectID, ProjectEdit); err != nil {
			return errProfileProjectNotFound
		}
		currentDescription, currentRevision, err := projectDescriptionValues(snapshot.Data())
		if err != nil {
			return err
		}
		if currentRevision != expectedRevision {
			return errProjectDescriptionConflict
		}
		if description == currentDescription {
			response = projectDescriptionResponse{ProjectID: projectID, Description: currentDescription, Revision: currentRevision}
			return nil
		}
		response = projectDescriptionResponse{ProjectID: projectID, Description: description, Revision: currentRevision + 1}
		return tx.Update(projectRef, []firestore.Update{
			{Path: "description", Value: description},
			{Path: "description_revision", Value: response.Revision},
			{Path: "description_updated_at", Value: time.Now().UTC()},
		})
	})
	if err != nil {
		return projectDescriptionResponse{}, err
	}
	return response, nil
}
