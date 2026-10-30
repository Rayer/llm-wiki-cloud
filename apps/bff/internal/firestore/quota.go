package firestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
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
	ID                             string
	AttemptID                      string
	UserID                         string
	ProjectID                      string
	ExecutionID                    string
	ReservedAt                     time.Time
	PreviousLastRunAt              time.Time
	DayKey                         string
	Trigger                        pipelinequota.Trigger
	BudgetClass                    string
	SettlementStatus               string
	Outcome                        string
	IntentStatus                   string
	CompileMode                    pipelinequota.ExecutionMode
	ProfileRevision                int64
	ProfileRequirements            []PipelineProfileRequirement
	RequirementsDigest             string
	ConsumedGuidanceKind           string
	ConsumedGuidanceStatus         string
	ConsumedGuidanceRevision       string
	ConsumedGuidanceSourceRevision int64
	ConsumedGuidanceInputDigest    string
	ConsumedGuidanceModelVersion   string
	ConsumedGuidancePromptVersion  string
	ConsumedGuidanceSchemaVersion  string
	ConsumedActiveGuidance         *profileartifacts.DerivedRef
	ConsumedBootstrapGuidance      *profileartifacts.BootstrapGuidanceRef
	ActiveCandidateID              string
	ActiveContentGeneration        string
	SourceFingerprint              string
	SourceRevision                 int64
	AutomationProjectRevision      int64
	AutomationAdminRevision        int64
	IntentErrorStage               string
	IntentErrorCode                string
	IntentErrorMessage             string
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// PipelineAutomationAdmission binds an automatic run to the committed source
// inventory it is expected to consume. The two switches and failure fence are
// rechecked transactionally by ReserveQuotaForPipelineAutomation.
type PipelineAutomationAdmission struct {
	SourceFingerprint string
	SourceRevision    int64
	ProjectRevision   int64
	AdminRevision     int64
}

// PipelineProfileRequirement is the saved Profile input bound to one accepted run.
type PipelineProfileRequirement struct {
	ID   string `firestore:"id" json:"id"`
	Text string `firestore:"text" json:"text"`
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

// PipelineEventCandidate is a Project-scoped durable intent identity that can
// supplement a bounded Cloud Run execution-list scan.
type PipelineEventCandidate struct {
	ReservationID string
	AttemptID     string
	ExecutionID   string
	Trigger       pipelinequota.Trigger
	IntentStatus  string
}

// PipelineEventCandidateSet contains at most the active intent, last accepted
// intent, and last scheduled attempt. Unresolved counts preserve coverage when
// a scoped pointer cannot be validated without exposing foreign data.
type PipelineEventCandidateSet struct {
	Known           []PipelineEventCandidate
	Pending         []PipelineEventCandidate
	UnresolvedCount int
}

// LoadPipelineEventCandidates reads only the authenticated user's quota and
// scheduled status documents, then validates each pointed reservation before
// returning its execution identity.
func LoadPipelineEventCandidates(ctx context.Context, fs *firestore.Client, userID, projectID string) (PipelineEventCandidateSet, error) {
	result := PipelineEventCandidateSet{}
	quotaSnapshot, err := Collection(fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID)).Get(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return result, err
	}
	reservationIDs := make([]string, 0, 2)
	if status.Code(err) != codes.NotFound {
		data := quotaSnapshot.Data()
		reservationIDs = append(reservationIDs,
			strings.TrimSpace(stringFromData(data, "active_reservation_id")),
			strings.TrimSpace(stringFromData(data, "last_pipeline_intent_id")),
		)
	}
	seenReservations := make(map[string]struct{}, 3)
	seenExecutions := make(map[string]struct{}, 3)
	addReservation := func(reservationID, requiredAttempt string, requiredTrigger pipelinequota.Trigger, requiredExecution string) (PipelineEventCandidate, bool, error) {
		if reservationID == "" {
			return PipelineEventCandidate{}, false, nil
		}
		_, duplicate := seenReservations[reservationID]
		reservation, exists, err := GetQuotaReservationFromFirestore(ctx, fs, reservationID)
		if err != nil {
			return PipelineEventCandidate{}, false, err
		}
		if !exists || reservation.UserID != userID || reservation.ProjectID != projectID || reservation.IntentStatus == "" ||
			(requiredAttempt != "" && reservation.AttemptID != requiredAttempt) ||
			(requiredTrigger != "" && reservation.Trigger != requiredTrigger) ||
			(requiredExecution != "" && reservation.ExecutionID != requiredExecution) {
			result.UnresolvedCount++
			return PipelineEventCandidate{}, false, nil
		}
		candidate := PipelineEventCandidate{
			ReservationID: reservation.ID, AttemptID: reservation.AttemptID,
			ExecutionID: reservation.ExecutionID, Trigger: reservation.Trigger,
			IntentStatus: reservation.IntentStatus,
		}
		if duplicate {
			return candidate, false, nil
		}
		seenReservations[reservationID] = struct{}{}
		return candidate, true, nil
	}
	for _, reservationID := range reservationIDs {
		candidate, valid, err := addReservation(reservationID, "", "", "")
		if err != nil {
			return result, err
		}
		if !valid {
			continue
		}
		if candidate.ExecutionID == "" {
			result.Pending = append(result.Pending, candidate)
			continue
		}
		if _, duplicate := seenExecutions[candidate.ExecutionID]; !duplicate {
			seenExecutions[candidate.ExecutionID] = struct{}{}
			result.Known = append(result.Known, candidate)
		}
	}

	scheduledSnapshot, err := Collection(fs, pipelineScheduledStatusCollection).Doc(QuotaDocID(userID, projectID)).Get(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return result, err
	}
	if status.Code(err) != codes.NotFound {
		data := scheduledSnapshot.Data()
		attemptID := strings.TrimSpace(stringFromData(data, "last_attempt_id"))
		executionID := strings.TrimSpace(stringFromData(data, "last_execution_id"))
		if attemptID != "" {
			if data["user_id"] != userID || data["project_id"] != projectID {
				result.UnresolvedCount++
			} else {
				candidate, valid, err := addReservation(attemptID, attemptID, pipelinequota.TriggerScheduledDaily, executionID)
				if err != nil {
					return result, err
				}
				if valid && candidate.ExecutionID != executionID {
					result.UnresolvedCount++
					valid = false
				}
				if valid {
					if candidate.ExecutionID == "" {
						result.Pending = append(result.Pending, candidate)
					} else if _, duplicate := seenExecutions[candidate.ExecutionID]; !duplicate {
						seenExecutions[candidate.ExecutionID] = struct{}{}
						result.Known = append(result.Known, candidate)
					}
				}
			}
		}
	}
	return result, nil
}

