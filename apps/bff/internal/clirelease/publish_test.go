package clirelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memoryObject struct {
	data       []byte
	generation int64
}

type memoryStore struct {
	objects        map[string]memoryObject
	nextGeneration int64
	failNextUpdate bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: map[string]memoryObject{}, nextGeneration: 1}
}

func (store *memoryStore) Read(_ context.Context, key string) ([]byte, int64, error) {
	object, ok := store.objects[key]
	if !ok {
		return nil, 0, ErrObjectNotFound
	}
	return append([]byte(nil), object.data...), object.generation, nil
}

func (store *memoryStore) Create(_ context.Context, key string, data []byte, _, _ string) (int64, error) {
	if _, ok := store.objects[key]; ok {
		return 0, errors.New("precondition failed")
	}
	return store.write(key, data), nil
}

func (store *memoryStore) Update(_ context.Context, key string, data []byte, generation int64, _, _ string) (int64, error) {
	current, ok := store.objects[key]
	if !ok || current.generation != generation {
		return 0, errors.New("generation mismatch")
	}
	if store.failNextUpdate {
		store.failNextUpdate = false
		store.write(key, []byte("concurrent writer"))
		return 0, errors.New("generation conflict")
	}
	return store.write(key, data), nil
}

func (store *memoryStore) write(key string, data []byte) int64 {
	generation := store.nextGeneration
	store.nextGeneration++
	store.objects[key] = memoryObject{data: append([]byte(nil), data...), generation: generation}
	return generation
}

func TestPublishCreatesVerifiedStableLatestAndRetriesIdentically(t *testing.T) {
	store := newMemoryStore()
	manifest, releaseDir := testRelease(t, "1.2.3", "stable")
	verify := func(_ context.Context, url string, _ int64, _ string) error {
		if !strings.HasPrefix(url, "https://downloads.example.test/cli/versions/1.2.3/") {
			t.Fatalf("unexpected download URL %q", url)
		}
		return nil
	}
	if err := Publish(context.Background(), store, manifest, releaseDir, "https://downloads.example.test", verify); err != nil {
		t.Fatal(err)
	}
	latest := store.objects["cli/stable/latest.json"]
	var decoded Manifest
	if err := json.Unmarshal(latest.data, &decoded); err != nil || decoded.Version != "1.2.3" {
		t.Fatalf("latest = %s, %v", latest.data, err)
	}
	objectCount := len(store.objects)
	if err := Publish(context.Background(), store, manifest, releaseDir, "https://downloads.example.test", verify); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if len(store.objects) != objectCount || store.objects["cli/stable/latest.json"].generation != latest.generation {
		t.Fatal("identical retry changed release objects or latest generation")
	}
}

func TestPublishPartialFailureNeverChangesLatest(t *testing.T) {
	store := newMemoryStore()
	manifest, releaseDir := testRelease(t, "1.2.3", "stable")
	verify := func(_ context.Context, url string, _ int64, _ string) error {
		if strings.Contains(url, "linux_amd64") {
			return errors.New("public endpoint unavailable")
		}
		return nil
	}
	if err := Publish(context.Background(), store, manifest, releaseDir, "https://downloads.example.test/cli", verify); err == nil {
		t.Fatal("expected unavailable asset failure")
	}
	if _, ok := store.objects["cli/stable/latest.json"]; ok {
		t.Fatal("partial release changed stable latest")
	}
	if _, ok := store.objects["cli/versions/1.2.3/manifest.json"]; ok {
		t.Fatal("incomplete public release wrote its version manifest")
	}
}

