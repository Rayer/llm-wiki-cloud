package documentimport

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion       = 1
	ConverterVersion    = "0.1.9"
	MaxOriginalBytes    = 5 << 20
	MaxFilenameBytes    = 512
	MaxMarkdownBytes    = 5 << 20
	OriginalObjectRoot  = ".lwc/document-imports"
	FailureStageConvert = "conversion"
)

var (
	ErrInvalidFilename      = errors.New("invalid filename")
	ErrUnsupportedFile      = errors.New("unsupported file type")
	ErrEmptyFile            = errors.New("empty file")
	ErrFileTooLarge         = errors.New("file too large")
	ErrInvalidContent       = errors.New("file content does not match its extension")
	ErrInvalidSubmissionID  = errors.New("invalid client submission id")
	ErrArchiveResourceLimit = errors.New("office archive exceeds safe processing limits")
	ErrNoReadableText       = errors.New("no readable text")
	ErrConflict             = errors.New("document import conflict")
	ErrNotFound             = errors.New("document import not found")
	ErrStateConflict        = errors.New("document import state conflict")
)

type ConversionState string

const (
	ConversionReceiving  ConversionState = "receiving"
	ConversionAccepted   ConversionState = "accepted"
	ConversionQueued     ConversionState = "queued"
	ConversionConverting ConversionState = "converting"
	ConversionSucceeded  ConversionState = "succeeded"
	ConversionFailed     ConversionState = "failed"
)

type SourceState string

const (
	SourceNotStarted SourceState = "not_started"
	SourceSaving     SourceState = "saving"
	SourceSaved      SourceState = "saved"
	SourceFailed     SourceState = "failed"
	SourceUnknown    SourceState = "unknown"
)

type DeletionState string

const (
	DeletionPresent    DeletionState = "present"
	DeletionInProgress DeletionState = "deleting"
	DeletionFailed     DeletionState = "delete_failed"
	DeletionComplete   DeletionState = "deleted"
)

type Failure struct {
	Stage          string `json:"stage" firestore:"stage"`
	Class          string `json:"class" firestore:"class"`
	SafeMessageKey string `json:"safe_message_key" firestore:"safe_message_key"`
	DiagnosticID   string `json:"diagnostic_id,omitempty" firestore:"diagnostic_id,omitempty"`
	Retryable      bool   `json:"retryable" firestore:"retryable"`
}

type SourceRef struct {
	RawPath          string `json:"raw_path" firestore:"raw_path"`
	SHA256           string `json:"sha256" firestore:"sha256"`
	ObjectGeneration int64  `json:"object_generation" firestore:"object_generation"`
	SourceID         string `json:"source_id,omitempty" firestore:"source_id,omitempty"`
}

type PipelineRef struct {
	Status            string `json:"status" firestore:"status"`
	IntentID          string `json:"intent_id,omitempty" firestore:"intent_id,omitempty"`
	ExecutionID       string `json:"execution_id,omitempty" firestore:"execution_id,omitempty"`
	SourceRevision    int64  `json:"source_revision,omitempty" firestore:"source_revision,omitempty"`
	ContentGeneration string `json:"content_generation,omitempty" firestore:"content_generation,omitempty"`
	Reason            string `json:"reason,omitempty" firestore:"reason,omitempty"`
}

