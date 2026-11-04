package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxMCPContentFrontmatterBytes = 1 << 20
	maxMCPContentReferences       = 20_000
	maxMCPReferenceDefinitions    = 4096
	maxMCPReferenceTargetBytes    = 2048
	maxMCPReferenceLabelBytes     = 2048
	maxMCPHTMLTagBytes            = 4096
)

type mcpResolvedRef struct {
	Relation           string          `json:"relation"`
	ReferenceID        string          `json:"reference_id"`
	ReferenceIndex     int             `json:"reference_index"`
	CandidateOffset    int             `json:"candidate_offset"`
	CandidatesComplete bool            `json:"candidates_complete"`
	TargetText         string          `json:"target_text"`
	Label              string          `json:"label"`
	Kind               *string         `json:"kind"`
	Resolution         string          `json:"resolution"`
	ID                 *string         `json:"id"`
	Title              *string         `json:"title"`
	CitationPath       *string         `json:"citation_path"`
	Candidates         []mcpContentRef `json:"candidates"`
	Origin             string          `json:"origin"`
	ByteStart          *int            `json:"byte_start"`
	ByteEnd            *int            `json:"byte_end"`
}

type mcpMarkdownReference struct {
	Target string
	Label  string
	Origin string
	Start  int
	End    int
}

type mcpMCPFrontmatter struct {
	Sources    []string
	HasSources bool
	BodyStart  int
	Available  bool
}

type mcpCandidateSet struct {
	Resolution string
	Kind       *string
	ID         *string
	Title      *string
	Indexes    []int
}

func parseMCPContentFrontmatter(data []byte) mcpMCPFrontmatter {
	result := mcpMCPFrontmatter{Available: true}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return result
	}
	frontmatterEnd := min(len(data), maxMCPContentFrontmatterBytes+9)
	end := bytes.Index(data[4:frontmatterEnd], []byte("\n---\n"))
	if end < 0 || end+4 > maxMCPContentFrontmatterBytes {
		return mcpMCPFrontmatter{Available: false}
	}
	bodyStart := 4 + end + len("\n---\n")
	var fields map[string]any
	if err := yaml.Unmarshal(data[4:4+end], &fields); err != nil {
		return mcpMCPFrontmatter{Available: false}
	}
	result.BodyStart = bodyStart
	if value, exists := fields["sources"]; exists {
		result.HasSources = true
		var values []string
		switch typed := value.(type) {
		case nil:
		case string:
			values = append(values, typed)
		case []any:
			for _, item := range typed {
				text, ok := item.(string)
				if !ok {
					return mcpMCPFrontmatter{Available: false}
				}
				values = append(values, text)
			}
		default:
			return mcpMCPFrontmatter{Available: false}
		}
		result.Sources = normalizedMCPReferences(values)
	}
	if len(result.Sources) > maxMCPContentReferences {
		return mcpMCPFrontmatter{Available: false}
	}
	return result
}

