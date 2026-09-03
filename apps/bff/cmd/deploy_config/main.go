package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"gopkg.in/yaml.v3"
)

var (
	allowedEnvironments = map[string]struct{}{"development": {}, "production": {}}
	allowedComponents   = []string{"auth", "bff", "worker", "frontend"}
	componentSet        = map[string]struct{}{"auth": {}, "bff": {}, "worker": {}, "frontend": {}}
	secretRefPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	secretValuePattern  = regexp.MustCompile(`(?i)(?:github_pat_|ghp_|xox[baprs]-|-----begin|sk-[A-Za-z0-9])`)
)

type EnvironmentConfig struct {
	GCP      GCPConfig      `yaml:"gcp"`
	Auth     AuthConfig     `yaml:"auth"`
	BFF      BFFConfig      `yaml:"bff"`
	Worker   WorkerConfig   `yaml:"worker"`
	Frontend FrontendConfig `yaml:"frontend"`
}

type GCPConfig struct {
	ProjectID        string `yaml:"project_id" json:"project_id"`
	Region           string `yaml:"region" json:"region"`
	ArtifactRegistry string `yaml:"artifact_registry" json:"artifact_registry"`
}

type AuthConfig struct {
	ServiceName           string               `yaml:"service_name" json:"service_name"`
	RuntimeServiceAccount string               `yaml:"runtime_service_account" json:"runtime_service_account"`
	Network               string               `yaml:"network" json:"network"`
	Subnet                string               `yaml:"subnet" json:"subnet"`
	VPCEgress             string               `yaml:"vpc_egress" json:"vpc_egress"`
	Ingress               string               `yaml:"ingress" json:"ingress"`
	MaxInstances          int                  `yaml:"max_instances" json:"max_instances"`
	FirestoreDatabaseID   string               `yaml:"firestore_database_id" json:"firestore_database_id"`
	PublicDomain          string               `yaml:"public_domain" json:"public_domain"`
	AllowedHosts          []string             `yaml:"allowed_hosts" json:"allowed_hosts"`
	AllowedOrigins        []string             `yaml:"allowed_origins" json:"allowed_origins"`
	SecretReferences      AuthSecretReferences `yaml:"secret_references" json:"secret_references"`
}

type AuthSecretReferences struct {
	JWT string `yaml:"jwt" json:"jwt"`
}

type BFFConfig struct {
	ServiceName           string                  `yaml:"service_name" json:"service_name"`
	RuntimeServiceAccount string                  `yaml:"runtime_service_account" json:"runtime_service_account"`
	Network               string                  `yaml:"network" json:"network"`
	Subnet                string                  `yaml:"subnet" json:"subnet"`
	VPCEgress             string                  `yaml:"vpc_egress" json:"vpc_egress"`
	Ingress               string                  `yaml:"ingress" json:"ingress"`
	MaxInstances          int                     `yaml:"max_instances" json:"max_instances"`
	Bucket                string                  `yaml:"bucket" json:"bucket"`
	FirestoreDatabaseID   string                  `yaml:"firestore_database_id" json:"firestore_database_id"`
	PipelineJobName       string                  `yaml:"pipeline_job_name" json:"pipeline_job_name"`
	PipelineJobLocation   string                  `yaml:"pipeline_job_location" json:"pipeline_job_location"`
	PipelineJobURL        string                  `yaml:"pipeline_job_url" json:"pipeline_job_url"`
	AuthServiceURL        string                  `yaml:"auth_service_url" json:"auth_service_url"`
	AllowedOrigins        []string                `yaml:"allowed_origins" json:"allowed_origins"`
	DevJWT                *bool                   `yaml:"dev_jwt" json:"dev_jwt"`
	QueryConfig           string                  `yaml:"query_config" json:"query_config"`
	SecretReferences      RuntimeSecretReferences `yaml:"secret_references" json:"secret_references"`
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
	JWT            string `yaml:"jwt" json:"jwt"`
	DeepSeekAPIKey string `yaml:"deepseek_api_key" json:"deepseek_api_key"`
}

