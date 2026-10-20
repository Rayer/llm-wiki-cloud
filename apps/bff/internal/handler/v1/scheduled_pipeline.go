package v1

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const scheduledPipelinePageSize = 100

func trustedScheduledPipelineIssuer(issuer string) bool {
	switch strings.TrimSpace(issuer) {
	case "accounts.google.com", "https://accounts.google.com":
		return true
	default:
		return false
	}
}

type scheduledIdentityValidator func(context.Context, string, string) (*idtoken.Payload, error)

type ScheduledPipelineSweepResult struct {
	Scanned     int `json:"scanned"`
	Eligible    int `json:"eligible"`
	Accepted    int `json:"accepted"`
	Skipped     int `json:"skipped"`
	Unavailable int `json:"unavailable"`
}

// SetScheduledPipelineConfig passes the single generated schedule and its
// separately provisioned OIDC identity into the internal entrypoint.
func (h *Handler) SetScheduledPipelineConfig(cron, timezone, audience, serviceAccount string) {
	h.pipelineScheduleCron = strings.TrimSpace(cron)
	h.pipelineScheduleTimezone = strings.TrimSpace(timezone)
	h.scheduledPipelineAudience = strings.TrimSpace(audience)
	h.scheduledPipelineServiceAcct = strings.TrimSpace(serviceAccount)
}

// SetScheduledPipelineIdentityValidator is an explicit HTTP fixture seam; the
// production verifier remains Google's ID-token validator.
func (h *Handler) SetScheduledPipelineIdentityValidator(validator scheduledIdentityValidator) {
	h.scheduledPipelineValidator = validator
}

