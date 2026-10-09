package v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
)

const projectKeyCreateBodyLimit = 4096

func (h *Handler) ListProjectKeys(c *gin.Context) {
	keys, err := h.projectKeyService.List(c.Request.Context(), c.GetString("userID"), c.Param("pid"))
	if err != nil {
		writeProjectKeyManagementError(c, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

func (h *Handler) CreateProjectKey(c *gin.Context) {
	name, err := decodeProjectKeyCreateBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": auth.ErrProjectKeyInvalidInput.Error()})
		return
	}
	key, secret, outcomeKeyID, err := h.projectKeyService.Create(c.Request.Context(), c.GetString("userID"), c.Param("pid"), name)
	if err != nil {
		writeProjectKeyManagementError(c, err, outcomeKeyID)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": key, "secret": secret})
}

func (h *Handler) RevokeProjectKey(c *gin.Context) {
	key, err := h.projectKeyService.Revoke(c.Request.Context(), c.GetString("userID"), c.Param("pid"), c.Param("keyID"))
	if err != nil {
		writeProjectKeyManagementError(c, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": key})
}

func decodeProjectKeyCreateBody(c *gin.Context) (string, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, projectKeyCreateBodyLimit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		Name string `json:"name"`
	}
	if err := decoder.Decode(&request); err != nil {
		return "", err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	return request.Name, nil
}

func writeProjectKeyManagementError(c *gin.Context, err error, outcomeKeyID string) {
	if errors.Is(err, auth.ErrProjectKeyAccessDenied) {
		c.JSON(http.StatusNotFound, gin.H{"error": auth.ErrProjectKeyNotFound.Error()})
		return
	}
	response := gin.H{"error": auth.ProjectKeySafeError(err)}
	if errors.Is(err, auth.ErrProjectKeyCreateUnknown) && outcomeKeyID != "" {
		response["key_id"] = outcomeKeyID
	}
	c.JSON(auth.ProjectKeyErrorStatus(err), response)
}
