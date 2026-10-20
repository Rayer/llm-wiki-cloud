package firestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pipelineQuotaCollection            = "pipeline_quota"
	pipelineQuotaReservationCollection = "pipeline_quota_reservations"
	pipelineScheduledDailyCollection   = "pipeline_scheduled_daily"
	pipelineScheduledStatusCollection  = "pipeline_scheduled_status"
)

// QuotaReservation ties one accepted user run to its quota debit. Its settlement
// fields are changed in the same transaction that applies a refund.
type QuotaReservation struct {
	ID                string
	AttemptID         string
	UserID            string
	ProjectID         string
	ExecutionID       string
	ReservedAt        time.Time
	PreviousLastRunAt time.Time
	DayKey            string
	Trigger           pipelinequota.Trigger
	BudgetClass       string
	SettlementStatus  string
	Outcome           string
}

type ScheduledPipelineStatus struct {
	Availability         string     `json:"availability"`
	UserID               string     `json:"user_id"`
	ProjectID            string     `json:"project_id"`
	Schedule             string     `json:"schedule"`
	Timezone             string     `json:"timezone"`
	LastCheckAt          *time.Time `json:"last_check_at,omitempty"`
	NextCheckAt          *time.Time `json:"next_check_at,omitempty"`
	SkipReason           string     `json:"skip_reason,omitempty"`
	PendingWork          int        `json:"pending_work"`
	NewRawFiles          int        `json:"new_raw_files"`
	RawDirtyFiles        int        `json:"raw_dirty_files"`
	AnnotationDirtyFiles int        `json:"annotation_dirty_files"`
	DayKey               string     `json:"day_key"`
	BudgetUsed           bool       `json:"budget_used"`
	LastAttemptAt        *time.Time `json:"last_attempt_at,omitempty"`
	LastAttemptID        string     `json:"last_attempt_id,omitempty"`
	LastExecutionID      string     `json:"last_execution_id,omitempty"`
	LastResult           string     `json:"last_result,omitempty"`
}

// QuotaDocID returns the pipeline_quota document ID for a user/project pair.
func QuotaDocID(userID, projectID string) string {
	return fmt.Sprintf("%s__%s", userID, projectID)
}

// LoadQuotaState reads the pipeline_quota document (missing → zeros).
func (c *Client) LoadQuotaState(ctx context.Context, userID, projectID string) (runsToday int, dayKey string, lastRunAt time.Time, err error) {
	doc, err := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID)).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return 0, "", time.Time{}, nil
		}
		return 0, "", time.Time{}, err
	}
	runsToday, dayKey, lastRunAt = quotaStateFromData(doc.Data())
	return runsToday, dayKey, lastRunAt, nil
}

// PipelineAdmissionActive reads the transactional claim shared by every run trigger.
func (c *Client) PipelineAdmissionActive(ctx context.Context, userID, projectID string) (bool, error) {
	doc, err := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	claim, _ := doc.Data()["active_reservation_id"].(string)
	return strings.TrimSpace(claim) != "", nil
}

func ScheduledPipelineBudgetID(userID, projectID, dayKey string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + projectID + "\x00" + dayKey))
	return "sd-" + hex.EncodeToString(sum[:])
}

func (c *Client) RecordScheduledPipelineCheck(ctx context.Context, status ScheduledPipelineStatus) error {
	statusRef := Collection(c.fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(status.UserID, status.ProjectID))
	_, err := statusRef.Set(ctx, map[string]interface{}{
		"user_id": status.UserID, "project_id": status.ProjectID,
		"schedule": status.Schedule, "timezone": status.Timezone,
		"last_check_at": status.LastCheckAt, "next_check_at": status.NextCheckAt,
		"skip_reason": status.SkipReason, "pending_work": status.PendingWork,
		"new_raw_files": status.NewRawFiles, "raw_dirty_files": status.RawDirtyFiles,
		"annotation_dirty_files": status.AnnotationDirtyFiles, "day_key": status.DayKey,
		"budget_used": status.BudgetUsed, "updated_at": status.LastCheckAt,
	}, firestore.MergeAll)
	return err
}