// ScheduledPipeline is the narrowly scoped scheduler entrypoint. Cloud Run IAM
// protects the service; the OIDC identity check also fences non-scheduler calls.
func (h *Handler) ScheduledPipeline(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	if h.scheduledPipelineAudience == "" || h.scheduledPipelineServiceAcct == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scheduled pipeline identity is not configured"})
		return
	}
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	validate := h.scheduledPipelineValidator
	if validate == nil {
		validate = idtoken.Validate
	}
	payload, err := validate(c.Request.Context(), strings.TrimSpace(parts[1]), h.scheduledPipelineAudience)
	if err != nil || payload == nil || payload.Audience != h.scheduledPipelineAudience ||
		!trustedScheduledPipelineIssuer(payload.Issuer) ||
		payload.Claims["email"] != h.scheduledPipelineServiceAcct || payload.Claims["email_verified"] != true {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	result, err := h.RunScheduledPipelineSweep(c.Request.Context(), time.Now())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scheduled pipeline scan unavailable"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// RunScheduledPipelineSweep scans every ready Project page. Project failures
// are recorded and isolated; a collection read failure stops the sweep so a
// later tick can resume from the beginning without duplicate day grants.
func (h *Handler) RunScheduledPipelineSweep(ctx context.Context, now time.Time) (ScheduledPipelineSweepResult, error) {
	var result ScheduledPipelineSweepResult
	if h.firestore == nil || h.firestore.Raw() == nil || h.store == nil || h.pipelineScheduleCron == "" || h.pipelineScheduleTimezone == "" {
		return result, errors.New("scheduled pipeline dependencies are unavailable")
	}
	if _, err := time.LoadLocation(h.pipelineScheduleTimezone); err != nil {
		return result, errors.New("scheduled pipeline timezone is invalid")
	}
	store, ok := h.effectiveQuotaStore().(scheduledPipelineQuotaStore)
	if !ok {
		return result, errors.New("scheduled pipeline quota store is unavailable")
	}
	if _, ok := h.effectiveQuotaStore().(pipelineAdmissionStore); !ok {
		return result, errors.New("shared pipeline admission store is unavailable")
	}
	now = now.UTC().Truncate(time.Microsecond)
	projects := scopedfirestore.Collection(h.firestore.Raw(), "projects")
	lastID := ""
	for {
		query := projects.OrderBy(cloudfirestore.DocumentID, cloudfirestore.Asc).Limit(scheduledPipelinePageSize)
		if lastID != "" {
			query = query.StartAfter(lastID)
		}
		page, err := query.Documents(ctx).GetAll()
		if err != nil {
			return result, err
		}
		for _, doc := range page {
			result.Scanned++
			userID, projectID, ready := scheduledProjectIdentity(doc.Ref.ID, doc.Data())
			if !ready {
				continue
			}
			if h.isDemoUser(userID) {
				if h.recordScheduledCheck(ctx, store, userID, projectID, now, "ineligible", 0, 0, 0, 0) != nil {
					result.Unavailable++
				} else {
					result.Skipped++
				}
				continue
			}
			result.Eligible++
			accepted, skipped, unavailable := h.runScheduledProject(ctx, store, userID, projectID, now)
			if accepted {
				result.Accepted++
			} else if unavailable {
				result.Unavailable++
			} else if skipped {
				result.Skipped++
			}
		}
		if len(page) < scheduledPipelinePageSize {
			return result, nil
		}
		lastID = page[len(page)-1].Ref.ID
	}
}

func scheduledProjectIdentity(documentID string, data map[string]interface{}) (userID, projectID string, ready bool) {
	userID, _ = data["user_id"].(string)
	projectID, _ = data["project_id"].(string)
	projectStatus, _ := data["status"].(string)
	if !auth.ValidPathSegment(userID) || !auth.ValidPathSegment(projectID) ||
		projectStatus != "ready" || documentID != userID+"_"+projectID {
		return "", "", false
	}
	return userID, projectID, true
}

func (h *Handler) runScheduledProject(ctx context.Context, qs scheduledPipelineQuotaStore, userID, projectID string, now time.Time) (accepted, skipped, unavailable bool) {
	statusStore := qs
	if h.accountLookup == nil {
		h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", 0, 0, 0, 0)
		return false, false, true
	}
	owner, err := h.accountLookup(ctx, userID)
	if err != nil {
		reason := "unavailable"
		if status.Code(err) == codes.NotFound || errors.Is(err, auth.ErrAccountInactive) {
			reason = "ineligible"
		}
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, reason, 0, 0, 0, 0) {
			return false, false, true
		}
		return false, reason == "ineligible", reason == "unavailable"
	}
	if !owner.Active() {
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "ineligible", 0, 0, 0, 0) {
			return false, false, true
		}
		return false, true, false
	}
	newRaw, rawDirty, annotationDirty, err := h.pendingWorkForProject(ctx, userID, projectID)
	if err != nil {
		h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", 0, 0, 0, 0)
		return false, false, true
	}
	pending := newRaw + rawDirty + annotationDirty
	if pending == 0 {
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "no_work", pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, true, false
	}
	alreadyRunning, err := h.isScheduledPipelineRunning(ctx, userID, projectID)
	if err != nil {
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, false, true
	}
	if alreadyRunning {
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "busy", pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, true, false
	}
	dayKey := pipelinequota.DayKeyUTC(now)
	reservationID := scopedfirestore.ScheduledPipelineBudgetID(userID, projectID, dayKey)
	snapshot, reserved, err := qs.ReserveScheduledQuota(ctx, reservationID, userID, projectID,
		h.pipelineLimits(), now, pipelinequota.ModeIncremental, false, false, newRaw, rawDirty, annotationDirty)
	if err != nil {
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, false, true
	}
	if !reserved {
		reason := scheduledSkipReason(snapshot.Reason)
		if !snapshot.Allowed && reason != "" {
			if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, reason, pending, newRaw, rawDirty, annotationDirty) {
				return false, false, true
			}
			return false, true, false
		}
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, false, true
	}
	if err := h.recordScheduledCheck(ctx, statusStore, userID, projectID, now, "", pending, newRaw, rawDirty, annotationDirty); err != nil {
		admission := h.effectiveQuotaStore().(pipelineAdmissionStore)
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := admission.ReleaseQuotaReservation(releaseCtx, reservationID)
		cancel()
		if releaseErr != nil {
			if evidenceErr := h.persistScheduledNotInvokedEvidence(userID, projectID, reservationID); evidenceErr != nil {
				log.Print("scheduled pipeline recovery evidence unavailable")
			}
		}
		return false, false, true
	}
	executionID, err := h.invokePipelineJobStageWithReservation(ctx, userID, projectID, false, pipelineStageFull, reservationID)
	if err != nil {
		if errors.Is(err, errPipelineInvocationOutcomeUnknown) {
			if attemptErr := qs.RecordScheduledPipelineAttempt(ctx, userID, projectID, dayKey, reservationID, "", "unknown", now); attemptErr != nil {
				h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, "unavailable", pending, newRaw, rawDirty, annotationDirty)
			}
			return false, false, true
		}
		admission := h.effectiveQuotaStore().(pipelineAdmissionStore)
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := admission.ReleaseQuotaReservation(releaseCtx, reservationID)
		cancel()
		if releaseErr != nil {
			if evidenceErr := h.persistScheduledNotInvokedEvidence(userID, projectID, reservationID); evidenceErr != nil {
				log.Print("scheduled pipeline recovery evidence unavailable")
			}
			return false, false, true
		}
		reason := "unavailable"
		if errors.Is(err, auth.ErrAccountInactive) {
			reason = "ineligible"
		}
		if !h.recordScheduledCheckOK(ctx, statusStore, userID, projectID, now, reason, pending, newRaw, rawDirty, annotationDirty) {
			return false, false, true
		}
		return false, reason == "ineligible", reason == "unavailable"
	}
	if h.localPipeline == nil {
		if err := h.effectiveQuotaStore().LinkQuotaReservation(ctx, reservationID, executionID); err != nil {
			// The execution environment carries the reservation ID for reconciliation.
		}
	}
	if err := qs.RecordScheduledPipelineAttempt(ctx, userID, projectID, dayKey, reservationID, executionID, "accepted", now); err != nil {
		return true, false, true
	}
	return true, false, false
}