func stringFromData(data map[string]interface{}, key string) string {
	value, _ := data[key].(string)
	return value
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
		newRawFiles, rawDirtyFiles, annotationDirtyFiles, false, "", nil)
}

// ReserveQuotaForPipelineIntent freezes the saved Profile and active guidance
// inside the same transaction that reserves quota and the shared Project claim.
func (c *Client) ReserveQuotaForPipelineIntent(
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
		newRawFiles, rawDirtyFiles, annotationDirtyFiles, true, "", nil)
}

// ReserveQuotaForPipelineIntentRetry creates a new quota attempt from the exact
// saved Profile and guidance references of a prior failed intent.
func (c *Client) ReserveQuotaForPipelineIntentRetry(ctx context.Context, reservationID, sourceReservationID, userID, projectID string, limits pipelinequota.Limits, now time.Time, mode pipelinequota.ExecutionMode, isDemo, alreadyRunning bool, newRawFiles, rawDirtyFiles, annotationDirtyFiles int) (pipelinequota.Snapshot, bool, error) {
	return c.reservePipelineQuota(ctx, reservationID, userID, projectID, limits, now, mode,
		pipelinequota.TriggerManual, "manual", "", isDemo, alreadyRunning,
		newRawFiles, rawDirtyFiles, annotationDirtyFiles, true, sourceReservationID, nil)
}

