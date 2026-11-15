package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
	"github.com/rayer/llm-wiki-bff/internal/releasenotes"
)

type releaseNotesRouteStore struct {
	mu         sync.Mutex
	snapshot   releasenotes.IndexSnapshot
	content    map[string][]byte
	indexErr   error
	indexReads int
}

func (s *releaseNotesRouteStore) ReadIndex(context.Context) (releasenotes.IndexSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexReads++
	if s.indexErr != nil {
		return releasenotes.IndexSnapshot{}, s.indexErr
	}
	return releasenotes.IndexSnapshot{Bytes: append([]byte(nil), s.snapshot.Bytes...), Generation: s.snapshot.Generation}, nil
}

func (s *releaseNotesRouteStore) ReadContent(_ context.Context, objectPath string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.content[objectPath]
	if !ok {
		return nil, errors.New("test content missing")
	}
	return append([]byte(nil), body...), nil
}

func (s *releaseNotesRouteStore) setIndex(snapshot releasenotes.IndexSnapshot, content map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = releasenotes.IndexSnapshot{Bytes: append([]byte(nil), snapshot.Bytes...), Generation: snapshot.Generation}
	s.content = content
	s.indexErr = nil
}

func (s *releaseNotesRouteStore) setIndexError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexErr = err
}

func (s *releaseNotesRouteStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.indexReads
}

func releaseNotesRuntime(version, revision string) releasenotes.BuildInfo {
	return releasenotes.BuildInfo{Environment: "dev", ProductVersion: version, Commit: strings.Repeat("a", 40),
		Branch: "develop", Tag: "v" + version, ImageTag: "bff-test", Service: "llm-wiki-bff-dev", Revision: revision}
}

func releaseNotesIndexFixture(t *testing.T, runtime releasenotes.BuildInfo, generation int64, includeCurrent bool, correction string) (*releaseNotesRouteStore, releasenotes.IndexSnapshot) {
	t.Helper()
	marker := &releasenotes.CurrentDeployment{SchemaVersion: 1, Environment: runtime.Environment,
		ProductVersion: runtime.ProductVersion, Commit: runtime.Commit, Branch: runtime.Branch, Tag: runtime.Tag,
		ImageTag: runtime.ImageTag, ImageDigest: "sha256:" + strings.Repeat("c", 64), Service: runtime.Service,
		ServiceUID: "service-uid-1", ServiceGeneration: 42, Revision: runtime.Revision, Operation: "release",
		DeploymentEvidenceDigest: "sha256:" + strings.Repeat("d", 64)}
	index := releasenotes.Index{SchemaVersion: 1, Versions: []releasenotes.VersionRow{}, CurrentDeployment: marker}
	content := map[string][]byte{}
	if includeCurrent {
		body, err := json.Marshal(releasenotes.Content{SchemaVersion: 1, Product: releasenotes.ProductName,
			ProductVersion: runtime.ProductVersion, Locale: "en", Entries: []releasenotes.Entry{{
				Title: "Release notes " + correction, WhatChanged: "A tested release change.",
				UserBenefit: "People can understand the change.", WhereToUse: "Open the Release Notes page.",
				Limitations: []string{},
			}}})
		if err != nil {
			t.Fatalf("marshal fixture Release content: %v", err)
		}
		digest := sha256.Sum256(body)
		digestHex := hex.EncodeToString(digest[:])
		objectPath := fmt.Sprintf("release-notes/v1/versions/%s/en/%s.json", runtime.ProductVersion, digestHex)
		index.Versions = append(index.Versions, releasenotes.VersionRow{ProductVersion: runtime.ProductVersion,
			PublishedAt: "2026-11-14T00:00:00Z", Locales: map[string]releasenotes.LocaleFile{
				"en": {SHA256: digestHex, ObjectPath: objectPath},
			}})
		content[objectPath] = body
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal fixture Release index: %v", err)
	}
	snapshot := releasenotes.IndexSnapshot{Bytes: indexBytes, Generation: generation}
	return &releaseNotesRouteStore{snapshot: snapshot, content: content}, snapshot
}

func releaseNotesRouteCall(t *testing.T, router http.Handler, method, path, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response.Code, response.Body.String()
}