// Record is the single durable identity for one document submission. Storage
// and execution fields are persisted but never exposed by the HTTP/MCP reader.
type Record struct {
	SchemaVersion        int             `json:"-" firestore:"schema_version"`
	OwnerID              string          `json:"-" firestore:"owner_id"`
	ProjectID            string          `json:"-" firestore:"project_id"`
	DocumentID           string          `json:"document_id" firestore:"document_id"`
	ClientSubmissionID   string          `json:"-" firestore:"client_submission_id"`
	BatchID              string          `json:"batch_id,omitempty" firestore:"batch_id,omitempty"`
	OriginalFilename     string          `json:"original_filename" firestore:"original_filename"`
	MediaType            string          `json:"media_type" firestore:"media_type"`
	Bytes                int64           `json:"bytes" firestore:"bytes"`
	SHA256               string          `json:"sha256" firestore:"sha256"`
	OriginalObjectPath   string          `json:"-" firestore:"original_object_path"`
	OriginalGeneration   int64           `json:"-" firestore:"original_object_generation"`
	CreatedAt            time.Time       `json:"created_at" firestore:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at" firestore:"updated_at"`
	Revision             int64           `json:"revision" firestore:"revision"`
	ConverterVersion     string          `json:"converter_version" firestore:"converter_version"`
	DependencyLockDigest string          `json:"dependency_lock_digest,omitempty" firestore:"dependency_lock_digest,omitempty"`
	ConversionState      ConversionState `json:"conversion_state" firestore:"conversion_state"`
	SourceState          SourceState     `json:"source_state" firestore:"source_state"`
	Failure              *Failure        `json:"failure,omitempty" firestore:"failure,omitempty"`
	SourceRef            *SourceRef      `json:"source_ref,omitempty" firestore:"source_ref,omitempty"`
	PipelineRef          *PipelineRef    `json:"pipeline_ref" firestore:"pipeline_ref"`
	SourceOperationID    string          `json:"-" firestore:"source_operation_id,omitempty"`
	FailureSequence      int64           `json:"failure_sequence,omitempty" firestore:"failure_sequence,omitempty"`
	DeletionState        DeletionState   `json:"deletion_state" firestore:"deletion_state"`
	AttemptCount         int             `json:"-" firestore:"attempt_count"`
	AttemptID            string          `json:"-" firestore:"attempt_id,omitempty"`
	LeaseToken           string          `json:"-" firestore:"lease_token,omitempty"`
	LeaseUntil           time.Time       `json:"-" firestore:"lease_until,omitempty"`
	DispatchState        string          `json:"-" firestore:"dispatch_state"`
	JobExecutionID       string          `json:"-" firestore:"job_execution_id,omitempty"`
	MarkdownObjectPath   string          `json:"-" firestore:"markdown_object_path,omitempty"`
	MarkdownGeneration   int64           `json:"-" firestore:"markdown_object_generation,omitempty"`
	MarkdownSHA256       string          `json:"-" firestore:"markdown_sha256,omitempty"`
}

type Summary struct {
	FailedCount              int64  `json:"failed_count"`
	LatestFailureSequence    int64  `json:"latest_failure_sequence"`
	ReminderVisible          bool   `json:"reminder_visible"`
	DismissedThroughSequence int64  `json:"dismissed_through_sequence"`
	SiteURL                  string `json:"site_url"`
	SnapshotRevision         int64  `json:"snapshot_revision"`
}

type ListResult struct {
	Items                 []Record `json:"items"`
	NextCursor            string   `json:"next_cursor,omitempty"`
	SnapshotRevision      int64    `json:"snapshot_revision"`
	FailedCount           int64    `json:"failed_count"`
	LatestFailureSequence int64    `json:"latest_failure_sequence"`
}

type Upload struct {
	Filename string
	Bytes    []byte
}

func ValidateUpload(filename string, data []byte) (string, string, error) {
	if !safeFilename(filename) {
		return "", "", ErrInvalidFilename
	}
	if len(data) == 0 {
		return "", "", ErrEmptyFile
	}
	if len(data) > MaxOriginalBytes {
		return "", "", ErrFileTooLarge
	}
	ext := strings.ToLower(filepath.Ext(filename))
	mediaType, err := mediaTypeForExtension(ext)
	if err != nil {
		return "", "", err
	}
	if err := validateSignature(ext, data); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	return mediaType, fmt.Sprintf("%x", sum[:]), nil
}

