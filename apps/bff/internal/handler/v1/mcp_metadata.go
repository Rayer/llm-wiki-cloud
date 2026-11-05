package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

const (
	mcpMetadataMaxDepth          = 64
	mcpMetadataMaxTopFields      = 32
	mcpMetadataMaxSourceMetaKeys = 16
	mcpMetadataMaxConceptsBytes  = 10 << 20
	mcpMetadataMaxConceptRow     = 16 << 20
)

type mcpJSONCursor struct {
	data []byte
	pos  int
}

type mcpIDPair struct {
	Key   string
	Value string
}

type mcpSourceMetadata struct {
	ID         string
	Slug       string
	Title      string
	SourceFile string
}

type mcpContentIDs struct {
	Concept     []mcpIDPair
	Source      []mcpIDPair
	SourceMeta  []mcpSourceMetadata
	IDRedirects []mcpIDPair
}

type mcpConceptPage struct {
	Slug    string
	Title   string
	Sources []string
	Ordinal int
}

type mcpIDMapShape struct {
	TopFields         int
	ConceptEntries    int
	DormantEntries    int
	EntityEntries     int
	SourceEntries     int
	SourceMetaEntries int
	RedirectEntries   int
	RedirectItems     int
	IDRedirectEntries int
}

type mcpConceptRowShape struct {
	Include bool
	Sources int
}

type mcpConceptsShape struct {
	Rows             []mcpConceptRowShape
	IncludedRows     int
	SourceItems      int
	MaxSourcesPerRow int
}

func inspectMCPIDMap(data []byte) (mcpIDMapShape, error) {
	_, shape, err := parseMCPIDMap(data, false, nil)
	return shape, err
}

func decodeMCPIDMap(data []byte, shape mcpIDMapShape) (mcpContentIDs, error) {
	ids, actual, err := parseMCPIDMap(data, true, &shape)
	if err != nil {
		return mcpContentIDs{}, err
	}
	if actual != shape {
		return mcpContentIDs{}, errors.New("id_map shape changed during decode")
	}
	if err := sortUniqueMCPIDPairs(ids.Concept); err != nil {
		return mcpContentIDs{}, err
	}
	if err := sortUniqueMCPIDPairs(ids.Source); err != nil {
		return mcpContentIDs{}, err
	}
	if err := sortUniqueMCPIDPairs(ids.IDRedirects); err != nil {
		return mcpContentIDs{}, err
	}
	sort.Slice(ids.SourceMeta, func(i, j int) bool { return ids.SourceMeta[i].ID < ids.SourceMeta[j].ID })
	for i := 1; i < len(ids.SourceMeta); i++ {
		if ids.SourceMeta[i-1].ID == ids.SourceMeta[i].ID {
			return mcpContentIDs{}, errors.New("duplicate source metadata id")
		}
	}
	return ids, nil
}

