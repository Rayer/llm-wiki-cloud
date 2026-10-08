package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"gopkg.in/yaml.v3"
)

const (
	maxConfigBytes    = 1 << 20
	maxBFFConfigBytes = 64 << 10
)

var (
	allowedEnvironments                 = map[string]struct{}{"development": {}, "production": {}}
	allowedComponents                   = []string{"auth", "bff", "worker", "exportjob", "frontend"}
	componentSet                        = map[string]struct{}{"auth": {}, "bff": {}, "worker": {}, "exportjob": {}, "frontend": {}}
	secretRefPattern                    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	secretValuePattern                  = regexp.MustCompile(`(?i)(?:github_pat_|ghp_|xox[baprs]-|-----begin|sk-[A-Za-z0-9])`)
	secretVersionPattern                = regexp.MustCompile(`^[1-9][0-9]*$`)
	profileRuntimeServiceAccountPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9.-]+\.iam\.gserviceaccount\.com$`)
	pipelineDemoUserIDPattern           = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	demoEmailPattern                    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	demoRolePattern                     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

type EnvironmentConfig struct {
	GCP       GCPConfig       `yaml:"gcp"`
	Auth      AuthConfig      `yaml:"auth"`
	BFF       BFFConfig       `yaml:"bff"`
	Worker    WorkerConfig    `yaml:"worker"`
	ExportJob ExportJobConfig `yaml:"export_job"`
	Frontend  FrontendConfig  `yaml:"frontend"`
}

type GCPConfig struct {
	ProjectID        string `yaml:"project_id" json:"project_id"`
	Region           string `yaml:"region" json:"region"`
	ArtifactRegistry string `yaml:"artifact_registry" json:"artifact_registry"`
}

type AuthConfig struct {
	Google                *GoogleConfig        `yaml:"google" json:"google,omitempty"`
	ServiceName           string               `yaml:"service_name" json:"service_name"`
	RuntimeServiceAccount string               `yaml:"runtime_service_account" json:"runtime_service_account"`
	Network               string               `yaml:"network" json:"network"`
	Subnet                string               `yaml:"subnet" json:"subnet"`
	VPCEgress             string               `yaml:"vpc_egress" json:"vpc_egress"`
	Ingress               string               `yaml:"ingress" json:"ingress"`
	MaxInstances          int                  `yaml:"max_instances" json:"max_instances"`
	FirestoreDatabaseID   string               `yaml:"firestore_database_id" json:"firestore_database_id"`
	DemoUserID            string               `yaml:"demo_user_id" json:"demo_user_id"`
	DemoUserEmail         string               `yaml:"demo_user_email" json:"demo_user_email"`
	DemoUserRole          string               `yaml:"demo_user_role" json:"demo_user_role"`
	PublicDomain          string               `yaml:"public_domain" json:"public_domain"`
	AllowedHosts          []string             `yaml:"allowed_hosts" json:"allowed_hosts"`
	AllowedOrigins        []string             `yaml:"allowed_origins" json:"allowed_origins"`
	SecretReferences      AuthSecretReferences `yaml:"secret_references" json:"secret_references"`
}

type AuthSecretReferences struct {
	JWT string `yaml:"jwt" json:"jwt"`
}

type BFFConfig struct {
	ServiceName                  string                  `yaml:"service_name" json:"service_name"`
	RuntimeServiceAccount        string                  `yaml:"runtime_service_account" json:"runtime_service_account"`
	Network                      string                  `yaml:"network" json:"network"`
	Subnet                       string                  `yaml:"subnet" json:"subnet"`
	VPCEgress                    string                  `yaml:"vpc_egress" json:"vpc_egress"`
	Ingress                      string                  `yaml:"ingress" json:"ingress"`
	MaxInstances                 int                     `yaml:"max_instances" json:"max_instances"`
	PipelineJobName              string                  `yaml:"pipeline_job_name" json:"pipeline_job_name"`
	PipelineJobLocation          string                  `yaml:"pipeline_job_location" json:"pipeline_job_location"`
	Bucket                       string                  `yaml:"-" json:"bucket,omitempty"`
	FirestoreDatabaseID          string                  `yaml:"-" json:"firestore_database_id,omitempty"`
	PipelineJobURL               string                  `yaml:"-" json:"pipeline_job_url,omitempty"`
	AuthServiceURL               string                  `yaml:"-" json:"auth_service_url,omitempty"`
	AllowedOrigins               []string                `yaml:"-" json:"allowed_origins,omitempty"`
	DevJWT                       *bool                   `yaml:"-" json:"dev_jwt,omitempty"`
	QueryConfig                  string                  `yaml:"-" json:"query_config,omitempty"`
	PipelineDailyLimit           int                     `yaml:"-" json:"pipeline_daily_limit,omitempty"`
	PipelineCooldownSeconds      int                     `yaml:"-" json:"pipeline_cooldown_seconds,omitempty"`
	PipelineMinNewRaw            int                     `yaml:"-" json:"pipeline_min_new_raw,omitempty"`
	PipelineDemoUserIDs          []string                `yaml:"-" json:"pipeline_demo_user_ids,omitempty"`
	ProfileRuntimeAudience       string                  `yaml:"-" json:"profile_runtime_audience,omitempty"`
	ProfileRuntimeServiceAccount string                  `yaml:"-" json:"profile_runtime_service_account,omitempty"`
	SecretReferences             RuntimeSecretReferences `yaml:"-" json:"secret_references,omitempty"`
	ConfigSecretResource         string                  `yaml:"-" json:"config_secret_resource,omitempty"`
	RuntimeInputs                *BFFSourceProjection    `yaml:"-" json:"runtime_inputs,omitempty"`
}

type BFFSecretReference struct {
	Source   string `json:"source"`
	EnvName  string `json:"env_name"`
	Resource string `json:"resource"`
}

type BFFLLM struct {
	Provider              string `json:"provider"`
	BaseURL               string `json:"base_url"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	Model                 string `json:"model"`
}

