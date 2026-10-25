package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	profileGuidanceSchemaVersion = profileartifacts.GuidanceSchema
	profileGuidanceObjectPrefix  = profileartifacts.GuidancePathPrefix
	profileGuidanceReadLimit     = profileartifacts.MaxArtifactBytes
	syntoVaultSchemaCharLimit    = 1500
)

const defaultProfileVaultSchema = "# Vault Schema\n\n" +
	"## Folder Structure\n" +
	"- `raw/` — input notes (immutable, never edited by synto)\n" +
	"- `wiki/` — AI-synthesised articles (managed by synto)\n" +
	"- `wiki/.drafts/` — pending human review\n\n" +
	"## Note Format\n" +
	"Every wiki note has YAML frontmatter with: title, tags, sources, " +
	"confidence, status, created, updated.\n\n" +
	"## Links\n" +
	"Use `[[Article Title]]` wikilinks between notes.\n"

type profileGuidancePin struct {
	ProfileRevision         int64
	RequirementsDigest      string
	Revision                string
	SchemaVersion           string
	Content                 string
	BootstrapRef            *profileartifacts.BootstrapGuidanceRef
	BootstrapStatus         string
	BootstrapConfirmedAt    string
	SourceProfileRevision   int64
	SourceContentGeneration string
	SourceCandidateID       string
	SourceInputDigest       string
	SourceModelVersion      string
	SourcePromptVersion     string
	SourceSchemaVersion     string
}

type profileGuidanceRef struct {
	Revision      string `firestore:"revision"`
	InputDigest   string `firestore:"input_digest"`
	ModelVersion  string `firestore:"model_version"`
	PromptVersion string `firestore:"prompt_version"`
	SchemaVersion string `firestore:"schema_version"`
}

func profileGuidanceRefFromFirestore(value interface{}) profileGuidanceRef {
	fields, ok := value.(map[string]interface{})
	if !ok {
		return profileGuidanceRef{}
	}
	revision, canonical := fields["revision"].(string)
	canonical = canonical && revision != ""
	read := func(wireKey, legacyKey string) string {
		key := legacyKey
		if canonical {
			key = wireKey
		}
		text, _ := fields[key].(string)
		return text
	}
	return profileGuidanceRef{
		Revision: read("revision", "Revision"), InputDigest: read("input_digest", "InputDigest"),
		ModelVersion: read("model_version", "ModelVersion"), PromptVersion: read("prompt_version", "PromptVersion"),
		SchemaVersion: read("schema_version", "SchemaVersion"),
	}
}

type profileGuidanceActive struct {
	CandidateID        string `firestore:"candidate_id"`
	ContentGeneration  string `firestore:"content_generation"`
	DictionaryRevision string `firestore:"dictionary_revision"`
	TagSetRevision     string `firestore:"tag_set_revision"`
	QueryRuleRevision  string `firestore:"query_rule_revision"`
	GuidanceRevision   string `firestore:"guidance_revision"`
}

type profileBootstrapGuidance struct {
	Revision        string  `firestore:"revision"`
	InputDigest     string  `firestore:"input_digest"`
	ProfileRevision int64   `firestore:"profile_revision"`
	Status          string  `firestore:"status"`
	ModelVersion    string  `firestore:"model_version"`
	PromptVersion   string  `firestore:"prompt_version"`
	SchemaVersion   string  `firestore:"schema_version"`
	ConfirmedAt     *string `firestore:"confirmed_at"`
}

type profileGuidanceState struct {
	ProjectID         string                       `firestore:"project_id"`
	Revision          int64                        `firestore:"revision"`
	Requirements      []profileGuidanceRequirement `firestore:"requirements"`
	BootstrapGuidance *profileBootstrapGuidance    `firestore:"bootstrap_guidance"`
	Active            *profileGuidanceActive       `firestore:"active"`
}

type profileGuidanceRequirement struct {
	ID   string `firestore:"id" json:"id"`
	Text string `firestore:"text" json:"text"`
}

