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
	active, err := parseRecencyRows(snapshot.ActiveJSONL, false)
	if err != nil {
		return snapshot, fmt.Errorf("decode prior active concept cache: %w", err)
	}
	dormant, err := parseRecencyRows(snapshot.DormantJSONL, true)
	if err != nil {
		return snapshot, fmt.Errorf("decode prior dormant concept cache: %w", err)
	}
	rows := map[string]*recencyCacheRow{}
	if snapshot.IDMapVerified {
		rows, err = bindRecencyRows(snapshot.IDMap, active, dormant)
	} else {
		rows, err = bindCacheRowsByEmbeddedID(active, dormant)
	}
	if err != nil {
		return snapshot, fmt.Errorf("bind prior cache rows: %w", err)
	}
	for id, row := range rows {
		if strings.Contains(row.slug, "/") || row.slug == "." || row.slug == ".." || strings.TrimSpace(row.slug) != row.slug {
			return snapshot, fmt.Errorf("unsafe prior concept slug %q", row.slug)
		}
		path := "wiki/" + row.slug + ".md"
		if row.dormant {
			path = "wiki/.dormant/" + row.slug + ".md"
		}
		page, readErr := readOptionalRecencyFile(ctx, store, path)
		if readErr != nil {
			return snapshot, fmt.Errorf("read prior concept page %q: %w", id, readErr)
		}
		if row.dormant {
			snapshot.DormantPages[id] = page
		} else {
			snapshot.ActivePages[id] = page
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
	refs := make([]ConceptRecencyPageRef, 0, len(rows))
	for id, row := range rows {
		if strings.Contains(row.slug, "/") || row.slug == "." || row.slug == ".." || strings.TrimSpace(row.slug) != row.slug {
			return nil, fmt.Errorf("unsafe prior concept slug %q", row.slug)
		}
		path := "wiki/" + row.slug + ".md"
		if row.dormant {
			path = "wiki/.dormant/" + row.slug + ".md"
		}
		refs = append(refs, ConceptRecencyPageRef{ID: id, Slug: row.slug, Path: path, Dormant: row.dormant})
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
				if len(old.page) > 0 && priorHashesOld[old.row.id] != oldHash {
					return nil, nil, fmt.Errorf("concept %q recency basis UNKNOWN: prior page does not replay content_sha256 (stored %s, replay %s)", currentID, oldHash, priorHashesOld[old.row.id])
				}
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
	set := map[string]bool{}
	for _, target := range priorTargets {
		set[target] = true
	}
	for _, target := range currentTargets {
		set[target] = true
	}
	for target := range set {
		if resolveConceptTarget(target, priorRedirects) != resolveConceptTarget(target, currentRedirects) {
			return true, nil
		}
	}
	return false, nil
}

func conceptTargets(page []byte) ([]string, error) {
	_, body, err := parseRecencyPage(page)
	if err != nil {
		return nil, err
	}
	var targets []string
	section := "concepts"
	fenceChar, fenceSize := byte(0), 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if fenceChar != 0 {
			if count, ch, rest, ok := markdownFenceLine(trimmed); ok && rest == "" && ch == fenceChar && count >= fenceSize {
				fenceChar, fenceSize = 0, 0
			}
			continue
		}
		if count, ch, _, opening := markdownFenceLine(trimmed); opening {
			fenceChar, fenceSize = ch, count
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
		if section == "sources" {
			continue
		}
		targets = append(targets, findConceptWikilinks([]byte(line))...)
	}
	return targets, nil
}

func findConceptWikilinks(line []byte) []string {
	var targets []string
	for i := 0; i < len(line); {
		if line[i] == '`' {
			n := 1
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			j := i + n
			for j < len(line) {
				if line[j] != '`' {
					j++
					continue
				}
				m := 1
				for j+m < len(line) && line[j+m] == '`' {
					m++
				}
				if m == n {
					j += m
					break
				}
				j += m
			}
			if j > len(line) {
				j = len(line)
			}
			i = j
			continue
		}
		if i+1 < len(line) && line[i] == '[' && line[i+1] == '[' {
			end := bytes.Index(line[i+2:], []byte("]]"))
			if end >= 0 {
				inner := strings.TrimSpace(string(line[i+2 : i+2+end]))
				if target, ok := parseConceptTarget(inner); ok {
					targets = append(targets, target)
				}
				i += end + 4
				continue
			}
		}
		i++
	}
	return targets
}

func parseConceptTarget(inner string) (string, bool) {
	target, alias, hasAlias := strings.Cut(inner, "|")
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "sources/") {
		return "", false
	}
	if strings.HasPrefix(target, "concepts/") {
		target = strings.TrimPrefix(target, "concepts/")
	}
	if strings.Contains(target, "/") || target == "" {
		return "", false
	}
	path, anchor, hasAnchor := strings.Cut(target, "#")
	identity := path
	label := path
	if id, suffix, ok := splitCanonicalIDSlug(path); ok {
		identity, label = id, suffix
	}
	if hasAlias {
		label = strings.TrimSpace(alias)
	}
	if hasAnchor {
		target = identity + "#" + anchor
	} else {
		target = identity
	}
	// Keep renderer-visible label and anchor separate from technical identity.
	return target + "\x00" + label, true
}

func resolveConceptTarget(value string, redirects map[string]string) string {
	identity, visible, _ := strings.Cut(value, "\x00")
	path, anchor, hasAnchor := strings.Cut(identity, "#")
	if redirected, ok := redirects[path]; ok {
		path = redirected
	}
	if hasAnchor {
		path += "#" + anchor
	}
	return path + "\x00" + visible
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
	body, err = normalizeConceptWikilinks(body, redirects)
	if err != nil {
		return "", err
	}
	projection := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}{title, description, body}
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

func normalizeConceptWikilinks(body string, redirects map[string]string) (string, error) {
	lines := strings.Split(body, "\n")
	section := "concepts"
	fenceChar, fenceSize := byte(0), 0
	for lineNo, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "## ") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			if strings.HasPrefix(heading, "sources") {
				section = "sources"
			}
			if strings.HasPrefix(heading, "concepts") {
				section = "concepts"
			}
		}
		if fenceChar != 0 {
			if count, ch, rest, ok := markdownFenceLine(trimmed); ok && rest == "" && ch == fenceChar && count >= fenceSize {
				fenceChar, fenceSize = 0, 0
			}
			continue
		}
		if count, ch, _, opening := markdownFenceLine(trimmed); opening {
			fenceChar, fenceSize = ch, count
			continue
		}
		if section == "sources" {
			continue
		}
		lines[lineNo] = string(rewriteRecencyLine([]byte(line), redirects))
	}
	return strings.Join(lines, "\n"), nil
}

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