type BFFQueryLegacy struct {
	QueryExpansionModel                          string `json:"query_expansion_model"`
	QueryExpansionReasoning                      string `json:"query_expansion_reasoning"`
	AnswerSynthesisModel                         string `json:"answer_synthesis_model"`
	AnswerSynthesisReasoning                     string `json:"answer_synthesis_reasoning"`
	QuerySelectionLimit                          int    `json:"query_selection_limit"`
	QuerySelectionExplorationSlots               int    `json:"query_selection_exploration_slots"`
	QuerySelectionEvidenceThreshold              int    `json:"query_selection_evidence_threshold"`
	QueryExpansionKeywordsPerAttempt             int    `json:"query_expansion_keywords_per_attempt"`
	QueryExpansionAttempts                       int    `json:"query_expansion_attempts"`
	QueryMatchingRareKeywordMaxDocumentFrequency int    `json:"query_matching_rare_keyword_max_document_frequency"`
}

type BFFQuery struct {
	StageConfigPath string          `json:"stage_config_path"`
	Legacy          *BFFQueryLegacy `json:"legacy"`
}

type BFFLocal struct {
	Scope                string `json:"scope"`
	WorkerPath           string `json:"worker_path"`
	PipelineConfigPath   string `json:"pipeline_config_path"`
	PipelineBindingsPath string `json:"pipeline_bindings_path"`
}

type BFFSourceProjection struct {
	SchemaVersion                int                 `json:"schema_version"`
	Environment                  string              `json:"environment"`
	Target                       string              `json:"target"`
	GCPProject                   string              `json:"gcp_project"`
	Bucket                       string              `json:"bucket"`
	FirestoreDatabaseID          string              `json:"firestore_database_id"`
	AuthServiceURL               string              `json:"auth_service_url"`
	PipelineJobURL               string              `json:"pipeline_job_url"`
	ExportJobURL                 string              `json:"export_job_url"`
	ExportSigningServiceAccount  string              `json:"export_signing_service_account"`
	AllowedOrigins               []string            `json:"allowed_origins"`
	AllowedHosts                 []string            `json:"allowed_hosts"`
	PipelineDailyLimit           int                 `json:"pipeline_daily_limit"`
	PipelineCooldownSeconds      int                 `json:"pipeline_cooldown_seconds"`
	PipelineMinNewRaw            int                 `json:"pipeline_min_new_raw"`
	PipelineDemoUserIDs          []string            `json:"pipeline_demo_user_ids"`
	AuthSessionEnvironment       string              `json:"auth_session_environment"`
	AuthSessionMigration         string              `json:"auth_session_migration"`
	RegistrationEnabled          *bool               `json:"registration_enabled"`
	JWTSecretReference           BFFSecretReference  `json:"jwt_secret_reference"`
	DeepSeekAPIKeyReference      BFFSecretReference  `json:"deepseek_api_key_reference"`
	TypeSafeAPIKeyReference      *BFFSecretReference `json:"typesafe_api_key_reference"`
	ProfileRuntimeAudience       string              `json:"profile_runtime_audience"`
	ProfileRuntimeServiceAccount string              `json:"profile_runtime_service_account"`
	LLM                          BFFLLM              `json:"llm"`
	Query                        BFFQuery            `json:"query"`
	Local                        *BFFLocal           `json:"local"`
	PortDefault                  int                 `json:"port_default"`
	ConfigSecretResource         string              `json:"config_secret_resource"`
}

type WorkerConfig struct {
	JobName               string                 `yaml:"job_name" json:"job_name"`
	RuntimeServiceAccount string                 `yaml:"runtime_service_account" json:"runtime_service_account"`
	Bucket                string                 `yaml:"bucket" json:"bucket"`
	Location              string                 `yaml:"location" json:"location"`
	Args                  []string               `yaml:"args" json:"args"`
	SecretReferences      WorkerSecretReferences `yaml:"secret_references" json:"secret_references"`
}

type RuntimeSecretReferences struct {
	JWT               string                    `yaml:"jwt" json:"jwt"`
	DeepSeekAPIKey    string                    `yaml:"deepseek_api_key" json:"deepseek_api_key"`
	TypeSafeJevAPIKey *VersionedSecretReference `yaml:"typesafe_jev_api_key,omitempty" json:"typesafe_jev_api_key,omitempty"`
}

type VersionedSecretReference struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

type WorkerSecretReferences struct {
	DeepSeekAPIKey string `yaml:"deepseek_api_key" json:"deepseek_api_key"`
}

// ExportJobConfig enables an environment only after its runtime resources are
// provisioned and read back by the operator.
type ExportJobConfig struct {
	Enabled               bool   `yaml:"enabled" json:"enabled"`
	JobName               string `yaml:"job_name" json:"job_name,omitempty"`
	RuntimeServiceAccount string `yaml:"runtime_service_account" json:"runtime_service_account,omitempty"`
	Bucket                string `yaml:"bucket" json:"bucket,omitempty"`
	FirestoreDatabaseID   string `yaml:"firestore_database_id" json:"firestore_database_id,omitempty"`
	Location              string `yaml:"location" json:"location,omitempty"`
	SigningServiceAccount string `yaml:"signing_service_account" json:"signing_service_account,omitempty"`
	JobTimeout            string `yaml:"job_timeout" json:"job_timeout"`
	MaxRetries            int    `yaml:"max_retries" json:"max_retries"`
	Parallelism           int    `yaml:"parallelism" json:"parallelism"`
	Tasks                 int    `yaml:"tasks" json:"tasks"`
}

