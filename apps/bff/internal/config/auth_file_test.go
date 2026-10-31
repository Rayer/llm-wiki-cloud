package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validAuthFileForTest() AuthFile {
	return AuthFile{
		SchemaVersion: 1, Target: "auth", Environment: "dev", ConfigID: "sha256:" + strings.Repeat("a", 64),
		GCPProject: "llm-wiki-cloud", FirestoreDatabaseID: "llm-wiki-cloud-dev",
		AuthServiceURL: "https://auth.dev.example.test", SyncServiceURL: "https://bff.dev.example.test",
		AllowedHosts:   []string{"auth.dev.example.test"},
		AllowedOrigins: []string{"https://wiki.example.test"}, AuthSessionEnvironment: "llm-wiki-cloud-dev",
		AuthSessionMigration: "disabled", AuthDemoUserID: "demo-user",
		AuthDemoUserEmail: "demo@example.test", AuthDemoUserRole: "member", JWTSecret: "synthetic-test-signing-key",
	}
}

func localAuthFileForTest() AuthFile {
	file := validAuthFileForTest()
	file.Environment = "local"
	file.GCPProject = "llm-wiki-cloud"
	file.FirestoreDatabaseID = "llm-wiki-cloud-local"
	file.LocalCloudScope = "worktree-0123456789abcdef01234567"
	file.AuthServiceURL = "http://localhost:8081"
	file.SyncServiceURL = "http://localhost:8080"
	file.AllowedHosts = []string{"localhost", "127.0.0.1"}
	file.AllowedOrigins = []string{"http://localhost:3000"}
	file.JWTSecret = strings.Repeat("a", 64)
	return file
}

func writeAuthFileForTest(t *testing.T, file AuthFile) string {
	t.Helper()
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDecodeAuthFileStrictSchemaAndGoogleModes(t *testing.T) {
	file := validAuthFileForTest()
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthFile(data); err != nil {
		t.Fatalf("DecodeAuthFile(valid disabled Google): %v", err)
	}

	google := AuthFileGoogle{
		Enabled: true, ClientID: "client-id", ClientSecret: "synthetic-google-secret", Issuer: GoogleIssuerURL,
		JWKSURL: GoogleJWKSURL, TokenURL: GoogleTokenURL,
		LoginRedirectURL: "https://auth.dev.example.test/api/v1/auth/google/callback",
		LinkRedirectURL:  "https://auth.dev.example.test/api/v1/auth/google/link/callback",
		CompletionURL:    "https://wiki.example.test/login",
	}
	file.Google = google
	data, err = json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthFile(data); err != nil {
		t.Fatalf("DecodeAuthFile(valid Google): %v", err)
	}

	for name, mutate := range map[string]func(*AuthFile){
		"wrong schema":                func(f *AuthFile) { f.SchemaVersion = 2 },
		"missing jwt":                 func(f *AuthFile) { f.JWTSecret = "" },
		"disabled Google residue":     func(f *AuthFile) { f.Google.ClientSecret = "synthetic" },
		"unsupported session mode":    func(f *AuthFile) { f.AuthSessionMigration = "import" },
		"wildcard host":               func(f *AuthFile) { f.AllowedHosts = []string{"*.example.test"} },
		"invalid sync service origin": func(f *AuthFile) { f.SyncServiceURL = "https://bff.example.test/api" },
		"missing Demo email":          func(f *AuthFile) { f.AuthDemoUserEmail = "" },
		"admin Demo role":             func(f *AuthFile) { f.AuthDemoUserRole = "admin" },
		"invalid Demo email":          func(f *AuthFile) { f.AuthDemoUserEmail = "not-an-email" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := validAuthFileForTest()
			mutate(&bad)
			encoded, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeAuthFile(encoded); err == nil {
				t.Fatal("DecodeAuthFile() accepted invalid input")
			}
		})
	}

	withUnknown := strings.TrimSuffix(string(data), "}") + `,"unrelated":true}`
	if _, err := DecodeAuthFile([]byte(withUnknown)); err == nil {
		t.Fatal("DecodeAuthFile() accepted an unknown top-level field")
	}
	duplicate := strings.TrimSuffix(string(data), "}") + `,"target":"auth"}`
	if _, err := DecodeAuthFile([]byte(duplicate)); err == nil {
		t.Fatal("DecodeAuthFile() accepted duplicate keys")
	}
	if _, err := DecodeAuthFile([]byte(strings.Repeat(" ", MaxAuthConfigBytes+1))); err == nil {
		t.Fatal("DecodeAuthFile() accepted an oversized document")
	}
}

