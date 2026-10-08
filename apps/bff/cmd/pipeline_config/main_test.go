package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	runtimeconfig "github.com/rayer/llm-wiki-bff/internal/config"
	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/secretmanager/v1"
)

type fakeSecretReader struct {
	resource      string
	resources     []string
	resolved      string
	value         []byte
	err           error
	identity      projectIdentity
	identityErr   error
	identityCalls int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (r *fakeSecretReader) Access(_ context.Context, resource string) (string, []byte, error) {
	r.resource = resource
	r.resources = append(r.resources, resource)
	resolved := r.resolved
	if resolved == "" {
		resolved = resource
		if strings.HasSuffix(resource, "/latest") {
			resolved = strings.TrimSuffix(resource, "latest") + "1"
		}
	}
	return resolved, r.value, r.err
}

func TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for real BFF prepare acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}

	root := t.TempDir()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_LOCAL_LLM_KEY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TYPESAFE_JEV_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")

	localRoot := filepath.Join(root, "local")
	if err := os.MkdirAll(localRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	jwtPath := filepath.Join(localRoot, "synthetic-jwt-key")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc369-synthetic-acceptance")
	t.Setenv("LOCAL_CLOUD_WORKER_PATH", filepath.Join(localRoot, "olw_worker"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_CONFIG_PATH", filepath.Join(localRoot, "synthetic-synto.toml"))
	t.Setenv("LOCAL_CLOUD_PIPELINE_BINDINGS_PATH", filepath.Join(localRoot, "synthetic-bindings.json"))
	t.Setenv("LOCAL_CLOUD_JWT_SECRET_FILE", jwtPath)
	t.Setenv("PIPELINE_DEMO_USER_IDS", "")
	t.Setenv("GCP_PROJECT", "llm-wiki-cloud")
	t.Setenv("BUCKET", "llm-wiki-cloud-local")
	t.Setenv("FIRESTORE_DATABASE_ID", "llm-wiki-cloud-local")
	t.Setenv("ALLOWED_HOSTS", "localhost,127.0.0.1")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:13000")
	t.Setenv("AUTH_SERVICE_URL", "http://localhost:18081")
	t.Setenv("DEV_JWT", "false")
	t.Setenv("LOCAL_DATA_DIR", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("PIPELINE_COOLDOWN_SECONDS", "")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")

	for _, tc := range []struct {
		environment string
		cooldown    int
		resources   []string
	}{
		{environment: "local", cooldown: 60},
		{environment: "dev", cooldown: 600, resources: []string{
			"projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions/latest",
			"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
			"projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-dev/versions/1",
		}},
		{environment: "prod", cooldown: 3600, resources: []string{
			"projects/llm-wiki-cloud/secrets/jwt-secret-prod/versions/latest",
			"projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest",
			"projects/llm-wiki-cloud/secrets/typesafe-jev-api-key-prod/versions/1",
		}},
	} {
		t.Run(tc.environment, func(t *testing.T) {
			reader := &fakeSecretReader{value: []byte("TEST_ONLY_SYNTHETIC_CLOUD_SECRET")}
			output := filepath.Join(root, tc.environment+"-output")
			if err := runPrepareTarget(context.Background(), "bff", tc.environment, output,
				func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
				t.Fatalf("real Pkl BFF prepare failed: %v", err)
			}
			if len(reader.resources) != len(tc.resources) {
				t.Fatalf("selected secret reads=%d, want %d", len(reader.resources), len(tc.resources))
			}
			for i := range tc.resources {
				if reader.resources[i] != tc.resources[i] {
					t.Fatalf("secret read %d did not match the selected BFF reference", i)
				}
			}
			data, err := os.ReadFile(filepath.Join(output, "bff.json"))
			if err != nil {
				t.Fatalf("prepared BFF file is unavailable: %v", err)
			}
			generated, err := runtimeconfig.DecodeBFFFile(data)
			if err != nil {
				t.Fatalf("prepared BFF schema 2 failed runtime validation: %v", err)
			}
			if generated.SchemaVersion != 2 || generated.Environment != tc.environment || generated.Target != "bff" ||
				generated.PipelineCooldownSeconds != tc.cooldown {
				t.Fatalf("prepared BFF identity or cooldown is incorrect for %s", tc.environment)
			}
			if generated.LLM.Provider != "deepseek" || generated.LLM.BaseURL != "https://api.deepseek.com" ||
				generated.LLM.RequestTimeoutSeconds != 60 || generated.LLM.Model != "deepseek-flash" {
				t.Fatalf("prepared BFF LLM connection options are incorrect for %s", tc.environment)
			}
			if tc.environment == "local" {
				if generated.PortDefault != 18080 || generated.AuthServiceURL != "http://localhost:18081" ||
					len(generated.AllowedOrigins) != 1 || generated.AllowedOrigins[0] != "http://localhost:13000" ||
					generated.Query.StageConfigPath != "" || generated.Query.Legacy == nil {
					t.Fatal("local BFF config lost selected ports or legacy query options")
				}
			} else if generated.PortDefault != 8080 || generated.Query.StageConfigPath != "/app/configs/query/dev/query-dev-2026-09-12.1.json" ||
				generated.Query.Legacy != nil {
				t.Fatal("cloud BFF config lost platform defaults or sealed query authority")
			}
			started, err := runtimeconfig.LoadBFFFile(filepath.Join(output, "bff.json"))
			if err != nil || started.GCPProject != generated.GCPProject ||
				started.Bucket != generated.Bucket || started.FirestoreDatabaseID != generated.FirestoreDatabaseID ||
				started.PipelineCooldownSeconds != tc.cooldown {
				t.Fatalf("startup loader did not preserve generated runtime settings for %s: err=%v", tc.environment, err)
			}
			if tc.environment == "local" {
				if generated.Local == nil || generated.Local.Scope != "lwc369-synthetic-acceptance" ||
					generated.JWTSecret != strings.Repeat("a", 64) || generated.DeepSeekAPIKey != "TEST_ONLY_LOCAL_LLM_KEY" {
					t.Fatal("local BFF config did not use the isolated synthetic fixtures")
				}
				legacyAuthConfig, err := runtimeconfig.Load(localRoot)
				if err != nil || legacyAuthConfig.JWTSecret != started.JWTSecret {
					t.Fatalf("Auth and BFF did not load the same local signing key: err=%v", err)
				}
				token, err := auth.GenerateAccessToken("lwc369-synthetic-user", "member", legacyAuthConfig.JWTSecret)
				if err != nil {
					t.Fatal("Auth could not issue a synthetic access token")
				}
				claims, err := auth.ValidateToken(token, started.JWTSecret)
				if err != nil || claims.Sub != "lwc369-synthetic-user" {
					t.Fatal("BFF could not validate an Auth token using the generated local config")
				}
			} else if generated.Local != nil || generated.JWTSecret != "TEST_ONLY_SYNTHETIC_CLOUD_SECRET" ||
				generated.DeepSeekAPIKey != "TEST_ONLY_SYNTHETIC_CLOUD_SECRET" ||
				generated.TypeSafeAPIKey != "TEST_ONLY_SYNTHETIC_CLOUD_SECRET" {
				t.Fatal("cloud BFF config did not use only the selected synthetic secret fixtures")
			}
			fileInfo, err := os.Stat(filepath.Join(output, "bff.json"))
			if err != nil || fileInfo.Mode().Perm() != 0o600 {
				t.Fatalf("BFF file permissions are not 0600: err=%v", err)
			}
			dirInfo, err := os.Stat(output)
			if err != nil || dirInfo.Mode().Perm() != 0o700 {
				t.Fatalf("BFF directory permissions are not 0700: err=%v", err)
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 1 || entries[0].Name() != "bff.json" {
				t.Fatalf("BFF prepare emitted unexpected artifacts: err=%v", err)
			}
		})
	}
}

func (r *fakeSecretReader) ProjectIdentity(_ context.Context, project string) (projectIdentity, error) {
	r.identityCalls++
	if r.identityErr != nil {
		return projectIdentity{}, r.identityErr
	}
	if !projectIdentityMatchesRequest(project, r.identity) {
		return projectIdentity{}, errProjectIdentityMismatch
	}
	return r.identity, nil
}

func TestEnvironmentBindingDoesNotInitializeSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_KEY", "TEST_ONLY_SECRET_PAYLOAD")
	called := false
	value, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, fmt.Errorf("unexpected Secret Manager initialization")
	})
	if err != nil || called || string(value) != "TEST_ONLY_SECRET_PAYLOAD" {
		t.Fatalf("resolveBinding() = (%q, %v), Secret Manager initialized=%t", value, err, called)
	}
}

func TestMissingLocalEnvironmentSecretFailsWithoutSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_MISSING_KEY", "")
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_MISSING_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, errors.New("unexpected Secret Manager initialization")
	})
	if err == nil || !strings.Contains(err.Error(), "required local Pipeline secret is missing") || called {
		t.Fatalf("missing local key error=%v Secret Manager initialized=%t", err, called)
	}
}

