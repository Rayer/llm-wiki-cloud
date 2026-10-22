package urlintake

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	ConfirmationTTL       = 15 * time.Minute
	identityObjectPrefix  = "cache/raw_url_identity/v1/"
	candidateObjectPrefix = "cache/raw_url_candidate/v1/"
	maxIdentityCASRetries = 8
)

var DefaultQualityThresholds = QualityThresholds{
	RejectShellAt: 0.95, RejectContentAtMost: 0.05,
	UsableShellAtMost: 0.10, UsableContentAt: 0.90,
}

var (
	ErrInvalidScope       = errors.New("verified actor and project are required")
	ErrURLConflict        = errors.New("URL identity conflicts with existing raw content; no raw article was changed")
	ErrConfirmationStale  = errors.New("URL confirmation is stale or invalid")
	ErrConfirmationBusy   = errors.New("URL confirmation is already being committed")
	ErrIdentityUncertain  = errors.New("URL identity reservation outcome is uncertain")
	ErrStorageUnavailable = errors.New("raw storage is unavailable")
)

type ActorProject struct {
	ActorID   string
	ProjectID string
}

type ProjectStore interface {
	storage.ConditionalWriter
	ReadFile(context.Context, string) ([]byte, error)
	ListRawFiles(context.Context) ([]storage.RawFile, error)
}

type StoreFactory func(ActorProject) (ProjectStore, error)

type ImportRequest struct {
	URL  string   `json:"url"`
	Tags []string `json:"tags,omitempty"`
}

type StorageOutcome struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Status string `json:"status"`
}

type CandidatePreview struct {
	Title             string         `json:"title"`
	SourceURL         string         `json:"source_url"`
	Markdown          string         `json:"markdown"`
	Metadata          map[string]any `json:"metadata"`
	CandidateSHA256   string         `json:"candidate_sha256"`
	QualityChecked    bool           `json:"quality_checked"`
	QualityStatus     string         `json:"quality_status"`
	Reason            string         `json:"reason,omitempty"`
	Warning           string         `json:"warning,omitempty"`
	Provider          string         `json:"provider"`
	ProviderTruncated *bool          `json:"provider_truncated,omitempty"`
}

type Result struct {
	Status            string            `json:"status"`
	Message           string            `json:"message"`
	ConfirmationRef   string            `json:"confirmation_ref,omitempty"`
	CandidateID       string            `json:"candidate_id,omitempty"`
	CandidateRevision string            `json:"candidate_revision,omitempty"`
	ExpiresAt         string            `json:"expires_at,omitempty"`
	RetryAfterSeconds int64             `json:"retry_after_seconds,omitempty"`
	FailureStage      string            `json:"failure_stage,omitempty"`
	Preview           *CandidatePreview `json:"preview,omitempty"`
	Storage           *StorageOutcome   `json:"storage,omitempty"`
	ProcessingStatus  string            `json:"processing_status,omitempty"`
}

type Service struct {
	Fetcher    Fetcher
	Evaluator  QualityEvaluator
	Stores     StoreFactory
	Resolver   IPResolver
	Thresholds QualityThresholds
	Now        func() time.Time

	mu            sync.Mutex
	confirmations map[string]*pendingConfirmation
}

type pendingConfirmation struct {
	Candidate *candidate
	Busy      bool
}

type candidate struct {
	scope              ActorProject
	originalURL        string
	normalizedURL      string
	identitySHA256     string
	relativePath       string
	expectedGeneration int64
	existing           bool
	data               []byte
	digest             string
	preview            CandidatePreview
	expiresAt          time.Time
}

type candidateReceipt struct {
	Version            int              `json:"version"`
	ActorID            string           `json:"actor_id"`
	ProjectID          string           `json:"project_id"`
	OriginalURL        string           `json:"original_url"`
	NormalizedURL      string           `json:"normalized_url"`
	IdentitySHA256     string           `json:"identity_sha256"`
	RelativePath       string           `json:"relative_path"`
	ExpectedGeneration int64            `json:"expected_generation"`
	Existing           bool             `json:"existing"`
	Data               []byte           `json:"data"`
	Digest             string           `json:"digest"`
	Revision           string           `json:"revision"`
	Preview            CandidatePreview `json:"preview"`
	ExpiresAt          time.Time        `json:"expires_at"`
	State              string           `json:"state"`
	AcceptedTitle      string           `json:"accepted_title,omitempty"`
	Result             *Result          `json:"result,omitempty"`
}

type candidateOperation struct {
	Receipt    candidateReceipt
	Generation int64
}

type rawTarget struct {
	relativePath string
	data         []byte
	generation   int64
	exists       bool
	indexed      bool
}

type identityRecord struct {
	Version   int    `json:"version"`
	URLSHA256 string `json:"url_sha256"`
	RawPath   string `json:"raw_path"`
}

func NewService(fetcher Fetcher, evaluator QualityEvaluator, stores StoreFactory, thresholds QualityThresholds) *Service {
	if thresholds == (QualityThresholds{}) {
		thresholds = DefaultQualityThresholds
	}
	return &Service{
		Fetcher: fetcher, Evaluator: evaluator, Stores: stores, Thresholds: thresholds,
		Now: time.Now, confirmations: map[string]*pendingConfirmation{},
	}
}

// Import must receive identity and Project resolved by the Web/CLI/MCP auth
// adapter. The model-facing request contains only URL and optional source tags.
func (s *Service) Import(ctx context.Context, verified ActorProject, request ImportRequest) (Result, error) {
	return s.intake(ctx, verified, request, false)
}

// Prepare creates a durable, project-scoped URL candidate without modifying raw/.
func (s *Service) Prepare(ctx context.Context, verified ActorProject, request ImportRequest) (Result, error) {
	return s.intake(ctx, verified, request, true)
}

