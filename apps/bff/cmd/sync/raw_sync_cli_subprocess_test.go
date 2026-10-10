package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	handlerV1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

type rawSyncProcessFixture struct {
	mu                    sync.Mutex
	files                 map[string]store.RawSyncFile
	data                  map[string][]byte
	nextGeneration        int64
	force401              bool
	mode                  string
	loseCreateResponse    bool
	mutateLocalPath       string
	conflictPath          string
	conflictData          []byte
	dropUploadPath        string
	failListAfterDropPath string
	failNextListRead      bool
	readbackListFailures  int
	mutateAfterCommitPath string
	mutateAfterCommitFile string
	mutateAfterCommitData []byte
	mutateAfterCommitErr  string
	createLocalPath       string
	createLocalData       []byte
	truncateDownloadPath  string
	moveLocalParent       string
	moveParentTarget      string
	moveParentError       string
	rejectUploadPath      string
	dropBeforeCommitPath  string
	rotateOnLocator       bool
	rotateStore           *localAuthStore
	rotatedCredentials    cliLocalCredentials
	bindingCreates        int
	refreshes             int
	dataRequests          int
	uploadAttempts        int
	redirects             int
	bindings              []authSyncBinding
}

func TestRawSyncCLIRealSubprocessInitPushAndPushSync(t *testing.T) {
	fixture := &rawSyncProcessFixture{files: make(map[string]store.RawSyncFile), data: make(map[string][]byte), nextGeneration: 500, force401: true, loseCreateResponse: true}
	fixture.seed("remote-only/attachments/photo.bin", []byte("cloud bytes"))
	var redirectHits atomic.Int32
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
	defer redirectServer.Close()
	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serveData(w, r, redirectServer.URL)
	}))
	defer dataServer.Close()
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serveAuth(w, r, dataServer.URL)
	}))
	defer authServer.Close()

	root := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config")
	storeDir := filepath.Join(configRoot, localAuthDirectoryName)
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := cliLocalCredentials{
		AuthHost: authServer.URL, AccessToken: "access-token-1", RefreshToken: "refresh-token-1",
		SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1,
	}
	writeTestJSON(t, filepath.Join(storeDir, localConfigFileName), cliLocalConfig{AuthHost: authServer.URL})
	writeTestJSON(t, filepath.Join(storeDir, localCredentialsName), credentials)
	if err := os.MkdirAll(filepath.Join(root, "raw", "nested", "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "raw", "nested", "attachments", "note.txt"), []byte("local bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wiki", "must-stay-local.md"), []byte("private wiki file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("outside raw"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "lwc-sync")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	run := func(args ...string) (string, error) {
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot, "LWC_SYNC_AUTH_HOST="+authServer.URL)
		output, err := command.CombinedOutput()
		return string(output), err
	}

	if out, err := run("init", "--vault", root, "--host", authServer.URL, "--project-id", "project-1"); err == nil {
		t.Fatalf("init unexpectedly accepted a lost create response: %s", out)
	}
	if out, err := run("init", "--vault", root, "--host", authServer.URL, "--project-id", "project-1"); err != nil {
		t.Fatalf("init did not recover the existing server binding: %v\n%s", err, out)
	}
	if out, err := run("init", "--vault", root, "--host", authServer.URL, "--project-id", "project-1"); err != nil {
		t.Fatalf("idempotent init failed: %v\n%s", err, out)
	}
	emptyRoot := filepath.Join(t.TempDir(), "not-yet-created", "vault")
	if out, err := run("init", "--vault", emptyRoot, "--host", authServer.URL, "--project-id", "project-2"); err != nil {
		t.Fatalf("init did not create an empty vault: %v\n%s", err, out)
	}
	if info, err := os.Stat(filepath.Join(emptyRoot, "raw")); err != nil || !info.IsDir() {
		t.Fatalf("empty vault raw directory info=%v err=%v", info, err)
	}
	legacyBindRoot := t.TempDir()
	if out, err := run("bind", "--vault", legacyBindRoot, "--project-id", "project-3"); err != nil {
		t.Fatalf("existing bind command compatibility failed: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	if fixture.bindingCreates != 3 {
		t.Fatalf("binding creates=%d, want 3 after recovery, init and existing bind", fixture.bindingCreates)
	}
	fixture.mu.Unlock()

	if out, err := run("push", "--vault", root); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	if fixture.refreshes != 1 {
		t.Fatalf("refreshes=%d, want one retry after HTTP 401", fixture.refreshes)
	}
	if _, ok := fixture.files["nested/attachments/note.txt"]; !ok {
		t.Fatal("nested raw file was not uploaded")
	}
	if _, ok := fixture.files["outside.txt"]; ok {
		t.Fatal("file outside raw/ was uploaded")
	}
	fixture.mu.Unlock()
	rotated, err := os.ReadFile(filepath.Join(storeDir, localCredentialsName))
	if err != nil || !strings.Contains(string(rotated), "access-token-2") {
		t.Fatalf("rotated credential was not saved: err=%v", err)
	}
	longRotationPath := filepath.Join(root, "raw", "long-transfer-rotation.bin")
	if err := os.WriteFile(longRotationPath, []byte("current shared token wins"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.force401 = true
	fixture.rotateOnLocator = true
	fixture.rotateStore = newLocalAuthStoreAt(storeDir)
	fixture.rotatedCredentials = cliLocalCredentials{
		AuthHost: authServer.URL, AccessToken: testCLIAccessToken(t, time.Now().Add(10*time.Minute)), RefreshToken: "refresh-token-3",
		SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1,
	}
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err != nil {
		t.Fatalf("push did not adopt another process rotation after a stale 401: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	if fixture.refreshes != 1 || string(fixture.data["long-transfer-rotation.bin"]) != "current shared token wins" {
		fixture.mu.Unlock()
		t.Fatal("push did not use current shared credentials without replaying the stale refresh token")
	}
	fixture.mu.Unlock()
	rotated, err = os.ReadFile(filepath.Join(storeDir, localCredentialsName))
	if err != nil || !strings.Contains(string(rotated), fixture.rotatedCredentials.AccessToken) {
		t.Fatalf("concurrent credential rotation was lost: err=%v", err)
	}

	if out, err := run("push", "--vault", root, "--sync"); err != nil {
		t.Fatalf("push --sync failed: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(root, "raw", "remote-only", "attachments", "photo.bin")); err != nil || string(data) != "cloud bytes" {
		t.Fatalf("remote-only file was not downloaded: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "wiki", "must-stay-local.md")); err != nil || string(data) != "private wiki file" {
		t.Fatalf("wiki file changed: data=%q err=%v", data, err)
	}

	fixture.seed("race-download.bin", []byte("remote during local create"))
	fixture.mu.Lock()
	fixture.createLocalPath = filepath.Join(root, "raw", "race-download.bin")
	fixture.createLocalData = []byte("local appeared during download")
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root, "--sync"); err == nil {
		t.Fatalf("push --sync overwrote a local path created during download: %s", out)
	}
	if data, err := os.ReadFile(filepath.Join(root, "raw", "race-download.bin")); err != nil || string(data) != "local appeared during download" {
		t.Fatalf("local concurrent file was not preserved: data=%q err=%v", data, err)
	}
	fixture.seed("corrupt-download.bin", []byte("verified bytes required"))
	fixture.mu.Lock()
	fixture.truncateDownloadPath = "corrupt-download.bin"
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root, "--sync"); err == nil {
		t.Fatalf("push --sync accepted an incomplete download: %s", out)
	}
	if _, err := os.Lstat(filepath.Join(root, "raw", "corrupt-download.bin")); !os.IsNotExist(err) {
		t.Fatalf("incomplete download published a local file: err=%v", err)
	}

	localPath := filepath.Join(root, "raw", "nested", "attachments", "note.txt")
	if err := os.WriteFile(localPath, []byte("local update"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run("push", "--vault", root); err != nil {
		t.Fatalf("update push failed: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	updated := string(fixture.data["nested/attachments/note.txt"])
	fixture.mu.Unlock()
	if updated != "local update" {
		t.Fatalf("same-path local update=%q, want local update", updated)
	}

	changedAfterInventory := filepath.Join(root, "raw", "changed-after-inventory.bin")
	if err := os.WriteFile(changedAfterInventory, []byte("listed content"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.mutateLocalPath = changedAfterInventory
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err == nil {
		t.Fatalf("push accepted a local source changed after inventory: %s", out)
	}
	fixture.mu.Lock()
	_, uploadedAfterChange := fixture.files["changed-after-inventory.bin"]
	fixture.mu.Unlock()
	if uploadedAfterChange {
		t.Fatal("source changed after inventory was uploaded")
	}
	if err := os.Remove(changedAfterInventory); err != nil {
		t.Fatal(err)
	}

	concurrentPath := filepath.Join(root, "raw", "concurrent.bin")
	if err := os.WriteFile(concurrentPath, []byte("local concurrent write"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.conflictPath = "concurrent.bin"
	fixture.conflictData = []byte("remote concurrent write")
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err == nil {
		t.Fatalf("push overwrote a remote generation changed after inventory: %s", out)
	}
	fixture.mu.Lock()
	concurrentRemote := string(fixture.data["concurrent.bin"])
	fixture.mu.Unlock()
	if concurrentRemote != "remote concurrent write" {
		t.Fatalf("concurrent remote version=%q, want remote concurrent write", concurrentRemote)
	}
	if err := os.Remove(concurrentPath); err != nil {
		t.Fatal(err)
	}

	lostResponsePath := filepath.Join(root, "raw", "lost-response.bin")
	if err := os.WriteFile(lostResponsePath, []byte("server wrote before response loss"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.dropUploadPath = "lost-response.bin"
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err != nil {
		t.Fatalf("push did not reconcile a lost upload response by digest: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	lostResponseStored := string(fixture.data["lost-response.bin"])
	fixture.mu.Unlock()
	if lostResponseStored != "server wrote before response loss" {
		t.Fatalf("lost response upload stored=%q", lostResponseStored)
	}

	outside := filepath.Join(root, "outside-link-target.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "raw", "outside-link")
	if err := os.Symlink(outside, link); err == nil {
		if out, err := run("push", "--vault", root); err == nil {
			t.Fatalf("push accepted a symlink in raw/: %s", out)
		}
		_ = os.Remove(link)
	}

	parent := filepath.Join(root, "raw", "nested")
	movedParent := filepath.Join(root, "moved-raw-parent")
	fixture.mu.Lock()
	fixture.moveLocalParent = parent
	fixture.moveParentTarget = movedParent
	uploadsBeforeMovedParent := fixture.uploadAttempts
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err == nil {
		t.Fatalf("push accepted a raw parent replaced by an outside symlink: %s", out)
	}
	fixture.mu.Lock()
	moveParentError := fixture.moveParentError
	uploadsAfterMovedParent := fixture.uploadAttempts
	fixture.mu.Unlock()
	if moveParentError != "" {
		t.Fatalf("could not replace raw parent during inventory: %s", moveParentError)
	}
	if uploadsAfterMovedParent != uploadsBeforeMovedParent {
		t.Fatalf("raw parent replacement sent %d upload request(s)", uploadsAfterMovedParent-uploadsBeforeMovedParent)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(movedParent, parent); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	if out, err := run("push", "--vault", root); err != nil {
		t.Fatalf("push after local delete failed: %v\n%s", err, out)
	}
	fixture.mu.Lock()
	_, retained := fixture.files["nested/attachments/note.txt"]
	fixture.mu.Unlock()
	if !retained {
		t.Fatal("push propagated local deletion to remote")
	}
	if out, err := run("push", "--vault", root, "--sync"); err != nil {
		t.Fatalf("push --sync after local delete failed: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(localPath); err != nil || string(data) != "local update" {
		t.Fatalf("push --sync did not restore retained remote file: data=%q err=%v", data, err)
	}

	fixture.mu.Lock()
	fixture.mode = "forbidden"
	refreshesBefore403 := fixture.refreshes
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err == nil {
		t.Fatalf("push accepted HTTP 403: %s", out)
	}
	fixture.mu.Lock()
	if fixture.refreshes != refreshesBefore403 {
		t.Fatal("HTTP 403 triggered a credential refresh")
	}
	fixture.mode = "redirect"
	fixture.mu.Unlock()
	if out, err := run("push", "--vault", root); err == nil {
		t.Fatalf("push followed a cross-origin redirect: %s", out)
	}
	if redirectHits.Load() != 0 {
		t.Fatalf("cross-origin redirect target received %d requests", redirectHits.Load())
	}
}

func TestRawSyncFailedShrinksDoNotFundGrowth(t *testing.T) {
	fixture := &rawSyncProcessFixture{files: make(map[string]store.RawSyncFile), data: make(map[string][]byte), nextGeneration: 1000}
	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serveData(w, r, "")
	}))
	defer dataServer.Close()
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serveAuth(w, r, dataServer.URL)
	}))
	defer authServer.Close()

	root := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config")
	storeDir := filepath.Join(configRoot, localAuthDirectoryName)
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := cliLocalCredentials{AuthHost: authServer.URL, AccessToken: "access-token-1", RefreshToken: "refresh-token-1", SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1}
	writeTestJSON(t, filepath.Join(storeDir, localConfigFileName), cliLocalConfig{AuthHost: authServer.URL})
	writeTestJSON(t, filepath.Join(storeDir, localCredentialsName), credentials)
	binary := filepath.Join(t.TempDir(), "lwc-sync")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	run := func(args ...string) (string, error) {
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot, "LWC_SYNC_AUTH_HOST="+authServer.URL)
		output, err := command.CombinedOutput()
		return string(output), err
	}
	if out, err := run("init", "--vault", root, "--host", authServer.URL, "--project-id", "project-1"); err != nil {
		t.Fatalf("init fixture vault: %v\n%s", err, out)
	}

	for _, test := range []struct {
		name                    string
		configure               func(string)
		wantUploadAttempts      int
		wantAdded               int
		wantUpdated             int
		wantCapacityFailure     bool
		wantFailureReason       string
		wantSourceFailureReason string
		wantSuccess             bool
	}{
		{name: "source verification", configure: func(shrinkPath string) { fixture.mutateLocalPath = shrinkPath }, wantCapacityFailure: true, wantSourceFailureReason: "local source changed after inventory"},
		{name: "generation CAS", configure: func(string) { fixture.rejectUploadPath = "remote-000.bin" }, wantUploadAttempts: 1, wantCapacityFailure: true},
		{name: "transfer before commit", configure: func(string) { fixture.dropBeforeCommitPath = "remote-000.bin" }, wantUploadAttempts: 1, wantFailureReason: "ambiguous upload response; readback did not confirm the write and remaining writes stopped"},
		{name: "successful shrink funds growth", configure: func(string) {}, wantUploadAttempts: 2, wantAdded: 1, wantUpdated: 1, wantSuccess: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rawRoot := filepath.Join(root, "raw")
			if err := os.RemoveAll(rawRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(rawRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			shrinkPath := filepath.Join(rawRoot, "remote-000.bin")
			if err := os.WriteFile(shrinkPath, []byte("s"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rawRoot, "z-new.bin"), []byte("g"), 0o644); err != nil {
				t.Fatal(err)
			}

			fixture.mu.Lock()
			fixture.files = make(map[string]store.RawSyncFile)
			fixture.data = make(map[string][]byte)
			fixture.nextGeneration = 1000
			fixture.uploadAttempts = 0
			fixture.rejectUploadPath = ""
			fixture.dropBeforeCommitPath = ""
			fixture.mutateLocalPath = ""
			for i := range 52 {
				name := fmt.Sprintf("remote-%03d.bin", i)
				size := int64(store.MaxRawSyncFileBytes)
				if i == 51 {
					size = 2 << 20
				}
				digest := sha256.Sum256([]byte(name))
				fixture.nextGeneration++
				fixture.files[name] = store.RawSyncFile{Path: name, Size: size, SHA256: hex.EncodeToString(digest[:]), Generation: strconv.FormatInt(fixture.nextGeneration, 10)}
			}
			originalFiles := make(map[string]store.RawSyncFile, len(fixture.files))
			for path, file := range fixture.files {
				originalFiles[path] = file
			}
			test.configure(shrinkPath)
			fixture.mu.Unlock()

			out, err := run("push", "--vault", root)
			if test.wantSuccess && err != nil {
				t.Fatalf("push failed after successful shrinking replacement: %v\n%s", err, out)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("push accepted a growth after failed shrink: %s", out)
			}
			wantSummary := fmt.Sprintf("Added: %d  Updated: %d", test.wantAdded, test.wantUpdated)
			if !strings.Contains(out, wantSummary) {
				t.Fatalf("result summary=%q missing from output: %s", wantSummary, out)
			}
			if test.wantSuccess && strings.Contains(out, "raw tree capacity remains occupied") {
				t.Fatalf("successful shrink did not fund later growth: %s", out)
			}
			if test.wantCapacityFailure && !strings.Contains(out, "z-new.bin: raw tree capacity remains occupied") {
				t.Fatalf("failed shrink did not preserve occupied capacity: %s", out)
			}
			if test.wantFailureReason != "" && !strings.Contains(out, test.wantFailureReason) {
				t.Fatalf("expected conservative ambiguous-transfer failure %q: %s", test.wantFailureReason, out)
			}
			if test.wantSourceFailureReason != "" && !strings.Contains(out, test.wantSourceFailureReason) {
				t.Fatalf("source failure reason missing from result: %s", out)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.uploadAttempts != test.wantUploadAttempts {
				t.Fatalf("upload attempts=%d, want %d (shrink only; growth must not be sent): %s", fixture.uploadAttempts, test.wantUploadAttempts, out)
			}
			wantFileCount := len(originalFiles)
			wantBytes := store.MaxRawSyncTotalBytes
			if test.wantSuccess {
				wantFileCount++
				wantBytes = store.MaxRawSyncTotalBytes - store.MaxRawSyncFileBytes + 2
			}
			if len(fixture.files) != wantFileCount {
				t.Fatalf("remote file count=%d, want %d", len(fixture.files), wantFileCount)
			}
			var total int64
			for path, original := range originalFiles {
				current, ok := fixture.files[path]
				if !ok || (!test.wantSuccess && current != original) {
					t.Fatalf("failed shrink changed remote object %q: before=%#v after=%#v present=%v", path, original, current, ok)
				}
				if test.wantSuccess && path == "remote-000.bin" && (current.Size != 1 || current.SHA256 == original.SHA256 || current.Generation == original.Generation) {
					t.Fatalf("successful shrink metadata=%#v, want one-byte replacement of %#v", current, original)
				}
				total += current.Size
			}
			if test.wantSuccess {
				added, ok := fixture.files["z-new.bin"]
				if !ok || added.Size != 1 {
					t.Fatalf("growth metadata=%#v present=%v, want one-byte new file", added, ok)
				}
				total += added.Size
			}
			if total != wantBytes {
				t.Fatalf("remote bytes=%d, want %d", total, wantBytes)
			}
			if !test.wantSuccess {
				if _, exists := fixture.files["z-new.bin"]; exists {
					t.Fatal("growth file was uploaded after shrink failed")
				}
			}
		})
	}

	for _, test := range []struct {
		name                   string
		replacement            bool
		mutateAfterCommit      bool
		dropResponse           bool
		failReadback           bool
		wantAdded              int
		wantUpdated            int
		wantReadbackFailures   int
		wantFailureDescription string
	}{
		{name: "new file post-verification failure", mutateAfterCommit: true, wantFailureDescription: "b-first.bin: local source changed during upload"},
		{name: "enlarged replacement post-verification failure", replacement: true, mutateAfterCommit: true, wantFailureDescription: "b-first.bin: local source changed during upload"},
		{name: "ambiguous response readback confirms commit", dropResponse: true, wantAdded: 1, wantFailureDescription: "c-second.bin: raw tree capacity remains occupied"},
		{name: "ambiguous response readback failure stops writes", dropResponse: true, failReadback: true, wantReadbackFailures: 1, wantFailureDescription: "ambiguous upload response; capacity was reserved and remaining writes stopped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rawRoot := filepath.Join(root, "raw")
			if err := os.RemoveAll(rawRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(rawRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			shrinkLocal := filepath.Join(rawRoot, "a-shrink.bin")
			firstLocal := filepath.Join(rawRoot, "b-first.bin")
			secondLocal := filepath.Join(rawRoot, "c-second.bin")
			if err := os.WriteFile(shrinkLocal, []byte("s"), 0o644); err != nil {
				t.Fatal(err)
			}
			firstBytes := []byte("g")
			if test.replacement {
				firstBytes = []byte("gg")
			}
			if err := os.WriteFile(firstLocal, firstBytes, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(secondLocal, []byte("h"), 0o644); err != nil {
				t.Fatal(err)
			}

			fixture.mu.Lock()
			fixture.files = make(map[string]store.RawSyncFile)
			fixture.data = make(map[string][]byte)
			fixture.nextGeneration = 2000
			fixture.uploadAttempts = 0
			fixture.rejectUploadPath = "a-shrink.bin"
			fixture.dropBeforeCommitPath = ""
			fixture.mutateLocalPath = ""
			fixture.dropUploadPath = ""
			fixture.failListAfterDropPath = ""
			fixture.failNextListRead = false
			fixture.readbackListFailures = 0
			fixture.mutateAfterCommitPath = ""
			fixture.mutateAfterCommitFile = ""
			fixture.mutateAfterCommitData = nil
			fixture.mutateAfterCommitErr = ""
			remoteGrowthPath := ""
			if test.replacement {
				remoteGrowthPath = "b-first.bin"
			}
			seedRawSyncCapacityLocked(t, fixture, store.MaxRawSyncTotalBytes-1, "a-shrink.bin", 410, remoteGrowthPath, 1)
			if test.mutateAfterCommit {
				fixture.mutateAfterCommitPath = "b-first.bin"
				fixture.mutateAfterCommitFile = firstLocal
				fixture.mutateAfterCommitData = []byte("changed after committed upload")
			}
			if test.dropResponse {
				fixture.dropUploadPath = "b-first.bin"
				if test.failReadback {
					fixture.failListAfterDropPath = "b-first.bin"
				}
			}
			initialCount := len(fixture.files)
			fixture.mu.Unlock()

			out, err := run("push", "--vault", root)
			if err == nil {
				t.Fatalf("push did not report the injected partial failure: %s", out)
			}
			wantSummary := fmt.Sprintf("Added: %d  Updated: %d", test.wantAdded, test.wantUpdated)
			if !strings.Contains(out, wantSummary) || !strings.Contains(out, test.wantFailureDescription) {
				t.Fatalf("summary or expected failure missing (%q, %q): %s", wantSummary, test.wantFailureDescription, out)
			}
			if !strings.Contains(out, "a-shrink.bin: Sync data service returned HTTP 409") {
				t.Fatalf("reviewer reachability case did not exercise the rejected shrink: %s", out)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.uploadAttempts != 2 {
				t.Fatalf("upload attempts=%d, want failed shrink plus committed first growth only: %s", fixture.uploadAttempts, out)
			}
			if fixture.readbackListFailures != test.wantReadbackFailures {
				t.Fatalf("readback failures=%d, want %d", fixture.readbackListFailures, test.wantReadbackFailures)
			}
			wantCount := initialCount
			if !test.replacement {
				wantCount++
			}
			if len(fixture.files) != wantCount {
				t.Fatalf("remote file count=%d, want %d", len(fixture.files), wantCount)
			}
			var total int64
			for _, remote := range fixture.files {
				total += remote.Size
			}
			if total != store.MaxRawSyncTotalBytes {
				t.Fatalf("committed remote bytes=%d, want exactly limit %d", total, store.MaxRawSyncTotalBytes)
			}
			firstRemote, ok := fixture.files["b-first.bin"]
			if !ok || firstRemote.Size != int64(len(firstBytes)) {
				t.Fatalf("first growth remote metadata=%#v present=%v", firstRemote, ok)
			}
			digest := sha256.Sum256(firstBytes)
			if firstRemote.SHA256 != hex.EncodeToString(digest[:]) || string(fixture.data["b-first.bin"]) != string(firstBytes) {
				t.Fatalf("first growth was not committed before verification/response loss: metadata=%#v data=%q", firstRemote, fixture.data["b-first.bin"])
			}
			if _, ok := fixture.files["c-second.bin"]; ok {
				t.Fatal("second growth was written after the first write occupied the remaining capacity")
			}
			if fixture.mutateAfterCommitErr != "" {
				t.Fatalf("could not mutate local source after remote commit: %s", fixture.mutateAfterCommitErr)
			}
		})
	}
}

func seedRawSyncCapacityLocked(t *testing.T, fixture *rawSyncProcessFixture, total int64, shrinkPath string, shrinkSize int64, growthPath string, growthSize int64) {
	t.Helper()
	add := func(path string, size int64) {
		fixture.nextGeneration++
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", path, size)))
		fixture.files[path] = store.RawSyncFile{Path: path, Size: size, SHA256: hex.EncodeToString(digest[:]), Generation: strconv.FormatInt(fixture.nextGeneration, 10)}
	}
	used := int64(0)
	add(shrinkPath, shrinkSize)
	used += shrinkSize
	if growthPath != "" {
		add(growthPath, growthSize)
		used += growthSize
	}
	for i := 0; used < total; i++ {
		remaining := total - used
		size := min(remaining, store.MaxRawSyncFileBytes)
		add(fmt.Sprintf("capacity-%03d.bin", i), size)
		used += size
	}
	if used != total {
		t.Fatalf("seeded remote size=%d, want %d", used, total)
	}
	if len(fixture.files) > store.MaxRawSyncFiles {
		t.Fatalf("seeded remote count=%d exceeds limit %d", len(fixture.files), store.MaxRawSyncFiles)
	}
}

func TestRawSyncStoragePostCommitErrorsUseHandlerReadbackBeforeGrowth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	binary := filepath.Join(t.TempDir(), "lwc-sync")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	for _, test := range []struct {
		name            string
		replacement     bool
		failReadback    bool
		wantAdded       int
		wantUpdated     int
		wantSummaryFail int
	}{
		{name: "new file readback confirms commit", wantAdded: 1, wantSummaryFail: 2},
		{name: "new file readback fails", failReadback: true, wantSummaryFail: 2},
		{name: "enlarged replacement readback confirms commit", replacement: true, wantUpdated: 1, wantSummaryFail: 2},
		{name: "enlarged replacement readback fails", replacement: true, failReadback: true, wantSummaryFail: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := newPostCommitRawSyncStore(test.replacement, test.failReadback)
			initialCount := len(remote.files)
			initialBytes := int64(0)
			for _, file := range remote.files {
				initialBytes += file.Size
			}
			if initialBytes != store.MaxRawSyncTotalBytes-1 {
				t.Fatalf("fixture starts with %d bytes, want max-minus-one %d", initialBytes, store.MaxRawSyncTotalBytes-1)
			}
			base := localfs.New(t.TempDir())
			scoped := &postCommitRawSyncScopedStore{
				Store:        base.WithScope("user-1", "project-1"),
				RawSyncStore: remote,
			}
			rootStore := &postCommitRawSyncRootStore{RootStore: base, scoped: scoped}
			handler := handlerV1.New(rootStore, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("userID", "user-1")
				c.Set("projectID", "project-1")
				c.Next()
			})
			router.GET(cliRawListRoute, handler.SyncRawList)
			router.GET(cliRawFileRoute, handler.SyncRawDownload)
			router.PUT(cliRawFileRoute, handler.SyncRawUpload)

			var statusMu sync.Mutex
			var uploadStatuses []int
			dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				statusWriter := &rawSyncStatusWriter{ResponseWriter: w}
				router.ServeHTTP(statusWriter, r)
				if r.Method == http.MethodPut && r.URL.Path == cliRawFileRoute {
					statusMu.Lock()
					uploadStatuses = append(uploadStatuses, statusWriter.status)
					statusMu.Unlock()
				}
			}))
			defer dataServer.Close()

			authFixture := &rawSyncProcessFixture{}
			authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authFixture.serveAuth(w, r, dataServer.URL)
			}))
			defer authServer.Close()

			root := t.TempDir()
			rawDir := filepath.Join(root, "raw")
			if err := os.MkdirAll(rawDir, 0o755); err != nil {
				t.Fatal(err)
			}
			localFiles := map[string][]byte{
				"a-shrink.bin": bytes.Repeat([]byte("s"), 1),
				"c-second.bin": []byte("c"),
			}
			firstGrowth := []byte("n")
			if test.replacement {
				firstGrowth = []byte("en")
			}
			localFiles["b-first.bin"] = firstGrowth
			for path, data := range localFiles {
				if err := os.WriteFile(filepath.Join(rawDir, path), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			configRoot := filepath.Join(t.TempDir(), "config")
			storeDir := filepath.Join(configRoot, localAuthDirectoryName)
			if err := os.MkdirAll(storeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			credentials := cliLocalCredentials{
				AuthHost: authServer.URL, AccessToken: "access-token-1", RefreshToken: "refresh-token-1",
				SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1,
			}
			writeTestJSON(t, filepath.Join(storeDir, localConfigFileName), cliLocalConfig{AuthHost: authServer.URL})
			writeTestJSON(t, filepath.Join(storeDir, localCredentialsName), credentials)
			run := func(args ...string) (string, error) {
				command := exec.Command(binary, args...)
				command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot, "LWC_SYNC_AUTH_HOST="+authServer.URL)
				output, err := command.CombinedOutput()
				return string(output), err
			}
			if out, err := run("init", "--vault", root, "--host", authServer.URL, "--project-id", "project-1"); err != nil {
				t.Fatalf("init fixture vault: %v\n%s", err, out)
			}

			out, err := run("push", "--vault", root)
			if err == nil {
				t.Fatalf("push unexpectedly succeeded despite the rejected shrink and full capacity: %s", out)
			}
			if !strings.Contains(out, fmt.Sprintf("Added: %d  Updated: %d", test.wantAdded, test.wantUpdated)) || !strings.Contains(out, fmt.Sprintf("Failed: %d", test.wantSummaryFail)) {
				t.Fatalf("push summary did not preserve success/failure counts: %v\n%s", err, out)
			}
			if test.failReadback && !strings.Contains(out, "ambiguous upload response") {
				t.Fatalf("failed readback was not reported as ambiguous: %v\n%s", err, out)
			}
			if !test.failReadback && !strings.Contains(out, "capacity remains occupied") {
				t.Fatalf("confirmed growth did not block the next write at capacity: %v\n%s", err, out)
			}

			statusMu.Lock()
			gotStatuses := append([]int(nil), uploadStatuses...)
			statusMu.Unlock()
			if len(gotStatuses) != 2 || gotStatuses[0] != http.StatusConflict || gotStatuses[1] != http.StatusServiceUnavailable {
				t.Fatalf("actual handler upload statuses=%v, want [409 503]", gotStatuses)
			}
			remote.mu.Lock()
			if len(remote.writeAttempts) != 2 || remote.writeAttempts[0] != "a-shrink.bin" || remote.writeAttempts[1] != "b-first.bin" {
				remote.mu.Unlock()
				t.Fatalf("storage write attempts=%v, want rejected shrink then one growth", remote.writeAttempts)
			}
			if _, ok := remote.files["c-second.bin"]; ok {
				remote.mu.Unlock()
				t.Fatal("later growth was committed after an uncertain post-commit error")
			}
			growth, ok := remote.files["b-first.bin"]
			if !ok || growth.Size != int64(len(firstGrowth)) || growth.SHA256 != fmt.Sprintf("%x", sha256.Sum256(firstGrowth)) || !bytes.Equal(remote.data["b-first.bin"], firstGrowth) {
				remote.mu.Unlock()
				t.Fatalf("first committed growth=%#v data=%q present=%v", growth, remote.data["b-first.bin"], ok)
			}
			var totalBytes int64
			for _, file := range remote.files {
				totalBytes += file.Size
			}
			wantCount := initialCount
			if !test.replacement {
				wantCount++
			}
			if len(remote.files) != wantCount || len(remote.files) > store.MaxRawSyncFiles || totalBytes != store.MaxRawSyncTotalBytes {
				remote.mu.Unlock()
				t.Fatalf("remote usage count=%d bytes=%d, want count=%d <=%d and exactly %d", len(remote.files), totalBytes, wantCount, store.MaxRawSyncFiles, store.MaxRawSyncTotalBytes)
			}
			remote.mu.Unlock()
		})
	}
}

type rawSyncStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *rawSyncStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *rawSyncStatusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

type postCommitRawSyncRootStore struct {
	store.RootStore
	scoped store.Store
}

func (s *postCommitRawSyncRootStore) Scope(userID, projectID string) store.Store {
	if userID != "user-1" || projectID != "project-1" {
		return nil
	}
	return s.scoped
}

type postCommitRawSyncScopedStore struct {
	store.Store
	store.RawSyncStore
}

type postCommitRawSyncStore struct {
	mu                     sync.Mutex
	files                  map[string]store.RawSyncFile
	data                   map[string][]byte
	writeAttempts          []string
	nextGeneration         int64
	rejectPath             string
	postCommitErrorPath    string
	failReadbackAfterWrite bool
	failNextList           bool
}

func newPostCommitRawSyncStore(replacement, failReadback bool) *postCommitRawSyncStore {
	f := &postCommitRawSyncStore{
		files:                  make(map[string]store.RawSyncFile),
		data:                   make(map[string][]byte),
		nextGeneration:         2000,
		rejectPath:             "a-shrink.bin",
		postCommitErrorPath:    "b-first.bin",
		failReadbackAfterWrite: failReadback,
	}
	add := func(path string, data []byte, size int64) {
		f.nextGeneration++
		seed := data
		if seed == nil {
			seed = []byte(fmt.Sprintf("%s:%d", path, size))
		}
		digest := sha256.Sum256(seed)
		f.files[path] = store.RawSyncFile{Path: path, Size: size, SHA256: hex.EncodeToString(digest[:]), Generation: strconv.FormatInt(f.nextGeneration, 10)}
		if data != nil {
			f.data[path] = append([]byte(nil), data...)
		}
	}
	shrinkData := bytes.Repeat([]byte("s"), 410)
	add("a-shrink.bin", shrinkData, int64(len(shrinkData)))
	used := int64(len(shrinkData))
	if replacement {
		add("b-first.bin", []byte("o"), 1)
		used++
	}
	for i := 0; used < store.MaxRawSyncTotalBytes-1; i++ {
		size := min(store.MaxRawSyncTotalBytes-1-used, store.MaxRawSyncFileBytes)
		add(fmt.Sprintf("capacity-%03d.bin", i), nil, size)
		used += size
	}
	return f
}

func (f *postCommitRawSyncStore) ListSyncRawFiles(ctx context.Context) ([]store.RawSyncFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextList {
		f.failNextList = false
		return nil, errors.New("injected inventory failure after commit")
	}
	files := make([]store.RawSyncFile, 0, len(f.files))
	for _, file := range f.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (f *postCommitRawSyncStore) OpenSyncRawFile(ctx context.Context, path, generation string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[path]
	if !ok || file.Generation != generation {
		return nil, 0, store.ErrRawSyncConflict
	}
	data, ok := f.data[path]
	if !ok || int64(len(data)) != file.Size {
		return nil, 0, errors.New("test raw object body unavailable")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), file.Size, nil
}

func (f *postCommitRawSyncStore) WriteSyncRawFile(ctx context.Context, path string, body io.Reader, expectedGeneration, expectedSHA256 string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store.ValidateRawSyncPath(path) != nil || !store.ValidRawSyncSHA256(expectedSHA256) {
		return "", errors.New("invalid raw sync upload")
	}
	data, err := io.ReadAll(io.LimitReader(body, store.MaxRawSyncFileBytes+1))
	if err != nil || int64(len(data)) > store.MaxRawSyncFileBytes {
		return "", errors.New("invalid raw sync upload body")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return "", errors.New("raw sync upload digest mismatch")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeAttempts = append(f.writeAttempts, path)
	current, exists := f.files[path]
	if f.rejectPath == path {
		f.rejectPath = ""
		return "", store.ErrRawSyncConflict
	}
	if (exists && current.Generation != expectedGeneration) || (!exists && expectedGeneration != "") {
		return "", store.ErrRawSyncConflict
	}
	f.nextGeneration++
	generation := strconv.FormatInt(f.nextGeneration, 10)
	f.data[path] = append([]byte(nil), data...)
	f.files[path] = store.RawSyncFile{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Generation: generation}
	if f.postCommitErrorPath == path {
		f.postCommitErrorPath = ""
		f.failNextList = f.failReadbackAfterWrite
		return "", errors.Join(store.ErrRawSyncCommitUncertain, store.ErrRawSyncConflict, errors.New("injected post-commit inspection conflict"))
	}
	return generation, nil
}

func (f *rawSyncProcessFixture) seed(path string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextGeneration++
	digest := sha256.Sum256(data)
	f.data[path] = append([]byte(nil), data...)
	f.files[path] = store.RawSyncFile{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Generation: strconv.FormatInt(f.nextGeneration, 10)}
}

func (f *rawSyncProcessFixture) serveAuth(w http.ResponseWriter, r *http.Request, dataOrigin string) {
	if r.URL.Path == "/api/v1/auth/cli/refresh" && r.Method == http.MethodPost {
		f.mu.Lock()
		f.refreshes++
		f.mu.Unlock()
		writeTestJSONResponse(w, http.StatusOK, cliLocalCredentials{AuthHost: r.Host, AccessToken: "access-token-2", RefreshToken: "refresh-token-2", SessionID: "session-1", UserID: "user-1", Role: "user", AuthVersion: 1})
		return
	}
	if !rawSyncFixtureAcceptsAccessToken(r.Header.Get("Authorization")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/cli/bindings":
		if r.Method == http.MethodGet {
			f.mu.Lock()
			bindings := append([]authSyncBinding(nil), f.bindings...)
			f.mu.Unlock()
			writeTestJSONResponse(w, http.StatusOK, map[string]any{"bindings": bindings})
			return
		}
		if r.Method == http.MethodPost {
			var input struct {
				ProjectID string `json:"project_id"`
				WikiID    string `json:"wiki_id"`
				Host      string `json:"host"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.bindingCreates++
			binding := authSyncBinding{ID: "binding-1", Host: input.Host, ProjectID: input.ProjectID, WikiID: input.WikiID, Status: "active"}
			f.bindings = []authSyncBinding{binding}
			loseResponse := f.loseCreateResponse
			f.loseCreateResponse = false
			f.mu.Unlock()
			if loseResponse {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeTestJSONResponse(w, http.StatusCreated, map[string]string{"binding_id": binding.ID})
			return
		}
	case "/api/v1/auth/cli/sync-service":
		f.mu.Lock()
		rotate, store, credentials := f.rotateOnLocator, f.rotateStore, f.rotatedCredentials
		f.rotateOnLocator = false
		f.mu.Unlock()
		if rotate {
			if store == nil || store.withLock(func() error { return store.saveCredentials(credentials) }) != nil {
				http.Error(w, "could not rotate local test credentials", http.StatusInternalServerError)
				return
			}
		}
		writeTestJSONResponse(w, http.StatusOK, syncServiceLocator{Origin: dataOrigin})
		return
	}
	http.NotFound(w, r)
}

func (f *rawSyncProcessFixture) serveData(w http.ResponseWriter, r *http.Request, redirectOrigin string) {
	f.mu.Lock()
	f.dataRequests++
	if r.Method == http.MethodGet && r.URL.Path == cliRawListRoute && f.failNextListRead {
		f.failNextListRead = false
		f.readbackListFailures++
		f.mu.Unlock()
		http.Error(w, "injected remote readback failure", http.StatusServiceUnavailable)
		return
	}
	if f.force401 && r.Method == http.MethodGet && r.URL.Path == cliRawListRoute {
		f.force401 = false
		f.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == cliRawListRoute && f.mutateLocalPath != "" {
		path := f.mutateLocalPath
		f.mutateLocalPath = ""
		f.mu.Unlock()
		_ = os.WriteFile(path, []byte("changed after inventory"), 0o644)
		f.mu.Lock()
	}
	if r.Method == http.MethodGet && r.URL.Path == cliRawListRoute && f.moveLocalParent != "" {
		parent, moved := f.moveLocalParent, f.moveParentTarget
		f.moveLocalParent, f.moveParentTarget = "", ""
		f.mu.Unlock()
		err := os.Rename(parent, moved)
		if err == nil {
			err = os.Symlink(moved, parent)
		}
		f.mu.Lock()
		if err != nil {
			f.moveParentError = err.Error()
			f.mu.Unlock()
			http.Error(w, "could not replace local parent", http.StatusInternalServerError)
			return
		}
	}
	mode := f.mode
	if mode == "forbidden" && r.URL.Path == cliRawListRoute {
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if mode == "redirect" && r.URL.Path == cliRawListRoute {
		f.mu.Unlock()
		w.Header().Set("Location", redirectOrigin+"/received")
		w.WriteHeader(http.StatusFound)
		return
	}
	if !rawSyncFixtureAcceptsAccessToken(r.Header.Get("Authorization")) {
		f.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-Project-ID") != "project-1" || r.Header.Get("X-Sync-Binding-ID") != "binding-1" || r.Header.Get("X-Wiki-ID") == "" {
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case cliRawListRoute:
		files := make([]store.RawSyncFile, 0, len(f.files))
		for _, file := range f.files {
			files = append(files, file)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		snapshot := fixtureSnapshot(files)
		offset := 0
		if token := r.URL.Query().Get("page_token"); token != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(token)
			parts := strings.Split(string(decoded), ":")
			if err != nil || len(parts) != 2 || parts[0] != snapshot {
				f.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			offset, _ = strconv.Atoi(parts[1])
		}
		end := min(offset+cliRawPageSize, len(files))
		page := map[string]any{"files": files[offset:end], "snapshot": snapshot, "total_files": len(files)}
		if end < len(files) {
			page["next_page_token"] = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", snapshot, end)))
		}
		f.mu.Unlock()
		writeTestJSONResponse(w, http.StatusOK, page)
		return
	case cliRawFileRoute:
		path := r.URL.Query().Get("path")
		if r.Method == http.MethodGet {
			file, ok := f.files[path]
			data := append([]byte(nil), f.data[path]...)
			truncate := f.truncateDownloadPath == path
			if truncate {
				f.truncateDownloadPath = ""
				if len(data) > 0 {
					data = data[:len(data)-1]
				}
			}
			localPath := f.createLocalPath
			localData := append([]byte(nil), f.createLocalData...)
			f.createLocalPath = ""
			f.createLocalData = nil
			if !ok || file.Generation != r.URL.Query().Get("generation") {
				f.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.mu.Unlock()
			if localPath != "" {
				_ = os.WriteFile(localPath, localData, 0o644)
			}
			w.Header().Set("X-Raw-Generation", file.Generation)
			if truncate {
				w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
			} else {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		if r.Method == http.MethodPut {
			f.uploadAttempts++
			old, exists := f.files[path]
			if f.conflictPath == path {
				f.nextGeneration++
				concurrentDigest := sha256.Sum256(f.conflictData)
				f.data[path] = append([]byte(nil), f.conflictData...)
				f.files[path] = store.RawSyncFile{Path: path, Size: int64(len(f.conflictData)), SHA256: hex.EncodeToString(concurrentDigest[:]), Generation: strconv.FormatInt(f.nextGeneration, 10)}
				f.conflictPath = ""
				old, exists = f.files[path]
			}
			expected := r.Header.Get("X-Expected-Generation")
			if (exists && old.Generation != expected) || (!exists && expected != "") {
				f.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			rejectUpload := f.rejectUploadPath == path
			if rejectUpload {
				f.rejectUploadPath = ""
			}
			dropBeforeCommit := f.dropBeforeCommitPath == path
			if dropBeforeCommit {
				f.dropBeforeCommitPath = ""
			}
			f.mu.Unlock()
			if rejectUpload {
				w.WriteHeader(http.StatusConflict)
				return
			}
			data, err := io.ReadAll(io.LimitReader(r.Body, store.MaxRawSyncFileBytes+1))
			digest := sha256.Sum256(data)
			if err != nil || int64(len(data)) > store.MaxRawSyncFileBytes || hex.EncodeToString(digest[:]) != r.Header.Get("X-Content-SHA256") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if dropBeforeCommit {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			f.mu.Lock()
			current, currentExists := f.files[path]
			if currentExists != exists || (exists && current.Generation != old.Generation) {
				f.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.nextGeneration++
			generation := strconv.FormatInt(f.nextGeneration, 10)
			f.data[path] = data
			f.files[path] = store.RawSyncFile{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Generation: generation}
			dropResponse := f.dropUploadPath == path
			if dropResponse {
				f.dropUploadPath = ""
			}
			if dropResponse && f.failListAfterDropPath == path {
				f.failNextListRead = true
				f.failListAfterDropPath = ""
			}
			mutateLocalPath := ""
			var mutateLocalData []byte
			if f.mutateAfterCommitPath == path {
				mutateLocalPath = f.mutateAfterCommitFile
				mutateLocalData = append([]byte(nil), f.mutateAfterCommitData...)
				f.mutateAfterCommitPath = ""
				f.mutateAfterCommitFile = ""
				f.mutateAfterCommitData = nil
			}
			f.mu.Unlock()
			if mutateLocalPath != "" {
				if err := os.WriteFile(mutateLocalPath, mutateLocalData, 0o644); err != nil {
					f.mu.Lock()
					f.mutateAfterCommitErr = err.Error()
					f.mu.Unlock()
				}
			}
			if dropResponse {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			writeTestJSONResponse(w, http.StatusOK, map[string]string{"generation": generation, "sha256": hex.EncodeToString(digest[:])})
			return
		}
	}
	f.mu.Unlock()
	http.NotFound(w, r)
}

func fixtureSnapshot(files []store.RawSyncFile) string {
	h := sha256.New()
	for _, file := range files {
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\n", file.Path, file.Size, file.SHA256, file.Generation)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rawSyncFixtureAcceptsAccessToken(header string) bool {
	if header == "Bearer access-token-1" || header == "Bearer access-token-2" {
		return true
	}
	return cliAccessTokenUsable(strings.TrimPrefix(header, "Bearer "))
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