func (c *Client) ReserveQuotaForPipelineAutomation(ctx context.Context, reservationID, userID, projectID string, limits pipelinequota.Limits, now time.Time, isDemo, alreadyRunning bool, newRawFiles, rawDirtyFiles, annotationDirtyFiles int, admission PipelineAutomationAdmission) (pipelinequota.Snapshot, bool, error) {
	return c.reservePipelineQuota(ctx, reservationID, userID, projectID, limits, now, pipelinequota.ModeIncremental,
		pipelinequota.TriggerSourceCommit, "none", "", isDemo, alreadyRunning, newRawFiles, rawDirtyFiles, annotationDirtyFiles, true, "", &admission)
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
		newRawFiles, rawDirtyFiles, annotationDirtyFiles, false, "", nil)
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
	captureProfileIntent bool,
	retryReservationID string,
	automationAdmission *PipelineAutomationAdmission,
) (snap pipelinequota.Snapshot, reserved bool, err error) {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.ContainsAny(reservationID, "/\\") {
		return pipelinequota.Snapshot{}, false, errors.New("invalid pipeline quota reservation ID")
	}
	if retryReservationID != "" && (retryReservationID == reservationID || strings.ContainsAny(retryReservationID, "/\\")) {
		return pipelinequota.Snapshot{}, false, errors.New("invalid Pipeline retry source ID")
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
		var automationProjectRef *firestore.DocumentRef
		var automationProjectData map[string]interface{}
		var automationProjectRevision, automationAdminRevision, automationSourceRevision int64
		if automationAdmission != nil {
			automationProjectRef = Collection(c.fs, "projects").Doc(userID + "_" + projectID)
			projectSnapshot, projectErr := tx.Get(automationProjectRef)
			if status.Code(projectErr) == codes.NotFound {
				return nil
			}
			if projectErr != nil {
				return projectErr
			}
			adminRef := Collection(c.fs, "system").Doc("pipeline_automation")
			adminSnapshot, adminErr := tx.Get(adminRef)
			if adminErr != nil && status.Code(adminErr) != codes.NotFound {
				return adminErr
			}
			automationProjectData = projectSnapshot.Data()
			if automationProjectData["user_id"] != userID || automationProjectData["project_id"] != projectID || automationProjectData["status"] != "ready" {
				return nil
			}
			projectEnabled, _ := automationProjectData["auto_pipeline_enabled"].(bool)
			adminEnabled := false
			if adminSnapshot != nil && adminSnapshot.Exists() {
				adminData := adminSnapshot.Data()
				adminEnabled, _ = adminData["allow_auto_pipeline"].(bool)
				automationAdminRevision = int64FromFirestore(adminData["revision"])
			}
			if !projectEnabled || !adminEnabled || strings.TrimSpace(automationAdmission.SourceFingerprint) == "" ||
				(automationProjectData["pipeline_failure_blocked_fingerprint"] == automationAdmission.SourceFingerprint &&
					int64FromFirestore(automationProjectData["pipeline_failure_blocked_admin_revision"]) == automationAdminRevision) ||
				automationProjectData["pipeline_auto_consumed_fingerprint"] == automationAdmission.SourceFingerprint {
				return nil
			}
			automationProjectRevision = int64FromFirestore(automationProjectData["auto_pipeline_revision"])
			automationSourceRevision = int64FromFirestore(automationProjectData["pipeline_source_revision"])
			if automationProjectRevision != automationAdmission.ProjectRevision ||
				automationAdminRevision != automationAdmission.AdminRevision ||
				automationSourceRevision != automationAdmission.SourceRevision {
				return nil
			}
			if newRawFiles+rawDirtyFiles == 0 {
				return nil
			}
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
		var intent pipelineProfileIntentSnapshot
		if captureProfileIntent {
			if retryReservationID != "" {
				priorRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(retryReservationID)
				priorSnapshot, priorErr := tx.Get(priorRef)
				if priorErr != nil {
					return priorErr
				}
				prior := quotaReservationFromData(retryReservationID, priorSnapshot.Data())
				if prior.UserID != userID || prior.ProjectID != projectID || prior.IntentStatus != "failed" || prior.CompileMode != mode ||
					prior.SettlementStatus == "pending" || prior.AttemptID != retryReservationID {
					return errors.New("Pipeline retry source is not an owned failed intent")
				}
				intent = pipelineProfileIntentSnapshot{
					Status: "ready", ProfileRevision: prior.ProfileRevision, Requirements: prior.ProfileRequirements,
					RequirementsDigest: prior.RequirementsDigest, GuidanceKind: prior.ConsumedGuidanceKind,
					GuidanceStatus: prior.ConsumedGuidanceStatus, GuidanceRevision: prior.ConsumedGuidanceRevision,
					GuidanceSourceRevision: prior.ConsumedGuidanceSourceRevision, GuidanceInputDigest: prior.ConsumedGuidanceInputDigest,
					GuidanceModelVersion: prior.ConsumedGuidanceModelVersion, GuidancePromptVersion: prior.ConsumedGuidancePromptVersion,
					GuidanceSchemaVersion: prior.ConsumedGuidanceSchemaVersion, ActiveGuidance: prior.ConsumedActiveGuidance,
					BootstrapGuidance: prior.ConsumedBootstrapGuidance, ActiveCandidateID: prior.ActiveCandidateID,
					ActiveContentGeneration: prior.ActiveContentGeneration,
				}
				if intent.Requirements == nil {
					intent.Requirements = []PipelineProfileRequirement{}
				}
				if len(intent.Requirements) > 0 && intent.GuidanceKind == "" {
					intent.Status = "preparing"
				}
			} else {
				intent, readErr = readPipelineProfileIntentSnapshot(ctx, tx, c.fs, userID, projectID)
			}
			if readErr != nil {
				return readErr
			}
			intent = bindFullModeGuidanceToRequirements(intent, mode)
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
		if budgetClass != "none" {
			quotaUpdates["last_run_at"] = now
		}
		if captureProfileIntent {
			quotaUpdates["last_pipeline_intent_id"] = reservationID
		}
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
		if captureProfileIntent {
			reservationData["attempt_id"] = reservationID
			reservationData["intent_status"] = intent.Status
			reservationData["intent_mode"] = string(mode)
			reservationData["profile_revision"] = intent.ProfileRevision
			reservationData["profile_requirements"] = intent.Requirements
			reservationData["requirements_digest"] = intent.RequirementsDigest
			reservationData["consumed_guidance_kind"] = intent.GuidanceKind
			reservationData["consumed_guidance_status"] = intent.GuidanceStatus
			reservationData["consumed_guidance_revision"] = intent.GuidanceRevision
			reservationData["consumed_guidance_source_revision"] = intent.GuidanceSourceRevision
			reservationData["consumed_guidance_input_digest"] = intent.GuidanceInputDigest
			reservationData["consumed_guidance_model_version"] = intent.GuidanceModelVersion
			reservationData["consumed_guidance_prompt_version"] = intent.GuidancePromptVersion
			reservationData["consumed_guidance_schema_version"] = intent.GuidanceSchemaVersion
			reservationData["consumed_active_guidance"] = consumedActiveGuidanceWire(intent.ActiveGuidance)
			reservationData["consumed_bootstrap_guidance"] = intent.BootstrapGuidance
			reservationData["active_candidate_id"] = intent.ActiveCandidateID
			reservationData["active_content_generation"] = intent.ActiveContentGeneration
			reservationData["intent_error_stage"] = ""
			reservationData["intent_error_code"] = ""
			reservationData["intent_error_message"] = ""
			reservationData["intent_updated_at"] = now
			reservationData["updated_at"] = now
		}
		if automationAdmission != nil {
			reservationData["source_fingerprint"] = automationAdmission.SourceFingerprint
			reservationData["source_revision"] = automationSourceRevision
			reservationData["auto_pipeline_project_revision"] = automationProjectRevision
			reservationData["auto_pipeline_admin_revision"] = automationAdminRevision
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
		if automationAdmission != nil {
			if err := tx.Update(automationProjectRef, []firestore.Update{
				{Path: "pipeline_automation_status", Value: "preparing"},
				{Path: "pipeline_automation_source_revision", Value: automationSourceRevision},
				{Path: "pipeline_automation_updated_at", Value: now},
				{Path: "pipeline_automation_failure_stage", Value: firestore.Delete},
				{Path: "pipeline_automation_failure_code", Value: firestore.Delete},
			}); err != nil {
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
		if reservation.IntentStatus != "" && reservation.IntentStatus != "failed" && reservation.IntentStatus != "succeeded" {
			if err := tx.Update(reservationRef, []firestore.Update{
				{Path: "intent_status", Value: "compiling"},
				{Path: "intent_updated_at", Value: now},
			}); err != nil {
				return err
			}
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

// ReleaseQuotaReservationIfAttemptInTransaction releases only the exact
// scheduled reservation incarnation observed by a recovery marker.
func ReleaseQuotaReservationIfAttemptInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, attemptID string) (bool, error) {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return false, errors.New("pipeline quota attempt ID is required")
	}
	return releaseQuotaReservationInTransaction(ctx, tx, fs, reservationID, attemptID)
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

// GetPipelineIntent reads one owned run intent. An empty reservationID selects
// the active user intent, then the most recently accepted user intent.
func (c *Client) GetPipelineIntent(ctx context.Context, userID, projectID, reservationID string) (QuotaReservation, bool, error) {
	if strings.TrimSpace(reservationID) == "" {
		quotaRef := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID))
		quotaSnapshot, err := quotaRef.Get(ctx)
		if status.Code(err) == codes.NotFound {
			return QuotaReservation{}, false, nil
		}
		if err != nil {
			return QuotaReservation{}, false, err
		}
		data := quotaSnapshot.Data()
		reservationID, _ = data["active_reservation_id"].(string)
		if reservationID != "" {
			active, exists, activeErr := GetQuotaReservationFromFirestore(ctx, c.fs, reservationID)
			if activeErr != nil {
				return QuotaReservation{}, false, activeErr
			}
			if exists && active.IntentStatus != "" && active.UserID == userID && active.ProjectID == projectID {
				return active, true, nil
			}
		}
		reservationID, _ = data["last_pipeline_intent_id"].(string)
		if reservationID == "" {
			return QuotaReservation{}, false, nil
		}
	}
	reservation, exists, err := GetQuotaReservationFromFirestore(ctx, c.fs, reservationID)
	if err != nil || !exists {
		return reservation, exists, err
	}
	if reservation.IntentStatus == "" || reservation.UserID != userID || reservation.ProjectID != projectID {
		return QuotaReservation{}, false, nil
	}
	return reservation, true, nil
}

// CompletePipelineIntentPreparation binds the immutable bootstrap artifact to
// the accepted snapshot without changing the current Profile pointers.
func (c *Client) CompletePipelineIntentPreparation(ctx context.Context, reservationID, attemptID, userID, projectID string, guidance profileartifacts.BootstrapGuidanceRef) (bool, error) {
	if guidance.ProfileRevision < 1 || guidance.ProfileRevision == 0 || guidance.InputDigest == "" ||
		!profileartifacts.IsSHA256(guidance.Revision) || !profileartifacts.IsSHA256(guidance.InputDigest) ||
		guidance.ModelVersion == "" || guidance.PromptVersion == "" || guidance.SchemaVersion != profileartifacts.BootstrapGuidanceSchema {
		return false, errors.New("invalid run-bound Profile bootstrap reference")
	}
	completed := false
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		completed = false
		reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
		snapshot, err := tx.Get(reservationRef)
		if err != nil {
			return err
		}
		reservation := quotaReservationFromData(reservationID, snapshot.Data())
		if reservation.UserID != userID || reservation.ProjectID != projectID || reservation.AttemptID != attemptID {
			return errors.New("pipeline intent owner or attempt identity mismatch")
		}
		if reservation.SettlementStatus != "pending" || reservation.ExecutionID != "" {
			return errors.New("pipeline intent is no longer preparing")
		}
		if reservation.IntentStatus == "ready" {
			if reservation.ConsumedBootstrapGuidance == nil || *reservation.ConsumedBootstrapGuidance != guidance {
				return errors.New("pipeline intent guidance reference conflicts")
			}
			return nil
		}
		if reservation.IntentStatus != "preparing" || reservation.ProfileRevision != guidance.ProfileRevision || reservation.RequirementsDigest != guidance.InputDigest {
			return nil
		}
		if err := tx.Update(reservationRef, []firestore.Update{
			{Path: "intent_status", Value: "ready"},
			{Path: "consumed_guidance_kind", Value: "bootstrap"},
			{Path: "consumed_guidance_status", Value: "prepared"},
			{Path: "consumed_guidance_revision", Value: guidance.Revision},
			{Path: "consumed_guidance_source_revision", Value: guidance.ProfileRevision},
			{Path: "consumed_guidance_input_digest", Value: guidance.InputDigest},
			{Path: "consumed_guidance_model_version", Value: guidance.ModelVersion},
			{Path: "consumed_guidance_prompt_version", Value: guidance.PromptVersion},
			{Path: "consumed_guidance_schema_version", Value: guidance.SchemaVersion},
			{Path: "consumed_bootstrap_guidance", Value: guidance},
			{Path: "intent_updated_at", Value: time.Now().UTC()},
			{Path: "updated_at", Value: time.Now().UTC()},
		}); err != nil {
			return err
		}
		completed = true
		return nil
	})
	return completed, err
}

// BeginPipelineIntentInvocation is the fencing point that allows only one
// continuation to cross the provider boundary for a ready intent.
func (c *Client) BeginPipelineIntentInvocation(ctx context.Context, reservationID, attemptID, userID, projectID string) (QuotaReservation, bool, error) {
	var reservation QuotaReservation
	claimed := false
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reservation = QuotaReservation{}
		claimed = false
		reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
		snapshot, err := tx.Get(reservationRef)
		if err != nil {
			return err
		}
		reservation = quotaReservationFromData(reservationID, snapshot.Data())
		if reservation.UserID != userID || reservation.ProjectID != projectID || reservation.AttemptID != attemptID {
			return errors.New("pipeline intent owner or attempt identity mismatch")
		}
		if reservation.IntentStatus != "ready" || reservation.SettlementStatus != "pending" || reservation.ExecutionID != "" {
			return nil
		}
		var autoProjectRef *firestore.DocumentRef
		var autoProjectSnapshot *firestore.DocumentSnapshot
		autoPauseReason := ""
		automationProjectEnabled, automationAdminEnabled := false, false
		if reservation.Trigger == pipelinequota.TriggerSourceCommit {
			autoProjectRef = Collection(c.fs, "projects").Doc(userID + "_" + projectID)
			var projectErr error
			autoProjectSnapshot, projectErr = tx.Get(autoProjectRef)
			if projectErr != nil {
				return projectErr
			}
			adminSnapshot, adminErr := tx.Get(Collection(c.fs, "system").Doc("pipeline_automation"))
			if adminErr != nil && status.Code(adminErr) != codes.NotFound {
				return adminErr
			}
			projectData := autoProjectSnapshot.Data()
			automationProjectEnabled, _ = projectData["auto_pipeline_enabled"].(bool)
			adminRevision := int64(0)
			if adminSnapshot != nil && adminSnapshot.Exists() {
				automationAdminEnabled, _ = adminSnapshot.Data()["allow_auto_pipeline"].(bool)
				adminRevision = int64FromFirestore(adminSnapshot.Data()["revision"])
			}
			switch {
			case !automationAdminEnabled:
				autoPauseReason = "admin_disabled"
			case !automationProjectEnabled:
				autoPauseReason = "project_disabled"
			case int64FromFirestore(projectData["auto_pipeline_revision"]) != reservation.AutomationProjectRevision || adminRevision != reservation.AutomationAdminRevision:
				autoPauseReason = "switch_revision_changed"
			case int64FromFirestore(projectData["pipeline_source_revision"]) != reservation.SourceRevision:
				autoPauseReason = "source_revision_changed"
			case reservation.SourceFingerprint != "" && projectData["pipeline_failure_blocked_fingerprint"] == reservation.SourceFingerprint &&
				int64FromFirestore(projectData["pipeline_failure_blocked_admin_revision"]) == reservation.AutomationAdminRevision:
				autoPauseReason = "failure_fence"
			}
		}
		quotaRef := Collection(c.fs, pipelineQuotaCollection).Doc(QuotaDocID(userID, projectID))
		quotaData, err := readQuotaDataTx(tx, quotaRef)
		if err != nil {
			return err
		}
		active, _ := quotaData["active_reservation_id"].(string)
		if active != reservationID {
			return nil
		}
		now := time.Now().UTC()
		if autoPauseReason != "" {
			if err := tx.Update(quotaRef, []firestore.Update{
				{Path: "active_reservation_id", Value: firestore.Delete},
				{Path: "active_trigger", Value: firestore.Delete},
				{Path: "updated_at", Value: now},
			}); err != nil {
				return err
			}
			if err := tx.Update(reservationRef, []firestore.Update{
				{Path: "settlement_status", Value: "released"}, {Path: "outcome", Value: "AUTOMATION_PAUSED"},
				{Path: "intent_status", Value: "cancelled"}, {Path: "intent_error_stage", Value: "automation_admission"},
				{Path: "intent_error_code", Value: autoPauseReason}, {Path: "intent_error_message", Value: "Automatic processing was paused before Pipeline start."},
				{Path: "intent_updated_at", Value: now}, {Path: "settled_at", Value: now}, {Path: "updated_at", Value: now},
			}); err != nil {
				return err
			}
			if autoProjectRef != nil {
				statusValue := "paused"
				if autoPauseReason == "source_revision_changed" && automationProjectEnabled && automationAdminEnabled {
					currentRevision := int64FromFirestore(autoProjectSnapshot.Data()["pipeline_source_revision"])
					if err := enqueuePipelineAutomationWork(tx, c.fs, userID, projectID, currentRevision, now); err != nil {
						return err
					}
					statusValue = "pending"
				}
				if err := tx.Update(autoProjectRef, []firestore.Update{
					{Path: "pipeline_automation_status", Value: statusValue},
					{Path: "pipeline_automation_updated_at", Value: now},
				}); err != nil {
					return err
				}
			}
			reservation.IntentStatus, reservation.SettlementStatus = "cancelled", "released"
			claimed = false
			return nil
		}
		if err := tx.Update(reservationRef, []firestore.Update{
			{Path: "intent_status", Value: "invoking"},
			{Path: "intent_updated_at", Value: now},
			{Path: "updated_at", Value: now},
		}); err != nil {
			return err
		}
		if autoProjectRef != nil {
			if err := tx.Update(autoProjectRef, []firestore.Update{
				{Path: "pipeline_automation_status", Value: "running"},
				{Path: "pipeline_automation_updated_at", Value: now},
			}); err != nil {
				return err
			}
		}
		reservation.IntentStatus = "invoking"
		claimed = true
		return nil
	})
	return reservation, claimed, err
}

// MarkPipelineIntentInvocationUnknown retains the shared claim for reconciliation.
func (c *Client) MarkPipelineIntentInvocationUnknown(ctx context.Context, reservationID, attemptID, userID, projectID string) error {
	return c.updatePipelineIntent(ctx, reservationID, attemptID, userID, projectID, []string{"invoking"}, map[string]interface{}{
		"intent_status": "invocation_unknown", "intent_error_stage": "pipeline_invocation",
		"intent_error_code": "invocation_outcome_unknown", "intent_error_message": "Checking whether the Pipeline started. Keep this page open or return later to see the result.",
	})
}

// FailPipelineIntent records a confirmed no-execution failure and releases its
// existing reservation in the same transaction.
func (c *Client) FailPipelineIntent(ctx context.Context, reservationID, attemptID, userID, projectID, expectedStatus, stage, code, message string) error {
	return c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return FailPipelineIntentInTransaction(ctx, tx, c.fs, reservationID, attemptID, userID, projectID, expectedStatus, stage, code, message)
	})
}