type profileGuidanceCandidate struct {
	CandidateID        string             `firestore:"candidate_id"`
	Source             string             `firestore:"source"`
	BaseRevision       int64              `firestore:"base_revision"`
	ContentGeneration  string             `firestore:"content_generation"`
	RequirementsDigest string             `firestore:"requirements_digest"`
	Guidance           profileGuidanceRef `firestore:"guidance"`
}

type profileGuidanceEnvelope = profileartifacts.GenerationGuidanceEnvelope

// This worker-start seam pins both Profile input identity and the exact active
// or confirmed bootstrap guidance; an unconfirmed candidate is never a fallback.
var resolveProfileGuidanceAtCompileStart = pinActiveProfileGuidance

func pinActiveProfileGuidance(ctx context.Context, cfg workerConfig, objects objectStore) (*profileGuidancePin, error) {
	if !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) || objects == nil {
		return nil, errors.New("invalid worker Project identity for Profile guidance")
	}
	cloudProject := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if cloudProject == "" {
		return nil, errors.New("Google Cloud project identity is unavailable for Profile guidance")
	}
	client, err := newProfileFirestoreClient(ctx, cloudProject)
	if err != nil {
		return nil, fmt.Errorf("create Profile guidance Firestore reader: %w", err)
	}
	defer client.Close()
	return readActiveProfileGuidance(ctx, client, cfg, objects)
}

func readActiveProfileGuidance(ctx context.Context, client *firestore.Client, cfg workerConfig, objects objectStore) (*profileGuidancePin, error) {
	if client == nil || objects == nil || !auth.ValidPathSegment(cfg.UserID) || !auth.ValidPathSegment(cfg.ProjectID) {
		return nil, errors.New("invalid Profile guidance reader input")
	}
	if cfg.PipelineReservationID != "" {
		if pin, handled, err := readPipelineIntentGuidance(ctx, client, cfg, objects); handled || err != nil {
			return pin, err
		}
	}
	projectID := cfg.UserID + "_" + cfg.ProjectID
	stateRef := scopedfirestore.Collection(client, "projects").Doc(projectID).Collection("profile").Doc("state")
	stateSnapshot, err := stateRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Profile state: %w", err)
	}
	var state profileGuidanceState
	if err := stateSnapshot.DataTo(&state); err != nil {
		return nil, fmt.Errorf("decode Profile state: %w", err)
	}
	if state.ProjectID != cfg.ProjectID {
		return nil, errors.New("Profile state Project identity mismatch")
	}
	if _, exists := stateSnapshot.Data()["requirements"]; !exists {
		return nil, errors.New("Profile state has no requirements array")
	}
	if state.Active == nil {
		if len(state.Requirements) > 0 {
			return readConfirmedBootstrapProfileGuidance(ctx, state, cfg, objects)
		}
		return &profileGuidancePin{ProfileRevision: state.Revision, RequirementsDigest: profileRequirementsDigest(state.Requirements)}, nil
	}
	active := state.Active
	if active.CandidateID == "" || active.ContentGeneration == "" || active.DictionaryRevision == "" || active.TagSetRevision == "" || active.QueryRuleRevision == "" || !isLowerSHA256(active.GuidanceRevision) {
		return nil, errors.New("active Profile pointer is incomplete")
	}
	candidateSnapshot, err := stateRef.Collection("candidates").Doc(active.CandidateID).Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read active Profile candidate: %w", err)
	}
	var candidate profileGuidanceCandidate
	if err := candidateSnapshot.DataTo(&candidate); err != nil {
		return nil, fmt.Errorf("decode active Profile candidate: %w", err)
	}
	if candidate.CandidateID != active.CandidateID || (candidate.Source != "manual" && candidate.Source != "compile_auto") || candidate.ContentGeneration != active.ContentGeneration {
		return nil, errors.New("active Profile candidate identity mismatch")
	}
	ref := candidate.Guidance
	if ref.Revision != active.GuidanceRevision || !isLowerSHA256(ref.InputDigest) || ref.ModelVersion == "" || ref.PromptVersion == "" || ref.SchemaVersion != profileGuidanceSchemaVersion || candidate.RequirementsDigest != ref.InputDigest {
		return nil, errors.New("active Profile guidance reference is incomplete")
	}
	relativePath := profileartifacts.GuidanceObjectPath(ref.Revision)
	if relativePath == "" {
		return nil, errors.New("active Profile guidance reference has an invalid object revision")
	}
	objectName := workerProjectObjectPrefix(cfg) + relativePath
	data, _, err := objects.Read(ctx, objectName, 0, profileGuidanceReadLimit)
	if err != nil {
		return nil, fmt.Errorf("read active Profile guidance artifact: %w", err)
	}
	envelope, err := validateProfileGuidanceArtifact(data, ref)
	if err != nil {
		return nil, err
	}
	return &profileGuidancePin{
		ProfileRevision: state.Revision, RequirementsDigest: profileRequirementsDigest(state.Requirements),
		Revision: ref.Revision, SchemaVersion: envelope.SchemaVersion, Content: envelope.CompileGuidance, BootstrapStatus: "active",
		SourceProfileRevision:   candidate.BaseRevision,
		SourceContentGeneration: candidate.ContentGeneration, SourceCandidateID: candidate.CandidateID,
		SourceInputDigest: ref.InputDigest, SourceModelVersion: ref.ModelVersion,
		SourcePromptVersion: ref.PromptVersion, SourceSchemaVersion: ref.SchemaVersion,
	}, nil
}