func (s *Service) intake(ctx context.Context, verified ActorProject, request ImportRequest, prepare bool) (Result, error) {
	if s == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake is not configured.", FailureStage: "setup"}, nil
	}
	if strings.TrimSpace(verified.ActorID) == "" || strings.TrimSpace(verified.ProjectID) == "" {
		return Result{}, ErrInvalidScope
	}
	if len(request.Tags) > 64 {
		return Result{Status: "rejected", Message: "Too many source tags were supplied."}, nil
	}
	for _, tag := range request.Tags {
		if utf8.RuneCountInString(tag) > 128 {
			return Result{Status: "rejected", Message: "A source tag is too long."}, nil
		}
	}
	if s.Stores == nil || s.Fetcher == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake storage or Fetch is not configured.", FailureStage: "setup"}, nil
	}
	validatedURL, err := ValidatePublicURL(ctx, request.URL, s.Resolver)
	if err != nil {
		if errors.Is(err, ErrUnresolvedURL) {
			recordIntakeEvent(ctx, "fetch", "transport", "failure")
			return Result{Status: "unavailable", Message: "The public address could not be verified, so no content was fetched.", FailureStage: "fetch"}, nil
		}
		return Result{Status: "rejected", Message: "Use a public HTTP or HTTPS URL without embedded credentials.", FailureStage: "validation"}, nil
	}
	normalizedURL, err := NormalizeURL(validatedURL)
	if err != nil {
		return Result{Status: "rejected", Message: "The URL could not be normalized safely."}, nil
	}
	if err := validateTags(request.Tags); err != nil {
		return Result{Status: "rejected", Message: err.Error()}, nil
	}
	store, err := s.Stores(verified)
	if err != nil || store == nil {
		recordIntakeEvent(ctx, "storage", "dependency", "failure")
		return Result{Status: "unavailable", Message: "Project storage is unavailable; no raw article was changed.", FailureStage: "storage"}, nil
	}
	page, err := s.Fetcher.Fetch(ctx, request.URL)
	if err != nil {
		if class := fetchFailureClass(err); class != "" {
			recordIntakeEvent(ctx, "fetch", class, "failure")
		}
		return failureResult(err), nil
	}
	page.Markdown = NormalizeMarkdown(page.Markdown)
	if len(page.Markdown) > MaxCandidateBytes {
		return Result{Status: "rejected", Message: "The fetched content exceeds the supported size.", FailureStage: "fetch"}, nil
	}
	if strings.TrimSpace(page.Markdown) == "" {
		return Result{Status: "rejected", Message: "Fetch completed without usable article content; nothing was saved.", FailureStage: "fetch"}, nil
	}
	title := usableTitle(page.Title, page.Markdown, validatedURL)
	identityDigest := digestString([]byte(normalizedURL))
	target, err := resolveTarget(ctx, store, normalizedURL, identityDigest, title)
	if err != nil {
		if errors.Is(err, ErrURLConflict) || errors.Is(err, rawintake.ErrConflict) {
			return Result{Status: "conflict", Message: "More than one existing raw file matches this URL; no file was changed."}, nil
		}
		recordIntakeEvent(ctx, "storage", "storage", "failure")
		return Result{Status: "unavailable", Message: "Could not read Project raw content to prepare the candidate; no file was changed.", FailureStage: "storage"}, nil
	}
	metadata := SourceMetadata{Title: title, SourceURL: request.URL, Tags: request.Tags}
	data, _, err := MergeRawMarkdown(target.data, metadata, page.Markdown, s.now())
	if err != nil {
		return Result{Status: "conflict", Message: "The existing raw metadata could not be refreshed safely; no file was changed."}, nil
	}
	if len(data) > MaxCandidateBytes {
		return Result{Status: "rejected", Message: "The merged raw content exceeds the supported size; no file was changed."}, nil
	}
	digest := digestString(data)
	_, previewBody, _, _ := parseRawFrontmatter(data)
	previewMarkdown := string(data)
	if prepare {
		previewMarkdown = string(previewBody)
	}
	preview := CandidatePreview{
		Title: title, SourceURL: request.URL, Markdown: previewMarkdown,
		Metadata: previewMetadata(data), CandidateSHA256: digest,
		Provider: DeepSeekFetchProvider, ProviderTruncated: page.Truncated,
	}
	providerMetadata := map[string]any{}
	if page.Truncated != nil {
		providerMetadata["truncated"] = *page.Truncated
	}
	var evaluation QualityEvaluation
	var qualityErr error
	qualityClass := ""
	if s.Evaluator == nil {
		qualityClass = "configuration"
	} else {
		evaluation, qualityErr = s.Evaluator.Evaluate(ctx, QualityInput{
			URL: request.URL, Title: title, Markdown: page.Markdown,
			Provider: DeepSeekFetchProvider, ProviderMetadata: providerMetadata,
		})
		if qualityErr != nil {
			qualityClass = qualityFailureClass(ctx, qualityErr)
		}
	}
	if qualityClass != "" {
		recordIntakeEvent(ctx, "quality", qualityClass, "failure")
		recordIntakeEvent(ctx, "quality", qualityClass, "fallback")
		preview.QualityChecked = false
		preview.QualityStatus = "not_checked"
		if page.Truncated != nil && *page.Truncated {
			preview.Warning = "The Fetch provider explicitly marked this content as truncated."
		}
		ref, expiresAt, saveErr := s.saveCandidate(ctx, store, verified, &candidate{
			scope: verified, originalURL: request.URL, normalizedURL: normalizedURL,
			identitySHA256: identityDigest, relativePath: target.relativePath,
			expectedGeneration: target.generation, existing: target.exists,
			data: data, digest: digest, preview: preview,
		})
		if saveErr != nil {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
			return Result{Status: "unavailable", Message: "The page was fetched, but its review candidate could not be stored; no raw article was changed.", FailureStage: "candidate"}, nil
		}
		return Result{
			Status: "preview", Message: "The page was fetched successfully, but the automatic quality check was unavailable and skipped. Review the content before saving.",
			ConfirmationRef: ref, CandidateID: ref, CandidateRevision: digest,
			ExpiresAt: expiresAt.Format(time.RFC3339), Preview: &preview,
		}, nil
	}
	preview.QualityChecked = true
	quality := DecideQuality(evaluation, s.Thresholds)
	if quality == QualityRejected {
		preview.QualityStatus = "rejected"
		preview.Reason = "The page appears to be a blocking, error, or loading shell without readable main content."
		return Result{Status: "rejected", Message: preview.Reason, FailureStage: "quality", Preview: &preview}, nil
	}
	needsConfirmation := quality == QualityNeedsConfirmation
	preview.QualityStatus = "usable"
	if needsConfirmation {
		preview.QualityStatus = "needs_confirmation"
	}
	if page.Truncated != nil && *page.Truncated {
		needsConfirmation = true
		preview.Warning = "The Fetch provider explicitly marked this content as truncated."
	}
	if needsConfirmation {
		preview.Reason = "The page could not be classified as clearly substantive and complete."
	}
	if prepare {
		ref, expiresAt, err := s.saveCandidate(ctx, store, verified, &candidate{
			scope: verified, originalURL: request.URL, normalizedURL: normalizedURL,
			identitySHA256: identityDigest, relativePath: target.relativePath,
			expectedGeneration: target.generation, existing: target.exists,
			data: data, digest: digest, preview: preview,
		})
		if err != nil {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
			return Result{Status: "unavailable", Message: "The fetched page could not be stored as a review candidate; no raw article was changed.", FailureStage: "candidate"}, nil
		}
		return Result{Status: "preview", Message: "Review the rendered candidate before saving.", ConfirmationRef: ref, CandidateID: ref, CandidateRevision: digest, ExpiresAt: expiresAt.Format(time.RFC3339), Preview: &preview}, nil
	}
	if needsConfirmation {
		ref, expiresAt, err := s.saveConfirmation(&candidate{
			scope: verified, originalURL: request.URL, normalizedURL: normalizedURL,
			identitySHA256: identityDigest, relativePath: target.relativePath,
			expectedGeneration: target.generation, existing: target.exists,
			data: data, digest: digest, preview: preview,
		})
		if err != nil {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
			return Result{Status: "unavailable", Message: "The review candidate could not be retained; no raw article was changed.", FailureStage: "candidate", Preview: &preview}, nil
		}
		return Result{Status: "needs_confirmation", Message: preview.Reason, ConfirmationRef: ref, ExpiresAt: expiresAt.Format(time.RFC3339), Preview: &preview}, nil
	}
	prepared := &candidate{
		scope: verified, originalURL: request.URL, normalizedURL: normalizedURL,
		identitySHA256: identityDigest, relativePath: target.relativePath,
		expectedGeneration: target.generation, existing: target.exists,
		data: data, digest: digest, preview: preview,
	}
	return s.commit(ctx, store, prepared)
}

