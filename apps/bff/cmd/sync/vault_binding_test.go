package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultBindingFileStoresOnlyStableBindingIdentifiers(t *testing.T) {
	vault := t.TempDir()
	config, err := prepareVaultBinding(vault, "https://auth.example.test", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://auth.example.test" || config.WikiID == "" || config.ProjectID != "project-1" || config.BindingID != "" {
		t.Fatalf("new vault binding=%#v", config)
	}
	if err := saveVaultBindingID(vault, config, "binding-1"); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadVaultBinding(vault)
	if err != nil || loaded != (vaultBindingConfig{Host: config.Host, WikiID: config.WikiID, ProjectID: "project-1", BindingID: "binding-1"}) {
		t.Fatalf("loaded vault binding=%#v error=%v", loaded, err)
	}
	data, err := os.ReadFile(vaultBindingPath(vault))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "secret"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("vault binding contains %q: %s", forbidden, data)
		}
	}
	info, err := os.Stat(vaultBindingPath(vault))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("binding file mode=%v error=%v", info, err)
	}
}

func TestVaultBindingRequiresExplicitReauthorizationAndPinsHost(t *testing.T) {
	vault := t.TempDir()
	first, err := prepareVaultBinding(vault, "https://auth.example.test", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveVaultBindingID(vault, first, "binding-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareVaultBinding(vault, "https://auth.example.test", "project-1"); err == nil {
		t.Fatal("existing binding was silently recreated")
	}
	if _, err := prepareVaultBinding(vault, "https://other.example.test", "project-1"); err == nil {
		t.Fatal("vault binding host was silently changed")
	}
}

func TestInitPreparesMissingVaultAndPreservesExistingRawFiles(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "new", "vault")
	first, err := prepareVaultForInit(vault, "https://auth.example.test", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(vault, "raw")); err != nil || !info.IsDir() {
		t.Fatalf("raw directory info=%v err=%v", info, err)
	}
	filePath := filepath.Join(vault, "raw", "nested", "attachment.bin")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := prepareVaultForInit(vault, "https://auth.example.test", "project-1")
	if err != nil || second != first {
		t.Fatalf("repeat init binding=%#v err=%v, want %#v", second, err, first)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "existing" {
		t.Fatalf("repeat init changed existing raw file: data=%q err=%v", data, err)
	}
	if _, err := prepareVaultForInit(vault, "https://other.example.test", "project-1"); err == nil {
		t.Fatal("init silently changed the bound host")
	}
	if _, err := prepareVaultForInit(vault, "https://auth.example.test", "project-2"); err == nil {
		t.Fatal("init silently changed the bound Project")
	}
}