// FailPipelineIntentInTransaction settles a confirmed no-execution failure in
// the caller's transaction so durable runtime exhaustion can release the same
// reservation and shared claim atomically with its work record.
func FailPipelineIntentInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, attemptID, userID, projectID, expectedStatus, stage, code, message string) error {
	reservationRef := Collection(fs, pipelineQuotaReservationCollection).Doc(reservationID)
	snapshot, err := tx.Get(reservationRef)
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	reservation := quotaReservationFromData(reservationID, snapshot.Data())
	if reservation.UserID != userID || reservation.ProjectID != projectID || reservation.AttemptID != attemptID {
		return errors.New("pipeline intent owner or attempt identity mismatch")
	}
	return failPipelineIntentReservationInTransaction(ctx, tx, fs, reservationRef, reservation, expectedStatus, stage, code, message)
}

// SettlePipelineIntentRuntimeExhaustedInTransaction records terminal
// pre-invocation failure, while keeping an ambiguous invocation claimed for
// the existing reconciler to resolve.
func SettlePipelineIntentRuntimeExhaustedInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationID, attemptID, userID, projectID string, profileRevision int64) error {
	reservationRef := Collection(fs, pipelineQuotaReservationCollection).Doc(reservationID)
	snapshot, err := tx.Get(reservationRef)
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	reservation := quotaReservationFromData(reservationID, snapshot.Data())
	if reservation.UserID != userID || reservation.ProjectID != projectID || reservation.AttemptID != attemptID {
		return errors.New("pipeline intent owner or attempt identity mismatch")
	}
	if reservation.ProfileRevision != profileRevision {
		return nil
	}
	switch reservation.IntentStatus {
	case "preparing", "ready":
		return failPipelineIntentReservationInTransaction(ctx, tx, fs, reservationRef, reservation, reservation.IntentStatus,
			"pipeline_runtime", "runtime_retry_exhausted", "Pipeline guidance preparation failed after the available retries. Review the saved Profile and retry this run.")
	case "invoking":
		now := time.Now().UTC()
		return tx.Update(reservationRef, []firestore.Update{
			{Path: "intent_status", Value: "invocation_unknown"},
			{Path: "intent_error_stage", Value: "pipeline_invocation"},
			{Path: "intent_error_code", Value: "invocation_outcome_unknown"},
			{Path: "intent_error_message", Value: "Checking whether the Pipeline started. Keep this page open or return later to see the result."},
			{Path: "intent_updated_at", Value: now},
			{Path: "updated_at", Value: now},
		})
	default:
		return nil
	}
}

func failPipelineIntentReservationInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, reservationRef *firestore.DocumentRef, reservation QuotaReservation, expectedStatus, stage, code, message string) error {
	if reservation.IntentStatus == "failed" && reservation.SettlementStatus == "released" {
		return nil
	}
	if (expectedStatus != "preparing" && expectedStatus != "ready" && expectedStatus != "invoking" && expectedStatus != "invocation_unknown") ||
		reservation.IntentStatus != expectedStatus || reservation.SettlementStatus != "pending" || reservation.ExecutionID != "" {
		return errors.New("pipeline intent is not a confirmed pre-invocation failure")
	}
	var projectRef *firestore.DocumentRef
	if reservation.Trigger == pipelinequota.TriggerSourceCommit {
		projectRef = Collection(fs, "projects").Doc(reservation.UserID + "_" + reservation.ProjectID)
		if _, err := tx.Get(projectRef); err != nil {
			return err
		}
	}
	if _, err := releaseQuotaReservationInTransaction(ctx, tx, fs, reservation.ID, reservation.AttemptID); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		message = "Pipeline preparation failed. Review the Profile and retry this run."
	}
	now := time.Now().UTC()
	if err := tx.Update(reservationRef, []firestore.Update{
		{Path: "intent_status", Value: "failed"},
		{Path: "intent_error_stage", Value: stage},
		{Path: "intent_error_code", Value: code},
		{Path: "intent_error_message", Value: message},
		{Path: "intent_updated_at", Value: now},
		{Path: "updated_at", Value: now},
	}); err != nil {
		return err
	}
	if projectRef != nil {
		return tx.Update(projectRef, []firestore.Update{
			{Path: "pipeline_failure_blocked_fingerprint", Value: reservation.SourceFingerprint},
			{Path: "pipeline_failure_blocked_admin_revision", Value: reservation.AutomationAdminRevision},
			{Path: "pipeline_automation_status", Value: "failed"},
			{Path: "pipeline_automation_failure_stage", Value: stage},
			{Path: "pipeline_automation_failure_code", Value: code},
			{Path: "pipeline_automation_updated_at", Value: now},
		})
	}
	return nil
}