type storedPipelineProfileIntent struct {
	UserID                         string                                 `firestore:"user_id"`
	ProjectID                      string                                 `firestore:"project_id"`
	AttemptID                      string                                 `firestore:"attempt_id"`
	IntentStatus                   string                                 `firestore:"intent_status"`
	IntentMode                     string                                 `firestore:"intent_mode"`
	SettlementStatus               string                                 `firestore:"settlement_status"`
	ProfileRevision                int64                                  `firestore:"profile_revision"`
	ProfileRequirements            []profileGuidanceRequirement           `firestore:"profile_requirements"`
	RequirementsDigest             string                                 `firestore:"requirements_digest"`
	ConsumedGuidanceKind           string                                 `firestore:"consumed_guidance_kind"`
	ConsumedGuidanceStatus         string                                 `firestore:"consumed_guidance_status"`
	ConsumedGuidanceRevision       string                                 `firestore:"consumed_guidance_revision"`
	ConsumedGuidanceSourceRevision int64                                  `firestore:"consumed_guidance_source_revision"`
	ConsumedGuidanceInputDigest    string                                 `firestore:"consumed_guidance_input_digest"`
	ConsumedGuidanceModelVersion   string                                 `firestore:"consumed_guidance_model_version"`
	ConsumedGuidancePromptVersion  string                                 `firestore:"consumed_guidance_prompt_version"`
	ConsumedGuidanceSchemaVersion  string                                 `firestore:"consumed_guidance_schema_version"`
	ConsumedActiveGuidance         profileGuidanceRef                     `firestore:"consumed_active_guidance"`
	ConsumedBootstrapGuidance      *profileartifacts.BootstrapGuidanceRef `firestore:"consumed_bootstrap_guidance"`
	ActiveCandidateID              string                                 `firestore:"active_candidate_id"`
	ActiveContentGeneration        string                                 `firestore:"active_content_generation"`
}