func TestSchema2AuthFileUsesStrictOAuthRegistryAndPreservesSchema1Defaults(t *testing.T) {
	file := validAuthFileForTest()
	file.SchemaVersion = 2
	file.OAuthResources = authOAuthResourcesForTest(file.SyncServiceURL, file.Environment)
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAuthFile(data)
	if err != nil {
		t.Fatalf("DecodeAuthFile(schema 2): %v", err)
	}
	if len(decoded.OAuthResources) != 2 || decoded.OAuthResources[1].Enabled {
		t.Fatalf("schema-2 resources = %#v", decoded.OAuthResources)
	}
	loaded, err := LoadAuthFile(writeAuthFileForTest(t, file))
	if err != nil {
		t.Fatalf("LoadAuthFile(schema 2): %v", err)
	}
	if loaded.AuthEnvironment != "dev" || len(loaded.AuthOAuthResources) != 2 {
		t.Fatalf("runtime policy/environment = %d resources, %q", len(loaded.AuthOAuthResources), loaded.AuthEnvironment)
	}
	legacy, err := LoadAuthFile(writeAuthFileForTest(t, validAuthFileForTest()))
	if err != nil {
		t.Fatalf("LoadAuthFile(schema 1): %v", err)
	}
	if len(legacy.AuthOAuthResources) != 1 || legacy.AuthOAuthResources[0].ID != "lwc-project" ||
		len(legacy.AuthOAuthResources[0].DefaultScopeSet) != 1 || legacy.AuthOAuthResources[0].DefaultScopeSet[0] != "mcp:query" {
		t.Fatalf("schema-1 compatibility policy = %#v", legacy.AuthOAuthResources)
	}

	for name, mutate := range map[string]func(*AuthFile){
		"missing registry":       func(v *AuthFile) { v.OAuthResources = nil },
		"duplicate scope":        func(v *AuthFile) { v.OAuthResources[1].Scopes[0].Name = "mcp:query" },
		"invalid default":        func(v *AuthFile) { v.OAuthResources[1].DefaultScopeSet = []string{"mcp:query"} },
		"enabled without caller": func(v *AuthFile) { v.OAuthResources[1].Enabled = true },
		"wrong audience":         func(v *AuthFile) { v.OAuthResources[1].VerifierAudience = "https://wrong.example.test" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := file
			bad.OAuthResources = authOAuthResourcesForTest(bad.SyncServiceURL, bad.Environment)
			mutate(&bad)
			encoded, marshalErr := json.Marshal(bad)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, decodeErr := DecodeAuthFile(encoded); decodeErr == nil {
				t.Fatal("DecodeAuthFile accepted invalid OAuth registry")
			}
		})
	}
	unknown := strings.Replace(string(data), `"id":"daily-brief"`, `"id":"daily-brief","unknown":true`, 1)
	if _, err := DecodeAuthFile([]byte(unknown)); err == nil {
		t.Fatal("DecodeAuthFile accepted unknown nested OAuth registry field")
	}
}

func TestAuthDailyBriefBindingMatchesSelectedEnvironment(t *testing.T) {
	for _, test := range []struct {
		environment string
		uri         string
		swappedURI  string
		audience    string
	}{
		{environment: "dev", uri: authDailyBriefDevResourceURI, swappedURI: authDailyBriefProdResourceURI, audience: authDailyBriefDevVerifierAudience},
		{environment: "prod", uri: authDailyBriefProdResourceURI, swappedURI: authDailyBriefDevResourceURI, audience: authDailyBriefProdVerifierAudience},
	} {
		t.Run(test.environment, func(t *testing.T) {
			file := validAuthFileForTest()
			file.Environment = test.environment
			file.OAuthResources = authOAuthResourcesForTest(file.SyncServiceURL, file.Environment)
			file.OAuthResources[1].VerifierAudience = test.audience
			if file.OAuthResources[1].URI != test.uri {
				t.Fatalf("fixture URI=%q, want %q", file.OAuthResources[1].URI, test.uri)
			}
			if err := validateAuthOAuthResources(file); err != nil {
				t.Fatalf("valid %s Daily Brief binding: %v", test.environment, err)
			}

			for name, uri := range map[string]string{
				"swapped environment": test.swappedURI,
				"unknown URI":         "https://brief-unknown.rayer.idv.tw/mcp",
			} {
				t.Run(name, func(t *testing.T) {
					bad := file
					bad.OAuthResources = append([]AuthOAuthResource(nil), file.OAuthResources...)
					bad.OAuthResources[1].URI = uri
					if err := validateAuthOAuthResources(bad); err == nil {
						t.Fatalf("%s URI was accepted for %s", uri, test.environment)
					}
				})
			}
		})
	}
}

