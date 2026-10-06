package generation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeRejectsUnsafeOrInvalidFileTables(t *testing.T) {
	valid := `{"version":1,"generation_id":"g_abc123","created_at":"2026-07-18T00:00:00Z","input_fingerprint":"x","files":[{"path":"wiki/a.md","size":1,"sha256":"` + strings.Repeat("a", 64) + `","generation":7}]}`
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatalf("Decode(valid): %v", err)
	}
	for _, replace := range []struct{ old, new string }{
		{"wiki/a.md", "raw/a.md"},
		{"wiki/a.md", "wiki/../secret"},
		{"wiki/a.md", "wiki//a.md"},
		{"\"generation\":7", "\"generation\":0"},
		{"\"size\":1", "\"size\":-1"},
		{"\"g_abc123\"", "\"../unsafe\""},
	} {
		if _, err := Decode([]byte(strings.Replace(valid, replace.old, replace.new, 1))); err == nil {
			t.Fatalf("Decode accepted %q -> %q", replace.old, replace.new)
		}
	}

	duplicate := strings.Replace(valid, `]}`, `,{"path":"wiki/a.md","size":1,"sha256":"`+strings.Repeat("a", 64)+`","generation":8}]}`, 1)
	if _, err := Decode([]byte(duplicate)); err == nil {
		t.Fatal("Decode accepted duplicate path")
	}
}

func TestGenerationOwnedAndCanonicalPaths(t *testing.T) {
	for _, path := range []string{"wiki/a.md", "wiki/.drafts/a.md", "wiki.toml", "synto.toml", "cache/id_map.json", "cache/concepts.jsonl", "cache/dormant_concepts.jsonl", "cache/raw_status.json", "cache/suggested_queries.json", ".olw/state.db", ".synto/state.db", ".synto/INDEX.json"} {
		if !GenerationOwned(path) {
			t.Errorf("GenerationOwned(%q) = false", path)
		}
	}
	for _, path := range []string{"raw/a.md", "cache/annotations/a.json", "cache/source_status.json", "cache/pipeline-run.log", "wiki", "meta/index.md"} {
		if GenerationOwned(path) {
			t.Errorf("GenerationOwned(%q) = true", path)
		}
	}
}

func TestDecodeRejectsOversizedEncodingBeforeDecode(t *testing.T) {
	data := make([]byte, MaxManifestBytes+1)
	if _, err := Decode(data); err == nil || err.Error() != "generation manifest exceeds limit" {
		t.Fatalf("Decode oversized manifest error = %v", err)
	}
}

func TestDecodeRejectsDuplicateManifestFields(t *testing.T) {
	valid := `{"version":1,"generation_id":"g_abc123","created_at":"2026-07-18T00:00:00Z","input_fingerprint":"x","files":[{"path":"wiki/a.md","size":1,"sha256":"` + strings.Repeat("a", 64) + `","generation":7}]}`
	duplicateTopLevel := strings.Replace(valid, `"version":1,`, `"version":1,"version":1,`, 1)
	duplicateFileField := strings.Replace(valid, `"path":"wiki/a.md","size":1`, `"path":"wiki/a.md","path":"wiki/a.md","size":1`, 1)
	unknownTopLevel := strings.Replace(valid, `"version":1,`, `"unexpected":true,"version":1,`, 1)
	for name, data := range map[string]string{
		"duplicate top level":  duplicateTopLevel,
		"duplicate file field": duplicateFileField,
		"unknown top level":    unknownTopLevel,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(data)); err == nil {
				t.Fatal("Decode accepted malformed manifest")
			}
		})
	}
}

func TestGenerationManifestArchivesAndSourceSnapshotReference(t *testing.T) {
	valid := `{"version":1,"generation_id":"g_abc123","created_at":"2026-07-18T00:00:00Z","input_fingerprint":"x","source_snapshot_digest":"` + strings.Repeat("a", 64) + `","files":[]}`
	manifest, err := Decode([]byte(valid))
	if err != nil || manifest.SourceSnapshotDigest != strings.Repeat("a", 64) {
		t.Fatalf("Decode(source reference) = %+v, %v", manifest, err)
	}
	path, err := ArchivedManifestPath(manifest.GenerationID)
	if err != nil || path != Prefix+manifest.GenerationID+"/manifest.json" {
		t.Fatalf("ArchivedManifestPath = %q, %v", path, err)
	}
	if _, err := ArchivedManifestPath("../bad"); err == nil {
		t.Fatal("ArchivedManifestPath accepted unsafe ID")
	}
	manifest.SourceSnapshotDigest = "bad"
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate accepted malformed source snapshot digest")
	}
}

func TestManifestCarriesBoundedLocalExecutionIdentity(t *testing.T) {
	manifest := Manifest{
		Version: Version, GenerationID: "g_abc123", LocalExecutionID: "local-aaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt: "2026-07-18T00:00:00Z", InputFingerprint: "x", Files: []File{},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil || decoded.LocalExecutionID != manifest.LocalExecutionID {
		t.Fatalf("Decode(local execution identity) = %+v, %v", decoded, err)
	}
	manifest.LocalExecutionID = "local/unsafe"
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate accepted an unsafe local execution identity")
	}
}

func TestSourceSnapshotCompactStrictManifestAndPaths(t *testing.T) {
	contentDigest := Digest([]byte("original raw bytes"))
	manifest := SourceSnapshotManifest{
		SchemaVersion: SourceSnapshotSchema, ContentGeneration: "g_abc123",
		IDMapDigest: strings.Repeat("a", 64), SourceStatusDigest: strings.Repeat("b", 64),
		Rows: []SourceSnapshotRow{{StableID: "stable-source", RawPath: "raw/source.md", ContentDigest: contentDigest, ObjectGeneration: 17}},
	}
	data, digest, err := EncodeSourceSnapshot(manifest)
	if err != nil || digest != Digest(data) || strings.HasSuffix(string(data), "\n") {
		t.Fatalf("EncodeSourceSnapshot = %s, %q, %v", data, digest, err)
	}
	decoded, err := DecodeSourceSnapshot(data)
	if err != nil || decoded.ContentGeneration != manifest.ContentGeneration || decoded.Rows[0] != manifest.Rows[0] {
		t.Fatalf("DecodeSourceSnapshot = %+v, %v", decoded, err)
	}
	if got, err := SourceSnapshotPath(digest); err != nil || got != SourceSnapshotPrefix+digest+".json" {
		t.Fatalf("SourceSnapshotPath = %q, %v", got, err)
	}
	if got, err := SourceBytesPath(contentDigest); err != nil || got != SourceBytesPrefix+contentDigest+".txt" {
		t.Fatalf("SourceBytesPath = %q, %v", got, err)
	}

	for _, malformed := range []string{
		strings.Replace(string(data), `"schema_version":"`+SourceSnapshotSchema+`",`, `"extra":true,"schema_version":"`+SourceSnapshotSchema+`",`, 1),
		strings.Replace(string(data), `"stable_id":"stable-source",`, `"stable_id":"stable-source","stable_id":"stable-source",`, 1),
		strings.Replace(string(data), `"object_generation":17`, `"object_generation":0`, 1),
	} {
		if _, err := DecodeSourceSnapshot([]byte(malformed)); err == nil {
			t.Fatalf("DecodeSourceSnapshot accepted %s", malformed)
		}
	}
}