func (c *Client) RecordScheduledPipelineAttempt(ctx context.Context, userID, projectID, dayKey, reservationID, executionID, result string, attemptedAt time.Time) error {
	statusRef := Collection(c.fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(userID, projectID))
	_, err := statusRef.Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "day_key": dayKey, "budget_used": result != "not_invoked",
		"last_attempt_at": attemptedAt, "last_attempt_id": reservationID,
		"last_execution_id": executionID, "last_result": result, "updated_at": time.Now().UTC(),
	}, firestore.MergeAll)
	return err
}

func (c *Client) LoadScheduledPipelineStatus(ctx context.Context, userID, projectID string, now time.Time) (ScheduledPipelineStatus, error) {
	result := ScheduledPipelineStatus{Availability: "available", UserID: userID, ProjectID: projectID, DayKey: pipelinequota.DayKeyUTC(now)}
	statusSnapshot, err := Collection(c.fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(userID, projectID)).Get(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return ScheduledPipelineStatus{}, err
	}
	if status.Code(err) != codes.NotFound {
		data := statusSnapshot.Data()
		result.Schedule, _ = data["schedule"].(string)
		result.Timezone, _ = data["timezone"].(string)
		result.SkipReason, _ = data["skip_reason"].(string)
		result.PendingWork = intFromFirestore(data["pending_work"])
		result.NewRawFiles = intFromFirestore(data["new_raw_files"])
		result.RawDirtyFiles = intFromFirestore(data["raw_dirty_files"])
		result.AnnotationDirtyFiles = intFromFirestore(data["annotation_dirty_files"])
		result.LastAttemptID, _ = data["last_attempt_id"].(string)
		result.LastExecutionID, _ = data["last_execution_id"].(string)
		result.LastResult, _ = data["last_result"].(string)
		if value, ok := firestoreTimestamp(data["last_check_at"]); ok {
			result.LastCheckAt = &value
		}
		if value, ok := firestoreTimestamp(data["next_check_at"]); ok {
			result.NextCheckAt = &value
		}
		if value, ok := firestoreTimestamp(data["last_attempt_at"]); ok {
			result.LastAttemptAt = &value
		}
	}
	budgetSnapshot, err := Collection(c.fs, pipelineScheduledDailyCollection).Doc(ScheduledPipelineBudgetID(userID, projectID, result.DayKey)).Get(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return ScheduledPipelineStatus{}, err
	}
	if status.Code(err) != codes.NotFound {
		result.BudgetUsed, _ = budgetSnapshot.Data()["used"].(bool)
	}
	return result, nil
}

// ReserveQuota evaluates quota and creates the execution-owned reservation in
// one transaction. The reservation ID is generated by the accepting handler
// before this call and is also carried by the backend execution.
func (c *Client) ReserveQuota(
	ctx context.Context,
	reservationID, userID, projectID string,
	limits pipelinequota.Limits,
	now time.Time,
	mode pipelinequota.ExecutionMode,
	isDemo, alreadyRunning bool,
	newRawFiles, rawDirtyFiles, annotationDirtyFiles int,
) (snap pipelinequota.Snapshot, reserved bool, err error) {
	return c.reservePipelineQuota(ctx, reservationID, userID, projectID, limits, now, mode,
		pipelinequota.TriggerManual, "manual", "", isDemo, alreadyRunning,
		newRawFiles, rawDirtyFiles, annotationDirtyFiles)
}

