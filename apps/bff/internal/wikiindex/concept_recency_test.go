package wikiindex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const recencyLegacyA = "a1b2c3d4e5f6"
const recencyLegacyB = "b1c2d3e4f5a6"
const recencyEntityA = "01JAZ5N7Y3K8M2Q4R6T9VWXABE"
const recencyEntityB = "01JAZ5N7Y3K8M2Q4R6T9VWXABF"

func recencyIDMap(active map[string]string, redirects map[string]string) IDMap {
	return IDMap{Concept: active, DormantConcept: map[string]string{}, IDRedirects: redirects}
}

func recencyPageRow(id, slug, hash, updated string) []byte {
	fields := map[string]any{"slug": slug, "title": slug, "frontmatter": map[string]any{"id": id}}
	if hash != "" {
		fields["content_sha256"] = hash
	}
	if updated != "" {
		fields["content_updated_at"] = updated
	}
	data, _ := json.Marshal(fields)
	return append(data, '\n')
}

func recencySnapshot(ids IDMap, row []byte, page []byte, verified bool) ConceptRecencySnapshot {
	return ConceptRecencySnapshot{
		IDMap: ids, IDMapVerified: verified, ActiveJSONL: row,
		ActivePages: map[string][]byte{}, DormantPages: map[string][]byte{},
	}
}