func newReleaseNotesRoute(t *testing.T, handler *Handler, ownerID string) *gin.Engine {
	t.Helper()
	router := gin.New()
	router.POST("/api/v1/nudges/reconcile", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.ReconcileNudges(c)
	})
	router.GET("/api/v1/nudges", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.GetNudges(c)
	})
	router.PATCH("/api/v1/nudges/:category/attention", func(c *gin.Context) {
		c.Set("userID", ownerID)
		handler.PatchNudgeAttention(c)
	})
	return router
}

func TestReleaseNotesNudgeAuthenticatedRoutePersistsProjectsAndPreservesTransitions(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	runtime := releaseNotesRuntime("2026.11.4", "llm-wiki-bff-dev-00041-xyz")
	store, snapshot := releaseNotesIndexFixture(t, runtime, 123, true, "original")
	ownerID := fmt.Sprintf("release-notes-route-%d", time.Now().UnixNano())
	handler := &Handler{firestore: fsClient}
	handler.SetReleaseNotesSource(store, runtime)
	router := newReleaseNotesRoute(t, handler, ownerID)
	key := "release-notes:llm-wiki-cloud:dev"

	status, response := releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var view nudge.View
	if status != http.StatusOK || json.Unmarshal([]byte(response), &view) != nil || view.Partial || len(view.Items) != 1 {
		t.Fatalf("authenticated reconcile route did not persist Release observation: status=%d body=%s", status, response)
	}
	item := view.Items[0]
	if item.Category != nudge.CategoryReleaseNotes || item.AttentionID == "" || len(item.Observations) != 1 {
		t.Fatalf("Release item identity=%+v", item)
	}
	slot := item.Observations[0]
	indexDigest := sha256.Sum256(snapshot.Bytes)
	if slot.SourceKey != key || slot.SourceRevision != 1 || !slot.Active || slot.ProducerWitness.Kind != "release-notes" ||
		slot.ProducerWitness.Environment != "dev" || slot.ProducerWitness.ProductVersion != runtime.ProductVersion ||
		slot.ProducerWitness.IndexRevision != snapshot.Generation || slot.ProducerWitness.IndexDigest != hex.EncodeToString(indexDigest[:]) ||
		slot.AttentionMaterialRevision != "version:"+runtime.ProductVersion || slot.Observation == nil ||
		slot.Observation.Actions[0].Target == nil || slot.Observation.Actions[0].Target.Kind != "release-notes" ||
		slot.Observation.Actions[0].Target.Mode != "published-version" || slot.Observation.Actions[0].Target.Version != runtime.ProductVersion {
		t.Fatalf("common persisted Release wrapper/witness/target mismatch: %+v", slot)
	}

	readsBeforeGet := store.readCount()
	status, response = releaseNotesRouteCall(t, router, http.MethodGet, "/api/v1/nudges", "")
	if status != http.StatusOK || store.readCount() != readsBeforeGet {
		t.Fatalf("persisted GET unexpectedly called source or failed: status=%d reads=%d before=%d body=%s", status, store.readCount(), readsBeforeGet, response)
	}
	status, response = releaseNotesRouteCall(t, router, http.MethodPatch, "/api/v1/nudges/release-notes/attention",
		fmt.Sprintf(`{"attention_id":%q,"action":"read"}`, item.AttentionID))
	if status != http.StatusNoContent {
		t.Fatalf("read accepted Release attention: status=%d body=%s", status, response)
	}
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	if status != http.StatusOK {
		t.Fatalf("idempotent Release reconciliation failed: status=%d body=%s", status, response)
	}
	stored, err := nudge.NewRepository(fsClient.Raw()).ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || stored.SourceRevision != 1 || !stored.Active {
		t.Fatalf("same-source replay changed active slot/revision: slot=%+v err=%v", stored, err)
	}
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).
		Collection("categories").Doc(nudge.CategoryReleaseNotes)
	category, err := categoryRef.Get(ctx)
	if err != nil || category.Data()["attention_id"] != item.AttentionID || category.Data()["attention_state"] != string(nudge.AttentionRead) {
		t.Fatalf("same-version replay replaced read attention: category=%v err=%v", category.Data(), err)
	}

	correctedStore, correctedSnapshot := releaseNotesIndexFixture(t, runtime, 124, true, "corrected copy")
	store.setIndex(correctedSnapshot, correctedStore.content)
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	if status != http.StatusOK {
		t.Fatalf("same-version correction reconciliation failed: status=%d body=%s", status, response)
	}
	stored, err = nudge.NewRepository(fsClient.Raw()).ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	category, categoryErr := categoryRef.Get(ctx)
	if err != nil || categoryErr != nil || stored.SourceRevision != 2 || !stored.Active ||
		stored.ProducerWitness.IndexDigest == slot.ProducerWitness.IndexDigest ||
		stored.AttentionMaterialRevision != "version:"+runtime.ProductVersion || category.Data()["attention_id"] != item.AttentionID ||
		category.Data()["attention_state"] != string(nudge.AttentionRead) {
		t.Fatalf("same-version correction lost material identity/read state: slot=%+v initial_attention=%q current_attention=%v current_state=%v category=%v err=%v/%v",
			stored, item.AttentionID, category.Data()["attention_id"], category.Data()["attention_state"], category.Data(), err, categoryErr)
	}

	missingStore, missingSnapshot := releaseNotesIndexFixture(t, runtime, 125, false, "")
	store.setIndex(missingSnapshot, missingStore.content)
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	if status != http.StatusOK || !strings.Contains(response, `"state":"missing"`) {
		t.Fatalf("authoritative current-version absence not reported: status=%d body=%s", status, response)
	}
	stored, err = nudge.NewRepository(fsClient.Raw()).ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || stored.SourceRevision != 3 || stored.Active || stored.ProducerWitness.IndexRevision != missingSnapshot.Generation {
		t.Fatalf("current missing did not persist exact common clear tombstone: slot=%+v err=%v", stored, err)
	}

	store.setIndexError(errors.New("test-only source outage"))
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	if status != http.StatusOK || !strings.Contains(response, `"state":"stale"`) {
		t.Fatalf("index outage did not return stale partial state: status=%d body=%s", status, response)
	}
	afterOutage, err := nudge.NewRepository(fsClient.Raw()).ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	categoryAfterOutage, categoryErr := categoryRef.Get(ctx)
	if err != nil || categoryErr != nil || afterOutage.SourceRevision != stored.SourceRevision || afterOutage.Active ||
		categoryAfterOutage.Data()["attention_id"] != category.Data()["attention_id"] ||
		categoryAfterOutage.Data()["attention_state"] != category.Data()["attention_state"] {
		t.Fatalf("outage overwrote clear/read state: slot=%+v category=%v errors=%v/%v", afterOutage, categoryAfterOutage.Data(), err, categoryErr)
	}
}

