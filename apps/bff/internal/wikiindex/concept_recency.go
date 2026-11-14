package wikiindex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	fm "github.com/adrg/frontmatter"
)

// ConceptRecencySnapshot is one complete candidate or committed Concept view.
// Pages are keyed by the exact stable ID in that snapshot's ID map. IDMapVerified
// is deliberately separate from an empty IDMap: a missing prior map is unknown.
type ConceptRecencySnapshot struct {
	IDMap         IDMap
	IDMapVerified bool
	ActiveJSONL   []byte
	DormantJSONL  []byte
	ActivePages   map[string][]byte
	DormantPages  map[string][]byte
}

// ConceptRecencyPageRef names a prior committed page that may witness a cache
// row. A missing page is represented by its absence from the supplied file
// map; it never changes identity binding.
type ConceptRecencyPageRef struct {
	ID      string
	Slug    string
	Path    string
	Dormant bool
}

type recencyCacheRow struct {
	fields  map[string]json.RawMessage
	slug    string
	id      string
	hash    string
	time    string
	hasHash bool
	hasTime bool
	dormant bool
}

type recencyPair struct {
	row     *recencyCacheRow
	page    []byte
	dormant bool
}

// conceptFingerprintSegment is the stable, renderer-visible representation of
// a Concept page body. Literal Markdown (including code) remains text; only a
// recognized prose Concept wikilink is represented by identity/route, anchor,
// and the label the frontend renders.
type conceptFingerprintSegment struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Identity  string `json:"identity,omitempty"`
	Route     string `json:"route,omitempty"`
	Anchor    string `json:"anchor,omitempty"`
	HasAnchor bool   `json:"has_anchor,omitempty"`
	Label     string `json:"label,omitempty"`
}

type conceptLinkTarget struct {
	Identity  string
	Route     string
	Anchor    string
	HasAnchor bool
}

func (target conceptLinkTarget) resolve(redirects map[string]string) string {
	path := target.Route
	if target.Identity != "" {
		path = target.Identity
		if resolved, ok := redirects[target.Identity]; ok {
			path = resolved
		}
	}
	if target.HasAnchor {
		path += "#" + target.Anchor
	}
	return path
}

var hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const DormantConceptsJSONLPath = "cache/dormant_concepts.jsonl"

// CaptureConceptRecencySnapshot reads the committed inputs needed by the
// recency planner before a rebuild overwrites any derived artifact.
func CaptureConceptRecencySnapshot(ctx context.Context, store Store) (ConceptRecencySnapshot, error) {
	snapshot := ConceptRecencySnapshot{ActivePages: map[string][]byte{}, DormantPages: map[string][]byte{}}
	mapData, err := readOptionalRecencyFile(ctx, store, IDMapPath)
	if err != nil {
		return snapshot, fmt.Errorf("read prior concept ID map: %w", err)
	}
	if len(mapData) > 0 {
		snapshot.IDMap, err = DecodeIDMap(mapData)
		if err != nil {
			return snapshot, fmt.Errorf("decode prior concept ID map: %w", err)
		}
		snapshot.IDMapVerified = true
	}
	snapshot.ActiveJSONL, err = readOptionalRecencyFile(ctx, store, ConceptsJSONLPath)
	if err != nil {
		return snapshot, fmt.Errorf("read prior active concept cache: %w", err)
	}
	snapshot.DormantJSONL, err = readOptionalRecencyFile(ctx, store, DormantConceptsJSONLPath)
	if err != nil {
		return snapshot, fmt.Errorf("read prior dormant concept cache: %w", err)
	}
	refs, err := ConceptRecencyPageRefs(snapshot.IDMap, snapshot.IDMapVerified, snapshot.ActiveJSONL, snapshot.DormantJSONL)
	if err != nil {
		return snapshot, fmt.Errorf("bind prior concept snapshot: %w", err)
	}
	for _, ref := range refs {
		page, readErr := readOptionalRecencyFile(ctx, store, ref.Path)
		if readErr != nil {
			return snapshot, fmt.Errorf("read prior concept page %q: %w", ref.ID, readErr)
		}
		if ref.Dormant {
			snapshot.DormantPages[ref.ID] = page
		} else {
			snapshot.ActivePages[ref.ID] = page
		}
	}
	return snapshot, nil
}

// ConceptRecencySnapshotFromFiles binds generated cache rows to the final
// candidate ID map and page bytes before the rebuild writes anything.
func ConceptRecencySnapshotFromFiles(ids IDMap, activeJSONL, dormantJSONL []byte, files []MarkdownFile, prior ConceptRecencySnapshot) (ConceptRecencySnapshot, error) {
	snapshot := ConceptRecencySnapshot{IDMap: ids, IDMapVerified: true, ActiveJSONL: activeJSONL, DormantJSONL: dormantJSONL, ActivePages: map[string][]byte{}, DormantPages: map[string][]byte{}}
	fileByPath := make(map[string][]byte, len(files))
	fileBySlug := make(map[string][]byte, len(files))
	for _, file := range files {
		fileByPath[file.Path] = file.Data
		fileBySlug[file.Slug] = file.Data
	}
	for id, slug := range ids.Concept {
		page, ok := fileByPath["wiki/"+slug+".md"]
		if !ok {
			page, ok = fileBySlug[slug]
		}
		if !ok {
			return snapshot, fmt.Errorf("candidate concept page %q is missing", id)
		}
		snapshot.ActivePages[id] = page
	}
	for id, slug := range ids.DormantConcept {
		if page, ok := prior.DormantPages[id]; ok {
			snapshot.DormantPages[id] = page
			continue
		}
		if page, ok := fileByPath["wiki/.dormant/"+slug+".md"]; ok {
			snapshot.DormantPages[id] = page
		}
	}
	return snapshot, nil
}