// Accept commits exactly the immutable prepared bytes, binding the first accepted title.
func (s *Service) Accept(ctx context.Context, verified ActorProject, id, revision, title string) (Result, error) {
	if s == nil || s.Stores == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake is not configured; the candidate was not saved.", FailureStage: "setup"}, nil
	}
	if !validCandidateID(id) || strings.TrimSpace(verified.ActorID) == "" || strings.TrimSpace(verified.ProjectID) == "" {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	store, err := s.Stores(verified)
	if err != nil || store == nil {
		recordIntakeEvent(ctx, "storage", "dependency", "failure")
		return Result{Status: "unavailable", Message: "Project storage is unavailable; the candidate was not saved.", FailureStage: "storage"}, nil
	}
	op, err := readCandidate(ctx, store, id)
	if err != nil {
		if !errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) && !errors.Is(err, ErrConfirmationStale) {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
		}
		return candidateReadFailure(id, err), nil
	}
	if op.Receipt.ActorID != verified.ActorID || op.Receipt.ProjectID != verified.ProjectID || !op.Receipt.ExpiresAt.After(s.now()) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if op.Receipt.Revision != revision || !validCandidateTitle(title) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	title = strings.TrimSpace(title)
	if (op.Receipt.State == "accepted" || op.Receipt.State == "conflict") && op.Receipt.Result != nil {
		if op.Receipt.AcceptedTitle != title {
			return Result{Status: "conflict", Message: "The candidate was already accepted with a different title."}, nil
		}
		return *op.Receipt.Result, nil
	}
	if op.Receipt.State == "rejected" || op.Receipt.State == "expired" {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if op.Receipt.State == "prepared" {
		receipt := op.Receipt
		_, body, _, parseErr := parseRawFrontmatter(receipt.Data)
		if parseErr != nil {
			return Result{Status: "conflict", Message: ErrConfirmationStale.Error()}, nil
		}
		updated, _, mergeErr := MergeRawMarkdown(receipt.Data, SourceMetadata{Title: title}, string(body), s.now())
		if mergeErr != nil || len(updated) > MaxCandidateBytes {
			return Result{Status: "rejected", Message: "The edited title makes the saved article exceed the supported size."}, nil
		}
		receipt.Data = updated
		receipt.Digest = digestString(updated)
		receipt.Preview.Title = title
		receipt.Preview.CandidateSHA256 = receipt.Digest
		receipt.Preview.Metadata = previewMetadata(updated)
		receipt.AcceptedTitle = title
		receipt.State = "accepting"
		if err := writeCandidate(ctx, store, id, receipt, op.Generation); err != nil {
			if errors.Is(err, storage.ErrGenerationMismatch) {
				return s.resolveOperation(ctx, store, id, verified, title)
			}
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
			recordIntakeEvent(ctx, "candidate", "storage", "uncertain")
			return candidateUncertain(id, "The save decision was recorded but its status is uncertain; check save status before retrying."), nil
		}
		op.Receipt = receipt
	} else if op.Receipt.AcceptedTitle != title {
		return Result{Status: "conflict", Message: "The candidate was already accepted with a different title."}, nil
	}
	return s.finishAccepted(ctx, store, id, verified, op.Receipt)
}

