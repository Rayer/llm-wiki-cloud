package v1

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
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
	var recordErrors []error
	finish := func(err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = errors.Join(err, ctxErr)
		}
		return errors.Join(errors.Join(recordErrors...), err)
	}
	recordFailure := func(stage string, reservation firestore.QuotaReservation, err error) {
		if err == nil {
			return
		}
		recordErrors = append(recordErrors, fmt.Errorf("pipeline quota reservation %s %s: %w", reservation.ID, stage, err))
		log.Printf("pipeline quota reconciliation record failed stage=%s outcome=%s reservation_id=%s", stage, pipelineTraceOutcome(err), reservation.ID)
	}
	quotaStore := h.effectiveQuotaStore()
	if quotaStore == nil {
		return nil
	}
	if ctx.Err() != nil {
		return finish(nil)
	}
	listCtx, listSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.list")
	reservations, err := quotaStore.ListPendingQuotaReservations(listCtx)
	observability.FinishPipelineSpan(listSpan, pipelineTraceOutcome(err))
	if err != nil {
		return finish(err)
	}
	if len(reservations) == 0 {
		return finish(nil)
	}
	for _, reservation := range reservations {
		if ctx.Err() != nil {
			return finish(nil)
		}
		if reservation.IntentStatus == "preparing" || reservation.IntentStatus == "ready" {
			if err := h.ensurePipelineIntentWork(ctx, reservation); err != nil {
				recordFailure("intent_work", reservation, err)
				if ctx.Err() != nil {
					return finish(nil)
				}
			}
		}
	}
	if h.localPipeline != nil {
		return finish(nil)
	}
	pendingWithoutExecution := make(map[string]firestore.QuotaReservation)
	needsExecutionLookup := false
	for _, reservation := range reservations {
		if ctx.Err() != nil {
			return finish(nil)
		}
		processedCount++
		if reservation.ExecutionID != "" {
			needsExecutionLookup = true
			continue
		}
		lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
		confirmedFailure, err := h.hasConfirmedPipelineInvokeFailure(lookupCtx, reservation)
		lookupOutcome := "pending"
		if err != nil {
			lookupOutcome = pipelineTraceOutcome(err)
		} else if confirmedFailure {
			lookupOutcome = "success"
		}
		observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
		if err != nil {
			recordFailure("invoke_failure_lookup", reservation, err)
			if ctx.Err() != nil {
				return finish(nil)
			}
			continue
		}
		if confirmedFailure {
			if reservation.IntentStatus != "" {
				intentStore, ok := quotaStore.(pipelineIntentQuotaStore)
				if !ok {
					recordFailure("invoke_failure_settlement", reservation, errors.New("pipeline intent store unavailable for confirmed invoke failure"))
					continue
				}
				if err := intentStore.FailPipelineIntent(ctx, reservation.ID, reservation.AttemptID, reservation.UserID, reservation.ProjectID,
					reservation.IntentStatus, "pipeline_invocation", "pipeline_invoke_failed", "Pipeline did not start. Retry this run when service is available."); err != nil {
					recordFailure("invoke_failure_settlement", reservation, err)
					if ctx.Err() != nil {
						return finish(nil)
					}
				}
				continue
			}
			if reservation.Trigger == pipelinequota.TriggerScheduledDaily {
				admission, ok := quotaStore.(interface {
					ReleaseQuotaReservationIfAttempt(context.Context, string, string) (bool, error)
				})
				if !ok {
					recordFailure("scheduled_release", reservation, errors.New("scheduled quota store does not support attempt-bound release"))
					continue
				}
				if _, err := admission.ReleaseQuotaReservationIfAttempt(ctx, reservation.ID, reservation.AttemptID); err != nil {
					recordFailure("scheduled_release", reservation, err)
					if ctx.Err() != nil {
						return finish(nil)
					}
				}
				continue
			}
			admission, ok := quotaStore.(interface {
				ReleaseQuotaReservation(context.Context, string) error
			})
			if ok {
				if err := admission.ReleaseQuotaReservation(ctx, reservation.ID); err != nil {
					recordFailure("claim_release", reservation, err)
					if ctx.Err() != nil {
						return finish(nil)
					}
				}
			} else if _, err := quotaStore.SettleQuotaReservation(ctx, reservation.ID, "FAILED"); err != nil {
				recordFailure("invoke_failure_settlement", reservation, err)
				if ctx.Err() != nil {
					return finish(nil)
				}
			}
			continue
		}
		pendingWithoutExecution[reservation.ID] = reservation
	}
	if !needsExecutionLookup && len(pendingWithoutExecution) == 0 {
		return finish(nil)
	}
	if ctx.Err() != nil {
		return finish(nil)
	}
	token, err := h.getMetadataAccessToken(ctx)
	if err != nil {
		return finish(err)
	}
	for _, reservation := range reservations {
		if ctx.Err() != nil {
			return finish(nil)
		}
		if reservation.ExecutionID == "" {
			continue
		}
		lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
		lookupOutcome := "pending"
		execution, lookupErr := h.fetchCloudRunExecution(lookupCtx, token, reservation.ExecutionID)
		if lookupErr != nil {
			if lookupErr == errPipelineExecutionNotFound {
				lookupOutcome = "unknown"
				observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
				continue
			}
			lookupOutcome = pipelineTraceOutcome(lookupErr)
			observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
			recordFailure("execution_lookup", reservation, lookupErr)
			if ctx.Err() != nil {
				return finish(nil)
			}
			continue
		}
		if !cloudRunExecutionMatchesReservation(execution, reservation) {
			lookupOutcome = "not_applicable"
			observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
			continue
		}
		outcome, settleErr := h.settleCloudRunQuotaReservation(lookupCtx, quotaStore, reservation, execution)
		lookupOutcome = outcome
		observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
		if settleErr != nil {
			recordFailure("execution_settlement", reservation, settleErr)
			if ctx.Err() != nil {
				return finish(nil)
			}
		}
	}
	if len(pendingWithoutExecution) == 0 {
		return finish(nil)
	}

	pendingByID := pendingWithoutExecution
	pageToken := ""
	for {
		if ctx.Err() != nil {
			return finish(nil)
		}
		listCtx, pageSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.list")
		page, err := h.listCloudRunExecutions(listCtx, token, pageToken)
		observability.FinishPipelineSpan(pageSpan, pipelineTraceOutcome(err))
		if err != nil {
			return finish(err)
		}
		for _, execution := range page.Executions {
			if ctx.Err() != nil {
				return finish(nil)
			}
			for reservationID, reservation := range pendingByID {
				if ctx.Err() != nil {
					return finish(nil)
				}
				if !cloudRunExecutionMatchesReservation(execution, reservation) {
					continue
				}
				// This matching execution is the one candidate for this reservation in
				// this sweep. Leave failures pending for the next sweep, but do not
				// invoke or link a second candidate in a later page.
				delete(pendingByID, reservationID)
				lookupCtx, lookupSpan := observability.StartPipelineSpan(ctx, "pipeline.quota.lookup")
				lookupOutcome := "pending"
				executionID := shortCloudRunExecutionName(execution.Name, true)
				if executionID == "" {
					lookupOutcome = "unknown"
					observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
					recordFailure("execution_link", reservation, errors.New("matched pipeline execution has no stable execution ID"))
					if ctx.Err() != nil {
						return finish(nil)
					}
					continue
				}
				linkCtx, linkSpan := observability.StartPipelineSpan(lookupCtx, "pipeline.quota.link")
				linkErr := linkPipelineQuotaReservation(linkCtx, quotaStore, reservation, executionID)
				linkOutcome := "success"
				if linkErr != nil {
					linkOutcome = "degraded"
				}
				observability.FinishPipelineSpan(linkSpan, linkOutcome)
				if linkErr != nil {
					observability.FinishPipelineSpan(lookupSpan, pipelineTraceOutcome(linkErr))
					recordFailure("execution_link", reservation, linkErr)
					if ctx.Err() != nil {
						return finish(nil)
					}
					continue
				}
				outcome, settleErr := h.settleCloudRunQuotaReservation(lookupCtx, quotaStore, reservation, execution)
				lookupOutcome = outcome
				observability.FinishPipelineSpan(lookupSpan, lookupOutcome)
				if settleErr != nil {
					recordFailure("execution_settlement", reservation, settleErr)
					if ctx.Err() != nil {
						return finish(nil)
					}
				}
			}
		}
		if page.NextPageToken == "" || len(pendingByID) == 0 {
			return finish(nil)
		}
		pageToken = page.NextPageToken
	}
}

