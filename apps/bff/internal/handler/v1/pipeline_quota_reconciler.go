package v1

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/firestore"
)

// ReconcilePipelineQuota settles accepted Cloud Run executions independently
// of user status requests. Local executions settle in the native manager.
func (h *Handler) ReconcilePipelineQuota(ctx context.Context) error {
	quotaStore := h.effectiveQuotaStore()
	if quotaStore == nil || h.localPipeline != nil {
		return nil
	}
	reservations, err := quotaStore.ListPendingQuotaReservations(ctx)
	if err != nil || len(reservations) == 0 {
		return err
	}
	token, err := h.getMetadataAccessToken(ctx)
	if err != nil {
		return err
	}
	needsExecutionLookup := false
	for _, reservation := range reservations {
		if reservation.ExecutionID == "" {
			needsExecutionLookup = true
			continue
		}
		execution, err := h.fetchCloudRunExecution(ctx, token, reservation.ExecutionID)
		if err != nil {
			if err == errPipelineExecutionNotFound {
				continue
			}
			return err
		}
		if cloudRunExecutionReservationID(execution, reservation.UserID, reservation.ProjectID) != reservation.ID {
			continue
		}
		if err := h.settleCloudRunQuotaReservation(ctx, quotaStore, reservation, execution); err != nil {
			return err
		}
	}
	if !needsExecutionLookup {
		return nil
	}

	pendingByID := make(map[string]firestore.QuotaReservation, len(reservations))
	for _, reservation := range reservations {
		if reservation.ExecutionID == "" {
			pendingByID[reservation.ID] = reservation
		}
	}
	pageToken := ""
	for {
		page, err := h.listCloudRunExecutions(ctx, token, pageToken)
		if err != nil {
			return err
		}
		for _, execution := range page.Executions {
			for reservationID, reservation := range pendingByID {
				if cloudRunExecutionReservationID(execution, reservation.UserID, reservation.ProjectID) != reservationID {
					continue
				}
				executionID := shortCloudRunExecutionName(execution.Name, true)
				if executionID != "" {
					_ = quotaStore.LinkQuotaReservation(ctx, reservationID, executionID)
				}
				if err := h.settleCloudRunQuotaReservation(ctx, quotaStore, reservation, execution); err != nil {
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

func (h *Handler) settleCloudRunQuotaReservation(ctx context.Context, quotaStore pipelineQuotaStore, reservation firestore.QuotaReservation, execution cloudRunExecution) error {
	state := cloudRunExecutionStatus(execution)
	if state == "FAILED" && h.cloudRunPublicationCommitted(ctx, &pipelineExecutionOwner{userID: reservation.UserID, projectID: reservation.ProjectID}, shortCloudRunExecutionName(execution.Name, true)) {
		state = "SUCCEEDED"
	}
	if state != "SUCCEEDED" && state != "FAILED" && state != "CANCELLED" {
		return nil
	}
	_, err := quotaStore.SettleQuotaReservation(ctx, reservation.ID, state)
	return err
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
