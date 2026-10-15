package v1

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/observability"
)

// ReconcilePipelineQuota settles accepted Cloud Run executions independently
// of user status requests. Local executions settle in the native manager.
func (h *Handler) ReconcilePipelineQuota(ctx context.Context) (resultErr error) {
	ctx, root := observability.StartPipelineSpan(ctx, "pipeline.quota.reconcile")
	ctx, detailBudget := observability.WithPipelineDetailBudget(ctx, 16)
	processedCount := 0
	defer func() {
		observability.SetPipelineCount(root, "pipeline.processed_count", processedCount)
		observability.SetPipelineCount(root, "pipeline.suppressed_count", detailBudget.Suppressed())
		observability.FinishPipelineSpan(root, pipelineTraceOutcome(resultErr))
	}()
	quotaStore := h.effectiveQuotaStore()
	if quotaStore == nil || h.localPipeline != nil {
		return nil
	}
	listCtx, listSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.list")
	reservations, err := quotaStore.ListPendingQuotaReservations(listCtx)
	observability.FinishPipelineSpan(listSpan, pipelineTraceOutcome(err))
	if err != nil || len(reservations) == 0 {
		return err
	}
	pendingWithoutExecution := make(map[string]firestore.QuotaReservation)
	needsExecutionLookup := false
	for _, reservation := range reservations {
		processedCount++
		if reservation.ExecutionID != "" {
			needsExecutionLookup = true
			continue
		}
		lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
		confirmedFailure, err := h.hasConfirmedPipelineInvokeFailure(lookupCtx, reservation.UserID, reservation.ProjectID, reservation.ID)
		lookupOutcome := "pending"
		if err != nil {
			lookupOutcome = pipelineTraceOutcome(err)
		} else if confirmedFailure {
			lookupOutcome = "success"
		}
		observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
		if err != nil {
			return err
		}
		if confirmedFailure {
			if _, err := quotaStore.SettleQuotaReservation(ctx, reservation.ID, "FAILED"); err != nil {
				return err
			}
			continue
		}
		pendingWithoutExecution[reservation.ID] = reservation
	}
	if !needsExecutionLookup && len(pendingWithoutExecution) == 0 {
		return nil
	}
	token, err := h.getMetadataAccessToken(ctx)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.ExecutionID == "" {
			continue
		}
		if err := func() error {
			lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
			lookupOutcome := "pending"
			defer func() { observability.FinishPipelineSpan(lookupSpan, lookupOutcome) }()
			execution, err := h.fetchCloudRunExecution(lookupCtx, token, reservation.ExecutionID)
			if err != nil {
				if err == errPipelineExecutionNotFound {
					lookupOutcome = "unknown"
					return nil
				}
				lookupOutcome = pipelineTraceOutcome(err)
				return err
			}
			if cloudRunExecutionReservationID(execution, reservation.UserID, reservation.ProjectID) != reservation.ID {
				lookupOutcome = "not_applicable"
				return nil
			}
			outcome, err := h.settleCloudRunQuotaReservation(lookupCtx, quotaStore, reservation, execution)
			lookupOutcome = outcome
			if err != nil {
				return err
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	if len(pendingWithoutExecution) == 0 {
		return nil
	}

	pendingByID := pendingWithoutExecution
	pageToken := ""
	for {
		listCtx, pageSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.list")
		page, err := h.listCloudRunExecutions(listCtx, token, pageToken)
		observability.FinishPipelineSpan(pageSpan, pipelineTraceOutcome(err))
		if err != nil {
			return err
		}
		for _, execution := range page.Executions {
			for reservationID, reservation := range pendingByID {
				if cloudRunExecutionReservationID(execution, reservation.UserID, reservation.ProjectID) != reservationID {
					continue
				}
				if err := func() error {
					lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
					lookupOutcome := "pending"
					defer func() { observability.FinishPipelineSpan(lookupSpan, lookupOutcome) }()
					executionID := shortCloudRunExecutionName(execution.Name, true)
					if executionID != "" {
						linkCtx, linkSpan := observability.StartPipelineSpan(lookupCtx, "pipeline.quota.link")
						linkErr := quotaStore.LinkQuotaReservation(linkCtx, reservationID, executionID)
						linkOutcome := "success"
						if linkErr != nil {
							linkOutcome = "degraded"
						}
						observability.FinishPipelineSpan(linkSpan, linkOutcome)
					}
					outcome, err := h.settleCloudRunQuotaReservation(lookupCtx, quotaStore, reservation, execution)
					lookupOutcome = outcome
					if err != nil {
						return err
					}
					return nil
				}(); err != nil {
					return err
				}
				delete(pendingByID, reservationID)
			}
		}
		if page.NextPageToken == "" || len(pendingByID) == 0 {
			return nil
		}
		pageToken = page.NextPageToken
	}
}

func (h *Handler) settleCloudRunQuotaReservation(ctx context.Context, quotaStore pipelineQuotaStore, reservation firestore.QuotaReservation, execution cloudRunExecution) (outcome string, resultErr error) {
	outcome = "pending"
	state := cloudRunExecutionStatus(execution)
	if state == "FAILED" {
		publication := h.cloudRunQuotaPublicationOutcome(ctx, &pipelineExecutionOwner{userID: reservation.UserID, projectID: reservation.ProjectID}, shortCloudRunExecutionName(execution.Name, true))
		switch publication {
		case pipelinePublicationCommitted:
			state = "SUCCEEDED"
		case pipelinePublicationUnknown:
			return "unknown", nil
		}
	}
	if state != "SUCCEEDED" && state != "FAILED" && state != "CANCELLED" {
		return "pending", nil
	}
	settlement, err := quotaStore.SettleQuotaReservation(ctx, reservation.ID, state)
	if err != nil {
		return pipelineTraceOutcome(err), err
	}
	if settlement == "not_applicable" {
		return "not_applicable", nil
	}
	if settlement == "pending" {
		return "pending", nil
	}
	return "success", nil
}

func cloudRunExecutionReservationID(execution cloudRunExecution, userID, projectID string) string {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return ""
	}
	for _, container := range execution.Template.Containers {
		env := make(map[string]string)
		for _, variable := range container.Env {
			env[variable.Name] = variable.Value
		}
		if env["USER_ID"] == userID && env["PROJECT_ID"] == projectID && env["TASK_TYPE"] == "pipeline" {
			return strings.TrimSpace(env[pipelineReservationEnvName])
		}
	}
	return ""
}

func (h *Handler) RunPipelineQuotaReconciler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := h.ReconcilePipelineQuota(ctx); err != nil {
			log.Print("pipeline quota reconciliation unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