func parseMCPIDMap(data []byte, retain bool, expected *mcpIDMapShape) (mcpContentIDs, mcpIDMapShape, error) {
	var ids mcpContentIDs
	var shape mcpIDMapShape
	if len(data) > generation.MaxFileBytes {
		return ids, shape, errors.New("id_map exceeds byte limit")
	}
	if retain && expected != nil {
		ids.Concept = make([]mcpIDPair, 0, expected.ConceptEntries)
		ids.Source = make([]mcpIDPair, 0, expected.SourceEntries)
		ids.SourceMeta = make([]mcpSourceMetadata, 0, expected.SourceMetaEntries)
		ids.IDRedirects = make([]mcpIDPair, 0, expected.IDRedirectEntries)
	}
	c := mcpJSONCursor{data: data}
	if err := c.objectStart(); err != nil {
		return ids, shape, errors.New("id_map must be an object")
	}
	var topKeys [mcpMetadataMaxTopFields]string
	for first := true; ; first = false {
		name, more, err := c.objectKey(first, true)
		if err != nil {
			return ids, shape, err
		}
		if !more {
			break
		}
		if err := addMCPJSONKey(topKeys[:], &shape.TopFields, name, mcpMetadataMaxTopFields); err != nil {
			return ids, shape, err
		}
		switch name {
		case "concept":
			err = parseMCPStringMap(&c, retain, &ids.Concept, expectedCount(expected, func(s *mcpIDMapShape) *int { return &s.ConceptEntries }), &shape.ConceptEntries)
		case "dormant_concept":
			err = parseMCPStringMap(&c, false, nil, 0, &shape.DormantEntries)
		case "concept_entity_id":
			err = parseMCPStringMap(&c, false, nil, 0, &shape.EntityEntries)
		case "source":
			err = parseMCPStringMap(&c, retain, &ids.Source, expectedCount(expected, func(s *mcpIDMapShape) *int { return &s.SourceEntries }), &shape.SourceEntries)
		case "source_meta":
			err = parseMCPSourceMetaMap(&c, retain, &ids.SourceMeta, expectedCount(expected, func(s *mcpIDMapShape) *int { return &s.SourceMetaEntries }), &shape.SourceMetaEntries)
		case "redirects":
			err = parseMCPStringLists(&c, &shape.RedirectEntries, &shape.RedirectItems)
		case "id_redirects":
			err = parseMCPStringMap(&c, retain, &ids.IDRedirects, expectedCount(expected, func(s *mcpIDMapShape) *int { return &s.IDRedirectEntries }), &shape.IDRedirectEntries)
		default:
			err = c.skipValue(1)
		}
		if err != nil {
			return ids, shape, err
		}
	}
	if err := c.end(); err != nil {
		return ids, shape, err
	}
	if retain && expected != nil && (len(ids.Concept) != expected.ConceptEntries || len(ids.Source) != expected.SourceEntries || len(ids.SourceMeta) != expected.SourceMetaEntries || len(ids.IDRedirects) != expected.IDRedirectEntries) {
		return mcpContentIDs{}, shape, errors.New("id_map retained count mismatch")
	}
	return ids, shape, nil
}

func expectedCount(shape *mcpIDMapShape, field func(*mcpIDMapShape) *int) int {
	if shape == nil {
		return 0
	}
	return *field(shape)
}

func parseMCPStringMap(c *mcpJSONCursor, retain bool, dst *[]mcpIDPair, capacity int, entries *int) error {
	if err := c.objectStart(); err != nil {
		return errors.New("id_map collection must be an object")
	}
	for first := true; ; first = false {
		key, more, err := c.objectKey(first, retain)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if *entries >= generation.MaxFiles {
			return generation.ErrLogicalEntryLimit
		}
		var value string
		if retain {
			value, err = c.stringOrNull()
		} else {
			err = c.skipStringOrNull()
		}
		if err != nil {
			return err
		}
		*entries++
		if retain {
			if dst == nil || len(*dst) >= capacity {
				return errors.New("id_map shape changed during decode")
			}
			*dst = append(*dst, mcpIDPair{Key: key, Value: value})
		}
	}
}

func parseMCPSourceMetaMap(c *mcpJSONCursor, retain bool, dst *[]mcpSourceMetadata, capacity int, entries *int) error {
	if err := c.objectStart(); err != nil {
		return errors.New("source_meta must be an object")
	}
	for first := true; ; first = false {
		id, more, err := c.objectKey(first, retain)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if *entries >= generation.MaxFiles {
			return generation.ErrLogicalEntryLimit
		}
		if err := c.objectStart(); err != nil {
			return errors.New("source_meta row must be an object")
		}
		var meta mcpSourceMetadata
		meta.ID = id
		var keys [mcpMetadataMaxSourceMetaKeys]string
		fieldCount := 0
		for rowFirst := true; ; rowFirst = false {
			name, hasField, err := c.objectKey(rowFirst, true)
			if err != nil {
				return err
			}
			if !hasField {
				break
			}
			if err := addMCPJSONKey(keys[:], &fieldCount, name, mcpMetadataMaxSourceMetaKeys); err != nil {
				return err
			}
			switch name {
			case "slug":
				err = decodeMCPStringField(c, retain, &meta.Slug)
			case "title":
				err = decodeMCPStringField(c, retain, &meta.Title)
			case "source_file":
				err = decodeMCPStringField(c, retain, &meta.SourceFile)
			default:
				err = c.skipValue(3)
			}
			if err != nil {
				return err
			}
		}
		*entries++
		if retain {
			if dst == nil || len(*dst) >= capacity {
				return errors.New("source_meta shape changed during decode")
			}
			*dst = append(*dst, meta)
		}
	}
}