// ReserveScheduledQuota reserves the independent once-per-UTC-day scheduled
// entitlement and the shared Project execution claim in one transaction.
func (c *Client) ReserveScheduledQuota(
	ctx context.Context,
	reservationID, userID, projectID string,
	limits pipelinequota.Limits,
	now time.Time,
	mode pipelinequota.ExecutionMode,
	isDemo, alreadyRunning bool,
	newRawFiles, rawDirtyFiles, annotationDirtyFiles int,
) (snap pipelinequota.Snapshot, reserved bool, err error) {
	attemptID, err := newQuotaReservationAttemptID()
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	return c.ReserveScheduledQuotaWithAttempt(ctx, reservationID, attemptID, userID, projectID, limits, now,
		mode, isDemo, alreadyRunning, newRawFiles, rawDirtyFiles, annotationDirtyFiles)
}

// ReserveScheduledQuotaWithAttempt stores a fresh invocation incarnation beside
// the stable Project/day reservation ID so stale recovery evidence cannot own a
// later same-day attempt.
func (c *Client) ReserveScheduledQuotaWithAttempt(
	ctx context.Context,
	reservationID, attemptID, userID, projectID string,
	limits pipelinequota.Limits,
	now time.Time,
	mode pipelinequota.ExecutionMode,
	isDemo, alreadyRunning bool,
	newRawFiles, rawDirtyFiles, annotationDirtyFiles int,
) (snap pipelinequota.Snapshot, reserved bool, err error) {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return pipelinequota.Snapshot{}, false, errors.New("invalid pipeline quota attempt ID")
	}
	return c.reservePipelineQuota(ctx, reservationID, userID, projectID, limits, now, mode,
		pipelinequota.TriggerScheduledDaily, "scheduled_daily", attemptID, isDemo, alreadyRunning,
		newRawFiles, rawDirtyFiles, annotationDirtyFiles)
}