func scheduledSkipReason(reason pipelinequota.Reason) string {
	switch reason {
	case pipelinequota.ReasonAlreadyRunning:
		return "busy"
	case pipelinequota.ReasonCooldown:
		return "cooldown"
	case pipelinequota.ReasonEntitlementUsed:
		return "daily_entitlement_used"
	case pipelinequota.ReasonNoNewRaw:
		return "no_work"
	case pipelinequota.ReasonDemo:
		return "ineligible"
	default:
		return ""
	}
}

func (h *Handler) recordScheduledCheck(ctx context.Context, qs scheduledPipelineQuotaStore, userID, projectID string, now time.Time, reason string, pending, newRaw, rawDirty, annotationDirty int) error {
	current, err := qs.LoadScheduledPipelineStatus(ctx, userID, projectID, now)
	if err != nil {
		return err
	}
	current.Schedule = h.pipelineScheduleCron
	current.Timezone = h.pipelineScheduleTimezone
	lastCheck, nextCheck := now, nextScheduledPipelineCheck(now, h.pipelineScheduleTimezone)
	current.LastCheckAt = &lastCheck
	current.NextCheckAt = &nextCheck
	current.SkipReason = reason
	current.PendingWork = pending
	current.NewRawFiles = newRaw
	current.RawDirtyFiles = rawDirty
	current.AnnotationDirtyFiles = annotationDirty
	current.DayKey = pipelinequota.DayKeyUTC(now)
	current.Availability = "available"
	return qs.RecordScheduledPipelineCheck(ctx, current)
}

func (h *Handler) recordScheduledCheckOK(ctx context.Context, qs scheduledPipelineQuotaStore, userID, projectID string, now time.Time, reason string, pending, newRaw, rawDirty, annotationDirty int) bool {
	return h.recordScheduledCheck(ctx, qs, userID, projectID, now, reason, pending, newRaw, rawDirty, annotationDirty) == nil
}

func (h *Handler) persistScheduledNotInvokedEvidence(userID, projectID, reservationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.writeConfirmedPipelineInvokeFailure(ctx, userID, projectID, reservationID)
}

