package releasenotes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

type memoryReadStore struct {
	index      []byte
	generation int64
	content    map[string][]byte
	err        error
}

func (s *memoryReadStore) ReadIndex(context.Context) (IndexSnapshot, error) {
	if s.err != nil {
		return IndexSnapshot{}, s.err
	}
	return IndexSnapshot{Bytes: append([]byte(nil), s.index...), Generation: s.generation}, nil
}
func (s *memoryReadStore) ReadContent(_ context.Context, path string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	body, ok := s.content[path]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), body...), nil
}

func testRuntime() BuildInfo {
	return BuildInfo{Environment: "dev", ProductVersion: "2026.11.4", Commit: strings.Repeat("a", 40),
		Branch: "develop", Tag: "v2026.11.4", ImageTag: "bff-test", Service: "llm-wiki-bff-dev", Revision: "llm-wiki-bff-dev-00041-xyz"}
}

func testMarker(runtime BuildInfo) *CurrentDeployment {
	return &CurrentDeployment{SchemaVersion: 1, Environment: runtime.Environment, ProductVersion: runtime.ProductVersion,
		Commit: runtime.Commit, Branch: runtime.Branch, Tag: runtime.Tag, ImageTag: runtime.ImageTag,
		ImageDigest: "sha256:" + strings.Repeat("c", 64), Service: runtime.Service, ServiceUID: "service-uid-1",
		ServiceGeneration: 42, Revision: runtime.Revision, Operation: "release",
		DeploymentEvidenceDigest: "sha256:" + strings.Repeat("d", 64)}
}

func testContent(version, locale string) []byte {
	content := Content{SchemaVersion: 1, Product: ProductName, ProductVersion: version, Locale: locale,
		Entries: []Entry{{Title: "New view", WhatChanged: "A new view is available.", UserBenefit: "Find information faster.", WhereToUse: "Open the workspace.", Limitations: []string{}}}}
	body, _ := json.Marshal(content)
	return body
}

