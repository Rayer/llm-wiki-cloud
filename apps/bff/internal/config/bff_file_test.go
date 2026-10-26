package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validBFFFileForTest() BFFFile {
	enabled := true
	return BFFFile{
		SchemaVersion: 2, Environment: "dev", Target: "bff",
		GCPProject: "llm-wiki-cloud", Bucket: "llm-wiki-data-dev", FirestoreDatabaseID: "llm-wiki-cloud-dev",
		AuthServiceURL: "https://auth.dev.example", MCPResourceOrigin: "https://bff.dev.example", PipelineJobURL: DefaultPipelineJobURL,
		AllowedOrigins: []string{"https://wiki.dev.example"}, AllowedHosts: []string{},
		PipelineDailyLimit: 2, PipelineCooldownSeconds: 600, PipelineMinNewRaw: 1,
		PipelineDemoUserIDs: []string{}, AuthSessionEnvironment: "development", AuthSessionMigration: "disabled",
		RegistrationEnabled: &enabled, JWTSecret: "fixture-jwt-secret", DeepSeekAPIKey: "fixture-deepseek-key",
		PipelineSchedule: BFFPipelineSchedule{Cron: "0 * * * *", Timezone: "UTC"},
		LLM:              BFFLLM{Provider: "deepseek", BaseURL: "https://api.deepseek.com", RequestTimeoutSeconds: 60, Model: "deepseek-flash"},
		Query:            BFFQuery{StageConfigPath: "/app/configs/query/dev/query.json"}, PortDefault: 8080,
	}
}

func validBFFMCPForTest() *BFFMCP {
	return &BFFMCP{
		Instructions: "configured instructions",
		ToolDescriptions: BFFMCPToolDescriptions{
			QueryProject: "configured query description",
			AddRaw:       "configured add description",
			ImportURL:    "configured import description",
		},
		DiscoveryTTLMS: 60_000, ToolsListTTLMS: 60_000,
	}
}

func validLocalBFFFileForTest() BFFFile {
	file := validBFFFileForTest()
	file.Environment = "local"
	file.GCPProject = "llm-wiki-cloud"
	file.Bucket = "llm-wiki-cloud-local"
	file.FirestoreDatabaseID = "llm-wiki-cloud-local"
	file.AuthServiceURL = "http://localhost:8081"
	file.MCPResourceOrigin = "http://localhost:8080"
	file.AllowedOrigins = []string{"http://localhost:3000"}
	file.AllowedHosts = []string{"localhost", "127.0.0.1"}
	file.JWTSecret = strings.Repeat("a", 64)
	file.AuthSessionEnvironment = "llm-wiki-cloud-local"
	file.PipelineCooldownSeconds = 60
	file.Query = BFFQuery{Legacy: &BFFQueryLegacy{
		QueryExpansionModel: "deepseek-flash", QueryExpansionReasoning: "none",
		AnswerSynthesisModel: "deepseek-flash", AnswerSynthesisReasoning: "none",
		QuerySelectionLimit: 10, QuerySelectionExplorationSlots: 1, QuerySelectionEvidenceThreshold: 2,
		QueryExpansionKeywordsPerAttempt: 24, QueryExpansionAttempts: 3,
		QueryMatchingRareKeywordMaxDocumentFrequency: 1,
	}}
	file.Local = &BFFLocal{Scope: "worktree-current", WorkerPath: "/tmp/worker", PipelineConfigPath: "/tmp/synto.toml", PipelineBindingsPath: "/tmp/bindings.json"}
	return file
}

func validBridgeBFFFileForTest() BFFFile {
	file := validBFFFileForTest()
	file.AuthServiceURL = legacyDevAuthURL
	file.MCPResourceOrigin = legacyDevBFFResource
	return file
}

func marshalBFFFileForTest(t *testing.T, file BFFFile) []byte {
	t.Helper()
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setBFFJSONValueForTest(t *testing.T, data []byte, path []string, value string) []byte {
	t.Helper()
	var replace func(json.RawMessage, []string) json.RawMessage
	replace = func(raw json.RawMessage, remaining []string) json.RawMessage {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			t.Fatalf("test fixture path %v is not an object", path)
		}
		if len(remaining) == 1 {
			fields[remaining[0]] = json.RawMessage(value)
		} else {
			fields[remaining[0]] = replace(fields[remaining[0]], remaining[1:])
		}
		updated, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}
	updated := replace(json.RawMessage(data), path)
	return append([]byte(nil), updated...)
}

