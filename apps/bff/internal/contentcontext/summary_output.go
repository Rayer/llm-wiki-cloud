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

type SummaryOverview struct {
	Summary    string   `json:"summary"`
	ShortTitle string   `json:"short_title"`
	TopicTags  []string `json:"topic_tags"`
}

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

// ParseSummaryOverviewOutput accepts one strict three-field JSON object. The
// complete overview is validated together so the stored values always describe
// the same inference and publication batch.
func ParseSummaryOverviewOutput(raw []byte) (SummaryOverview, error) {
	invalid := func() (SummaryOverview, error) {
		return SummaryOverview{}, errors.New("invalid content-context overview output")
	}
	if len(raw) == 0 || len(raw) > SummaryMaxOutputBytes || !utf8.Valid(raw) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return invalid()
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return invalid()
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return invalid()
		}
		key, ok := token.(string)
		if !ok {
			return invalid()
		}
		if _, exists := fields[key]; exists {
			return invalid()
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return invalid()
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil || len(fields) != 3 {
		return invalid()
	}
	if _, ok := fields["summary"]; !ok {
		return invalid()
	}
	if _, ok := fields["short_title"]; !ok {
		return invalid()
	}
	if _, ok := fields["topic_tags"]; !ok {
		return invalid()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return invalid()
	}
	var overview SummaryOverview
	if json.Unmarshal(raw, &overview) != nil {
		return invalid()
	}
	if !validOverviewText(overview.Summary, 1000) || !validOverviewText(overview.ShortTitle, 24) || len(overview.TopicTags) < 1 || len(overview.TopicTags) > 5 {
		return invalid()
	}
	seen := make(map[string]struct{}, len(overview.TopicTags))
	for _, tag := range overview.TopicTags {
		trimmed := strings.TrimSpace(tag)
		if !validOverviewText(trimmed, 16) || trimmed != tag {
			return invalid()
		}
		key := strings.ToLower(trimmed)
		if _, duplicate := seen[key]; duplicate {
			return invalid()
		}
		seen[key] = struct{}{}
	}
	return overview, nil
}

func validOverviewText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maxRunes &&
		!strings.Contains(value, "<") && !strings.Contains(value, ">")
}
