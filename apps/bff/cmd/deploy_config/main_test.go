package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("../../../.."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadReviewedEnvironmentsAndQueryIdentity(t *testing.T) {
	root := repoRoot(t)
	for _, environment := range []string{"development", "production"} {
		config, err := Load(environment, filepath.Join(root, "deploy/environments", environment+".yaml"), "auth,bff,worker,frontend")
		if err != nil {
			t.Fatalf("Load(%s): %v", environment, err)
		}
		if config.QueryConfig.RuntimePath != "/app/configs/query/dev/query-dev-2026-09-12.1.json" || config.QueryConfig.Revision != "query-dev-2026-09-12.1" || config.QueryConfig.Digest != "sha256:645404d90133ba8adabed71e83b22560093dabaf3e8136953961392be7b33da0" || config.QueryConfig.SchemaVersion != 2 {
			t.Fatalf("%s query identity = %#v", environment, config.QueryConfig)
		}
		if !config.Evidence.Validated || !config.Evidence.SecretFree || !strings.HasPrefix(config.Evidence.ConfigFingerprint, "sha256:") {
			t.Fatalf("%s evidence = %#v", environment, config.Evidence)
		}
		auth, ok := config.Components["auth"].(map[string]any)
		if !ok || auth["demo_user_id"] != config.Auth.DemoUserID {
			t.Fatalf("%s Auth component input omitted Demo UID: %#v", environment, config.Components["auth"])
		}
		bff, ok := config.Components["bff"].(map[string]any)
		if !ok {
			t.Fatalf("%s BFF component input omitted: %#v", environment, config.Components["bff"])
		}
		if environment == "development" {
			ref := config.BFF.SecretReferences.TypeSafeJevAPIKey
			if config.BFF.ProfileRuntimeAudience == "" || config.BFF.ProfileRuntimeServiceAccount == "" || ref == nil ||
				!validProfileRuntimeAudience(config.BFF.ProfileRuntimeAudience) || !validProfileRuntimeServiceAccount(config.BFF.ProfileRuntimeServiceAccount) ||
				!secretVersionPattern.MatchString(ref.Version) {
				t.Fatalf("DEV Profile runtime bindings are incomplete or invalid: %#v", config.BFF)
			}
			if bff["profile_runtime_audience"] != config.BFF.ProfileRuntimeAudience || bff["profile_runtime_service_account"] != config.BFF.ProfileRuntimeServiceAccount {
				t.Fatalf("DEV BFF component input omitted Profile runtime bindings: %#v", bff)
			}
			if !reflect.DeepEqual(config.BFF.PipelineDemoUserIDs, []string{"e492f6bdaf1735e12b2de96d"}) ||
				!reflect.DeepEqual(bff["pipeline_demo_user_ids"], config.BFF.PipelineDemoUserIDs) {
				t.Fatalf("DEV BFF component input omitted Demo IDs: %#v", bff)
			}
			secretRefs, ok := bff["secret_references"].(map[string]any)
			if !ok || secretRefs["typesafe_jev_api_key"] != ref {
				t.Fatalf("DEV BFF component input omitted TypeSafe secret binding: %#v", secretRefs)
			}
		} else if len(config.BFF.PipelineDemoUserIDs) != 0 {
			t.Fatalf("Production unexpectedly received DEV Demo IDs: %#v", config.BFF)
		} else if config.BFF.ProfileRuntimeAudience != "https://llm-wiki-bff-a5nkmux6pq-de.a.run.app" ||
			config.BFF.ProfileRuntimeServiceAccount != "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey == nil ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey.Name != "typesafe-jev-api-key-prod" ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey.Version != "1" ||
			!secretVersionPattern.MatchString(config.BFF.SecretReferences.TypeSafeJevAPIKey.Version) {
			t.Fatalf("Production Profile runtime bindings are incomplete or invalid: %#v", config.BFF)
		} else if bff["profile_runtime_audience"] != config.BFF.ProfileRuntimeAudience || bff["profile_runtime_service_account"] != config.BFF.ProfileRuntimeServiceAccount {
			t.Fatalf("Production BFF component input omitted Profile runtime bindings: %#v", bff)
		} else if _, exists := bff["pipeline_demo_user_ids"]; exists {
			t.Fatalf("Production BFF component input unexpectedly includes DEV Demo IDs: %#v", bff)
		}
		worker, ok := config.Components["worker"].(map[string]any)
		if !ok || worker["args"] == nil || worker["secret_references"] == nil {
			t.Fatalf("%s worker component input omitted behavior-bearing config: %#v", environment, config.Components["worker"])
		}
	}
}

func TestAuthDemoUIDConfigIsOptionalButValidatedAndRendered(t *testing.T) {
	root := repoRoot(t)
	config, err := decodeConfig(filepath.Join(root, "deploy/environments/development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config.Auth.DemoUserID = "fixture-demo-user"
	if err := validateConfigForEnvironment("development", config); err != nil {
		t.Fatalf("valid Auth Demo UID: %v", err)
	}
	input := componentInputs(config, QueryConfigIdentity{}, []string{"auth"})["auth"].(map[string]any)
	if input["demo_user_id"] != config.Auth.DemoUserID {
		t.Fatalf("Auth component Demo UID = %#v", input["demo_user_id"])
	}
	config.Auth.DemoUserID = "bad|uid"
	if err := validateConfigForEnvironment("development", config); err == nil {
		t.Fatal("invalid Auth Demo UID was accepted")
	}
}

func TestPipelineDemoUserIDsAreDevelopmentOnlyAndValidated(t *testing.T) {
	root := repoRoot(t)
	dev, err := decodeConfig(filepath.Join(root, "deploy/environments/development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfigForEnvironment("development", dev); err != nil {
		t.Fatalf("valid DEV Demo IDs: %v", err)
	}
	prod, err := decodeConfig(filepath.Join(root, "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	prod.BFF.PipelineDemoUserIDs = []string{"e492f6bdaf1735e12b2de96d"}
	if err := validateConfigForEnvironment("production", prod); err == nil {
		t.Fatal("Production unexpectedly accepted DEV Demo IDs")
	}
	for _, ids := range [][]string{{"bad,id"}, {"bad|id"}, {" duplicate ", "duplicate"}, {""}} {
		dev.BFF.PipelineDemoUserIDs = ids
		if err := validateConfigForEnvironment("development", dev); err == nil {
			t.Fatalf("invalid DEV Demo IDs accepted: %#v", ids)
		}
	}
}

func TestProfileRuntimeConfigSupportsBothEnvironmentsAndPinsNumericSecretVersion(t *testing.T) {
	root := repoRoot(t)
	dev, err := decodeConfig(filepath.Join(root, "deploy/environments/development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dev.BFF.ProfileRuntimeAudience = "https://profile-dispatch.dev.example.invalid"
	dev.BFF.ProfileRuntimeServiceAccount = "lwc-profile-dispatcher-dev@llm-wiki-cloud.iam.gserviceaccount.com"
	dev.BFF.SecretReferences.TypeSafeJevAPIKey = &VersionedSecretReference{Name: "typesafe-jev-api-key-dev-test", Version: "7"}
	if err := validateConfigForEnvironment("development", dev); err != nil {
		t.Fatalf("valid synthetic DEV Profile runtime config: %v", err)
	}
	for name, alter := range map[string]func(*EnvironmentConfig){
		"missing audience": func(c *EnvironmentConfig) { c.BFF.ProfileRuntimeAudience = "" },
		"non-HTTPS audience": func(c *EnvironmentConfig) {
			c.BFF.ProfileRuntimeAudience = "http://profile-dispatch.dev.example.invalid"
		},
		"path-bearing audience": func(c *EnvironmentConfig) {
			c.BFF.ProfileRuntimeAudience = "https://profile-dispatch.dev.example.invalid/dispatch"
		},
		"missing invoker":       func(c *EnvironmentConfig) { c.BFF.ProfileRuntimeServiceAccount = "" },
		"latest secret version": func(c *EnvironmentConfig) { c.BFF.SecretReferences.TypeSafeJevAPIKey.Version = "latest" },
		"credential-shaped secret reference": func(c *EnvironmentConfig) {
			c.BFF.SecretReferences.TypeSafeJevAPIKey.Name = "ghp_not-a-secret-reference"
		},
		"missing secret": func(c *EnvironmentConfig) { c.BFF.SecretReferences.TypeSafeJevAPIKey = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := dev
			candidate.BFF.SecretReferences.TypeSafeJevAPIKey = &VersionedSecretReference{Name: "typesafe-jev-api-key-dev-test", Version: "7"}
			alter(&candidate)
			if err := validateConfigForEnvironment("development", candidate); err == nil {
				t.Fatal("invalid DEV Profile runtime config unexpectedly passed")
			}
		})
	}
	prod, err := decodeConfig(filepath.Join(root, "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	prod.BFF.ProfileRuntimeAudience = "https://llm-wiki-bff-a5nkmux6pq-de.a.run.app"
	prod.BFF.ProfileRuntimeServiceAccount = "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com"
	prod.BFF.SecretReferences.TypeSafeJevAPIKey = &VersionedSecretReference{Name: "typesafe-jev-api-key-prod", Version: "17"}
	if err := validateConfigForEnvironment("production", prod); err != nil {
		t.Fatalf("valid synthetic Production Profile runtime config: %v", err)
	}
	prod.BFF.ProfileRuntimeAudience = dev.BFF.ProfileRuntimeAudience
	if err := validateConfigForEnvironment("production", prod); err == nil {
		t.Fatal("Production accepted the DEV Profile runtime audience")
	}
}

func TestProductionProfileBindingsAreRequiredOnlyForBFFDeployment(t *testing.T) {
	root := repoRoot(t)
	config, err := decodeConfig(filepath.Join(root, "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	unprovisioned := config
	unprovisioned.BFF.ProfileRuntimeAudience = ""
	unprovisioned.BFF.ProfileRuntimeServiceAccount = ""
	unprovisioned.BFF.SecretReferences.TypeSafeJevAPIKey = nil
	if err := validateConfigForEnvironment("production", unprovisioned); err != nil {
		t.Fatalf("Production non-BFF config without Profile bindings remains valid: %v", err)
	}
	if err := validateProfileRuntimeConfig("production", unprovisioned); err == nil {
		t.Fatal("Production accepted missing Profile runtime configuration for BFF deployment")
	}
	if _, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "worker"); err != nil {
		t.Fatalf("Production Worker plan remains loadable: %v", err)
	}
	if _, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "bff"); err != nil {
		t.Fatalf("Production BFF plan with reviewed Profile runtime configuration failed: %v", err)
	}
}

func TestParseComponentsIsExplicitAndDeterministic(t *testing.T) {
	got, err := parseComponents("frontend, exportjob, bff")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "bff,exportjob,frontend" {
		t.Fatalf("components = %v", got)
	}
	for _, raw := range []string{"", "bff,", "bff,bff", "all", "auth,unknown"} {
		if _, err := parseComponents(raw); err == nil {
			t.Fatalf("parseComponents(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestExportJobConfigSupportsBothEnvironmentContracts(t *testing.T) {
	root := repoRoot(t)
	dev, err := decodeConfig(filepath.Join(root, "deploy/environments/development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfigForEnvironment("development", dev); err != nil {
		t.Fatalf("provisioned DEV export job config: %v", err)
	}
	if !dev.ExportJob.Enabled || dev.ExportJob.JobName != "export-job-dev" ||
		dev.ExportJob.RuntimeServiceAccount != "lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com" ||
		dev.ExportJob.Bucket != "llm-wiki-data-dev" || dev.ExportJob.FirestoreDatabaseID != "llm-wiki-cloud-dev" ||
		dev.ExportJob.Location != "asia-east1" ||
		dev.ExportJob.SigningServiceAccount != "lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com" ||
		dev.ExportJob.JobTimeout != "23h" || dev.ExportJob.MaxRetries != 0 || dev.ExportJob.Parallelism != 1 || dev.ExportJob.Tasks != 1 {
		t.Fatalf("DEV export job config = %#v", dev.ExportJob)
	}
	prod, err := decodeConfig(filepath.Join(root, "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfigForEnvironment("production", prod); err != nil {
		t.Fatalf("reviewed Production export job config: %v", err)
	}
	if !prod.ExportJob.Enabled || prod.ExportJob.JobName != "export-job" ||
		prod.ExportJob.RuntimeServiceAccount != "lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
		prod.ExportJob.Bucket != "llm-wiki-data" || prod.ExportJob.FirestoreDatabaseID != "llm-wiki-cloud-prod" ||
		prod.ExportJob.Location != "asia-east1" ||
		prod.ExportJob.SigningServiceAccount != "lwc-export-signer-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
		prod.ExportJob.JobTimeout != "23h" || prod.ExportJob.MaxRetries != 0 || prod.ExportJob.Parallelism != 1 || prod.ExportJob.Tasks != 1 {
		t.Fatalf("Production export job config = %#v", prod.ExportJob)
	}
	productionInputs := componentInputs(prod, QueryConfigIdentity{}, []string{"bff", "exportjob"})
	exportInput, ok := productionInputs["exportjob"].(ExportJobConfig)
	if !ok || !reflect.DeepEqual(exportInput, prod.ExportJob) {
		t.Fatalf("Production component inputs omitted Export Job config: %#v", productionInputs)
	}
	if _, err := Load("development", filepath.Join(root, "deploy/environments/development.yaml"), "bff,exportjob"); err != nil {
		t.Fatalf("provisioned DEV export job selection: %v", err)
	}
	if _, err := Load("development", filepath.Join(root, "deploy/environments/development.yaml"), "exportjob"); err == nil {
		t.Fatal("DEV Export Job deployment unexpectedly bypassed its BFF invocation config")
	}
	if _, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "bff,exportjob"); err != nil {
		t.Fatalf("reviewed Production export job selection: %v", err)
	}
	if _, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "exportjob"); err == nil {
		t.Fatal("Production Export Job deployment unexpectedly bypassed its BFF invocation config")
	}
}

func TestDecodeAndQueryPathValidationFailClosed(t *testing.T) {
	root := repoRoot(t)
	valid := filepath.Join(root, "deploy/environments/development.yaml")
	content, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string]string{
		"unknown key":      "unexpected: value\n",
		"missing query":    "query_config: \"\"",
		"wrong type":       "dev_jwt: nope",
		"secret value key": "api_token: ghp_not-a-token",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			text := string(content)
			switch name {
			case "unknown key", "secret value key":
				text = replacement + text
			case "missing query":
				text = strings.Replace(text, "query_config: apps/bff/configs/query/dev/query-dev-2026-09-12.1.json", replacement, 1)
			case "wrong type":
				text = strings.Replace(text, "dev_jwt: false", replacement, 1)
			}
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := decodeConfig(path)
			if name == "unknown key" || name == "wrong type" || name == "secret value key" {
				if err == nil {
					t.Fatal("decode unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateConfig(config); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
	for _, path := range []string{"/tmp/query.json", "../apps/bff/configs/query/dev/query.json", "apps/bff/configs/other.json"} {
		if _, err := loadQueryConfig(root, path); err == nil {
			t.Fatalf("loadQueryConfig(%q) unexpectedly succeeded", path)
		}
	}
}
