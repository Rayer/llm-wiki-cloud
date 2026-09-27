package exportjob

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const maxProjectTOMLBytes int64 = 1 << 20

// sanitizeProjectTOML parses the two config formats emitted by OLW/Synto and
// removes only their schema-defined provider authorization fields. It does
// not inspect setting values, so ordinary model and project config survives.
func sanitizeProjectTOML(data []byte, path string) ([]byte, []string, error) {
	if path != "wiki.toml" && path != "synto.toml" {
		return nil, nil, fmt.Errorf("unsupported project TOML path %q", path)
	}
	if int64(len(data)) > maxProjectTOMLBytes {
		return nil, nil, errors.New("project TOML exceeds its size limit")
	}
	var document map[string]interface{}
	if _, err := toml.Decode(string(data), &document); err != nil {
		return nil, nil, fmt.Errorf("parse project TOML: %w", err)
	}
	var redacted []string
	if err := sanitizeTOMLTable(document, nil, path, &redacted); err != nil {
		return nil, nil, err
	}
	sort.Strings(redacted)
	var output bytes.Buffer
	if err := toml.NewEncoder(&output).Encode(document); err != nil {
		return nil, nil, fmt.Errorf("encode sanitized project TOML: %w", err)
	}
	return output.Bytes(), redacted, nil
}

func sanitizeTOMLTable(table map[string]interface{}, parent []string, configPath string, redacted *[]string) error {
	for key, value := range table {
		fieldPath := append(append([]string(nil), parent...), key)
		if isCredentialTOMLField(key) {
			if !knownProjectCredentialField(parent, key, configPath) {
				return fmt.Errorf("unsupported credential field %s in %s", strings.Join(fieldPath, "."), configPath)
			}
			delete(table, key)
			*redacted = append(*redacted, strings.Join(fieldPath, "."))
			continue
		}
		switch nested := value.(type) {
		case map[string]interface{}:
			if err := sanitizeTOMLTable(nested, fieldPath, configPath, redacted); err != nil {
				return err
			}
		case []interface{}:
			for _, item := range nested {
				if child, ok := item.(map[string]interface{}); ok {
					if err := sanitizeTOMLTable(child, fieldPath, configPath, redacted); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func knownProjectCredentialField(parent []string, key, configPath string) bool {
	if configPath == "wiki.toml" {
		return len(parent) == 1 && parent[0] == "provider" && key == "api_key"
	}
	return configPath == "synto.toml" && len(parent) == 2 && parent[0] == "providers" &&
		(key == "api_key" || key == "api_key_env")
}

// Credential detection is based only on TOML key names, never user values or
// file contents. max_tokens is a model setting, not an authorization field.
func isCredentialTOMLField(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "max_tokens" || key == "tokens" || strings.HasSuffix(key, "_tokens") || key == "token_limit" || key == "token_count" {
		return false
	}
	for _, marker := range []string{"secret", "credential", "password", "authorization", "private_key", "service_account", "signing_key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	if key == "auth" || strings.HasPrefix(key, "auth_") || strings.HasSuffix(key, "_auth") ||
		strings.Contains(key, "api_key") || key == "access_key" || strings.HasSuffix(key, "_access_key") ||
		key == "token" || strings.HasSuffix(key, "_token") || strings.HasPrefix(key, "token_") {
		return true
	}
	return false
}