type FrontendConfig struct {
	ProjectName   string   `yaml:"project_name" json:"project_name"`
	TeamSlug      string   `yaml:"team_slug" json:"team_slug"`
	Repository    string   `yaml:"repository" json:"repository"`
	RootDirectory string   `yaml:"root_directory" json:"root_directory"`
	StableAliases []string `yaml:"stable_aliases" json:"stable_aliases"`
	APIURL        string   `yaml:"api_url" json:"api_url"`
	AuthURL       string   `yaml:"auth_url" json:"auth_url"`
}

type QueryConfigIdentity struct {
	RepositoryPath string `json:"repository_path"`
	RuntimePath    string `json:"runtime_path"`
	SchemaVersion  int    `json:"schema_version"`
	Revision       string `json:"revision"`
	Digest         string `json:"digest"`
}

type Normalized struct {
	Environment string              `json:"environment"`
	ConfigPath  string              `json:"config_path"`
	Selected    []string            `json:"selected_components"`
	GCP         GCPConfig           `json:"gcp"`
	Auth        AuthConfig          `json:"auth"`
	BFF         BFFConfig           `json:"bff"`
	Worker      WorkerConfig        `json:"worker"`
	ExportJob   ExportJobConfig     `json:"export_job"`
	Frontend    FrontendConfig      `json:"frontend"`
	QueryConfig QueryConfigIdentity `json:"query_config"`
	Components  map[string]any      `json:"components"`
	Evidence    Evidence            `json:"evidence"`
}

type Evidence struct {
	Validated         bool   `json:"validated"`
	SecretFree        bool   `json:"secret_free"`
	ConfigFingerprint string `json:"config_fingerprint"`
}

func main() {
	environment := flag.String("environment", "", "fixed environment: development or production")
	configPath := flag.String("config", "", "repository-relative environment YAML path")
	components := flag.String("components", "", "explicit comma-separated component set")
	bffInputsPath := flag.String("bff-inputs", "", "nonsecret BFF inputs from pipeline_config prepare --target bff --descriptor")
	flag.Parse()

	if *environment == "" || *components == "" {
		fail("environment and components are required")
	}
	selected, err := parseComponents(*components)
	if err != nil {
		fail("%v", err)
	}
	var normalized Normalized
	if contains(selected, "bff") {
		normalized, err = LoadWithBFFInputs(*environment, *configPath, *components, *bffInputsPath)
	} else {
		if *bffInputsPath != "" {
			fail("--bff-inputs requires bff in --components")
		}
		normalized, err = Load(*environment, *configPath, *components)
	}
	if err != nil {
		fail("%v", err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		fail("encode normalized config: %v", err)
	}
}

func Load(environment, configPath, components string) (Normalized, error) {
	return load(environment, configPath, components, "", false)
}

func LoadWithBFFInputs(environment, configPath, components, bffInputsPath string) (Normalized, error) {
	return load(environment, configPath, components, bffInputsPath, true)
}

func load(environment, configPath, components, bffInputsPath string, requireBFFInputs bool) (Normalized, error) {
	if _, ok := allowedEnvironments[environment]; !ok {
		return Normalized{}, fmt.Errorf("environment %q is not allowlisted", environment)
	}
	selected, err := parseComponents(components)
	if err != nil {
		return Normalized{}, err
	}
	needsBFFInputs := contains(selected, "bff")
	if needsBFFInputs != requireBFFInputs {
		return Normalized{}, errors.New("generated BFF input descriptor must be supplied exactly when bff is selected")
	}
	if configPath == "" {
		configPath = filepath.Join("deploy", "environments", environment+".yaml")
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return Normalized{}, fmt.Errorf("resolve config path: %w", err)
	}
	absConfig = filepath.Clean(absConfig)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(absConfig)))
	expected := filepath.Join(repoRoot, "deploy", "environments", environment+".yaml")
	if absConfig != expected {
		return Normalized{}, fmt.Errorf("config path must be the fixed %s file", filepath.ToSlash(filepath.Join("deploy", "environments", environment+".yaml")))
	}

	config, err := decodeConfig(absConfig)
	if err != nil {
		return Normalized{}, err
	}
	if contains(selected, "worker") && !contains(selected, "bff") {
		// The Worker deploy adapter still reads this nonsecret compatibility field.
		// Preserve its reviewed database scope without preparing unrelated BFF inputs.
		config.BFF.FirestoreDatabaseID = config.Auth.FirestoreDatabaseID
	}
	if requireBFFInputs {
		if strings.TrimSpace(bffInputsPath) == "" {
			return Normalized{}, errors.New("generated BFF input descriptor is required when bff is selected")
		}
		inputs, err := loadBFFInputDescriptor(bffInputsPath, environment)
		if err != nil {
			return Normalized{}, err
		}
		applyBFFInputDescriptor(&config, inputs)
	} else if strings.TrimSpace(bffInputsPath) != "" {
		return Normalized{}, errors.New("BFF input descriptor requires bff in the selected components")
	}
	if err := validateConfigForSelection(environment, config, requireBFFInputs); err != nil {
		return Normalized{}, err
	}
	for _, component := range selected {
		if component == "exportjob" && !config.ExportJob.Enabled {
			return Normalized{}, errors.New("exportjob is disabled until its environment runtime resources are provisioned and read back")
		}
	}
	for _, component := range selected {
		if component == "exportjob" && !contains(selected, "bff") {
			return Normalized{}, errors.New("exportjob deployment must include bff so its invocation URL is configured from the same reviewed plan")
		}
	}
	var query QueryConfigIdentity
	if config.BFF.RuntimeInputs != nil {
		query, err = loadQueryConfig(repoRoot, config.BFF.QueryConfig)
		if err != nil {
			return Normalized{}, err
		}
	}

	result := Normalized{
		Environment: environment,
		ConfigPath:  filepath.ToSlash(filepath.Join("deploy", "environments", environment+".yaml")),
		Selected:    selected,
		GCP:         config.GCP, Auth: config.Auth, BFF: config.BFF,
		Worker: config.Worker, ExportJob: config.ExportJob, Frontend: config.Frontend, QueryConfig: query,
		Components: componentInputs(config, query, selected),
	}
	fingerprintInput := result
	fingerprintInput.Evidence = Evidence{}
	canonical, err := json.Marshal(fingerprintInput)
	if err != nil {
		return Normalized{}, fmt.Errorf("fingerprint config: %w", err)
	}
	sum := sha256.Sum256(canonical)
	result.Evidence = Evidence{
		Validated: true, SecretFree: true,
		ConfigFingerprint: "sha256:" + hex.EncodeToString(sum[:]),
	}
	return result, nil
}

