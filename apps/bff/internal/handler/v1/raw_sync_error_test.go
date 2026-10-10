package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestWriteRawSyncStorageErrorPreservesCommitAndKnownFailureClasses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "commit outcome uncertain", err: fmt.Errorf("%w: %w", store.ErrRawSyncCommitUncertain, context.Canceled), want: http.StatusServiceUnavailable},
		{name: "uncertain commit also reports conflict", err: errors.Join(store.ErrRawSyncCommitUncertain, store.ErrRawSyncConflict), want: http.StatusServiceUnavailable},
		{name: "precommit validation", err: errors.New("invalid upload digest"), want: http.StatusBadRequest},
		{name: "generation conflict", err: store.ErrRawSyncConflict, want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			writeRawSyncStorageError(ctx, test.err)
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), test.want)
			}
		})
	}
}