func (c *Client) reservePipelineQuota(
	ctx context.Context,
	reservationID, userID, projectID string,
	limits pipelinequota.Limits,
	now time.Time,
	mode pipelinequota.ExecutionMode,
	trigger pipelinequota.Trigger,
	budgetClass string,
	attemptID string,
	isDemo, alreadyRunning bool,
	newRawFiles, rawDirtyFiles, annotationDirtyFiles int,
) (snap pipelinequota.Snapshot, reserved bool, err error) {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.ContainsAny(reservationID, "/\\") {
		return pipelinequota.Snapshot{}, false, errors.New("invalid pipeline quota reservation ID")
	}
	now = now.UTC().Truncate(time.Microsecond)
	quotaRef := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID))
	reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
	budgetRef := Collection(c.fs, pipelineScheduledDailyCollection).Doc(reservationID)

	err = c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap = pipelinequota.Snapshot{}
		reserved = false
		transactionAlreadyRunning := alreadyRunning
		quotaData, readErr := readQuotaDataTx(tx, quotaRef)
		if readErr != nil {
			return readErr
		}
		runsToday, dayKey, lastRunAt := quotaStateFromData(quotaData)
		lastRunAt = normalizeQuotaTime(lastRunAt)
		activeReservationID, _ := quotaData["active_reservation_id"].(string)
		activeReservationID = strings.TrimSpace(activeReservationID)
		if activeReservationID != "" {
			transactionAlreadyRunning = true
		}
		scheduledUsed := false
		var existingBudget *firestore.DocumentSnapshot
		if trigger == pipelinequota.TriggerScheduledDaily {
			var budgetErr error
			existingBudget, budgetErr = tx.Get(budgetRef)
			if budgetErr != nil && status.Code(budgetErr) != codes.NotFound {
				return budgetErr
			}
			if existingBudget != nil && existingBudget.Exists() {
				scheduledUsed, _ = existingBudget.Data()["used"].(bool)
			}
		}

		pre := pipelinequota.Evaluate(pipelinequota.Input{
			Now: now, Limits: limits, IsDemo: isDemo, AlreadyRunning: transactionAlreadyRunning,
			Mode: mode, Trigger: trigger, ScheduledUsed: scheduledUsed,
			RunsToday: runsToday, DayKey: dayKey, LastRunAt: lastRunAt,
			NewRawFiles: newRawFiles, RawDirtyFiles: rawDirtyFiles,
			AnnotationDirtyFiles: annotationDirtyFiles, Enforced: true,
		})
		if !pre.Allowed {
			snap = pre
			return nil
		}

		today := pipelinequota.DayKeyUTC(now)
		newRuns := pre.RunsToday
		quotaUpdates := map[string]interface{}{
			"user_id": userID, "project_id": projectID,
			"active_reservation_id": reservationID, "active_trigger": string(trigger), "updated_at": now,
		}
		if budgetClass == "manual" {
			newRuns++
			quotaUpdates["day_key"] = today
			quotaUpdates["runs_today"] = newRuns
		}
		quotaUpdates["last_run_at"] = now
		if err := tx.Set(quotaRef, quotaUpdates, firestore.MergeAll); err != nil {
			return err
		}
		reservationData := map[string]interface{}{
			"user_id": userID, "project_id": projectID,
			"reserved_at": now, "day_key": today,
			"previous_last_run_at": lastRunAt,
			"trigger":              string(trigger), "budget_class": budgetClass,
			"settlement_status": "pending", "outcome": "",
			"execution_id": "", "created_at": now,
		}
		if trigger == pipelinequota.TriggerScheduledDaily {
			reservationData["attempt_id"] = attemptID
		}
		if trigger == pipelinequota.TriggerScheduledDaily && existingBudget != nil && existingBudget.Exists() {
			if err := tx.Set(reservationRef, reservationData); err != nil {
				return err
			}
		} else if err := tx.Create(reservationRef, reservationData); err != nil {
			return err
		}
		if trigger == pipelinequota.TriggerScheduledDaily {
			if err := tx.Set(budgetRef, map[string]interface{}{
				"user_id": userID, "project_id": projectID, "day_key": today,
				"reservation_id": reservationID, "used": true, "result": "pending",
				"execution_id": "", "updated_at": now,
			}, firestore.MergeAll); err != nil {
				return err
			}
		}

		snap = pipelinequota.Evaluate(pipelinequota.Input{
			Now: now, Limits: limits, IsDemo: isDemo, AlreadyRunning: transactionAlreadyRunning,
			Mode: mode, Trigger: trigger, ScheduledUsed: trigger == pipelinequota.TriggerScheduledDaily,
			RunsToday: newRuns, DayKey: today, LastRunAt: now,
			NewRawFiles: newRawFiles, RawDirtyFiles: rawDirtyFiles,
			AnnotationDirtyFiles: annotationDirtyFiles, Enforced: true,
		})
		reserved = true
		return nil
	})
	if err != nil {
		return pipelinequota.Snapshot{}, false, err
	}
	return snap, reserved, nil
}