func (c *Client) updatePipelineIntent(ctx context.Context, reservationID, attemptID, userID, projectID string, expected []string, fields map[string]interface{}) error {
	return c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reservationRef := Collection(c.fs, pipelineQuotaReservationCollection).Doc(reservationID)
		snapshot, err := tx.Get(reservationRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		reservation := quotaReservationFromData(reservationID, snapshot.Data())
		if reservation.UserID != userID || reservation.ProjectID != projectID || reservation.AttemptID != attemptID {
			return errors.New("pipeline intent owner or attempt identity mismatch")
		}
		allowed := false
		for _, state := range expected {
			allowed = allowed || reservation.IntentStatus == state
		}
		if !allowed {
			return nil
		}
		now := time.Now().UTC()
		updates := make([]firestore.Update, 0, len(fields)+2)
		for path, value := range fields {
			updates = append(updates, firestore.Update{Path: path, Value: value})
		}
		updates = append(updates, firestore.Update{Path: "intent_updated_at", Value: now}, firestore.Update{Path: "updated_at", Value: now})
		return tx.Update(reservationRef, updates)
	})
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
	var autoProjectRef *firestore.DocumentRef
	var autoProjectData map[string]interface{}
	autoAdminEnabled := false
	if reservation.Trigger == pipelinequota.TriggerSourceCommit {
		autoProjectRef = Collection(fs, "projects").Doc(reservation.UserID + "_" + reservation.ProjectID)
		projectSnapshot, projectErr := tx.Get(autoProjectRef)
		if projectErr != nil {
			return "", false, projectErr
		}
		autoProjectData = projectSnapshot.Data()
		adminSnapshot, adminErr := tx.Get(Collection(fs, "system").Doc("pipeline_automation"))
		if adminErr != nil && status.Code(adminErr) != codes.NotFound {
			return "", false, adminErr
		}
		if adminSnapshot != nil && adminSnapshot.Exists() {
			autoAdminEnabled, _ = adminSnapshot.Data()["allow_auto_pipeline"].(bool)
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
	if reservation.IntentStatus != "" {
		intentStatus := "failed"
		switch state {
		case "SUCCEEDED":
			intentStatus = "succeeded"
		case "CANCELLED":
			intentStatus = "cancelled"
		}
		reservationUpdates = append(reservationUpdates,
			firestore.Update{Path: "intent_status", Value: intentStatus},
			firestore.Update{Path: "intent_updated_at", Value: now},
		)
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
	if autoProjectRef != nil {
		updates := []firestore.Update{{Path: "pipeline_automation_updated_at", Value: now}}
		switch state {
		case "SUCCEEDED":
			updates = append(updates,
				firestore.Update{Path: "pipeline_auto_consumed_fingerprint", Value: reservation.SourceFingerprint},
				firestore.Update{Path: "pipeline_auto_consumed_source_revision", Value: reservation.SourceRevision},
				firestore.Update{Path: "pipeline_automation_status", Value: "published"},
				firestore.Update{Path: "pipeline_failure_blocked_fingerprint", Value: firestore.Delete},
				firestore.Update{Path: "pipeline_failure_blocked_admin_revision", Value: firestore.Delete},
				firestore.Update{Path: "pipeline_automation_failure_stage", Value: firestore.Delete},
				firestore.Update{Path: "pipeline_automation_failure_code", Value: firestore.Delete},
			)
		case "FAILED":
			updates = append(updates,
				firestore.Update{Path: "pipeline_failure_blocked_fingerprint", Value: reservation.SourceFingerprint},
				firestore.Update{Path: "pipeline_failure_blocked_admin_revision", Value: reservation.AutomationAdminRevision},
				firestore.Update{Path: "pipeline_automation_status", Value: "failed"},
				firestore.Update{Path: "pipeline_automation_failure_stage", Value: "pipeline"},
				firestore.Update{Path: "pipeline_automation_failure_code", Value: "pipeline_failed"},
			)
		case "CANCELLED":
			updates = append(updates, firestore.Update{Path: "pipeline_automation_status", Value: "cancelled"})
		}
		currentRevision := int64FromFirestore(autoProjectData["pipeline_source_revision"])
		projectEnabled, _ := autoProjectData["auto_pipeline_enabled"].(bool)
		if currentRevision > reservation.SourceRevision && projectEnabled && autoAdminEnabled && state != "CANCELLED" {
			if err := enqueuePipelineAutomationWork(tx, fs, reservation.UserID, reservation.ProjectID, currentRevision, now); err != nil {
				return "", false, err
			}
			updates = append(updates, firestore.Update{Path: "pipeline_automation_status", Value: "pending"})
		}
		if err := tx.Update(autoProjectRef, updates); err != nil {
			return "", false, err
		}
	}
	return settlement, true, nil
}

func enqueuePipelineAutomationWork(tx *firestore.Transaction, fs *firestore.Client, userID, projectID string, revision int64, due time.Time) error {
	id := "source-" + fmt.Sprint(revision)
	workID := sha256.Sum256([]byte(userID + "\x00" + projectID + "\x00pipeline_automation\x00" + id))
	return tx.Set(Collection(fs, "profile_runtime_work").Doc(hex.EncodeToString(workID[:])), map[string]interface{}{
		"user_id": userID, "project_id": projectID, "kind": "pipeline_automation", "revision": revision, "id": id,
		"candidate_id": "", "due": due, "pending": true, "attempts": 0, "token": "", "lease_until": time.Time{}, "status": "pending",
	})
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
	reservation.IntentStatus, _ = data["intent_status"].(string)
	mode, _ := data["intent_mode"].(string)
	reservation.CompileMode = pipelinequota.ExecutionMode(mode)
	reservation.ProfileRevision = int64FromFirestore(data["profile_revision"])
	reservation.ProfileRequirements = profileRequirementsFromFirestore(data["profile_requirements"])
	reservation.RequirementsDigest, _ = data["requirements_digest"].(string)
	reservation.ConsumedGuidanceKind, _ = data["consumed_guidance_kind"].(string)
	reservation.ConsumedGuidanceStatus, _ = data["consumed_guidance_status"].(string)
	reservation.ConsumedGuidanceRevision, _ = data["consumed_guidance_revision"].(string)
	reservation.ConsumedGuidanceSourceRevision = int64FromFirestore(data["consumed_guidance_source_revision"])
	reservation.ConsumedGuidanceInputDigest, _ = data["consumed_guidance_input_digest"].(string)
	reservation.ConsumedGuidanceModelVersion, _ = data["consumed_guidance_model_version"].(string)
	reservation.ConsumedGuidancePromptVersion, _ = data["consumed_guidance_prompt_version"].(string)
	reservation.ConsumedGuidanceSchemaVersion, _ = data["consumed_guidance_schema_version"].(string)
	reservation.ConsumedActiveGuidance = derivedGuidanceRefFromFirestore(data["consumed_active_guidance"])
	reservation.ConsumedBootstrapGuidance = bootstrapGuidanceRefFromFirestore(data["consumed_bootstrap_guidance"])
	reservation.ActiveCandidateID, _ = data["active_candidate_id"].(string)
	reservation.ActiveContentGeneration, _ = data["active_content_generation"].(string)
	reservation.SourceFingerprint, _ = data["source_fingerprint"].(string)
	reservation.SourceRevision = int64FromFirestore(data["source_revision"])
	reservation.AutomationProjectRevision = int64FromFirestore(data["auto_pipeline_project_revision"])
	reservation.AutomationAdminRevision = int64FromFirestore(data["auto_pipeline_admin_revision"])
	reservation.IntentErrorStage, _ = data["intent_error_stage"].(string)
	reservation.IntentErrorCode, _ = data["intent_error_code"].(string)
	reservation.IntentErrorMessage, _ = data["intent_error_message"].(string)
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
	if t, ok := firestoreTimestamp(data["created_at"]); ok {
		reservation.CreatedAt = t
	}
	if t, ok := firestoreTimestamp(data["updated_at"]); ok {
		reservation.UpdatedAt = t
	}
	return reservation
}

type pipelineProfileIntentSnapshot struct {
	Status                  string
	ProfileRevision         int64
	Requirements            []PipelineProfileRequirement
	RequirementsDigest      string
	GuidanceKind            string
	GuidanceStatus          string
	GuidanceRevision        string
	GuidanceSourceRevision  int64
	GuidanceInputDigest     string
	GuidanceModelVersion    string
	GuidancePromptVersion   string
	GuidanceSchemaVersion   string
	ActiveGuidance          *profileartifacts.DerivedRef
	BootstrapGuidance       *profileartifacts.BootstrapGuidanceRef
	ActiveCandidateID       string
	ActiveContentGeneration string
}

func bindFullModeGuidanceToRequirements(intent pipelineProfileIntentSnapshot, mode pipelinequota.ExecutionMode) pipelineProfileIntentSnapshot {
	if mode != pipelinequota.ModeFull || intent.GuidanceKind != "active" || intent.ActiveGuidance == nil {
		return intent
	}
	if len(intent.Requirements) > 0 && intent.ActiveGuidance.InputDigest == intent.RequirementsDigest && intent.GuidanceSourceRevision > 0 {
		return intent
	}
	intent.GuidanceKind = ""
	intent.GuidanceStatus = ""
	intent.GuidanceRevision = ""
	intent.GuidanceSourceRevision = 0
	intent.GuidanceInputDigest = ""
	intent.GuidanceModelVersion = ""
	intent.GuidancePromptVersion = ""
	intent.GuidanceSchemaVersion = ""
	intent.ActiveGuidance = nil
	intent.ActiveCandidateID = ""
	intent.ActiveContentGeneration = ""
	if len(intent.Requirements) == 0 {
		intent.Status = "ready"
		intent.GuidanceKind = "none"
		return intent
	}
	intent.Status = "preparing"
	return intent
}

func consumedActiveGuidanceWire(ref *profileartifacts.DerivedRef) map[string]interface{} {
	if ref == nil {
		return nil
	}
	return map[string]interface{}{
		"revision": ref.Revision, "input_digest": ref.InputDigest, "model_version": ref.ModelVersion,
		"prompt_version": ref.PromptVersion, "schema_version": ref.SchemaVersion,
	}
}

func readPipelineProfileIntentSnapshot(ctx context.Context, tx *firestore.Transaction, client *firestore.Client, userID, projectID string) (pipelineProfileIntentSnapshot, error) {
	result := pipelineProfileIntentSnapshot{Status: "ready", Requirements: []PipelineProfileRequirement{}}
	stateRef := Collection(client, "projects").Doc(userID + "_" + projectID).Collection("profile").Doc("state")
	snapshot, err := tx.Get(stateRef)
	if status.Code(err) == codes.NotFound {
		digestBytes, err := json.Marshal(result.Requirements)
		if err != nil {
			return result, err
		}
		digest := sha256.Sum256(digestBytes)
		result.RequirementsDigest = hex.EncodeToString(digest[:])
		result.GuidanceKind = "none"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	data := snapshot.Data()
	storedProjectID, _ := data["project_id"].(string)
	if storedProjectID != projectID {
		return result, errors.New("Profile state Project identity mismatch")
	}
	result.ProfileRevision = int64FromFirestore(data["revision"])
	if result.ProfileRevision < 0 {
		return result, errors.New("Profile revision is invalid")
	}
	rawRequirements, exists := data["requirements"]
	if !exists || rawRequirements == nil {
		return result, errors.New("Profile state has no requirements array")
	}
	items, ok := rawRequirements.([]interface{})
	if !ok {
		return result, errors.New("Profile requirements snapshot is invalid")
	}
	result.Requirements = profileRequirementsFromFirestore(rawRequirements)
	if len(items) > 0 && result.Requirements == nil {
		return result, errors.New("Profile requirements snapshot is invalid")
	}
	if result.Requirements == nil {
		result.Requirements = []PipelineProfileRequirement{}
	}
	digestBytes, err := json.Marshal(result.Requirements)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(digestBytes)
	result.RequirementsDigest = hex.EncodeToString(digest[:])

	if active := mapFromFirestore(data["active"]); active != nil {
		candidateID, _ := active["candidate_id"].(string)
		contentGeneration, _ := active["content_generation"].(string)
		guidanceRevision, _ := active["guidance_revision"].(string)
		if candidateID == "" || contentGeneration == "" || !profileartifacts.IsSHA256(guidanceRevision) {
			return result, errors.New("active Profile pointer is incomplete")
		}
		candidateRef := stateRef.Collection("candidates").Doc(candidateID)
		candidateSnapshot, err := tx.Get(candidateRef)
		if err != nil {
			return result, fmt.Errorf("read active Profile candidate: %w", err)
		}
		candidate := candidateSnapshot.Data()
		storedCandidateID, _ := candidate["candidate_id"].(string)
		source, _ := candidate["source"].(string)
		storedGeneration, _ := candidate["content_generation"].(string)
		ref := derivedGuidanceRefFromFirestore(candidate["guidance"])
		if storedCandidateID != candidateID || (source != "manual" && source != "compile_auto") || storedGeneration != contentGeneration ||
			ref == nil || ref.Revision != guidanceRevision || !profileartifacts.IsSHA256(ref.InputDigest) ||
			ref.ModelVersion == "" || ref.PromptVersion == "" || ref.SchemaVersion != profileartifacts.GuidanceSchema {
			return result, errors.New("active Profile guidance reference is incomplete")
		}
		result.GuidanceKind = "active"
		result.GuidanceStatus = "active"
		result.GuidanceRevision = ref.Revision
		result.GuidanceSourceRevision = int64FromFirestore(candidate["base_revision"])
		result.GuidanceInputDigest = ref.InputDigest
		result.GuidanceModelVersion = ref.ModelVersion
		result.GuidancePromptVersion = ref.PromptVersion
		result.GuidanceSchemaVersion = ref.SchemaVersion
		result.ActiveGuidance = ref
		result.ActiveCandidateID = candidateID
		result.ActiveContentGeneration = contentGeneration
		return result, nil
	}

	if len(result.Requirements) == 0 {
		result.GuidanceKind = "none"
		return result, nil
	}
	bootstrap := mapFromFirestore(data["bootstrap_guidance"])
	if bootstrap != nil {
		guidanceStatus, _ := bootstrap["status"].(string)
		ref := bootstrapGuidanceRefFromFirestore(bootstrap)
		if ref != nil && (guidanceStatus == "preview_ready" || guidanceStatus == "confirmed") &&
			ref.ProfileRevision == result.ProfileRevision && ref.InputDigest == result.RequirementsDigest &&
			profileartifacts.IsSHA256(ref.Revision) && profileartifacts.IsSHA256(ref.InputDigest) &&
			ref.ModelVersion != "" && ref.PromptVersion != "" && ref.SchemaVersion == profileartifacts.BootstrapGuidanceSchema {
			result.GuidanceKind = "bootstrap"
			result.GuidanceStatus = guidanceStatus
			result.GuidanceRevision = ref.Revision
			result.GuidanceSourceRevision = ref.ProfileRevision
			result.GuidanceInputDigest = ref.InputDigest
			result.GuidanceModelVersion = ref.ModelVersion
			result.GuidancePromptVersion = ref.PromptVersion
			result.GuidanceSchemaVersion = ref.SchemaVersion
			result.BootstrapGuidance = ref
			return result, nil
		}
	}
	result.Status = "preparing"
	return result, nil
}

func mapFromFirestore(value interface{}) map[string]interface{} {
	fields, _ := value.(map[string]interface{})
	return fields
}

func int64FromFirestore(value interface{}) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	default:
		return 0
	}
}

func profileRequirementsFromFirestore(value interface{}) []PipelineProfileRequirement {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]PipelineProfileRequirement, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		id, _ := fields["id"].(string)
		text, _ := fields["text"].(string)
		result = append(result, PipelineProfileRequirement{ID: id, Text: text})
	}
	return result
}