func TestDecodeBFFFileRejectsNullAndWrongScalarTypes(t *testing.T) {
	cloudData := marshalBFFFileForTest(t, validBFFFileForTest())
	localData := marshalBFFFileForTest(t, validLocalBFFFileForTest())
	type scalarField struct {
		name string
		path []string
		kind string
		data []byte
	}
	fields := []scalarField{
		{name: "schema_version", path: []string{"schema_version"}, kind: "integer", data: cloudData},
		{name: "environment", path: []string{"environment"}, kind: "string", data: cloudData},
		{name: "target", path: []string{"target"}, kind: "string", data: cloudData},
		{name: "gcp_project", path: []string{"gcp_project"}, kind: "string", data: cloudData},
		{name: "bucket", path: []string{"bucket"}, kind: "string", data: cloudData},
		{name: "firestore_database_id", path: []string{"firestore_database_id"}, kind: "string", data: cloudData},
		{name: "auth_service_url", path: []string{"auth_service_url"}, kind: "string", data: cloudData},
		{name: "pipeline_job_url", path: []string{"pipeline_job_url"}, kind: "string", data: cloudData},
		{name: "export_job_url", path: []string{"export_job_url"}, kind: "string", data: cloudData},
		{name: "export_signing_service_account", path: []string{"export_signing_service_account"}, kind: "string", data: cloudData},
		{name: "allowed_origins", path: []string{"allowed_origins"}, kind: "string_array", data: cloudData},
		{name: "allowed_hosts", path: []string{"allowed_hosts"}, kind: "string_array", data: cloudData},
		{name: "pipeline_daily_limit", path: []string{"pipeline_daily_limit"}, kind: "integer", data: cloudData},
		{name: "pipeline_cooldown_seconds", path: []string{"pipeline_cooldown_seconds"}, kind: "integer", data: cloudData},
		{name: "pipeline_min_new_raw", path: []string{"pipeline_min_new_raw"}, kind: "integer", data: cloudData},
		{name: "pipeline_demo_user_ids", path: []string{"pipeline_demo_user_ids"}, kind: "string_array", data: cloudData},
		{name: "auth_session_environment", path: []string{"auth_session_environment"}, kind: "string", data: cloudData},
		{name: "auth_session_migration", path: []string{"auth_session_migration"}, kind: "string", data: cloudData},
		{name: "jwt_secret", path: []string{"jwt_secret"}, kind: "string", data: cloudData},
		{name: "deepseek_api_key", path: []string{"deepseek_api_key"}, kind: "string", data: cloudData},
		{name: "typesafe_api_key", path: []string{"typesafe_api_key"}, kind: "string", data: cloudData},
		{name: "profile_runtime_audience", path: []string{"profile_runtime_audience"}, kind: "string", data: cloudData},
		{name: "pipeline_schedule.cron", path: []string{"pipeline_schedule", "cron"}, kind: "string", data: cloudData},
		{name: "pipeline_schedule.timezone", path: []string{"pipeline_schedule", "timezone"}, kind: "string", data: cloudData},
		{name: "pipeline_schedule.audience", path: []string{"pipeline_schedule", "audience"}, kind: "string", data: cloudData},
		{name: "pipeline_schedule.service_account", path: []string{"pipeline_schedule", "service_account"}, kind: "string", data: cloudData},
		{name: "profile_runtime_service_account", path: []string{"profile_runtime_service_account"}, kind: "string", data: cloudData},
		{name: "port_default", path: []string{"port_default"}, kind: "integer", data: cloudData},
		{name: "llm.provider", path: []string{"llm", "provider"}, kind: "string", data: cloudData},
		{name: "llm.base_url", path: []string{"llm", "base_url"}, kind: "string", data: cloudData},
		{name: "llm.request_timeout_seconds", path: []string{"llm", "request_timeout_seconds"}, kind: "integer", data: cloudData},
		{name: "llm.model", path: []string{"llm", "model"}, kind: "string", data: cloudData},
		{name: "query.stage_config_path", path: []string{"query", "stage_config_path"}, kind: "string", data: cloudData},
		{name: "query.legacy.query_expansion_model", path: []string{"query", "legacy", "query_expansion_model"}, kind: "string", data: localData},
		{name: "query.legacy.query_expansion_reasoning", path: []string{"query", "legacy", "query_expansion_reasoning"}, kind: "string", data: localData},
		{name: "query.legacy.answer_synthesis_model", path: []string{"query", "legacy", "answer_synthesis_model"}, kind: "string", data: localData},
		{name: "query.legacy.answer_synthesis_reasoning", path: []string{"query", "legacy", "answer_synthesis_reasoning"}, kind: "string", data: localData},
		{name: "query.legacy.query_selection_limit", path: []string{"query", "legacy", "query_selection_limit"}, kind: "integer", data: localData},
		{name: "query.legacy.query_selection_exploration_slots", path: []string{"query", "legacy", "query_selection_exploration_slots"}, kind: "integer", data: localData},
		{name: "query.legacy.query_selection_evidence_threshold", path: []string{"query", "legacy", "query_selection_evidence_threshold"}, kind: "integer", data: localData},
		{name: "query.legacy.query_expansion_keywords_per_attempt", path: []string{"query", "legacy", "query_expansion_keywords_per_attempt"}, kind: "integer", data: localData},
		{name: "query.legacy.query_expansion_attempts", path: []string{"query", "legacy", "query_expansion_attempts"}, kind: "integer", data: localData},
		{name: "query.legacy.query_matching_rare_keyword_max_document_frequency", path: []string{"query", "legacy", "query_matching_rare_keyword_max_document_frequency"}, kind: "integer", data: localData},
		{name: "local.scope", path: []string{"local", "scope"}, kind: "string", data: localData},
		{name: "local.worker_path", path: []string{"local", "worker_path"}, kind: "string", data: localData},
		{name: "local.pipeline_config_path", path: []string{"local", "pipeline_config_path"}, kind: "string", data: localData},
		{name: "local.pipeline_bindings_path", path: []string{"local", "pipeline_bindings_path"}, kind: "string", data: localData},
	}
	for _, field := range fields {
		field := field
		for _, invalid := range []struct {
			name  string
			value string
		}{
			{name: "null", value: "null"},
			{name: "wrong type", value: map[string]string{"string": `17`, "integer": `"wrong"`, "string_array": `{}`}[field.kind]},
		} {
			t.Run(field.name+"/"+invalid.name, func(t *testing.T) {
				data := setBFFJSONValueForTest(t, field.data, field.path, invalid.value)
				if _, err := DecodeBFFFile(data); err == nil {
					t.Fatal("DecodeBFFFile() accepted a null or wrong-typed field")
				} else if strings.Contains(err.Error(), "fixture-jwt-secret") || strings.Contains(err.Error(), "fixture-deepseek-key") {
					t.Fatalf("type diagnostic exposed a secret value: %v", err)
				}
			})
		}
	}
	for _, field := range []struct {
		name  string
		path  []string
		value string
	}{
		{name: "registration_enabled", path: []string{"registration_enabled"}, value: `"true"`},
		{name: "query.legacy object", path: []string{"query", "legacy"}, value: `"wrong"`},
		{name: "local object", path: []string{"local"}, value: `"wrong"`},
		{name: "allowed_hosts null element", path: []string{"allowed_hosts"}, value: `[null]`},
	} {
		t.Run(field.name+"/wrong type", func(t *testing.T) {
			if _, err := DecodeBFFFile(setBFFJSONValueForTest(t, cloudData, field.path, field.value)); err == nil {
				t.Fatal("DecodeBFFFile() accepted a wrong-typed nullable or array field")
			}
		})
	}
}