func loadBFFInputDescriptor(path, environment string) (BFFSourceProjection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BFFSourceProjection{}, fmt.Errorf("read generated BFF input descriptor: %w", err)
	}
	if len(data) == 0 || len(data) > maxBFFConfigBytes {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor has an invalid size")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor is malformed")
	}
	required := []string{"schema_version", "environment", "target", "gcp_project", "bucket", "firestore_database_id",
		"auth_service_url", "pipeline_job_url", "export_job_url", "export_signing_service_account", "allowed_origins",
		"allowed_hosts", "pipeline_daily_limit", "pipeline_cooldown_seconds", "pipeline_min_new_raw", "pipeline_demo_user_ids",
		"auth_session_environment", "auth_session_migration", "registration_enabled", "jwt_secret_reference",
		"deepseek_api_key_reference", "typesafe_api_key_reference", "profile_runtime_audience", "profile_runtime_service_account",
		"llm", "query", "local", "port_default", "config_secret_resource"}
	if len(fields) != len(required) {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor has an invalid shape")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return BFFSourceProjection{}, fmt.Errorf("generated BFF input descriptor is missing %s", key)
		}
	}
	if err := validateBFFDescriptorShape(fields); err != nil {
		return BFFSourceProjection{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var projection BFFSourceProjection
	if err := decoder.Decode(&projection); err != nil {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor must contain one JSON object")
	}
	expectedEnvironment := map[string]string{"development": "dev", "production": "prod"}[environment]
	if expectedEnvironment == "" || projection.SchemaVersion != 2 || projection.Environment != expectedEnvironment || projection.Target != "bff" ||
		projection.GCPProject != "llm-wiki-cloud" || projection.Local != nil || projection.Bucket == "" || projection.FirestoreDatabaseID == "" ||
		projection.PipelineCooldownSeconds <= 0 || projection.PipelineDailyLimit <= 0 || projection.PipelineMinNewRaw <= 0 ||
		projection.PipelineDemoUserIDs == nil || projection.AuthSessionEnvironment == "" || projection.AuthSessionMigration == "" ||
		projection.PortDefault < 1 || projection.PortDefault > 65535 || projection.ConfigSecretResource == "" {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor is invalid for the selected environment")
	}
	if len(projection.AllowedOrigins) == 0 || projection.AllowedHosts == nil || projection.LLM.Provider != "deepseek" ||
		projection.LLM.RequestTimeoutSeconds < 1 || projection.LLM.Model == "" || projection.LLM.BaseURL == "" {
		return BFFSourceProjection{}, errors.New("generated BFF input descriptor is incomplete")
	}
	if err := validateBFFDescriptorSecrets(projection, expectedEnvironment); err != nil {
		return BFFSourceProjection{}, err
	}
	return projection, nil
}

func validateBFFDescriptorShape(fields map[string]json.RawMessage) error {
	exact := func(raw json.RawMessage, keys ...string) bool {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil || len(object) != len(keys) {
			return false
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				return false
			}
		}
		return true
	}
	if !exact(fields["llm"], "provider", "base_url", "request_timeout_seconds", "model") ||
		!exact(fields["query"], "stage_config_path", "legacy") ||
		!exact(fields["jwt_secret_reference"], "source", "env_name", "resource") ||
		!exact(fields["deepseek_api_key_reference"], "source", "env_name", "resource") ||
		!exact(fields["typesafe_api_key_reference"], "source", "env_name", "resource") ||
		!strings.EqualFold(strings.TrimSpace(string(fields["local"])), "null") {
		return errors.New("generated BFF input descriptor has an invalid nested shape")
	}
	var query map[string]json.RawMessage
	if json.Unmarshal(fields["query"], &query) != nil || !strings.EqualFold(strings.TrimSpace(string(query["legacy"])), "null") {
		return errors.New("generated BFF input descriptor must select one sealed query configuration")
	}
	return nil
}