func derivedGuidanceRefFromFirestore(value interface{}) *profileartifacts.DerivedRef {
	fields, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	ref := &profileartifacts.DerivedRef{}
	ref.Revision, _ = fields["revision"].(string)
	ref.InputDigest, _ = fields["input_digest"].(string)
	ref.ModelVersion, _ = fields["model_version"].(string)
	ref.PromptVersion, _ = fields["prompt_version"].(string)
	ref.SchemaVersion, _ = fields["schema_version"].(string)
	if ref.Revision == "" {
		ref.Revision, _ = fields["Revision"].(string)
		ref.InputDigest, _ = fields["InputDigest"].(string)
		ref.ModelVersion, _ = fields["ModelVersion"].(string)
		ref.PromptVersion, _ = fields["PromptVersion"].(string)
		ref.SchemaVersion, _ = fields["SchemaVersion"].(string)
	}
	return ref
}

func bootstrapGuidanceRefFromFirestore(value interface{}) *profileartifacts.BootstrapGuidanceRef {
	fields, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	ref := &profileartifacts.BootstrapGuidanceRef{}
	ref.Revision, _ = fields["Revision"].(string)
	ref.InputDigest, _ = fields["InputDigest"].(string)
	ref.ModelVersion, _ = fields["ModelVersion"].(string)
	ref.PromptVersion, _ = fields["PromptVersion"].(string)
	ref.SchemaVersion, _ = fields["SchemaVersion"].(string)
	ref.ProfileRevision = int64FromFirestore(fields["ProfileRevision"])
	if ref.Revision == "" {
		ref.Revision, _ = fields["revision"].(string)
		ref.InputDigest, _ = fields["input_digest"].(string)
		ref.ModelVersion, _ = fields["model_version"].(string)
		ref.PromptVersion, _ = fields["prompt_version"].(string)
		ref.SchemaVersion, _ = fields["schema_version"].(string)
		ref.ProfileRevision = int64FromFirestore(fields["profile_revision"])
	}
	return ref
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
