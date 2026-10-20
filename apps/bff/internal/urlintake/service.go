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
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	ConfirmationTTL       = 15 * time.Minute
	identityObjectPrefix  = "cache/raw_url_identity/v1/"
	maxIdentityCASRetries = 8
)

var DefaultQualityThresholds = QualityThresholds{
	RejectShellAt: 0.95, RejectContentAtMost: 0.05,
	UsableShellAtMost: 0.10, UsableContentAt: 0.90,
}

var (
	ErrInvalidScope       = errors.New("verified actor and project are required")
	ErrURLConflict        = errors.New("URL identity conflicts with existing raw content")
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
	Reason            string         `json:"reason,omitempty"`
	Warning           string         `json:"warning,omitempty"`
	Provider          string         `json:"provider"`
	ProviderTruncated *bool          `json:"provider_truncated,omitempty"`
}

type Result struct {
	Status            string            `json:"status"`
	Message           string            `json:"message"`
	ConfirmationRef   string            `json:"confirmation_ref,omitempty"`
	ExpiresAt         string            `json:"expires_at,omitempty"`
	RetryAfterSeconds int64             `json:"retry_after_seconds,omitempty"`
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
	if s == nil {
		return Result{Status: "unavailable", Message: manualAlternative}, nil
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
	if s == nil || s.Stores == nil || s.Fetcher == nil {
		return Result{Status: "unavailable", Message: manualAlternative}, nil
	}
	validatedURL, err := ValidatePublicURL(ctx, request.URL, s.Resolver)
	if err != nil {
		if errors.Is(err, ErrUnresolvedURL) {
			return Result{Status: "unavailable", Message: manualAlternative}, nil
		}
		return Result{Status: "rejected", Message: "Use a public HTTP or HTTPS URL without embedded credentials."}, nil
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
		return Result{Status: "unavailable", Message: manualAlternative}, nil
	}
	page, err := s.Fetcher.Fetch(ctx, request.URL)
	if err != nil {
		return failureResult(err), nil
	}
	if len(page.Markdown) > MaxCandidateBytes {
		return Result{Status: "rejected", Message: "The fetched content exceeds the supported size."}, nil
	}
	if strings.TrimSpace(page.Markdown) == "" {
		return Result{Status: "rejected", Message: "The Fetch provider returned no usable content. " + manualAlternative}, nil
	}
	title := usableTitle(page.Title, page.Markdown, validatedURL)
	identityDigest := digestString([]byte(normalizedURL))
	target, err := resolveTarget(ctx, store, normalizedURL, identityDigest, title)
	if err != nil {
		if errors.Is(err, ErrURLConflict) || errors.Is(err, rawintake.ErrConflict) {
			return Result{Status: "conflict", Message: "More than one existing raw file matches this URL; no file was changed."}, nil
		}
		return Result{Status: "unavailable", Message: manualAlternative}, nil
	}
	metadata := SourceMetadata{Title: title, SourceURL: request.URL, Tags: request.Tags}
	data, _, err := MergeRawMarkdown(target.data, metadata, page.Markdown, s.now())
	if err != nil {
		return Result{Status: "conflict", Message: "The existing raw metadata could not be refreshed safely; no file was changed."}, nil
	}
	digest := digestString(data)
	preview := CandidatePreview{
		Title: title, SourceURL: request.URL, Markdown: string(data),
		Metadata: previewMetadata(data), CandidateSHA256: digest,
		Provider: DeepSeekFetchProvider, ProviderTruncated: page.Truncated,
	}
	if s.Evaluator == nil {
		return Result{Status: "unavailable", Message: manualAlternative, Preview: &preview}, nil
	}
	providerMetadata := map[string]any{}
	if page.Truncated != nil {
		providerMetadata["truncated"] = *page.Truncated
	}
	evaluation, err := s.Evaluator.Evaluate(ctx, QualityInput{
		URL: request.URL, Title: title, Markdown: page.Markdown,
		Provider: DeepSeekFetchProvider, ProviderMetadata: providerMetadata,
	})
	if err != nil {
		return Result{Status: "unavailable", Message: manualAlternative, Preview: &preview}, nil
	}
	quality := DecideQuality(evaluation, s.Thresholds)
	if quality == QualityRejected {
		preview.Reason = "The page appears to be a blocking, error, or loading shell without readable main content."
		return Result{Status: "rejected", Message: preview.Reason + " " + manualAlternative, Preview: &preview}, nil
	}
	needsConfirmation := quality == QualityNeedsConfirmation
	if page.Truncated != nil && *page.Truncated {
		needsConfirmation = true
		preview.Warning = "The Fetch provider explicitly marked this content as truncated."
	}
	if needsConfirmation {
		preview.Reason = "The page could not be classified as clearly substantive and complete."
		ref, expiresAt, err := s.saveConfirmation(&candidate{
			scope: verified, originalURL: request.URL, normalizedURL: normalizedURL,
			identitySHA256: identityDigest, relativePath: target.relativePath,
			expectedGeneration: target.generation, existing: target.exists,
			data: data, digest: digest, preview: preview,
		})
		if err != nil {
			return Result{Status: "unavailable", Message: manualAlternative, Preview: &preview}, nil
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

// Confirm commits the exact stored candidate without fetching its URL again.
func (s *Service) Confirm(ctx context.Context, verified ActorProject, reference string) (Result, error) {
	if s == nil || s.Stores == nil {
		return Result{Status: "unavailable", Message: manualAlternative}, nil
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
		return Result{Status: "unavailable", Message: manualAlternative}, nil
	}
	result, err := s.commit(ctx, store, prepared)
	s.removeConfirmation(reference)
	return result, err
}

const manualAlternative = "Could not safely fetch and check this public page. Paste the Markdown or clip the page and upload a supported file; logged-in, JavaScript-rendered, paywalled, or anti-bot pages may need manual capture."

func failureResult(err error) Result {
	var failure *ProviderFailure
	if !errors.As(err, &failure) {
		return Result{Status: "unavailable", Message: manualAlternative}
	}
	if failure.Kind == FailureInvalidURL || failure.Kind == FailureTooLarge {
		return Result{Status: "rejected", Message: failure.Error() + ". " + manualAlternative}
	}
	result := Result{Status: "unavailable", Message: failure.Error() + ". " + manualAlternative}
	if failure.RetryAfter > 0 {
		result.RetryAfterSeconds = int64(failure.RetryAfter.Round(time.Second).Seconds())
	}
	return result
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
			return Result{Status: "uncertain", Message: ErrIdentityUncertain.Error(), Preview: &prepared.preview}, nil
		}
		return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
	}
	if relative != prepared.relativePath {
		if prepared.existing || prepared.expectedGeneration != 0 {
			return Result{Status: "conflict", Message: ErrConfirmationStale.Error(), Preview: &prepared.preview}, nil
		}
		prepared.relativePath = relative
	}
	for attempt := 0; attempt < 2; attempt++ {
		current, generation, exists, readErr := readRaw(ctx, store, prepared.relativePath)
		if readErr != nil {
			return Result{Status: "unavailable", Message: manualAlternative, Preview: &prepared.preview}, nil
		}
		if exists {
			source, sourceKnown := sourceIdentity(current)
			if !sourceKnown || source != prepared.normalizedURL {
				if prepared.existing || prepared.expectedGeneration != 0 || attempt != 0 {
					return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
				}
				nextPath, pathErr := availableDisambiguatedFilename(ctx, store, prepared.preview.Title, prepared.identitySHA256)
				if pathErr != nil {
					return Result{Status: "conflict", Message: ErrURLConflict.Error(), Preview: &prepared.preview}, nil
				}
				reserved, remapErr := remapIdentity(ctx, store, prepared.identitySHA256, prepared.relativePath, nextPath)
				if remapErr != nil {
					return Result{Status: "uncertain", Message: ErrIdentityUncertain.Error(), Preview: &prepared.preview}, nil
				}
				prepared.relativePath = reserved
				continue
			}
			if generation != prepared.expectedGeneration {
				return Result{Status: "conflict", Message: ErrConfirmationStale.Error(), Preview: &prepared.preview}, nil
			}
		} else if prepared.expectedGeneration != 0 {
			return Result{Status: "conflict", Message: ErrConfirmationStale.Error(), Preview: &prepared.preview}, nil
		}
		overwrite := prepared.expectedGeneration != 0
		writer := expectedWriter{ConditionalWriter: store, path: "raw/" + prepared.relativePath, expected: prepared.expectedGeneration}
		committed, commitErr := rawintake.Commit(ctx, writer, prepared.relativePath, prepared.data, overwrite)
		if commitErr != nil {
			if errors.Is(commitErr, rawintake.ErrCommitUncertain) {
				readback, _, readbackExists, readErr := readRaw(ctx, store, prepared.relativePath)
				if readErr == nil && readbackExists && bytes.Equal(readback, prepared.data) {
					return storedResult(rawintake.Result{Path: "raw/" + prepared.relativePath, SHA256: prepared.digest, Status: rawintake.StatusAlreadyExists}, &prepared.preview), nil
				}
				return Result{Status: "uncertain", Message: "The storage result is uncertain. Check the raw path before retrying.", Preview: &prepared.preview}, nil
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
				return Result{Status: "conflict", Message: ErrConfirmationStale.Error(), Preview: &prepared.preview}, nil
			}
			return Result{Status: "unavailable", Message: manualAlternative, Preview: &prepared.preview}, nil
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