// LinkQuotaReservation records the Cloud Run execution ID after the provider
// accepts the job. The reservation ID is also in the job environment so a
// reconciler can recover if this write is temporarily unavailable.
func (c *Client) LinkQuotaReservation(ctx context.Context, reservationID, executionID string) error {
	reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
	now := time.Now().UTC()
	return c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(reservationRef)
		if err != nil {
			return err
		}
		reservation := quotaReservationFromData(reservationID, snapshot.Data())
		if reservation.SettlementStatus == "released" || reservation.UserID == "" || reservation.ProjectID == "" {
			return errors.New("pipeline reservation cannot be linked")
		}
		if reservation.ExecutionID != "" && reservation.ExecutionID != executionID {
			return errors.New("pipeline reservation execution link conflicts")
		}
		if err := tx.Update(reservationRef, []firestore.Update{
			{Path: "execution_id", Value: executionID},
			{Path: "updated_at", Value: now},
		}); err != nil {
			return err
		}
		if reservation.Trigger == pipelinequota.TriggerScheduledDaily {
			budgetRef := Collection(c.fs, pipelineScheduledDailyCollection).Doc(reservationID)
			if err := tx.Set(budgetRef, map[string]interface{}{"execution_id": executionID, "updated_at": now}, firestore.MergeAll); err != nil {
				return err
			}
			statusRef := Collection(c.fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(reservation.UserID, reservation.ProjectID))
			if err := tx.Set(statusRef, map[string]interface{}{"last_execution_id": executionID, "updated_at": now}, firestore.MergeAll); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReserveAdminPipelineAdmission claims the same Project slot as manual and
// scheduled runs without touching either user's daily budget.
func (c *Client) ReserveAdminPipelineAdmission(ctx context.Context, reservationID, userID, projectID string, now time.Time, alreadyRunning bool) (bool, error) {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.ContainsAny(reservationID, "/\\") {
		return false, errors.New("invalid pipeline quota reservation ID")
	}
	now = now.UTC().Truncate(time.Microsecond)
	quotaRef := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID))
	reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
	reserved := false
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reserved = false
		data, err := readQuotaDataTx(tx, quotaRef)
		if err != nil {
			return err
		}
		active, _ := data["active_reservation_id"].(string)
		if alreadyRunning || strings.TrimSpace(active) != "" {
			return nil
		}
		if err := tx.Set(quotaRef, map[string]interface{}{
			"user_id": userID, "project_id": projectID,
			"active_reservation_id": reservationID, "active_trigger": string(pipelinequota.TriggerAdmin),
			"updated_at": now,
		}, firestore.MergeAll); err != nil {
			return err
		}
		if err := tx.Create(reservationRef, map[string]interface{}{
			"user_id": userID, "project_id": projectID, "reserved_at": now,
			"day_key": pipelinequota.DayKeyUTC(now), "trigger": string(pipelinequota.TriggerAdmin),
			"budget_class": "none", "settlement_status": "pending", "outcome": "",
			"execution_id": "", "created_at": now,
		}); err != nil {
			return err
		}
		reserved = true
		return nil
	})
	return reserved, err
}

// ReleaseQuotaReservation records proof that no provider invocation was
// accepted, returning its manual or scheduled budget and shared Project claim.
func (c *Client) ReleaseQuotaReservation(ctx context.Context, reservationID string) error {
	return c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		_, err := releaseQuotaReservationInTransaction(ctx, tx, c.fs, reservationID, "")
		return err
	})
}

// ReleaseQuotaReservationIfAttempt releases only the exact reservation
// incarnation observed by the caller. A delayed reconciler for an earlier
// same-day attempt becomes a no-op after the stable reservation ID is reused.
func (c *Client) ReleaseQuotaReservationIfAttempt(ctx context.Context, reservationID, attemptID string) (bool, error) {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return false, errors.New("pipeline quota attempt ID is required")
	}
	released := false
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		released, err = releaseQuotaReservationInTransaction(ctx, tx, c.fs, reservationID, attemptID)
		return err
	})
	return released, err
}

func ReleaseQuotaReservationInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID string) error {
	_, err := releaseQuotaReservationInTransaction(ctx, tx, fs, reservationID, "")
	return err
}

func releaseQuotaReservationInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, expectedAttemptID string) (bool, error) {
	reservationRef := Collection(fs, pipelineQuotaReservationCollection).Doc(reservationID)
	reservationSnapshot, err := tx.Get(reservationRef)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	reservation := quotaReservationFromData(reservationID, reservationSnapshot.Data())
	if expectedAttemptID != "" && reservation.AttemptID != expectedAttemptID {
		return false, nil
	}
	if reservation.SettlementStatus != "pending" || reservation.ExecutionID != "" {
		return false, errors.New("pipeline reservation is not confirmed uninvoked")
	}
	now := time.Now().UTC()
	quotaRef := Collection(fs, pipelineQuotaCollection).Doc(QuotaDocID(reservation.UserID, reservation.ProjectID))
	quotaData, err := readQuotaDataTx(tx, quotaRef)
	if err != nil {
		return false, err
	}
	reservationUpdates := []firestore.Update{
		{Path: "settlement_status", Value: "released"},
		{Path: "outcome", Value: "NOT_INVOKED"},
		{Path: "settled_at", Value: now},
	}
	quotaUpdates := make([]firestore.Update, 0, 6)
	_, _, lastRunAt := quotaStateFromData(quotaData)
	if reservation.BudgetClass == "manual" {
		runsToday, dayKey, _ := quotaStateFromData(quotaData)
		if dayKey == reservation.DayKey && runsToday > 0 {
			quotaUpdates = append(quotaUpdates, firestore.Update{Path: "runs_today", Value: runsToday - 1})
		}
		if sameQuotaTime(lastRunAt, reservation.ReservedAt) {
			if reservation.PreviousLastRunAt.IsZero() {
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "last_run_at", Value: firestore.Delete})
			} else {
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "last_run_at", Value: reservation.PreviousLastRunAt})
			}
		}
	} else if reservation.BudgetClass == "scheduled_daily" {
		budgetRef := Collection(fs, pipelineScheduledDailyCollection).Doc(reservationID)
		if err := tx.Set(budgetRef, map[string]interface{}{"used": false, "result": "not_invoked", "updated_at": now}, firestore.MergeAll); err != nil {
			return false, err
		}
		if sameQuotaTime(lastRunAt, reservation.ReservedAt) {
			// The shared cooldown clock is restored only when no later run has advanced it.
			previous := reservation.PreviousLastRunAt
			if previous.IsZero() {
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "last_run_at", Value: firestore.Delete})
			} else {
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "last_run_at", Value: previous})
			}
		}
	}
	if current, _ := quotaData["active_reservation_id"].(string); current == reservationID {
		quotaUpdates = append(quotaUpdates, firestore.Update{Path: "active_reservation_id", Value: firestore.Delete}, firestore.Update{Path: "active_trigger", Value: firestore.Delete})
	}
	if len(quotaUpdates) > 0 {
		quotaUpdates = append(quotaUpdates, firestore.Update{Path: "updated_at", Value: now})
		if err := tx.Update(quotaRef, quotaUpdates); err != nil {
			return false, err
		}
	}
	if err := tx.Update(reservationRef, reservationUpdates); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Client) GetQuotaReservation(ctx context.Context, reservationID string) (QuotaReservation, bool, error) {
	return GetQuotaReservationFromFirestore(ctx, c.fs, reservationID)
}

func GetQuotaReservationFromFirestore(ctx context.Context, fs *firestore.Client, reservationID string) (QuotaReservation, bool, error) {
	snapshot, err := Collection(fs, pipelineQuotaReservationCollection).Doc(reservationID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return QuotaReservation{}, false, nil
	}
	if err != nil {
		return QuotaReservation{}, false, err
	}
	return quotaReservationFromData(reservationID, snapshot.Data()), true, nil
}

func (c *Client) ListPendingQuotaReservations(ctx context.Context) ([]QuotaReservation, error) {
	return ListPendingQuotaReservationsFromFirestore(ctx, c.fs)
}

func ListPendingQuotaReservationsFromFirestore(ctx context.Context, fs *firestore.Client) ([]QuotaReservation, error) {
	docs, err := Collection(fs, pipelineQuotaReservationCollection).
		Where("settlement_status", "==", "pending").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	reservations := make([]QuotaReservation, 0, len(docs))
	for _, doc := range docs {
		reservations = append(reservations, quotaReservationFromData(doc.Ref.ID, doc.Data()))
	}
	return reservations, nil
}