func applyBFFInputDescriptor(config *EnvironmentConfig, inputs BFFSourceProjection) {
	devJWT := false
	bff := &config.BFF
	bff.RuntimeInputs = &inputs
	bff.Bucket = inputs.Bucket
	bff.FirestoreDatabaseID = inputs.FirestoreDatabaseID
	bff.PipelineJobURL = inputs.PipelineJobURL
	bff.AuthServiceURL = inputs.AuthServiceURL
	bff.AllowedOrigins = inputs.AllowedOrigins
	bff.DevJWT = &devJWT
	bff.QueryConfig = inputs.Query.StageConfigPath
	bff.PipelineDailyLimit = inputs.PipelineDailyLimit
	bff.PipelineCooldownSeconds = inputs.PipelineCooldownSeconds
	bff.PipelineMinNewRaw = inputs.PipelineMinNewRaw
	bff.PipelineDemoUserIDs = inputs.PipelineDemoUserIDs
	bff.ProfileRuntimeAudience = inputs.ProfileRuntimeAudience
	bff.ProfileRuntimeServiceAccount = inputs.ProfileRuntimeServiceAccount
	bff.ConfigSecretResource = inputs.ConfigSecretResource
	bff.SecretReferences.JWT = secretNameFromResource(inputs.JWTSecretReference.Resource)
	bff.SecretReferences.DeepSeekAPIKey = secretNameFromResource(inputs.DeepSeekAPIKeyReference.Resource)
	if inputs.TypeSafeAPIKeyReference != nil {
		parts := strings.Split(inputs.TypeSafeAPIKeyReference.Resource, "/")
		if len(parts) == 6 {
			version := VersionedSecretReference{Name: parts[3], Version: parts[5]}
			bff.SecretReferences.TypeSafeJevAPIKey = &version
		}
	}
}

func secretNameFromResource(resource string) string {
	parts := strings.Split(resource, "/")
	if len(parts) != 6 {
		return ""
	}
	return parts[3]
}

func validateBFFDescriptorSecrets(p BFFSourceProjection, environment string) error {
	expected := "dev"
	if environment == "prod" {
		expected = "prod"
	}
	for key, ref := range map[string]BFFSecretReference{
		"jwt_secret_reference":       p.JWTSecretReference,
		"deepseek_api_key_reference": p.DeepSeekAPIKeyReference,
	} {
		if ref.Source != "secret-manager" || ref.EnvName != "" || !regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/[A-Za-z0-9_-]+/versions/(?:latest|[1-9][0-9]*)$`).MatchString(ref.Resource) {
			return fmt.Errorf("generated BFF %s is invalid", key)
		}
	}
	if p.TypeSafeAPIKeyReference == nil || p.TypeSafeAPIKeyReference.Source != "secret-manager" || p.TypeSafeAPIKeyReference.EnvName != "" ||
		!regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-(?:dev|prod)/versions/[1-9][0-9]*$`).MatchString(p.TypeSafeAPIKeyReference.Resource) {
		return errors.New("generated BFF TypeSafe reference is invalid")
	}
	configSecret := "projects/llm-wiki-cloud/secrets/lwc-bff-config-" + expected
	if p.ConfigSecretResource != configSecret {
		return errors.New("generated BFF destination secret resource is invalid")
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeConfig(path string) (EnvironmentConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return EnvironmentConfig{}, fmt.Errorf("read environment config: %w", err)
	}
	defer file.Close()
	var config EnvironmentConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return EnvironmentConfig{}, fmt.Errorf("decode environment config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return EnvironmentConfig{}, errors.New("environment config must contain exactly one YAML document")
		}
		return EnvironmentConfig{}, fmt.Errorf("decode environment config: %w", err)
	}
	return config, nil
}

func parseComponents(raw string) ([]string, error) {
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, errors.New("component set contains an empty component")
		}
		if _, ok := componentSet[name]; !ok {
			return nil, fmt.Errorf("component %q is not allowlisted", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("component %q is duplicated", name)
		}
		seen[name] = true
	}
	result := make([]string, 0, len(seen))
	for _, name := range allowedComponents {
		if seen[name] {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("component set must not be empty")
	}
	return result, nil
}

func validateConfig(config EnvironmentConfig) error {
	return validateConfigForSelection("", config, false)
}

func validateConfigForEnvironment(environment string, config EnvironmentConfig) error {
	return validateConfigForSelection(environment, config, false)
}