func decodeMCPStringField(c *mcpJSONCursor, retain bool, dst *string) error {
	if retain {
		value, err := c.stringOrNull()
		if err == nil {
			*dst = value
		}
		return err
	}
	return c.skipStringOrNull()
}

func parseMCPStringLists(c *mcpJSONCursor, entries, items *int) error {
	if err := c.objectStart(); err != nil {
		return errors.New("redirects must be an object")
	}
	for first := true; ; first = false {
		_, more, err := c.objectKey(first, false)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if *entries >= generation.MaxFiles {
			return generation.ErrLogicalEntryLimit
		}
		if err := c.arrayStart(); err != nil {
			return errors.New("redirect value must be an array")
		}
		for firstItem := true; ; firstItem = false {
			moreItem, err := c.arrayItem(firstItem)
			if err != nil {
				return err
			}
			if !moreItem {
				break
			}
			if err := c.skipStringOrNull(); err != nil {
				return err
			}
			*items++
		}
		*entries++
	}
}

func inspectMCPConcepts(data []byte) (mcpConceptsShape, error) {
	var shape mcpConceptsShape
	if len(data) > mcpMetadataMaxConceptsBytes {
		return shape, errors.New("concepts metadata exceeds byte limit")
	}
	shape.Rows = make([]mcpConceptRowShape, 0, generation.MaxFiles)
	for start, ordinal := 0, 0; start < len(data); ordinal++ {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			end = len(data)
		} else {
			end += start
		}
		line := bytes.TrimSpace(data[start:end])
		if len(line) > mcpMetadataMaxConceptRow {
			return shape, errors.New("concept row exceeds byte limit")
		}
		start = end + 1
		if len(line) == 0 {
			continue
		}
		if len(shape.Rows) >= generation.MaxFiles {
			return shape, generation.ErrLogicalEntryLimit
		}
		page, count, err := parseMCPConceptRow(line, false, false, 0, ordinal)
		if err != nil {
			return shape, fmt.Errorf("decode concepts metadata row %d: %w", len(shape.Rows)+1, err)
		}
		include := strings.TrimSpace(page.Slug) != ""
		rowShape := mcpConceptRowShape{Include: include, Sources: count}
		if include {
			shape.IncludedRows++
			shape.SourceItems += count
			shape.MaxSourcesPerRow = max(shape.MaxSourcesPerRow, count)
		}
		shape.Rows = append(shape.Rows, rowShape)
	}
	return shape, nil
}

func decodeMCPConcepts(data []byte, shape mcpConceptsShape) ([]mcpConceptPage, error) {
	pages := make([]mcpConceptPage, 0, shape.IncludedRows)
	row := 0
	for start := 0; start < len(data); {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			end = len(data)
		} else {
			end += start
		}
		line := bytes.TrimSpace(data[start:end])
		start = end + 1
		if len(line) == 0 {
			continue
		}
		if row >= len(shape.Rows) {
			return nil, errors.New("concepts shape changed during decode")
		}
		expected := shape.Rows[row]
		page, count, err := parseMCPConceptRow(line, true, expected.Include, expected.Sources, row)
		if err != nil || count != expected.Sources || (strings.TrimSpace(page.Slug) != "") != expected.Include {
			return nil, errors.New("concepts shape changed during decode")
		}
		if !expected.Include {
			row++
			continue
		}
		page.Slug = strings.TrimSpace(page.Slug)
		page.Title = strings.TrimSpace(page.Title)
		if page.Title == "" {
			page.Title = page.Slug
		}
		page.Sources = uniqueMCPReferencesStable(page.Sources)
		pages = append(pages, page)
		row++
	}
	if len(pages) != shape.IncludedRows || row != len(shape.Rows) {
		return nil, errors.New("concepts retained count mismatch")
	}
	return pages, nil
}

