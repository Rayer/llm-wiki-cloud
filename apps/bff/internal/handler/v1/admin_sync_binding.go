package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const adminBindingMutationBodyLimit = 16 << 10

const (
	adminBindingReadRateClass     = "read"
	adminBindingMutationRateClass = "mutation"
)

var adminBindingOperationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type adminBindingRevokeBody struct {
	SchemaVersion    int    `json:"schema_version"`
	OperationID      string `json:"operation_id"`
	Environment      string `json:"environment"`
	Subject          string `json:"subject"`
	Reason           string `json:"reason"`
	ExpectedRevision string `json:"expected_revision"`
}

type managementMeReader interface {
	ManagementMe(context.Context, string) (authconsumer.ManagementMeResult, error)
}

func (h *Handler) SetSyncBindingWireEnvironment(environment string) {
	h.syncBindingWireEnvironment = strings.TrimSpace(environment)
}

func (h *Handler) SetAdminMutationOrigins(origins []string) {
	h.adminMutationOrigins = make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if normalized := exactAdminOrigin(origin); normalized != "" {
			h.adminMutationOrigins[normalized] = struct{}{}
		}
	}
}

// AdminWebAccessOnly narrows the new binding management endpoints to browser
// Web access credentials. Existing product-admin routes retain their current
// credential policy.
func (h *Handler) AdminWebAccessOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if identity, ok := c.Get("authConsumerIdentity"); ok {
			verified, ok := identity.(authconsumer.Identity)
			if !ok || verified.CredentialKind != "web-access" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Web authentication required"})
				return
			}
		} else if kind := c.GetString("clientKind"); kind != "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Web authentication required"})
			return
		}
		c.Next()
	}
}

func (h *Handler) FreshLWCAdminScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !h.allowAdminRequest(c.GetString("userID"), adminBindingReadRateClass, 60, time.Now().UTC()) {
			c.Header("Retry-After", "60")
			writeAdminBindingError(c, http.StatusTooManyRequests, "rate_limited", true, "", nil)
			return
		}
		if _, err := h.readFreshLWCAdminAuthority(c.Request.Context(), c.GetString("userID"), requestUserCredential(c)); err != nil {
			writeAdminBindingAuthError(c, err)
			return
		}
		c.Next()
	}
}

func (h *Handler) AdminBindingMutationGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		origin := exactAdminOrigin(c.GetHeader("Origin"))
		if c.GetHeader("X-GateAuth-Admin-Browser") != "1" || origin == "" || !h.isAllowedAdminMutationOrigin(origin) || requestUserCredential(c) == "" {
			writeAdminBindingError(c, http.StatusForbidden, "cross_origin_rejected", false, "", nil)
			return
		}
		if fetchSite := strings.ToLower(strings.TrimSpace(c.GetHeader("Sec-Fetch-Site"))); fetchSite != "" &&
			fetchSite != "same-origin" && fetchSite != "same-site" && fetchSite != "cross-site" {
			writeAdminBindingError(c, http.StatusForbidden, "cross_origin_rejected", false, "", nil)
			return
		}
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, "", nil)
			return
		}
		if !h.allowAdminRequest(c.GetString("userID"), adminBindingMutationRateClass, 10, time.Now().UTC()) {
			c.Header("Retry-After", "60")
			writeAdminBindingError(c, http.StatusTooManyRequests, "rate_limited", true, "", nil)
			return
		}
		c.Next()
	}
}

func (h *Handler) isAllowedAdminMutationOrigin(origin string) bool {
	_, ok := h.adminMutationOrigins[origin]
	return ok
}

func exactAdminOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && parsed.Scheme != "http") || strings.Contains(raw, "*") {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

func (h *Handler) allowAdminRequest(actorID, class string, limit int, now time.Time) bool {
	h.adminMutationRateMu.Lock()
	defer h.adminMutationRateMu.Unlock()
	if h.adminRequestRates == nil {
		h.adminRequestRates = make(map[string][]time.Time)
	}
	key := actorID + "\x00" + class
	cutoff := now.Add(-time.Minute)
	for actor, entries := range h.adminRequestRates {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.After(cutoff) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(h.adminRequestRates, actor)
		} else {
			h.adminRequestRates[actor] = kept
		}
	}
	entries := h.adminRequestRates[key]
	if len(entries) >= limit {
		return false
	}
	h.adminRequestRates[key] = append(entries, now)
	return true
}

