package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"google.golang.org/api/option"
)

func TestMain(m *testing.M) {
	resolveProfileGuidanceAtCompileStart = func(context.Context, workerConfig, objectStore) (*profileGuidancePin, error) {
		return nil, nil
	}
	os.Exit(m.Run())
}

func TestValidateProfileGuidanceArtifactStrictly(t *testing.T) {
	envelope := profileGuidanceEnvelope{
		SchemaVersion:           profileGuidanceSchemaVersion,
		InputDigest:             strings.Repeat("1", 64),
		SourceContentGeneration: "g_source",
		CanonicalConceptsDigest: strings.Repeat("2", 64),
		ModelVersion:            "synthetic-model-v1",
		PromptVersion:           "synthetic-prompt-v1",
		CompileGuidance:         "Preserve the tested wikilink structure.",
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	ref := profileGuidanceRef{
		Revision:      hashBytes(data),
		InputDigest:   envelope.InputDigest,
		ModelVersion:  envelope.ModelVersion,
		PromptVersion: envelope.PromptVersion,
		SchemaVersion: envelope.SchemaVersion,
	}
	got, err := validateProfileGuidanceArtifact(data, ref)
	if err != nil || got != envelope {
		t.Fatalf("valid guidance = %+v, err=%v", got, err)
	}

	mutated := append(append([]byte(nil), data...), ' ')
	badRef := ref
	badRef.Revision = hashBytes(mutated)
	if _, err := validateProfileGuidanceArtifact(mutated, badRef); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("noncanonical artifact err=%v, want rejection", err)
	}
	if _, err := validateProfileGuidanceArtifact(data, profileGuidanceRef{Revision: strings.Repeat("a", 64), InputDigest: ref.InputDigest, ModelVersion: ref.ModelVersion, PromptVersion: ref.PromptVersion, SchemaVersion: ref.SchemaVersion}); err == nil || !strings.Contains(err.Error(), "reference mismatch") {
		t.Fatalf("wrong hash err=%v, want shared artifact reference mismatch", err)
	}

	unknown := strings.TrimSuffix(string(data), "}") + `,"unexpected":"field"}`
	unknownRef := ref
	unknownRef.Revision = hashBytes([]byte(unknown))
	if _, err := validateProfileGuidanceArtifact([]byte(unknown), unknownRef); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err=%v, want strict schema rejection", err)
	}

	duplicate := strings.TrimSuffix(string(data), "}") + `,"model_version":"second"}`
	duplicateRef := ref
	duplicateRef.Revision = hashBytes([]byte(duplicate))
	if _, err := validateProfileGuidanceArtifact([]byte(duplicate), duplicateRef); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate key err=%v, want duplicate key rejection", err)
	}

	tooLarge := make([]byte, profileGuidanceReadLimit+1)
	if _, err := validateProfileGuidanceArtifact(tooLarge, ref); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized artifact err=%v, want 1 MiB rejection", err)
	}
}

func TestProfileGuidanceRequiresCloudProjectAndHonorsNamedFirestoreDatabase(t *testing.T) {
	for _, name := range []string{"GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT"} {
		t.Setenv(name, "")
	}
	if _, err := pinActiveProfileGuidance(context.Background(), workerConfig{UserID: "user", ProjectID: "project"}, newMemoryObjects()); err == nil || !strings.Contains(err.Error(), "Google Cloud project identity is unavailable") {
		t.Fatalf("missing Cloud project error = %v", err)
	}

	t.Setenv("FIRESTORE_DATABASE_ID", "llm-wiki-cloud-dev")
	if got := profileFirestoreDatabaseID(); got != "llm-wiki-cloud-dev" {
		t.Fatalf("Profile guidance Firestore database = %q, want named DEV database", got)
	}
	t.Setenv("FIRESTORE_DATABASE_ID", "")
	if got := profileFirestoreDatabaseID(); got != "(default)" {
		t.Fatalf("unset Profile guidance Firestore database = %q, want default", got)
	}
}