func normalizedMCPReferences(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mcpReferenceID(scopeBinding, generationID, manifestDigest, kind, id, revision string, index int) string {
	hash := sha256.New()
	for _, value := range []string{"1", scopeBinding, generationID, manifestDigest, kind, id, revision, fmt.Sprint(index)} {
		var size [4]byte
		size[0], size[1], size[2], size[3] = byte(len(value)>>24), byte(len(value)>>16), byte(len(value)>>8), byte(len(value))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *mcpContentSnapshot) synthesisCandidates(target string) mcpCandidateSet {
	if index, ok := s.indexByID[mcpContentKindSource+"\x00"+target]; ok {
		return s.candidateSet([]int{index}, mcpContentKindSource)
	}
	indexes := make([]int, 0, 2)
	for index, ref := range s.refs {
		if ref.Kind != mcpContentKindSource {
			continue
		}
		meta := s.ids.SourceMeta[ref.ID]
		if target == ref.Slug || target == ref.exactTitle || target == meta.SourceFile || target == "wiki/sources/"+ref.Slug+".md" || target == "sources/"+ref.Slug+".md" {
			indexes = append(indexes, index)
		}
	}
	return s.candidateSet(indexes, mcpContentKindSource)
}

func (s *mcpContentSnapshot) mentionCandidates(target string) (bool, mcpCandidateSet) {
	target = strings.TrimSpace(target)
	if target == "" {
		return false, s.candidateSet(nil, "")
	}
	if strings.HasPrefix(target, "//") {
		return true, mcpCandidateSet{Resolution: "external", Indexes: []int{}}
	}
	parsed, err := url.Parse(target)
	if err == nil && parsed.IsAbs() {
		return true, mcpCandidateSet{Resolution: "external", Indexes: []int{}}
	}
	name := target
	if before, _, found := strings.Cut(name, "#"); found {
		name = before
	}
	if kind, slug, typed := typedMCPWikiTarget(name); typed {
		decoded, decodeErr := url.PathUnescape(slug)
		if decodeErr != nil || strings.ContainsAny(decoded, "/\\\x00") || strings.Contains(decoded, "..") || decoded == "" {
			return false, s.candidateSet(nil, kind)
		}
		if ref, exists := s.byCanonicalRoute[kind+"\x00"+decoded]; exists {
			if index, ok := s.indexByID[kind+"\x00"+ref.ID]; ok {
				return false, s.candidateSet([]int{index}, kind)
			}
		}
		indexes := make([]int, 0, len(s.bySlugAndKind[kind+"\x00"+decoded]))
		for _, ref := range s.bySlugAndKind[kind+"\x00"+decoded] {
			if index, exists := s.indexByID[kind+"\x00"+ref.ID]; exists {
				indexes = append(indexes, index)
			}
		}
		sort.Ints(indexes)
		return false, s.candidateSet(indexes, kind)
	}
	if !strings.ContainsAny(target, "/\\\x00") {
		if index, exists := s.indexByID[mcpContentKindConcept+"\x00"+target]; exists {
			return false, s.candidateSet([]int{index}, mcpContentKindConcept)
		}
		if index, exists := s.indexByID[mcpContentKindSource+"\x00"+target]; exists {
			return false, s.candidateSet([]int{index}, mcpContentKindSource)
		}
	}
	indexes := make([]int, 0, 2)
	for index, ref := range s.refs {
		if target == ref.Slug || target == ref.exactTitle {
			indexes = append(indexes, index)
		}
	}
	return false, s.candidateSet(indexes, "")
}

func (s *mcpContentSnapshot) candidateSet(indexes []int, forcedKind string) mcpCandidateSet {
	set := mcpCandidateSet{Resolution: "unresolved", Indexes: indexes}
	if forcedKind != "" {
		set.Kind = stringPointer(forcedKind)
	}
	if len(indexes) == 0 {
		return set
	}
	if len(indexes) == 1 {
		ref := s.refs[indexes[0]]
		set.Resolution, set.Kind = "resolved", stringPointer(ref.Kind)
		set.ID, set.Title = stringPointer(ref.ID), stringPointer(ref.Title)
		return set
	}
	kind := s.refs[indexes[0]].Kind
	for _, index := range indexes[1:] {
		if s.refs[index].Kind != kind {
			kind = ""
			break
		}
	}
	set.Resolution = "ambiguous"
	if kind == "" {
		set.Kind = nil
	} else {
		set.Kind = stringPointer(kind)
	}
	return set
}

func typedMCPWikiTarget(name string) (string, string, bool) {
	for _, prefix := range []struct{ path, kind string }{{"/concepts/", mcpContentKindConcept}, {"/sources/", mcpContentKindSource}, {"wiki/sources/", mcpContentKindSource}, {"sources/", mcpContentKindSource}} {
		if strings.HasPrefix(name, prefix.path) {
			slug := strings.TrimSuffix(strings.TrimPrefix(name, prefix.path), ".md")
			return prefix.kind, slug, true
		}
	}
	if strings.HasPrefix(name, "wiki/") && strings.HasSuffix(name, ".md") {
		return mcpContentKindConcept, strings.TrimSuffix(strings.TrimPrefix(name, "wiki/"), ".md"), true
	}
	return "", "", false
}

func pageMCPReadReferences(ctx context.Context, snapshot *mcpContentSnapshot, identity mcpProjectKeyIdentity, document mcpContentRef, revision string, data []byte, bodyStart int, synthesisSources []string, cursor mcpContentCursor, hasCursor bool, pageMax int, responseBase map[string]any) ([]mcpResolvedRef, []mcpResolvedRef, any, bool, error) {
	if pageMax < 1 || pageMax > 50 {
		return nil, nil, nil, false, errors.New("metadata_unavailable")
	}
	synthesis, mentions := make([]mcpResolvedRef, 0), make([]mcpResolvedRef, 0)
	startIndex, startCandidate := 0, 0
	if hasCursor {
		if cursor.ReferenceIndex < 0 || cursor.CandidateOffset < 0 || cursor.Generation != snapshot.generation || cursor.ManifestSHA != snapshot.manifestSHA || cursor.Revision != revision || cursor.ScopeBinding != mcpContentScopeBinding(identity) || cursor.Kind != document.Kind {
			return nil, nil, nil, false, errors.New("invalid_cursor")
		}
		startIndex, startCandidate = cursor.ReferenceIndex, cursor.CandidateOffset
	}
	index := 0
	fragmentCount, candidateCount := 0, 0
	started, cursorIndexSeen := !hasCursor, !hasCursor
	stopped := false
	more := false
	nextIndex, nextCandidate := 0, 0
	var pageErr error
	addReference := func(reference mcpMarkdownReference, relation string, currentIndex int) bool {
		if ctx.Err() != nil {
			pageErr = ctx.Err()
			return false
		}
		if index >= maxMCPContentReferences {
			pageErr = errors.New("metadata_unavailable")
			index++
			return true
		}
		current := index
		index++
		if current == startIndex {
			cursorIndexSeen = true
			started = true
		}
		if !started || stopped {
			if current > startIndex || stopped {
				more = true
			}
			return true
		}
		if current < startIndex {
			return true
		}
		if currentIndex != current {
			pageErr = errors.New("metadata_unavailable")
			return true
		}
		var candidateSet mcpCandidateSet
		var external bool
		if relation == "synthesis_source" {
			candidateSet = snapshot.synthesisCandidates(reference.Target)
		} else {
			external, candidateSet = snapshot.mentionCandidates(reference.Target)
		}
		if candidateSet.Resolution == "" {
			pageErr = errors.New("metadata_unavailable")
			return true
		}
		resolution, kind, id, title := candidateSet.Resolution, candidateSet.Kind, candidateSet.ID, candidateSet.Title
		var citation *string
		if resolution == "resolved" && len(candidateSet.Indexes) == 1 {
			citation = stringPointer(snapshot.refs[candidateSet.Indexes[0]].CitationPath)
		}
		candidateTotal := len(candidateSet.Indexes)
		if external && candidateTotal != 0 {
			pageErr = errors.New("metadata_unavailable")
			return true
		}
		offset := 0
		if current == startIndex {
			offset = startCandidate
		}
		if offset > candidateTotal || candidateTotal > 0 && offset == candidateTotal {
			pageErr = errors.New("invalid_cursor")
			return true
		}
		if candidateTotal == 0 && offset != 0 {
			pageErr = errors.New("invalid_cursor")
			return true
		}
		if fragmentCount >= pageMax {
			more = true
			nextIndex, nextCandidate = current, offset
			stopped = true
			return true
		}
		end := offset
		if candidateTotal > 0 {
			remaining := pageMax - candidateCount
			if remaining <= 0 {
				more = true
				nextIndex, nextCandidate = current, offset
				stopped = true
				return true
			}
			end = min(candidateTotal, offset+remaining)
		}
		fragmentCandidates := make([]mcpContentRef, 0, end-offset)
		for _, candidateIndex := range candidateSet.Indexes[offset:end] {
			fragmentCandidates = append(fragmentCandidates, snapshot.refs[candidateIndex])
		}
		fragment := mcpResolvedRef{
			Relation: relation, ReferenceID: mcpReferenceID(mcpContentScopeBinding(identity), snapshot.generation, snapshot.manifestSHA, document.Kind, document.ID, revision, current),
			ReferenceIndex: current, CandidateOffset: offset, CandidatesComplete: end == candidateTotal,
			TargetText: reference.Target, Label: reference.Label, Kind: kind, Resolution: resolution,
			ID: id, Title: title, CitationPath: citation, Candidates: fragmentCandidates, Origin: reference.Origin,
		}
		if relation == "synthesis_source" {
			fragment.ByteStart, fragment.ByteEnd = nil, nil
		} else {
			fragment.ByteStart, fragment.ByteEnd = mcpReferenceRange(reference)
		}
		fits := false
		for {
			fragment.Candidates = fragmentCandidates
			fragment.CandidatesComplete = end == candidateTotal
			trialSynthesis, trialMentions := synthesis, mentions
			if relation == "synthesis_source" {
				trialSynthesis = append(append([]mcpResolvedRef(nil), synthesis...), fragment)
			} else {
				trialMentions = append(append([]mcpResolvedRef(nil), mentions...), fragment)
			}
			if mcpReferencePageFits(responseBase, trialSynthesis, trialMentions) {
				if relation == "synthesis_source" {
					synthesis = trialSynthesis
				} else {
					mentions = trialMentions
				}
				fits = true
				break
			}
			if len(fragmentCandidates) == 0 {
				break
			}
			fragmentCandidates = fragmentCandidates[:len(fragmentCandidates)-1]
			end--
		}
		if !fits {
			if fragmentCount > 0 {
				more = true
				nextIndex, nextCandidate = current, offset
				stopped = true
				return true
			}
			pageErr = errors.New("response_limit")
			return true
		}
		fragmentCount++
		candidateCount += len(fragmentCandidates)
		if end < candidateTotal {
			more = true
			nextIndex, nextCandidate = current, end
			stopped = true
			return true
		}
		if fragmentCount >= pageMax {
			more = true
			nextIndex, nextCandidate = current+1, 0
			stopped = true
		}
		if candidateCount >= pageMax {
			more = true
			nextIndex, nextCandidate = current+1, 0
			stopped = true
		}
		return true
	}
	for _, source := range synthesisSources {
		if !addReference(mcpMarkdownReference{Target: source, Label: source, Origin: "frontmatter.sources", Start: -1, End: -1}, "synthesis_source", index) {
			break
		}
	}
	scanComplete := scanMCPMarkdownMentions(ctx, data, bodyStart, func(reference mcpMarkdownReference) bool {
		return addReference(reference, "mention", index)
	})
	if !scanComplete && pageErr == nil {
		pageErr = errors.New("metadata_unavailable")
	}
	if stopped && nextIndex == index && nextCandidate == 0 {
		more = false
	}
	if pageErr != nil {
		return nil, nil, nil, false, pageErr
	}
	if hasCursor && !cursorIndexSeen {
		if startIndex > index || startIndex == index && startCandidate != 0 {
			return nil, nil, nil, false, errors.New("invalid_cursor")
		}
	}
	if hasCursor && cursorIndexSeen {
		// The cursor's candidate position is validated against the exact
		// reference candidate cardinality when its index is emitted.
	}
	refsComplete := !more
	var next any
	if !refsComplete {
		next = encodeMCPContentCursor(mcpContentCursor{SchemaVersion: 1, Operation: "refs", Kind: document.Kind, ID: document.ID,
			Generation: snapshot.generation, ManifestSHA: snapshot.manifestSHA, ScopeBinding: mcpContentScopeBinding(identity),
			Revision: revision, ReferenceIndex: nextIndex, CandidateOffset: nextCandidate})
	}
	return synthesis, mentions, next, refsComplete, nil
}

func mcpReferencePageFits(base map[string]any, synthesis, mentions []mcpResolvedRef) bool {
	value := make(map[string]any, len(base)+2)
	for key, item := range base {
		value[key] = item
	}
	value["synthesis_sources"] = synthesis
	value["mentions"] = mentions
	value["refs_complete"] = false
	value["next_refs_cursor"] = strings.Repeat("x", mcpContentMaxCursor)
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	result := struct {
		Content           []map[string]string `json:"content"`
		StructuredContent json.RawMessage     `json:"structuredContent"`
	}{Content: []map[string]string{{"type": "text", "text": string(encoded)}}, StructuredContent: encoded}
	// Model the longest accepted protected request ID and a complete JSON-RPC
	// envelope; the extra bytes cover SDK response fields added around results.
	requestID := json.RawMessage(`"` + strings.Repeat(`\u0078`, 682) + `"`)
	wire, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: requestID, Result: result})
	return err == nil && len(wire)+512 <= maxMCPProtectedResponseBytes
}