func (h *Handler) settleCloudRunQuotaReservation(ctx context.Context, quotaStore pipelineQuotaStore, reservation firestore.QuotaReservation, execution cloudRunExecution) (outcome string, resultErr error) {
	outcome = "pending"
	state := cloudRunExecutionStatus(execution)
	if reservation.Trigger == pipelinequota.TriggerSourceCommit {
		publication := h.cloudRunQuotaPublicationOutcome(ctx, &pipelineExecutionOwner{userID: reservation.UserID, projectID: reservation.ProjectID}, shortCloudRunExecutionName(execution.Name, true))
		switch publication {
		case pipelinePublicationCommitted:
			state = "SUCCEEDED"
		case pipelinePublicationAbsent:
			if state == "SUCCEEDED" {
				state = "FAILED"
			}
		case pipelinePublicationUnknown:
			return "unknown", nil
		}
	} else if state == "FAILED" {
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
	settlement, err := settlePipelineQuotaReservation(ctx, quotaStore, reservation, state)
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

func cloudRunExecutionAttemptID(execution cloudRunExecution, userID, projectID string) string {
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
			return strings.TrimSpace(env[pipelineAttemptEnvName])
		}
	}
	return ""
}

func cloudRunExecutionMatchesReservation(execution cloudRunExecution, reservation firestore.QuotaReservation) bool {
	if cloudRunExecutionReservationID(execution, reservation.UserID, reservation.ProjectID) != reservation.ID {
		return false
	}
	actualAttemptID := cloudRunExecutionAttemptID(execution, reservation.UserID, reservation.ProjectID)
	if actualAttemptID != "" {
		return actualAttemptID == pipelineReservationAttemptID(reservation)
	}
	// Before attempt IDs were carried in the Worker environment, manual and
	// non-intent reservations used their stable reservation ID as the attempt.
	// Legacy scheduled jobs with a distinct attempt remain recoverable only while
	// their original non-intent reservation is still current.
	return reservation.AttemptID == "" || reservation.AttemptID == reservation.ID || reservation.IntentStatus == ""
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