func IsSupportedFilename(filename string) bool {
	_, err := mediaTypeForExtension(strings.ToLower(filepath.Ext(filename)))
	return err == nil
}

// ValidateSubmissionID accepts opaque, bounded identifiers while rejecting
// characters that could be misinterpreted as paths, headers, or log controls.
func ValidateSubmissionID(value string) error {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return ErrInvalidSubmissionID
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f || strings.ContainsRune(`/\\`, r) || r >= 0x80 && r <= 0x9f {
			return ErrInvalidSubmissionID
		}
	}
	return nil
}

func safeFilename(filename string) bool {
	if filename == "" || !utf8.ValidString(filename) || len(filename) > MaxFilenameBytes ||
		filename == "." || filename == ".." || strings.TrimSpace(filename) != filename || strings.ContainsAny(filename, `/\\`) {
		return false
	}
	for _, r := range filename {
		if r < 32 || r >= 0x7f && r <= 0x9f || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func mediaTypeForExtension(ext string) (string, error) {
	switch ext {
	case ".pdf":
		return "application/pdf", nil
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case ".xls":
		return "application/vnd.ms-excel", nil
	default:
		return "", ErrUnsupportedFile
	}
}

func validateSignature(ext string, data []byte) error {
	switch ext {
	case ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return ErrInvalidContent
		}
	case ".xls":
		if !bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
			return ErrInvalidContent
		}
	case ".docx", ".pptx", ".xlsx":
		if len(data) < 4 || !bytes.Equal(data[:4], []byte{'P', 'K', 3, 4}) {
			return ErrInvalidContent
		}
		zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return ErrInvalidContent
		}
		var contentType string
		foundContentTypes := false
		if len(zipReader.File) > 2000 {
			return ErrArchiveResourceLimit
		}
		var uncompressed uint64
		for _, entry := range zipReader.File {
			if uncompressed > 64<<20 || entry.UncompressedSize64 > (64<<20)-uncompressed {
				return ErrArchiveResourceLimit
			}
			uncompressed += entry.UncompressedSize64
			if entry.Name != "[Content_Types].xml" {
				continue
			}
			if foundContentTypes || entry.UncompressedSize64 > 1<<20 {
				return ErrInvalidContent
			}
			foundContentTypes = true
			reader, err := entry.Open()
			if err != nil {
				return ErrInvalidContent
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || len(body) > 1<<20 {
				return ErrInvalidContent
			}
			contentType = string(body)
		}
		want := map[string]string{
			".docx": "wordprocessingml.document.main+xml",
			".pptx": "presentationml.presentation.main+xml",
			".xlsx": "spreadsheetml.sheet.main+xml",
		}[ext]
		if !foundContentTypes || !strings.Contains(contentType, want) {
			return ErrInvalidContent
		}
	}
	return nil
}

func FailedCounted(record Record) bool {
	return record.ConversionState == ConversionFailed && record.DeletionState != DeletionComplete
}

func PublicRecord(record Record) Record {
	record.OwnerID = ""
	record.ProjectID = ""
	record.ClientSubmissionID = ""
	record.OriginalObjectPath = ""
	record.OriginalGeneration = 0
	record.AttemptCount = 0
	record.AttemptID = ""
	record.LeaseToken = ""
	record.LeaseUntil = time.Time{}
	record.DispatchState = ""
	record.JobExecutionID = ""
	record.MarkdownObjectPath = ""
	record.MarkdownGeneration = 0
	record.MarkdownSHA256 = ""
	return record
}

func OriginalPath(documentID string) string {
	return OriginalObjectRoot + "/" + documentID + "/original"
}

func MarkdownPath(documentID, attemptID string) string {
	return OriginalObjectRoot + "/" + documentID + "/attempts/" + attemptID + "/source.md"
}

func ContentType(filename string) string {
	if value, err := mediaTypeForExtension(strings.ToLower(filepath.Ext(filename))); err == nil {
		return value
	}
	return "application/octet-stream"
}
