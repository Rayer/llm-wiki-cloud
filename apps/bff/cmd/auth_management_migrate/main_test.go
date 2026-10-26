package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestReadManifestRequiresExactRegisteredScopeMapping(t *testing.T) {
	valid := `{"schema_version":1,"run_id":"preview-run-1","source_environment":"dev-db","created_at":"2026-10-27T00:00:00Z","targets":[{"user_id":"admin-1","source_role":"admin","source_status":"active","source_update_time":"2026-10-27T00:00:00Z","scopes":["auth:admin","lwc:admin"]}]}`
	path := t.TempDir() + "/manifest.json"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(path)
	if err != nil || len(manifest.Targets) != 1 || manifest.Targets[0].UserID != "admin-1" {
		t.Fatalf("valid manifest=%+v err=%v", manifest, err)
	}

	invalid := strings.Replace(valid, `"lwc:admin"`, `"lwc:delete-all"`, 1)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(path); err == nil {
		t.Fatal("manifest with an unregistered scope was accepted")
	}
}

func TestPreviewCommandIncludesExactSyntheticLegacyAdmin(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("local Firestore emulator is required")
	}
	configPath := strings.TrimSpace(os.Getenv("LWC_APP_CONFIG_PATH"))
	if configPath == "" {
		t.Skip("generated local Auth config is required")
	}
	cfg, err := config.LoadAuthFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalCloudScope == "" {
		t.Skip("preview integration test requires a local scoped config")
	}
	endpoint := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("preview integration test requires a loopback Firestore emulator")
	}
	fs, err := firestore.NewClientWithDatabaseAndScope(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "", cfg.LocalCloudScope)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	const userID = "lwc432-synthetic-preview-admin"
	if _, err := firestore.Collection(fs.Raw(), "users").Doc(userID).Set(context.Background(), auth.UserRecord{
		Role: "admin", Status: auth.AccountActive, AuthVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = write
	runErr := run([]string{"-mode", "preview", "-run-id", "lwc432-preview-contract"})
	_ = write.Close()
	os.Stdout = previousStdout
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if runErr != nil || readErr != nil {
		t.Fatalf("preview command error=%v read error=%v", runErr, readErr)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(bytes.TrimSpace(output), &manifest); err != nil {
		t.Fatalf("decode preview output: %v", err)
	}
	if manifest.SourceEnvironment != cfg.AuthSessionEnvironment || manifest.RunID != "lwc432-preview-contract" || len(manifest.Targets) != 1 {
		t.Fatalf("preview identity or target count = %+v", manifest)
	}
	target := manifest.Targets[0]
	if target.UserID != userID || target.Role != "admin" || target.Status != auth.AccountActive ||
		!sameScopes(target.Scopes, []string{auth.ManagementScopeAuthAdmin, auth.ManagementScopeLWCAdmin}) || target.SourceUpdateTime.IsZero() {
		t.Fatalf("preview target mapping = %+v", target)
	}
}