func parseMCPConceptRow(data []byte, retain, includeSources bool, capacity, ordinal int) (mcpConceptPage, int, error) {
	var page mcpConceptPage
	page.Ordinal = ordinal
	if retain && includeSources && capacity > 0 {
		page.Sources = make([]string, 0, capacity)
	}
	c := mcpJSONCursor{data: data}
	if err := c.objectStart(); err != nil {
		return page, 0, errors.New("concept row must be an object")
	}
	var keys [mcpMetadataMaxTopFields]string
	fieldCount, sourceCount := 0, 0
	for first := true; ; first = false {
		name, more, err := c.objectKey(first, true)
		if err != nil {
			return page, sourceCount, err
		}
		if !more {
			break
		}
		if err := addMCPJSONKey(keys[:], &fieldCount, name, mcpMetadataMaxTopFields); err != nil {
			return page, sourceCount, err
		}
		switch name {
		case "slug":
			page.Slug, err = c.stringOrNull()
		case "title":
			err = decodeMCPStringField(&c, retain && includeSources, &page.Title)
		case "frontmatter":
			sourceCount, err = parseMCPConceptFrontmatter(&c, retain && includeSources, capacity, &page.Sources)
		default:
			err = c.skipValue(1)
		}
		if err != nil {
			return page, sourceCount, err
		}
	}
	if err := c.end(); err != nil {
		return page, sourceCount, err
	}
	return page, sourceCount, nil
}

func parseMCPConceptFrontmatter(c *mcpJSONCursor, retain bool, capacity int, sources *[]string) (int, error) {
	c.skipSpace()
	if c.hasLiteral("null") {
		c.pos += len("null")
		return 0, nil
	}
	if err := c.objectStart(); err != nil {
		return 0, errors.New("concept frontmatter must be an object or null")
	}
	var keys [mcpMetadataMaxSourceMetaKeys]string
	fieldCount, sourceCount := 0, 0
	for first := true; ; first = false {
		name, more, err := c.objectKey(first, true)
		if err != nil {
			return sourceCount, err
		}
		if !more {
			return sourceCount, nil
		}
		if err := addMCPJSONKey(keys[:], &fieldCount, name, mcpMetadataMaxSourceMetaKeys); err != nil {
			return sourceCount, err
		}
		switch name {
		case "id":
			c.skipSpace()
			if c.pos < len(c.data) && (c.data[c.pos] == '"' || c.hasLiteral("null")) {
				err = c.skipStringOrNull()
			} else {
				err = c.skipValue(2)
			}
		case "sources":
			sourceCount, err = parseMCPSourceValues(c, retain, capacity, sources)
		default:
			err = c.skipValue(2)
		}
		if err != nil {
			return sourceCount, err
		}
	}
}

func parseMCPSourceValues(c *mcpJSONCursor, retain bool, capacity int, sources *[]string) (int, error) {
	c.skipSpace()
	if c.pos >= len(c.data) || c.data[c.pos] != '[' {
		return 0, c.skipValue(2)
	}
	if err := c.arrayStart(); err != nil {
		return 0, err
	}
	count := 0
	for first := true; ; first = false {
		more, err := c.arrayItem(first)
		if err != nil {
			return count, err
		}
		if !more {
			return count, nil
		}
		c.skipSpace()
		if c.pos >= len(c.data) {
			return count, errors.New("truncated source reference")
		}
		if c.data[c.pos] != '"' {
			if err := c.skipValue(3); err != nil {
				return count, err
			}
			continue
		}
		var value string
		if retain {
			var err error
			value, err = c.string()
			if err != nil {
				return count, err
			}
		} else if err := c.skipString(); err != nil {
			return count, err
		}
		count++
		if retain {
			value = strings.TrimSpace(value)
			if value != "" {
				if sources == nil || len(*sources) >= capacity {
					return count, errors.New("concept source count changed during decode")
				}
				*sources = append(*sources, value)
			}
		}
	}
}