func TestSecretManagerBindingUsesOnlySelectedVersion(t *testing.T) {
	reader := &fakeSecretReader{value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const resource = "projects/test-project/secrets/test-secret/versions/7"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != resource || resolved != resource {
		t.Fatalf("selected Secret Manager version was not resolved: value=%q resource=%q err=%v", value, reader.resource, err)
	}
	clear(value)
	for _, b := range value {
		if b != 0 {
			t.Fatal("resolved synthetic secret buffer was not cleared")
		}
	}
}

func TestLatestSecretManagerBindingIsPinnedToResolvedNumericVersion(t *testing.T) {
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/19", value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const latest = "projects/test-project/secrets/test-secret/versions/latest"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: latest,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || resolved != reader.resolved || string(value) != "TEST_ONLY_GSM_PAYLOAD" {
		t.Fatalf("latest Secret Manager version was not pinned: resolved=%q err=%v", resolved, err)
	}
	clear(value)
}

func TestSecretManagerBindingAcceptsAuthoritativeNumericProjectAlias(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/latest"
	const numericResponse = "projects/580854833715/secrets/test-secret/versions/19"
	reader := &fakeSecretReader{
		resolved: numericResponse, value: []byte("TEST_ONLY_GSM_PAYLOAD"),
		identity: projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"},
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != requested ||
		reader.identityCalls != 1 || resolved != "projects/test-project/secrets/test-secret/versions/19" {
		t.Fatalf("numeric project alias was not authoritatively resolved: resource=%q version=%q identity_calls=%d err=%v",
			reader.resource, resolved, reader.identityCalls, err)
	}
	clear(value)
}

func TestSecretManagerNumericProjectAliasRejectsUnboundResponses(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	identity := projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"}
	for _, tc := range []struct {
		name           string
		resolved       string
		identity       projectIdentity
		identityErr    error
		emptyPayload   bool
		wantIdentity   int
		wantDiagnostic string
	}{
		{name: "wrong numeric project", resolved: "projects/999999999999/secrets/test-secret/versions/11", identity: identity, wantIdentity: 1},
		{name: "wrong secret", resolved: "projects/580854833715/secrets/other-secret/versions/11", identity: identity},
		{name: "wrong version", resolved: "projects/580854833715/secrets/test-secret/versions/12", identity: identity},
		{name: "malformed response", resolved: "projects/580854833715/secrets/test-secret/versions/latest", identity: identity},
		{name: "metadata identity mismatch", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: projectIdentity{ProjectID: "other-project", ProjectNumber: "580854833715"}, wantIdentity: 1,
			wantDiagnostic: "does not identify the selected project"},
		{name: "metadata unavailable", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, identityErr: errors.New("TEST_ONLY_METADATA_FAILURE"), wantIdentity: 1,
			wantDiagnostic: "project identity lookup failed"},
		{name: "empty payload precedes metadata lookup", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, emptyPayload: true, wantDiagnostic: "payload is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("TEST_ONLY_GSM_PAYLOAD")
			if tc.emptyPayload {
				payload = nil
			}
			reader := &fakeSecretReader{
				resolved: tc.resolved, value: payload,
				identity: tc.identity, identityErr: tc.identityErr,
			}
			_, _, err := resolveBinding(context.Background(), secretBinding{
				Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
			}, func(context.Context) (secretReader, error) { return reader, nil })
			if err == nil || strings.Contains(err.Error(), "TEST_ONLY_GSM_PAYLOAD") ||
				strings.Contains(err.Error(), "TEST_ONLY_METADATA_FAILURE") || reader.identityCalls != tc.wantIdentity {
				t.Fatalf("unbound response was accepted or leaked data: identity_calls=%d err=%v", reader.identityCalls, err)
			}
			if tc.wantDiagnostic != "" && !strings.Contains(err.Error(), tc.wantDiagnostic) {
				t.Fatalf("metadata failure lost its diagnostic class: %v", err)
			}
			for _, b := range reader.value {
				if b != 0 {
					t.Fatal("rejected synthetic secret buffer was not cleared")
				}
			}
		})
	}
}

