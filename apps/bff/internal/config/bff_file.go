package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

const MaxBFFConfigBytes = 64 << 10

type BFFFile struct {
	SchemaVersion       int    `json:"schema_version"`
	Environment         string `json:"environment"`
	Target              string `json:"target"`
	GCPProject          string `json:"gcp_project"`
	Bucket              string `json:"bucket"`
	FirestoreDatabaseID string `json:"firestore_database_id"`
	AuthServiceURL      string `json:"auth_service_url"`
	MCPResourceOrigin   string `json:"mcp_resource_origin,omitempty"`
	// MCP is optional only for historical schema2 files. Its absence preserves
	// the former no-instructions, legacy-description, zero-TTL behavior.
	MCP                          *BFFMCP             `json:"mcp,omitempty"`
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
	JWTSecret                    string              `json:"jwt_secret,omitempty"`
	AuthConsumer                 *BFFAuthConsumer    `json:"auth_consumer,omitempty"`
	DeepSeekAPIKey               string              `json:"deepseek_api_key"`
	TypeSafeAPIKey               string              `json:"typesafe_api_key"`
	ProfileRuntimeAudience       string              `json:"profile_runtime_audience"`
	ProfileRuntimeServiceAccount string              `json:"profile_runtime_service_account"`
	PipelineSchedule             BFFPipelineSchedule `json:"pipeline_schedule"`
	LLM                          BFFLLM              `json:"llm"`
	Query                        BFFQuery            `json:"query"`
	Local                        *BFFLocal           `json:"local"`
	PortDefault                  int                 `json:"port_default"`
}

type BFFAuthConsumer struct {
	SchemaVersion         int      `json:"schema_version"`
	Environment           string   `json:"environment"`
	AuthURL               string   `json:"auth_url"`
	Issuer                string   `json:"issuer"`
	RuntimeServiceAccount string   `json:"runtime_service_account"`
	VerifierAudience      string   `json:"verifier_audience"`
	Resource              string   `json:"resource"`
	VerifierPath          string   `json:"verifier_path"`
	AccountCheckPath      string   `json:"account_check_path"`
	CatalogPath           string   `json:"catalog_path"`
	AuthorityModes        []string `json:"authority_modes"`
	TimeoutSeconds        int      `json:"timeout_seconds"`
}

