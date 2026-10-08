package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	runtimeconfig "github.com/rayer/llm-wiki-bff/internal/config"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("../../../.."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFrontendSelectionSeparatesRuntimeEndpointsFromBuildIdentity(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		environment string
		configURL   string
		apiURL      string
		authURL     string
	}{
		{"development", "https://storage.googleapis.com/llm-wiki-frontend-config-dev/frontend-config.json", "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app", "https://auth.dev.rayer.idv.tw"},
		{"production", "https://storage.googleapis.com/llm-wiki-frontend-config-prod/frontend-config.json", "https://llm-wiki-bff-580854833715.asia-east1.run.app", "https://auth.rayer.idv.tw"},
	} {
		t.Run(test.environment, func(t *testing.T) {
			config, err := Load(test.environment, filepath.Join(root, "deploy/environments", test.environment+".yaml"), "frontend")
			if err != nil {
				t.Fatal(err)
			}
			if config.Frontend.ConfigSchemaVersion != 1 || config.Frontend.ConfigURL != test.configURL ||
				config.Frontend.APIURL != test.apiURL || config.Frontend.AuthURL != test.authURL {
				t.Fatalf("selected Frontend config = %#v", config.Frontend)
			}
			if !reflect.DeepEqual(config.Components["frontend"], map[string]any{
				"project_name": config.Frontend.ProjectName, "team_slug": config.Frontend.TeamSlug,
				"repository": config.Frontend.Repository, "root_directory": config.Frontend.RootDirectory,
				"stable_aliases": config.Frontend.StableAliases, "config_schema_version": 1,
				"config_url": test.configURL,
			}) {
				t.Fatalf("Frontend build target config = %#v", config.Components["frontend"])
			}
			planBytes, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(planBytes), `"api_url"`) || strings.Contains(string(planBytes), `"auth_url"`) {
				t.Fatalf("runtime endpoints leaked into normalized build identity: %s", planBytes)
			}

			output := filepath.Join(t.TempDir(), "nested", "frontend-config.json")
			if err := writePublicFrontendRuntimeConfig(output, test.environment, config.Frontend.APIURL, config.Frontend.AuthURL); err != nil {
				t.Fatal(err)
			}
			var runtimeConfig PublicFrontendRuntimeConfig
			if err := json.Unmarshal(mustRead(t, output), &runtimeConfig); err != nil {
				t.Fatal(err)
			}
			if runtimeConfig != (PublicFrontendRuntimeConfig{SchemaVersion: 1, APIURL: test.apiURL, AuthURL: test.authURL}) {
				t.Fatalf("generated runtime config = %#v", runtimeConfig)
			}
		})
	}
}