// SettleQuotaReservation applies a confirmed terminal result. Replays are
// idempotent because the reservation state and any refund share one transaction.
func (c *Client) SettleQuotaReservation(ctx context.Context, reservationID, executionStatus string) (string, error) {
	ctx, span := observability.StartPipelineSpan(ctx, "pipeline.quota.settle")
	var settlement string
	var applied bool
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		settlement, applied, err = SettleQuotaReservationInTransactionWithResult(ctx, tx, c.fs, reservationID, executionStatus)
		return err
	})
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	observability.SetPipelineSettlementOutcome(span, settlement, applied, err)
	observability.FinishPipelineSpan(span, outcome)
	return settlement, err
}

// SettleQuotaReservationInTransaction lets the local execution writer commit
// terminal execution state and its quota settlement atomically.
func SettleQuotaReservationInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, executionStatus string) (string, error) {
	settlement, _, err := SettleQuotaReservationInTransactionWithResult(ctx, tx, fs, reservationID, executionStatus)
	return settlement, err
}

// SettleQuotaReservationInTransactionWithResult reports whether this closure
// staged a new reservation settlement. Callers must use that result only after
// the outer RunTransaction returns successfully, because Firestore may retry
// this closure.
func SettleQuotaReservationInTransactionWithResult(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, executionStatus string) (string, bool, error) {
	if strings.TrimSpace(reservationID) == "" {
		return "not_applicable", false, nil
	}
	reservationRef := Collection(fs, pipelineQuotaReservationCollection).Doc(reservationID)
	reservationSnapshot, err := tx.Get(reservationRef)
	if status.Code(err) == codes.NotFound {
		return "not_applicable", false, nil
	}
	if err != nil {
		return "", false, err
	}
	reservation := quotaReservationFromData(reservationID, reservationSnapshot.Data())
	if reservation.SettlementStatus != "pending" {
		return reservation.SettlementStatus, false, nil
	}
	quotaRef := Collection(fs, pipelineQuotaCollection).Doc(QuotaDocID(reservation.UserID, reservation.ProjectID))
	quotaData, err := readQuotaDataTx(tx, quotaRef)
	if err != nil {
		return "", false, err
	}
	budgetRef := Collection(fs, pipelineScheduledDailyCollection).Doc(reservationID)
	if reservation.BudgetClass == "scheduled_daily" {
		if _, err := tx.Get(budgetRef); err != nil {
			return "", false, err
		}
	}

	state := strings.ToUpper(strings.TrimSpace(executionStatus))
	settlement := "not_applicable"
	quotaUpdates := make([]firestore.Update, 0, 4)
	switch state {
	case "SUCCEEDED", "CANCELLED":
		if reservation.BudgetClass != "none" {
			settlement = "charged"
		}
	case "FAILED":
		if reservation.BudgetClass == "manual" {
			settlement = "refunded"
			runsToday, dayKey, lastRunAt := quotaStateFromData(quotaData)
			if dayKey == reservation.DayKey && runsToday > 0 {
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "runs_today", Value: runsToday - 1})
			}
			if sameQuotaTime(lastRunAt, reservation.ReservedAt) {
				var value interface{} = reservation.PreviousLastRunAt
				if reservation.PreviousLastRunAt.IsZero() {
					value = firestore.Delete
				}
				quotaUpdates = append(quotaUpdates, firestore.Update{Path: "last_run_at", Value: value})
			}
		}
		if reservation.BudgetClass == "scheduled_daily" {
			settlement = "charged"
		}
	default:
		return "pending", false, nil
	}

	now := time.Now().UTC()
	reservationUpdates := []firestore.Update{
		{Path: "settlement_status", Value: settlement},
		{Path: "outcome", Value: state},
		{Path: "settled_at", Value: now},
	}
	if err := tx.Update(reservationRef, reservationUpdates); err != nil {
		return "", false, err
	}
	if current, _ := quotaData["active_reservation_id"].(string); current == reservationID {
		quotaUpdates = append(quotaUpdates, firestore.Update{Path: "active_reservation_id", Value: firestore.Delete})
		quotaUpdates = append(quotaUpdates, firestore.Update{Path: "active_trigger", Value: firestore.Delete})
	}
	if len(quotaUpdates) > 0 {
		quotaUpdates = append(quotaUpdates, firestore.Update{Path: "updated_at", Value: now})
		if err := tx.Update(quotaRef, quotaUpdates); err != nil {
			return "", false, err
		}
	}
	if reservation.BudgetClass == "scheduled_daily" {
		if err := tx.Set(budgetRef, map[string]interface{}{
			"used": true, "result": state, "execution_id": reservation.ExecutionID, "updated_at": now,
		}, firestore.MergeAll); err != nil {
			return "", false, err
		}
		statusRef := Collection(fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(reservation.UserID, reservation.ProjectID))
		if err := tx.Set(statusRef, map[string]interface{}{
			"last_attempt_at": reservation.ReservedAt, "last_attempt_id": reservation.ID,
			"last_execution_id": reservation.ExecutionID, "last_result": state, "updated_at": now,
		}, firestore.MergeAll); err != nil {
			return "", false, err
		}
	}
	return settlement, true, nil
}

