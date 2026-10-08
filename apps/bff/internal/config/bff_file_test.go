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
		AuthServiceURL: "https://auth.dev.example", PipelineJobURL: DefaultPipelineJobURL,
		AllowedOrigins: []string{"https://wiki.dev.example"}, AllowedHosts: []string{},
		PipelineDailyLimit: 2, PipelineCooldownSeconds: 600, PipelineMinNewRaw: 1,
		PipelineDemoUserIDs: []string{}, AuthSessionEnvironment: "development", AuthSessionMigration: "disabled",
		RegistrationEnabled: &enabled, JWTSecret: "fixture-jwt-secret", DeepSeekAPIKey: "fixture-deepseek-key",
		LLM:   BFFLLM{Provider: "deepseek", BaseURL: "https://api.deepseek.com", RequestTimeoutSeconds: 60, Model: "deepseek-flash"},
		Query: BFFQuery{StageConfigPath: "/app/configs/query/dev/query.json"}, PortDefault: 8080,
	}
}

func marshalBFFFileForTest(t *testing.T, file BFFFile) []byte {
	t.Helper()
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeBFFFileAcceptsFrozenSchemaAndPreservesSSOTValues(t *testing.T) {
	data := marshalBFFFileForTest(t, validBFFFileForTest())
	got, err := DecodeBFFFile(data)
	if err != nil {
		t.Fatalf("DecodeBFFFile() error = %v", err)
	}
	if got.PipelineCooldownSeconds != 600 || got.LLM.RequestTimeoutSeconds != 60 || got.Query.StageConfigPath != "/app/configs/query/dev/query.json" {
		t.Fatalf("decoded config lost selected values: cooldown=%d timeout=%d query=%q", got.PipelineCooldownSeconds, got.LLM.RequestTimeoutSeconds, got.Query.StageConfigPath)
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
		{name: "TypeSafe key without profile identity", mutate: func(file *BFFFile) {
			file.TypeSafeAPIKey = "fixture-typesafe-key"
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

func TestLoadBFFFileUsesOnlyPortEnvironmentOverride(t *testing.T) {
	t.Setenv("PORT", "9091")
	t.Setenv("PIPELINE_COOLDOWN_SECONDS", "3")
	t.Setenv("DEEPSEEK_API_KEY", "stale-key")
	data := marshalBFFFileForTest(t, validBFFFileForTest())
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