func TestAuthDevDailyBriefRejectsProductionVerifierCaller(t *testing.T) {
	file := validAuthFileForTest()
	file.OAuthResources = authOAuthResourcesForTest(file.SyncServiceURL, file.Environment)
	brief := &file.OAuthResources[1]
	brief.Enabled = true
	brief.VerifierAudience = authDailyBriefDevVerifierAudience
	brief.VerifierCallerServiceAccount = authDailyBriefDevVerifierCallerServiceAccount
	if err := validateAuthOAuthResources(file); err != nil {
		t.Fatalf("valid DEV Daily Brief verifier caller: %v", err)
	}

	brief.VerifierCallerServiceAccount = "daily-brief-runtime@daily-brief-service.iam.gserviceaccount.com"
	if err := validateAuthOAuthResources(file); err == nil {
		t.Fatal("DEV accepted the production Daily Brief verifier caller")
	}
}

func TestAuthVerifierAudienceUsesSelectedEnvironmentPolicy(t *testing.T) {
	local := localAuthFileForTest()
	local.SchemaVersion = 2
	local.AuthSessionEnvironment = "llm-wiki-cloud-local"
	local.OAuthResources = authOAuthResourcesForTest(local.SyncServiceURL, local.Environment)
	local.OAuthResources[1].Enabled = true
	local.OAuthResources[1].VerifierCallerServiceAccount = "fixture-runtime@llm-wiki-cloud.iam.gserviceaccount.com"
	local.OAuthResources[1].VerifierAudience = "https://auth.fixture.example.test"
	encoded, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := DecodeAuthFile(encoded)
	if err != nil || loaded.OAuthResources[1].VerifierAudience != local.OAuthResources[1].VerifierAudience {
		t.Fatalf("local configured verifier audience=%q err=%v", loaded.OAuthResources[1].VerifierAudience, err)
	}

	badLocal := local
	badLocal.OAuthResources = append([]AuthOAuthResource(nil), local.OAuthResources...)
	badLocal.OAuthResources[1].VerifierAudience += "/path"
	encoded, _ = json.Marshal(badLocal)
	if _, err := DecodeAuthFile(encoded); err == nil {
		t.Fatal("local verifier audience accepted a non-origin URL")
	}

	prod := validAuthFileForTest()
	prod.SchemaVersion = 2
	prod.Environment = "prod"
	prod.FirestoreDatabaseID = "llm-wiki-cloud-prod"
	prod.AuthSessionEnvironment = "llm-wiki-cloud-prod"
	prod.OAuthResources = authOAuthResourcesForTest(prod.SyncServiceURL, prod.Environment)
	prod.OAuthResources[1].VerifierAudience = ""
	encoded, _ = json.Marshal(prod)
	if _, err := DecodeAuthFile(encoded); err != nil {
		t.Fatalf("disabled production Brief policy without an unapproved audience: %v", err)
	}
	prod.OAuthResources[1].Enabled = true
	prod.OAuthResources[1].VerifierAudience = authDailyBriefProdVerifierAudience
	prod.OAuthResources[1].VerifierCallerServiceAccount = "brief-runtime@llm-wiki-cloud.iam.gserviceaccount.com"
	encoded, _ = json.Marshal(prod)
	loadedProd, err := DecodeAuthFile(encoded)
	if err != nil {
		t.Fatalf("explicit production Brief verifier policy: %v", err)
	}
	if loadedProd.OAuthResources[1].VerifierAudience != authDailyBriefProdVerifierAudience ||
		loadedProd.OAuthResources[1].VerifierCallerServiceAccount != prod.OAuthResources[1].VerifierCallerServiceAccount {
		t.Fatalf("production verifier binding was not preserved: %#v", loadedProd.OAuthResources[1])
	}
	prod.OAuthResources[1].VerifierCallerServiceAccount = ""
	encoded, _ = json.Marshal(prod)
	if _, err := DecodeAuthFile(encoded); err == nil {
		t.Fatal("enabled production verifier policy accepted an empty caller")
	}
	prod.OAuthResources[1].VerifierCallerServiceAccount = "brief-runtime@llm-wiki-cloud.iam.gserviceaccount.com"
	prod.OAuthResources[1].VerifierAudience = authDailyBriefDevVerifierAudience
	encoded, _ = json.Marshal(prod)
	if _, err := DecodeAuthFile(encoded); err == nil {
		t.Fatal("production verifier policy accepted the DEV audience")
	}
	prod.OAuthResources[1].VerifierAudience = "https://wrong.example.test"
	encoded, _ = json.Marshal(prod)
	if _, err := DecodeAuthFile(encoded); err == nil {
		t.Fatal("production verifier policy accepted an unbound audience")
	}
	prod.Environment = "dev"
	prod.FirestoreDatabaseID = "llm-wiki-cloud-dev"
	prod.AuthSessionEnvironment = "llm-wiki-cloud-dev"
	prod.OAuthResources[1].VerifierAudience = authDailyBriefProdVerifierAudience
	encoded, _ = json.Marshal(prod)
	if _, err := DecodeAuthFile(encoded); err == nil {
		t.Fatal("DEV verifier policy accepted the production audience")
	}
}

