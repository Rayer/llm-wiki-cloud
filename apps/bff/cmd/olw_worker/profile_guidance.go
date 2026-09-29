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
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
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
	ProfileRevision      int64
	RequirementsDigest   string
	Revision             string
	SchemaVersion        string
	Content              string
	BootstrapRef         *profileartifacts.BootstrapGuidanceRef
	BootstrapStatus      string
	BootstrapConfirmedAt string
}

type profileGuidanceRef struct {
	Revision      string `firestore:"revision"`
	InputDigest   string `firestore:"input_digest"`
	ModelVersion  string `firestore:"model_version"`
	PromptVersion string `firestore:"prompt_version"`
	SchemaVersion string `firestore:"schema_version"`
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
	projectID := cfg.UserID + "_" + cfg.ProjectID
	stateRef := client.Collection("projects").Doc(projectID).Collection("profile").Doc("state")
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
	objectName := fmt.Sprintf("users/%s/projects/%s/%s", cfg.UserID, cfg.ProjectID, relativePath)
	data, _, err := objects.Read(ctx, objectName, 0, profileGuidanceReadLimit)
	if err != nil {
		return nil, fmt.Errorf("read active Profile guidance artifact: %w", err)
	}
	envelope, err := validateProfileGuidanceArtifact(data, ref)
	if err != nil {
		return nil, err
	}
	return &profileGuidancePin{ProfileRevision: state.Revision, RequirementsDigest: profileRequirementsDigest(state.Requirements), Revision: ref.Revision, SchemaVersion: envelope.SchemaVersion, Content: envelope.CompileGuidance}, nil
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
	objectName := fmt.Sprintf("users/%s/projects/%s/%s", cfg.UserID, cfg.ProjectID, relativePath)
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
	return firestore.NewClientWithDatabase(ctx, project, database)
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
		UserID: cfg.UserID, ProjectID: cfg.ProjectID, ExecutionID: cfg.ExecutionID,
		ProfileRevision: pin.ProfileRevision, RequirementsDigest: pin.RequirementsDigest,
		ContentGeneration: manifest.GenerationID, ManifestSHA256: profileartifacts.SHA256(data), CanonicalConceptsDigest: manifestConceptDigest(manifest), ManifestGeneration: objectGeneration, CreatedAt: time.Now().UTC(),
	}
	if pin.BootstrapRef != nil {
		ref := *pin.BootstrapRef
		receipt.ConsumedBootstrapGuidance = &ref
	}
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
	ref := client.Collection("projects").Doc(receipt.UserID + "_" + receipt.ProjectID).
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
		return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: receipt.UserID, ProjectID: receipt.ProjectID, Kind: "compile", Revision: receipt.ProfileRevision, ID: receipt.ContentGeneration, Due: receipt.CreatedAt})
	})
}

func manifestConceptDigest(manifest generation.Manifest) string {
	file, _ := manifest.File("cache/concepts.jsonl")
	return file.SHA256
}