type BFFLLM struct {
	Provider              string `json:"provider"`
	BaseURL               string `json:"base_url"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	Model                 string `json:"model"`
}

type BFFQuery struct {
	StageConfigPath string          `json:"stage_config_path"`
	Legacy          *BFFQueryLegacy `json:"legacy"`
}

type BFFMCP struct {
	Instructions     string                 `json:"instructions"`
	ToolDescriptions BFFMCPToolDescriptions `json:"tool_descriptions"`
	DiscoveryTTLMS   int                    `json:"discovery_ttl_ms"`
	ToolsListTTLMS   int                    `json:"tools_list_ttl_ms"`
}

type BFFMCPToolDescriptions struct {
	QueryProject string `json:"query_project"`
	AddRaw       string `json:"add_raw"`
	ImportURL    string `json:"import_url"`
}

const (
	maxMCPInstructionsBytes    = 12_000
	maxMCPToolDescriptionBytes = 6_000
	maxMCPMetadataTTLMS        = 3_600_000
)

// ValidateBFFMCP checks the non-secret, CaC-owned MCP metadata without
// including configured text in validation errors.
func ValidateBFFMCP(mcp BFFMCP) error {
	if strings.TrimSpace(mcp.Instructions) == "" || len(mcp.Instructions) > maxMCPInstructionsBytes {
		return errors.New("BFF config mcp.instructions is invalid")
	}
	for _, field := range []struct{ name, value string }{
		{"query_project", mcp.ToolDescriptions.QueryProject},
		{"add_raw", mcp.ToolDescriptions.AddRaw},
		{"import_url", mcp.ToolDescriptions.ImportURL},
	} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > maxMCPToolDescriptionBytes {
			return fmt.Errorf("BFF config mcp.tool_descriptions.%s is invalid", field.name)
		}
	}
	if mcp.DiscoveryTTLMS < 0 || mcp.DiscoveryTTLMS > maxMCPMetadataTTLMS {
		return errors.New("BFF config mcp.discovery_ttl_ms is out of range")
	}
	if mcp.ToolsListTTLMS < 0 || mcp.ToolsListTTLMS > maxMCPMetadataTTLMS {
		return errors.New("BFF config mcp.tools_list_ttl_ms is out of range")
	}
	return nil
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

type BFFPipelineSchedule struct {
	Cron           string `json:"cron"`
	Timezone       string `json:"timezone"`
	Audience       string `json:"audience"`
	ServiceAccount string `json:"service_account"`
}

type BFFLocal struct {
	Scope                string `json:"scope"`
	WorkerPath           string `json:"worker_path"`
	PipelineConfigPath   string `json:"pipeline_config_path"`
	PipelineBindingsPath string `json:"pipeline_bindings_path"`
}

var bffPortPattern = regexp.MustCompile(`^[0-9]{1,5}$`)
var bffDemoUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var bffRequiredKeys = []string{
	"schema_version", "environment", "target", "gcp_project", "bucket", "firestore_database_id",
	"auth_service_url", "pipeline_job_url", "export_job_url", "export_signing_service_account",
	"allowed_origins", "allowed_hosts", "pipeline_daily_limit", "pipeline_cooldown_seconds",
	"pipeline_min_new_raw", "pipeline_demo_user_ids", "auth_session_environment", "auth_session_migration",
	"registration_enabled", "jwt_secret", "deepseek_api_key", "typesafe_api_key", "profile_runtime_audience",
	"profile_runtime_service_account", "pipeline_schedule", "llm", "query", "local", "port_default",
}

func bffRequiredKeysForVersion(version int) []string {
	keys := append([]string(nil), bffRequiredKeys...)
	if version == 3 {
		for index, key := range keys {
			if key == "jwt_secret" {
				keys = append(keys[:index], keys[index+1:]...)
				break
			}
		}
		keys = append(keys, "auth_consumer")
	}
	return keys
}

// LoadBFFFile reads the required mounted BFF configuration. The file locator is
// the only bootstrap environment input; PORT is the platform listener override.
func LoadBFFFile(path string) (Config, error) {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return Config{}, errors.New("BFF config path must be an absolute file path")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("BFF config file is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, errors.New("BFF config path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBFFConfigBytes+1))
	if err != nil || len(data) > MaxBFFConfigBytes {
		return Config{}, errors.New("BFF config file size is invalid")
	}
	file, err := DecodeBFFFile(data)
	clear(data)
	if err != nil {
		return Config{}, err
	}
	port := fmt.Sprint(file.PortDefault)
	if override, ok := os.LookupEnv("PORT"); ok && strings.TrimSpace(override) != "" {
		port = strings.TrimSpace(override)
		if !bffPortPattern.MatchString(port) {
			return Config{}, errors.New("invalid PORT")
		}
		portNumber, _ := strconv.Atoi(port)
		if portNumber < 1 || portNumber > 65535 {
			return Config{}, errors.New("invalid PORT")
		}
	}
	cfg := configFromBFFFile(file, port)
	if file.SchemaVersion == 2 && file.Environment != "local" {
		consumer, _, err := LegacyLWCBridgeConsumer(file.Environment, file.AuthServiceURL, file.MCPResourceOrigin)
		if err != nil {
			return Config{}, err
		}
		cfg.BFFAuthConsumer = &consumer
		cfg.BFFManagementBridge = true
		if cfg.MCPResourceOrigin == "" {
			cfg.MCPResourceOrigin = consumer.Resource
		}
	}
	return cfg, nil
}

// DecodeBFFFile validates the complete schema before projecting it into the
// existing application config type. Errors intentionally identify keys only.
func DecodeBFFFile(data []byte) (BFFFile, error) {
	if len(data) == 0 || len(data) > MaxBFFConfigBytes {
		return BFFFile{}, errors.New("BFF config size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return BFFFile{}, errors.New("BFF config contains malformed or duplicate JSON keys")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return BFFFile{}, errors.New("BFF config document is malformed")
	}
	version := 0
	_ = json.Unmarshal(fields["schema_version"], &version)
	if err := requireJSONKeysWithOptional(fields, bffRequiredKeysForVersion(version), []string{"mcp_resource_origin", "mcp"}); err != nil {
		return BFFFile{}, err
	}
	if err := validateBFFNestedShapes(fields); err != nil {
		return BFFFile{}, err
	}
	if err := validateBFFJSONTypes(fields, version); err != nil {
		return BFFFile{}, err
	}
	var file BFFFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return BFFFile{}, errors.New("BFF config fields have invalid types or names")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return BFFFile{}, errors.New("BFF config has trailing data")
	}
	if err := validateBFFFile(file); err != nil {
		return BFFFile{}, err
	}
	return file, nil
}

func validateBFFNestedShapes(fields map[string]json.RawMessage) error {
	var llmFields, queryFields, scheduleFields map[string]json.RawMessage
	if json.Unmarshal(fields["llm"], &llmFields) != nil || json.Unmarshal(fields["query"], &queryFields) != nil ||
		json.Unmarshal(fields["pipeline_schedule"], &scheduleFields) != nil || llmFields == nil || queryFields == nil || scheduleFields == nil {
		return errors.New("BFF config llm, query, or pipeline_schedule object is invalid")
	}
	if err := requireJSONKeys(scheduleFields, []string{"cron", "timezone", "audience", "service_account"}); err != nil {
		return errors.New("BFF config pipeline_schedule object has missing or unknown keys")
	}
	if err := requireJSONKeys(llmFields, []string{"provider", "base_url", "request_timeout_seconds", "model"}); err != nil {
		return errors.New("BFF config llm object has missing or unknown keys")
	}
	if err := requireJSONKeys(queryFields, []string{"stage_config_path", "legacy"}); err != nil {
		return errors.New("BFF config query object has missing or unknown keys")
	}
	legacyRaw := bytes.TrimSpace(queryFields["legacy"])
	if !bytes.Equal(legacyRaw, []byte("null")) {
		var legacyFields map[string]json.RawMessage
		if json.Unmarshal(legacyRaw, &legacyFields) != nil || legacyFields == nil {
			return errors.New("BFF config legacy query object is invalid")
		}
		if err := requireJSONKeys(legacyFields, []string{
			"query_expansion_model", "query_expansion_reasoning", "answer_synthesis_model", "answer_synthesis_reasoning",
			"query_selection_limit", "query_selection_exploration_slots", "query_selection_evidence_threshold",
			"query_expansion_keywords_per_attempt", "query_expansion_attempts", "query_matching_rare_keyword_max_document_frequency",
		}); err != nil {
			return errors.New("BFF config legacy query object has missing or unknown keys")
		}
	}
	if raw, ok := fields["mcp"]; ok {
		var mcpFields map[string]json.RawMessage
		if json.Unmarshal(raw, &mcpFields) != nil || mcpFields == nil {
			return errors.New("BFF config mcp object is invalid")
		}
		if err := requireJSONKeys(mcpFields, []string{"instructions", "tool_descriptions", "discovery_ttl_ms", "tools_list_ttl_ms"}); err != nil {
			return errors.New("BFF config mcp object has missing or unknown keys")
		}
		var descriptions map[string]json.RawMessage
		if json.Unmarshal(mcpFields["tool_descriptions"], &descriptions) != nil || descriptions == nil {
			return errors.New("BFF config mcp.tool_descriptions object is invalid")
		}
		if err := requireJSONKeys(descriptions, []string{"query_project", "add_raw", "import_url"}); err != nil {
			return errors.New("BFF config mcp.tool_descriptions object has missing or unknown keys")
		}
	}
	localRaw := bytes.TrimSpace(fields["local"])
	if !bytes.Equal(localRaw, []byte("null")) {
		var localFields map[string]json.RawMessage
		if json.Unmarshal(localRaw, &localFields) != nil || localFields == nil {
			return errors.New("BFF config local object is invalid")
		}
		if err := requireJSONKeys(localFields, []string{"scope", "worker_path", "pipeline_config_path", "pipeline_bindings_path"}); err != nil {
			return errors.New("BFF config local object has missing or unknown keys")
		}
	}
	return nil
}

func validateBFFJSONTypes(fields map[string]json.RawMessage, version int) error {
	for _, field := range []struct {
		name     string
		kind     string
		nullable bool
	}{
		{"schema_version", "integer", false},
		{"environment", "string", false},
		{"target", "string", false},
		{"gcp_project", "string", false},
		{"bucket", "string", false},
		{"firestore_database_id", "string", false},
		{"auth_service_url", "string", false},
		{"pipeline_job_url", "string", false},
		{"export_job_url", "string", false},
		{"export_signing_service_account", "string", false},
		{"allowed_origins", "string_array", false},
		{"allowed_hosts", "string_array", false},
		{"pipeline_daily_limit", "integer", false},
		{"pipeline_cooldown_seconds", "integer", false},
		{"pipeline_min_new_raw", "integer", false},
		{"pipeline_demo_user_ids", "string_array", false},
		{"auth_session_environment", "string", false},
		{"auth_session_migration", "string", false},
		{"registration_enabled", "boolean", true},
		{"deepseek_api_key", "string", false},
		{"typesafe_api_key", "string", false},
		{"profile_runtime_audience", "string", false},
		{"profile_runtime_service_account", "string", false},
		{"pipeline_schedule", "object", false},
		{"llm", "object", false},
		{"query", "object", false},
		{"local", "object", true},
		{"port_default", "integer", false},
	} {
		if field.name == "jwt_secret" && version == 3 {
			continue
		}
		if err := requireBFFJSONType(field.name, fields[field.name], field.kind, field.nullable); err != nil {
			return err
		}
	}
	if version == 2 {
		if raw, ok := fields["auth_consumer"]; ok && !isBFFJSONNull(raw) {
			return errors.New("BFF config auth_consumer is only valid in schema 3")
		}
	}
	if version == 3 {
		if err := requireBFFJSONType("auth_consumer", fields["auth_consumer"], "object", false); err != nil {
			return err
		}
		var consumer map[string]json.RawMessage
		if json.Unmarshal(fields["auth_consumer"], &consumer) != nil || consumer == nil ||
			requireJSONKeys(consumer, []string{"schema_version", "environment", "auth_url", "issuer", "runtime_service_account", "verifier_audience", "resource", "verifier_path", "account_check_path", "catalog_path", "authority_modes", "timeout_seconds"}) != nil {
			return errors.New("BFF config auth_consumer object has missing or unknown keys")
		}
		for _, field := range []struct{ name, kind string }{
			{"schema_version", "integer"}, {"environment", "string"}, {"auth_url", "string"}, {"issuer", "string"},
			{"runtime_service_account", "string"}, {"verifier_audience", "string"}, {"resource", "string"},
			{"verifier_path", "string"}, {"account_check_path", "string"}, {"catalog_path", "string"}, {"authority_modes", "string_array"}, {"timeout_seconds", "integer"},
		} {
			if err := requireBFFJSONType("auth_consumer."+field.name, consumer[field.name], field.kind, false); err != nil {
				return err
			}
		}
	}
	if raw, ok := fields["mcp_resource_origin"]; ok {
		if err := requireBFFJSONType("mcp_resource_origin", raw, "string", true); err != nil {
			return err
		}
	}
	if raw, ok := fields["mcp"]; ok {
		if err := requireBFFJSONType("mcp", raw, "object", false); err != nil {
			return err
		}
		var mcpFields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &mcpFields); err != nil {
			return errors.New("BFF config mcp object has invalid types")
		}
		for _, field := range []struct{ name, kind string }{
			{"instructions", "string"}, {"discovery_ttl_ms", "integer"}, {"tools_list_ttl_ms", "integer"},
		} {
			if err := requireBFFJSONType("mcp."+field.name, mcpFields[field.name], field.kind, false); err != nil {
				return err
			}
		}
		var descriptions map[string]json.RawMessage
		if err := json.Unmarshal(mcpFields["tool_descriptions"], &descriptions); err != nil || descriptions == nil {
			return errors.New("BFF config mcp.tool_descriptions object has invalid types")
		}
		for _, key := range []string{"query_project", "add_raw", "import_url"} {
			if err := requireBFFJSONType("mcp.tool_descriptions."+key, descriptions[key], "string", false); err != nil {
				return err
			}
		}
	}
	var scheduleFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["pipeline_schedule"], &scheduleFields); err != nil {
		return errors.New("BFF config pipeline_schedule object has invalid types")
	}
	for _, field := range []string{"cron", "timezone", "audience", "service_account"} {
		if err := requireBFFJSONType("pipeline_schedule."+field, scheduleFields[field], "string", false); err != nil {
			return err
		}
	}
	var llmFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["llm"], &llmFields); err != nil {
		return errors.New("BFF config llm object has invalid types")
	}
	for _, field := range []struct{ name, kind string }{
		{"provider", "string"}, {"base_url", "string"},
		{"request_timeout_seconds", "integer"}, {"model", "string"},
	} {
		if err := requireBFFJSONType("llm."+field.name, llmFields[field.name], field.kind, false); err != nil {
			return err
		}
	}
	var queryFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["query"], &queryFields); err != nil {
		return errors.New("BFF config query object has invalid types")
	}
	if err := requireBFFJSONType("query.stage_config_path", queryFields["stage_config_path"], "string", false); err != nil {
		return err
	}
	if err := requireBFFJSONType("query.legacy", queryFields["legacy"], "object", true); err != nil {
		return err
	}
	if !isBFFJSONNull(queryFields["legacy"]) {
		var legacyFields map[string]json.RawMessage
		if err := json.Unmarshal(queryFields["legacy"], &legacyFields); err != nil {
			return errors.New("BFF config legacy query object has invalid types")
		}
		for _, field := range []struct{ name, kind string }{
			{"query_expansion_model", "string"}, {"query_expansion_reasoning", "string"},
			{"answer_synthesis_model", "string"}, {"answer_synthesis_reasoning", "string"},
			{"query_selection_limit", "integer"}, {"query_selection_exploration_slots", "integer"},
			{"query_selection_evidence_threshold", "integer"}, {"query_expansion_keywords_per_attempt", "integer"},
			{"query_expansion_attempts", "integer"}, {"query_matching_rare_keyword_max_document_frequency", "integer"},
		} {
			if err := requireBFFJSONType("query.legacy."+field.name, legacyFields[field.name], field.kind, false); err != nil {
				return err
			}
		}
	}
	if !isBFFJSONNull(fields["local"]) {
		var localFields map[string]json.RawMessage
		if err := json.Unmarshal(fields["local"], &localFields); err != nil {
			return errors.New("BFF config local object has invalid types")
		}
		for _, field := range []string{"scope", "worker_path", "pipeline_config_path", "pipeline_bindings_path"} {
			if err := requireBFFJSONType("local."+field, localFields[field], "string", false); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireBFFJSONType(name string, raw json.RawMessage, kind string, nullable bool) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("BFF config field %s has invalid type", name)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if nullable {
			return nil
		}
		return fmt.Errorf("BFF config field %s cannot be null", name)
	}
	invalid := func() error { return fmt.Errorf("BFF config field %s has invalid type", name) }
	switch kind {
	case "string":
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "integer":
		var value int
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "string_array":
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
			return invalid()
		}
		for index, value := range values {
			var item string
			if isBFFJSONNull(value) || json.Unmarshal(value, &item) != nil {
				return fmt.Errorf("BFF config field %s[%d] has invalid type", name, index)
			}
		}
	case "object":
		var value map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &value); err != nil || value == nil {
			return invalid()
		}
	default:
		return errors.New("BFF config schema contains an unsupported type")
	}
	return nil
}

func isBFFJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func requireJSONKeys(fields map[string]json.RawMessage, required []string) error {
	if len(fields) != len(required) {
		return errors.New("BFF config object has missing or unknown keys")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("BFF config is missing %s", key)
		}
	}
	return nil
}

func requireJSONKeysWithOptional(fields map[string]json.RawMessage, required, optional []string) error {
	if len(fields) < len(required) || len(fields) > len(required)+len(optional) {
		return errors.New("BFF config object has missing or unknown keys")
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("BFF config is missing %s", key)
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("BFF config contains unknown %s", key)
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

// RejectDuplicateJSONKeys validates JSON object keys without decoding a
// particular schema. Producers use it before applying their own strict shape.
func RejectDuplicateJSONKeys(data []byte) error {
	return rejectDuplicateJSONKeys(data)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateBFFFile(file BFFFile) error {
	if (file.SchemaVersion != 2 && file.SchemaVersion != 3) || file.Target != "bff" ||
		(file.Environment != "local" && file.Environment != "dev" && file.Environment != "prod") {
		return errors.New("BFF config identity is invalid")
	}
	if strings.TrimSpace(file.GCPProject) == "" || strings.TrimSpace(file.Bucket) == "" || strings.TrimSpace(file.FirestoreDatabaseID) == "" {
		return errors.New("BFF config resource identity is incomplete")
	}
	if file.PipelineSchedule.Cron != "0 * * * *" || file.PipelineSchedule.Timezone != "UTC" ||
		(file.PipelineSchedule.Audience == "") != (file.PipelineSchedule.ServiceAccount == "") {
		return errors.New("BFF config pipeline schedule is invalid")
	}
	if file.PipelineSchedule.Audience != "" {
		audience, err := url.Parse(file.PipelineSchedule.Audience)
		if err != nil || audience.Scheme != "https" || audience.Host == "" || audience.User != nil ||
			(audience.Path != "" && audience.Path != "/") || audience.RawQuery != "" || audience.ForceQuery || audience.Fragment != "" ||
			!regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9.-]+\.iam\.gserviceaccount\.com$`).MatchString(file.PipelineSchedule.ServiceAccount) {
			return errors.New("BFF config pipeline schedule identity is invalid")
		}
	}
	if strings.TrimSpace(file.AuthSessionEnvironment) == "" {
		return errors.New("BFF config auth_session_environment is invalid")
	}
	localMode := file.Environment == "local"
	if file.SchemaVersion == 3 {
		if localMode || file.JWTSecret != "" || file.AuthConsumer == nil ||
			file.AuthConsumer.SchemaVersion != 1 || file.AuthConsumer.Environment != file.Environment ||
			file.AuthConsumer.AuthURL != file.AuthServiceURL || file.AuthConsumer.Issuer != file.AuthServiceURL ||
			file.AuthConsumer.TimeoutSeconds != 5 ||
			!authServiceAccountPattern.MatchString(file.AuthConsumer.RuntimeServiceAccount) ||
			!validVerifierAudience(file.AuthConsumer.VerifierAudience) ||
			file.AuthConsumer.Resource != file.MCPResourceOrigin ||
			file.AuthConsumer.VerifierPath != "/api/v1/auth/consumers/lwc/verify-credential" ||
			file.AuthConsumer.AccountCheckPath != "/api/v1/auth/consumers/lwc/accounts/check" ||
			file.AuthConsumer.CatalogPath != "/api/v1/auth/consumers/lwc/management/catalog" ||
			!equalStringSet(file.AuthConsumer.AuthorityModes, []string{"legacy-shadow", "central"}) {
			return errors.New("BFF config auth_consumer is invalid")
		}
	} else if file.AuthConsumer != nil {
		return errors.New("BFF schema-2 config must not contain auth_consumer")
	}
	if err := validateRuntimeURL(file.AuthServiceURL, localMode); err != nil {
		return errors.New("BFF config auth_service_url is invalid")
	}
	if file.MCPResourceOrigin != "" {
		if err := validateRuntimeOrigin(file.MCPResourceOrigin, localMode); err != nil {
			return errors.New("BFF config mcp_resource_origin is invalid")
		}
	}
	if err := validatePipelineJobURL(file.PipelineJobURL); err != nil {
		return errors.New("BFF config pipeline_job_url is invalid")
	}
	if (file.ExportJobURL == "") != (file.ExportSigningServiceAccount == "") {
		return errors.New("BFF config export settings are incomplete")
	}
	if file.ExportJobURL != "" {
		if err := validatePipelineJobURL(file.ExportJobURL); err != nil || strings.TrimSpace(file.ExportSigningServiceAccount) == "" {
			return errors.New("BFF config export settings are invalid")
		}
	}
	if len(file.AllowedOrigins) == 0 || file.AllowedHosts == nil || file.PipelineDemoUserIDs == nil {
		return errors.New("BFF config allowlist or identity arrays are invalid")
	}
	for _, origin := range file.AllowedOrigins {
		if err := validateOrigin(origin); err != nil {
			return errors.New("BFF config allowed_origins is invalid")
		}
	}
	for _, host := range file.AllowedHosts {
		if strings.TrimSpace(host) == "" || strings.Contains(host, "*") || strings.ContainsAny(host, " /\\\r\n\t") {
			return errors.New("BFF config allowed_hosts is invalid")
		}
		if localMode && host != "localhost" && host != "127.0.0.1" {
			return errors.New("BFF config local allowed_hosts must use loopback")
		}
	}
	if localMode {
		if len(file.AllowedHosts) == 0 || len(file.AllowedOrigins) != 1 ||
			!strings.HasPrefix(file.AllowedOrigins[0], "http://localhost:") {
			return errors.New("BFF config local allowlist is invalid")
		}
		if file.GCPProject != "llm-wiki-cloud" || file.Bucket != "llm-wiki-cloud-local" || file.FirestoreDatabaseID != "llm-wiki-cloud-local" {
			return errors.New("BFF config local resource identity is invalid")
		}
		if file.Local == nil {
			return errors.New("BFF config local settings are missing")
		}
		if _, err := localcloud.Parse(file.Local.Scope); err != nil || file.Local.Scope == "" ||
			!absoluteRuntimePath(file.Local.WorkerPath) || !absoluteRuntimePath(file.Local.PipelineConfigPath) ||
			!absoluteRuntimePath(file.Local.PipelineBindingsPath) {
			return errors.New("BFF config local settings are invalid")
		}
		decoded, err := hex.DecodeString(file.JWTSecret)
		if err != nil || len(decoded) != 32 {
			return errors.New("BFF config jwt_secret is invalid")
		}
	} else if file.Local != nil {
		return errors.New("BFF config local settings must be null for cloud")
	}
	if (file.SchemaVersion == 2 && strings.TrimSpace(file.JWTSecret) == "") || strings.TrimSpace(file.DeepSeekAPIKey) == "" {
		return errors.New("BFF config required secret is empty")
	}
	if file.AuthSessionMigration != "disabled" && file.AuthSessionMigration != "legacy_read_through" {
		return errors.New("BFF config auth_session_migration is invalid")
	}
	if file.PipelineDailyLimit < 1 || file.PipelineDailyLimit > 1_000_000 ||
		file.PipelineCooldownSeconds < 1 || file.PipelineCooldownSeconds > 31_536_000 ||
		file.PipelineMinNewRaw < 1 || file.PipelineMinNewRaw > 1_000_000 || file.PortDefault < 1 || file.PortDefault > 65535 {
		return errors.New("BFF config numeric setting is out of range")
	}
	for _, id := range file.PipelineDemoUserIDs {
		if !bffDemoUserIDPattern.MatchString(id) {
			return errors.New("BFF config pipeline_demo_user_ids is invalid")
		}
	}
	if file.LLM.Provider != "deepseek" || file.LLM.RequestTimeoutSeconds < 1 || file.LLM.RequestTimeoutSeconds > 3600 ||
		llm.CanonicalDeepSeekModel(file.LLM.Model) == "" || !validBFFBaseURL(file.LLM.BaseURL) {
		return errors.New("BFF config llm settings are invalid")
	}
	if file.ProfileRuntimeAudience == "" != (file.ProfileRuntimeServiceAccount == "") {
		return errors.New("BFF config profile runtime identity is incomplete")
	}
	if err := validateBFFQuery(file.Query, localMode); err != nil {
		return err
	}
	if file.MCP != nil {
		if err := ValidateBFFMCP(*file.MCP); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeURL(raw string, local bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && !(local && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return errors.New("invalid URL")
	}
	return nil
}

func validateOrigin(raw string) error {
	if raw == "" || raw == "*" {
		return errors.New("invalid origin")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid origin")
	}
	return nil
}

func validBFFBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func validVerifierAudience(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func absoluteRuntimePath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, "\r\n\x00")
}

