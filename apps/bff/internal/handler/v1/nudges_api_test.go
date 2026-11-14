package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

func TestNudgeAttentionAPIRejectsDirectUrgentReadAndDismiss(t *testing.T) {
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID := "nudge-api-owner"
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPipelineFailure)
	category := nudge.Category{SchemaVersion: nudge.SchemaVersion, Category: nudge.CategoryPipelineFailure,
		AttentionID: "urgent-revision", AttentionState: nudge.AttentionUnread,
		MaterialBySource: map[string]string{"source": "urgent"}, SeverityBySource: map[string]nudge.Severity{"source": nudge.SeverityUrgent},
		ActiveCount: 1, UrgentCount: 1, UpdatedAt: time.Now().UTC()}
	if _, err := categoryRef.Set(context.Background(), category); err != nil {
		t.Fatal(err)
	}

	handler := &Handler{firestore: fsClient}
	router := gin.New()
	router.PATCH("/api/v1/nudges/:category/attention", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.PatchNudgeAttention(c)
	})
	for _, action := range []string{"read", "dismiss"} {
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/nudges/pipeline-failure/attention", strings.NewReader(`{"attention_id":"urgent-revision","action":"`+action+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"attention_not_dismissible"`) {
			t.Fatalf("direct urgent %s: status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
}