func TestDecodeBFFFileValidatesMCPMetadataAndPreservesSchema2Compatibility(t *testing.T) {
	legacyBytes := marshalBFFFileForTest(t, validBFFFileForTest())
	legacy, err := DecodeBFFFile(legacyBytes)
	if err != nil || legacy.MCP != nil || configFromBFFFile(legacy, "8080").MCP != nil {
		t.Fatalf("schema2 without mcp did not retain the legacy path: mcp=%+v err=%v", legacy.MCP, err)
	}

	configured := validBFFFileForTest()
	configured.MCP = validBFFMCPForTest()
	data := marshalBFFFileForTest(t, configured)
	accepted := func(label string, raw []byte) {
		t.Helper()
		if _, err := DecodeBFFFile(raw); err != nil {
			t.Errorf("%s rejected: %v", label, err)
		}
	}
	accepted("zero discovery TTL", setBFFJSONValueForTest(t, data, []string{"mcp", "discovery_ttl_ms"}, "0"))
	accepted("zero tools TTL", setBFFJSONValueForTest(t, data, []string{"mcp", "tools_list_ttl_ms"}, "0"))
	accepted("maximum discovery TTL", setBFFJSONValueForTest(t, data, []string{"mcp", "discovery_ttl_ms"}, "3600000"))
	accepted("maximum tools TTL", setBFFJSONValueForTest(t, data, []string{"mcp", "tools_list_ttl_ms"}, "3600000"))

	invalid := []struct {
		name  string
		path  []string
		value string
	}{
		{"null object", []string{"mcp"}, "null"},
		{"wrong object type", []string{"mcp"}, `[]`},
		{"empty object", []string{"mcp"}, `{}`},
		{"missing key", []string{"mcp"}, `{"instructions":"x","tool_descriptions":{"query_project":"x","add_raw":"x","import_url":"x"},"discovery_ttl_ms":0}`},
		{"unknown key", []string{"mcp"}, `{"instructions":"x","tool_descriptions":{"query_project":"x","add_raw":"x","import_url":"x"},"discovery_ttl_ms":0,"tools_list_ttl_ms":0,"extra":"x"}`},
		{"wrong instructions type", []string{"mcp", "instructions"}, "null"},
		{"empty instructions", []string{"mcp", "instructions"}, `"  "`},
		{"unknown description key", []string{"mcp", "tool_descriptions"}, `{"query_project":"x","add_raw":"x","import_url":"x","extra":"x"}`},
		{"missing description key", []string{"mcp", "tool_descriptions"}, `{"query_project":"x","add_raw":"x"}`},
		{"empty description", []string{"mcp", "tool_descriptions", "query_project"}, `""`},
		{"wrong description type", []string{"mcp", "tool_descriptions", "add_raw"}, "false"},
		{"wrong TTL type", []string{"mcp", "discovery_ttl_ms"}, `"60000"`},
		{"fractional TTL", []string{"mcp", "discovery_ttl_ms"}, "0.5"},
		{"negative TTL", []string{"mcp", "discovery_ttl_ms"}, "-1"},
		{"too large TTL", []string{"mcp", "tools_list_ttl_ms"}, "3600001"},
		{"oversized instructions", []string{"mcp", "instructions"}, `"` + strings.Repeat("x", maxMCPInstructionsBytes+1) + `"`},
		{"oversized description", []string{"mcp", "tool_descriptions", "import_url"}, `"` + strings.Repeat("x", maxMCPToolDescriptionBytes+1) + `"`},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			bad := setBFFJSONValueForTest(t, data, tc.path, tc.value)
			if _, err := DecodeBFFFile(bad); err == nil {
				t.Fatal("invalid MCP metadata was accepted")
			} else if strings.Contains(err.Error(), "configured") || strings.Contains(err.Error(), strings.Repeat("x", 32)) {
				t.Fatalf("validation error exposed configured text: %v", err)
			}
		})
	}
}

