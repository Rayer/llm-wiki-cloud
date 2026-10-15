package exportjob

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/observability"
)

func startExportRequest(c *gin.Context) func() {
	ctx, span := observability.StartExportSpan(c.Request.Context(), "export.request")
	c.Request = c.Request.WithContext(ctx)
	return func() {
		status := c.Writer.Status()
		outcome := "success"
		if status >= http.StatusInternalServerError {
			outcome = "failure"
		} else if status >= http.StatusBadRequest {
			outcome = "rejected"
		}
		observability.SetExportOutcome(span, "export.phase.outcome", outcome)
		observability.FinishExportSpan(span, outcome)
	}
}

func runExportPhase(ctx context.Context, name string, run func(context.Context) error) error {
	phaseCtx, span := observability.StartExportSpan(ctx, name)
	err := run(phaseCtx)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	observability.SetExportOutcome(span, "export.phase.outcome", outcome)
	observability.FinishExportSpan(span, outcome)
	return err
}