func stringPointer(value string) *string { return &value }

// scanMCPMarkdownMentions is a deterministic byte-state scanner. It skips
// fenced and inline code, HTML comments/tags and image links while preserving
// source offsets; link reference definitions are resolved in a bounded first
// pass and usages are emitted in document order in the second pass.
func scanMCPMarkdownMentions(ctx context.Context, data []byte, bodyStart int, yield func(mcpMarkdownReference) bool) bool {
	body := data[bodyStart:]
	definitions := make(map[string]string)
	if !scanMCPMarkdown(ctx, body, 0, definitions, nil, true) {
		return false
	}
	return scanMCPMarkdown(ctx, body, bodyStart, definitions, yield, false)
}

func scanMCPMarkdown(ctx context.Context, body []byte, base int, definitions map[string]string, yield func(mcpMarkdownReference) bool, collectDefinitions bool) bool {
	fenceChar := byte(0)
	fenceLen := 0
	inlineTicks := 0
	inScript := false
	definitionsCount := 0
	for i := 0; i < len(body); {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return false
		}
		lineStart := i == 0 || body[i-1] == '\n'
		if lineStart {
			lineEnd := findByteContext(ctx, body, i, '\n')
			if lineEnd < 0 {
				if ctx.Err() != nil {
					return false
				}
				lineEnd = len(body) - i
			} else {
				lineEnd -= i
			}
			line := body[i : i+lineEnd]
			space := 0
			for space < len(line) && space < 4 && line[space] == ' ' {
				space++
			}
			if space <= 3 && space < len(line) && (line[space] == '`' || line[space] == '~') {
				count := 0
				for space+count < len(line) && line[space+count] == line[space] {
					count++
				}
				if count >= 3 {
					if fenceChar == 0 {
						fenceChar, fenceLen = line[space], count
					} else if line[space] == fenceChar && count >= fenceLen {
						fenceChar, fenceLen = 0, 0
					}
					i += lineEnd
					if i < len(body) {
						i++
					}
					continue
				}
			}
			if fenceChar != 0 {
				i += lineEnd
				if i < len(body) {
					i++
				}
				continue
			}
			if collectDefinitions {
				if key, target, ok := parseMCPReferenceDefinition(ctx, line); ok {
					if definitionsCount < maxMCPReferenceDefinitions {
						definitions[key] = target
						definitionsCount++
					}
					i += lineEnd
					if i < len(body) {
						i++
					}
					continue
				}
			}
		}
		if fenceChar != 0 {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			continue
		}
		if inScript {
			if body[i] == '<' && asciiEqualFoldPrefix(body[i:], "</script") {
				if end := findByteContext(ctx, body, i, '>'); end >= 0 {
					i = end + 1
					inScript = false
					continue
				}
				if ctx.Err() != nil {
					return false
				}
			}
			i++
			continue
		}
		if inlineTicks > 0 {
			if body[i] == '`' {
				run := countByteRun(body, i, '`')
				if run == inlineTicks {
					inlineTicks = 0
				}
				i += run
				continue
			}
			i++
			continue
		}
		if body[i] == '`' {
			inlineTicks = countByteRun(body, i, '`')
			i += inlineTicks
			continue
		}
		if bytes.HasPrefix(body[i:], []byte("<!--")) {
			end := findTokenContext(ctx, body, i+4, []byte("-->"))
			if end < 0 {
				if ctx.Err() != nil {
					return false
				}
				return true
			}
			i = end + 3
			continue
		}
		if body[i] == '<' {
			if end := findByteContext(ctx, body, i, '>'); end >= 0 {
				tag := bytes.TrimSpace(body[i+1 : end])
				if asciiEqualFoldPrefix(tag, "script") {
					inScript = true
				}
				i = end + 1
				continue
			}
			if ctx.Err() != nil {
				return false
			}
		}
		if bytes.HasPrefix(body[i:], []byte("[[")) {
			if end := findTokenContext(ctx, body, i+2, []byte("]]")); end >= 0 {
				inside := body[i+2 : end]
				if len(inside) > maxMCPReferenceTargetBytes+maxMCPReferenceLabelBytes+1 {
					return false
				}
				target, label, ok := splitMCPWikiTargetBytes(inside)
				if !ok {
					return false
				}
				if target != "" {
					ref := mcpMarkdownReference{Target: target, Label: label, Origin: "wikilink", Start: base + i, End: base + end + 2}
					if yield != nil && !collectDefinitions && !yield(ref) {
						return false
					}
				}
				i = end + 2
				continue
			}
			if ctx.Err() != nil {
				return false
			}
			return true
		}
		if body[i] == '[' && (i == 0 || body[i-1] != '!') {
			reference, end, ok := parseMCPMarkdownLink(ctx, body, i, definitions)
			if ctx.Err() != nil {
				return false
			}
			if end < 0 {
				return false
			}
			if ok {
				if yield != nil && !collectDefinitions {
					reference.Start += base
					reference.End += base
					if !yield(reference) {
						return false
					}
				}
				i = end
				continue
			}
			if end > i {
				i = end
				continue
			}
		}
		i++
	}
	return true
}