func readPipelineIntentGuidance(ctx context.Context, client *firestore.Client, cfg workerConfig, objects objectStore) (*profileGuidancePin, bool, error) {
	reservationRef := scopedfirestore.Collection(client, "pipeline_quota_reservations").Doc(cfg.PipelineReservationID)
	snapshot, err := reservationRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("read Pipeline Profile snapshot: %w", err)
	}
	var reservation storedPipelineProfileIntent
	if err := snapshot.DataTo(&reservation); err != nil {
		return nil, true, fmt.Errorf("decode Pipeline Profile snapshot: %w", err)
	}
	reservation.ConsumedActiveGuidance = profileGuidanceRefFromFirestore(snapshot.Data()["consumed_active_guidance"])
	if reservation.IntentMode == "" {
		return nil, false, nil
	}
	if reservation.UserID != cfg.UserID || reservation.ProjectID != cfg.ProjectID || reservation.AttemptID != cfg.PipelineReservationID ||
		(reservation.IntentStatus != "invoking" && reservation.IntentStatus != "invocation_unknown" && reservation.IntentStatus != "compiling") ||
		reservation.SettlementStatus != "pending" || reservation.ProfileRevision < 0 ||
		!isLowerSHA256(reservation.RequirementsDigest) || profileRequirementsDigest(reservation.ProfileRequirements) != reservation.RequirementsDigest {
		return nil, true, errors.New("Pipeline Profile snapshot is incomplete or no longer owned by this run")
	}
	if reservation.ProfileRevision == 0 {
		if len(reservation.ProfileRequirements) != 0 || reservation.ConsumedGuidanceKind != "none" {
			return nil, true, errors.New("Pipeline Profile snapshot has invalid empty Profile evidence")
		}
		return nil, true, nil
	}
	pin := &profileGuidancePin{
		ProfileRevision: reservation.ProfileRevision, RequirementsDigest: reservation.RequirementsDigest,
		BootstrapStatus:         reservation.ConsumedGuidanceStatus,
		SourceProfileRevision:   reservation.ConsumedGuidanceSourceRevision,
		SourceContentGeneration: reservation.ActiveContentGeneration,
		SourceCandidateID:       reservation.ActiveCandidateID,
		SourceInputDigest:       reservation.ConsumedGuidanceInputDigest,
		SourceModelVersion:      reservation.ConsumedGuidanceModelVersion,
		SourcePromptVersion:     reservation.ConsumedGuidancePromptVersion,
		SourceSchemaVersion:     reservation.ConsumedGuidanceSchemaVersion,
	}
	switch reservation.ConsumedGuidanceKind {
	case "none":
		if len(reservation.ProfileRequirements) != 0 || reservation.ConsumedGuidanceRevision != "" {
			return nil, true, errors.New("Pipeline Profile snapshot is missing guidance for saved requirements")
		}
	case "active":
		ref := reservation.ConsumedActiveGuidance
		if len(reservation.ProfileRequirements) == 0 || ref.Revision == "" || ref.Revision != reservation.ConsumedGuidanceRevision ||
			ref.InputDigest != reservation.ConsumedGuidanceInputDigest || ref.ModelVersion != reservation.ConsumedGuidanceModelVersion ||
			ref.PromptVersion != reservation.ConsumedGuidancePromptVersion || ref.SchemaVersion != reservation.ConsumedGuidanceSchemaVersion ||
			!profileartifacts.IsSHA256(ref.Revision) || !profileartifacts.IsSHA256(ref.InputDigest) ||
			ref.SchemaVersion != profileGuidanceSchemaVersion || reservation.ActiveCandidateID == "" ||
			reservation.ActiveContentGeneration == "" || reservation.ConsumedGuidanceSourceRevision < 1 {
			return nil, true, errors.New("Pipeline active guidance snapshot is incomplete")
		}
		if reservation.IntentMode == string(pipelinequota.ModeFull) && ref.InputDigest != reservation.RequirementsDigest {
			return nil, true, errors.New("Pipeline active guidance does not match the saved Profile requirements")
		}
		data, _, err := objects.Read(ctx, workerProjectObjectPrefix(cfg)+profileartifacts.GuidanceObjectPath(ref.Revision), 0, profileGuidanceReadLimit)
		if err != nil {
			return nil, true, fmt.Errorf("read pinned active Profile guidance artifact: %w", err)
		}
		envelope, err := validateProfileGuidanceArtifact(data, ref)
		if err != nil {
			return nil, true, err
		}
		pin.Revision, pin.SchemaVersion, pin.Content = ref.Revision, envelope.SchemaVersion, envelope.CompileGuidance
	case "bootstrap":
		ref := reservation.ConsumedBootstrapGuidance
		if ref == nil || len(reservation.ProfileRequirements) == 0 || ref.Revision != reservation.ConsumedGuidanceRevision ||
			ref.ProfileRevision != reservation.ProfileRevision || ref.InputDigest != reservation.RequirementsDigest ||
			ref.InputDigest != reservation.ConsumedGuidanceInputDigest || ref.ModelVersion != reservation.ConsumedGuidanceModelVersion ||
			ref.PromptVersion != reservation.ConsumedGuidancePromptVersion || ref.SchemaVersion != reservation.ConsumedGuidanceSchemaVersion {
			return nil, true, errors.New("Pipeline bootstrap guidance snapshot is incomplete")
		}
		data, _, err := objects.Read(ctx, workerProjectObjectPrefix(cfg)+profileartifacts.BootstrapObjectPath(ref.Revision), 0, profileGuidanceReadLimit)
		if err != nil {
			return nil, true, fmt.Errorf("read pinned bootstrap Profile guidance artifact: %w", err)
		}
		envelope, err := profileartifacts.ValidateBootstrapGuidance(data, *ref)
		if err != nil {
			return nil, true, fmt.Errorf("validate pinned bootstrap Profile guidance artifact: %w", err)
		}
		refCopy := *ref
		pin.BootstrapRef = &refCopy
		pin.Revision, pin.SchemaVersion, pin.Content = ref.Revision, envelope.SchemaVersion, envelope.CompileGuidance
	default:
		return nil, true, errors.New("Pipeline Profile snapshot has an unknown guidance source")
	}
	return pin, true, nil
}

