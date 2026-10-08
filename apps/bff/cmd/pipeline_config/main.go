package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	runtimeconfig "github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/secretmanager/v1"
)

const maxConfigBytes = 1 << 20
const maxBFFConfigBytes = 64 << 10

var secretResourcePattern = regexp.MustCompile(`^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+/versions/(?:[1-9][0-9]*|latest)$`)
var resolvedSecretResourcePattern = regexp.MustCompile(`^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+/versions/[1-9][0-9]*$`)
var projectNumberPattern = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)

var (
	errProjectIdentityUnavailable = errors.New("Cloud Resource Manager response omitted canonical project identity")
	errProjectIdentityMismatch    = errors.New("Cloud Resource Manager response does not identify the selected project")
)

type secretBinding struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	EnvName  string `json:"envName"`
	Resource string `json:"resource"`
}

type llmProfile struct {
	Provider              string        `json:"provider"`
	Endpoint              string        `json:"endpoint"`
	Model                 string        `json:"model"`
	RequestTimeoutSeconds int           `json:"requestTimeoutSeconds"`
	Secret                secretBinding `json:"secret"`
}

type pipelineConfig struct {
	Environment       string     `json:"environment"`
	Bucket            string     `json:"bucket"`
	RunTimeoutSeconds int        `json:"runTimeoutSeconds"`
	ArticleMaxTokens  *int       `json:"articleMaxTokens"`
	LLM               llmProfile `json:"llm"`
}

type bffSecretReference struct {
	Source   string `json:"source"`
	EnvName  string `json:"env_name"`
	Resource string `json:"resource"`
}