// Reject atomically declines a prepared candidate. A competing accept/reject has one winner.
func (s *Service) Reject(ctx context.Context, verified ActorProject, id, revision string) (Result, error) {
	if s == nil || s.Stores == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake is not configured; the candidate was not changed.", FailureStage: "setup"}, nil
	}
	if !validCandidateID(id) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	store, err := s.Stores(verified)
	if err != nil || store == nil {
		recordIntakeEvent(ctx, "storage", "dependency", "failure")
		return Result{Status: "unavailable", Message: "Project storage is unavailable; the candidate was not changed.", FailureStage: "storage"}, nil
	}
	op, err := readCandidate(ctx, store, id)
	if err != nil {
		if !errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) && !errors.Is(err, ErrConfirmationStale) {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
		}
		return candidateReadFailure(id, err), nil
	}
	if op.Receipt.ActorID != verified.ActorID || op.Receipt.ProjectID != verified.ProjectID || op.Receipt.Revision != revision || !op.Receipt.ExpiresAt.After(s.now()) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if (op.Receipt.State == "accepted" || op.Receipt.State == "conflict") && op.Receipt.Result != nil {
		return *op.Receipt.Result, nil
	}
	if op.Receipt.State == "accepting" {
		return candidateUncertain(id, "The save is in progress; check save status."), nil
	}
	if op.Receipt.State == "rejected" {
		return Result{Status: "rejected", Message: "Candidate declined."}, nil
	}
	receipt := op.Receipt
	receipt.State = "rejected"
	if err := writeCandidate(ctx, store, id, receipt, op.Generation); err != nil {
		if errors.Is(err, storage.ErrGenerationMismatch) {
			return s.Resolve(ctx, verified, id)
		}
		recordIntakeEvent(ctx, "candidate", "storage", "failure")
		recordIntakeEvent(ctx, "candidate", "storage", "uncertain")
		return candidateUncertain(id, "The decline result is uncertain; check candidate status."), nil
	}
	return Result{Status: "rejected", Message: "Candidate declined."}, nil
}

// Resolve returns the durable result or resumes only the already accepted exact candidate.
func (s *Service) Resolve(ctx context.Context, verified ActorProject, id string) (Result, error) {
	if s == nil || s.Stores == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake is not configured; candidate status could not be read.", FailureStage: "setup"}, nil
	}
	if !validCandidateID(id) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	store, err := s.Stores(verified)
	if err != nil || store == nil {
		recordIntakeEvent(ctx, "storage", "dependency", "failure")
		return Result{Status: "unavailable", Message: "Project storage is unavailable; candidate status could not be read.", FailureStage: "storage"}, nil
	}
	return s.resolveOperation(ctx, store, id, verified, "")
}

func (s *Service) resolveOperation(ctx context.Context, store ProjectStore, id string, verified ActorProject, title string) (Result, error) {
	op, err := readCandidate(ctx, store, id)
	if err != nil {
		if !errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) && !errors.Is(err, ErrConfirmationStale) {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
		}
		return candidateReadFailure(id, err), nil
	}
	if op.Receipt.ActorID != verified.ActorID || op.Receipt.ProjectID != verified.ProjectID || !op.Receipt.ExpiresAt.After(s.now()) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	switch op.Receipt.State {
	case "accepted", "conflict":
		if op.Receipt.Result != nil {
			return *op.Receipt.Result, nil
		}
	case "rejected", "expired":
		return Result{Status: "rejected", Message: "Candidate was declined or expired."}, nil
	case "accepting":
		if title != "" && title != op.Receipt.AcceptedTitle {
			return Result{Status: "conflict", Message: "The candidate was accepted with a different title."}, nil
		}
		return s.finishAccepted(ctx, store, id, verified, op.Receipt)
	case "prepared":
		return Result{Status: "preview", Message: "Candidate is ready for review.", ConfirmationRef: id, CandidateID: id, CandidateRevision: op.Receipt.Revision, ExpiresAt: op.Receipt.ExpiresAt.Format(time.RFC3339), Preview: &op.Receipt.Preview}, nil
	}
	recordIntakeEvent(ctx, "candidate", "storage", "failure")
	recordIntakeEvent(ctx, "candidate", "storage", "uncertain")
	return candidateUncertain(id, "The save result is uncertain; check again."), nil
}

func candidateReadFailure(id string, err error) Result {
	if errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist) || errors.Is(err, ErrConfirmationStale) {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}
	}
	return Result{Status: "uncertain", Message: "Could not read candidate status; check again before retrying.", CandidateID: id, FailureStage: "candidate"}
}

func candidateUncertain(id, message string) Result {
	return Result{Status: "uncertain", Message: message, CandidateID: id, FailureStage: "candidate"}
}

func (s *Service) finishAccepted(ctx context.Context, store ProjectStore, id string, verified ActorProject, receipt candidateReceipt) (Result, error) {
	prepared := &candidate{scope: verified, originalURL: receipt.OriginalURL, normalizedURL: receipt.NormalizedURL,
		identitySHA256: receipt.IdentitySHA256, relativePath: receipt.RelativePath, expectedGeneration: receipt.ExpectedGeneration,
		existing: receipt.Existing, data: receipt.Data, digest: receipt.Digest, preview: receipt.Preview, expiresAt: receipt.ExpiresAt}
	result, err := s.commit(ctx, store, prepared)
	if err != nil || (result.Status != "stored" && result.Status != "conflict" && result.Status != "rejected") {
		recordIntakeEvent(ctx, "candidate", "storage", "uncertain")
		return candidateUncertain(id, "The save result is uncertain; check save status before retrying."), nil
	}
	op, readErr := readCandidate(ctx, store, id)
	if readErr != nil {
		recordIntakeEvent(ctx, "candidate", "storage", "failure")
		return candidateUncertain(id, "Saved article status is uncertain; check save status."), nil
	}
	if op.Receipt.State == "accepting" && op.Receipt.AcceptedTitle == receipt.AcceptedTitle {
		op.Receipt.State = map[bool]string{true: "accepted", false: "conflict"}[result.Status == "stored"]
		op.Receipt.Result = &result
		if writeErr := writeCandidate(ctx, store, id, op.Receipt, op.Generation); writeErr != nil {
			recordIntakeEvent(ctx, "candidate", "storage", "failure")
			latest, latestErr := readCandidate(ctx, store, id)
			if latestErr == nil && latest.Receipt.Result != nil && (latest.Receipt.State == "accepted" || latest.Receipt.State == "conflict") {
				recordIntakeEvent(ctx, "candidate", "storage", "recovered")
				return *latest.Receipt.Result, nil
			}
			recordIntakeEvent(ctx, "candidate", "storage", "uncertain")
			return candidateUncertain(id, "The article may be saved; check save status before retrying."), nil
		}
	}
	return result, nil
}

func validCandidateTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title != "" && utf8.RuneCountInString(title) <= 200 && !strings.ContainsAny(title, "\x00\r\n")
}

func validCandidateID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 32 && strings.ToLower(id) == id
}

func candidateObjectPath(id string) string { return candidateObjectPrefix + id + ".json" }

func (s *Service) saveCandidate(ctx context.Context, store ProjectStore, scope ActorProject, prepared *candidate) (string, time.Time, error) {
	for attempt := 0; attempt < 3; attempt++ {
		rawID := make([]byte, 32)
		if _, err := rand.Read(rawID); err != nil {
			return "", time.Time{}, err
		}
		id := hex.EncodeToString(rawID)
		receipt := receiptFromCandidate(scope, prepared, s.now().Add(ConfirmationTTL))
		if err := writeCandidate(ctx, store, id, receipt, 0); err == nil {
			return id, receipt.ExpiresAt, nil
		} else if !errors.Is(err, storage.ErrGenerationMismatch) {
			return "", time.Time{}, err
		}
	}
	return "", time.Time{}, ErrURLConflict
}

func receiptFromCandidate(scope ActorProject, c *candidate, expires time.Time) candidateReceipt {
	return candidateReceipt{Version: 1, ActorID: scope.ActorID, ProjectID: scope.ProjectID, OriginalURL: c.originalURL,
		NormalizedURL: c.normalizedURL, IdentitySHA256: c.identitySHA256, RelativePath: c.relativePath,
		ExpectedGeneration: c.expectedGeneration, Existing: c.existing, Data: append([]byte(nil), c.data...), Digest: c.digest,
		Preview: c.preview, ExpiresAt: expires.UTC(), State: "prepared", Revision: c.digest}
}

func readCandidate(ctx context.Context, store ProjectStore, id string) (candidateOperation, error) {
	data, generation, err := store.ReadFileWithGeneration(ctx, candidateObjectPath(id))
	if err != nil {
		return candidateOperation{}, err
	}
	var receipt candidateReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(new(any)) != io.EOF || receipt.Version != 1 || receipt.Digest != digestString(receipt.Data) {
		return candidateOperation{}, ErrConfirmationStale
	}
	if err := validateRelativeRawPath(receipt.RelativePath); err != nil {
		return candidateOperation{}, ErrConfirmationStale
	}
	return candidateOperation{Receipt: receipt, Generation: generation}, nil
}

