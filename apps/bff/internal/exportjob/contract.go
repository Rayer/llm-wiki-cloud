package exportjob

import (
	"errors"
	"strings"
	"time"
)

const (
	ScopeRaw             Scope = "raw"
	ScopeRawFull         Scope = "raw-full"
	ScopeRawFullMetadata Scope = "raw-full-metadata"

	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
	StatusExpired Status = "expired"

	RejectInProgress         RejectionReason = "in_progress"
	RejectCooldown           RejectionReason = "cooldown"
	RejectNotFound           RejectionReason = "not_found"
	RejectForbidden          RejectionReason = "forbidden"
	RejectExpired            RejectionReason = "expired"
	RejectStorageUnavailable RejectionReason = "storage_unavailable"
	RejectWorkerFailure      RejectionReason = "worker_failure"
)

const (
	Retention   = 72 * time.Hour
	Cooldown    = 24 * time.Hour
	HardTimeout = 22 * time.Hour
	JobDeadline = 23 * time.Hour
)

var ErrInvalidScope = errors.New("invalid export scope")
var ErrInvalidPath = errors.New("invalid export object path")

type Scope string

func (s Scope) Valid() bool {
	switch s {
	case ScopeRaw, ScopeRawFull, ScopeRawFullMetadata:
		return true
	default:
		return false
	}
}

type Status string
type RejectionReason string

type CreateRequest struct {
	Scope Scope `json:"scope"`
}

type Job struct {
	ExportID          string     `json:"export_id"`
	Scope             Scope      `json:"scope"`
	Status            Status     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	SnapshotAt        *time.Time `json:"snapshot_at"`
	CompletedAt       *time.Time `json:"completed_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	NextAllowedAt     *time.Time `json:"next_allowed_at"`
	SizeBytes         *int64     `json:"size_bytes"`
	DownloadAvailable bool       `json:"download_available"`
	ErrorCode         *string    `json:"error_code"`
	ErrorMessage      *string    `json:"error_message"`
	DeadlineAt        time.Time  `json:"-"`
}

type Archive struct {
	ExportID          string    `json:"export_id"`
	Scope             Scope     `json:"scope"`
	Status            Status    `json:"status"`
	SnapshotAt        time.Time `json:"snapshot_at"`
	CompletedAt       time.Time `json:"completed_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	SizeBytes         int64     `json:"size_bytes"`
	DownloadAvailable bool      `json:"download_available"`
}

type State struct {
	LatestJob       *Job             `json:"latest_job"`
	Current         *Archive         `json:"current"`
	Previous        *Archive         `json:"previous"`
	Eligible        bool             `json:"eligible"`
	RejectionReason *RejectionReason `json:"rejection_reason"`
	NextAllowedAt   *time.Time       `json:"next_allowed_at"`
}

type Download struct {
	ExportID  string    `json:"export_id"`
	SignedURL string    `json:"signed_url"`
	ExpiresAt time.Time `json:"expires_at"`
	Filename  string    `json:"filename"`
}

type ExportMeta struct {
	FormatVersion  int                 `json:"format_version"`
	ExportID       string              `json:"export_id"`
	ProjectID      string              `json:"project_id"`
	SnapshotAt     time.Time           `json:"snapshot_at"`
	Scope          Scope               `json:"scope"`
	SourceVersions map[string]string   `json:"source_versions,omitempty"`
	RedactedFields map[string][]string `json:"redacted_fields,omitempty"`
	Files          []FileDigest        `json:"files"`
}

type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

func ExpiryFromCustomTime(customTime time.Time) time.Time {
	return customTime.UTC().Add(Retention)
}

func ReadyObject(userID, projectID, exportID string) (string, error) {
	if !validSegment(userID) || !validSegment(projectID) || !validSegment(exportID) {
		return "", ErrInvalidPath
	}
	return "exports/ready/" + userID + "/" + projectID + "/" + exportID + "/archive.zip", nil
}

func TempPrefix(userID, projectID, exportID string) (string, error) {
	if !validSegment(userID) || !validSegment(projectID) || !validSegment(exportID) {
		return "", ErrInvalidPath
	}
	return "exports/tmp/" + userID + "/" + projectID + "/" + exportID + "/", nil
}

func validSegment(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
