package nudge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	CategoryPipelineFailure = "pipeline-failure"
	CategoryReleaseNotes    = "release-notes"
	CategoryPurposeGap      = "content-purpose-gap"
	SchemaVersion           = 1
	MaxTextRunes            = 1000
	PurposeCooldown         = 7 * 24 * time.Hour
)

var ErrSourceRevisionConflict = errors.New("nudge source revision conflict")
var ErrAttentionConflict = errors.New("nudge attention revision conflict")
var ErrAttentionNotDismissible = errors.New("urgent attention is not dismissible")
var ErrInvalidInput = errors.New("invalid nudge input")

type Severity string

const (
	SeverityUrgent    Severity = "urgent"
	SeverityAttention Severity = "attention"
	SeverityInfo      Severity = "info"
)

type AttentionState string

const (
	AttentionUnread    AttentionState = "unread"
	AttentionRead      AttentionState = "read"
	AttentionDismissed AttentionState = "dismissed"
)

type Target struct {
	Kind        string `json:"kind" firestore:"kind"`
	ProjectID   string `json:"project_id,omitempty" firestore:"project_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty" firestore:"execution_id,omitempty"`
	Mode        string `json:"mode,omitempty" firestore:"mode,omitempty"`
	Version     string `json:"version,omitempty" firestore:"version,omitempty"`
}

type Action struct {
	ID       string  `json:"id" firestore:"id"`
	Kind     string  `json:"kind" firestore:"kind"`
	Label    string  `json:"label" firestore:"label"`
	CopyText string  `json:"copy_text,omitempty" firestore:"copy_text,omitempty"`
	Target   *Target `json:"target,omitempty" firestore:"target,omitempty"`
}

type Observation struct {
	ProjectID        string     `json:"project_id,omitempty" firestore:"project_id,omitempty"`
	ProjectName      string     `json:"project_name,omitempty" firestore:"project_name,omitempty"`
	ExecutionID      string     `json:"execution_id,omitempty" firestore:"execution_id,omitempty"`
	Title            string     `json:"title" firestore:"title"`
	Body             string     `json:"body" firestore:"body"`
	DeclaredPurpose  string     `json:"declared_purpose,omitempty" firestore:"declared_purpose,omitempty"`
	PublishedSummary string     `json:"published_summary,omitempty" firestore:"published_summary,omitempty"`
	Coverage         Coverage   `json:"coverage,omitempty" firestore:"coverage"`
	Severity         Severity   `json:"severity" firestore:"severity"`
	Actions          []Action   `json:"actions" firestore:"actions"`
	Stale            bool       `json:"stale,omitempty" firestore:"stale,omitempty"`
	ObservedAt       *time.Time `json:"observed_at,omitempty" firestore:"observed_at,omitempty"`
}

type Coverage struct {
	TotalConcepts          int  `json:"total_concepts" firestore:"total_concepts"`
	SelectedConcepts       int  `json:"selected_concepts" firestore:"selected_concepts"`
	Sampled                bool `json:"sampled" firestore:"sampled"`
	TitleTruncatedConcepts int  `json:"title_truncated_concepts" firestore:"title_truncated_concepts"`
	BodyTruncatedConcepts  int  `json:"body_truncated_concepts" firestore:"body_truncated_concepts"`
}

// ProducerWitness is a tagged union. Fields irrelevant to Kind must remain empty.
type ProducerWitness struct {
	Kind                    string    `json:"kind" firestore:"kind"`
	ProjectID               string    `json:"project_id,omitempty" firestore:"project_id,omitempty"`
	ProjectPipelineSequence int64     `json:"project_pipeline_sequence,omitempty" firestore:"project_pipeline_sequence,omitempty"`
	ReservationID           string    `json:"reservation_id,omitempty" firestore:"reservation_id,omitempty"`
	AttemptID               string    `json:"attempt_id,omitempty" firestore:"attempt_id,omitempty"`
	ExecutionID             string    `json:"execution_id,omitempty" firestore:"execution_id,omitempty"`
	Trigger                 string    `json:"trigger,omitempty" firestore:"trigger,omitempty"`
	Mode                    string    `json:"mode,omitempty" firestore:"mode,omitempty"`
	TerminalClass           string    `json:"terminal_class,omitempty" firestore:"terminal_class,omitempty"`
	Environment             string    `json:"environment,omitempty" firestore:"environment,omitempty"`
	IndexRevision           int64     `json:"index_revision,omitempty" firestore:"index_revision,omitempty"`
	ProductVersion          string    `json:"product_version,omitempty" firestore:"product_version,omitempty"`
	IndexDigest             string    `json:"index_digest,omitempty" firestore:"index_digest,omitempty"`
	ConfirmationID          string    `json:"confirmation_id,omitempty" firestore:"confirmation_id,omitempty"`
	PairKey                 string    `json:"pair_key,omitempty" firestore:"pair_key,omitempty"`
	DescriptionRevision     int64     `json:"description_revision,omitempty" firestore:"description_revision,omitempty"`
	PublicationGeneration   string    `json:"publication_generation,omitempty" firestore:"publication_generation,omitempty"`
	ConceptsDigest          string    `json:"concepts_digest,omitempty" firestore:"concepts_digest,omitempty"`
	SummaryInputDigest      string    `json:"summary_input_digest,omitempty" firestore:"summary_input_digest,omitempty"`
	AttemptStartedAt        time.Time `json:"attempt_started_at,omitempty" firestore:"attempt_started_at,omitempty"`
}