func parseMCPReferenceDefinition(ctx context.Context, line []byte) (string, string, bool) {
	leading := 0
	for leading < len(line) && leading < 4 && line[leading] == ' ' {
		leading++
	}
	if leading > 3 || len(line)-leading < 5 || line[leading] != '[' {
		return "", "", false
	}
	trimmed := line[leading:]
	close := findTokenContext(ctx, trimmed, 1, []byte("]:"))
	if close <= 1 {
		return "", "", false
	}
	keyBytes := trimmed[1:close]
	rawTarget := trimmed[close+2:]
	if len(rawTarget) > maxMCPReferenceTargetBytes+2 {
		return "", "", false
	}
	targetBytes := bytes.TrimSpace(rawTarget)
	if len(keyBytes) > 256 || len(targetBytes) > maxMCPReferenceTargetBytes {
		return "", "", false
	}
	key := normalizeMCPReferenceLabel(string(keyBytes))
	if len(targetBytes) >= 2 && targetBytes[0] == '<' && targetBytes[len(targetBytes)-1] == '>' {
		targetBytes = targetBytes[1 : len(targetBytes)-1]
	}
	if key == "" || len(targetBytes) == 0 || len(targetBytes) > maxMCPReferenceTargetBytes {
		return "", "", false
	}
	return key, string(targetBytes), true
}

