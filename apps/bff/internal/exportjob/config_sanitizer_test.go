package exportjob

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeProjectTOMLUsesOLWAndSyntoSchemas(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		input    string
		keep     []string
		redacted []string
		secret   string
	}{
		{
			name: "legacy OLW provider",
			path: "wiki.toml",
			input: `[provider]
name = "custom"
url = "https://api.deepseek.com/v1"
api_key = "LWC346_TOML_SECRET_MARKER"

[models]
fast = "deepseek-chat"
heavy = "deepseek-reasoner"

[pipeline]
auto_approve = true
`,
			keep:     []string{`name = "custom"`, `fast = "deepseek-chat"`, `auto_approve = true`},
			redacted: []string{"provider.api_key"},
			secret:   "LWC346_TOML_SECRET_MARKER",
		},
		{
			name: "Synto provider environment reference",
			path: "synto.toml",
			input: `[providers.default]
name = "deepseek"
url = "https://api.deepseek.com/v1"
timeout = 600
api_key_env = "LWC346_TOML_ENV_REFERENCE"

[models.fast]
provider = "default"
model = "deepseek-flash"
ctx = 16384

[pipeline]
auto_commit = false
`,
			keep:     []string{`name = "deepseek"`, `model = "deepseek-flash"`, `auto_commit = false`},
			redacted: []string{"providers.default.api_key_env"},
			secret:   "LWC346_TOML_ENV_REFERENCE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fields, err := sanitizeProjectTOML([]byte(tc.input), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(got, []byte(tc.secret)) {
				t.Fatalf("sanitized config retains credential marker %q", tc.secret)
			}
			for _, value := range tc.keep {
				if !bytes.Contains(got, []byte(value)) {
					t.Errorf("sanitized config lost nonsecret setting %q: %s", value, got)
				}
			}
			if !reflect.DeepEqual(fields, tc.redacted) {
				t.Fatalf("redacted fields = %v, want %v", fields, tc.redacted)
			}
		})
	}
}

func TestSanitizeProjectTOMLFailsClosedForUnknownCredentialFields(t *testing.T) {
	for _, key := range []string{"api_token", "client_secret_reference", "custom_auth", "token_env"} {
		t.Run(key, func(t *testing.T) {
			_, _, err := sanitizeProjectTOML([]byte("[provider]\n"+key+" = \"unknown credential\"\n"), "wiki.toml")
			if err == nil || !strings.Contains(err.Error(), "provider."+key) {
				t.Fatalf("unknown credential field error = %v, want clear path-specific rejection", err)
			}
		})
	}
}

func TestSanitizeProjectTOMLRejectsUnsupportedPath(t *testing.T) {
	if _, _, err := sanitizeProjectTOML([]byte(`[project]
name = "demo"
`), "private.toml"); err == nil {
		t.Fatal("unsupported config path was accepted")
	}
}