func TestProfileGuidanceSchemaMaterializationIsAdditiveAndBounded(t *testing.T) {
	vault := t.TempDir()
	if data, err := profileGuidanceSchema(vault, nil); err != nil || data != nil {
		t.Fatalf("no-active materialization = %q, err=%v", data, err)
	}
	if data, err := profileGuidanceSchema(vault, &profileGuidancePin{Revision: strings.Repeat("a", 64)}); err != nil || data != nil {
		t.Fatalf("empty neutral guidance materialization = %q, err=%v", data, err)
	}

	original := []byte("# Existing Schema\n\n## Links\nUse [[Existing Note]].\n")
	if err := os.WriteFile(filepath.Join(vault, "vault-schema.md"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	pin := &profileGuidancePin{Revision: strings.Repeat("b", 64), Content: "Write short explanations with clear causes."}
	got, err := profileGuidanceSchema(vault, pin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), string(original)) || !strings.Contains(string(got), "## Project Profile Compile Guidance\n"+pin.Content) || !strings.Contains(string(got), "Use [[Existing Note]].") {
		t.Fatalf("materialized schema lost base conventions or guidance: %s", got)
	}
	if chars := len([]rune(string(got))); chars > syntoVaultSchemaCharLimit {
		t.Fatalf("materialized schema has %d characters, over the %d-character budget", chars, syntoVaultSchemaCharLimit)
	}

	tooLong := &profileGuidancePin{Revision: strings.Repeat("c", 64), Content: strings.Repeat("界", syntoVaultSchemaCharLimit)}
	if _, err := profileGuidanceSchema(vault, tooLong); err == nil || !strings.Contains(err.Error(), "1500-character") {
		t.Fatalf("oversized guidance err=%v, want visible 1500-character rejection", err)
	}
	if _, err := profileGuidanceSchema(vault, &profileGuidancePin{Revision: "../bad", Content: "x"}); err == nil {
		t.Fatal("invalid revision was accepted")
	}

	configHome := t.TempDir()
	path, err := profileGuidanceSchemaPath(vault, pin, configHome)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != configHome {
		t.Fatalf("profile schema path=%q, want ephemeral config directory %q", path, configHome)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(got) {
		t.Fatalf("ephemeral schema = %q, err=%v; want materialized schema", data, err)
	}
}

func TestRunWorkerBatchAtVaultPassesPinnedGuidanceOnlyThroughSyntoSchemaPath(t *testing.T) {
	vault := t.TempDir()
	config := []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\n")
	if err := os.WriteFile(filepath.Join(vault, "synto.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	baseSchema := []byte("# Existing project schema\n\n## Links\nUse [[Existing]].\n")
	if err := os.WriteFile(filepath.Join(vault, "vault-schema.md"), baseSchema, 0o600); err != nil {
		t.Fatal(err)
	}
	pin := &profileGuidancePin{Revision: strings.Repeat("a", 64), Content: "Keep cause and effect explicit."}
	var schemaPath string
	previous := execOLW
	t.Cleanup(func() { execOLW = previous })
	execOLW = func(_ context.Context, _ string, _ []string, env []string, _, _ io.Writer) error {
		for _, entry := range allowlistedSyntoEnvironment(env) {
			key, value, ok := strings.Cut(entry, "=")
			if ok && key == "LWC_PROFILE_SCHEMA_PATH" {
				schemaPath = value
			}
		}
		if schemaPath == "" {
			return errors.New("pinned Profile schema path was not passed to Synto")
		}
		data, err := os.ReadFile(schemaPath)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), string(baseSchema)) || !strings.Contains(string(data), pin.Content) {
			return errors.New("pinned Profile schema did not preserve base schema and add guidance")
		}
		return nil
	}
	err := runWorkerBatchAtVault(context.Background(), workerConfig{
		APIKey: "synthetic", Postprocess: false, StopOnError: true, SuppressOutput: true,
		pinnedProfileGuidance: pin,
	}, [][]string{{"run"}}, vault)
	if err != nil {
		t.Fatalf("runWorkerBatchAtVault() error = %v", err)
	}
	if schemaPath == "" {
		t.Fatal("worker did not pass the pinned schema path")
	}
	if _, err := os.Stat(schemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary Profile schema remained after compile: stat err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "vault-schema.md")); err != nil || string(got) != string(baseSchema) {
		t.Fatalf("worker mutated project vault-schema.md: data=%q err=%v", got, err)
	}
}

func TestRunWorkerBatchAtVaultLeavesNoProfileSchemaOverrideWhenNoPin(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "synto.toml"), []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\nrelation_extraction = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := execOLW
	t.Cleanup(func() { execOLW = previous })
	execOLW = func(_ context.Context, _ string, _ []string, env []string, _, _ io.Writer) error {
		for _, entry := range allowlistedSyntoEnvironment(env) {
			if strings.HasPrefix(entry, "LWC_PROFILE_SCHEMA_PATH=") {
				return errors.New("no-active compile unexpectedly received Profile guidance")
			}
		}
		return nil
	}
	if err := runWorkerBatchAtVault(context.Background(), workerConfig{Postprocess: false, StopOnError: true, SuppressOutput: true}, [][]string{{"run"}}, vault); err != nil {
		t.Fatalf("no-active runWorkerBatchAtVault() error = %v", err)
	}
}

