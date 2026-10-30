package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxPipelineAutomationBody = 8 << 10
	pipelineAutomationDoc     = "pipeline_automation"
	pipelineAutomationScans   = "pipeline_automation_scans"
)

var (
	errPipelineAutomationRevision = errors.New("pipeline automation settings changed")
	errPipelineAutomationBodySize = errors.New("pipeline automation request is too large")
)

type pipelineAutomationAdminSettings struct {
	AllowAutoPipeline bool      `json:"allow_auto_pipeline"`
	Revision          int64     `json:"revision"`
	BacklogScanStatus string    `json:"backlog_scan_status"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type pipelineAutomationProjectSettings struct {
	Enabled           bool  `json:"enabled"`
	Revision          int64 `json:"revision"`
	adminRevision     int64
	AdminAllowed      bool   `json:"admin_allowed"`
	EffectiveEnabled  bool   `json:"effective_enabled"`
	BlockedReason     string `json:"blocked_reason,omitempty"`
	BacklogScanStatus string `json:"backlog_scan_status"`
	PipelineStatus    string `json:"pipeline_status"`
	SourceRevision    int64  `json:"source_revision"`
	ProcessedRevision int64  `json:"processed_revision"`
	FailureStage      string `json:"failure_stage,omitempty"`
	FailureCode       string `json:"failure_code,omitempty"`
}

type adminPipelineAutomationRequest struct {
	AllowAutoPipeline *bool  `json:"allow_auto_pipeline"`
	Revision          *int64 `json:"revision"`
}

type projectPipelineAutomationRequest struct {
	Enabled  *bool  `json:"enabled"`
	Revision *int64 `json:"revision"`
}

type pipelineAutomationSetting struct {
	allow         bool
	revision      int64
	backlogStatus string
	updatedAt     time.Time
}

func (h *Handler) AdminGetPipelineAutomation(c *gin.Context) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	settings, err := readPipelineAutomationAdmin(c.Request.Context(), h.firestore.Raw())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	c.JSON(http.StatusOK, pipelineAutomationAdminResponse(settings))
}

func (h *Handler) AdminPutPipelineAutomation(c *gin.Context) {
	var request adminPipelineAutomationRequest
	if err := decodePipelineAutomationJSON(c, &request); err != nil || request.AllowAutoPipeline == nil || request.Revision == nil || *request.Revision < 0 {
		writePipelineAutomationInputError(c, err)
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	settings, err := updatePipelineAutomationAdmin(c.Request.Context(), h.firestore.Raw(), *request.AllowAutoPipeline, *request.Revision)
	if errors.Is(err, errPipelineAutomationRevision) {
		c.JSON(http.StatusConflict, gin.H{"error": "pipeline automation revision conflict"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	c.JSON(http.StatusOK, pipelineAutomationAdminResponse(settings))
}

func (h *Handler) GetProjectPipelineAutomation(c *gin.Context) {
	ownerID, projectID, ok := pipelineAutomationProjectIdentity(c)
	if !ok {
		return
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), ownerID, projectID, ProjectRead); err != nil {
		writePipelineAutomationProjectError(c, err)
		return
	}
	settings, err := readPipelineAutomationProject(c.Request.Context(), h.firestore.Raw(), ownerID, projectID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h *Handler) PutProjectPipelineAutomation(c *gin.Context) {
	ownerID, projectID, ok := pipelineAutomationProjectIdentity(c)
	if !ok {
		return
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), ownerID, projectID, ProjectEdit); err != nil {
		writePipelineAutomationProjectError(c, err)
		return
	}
	var request projectPipelineAutomationRequest
	if err := decodePipelineAutomationJSON(c, &request); err != nil || request.Enabled == nil || request.Revision == nil || *request.Revision < 0 {
		writePipelineAutomationInputError(c, err)
		return
	}
	settings, err := updatePipelineAutomationProject(c.Request.Context(), h.firestore.Raw(), ownerID, projectID, *request.Enabled, *request.Revision)
	if errors.Is(err, errPipelineAutomationRevision) {
		c.JSON(http.StatusConflict, gin.H{"error": "pipeline automation revision conflict"})
		return
	}
	if errors.Is(err, errProfileProjectNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pipeline automation settings unavailable"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func pipelineAutomationProjectIdentity(c *gin.Context) (string, string, bool) {
	ownerID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.Param("pid"))
	if ownerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return "", "", false
	}
	if !auth.ValidPathSegment(projectID) || projectID != strings.TrimSpace(c.GetString("projectID")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return "", "", false
	}
	return ownerID, projectID, true
}

func decodePipelineAutomationJSON(c *gin.Context, target any) error {
	if c.Request.Body == nil {
		return errors.New("missing JSON body")
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, maxPipelineAutomationBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxPipelineAutomationBody {
		return errPipelineAutomationBodySize
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("expected JSON object")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid JSON object key")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate JSON object key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return decodeStrictJSON(bytes.NewReader(data), target)
}

func writePipelineAutomationInputError(c *gin.Context, err error) {
	if errors.Is(err, errPipelineAutomationBodySize) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "pipeline automation request is too large"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "valid pipeline automation settings are required"})
}

func writePipelineAutomationProjectError(c *gin.Context, err error) {
	if errors.Is(err, errProfileProjectNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "project settings unavailable"})
}

func pipelineAutomationAdminRef(fs *cloudfirestore.Client) *cloudfirestore.DocumentRef {
	return scopedfirestore.Collection(fs, "system").Doc(pipelineAutomationDoc)
}

func readPipelineAutomationAdmin(ctx context.Context, fs *cloudfirestore.Client) (pipelineAutomationSetting, error) {
	doc, err := pipelineAutomationAdminRef(fs).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return pipelineAutomationSetting{backlogStatus: "idle"}, nil
	}
	if err != nil {
		return pipelineAutomationSetting{}, err
	}
	return pipelineAutomationSettingFromData(doc.Data()), nil
}

func pipelineAutomationSettingFromData(data map[string]interface{}) pipelineAutomationSetting {
	setting := pipelineAutomationSetting{backlogStatus: "idle"}
	setting.allow, _ = data["allow_auto_pipeline"].(bool)
	setting.revision, _ = data["revision"].(int64)
	setting.backlogStatus, _ = data["backlog_scan_status"].(string)
	if setting.backlogStatus == "" {
		setting.backlogStatus = "idle"
	}
	setting.updatedAt, _ = data["updated_at"].(time.Time)
	return setting
}

func updatePipelineAutomationAdmin(ctx context.Context, fs *cloudfirestore.Client, enabled bool, expectedRevision int64) (pipelineAutomationSetting, error) {
	ref := pipelineAutomationAdminRef(fs)
	result := pipelineAutomationSetting{}
	err := fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		result = pipelineAutomationSetting{backlogStatus: "idle"}
		snapshot, err := tx.Get(ref)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if snapshot != nil && snapshot.Exists() {
			result = pipelineAutomationSettingFromData(snapshot.Data())
		}
		if result.revision != expectedRevision {
			return errPipelineAutomationRevision
		}
		if result.allow == enabled {
			return nil
		}
		result.allow = enabled
		result.revision++
		result.updatedAt = time.Now().UTC()
		if enabled {
			result.backlogStatus = "pending"
			work := profileruntime.Work{
				UserID: "system", ProjectID: "automation", Kind: "pipeline_automation_scan",
				Revision: result.revision, ID: "scan-" + strconv.FormatInt(result.revision, 10), Due: result.updatedAt,
			}
			if err := tx.Set(scopedfirestore.Collection(fs, pipelineAutomationScans).Doc(strconv.FormatInt(result.revision, 10)), map[string]interface{}{
				"revision": result.revision, "cursor": "", "status": "pending", "updated_at": result.updatedAt,
			}); err != nil {
				return err
			}
			if err := profileruntime.Enqueue(tx, fs, work); err != nil {
				return err
			}
		} else {
			result.backlogStatus = "paused"
		}
		return tx.Set(ref, map[string]interface{}{
			"schema_version": 1, "allow_auto_pipeline": enabled, "revision": result.revision,
			"backlog_scan_status": result.backlogStatus, "updated_at": result.updatedAt,
		}, cloudfirestore.MergeAll)
	})
	return result, err
}

func pipelineAutomationProjectRefs(fs *cloudfirestore.Client, ownerID, projectID string) (*cloudfirestore.DocumentRef, *cloudfirestore.DocumentRef) {
	project := scopedfirestore.Collection(fs, "projects").Doc(projectDocID(ownerID, projectID))
	return project, pipelineAutomationAdminRef(fs)
}

func readPipelineAutomationProject(ctx context.Context, fs *cloudfirestore.Client, ownerID, projectID string) (pipelineAutomationProjectSettings, error) {
	projectRef, adminRef := pipelineAutomationProjectRefs(fs, ownerID, projectID)
	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil {
		return pipelineAutomationProjectSettings{}, err
	}
	adminSnapshot, err := adminRef.Get(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return pipelineAutomationProjectSettings{}, err
	}
	projectData := projectSnapshot.Data()
	if !projectMatchesOwner(projectRef.ID, projectData, ownerID, projectID) {
		return pipelineAutomationProjectSettings{}, errProfileProjectNotFound
	}
	admin := pipelineAutomationSetting{backlogStatus: "idle"}
	if adminSnapshot != nil && adminSnapshot.Exists() {
		admin = pipelineAutomationSettingFromData(adminSnapshot.Data())
	}
	return projectPipelineAutomationFromData(projectData, admin), nil
}

func updatePipelineAutomationProject(ctx context.Context, fs *cloudfirestore.Client, ownerID, projectID string, enabled bool, expectedRevision int64) (pipelineAutomationProjectSettings, error) {
	projectRef, adminRef := pipelineAutomationProjectRefs(fs, ownerID, projectID)
	result := pipelineAutomationProjectSettings{}
	now := time.Now().UTC()
	err := fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		projectSnapshot, err := tx.Get(projectRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return errProfileProjectNotFound
			}
			return err
		}
		adminSnapshot, adminErr := tx.Get(adminRef)
		if adminErr != nil && status.Code(adminErr) != codes.NotFound {
			return adminErr
		}
		projectData := projectSnapshot.Data()
		if !projectMatchesOwner(projectRef.ID, projectData, ownerID, projectID) {
			return errProfileProjectNotFound
		}
		admin := pipelineAutomationSetting{backlogStatus: "idle"}
		if adminSnapshot != nil && adminSnapshot.Exists() {
			admin = pipelineAutomationSettingFromData(adminSnapshot.Data())
		}
		result = projectPipelineAutomationFromData(projectData, admin)
		if result.Revision != expectedRevision {
			return errPipelineAutomationRevision
		}
		if result.Enabled == enabled {
			return nil
		}
		result.Enabled = enabled
		result.Revision++
		if enabled {
			result.BacklogScanStatus = "pending"
			work := profileruntime.Work{
				UserID: ownerID, ProjectID: projectID, Kind: "pipeline_automation",
				Revision: result.Revision, ID: "project-toggle-" + strconv.FormatInt(result.Revision, 10), Due: now,
			}
			if err := profileruntime.Enqueue(tx, fs, work); err != nil {
				return err
			}
		} else {
			result.BacklogScanStatus = "paused"
		}
		updates := []cloudfirestore.Update{
			{Path: "auto_pipeline_enabled", Value: enabled},
			{Path: "auto_pipeline_revision", Value: result.Revision},
			{Path: "pipeline_backlog_scan_status", Value: result.BacklogScanStatus},
			{Path: "updated_at", Value: now},
		}
		if enabled {
			updates = append(updates,
				cloudfirestore.Update{Path: "pipeline_failure_blocked_fingerprint", Value: cloudfirestore.Delete},
				cloudfirestore.Update{Path: "pipeline_failure_blocked_admin_revision", Value: cloudfirestore.Delete},
				cloudfirestore.Update{Path: "pipeline_automation_epoch", Value: result.Revision},
				cloudfirestore.Update{Path: "pipeline_automation_status", Value: "pending"},
			)
		}
		if err := tx.Update(projectRef, updates); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return pipelineAutomationProjectSettings{}, err
	}
	return readPipelineAutomationProject(ctx, fs, ownerID, projectID)
}

func projectPipelineAutomationFromData(project map[string]interface{}, admin pipelineAutomationSetting) pipelineAutomationProjectSettings {
	result := pipelineAutomationProjectSettings{AdminAllowed: admin.allow, adminRevision: admin.revision}
	result.Enabled, _ = project["auto_pipeline_enabled"].(bool)
	result.Revision, _ = project["auto_pipeline_revision"].(int64)
	result.EffectiveEnabled = result.AdminAllowed && result.Enabled
	result.BacklogScanStatus, _ = project["pipeline_backlog_scan_status"].(string)
	if result.BacklogScanStatus == "" {
		result.BacklogScanStatus = "idle"
	}
	result.PipelineStatus, _ = project["pipeline_automation_status"].(string)
	result.SourceRevision, _ = project["pipeline_source_revision"].(int64)
	result.ProcessedRevision, _ = project["pipeline_auto_consumed_source_revision"].(int64)
	result.FailureStage, _ = project["pipeline_automation_failure_stage"].(string)
	result.FailureCode, _ = project["pipeline_automation_failure_code"].(string)
	if !result.AdminAllowed {
		result.BlockedReason = "admin_disabled"
		result.BacklogScanStatus = "blocked_admin_disabled"
	} else if !result.Enabled {
		result.BlockedReason = "project_disabled"
	}
	return result
}

func pipelineAutomationAdminResponse(setting pipelineAutomationSetting) pipelineAutomationAdminSettings {
	return pipelineAutomationAdminSettings{AllowAutoPipeline: setting.allow, Revision: setting.revision,
		BacklogScanStatus: setting.backlogStatus, UpdatedAt: setting.updatedAt}
}

func projectMatchesOwner(docID string, data map[string]interface{}, ownerID, projectID string) bool {
	return docID == projectDocID(ownerID, projectID) && data["user_id"] == ownerID && data["project_id"] == projectID
}