func readConfirmedBootstrapProfileGuidance(ctx context.Context, state profileGuidanceState, cfg workerConfig, objects objectStore) (*profileGuidancePin, error) {
	bootstrap := state.BootstrapGuidance
	if bootstrap == nil || bootstrap.Status != "confirmed" {
		return nil, errors.New("Profile bootstrap guidance is pending confirmation; first compile is blocked")
	}
	if state.Revision < 1 || bootstrap.ProfileRevision != state.Revision || !isLowerSHA256(bootstrap.InputDigest) ||
		bootstrap.InputDigest != profileRequirementsDigest(state.Requirements) || !isLowerSHA256(bootstrap.Revision) ||
		strings.TrimSpace(bootstrap.ModelVersion) == "" || strings.TrimSpace(bootstrap.PromptVersion) == "" ||
		bootstrap.SchemaVersion != profileartifacts.BootstrapGuidanceSchema || bootstrap.ConfirmedAt == nil || strings.TrimSpace(*bootstrap.ConfirmedAt) == "" {
		return nil, errors.New("confirmed Profile bootstrap guidance is stale or incomplete; first compile is blocked")
	}
	relativePath := profileartifacts.BootstrapObjectPath(bootstrap.Revision)
	if relativePath == "" {
		return nil, errors.New("confirmed Profile bootstrap guidance has an invalid object revision")
	}
	objectName := workerProjectObjectPrefix(cfg) + relativePath
	data, _, err := objects.Read(ctx, objectName, 0, profileGuidanceReadLimit)
	if err != nil {
		return nil, fmt.Errorf("read confirmed Profile bootstrap guidance artifact: %w", err)
	}
	ref := profileartifacts.BootstrapGuidanceRef{
		Revision: bootstrap.Revision, ProfileRevision: bootstrap.ProfileRevision,
		InputDigest: bootstrap.InputDigest, ModelVersion: bootstrap.ModelVersion,
		PromptVersion: bootstrap.PromptVersion, SchemaVersion: bootstrap.SchemaVersion,
	}
	envelope, err := profileartifacts.ValidateBootstrapGuidance(data, ref)
	if err != nil {
		return nil, fmt.Errorf("validate confirmed Profile bootstrap guidance artifact: %w", err)
	}
	return &profileGuidancePin{
		ProfileRevision: state.Revision, RequirementsDigest: bootstrap.InputDigest,
		Revision: bootstrap.Revision, SchemaVersion: envelope.SchemaVersion, Content: envelope.CompileGuidance,
		BootstrapRef: &ref, BootstrapStatus: bootstrap.Status, BootstrapConfirmedAt: *bootstrap.ConfirmedAt,
		SourceProfileRevision: bootstrap.ProfileRevision, SourceInputDigest: bootstrap.InputDigest,
		SourceModelVersion: bootstrap.ModelVersion, SourcePromptVersion: bootstrap.PromptVersion,
		SourceSchemaVersion: bootstrap.SchemaVersion,
	}, nil
}