func (h *Handler) AdminSyncBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.adminSyncBindingAuthority == nil {
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, "", nil)
		return
	}
	if !auth.ValidPathSegment(c.Param("id")) {
		writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, "", nil)
		return
	}
	record, err := h.resolveAdminProjectRecord(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAdminBindingProjectError(c, err)
		return
	}
	binding, _, err := h.adminSyncBindingAuthority.AdminBinding(c.Request.Context(), record.id, record.userID, record.projectID)
	if err != nil {
		writeAdminBindingAuthorityError(c, err, "")
		return
	}
	wireEnvironment := h.syncBindingWireEnvironment
	if wireEnvironment == "" {
		wireEnvironment = h.adminSyncBindingAuthorityEnvironment()
	}
	c.JSON(http.StatusOK, gin.H{"schema_version": 1, "environment": h.adminSyncBindingAuthorityEnvironment(),
		"wire_environment": wireEnvironment, "owner_id": record.userID, "project_id": record.projectID,
		"project_record_id": record.id, "binding": binding})
}

func (h *Handler) AdminRevokeSyncBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.adminSyncBindingAuthority == nil {
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, "", nil)
		return
	}
	if !auth.ValidPathSegment(c.Param("id")) || !auth.ValidPathSegment(c.Param("bindingID")) {
		writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, "", nil)
		return
	}
	record, err := h.resolveAdminProjectRecord(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAdminBindingProjectError(c, err)
		return
	}
	var body adminBindingRevokeBody
	if err := decodeAdminBindingMutation(c, &body); err != nil {
		if errors.Is(err, errAdminBindingBodyTooLarge) {
			writeAdminBindingError(c, http.StatusRequestEntityTooLarge, "request_too_large", false, body.OperationID, nil)
		} else {
			writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, body.OperationID, nil)
		}
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.SchemaVersion != 1 || !adminBindingOperationID.MatchString(body.OperationID) || body.Environment == "" ||
		body.Subject != record.userID || !validAdminBindingReason(body.Reason) || body.ExpectedRevision == "" {
		writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, body.OperationID, nil)
		return
	}
	request := auth.AdminSyncBindingRevocationRequest{ActorID: c.GetString("userID"), SubjectID: record.userID,
		ProjectRecordID: record.id, ProjectID: record.projectID, BindingID: c.Param("bindingID"), OperationID: body.OperationID,
		Environment: body.Environment, Reason: body.Reason, ExpectedRevision: body.ExpectedRevision}
	credential := requestUserCredential(c)
	receipt, err := h.adminSyncBindingAuthority.AdminRevokeBinding(c.Request.Context(), request, func(ctx context.Context) (time.Time, error) {
		return h.readFreshLWCAdminAuthority(ctx, request.ActorID, credential)
	})
	if err != nil {
		writeAdminBindingAuthorityError(c, err, body.OperationID)
		return
	}
	c.JSON(http.StatusOK, receipt)
}

func (h *Handler) AdminReadSyncBindingRevocation(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.adminSyncBindingAuthority == nil {
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, c.Param("operationID"), nil)
		return
	}
	receipt, err := h.adminSyncBindingAuthority.AdminReadBindingRevocationReceipt(c.Request.Context(), c.GetString("userID"), c.Param("operationID"))
	if err != nil {
		writeAdminBindingAuthorityError(c, err, c.Param("operationID"))
		return
	}
	c.JSON(http.StatusOK, receipt)
}

var errAdminBindingBodyTooLarge = errors.New("admin binding request too large")

func decodeAdminBindingMutation(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, adminBindingMutationBodyLimit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errAdminBindingBodyTooLarge
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing or malformed JSON")
	}
	return nil
}

