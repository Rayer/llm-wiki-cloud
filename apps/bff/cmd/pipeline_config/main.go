package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/secretmanager/v1"
)

const maxConfigBytes = 1 << 20

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
		fmt.Fprintln(os.Stderr, "usage: pipeline-config prepare --environment local|dev|prod --output DIR")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("prepare", flag.ExitOnError)
	environment := flags.String("environment", "", "selected target environment")
	output := flags.String("output", "", "generated output directory")
	_ = flags.Parse(os.Args[2:])
	if err := runPrepare(context.Background(), *environment, *output, googleReader); err != nil {
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
	if environment != "local" && environment != "dev" && environment != "prod" {
		return errors.New("environment must be local, dev, or prod")
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("output directory is required")
	}
	timeoutSeconds, err := runTimeoutFromEnvironment()
	if err != nil {
		return err
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	cacheDir := strings.TrimSpace(os.Getenv("PKL_CACHE_DIR"))
	properties := []string{"--property", "environment=" + environment, "--property", "runTimeoutSeconds=" + strconv.Itoa(timeoutSeconds)}
	localResource := strings.TrimSpace(os.Getenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE"))
	if localResource != "" {
		properties = append(properties, "--property", "localSecretVersionResource="+localResource)
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
	if err := json.Unmarshal(ssotBytes, &config); err != nil {
		return fmt.Errorf("decode selected Pipeline SSOT: %w", err)
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
	mode := os.FileMode(0o644)
	if filepath.Base(destination) == "private-bindings.json" {
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