func parseMCPMarkdownLink(ctx context.Context, data []byte, start int, definitions map[string]string) (mcpMarkdownReference, int, bool) {
	labelEnd := findUnescapedByte(ctx, data, start+1, ']', maxMCPReferenceLabelBytes+1)
	if labelEnd < 0 {
		return mcpMarkdownReference{}, skipMCPMarkdownLine(ctx, data, start), false
	}
	if labelEnd-start > maxMCPReferenceLabelBytes {
		return mcpMarkdownReference{}, -1, false
	}
	label := unescapeMCPMarkdown(string(data[start+1 : labelEnd]))
	if labelEnd+1 < len(data) && data[labelEnd+1] == '(' {
		end := findMCPMarkdownDestinationEnd(ctx, data, labelEnd+2)
		if end < 0 {
			return mcpMarkdownReference{}, skipMCPMarkdownLine(ctx, data, labelEnd+2), false
		}
		target, ok := parseMCPDestination(ctx, data[labelEnd+2:end])
		if !ok {
			return mcpMarkdownReference{}, -1, false
		}
		if target == "" {
			return mcpMarkdownReference{}, end + 1, false
		}
		return mcpMarkdownReference{Target: target, Label: label, Origin: "markdown_link", Start: start, End: end + 1}, end + 1, true
	}
	if labelEnd+1 < len(data) && data[labelEnd+1] == '[' {
		close := findUnescapedByte(ctx, data, labelEnd+2, ']', 257)
		if close < 0 {
			return mcpMarkdownReference{}, skipMCPMarkdownLine(ctx, data, labelEnd+2), false
		}
		keyBytes := data[labelEnd+2 : close]
		if len(keyBytes) > 256 {
			return mcpMarkdownReference{}, -1, false
		}
		key := string(keyBytes)
		if key == "" {
			key = string(data[start+1 : labelEnd])
		}
		target, ok := definitions[normalizeMCPReferenceLabel(key)]
		if !ok {
			return mcpMarkdownReference{}, start, false
		}
		return mcpMarkdownReference{Target: target, Label: label, Origin: "markdown_link", Start: start, End: close + 1}, close + 1, true
	}
	return mcpMarkdownReference{}, start, false
}