func TestEnrichConceptRecencySameBasisMissingPageStillMarksRealEdit(t *testing.T) {
	priorPage := []byte("---\ntitle: Alpha\n---\nold body\n")
	currentPage := []byte("---\ntitle: Alpha\n---\nnew body\n")
	priorHash, err := fingerprintConcept(priorPage, nil, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	ids := recencyIDMap(map[string]string{recencyLegacyA: "alpha"}, nil)
	prior := recencySnapshot(ids, recencyPageRow(recencyLegacyA, "alpha", priorHash, "2026-01-01T00:00:00Z"), nil, true)
	current := recencySnapshot(ids, recencyPageRow(recencyLegacyA, "alpha", "", ""), currentPage, true)
	current.ActivePages[recencyLegacyA] = currentPage
	runAt := time.Date(2026, 11, 14, 8, 0, 0, 123, time.UTC)
	active, _, err := EnrichConceptRecency(current, prior, runAt)
	if err != nil {
		t.Fatalf("EnrichConceptRecency() error = %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(active), &row); err != nil {
		t.Fatal(err)
	}
	if row["content_sha256"] != fingerprintForTest(t, currentPage, nil, "alpha") {
		t.Fatalf("content_sha256 = %v", row["content_sha256"])
	}
	if row["content_updated_at"] != runAt.Format(time.RFC3339Nano) {
		t.Fatalf("content_updated_at = %v", row["content_updated_at"])
	}
}

func TestEnrichConceptRecencyMissingPriorMapIsUnknownWhenHashChanges(t *testing.T) {
	oldPage := []byte("---\ntitle: Alpha\n---\n[[b1c2d3e4f5a6-beta#part|Beta]]\n")
	newPage := []byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#part|Beta]]\n")
	oldHash := fingerprintForTest(t, oldPage, nil, "alpha")
	prior := recencySnapshot(IDMap{}, recencyPageRow(recencyLegacyA, "alpha", oldHash, "2026-01-01T00:00:00Z"), nil, false)
	currentIDs := recencyIDMap(
		map[string]string{recencyEntityA: "alpha", recencyEntityB: "beta"},
		map[string]string{recencyLegacyA: recencyEntityA, recencyLegacyB: recencyEntityB},
	)
	current := recencySnapshot(currentIDs, recencyPageRow(recencyEntityA, "alpha", "", ""), newPage, true)
	current.ActivePages[recencyEntityA] = newPage
	current.ActiveJSONL = append(current.ActiveJSONL, recencyPageRow(recencyEntityB, "beta", "", "")...)
	current.ActivePages[recencyEntityB] = []byte("---\ntitle: Beta\n---\nTarget\n")
	if _, _, err := EnrichConceptRecency(current, prior, time.Now()); err == nil || !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("missing prior map result = %v", err)
	}
}

func TestEnrichConceptRecencyMissingPriorPageWithChangedRedirectMapFails(t *testing.T) {
	oldPage := []byte("---\ntitle: Alpha\n---\n[[b1c2d3e4f5a6-beta#part|Beta]]\n")
	oldHash := fingerprintForTest(t, oldPage, nil, "alpha")
	priorIDs := recencyIDMap(map[string]string{recencyLegacyA: "alpha", recencyEntityB: "beta"}, nil)
	prior := recencySnapshot(priorIDs, recencyPageRow(recencyLegacyA, "alpha", oldHash, "2026-01-01T00:00:00Z"), nil, true)
	currentPage := []byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#part|Beta]]\n")
	currentIDs := recencyIDMap(
		map[string]string{recencyEntityA: "alpha", recencyEntityB: "beta"},
		map[string]string{recencyLegacyA: recencyEntityA, recencyLegacyB: recencyEntityB},
	)
	current := recencySnapshot(currentIDs, recencyPageRow(recencyEntityA, "alpha", "", ""), currentPage, true)
	current.ActivePages[recencyEntityA] = currentPage
	current.ActiveJSONL = append(current.ActiveJSONL, recencyPageRow(recencyEntityB, "beta", "", "")...)
	current.ActivePages[recencyEntityB] = []byte("---\ntitle: Beta\n---\nTarget\n")
	if _, _, err := EnrichConceptRecency(current, prior, time.Now()); err == nil || !strings.Contains(err.Error(), "prior page is missing") {
		t.Fatalf("missing prior page result = %v", err)
	}
}

func TestEnrichConceptRecencyVerifiedFullWitnessPreservesMigrationTimeAndGen3(t *testing.T) {
	oldPage := []byte("---\ntitle: Alpha\ndescription: Stable\n---\nSee [[b1c2d3e4f5a6-beta#part|Beta]]\n")
	newPage := []byte("---\ntitle: Alpha\ndescription: Stable\n---\nSee [[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#part|Beta]]\n")
	priorHash := fingerprintForTest(t, oldPage, nil, "alpha")
	priorIDs := recencyIDMap(map[string]string{recencyLegacyA: "alpha", recencyEntityB: "beta"}, nil)
	prior := recencySnapshot(priorIDs, recencyPageRow(recencyLegacyA, "alpha", priorHash, "2026-01-01T00:00:00Z"), oldPage, true)
	prior.ActivePages[recencyLegacyA] = oldPage
	prior.ActiveJSONL = append(prior.ActiveJSONL, recencyPageRow(recencyEntityB, "beta", fingerprintForTest(t, []byte("---\ntitle: Beta\n---\nTarget\n"), nil, "beta"), "2026-02-01T00:00:00Z")...)
	prior.ActivePages[recencyEntityB] = []byte("---\ntitle: Beta\n---\nTarget\n")
	currentIDs := recencyIDMap(
		map[string]string{recencyEntityA: "alpha", recencyEntityB: "beta"},
		map[string]string{recencyLegacyA: recencyEntityA, recencyLegacyB: recencyEntityB},
	)
	current := recencySnapshot(currentIDs, recencyPageRow(recencyEntityA, "alpha", "", ""), newPage, true)
	current.ActivePages[recencyEntityA] = newPage
	current.ActiveJSONL = append(current.ActiveJSONL, recencyPageRow(recencyEntityB, "beta", "", "")...)
	current.ActivePages[recencyEntityB] = prior.ActivePages[recencyEntityB]
	runAt := time.Date(2026, 11, 14, 8, 30, 0, 0, time.UTC)
	active, _, err := EnrichConceptRecency(current, prior, runAt)
	if err != nil {
		t.Fatalf("gen2 EnrichConceptRecency() error = %v", err)
	}
	alpha := rowByCacheID(t, active, recencyEntityA)
	if alpha["content_sha256"] != fingerprintForTest(t, newPage, currentIDs.IDRedirects, "alpha") {
		t.Fatalf("gen2 hash = %v", alpha["content_sha256"])
	}
	if alpha["content_updated_at"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("gen2 time = %v", alpha["content_updated_at"])
	}
	gen2 := recencySnapshot(currentIDs, active, nil, true)
	gen2.ActivePages = current.ActivePages
	gen3, _, err := EnrichConceptRecency(gen2, gen2, runAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("gen3 EnrichConceptRecency() error = %v", err)
	}
	alpha3 := rowByCacheID(t, gen3, recencyEntityA)
	if alpha3["content_updated_at"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("gen3 time = %v", alpha3["content_updated_at"])
	}
}

func TestConceptFingerprintKeepsVisibleLabelAnchorAndIdentityIndependent(t *testing.T) {
	redirects := map[string]string{recencyLegacyB: recencyEntityB}
	base := []byte("---\ntitle: Alpha\n---\n[[b1c2d3e4f5a6-beta#part|Beta]]\n")
	technicalRewrite := []byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#part|Beta]]\n")
	if fingerprintForTest(t, base, redirects, "alpha") != fingerprintForTest(t, technicalRewrite, redirects, "alpha") {
		t.Fatal("direct ID migration with same visible label and anchor changed fingerprint")
	}
	for _, page := range [][]byte{
		[]byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#part|Different]]\n"),
		[]byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABF-beta#other|Beta]]\n"),
		[]byte("---\ntitle: Alpha\n---\n[[01JAZ5N7Y3K8M2Q4R6T9VWXABC-beta#part|Beta]]\n"),
	} {
		if fingerprintForTest(t, page, redirects, "alpha") == fingerprintForTest(t, technicalRewrite, redirects, "alpha") {
			t.Fatalf("visible change did not alter fingerprint: %s", page)
		}
	}
}

func fingerprintForTest(t *testing.T, page []byte, redirects map[string]string, slug string) string {
	t.Helper()
	hash, err := fingerprintConcept(page, redirects, slug)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func rowByCacheID(t *testing.T, data []byte, id string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		matter, _ := row["frontmatter"].(map[string]any)
		if matter["id"] == id {
			return row
		}
	}
	t.Fatalf("cache row for %s missing in %s", id, data)
	return nil
}