type WorkerSecretReferences struct {
	DeepSeekAPIKey string `yaml:"deepseek_api_key" json:"deepseek_api_key"`
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
	flag.Parse()

	if *environment == "" || *components == "" {
		fail("environment and components are required")
	}
	normalized, err := Load(*environment, *configPath, *components)
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
	if _, ok := allowedEnvironments[environment]; !ok {
		return Normalized{}, fmt.Errorf("environment %q is not allowlisted", environment)
	}
	selected, err := parseComponents(components)
	if err != nil {
		return Normalized{}, err
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
	if err := validateConfigForEnvironment(environment, config); err != nil {
		return Normalized{}, err
	}
	query, err := loadQueryConfig(repoRoot, config.BFF.QueryConfig)
	if err != nil {
		return Normalized{}, err
	}

	result := Normalized{
		Environment: environment,
		ConfigPath:  filepath.ToSlash(filepath.Join("deploy", "environments", environment+".yaml")),
		Selected:    selected,
		GCP:         config.GCP, Auth: config.Auth, BFF: config.BFF,
		Worker: config.Worker, Frontend: config.Frontend, QueryConfig: query,
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
	return validateConfigForEnvironment("", config)
}

func validateConfigForEnvironment(environment string, config EnvironmentConfig) error {
	for name, value := range map[string]string{
		"gcp.project_id": config.GCP.ProjectID, "gcp.region": config.GCP.Region,
		"gcp.artifact_registry": config.GCP.ArtifactRegistry,
		"auth.service_name":     config.Auth.ServiceName, "auth.runtime_service_account": config.Auth.RuntimeServiceAccount,
		"auth.network":          config.Auth.Network, "auth.subnet": config.Auth.Subnet, "auth.vpc_egress": config.Auth.VPCEgress, "auth.ingress": config.Auth.Ingress,
		"auth.firestore_database_id": config.Auth.FirestoreDatabaseID, "auth.public_domain": config.Auth.PublicDomain,
		"auth.secret_references.jwt": config.Auth.SecretReferences.JWT,
		"bff.service_name":           config.BFF.ServiceName, "bff.runtime_service_account": config.BFF.RuntimeServiceAccount,
		"bff.network":                config.BFF.Network, "bff.subnet": config.BFF.Subnet, "bff.vpc_egress": config.BFF.VPCEgress, "bff.ingress": config.BFF.Ingress,
		"bff.bucket": config.BFF.Bucket, "bff.firestore_database_id": config.BFF.FirestoreDatabaseID,
		"bff.pipeline_job_name": config.BFF.PipelineJobName, "bff.pipeline_job_location": config.BFF.PipelineJobLocation,
		"bff.pipeline_job_url": config.BFF.PipelineJobURL, "bff.auth_service_url": config.BFF.AuthServiceURL,
		"bff.query_config": config.BFF.QueryConfig, "bff.secret_references.jwt": config.BFF.SecretReferences.JWT,
		"bff.secret_references.deepseek_api_key": config.BFF.SecretReferences.DeepSeekAPIKey,
		"worker.job_name":                        config.Worker.JobName, "worker.runtime_service_account": config.Worker.RuntimeServiceAccount,
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
	if config.GCP.ProjectID != "llm-wiki-cloud" || config.GCP.Region != "asia-east1" {
		return errors.New("gcp identity is not the reviewed deployment target")
	}
	if !strings.HasPrefix(config.GCP.ArtifactRegistry, config.GCP.Region+"-docker.pkg.dev/") {
		return errors.New("artifact registry must be regional")
	}
	if config.Auth.MaxInstances != 1 || config.BFF.MaxInstances != 1 {
		return errors.New("service max_instances must be exactly 1")
	}
	if config.Auth.Network != "default" || config.Auth.Subnet != "default" || config.Auth.VPCEgress != "private-ranges-only" || config.Auth.Ingress != "all" ||
		config.BFF.Network != "default" || config.BFF.Subnet != "default" || config.BFF.VPCEgress != "private-ranges-only" || config.BFF.Ingress != "all" {
		return errors.New("service network configuration is not the reviewed Cloud Run definition")
	}
	if err := validateStringList("auth.allowed_hosts", config.Auth.AllowedHosts); err != nil {
		return err
	}
	if err := validateStringList("auth.allowed_origins", config.Auth.AllowedOrigins); err != nil {
		return err
	}
	if err := validateStringList("bff.allowed_origins", config.BFF.AllowedOrigins); err != nil {
		return err
	}
	if err := validateStringList("frontend.stable_aliases", config.Frontend.StableAliases); err != nil {
		return err
	}
	if len(config.Worker.Args) == 0 {
		return errors.New("worker.args must not be empty")
	}
	if config.BFF.DevJWT == nil {
		return errors.New("missing required field bff.dev_jwt")
	}
	if *config.BFF.DevJWT {
		return errors.New("bff.dev_jwt must be false for deployed environments")
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
	for name, value := range map[string]string{
		"auth.jwt":                config.Auth.SecretReferences.JWT,
		"bff.jwt":                 config.BFF.SecretReferences.JWT,
		"bff.deepseek_api_key":    config.BFF.SecretReferences.DeepSeekAPIKey,
		"worker.deepseek_api_key": config.Worker.SecretReferences.DeepSeekAPIKey,
	} {
		if !secretRefPattern.MatchString(value) {
			return fmt.Errorf("secret reference %s is invalid", name)
		}
	}
	if environment != "" {
		jwt := "jwt-secret-dev"
		if environment == "production" {
			jwt = "jwt-secret-prod"
		}
		if config.Auth.SecretReferences.JWT != jwt || config.BFF.SecretReferences.JWT != jwt || config.BFF.SecretReferences.DeepSeekAPIKey != "deepseek-apikey" || config.Worker.SecretReferences.DeepSeekAPIKey != "deepseek-apikey" {
			return errors.New("secret references are not the reviewed environment bindings")
		}
	}
	return nil
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
			components[name] = map[string]any{"service_name": config.Auth.ServiceName, "runtime_service_account": config.Auth.RuntimeServiceAccount, "network": config.Auth.Network, "subnet": config.Auth.Subnet, "vpc_egress": config.Auth.VPCEgress, "ingress": config.Auth.Ingress, "max_instances": config.Auth.MaxInstances, "public_domain": config.Auth.PublicDomain, "firestore_database_id": config.Auth.FirestoreDatabaseID, "allowed_hosts": config.Auth.AllowedHosts, "allowed_origins": config.Auth.AllowedOrigins, "dev_jwt": false, "secret_references": map[string]any{"jwt": config.Auth.SecretReferences.JWT}}
		case "bff":
			components[name] = map[string]any{"service_name": config.BFF.ServiceName, "runtime_service_account": config.BFF.RuntimeServiceAccount, "network": config.BFF.Network, "subnet": config.BFF.Subnet, "vpc_egress": config.BFF.VPCEgress, "ingress": config.BFF.Ingress, "max_instances": config.BFF.MaxInstances, "bucket": config.BFF.Bucket, "firestore_database_id": config.BFF.FirestoreDatabaseID, "pipeline_job_name": config.BFF.PipelineJobName, "pipeline_job_location": config.BFF.PipelineJobLocation, "pipeline_job_url": config.BFF.PipelineJobURL, "auth_service_url": config.BFF.AuthServiceURL, "allowed_origins": config.BFF.AllowedOrigins, "dev_jwt": false, "query_config": query, "secret_references": map[string]any{"jwt": config.BFF.SecretReferences.JWT, "deepseek_api_key": config.BFF.SecretReferences.DeepSeekAPIKey}}
		case "worker":
			components[name] = map[string]any{"job_name": config.Worker.JobName, "runtime_service_account": config.Worker.RuntimeServiceAccount, "bucket": config.Worker.Bucket, "location": config.Worker.Location, "args": config.Worker.Args, "secret_references": map[string]any{"deepseek_api_key": config.Worker.SecretReferences.DeepSeekAPIKey}}
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