func (h *Handler) isScheduledPipelineRunning(ctx context.Context, userID, projectID string) (bool, error) {
	if admission, ok := h.effectiveQuotaStore().(interface {
		PipelineAdmissionActive(context.Context, string, string) (bool, error)
	}); ok {
		active, err := admission.PipelineAdmissionActive(ctx, userID, projectID)
		if err != nil || active {
			return active, err
		}
	}
	if h.localPipeline != nil {
		return h.localPipeline.Running(ctx, userID, projectID)
	}
	locked, err := h.projectLockActive(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	hasOwned, allTerminal, anyRunning, err := h.pipelineOwnedExecutionActivityForOwner(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	return locked || anyRunning || (hasOwned && !allTerminal), nil
}

func (h *Handler) scheduledPipelineStatus(ctx context.Context, userID, projectID string, now time.Time) *scopedfirestore.ScheduledPipelineStatus {
	configured := h.scheduledPipelineConfigured()
	result := &scopedfirestore.ScheduledPipelineStatus{
		Availability: "unavailable", UserID: userID, ProjectID: projectID,
		Schedule: strings.TrimSpace(h.pipelineScheduleCron), Timezone: strings.TrimSpace(h.pipelineScheduleTimezone),
		DayKey: pipelinequota.DayKeyUTC(now), SkipReason: "unavailable",
	}
	if !configured {
		result.Availability, result.SkipReason = "disabled", "disabled"
	}
	store, ok := h.effectiveQuotaStore().(scheduledPipelineQuotaStore)
	if !ok {
		return result
	}
	loaded, err := store.LoadScheduledPipelineStatus(ctx, userID, projectID, now)
	if err != nil {
		return result
	}
	loaded.Schedule = strings.TrimSpace(h.pipelineScheduleCron)
	loaded.Timezone = strings.TrimSpace(h.pipelineScheduleTimezone)
	if !configured {
		loaded.Availability, loaded.SkipReason, loaded.NextCheckAt = "disabled", "disabled", nil
		return &loaded
	}
	newRaw, rawDirty, annotationDirty, err := h.pendingWorkForProject(ctx, userID, projectID)
	if err != nil {
		loaded.Availability, loaded.SkipReason, loaded.NextCheckAt = "unavailable", "unavailable", nil
		return &loaded
	}
	loaded.PendingWork = newRaw + rawDirty + annotationDirty
	loaded.NewRawFiles = newRaw
	loaded.RawDirtyFiles = rawDirty
	loaded.AnnotationDirtyFiles = annotationDirty
	if loaded.LastCheckAt == nil {
		loaded.Availability, loaded.SkipReason, loaded.NextCheckAt = "not_checked", "not_checked", nil
	} else {
		nextCheck := nextScheduledPipelineCheck(now, h.pipelineScheduleTimezone)
		loaded.Availability = "available"
		loaded.NextCheckAt = &nextCheck
	}
	return &loaded
}

func (h *Handler) scheduledPipelineConfigured() bool {
	if strings.TrimSpace(h.pipelineScheduleCron) == "" || strings.TrimSpace(h.pipelineScheduleTimezone) == "" ||
		strings.TrimSpace(h.scheduledPipelineAudience) == "" || strings.TrimSpace(h.scheduledPipelineServiceAcct) == "" {
		return false
	}
	_, err := time.LoadLocation(h.pipelineScheduleTimezone)
	return err == nil
}

func (h *Handler) scheduledPipelineExecutionStatus(ctx context.Context, userID, projectID string, scheduled *scopedfirestore.ScheduledPipelineStatus) *handler.PipelineExecutionResponse {
	if scheduled == nil || strings.TrimSpace(scheduled.LastAttemptID) == "" || strings.TrimSpace(scheduled.LastExecutionID) == "" {
		return nil
	}
	quotaStore := h.effectiveQuotaStore()
	if quotaStore == nil {
		return nil
	}
	reservation, found, err := quotaStore.GetQuotaReservation(ctx, scheduled.LastAttemptID)
	if err != nil || !found || reservation.UserID != userID || reservation.ProjectID != projectID ||
		reservation.Trigger != pipelinequota.TriggerScheduledDaily || reservation.BudgetClass != "scheduled_daily" ||
		(reservation.ExecutionID != "" && reservation.ExecutionID != scheduled.LastExecutionID) {
		return nil
	}
	response, err := h.pipelineExecutionStatusForOwner(ctx, scheduled.LastExecutionID, userID, projectID)
	if err != nil || response == nil {
		return nil
	}
	h.attachExactPipelineExecutionEvidence(ctx, response, &pipelineExecutionOwner{userID: userID, projectID: projectID}, scheduled.LastExecutionID)
	return response
}

func nextScheduledPipelineCheck(now time.Time, timezone string) time.Time {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}
	}
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), local.Hour()+1, 0, 0, 0, location)
	return next.UTC()
}