func uniqueMCPReferencesStable(values []string) []string {
	if len(values) < 2 {
		return values
	}
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		left, right := values[order[i]], values[order[j]]
		if left == right {
			return order[i] < order[j]
		}
		return left < right
	})
	unique := 0
	for _, index := range order {
		if unique == 0 || values[index] != values[order[unique-1]] {
			order[unique] = index
			unique++
		}
	}
	order = order[:unique]
	sort.Ints(order)
	for i, index := range order {
		values[i] = values[index]
	}
	return values[:unique]
}

func mcpMetadataInputReservation(path string, size int64) int64 {
	if size < 0 || size > generation.MaxFileBytes {
		return 0
	}
	switch path {
	case wikiindex.IDMapPath:
		// The input body and any decoded known keys/string token can coexist;
		// arbitrary nested keys and values are skipped without string allocation.
		return size*2 + (64 << 10)
	case wikiindex.ConceptsJSONLPath:
		if size > mcpMetadataMaxConceptsBytes {
			return 0
		}
		// One decoded row's keys/string values coexist with the raw body; the
		// shape pass preallocates all 10k fixed-size row counters before scanning.
		return size*2 + int64(generation.MaxFiles)*int64(unsafe.Sizeof(mcpConceptRowShape{})) + (64 << 10)
	default:
		return 0
	}
}

func mcpIDMapDecodeReservation(size int64, shape mcpIDMapShape) int64 {
	pairs := int64(shape.ConceptEntries + shape.SourceEntries + shape.IDRedirectEntries)
	meta := int64(shape.SourceMetaEntries)
	stringsCount := pairs*2 + meta*4
	// String payload bytes are bounded by the encoded body. Exact slices hold
	// the structs; 16 bytes per retained string covers allocator size-class
	// slack, while 64 KiB covers parser stacks and sort/runtime headers.
	return size + pairs*int64(unsafe.Sizeof(mcpIDPair{})) + meta*int64(unsafe.Sizeof(mcpSourceMetadata{})) + stringsCount*16 + 4*24 + (64 << 10)
}

func mcpConceptDecodeReservation(size int64, shape mcpConceptsShape) int64 {
	rows := int64(shape.IncludedRows)
	items := int64(shape.SourceItems)
	// The body bounds decoded string payloads. Each raw string item reserves a
	// 16-byte slice header plus allocator slack; the largest per-row 8-byte
	// index array is the stable-order dedup scratch. Row and slice capacities
	// are set from the first-pass counts, so no map or geometric table growth is
	// involved.
	return size + rows*int64(unsafe.Sizeof(mcpConceptPage{})) + items*24 + rows*24 + int64(shape.MaxSourcesPerRow)*8 + (64 << 10)
}

func mcpSnapshotWorkingSetReservation(idMapSize, conceptsSize int64, ids mcpIDMapShape, concepts mcpConceptsShape) int64 {
	refs := int64(ids.ConceptEntries + ids.SourceEntries)
	routes := refs + 2*int64(ids.IDRedirectEntries)
	// Eight encoded ID-map sizes cover retained string payloads and the two
	// simultaneous copies of a worst-case 3x escaped slug (PathEscape and final
	// path). Titles clipped to 512 bytes are separately count-bounded. All
	// lookup indexes are flat slices whose capacities follow exact counts.
	return 8*idMapSize + conceptsSize + refs*int64(unsafe.Sizeof(mcpContentRef{})) + refs*int64(unsafe.Sizeof(mcpSlugIndex{})) + routes*int64(unsafe.Sizeof(mcpRouteIndex{})) + int64(ids.SourceMetaEntries)*int64(unsafe.Sizeof(mcpSourceMetadata{})) + int64(ids.IDRedirectEntries)*int64(unsafe.Sizeof(mcpIDPair{})) + int64(concepts.IncludedRows)*int64(unsafe.Sizeof(mcpConceptPage{})) + int64(concepts.SourceItems)*16 + refs*512 + (128 << 10)
}