func TestPublishRejectsDifferentBytesForExistingVersionObject(t *testing.T) {
	store := newMemoryStore()
	manifest, releaseDir := testRelease(t, "1.2.3", "stable")
	store.write("cli/versions/1.2.3/"+manifest.Assets[0].AssetName, []byte("other bytes"))
	err := Publish(context.Background(), store, manifest, releaseDir, "https://downloads.example.test", func(context.Context, string, int64, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "different bytes") {
		t.Fatalf("expected immutable object conflict, got %v", err)
	}
	if _, ok := store.objects["cli/stable/latest.json"]; ok {
		t.Fatal("conflicting version changed latest")
	}
}

func TestPublishRejectsChecksumsThatDifferFromManifestBeforeWriting(t *testing.T) {
	store := newMemoryStore()
	manifest, releaseDir := testRelease(t, "1.2.3", "stable")
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte("incorrect\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Publish(context.Background(), store, manifest, releaseDir, "https://downloads.example.test", func(context.Context, string, int64, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "checksums do not match") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if len(store.objects) != 0 {
		t.Fatalf("checksum mismatch wrote release objects: %v", store.objects)
	}
}

func TestPublishKeepsNewerLatestAndPrereleaseOutOfStable(t *testing.T) {
	store := newMemoryStore()
	newer, newerDir := testRelease(t, "2.0.0", "stable")
	verify := func(context.Context, string, int64, string) error { return nil }
	if err := Publish(context.Background(), store, newer, newerDir, "https://downloads.example.test", verify); err != nil {
		t.Fatal(err)
	}
	older, olderDir := testRelease(t, "1.9.0", "stable")
	if err := Publish(context.Background(), store, older, olderDir, "https://downloads.example.test", verify); err == nil {
		t.Fatal("older stable version unexpectedly published")
	}
	prerelease, prereleaseDir := testRelease(t, "2.1.0-rc.1", "prerelease")
	latestBefore := append([]byte(nil), store.objects["cli/stable/latest.json"].data...)
	if err := Publish(context.Background(), store, prerelease, prereleaseDir, "https://downloads.example.test", verify); err != nil {
		t.Fatal(err)
	}
	if string(store.objects["cli/stable/latest.json"].data) != string(latestBefore) {
		t.Fatal("prerelease changed stable latest")
	}
}

func TestVerifyPublicAssetRequiresDevWikiCORSAndMatchingBytes(t *testing.T) {
	data := []byte("release archive")
	digest := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != "https://wiki.dev.rayer.idv.tw" {
			t.Errorf("Origin = %q", request.Header.Get("Origin"))
		}
		writer.Header().Set("Access-Control-Allow-Origin", "https://wiki.dev.rayer.idv.tw")
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	if err := VerifyPublicAsset(context.Background(), server.URL, int64(len(data)), hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("verified CORS-enabled archive: %v", err)
	}

	missingCORS := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(data)
	}))
	defer missingCORS.Close()
	if err := VerifyPublicAsset(context.Background(), missingCORS.URL, int64(len(data)), hex.EncodeToString(digest[:])); err == nil || !strings.Contains(err.Error(), "DEV wiki origin") {
		t.Fatalf("missing CORS header error = %v", err)
	}
}

func TestPublishGenerationConflictDoesNotOverwriteConcurrentLatest(t *testing.T) {
	store := newMemoryStore()
	older, olderDir := testRelease(t, "1.0.0", "stable")
	verify := func(context.Context, string, int64, string) error { return nil }
	if err := Publish(context.Background(), store, older, olderDir, "https://downloads.example.test", verify); err != nil {
		t.Fatal(err)
	}
	newer, newerDir := testRelease(t, "1.1.0", "stable")
	store.failNextUpdate = true
	if err := Publish(context.Background(), store, newer, newerDir, "https://downloads.example.test", verify); err == nil || !strings.Contains(err.Error(), "generation-conditional") {
		t.Fatalf("expected generation conflict, got %v", err)
	}
	if got := string(store.objects["cli/stable/latest.json"].data); got != "concurrent writer" {
		t.Fatalf("concurrent latest was overwritten: %q", got)
	}
}

func testRelease(t *testing.T, version, channel string) (Manifest, string) {
	t.Helper()
	releaseDir := t.TempDir()
	manifest := Manifest{SchemaVersion: 1, Version: version, SourceCommit: "0123456789abcdef0123456789abcdef01234567", Channel: channel}
	for _, target := range []struct{ os, arch string }{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}} {
		extension := ".tar.gz"
		if target.os == "windows" {
			extension = ".zip"
		}
		name := fmt.Sprintf("lwc-sync_%s_%s_%s%s", version, target.os, target.arch, extension)
		data := []byte(name)
		if err := os.WriteFile(filepath.Join(releaseDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		manifest.Assets = append(manifest.Assets, Asset{OS: target.os, Arch: target.arch, AssetName: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	}
	checksumLines := make([]string, 0, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		checksumLines = append(checksumLines, fmt.Sprintf("%s  %s", asset.SHA256, asset.AssetName))
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(strings.Join(checksumLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest, releaseDir
}