func indexedStore(t *testing.T, runtime BuildInfo, includeRow bool) *memoryReadStore {
	t.Helper()
	s := &memoryReadStore{generation: 123, content: map[string][]byte{}}
	index := Index{SchemaVersion: 1, Versions: []VersionRow{}, CurrentDeployment: testMarker(runtime)}
	if includeRow {
		row := VersionRow{ProductVersion: runtime.ProductVersion,
			PublishedAt: "2026-11-13T04:00:00Z", Locales: map[string]LocaleFile{}}
		for _, locale := range []string{"en", "zh-TW"} {
			body := testContent(runtime.ProductVersion, locale)
			digest := sha256.Sum256(body)
			sha := hex.EncodeToString(digest[:])
			path := contentPath(runtime.ProductVersion, locale, sha)
			row.Locales[locale] = LocaleFile{SHA256: sha, ObjectPath: path}
			s.content[path] = body
		}
		index.Versions = append(index.Versions, row)
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	s.index = indexBytes
	return s
}

func TestIndexRequiresExactMarkerBeforeCallingRuntimeVersionCurrent(t *testing.T) {
	runtime := testRuntime()
	store := indexedStore(t, runtime, true)
	reader := Reader{Store: store, Runtime: runtime}
	response, err := reader.Index(context.Background())
	if err != nil || response.CurrentStatus != "available" || response.CurrentProductVersion != runtime.ProductVersion || response.Current == nil || len(response.History) != 0 {
		t.Fatalf("matching marker response=%+v err=%v", response, err)
	}
	oldRuntime := runtime
	oldRuntime.Revision = "llm-wiki-bff-dev-00040-old"
	response, err = (Reader{Store: store, Runtime: oldRuntime}).Index(context.Background())
	if err != nil || response.CurrentStatus != "unavailable" || response.Current != nil || response.CurrentProductVersion != "" || len(response.History) != 1 {
		t.Fatalf("stale runtime response=%+v err=%v", response, err)
	}
}

func TestCurrentMissingRequiresPresentMatchingMarker(t *testing.T) {
	runtime := testRuntime()
	response, err := (Reader{Store: indexedStore(t, runtime, false), Runtime: runtime}).Index(context.Background())
	if err != nil || response.CurrentStatus != "missing" || response.CurrentProductVersion != runtime.ProductVersion || response.Current != nil {
		t.Fatalf("matching marker missing response=%+v err=%v", response, err)
	}
	legacy := indexedStore(t, runtime, false)
	var index Index
	if err := json.Unmarshal(legacy.index, &index); err != nil {
		t.Fatal(err)
	}
	index.CurrentDeployment = nil
	legacy.index, _ = json.Marshal(index)
	response, err = (Reader{Store: legacy, Runtime: runtime}).Index(context.Background())
	if err != nil || response.CurrentStatus != "unavailable" {
		t.Fatalf("legacy index response=%+v err=%v", response, err)
	}
}

func TestMalformedCurrentMarkerLeavesOnlyValidatedHistoryReadable(t *testing.T) {
	runtime := testRuntime()
	store := indexedStore(t, runtime, true)
	var index Index
	if err := json.Unmarshal(store.index, &index); err != nil {
		t.Fatal(err)
	}
	index.CurrentDeployment = nil
	legacy, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	store.index = []byte(strings.TrimSuffix(string(legacy), "}") + `,"current_deployment":{"schema_version":1,"unknown":"bad"}}`)
	response, err := (Reader{Store: store, Runtime: runtime}).Index(context.Background())
	if err != nil || response.CurrentStatus != "unavailable" || response.Current != nil || len(response.History) != 1 {
		t.Fatalf("malformed marker index response=%+v err=%v", response, err)
	}
	history, err := (Reader{Store: store, Runtime: runtime}).Content(context.Background(), runtime.ProductVersion, "en")
	if err != nil || history.IsCurrent || history.ProductVersion != runtime.ProductVersion {
		t.Fatalf("validated history response=%+v err=%v", history, err)
	}
	if _, err := DecodeIndex(store.index); !errors.Is(err, ErrInvalid) {
		t.Fatalf("strict mutation decode should reject malformed marker, got %v", err)
	}
}

func TestContentHashLocaleFallbackAndExactVersion(t *testing.T) {
	runtime := testRuntime()
	store := indexedStore(t, runtime, true)
	response, err := (Reader{Store: store, Runtime: runtime}).Content(context.Background(), runtime.ProductVersion, "en")
	if err != nil || !response.IsCurrent || response.LocaleFallback || response.Locale != "en" || len(response.Entries) != 1 {
		t.Fatalf("exact current response=%+v err=%v", response, err)
	}
	response, err = (Reader{Store: store, Runtime: runtime}).Content(context.Background(), runtime.ProductVersion, "zh-TW")
	if err != nil || !response.IsCurrent || response.Locale != "zh-TW" {
		t.Fatalf("zh response=%+v err=%v", response, err)
	}
	store.content[responseLocalePath(t, store, runtime.ProductVersion, "zh-TW")] = []byte("corrupt")
	if _, err := (Reader{Store: store, Runtime: runtime}).Content(context.Background(), runtime.ProductVersion, "zh-TW"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("corrupt body error=%v", err)
	}
}

func responseLocalePath(t *testing.T, store *memoryReadStore, version, locale string) string {
	t.Helper()
	var index Index
	if err := json.Unmarshal(store.index, &index); err != nil {
		t.Fatal(err)
	}
	for _, row := range index.Versions {
		if row.ProductVersion == version {
			return row.Locales[locale].ObjectPath
		}
	}
	t.Fatal("version missing")
	return ""
}

func TestObserveSourceUsesCommonReleaseWrapperAndHonestTransitions(t *testing.T) {
	runtime := testRuntime()
	store := indexedStore(t, runtime, true)
	result := ObserveSource(context.Background(), store, runtime, 9)
	if result.Status != "complete" || len(result.Upserts) != 1 || len(result.AuthoritativeClears) != 0 {
		t.Fatalf("current result=%+v", result)
	}
	upsert := result.Upserts[0]
	if err := nudge.ValidateObservationInput(upsert); err != nil {
		t.Fatalf("common validator rejected wrapper: %v", err)
	}
	observationJSON, err := json.Marshal(upsert.Observation)
	if err != nil {
		t.Fatal(err)
	}
	var canonicalObservation any
	if err := json.Unmarshal(observationJSON, &canonicalObservation); err != nil {
		t.Fatal(err)
	}
	canonicalJSON, err := json.Marshal(canonicalObservation)
	if err != nil {
		t.Fatal(err)
	}
	canonicalDigest := sha256.Sum256(canonicalJSON)
	if upsert.PayloadDigest != hex.EncodeToString(canonicalDigest[:]) {
		t.Fatalf("payload digest is not canonical observation JSON: %s", upsert.PayloadDigest)
	}
	if upsert.Category != nudge.CategoryReleaseNotes || upsert.SourceKey != "release-notes:llm-wiki-cloud:dev" || upsert.ExpectedSourceRevision != 9 ||
		upsert.AttentionMaterialRevision != "version:"+runtime.ProductVersion || upsert.ProducerWitness.IndexRevision != store.generation || upsert.ProducerWitness.IndexDigest != sha256Hex(store.index) {
		t.Fatalf("incorrect wrapper: %+v", upsert)
	}
	url, err := nudge.ResolveTarget(*upsert.Observation.Actions[0].Target)
	if err != nil || url != "/release-notes?version=2026.11.4" {
		t.Fatalf("target URL=%q err=%v", url, err)
	}

	missing := indexedStore(t, runtime, false)
	result = ObserveSource(context.Background(), missing, runtime, 10)
	if result.Status != "complete" || len(result.Upserts) != 0 || len(result.AuthoritativeClears) != 1 || result.AuthoritativeClears[0].Proof != "authoritative-current-missing" {
		t.Fatalf("missing result=%+v", result)
	}
	var legacyIndex Index
	_ = json.Unmarshal(missing.index, &legacyIndex)
	legacyIndex.CurrentDeployment = nil
	missing.index, _ = json.Marshal(legacyIndex)
	result = ObserveSource(context.Background(), missing, runtime, 11)
	if result.Status != "unavailable" || len(result.AuthoritativeClears) != 0 || len(result.Upserts) != 0 {
		t.Fatalf("legacy result=%+v", result)
	}
	missing.err = errors.New("offline")
	result = ObserveSource(context.Background(), missing, runtime, 12)
	if result.Status != "unavailable" || len(result.AuthoritativeClears) != 0 || len(result.Upserts) != 0 {
		t.Fatalf("outage result=%+v", result)
	}
}

func TestStrictIndexRejectsUnknownAndDuplicateVersions(t *testing.T) {
	unknown := []byte(`{"schema_version":1,"versions":[],"unexpected":true}`)
	if _, err := DecodeIndex(unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field error=%v", err)
	}
	duplicateKey := []byte(`{"schema_version":1,"schema_version":1,"versions":[]}`)
	if _, err := DecodeIndex(duplicateKey); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate JSON key error=%v", err)
	}
	index := Index{SchemaVersion: 1, Versions: []VersionRow{{ProductVersion: "1.0.0", PublishedAt: "2026-11-13T04:00:00Z", Locales: map[string]LocaleFile{"en": {SHA256: strings.Repeat("a", 64), ObjectPath: fmt.Sprintf("release-notes/v1/versions/1.0.0/en/%s.json", strings.Repeat("a", 64))}}}, {ProductVersion: "1.0.0", PublishedAt: "2026-11-13T04:00:00Z", Locales: map[string]LocaleFile{"en": {SHA256: strings.Repeat("a", 64), ObjectPath: fmt.Sprintf("release-notes/v1/versions/1.0.0/en/%s.json", strings.Repeat("a", 64))}}}}}
	if err := ValidateIndex(index); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate row error=%v", err)
	}
}