func TestLocalFrontendConfigUsesOnlyLoopbackPorts(t *testing.T) {
	if !validLocalPort("19080") || validLocalPort("0") || validLocalPort("65536") || validLocalPort("80x") {
		t.Fatal("local port validation did not enforce the numeric TCP range")
	}
	output := filepath.Join(t.TempDir(), "frontend-config.json")
	if err := writePublicFrontendRuntimeConfig(output, "local", "http://localhost:19080", "http://localhost:19081"); err != nil {
		t.Fatal(err)
	}
	if err := writePublicFrontendRuntimeConfig(output, "local", "https://api.example.test", "http://localhost:19081"); err == nil {
		t.Fatal("local config unexpectedly accepted a deployed endpoint")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadReviewedEnvironmentsAndQueryIdentity(t *testing.T) {
	root := repoRoot(t)
	for _, environment := range []string{"development", "production"} {
		cooldown := 600
		if environment == "production" {
			cooldown = 3600
		}
		bffInputs := writeBFFDescriptorFixture(t, environment, cooldown)
		authInputs := writeAuthInputFixture(t, environment)
		config, err := LoadWithRuntimeInputs(environment, filepath.Join(root, "deploy/environments", environment+".yaml"), "auth,bff,worker,frontend", bffInputs, authInputs)
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
		if !ok || auth["demo_user_id"] != config.Auth.DemoUserID ||
			auth["demo_user_email"] != config.Auth.DemoUserEmail || auth["demo_user_role"] != config.Auth.DemoUserRole {
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
			if !reflect.DeepEqual(config.BFF.PipelineDemoUserIDs, []string{config.Auth.DemoUserID}) ||
				!reflect.DeepEqual(bff["pipeline_demo_user_ids"], config.BFF.PipelineDemoUserIDs) {
				t.Fatalf("DEV BFF component input did not project Auth Demo ID: %#v", bff)
			}
			secretRefs, ok := bff["secret_references"].(map[string]any)
			if !ok || secretRefs["typesafe_jev_api_key"] != ref {
				t.Fatalf("DEV BFF component input omitted TypeSafe secret binding: %#v", secretRefs)
			}
		} else if !reflect.DeepEqual(config.BFF.PipelineDemoUserIDs, []string{config.Auth.DemoUserID}) ||
			!reflect.DeepEqual(bff["pipeline_demo_user_ids"], config.BFF.PipelineDemoUserIDs) {
			t.Fatalf("Production BFF component input did not project its Auth Demo ID: %#v", bff)
		} else if config.BFF.ProfileRuntimeAudience != "https://llm-wiki-bff-a5nkmux6pq-de.a.run.app" ||
			config.BFF.ProfileRuntimeServiceAccount != "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey == nil ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey.Name != "typesafe-jev-api-key-prod" ||
			config.BFF.SecretReferences.TypeSafeJevAPIKey.Version != "1" ||
			!secretVersionPattern.MatchString(config.BFF.SecretReferences.TypeSafeJevAPIKey.Version) {
			t.Fatalf("Production Profile runtime bindings are incomplete or invalid: %#v", config.BFF)
		} else if bff["profile_runtime_audience"] != config.BFF.ProfileRuntimeAudience || bff["profile_runtime_service_account"] != config.BFF.ProfileRuntimeServiceAccount {
			t.Fatalf("Production BFF component input omitted Profile runtime bindings: %#v", bff)
		}
		worker, ok := config.Components["worker"].(map[string]any)
		if !ok || worker["args"] == nil || worker["secret_references"] == nil {
			t.Fatalf("%s worker component input omitted behavior-bearing config: %#v", environment, config.Components["worker"])
		}
	}
}

func TestGeneratedBFFInputsFlowIntoNormalizedPlanIdentity(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		environment string
		cooldown    int
	}{
		{"development", 600},
		{"production", 3600},
	} {
		path := writeBFFDescriptorFixture(t, tc.environment, tc.cooldown)
		configPath := filepath.Join(root, "deploy/environments", tc.environment+".yaml")
		plan, err := LoadWithBFFInputs(tc.environment, configPath, "bff", path)
		if err != nil {
			t.Fatalf("LoadWithBFFInputs(%s): %v", tc.environment, err)
		}
		bff, ok := plan.Components["bff"].(map[string]any)
		if !ok || plan.BFF.PipelineCooldownSeconds != tc.cooldown || bff["pipeline_cooldown_seconds"] != tc.cooldown || plan.BFF.RuntimeInputs == nil {
			t.Fatalf("%s normalized BFF cooldown=%d component=%#v", tc.environment, plan.BFF.PipelineCooldownSeconds, bff)
		}
		if plan.Evidence.ConfigFingerprint == "" {
			t.Fatalf("%s omitted normalized plan fingerprint", tc.environment)
		}
		if tc.environment == "development" {
			changedPath := writeBFFDescriptorFixture(t, tc.environment, 601)
			changed, err := LoadWithBFFInputs(tc.environment, configPath, "bff", changedPath)
			if err != nil {
				t.Fatal(err)
			}
			if changed.Evidence.ConfigFingerprint == plan.Evidence.ConfigFingerprint {
				t.Fatal("normalized plan identity ignored the generated BFF cooldown")
			}
		}
	}
}

func TestGeneratedBFFDescriptorRejectsInvalidInputs(t *testing.T) {
	root := repoRoot(t)
	configPath := filepath.Join(root, "deploy/environments/development.yaml")
	for _, mutate := range []func(map[string]any){
		func(value map[string]any) { value["schema_version"] = 1 },
		func(value map[string]any) { value["environment"] = "prod" },
		func(value map[string]any) { value["config_secret_resource"] = "projects/llm-wiki-cloud/secrets/wrong" },
	} {
		path := writeBFFDescriptorFixture(t, "development", 600)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		mutate(value)
		data, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWithRuntimeInputs("development", configPath, "auth,bff", path, writeAuthInputFixture(t, "development")); err == nil {
			t.Error("LoadWithBFFInputs accepted invalid descriptor")
		}
	}
}

func writeBFFDescriptorFixture(t *testing.T, environment string, cooldown int) string {
	t.Helper()
	root := repoRoot(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "bff-inputs.json")
	target := "dev"
	if environment == "production" {
		target = "prod"
	}
	command := exec.Command("go", "run", "./cmd/pipeline_config", "prepare", "--target", "bff", "--descriptor", "--environment", target, "--output", directory)
	command.Dir = filepath.Join(root, "apps/bff")
	command.Env = append(os.Environ(), "LWC_REPOSITORY_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate BFF descriptor: %v\n%s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	value["pipeline_cooldown_seconds"] = cooldown
	data, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func authInputFixture(environment string) runtimeconfig.AuthInputSnapshot {
	target, database, domain, hosts, origins, googleID, googleSecret := "dev", "llm-wiki-cloud-dev", "auth.dev.rayer.idv.tw",
		[]string{"auth.dev.rayer.idv.tw", "auth-dev.rayer.idv.tw"},
		[]string{"https://wiki.dev.rayer.idv.tw", "https://llm-wiki-frontend-dev.vercel.app", "http://localhost:3000"},
		"580854833715-vo7fg6f7f15g1kkgchk1ulccllbc24qg.apps.googleusercontent.com", "google-oauth-client-dev"
	if environment == "production" {
		target, database, domain, hosts, origins, googleID, googleSecret = "prod", "llm-wiki-cloud-prod", "auth.rayer.idv.tw",
			[]string{"auth.rayer.idv.tw"},
			[]string{"https://wiki.rayer.idv.tw", "https://llm-wiki-frontend.vercel.app"},
			"580854833715-1b37asap0uocbdcrighjaorflvj2n94m.apps.googleusercontent.com", "google-oauth-client-prod"
	}
	inputs := runtimeconfig.AuthInputSnapshot{
		SchemaVersion: 1, Environment: target, Target: "auth", SourceSHA: strings.Repeat("a", 40),
		GCPProject: "llm-wiki-cloud", FirestoreDatabaseID: database, AuthServiceURL: "https://" + domain,
		AllowedHosts: hosts, AllowedOrigins: origins, AuthSessionEnvironment: database,
		AuthSessionMigration: "disabled", AuthDemoUserID: "e492f6bdaf1735e12b2de96d",
		AuthDemoUserEmail: "demo@llm-wiki.dev", AuthDemoUserRole: "member",
		JWTSecretVersion: "projects/llm-wiki-cloud/secrets/jwt-secret-" + target + "/versions/3",
		Google: runtimeconfig.AuthInputGoogle{
			Enabled: true, ClientID: googleID,
			ClientSecretVersion: "projects/llm-wiki-cloud/secrets/" + googleSecret + "/versions/1",
			Issuer:              "https://accounts.google.com", JWKSURL: "https://www.googleapis.com/oauth2/v3/certs",
			TokenURL: "https://oauth2.googleapis.com/token", LoginRedirectURL: "https://" + domain + "/api/v1/auth/google/callback",
			LinkRedirectURL: "https://" + domain + "/api/v1/auth/google/link/callback",
			CompletionURL:   "https://" + strings.Replace(domain, "auth.", "wiki.", 1) + "/login",
		},
		ConfigSecretResource: "projects/llm-wiki-cloud/secrets/lwc-auth-app-config-" + target,
	}
	if environment == "production" {
		inputs.Google.CompletionURL = "https://wiki.rayer.idv.tw/login"
	}
	inputs.ConfigID, _ = runtimeconfig.AuthInputConfigID(inputs)
	return inputs
}

func writeAuthInputFixture(t *testing.T, environment string) string {
	t.Helper()
	inputs := authInputFixture(environment)
	return writeAuthInputValue(t, inputs)
}

func writeAuthInputValue(t *testing.T, inputs runtimeconfig.AuthInputSnapshot) string {
	t.Helper()
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-inputs.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthDemoIdentityConfigIsRequiredAndRendered(t *testing.T) {
	root := repoRoot(t)
	configPath := filepath.Join(root, "deploy/environments/development.yaml")
	inputs := authInputFixture("development")
	plan, err := LoadWithAuthInputs("development", configPath, "auth", writeAuthInputValue(t, inputs))
	if err != nil {
		t.Fatalf("configured Auth Demo identity: %v", err)
	}
	input := plan.Components["auth"].(map[string]any)
	if input["demo_user_id"] != inputs.AuthDemoUserID || input["demo_user_email"] != inputs.AuthDemoUserEmail || input["demo_user_role"] != inputs.AuthDemoUserRole {
		t.Fatalf("Auth component Demo identity = %#v", input)
	}
	for name, mutate := range map[string]func(*runtimeconfig.AuthInputSnapshot){
		"missing UID": func(v *runtimeconfig.AuthInputSnapshot) { v.AuthDemoUserID = "" },
		"invalid UID": func(v *runtimeconfig.AuthInputSnapshot) { v.AuthDemoUserID = "bad|uid" },
		"admin role":  func(v *runtimeconfig.AuthInputSnapshot) { v.AuthDemoUserRole = "admin" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := inputs
			mutate(&bad)
			bad.ConfigID, _ = runtimeconfig.AuthInputConfigID(bad)
			if _, err := LoadWithAuthInputs("development", configPath, "auth", writeAuthInputValue(t, bad)); err == nil {
				t.Fatal("invalid Auth Demo identity was accepted")
			}
		})
	}
}

func TestPipelineDemoUserIDsMustMatchTheConfiguredAuthIdentity(t *testing.T) {
	root := repoRoot(t)
	configPath := filepath.Join(root, "deploy/environments/development.yaml")
	for _, ids := range [][]string{{"different-demo"}, {"bad,id"}, {"bad|id"}, {" duplicate ", "duplicate"}, {""}} {
		path := writeBFFDescriptorFixture(t, "development", 600)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var descriptor map[string]any
		if err := json.Unmarshal(data, &descriptor); err != nil {
			t.Fatal(err)
		}
		descriptor["pipeline_demo_user_ids"] = ids
		data, err = json.Marshal(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWithRuntimeInputs("development", configPath, "auth,bff", path, writeAuthInputFixture(t, "development")); err == nil {
			t.Fatalf("mismatched or invalid Demo IDs accepted: %#v", ids)
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
	if err := validateProfileRuntimeConfig("development", dev); err != nil {
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
			if err := validateProfileRuntimeConfig("development", candidate); err == nil {
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
	if err := validateProfileRuntimeConfig("production", prod); err != nil {
		t.Fatalf("valid synthetic Production Profile runtime config: %v", err)
	}
	prod.BFF.ProfileRuntimeAudience = dev.BFF.ProfileRuntimeAudience
	if err := validateProfileRuntimeConfig("production", prod); err == nil {
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
	if _, err := LoadWithAuthInputs("production", filepath.Join(root, "deploy/environments/production.yaml"), "auth", writeAuthInputFixture(t, "production")); err != nil {
		t.Fatalf("Production Auth-only plan remains loadable without BFF input resolution: %v", err)
	}
	workerPlan, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "worker")
	if err != nil {
		t.Fatalf("Production Worker plan must use its own reviewed config without BFF inputs: %v", err)
	}
	worker, ok := workerPlan.Components["worker"].(map[string]any)
	if !ok || worker["secret_references"] == nil || workerPlan.BFF.RuntimeInputs != nil ||
		workerPlan.BFF.FirestoreDatabaseID != config.Auth.FirestoreDatabaseID {
		t.Fatalf("Production Worker-only plan unexpectedly consumed BFF inputs: %#v", workerPlan)
	}
	if _, err := LoadWithBFFInputs("production", filepath.Join(root, "deploy/environments/production.yaml"), "bff",
		writeBFFDescriptorFixture(t, "production", 3600)); err != nil {
		t.Fatalf("Production BFF plan with generated runtime inputs failed: %v", err)
	}
	if _, err := LoadWithBFFInputs("production", filepath.Join(root, "deploy/environments/production.yaml"), "worker",
		writeBFFDescriptorFixture(t, "production", 3600)); err == nil {
		t.Fatal("Production Worker-only plan accepted an unrelated BFF descriptor")
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
	if _, err := LoadWithBFFInputs("development", filepath.Join(root, "deploy/environments/development.yaml"), "bff,exportjob",
		writeBFFDescriptorFixture(t, "development", 600)); err != nil {
		t.Fatalf("provisioned DEV export job selection: %v", err)
	}
	if _, err := Load("development", filepath.Join(root, "deploy/environments/development.yaml"), "exportjob"); err == nil {
		t.Fatal("DEV Export Job deployment unexpectedly bypassed its BFF invocation config")
	}
	if _, err := LoadWithBFFInputs("production", filepath.Join(root, "deploy/environments/production.yaml"), "bff,exportjob",
		writeBFFDescriptorFixture(t, "production", 3600)); err != nil {
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
		"wrong type":       "max_instances: nope",
		"secret value key": "api_token: ghp_not-a-token",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			text := string(content)
			switch name {
			case "unknown key", "secret value key":
				text = replacement + text
			case "wrong type":
				text = strings.Replace(text, "max_instances: 1", replacement, 1)
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
