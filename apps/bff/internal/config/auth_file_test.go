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