type ObservationInput struct {
	Category                  string          `json:"category"`
	SourceKey                 string          `json:"source_key"`
	ExpectedSourceRevision    int64           `json:"expected_source_revision"`
	ProducerWitness           ProducerWitness `json:"producer_witness"`
	PayloadDigest             string          `json:"payload_digest"`
	AttentionMaterialRevision string          `json:"attention_material_revision"`
	Observation               Observation     `json:"observation"`
	ConfirmationID            string          `json:"confirmation_id,omitempty"`
	EligibilityStartedAt      *time.Time      `json:"eligibility_started_at,omitempty"`
	ConflictedComparison      bool            `json:"conflicted_comparison,omitempty"`
}

type ClearInput struct {
	Category               string          `json:"category"`
	SourceKey              string          `json:"source_key"`
	ExpectedSourceRevision int64           `json:"expected_source_revision"`
	ProducerWitness        ProducerWitness `json:"producer_witness"`
	Proof                  string          `json:"proof"`
	ConfirmationID         string          `json:"confirmation_id,omitempty"`
}

type SourceResult struct {
	Status              string             `json:"status"`
	Upserts             []ObservationInput `json:"upserts"`
	AuthoritativeClears []ClearInput       `json:"authoritative_clears"`
	Failures            []SourceFailure    `json:"failures"`
	Notices             []SourceNotice     `json:"notices"`
}

type SourceFailure struct {
	ProjectID string `json:"project_id,omitempty"`
	SafeClass string `json:"safe_class"`
}

type SourceNotice struct {
	SourceKey string `json:"source_key"`
	State     string `json:"state"`
	SafeClass string `json:"safe_class,omitempty"`
}

type Slot struct {
	SchemaVersion             int             `json:"schema_version" firestore:"schema_version"`
	SourceKey                 string          `json:"source_key" firestore:"source_key"`
	SourceRevision            int64           `json:"source_revision" firestore:"source_revision"`
	ProducerWitness           ProducerWitness `json:"producer_witness" firestore:"producer_witness"`
	Active                    bool            `json:"active" firestore:"active"`
	PayloadDigest             string          `json:"payload_digest,omitempty" firestore:"payload_digest,omitempty"`
	AttentionMaterialRevision string          `json:"attention_material_revision,omitempty" firestore:"attention_material_revision,omitempty"`
	Observation               *Observation    `json:"observation,omitempty" firestore:"observation,omitempty"`
	ObservedAt                time.Time       `json:"observed_at" firestore:"observed_at"`
	LastAppliedConfirmationID string          `json:"last_applied_confirmation_id,omitempty" firestore:"last_applied_confirmation_id,omitempty"`
	LastConfirmationStartedAt *time.Time      `json:"last_confirmation_started_at,omitempty" firestore:"last_confirmation_started_at,omitempty"`
}

type Category struct {
	SchemaVersion    int                 `json:"schema_version" firestore:"schema_version"`
	Category         string              `json:"category" firestore:"category"`
	AttentionID      string              `json:"attention_id,omitempty" firestore:"attention_id,omitempty"`
	AttentionState   AttentionState      `json:"attention_state,omitempty" firestore:"attention_state,omitempty"`
	MaterialBySource map[string]string   `json:"material_by_source" firestore:"material_by_source"`
	SeverityBySource map[string]Severity `json:"severity_by_source" firestore:"severity_by_source"`
	ActiveCount      int                 `json:"active_count" firestore:"active_count"`
	UrgentCount      int                 `json:"urgent_count" firestore:"urgent_count"`
	MaterialDigest   string              `json:"material_digest,omitempty" firestore:"material_digest,omitempty"`
	UpdatedAt        time.Time           `json:"updated_at" firestore:"updated_at"`
	LastNotifiedAt   *time.Time          `json:"last_notified_at,omitempty" firestore:"last_notified_at,omitempty"`
}

type Item struct {
	Category       string         `json:"category"`
	AttentionID    string         `json:"attention_id,omitempty"`
	AttentionState AttentionState `json:"attention_state,omitempty"`
	Severity       Severity       `json:"severity"`
	Observations   []Slot         `json:"observations"`
}

