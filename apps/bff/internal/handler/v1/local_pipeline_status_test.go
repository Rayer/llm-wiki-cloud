package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

type emptyLocalPipelineHistory struct{}

func (emptyLocalPipelineHistory) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", nil
}
func (emptyLocalPipelineHistory) Status(_ context.Context, _, _, executionID string) (*handler.PipelineExecutionResponse, error) {
	if executionID == "" {
		return nil, nil
	}
	return nil, handler.ErrPipelineExecutionNotFound
}
func (emptyLocalPipelineHistory) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestLocalPipelineStatusReturnsEmptyHistoryAnd404ForSpecifiedForeignExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := New(nil, nil, search.NewIndex(), nil, nil, nil)
	h.SetLocalPipelineExecutor(emptyLocalPipelineHistory{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", "user-a")
		c.Set("projectID", "project-a")
		c.Next()
	})
	router.GET("/api/v1/pipeline/status", h.PipelineStatus)

	empty := httptest.NewRecorder()
	router.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/v1/pipeline/status", nil))
	if empty.Code != http.StatusOK {
		t.Fatalf("empty local history status=%d body=%s; want 200", empty.Code, empty.Body.String())
	}
	var emptyPayload map[string]json.RawMessage
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if value, exists := emptyPayload["last_execution"]; !exists || string(value) != "null" {
		t.Fatalf("empty local history last_execution=%s; want null", value)
	}

	foreign := httptest.NewRecorder()
	router.ServeHTTP(foreign, httptest.NewRequest(http.MethodGet, "/api/v1/pipeline/status?execution_id=foreign-execution", nil))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("specified foreign execution status=%d body=%s; want 404", foreign.Code, foreign.Body.String())
	}
}
