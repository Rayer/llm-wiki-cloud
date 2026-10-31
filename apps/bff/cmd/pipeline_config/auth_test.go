package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/config"
)

type fakeAuthReader struct {
	versions []string
	accesses []string
	data     []byte
	err      error
}

func (r *fakeAuthReader) ResolveVersion(_ context.Context, resource string) (string, error) {
	r.versions = append(r.versions, resource)
	if r.err != nil {
		return "", r.err
	}
	if strings.HasSuffix(resource, "/latest") {
		return strings.TrimSuffix(resource, "latest") + "7", nil
	}
	return resource, nil
}

func (r *fakeAuthReader) Access(_ context.Context, resource string) (string, []byte, error) {
	r.accesses = append(r.accesses, resource)
	if r.err != nil {
		return "", nil, r.err
	}
	return resource, append([]byte(nil), r.data...), nil
}

func authProjectionForTest(environment string) authSourceProjection {
	briefURI := "https://brief.rayer.idv.tw/mcp"
	briefAudience := "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app"
	if environment == "dev" {
		briefURI = "https://brief.dev.rayer.idv.tw/mcp"
		briefAudience = "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app"
	}
	projection := authSourceProjection{
		SchemaVersion: 2, Environment: environment, Target: "auth", GCPProject: "llm-wiki-cloud",
		FirestoreDatabaseID: "llm-wiki-cloud-" + environment, AuthServiceURL: "https://auth." + environment + ".example.test",
		SyncServiceURL:         "https://bff." + environment + ".example.test",
		AllowedHosts:           []string{"auth." + environment + ".example.test"},
		AllowedOrigins:         []string{"https://wiki." + environment + ".example.test"},
		AuthSessionEnvironment: "llm-wiki-cloud-" + environment, AuthSessionMigration: "disabled",
		AuthDemoUserID: "demo-user", AuthDemoUserEmail: "demo@example.test", AuthDemoUserRole: "member",
		JWTSecretReference:   "projects/llm-wiki-cloud/secrets/jwt-secret-" + environment + "/versions/latest",
		ConfigSecretResource: "projects/llm-wiki-cloud/secrets/lwc-auth-app-config-" + environment,
		Google: authSourceGoogle{
			Enabled: true, ClientID: "client-id-" + environment,
			ClientSecretReference: "projects/llm-wiki-cloud/secrets/google-oauth-client-" + environment + "/versions/1",
			Issuer:                config.GoogleIssuerURL, JWKSURL: config.GoogleJWKSURL, TokenURL: config.GoogleTokenURL,
			LoginRedirectURL: "https://auth." + environment + ".example.test/api/v1/auth/google/callback",
			LinkRedirectURL:  "https://auth." + environment + ".example.test/api/v1/auth/google/link/callback",
			CompletionURL:    "https://wiki." + environment + ".example.test/login",
		},
		OAuthResources: []config.AuthOAuthResource{
			{
				ID: "lwc-project", Kind: "project-template", Enabled: true,
				URI: "https://bff." + environment + ".example.test/mcp/projects/{project_id}", Name: "LWC Project",
				Scopes:           []config.AuthOAuthScope{{Name: "mcp:query", Description: "Query project knowledge."}, {Name: "mcp:raw:write", Description: "Add permitted raw sources."}},
				AllowedScopeSets: [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}}, DefaultScopeSet: []string{"mcp:query"},
			},
			{
				ID: "daily-brief", Kind: "fixed", Enabled: false, URI: briefURI, Name: "Daily Brief",
				Scopes:           []config.AuthOAuthScope{{Name: "brief:manage", Description: "Read and manage Daily Brief reports and annotations in your private account space."}},
				AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{},
				VerifierAudience: briefAudience,
			},
		},
	}
	return projection
}