func TestPreparedSecretReferenceUpdatesOnlyPublicAndPrivateBindings(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "pipeline.json")
	if err := os.WriteFile(path, []byte(`{"environment":"dev","secret":{"source":"secret-manager","target":"DEEPSEEK_API_KEY","envName":"","resource":"projects/test-project/secrets/test-secret/versions/latest"},"synto":{"ok":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const resolved = "projects/test-project/secrets/test-secret/versions/19"
	if err := bindResolvedSecretReference(temp, secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resolved}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	var binding secretBinding
	if err := json.Unmarshal(output["secret"], &binding); err != nil || binding.Resource != resolved {
		t.Fatalf("public prepared binding = %+v, err=%v", binding, err)
	}
	var synto map[string]bool
	if err := json.Unmarshal(output["synto"], &synto); err != nil || !synto["ok"] {
		t.Fatalf("unrelated public config changed: %s err=%v", output["synto"], err)
	}
}

func TestSecretManagerFailureIsExplicitAndDoesNotExposePayload(t *testing.T) {
	reader := &fakeSecretReader{err: fmt.Errorf("TEST_ONLY_SECRET_PAYLOAD denied")}
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY",
		Resource: "projects/test-project/secrets/test-secret/versions/3",
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "access failed") || strings.Contains(err.Error(), "TEST_ONLY_SECRET_PAYLOAD") {
		t.Fatalf("secret access failure was not explicit and redacted: %v", err)
	}
}

func TestInvalidSecretVersionFailsBeforeClientInitialization(t *testing.T) {
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Resource: "projects/test/secrets/test/versions/0",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "full secret version resource path") {
		t.Fatalf("invalid secret version was accepted: err=%v initialized=%t", err, called)
	}
}

func TestRunTimeoutMustBeExplicit(t *testing.T) {
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	if _, err := runTimeoutFromEnvironment(); err == nil || !strings.Contains(err.Error(), "verified Pipeline Job timeout") {
		t.Fatalf("missing timeout was not rejected clearly: %v", err)
	}
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "0")
	if _, err := runTimeoutFromEnvironment(); err == nil {
		t.Fatal("zero timeout was accepted")
	}
}

func TestRunPrepareBFFTargetDoesNotReadPipelineInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	t.Setenv("LLM_API_KEY", "TEST_ONLY_DEEPSEEK_KEY")
	t.Setenv("DEEPSEEK_API_KEY", "")
	jwtPath := filepath.Join(root, "jwt-secret")
	if err := os.WriteFile(jwtPath, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_TEST_BFF_JSON", localBFFSourceJSON(root, jwtPath, 60))
	readerCalled := false
	output := filepath.Join(root, "out")
	if err := runPrepareTarget(context.Background(), "bff", "local", output,
		func(context.Context) (secretReader, error) {
			readerCalled = true
			return nil, errors.New("BFF target must not resolve a secret")
		}); err != nil {
		t.Fatalf("runPrepareTarget(bff) = %v", err)
	}
	if readerCalled {
		t.Fatal("BFF target initialized a Pipeline secret reader")
	}
	data, err := os.ReadFile(filepath.Join(output, "bff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generated runtimeconfig.BFFFile
	if err := json.Unmarshal(data, &generated); err != nil || generated.SchemaVersion != 2 ||
		generated.Environment != "local" || generated.Target != "bff" || generated.PipelineCooldownSeconds != 60 ||
		generated.JWTSecret != strings.Repeat("a", 64) || generated.DeepSeekAPIKey != "TEST_ONLY_DEEPSEEK_KEY" ||
		generated.LLM.BaseURL != "https://api.deepseek.com" || generated.Local == nil {
		t.Fatalf("BFF runtime config identity or values are invalid: schema=%d environment=%s target=%s cooldown=%d local=%t", generated.SchemaVersion, generated.Environment, generated.Target, generated.PipelineCooldownSeconds, generated.Local != nil)
	}
	info, err := os.Stat(filepath.Join(output, "bff.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("BFF runtime config mode = %v, err=%v", info, err)
	}
	dirInfo, err := os.Stat(output)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("BFF runtime directory mode = %v, err=%v", dirInfo, err)
	}
	for _, name := range []string{"pipeline.json", "synto.toml", "private-bindings.json"} {
		if _, err := os.Stat(filepath.Join(output, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("BFF target emitted Pipeline artifact %s: err=%v", name, err)
		}
	}
}

func TestBFFDescriptorDoesNotResolveSecretsOrLeaveRuntimeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	jwtPath := filepath.Join(root, "jwt-secret")
	t.Setenv("LWC_TEST_BFF_JSON", localBFFSourceJSON(root, jwtPath, 60))
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "bff.json"), []byte("prior sensitive config"), 0o600); err != nil {
		t.Fatal(err)
	}
	readerCalled := false
	if err := runPrepareTargetMode(context.Background(), "bff", "local", output, true, func(context.Context) (secretReader, error) {
		readerCalled = true
		return nil, errors.New("descriptor mode must not resolve secrets")
	}); err != nil {
		t.Fatalf("descriptor prepare failed: %v", err)
	}
	if readerCalled {
		t.Fatal("descriptor mode initialized a Secret Manager reader")
	}
	if _, err := os.Stat(filepath.Join(output, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor mode left a launchable BFF file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "bff-inputs.json"))
	if err != nil || !strings.Contains(string(data), "jwt_secret_reference") || !strings.Contains(string(data), "deepseek_api_key_reference") {
		t.Fatalf("descriptor did not retain the selected reference metadata: err=%v", err)
	}
}

func TestInvalidBFFAttemptInvalidatesPriorRuntimeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkl := filepath.Join(root, "pkl-stub")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\nprintf '%s\\n' \"$LWC_TEST_BFF_JSON\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	invalid := localBFFSourceJSON(root, filepath.Join(root, "jwt-secret"), 0)
	t.Setenv("LWC_TEST_BFF_JSON", invalid)
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "bff.json"), []byte("prior config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPrepareTarget(context.Background(), "bff", "local", output, nil); err == nil {
		t.Fatal("invalid generated BFF config was accepted")
	}
	if _, err := os.Stat(filepath.Join(output, "bff.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed generation left a launchable prior BFF file: %v", err)
	}
}

func localBFFSourceJSON(root, jwtPath string, cooldown int) string {
	local := map[string]any{
		"scope": "worktree-0123456789abcdef01234567", "worker_path": filepath.Join(root, "olw_worker"),
		"pipeline_config_path": filepath.Join(root, "synto.toml"), "pipeline_bindings_path": filepath.Join(root, "private-bindings.json"),
	}
	value := map[string]any{
		"schema_version": 2, "environment": "local", "target": "bff", "gcp_project": "llm-wiki-cloud",
		"bucket": "llm-wiki-cloud-local", "firestore_database_id": "llm-wiki-cloud-local",
		"auth_service_url": "http://localhost:8081", "pipeline_job_url": "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline:run",
		"export_job_url": "", "export_signing_service_account": "", "allowed_origins": []string{"http://localhost:3000"},
		"allowed_hosts": []string{"localhost", "127.0.0.1"}, "pipeline_daily_limit": 2, "pipeline_cooldown_seconds": cooldown,
		"pipeline_min_new_raw": 1, "pipeline_demo_user_ids": []string{}, "auth_session_environment": "llm-wiki-cloud-local",
		"auth_session_migration": "disabled", "registration_enabled": nil,
		"jwt_secret_reference":       map[string]any{"source": "file", "resource": jwtPath},
		"deepseek_api_key_reference": map[string]any{"source": "environment", "env_name": "LLM_API_KEY"},
		"profile_runtime_audience":   "", "profile_runtime_service_account": "",
		"llm": map[string]any{"provider": "deepseek", "base_url": "https://api.deepseek.com/v1/", "request_timeout_seconds": 60, "model": "deepseek-flash"},
		"query": map[string]any{"stage_config_path": "", "legacy": map[string]any{
			"query_expansion_model": "deepseek-flash", "query_expansion_reasoning": "none", "answer_synthesis_model": "deepseek-flash",
			"answer_synthesis_reasoning": "none", "query_selection_limit": 10, "query_selection_exploration_slots": 1,
			"query_selection_evidence_threshold": 2, "query_expansion_keywords_per_attempt": 24, "query_expansion_attempts": 3,
			"query_matching_rare_keyword_max_document_frequency": 1,
		}},
		"local": local, "port_default": 8080, "config_secret_resource": "",
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func TestRunPrepareRendersOnlyThePreparedProjection(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("LWC_TEST_REAL_PKL"))
	if realPKL == "" {
		if found, err := exec.LookPath("pkl"); err == nil {
			realPKL = found
		} else if _, err := os.Stat("/Users/rayer/.local/bin/pkl"); err == nil {
			realPKL = "/Users/rayer/.local/bin/pkl"
		}
	}
	if realPKL == "" {
		t.Skip("Pkl is unavailable")
	}
	cacheDir := strings.TrimSpace(os.Getenv("PKL_CACHE_DIR"))
	if cacheDir == "" {
		if _, err := os.Stat("/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"); err == nil {
			cacheDir = "/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"
		}
	}
	if cacheDir == "" {
		t.Skip("locked Pkl package cache is unavailable")
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Skip("locked Pkl package cache is unavailable")
	}

	sourceDir := filepath.Join("..", "..", "..", "..", "deploy", "cac")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "ssot.pkl" {
			content := string(data)
			for _, replacement := range [][2]string{
				{`provider = "deepseek"`, `provider = "selected-provider"`},
				{`endpoint = "https://api.deepseek.com/v1"`, `endpoint = "https://selected.example.invalid/v1"`},
				{`model = "deepseek-flash"`, `model = "selected-model"`},
				{`requestTimeoutSeconds = 600`, `requestTimeoutSeconds = 437`},
				{`pipelineArticleMaxTokens: Int? = read?("prop:pipelineArticleMaxTokens")?.toInt()`,
					`pipelineArticleMaxTokens: Int? = 24680`},
			} {
				if !strings.Contains(content, replacement[0]) {
					t.Fatalf("SSOT fixture did not contain selected profile override %q", replacement[0])
				}
				content = strings.Replace(content, replacement[0], replacement[1], 1)
			}
			data = []byte(content)
		}
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := filepath.Join(root, "pkl-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nset -eu\nlast=\nfor arg do last=\"$arg\"; done\nif [ \"$last\" = \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\" ]; then\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\n  printf '%s\\n' 'module mutated' > \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\"\nelse\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\nfi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", wrapper)
	t.Setenv("LWC_TEST_REAL_PKL", realPKL)
	t.Setenv("PKL_CACHE_DIR", cacheDir)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "projects/test-project/secrets/test-secret/versions/latest")
	const payload = "TEST_ONLY_LOCAL_GSM_PAYLOAD"
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/7", value: []byte(payload)}
	if err := runPrepare(context.Background(), "local", output,
		func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
		t.Fatalf("runPrepare() with synthetic local GSM input: %v", err)
	}
	mutated, err := os.ReadFile(filepath.Join(root, "deploy", "cac", "ssot.pkl"))
	if err != nil || string(mutated) != "module mutated\n" {
		t.Fatalf("SSOT fixture was not mutated between evaluate and render: %q err=%v", mutated, err)
	}
	tomlBytes, err := os.ReadFile(filepath.Join(output, "synto.toml"))
	if err != nil || !strings.Contains(string(tomlBytes), "article_max_tokens = 24680") ||
		!strings.Contains(string(tomlBytes), "run_timeout_seconds = 23") || strings.Contains(string(tomlBytes), payload) {
		t.Fatalf("render did not use the prepared projection/override: %s err=%v", tomlBytes, err)
	}
	for _, selected := range []string{
		`name = "selected-provider"`, `url = "https://selected.example.invalid/v1"`,
		`timeout = 437`, `model = "selected-model"`, `article_max_tokens = 24680`,
	} {
		if !strings.Contains(string(tomlBytes), selected) {
			t.Fatalf("render did not use selected SSOT profile/override %q: %s", selected, tomlBytes)
		}
	}
	publicBytes, err := os.ReadFile(filepath.Join(output, "pipeline.json"))
	if err != nil || strings.Contains(string(publicBytes), payload) {
		t.Fatalf("public config contains local secret payload: err=%v", err)
	}
	for _, selected := range []string{"selected-provider", "https://selected.example.invalid/v1", "selected-model"} {
		if !strings.Contains(string(publicBytes), selected) {
			t.Fatalf("public Pipeline projection lost selected profile value %q", selected)
		}
	}
	privateInfo, err := os.Stat(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private bindings mode=%v, want 0600", privateInfo.Mode())
	}
	privateBytes, err := os.ReadFile(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var private privateBindings
	if err := json.Unmarshal(privateBytes, &private); err != nil || string(private.LocalAPIKey) != payload {
		t.Fatalf("private local binding payload=%q err=%v", private.LocalAPIKey, err)
	}
	clear(private.LocalAPIKey)
}

func TestGoogleSecretManagerSDKAccessPath(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":"%s","payload":{"data":"%s"}}`, resource, base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) {
		return googleSecretReader{service: service}, nil
	})
	if err != nil || string(value) != payload || resolved != resource {
		t.Fatalf("Google Secret Manager client response = %q version=%q, %v", value, resolved, err)
	}
	clear(value)
}