func TestDecodeBFFFileKeepsMCPOriginOptionalForOldRuntimeConfigs(t *testing.T) {
	file := validBFFFileForTest()
	file.MCPResourceOrigin = ""
	decoded, err := DecodeBFFFile(marshalBFFFileForTest(t, file))
	if err != nil || decoded.MCPResourceOrigin != "" || configFromBFFFile(decoded, "8080").MCPResourceOrigin != "" {
		t.Fatalf("legacy BFF config with no MCP origin = %+v, err=%v", decoded, err)
	}

	file.MCPResourceOrigin = "https://bff.dev.example"
	decoded, err = DecodeBFFFile(marshalBFFFileForTest(t, file))
	if err != nil || configFromBFFFile(decoded, "8080").MCPResourceOrigin != file.MCPResourceOrigin {
		t.Fatalf("configured MCP origin did not reach the consumer: %+v, err=%v", decoded, err)
	}

	file.MCPResourceOrigin = "https://bff.dev.example/api"
	if _, err := DecodeBFFFile(marshalBFFFileForTest(t, file)); err == nil {
		t.Fatal("pathful MCP resource origin was accepted")
	}
}

func TestDecodeBFFFilePreservesFrozenNullableAndEmptyControls(t *testing.T) {
	cloud := validBFFFileForTest()
	cloud.RegistrationEnabled = nil
	cloud.ExportJobURL = ""
	cloud.ExportSigningServiceAccount = ""
	cloud.TypeSafeAPIKey = ""
	cloud.ProfileRuntimeAudience = ""
	cloud.ProfileRuntimeServiceAccount = ""
	decoded, err := DecodeBFFFile(marshalBFFFileForTest(t, cloud))
	if err != nil || decoded.RegistrationEnabled != nil || decoded.Local != nil || decoded.Query.Legacy != nil {
		t.Fatalf("valid nullable cloud fields or empty optional strings were rejected: err=%v", err)
	}

	local := validLocalBFFFileForTest()
	local.Query.StageConfigPath = ""
	if _, err := DecodeBFFFile(marshalBFFFileForTest(t, local)); err != nil {
		t.Fatalf("valid local legacy query object with empty stage path was rejected: %v", err)
	}
}

