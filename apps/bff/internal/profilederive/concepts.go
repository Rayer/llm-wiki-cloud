package profilederive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

// Fields mirror cache.Entry, serialized by wikiindex buildSyntoConceptsJSONL.
// Keep frontmatter raw so derivation retains its exact provider context.
type conceptRow struct {
	Slug        string          `json:"slug"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Sources     []string        `json:"sources"`
	Frontmatter json.RawMessage `json:"frontmatter"`
}

// ValidateConceptSnapshot parses one already-read manifest-listed JSONL file,
// validates each stable ID against the generation's active ID map, and returns
// the exact row bytes for provider context. It does not access concept pages.
func ValidateConceptSnapshot(data []byte, idMap wikiindex.IDMap) ([]Concept, string, error) {
	digest := profileartifacts.SHA256(data)
	if !utf8.Valid(data) {
		return nil, digest, errors.New("canonical concepts JSONL is not valid UTF-8")
	}
	active := idMap.Concept
	if active == nil {
		active = map[string]string{}
	}
	if len(data) == 0 {
		if len(active) != 0 {
			return nil, digest, errors.New("empty concepts JSONL disagrees with active ID map")
		}
		return []Concept{}, digest, nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	concepts := make([]Concept, 0, len(lines))
	seenIDs := make(map[string]struct{}, len(lines))
	seenSlugs := make(map[string]struct{}, len(lines))
	for index, line := range lines {
		if len(line) == 0 {
			return nil, digest, fmt.Errorf("canonical concepts JSONL has an empty row at line %d", index+1)
		}
		if err := rejectDuplicateKeys(line); err != nil {
			return nil, digest, fmt.Errorf("invalid canonical concept row %d: %w", index+1, err)
		}
		var row conceptRow
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&row); err != nil {
			return nil, digest, fmt.Errorf("invalid canonical concept row %d: %w", index+1, err)
		}
		if _, err := decoder.Token(); err != io.EOF {
			return nil, digest, fmt.Errorf("canonical concept row %d has trailing data", index+1)
		}
		if row.Slug == "" || row.Slug != strings.TrimSpace(row.Slug) || row.Slug == "." || row.Slug == ".." || strings.ContainsAny(row.Slug, `/\\`) {
			return nil, digest, fmt.Errorf("canonical concept row %d has an invalid slug", index+1)
		}
		var frontmatter map[string]json.RawMessage
		if len(row.Frontmatter) == 0 || string(row.Frontmatter) == "null" || json.Unmarshal(row.Frontmatter, &frontmatter) != nil {
			return nil, digest, fmt.Errorf("canonical concept row %d has invalid frontmatter", index+1)
		}
		var id string
		if json.Unmarshal(frontmatter["id"], &id) != nil || !wikiindex.ValidSyntoEntityID(id) || id != strings.TrimSpace(id) {
			return nil, digest, fmt.Errorf("canonical concept row %d has no stable entity ID", index+1)
		}
		if active[id] != row.Slug {
			return nil, digest, fmt.Errorf("canonical concept ID %q disagrees with active ID map", id)
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, digest, fmt.Errorf("duplicate canonical concept ID %q", id)
		}
		if _, duplicate := seenSlugs[row.Slug]; duplicate {
			return nil, digest, fmt.Errorf("duplicate canonical concept slug %q", row.Slug)
		}
		seenIDs[id], seenSlugs[row.Slug] = struct{}{}, struct{}{}
		concepts = append(concepts, Concept{ID: id, Slug: row.Slug, Title: row.Title, Frontmatter: append(json.RawMessage(nil), row.Frontmatter...), Row: string(line)})
	}
	if len(seenIDs) != len(active) {
		return nil, digest, errors.New("canonical concepts JSONL does not contain every active ID map concept")
	}
	return concepts, digest, nil
}