func TestGoogleSecretManagerSDKResolvesCanonicalNumericProjectName(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	const canonical = "projects/580854833715/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	accessCalls, projectCalls := 0, 0
	secretHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		accessCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+requested+":access" {
			return nil, fmt.Errorf("unexpected Secret Manager request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":%q,"payload":{"data":%q}}`, canonical,
			base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	projectHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		projectCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v3/projects/test-project" {
			return nil, fmt.Errorf("unexpected Cloud Resource Manager request %s %s", request.Method, request.URL.Path)
		}
		body := `{"name":"projects/580854833715","projectId":"test-project","state":"ACTIVE"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	secretService, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"),
		option.WithHTTPClient(secretHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	projectService, err := cloudresourcemanager.NewService(context.Background(), option.WithEndpoint("https://cloudresourcemanager.test/"),
		option.WithHTTPClient(projectHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	reader := googleSecretReader{service: secretService, projects: projectService}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != payload || accessCalls != 1 || projectCalls != 1 ||
		resolved != requested {
		t.Fatalf("SDK canonical project response was not safely pinned: version=%q access_calls=%d project_calls=%d err=%v",
			resolved, accessCalls, projectCalls, err)
	}
	clear(value)
}

func TestRunPreparePreservesBoundedSecretManagerSDKMessage(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const serverBodyMarker = "TEST_ONLY_RAW_SECRET_MANAGER_ERROR_BODY"
	message := "Permission denied for the selected Secret Manager version: " + strings.Repeat("x", 700)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code": 403, "message": message,
			"details": []any{map[string]any{"message": serverBodyMarker}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := secretManagerResponseReader(t, resource, http.StatusForbidden, string(body))
	root := prepareRootForSecretManager(t, resource)
	err = runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil {
		t.Fatal("runPrepare() accepted a Secret Manager access failure")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "Google API HTTP 403") ||
		!strings.Contains(diagnostic, "Permission denied for the selected Secret Manager version") ||
		!strings.Contains(diagnostic, "message_truncated=true") {
		t.Fatalf("runPrepare() lost the bounded SDK diagnostic: %q", diagnostic)
	}
	if strings.Contains(diagnostic, serverBodyMarker) || len(diagnostic) > 900 {
		t.Fatalf("runPrepare() exposed raw SDK body or an unbounded message: %q", diagnostic)
	}
}

func TestRunPrepareDistinguishesEmptySecretManagerPayload(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	body := fmt.Sprintf(`{"name":%q,"payload":{"data":""}}`, resource)
	reader := secretManagerResponseReader(t, resource, http.StatusOK, body)
	root := prepareRootForSecretManager(t, resource)
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "payload is empty") || strings.Contains(err.Error(), "access failed") {
		t.Fatalf("empty payload was not distinguished from access failure: %v", err)
	}
}

func TestRunPrepareDistinguishesSecretManagerClientInitializationFailure(t *testing.T) {
	root := prepareRootForSecretManager(t, "projects/test-project/secrets/test-secret/versions/11")
	const detail = "TEST_ONLY_INITIALIZATION_DETAIL"
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return nil, errors.New(detail) })
	if err == nil || !strings.Contains(err.Error(), "initialize Secret Manager resolver") ||
		!strings.Contains(err.Error(), "error type") || strings.Contains(err.Error(), detail) {
		t.Fatalf("client initialization failure was not safely classified: %v", err)
	}
}

func secretManagerResponseReader(t *testing.T, resource string, status int, body string) secretReader {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return googleSecretReader{service: service}
}

func prepareRootForSecretManager(t *testing.T, resource string) string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "deploy", "cac")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("offline fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := json.Marshal(pipelineConfig{
		Environment: "dev", RunTimeoutSeconds: 23,
		LLM: llmProfile{
			Provider: "deepseek", Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-flash",
			RequestTimeoutSeconds: 600,
			Secret:                secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkl := filepath.Join(root, "pkl-fixture")
	script := "#!/bin/sh\ncat <<'SSOT_JSON'\n" + string(config) + "\nSSOT_JSON\n"
	if err := os.WriteFile(pkl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	return root
}
