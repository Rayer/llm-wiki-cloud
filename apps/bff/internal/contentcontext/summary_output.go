package contentcontext

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const SummaryMaxOutputBytes = 64 << 10

// ParseSummaryOutput accepts exactly one strict JSON object with one nonempty
// plain-text summary field bounded by the public 1000-codepoint contract.
func ParseSummaryOutput(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > SummaryMaxOutputBytes || !utf8.Valid(raw) {
		return "", errors.New("invalid content-context summary output")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return "", errors.New("invalid content-context summary output")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return "", errors.New("invalid content-context summary output")
	}
	fields := make(map[string]json.RawMessage, 1)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", errors.New("invalid content-context summary output")
		}
		key, ok := token.(string)
		if !ok {
			return "", errors.New("invalid content-context summary output")
		}
		if _, exists := fields[key]; exists {
			return "", errors.New("invalid content-context summary output")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("invalid content-context summary output")
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return "", errors.New("invalid content-context summary output")
	}
	if len(fields) != 1 {
		return "", errors.New("invalid content-context summary output")
	}
	value, ok := fields["summary"]
	if !ok {
		return "", errors.New("invalid content-context summary output")
	}
	var summary string
	if err := json.Unmarshal(value, &summary); err != nil || !utf8.ValidString(summary) || strings.TrimSpace(summary) == "" || utf8.RuneCountInString(summary) > 1000 {
		return "", errors.New("invalid content-context summary output")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("invalid content-context summary output")
	}
	return summary, nil
}