func TestRealPklAuthPrepareProducesLocalFileAndCloudInputStages(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("PKL_BIN"))
	if realPKL == "" {
		var err error
		realPKL, err = exec.LookPath("pkl")
		if err != nil {
			t.Fatalf("Pkl is required for real Auth prepare acceptance: %v", err)
		}
	}
	if _, err := os.Stat(realPKL); err != nil {
		t.Fatalf("configured Pkl binary is unavailable: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", repoRoot)
	t.Setenv("PKL_BIN", realPKL)
	t.Setenv("PKL_CACHE_DIR", "")

	fixtureKey := filepath.Join(t.TempDir(), "synthetic-auth-key")
	if err := os.WriteFile(fixtureKey, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_CLOUD_SCOPE", "worktree-0123456789abcdef01234567")
	t.Setenv("LOCAL_CLOUD_JWT_SECRET_FILE", fixtureKey)
	t.Setenv("AUTH_DEMO_USER_ID", "demo-local-test")
	t.Setenv("AUTH_DEMO_USER_EMAIL", "demo-local@example.test")
	t.Setenv("AUTH_DEMO_USER_ROLE", "member")
	t.Setenv("BFF_PORT", "18080")
	t.Setenv("AUTH_PORT", "18081")
	t.Setenv("FRONTEND_PORT", "13000")
	localOutput := t.TempDir()
	if err := runPrepareTargetModeAtSource(context.Background(), "auth", "local", localOutput, false, strings.Repeat("c", 40), nil); err != nil {
		t.Fatalf("prepare Auth local: %v", err)
	}
	localPath := filepath.Join(localOutput, "auth", "auth.json")
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("local Auth file mode=%#o, want 0600", info.Mode().Perm())
	}
	localConfig, err := config.LoadAuthFile(localPath)
	if err != nil {
		t.Fatalf("LoadAuthFile(local output): %v", err)
	}
	if localConfig.JWTSecret != strings.Repeat("a", 64) || localConfig.LocalCloudScope != "worktree-0123456789abcdef01234567" ||
		localConfig.Port != "8080" || localConfig.AuthDemoUserID != "demo-local-test" ||
		localConfig.AuthDemoUserEmail != "demo-local@example.test" || localConfig.AuthDemoUserRole != "member" {
		t.Fatal("local Auth output did not preserve the fixture key, explicit scope, or default PORT")
	}
	if localConfig.SyncServiceURL != "http://localhost:18080" {
		t.Fatalf("local Auth resource binding=%q, want the selected BFF port", localConfig.SyncServiceURL)
	}
	if len(localConfig.AuthOAuthResources) != 2 || localConfig.AuthOAuthResources[1].Enabled ||
		localConfig.AuthOAuthResources[1].URI != "https://brief.rayer.idv.tw/mcp" {
		t.Fatalf("local disabled Brief policy changed: %#v", localConfig.AuthOAuthResources)
	}
	manifest, err := os.ReadFile(filepath.Join(localOutput, "auth", "success.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), strings.Repeat("a", 64)) {
		t.Fatal("local Auth success manifest contains the synthetic signing key")
	}
	if directory, err := os.Stat(filepath.Join(localOutput, "auth")); err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatalf("local Auth directory permissions are not 0700: %v", err)
	}

	for _, environment := range []string{"dev", "prod"} {
		t.Run(environment+" projection and Stage 1", func(t *testing.T) {
			reader := &fakeAuthReader{}
			output := t.TempDir()
			if err := runPrepareTargetModeAtSource(context.Background(), "auth", environment, output, false, strings.Repeat("c", 40), func(context.Context) (secretReader, error) {
				return reader, nil
			}); err != nil {
				t.Fatalf("prepare Auth source projection: %v", err)
			}
			sourceBytes, err := os.ReadFile(filepath.Join(output, "auth-source.json"))
			if err != nil {
				t.Fatal(err)
			}
			projection, err := decodeAuthSourceProjection(sourceBytes, environment)
			if err != nil {
				t.Fatalf("decode real Pkl Auth source: %v", err)
			}
			if projection.Environment != environment || projection.Target != "auth" || !projection.Google.Enabled {
				t.Fatalf("unexpected Pkl Auth projection identity for %s", environment)
			}
			wantAudience, wantCaller, wantResourceURI := "", "", ""
			wantEnabled := false
			if environment == "dev" {
				wantAudience = "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app"
				wantCaller = "daily-brief-dev-runtime@daily-brief-service.iam.gserviceaccount.com"
				wantResourceURI = "https://brief.dev.rayer.idv.tw/mcp"
				wantEnabled = true
			} else if environment == "prod" {
				wantAudience = "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app"
				wantCaller = "daily-brief-runtime@daily-brief-service.iam.gserviceaccount.com"
				wantResourceURI = "https://brief.rayer.idv.tw/mcp"
				wantEnabled = true
			}
			if len(projection.OAuthResources) != 2 {
				t.Fatalf("%s Pkl Auth projection has %d OAuth resources, want 2", environment, len(projection.OAuthResources))
			}
			if projection.OAuthResources[1].VerifierAudience != wantAudience {
				t.Fatalf("%s Pkl Auth projection verifier audience=%q, want %q", environment, projection.OAuthResources[1].VerifierAudience, wantAudience)
			}
			if projection.OAuthResources[1].URI != wantResourceURI {
				t.Fatalf("%s Pkl Auth projection resource URI=%q, want %q", environment, projection.OAuthResources[1].URI, wantResourceURI)
			}
			if projection.OAuthResources[1].Enabled != wantEnabled || projection.OAuthResources[1].VerifierCallerServiceAccount != wantCaller {
				t.Fatalf("canonical %s Brief policy enabled=%t caller=%q; want enabled=%t caller=%q", environment,
					projection.OAuthResources[1].Enabled, projection.OAuthResources[1].VerifierCallerServiceAccount, wantEnabled, wantCaller)
			}
			if err := runPrepareTargetModeAtSource(context.Background(), "auth", environment, output, true, strings.Repeat("c", 40), func(context.Context) (secretReader, error) {
				return reader, nil
			}); err != nil {
				t.Fatalf("prepare Auth Stage 1 snapshot: %v", err)
			}
			inputsBytes, err := os.ReadFile(filepath.Join(output, "auth-inputs.json"))
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := config.DecodeAuthInputSnapshot(inputsBytes)
			if err != nil {
				t.Fatalf("decode Auth Stage 1 snapshot: %v", err)
			}
			if inputs.SourceSHA != strings.Repeat("c", 40) || !strings.HasSuffix(inputs.JWTSecretVersion, "/versions/7") ||
				!strings.HasSuffix(inputs.Google.ClientSecretVersion, "/versions/1") {
				t.Fatal("Stage 1 did not pin selected credential versions and source SHA")
			}
			if len(inputs.OAuthResources) != 2 {
				t.Fatalf("%s Auth Stage 1 has %d OAuth resources, want 2", environment, len(inputs.OAuthResources))
			}
			if inputs.OAuthResources[1].VerifierAudience != wantAudience {
				t.Fatalf("%s Auth Stage 1 verifier audience=%q, want %q", environment, inputs.OAuthResources[1].VerifierAudience, wantAudience)
			}
			if inputs.OAuthResources[1].URI != wantResourceURI || inputs.OAuthResources[1].Enabled != wantEnabled || inputs.OAuthResources[1].VerifierCallerServiceAccount != wantCaller ||
				inputs.AuthSessionEnvironment != "llm-wiki-cloud-"+environment {
				t.Fatalf("canonical %s Auth snapshot did not preserve Brief policy identity or changed its session namespace", environment)
			}

			canonicalRuntimePath := filepath.Join(output, "auth", "canonical-auth.json")
			if err := materializeAuthSnapshot(context.Background(), inputs, canonicalRuntimePath, func(context.Context) (secretReader, error) {
				return &fakeAuthReader{data: []byte("SYNTHETIC_AUTH_CREDENTIAL")}, nil
			}); err != nil {
				t.Fatalf("materialize canonical %s snapshot: %v", environment, err)
			}
			canonicalRuntime, err := config.LoadAuthFile(canonicalRuntimePath)
			if err != nil {
				t.Fatalf("load canonical %s runtime config: %v", environment, err)
			}
			if canonicalRuntime.AuthEnvironment != environment || canonicalRuntime.ConfigID != inputs.ConfigID ||
				len(canonicalRuntime.AuthOAuthResources) != 2 ||
				canonicalRuntime.AuthOAuthResources[1].URI != wantResourceURI ||
				canonicalRuntime.AuthOAuthResources[1].VerifierAudience != wantAudience ||
				canonicalRuntime.AuthOAuthResources[1].Enabled != wantEnabled ||
				canonicalRuntime.AuthOAuthResources[1].VerifierCallerServiceAccount != wantCaller {
				t.Fatalf("canonical %s runtime verifier policy was not read back exactly", environment)
			}

			if environment == "prod" {
				prodAudience := "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app"
				synthetic := projection
				synthetic.OAuthResources = append([]config.AuthOAuthResource(nil), projection.OAuthResources...)
				synthetic.OAuthResources[1].Enabled = true
				synthetic.OAuthResources[1].VerifierAudience = prodAudience
				synthetic.OAuthResources[1].VerifierCallerServiceAccount = "brief-runtime-fixture@llm-wiki-cloud.iam.gserviceaccount.com"
				if err := validateAuthSourceProjection(synthetic, "prod"); err != nil {
					t.Fatalf("validate explicit production source policy: %v", err)
				}
				syntheticInputs, err := prepareAuthInputs(context.Background(), synthetic, strings.Repeat("c", 40), func(context.Context) (secretReader, error) {
					return &fakeAuthReader{}, nil
				})
				if err != nil {
					t.Fatalf("generate production input snapshot: %v", err)
				}
				if syntheticInputs.ConfigID == inputs.ConfigID {
					t.Fatal("production policy source change did not change config_id")
				}
				snapshotBytes, err := json.Marshal(syntheticInputs)
				if err != nil {
					t.Fatal(err)
				}
				syntheticInputs, err = config.DecodeAuthInputSnapshot(snapshotBytes)
				if err != nil {
					t.Fatalf("decode explicit production input snapshot: %v", err)
				}
				syntheticRuntimePath := filepath.Join(output, "auth", "synthetic-prod-auth.json")
				if err := materializeAuthSnapshot(context.Background(), syntheticInputs, syntheticRuntimePath, func(context.Context) (secretReader, error) {
					return &fakeAuthReader{data: []byte("SYNTHETIC_AUTH_CREDENTIAL")}, nil
				}); err != nil {
					t.Fatalf("materialize explicit production snapshot: %v", err)
				}
				syntheticRuntime, err := config.LoadAuthFile(syntheticRuntimePath)
				if err != nil {
					t.Fatalf("load explicit production runtime config: %v", err)
				}
				brief := syntheticRuntime.AuthOAuthResources[1]
				if syntheticRuntime.AuthEnvironment != "prod" || syntheticRuntime.ConfigID != syntheticInputs.ConfigID ||
					brief.Enabled != true || brief.VerifierAudience != prodAudience ||
					brief.VerifierCallerServiceAccount != synthetic.OAuthResources[1].VerifierCallerServiceAccount {
					t.Fatalf("explicit production verifier input was not read back: %#v", brief)
				}
			}
		})
	}
}