func validateBFFQuery(query BFFQuery, local bool) error {
	if query.StageConfigPath != "" {
		if query.Legacy != nil || !absoluteRuntimePath(query.StageConfigPath) || (!local && !strings.HasPrefix(filepath.ToSlash(query.StageConfigPath), "/app/configs/query/")) {
			return errors.New("BFF config query authority is invalid")
		}
		return nil
	}
	if query.Legacy == nil {
		return errors.New("BFF config query authority is missing")
	}
	q := query.Legacy
	if (q.QueryExpansionModel != "deepseek-flash" && q.QueryExpansionModel != "deepseek-v4-flash") || q.QueryExpansionReasoning != "none" ||
		(q.AnswerSynthesisModel != "deepseek-flash" && q.AnswerSynthesisModel != "deepseek-v4-flash" && q.AnswerSynthesisModel != "deepseek-v4-pro") ||
		(q.AnswerSynthesisReasoning != "none" && q.AnswerSynthesisReasoning != "low" && q.AnswerSynthesisReasoning != "high" && q.AnswerSynthesisReasoning != "max") ||
		q.QuerySelectionLimit < 1 || q.QuerySelectionLimit > MaxQuerySelectionLimit || q.QuerySelectionExplorationSlots < 0 || q.QuerySelectionExplorationSlots > q.QuerySelectionLimit ||
		q.QuerySelectionEvidenceThreshold < 1 || q.QuerySelectionEvidenceThreshold > MaxQuerySelectionEvidenceThreshold ||
		q.QueryExpansionKeywordsPerAttempt < 1 || q.QueryExpansionKeywordsPerAttempt > MaxQueryExpansionKeywordsPerAttempt ||
		q.QueryExpansionAttempts < 1 || q.QueryExpansionAttempts > MaxQueryExpansionAttempts ||
		q.QueryMatchingRareKeywordMaxDocumentFrequency < 1 || q.QueryMatchingRareKeywordMaxDocumentFrequency > MaxQueryMatchingRareKeywordDocumentFrequency {
		return errors.New("BFF config legacy query settings are invalid")
	}
	return nil
}