func validateConfigForSelection(environment string, config EnvironmentConfig, hasBFFInputs bool) error {
	for name, value := range map[string]string{
		"gcp.project_id": config.GCP.ProjectID, "gcp.region": config.GCP.Region,
		"gcp.artifact_registry": config.GCP.ArtifactRegistry,
		"auth.service_name":     config.Auth.ServiceName, "auth.runtime_service_account": config.Auth.RuntimeServiceAccount,
		"auth.network": config.Auth.Network, "auth.subnet": config.Auth.Subnet, "auth.vpc_egress": config.Auth.VPCEgress, "auth.ingress": config.Auth.Ingress,
		"auth.firestore_database_id": config.Auth.FirestoreDatabaseID, "auth.public_domain": config.Auth.PublicDomain,
		"auth.demo_user_id": config.Auth.DemoUserID, "auth.demo_user_email": config.Auth.DemoUserEmail,
		"auth.demo_user_role":        config.Auth.DemoUserRole,
		"auth.secret_references.jwt": config.Auth.SecretReferences.JWT,
		"bff.service_name":           config.BFF.ServiceName, "bff.runtime_service_account": config.BFF.RuntimeServiceAccount,
		"bff.network": config.BFF.Network, "bff.subnet": config.BFF.Subnet, "bff.vpc_egress": config.BFF.VPCEgress, "bff.ingress": config.BFF.Ingress,
		"bff.pipeline_job_name": config.BFF.PipelineJobName, "bff.pipeline_job_location": config.BFF.PipelineJobLocation,
		"worker.job_name": config.Worker.JobName, "worker.runtime_service_account": config.Worker.RuntimeServiceAccount,
		"worker.bucket": config.Worker.Bucket, "worker.location": config.Worker.Location,
		"worker.secret_references.deepseek_api_key": config.Worker.SecretReferences.DeepSeekAPIKey,
		"frontend.project_name":                     config.Frontend.ProjectName, "frontend.team_slug": config.Frontend.TeamSlug,
		"frontend.repository": config.Frontend.Repository, "frontend.root_directory": config.Frontend.RootDirectory,
		"frontend.api_url": config.Frontend.APIURL, "frontend.auth_url": config.Frontend.AuthURL,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing required field %s", name)
		}
		if secretValuePattern.MatchString(value) {
			return fmt.Errorf("secret-bearing value is not allowed in %s", name)
		}
	}
	if hasBFFInputs {
		for name, value := range map[string]string{
			"bff.bucket": config.BFF.Bucket, "bff.firestore_database_id": config.BFF.FirestoreDatabaseID,
			"bff.pipeline_job_url": config.BFF.PipelineJobURL, "bff.auth_service_url": config.BFF.AuthServiceURL,
			"bff.query_config": config.BFF.QueryConfig, "bff.secret_references.jwt": config.BFF.SecretReferences.JWT,
			"bff.secret_references.deepseek_api_key": config.BFF.SecretReferences.DeepSeekAPIKey,
			"bff.config_secret_resource":             config.BFF.ConfigSecretResource,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("missing required field %s", name)
			}
		}
	}
	if config.GCP.ProjectID != "llm-wiki-cloud" || config.GCP.Region != "asia-east1" {
		return errors.New("gcp identity is not the reviewed deployment target")
	}
	if !strings.HasPrefix(config.GCP.ArtifactRegistry, config.GCP.Region+"-docker.pkg.dev/") {
		return errors.New("artifact registry must be regional")
	}
	if config.Auth.MaxInstances != 1 || config.BFF.MaxInstances != 1 {
		return errors.New("service max_instances must be exactly 1")
	}
	if config.ExportJob.Enabled {
		for name, value := range map[string]string{
			"export_job.job_name": config.ExportJob.JobName, "export_job.runtime_service_account": config.ExportJob.RuntimeServiceAccount,
			"export_job.bucket": config.ExportJob.Bucket, "export_job.firestore_database_id": config.ExportJob.FirestoreDatabaseID,
			"export_job.location": config.ExportJob.Location, "export_job.signing_service_account": config.ExportJob.SigningServiceAccount,
		} {
			if strings.TrimSpace(value) == "" || secretValuePattern.MatchString(value) {
				return fmt.Errorf("export_job has missing or secret-bearing %s", name)
			}
		}
		if config.ExportJob.JobTimeout != "23h" || config.ExportJob.MaxRetries != 0 ||
			config.ExportJob.Parallelism != 1 || config.ExportJob.Tasks != 1 {
			return errors.New("export_job task limits are not the reviewed single-task contract")
		}
		if config.ExportJob.Location != config.GCP.Region || (hasBFFInputs && (config.ExportJob.Bucket != config.BFF.Bucket || config.ExportJob.FirestoreDatabaseID != config.BFF.FirestoreDatabaseID)) {
			return errors.New("export_job target must match the reviewed environment region, bucket, and Firestore database")
		}
		if environment == "development" && (config.ExportJob.JobName != "export-job-dev" ||
			config.ExportJob.RuntimeServiceAccount != "lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com" ||
			config.ExportJob.SigningServiceAccount != "lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com") {
			return errors.New("export_job identities are not the reviewed Development resources")
		}
		if environment == "production" && (config.ExportJob.JobName != "export-job" ||
			config.ExportJob.RuntimeServiceAccount != "lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
			config.ExportJob.SigningServiceAccount != "lwc-export-signer-prod@llm-wiki-cloud.iam.gserviceaccount.com") {
			return errors.New("export_job identities are not the reviewed Production resources")
		}
	} else if config.ExportJob.JobName != "" || config.ExportJob.RuntimeServiceAccount != "" || config.ExportJob.Bucket != "" || config.ExportJob.FirestoreDatabaseID != "" || config.ExportJob.Location != "" || config.ExportJob.SigningServiceAccount != "" || config.ExportJob.JobTimeout != "" || config.ExportJob.MaxRetries != 0 || config.ExportJob.Parallelism != 0 || config.ExportJob.Tasks != 0 {
		return errors.New("disabled export_job must remain unconfigured")
	}
	if config.Auth.Network != "default" || config.Auth.Subnet != "default" || config.Auth.VPCEgress != "private-ranges-only" || config.Auth.Ingress != "all" ||
		config.BFF.Network != "default" || config.BFF.Subnet != "default" || config.BFF.VPCEgress != "private-ranges-only" || config.BFF.Ingress != "all" {
		return errors.New("service network configuration is not the reviewed Cloud Run definition")
	}
	if !pipelineDemoUserIDPattern.MatchString(config.Auth.DemoUserID) {
		return errors.New("auth.demo_user_id is invalid")
	}
	if config.Auth.DemoUserEmail != strings.TrimSpace(config.Auth.DemoUserEmail) ||
		!demoEmailPattern.MatchString(config.Auth.DemoUserEmail) ||
		strings.ToLower(config.Auth.DemoUserEmail) != config.Auth.DemoUserEmail {
		return errors.New("auth.demo_user_email is invalid")
	}
	if !demoRolePattern.MatchString(config.Auth.DemoUserRole) || strings.EqualFold(config.Auth.DemoUserRole, "admin") {
		return errors.New("auth.demo_user_role must be a non-admin role")
	}
	if err := validateStringList("auth.allowed_hosts", config.Auth.AllowedHosts); err != nil {
		return err
	}
	if err := validateStringList("auth.allowed_origins", config.Auth.AllowedOrigins); err != nil {
		return err
	}
	if hasBFFInputs {
		if err := validateStringList("bff.allowed_origins", config.BFF.AllowedOrigins); err != nil {
			return err
		}
	}
	if hasBFFInputs && len(config.BFF.PipelineDemoUserIDs) > 0 {
		if err := validateStringList("bff.pipeline_demo_user_ids", config.BFF.PipelineDemoUserIDs); err != nil {
			return err
		}
	}
	if hasBFFInputs && (len(config.BFF.PipelineDemoUserIDs) != 1 ||
		config.BFF.PipelineDemoUserIDs[0] != config.Auth.DemoUserID) {
		return errors.New("BFF Pipeline Demo identity must match the selected Auth Demo identity")
	}
	if err := validateStringList("frontend.stable_aliases", config.Frontend.StableAliases); err != nil {
		return err
	}
	if len(config.Worker.Args) == 0 {
		return errors.New("worker.args must not be empty")
	}
	if hasBFFInputs {
		if config.BFF.DevJWT == nil || *config.BFF.DevJWT {
			return errors.New("BFF local-only JWT mode must be false for deployed environments")
		}
	}
	if len(config.Worker.Args) != 2 || config.Worker.Args[0] != "run" || config.Worker.Args[1] != `[["run","--auto-approve"]]` {
		return errors.New("worker.args is not the reviewed Cloud Run definition")
	}
	if config.Frontend.Repository != "Rayer/llm-wiki-cloud" || config.Frontend.RootDirectory != "apps/frontend" {
		return errors.New("frontend repository identity is not reviewed")
	}
	if config.Frontend.ProjectName != "llm-wiki-frontend" && config.Frontend.ProjectName != "llm-wiki-frontend-dev" {
		return errors.New("frontend project identity is not reviewed")
	}
	if config.Frontend.TeamSlug != "rayer-tung-s-projects" {
		return errors.New("frontend team identity is not reviewed")
	}
	secretRefs := map[string]string{
		"auth.jwt": config.Auth.SecretReferences.JWT, "worker.deepseek_api_key": config.Worker.SecretReferences.DeepSeekAPIKey,
	}
	if hasBFFInputs {
		secretRefs["bff.jwt"] = config.BFF.SecretReferences.JWT
		secretRefs["bff.deepseek_api_key"] = config.BFF.SecretReferences.DeepSeekAPIKey
	}
	for name, value := range secretRefs {
		if !secretRefPattern.MatchString(value) {
			return fmt.Errorf("secret reference %s is invalid", name)
		}
	}
	if environment != "" {
		jwt := "jwt-secret-dev"
		if environment == "production" {
			jwt = "jwt-secret-prod"
		}
		if config.Auth.SecretReferences.JWT != jwt || config.Worker.SecretReferences.DeepSeekAPIKey != "deepseek-apikey" {
			return errors.New("secret references are not the reviewed environment bindings")
		}
		if hasBFFInputs && (config.BFF.SecretReferences.JWT != jwt || config.BFF.SecretReferences.DeepSeekAPIKey != "deepseek-apikey") {
			return errors.New("BFF secret references are not the selected environment bindings")
		}
		if hasBFFInputs {
			if err := validateProfileRuntimeConfig(environment, config); err != nil {
				return err
			}
		}
	}
	return validateGoogleDeployment(environment, config, hasBFFInputs)
}