func TestAuthStage1SkipsDisabledGoogleAndStage2UsesOnlyPinnedVersions(t *testing.T) {
	projection := authProjectionForTest("dev")
	projection.Google = authSourceGoogle{}
	reader := &fakeAuthReader{}
	inputs, err := prepareAuthInputs(context.Background(), projection, strings.Repeat("d", 40), func(context.Context) (secretReader, error) {
		return reader, nil
	})
	if err != nil {
		t.Fatalf("prepare Auth input snapshot with disabled Google: %v", err)
	}
	if len(reader.versions) != 1 || inputs.Google.Enabled || inputs.Google.ClientSecretVersion != "" {
		t.Fatalf("disabled Google caused credential resolution: requests=%v", reader.versions)
	}

	projection = authProjectionForTest("dev")
	reader = &fakeAuthReader{}
	inputs, err = prepareAuthInputs(context.Background(), projection, strings.Repeat("d", 40), func(context.Context) (secretReader, error) {
		return reader, nil
	})
	if err != nil {
		t.Fatalf("prepare enabled Auth input snapshot: %v", err)
	}
	reader = &fakeAuthReader{data: []byte("TEST_ONLY_AUTH_CREDENTIAL_PAYLOAD")}
	output := filepath.Join(t.TempDir(), "runtime", "auth.json")
	if err := materializeAuthSnapshot(context.Background(), inputs, output, func(context.Context) (secretReader, error) {
		return reader, nil
	}); err != nil {
		t.Fatalf("materialize pinned Auth snapshot: %v", err)
	}
	if len(reader.accesses) != 2 || reader.accesses[0] != inputs.JWTSecretVersion || reader.accesses[1] != inputs.Google.ClientSecretVersion {
		t.Fatalf("Stage 2 did not access only numeric Stage 1 references: %v", reader.accesses)
	}
	fileBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	file, err := config.DecodeAuthFile(fileBytes)
	if err != nil {
		t.Fatalf("decode materialized fixture: %v", err)
	}
	if file.JWTSecret != "TEST_ONLY_AUTH_CREDENTIAL_PAYLOAD" || file.Google.ClientSecret != "TEST_ONLY_AUTH_CREDENTIAL_PAYLOAD" ||
		file.ConfigID != inputs.ConfigID || file.AuthDemoUserID != projection.AuthDemoUserID ||
		file.AuthDemoUserEmail != projection.AuthDemoUserEmail || file.AuthDemoUserRole != projection.AuthDemoUserRole {
		t.Fatal("Stage 2 did not preserve pinned config identity and synthetic credential payloads")
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized Auth file mode is not 0600: %v", err)
	}
}

func TestAuthPrepareDoesNotUseLatestCredentialPayloadForStage1(t *testing.T) {
	projection := authProjectionForTest("dev")
	reader := &fakeAuthReader{err: errors.New("TEST_ONLY resolver failure")}
	if _, err := prepareAuthInputs(context.Background(), projection, strings.Repeat("e", 40), func(context.Context) (secretReader, error) {
		return reader, nil
	}); err == nil || strings.Contains(err.Error(), "TEST_ONLY resolver failure") {
		t.Fatal("Stage 1 exposed a metadata resolver error or accepted unresolved input")
	}
	if len(reader.versions) != 1 || strings.Contains(strings.Join(reader.accesses, " "), "latest") {
		t.Fatal("Stage 1 accessed a credential payload instead of resolving metadata")
	}
}