func TestReleaseNotesNudgeOutageReturnsTransientStaleNoticeForActiveItem(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	runtime := releaseNotesRuntime("1.0.0", "llm-wiki-bff-dev-00041-xyz")
	store, snapshot := releaseNotesIndexFixture(t, runtime, 123, true, "original")
	ownerID := fmt.Sprintf("release-notes-stale-wire-%d", time.Now().UnixNano())
	handler := &Handler{firestore: fsClient}
	handler.SetReleaseNotesSource(store, runtime)
	router := newReleaseNotesRoute(t, handler, ownerID)
	key := "release-notes:llm-wiki-cloud:dev"

	status, response := releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var initial nudge.View
	if status != http.StatusOK || json.Unmarshal([]byte(response), &initial) != nil || initial.Partial || len(initial.Items) != 1 {
		t.Fatalf("current Release source did not create one visible item: status=%d body=%s", status, response)
	}
	item := initial.Items[0]
	if item.Category != nudge.CategoryReleaseNotes || item.AttentionID == "" || len(item.Observations) != 1 {
		t.Fatalf("current Release item identity=%+v", item)
	}
	repository := nudge.NewRepository(fsClient.Raw())
	before, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || !before.Active || before.SourceRevision != 1 || before.Observation == nil {
		t.Fatalf("active Release source slot missing before outage: slot=%+v err=%v", before, err)
	}
	categoryRef := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).
		Collection("categories").Doc(nudge.CategoryReleaseNotes)
	status, response = releaseNotesRouteCall(t, router, http.MethodPatch, "/api/v1/nudges/release-notes/attention",
		fmt.Sprintf(`{"attention_id":%q,"action":"read"}`, item.AttentionID))
	categoryBefore, categoryErr := categoryRef.Get(ctx)
	if status != http.StatusNoContent || categoryErr != nil || categoryBefore.Data()["attention_id"] != item.AttentionID ||
		categoryBefore.Data()["attention_state"] != string(nudge.AttentionRead) {
		t.Fatalf("could not mark the active Release attention read before outage: status=%d category=%v err=%v body=%s",
			status, categoryBefore.Data(), categoryErr, response)
	}

	store.setIndexError(errors.New("test-only source outage"))
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var outage nudge.View
	if status != http.StatusOK || json.Unmarshal([]byte(response), &outage) != nil || !outage.Partial || len(outage.Items) != 1 ||
		outage.Items[0].AttentionID != item.AttentionID || outage.Items[0].AttentionState != nudge.AttentionRead || len(outage.Items[0].Observations) != 1 {
		t.Fatalf("matching current Release outage did not retain its visible read item and partial state: status=%d body=%s", status, response)
	}
	staleNotice := false
	for _, notice := range outage.Notices {
		if notice.SourceKey == key && notice.State == "stale" {
			staleNotice = true
		}
	}
	if !staleNotice {
		t.Fatalf("handler wire omitted the stale notice for the retained Release source: %+v", outage)
	}
	afterOutage, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	categoryAfter, categoryErr := categoryRef.Get(ctx)
	if err != nil || categoryErr != nil || !afterOutage.Active || afterOutage.SourceRevision != before.SourceRevision ||
		afterOutage.PayloadDigest != before.PayloadDigest || afterOutage.AttentionMaterialRevision != before.AttentionMaterialRevision ||
		afterOutage.ProducerWitness != before.ProducerWitness || afterOutage.Observation == nil ||
		categoryAfter.Data()["attention_id"] != item.AttentionID || categoryAfter.Data()["attention_state"] != string(nudge.AttentionRead) {
		t.Fatalf("outage rewrote the Release source or read attention: before=%+v after=%+v category=%v errors=%v/%v",
			before, afterOutage, categoryAfter.Data(), err, categoryErr)
	}

	readsBeforeGet := store.readCount()
	status, response = releaseNotesRouteCall(t, router, http.MethodGet, "/api/v1/nudges", "")
	var persistedGet nudge.View
	if status != http.StatusOK || store.readCount() != readsBeforeGet || json.Unmarshal([]byte(response), &persistedGet) != nil ||
		persistedGet.Partial || len(persistedGet.Items) != 1 || persistedGet.Items[0].AttentionID != item.AttentionID ||
		persistedGet.Items[0].AttentionState != nudge.AttentionRead {
		t.Fatalf("persisted GET did not remain source-read-only with the retained item: status=%d reads=%d before=%d body=%s",
			status, store.readCount(), readsBeforeGet, response)
	}

	store.setIndex(snapshot, store.content)
	status, response = releaseNotesRouteCall(t, router, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var recovered nudge.View
	if status != http.StatusOK || json.Unmarshal([]byte(response), &recovered) != nil || recovered.Partial || len(recovered.Items) != 1 ||
		recovered.Items[0].AttentionID != item.AttentionID || recovered.Items[0].AttentionState != nudge.AttentionRead {
		t.Fatalf("successful source recovery did not clear partial state and retain attention: status=%d body=%s", status, response)
	}
	for _, notice := range recovered.Notices {
		if notice.SourceKey == key && notice.State == "stale" {
			t.Fatalf("recovered source still emitted stale notice: %+v", recovered.Notices)
		}
	}
}