func readOptionalRecencyFile(ctx context.Context, store Store, path string) ([]byte, error) {
	data, err := store.ReadFile(ctx, path)
	if errors.Is(err, ErrNotFound) || errors.Is(err, storage.ErrObjectNotExist) {
		return nil, nil
	}
	return data, err
}

// ConceptRecencyPageRefs validates cache identity joins and returns the exact
// generation-owned Markdown paths that may be read for those rows. When the
// prior ID map is unavailable, only an exact stable ID embedded in each cache
// row is accepted; slug similarity is not identity evidence.
func ConceptRecencyPageRefs(ids IDMap, idMapVerified bool, activeJSONL, dormantJSONL []byte) ([]ConceptRecencyPageRef, error) {
	active, err := parseRecencyRows(activeJSONL, false)
	if err != nil {
		return nil, fmt.Errorf("decode active concept cache: %w", err)
	}
	dormant, err := parseRecencyRows(dormantJSONL, true)
	if err != nil {
		return nil, fmt.Errorf("decode dormant concept cache: %w", err)
	}
	var rows map[string]*recencyCacheRow
	if idMapVerified {
		rows, err = bindRecencyRows(ids, active, dormant)
		if err == nil {
			err = ensureRecencyRowsBound(active, dormant, rows)
		}
	} else {
		rows, err = bindCacheRowsByEmbeddedID(active, dormant)
	}
	if err != nil {
		return nil, err
	}
	refsByID := make(map[string]ConceptRecencyPageRef, len(rows))
	if idMapVerified {
		idBySlug := make(map[string]string, len(ids.Concept)+len(ids.DormantConcept))
		appendIdentity := func(id, slug string, dormant bool) error {
			if id == "" || strings.TrimSpace(slug) == "" || slug != strings.TrimSpace(slug) || strings.ContainsAny(slug, "/\\") || slug == "." || slug == ".." {
				return fmt.Errorf("unsafe prior concept mapping %q -> %q", id, slug)
			}
			if _, exists := refsByID[id]; exists {
				return fmt.Errorf("duplicate prior concept identity %q", id)
			}
			if otherID, exists := idBySlug[slug]; exists {
				return fmt.Errorf("duplicate prior concept slug %q for %q and %q", slug, otherID, id)
			}
			idBySlug[slug] = id
			path := "wiki/" + slug + ".md"
			if dormant {
				path = "wiki/.dormant/" + slug + ".md"
			}
			refsByID[id] = ConceptRecencyPageRef{ID: id, Slug: slug, Path: path, Dormant: dormant}
			return nil
		}
		for id, slug := range ids.Concept {
			if err := appendIdentity(id, slug, false); err != nil {
				return nil, err
			}
		}
		for id, slug := range ids.DormantConcept {
			if err := appendIdentity(id, slug, true); err != nil {
				return nil, err
			}
		}
	} else {
		for id, row := range rows {
			if strings.ContainsAny(row.slug, "/\\") || row.slug == "." || row.slug == ".." || strings.TrimSpace(row.slug) != row.slug {
				return nil, fmt.Errorf("unsafe prior concept slug %q", row.slug)
			}
			path := "wiki/" + row.slug + ".md"
			if row.dormant {
				path = "wiki/.dormant/" + row.slug + ".md"
			}
			refsByID[id] = ConceptRecencyPageRef{ID: id, Slug: row.slug, Path: path, Dormant: row.dormant}
		}
	}
	refs := make([]ConceptRecencyPageRef, 0, len(refsByID))
	for _, ref := range refsByID {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

// ConceptRecencySnapshotFromCommittedFiles binds digest-verified manifest
// artifacts without requiring legacy pages to exist. The caller is responsible
// for verifying each file against its manifest row before passing it here.
func ConceptRecencySnapshotFromCommittedFiles(idMapData []byte, idMapVerified bool, activeJSONL, dormantJSONL []byte, pagesByPath map[string][]byte) (ConceptRecencySnapshot, error) {
	snapshot := ConceptRecencySnapshot{ActiveJSONL: activeJSONL, DormantJSONL: dormantJSONL, IDMapVerified: idMapVerified, ActivePages: map[string][]byte{}, DormantPages: map[string][]byte{}}
	if idMapVerified {
		ids, err := DecodeIDMap(idMapData)
		if err != nil {
			return snapshot, fmt.Errorf("decode committed concept ID map: %w", err)
		}
		snapshot.IDMap = ids
	}
	refs, err := ConceptRecencyPageRefs(snapshot.IDMap, idMapVerified, activeJSONL, dormantJSONL)
	if err != nil {
		return snapshot, fmt.Errorf("bind committed concept cache rows: %w", err)
	}
	for _, ref := range refs {
		page, ok := pagesByPath[ref.Path]
		if !ok {
			continue
		}
		if ref.Dormant {
			snapshot.DormantPages[ref.ID] = append([]byte(nil), page...)
		} else {
			snapshot.ActivePages[ref.ID] = append([]byte(nil), page...)
		}
	}
	return snapshot, nil
}

// EnrichConceptRecency returns both candidate cache files without writing. The
// caller must complete this function before applying either cache replacement.
func EnrichConceptRecency(current, prior ConceptRecencySnapshot, runAt time.Time) ([]byte, []byte, error) {
	if !current.IDMapVerified {
		return nil, nil, errors.New("current concept ID map is not verified")
	}
	if err := validateRecencyRedirects(current.IDMap); err != nil {
		return nil, nil, fmt.Errorf("current concept ID map: %w", err)
	}
	if err := validateRecencyRedirects(prior.IDMap); prior.IDMapVerified && err != nil {
		return nil, nil, fmt.Errorf("prior concept ID map: %w", err)
	}
	currentRows, err := parseRecencyRows(current.ActiveJSONL, false)
	if err != nil {
		return nil, nil, fmt.Errorf("decode active concept cache: %w", err)
	}
	currentDormant, err := parseRecencyRows(current.DormantJSONL, true)
	if err != nil {
		return nil, nil, fmt.Errorf("decode dormant concept cache: %w", err)
	}
	priorRows, err := parseRecencyRows(prior.ActiveJSONL, false)
	if err != nil {
		return nil, nil, fmt.Errorf("decode prior active concept cache: %w", err)
	}
	priorDormant, err := parseRecencyRows(prior.DormantJSONL, true)
	if err != nil {
		return nil, nil, fmt.Errorf("decode prior dormant concept cache: %w", err)
	}
	currentByID, err := bindRecencyRows(current.IDMap, currentRows, currentDormant)
	if err != nil {
		return nil, nil, fmt.Errorf("bind current concept cache: %w", err)
	}
	priorByID, err := bindRecencyRows(prior.IDMap, priorRows, priorDormant)
	if err != nil {
		return nil, nil, fmt.Errorf("bind prior concept cache: %w", err)
	}
	// A missing map can still leave an independently readable row keyed by its
	// frontmatter ID. Exact identity is usable; slug similarity is never used.
	if !prior.IDMapVerified {
		priorByID, err = bindCacheRowsByEmbeddedID(priorRows, priorDormant)
		if err != nil {
			return nil, nil, fmt.Errorf("bind prior cache rows without ID map: %w", err)
		}
	}
	if err := ensureRecencyRowsBound(currentRows, currentDormant, currentByID); err != nil {
		return nil, nil, fmt.Errorf("bind current concept cache: %w", err)
	}
	if prior.IDMapVerified {
		if err := ensureRecencyRowsBound(priorRows, priorDormant, priorByID); err != nil {
			return nil, nil, fmt.Errorf("bind prior concept cache: %w", err)
		}
		if err := addPriorIdentityRows(prior.IDMap, priorByID); err != nil {
			return nil, nil, fmt.Errorf("bind prior concept identities: %w", err)
		}
	} else if err := ensureUnknownPriorRowsJoined(priorByID, currentByID, current.IDMap.IDRedirects); err != nil {
		return nil, nil, err
	}
	priorPairs := make(map[string]recencyPair, len(priorByID))
	for id, row := range priorByID {
		page := recencyPage(prior, id, row.dormant)
		priorPairs[id] = recencyPair{row: row, page: page, dormant: row.dormant}
	}
	currentPairs := make(map[string]recencyPair, len(currentByID))
	currentHashes := make(map[string]string, len(currentByID))
	for id, row := range currentByID {
		page := recencyPage(current, id, row.dormant)
		if len(page) == 0 {
			return nil, nil, fmt.Errorf("current concept %q has no verified page", id)
		}
		hash, err := fingerprintConcept(page, current.IDMap.IDRedirects, row.slug)
		if err != nil {
			return nil, nil, fmt.Errorf("fingerprint current concept %q: %w", id, err)
		}
		currentHashes[id] = hash
		currentPairs[id] = recencyPair{row: row, page: page, dormant: row.dormant}
	}
	priorHashesOld := make(map[string]string, len(priorPairs))
	priorHashesCurrent := make(map[string]string, len(priorPairs))
	for id, priorPair := range priorPairs {
		if len(priorPair.page) == 0 {
			continue
		}
		if prior.IDMapVerified {
			hash, err := fingerprintConcept(priorPair.page, prior.IDMap.IDRedirects, priorPair.row.slug)
			if err != nil {
				return nil, nil, fmt.Errorf("fingerprint prior concept %q: %w", id, err)
			}
			priorHashesOld[id] = hash
		}
		hash, err := fingerprintConcept(priorPair.page, current.IDMap.IDRedirects, priorPair.row.slug)
		if err != nil {
			return nil, nil, fmt.Errorf("fingerprint prior concept %q on current basis: %w", id, err)
		}
		priorHashesCurrent[id] = hash
	}
	redirectSourceByTarget := make(map[string][]string)
	for oldID, targetID := range current.IDMap.IDRedirects {
		redirectSourceByTarget[targetID] = append(redirectSourceByTarget[targetID], oldID)
	}
	for _, ids := range redirectSourceByTarget {
		sort.Strings(ids)
	}
	runAt = runAt.UTC()
	for currentID, candidate := range currentPairs {
		candidateHash := currentHashes[currentID]
		candidate.row.clearRecency()
		witnesses := make([]recencyPair, 0, 2)
		if old := priorPairs[currentID]; old.row != nil {
			witnesses = append(witnesses, old)
		}
		for _, oldID := range redirectSourceByTarget[currentID] {
			if old := priorPairs[oldID]; old.row != nil && oldID != currentID {
				witnesses = append(witnesses, old)
			}
		}
		if len(witnesses) == 0 {
			candidate.row.setHash(candidateHash)
			candidate.row.setTime(runAt.Format(time.RFC3339Nano))
			continue
		}
		sort.Slice(witnesses, func(i, j int) bool { return witnesses[i].row.id < witnesses[j].row.id })
		previousFingerprints := make([]string, 0, len(witnesses))
		knownTimes := make(map[string]bool)
		unknownTime := false
		changed := false
		unavailableWitness := false
		for _, old := range witnesses {
			oldHash := ""
			if old.row.hasHash {
				oldHash = old.row.hash
			}
			if old.row.hasTime && !validRecencyTime(old.row.time) {
				return nil, nil, fmt.Errorf("prior concept %q has invalid content_updated_at", old.row.id)
			}
			if oldHash != "" && !hexDigestPattern.MatchString(oldHash) {
				return nil, nil, fmt.Errorf("prior concept %q has invalid content_sha256", old.row.id)
			}
			if oldHash == "" {
				// Legacy rows have no trusted baseline hash. A verified prior page
				// can seed one, but its old timestamp is not trusted as a baseline.
				unknownTime = true
				if len(old.page) == 0 || !prior.IDMapVerified {
					unavailableWitness = true
					continue
				}
				previousFingerprints = append(previousFingerprints, priorHashesCurrent[old.row.id])
				if priorHashesCurrent[old.row.id] != candidateHash {
					changed = true
				}
				continue
			}
			if old.row.hasTime {
				knownTimes[old.row.time] = true
			} else {
				unknownTime = true
			}
			if oldHash == candidateHash {
				// A valid H1==H2 is sufficient without basis inference.
				previousFingerprints = append(previousFingerprints, candidateHash)
				continue
			}
			if !prior.IDMapVerified {
				return nil, nil, fmt.Errorf("concept %q recency basis UNKNOWN: prior ID map is missing while content hash changed", currentID)
			}
			if !equalStringMap(current.IDMap.IDRedirects, prior.IDMap.IDRedirects) {
				if len(old.page) == 0 {
					return nil, nil, fmt.Errorf("concept %q recency basis UNKNOWN: prior page is missing and redirect maps differ", currentID)
				}
				if priorHashesOld[old.row.id] != oldHash {
					return nil, nil, fmt.Errorf("concept %q recency basis UNKNOWN: prior page does not replay content_sha256 (stored %s, replay %s)", currentID, oldHash, priorHashesOld[old.row.id])
				}
				basisChanged, err := recencyBasisChanged(old.page, candidate.page, prior.IDMap.IDRedirects, current.IDMap.IDRedirects)
				if err != nil {
					return nil, nil, fmt.Errorf("concept %q recency basis UNKNOWN: %w", currentID, err)
				}
				if basisChanged {
					previousFingerprints = append(previousFingerprints, priorHashesCurrent[old.row.id])
				} else {
					previousFingerprints = append(previousFingerprints, oldHash)
				}
			} else {
				// Verified equal redirect projections establish SAME directly.
				// A page may already contain the user's new H2 bytes before the
				// rebuild reads it, so requiring it to replay committed H1 would
				// reject ordinary edits. The stored H1 remains the baseline.
				previousFingerprints = append(previousFingerprints, oldHash)
			}
			if previousFingerprints[len(previousFingerprints)-1] != candidateHash {
				changed = true
			}
		}
		if unavailableWitness && len(witnesses) > 1 {
			return nil, nil, fmt.Errorf("concept %q has an unavailable convergent prior witness", currentID)
		}
		if len(previousFingerprints) > 1 {
			first := previousFingerprints[0]
			for _, fingerprint := range previousFingerprints[1:] {
				if fingerprint != first {
					return nil, nil, fmt.Errorf("concept %q has conflicting convergent prior fingerprints", currentID)
				}
			}
		}
		if len(knownTimes) > 1 {
			unknownTime = true
		}
		candidate.row.setHash(candidateHash)
		if changed {
			candidate.row.setTime(runAt.Format(time.RFC3339Nano))
		} else if !unknownTime && len(knownTimes) == 1 {
			for value := range knownTimes {
				candidate.row.setTime(value)
			}
		}
	}
	active, err := encodeRecencyRows(currentRows)
	if err != nil {
		return nil, nil, err
	}
	dormant, err := encodeRecencyRows(currentDormant)
	if err != nil {
		return nil, nil, err
	}
	return active, dormant, nil
}

func (r *recencyCacheRow) setHash(value string) {
	r.fields["content_sha256"], _ = json.Marshal(value)
	r.hash, r.hasHash = value, true
}

func (r *recencyCacheRow) setTime(value string) {
	r.fields["content_updated_at"], _ = json.Marshal(value)
	r.time, r.hasTime = value, true
}

func (r *recencyCacheRow) clearRecency() {
	delete(r.fields, "content_sha256")
	delete(r.fields, "content_updated_at")
	r.hash, r.time = "", ""
	r.hasHash, r.hasTime = false, false
}

func parseRecencyRows(data []byte, dormant bool) ([]*recencyCacheRow, error) {
	rows := []*recencyCacheRow{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	seenSlug := map[string]bool{}
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := rejectDuplicateJSONMembers(line); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
			if err == nil {
				err = errors.New("expected object")
			}
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		var row recencyCacheRow
		row.fields, row.dormant = make(map[string]json.RawMessage, len(fields)+2), dormant
		for key, value := range fields {
			row.fields[key] = value
		}
		if err := json.Unmarshal(fields["slug"], &row.slug); err != nil || strings.TrimSpace(row.slug) == "" {
			return nil, fmt.Errorf("line %d has invalid slug", lineNo)
		}
		if seenSlug[row.slug] {
			return nil, fmt.Errorf("duplicate concept cache slug %q", row.slug)
		}
		seenSlug[row.slug] = true
		if raw, ok := fields["frontmatter"]; ok {
			var matter map[string]json.RawMessage
			if err := json.Unmarshal(raw, &matter); err != nil || matter == nil {
				return nil, fmt.Errorf("line %d has invalid frontmatter object", lineNo)
			}
			if idRaw, ok := matter["id"]; ok {
				_ = json.Unmarshal(idRaw, &row.id)
			}
		}
		if raw, ok := fields["id"]; ok {
			var topLevelID string
			if err := json.Unmarshal(raw, &topLevelID); err == nil && topLevelID != "" {
				if row.id != "" && row.id != topLevelID {
					return nil, fmt.Errorf("line %d has conflicting embedded concept IDs", lineNo)
				}
				row.id = topLevelID
			}
		}
		if raw, ok := fields["content_sha256"]; ok {
			row.hasHash = true
			if err := json.Unmarshal(raw, &row.hash); err != nil || !hexDigestPattern.MatchString(row.hash) {
				return nil, fmt.Errorf("line %d has invalid content_sha256", lineNo)
			}
		}
		if raw, ok := fields["content_updated_at"]; ok {
			row.hasTime = true
			if err := json.Unmarshal(raw, &row.time); err != nil || !validRecencyTime(row.time) {
				return nil, fmt.Errorf("line %d has invalid content_updated_at", lineNo)
			}
		}
		rows = append(rows, &row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func bindRecencyRows(ids IDMap, active, dormant []*recencyCacheRow) (map[string]*recencyCacheRow, error) {
	out := make(map[string]*recencyCacheRow, len(active)+len(dormant))
	bind := func(entries map[string]string, rows []*recencyCacheRow) error {
		bySlug := make(map[string]*recencyCacheRow, len(rows))
		for _, row := range rows {
			bySlug[row.slug] = row
		}
		for id, slug := range entries {
			row := bySlug[slug]
			if row == nil {
				continue
			}
			if row.id != "" && row.id != id {
				return fmt.Errorf("cache row %q identity %q does not match ID map %q", slug, row.id, id)
			}
			row.id = id
			if _, duplicate := out[id]; duplicate {
				return fmt.Errorf("duplicate concept identity %q", id)
			}
			out[id] = row
		}
		return nil
	}
	if err := bind(ids.Concept, active); err != nil {
		return nil, err
	}
	if err := bind(ids.DormantConcept, dormant); err != nil {
		return nil, err
	}
	return out, nil
}

func bindCacheRowsByEmbeddedID(active, dormant []*recencyCacheRow) (map[string]*recencyCacheRow, error) {
	out := make(map[string]*recencyCacheRow)
	for _, row := range append(append([]*recencyCacheRow(nil), active...), dormant...) {
		if row.id == "" {
			return nil, fmt.Errorf("prior cache row %q has no exact stable identity", row.slug)
		}
		if previous := out[row.id]; previous != nil {
			return nil, fmt.Errorf("duplicate prior cache identity %q", row.id)
		}
		out[row.id] = row
	}
	return out, nil
}

// addPriorIdentityRows retains a verified legacy page as a baseline even when
// its cache row is absent. A mapped identity is existing history; absence of a
// cache row alone must not make it look like a newly created Concept.
func addPriorIdentityRows(ids IDMap, rows map[string]*recencyCacheRow) error {
	add := func(id, slug string, dormant bool) error {
		if id == "" || strings.TrimSpace(slug) == "" || slug != strings.TrimSpace(slug) || strings.ContainsAny(slug, "/\\") || slug == "." || slug == ".." {
			return fmt.Errorf("unsafe prior concept mapping %q -> %q", id, slug)
		}
		if old := rows[id]; old != nil {
			if old.slug != slug || old.dormant != dormant {
				return fmt.Errorf("prior concept %q cache row does not match its identity map", id)
			}
			return nil
		}
		rows[id] = &recencyCacheRow{fields: map[string]json.RawMessage{}, slug: slug, id: id, dormant: dormant}
		return nil
	}
	for id, slug := range ids.Concept {
		if err := add(id, slug, false); err != nil {
			return err
		}
	}
	for id, slug := range ids.DormantConcept {
		if err := add(id, slug, true); err != nil {
			return err
		}
	}
	return nil
}

// ensureUnknownPriorRowsJoined refuses to call unmatched legacy rows new when
// the prior identity map is absent. Exact stable IDs and the verified current
// direct redirect projection are the only permitted joins; slug is never used
// as a fallback identity.
func ensureUnknownPriorRowsJoined(prior, current map[string]*recencyCacheRow, redirects map[string]string) error {
	if len(prior) == 0 {
		return nil
	}
	joined := make(map[string]bool, len(prior))
	for id := range current {
		if prior[id] != nil {
			joined[id] = true
		}
	}
	for oldID, currentID := range redirects {
		if current[currentID] != nil && prior[oldID] != nil {
			joined[oldID] = true
		}
	}
	for id := range prior {
		if !joined[id] {
			return fmt.Errorf("concept recency basis UNKNOWN: prior ID map is missing and cache row %q cannot be joined to current identities", id)
		}
	}
	return nil
}

func ensureRecencyRowsBound(active, dormant []*recencyCacheRow, bound map[string]*recencyCacheRow) error {
	covered := make(map[*recencyCacheRow]bool, len(bound))
	for _, row := range bound {
		covered[row] = true
	}
	for _, row := range append(append([]*recencyCacheRow(nil), active...), dormant...) {
		if !covered[row] {
			return fmt.Errorf("cache row %q is not declared by the identity map", row.slug)
		}
	}
	return nil
}

func recencyPage(snapshot ConceptRecencySnapshot, id string, dormant bool) []byte {
	if dormant {
		return snapshot.DormantPages[id]
	}
	return snapshot.ActivePages[id]
}

func validateRecencyRedirects(ids IDMap) error {
	for from, to := range ids.IDRedirects {
		if !ValidLegacyConceptID(from) || !ValidSyntoEntityID(to) {
			return fmt.Errorf("invalid direct ID redirect %q -> %q", from, to)
		}
		if _, chained := ids.IDRedirects[to]; chained {
			return fmt.Errorf("ID redirect chain at %q", from)
		}
		if _, active := ids.Concept[to]; !active {
			return fmt.Errorf("ID redirect target %q is not active", to)
		}
	}
	return nil
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func recencyBasisChanged(priorPage, currentPage []byte, priorRedirects, currentRedirects map[string]string) (bool, error) {
	priorTargets, err := conceptTargets(priorPage)
	if err != nil {
		return false, err
	}
	currentTargets, err := conceptTargets(currentPage)
	if err != nil {
		return false, err
	}
	set := map[conceptLinkTarget]bool{}
	for _, target := range priorTargets {
		set[target] = true
	}
	for _, target := range currentTargets {
		set[target] = true
	}
	for target := range set {
		if target.resolve(priorRedirects) != target.resolve(currentRedirects) {
			return true, nil
		}
	}
	return false, nil
}

func conceptTargets(page []byte) ([]conceptLinkTarget, error) {
	_, body, err := parseRecencyPage(page)
	if err != nil {
		return nil, err
	}
	_, targets, err := scanConceptBody(body, nil)
	return targets, err
}

func fingerprintBody(body string, redirects map[string]string) ([]conceptFingerprintSegment, error) {
	segments, _, err := scanConceptBody(body, redirects)
	return segments, err
}

func appendFingerprintText(segments *[]conceptFingerprintSegment, value string) {
	if value == "" {
		return
	}
	if length := len(*segments); length > 0 && (*segments)[length-1].Type == "text" {
		(*segments)[length-1].Text += value
		return
	}
	*segments = append(*segments, conceptFingerprintSegment{Type: "text", Text: value})
}

func scanConceptBody(body string, redirects map[string]string) ([]conceptFingerprintSegment, []conceptLinkTarget, error) {
	segments := []conceptFingerprintSegment{}
	var targets []conceptLinkTarget
	section := "concepts"
	fenceChar, fenceSize := byte(0), 0
	lines := strings.Split(body, "\n")
	for lineNo := 0; lineNo < len(lines); {
		line := lines[lineNo]
		trimmed := strings.TrimLeft(line, " ")
		if fenceChar != 0 {
			appendFingerprintText(&segments, line)
			if count, ch, rest, ok := markdownFenceLine(trimmed); ok && rest == "" && ch == fenceChar && count >= fenceSize {
				fenceChar, fenceSize = 0, 0
			}
			if lineNo < len(lines)-1 {
				appendFingerprintText(&segments, "\n")
			}
			lineNo++
			continue
		}
		if count, ch, _, opening := markdownFenceLine(trimmed); opening {
			appendFingerprintText(&segments, line)
			fenceChar, fenceSize = ch, count
			if lineNo < len(lines)-1 {
				appendFingerprintText(&segments, "\n")
			}
			lineNo++
			continue
		}
		if strings.HasPrefix(line, "## ") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			if strings.HasPrefix(heading, "sources") {
				section = "sources"
			}
			if strings.HasPrefix(heading, "concepts") {
				section = "concepts"
			}
		}
		endLine := lineNo
		for endLine+1 < len(lines) {
			next := lines[endLine+1]
			if next == "" || isMarkdownHeadingLine(next) {
				break
			}
			if _, _, _, opensFence := markdownFenceLine(strings.TrimLeft(next, " ")); opensFence {
				break
			}
			endLine++
		}
		paragraph := strings.Join(lines[lineNo:endLine+1], "\n")
		if section == "sources" {
			appendFingerprintText(&segments, paragraph)
		} else {
			paragraphSegments, paragraphTargets := scanConceptParagraph(paragraph, section, redirects)
			appendFingerprintSegments(&segments, paragraphSegments)
			targets = append(targets, paragraphTargets...)
		}
		if endLine < len(lines)-1 {
			appendFingerprintText(&segments, "\n")
		}
		lineNo = endLine + 1
	}
	return segments, targets, nil
}

func appendFingerprintSegments(target *[]conceptFingerprintSegment, segments []conceptFingerprintSegment) {
	for _, segment := range segments {
		if segment.Type == "text" {
			appendFingerprintText(target, segment.Text)
		} else {
			*target = append(*target, segment)
		}
	}
}

func scanConceptParagraph(paragraph, section string, redirects map[string]string) ([]conceptFingerprintSegment, []conceptLinkTarget) {
	segments := []conceptFingerprintSegment{}
	var targets []conceptLinkTarget
	inlineCodeEnds := inlineCodeSpanEnds(paragraph)
	textStart := 0
	for i := 0; i < len(paragraph); {
		if paragraph[i] == '`' {
			runEnd := backtickRunEnd(paragraph, i)
			if closeEnd, found := inlineCodeEnds[i]; found {
				appendFingerprintText(&segments, paragraph[textStart:i])
				appendFingerprintText(&segments, paragraph[i:closeEnd])
				i = closeEnd
				textStart = closeEnd
				continue
			}
			i = runEnd
			continue
		}
		if i+1 < len(paragraph) && paragraph[i] == '[' && paragraph[i+1] == '[' {
			lineEnd := strings.IndexByte(paragraph[i:], '\n')
			if lineEnd < 0 {
				lineEnd = len(paragraph) - i
			}
			linkEnd := i + lineEnd
			if relativeEnd := strings.Index(paragraph[i+2:linkEnd], "]]"); relativeEnd >= 0 {
				end := i + 2 + relativeEnd
				inner := paragraph[i+2 : end]
				segment, target, ok := fingerprintLinkSegment(inner, section, redirects)
				if ok {
					appendFingerprintText(&segments, paragraph[textStart:i])
					segments = append(segments, segment)
					targets = append(targets, target)
					i = end + 2
					textStart = i
					continue
				}
				i = end + 2
				continue
			}
		}
		i++
	}
	appendFingerprintText(&segments, paragraph[textStart:])
	return segments, targets
}

func backtickRunEnd(text string, start int) int {
	end := start + 1
	for end < len(text) && text[end] == '`' {
		end++
	}
	return end
}

func isEscapedMarkdownDelimiter(text string, start int) bool {
	backslashes := 0
	for index := start - 1; index >= 0 && text[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func inlineCodeSpanEnds(text string) map[int]int {
	ends := map[int]int{}
	type delimiterRun struct {
		start      int
		end        int
		width      int
		escaped    bool
		nextOfSize int
	}
	var runs []delimiterRun
	for index := 0; index < len(text); {
		if text[index] != '`' {
			index++
			continue
		}
		end := backtickRunEnd(text, index)
		runs = append(runs, delimiterRun{
			start: index, end: end, width: end - index,
			escaped: isEscapedMarkdownDelimiter(text, index), nextOfSize: -1,
		})
		index = end
	}
	nextByWidth := map[int]int{}
	for index := len(runs) - 1; index >= 0; index-- {
		if next, ok := nextByWidth[runs[index].width]; ok {
			runs[index].nextOfSize = next
		}
		nextByWidth[runs[index].width] = index
	}
	for index := 0; index < len(runs); index++ {
		open := runs[index]
		if open.escaped || open.nextOfSize < 0 {
			continue
		}
		close := runs[open.nextOfSize]
		ends[open.start] = close.end
		index = open.nextOfSize
	}
	return ends
}

func isMarkdownHeadingLine(line string) bool {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return false
	}
	line = line[indent:]
	width := 0
	for width < len(line) && line[width] == '#' {
		width++
	}
	return width > 0 && width <= 6 && width < len(line) && (line[width] == ' ' || line[width] == '\t')
}

func fingerprintLinkSegment(inner, section string, redirects map[string]string) (conceptFingerprintSegment, conceptLinkTarget, bool) {
	targetText, alias, hasAlias := strings.Cut(inner, "|")
	rawTarget := strings.TrimSpace(targetText)
	if rawTarget == "" {
		return conceptFingerprintSegment{}, conceptLinkTarget{}, false
	}
	routeSection, routeTarget := section, rawTarget
	if collection, rest, ok := strings.Cut(rawTarget, "/"); ok {
		switch collection {
		case "concepts":
			routeSection, routeTarget = "concepts", rest
		case "sources":
			routeSection, routeTarget = "sources", rest
		}
	}
	if routeSection != "concepts" || routeTarget == "" || strings.Contains(routeTarget, "/") {
		return conceptFingerprintSegment{}, conceptLinkTarget{}, false
	}
	path, anchor, hasAnchor := strings.Cut(routeTarget, "#")
	if path == "" {
		return conceptFingerprintSegment{}, conceptLinkTarget{}, false
	}
	label := frontendVisibleLinkLabel(rawTarget)
	if hasAlias {
		label = strings.TrimSpace(alias)
	}
	target := conceptLinkTarget{Anchor: anchor, HasAnchor: hasAnchor}
	identity, _, hasIdentity := splitCanonicalIDSlug(path)
	segment := conceptFingerprintSegment{Type: "concept_link", Anchor: anchor, HasAnchor: hasAnchor, Label: label}
	if hasIdentity {
		if resolved, ok := redirects[identity]; ok {
			identity = resolved
		}
		target.Identity = identity
		segment.Identity = identity
	} else {
		route := rawTarget
		if hasAlias {
			route, _, _ = strings.Cut(rawTarget, "|")
		}
		if _, _, explicitAnchor := strings.Cut(routeTarget, "#"); explicitAnchor {
			route = strings.TrimSuffix(route, "#"+anchor)
		}
		target.Route = route
		segment.Route = route
	}
	return segment, target, true
}

func frontendVisibleLinkLabel(target string) string {
	path := strings.TrimSpace(target)
	if collection, rest, ok := strings.Cut(path, "/"); ok && (collection == "concepts" || collection == "sources") {
		path = rest
	}
	path, _, _ = strings.Cut(path, "#")
	if _, slug, ok := splitCanonicalIDSlug(path); ok {
		return slug
	}
	return path
}

func splitCanonicalIDSlug(path string) (id, slug string, ok bool) {
	prefix, suffix, found := strings.Cut(path, "-")
	if !found || suffix == "" {
		return "", "", false
	}
	if ValidLegacyConceptID(prefix) || ValidSyntoEntityID(prefix) {
		return prefix, suffix, true
	}
	return "", "", false
}

func mustFingerprint(page []byte, redirects map[string]string, fallbackSlug string) string {
	value, err := fingerprintConcept(page, redirects, fallbackSlug)
	if err != nil {
		panic(err)
	}
	return value
}

func fingerprintConcept(page []byte, redirects map[string]string, fallbackSlug string) (string, error) {
	matter, body, err := parseRecencyPage(page)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(stringValue(matter["title"]))
	if title == "" {
		title = fallbackSlug
	}
	description := ""
	for _, key := range []string{"description", "summary", "excerpt"} {
		if value := strings.TrimSpace(stringValue(matter[key])); value != "" {
			description = value
			break
		}
	}
	if description == "" {
		for _, key := range []string{"description", "summary"} {
			if value := strings.TrimSpace(stringValue(matter[key])); value != "" {
				description = value
				break
			}
		}
	}
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	body = strings.TrimRight(body, "\n")
	canonicalBody, err := fingerprintBody(body, redirects)
	if err != nil {
		return "", err
	}
	projection := struct {
		Title       string                      `json:"title"`
		Description string                      `json:"description"`
		Body        []conceptFingerprintSegment `json:"body"`
	}{title, description, canonicalBody}
	data, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func parseRecencyPage(page []byte) (map[string]interface{}, string, error) {
	matter := map[string]interface{}{}
	raw := string(page)
	if strings.HasPrefix(raw, "---\n") || strings.HasPrefix(raw, "---\r\n") {
		body, err := fm.MustParse(strings.NewReader(raw), &matter)
		if err != nil {
			return nil, "", fmt.Errorf("parse concept frontmatter: %w", err)
		}
		return matter, string(body), nil
	}
	return matter, raw, nil
}

func stringValue(value interface{}) string { text, _ := value.(string); return text }

func markdownFenceLine(line string) (int, byte, string, bool) {
	if strings.HasPrefix(line, ">") {
		line = strings.TrimLeft(strings.TrimLeft(line, ">"), " ")
	}
	if len(line) == 0 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, "", false
	}
	ch, n := line[0], 0
	for n < len(line) && line[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	remainder := strings.TrimSpace(line[n:])
	return n, ch, remainder, true
}

func validRecencyTime(value string) bool {
	t, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && t.Location() == time.UTC && t.Format(time.RFC3339Nano) == value
}

func encodeRecencyRows(rows []*recencyCacheRow) ([]byte, error) {
	var out bytes.Buffer
	for _, row := range rows {
		data, err := json.Marshal(row.fields)
		if err != nil {
			return nil, err
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func rejectDuplicateJSONMembers(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := consumeJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = true
			if err := consumeJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			if err != nil {
				return err
			}
			return errors.New("unterminated JSON object")
		}
	case '[':
		for dec.More() {
			if err := consumeJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			if err != nil {
				return err
			}
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