func validateProfileRuntimeConfig(environment string, config EnvironmentConfig) error {
	if !validProfileRuntimeAudience(config.BFF.ProfileRuntimeAudience) || !validProfileRuntimeServiceAccount(config.BFF.ProfileRuntimeServiceAccount) {
		return errors.New("Profile runtime audience or invoker identity is invalid")
	}
	ref := config.BFF.SecretReferences.TypeSafeJevAPIKey
	if ref == nil || !secretRefPattern.MatchString(ref.Name) || secretValuePattern.MatchString(ref.Name) || !secretVersionPattern.MatchString(ref.Version) {
		return errors.New("TypeSafe credential must use an exact secret reference and numeric version")
	}
	if environment == "production" && (config.BFF.ProfileRuntimeAudience != "https://llm-wiki-bff-a5nkmux6pq-de.a.run.app" ||
		config.BFF.ProfileRuntimeServiceAccount != "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
		ref.Name != "typesafe-jev-api-key-prod") {
		return errors.New("Production Profile runtime bindings are not the reviewed target")
	}
	return nil
}

func validProfileRuntimeAudience(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		(parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && !strings.Contains(value, "#")
}

func validProfileRuntimeServiceAccount(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && profileRuntimeServiceAccountPattern.MatchString(value)
}

func validateStringList(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s contains an empty value", name)
		}
		if seen[value] {
			return fmt.Errorf("%s contains duplicate value", name)
		}
		seen[value] = true
	}
	return nil
}