type View struct {
	SchemaVersion int             `json:"schema_version"`
	Partial       bool            `json:"partial"`
	Failures      []SourceFailure `json:"failures"`
	Notices       []SourceNotice  `json:"notices"`
	Items         []Item          `json:"items"`
}

func ValidateTarget(target Target) error {
	switch target.Kind {
	case "pipeline-run":
		if !validID(target.ProjectID) || !validID(target.ExecutionID) || target.Mode != "" || target.Version != "" {
			return ErrInvalidInput
		}
	case "project-description":
		if !validID(target.ProjectID) || target.ExecutionID != "" || target.Mode != "" || target.Version != "" {
			return ErrInvalidInput
		}
	case "release-notes":
		if target.ProjectID != "" || target.ExecutionID != "" || (target.Mode != "current" && target.Mode != "published-version") {
			return ErrInvalidInput
		}
		if target.Mode == "current" && target.Version != "" || target.Mode == "published-version" && !validProductVersion(target.Version) {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

func ResolveTarget(target Target) (string, error) {
	if err := ValidateTarget(target); err != nil {
		return "", err
	}
	switch target.Kind {
	case "pipeline-run":
		return "/status?project_id=" + url.QueryEscape(target.ProjectID) + "&execution_id=" + url.QueryEscape(target.ExecutionID), nil
	case "project-description":
		return "/profile?project_id=" + url.QueryEscape(target.ProjectID) + "#project-description", nil
	case "release-notes":
		version := target.Version
		if target.Mode == "current" {
			version = "current"
		}
		return "/release-notes?version=" + url.QueryEscape(version), nil
	default:
		return "", ErrInvalidInput
	}
}

func ValidateObservationInput(in ObservationInput) error {
	if !validCategory(in.Category) || !validSourceKey(in.Category, in.SourceKey) || in.ExpectedSourceRevision < 0 || len(in.PayloadDigest) != 64 || !isHex(in.PayloadDigest) || strings.TrimSpace(in.AttentionMaterialRevision) == "" {
		return ErrInvalidInput
	}
	if err := validateWitness(in.Category, in.SourceKey, in.ProducerWitness); err != nil {
		return err
	}
	if err := validateObservation(in.Observation); err != nil {
		return err
	}
	if in.Category == CategoryPurposeGap {
		if in.ConfirmationID == "" || in.ConfirmationID != in.ProducerWitness.ConfirmationID || in.EligibilityStartedAt == nil || in.EligibilityStartedAt.IsZero() {
			return ErrInvalidInput
		}
	} else if in.ConfirmationID != "" || in.EligibilityStartedAt != nil {
		return ErrInvalidInput
	}
	return nil
}

func ValidateClearInput(in ClearInput) error {
	if !validCategory(in.Category) || !validSourceKey(in.Category, in.SourceKey) || in.ExpectedSourceRevision < 0 ||
		(in.Proof != "authoritative-current-missing" && in.Proof != "confirmed-recovery" && in.Proof != "confirmed-no-conflict") {
		return ErrInvalidInput
	}
	if err := validateWitness(in.Category, in.SourceKey, in.ProducerWitness); err != nil {
		return err
	}
	if in.Category == CategoryPurposeGap && (in.ConfirmationID == "" || in.ConfirmationID != in.ProducerWitness.ConfirmationID) {
		return ErrInvalidInput
	}
	if in.Category == CategoryPipelineFailure && in.Proof == "confirmed-recovery" && in.ProducerWitness.TerminalClass != "recovered" {
		return ErrInvalidInput
	}
	return nil
}

func validateObservation(value Observation) error {
	if !utf8.ValidString(value.Title) || strings.TrimSpace(value.Title) == "" || utf8.RuneCountInString(value.Title) > MaxTextRunes || !utf8.ValidString(value.Body) || utf8.RuneCountInString(value.Body) > MaxTextRunes ||
		(value.Severity != SeverityUrgent && value.Severity != SeverityAttention && value.Severity != SeverityInfo) || len(value.Actions) > 3 {
		return ErrInvalidInput
	}
	if value.ProjectID != "" && !validID(value.ProjectID) {
		return ErrInvalidInput
	}
	if !utf8.ValidString(value.DeclaredPurpose) || utf8.RuneCountInString(value.DeclaredPurpose) > 1000 ||
		!utf8.ValidString(value.PublishedSummary) || len([]byte(value.PublishedSummary)) > 8192 ||
		value.Coverage.TotalConcepts < 0 || value.Coverage.SelectedConcepts < 0 || value.Coverage.SelectedConcepts > value.Coverage.TotalConcepts ||
		value.Coverage.TitleTruncatedConcepts < 0 || value.Coverage.BodyTruncatedConcepts < 0 {
		return ErrInvalidInput
	}
	for _, action := range value.Actions {
		if action.ID == "" || !validID(action.ID) || strings.TrimSpace(action.Label) == "" || utf8.RuneCountInString(action.Label) > 160 {
			return ErrInvalidInput
		}
		switch action.Kind {
		case "link":
			if action.Target == nil || action.CopyText != "" {
				return ErrInvalidInput
			}
		case "copy":
			if action.Target != nil && ValidateTarget(*action.Target) != nil || action.CopyText == "" || len(action.CopyText) > 8192 {
				return ErrInvalidInput
			}
		case "copy-then-link":
			if action.Target == nil || action.CopyText == "" || len(action.CopyText) > 8192 {
				return ErrInvalidInput
			}
		default:
			return ErrInvalidInput
		}
		if action.Target != nil {
			if err := ValidateTarget(*action.Target); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWitness(category, sourceKey string, w ProducerWitness) error {
	switch category {
	case CategoryPipelineFailure:
		if w.Kind != "pipeline-run" || !validID(w.ProjectID) || w.ProjectPipelineSequence < 1 || !validID(w.ReservationID) || !validID(w.AttemptID) || !validID(w.ExecutionID) || w.Trigger == "" || (w.Mode != "full" && w.Mode != "incremental") || (w.TerminalClass != "failed" && w.TerminalClass != "recovered") || sourceKey != "pipeline-fail:"+w.ProjectID {
			return ErrInvalidInput
		}
	case CategoryPurposeGap:
		if w.Kind != "purpose-comparison" || !validID(w.ProjectID) || !validID(w.AttemptID) || !validID(w.ConfirmationID) || w.PairKey == "" || w.DescriptionRevision < 0 || w.PublicationGeneration == "" || len(w.ConceptsDigest) != 64 || !isHex(w.ConceptsDigest) || len(w.SummaryInputDigest) != 64 || !isHex(w.SummaryInputDigest) || w.AttemptStartedAt.IsZero() || sourceKey != "content-purpose-gap:"+w.ProjectID {
			return ErrInvalidInput
		}
	case CategoryReleaseNotes:
		if w.Kind != "release-notes" || (w.Environment != "dev" && w.Environment != "local" && w.Environment != "prod") || w.IndexRevision < 1 || w.ProductVersion == "" || !validProductVersion(w.ProductVersion) || len(w.IndexDigest) != 64 || !isHex(w.IndexDigest) || sourceKey != "release-notes:llm-wiki-cloud:"+w.Environment {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

func CompareWitness(category string, incoming, current ProducerWitness) int {
	if current.Kind == "" {
		return 1
	}
	switch category {
	case CategoryPipelineFailure:
		if incoming.ProjectPipelineSequence < current.ProjectPipelineSequence {
			return -1
		}
		if incoming.ProjectPipelineSequence > current.ProjectPipelineSequence {
			return 1
		}
		if incoming.ExecutionID == current.ExecutionID {
			if incoming.TerminalClass == current.TerminalClass {
				return 0
			}
			if incoming.TerminalClass == "recovered" {
				return 1
			}
			return -1
		}
		return -1
	case CategoryReleaseNotes:
		if incoming.Environment != current.Environment {
			return -1
		}
		if incoming.IndexRevision < current.IndexRevision {
			return -1
		}
		if incoming.IndexRevision > current.IndexRevision {
			return 1
		}
		if incoming.ProductVersion == current.ProductVersion && incoming.IndexDigest == current.IndexDigest {
			return 0
		}
		return -1
	case CategoryPurposeGap:
		if incoming.ProjectID != current.ProjectID {
			return -1
		}
		if incoming.AttemptStartedAt.Before(current.AttemptStartedAt) {
			return -1
		}
		if incoming.AttemptStartedAt.After(current.AttemptStartedAt) {
			return 1
		}
		if incoming.AttemptID == current.AttemptID && incoming.ConfirmationID == current.ConfirmationID {
			return 0
		}
		return 0
	}
	return -1
}

func validCategory(value string) bool {
	return value == CategoryPipelineFailure || value == CategoryReleaseNotes || value == CategoryPurposeGap
}
func validSourceKey(category, key string) bool {
	if len(key) == 0 || len(key) > 512 {
		return false
	}
	switch category {
	case CategoryPipelineFailure:
		return strings.HasPrefix(key, "pipeline-fail:")
	case CategoryReleaseNotes:
		return strings.HasPrefix(key, "release-notes:llm-wiki-cloud:")
	case CategoryPurposeGap:
		return strings.HasPrefix(key, "content-purpose-gap:")
	}
	return false
}
func validID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "/\\\x00")
}
func validProductVersion(value string) bool {
	return value != "" && len(value) <= 64 && !strings.ContainsAny(value, "/\\?#\x00")
}
func isHex(value string) bool {
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
func sourceDocumentID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