func TestCloudProfileGuidanceFailureReleasesLease(t *testing.T) {
	previous := resolveProfileGuidanceAtCompileStart
	t.Cleanup(func() { resolveProfileGuidanceAtCompileStart = previous })
	guidanceReadFailure := errors.New("synthetic active guidance read failure")
	resolveProfileGuidanceAtCompileStart = func(context.Context, workerConfig, objectStore) (*profileGuidancePin, error) {
		return nil, guidanceReadFailure
	}
	m := newMemoryObjects()
	err := runCloudWorkerBatch(context.Background(), cloudCfgFor("user", "project", "profile-failure"), [][]string{{"run"}}, m)
	var failure *workerFailure
	if err == nil || !errors.Is(err, errCloudMaterialization) || !errors.Is(err, guidanceReadFailure) ||
		!errors.As(err, &failure) || failure.Stage != failureStageSyntoConfigValidation {
		t.Fatalf("runCloudWorkerBatch() error = %v, want Profile guidance failure", err)
	}
	leaseName := "users/user/projects/project/" + generation.LeasePath
	m.mu.Lock()
	_, leaseStillPresent := m.objects[leaseName]
	m.mu.Unlock()
	if leaseStillPresent {
		t.Fatalf("worker guidance failure leaked cloud lease %q", leaseName)
	}
}

func TestCloudProfileGuidanceRemainsPinnedAfterProfileEdit(t *testing.T) {
	previousResolver := resolveProfileGuidanceAtCompileStart
	previousExec := execOLW
	t.Cleanup(func() {
		resolveProfileGuidanceAtCompileStart = previousResolver
		execOLW = previousExec
	})
	m := newMemoryObjects()
	prefix := "users/user/projects/project/"
	seedCloudSource(t, m, prefix, "raw-start", "", priorCloudReceipt())
	cfg := cloudCfgFor("user", "project", "profile-pinned")
	cfg.SuggestedQueries = false
	activeContent := "Use the currently confirmed causal writing guidance."
	resolverCalls := 0
	resolveProfileGuidanceAtCompileStart = func(context.Context, workerConfig, objectStore) (*profileGuidancePin, error) {
		resolverCalls++
		return &profileGuidancePin{Revision: strings.Repeat("a", 64), Content: activeContent}, nil
	}
	execOLW = func(_ context.Context, vault string, _ []string, env []string, _, _ io.Writer) error {
		activeContent = "New Profile guidance saved after this compile started."
		var schemaPath string
		for _, entry := range allowlistedSyntoEnvironment(env) {
			if key, value, ok := strings.Cut(entry, "="); ok && key == "LWC_PROFILE_SCHEMA_PATH" {
				schemaPath = value
			}
		}
		data, err := os.ReadFile(schemaPath)
		if err != nil {
			return fmt.Errorf("read pinned schema: %w", err)
		}
		if !strings.Contains(string(data), "Use the currently confirmed causal writing guidance.") || strings.Contains(string(data), "New Profile guidance saved after this compile started.") {
			return errors.New("compile did not retain the guidance pinned at start")
		}
		writeCloudRequiredOutputs(t, vault)
		return nil
	}
	if err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, m); err != nil {
		t.Fatalf("runCloudWorkerBatch() error = %v", err)
	}
	if resolverCalls != 1 {
		t.Fatalf("Profile guidance resolver calls = %d, want one at compile start", resolverCalls)
	}
}

type profileGuidanceObjectStore struct {
	objects map[string][]byte
	reads   []profileGuidanceObjectRead
}

type profileGuidanceObjectRead struct {
	name       string
	generation int64
	limit      int64
}

func (s *profileGuidanceObjectStore) Read(_ context.Context, name string, generation, limit int64) ([]byte, objectAttrs, error) {
	s.reads = append(s.reads, profileGuidanceObjectRead{name: name, generation: generation, limit: limit})
	data, ok := s.objects[name]
	if !ok {
		return nil, objectAttrs{}, errObjectNotFound
	}
	if int64(len(data)) > limit {
		return nil, objectAttrs{}, errors.New("object exceeds input limit")
	}
	return append([]byte(nil), data...), objectAttrs{Name: name, Size: int64(len(data))}, nil
}

func (*profileGuidanceObjectStore) List(context.Context, string, int) ([]objectAttrs, error) {
	return nil, nil
}
func (*profileGuidanceObjectStore) Write(context.Context, string, []byte, map[string]string, objectConditions) (objectAttrs, error) {
	return objectAttrs{}, errors.New("unexpected write")
}
func (*profileGuidanceObjectStore) Delete(context.Context, string, int64) error {
	return errors.New("unexpected delete")
}
func (*profileGuidanceObjectStore) Close() error { return nil }