func findMCPMarkdownDestinationEnd(ctx context.Context, data []byte, start int) int {
	depth := 0
	for i := start; i < len(data); i++ {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return -1
		}
		if data[i] == '\\' {
			i++
			continue
		}
		switch data[i] {
		case '(':
			depth++
			if depth > 32 {
				return -1
			}
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		case '\n', '\r':
			return -1
		}
	}
	return -1
}

func parseMCPDestination(ctx context.Context, raw []byte) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", true
	}
	if raw[0] == '<' {
		if end := findByteContext(ctx, raw, 1, '>'); end >= 0 {
			raw = raw[1:end]
		} else {
			return "", true
		}
	}
	if len(raw) == 0 {
		return "", true
	}
	if raw[0] != '<' {
		for i, value := range raw {
			if i&0x3ff == 0 && ctx.Err() != nil {
				return "", false
			}
			if value == ' ' || value == '\t' {
				raw = raw[:i]
				break
			}
		}
	}
	if len(raw) > maxMCPReferenceTargetBytes {
		return "", false
	}
	return unescapeMCPMarkdown(string(raw)), true
}

func splitMCPWikiTarget(value string) (string, string) {
	target, label, found := strings.Cut(value, "|")
	target = strings.TrimSpace(target)
	if !found || strings.TrimSpace(label) == "" {
		label = target
	} else {
		label = strings.TrimSpace(label)
	}
	if before, _, hasAnchor := strings.Cut(target, "#"); hasAnchor {
		target = before
	}
	return target, label
}