func writeCandidate(ctx context.Context, store ProjectStore, id string, receipt candidateReceipt, expected int64) error {
	if !validCandidateID(id) {
		return ErrConfirmationStale
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	_, err = store.WriteFileIfGeneration(ctx, data, candidateObjectPath(id), expected)
	return err
}

// Confirm commits the exact stored candidate without fetching its URL again.
func (s *Service) Confirm(ctx context.Context, verified ActorProject, reference string) (Result, error) {
	if s == nil || s.Stores == nil {
		recordIntakeEvent(ctx, "setup", "configuration", "failure")
		return Result{Status: "unavailable", Message: "URL intake is not configured; the candidate was not committed.", FailureStage: "setup"}, nil
	}
	if strings.TrimSpace(verified.ActorID) == "" || strings.TrimSpace(verified.ProjectID) == "" {
		return Result{}, ErrInvalidScope
	}
	s.mu.Lock()
	pending, ok := s.confirmations[reference]
	if !ok || pending == nil || pending.Candidate == nil {
		delete(s.confirmations, reference)
		s.mu.Unlock()
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if pending.Candidate.scope != verified {
		s.mu.Unlock()
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if !pending.Candidate.expiresAt.After(s.now()) {
		delete(s.confirmations, reference)
		s.mu.Unlock()
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	if pending.Busy {
		s.mu.Unlock()
		return Result{Status: "conflict", Message: ErrConfirmationBusy.Error()}, nil
	}
	pending.Busy = true
	prepared := pending.Candidate
	s.mu.Unlock()

	store, err := s.Stores(verified)
	if err != nil || store == nil {
		s.removeConfirmation(reference)
		recordIntakeEvent(ctx, "storage", "dependency", "failure")
		return Result{Status: "unavailable", Message: "Project storage is unavailable; the candidate was not committed.", FailureStage: "storage"}, nil
	}
	result, err := s.commit(ctx, store, prepared)
	s.removeConfirmation(reference)
	return result, err
}

func failureResult(err error) Result {
	var failure *ProviderFailure
	if !errors.As(err, &failure) {
		return Result{Status: "unavailable", Message: "The page could not be fetched. Paste Markdown or upload a saved page capture instead.", FailureStage: "fetch"}
	}
	if failure.Kind == FailureInvalidURL {
		return Result{Status: "rejected", Message: failure.Error(), FailureStage: "validation"}
	}
	if failure.Kind == FailureTooLarge {
		return Result{Status: "rejected", Message: failure.Error(), FailureStage: "size"}
	}
	result := Result{Status: "unavailable", Message: failure.Error() + ". Paste Markdown or upload a saved page capture instead.", FailureStage: "fetch"}
	if failure.RetryAfter > 0 {
		result.RetryAfterSeconds = int64(failure.RetryAfter.Round(time.Second).Seconds())
	}
	return result
}

func fetchFailureClass(err error) string {
	var failure *ProviderFailure
	if !errors.As(err, &failure) {
		return "internal"
	}
	switch failure.Kind {
	case FailureNotConfigured:
		return "configuration"
	case FailureInvalidURL, FailureTooLarge:
		return ""
	case FailureTransport:
		return "transport"
	case FailureResponse:
		return "invalid_response"
	case FailureProvider, FailureRateLimited:
		return "provider"
	default:
		return "dependency"
	}
}

func qualityFailureClass(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var failure *QualityProviderFailure
	if errors.As(err, &failure) {
		if failure.StatusCode > 0 {
			return "provider"
		}
		switch failure.Kind {
		case "not configured":
			return "configuration"
		case "timeout":
			return "timeout"
		case "transport failed":
			return "transport"
		case "invalid_response":
			return "invalid_response"
		case "request failed":
			return "provider"
		default:
			return "dependency"
		}
	}
	return "dependency"
}

func recordIntakeEvent(ctx context.Context, stage, errorClass, outcome string) {
	observability.RecordURLIntakeEvent(ctx, stage, errorClass, outcome)
	if outcome != "fallback" {
		if requestID := observability.RequestIDFromContext(ctx); requestID != "" {
			log.Printf("URL intake internal event: stage=%s error_class=%s outcome=%s request_id=%s", stage, errorClass, outcome, requestID)
		}
	}
}

func validateTags(tags []string) error {
	for _, tag := range tags {
		if !utf8.ValidString(tag) || strings.ContainsAny(tag, "\x00\r\n") {
			return errors.New("A source tag contains unsupported characters.")
		}
	}
	return nil
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) saveConfirmation(prepared *candidate) (string, time.Time, error) {
	refBytes := make([]byte, 32)
	if _, err := rand.Read(refBytes); err != nil {
		return "", time.Time{}, err
	}
	ref := hex.EncodeToString(refBytes)
	expires := s.now().Add(ConfirmationTTL)
	prepared.expiresAt = expires
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, pending := range s.confirmations {
		if pending == nil || pending.Candidate == nil || !pending.Candidate.expiresAt.After(s.now()) {
			delete(s.confirmations, key)
		}
	}
	if len(s.confirmations) >= 1024 {
		return "", time.Time{}, errors.New("confirmation capacity reached")
	}
	s.confirmations[ref] = &pendingConfirmation{Candidate: prepared}
	return ref, expires, nil
}

func (s *Service) removeConfirmation(reference string) {
	s.mu.Lock()
	delete(s.confirmations, reference)
	s.mu.Unlock()
}

func resolveTarget(ctx context.Context, store ProjectStore, normalizedURL, identityDigest, title string) (rawTarget, error) {
	if record, _, ok, err := readIdentity(ctx, store, identityDigest); err != nil {
		return rawTarget{}, err
	} else if ok {
		rel, err := relativeRawPath(record.RawPath)
		if err != nil {
			return rawTarget{}, ErrURLConflict
		}
		data, generation, exists, err := readRaw(ctx, store, rel)
		if err != nil {
			return rawTarget{}, err
		}
		if exists {
			if source, ok := sourceIdentity(data); ok && source != normalizedURL {
				return rawTarget{}, ErrURLConflict
			}
		}
		return rawTarget{relativePath: rel, data: data, generation: generation, exists: exists, indexed: true}, nil
	}

	legacy, err := findLegacyMatches(ctx, store, normalizedURL)
	if err != nil {
		return rawTarget{}, err
	}
	if len(legacy) > 1 {
		return rawTarget{}, ErrURLConflict
	}
	if len(legacy) == 1 {
		return legacy[0], nil
	}
	filename := safeTitleFilename(title)
	data, generation, exists, err := readRaw(ctx, store, filename)
	if err != nil {
		return rawTarget{}, err
	}
	if exists {
		if source, ok := sourceIdentity(data); ok && source == normalizedURL {
			return rawTarget{relativePath: filename, data: data, generation: generation, exists: true}, nil
		}
		filename, err = availableDisambiguatedFilename(ctx, store, title, identityDigest)
		if err != nil {
			return rawTarget{}, err
		}
	}
	return rawTarget{relativePath: filename}, nil
}

func findLegacyMatches(ctx context.Context, store ProjectStore, normalizedURL string) ([]rawTarget, error) {
	files, err := store.ListRawFiles(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]rawTarget, 0, 1)
	for _, file := range files {
		rel, err := relativeRawPath(file.Path)
		if err != nil {
			continue
		}
		if !strings.EqualFold(path.Ext(rel), ".md") {
			continue
		}
		data, generation, exists, err := readRaw(ctx, store, rel)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		if source, ok := sourceIdentity(data); ok && source == normalizedURL {
			matches = append(matches, rawTarget{relativePath: rel, data: data, generation: generation, exists: true})
		}
	}
	return matches, nil
}

func sourceIdentity(data []byte) (string, bool) {
	metadata, _, hasFrontmatter, err := parseRawFrontmatter(data)
	if err != nil || !hasFrontmatter {
		return "", false
	}
	source, ok := metadata["source"].(string)
	if !ok || strings.TrimSpace(source) == "" {
		return "", false
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return "", false
	}
	normalized, err := NormalizeURL(parsed)
	return normalized, err == nil
}

func relativeRawPath(rawPath string) (string, error) {
	if !strings.HasPrefix(rawPath, "raw/") {
		return "", rawintake.ErrConflict
	}
	rel := strings.TrimPrefix(rawPath, "raw/")
	dir, filename := path.Split(rel)
	return rawintake.Destination(strings.TrimSuffix(dir, "/"), filename)
}

func readRaw(ctx context.Context, store ProjectStore, relative string) ([]byte, int64, bool, error) {
	if _, err := rawintake.Destination("", relative); err == nil {
		// The root destination is already valid.
	} else if dir, filename := path.Split(relative); filename != "" {
		if _, pathErr := rawintake.Destination(strings.TrimSuffix(dir, "/"), filename); pathErr != nil {
			return nil, 0, false, pathErr
		}
	} else {
		return nil, 0, false, err
	}
	data, generation, err := store.ReadFileWithGeneration(ctx, "raw/"+relative)
	if isNotFound(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return data, generation, true, nil
}

func safeTitleFilename(title string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.TrimSpace(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		} else if r == '-' {
			if b.Len() > 0 && !separator {
				b.WriteByte('-')
			}
			separator = true
		} else if unicode.IsSpace(r) {
			separator = true
		} else {
			separator = true
		}
		if utf8.RuneCountInString(b.String()) >= 80 {
			break
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		name = "page"
	}
	return name + ".md"
}

func availableDisambiguatedFilename(ctx context.Context, store ProjectStore, title, identityDigest string) (string, error) {
	base := strings.TrimSuffix(safeTitleFilename(title), ".md") + "-" + identityDigest + ".md"
	for attempt := 1; attempt <= 10; attempt++ {
		filename := base
		if attempt > 1 {
			filename = strings.TrimSuffix(base, ".md") + "-" + fmt.Sprint(attempt) + ".md"
		}
		if _, err := rawintake.Destination("", filename); err != nil {
			return "", err
		}
		_, _, exists, err := readRaw(ctx, store, filename)
		if err != nil {
			return "", err
		}
		if !exists {
			return filename, nil
		}
	}
	return "", ErrURLConflict
}

func usableTitle(title, markdown string, u *url.URL) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#")); heading != "" {
				return heading
			}
		}
	}
	if u != nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "Untitled Page"
}

