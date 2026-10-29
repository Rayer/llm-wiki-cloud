package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
)

func requireJSONKeys(fields map[string]json.RawMessage, required []string) error {
	if len(fields) != len(required) {
		return errors.New("config object has missing or unknown keys")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("config is missing %s", key)
		}
	}
	return nil
}

func requireAuthFileJSONType(name string, raw json.RawMessage, kind string, nullable bool) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("Auth config field %s has invalid type", name)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if nullable {
			return nil
		}
		return fmt.Errorf("Auth config field %s cannot be null", name)
	}
	invalid := func() error { return fmt.Errorf("Auth config field %s has invalid type", name) }
	switch kind {
	case "string":
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "integer":
		var value int
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid()
		}
	case "string_array":
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
			return invalid()
		}
		for index, value := range values {
			var item string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &item) != nil {
				return fmt.Errorf("Auth config field %s[%d] has invalid type", name, index)
			}
		}
	case "object":
		var value map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &value); err != nil || value == nil {
			return invalid()
		}
	default:
		return errors.New("Auth config schema contains an unsupported type")
	}
	return nil
}

func validateRuntimeURL(raw string, local bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		(u.Scheme != "https" && !(local && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return errors.New("invalid URL")
	}
	return nil
}

func validateOrigin(raw string) error {
	if raw == "" || raw == "*" {
		return errors.New("invalid origin")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid origin")
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