func configFromBFFFile(file BFFFile, port string) Config {
	cfg := Config{
		GCPProject: file.GCPProject, Bucket: file.Bucket, FirestoreDatabaseID: file.FirestoreDatabaseID,
		LocalCloudScope: localScope(file.Local), Port: port, DeepSeekAPIKey: file.DeepSeekAPIKey,
		TypeSafeAPIKey: file.TypeSafeAPIKey, LLMProvider: file.LLM.Provider, LLMBaseURL: file.LLM.BaseURL,
		LLMRequestTimeoutSeconds: file.LLM.RequestTimeoutSeconds, LLMModel: llm.CanonicalDeepSeekModel(file.LLM.Model),
		JWTSecret: file.JWTSecret, PipelineJobURL: file.PipelineJobURL, ExportJobURL: file.ExportJobURL,
		ExportSigningServiceAccount: file.ExportSigningServiceAccount, AllowedOrigins: file.AllowedOrigins,
		AllowedHosts: file.AllowedHosts, PipelineDailyLimit: file.PipelineDailyLimit,
		PipelineCooldownSeconds: file.PipelineCooldownSeconds, PipelineMinNewRaw: file.PipelineMinNewRaw,
		PipelineDemoUserIDs: file.PipelineDemoUserIDs, RegistrationEnabled: file.RegistrationEnabled,
		AuthServiceURL: file.AuthServiceURL, AuthSessionEnvironment: file.AuthSessionEnvironment,
		MCPResourceOrigin:      file.MCPResourceOrigin,
		MCP:                    file.MCP,
		BFFAuthConsumer:        file.AuthConsumer,
		BFFConfigSchemaVersion: file.SchemaVersion,
		AuthSessionMigration:   file.AuthSessionMigration, ProfileRuntimeAudience: file.ProfileRuntimeAudience,
		ProfileRuntimeServiceAccount: file.ProfileRuntimeServiceAccount,
		PipelineScheduleCron:         file.PipelineSchedule.Cron, PipelineScheduleTimezone: file.PipelineSchedule.Timezone,
		ScheduledPipelineAudience: file.PipelineSchedule.Audience, ScheduledPipelineServiceAccount: file.PipelineSchedule.ServiceAccount,
	}
	if file.Local != nil {
		cfg.LocalWorkerPath = file.Local.WorkerPath
		cfg.LocalPipelineConfigPath = file.Local.PipelineConfigPath
		cfg.LocalPipelineBindingsPath = file.Local.PipelineBindingsPath
	}
	if file.Query.StageConfigPath != "" {
		cfg.QueryStageConfigPath = file.Query.StageConfigPath
	} else if file.Query.Legacy != nil {
		q := file.Query.Legacy
		cfg.QueryExpansionModel = llm.CanonicalDeepSeekModel(q.QueryExpansionModel)
		cfg.QueryExpansionReasoning = llm.Reasoning(q.QueryExpansionReasoning)
		cfg.AnswerSynthesisModel = llm.CanonicalDeepSeekModel(q.AnswerSynthesisModel)
		cfg.AnswerSynthesisReasoning = llm.Reasoning(q.AnswerSynthesisReasoning)
		cfg.QuerySelectionLimit = q.QuerySelectionLimit
		cfg.QuerySelectionExplorationSlots = q.QuerySelectionExplorationSlots
		cfg.QuerySelectionEvidenceThreshold = q.QuerySelectionEvidenceThreshold
		cfg.QueryExpansionKeywordsPerAttempt = q.QueryExpansionKeywordsPerAttempt
		cfg.QueryExpansionAttempts = q.QueryExpansionAttempts
		cfg.QueryMatchingRareKeywordMaxDocumentFrequency = q.QueryMatchingRareKeywordMaxDocumentFrequency
	}
	return cfg
}

func localScope(local *BFFLocal) string {
	if local == nil {
		return ""
	}
	return local.Scope
}