func sortUniqueMCPIDPairs(values []mcpIDPair) error {
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	for i := 1; i < len(values); i++ {
		if values[i-1].Key == values[i].Key {
			return errors.New("duplicate id_map key")
		}
	}
	return nil
}

func addMCPJSONKey(keys []string, count *int, key string, limit int) error {
	if *count >= limit {
		return errors.New("JSON object field limit exceeded")
	}
	for i := 0; i < *count; i++ {
		if keys[i] == key {
			return errors.New("duplicate JSON object field")
		}
	}
	keys[*count] = key
	(*count)++
	return nil
}

func (c *mcpJSONCursor) objectStart() error {
	c.skipSpace()
	if c.pos >= len(c.data) || c.data[c.pos] != '{' {
		return errors.New("expected JSON object")
	}
	c.pos++
	return nil
}

func (c *mcpJSONCursor) arrayStart() error {
	c.skipSpace()
	if c.pos >= len(c.data) || c.data[c.pos] != '[' {
		return errors.New("expected JSON array")
	}
	c.pos++
	return nil
}

func (c *mcpJSONCursor) objectKey(first, retain bool) (string, bool, error) {
	c.skipSpace()
	if first {
		if c.take('}') {
			return "", false, nil
		}
	} else {
		if c.take('}') {
			return "", false, nil
		}
		if !c.take(',') {
			return "", false, errors.New("expected JSON object comma")
		}
		c.skipSpace()
		if c.pos < len(c.data) && c.data[c.pos] == '}' {
			return "", false, errors.New("trailing JSON object comma")
		}
	}
	var key string
	var err error
	if retain {
		key, err = c.string()
	} else {
		err = c.skipString()
	}
	if err != nil {
		return "", false, err
	}
	c.skipSpace()
	if !c.take(':') {
		return "", false, errors.New("expected JSON object colon")
	}
	return key, true, nil
}

func (c *mcpJSONCursor) arrayItem(first bool) (bool, error) {
	c.skipSpace()
	if first {
		if c.take(']') {
			return false, nil
		}
		return true, nil
	}
	if c.take(']') {
		return false, nil
	}
	if !c.take(',') {
		return false, errors.New("expected JSON array comma")
	}
	c.skipSpace()
	if c.pos < len(c.data) && c.data[c.pos] == ']' {
		return false, errors.New("trailing JSON array comma")
	}
	return true, nil
}

func (c *mcpJSONCursor) skipValue(parentDepth int) error {
	c.skipSpace()
	if c.pos >= len(c.data) {
		return errors.New("truncated JSON value")
	}
	switch c.data[c.pos] {
	case '"':
		return c.skipString()
	case '{':
		if parentDepth+1 > mcpMetadataMaxDepth {
			return errors.New("metadata nesting depth exceeded")
		}
		if err := c.objectStart(); err != nil {
			return err
		}
		for first := true; ; first = false {
			if _, more, err := c.objectKey(first, false); err != nil {
				return err
			} else if !more {
				return nil
			}
			if err := c.skipValue(parentDepth + 1); err != nil {
				return err
			}
		}
	case '[':
		if parentDepth+1 > mcpMetadataMaxDepth {
			return errors.New("metadata nesting depth exceeded")
		}
		if err := c.arrayStart(); err != nil {
			return err
		}
		for first := true; ; first = false {
			more, err := c.arrayItem(first)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
			if err := c.skipValue(parentDepth + 1); err != nil {
				return err
			}
		}
	case 't':
		return c.literal("true")
	case 'f':
		return c.literal("false")
	case 'n':
		return c.literal("null")
	default:
		return c.number()
	}
}

func (c *mcpJSONCursor) stringOrNull() (string, error) {
	c.skipSpace()
	if c.hasLiteral("null") {
		c.pos += len("null")
		return "", nil
	}
	return c.string()
}