func TestLoadAuthFileUsesFileAuthorityAndPORTException(t *testing.T) {
	file := validAuthFileForTest()
	file.LocalCloudScope = ""
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "9090")
	t.Setenv("GCP_PROJECT", "wrong-project")
	t.Setenv("FIRESTORE_DATABASE_ID", "wrong-database")
	t.Setenv("AUTH_SERVICE_URL", "https://wrong.example.test")
	t.Setenv("AUTH_DEMO_USER_EMAIL", "wrong@example.test")
	t.Setenv("AUTH_DEMO_USER_ROLE", "admin")
	t.Setenv("JWT_SECRET", "wrong-secret")
	cfg, err := LoadAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCPProject != file.GCPProject || cfg.FirestoreDatabaseID != file.FirestoreDatabaseID ||
		cfg.AuthServiceURL != file.AuthServiceURL || cfg.JWTSecret != file.JWTSecret || cfg.Port != "9090" || cfg.ConfigID != file.ConfigID ||
		cfg.AuthDemoUserID != file.AuthDemoUserID || cfg.AuthDemoUserEmail != file.AuthDemoUserEmail || cfg.AuthDemoUserRole != file.AuthDemoUserRole {
		t.Fatalf("loaded config=%+v; file authority or PORT exception was lost", cfg)
	}
}

func TestLoadAuthFileRejectsLocalLegacySwitchesBeforeStartup(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "DEV_JWT", env: "DEV_JWT", value: "true"},
		{name: "LOCAL_DATA_DIR", env: "LOCAL_DATA_DIR", value: "/tmp/legacy-local-data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DEV_JWT", "")
			t.Setenv("LOCAL_DATA_DIR", "")
			t.Setenv(test.env, test.value)
			if _, err := LoadAuthFile(writeAuthFileForTest(t, localAuthFileForTest())); err == nil ||
				!strings.Contains(err.Error(), "local cloud does not allow DEV_JWT or LOCAL_DATA_DIR") {
				t.Fatalf("LoadAuthFile() err = %v, want local legacy-switch rejection before startup", err)
			}
		})
	}

	t.Run("false DEV_JWT preserves the existing boolean parse behavior", func(t *testing.T) {
		t.Setenv("DEV_JWT", "false")
		t.Setenv("LOCAL_DATA_DIR", "")
		if _, err := LoadAuthFile(writeAuthFileForTest(t, localAuthFileForTest())); err != nil {
			t.Fatalf("LoadAuthFile() with DEV_JWT=false: %v", err)
		}
	})
}

func TestDecodeAuthFileLocalScopeAndKeyContract(t *testing.T) {
	file := validAuthFileForTest()
	file.Environment = "local"
	file.GCPProject = "llm-wiki-cloud"
	file.FirestoreDatabaseID = "llm-wiki-cloud-local"
	file.LocalCloudScope = "worktree-0123456789abcdef01234567"
	file.AuthServiceURL = "http://localhost:8081"
	file.AllowedHosts = []string{"localhost", "127.0.0.1"}
	file.AllowedOrigins = []string{"http://localhost:3000"}
	file.JWTSecret = strings.Repeat("a", 64)
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthFile(data); err != nil {
		t.Fatalf("DecodeAuthFile(valid local): %v", err)
	}
	file.LocalCloudScope = ""
	data, _ = json.Marshal(file)
	if _, err := DecodeAuthFile(data); err == nil {
		t.Fatal("DecodeAuthFile() accepted local config without its scope")
	}
}