func (h *Handler) readFreshLWCAdminAuthority(ctx context.Context, actorID, credential string) (time.Time, error) {
	reader, ok := h.consumerVerifier.(managementMeReader)
	if !ok || !auth.ValidPathSegment(actorID) {
		return time.Time{}, authconsumer.ErrUnavailable
	}
	result, err := reader.ManagementMe(ctx, credential)
	if err != nil {
		return time.Time{}, err
	}
	if result.Subject != actorID {
		return time.Time{}, authconsumer.ErrInvalidCredential
	}
	for _, scope := range result.GrantedScopes {
		if scope == auth.ManagementScopeLWCAdmin {
			return time.Now().UTC(), nil
		}
	}
	return time.Time{}, authconsumer.ErrForbidden
}

func writeAdminBindingAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authconsumer.ErrInvalidCredential):
		writeAdminBindingError(c, http.StatusUnauthorized, "invalid_credential", false, "", nil)
	case errors.Is(err, authconsumer.ErrForbidden):
		writeAdminBindingError(c, http.StatusForbidden, "management_scope_required", false, "", nil)
	default:
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, "", nil)
	}
}

func writeAdminBindingProjectError(c *gin.Context, err error) {
	if errors.Is(err, errInvalidAdminProjectRecord) {
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, "", nil)
		return
	}
	if status.Code(err) == codes.NotFound {
		writeAdminBindingError(c, http.StatusNotFound, "target_not_found", false, "", nil)
		return
	}
	writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, "", nil)
}

func writeAdminBindingAuthorityError(c *gin.Context, err error, operationID string) {
	switch {
	case errors.Is(err, auth.ErrSyncBindingNotFound):
		writeAdminBindingError(c, http.StatusNotFound, "target_not_found", false, operationID, nil)
	case errors.Is(err, auth.ErrSyncBindingConflict):
		writeAdminBindingError(c, http.StatusConflict, "target_changed", false, operationID, nil)
	case errors.Is(err, auth.ErrSyncBindingOperationConflict):
		writeAdminBindingError(c, http.StatusConflict, "operation_conflict", false, operationID, nil)
	case errors.Is(err, auth.ErrSyncBindingUnauthorized), errors.Is(err, authconsumer.ErrForbidden):
		writeAdminBindingError(c, http.StatusForbidden, "management_scope_required", false, operationID, nil)
	case errors.Is(err, authconsumer.ErrInvalidCredential):
		writeAdminBindingError(c, http.StatusUnauthorized, "invalid_credential", false, operationID, nil)
	case errors.Is(err, auth.ErrSyncBindingInvalid):
		writeAdminBindingError(c, http.StatusBadRequest, "invalid_request", false, operationID, nil)
	default:
		writeAdminBindingError(c, http.StatusServiceUnavailable, "authority_unavailable", true, operationID, nil)
	}
}

func writeAdminBindingError(c *gin.Context, statusCode int, code string, retryable bool, operationID string, cause error) {
	c.Abort()
	c.Header("Cache-Control", "no-store")
	var operation any
	if operationID != "" {
		operation = operationID
	}
	message := map[string]string{"invalid_request": "請求格式無效", "invalid_credential": "登入憑證無效", "management_scope_required": "需要目前有效的 LWC 管理權限",
		"cross_origin_rejected": "管理請求來源無效", "target_not_found": "找不到指定的 Project 或同步綁定", "target_changed": "目標已變更，請重新查詢並確認",
		"operation_conflict": "操作識別碼已用於不同請求", "request_too_large": "請求內容超過限制", "rate_limited": "操作次數過多，請稍後重試",
		"authority_unavailable": "管理權威暫時無法使用，請稍後重試"}[code]
	c.JSON(statusCode, gin.H{"error": code, "message": message, "retryable": retryable, "operation_id": operation})
}

func (h *Handler) adminSyncBindingAuthorityEnvironment() string {
	if h.adminSyncBindingAuthority == nil {
		return ""
	}
	return h.adminSyncBindingAuthority.Environment()
}

func validAdminBindingReason(reason string) bool {
	if utf8.RuneCountInString(reason) == 0 || utf8.RuneCountInString(reason) > 256 {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