func previewMetadata(markdown []byte) map[string]any {
	metadata, _, _, err := parseRawFrontmatter(markdown)
	if err != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}

func digestString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func identityObjectPath(identityDigest string) string {
	return identityObjectPrefix + identityDigest + ".json"
}

func readIdentity(ctx context.Context, store ProjectStore, identityDigest string) (identityRecord, int64, bool, error) {
	data, generation, err := store.ReadFileWithGeneration(ctx, identityObjectPath(identityDigest))
	if isNotFound(err) {
		return identityRecord{}, 0, false, nil
	}
	if err != nil {
		return identityRecord{}, 0, false, err
	}
	var record identityRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&record); err != nil || decoder.Decode(new(any)) != io.EOF || record.Version != 1 || record.URLSHA256 != identityDigest {
		return identityRecord{}, 0, false, ErrURLConflict
	}
	if _, err := relativeRawPath(record.RawPath); err != nil {
		return identityRecord{}, 0, false, ErrURLConflict
	}
	return record, generation, true, nil
}

func reserveIdentity(ctx context.Context, store ProjectStore, identityDigest, relativePath string) (string, error) {
	if err := validateRelativeRawPath(relativePath); err != nil {
		return "", err
	}
	indexPath := identityObjectPath(identityDigest)
	for attempt := 0; attempt < maxIdentityCASRetries; attempt++ {
		record, _, exists, err := readIdentity(ctx, store, identityDigest)
		if err != nil {
			return "", err
		}
		if exists {
			return relativeRawPathString(record.RawPath)
		}
		payload, _ := json.Marshal(identityRecord{Version: 1, URLSHA256: identityDigest, RawPath: "raw/" + relativePath})
		_, err = store.WriteFileIfGeneration(ctx, payload, indexPath, 0)
		if err == nil {
			return relativePath, nil
		}
		if errors.Is(err, storage.ErrGenerationMismatch) {
			continue
		}
		readback, _, ok, readErr := readIdentity(ctx, store, identityDigest)
		if readErr == nil && ok {
			return relativeRawPathString(readback.RawPath)
		}
		return "", fmt.Errorf("%w: %v", ErrIdentityUncertain, err)
	}
	return "", ErrURLConflict
}

func remapIdentity(ctx context.Context, store ProjectStore, identityDigest, expectedPath, newPath string) (string, error) {
	indexPath := identityObjectPath(identityDigest)
	for attempt := 0; attempt < maxIdentityCASRetries; attempt++ {
		record, generation, exists, err := readIdentity(ctx, store, identityDigest)
		if err != nil {
			return "", err
		}
		if !exists {
			return reserveIdentity(ctx, store, identityDigest, newPath)
		}
		current, err := relativeRawPathString(record.RawPath)
		if err != nil {
			return "", err
		}
		if current != expectedPath {
			return current, nil
		}
		payload, _ := json.Marshal(identityRecord{Version: 1, URLSHA256: identityDigest, RawPath: "raw/" + newPath})
		if _, err := store.WriteFileIfGeneration(ctx, payload, indexPath, generation); err == nil {
			return newPath, nil
		} else if errors.Is(err, storage.ErrGenerationMismatch) {
			continue
		} else {
			readback, _, ok, readErr := readIdentity(ctx, store, identityDigest)
			if readErr == nil && ok && readback.RawPath == "raw/"+newPath {
				return newPath, nil
			}
			return "", fmt.Errorf("%w: %v", ErrIdentityUncertain, err)
		}
	}
	return "", ErrURLConflict
}

func relativeRawPathString(rawPath string) (string, error) {
	return relativeRawPath(rawPath)
}

func validateRelativeRawPath(relative string) error {
	dir, filename := path.Split(relative)
	_, err := rawintake.Destination(strings.TrimSuffix(dir, "/"), filename)
	return err
}

func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist)
}

type expectedWriter struct {
	storage.ConditionalWriter
	path     string
	expected int64
}

func (w expectedWriter) ReadFileWithGeneration(ctx context.Context, objectPath string) ([]byte, int64, error) {
	if objectPath != w.path {
		return nil, 0, storage.ErrGenerationMismatch
	}
	data, generation, err := w.ConditionalWriter.ReadFileWithGeneration(ctx, objectPath)
	if isNotFound(err) {
		if w.expected != 0 {
			return nil, 0, storage.ErrGenerationMismatch
		}
		return nil, 0, err
	}
	if err != nil {
		return nil, 0, err
	}
	if generation != w.expected {
		return nil, 0, storage.ErrGenerationMismatch
	}
	return data, generation, nil
}

func (w expectedWriter) WriteFileIfGeneration(ctx context.Context, data []byte, objectPath string, expected int64) (int64, error) {
	if objectPath != w.path || expected != w.expected {
		return 0, storage.ErrGenerationMismatch
	}
	return w.ConditionalWriter.WriteFileIfGeneration(ctx, data, objectPath, expected)
}

