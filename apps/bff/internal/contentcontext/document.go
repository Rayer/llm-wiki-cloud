package contentcontext

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const StateSchemaVersion = 1

type StateDocument struct {
	SchemaVersion     int                `json:"schema_version" firestore:"schema_version"`
	TargetGeneration  string             `json:"target_generation" firestore:"target_generation"`
	TargetConceptsSHA string             `json:"target_concepts_sha256" firestore:"target_concepts_sha256"`
	Status            Status             `json:"status" firestore:"status"`
	AttemptID         string             `json:"attempt_id" firestore:"attempt_id"`
	AttemptStartedAt  *time.Time         `json:"attempt_started_at" firestore:"attempt_started_at"`
	AttemptFinishedAt *time.Time         `json:"attempt_finished_at" firestore:"attempt_finished_at"`
	ErrorClass        string             `json:"error_class" firestore:"error_class"`
	LastGood          *LastGood          `json:"last_good" firestore:"last_good"`
	LatestPublication *PublishedHead     `json:"latest_publication" firestore:"latest_publication"`
	TargetSequence    int64              `json:"target_sequence" firestore:"target_sequence"`
	SummaryWork       *SummaryWorkRecord `json:"summary_work" firestore:"summary_work"`
}

type PublishedHead struct {
	Generation       string `json:"generation" firestore:"generation"`
	ConceptsSHA256   string `json:"concepts_sha256" firestore:"concepts_sha256"`
	ManifestIdentity string `json:"manifest_identity" firestore:"manifest_identity"`
}

type SummaryWorkRecord struct {
	WorkID                 string     `json:"work_id" firestore:"work_id"`
	ConceptsSHA256         string     `json:"concepts_sha256" firestore:"concepts_sha256"`
	SourceGeneration       string     `json:"source_generation" firestore:"source_generation"`
	SourceManifestIdentity string     `json:"source_manifest_identity" firestore:"source_manifest_identity"`
	AttemptID              string     `json:"attempt_id" firestore:"attempt_id"`
	LeaseUntil             *time.Time `json:"lease" firestore:"lease"`
	RetryCount             int        `json:"retry_count" firestore:"retry_count"`
	Status                 Status     `json:"status" firestore:"status"`
}

type LastGood struct {
	Text             string    `json:"text" firestore:"text"`
	SourceGeneration string    `json:"source_generation" firestore:"source_generation"`
	ConceptsSHA256   string    `json:"concepts_sha256" firestore:"concepts_sha256"`
	GeneratedAt      time.Time `json:"generated_at" firestore:"generated_at"`
	PromptRevision   string    `json:"prompt_revision" firestore:"prompt_revision"`
	ProfileID        string    `json:"profile_id" firestore:"profile_id"`
	ProfileRevision  int64     `json:"profile_revision" firestore:"profile_revision"`
	Role             string    `json:"role" firestore:"role"`
	RequestedModel   string    `json:"requested_model" firestore:"requested_model"`
	ReturnedModel    string    `json:"returned_model" firestore:"returned_model"`
	InputDigest      string    `json:"input_digest" firestore:"input_digest"`
	SelectionVersion string    `json:"selection_version" firestore:"selection_version"`
}