func profileRequirementsDigest(requirements []profileGuidanceRequirement) string {
	if requirements == nil {
		requirements = []profileGuidanceRequirement{}
	}
	data, _ := json.Marshal(requirements)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validateProfileGuidanceArtifact(data []byte, ref profileGuidanceRef) (profileGuidanceEnvelope, error) {
	return profileartifacts.ValidateGuidance(data, profileartifacts.DerivedRef{
		Revision: ref.Revision, InputDigest: ref.InputDigest, ModelVersion: ref.ModelVersion,
		PromptVersion: ref.PromptVersion, SchemaVersion: ref.SchemaVersion,
	})
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func profileGuidanceSchema(vault string, pin *profileGuidancePin) ([]byte, error) {
	if pin == nil || pin.Content == "" {
		return nil, nil
	}
	if !isLowerSHA256(pin.Revision) {
		return nil, errors.New("pinned Profile guidance revision is invalid")
	}
	if !utf8.ValidString(pin.Content) {
		return nil, errors.New("active Profile guidance is not valid UTF-8")
	}
	base := []byte(defaultProfileVaultSchema)
	if existing, err := readRegularFileWithin(vault, "vault-schema.md"); err == nil {
		base = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read existing vault-schema.md: %w", err)
	}
	separator := "\n\n"
	if len(base) > 0 && base[len(base)-1] == '\n' {
		separator = "\n"
	}
	materialized := string(base) + separator + "## Project Profile Compile Guidance\n" + pin.Content + "\n"
	if utf8.RuneCountInString(materialized) > syntoVaultSchemaCharLimit {
		return nil, fmt.Errorf("active Profile guidance plus vault schema exceeds Synto's 1500-character adapter budget; edit Profile guidance before compiling")
	}
	return []byte(materialized), nil
}

func profileGuidanceSchemaPath(vault string, pin *profileGuidancePin, configHome string) (string, error) {
	data, err := profileGuidanceSchema(vault, pin)
	if err != nil || data == nil {
		return "", err
	}
	path := filepath.Join(configHome, "profile-vault-schema.md")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write pinned Profile guidance schema: %w", err)
	}
	return path, nil
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// Both the compile-start reader and completion writer use the configured database.
var newProfileFirestoreClient = func(ctx context.Context, project string) (*firestore.Client, error) {
	database := profileFirestoreDatabaseID()
	client, err := firestore.NewClientWithDatabase(ctx, project, database)
	if err != nil {
		return nil, err
	}
	if err := scopedfirestore.RegisterLocalScope(client, os.Getenv("LOCAL_CLOUD_SCOPE")); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func profileFirestoreDatabaseID() string {
	database := firstNonEmptyEnv("FIRESTORE_DATABASE_ID")
	if database == "" {
		database = "(default)"
	}
	return database
}

var persistProfileCompileReceipt = writeProfileCompileReceipt

func recordProfileCompileReceipt(ctx context.Context, cfg workerConfig, manifest generation.Manifest, objectGeneration int64) error {
	pin := cfg.pinnedProfileGuidance
	// No saved Profile means there is no Profile reconciliation to dispatch.
	if pin == nil || pin.ProfileRevision == 0 {
		return nil
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if objectGeneration <= 0 || !isLowerSHA256(pin.RequirementsDigest) {
		return errors.New("invalid Profile compile receipt evidence")
	}
	if _, ok := manifest.File("cache/concepts.jsonl"); !ok {
		return errors.New("Profile compile receipt has no concepts")
	}
	if _, ok := manifest.File("cache/id_map.json"); !ok {
		return errors.New("Profile compile receipt has no ID map")
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	receipt := profileruntime.CompileReceipt{
		UserID: cfg.UserID, ProjectID: cfg.ProjectID, ExecutionID: cfg.ExecutionID, PipelineReservationID: cfg.PipelineReservationID,
		ProfileRevision: pin.ProfileRevision, RequirementsDigest: pin.RequirementsDigest,
		ContentGeneration: manifest.GenerationID, ManifestSHA256: profileartifacts.SHA256(data), CanonicalConceptsDigest: manifestConceptDigest(manifest), ManifestGeneration: objectGeneration, CreatedAt: time.Now().UTC(),
	}
	consumedKind := "none"
	if pin.BootstrapRef != nil {
		consumedKind = "bootstrap"
		revision := pin.BootstrapRef.Revision
		receipt.ConsumedGuidanceRevision = &revision
	} else if pin.Revision != "" {
		consumedKind = "active"
		revision := pin.Revision
		receipt.ConsumedGuidanceRevision = &revision
	}
	receipt.ConsumedGuidanceKind = consumedKind
	if pin.BootstrapRef != nil {
		ref := *pin.BootstrapRef
		receipt.ConsumedBootstrapGuidance = &ref
	} else if pin.Revision != "" {
		receipt.ConsumedActiveGuidance = &profileartifacts.DerivedRef{
			Revision: pin.Revision, InputDigest: pin.SourceInputDigest,
			ModelVersion: pin.SourceModelVersion, PromptVersion: pin.SourcePromptVersion,
			SchemaVersion: pin.SourceSchemaVersion,
		}
	}
	receipt.ConsumedGuidanceStatus = pin.BootstrapStatus
	receipt.ConsumedGuidanceSourceRevision = pin.SourceProfileRevision
	receipt.ConsumedGuidanceSourceContentGeneration = pin.SourceContentGeneration
	receipt.ConsumedGuidanceSourceCandidateID = pin.SourceCandidateID
	receipt.ConsumedGuidanceInputDigest = pin.SourceInputDigest
	receipt.ConsumedGuidanceModelVersion = pin.SourceModelVersion
	receipt.ConsumedGuidancePromptVersion = pin.SourcePromptVersion
	receipt.ConsumedGuidanceSchemaVersion = pin.SourceSchemaVersion
	return persistProfileCompileReceipt(ctx, receipt)
}

func writeProfileCompileReceipt(ctx context.Context, receipt profileruntime.CompileReceipt) error {
	project := firstNonEmptyEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	if project == "" {
		return errors.New("Google Cloud project identity is unavailable for Profile receipt")
	}
	client, err := newProfileFirestoreClient(ctx, project)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := generation.ArchivedManifestPath(receipt.ContentGeneration); err != nil {
		return err
	}
	if !isLowerSHA256(receipt.ManifestSHA256) || !isLowerSHA256(receipt.CanonicalConceptsDigest) {
		return errors.New("invalid Profile compile manifest evidence")
	}
	ref := scopedfirestore.Collection(client, "projects").Doc(receipt.UserID + "_" + receipt.ProjectID).
		Collection("profile").Doc("state").Collection(profileruntime.CompileReceiptsCollection).Doc(receipt.ContentGeneration)
	return client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		existing, err := tx.Get(ref)
		if err == nil {
			var prior profileruntime.CompileReceipt
			if err := existing.DataTo(&prior); err != nil {
				return err
			}
			prior.CreatedAt = receipt.CreatedAt
			if !reflect.DeepEqual(prior, receipt) {
				return errors.New("Profile compile receipt conflict")
			}
			return nil
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
		if err := tx.Create(ref, receipt); err != nil {
			return err
		}
		return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: receipt.UserID, ProjectID: receipt.ProjectID, Kind: "compile", Revision: receipt.ProfileRevision, ID: receipt.ContentGeneration, RequestID: observability.RequestIDFromContext(ctx), Due: receipt.CreatedAt})
	})
}

func manifestConceptDigest(manifest generation.Manifest) string {
	file, _ := manifest.File("cache/concepts.jsonl")
	return file.SHA256
}