func TestLoadBFFFileRejectsInvalidSessionNamespaceBeforeReturningConfig(t *testing.T) {
	data := marshalBFFFileForTest(t, validBridgeBFFFileForTest())
	validPath := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(validPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	valid, err := LoadBFFFile(validPath)
	if err != nil || valid.AuthSessionEnvironment != "development" {
		t.Fatalf("valid generated session namespace was not preserved: namespace=%q err=%v", valid.AuthSessionEnvironment, err)
	}
	for _, value := range []string{"null", `""`} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		invalid := setBFFJSONValueForTest(t, data, []string{"auth_session_environment"}, value)
		if err := os.WriteFile(path, invalid, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadBFFFile(path)
		if err == nil || loaded.AuthSessionEnvironment != "" || loaded.FirestoreDatabaseID != "" {
			t.Fatalf("invalid explicit session namespace reached runtime config: config=%#v err=%v", loaded, err)
		}
	}
}

func TestLoadBFFFileRejectsNullAndWrongTypesBeforeReturningRuntimeConfig(t *testing.T) {
	t.Setenv("PORT", "")
	cloudData := marshalBFFFileForTest(t, validBFFFileForTest())
	localData := marshalBFFFileForTest(t, validLocalBFFFileForTest())
	cases := []struct {
		name  string
		data  []byte
		path  []string
		value string
	}{
		{name: "auth_session_environment/null", data: cloudData, path: []string{"auth_session_environment"}, value: "null"},
		{name: "auth_session_environment/wrong_type", data: cloudData, path: []string{"auth_session_environment"}, value: `17`},
		{name: "typesafe_api_key/null", data: cloudData, path: []string{"typesafe_api_key"}, value: "null"},
		{name: "typesafe_api_key/wrong_type", data: cloudData, path: []string{"typesafe_api_key"}, value: `17`},
		{name: "export_job_url/null", data: cloudData, path: []string{"export_job_url"}, value: "null"},
		{name: "export_job_url/wrong_type", data: cloudData, path: []string{"export_job_url"}, value: `17`},
		{name: "export_signing_service_account/null", data: cloudData, path: []string{"export_signing_service_account"}, value: "null"},
		{name: "export_signing_service_account/wrong_type", data: cloudData, path: []string{"export_signing_service_account"}, value: `17`},
		{name: "profile_runtime_audience/null", data: cloudData, path: []string{"profile_runtime_audience"}, value: "null"},
		{name: "profile_runtime_audience/wrong_type", data: cloudData, path: []string{"profile_runtime_audience"}, value: `17`},
		{name: "profile_runtime_service_account/null", data: cloudData, path: []string{"profile_runtime_service_account"}, value: "null"},
		{name: "profile_runtime_service_account/wrong_type", data: cloudData, path: []string{"profile_runtime_service_account"}, value: `17`},
		{name: "llm.provider/null", data: cloudData, path: []string{"llm", "provider"}, value: "null"},
		{name: "llm.provider/wrong_type", data: cloudData, path: []string{"llm", "provider"}, value: `17`},
		{name: "query.legacy.query_expansion_model/null", data: localData, path: []string{"query", "legacy", "query_expansion_model"}, value: "null"},
		{name: "query.legacy.query_expansion_model/wrong_type", data: localData, path: []string{"query", "legacy", "query_expansion_model"}, value: `17`},
		{name: "local.worker_path/null", data: localData, path: []string{"local", "worker_path"}, value: "null"},
		{name: "local.worker_path/wrong_type", data: localData, path: []string{"local", "worker_path"}, value: `17`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bff.json")
			data := setBFFJSONValueForTest(t, tc.data, tc.path, tc.value)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadBFFFile(path)
			if err == nil || loaded.FirestoreDatabaseID != "" || loaded.AuthSessionEnvironment != "" || loaded.JWTSecret != "" {
				t.Fatalf("invalid file reached runtime config: config=%#v err=%v", loaded, err)
			}
		})
	}
}