func loadQueryConfig(repoRoot, repositoryPath string) (QueryConfigIdentity, error) {
	repositoryPath = filepath.ToSlash(strings.TrimSpace(repositoryPath))
	if repositoryPath == "" || filepath.IsAbs(repositoryPath) || strings.HasPrefix(repositoryPath, "/") {
		return QueryConfigIdentity{}, errors.New("query_config must be a repository-relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(repositoryPath))
	if clean != repositoryPath || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return QueryConfigIdentity{}, errors.New("query_config must not traverse outside the repository")
	}
	if !strings.HasPrefix(clean, "apps/bff/configs/query/") {
		return QueryConfigIdentity{}, errors.New("query_config must point under apps/bff/configs/query")
	}
	absPath := filepath.Join(repoRoot, filepath.FromSlash(clean))
	config, raw, err := queryconfig.LoadFileCanonicalBytes(absPath)
	if err != nil {
		return QueryConfigIdentity{}, fmt.Errorf("load query_config %q: %w", repositoryPath, err)
	}
	canonical, err := queryconfig.CanonicalJSON(config)
	if err != nil || string(raw) != string(canonical) {
		return QueryConfigIdentity{}, errors.New("query_config must be an immutable canonical sealed JSON artifact")
	}
	return QueryConfigIdentity{
		RepositoryPath: clean,
		RuntimePath:    "/app/" + strings.TrimPrefix(clean, "apps/bff/"),
		SchemaVersion:  config.SchemaVersion,
		Revision:       config.ConfigRevision,
		Digest:         config.ConfigDigest,
	}, nil
}

func componentInputs(config EnvironmentConfig, query QueryConfigIdentity, selected []string) map[string]any {
	components := make(map[string]any, len(selected))
	for _, name := range selected {
		switch name {
		case "auth":
			components[name] = map[string]any{"service_name": config.Auth.ServiceName, "runtime_service_account": config.Auth.RuntimeServiceAccount, "network": config.Auth.Network, "subnet": config.Auth.Subnet, "vpc_egress": config.Auth.VPCEgress, "ingress": config.Auth.Ingress, "max_instances": config.Auth.MaxInstances, "public_domain": config.Auth.PublicDomain, "firestore_database_id": config.Auth.FirestoreDatabaseID, "demo_user_id": config.Auth.DemoUserID, "demo_user_email": config.Auth.DemoUserEmail, "demo_user_role": config.Auth.DemoUserRole, "allowed_hosts": config.Auth.AllowedHosts, "allowed_origins": config.Auth.AllowedOrigins, "dev_jwt": false, "secret_references": map[string]any{"jwt": config.Auth.SecretReferences.JWT}}
			if config.Auth.Google != nil {
				components[name].(map[string]any)["google"] = config.Auth.Google
			}
		case "bff":
			secretReferences := map[string]any{"jwt": config.BFF.SecretReferences.JWT, "deepseek_api_key": config.BFF.SecretReferences.DeepSeekAPIKey}
			if config.BFF.SecretReferences.TypeSafeJevAPIKey != nil {
				secretReferences["typesafe_jev_api_key"] = config.BFF.SecretReferences.TypeSafeJevAPIKey
			}
			bff := map[string]any{"service_name": config.BFF.ServiceName, "runtime_service_account": config.BFF.RuntimeServiceAccount, "network": config.BFF.Network, "subnet": config.BFF.Subnet, "vpc_egress": config.BFF.VPCEgress, "ingress": config.BFF.Ingress, "max_instances": config.BFF.MaxInstances, "bucket": config.BFF.Bucket, "firestore_database_id": config.BFF.FirestoreDatabaseID, "pipeline_job_name": config.BFF.PipelineJobName, "pipeline_job_location": config.BFF.PipelineJobLocation, "pipeline_job_url": config.BFF.PipelineJobURL, "auth_service_url": config.BFF.AuthServiceURL, "allowed_origins": config.BFF.AllowedOrigins, "dev_jwt": false, "query_config": query, "secret_references": secretReferences, "config_secret_resource": config.BFF.ConfigSecretResource, "pipeline_daily_limit": config.BFF.PipelineDailyLimit, "pipeline_min_new_raw": config.BFF.PipelineMinNewRaw, "runtime_inputs": config.BFF.RuntimeInputs}
			if config.BFF.ProfileRuntimeAudience != "" {
				bff["profile_runtime_audience"] = config.BFF.ProfileRuntimeAudience
				bff["profile_runtime_service_account"] = config.BFF.ProfileRuntimeServiceAccount
			}
			if len(config.BFF.PipelineDemoUserIDs) > 0 {
				bff["pipeline_demo_user_ids"] = config.BFF.PipelineDemoUserIDs
			}
			if config.BFF.PipelineCooldownSeconds > 0 {
				bff["pipeline_cooldown_seconds"] = config.BFF.PipelineCooldownSeconds
			}
			components[name] = bff
		case "worker":
			components[name] = map[string]any{"job_name": config.Worker.JobName, "runtime_service_account": config.Worker.RuntimeServiceAccount, "bucket": config.Worker.Bucket, "location": config.Worker.Location, "args": config.Worker.Args, "secret_references": map[string]any{"deepseek_api_key": config.Worker.SecretReferences.DeepSeekAPIKey}}
		case "exportjob":
			components[name] = config.ExportJob
		case "frontend":
			components[name] = map[string]any{"project_name": config.Frontend.ProjectName, "team_slug": config.Frontend.TeamSlug, "repository": config.Frontend.Repository, "root_directory": config.Frontend.RootDirectory, "stable_aliases": config.Frontend.StableAliases, "api_url": config.Frontend.APIURL, "auth_url": config.Frontend.AuthURL}
		}
	}
	return components
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "deploy config validation failed: "+format+"\n", args...)
	os.Exit(1)
}