func TestReadActiveProfileGuidancePinsImmutableReference(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	project := fmt.Sprintf("lwc353-guidance-%d", time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, project, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	cfg := workerConfig{UserID: "owner", ProjectID: fmt.Sprintf("p%d", time.Now().UnixNano())}
	objects := &profileGuidanceObjectStore{objects: map[string][]byte{}}
	if pin, err := readActiveProfileGuidance(ctx, client, cfg, objects); err != nil || pin != nil {
		t.Fatalf("missing Profile state pin=%+v err=%v, want unchanged no-active behavior", pin, err)
	}
	stateRef := client.Collection("projects").Doc(cfg.UserID + "_" + cfg.ProjectID).Collection("profile").Doc("state")
	if _, err := stateRef.Set(ctx, map[string]interface{}{"project_id": cfg.ProjectID, "requirements": []interface{}{map[string]interface{}{"id": "r1", "text": "first-compile guidance"}}}); err != nil {
		t.Fatalf("seed pending bootstrap Profile state: %v", err)
	}
	if pin, err := readActiveProfileGuidance(ctx, client, cfg, objects); err == nil || pin != nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("nonempty no-active Profile pin=%+v err=%v, want fail-closed bootstrap gate", pin, err)
	}

	requirements := []profileGuidanceRequirement{{ID: "r1", Text: "first-compile guidance"}}
	bootstrapEnvelope := profileartifacts.BootstrapGuidanceEnvelope{
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, ProfileRevision: 7,
		InputDigest: profileRequirementsDigest(requirements), ModelVersion: "synthetic-model-v1",
		PromptVersion: "synthetic-bootstrap-v1", CompileGuidance: "Use the confirmed first-generation style.",
	}
	bootstrapData, bootstrapRef, err := profileartifacts.EncodeBootstrapGuidance(bootstrapEnvelope)
	if err != nil {
		t.Fatalf("encode bootstrap fixture: %v", err)
	}
	confirmedAt := "2026-09-25T02:00:00Z"
	bootstrapState := map[string]interface{}{
		"project_id": cfg.ProjectID, "revision": int64(7),
		"requirements": []interface{}{map[string]interface{}{"id": "r1", "text": "first-compile guidance"}},
		"bootstrap_guidance": map[string]interface{}{
			"revision": bootstrapRef.Revision, "input_digest": bootstrapRef.InputDigest,
			"profile_revision": bootstrapRef.ProfileRevision, "status": "preview_ready",
			"model_version": bootstrapRef.ModelVersion, "prompt_version": bootstrapRef.PromptVersion,
			"schema_version": bootstrapRef.SchemaVersion, "confirmed_at": nil,
		},
	}
	if _, err := stateRef.Set(ctx, bootstrapState); err != nil {
		t.Fatalf("seed preview-ready bootstrap Profile state: %v", err)
	}
	if pendingPin, err := readActiveProfileGuidance(ctx, client, cfg, objects); err == nil || pendingPin != nil || !strings.Contains(err.Error(), "pending confirmation") {
		t.Fatalf("pending bootstrap pin=%+v err=%v, want confirmation gate", pendingPin, err)
	}
	bootstrapState["bootstrap_guidance"].(map[string]interface{})["status"] = "confirmed"
	bootstrapState["bootstrap_guidance"].(map[string]interface{})["confirmed_at"] = confirmedAt
	if _, err := stateRef.Set(ctx, bootstrapState); err != nil {
		t.Fatalf("seed confirmed bootstrap Profile state: %v", err)
	}
	bootstrapObjectName := fmt.Sprintf("users/%s/projects/%s/%s", cfg.UserID, cfg.ProjectID, profileartifacts.BootstrapObjectPath(bootstrapRef.Revision))
	objects.objects[bootstrapObjectName] = bootstrapData
	bootstrapPin, err := readActiveProfileGuidance(ctx, client, cfg, objects)
	if err != nil {
		t.Fatalf("read confirmed bootstrap guidance: %v", err)
	}
	if bootstrapPin == nil || bootstrapPin.Revision != bootstrapRef.Revision || bootstrapPin.SchemaVersion != profileartifacts.BootstrapGuidanceSchema ||
		bootstrapPin.Content != bootstrapEnvelope.CompileGuidance || bootstrapPin.BootstrapRef == nil || *bootstrapPin.BootstrapRef != bootstrapRef ||
		bootstrapPin.BootstrapStatus != "confirmed" || bootstrapPin.BootstrapConfirmedAt != confirmedAt {
		t.Fatalf("bootstrap pin=%+v, want exact confirmed immutable guidance", bootstrapPin)
	}
	if len(objects.reads) != 1 || objects.reads[0] != (profileGuidanceObjectRead{name: bootstrapObjectName, generation: 0, limit: profileGuidanceReadLimit}) {
		t.Fatalf("bootstrap object reads=%+v, want exact immutable path and 1 MiB bound", objects.reads)
	}
	bootstrapReads := len(objects.reads)
	bootstrapState["revision"] = int64(8)
	if _, err := stateRef.Set(ctx, bootstrapState); err != nil {
		t.Fatalf("seed stale confirmed bootstrap Profile state: %v", err)
	}
	if stalePin, err := readActiveProfileGuidance(ctx, client, cfg, objects); err == nil || stalePin != nil || !strings.Contains(err.Error(), "stale or incomplete") {
		t.Fatalf("stale bootstrap pin=%+v err=%v, want revision mismatch rejection", stalePin, err)
	}
	if len(objects.reads) != bootstrapReads {
		t.Fatalf("stale bootstrap guidance read artifact before ref checks: reads=%+v", objects.reads)
	}

	objects.reads = nil
	envelope := profileGuidanceEnvelope{
		SchemaVersion:           profileGuidanceSchemaVersion,
		InputDigest:             strings.Repeat("1", 64),
		SourceContentGeneration: "g_initial",
		CanonicalConceptsDigest: strings.Repeat("2", 64),
		ModelVersion:            "synthetic-model-v1",
		PromptVersion:           "synthetic-prompt-v1",
		CompileGuidance:         "Use concise causal transitions. Keep [[links]] intact.",
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	revision := hashBytes(data)
	candidateID := "candidate-1"
	projectRef := client.Collection("projects").Doc(cfg.UserID + "_" + cfg.ProjectID)
	stateRef = projectRef.Collection("profile").Doc("state")
	active := map[string]interface{}{
		"candidate_id": candidateID, "content_generation": "g_initial",
		"dictionary_revision": strings.Repeat("d", 64), "tag_set_revision": strings.Repeat("e", 64),
		"query_rule_revision": strings.Repeat("f", 64), "guidance_revision": revision,
	}
	if _, err := stateRef.Set(ctx, map[string]interface{}{"project_id": cfg.ProjectID, "requirements": []interface{}{}, "active": active}); err != nil {
		t.Fatalf("seed Profile state: %v", err)
	}
	guidanceRef := map[string]interface{}{
		"revision": revision, "input_digest": envelope.InputDigest, "model_version": envelope.ModelVersion,
		"prompt_version": envelope.PromptVersion, "schema_version": envelope.SchemaVersion,
	}
	candidate := map[string]interface{}{
		"candidate_id": candidateID, "source": "manual", "content_generation": "g_initial",
		"requirements_digest": envelope.InputDigest, "guidance": guidanceRef,
	}
	if _, err := stateRef.Collection("candidates").Doc(candidateID).Set(ctx, candidate); err != nil {
		t.Fatalf("seed active Profile candidate: %v", err)
	}
	name := fmt.Sprintf("users/%s/projects/%s/%s%s.json", cfg.UserID, cfg.ProjectID, profileGuidanceObjectPrefix, revision)
	objects.objects[name] = data

	pin, err := readActiveProfileGuidance(ctx, client, cfg, objects)
	if err != nil {
		t.Fatalf("read active guidance: %v", err)
	}
	if pin == nil || pin.Revision != revision || pin.Content != envelope.CompileGuidance || pin.BootstrapRef != nil {
		t.Fatalf("pin=%+v, want revision %s and exact guidance content", pin, revision)
	}
	if len(objects.reads) != 1 || objects.reads[0] != (profileGuidanceObjectRead{name: name, generation: 0, limit: profileGuidanceReadLimit}) {
		t.Fatalf("object reads=%+v, want exact immutable path and 1 MiB bound", objects.reads)
	}

	mutated := strings.Replace(string(data), "Use concise causal transitions.", "Use concise causal transitions!", 1)
	if mutated == string(data) {
		t.Fatal("guidance fixture did not contain the expected mutation target")
	}
	objects.objects[name] = []byte(mutated)
	if _, err := readActiveProfileGuidance(ctx, client, cfg, objects); err == nil || !strings.Contains(err.Error(), "reference mismatch") {
		t.Fatalf("mutated guidance object err=%v, want fail-closed reference mismatch", err)
	}
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