func (c *mcpJSONCursor) skipStringOrNull() error {
	c.skipSpace()
	if c.hasLiteral("null") {
		c.pos += len("null")
		return nil
	}
	return c.skipString()
}

func (c *mcpJSONCursor) string() (string, error) {
	start := c.pos
	if err := c.skipString(); err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(c.data[start:c.pos], &value); err != nil {
		return "", err
	}
	return value, nil
}

func (c *mcpJSONCursor) skipString() error {
	c.skipSpace()
	if c.pos >= len(c.data) || c.data[c.pos] != '"' {
		return errors.New("expected JSON string")
	}
	c.pos++
	for c.pos < len(c.data) {
		b := c.data[c.pos]
		c.pos++
		switch {
		case b == '"':
			return nil
		case b == '\\':
			if c.pos >= len(c.data) {
				return errors.New("truncated JSON escape")
			}
			escape := c.data[c.pos]
			c.pos++
			switch escape {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if c.pos+4 > len(c.data) {
					return errors.New("truncated Unicode escape")
				}
				for _, hex := range c.data[c.pos : c.pos+4] {
					if !isMCPHex(hex) {
						return errors.New("invalid Unicode escape")
					}
				}
				c.pos += 4
			default:
				return errors.New("invalid JSON escape")
			}
		case b < 0x20:
			return errors.New("control byte in JSON string")
		}
	}
	return errors.New("unterminated JSON string")
}

func (c *mcpJSONCursor) number() error {
	start := c.pos
	if c.pos < len(c.data) && c.data[c.pos] == '-' {
		c.pos++
	}
	if c.pos >= len(c.data) {
		return errors.New("invalid JSON number")
	}
	if c.data[c.pos] == '0' {
		c.pos++
	} else if c.data[c.pos] >= '1' && c.data[c.pos] <= '9' {
		for c.pos < len(c.data) && c.data[c.pos] >= '0' && c.data[c.pos] <= '9' {
			c.pos++
		}
	} else {
		return errors.New("invalid JSON number")
	}
	if c.pos < len(c.data) && c.data[c.pos] == '.' {
		c.pos++
		fractionStart := c.pos
		for c.pos < len(c.data) && c.data[c.pos] >= '0' && c.data[c.pos] <= '9' {
			c.pos++
		}
		if c.pos == fractionStart {
			return errors.New("invalid JSON fraction")
		}
	}
	if c.pos < len(c.data) && (c.data[c.pos] == 'e' || c.data[c.pos] == 'E') {
		c.pos++
		if c.pos < len(c.data) && (c.data[c.pos] == '+' || c.data[c.pos] == '-') {
			c.pos++
		}
		exponentStart := c.pos
		for c.pos < len(c.data) && c.data[c.pos] >= '0' && c.data[c.pos] <= '9' {
			c.pos++
		}
		if c.pos == exponentStart {
			return errors.New("invalid JSON exponent")
		}
	}
	if c.pos == start {
		return errors.New("invalid JSON number")
	}
	return nil
}

func (c *mcpJSONCursor) literal(value string) error {
	if !c.hasLiteral(value) {
		return errors.New("invalid JSON literal")
	}
	c.pos += len(value)
	return nil
}

func (c *mcpJSONCursor) hasLiteral(value string) bool {
	return len(c.data)-c.pos >= len(value) && string(c.data[c.pos:c.pos+len(value)]) == value
}

func (c *mcpJSONCursor) end() error {
	c.skipSpace()
	if c.pos != len(c.data) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func (c *mcpJSONCursor) skipSpace() {
	for c.pos < len(c.data) {
		switch c.data[c.pos] {
		case ' ', '\t', '\n', '\r':
			c.pos++
		default:
			return
		}
	}
}

func (c *mcpJSONCursor) take(b byte) bool {
	if c.pos < len(c.data) && c.data[c.pos] == b {
		c.pos++
		return true
	}
	return false
}

func isMCPHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