func quotaReservationFromData(id string, data map[string]interface{}) QuotaReservation {
	reservation := QuotaReservation{ID: id, SettlementStatus: "pending"}
	reservation.UserID, _ = data["user_id"].(string)
	reservation.AttemptID, _ = data["attempt_id"].(string)
	reservation.ProjectID, _ = data["project_id"].(string)
	reservation.ExecutionID, _ = data["execution_id"].(string)
	reservation.DayKey, _ = data["day_key"].(string)
	reservation.SettlementStatus, _ = data["settlement_status"].(string)
	reservation.Outcome, _ = data["outcome"].(string)
	trigger, _ := data["trigger"].(string)
	reservation.Trigger = pipelinequota.Trigger(trigger)
	reservation.BudgetClass, _ = data["budget_class"].(string)
	if reservation.Trigger == "" {
		reservation.Trigger = pipelinequota.TriggerManual
	}
	if reservation.BudgetClass == "" {
		reservation.BudgetClass = "manual"
	}
	if t, ok := firestoreTimestamp(data["reserved_at"]); ok {
		reservation.ReservedAt = normalizeQuotaTime(t)
	}
	if t, ok := firestoreTimestamp(data["previous_last_run_at"]); ok {
		reservation.PreviousLastRunAt = normalizeQuotaTime(t)
	}
	return reservation
}

func newQuotaReservationAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "pa-" + hex.EncodeToString(raw[:]), nil
}

func sameQuotaTime(a, b time.Time) bool {
	return !a.IsZero() && !b.IsZero() && normalizeQuotaTime(a).Equal(normalizeQuotaTime(b))
}

func normalizeQuotaTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Microsecond)
}

func readQuotaStateTx(tx *firestore.Transaction, ref *firestore.DocumentRef) (runsToday int, dayKey string, lastRunAt time.Time, err error) {
	data, err := readQuotaDataTx(tx, ref)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	runsToday, dayKey, lastRunAt = quotaStateFromData(data)
	return runsToday, dayKey, lastRunAt, nil
}

func readQuotaDataTx(tx *firestore.Transaction, ref *firestore.DocumentRef) (map[string]interface{}, error) {
	snap, err := tx.Get(ref)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snap.Data(), nil
}

// quotaStateFromData extracts quota fields from a pipeline_quota document.
func quotaStateFromData(data map[string]interface{}) (runsToday int, dayKey string, lastRunAt time.Time) {
	if data == nil {
		return 0, "", time.Time{}
	}
	runsToday = intFromFirestore(data["runs_today"])
	if v, ok := data["day_key"].(string); ok {
		dayKey = v
	}
	if t, ok := firestoreTimestamp(data["last_run_at"]); ok {
		lastRunAt = t
	}
	return runsToday, dayKey, lastRunAt
}

func intFromFirestore(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}
