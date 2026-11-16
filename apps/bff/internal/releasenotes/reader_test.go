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
	index        []byte
	generation   int64
	content      map[string][]byte
	err          error
	indexReads   int
	contentReads int
}

func (s *memoryReadStore) ReadIndex(context.Context) (IndexSnapshot, error) {
	s.indexReads++
	if s.err != nil {
		return IndexSnapshot{}, s.err
	}
	return IndexSnapshot{Bytes: append([]byte(nil), s.index...), Generation: s.generation}, nil
}
func (s *memoryReadStore) ReadContent(_ context.Context, path string) ([]byte, error) {
	s.contentReads++
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

func schema2AliasStore(t *testing.T, runtime BuildInfo, includeCanonical bool) *memoryReadStore {
	t.Helper()
	store := &memoryReadStore{generation: 321, content: map[string][]byte{}}
	versions := make([]any, 0, 2)
	addRow := func(version, publishedAt string) {
		body := testContent(version, "en")
		digest := sha256Hex(body)
		path := contentPath(version, "en", digest)
		store.content[path] = body
		versions = append(versions, map[string]any{
			"product_version": version,
			"published_at":    publishedAt,
			"locales": map[string]any{
				"en": map[string]any{"sha256": digest, "object_path": path},
			},
		})
	}
	if includeCanonical {
		addRow(PublicVersion, "2026-11-15T01:14:01Z")
	}
	addRow(LegacyVersion, "2026-11-13T04:00:00Z")
	aliasRecord := map[string]any{
		"environment": "dev", "requested_version": LegacyVersion, "canonical_version": PublicVersion,
		"legacy_row_version": LegacyVersion, "source_marker_sha256": strings.Repeat("a", 64),
		"source_index_generation": int64(123), "transition_plan_sha256": strings.Repeat("b", 64),
		"transition_run_id": "1234567890", "transition_run_attempt": 1,
		"creation_marker_sha256": strings.Repeat("c", 64),
	}
	canonicalAlias, err := json.Marshal(aliasRecord)
	if err != nil {
		t.Fatal(err)
	}
	aliasRecord["alias_record_sha256"] = sha256Hex(canonicalAlias)
	marker := map[string]any{
		"schema_version": MarkerSchemaV2, "environment": runtime.Environment,
		"product_version": runtime.ProductVersion, "commit": runtime.Commit, "branch": runtime.Branch,
		"tag": runtime.Tag, "image_tag": runtime.ImageTag, "image_digest": "sha256:" + strings.Repeat("c", 64),
		"service": runtime.Service, "service_uid": "service-uid-1", "service_generation": 42,
		"revision": runtime.Revision, "operation": "release",
		"deployment_evidence_digest": "sha256:" + strings.Repeat("d", 64),
		"release_version":            PublicVersion, "operation_run_id": "1234567890", "operation_run_attempt": 1,
		"component_builds": []any{}, "previous_release": nil,
	}
	index := map[string]any{
		"schema_version": IndexSchemaV2, "versions": versions,
		"current_deployment": marker, "legacy_aliases": []any{aliasRecord},
	}
	store.index, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSchema2SeparatesPublicReleaseFromBFFBuildAndResolvesOnlyFixedAlias(t *testing.T) {
	runtime := testRuntime()
	runtime.ProductVersion = LegacyVersion
	runtime.Tag = "dev-bff-1.0.0"
	store := schema2AliasStore(t, runtime, true)
	reader := Reader{Store: store, Runtime: runtime}
	index, err := reader.Index(context.Background())
	if err != nil || index.RuntimeProductVersion != LegacyVersion || index.CurrentProductVersion != PublicVersion ||
		index.CurrentStatus != "available" || index.Current == nil || len(index.CompatibilityAliases) != 1 ||
		index.CompatibilityAliases[0] != (PublicAlias{RequestedVersion: LegacyVersion, CanonicalVersion: PublicVersion, LegacyRowVersion: LegacyVersion}) {
		t.Fatalf("schema-2 public index lost build/public identities or fixed alias: response=%+v err=%v", index, err)
	}
	if len(index.CurrentComponents) != 1 || index.CurrentComponents[0].Component != "bff" ||
		index.CurrentComponents[0].ProductVersion != LegacyVersion || index.ComponentDetailsStatus != "complete" {
		t.Fatalf("schema-2 BFF component facts do not preserve actual build version: %+v", index)
	}
	aliasJSON, err := json.Marshal(index.CompatibilityAliases[0])
	if err != nil {
		t.Fatal(err)
	}
	var aliasFields map[string]json.RawMessage
	if err := json.Unmarshal(aliasJSON, &aliasFields); err != nil || len(aliasFields) != 3 {
		t.Fatalf("private alias proof leaked into public DTO: fields=%s err=%v", aliasJSON, err)
	}
	content, err := reader.Content(context.Background(), LegacyVersion, "en")
	if err != nil || content.RequestedProductVersion != LegacyVersion || content.CanonicalProductVersion != PublicVersion ||
		content.ProductVersion != PublicVersion || !content.LegacyAlias || content.LegacyFallback || !content.IsCurrent {
		t.Fatalf("exact compatibility alias did not resolve to the verified public release: %+v err=%v", content, err)
	}

	fallbackStore := schema2AliasStore(t, runtime, false)
	fallback, err := (Reader{Store: fallbackStore, Runtime: runtime}).Content(context.Background(), LegacyVersion, "en")
	if err != nil || fallback.RequestedProductVersion != LegacyVersion || fallback.CanonicalProductVersion != PublicVersion ||
		fallback.ProductVersion != LegacyVersion || !fallback.LegacyAlias || !fallback.LegacyFallback || fallback.IsCurrent {
		t.Fatalf("legacy fallback changed identity or claimed current: %+v err=%v", fallback, err)
	}
	if _, err := (Reader{Store: store, Runtime: runtime}).Content(context.Background(), "1.0.1", "en"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reader accepted an unlisted replacement version: %v", err)
	}
	staleRuntime := runtime
	staleRuntime.Revision = "llm-wiki-bff-dev-00040-old"
	staleIndex, err := (Reader{Store: store, Runtime: staleRuntime}).Index(context.Background())
	if err != nil || staleIndex.CurrentStatus != "unavailable" || staleIndex.Current != nil ||
		staleIndex.RuntimeProductVersion != LegacyVersion {
		t.Fatalf("stale BFF build was projected as the public current release: %+v err=%v", staleIndex, err)
	}
}

func TestSchema2ClosedShapesRejectMissingAliasAndExtraUnionFields(t *testing.T) {
	runtime := testRuntime()
	runtime.ProductVersion = LegacyVersion
	store := schema2AliasStore(t, runtime, true)
	var index map[string]any
	if err := json.Unmarshal(store.index, &index); err != nil {
		t.Fatal(err)
	}
	delete(index, "legacy_aliases")
	missingAliases, _ := json.Marshal(index)
	if _, err := DecodeIndex(missingAliases); !errors.Is(err, ErrInvalid) {
		t.Fatalf("schema-2 index without required legacy_aliases accepted: %v", err)
	}
	store = schema2AliasStore(t, runtime, true)
	var decoded map[string]any
	_ = json.Unmarshal(store.index, &decoded)
	marker := decoded["current_deployment"].(map[string]any)
	marker["component_builds"] = []any{map[string]any{
		"component": "frontend", "deployment_id": "d1", "receipt_sha256": strings.Repeat("e", 64), "job_name": "",
	}}
	extraUnionField, _ := json.Marshal(decoded)
	if _, err := DecodeIndex(extraUnionField); !errors.Is(err, ErrInvalid) {
		t.Fatalf("frontend union accepted an extraneous worker field: %v", err)
	}
}

func TestNudgeSchema2WitnessUsesPublicVersionAndGETSnapshotReadsNoBodies(t *testing.T) {
	runtime := testRuntime()
	runtime.ProductVersion = LegacyVersion
	runtime.Tag = "dev-bff-1.0.0"
	store := schema2AliasStore(t, runtime, true)
	result, snapshot := ObserveSourceWithSnapshot(context.Background(), store, runtime, 7)
	if result.Status != "complete" || len(result.Upserts) != 1 || snapshot == nil ||
		store.contentReads != 1 || result.ReleaseAliasTransfer == nil {
		t.Fatalf("schema-2 Nudge observation did not bind one verified public item/alias: result=%+v snapshot=%+v reads=%d", result, snapshot, store.contentReads)
	}
	upsert := result.Upserts[0]
	if err := nudge.ValidateObservationInput(upsert); err != nil {
		t.Fatalf("common Release wrapper rejected schema-2 witness: %v", err)
	}
	if upsert.ProducerWitness.ProductVersion != PublicVersion || upsert.AttentionMaterialRevision != "version:"+PublicVersion ||
		upsert.ProducerWitness.IndexRevision != store.generation || upsert.ProducerWitness.IndexDigest != sha256Hex(store.index) ||
		upsert.Observation.Actions[0].Target.Version != PublicVersion ||
		result.ReleaseAliasTransfer.RequestedVersion != LegacyVersion || result.ReleaseAliasTransfer.CanonicalVersion != PublicVersion {
		t.Fatalf("Nudge conflated BFF build and public release identity: %+v proof=%+v", upsert, result.ReleaseAliasTransfer)
	}

	store.indexReads, store.contentReads = 0, 0
	visibility, err := ReadVisibilitySnapshot(context.Background(), store, runtime)
	if err != nil || visibility == nil || visibility.IndexRevision != store.generation ||
		visibility.IndexDigest != sha256Hex(store.index) || store.indexReads != 1 || store.contentReads != 0 {
		t.Fatalf("authenticated GET visibility snapshot exceeded one metadata read: snapshot=%+v err=%v index=%d content=%d",
			visibility, err, store.indexReads, store.contentReads)
	}
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

func TestMixedIndexMarkerSchemasNeverExposeCurrentButKeepHistoryReadable(t *testing.T) {
	runtime := testRuntime()
	legacyStore := indexedStore(t, runtime, true)
	version2Store := schema2AliasStore(t, runtime, true)
	var version2Envelope map[string]any
	if err := json.Unmarshal(version2Store.index, &version2Envelope); err != nil {
		t.Fatal(err)
	}
	version2Marker := version2Envelope["current_deployment"]

	legacyMixed := indexedStore(t, runtime, true)
	var legacyEnvelope map[string]any
	if err := json.Unmarshal(legacyStore.index, &legacyEnvelope); err != nil {
		t.Fatal(err)
	}
	legacyEnvelope["current_deployment"] = version2Marker
	legacyMixed.index, _ = json.Marshal(legacyEnvelope)

	version2Envelope["current_deployment"] = testMarker(runtime)
	version2Store.index, _ = json.Marshal(version2Envelope)

	for name, store := range map[string]*memoryReadStore{"v1-index-v2-marker": legacyMixed, "v2-index-v1-marker": version2Store} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeIndex(store.index); !errors.Is(err, ErrInvalid) {
				t.Fatalf("strict decoder accepted mixed marker schema: %v", err)
			}
			response, err := (Reader{Store: store, Runtime: runtime}).Index(context.Background())
			if err != nil || response.CurrentStatus != "unavailable" || response.Current != nil || len(response.History) != 1 {
				t.Fatalf("mixed marker public index=%+v err=%v", response, err)
			}
			content, err := (Reader{Store: store, Runtime: runtime}).Content(context.Background(), runtime.ProductVersion, "en")
			if err != nil || content.IsCurrent {
				t.Fatalf("mixed marker content=%+v err=%v", content, err)
			}
		})
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