func (s *Service) commit(ctx context.Context, store ProjectStore, prepared *candidate) (Result, error) {
	if prepared == nil || digestString(prepared.data) != prepared.digest {
		return Result{Status: "rejected", Message: ErrConfirmationStale.Error()}, nil
	}
	relative, err := reserveIdentity(ctx, store, prepared.identitySHA256, prepared.relativePath)
	if err != nil {
		if errors.Is(err, ErrIdentityUncertain) {
			recordIntakeEvent(ctx, "commit", "storage", "uncertain")
			return Result{Status: "uncertain", Message: ErrIdentityUncertain.Error(), FailureStage: "commit", Preview: &prepared.preview}, nil
		}
		if !errors.Is(err, ErrURLConflict) && !errors.Is(err, rawintake.ErrConflict) {
			recordIntakeEvent(ctx, "commit", "storage", "failure")
			return Result{Status: "unavailable", Message: "The raw URL identity could not be reserved; no raw article was changed.", FailureStage: "commit", Preview: &prepared.preview}, nil
		}
		return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
	}
	if relative != prepared.relativePath {
		if prepared.existing || prepared.expectedGeneration != 0 {
			return Result{Status: "conflict", Message: "The raw article changed after this preview; no raw article was changed.", Preview: &prepared.preview}, nil
		}
		prepared.relativePath = relative
	}
	for attempt := 0; attempt < 2; attempt++ {
		current, generation, exists, readErr := readRaw(ctx, store, prepared.relativePath)
		if readErr != nil {
			recordIntakeEvent(ctx, "commit", "storage", "failure")
			return Result{Status: "unavailable", Message: "Could not read the target raw article before saving; no raw article was changed.", FailureStage: "commit", Preview: &prepared.preview}, nil
		}
		if exists {
			source, sourceKnown := sourceIdentity(current)
			if !sourceKnown || source != prepared.normalizedURL {
				if prepared.existing || prepared.expectedGeneration != 0 || attempt != 0 {
					return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
				}
				nextPath, pathErr := availableDisambiguatedFilename(ctx, store, prepared.preview.Title, prepared.identitySHA256)
				if pathErr != nil {
					recordIntakeEvent(ctx, "commit", "storage", "failure")
					return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
				}
				reserved, remapErr := remapIdentity(ctx, store, prepared.identitySHA256, prepared.relativePath, nextPath)
				if remapErr != nil {
					recordIntakeEvent(ctx, "commit", "storage", "uncertain")
					return Result{Status: "uncertain", Message: ErrIdentityUncertain.Error(), FailureStage: "commit", Preview: &prepared.preview}, nil
				}
				prepared.relativePath = reserved
				continue
			}
			if bytes.Equal(current, prepared.data) {
				return storedResult(rawintake.Result{Path: "raw/" + prepared.relativePath, SHA256: prepared.digest, Status: rawintake.StatusAlreadyExists}, &prepared.preview), nil
			}
			if generation != prepared.expectedGeneration {
				return Result{Status: "conflict", Message: "The raw article changed after this preview; no raw article was changed.", Preview: &prepared.preview}, nil
			}
		} else if prepared.expectedGeneration != 0 {
			return Result{Status: "conflict", Message: "The raw article changed after this preview; no raw article was changed.", Preview: &prepared.preview}, nil
		}
		overwrite := prepared.expectedGeneration != 0
		writer := expectedWriter{ConditionalWriter: store, path: "raw/" + prepared.relativePath, expected: prepared.expectedGeneration}
		committed, commitErr := rawintake.Commit(ctx, writer, prepared.relativePath, prepared.data, overwrite)
		if commitErr != nil {
			if errors.Is(commitErr, rawintake.ErrCommitUncertain) {
				readback, _, readbackExists, readErr := readRaw(ctx, store, prepared.relativePath)
				if readErr == nil && readbackExists && bytes.Equal(readback, prepared.data) {
					recordIntakeEvent(ctx, "commit", "storage", "recovered")
					return storedResult(rawintake.Result{Path: "raw/" + prepared.relativePath, SHA256: prepared.digest, Status: rawintake.StatusAlreadyExists}, &prepared.preview), nil
				}
				recordIntakeEvent(ctx, "commit", "storage", "uncertain")
				return Result{Status: "uncertain", Message: "The storage result is uncertain. Check the raw path before retrying.", FailureStage: "commit", Preview: &prepared.preview}, nil
			}
			if errors.Is(commitErr, rawintake.ErrConflict) {
				readback, _, readbackExists, readErr := readRaw(ctx, store, prepared.relativePath)
				if readErr == nil && readbackExists && bytes.Equal(readback, prepared.data) {
					return storedResult(rawintake.Result{Path: "raw/" + prepared.relativePath, SHA256: prepared.digest, Status: rawintake.StatusAlreadyExists}, &prepared.preview), nil
				}
			}
			if errors.Is(commitErr, rawintake.ErrConflict) && attempt == 0 && !prepared.existing && prepared.expectedGeneration == 0 {
				readback, _, readbackExists, readErr := readRaw(ctx, store, prepared.relativePath)
				if readErr == nil && readbackExists {
					if source, ok := sourceIdentity(readback); ok && source != prepared.normalizedURL {
						nextPath, pathErr := availableDisambiguatedFilename(ctx, store, prepared.preview.Title, prepared.identitySHA256)
						if pathErr == nil {
							prepared.relativePath, err = remapIdentity(ctx, store, prepared.identitySHA256, prepared.relativePath, nextPath)
							if err == nil {
								continue
							}
						}
					}
				}
			}
			if errors.Is(commitErr, rawintake.ErrConflict) || errors.Is(commitErr, storage.ErrGenerationMismatch) {
				return Result{Status: "conflict", Message: "The raw article changed after this preview; no raw article was changed.", Preview: &prepared.preview}, nil
			}
			recordIntakeEvent(ctx, "commit", "storage", "failure")
			return Result{Status: "unavailable", Message: "The raw article could not be saved; no successful write was confirmed.", FailureStage: "commit", Preview: &prepared.preview}, nil
		}
		return storedResult(committed, &prepared.preview), nil
	}
	return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
}

func storedResult(committed rawintake.Result, preview *CandidatePreview) Result {
	return Result{
		Status: "stored", Message: "URL content saved to raw/. Run the Project pipeline separately to process it.",
		Preview: preview, Storage: &StorageOutcome{Path: committed.Path, SHA256: committed.SHA256, Status: string(committed.Status)},
		ProcessingStatus: "not_started",
	}
}