func splitMCPWikiTargetBytes(value []byte) (string, string, bool) {
	target, label, hasLabel := value, []byte(nil), false
	if separator := bytes.IndexByte(value, '|'); separator >= 0 {
		target, label, hasLabel = value[:separator], value[separator+1:], true
	}
	target = bytes.TrimSpace(target)
	if anchor := bytes.IndexByte(target, '#'); anchor >= 0 {
		target = target[:anchor]
	}
	label = bytes.TrimSpace(label)
	if !hasLabel || len(label) == 0 {
		label = target
	}
	if len(target) > maxMCPReferenceTargetBytes || len(label) > maxMCPReferenceLabelBytes {
		return "", "", false
	}
	return string(target), string(label), true
}

func skipMCPMarkdownLine(ctx context.Context, data []byte, start int) int {
	if end := findByteContext(ctx, data, start, '\n'); end >= 0 {
		return end + 1
	}
	return len(data)
}

func findUnescapedByte(ctx context.Context, data []byte, start int, want byte, maxLookahead int) int {
	end := min(len(data), start+maxLookahead)
	for i := start; i < end; i++ {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return -1
		}
		if data[i] == '\\' {
			i++
			continue
		}
		if data[i] == want {
			return i
		}
		if data[i] == '\n' {
			return -1
		}
	}
	return -1
}

func findByteContext(ctx context.Context, data []byte, start int, value byte) int {
	for i := start; i < len(data); i++ {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return -1
		}
		if data[i] == value {
			return i
		}
	}
	return -1
}

func findTokenContext(ctx context.Context, data []byte, start int, token []byte) int {
	for i := start; i+len(token) <= len(data); i++ {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return -1
		}
		if bytes.Equal(data[i:i+len(token)], token) {
			return i
		}
	}
	return -1
}

func normalizeMCPReferenceLabel(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func unescapeMCPMarkdown(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	escaped := false
	for _, r := range value {
		if escaped {
			result.WriteRune(r)
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else {
			result.WriteRune(r)
		}
	}
	if escaped {
		result.WriteByte('\\')
	}
	return result.String()
}
func countByteRun(data []byte, start int, value byte) int {
	end := start
	for end < len(data) && data[end] == value {
		end++
	}
	return end - start
}
func asciiEqualFoldPrefix(data []byte, value string) bool {
	if len(data) < len(value) {
		return false
	}
	for i := range value {
		a, b := data[i], value[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func mcpReferenceRange(ref mcpMarkdownReference) (*int, *int) {
	start, end := ref.Start, ref.End
	return &start, &end
}

func validMCPReferenceText(value string) bool {
	return utf8.ValidString(value) && len(value) <= 4096
}