func rewriteRecencyLine(line []byte, redirects map[string]string) []byte {
	var out bytes.Buffer
	for i := 0; i < len(line); {
		if line[i] == '`' {
			n := 1
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			j := i + n
			for j < len(line) {
				if line[j] != '`' {
					j++
					continue
				}
				m := 1
				for j+m < len(line) && line[j+m] == '`' {
					m++
				}
				if m == n {
					j += m
					break
				}
				j += m
			}
			if j > len(line) {
				j = len(line)
			}
			out.Write(line[i:j])
			i = j
			continue
		}
		if i+1 < len(line) && line[i] == '[' && line[i+1] == '[' {
			end := bytes.Index(line[i+2:], []byte("]]"))
			if end >= 0 {
				inner := string(line[i+2 : i+2+end])
				out.WriteString("[[")
				out.WriteString(normalizeRecencyTarget(inner, redirects))
				out.WriteString("]]")
				i += end + 4
				continue
			}
		}
		out.WriteByte(line[i])
		i++
	}
	return out.Bytes()
}

func normalizeRecencyTarget(inner string, redirects map[string]string) string {
	target, alias, hasAlias := strings.Cut(inner, "|")
	trimmed := strings.TrimSpace(target)
	collection := ""
	if prefix, rest, ok := strings.Cut(trimmed, "/"); ok && (prefix == "concepts" || prefix == "sources") {
		collection, trimmed = prefix+"/", rest
	}
	if collection == "sources/" {
		return inner
	}
	path, anchor, hasAnchor := strings.Cut(trimmed, "#")
	id, slug, hasID := splitCanonicalIDSlug(path)
	if hasID {
		if redirected, ok := redirects[id]; ok {
			id = redirected
		}
		path = id + "-" + slug
	} else if redirected, ok := redirects[path]; ok {
		path = redirected
	}
	var out strings.Builder
	out.WriteString(collection)
	out.WriteString(path)
	if hasAnchor {
		out.WriteByte('#')
		out.WriteString(anchor)
	}
	if hasAlias {
		out.WriteByte('|')
		out.WriteString(strings.TrimSpace(alias))
	}
	return out.String()
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