type bffLLM struct {
	Provider              string `json:"provider"`
	BaseURL               string `json:"base_url"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	Model                 string `json:"model"`
}

type bffQueryLegacy struct {
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

type bffQuery struct {
	StageConfigPath string          `json:"stage_config_path"`
	Legacy          *bffQueryLegacy `json:"legacy"`
}

type bffLocal struct {
	Scope                string `json:"scope"`
	WorkerPath           string `json:"worker_path"`
	PipelineConfigPath   string `json:"pipeline_config_path"`
	PipelineBindingsPath string `json:"pipeline_bindings_path"`
}

type bffSourceProjection struct {
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
	JWTSecretReference           bffSecretReference  `json:"jwt_secret_reference"`
	DeepSeekAPIKeyReference      bffSecretReference  `json:"deepseek_api_key_reference"`
	TypeSafeAPIKeyReference      *bffSecretReference `json:"typesafe_api_key_reference"`
	ProfileRuntimeAudience       string              `json:"profile_runtime_audience"`
	ProfileRuntimeServiceAccount string              `json:"profile_runtime_service_account"`
	LLM                          bffLLM              `json:"llm"`
	Query                        bffQuery            `json:"query"`
	Local                        *bffLocal           `json:"local"`
	PortDefault                  int                 `json:"port_default"`
	ConfigSecretResource         string              `json:"config_secret_resource"`
}

type privateBindings struct {
	Environment string          `json:"environment"`
	Bindings    []secretBinding `json:"bindings"`
	LocalAPIKey []byte          `json:"localApiKey,omitempty"`
}

type secretReader interface {
	Access(context.Context, string) (string, []byte, error)
}

type projectIdentity struct {
	ProjectID     string
	ProjectNumber string
}

type projectIdentityReader interface {
	ProjectIdentity(context.Context, string) (projectIdentity, error)
}

type googleSecretReader struct {
	service  *secretmanager.Service
	projects *cloudresourcemanager.Service
}

func (r googleSecretReader) Access(ctx context.Context, resource string) (string, []byte, error) {
	response, err := r.service.Projects.Secrets.Versions.Access(resource).Context(ctx).Do()
	if err != nil {
		return "", nil, err
	}
	if response.Name == "" {
		return "", nil, errors.New("Secret Manager response omitted version name")
	}
	if response.Payload == nil || response.Payload.Data == "" {
		return response.Name, nil, nil
	}
	value, err := base64.StdEncoding.DecodeString(response.Payload.Data)
	if err != nil {
		return "", nil, errors.New("Secret Manager payload encoding is invalid")
	}
	if len(value) == 0 {
		return response.Name, nil, nil
	}
	return response.Name, value, nil
}

func (r googleSecretReader) ProjectIdentity(ctx context.Context, project string) (projectIdentity, error) {
	if r.projects == nil {
		return projectIdentity{}, errProjectIdentityUnavailable
	}
	response, err := r.projects.Projects.Get("projects/" + project).Context(ctx).Do()
	if err != nil {
		return projectIdentity{}, err
	}
	if response == nil || !strings.HasPrefix(response.Name, "projects/") {
		return projectIdentity{}, errProjectIdentityUnavailable
	}
	projectNumber := strings.TrimPrefix(response.Name, "projects/")
	if response.ProjectId == "" || !projectNumberPattern.MatchString(projectNumber) {
		return projectIdentity{}, errProjectIdentityUnavailable
	}
	if !projectIdentityMatchesRequest(project, projectIdentity{
		ProjectID: response.ProjectId, ProjectNumber: projectNumber,
	}) {
		return projectIdentity{}, errProjectIdentityMismatch
	}
	return projectIdentity{ProjectID: response.ProjectId, ProjectNumber: projectNumber}, nil
}

type readerFactory func(context.Context) (secretReader, error)

func googleReader(ctx context.Context) (secretReader, error) {
	const scope = "https://www.googleapis.com/auth/cloud-platform"
	service, err := secretmanager.NewService(ctx, option.WithScopes(scope))
	if err != nil {
		return nil, err
	}
	projects, err := cloudresourcemanager.NewService(ctx, option.WithScopes(scope))
	if err != nil {
		return nil, err
	}
	return googleSecretReader{service: service, projects: projects}, nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "prepare" {
		fmt.Fprintln(os.Stderr, "usage: pipeline-config prepare --target pipeline|bff --environment local|dev|prod --output DIR")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("prepare", flag.ExitOnError)
	target := flags.String("target", "pipeline", "generated target: pipeline or bff")
	environment := flags.String("environment", "", "selected target environment")
	output := flags.String("output", "", "generated output directory")
	descriptor := flags.Bool("descriptor", false, "emit the nonsecret BFF input projection for deployment admission")
	_ = flags.Parse(os.Args[2:])
	if err := runPrepareTargetMode(context.Background(), *target, *environment, *output, *descriptor, googleReader); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runTimeoutFromEnvironment() (int, error) {
	raw := strings.TrimSpace(os.Getenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS"))
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 || int64(seconds) > (int64(1<<63-1)/int64(time.Second)) {
		return 0, errors.New("LWC_PIPELINE_RUN_TIMEOUT_SECONDS must be set to the verified Pipeline Job timeout in seconds")
	}
	return seconds, nil
}

func runPrepare(ctx context.Context, environment, output string, newReader readerFactory) error {
	return runPrepareTarget(ctx, "pipeline", environment, output, newReader)
}

func runPrepareTarget(ctx context.Context, target, environment, output string, newReader readerFactory) error {
	return runPrepareTargetMode(ctx, target, environment, output, false, newReader)
}

func runPrepareTargetMode(ctx context.Context, target, environment, output string, descriptorOnly bool, newReader readerFactory) error {
	if target != "pipeline" && target != "bff" {
		return errors.New("target must be pipeline or bff")
	}
	if descriptorOnly && target != "bff" {
		return errors.New("--descriptor requires --target bff")
	}
	if environment != "local" && environment != "dev" && environment != "prod" {
		return errors.New("environment must be local, dev, or prod")
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("output directory is required")
	}
	if target == "bff" {
		if err := invalidateBFFOutput(output); err != nil {
			return err
		}
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	cacheDir := strings.TrimSpace(os.Getenv("PKL_CACHE_DIR"))
	properties := []string{"--property", "environment=" + environment, "--property", "target=" + target}
	if target == "pipeline" {
		timeoutSeconds, err := runTimeoutFromEnvironment()
		if err != nil {
			return err
		}
		properties = append(properties, "--property", "runTimeoutSeconds="+strconv.Itoa(timeoutSeconds))
		localResource := strings.TrimSpace(os.Getenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE"))
		if localResource != "" {
			properties = append(properties, "--property", "localSecretVersionResource="+localResource)
		}
	} else {
		for _, property := range []struct{ name, env, fallback string }{
			{"bffLocalScope", "LOCAL_CLOUD_SCOPE", ""},
			{"bffLocalWorkerPath", "LOCAL_CLOUD_WORKER_PATH", ""},
			{"bffLocalPipelineConfigPath", "LOCAL_CLOUD_PIPELINE_CONFIG_PATH", ""},
			{"bffLocalPipelineBindingsPath", "LOCAL_CLOUD_PIPELINE_BINDINGS_PATH", ""},
			{"bffLocalJWTSecretFile", "LOCAL_CLOUD_JWT_SECRET_FILE", ""},
			{"bffLocalDemoUserIDs", "PIPELINE_DEMO_USER_IDS", ""},
			{"bffLocalPort", "BFF_PORT", "8080"},
			{"bffLocalAuthPort", "AUTH_PORT", "8081"},
			{"bffLocalFrontendPort", "FRONTEND_PORT", "3000"},
		} {
			value := strings.TrimSpace(os.Getenv(property.env))
			if value == "" {
				value = property.fallback
			}
			properties = append(properties, "--property", property.name+"="+value)
		}
	}
	pklArgs := func(propertySet []string, args ...string) []string {
		all := make([]string, 0, len(args)+len(propertySet)+2)
		all = append(all, args[:len(args)-1]...)
		if cacheDir != "" {
			all = append(all, "--cache-dir", cacheDir)
		}
		all = append(all, propertySet...)
		return append(all, args[len(args)-1])
	}

	ssotCmd := exec.CommandContext(ctx, pklBinary(), pklArgs(properties, "eval", "--format", "json", filepath.Join(root, "deploy/cac/ssot.pkl"))...)
	var ssotStderr bytes.Buffer
	ssotCmd.Stderr = &ssotStderr
	ssotBytes, err := ssotCmd.Output()
	if err != nil {
		return fmt.Errorf("evaluate selected Pipeline SSOT: %w: %s", err, strings.TrimSpace(ssotStderr.String()))
	}
	var config pipelineConfig
	if target == "bff" {
		projection, err := decodeBFFSourceProjection(ssotBytes, environment)
		if err != nil {
			return err
		}
		if descriptorOnly {
			return writeBFFDescriptor(output, projection)
		}
		return prepareBFFRuntimeConfig(ctx, root, output, projection, newReader)
	}
	if err := json.Unmarshal(ssotBytes, &config); err != nil {
		return fmt.Errorf("decode selected Pipeline SSOT: %w", err)
	}
	timeoutSeconds, err := runTimeoutFromEnvironment()
	if err != nil {
		return err
	}
	if config.Environment != environment || config.RunTimeoutSeconds != timeoutSeconds ||
		config.LLM.Provider == "" || config.LLM.Endpoint == "" || config.LLM.Model == "" ||
		config.LLM.RequestTimeoutSeconds <= 0 || config.LLM.Secret.Target != "DEEPSEEK_API_KEY" {
		return errors.New("selected Pipeline SSOT identity is invalid")
	}
	secret := config.LLM.Secret
	secretValue, resolvedResource, err := resolveBinding(ctx, secret, newReader)
	if err != nil {
		return err
	}
	defer clear(secretValue)
	if secret.Source == "secret-manager" {
		secret.Resource = resolvedResource
	}

	output = filepath.Clean(output)
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tempDir, err := os.MkdirTemp(output, ".prepare-")
	if err != nil {
		return fmt.Errorf("create temporary config directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	renderProperties := []string{"--property", "preparedEnvironment=" + config.Environment,
		"--property", "preparedRunTimeoutSeconds=" + strconv.Itoa(config.RunTimeoutSeconds),
		"--property", "preparedSecretSource=" + secret.Source,
		"--property", "preparedSecretTarget=" + secret.Target,
		"--property", "preparedLlmProvider=" + config.LLM.Provider,
		"--property", "preparedLlmEndpoint=" + config.LLM.Endpoint,
		"--property", "preparedLlmModel=" + config.LLM.Model,
		"--property", "preparedLlmRequestTimeoutSeconds=" + strconv.Itoa(config.LLM.RequestTimeoutSeconds)}
	if config.Bucket != "" {
		renderProperties = append(renderProperties, "--property", "preparedBucket="+config.Bucket)
	}
	if secret.EnvName != "" {
		renderProperties = append(renderProperties, "--property", "preparedSecretEnvName="+secret.EnvName)
	}
	if secret.Resource != "" {
		renderProperties = append(renderProperties, "--property", "preparedSecretResource="+secret.Resource)
	}
	if config.ArticleMaxTokens != nil {
		renderProperties = append(renderProperties, "--property",
			"preparedArticleMaxTokens="+strconv.Itoa(*config.ArticleMaxTokens))
	}
	renderCmd := exec.CommandContext(ctx, pklBinary(), pklArgs(renderProperties,
		"eval", "--multiple-file-output-path", tempDir,
		filepath.Join(root, "deploy/cac/synto.pkl"))...)
	var renderStderr bytes.Buffer
	renderCmd.Stderr = &renderStderr
	if err := renderCmd.Run(); err != nil {
		return fmt.Errorf("render fresh Pipeline configuration: %w: %s", err, strings.TrimSpace(renderStderr.String()))
	}
	for _, name := range []string{"pipeline.json", "synto.toml"} {
		info, err := os.Lstat(filepath.Join(tempDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxConfigBytes {
			return fmt.Errorf("renderer did not produce a valid %s", name)
		}
	}
	if err := bindResolvedSecretReference(tempDir, secret); err != nil {
		return fmt.Errorf("bind prepared Pipeline secret reference: %w", err)
	}
	tomlBytes, err := os.ReadFile(filepath.Join(tempDir, "synto.toml"))
	if err != nil {
		return fmt.Errorf("read rendered synto.toml: %w", err)
	}
	if strings.Contains(string(tomlBytes), "secret-manager") ||
		(secret.Resource != "" && strings.Contains(string(tomlBytes), secret.Resource)) {
		return errors.New("rendered synto.toml contains a secret reference")
	}
	private := privateBindings{Environment: environment}
	if secret.Source == "secret-manager" {
		private.Bindings = []secretBinding{secret}
		if environment == "local" {
			private.LocalAPIKey = append([]byte(nil), secretValue...)
		}
	}
	privateBytes, err := json.Marshal(private)
	if err != nil {
		return fmt.Errorf("encode private Pipeline bindings: %w", err)
	}
	clear(private.LocalAPIKey)
	if err := os.WriteFile(filepath.Join(tempDir, "private-bindings.json"), privateBytes, 0o600); err != nil {
		clear(privateBytes)
		return fmt.Errorf("write private Pipeline bindings: %w", err)
	}
	clear(privateBytes)
	for _, name := range []string{"pipeline.json", "synto.toml", "private-bindings.json"} {
		if err := replaceFile(filepath.Join(tempDir, name), filepath.Join(output, name)); err != nil {
			return fmt.Errorf("publish generated %s: %w", name, err)
		}
	}
	fmt.Printf("prepared Pipeline config environment=%s synto_sha256=%x\n", environment, sha256Sum(tomlBytes))
	return nil
}

func invalidateBFFOutput(output string) error {
	output = filepath.Clean(output)
	if err := os.MkdirAll(output, 0o700); err != nil {
		return fmt.Errorf("create private BFF config directory: %w", err)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("BFF config output must be a real directory")
	}
	if err := os.Chmod(output, 0o700); err != nil {
		return fmt.Errorf("secure BFF config directory: %w", err)
	}
	if err := os.Remove(filepath.Join(output, "bff.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("invalidate prior BFF config: %w", err)
	}
	return nil
}

func decodeBFFSourceProjection(data []byte, environment string) (bffSourceProjection, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return bffSourceProjection{}, errors.New("selected BFF SSOT is malformed")
	}
	required := []string{
		"schema_version", "environment", "target", "gcp_project", "bucket", "firestore_database_id",
		"auth_service_url", "pipeline_job_url", "export_job_url", "export_signing_service_account",
		"allowed_origins", "allowed_hosts", "pipeline_daily_limit", "pipeline_cooldown_seconds",
		"pipeline_min_new_raw", "pipeline_demo_user_ids", "auth_session_environment", "auth_session_migration",
		"jwt_secret_reference", "deepseek_api_key_reference", "profile_runtime_audience",
		"profile_runtime_service_account", "llm", "query", "port_default", "config_secret_resource",
	}
	if len(fields) < len(required)+1 || len(fields) > len(required)+3 {
		return bffSourceProjection{}, errors.New("selected BFF SSOT has an invalid shape")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return bffSourceProjection{}, fmt.Errorf("selected BFF SSOT is missing %s", key)
		}
	}
	for key := range fields {
		if !slicesContains(append(required, "registration_enabled", "local", "typesafe_api_key_reference"), key) {
			return bffSourceProjection{}, fmt.Errorf("selected BFF SSOT contains unknown %s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projection bffSourceProjection
	if err := decoder.Decode(&projection); err != nil {
		return bffSourceProjection{}, errors.New("selected BFF SSOT has an invalid type")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return bffSourceProjection{}, errors.New("selected BFF SSOT has trailing data")
	}
	if err := validateBFFSourceProjection(projection, environment); err != nil {
		return bffSourceProjection{}, err
	}
	return projection, nil
}

func validateBFFSourceProjection(p bffSourceProjection, environment string) error {
	maxSeconds := int64(^uint64(0)>>1) / int64(time.Second)
	if p.SchemaVersion != 2 || p.Environment != environment || p.Target != "bff" ||
		strings.TrimSpace(p.GCPProject) == "" || strings.TrimSpace(p.Bucket) == "" ||
		strings.TrimSpace(p.FirestoreDatabaseID) == "" || p.PipelineDailyLimit <= 0 ||
		p.PipelineCooldownSeconds <= 0 || int64(p.PipelineCooldownSeconds) > maxSeconds ||
		p.PipelineMinNewRaw <= 0 || len(p.AllowedOrigins) == 0 || p.PipelineDemoUserIDs == nil ||
		p.AuthSessionEnvironment == "" || (p.AuthSessionMigration != "disabled" && p.AuthSessionMigration != "legacy_read_through") ||
		p.PortDefault < 1 || p.PortDefault > 65535 || p.LLM.Provider != "deepseek" ||
		p.LLM.RequestTimeoutSeconds <= 0 || int64(p.LLM.RequestTimeoutSeconds) > maxSeconds ||
		p.LLM.Model != "deepseek-flash" || !absoluteURL(p.LLM.BaseURL) ||
		p.ProfileRuntimeAudience == "" != (p.ProfileRuntimeServiceAccount == "") {
		return errors.New("selected BFF SSOT identity or required value is invalid")
	}
	if err := validateBFFURL(p.AuthServiceURL, environment == "local"); err != nil {
		return errors.New("selected BFF auth_service_url is invalid")
	}
	if err := validateBFFPipelineURL(p.PipelineJobURL); err != nil {
		return errors.New("selected BFF pipeline_job_url is invalid")
	}
	if (p.ExportJobURL == "") != (p.ExportSigningServiceAccount == "") {
		return errors.New("selected BFF export URL and signer must be configured together")
	}
	if p.ExportJobURL != "" && (!absoluteURL(p.ExportJobURL) || !strings.HasPrefix(p.ExportJobURL, "https://run.googleapis.com/")) {
		return errors.New("selected BFF export_job_url is invalid")
	}
	for _, origin := range p.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || strings.Contains(origin, "*") {
			return errors.New("selected BFF allowed_origins is invalid")
		}
		if environment == "local" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1")) {
			return errors.New("selected local BFF allowed_origins must use loopback")
		}
	}
	for _, host := range p.AllowedHosts {
		if host == "" || strings.Contains(host, "*") || (environment == "local" && host != "localhost" && host != "127.0.0.1") {
			return errors.New("selected BFF allowed_hosts is invalid")
		}
	}
	for _, id := range p.PipelineDemoUserIDs {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(id) {
			return errors.New("selected BFF pipeline_demo_user_ids is invalid")
		}
	}
	if err := validateBFFSecretReference("jwt_secret_reference", p.JWTSecretReference, environment); err != nil {
		return err
	}
	if err := validateBFFSecretReference("deepseek_api_key_reference", p.DeepSeekAPIKeyReference, environment); err != nil {
		return err
	}
	if p.TypeSafeAPIKeyReference != nil {
		if err := validateBFFSecretReference("typesafe_api_key_reference", *p.TypeSafeAPIKeyReference, environment); err != nil {
			return err
		}
	}
	if environment == "local" {
		if p.Local == nil || p.ConfigSecretResource != "" || p.GCPProject != "llm-wiki-cloud" ||
			p.Bucket != "llm-wiki-cloud-local" || p.FirestoreDatabaseID != "llm-wiki-cloud-local" {
			return errors.New("selected local BFF target identity is invalid")
		}
		if _, err := localcloud.Parse(p.Local.Scope); err != nil || p.Local.Scope == "" ||
			!absoluteFilePath(p.Local.WorkerPath) || !absoluteFilePath(p.Local.PipelineConfigPath) ||
			!absoluteFilePath(p.Local.PipelineBindingsPath) {
			return errors.New("selected local BFF paths or scope are invalid")
		}
	} else if p.Local != nil || !regexp.MustCompile(`^projects/[A-Za-z0-9.-]+/secrets/lwc-bff-config-(?:dev|prod)$`).MatchString(p.ConfigSecretResource) {
		return errors.New("selected cloud BFF secret resource is invalid")
	}
	if p.Query.StageConfigPath != "" {
		if p.Query.Legacy != nil || !validRepositoryQueryPath(p.Query.StageConfigPath) {
			return errors.New("selected BFF query authority is invalid")
		}
	} else if p.Query.Legacy == nil || !validBFFLegacyQuery(*p.Query.Legacy) {
		return errors.New("selected BFF legacy query config is invalid")
	}
	return nil
}

func validateBFFSecretReference(name string, ref bffSecretReference, environment string) error {
	switch ref.Source {
	case "secret-manager":
		if !secretResourcePattern.MatchString(ref.Resource) || ref.EnvName != "" {
			return fmt.Errorf("selected BFF %s is invalid", name)
		}
	case "environment":
		if environment != "local" || ref.EnvName == "" || ref.Resource != "" {
			return fmt.Errorf("selected BFF %s is invalid", name)
		}
	case "file":
		if name != "jwt_secret_reference" || environment != "local" || !absoluteFilePath(ref.Resource) || ref.EnvName != "" {
			return fmt.Errorf("selected BFF %s is invalid", name)
		}
	default:
		return fmt.Errorf("selected BFF %s source is invalid", name)
	}
	return nil
}

func absoluteURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == ""
}

func validateBFFURL(raw string, allowLocal bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if allowLocal && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return nil
	}
	return errors.New("invalid scheme")
}

func validateBFFPipelineURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "run.googleapis.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid URL")
	}
	return nil
}

func absoluteFilePath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, "\r\n\x00")
}

func validRepositoryQueryPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return !filepath.IsAbs(path) && clean == path && strings.HasPrefix(path, "apps/bff/configs/query/") &&
		!strings.Contains(path, "../") && strings.HasSuffix(path, ".json")
}

func validBFFLegacyQuery(q bffQueryLegacy) bool {
	return (q.QueryExpansionModel == "deepseek-flash" || q.QueryExpansionModel == "deepseek-v4-flash") &&
		q.QueryExpansionReasoning == "none" &&
		(q.AnswerSynthesisModel == "deepseek-flash" || q.AnswerSynthesisModel == "deepseek-v4-flash" || q.AnswerSynthesisModel == "deepseek-v4-pro") &&
		(q.AnswerSynthesisReasoning == "none" || q.AnswerSynthesisReasoning == "low" || q.AnswerSynthesisReasoning == "high" || q.AnswerSynthesisReasoning == "max") &&
		q.QuerySelectionLimit >= 1 && q.QuerySelectionLimit <= 1000 && q.QuerySelectionExplorationSlots >= 0 &&
		q.QuerySelectionExplorationSlots <= q.QuerySelectionLimit && q.QuerySelectionEvidenceThreshold >= 1 && q.QuerySelectionEvidenceThreshold <= 100 &&
		q.QueryExpansionKeywordsPerAttempt >= 1 && q.QueryExpansionKeywordsPerAttempt <= 100 &&
		q.QueryExpansionAttempts >= 1 && q.QueryExpansionAttempts <= 10 &&
		q.QueryMatchingRareKeywordMaxDocumentFrequency >= 1 && q.QueryMatchingRareKeywordMaxDocumentFrequency <= 1000
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func prepareBFFRuntimeConfig(ctx context.Context, root, output string, source bffSourceProjection, newReader readerFactory) error {
	query := source.Query
	if query.StageConfigPath != "" {
		repositoryPath := filepath.Join(root, filepath.FromSlash(query.StageConfigPath))
		if err := validateSealedQueryArtifact(repositoryPath); err != nil {
			return err
		}
		if source.Environment == "local" {
			query.StageConfigPath, _ = filepath.Abs(repositoryPath)
		} else {
			query.StageConfigPath = "/app/" + strings.TrimPrefix(filepath.ToSlash(source.Query.StageConfigPath), "apps/bff/")
		}
	}
	baseURL := normalizeBFFBaseURL(source.LLM.BaseURL)
	if !absoluteURL(baseURL) {
		return errors.New("selected BFF LLM base_url is invalid")
	}
	jwt, err := resolveBFFSecret(ctx, "jwt_secret", source.JWTSecretReference, newReader)
	if err != nil {
		return err
	}
	defer clear(jwt)
	deepseek, err := resolveBFFSecret(ctx, "deepseek_api_key", source.DeepSeekAPIKeyReference, newReader)
	if err != nil {
		return err
	}
	defer clear(deepseek)
	typesafe := []byte{}
	if source.ProfileRuntimeAudience != "" {
		if source.TypeSafeAPIKeyReference == nil {
			return errors.New("selected BFF typesafe_api_key_reference is required")
		}
		typesafe, err = resolveBFFSecret(ctx, "typesafe_api_key", *source.TypeSafeAPIKeyReference, newReader)
		if err != nil {
			return err
		}
		defer clear(typesafe)
	}
	runtimeFile := runtimeconfig.BFFFile{
		SchemaVersion: source.SchemaVersion, Environment: source.Environment, Target: source.Target,
		GCPProject: source.GCPProject, Bucket: source.Bucket, FirestoreDatabaseID: source.FirestoreDatabaseID,
		AuthServiceURL: source.AuthServiceURL, PipelineJobURL: source.PipelineJobURL,
		ExportJobURL: source.ExportJobURL, ExportSigningServiceAccount: source.ExportSigningServiceAccount,
		AllowedOrigins: source.AllowedOrigins, AllowedHosts: source.AllowedHosts,
		PipelineDailyLimit: source.PipelineDailyLimit, PipelineCooldownSeconds: source.PipelineCooldownSeconds,
		PipelineMinNewRaw: source.PipelineMinNewRaw, PipelineDemoUserIDs: source.PipelineDemoUserIDs,
		AuthSessionEnvironment: source.AuthSessionEnvironment, AuthSessionMigration: source.AuthSessionMigration,
		RegistrationEnabled: source.RegistrationEnabled, JWTSecret: string(jwt), DeepSeekAPIKey: string(deepseek),
		TypeSafeAPIKey: string(typesafe), ProfileRuntimeAudience: source.ProfileRuntimeAudience,
		ProfileRuntimeServiceAccount: source.ProfileRuntimeServiceAccount,
		LLM: runtimeconfig.BFFLLM{Provider: source.LLM.Provider, BaseURL: baseURL,
			RequestTimeoutSeconds: source.LLM.RequestTimeoutSeconds, Model: source.LLM.Model},
		Query: runtimeconfig.BFFQuery{StageConfigPath: query.StageConfigPath}, PortDefault: source.PortDefault,
	}
	if query.Legacy != nil {
		legacy := *query.Legacy
		runtimeFile.Query.Legacy = &runtimeconfig.BFFQueryLegacy{
			QueryExpansionModel:                          legacy.QueryExpansionModel,
			QueryExpansionReasoning:                      legacy.QueryExpansionReasoning,
			AnswerSynthesisModel:                         legacy.AnswerSynthesisModel,
			AnswerSynthesisReasoning:                     legacy.AnswerSynthesisReasoning,
			QuerySelectionLimit:                          legacy.QuerySelectionLimit,
			QuerySelectionExplorationSlots:               legacy.QuerySelectionExplorationSlots,
			QuerySelectionEvidenceThreshold:              legacy.QuerySelectionEvidenceThreshold,
			QueryExpansionKeywordsPerAttempt:             legacy.QueryExpansionKeywordsPerAttempt,
			QueryExpansionAttempts:                       legacy.QueryExpansionAttempts,
			QueryMatchingRareKeywordMaxDocumentFrequency: legacy.QueryMatchingRareKeywordMaxDocumentFrequency,
		}
	}
	if source.Local != nil {
		runtimeFile.Local = &runtimeconfig.BFFLocal{
			Scope: source.Local.Scope, WorkerPath: source.Local.WorkerPath,
			PipelineConfigPath:   source.Local.PipelineConfigPath,
			PipelineBindingsPath: source.Local.PipelineBindingsPath,
		}
	}
	data, err := json.MarshalIndent(runtimeFile, "", "  ")
	if err != nil {
		return errors.New("encode generated BFF config")
	}
	data = append(data, '\n')
	if len(data) == 0 || len(data) > maxBFFConfigBytes {
		clear(data)
		return errors.New("generated BFF config size is invalid")
	}
	if _, err := runtimeconfig.DecodeBFFFile(data); err != nil {
		clear(data)
		return fmt.Errorf("generated BFF config is invalid: %w", err)
	}
	tempDir, err := os.MkdirTemp(output, ".prepare-bff-")
	if err != nil {
		clear(data)
		return fmt.Errorf("create private BFF config staging directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		clear(data)
		return fmt.Errorf("secure BFF config staging directory: %w", err)
	}
	tempPath := filepath.Join(tempDir, "bff.json")
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		clear(data)
		return fmt.Errorf("write private BFF config: %w", err)
	}
	clear(data)
	if err := replaceFile(tempPath, filepath.Join(output, "bff.json")); err != nil {
		return fmt.Errorf("publish generated BFF config: %w", err)
	}
	fmt.Printf("prepared BFF config schema=2 environment=%s target=bff\n", source.Environment)
	return nil
}

func writeBFFDescriptor(output string, projection bffSourceProjection) error {
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return errors.New("encode BFF input projection")
	}
	path := filepath.Join(output, "bff-inputs.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write BFF input projection: %w", err)
	}
	fmt.Printf("prepared BFF input projection environment=%s target=bff\n", projection.Environment)
	return nil
}

func normalizeBFFBaseURL(raw string) string {
	base := strings.TrimRight(raw, "/")
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return strings.TrimRight(base, "/")
}

func validateSealedQueryArtifact(path string) error {
	config, raw, err := queryconfig.LoadFileCanonicalBytes(path)
	if err != nil {
		return errors.New("selected BFF sealed query artifact is invalid")
	}
	canonical, err := queryconfig.CanonicalJSON(config)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("selected BFF sealed query artifact is not canonical")
	}
	return nil
}

func resolveBFFSecret(ctx context.Context, key string, reference bffSecretReference, newReader readerFactory) ([]byte, error) {
	switch reference.Source {
	case "environment":
		value := os.Getenv(reference.EnvName)
		if value == "" && reference.EnvName == "LLM_API_KEY" {
			value = os.Getenv("DEEPSEEK_API_KEY")
		}
		if value == "" && reference.EnvName == "TYPESAFE_JEV_API_KEY" {
			value = os.Getenv("TYPESAFE_API_KEY")
		}
		if value == "" {
			return nil, fmt.Errorf("required local BFF %s is unavailable", key)
		}
		return []byte(value), nil
	case "file":
		value, err := os.ReadFile(reference.Resource)
		if err != nil {
			return nil, errors.New("local BFF signing key file is unavailable")
		}
		secret := strings.TrimSpace(string(value))
		decoded, decodeErr := hex.DecodeString(secret)
		clear(value)
		if decodeErr != nil || len(decoded) != 32 {
			clear(decoded)
			return nil, errors.New("local BFF signing key file is invalid")
		}
		clear(decoded)
		return []byte(secret), nil
	case "secret-manager":
		value, _, err := resolveBinding(ctx, secretBinding{
			Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: reference.Resource,
		}, newReader)
		if err != nil {
			clear(value)
			return nil, fmt.Errorf("resolve BFF %s failed: %w", key, err)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("BFF %s source is invalid", key)
	}
}

func resolveBinding(ctx context.Context, binding secretBinding, newReader readerFactory) ([]byte, string, error) {
	switch binding.Source {
	case "environment":
		name := binding.EnvName
		if name == "" {
			return nil, "", errors.New("local Pipeline secret environment name is empty")
		}
		value := os.Getenv(name)
		if value == "" && name == "LLM_API_KEY" {
			value = os.Getenv("DEEPSEEK_API_KEY")
		}
		if value == "" {
			return nil, "", fmt.Errorf("required local Pipeline secret is missing from %s", name)
		}
		return []byte(value), "", nil
	case "secret-manager":
		if !secretResourcePattern.MatchString(binding.Resource) {
			return nil, "", errors.New("Pipeline Secret Manager reference must be a full secret version resource path")
		}
		reader, err := newReader(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("initialize Secret Manager resolver failed: %s", googleAPIErrorSummary(err))
		}
		resolvedResource, value, err := reader.Access(ctx, binding.Resource)
		if err != nil {
			clear(value)
			return nil, "", fmt.Errorf("resolve Secret Manager version %s: access failed: %s", binding.Resource, googleAPIErrorSummary(err))
		}
		if len(value) == 0 {
			return nil, "", fmt.Errorf("resolve Secret Manager version %s: payload is empty", binding.Resource)
		}
		if !resolvedSecretResourceMatches(binding.Resource, resolvedResource) {
			requestedParts := strings.Split(binding.Resource, "/")
			if !resolvedSecretVersionMatches(binding.Resource, resolvedResource) {
				clear(value)
				return nil, "", fmt.Errorf("resolve Secret Manager version %s: response did not identify the selected numeric version", binding.Resource)
			}
			identityReader, ok := reader.(projectIdentityReader)
			if !ok {
				clear(value)
				return nil, "", fmt.Errorf("resolve Secret Manager version %s: response did not identify the selected project and numeric version", binding.Resource)
			}
			identity, err := identityReader.ProjectIdentity(ctx, requestedParts[1])
			if err != nil {
				clear(value)
				if errors.Is(err, errProjectIdentityMismatch) || errors.Is(err, errProjectIdentityUnavailable) {
					return nil, "", fmt.Errorf("resolve Secret Manager version %s: %s", binding.Resource, err)
				}
				return nil, "", fmt.Errorf("resolve Secret Manager version %s: project identity lookup failed: %s", binding.Resource, googleAPIErrorSummary(err))
			}
			if !resolvedSecretResourceMatchesProjectIdentity(binding.Resource, resolvedResource, identity) {
				clear(value)
				return nil, "", fmt.Errorf("resolve Secret Manager version %s: response did not identify the selected project and numeric version", binding.Resource)
			}
			resolvedParts := strings.Split(resolvedResource, "/")
			resolvedResource = strings.Join([]string{
				"projects", requestedParts[1], "secrets", requestedParts[3], "versions", resolvedParts[5],
			}, "/")
		}
		return value, resolvedResource, nil
	default:
		return nil, "", errors.New("Pipeline secret binding source is unsupported")
	}
}

func googleAPIErrorSummary(err error) string {
	var apiError *googleapi.Error
	if errors.As(err, &apiError) && apiError != nil {
		message, truncated := boundedSecretManagerMessage(apiError.Message)
		return fmt.Sprintf("Google API HTTP %d: %s (message_truncated=%t)", apiError.Code, message, truncated)
	}
	return fmt.Sprintf("SDK error type %T", err)
}

func boundedSecretManagerMessage(message string) (string, bool) {
	const maxMessageBytes = 512
	if len(message) <= maxMessageBytes {
		return message, false
	}
	end := maxMessageBytes
	for end > 0 && !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end], true
}

func resolvedSecretResourceMatches(requested, resolved string) bool {
	if !resolvedSecretVersionMatches(requested, resolved) {
		return false
	}
	requestedParts, resolvedParts := strings.Split(requested, "/"), strings.Split(resolved, "/")
	return requestedParts[1] == resolvedParts[1]
}

func resolvedSecretVersionMatches(requested, resolved string) bool {
	if !secretResourcePattern.MatchString(requested) || !resolvedSecretResourcePattern.MatchString(resolved) {
		return false
	}
	requestedParts, resolvedParts := strings.Split(requested, "/"), strings.Split(resolved, "/")
	return requestedParts[3] == resolvedParts[3] &&
		(requestedParts[5] == "latest" || requestedParts[5] == resolvedParts[5])
}

func projectIdentityMatchesRequest(requestedProject string, identity projectIdentity) bool {
	return identity.ProjectID != "" && projectNumberPattern.MatchString(identity.ProjectNumber) &&
		(requestedProject == identity.ProjectID || requestedProject == identity.ProjectNumber)
}

func resolvedSecretResourceMatchesProjectIdentity(requested, resolved string, identity projectIdentity) bool {
	if !resolvedSecretVersionMatches(requested, resolved) {
		return false
	}
	requestedProject := strings.Split(requested, "/")[1]
	resolvedProject := strings.Split(resolved, "/")[1]
	return projectIdentityMatchesRequest(requestedProject, identity) &&
		(resolvedProject == identity.ProjectID || resolvedProject == identity.ProjectNumber)
}

func bindResolvedSecretReference(outputDir string, binding secretBinding) error {
	if binding.Source != "secret-manager" {
		return nil
	}
	path := filepath.Join(outputDir, "pipeline.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(data, &public); err != nil {
		return err
	}
	var rendered secretBinding
	if err := json.Unmarshal(public["secret"], &rendered); err != nil {
		return err
	}
	if rendered.Target != binding.Target || !resolvedSecretResourceMatches(rendered.Resource, binding.Resource) {
		return errors.New("rendered and prepared secret references do not match")
	}
	secretBytes, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	public["secret"] = secretBytes
	data, err = json.MarshalIndent(public, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func repositoryRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("LWC_REPOSITORY_ROOT"))
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	for _, path := range []string{filepath.Join(root, "deploy/cac/ssot.pkl"), filepath.Join(root, "deploy/cac/synto.pkl")} {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("Pipeline config source is unavailable: %s", path)
		}
	}
	return root, nil
}

func pklBinary() string {
	if value := strings.TrimSpace(os.Getenv("PKL_BIN")); value != "" {
		return value
	}
	return "pkl"
}

func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func replaceFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	defer clear(data)
	mode := os.FileMode(0o644)
	if filepath.Base(destination) == "private-bindings.json" || filepath.Base(destination) == "bff.json" {
		mode = 0o600
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".pipeline-config-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, destination)
}
