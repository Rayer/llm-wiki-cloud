package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const googleFixture = `  google:
    enabled: true
    client_id: 123456-test.apps.googleusercontent.com
    client_secret_reference: google-oauth-client-dev
    client_secret_version: "1"
    issuer: https://accounts.google.com
    jwks_url: https://www.googleapis.com/oauth2/v3/certs
    token_url: https://oauth2.googleapis.com/token
    login_redirect_url: https://auth.dev.rayer.idv.tw/api/v1/auth/google/callback
    link_redirect_url: https://auth.dev.rayer.idv.tw/api/v1/auth/google/link/callback
    completion_url: https://wiki.dev.rayer.idv.tw/login
`

func TestGoogleDeploymentContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/environments/development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Strip the shipped Google block; each case supplies its own contract.
	base := regexp.MustCompile(`(?m)^  google:\n(?:    .*\n)*`).ReplaceAllString(string(raw), "")
	cases := map[string]struct {
		block, old, replacement string
		valid                   bool
	}{
		"enabled":              {block: googleFixture, valid: true},
		"disabled":             {block: "  google:\n    enabled: false\n", valid: true},
		"unprovisioned":        {block: "  google:\n    enabled: true\n"},
		"missing enablement":   {block: "  google: {}\n"},
		"disabled with config": {block: strings.Replace(googleFixture, "enabled: true", "enabled: false", 1)},
	}
	for _, field := range []string{"client_id", "client_secret_reference", "client_secret_version", "issuer", "jwks_url", "token_url", "login_redirect_url", "link_redirect_url", "completion_url"} {
		lines := strings.Split(googleFixture, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "    "+field+":") {
				lines[i] = "    " + field + ": \"\""
			}
		}
		cases["missing "+field] = struct {
			block, old, replacement string
			valid                   bool
		}{block: strings.Join(lines, "\n")}
	}
	for old, replacement := range map[string]string{
		"123456-test.apps.googleusercontent.com": "not-a-client", "google-oauth-client-dev": "google-oauth-client-prod", `client_secret_version: "1"`: `client_secret_version: latest`,
		"https://accounts.google.com": "https://example.org", "https://www.googleapis.com/oauth2/v3/certs": "https://example.org/certs", "https://oauth2.googleapis.com/token": "https://example.org/token",
		"auth.dev.rayer.idv.tw": "auth.rayer.idv.tw", "https://wiki.dev.rayer.idv.tw/login": "https://wiki.rayer.idv.tw/login", "llm-wiki-cloud-dev": "llm-wiki-cloud-prod", "llm-wiki-auth-dev": "llm-wiki-auth", "https://wiki.dev.rayer.idv.tw\n": "https://evil.example\n",
	} {
		cases["mismatch "+old] = struct {
			block, old, replacement string
			valid                   bool
		}{block: googleFixture, old: old, replacement: replacement}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(base, "auth:\n", "auth:\n"+tc.block, 1)
			if tc.old != "" {
				content = strings.ReplaceAll(content, tc.old, tc.replacement)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := decodeConfig(path)
			if err == nil {
				err = validateConfigForEnvironment("development", cfg)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestProductionGoogleDeploymentContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	base := regexp.MustCompile(`(?m)^  google:\n(?:    .*\n)*`).ReplaceAllString(string(raw), "")
	fixture := strings.NewReplacer("google-oauth-client-dev", "google-oauth-client-prod", "auth.dev.rayer.idv.tw", "auth.rayer.idv.tw", "wiki.dev.rayer.idv.tw", "wiki.rayer.idv.tw").Replace(googleFixture)
	cases := map[string]struct {
		block, old, replacement string
		valid                   bool
	}{
		"enabled":              {block: fixture, valid: true},
		"disabled":             {block: "  google:\n    enabled: false\n", valid: true},
		"missing enablement":   {block: "  google: {}\n"},
		"unprovisioned":        {block: "  google:\n    enabled: true\n"},
		"disabled with config": {block: strings.Replace(fixture, "enabled: true", "enabled: false", 1)},
		"DEV block":            {block: googleFixture},
	}
	for old, replacement := range map[string]string{
		"123456-test.apps.googleusercontent.com":      "580854833715-vo7fg6f7f15g1kkgchk1ulccllbc24qg.apps.googleusercontent.com",
		"google-oauth-client-prod":                    "google-oauth-client-dev",
		`client_secret_version: "1"`:                  `client_secret_version: latest`,
		"auth.rayer.idv.tw":                           "auth.dev.rayer.idv.tw",
		"https://wiki.rayer.idv.tw/login":             "https://wiki.dev.rayer.idv.tw/login",
		"llm-wiki-cloud-prod":                         "llm-wiki-cloud-dev",
		"service_name: llm-wiki-auth\n":               "service_name: llm-wiki-auth-dev\n",
		"lwc-auth-prod@":                              "lwc-auth-dev@",
		"service_name: llm-wiki-bff\n":                "service_name: llm-wiki-bff-dev\n",
		"lwc-bff-prod@":                               "lwc-bff-dev@",
		"auth_service_url: https://auth.rayer.idv.tw": "auth_service_url: https://auth.dev.rayer.idv.tw",
		"api_url: https://llm-wiki-bff-":              "api_url: https://llm-wiki-bff-dev-",
		"project_name: llm-wiki-frontend\n":           "project_name: llm-wiki-frontend-dev\n",
		"https://wiki.rayer.idv.tw\n":                 "https://evil.example\n",
		"jwt-secret-prod":                             "jwt-secret-dev",
		"https://accounts.google.com":                 "https://evil.example",
	} {
		cases["mismatch "+old] = struct {
			block, old, replacement string
			valid                   bool
		}{block: fixture, old: old, replacement: replacement}
	}
	for _, field := range []string{"client_id", "client_secret_reference", "client_secret_version", "issuer", "jwks_url", "token_url", "login_redirect_url", "link_redirect_url", "completion_url"} {
		block := regexp.MustCompile(`(?m)^    `+field+`:.*\n`).ReplaceAllString(fixture, "")
		cases["missing "+field] = struct {
			block, old, replacement string
			valid                   bool
		}{block: block}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(base, "auth:\n", "auth:\n"+tc.block, 1)
			if tc.old != "" {
				content = strings.ReplaceAll(content, tc.old, tc.replacement)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := decodeConfig(path)
			if err == nil {
				err = validateConfigForEnvironment("production", cfg)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestGoogleNormalizedPlanAndProductionIsolation(t *testing.T) {
	root := repoRoot(t)
	dev, err := Load("development", filepath.Join(root, "deploy/environments/development.yaml"), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Auth.Google == nil || dev.Auth.Google.Enabled == nil || !*dev.Auth.Google.Enabled {
		t.Fatal("DEV must ship explicitly enabled")
	}
	if dev.Auth.Google.ClientID != "580854833715-vo7fg6f7f15g1kkgchk1ulccllbc24qg.apps.googleusercontent.com" || dev.Auth.Google.ClientSecretReference != "google-oauth-client-dev" || dev.Auth.Google.ClientSecretVersion != "1" {
		t.Fatal("DEV must use the provisioned client and pinned secret reference")
	}
	if dev.Components["auth"].(map[string]any)["google"] != dev.Auth.Google {
		t.Fatal("component plan omitted Google config")
	}
	prod, err := Load("production", filepath.Join(root, "deploy/environments/production.yaml"), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Auth.Google == nil || prod.Auth.Google.Enabled == nil || !*prod.Auth.Google.Enabled {
		t.Fatal("Production must ship explicitly enabled")
	}
	if prod.Auth.Google.ClientID != "580854833715-1b37asap0uocbdcrighjaorflvj2n94m.apps.googleusercontent.com" || prod.Auth.Google.ClientSecretReference != "google-oauth-client-prod" || prod.Auth.Google.ClientSecretVersion != "1" {
		t.Fatal("Production must use the provisioned client and pinned secret reference")
	}
	if prod.Components["auth"].(map[string]any)["google"] != prod.Auth.Google {
		t.Fatal("Production component plan omitted Google config")
	}
	cfg, err := decodeConfig(filepath.Join(root, "deploy/environments/production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.Google = dev.Auth.Google
	if err := validateConfigForEnvironment("production", cfg); err == nil {
		t.Fatal("Production accepted DEV Google configuration")
	}
}
