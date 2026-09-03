package main

import (
	"os"
	"path/filepath"
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
		if config.QueryConfig.RuntimePath != "/app/configs/query/dev/query-dev-2026-08-31.1.json" || config.QueryConfig.Revision != "query-dev-2026-08-31.1" || config.QueryConfig.Digest != "sha256:2ee1a7303c60e810c3240c966a784c4d6cc76419a37b6e0e13e2d9e80f344305" || config.QueryConfig.SchemaVersion != 2 {
			t.Fatalf("%s query identity = %#v", environment, config.QueryConfig)
		}
		if !config.Evidence.Validated || !config.Evidence.SecretFree || !strings.HasPrefix(config.Evidence.ConfigFingerprint, "sha256:") {
			t.Fatalf("%s evidence = %#v", environment, config.Evidence)
		}
		worker, ok := config.Components["worker"].(map[string]any)
		if !ok || worker["args"] == nil || worker["secret_references"] == nil {
			t.Fatalf("%s worker component input omitted behavior-bearing config: %#v", environment, config.Components["worker"])
		}
	}
}

func TestParseComponentsIsExplicitAndDeterministic(t *testing.T) {
	got, err := parseComponents("frontend, bff")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "bff,frontend" {
		t.Fatalf("components = %v", got)
	}
	for _, raw := range []string{"", "bff,", "bff,bff", "all", "auth,unknown"} {
		if _, err := parseComponents(raw); err == nil {
			t.Fatalf("parseComponents(%q) unexpectedly succeeded", raw)
		}
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
				text = strings.Replace(text, "query_config: apps/bff/configs/query/dev/query-dev-2026-08-31.1.json", replacement, 1)
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