func TestDecodeBFFFileAcceptsFrozenSchemaAndPreservesSSOTValues(t *testing.T) {
	data := marshalBFFFileForTest(t, validBFFFileForTest())
	got, err := DecodeBFFFile(data)
	if err != nil {
		t.Fatalf("DecodeBFFFile() error = %v", err)
	}
	if got.PipelineCooldownSeconds != 600 || got.LLM.RequestTimeoutSeconds != 60 || got.Query.StageConfigPath != "/app/configs/query/dev/query.json" ||
		got.PipelineSchedule.Cron != "0 * * * *" || got.PipelineSchedule.Timezone != "UTC" {
		t.Fatalf("decoded config lost selected values: cooldown=%d timeout=%d query=%q schedule=%q %q", got.PipelineCooldownSeconds, got.LLM.RequestTimeoutSeconds, got.Query.StageConfigPath, got.PipelineSchedule.Cron, got.PipelineSchedule.Timezone)
	}
}

func TestDecodeBFFFileRejectsMalformedIdentityAndShape(t *testing.T) {
	tests := []struct {
		name string
		data func(*testing.T) []byte
	}{
		{name: "wrong schema", data: func(t *testing.T) []byte {
			f := validBFFFileForTest()
			f.SchemaVersion = 1
			return marshalBFFFileForTest(t, f)
		}},
		{name: "wrong environment", data: func(t *testing.T) []byte {
			f := validBFFFileForTest()
			f.Environment = "development"
			return marshalBFFFileForTest(t, f)
		}},
		{name: "query authorities conflict", data: func(t *testing.T) []byte {
			f := validBFFFileForTest()
			f.Query.Legacy = &BFFQueryLegacy{}
			return marshalBFFFileForTest(t, f)
		}},
		{name: "duplicate root key", data: func(t *testing.T) []byte {
			return []byte(strings.Replace(string(marshalBFFFileForTest(t, validBFFFileForTest())), `"target":"bff"`, `"target":"bff","target":"bff"`, 1))
		}},
		{name: "trailing value", data: func(t *testing.T) []byte {
			return append(marshalBFFFileForTest(t, validBFFFileForTest()), []byte(` {}`)...)
		}},
		{name: "missing nested key", data: func(t *testing.T) []byte {
			return []byte(strings.Replace(string(marshalBFFFileForTest(t, validBFFFileForTest())), `"stage_config_path":"/app/configs/query/dev/query.json",`, "", 1))
		}},
		{name: "unknown key", data: func(t *testing.T) []byte {
			return []byte(strings.TrimSuffix(string(marshalBFFFileForTest(t, validBFFFileForTest())), "}") + `,"extra":true}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeBFFFile(tt.data(t)); err == nil {
				t.Fatal("DecodeBFFFile() succeeded for invalid document")
			}
		})
	}
}

func TestDecodeBFFFileRejectsMissingRequiredSettingsIllegalModesAndUnpairedIdentity(t *testing.T) {
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(marshalBFFFileForTest(t, validBFFFileForTest()), &missingRequired); err != nil {
		t.Fatal(err)
	}
	delete(missingRequired, "jwt_secret")
	missingData, err := json.Marshal(missingRequired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBFFFile(missingData); err == nil || strings.Contains(err.Error(), "fixture-jwt-secret") {
		t.Fatalf("DecodeBFFFile() did not safely reject a missing required key: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*BFFFile)
	}{
		{name: "missing signing key", mutate: func(file *BFFFile) { file.JWTSecret = "" }},
		{name: "illegal cooldown", mutate: func(file *BFFFile) { file.PipelineCooldownSeconds = 0 }},
		{name: "illegal LLM timeout", mutate: func(file *BFFFile) { file.LLM.RequestTimeoutSeconds = 0 }},
		{name: "illegal session mode", mutate: func(file *BFFFile) { file.AuthSessionMigration = "automatic" }},
		{name: "profile audience without service account", mutate: func(file *BFFFile) {
			file.ProfileRuntimeAudience = "https://profile.example.test"
		}},
		{name: "local settings on cloud environment", mutate: func(file *BFFFile) {
			file.Local = &BFFLocal{Scope: "worktree-fixture", WorkerPath: "/tmp/worker",
				PipelineConfigPath: "/tmp/synto.toml", PipelineBindingsPath: "/tmp/bindings.json"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := validBFFFileForTest()
			tc.mutate(&file)
			if _, err := DecodeBFFFile(marshalBFFFileForTest(t, file)); err == nil {
				t.Fatal("DecodeBFFFile() accepted invalid required settings or pairing")
			} else if strings.Contains(err.Error(), "fixture-jwt-secret") || strings.Contains(err.Error(), "fixture-deepseek-key") {
				t.Fatalf("validation diagnostics exposed a fixture secret: %v", err)
			}
		})
	}
}

func TestDecodeBFFFileAllowsQualityKeyWithoutProfileRuntime(t *testing.T) {
	file := validLocalBFFFileForTest()
	file.TypeSafeAPIKey = "fixture-typesafe-key"
	decoded, err := DecodeBFFFile(marshalBFFFileForTest(t, file))
	if err != nil {
		t.Fatalf("DecodeBFFFile() rejected an independent quality key: %v", err)
	}
	loaded := configFromBFFFile(decoded, "8080")
	if loaded.TypeSafeAPIKey != file.TypeSafeAPIKey || loaded.ProfileRuntimeAudience != "" || loaded.ProfileRuntimeServiceAccount != "" {
		t.Fatalf("loaded config coupled quality key to Profile identity: %+v", loaded)
	}
}

func TestLoadBFFFileUsesOnlyPortEnvironmentOverride(t *testing.T) {
	t.Setenv("PORT", "9091")
	t.Setenv("PIPELINE_COOLDOWN_SECONDS", "3")
	t.Setenv("DEEPSEEK_API_KEY", "stale-key")
	t.Setenv("MCP_DISCOVERY_TTL_MS", "0")
	t.Setenv("MCP_TOOLS_LIST_TTL_MS", "0")
	t.Setenv("MCP_INSTRUCTIONS", "stale instructions")
	file := validBridgeBFFFileForTest()
	file.MCP = validBFFMCPForTest()
	data := marshalBFFFileForTest(t, file)
	path := filepath.Join(t.TempDir(), "bff.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadBFFFile(path)
	if err != nil {
		t.Fatalf("LoadBFFFile() error = %v", err)
	}
	if cfg.Port != "9091" || cfg.PipelineCooldownSeconds != 600 || cfg.DeepSeekAPIKey != "fixture-deepseek-key" {
		t.Fatalf("loaded config port=%q cooldown=%d key=%q; stale env must not override file", cfg.Port, cfg.PipelineCooldownSeconds, cfg.DeepSeekAPIKey)
	}
	if cfg.MCP == nil || cfg.MCP.DiscoveryTTLMS != 60_000 || cfg.MCP.ToolsListTTLMS != 60_000 || cfg.MCP.Instructions != "configured instructions" {
		t.Fatalf("MCP environment values overrode CaC metadata: %+v", cfg.MCP)
	}
}

func TestLoadBFFFileBuildsBoundedSchema2AuthBridge(t *testing.T) {
	t.Setenv("PORT", "")
	path := filepath.Join(t.TempDir(), "bff.json")
	if err := os.WriteFile(path, marshalBFFFileForTest(t, validBridgeBFFFileForTest()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadBFFFile(path)
	if err != nil {
		t.Fatalf("LoadBFFFile() error = %v", err)
	}
	if cfg.BFFConfigSchemaVersion != 2 || !cfg.BFFManagementBridge || cfg.BFFAuthConsumer == nil {
		t.Fatalf("schema-2 bridge not enabled: version=%d bridge=%t consumer=%+v", cfg.BFFConfigSchemaVersion, cfg.BFFManagementBridge, cfg.BFFAuthConsumer)
	}
	consumer := cfg.BFFAuthConsumer
	if consumer.AuthURL != legacyDevAuthURL || consumer.Resource != legacyDevBFFResource || consumer.VerifierAudience != legacyDevAuthAudience ||
		consumer.RuntimeServiceAccount != legacyDevBFFCaller || consumer.VerifierPath != "/api/v1/auth/consumers/lwc/verify-credential" ||
		consumer.AccountCheckPath != "/api/v1/auth/consumers/lwc/accounts/check" {
		t.Fatalf("schema-2 bridge identity is not the frozen DEV projection: %+v", consumer)
	}
	file := validBridgeBFFFileForTest()
	file.AuthServiceURL = "https://auth.dev.untrusted.example"
	if err := os.WriteFile(path, marshalBFFFileForTest(t, file), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := LoadBFFFile(path); err == nil || loaded.BFFAuthConsumer != nil || loaded.FirestoreDatabaseID != "" {
		t.Fatalf("untrusted schema-2 Auth URL reached runtime config: config=%+v err=%v", loaded, err)
	}
}

func TestLoadBFFFileRejectsInvalidPortAndMissingLocator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bff.json")
	if err := os.WriteFile(path, marshalBFFFileForTest(t, validBFFFileForTest()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "65536")
	if _, err := LoadBFFFile(path); err == nil {
		t.Fatal("LoadBFFFile() accepted out-of-range PORT")
	}
	if _, err := LoadBFFFile(""); err == nil {
		t.Fatal("LoadBFFFile() accepted missing path")
	}
}

func TestLoadBFFFileRejectsMissingFileAndOversizedDocument(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadBFFFile(filepath.Join(directory, "missing.json")); err == nil {
		t.Fatal("LoadBFFFile() accepted an unavailable config file")
	}
	path := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(path, make([]byte, MaxBFFConfigBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBFFFile(path); err == nil {
		t.Fatal("LoadBFFFile() accepted a document over the 64 KiB limit")
	}
}

func TestDecodeBFFFileUsesExplicitLocalScopeAndPaths(t *testing.T) {
	file := validBFFFileForTest()
	file.Environment = "local"
	file.GCPProject = "llm-wiki-cloud"
	file.Bucket = "llm-wiki-cloud-local"
	file.FirestoreDatabaseID = "llm-wiki-cloud-local"
	file.AuthServiceURL = "http://localhost:8081"
	file.AllowedOrigins = []string{"http://localhost:3000"}
	file.AllowedHosts = []string{"localhost", "127.0.0.1"}
	file.JWTSecret = strings.Repeat("a", 64)
	file.Query = BFFQuery{Legacy: &BFFQueryLegacy{
		QueryExpansionModel: "deepseek-flash", QueryExpansionReasoning: "none",
		AnswerSynthesisModel: "deepseek-flash", AnswerSynthesisReasoning: "none",
		QuerySelectionLimit: 10, QuerySelectionExplorationSlots: 1, QuerySelectionEvidenceThreshold: 2,
		QueryExpansionKeywordsPerAttempt: 24, QueryExpansionAttempts: 3,
		QueryMatchingRareKeywordMaxDocumentFrequency: 1,
	}}
	file.Local = &BFFLocal{Scope: "worktree-current", WorkerPath: "/tmp/worker", PipelineConfigPath: "/tmp/synto.toml", PipelineBindingsPath: "/tmp/bindings.json"}
	file.PipelineCooldownSeconds = 60
	decoded, err := DecodeBFFFile(marshalBFFFileForTest(t, file))
	if err != nil {
		t.Fatalf("DecodeBFFFile() error = %v", err)
	}
	cfg := configFromBFFFile(decoded, "8080")
	if cfg.LocalCloudScope != "worktree-current" || cfg.LocalWorkerPath != "/tmp/worker" || cfg.PipelineCooldownSeconds != 60 {
		t.Fatalf("local config projection = %#v", cfg)
	}
}