func (d StateDocument) Validate() error {
	if d.SchemaVersion != StateSchemaVersion || !validStatus(d.Status) || d.TargetSequence < 0 {
		return errors.New("invalid content-context state")
	}
	if d.TargetConceptsSHA != "" && !validSHA256(d.TargetConceptsSHA) {
		return errors.New("invalid content-context target digest")
	}
	if d.LatestPublication != nil {
		publicationDigest, publicationDigestValid := NormalizeSHA256(d.LatestPublication.ConceptsSHA256)
		targetDigest, targetDigestValid := NormalizeSHA256(d.TargetConceptsSHA)
		if d.LatestPublication.Generation == "" || !publicationDigestValid || d.LatestPublication.ManifestIdentity == "" ||
			d.LatestPublication.Generation != d.TargetGeneration || !targetDigestValid || publicationDigest != targetDigest || d.TargetSequence < 1 {
			return errors.New("invalid content-context publication")
		}
		if d.SummaryWork == nil {
			return errors.New("content-context publication has no summary work")
		}
	} else if d.TargetGeneration != "" || d.TargetConceptsSHA != "" {
		return errors.New("content-context target has no publication")
	}
	if d.SummaryWork != nil {
		workDigest, workDigestValid := NormalizeSHA256(d.SummaryWork.ConceptsSHA256)
		targetDigest, targetDigestValid := NormalizeSHA256(d.TargetConceptsSHA)
		if !validStatus(d.SummaryWork.Status) || !workDigestValid || !targetDigestValid || workDigest != targetDigest ||
			d.SummaryWork.WorkID == "" || d.SummaryWork.SourceGeneration == "" || d.SummaryWork.RetryCount < 0 || d.SummaryWork.RetryCount >= SummaryMaxClaims ||
			(d.SummaryWork.Status == StatusPending && d.SummaryWork.SourceManifestIdentity == "") {
			return errors.New("invalid content-context summary work")
		}
		if d.Status != d.SummaryWork.Status {
			return errors.New("content-context state status mismatch")
		}
		if d.SummaryWork.AttemptID != d.AttemptID || (d.AttemptID == "") != (d.SummaryWork.LeaseUntil == nil) {
			return errors.New("content-context attempt identity mismatch")
		}
		if d.AttemptID == "" && (d.AttemptStartedAt != nil || d.AttemptFinishedAt != nil) {
			return errors.New("content-context attempt timestamps have no attempt")
		}
		if d.AttemptID != "" && (d.AttemptStartedAt == nil || (d.Status == StatusPending && d.AttemptFinishedAt != nil) ||
			(d.Status != StatusPending && d.AttemptFinishedAt == nil) ||
			(d.AttemptFinishedAt != nil && d.AttemptFinishedAt.Before(*d.AttemptStartedAt))) {
			return errors.New("invalid content-context attempt timestamps")
		}
	} else if d.Status != StatusMissing {
		return errors.New("content-context status has no summary work")
	} else if d.AttemptID != "" || d.AttemptStartedAt != nil || d.AttemptFinishedAt != nil {
		return errors.New("content-context attempt has no summary work")
	}
	if d.LastGood != nil {
		if strings.TrimSpace(d.LastGood.Text) == "" || utf8.RuneCountInString(d.LastGood.Text) > 1000 ||
			d.LastGood.SourceGeneration == "" || !validSHA256(d.LastGood.ConceptsSHA256) || d.LastGood.GeneratedAt.IsZero() ||
			d.LastGood.PromptRevision == "" || d.LastGood.ProfileID == "" || d.LastGood.ProfileRevision < 1 ||
			d.LastGood.Role != SummaryRole || d.LastGood.RequestedModel == "" || d.LastGood.ReturnedModel == "" ||
			!validSHA256(d.LastGood.InputDigest) || d.LastGood.SelectionVersion != SummarySelectionVersion {
			return errors.New("invalid content-context last-good summary")
		}
	}
	if d.Status == StatusFailed && !validErrorClass(d.ErrorClass) {
		return errors.New("invalid content-context error class")
	}
	if d.Status == StatusReady {
		if d.LastGood == nil {
			return errors.New("ready content-context has no summary")
		}
		lastDigest, lastDigestValid := NormalizeSHA256(d.LastGood.ConceptsSHA256)
		targetDigest, targetDigestValid := NormalizeSHA256(d.TargetConceptsSHA)
		if !lastDigestValid || !targetDigestValid || lastDigest != targetDigest {
			return errors.New("ready content-context summary does not match target")
		}
	}
	if d.Status != StatusFailed && d.ErrorClass != "" {
		return errors.New("unexpected content-context error class")
	}
	return nil
}

func validStatus(status Status) bool {
	return status == StatusMissing || status == StatusPending || status == StatusReady || status == StatusFailed
}

func validSHA256(value string) bool {
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// NormalizeSHA256 returns the bare, lowercase hexadecimal digest accepted by
// the Project context contract.
func NormalizeSHA256(value string) (string, bool) {
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	if !validSHA256(value) {
		return "", false
	}
	return value, true
}

func validErrorClass(value string) bool {
	switch value {
	case "provider_timeout", "invalid_response", "storage_failure", "provider_unavailable", "provider_error", "input_invalid", "lease_expired", "summary_invalid":
		return true
	default:
		return false
	}
}
