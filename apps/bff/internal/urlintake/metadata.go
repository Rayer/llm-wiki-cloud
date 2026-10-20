package urlintake

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type SourceMetadata struct {
	Title       string
	SourceURL   string
	Author      string
	Published   string
	Description string
	Tags        []string
}

// MergeRawMarkdown creates or refreshes a raw article while preserving its
// original created date, provenance and caller-owned frontmatter. It returns
// the original bytes unchanged when the persisted candidate is identical.
func MergeRawMarkdown(existing []byte, incoming SourceMetadata, markdown string, now time.Time) ([]byte, bool, error) {
	metadata, previousBody, hadFrontmatter, err := parseRawFrontmatter(existing)
	if err != nil {
		return nil, false, err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	setWhenPresent := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	setWhenPresent("title", incoming.Title)
	if isEmptyMetadataValue(metadata["source"]) {
		setWhenPresent("source", incoming.SourceURL)
	}
	setWhenPresent("author", incoming.Author)
	setWhenPresent("published", incoming.Published)
	setWhenPresent("description", incoming.Description)
	if isEmptyMetadataValue(metadata["created"]) {
		metadata["created"] = now.UTC().Format("2006-01-02")
	}
	if len(incoming.Tags) != 0 {
		tags, err := mergeTags(metadata["tags"], incoming.Tags)
		if err != nil {
			return nil, false, err
		}
		if len(tags) > 0 {
			metadata["tags"] = tags
		}
	}

	if hadFrontmatter && reflect.DeepEqual(metadata, metadataBefore(existing)) && bytes.Equal(previousBody, []byte(markdown)) {
		return existing, true, nil
	}
	frontmatter, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, false, fmt.Errorf("serialize raw metadata: %w", err)
	}
	result := make([]byte, 0, len(frontmatter)+len(markdown)+9)
	result = append(result, []byte("---\n")...)
	result = append(result, frontmatter...)
	result = append(result, []byte("---\n")...)
	result = append(result, markdown...)
	return result, false, nil
}

func metadataBefore(raw []byte) map[string]any {
	metadata, _, _, err := parseRawFrontmatter(raw)
	if err != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}

func parseRawFrontmatter(raw []byte) (map[string]any, []byte, bool, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil, false, nil
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if len(lines) == 0 || strings.TrimSpace(string(lines[0])) != "---" {
		return map[string]any{}, raw, false, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(string(lines[i])) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, nil, false, errors.New("raw frontmatter is unterminated")
	}
	header := bytes.Join(lines[1:end], nil)
	body := bytes.Join(lines[end+1:], nil)
	metadata := map[string]any{}
	if len(bytes.TrimSpace(header)) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(header))
		if err := decoder.Decode(&metadata); err != nil {
			return nil, nil, false, fmt.Errorf("raw frontmatter is invalid: %w", err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, nil, false, errors.New("raw frontmatter must contain one YAML document")
		}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	return metadata, body, true, nil
}

func mergeTags(existing any, incoming []string) ([]any, error) {
	var tags []string
	switch value := existing.(type) {
	case nil:
	case string:
		tags = append(tags, value)
	case []any:
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("existing raw tags are not strings")
			}
			tags = append(tags, text)
		}
	case []string:
		tags = append(tags, value...)
	default:
		return nil, errors.New("existing raw tags have an unsupported format")
	}
	seen := map[string]struct{}{}
	result := make([]any, 0, len(tags)+len(incoming))
	for _, tag := range append(tags, incoming...) {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, tag)
	}
	return result, nil
}

func isEmptyMetadataValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}
