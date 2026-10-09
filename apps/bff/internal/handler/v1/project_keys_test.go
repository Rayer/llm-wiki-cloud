package v1

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
)

func TestProjectKeyUnknownCreateResponseContainsOnlySafeCorrelationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeProjectKeyManagementError(ctx, auth.ErrProjectKeyCreateUnknown, "0123456789abcdef0123456789abcdef")

	if recorder.Code != 503 {
		t.Fatalf("status=%d, want 503", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["error"] != auth.ErrProjectKeyUnavailable.Error() || body["key_id"] != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unknown-create response contains unexpected fields: %#v", body)
	}
}