func TestReleaseNotesFixedOldRuntimeCannotReplaceNewerCommonSlot(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()

	runtimeV := releaseNotesRuntime("2026.11.4", "llm-wiki-bff-dev-00041-xyz")
	runtimeW := releaseNotesRuntime("2026.11.5", "llm-wiki-bff-dev-00042-xyz")
	oldStore, oldSnapshot := releaseNotesIndexFixture(t, runtimeV, 80, true, "old")
	newStore, newSnapshot := releaseNotesIndexFixture(t, runtimeW, 81, true, "new")
	ownerID := fmt.Sprintf("release-notes-fixed-runtime-%d", time.Now().UnixNano())
	handlerW := &Handler{firestore: fsClient}
	handlerW.SetReleaseNotesSource(newStore, runtimeW)
	routerW := newReleaseNotesRoute(t, handlerW, ownerID)
	status, response := releaseNotesRouteCall(t, routerW, http.MethodPost, "/api/v1/nudges/reconcile", "")
	if status != http.StatusOK || !strings.Contains(response, `"product_version":"2026.11.5"`) {
		t.Fatalf("new runtime did not apply its current source: status=%d body=%s", status, response)
	}
	key := "release-notes:llm-wiki-cloud:dev"
	repository := nudge.NewRepository(fsClient.Raw())
	before, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || !before.Active || before.ProducerWitness.ProductVersion != runtimeW.ProductVersion || before.SourceRevision != 1 {
		t.Fatalf("new current slot missing before stale checks: slot=%+v err=%v", before, err)
	}

	// A fixed V process whose first read sees the latest W index cannot claim V.
	handlerV := &Handler{firestore: fsClient}
	handlerV.SetReleaseNotesSource(newStore, runtimeV)
	routerV := newReleaseNotesRoute(t, handlerV, ownerID)
	status, response = releaseNotesRouteCall(t, routerV, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var firstCaptureV nudge.View
	if status != http.StatusOK || !strings.Contains(response, "release-transition-unavailable") ||
		json.Unmarshal([]byte(response), &firstCaptureV) != nil || len(firstCaptureV.Items) != 0 {
		t.Fatalf("fixed old runtime did not fail closed on latest W witness: status=%d body=%s", status, response)
	}
	afterLatest, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || afterLatest.SourceRevision != before.SourceRevision || afterLatest.ProducerWitness != before.ProducerWitness {
		t.Fatalf("old first-capture runtime replaced current common slot: before=%+v after=%+v err=%v", before, afterLatest, err)
	}

	unknownRuntime := releaseNotesRuntime("", "llm-wiki-bff-dev-00043-xyz")
	handlerUnknown := &Handler{firestore: fsClient}
	handlerUnknown.SetReleaseNotesSource(newStore, unknownRuntime)
	unknownRouter := newReleaseNotesRoute(t, handlerUnknown, ownerID)
	status, response = releaseNotesRouteCall(t, unknownRouter, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var unknownView nudge.View
	if status != http.StatusOK || json.Unmarshal([]byte(response), &unknownView) != nil || len(unknownView.Items) != 0 {
		t.Fatalf("unknown runtime version exposed the persisted W Release item: status=%d body=%s", status, response)
	}

	// A previously cached V index also fails the common monotonic witness fence,
	// even after capturing a fresh common source revision for both attempts.
	handlerCachedV := &Handler{firestore: fsClient}
	handlerCachedV.SetReleaseNotesSource(oldStore, runtimeV)
	cachedRouter := newReleaseNotesRoute(t, handlerCachedV, ownerID)
	status, response = releaseNotesRouteCall(t, cachedRouter, http.MethodPost, "/api/v1/nudges/reconcile", "")
	var cachedView nudge.View
	if status != http.StatusOK || !strings.Contains(response, "release_source_revision_conflict") ||
		json.Unmarshal([]byte(response), &cachedView) != nil || len(cachedView.Items) != 0 {
		t.Fatalf("stale old runtime did not report common witness conflict: status=%d body=%s", status, response)
	}
	afterCached, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryReleaseNotes, key)
	if err != nil || afterCached.SourceRevision != before.SourceRevision || afterCached.ProducerWitness != before.ProducerWitness ||
		afterCached.ProducerWitness.IndexRevision != newSnapshot.Generation || oldSnapshot.Generation >= newSnapshot.Generation {
		t.Fatalf("stale cached V overwrote newer W slot: before=%+v after=%+v old=%d new=%d err=%v", before, afterCached, oldSnapshot.Generation, newSnapshot.Generation, err)
	}
}
