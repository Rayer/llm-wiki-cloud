//go:build lwc_local_synto_runtime

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/firestore"
)

func localConflictFixture(t *testing.T, mode, vault, operationFile string, args ...string) {
	t.Helper()
	python := os.Getenv("LOCAL_CLOUD_PYTHON")
	if python == "" || !filepath.IsAbs(python) {
		t.Fatal("LOCAL_CLOUD_PYTHON must identify the worktree Synto interpreter")
	}
	argv := []string{"testdata/lwc341_local_worker_fixture.py", mode, vault, operationFile}
	argv = append(argv, args...)
	cmd := exec.Command(python, argv...)
	root := t.TempDir()
	cmd.Env = []string{
		"PATH=" + filepath.Dir(python) + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HOME=" + root,
		"TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + root,
		"PYTHONDONTWRITEBYTECODE=1",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Local disposable Synto fixture %s failed: %v\n%s", mode, err, output)
	}
}

func conceptConflictWorkerConfig(t *testing.T, root, operationFile string) workerConfig {
	t.Helper()
	data, err := os.ReadFile(operationFile)
	if err != nil {
		t.Fatal(err)
	}
	var operation struct {
		StorageView string `json:"storage_view"`
	}
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	python := os.Getenv("LOCAL_CLOUD_PYTHON")
	t.Setenv("PATH", filepath.Dir(python)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	return workerConfig{
		UserID: "fixture-user", ProjectID: "fixture-project", ExecutionID: "lwc341-local-fixture",
		WorkspaceDir: root, Postprocess: true, StopOnError: true, SuppressOutput: true,
		ConceptResolveJSON: string(data), ConceptConflictStorageView: operation.StorageView,
	}
}

func runLocalConflictWorker(t *testing.T, vault string, cfg workerConfig) error {
	t.Helper()
	oldClient := newContentContextFirestoreClient
	newContentContextFirestoreClient = func(context.Context, string) (*firestore.Client, error) {
		return nil, errors.New("fixture disables Cloud access")
	}
	t.Cleanup(func() { newContentContextFirestoreClient = oldClient })
	return runWorkerBatchWorkspace(context.Background(), cfg, [][]string{{"run"}}, vault)
}

func localConflictLog(vault string) string {
	data, err := os.ReadFile(filepath.Join(vault, "cache", "pipeline-lwc341-local-fixture.log"))
	if err != nil {
		return err.Error()
	}
	return string(data)
}

func TestLocalConceptConflictWorkerStageCommitAndReceiptReplay(t *testing.T) {
	for _, action := range []string{"keep_published", "publish_candidate"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			vault := filepath.Join(root, "users", "fixture-user", "projects", "fixture-project")
			operationFile := filepath.Join(root, "operation.json")
			localConflictFixture(t, "seed", vault, operationFile, action)
			cfg := conceptConflictWorkerConfig(t, root, operationFile)

			if err := runLocalConflictWorker(t, vault, cfg); err != nil {
				t.Fatalf("Local worker commit failed: %v\n%s", err, localConflictLog(vault))
			}
			localConflictFixture(t, "verify", vault, operationFile)
			if err := runLocalConflictWorker(t, vault, cfg); err != nil {
				t.Fatalf("same-operation Local receipt replay failed: %v", err)
			}
			localConflictFixture(t, "verify", vault, operationFile)
		})
	}
}

func TestLocalConceptConflictStaleAttemptCommitsUnderLease(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "users", "fixture-user", "projects", "fixture-project")
	operationFile := filepath.Join(root, "operation.json")
	localConflictFixture(t, "seed", vault, operationFile, "publish_candidate")
	candidate := filepath.Join(vault, "wiki", ".drafts", "A.md")
	data, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "candidate A body", "changed after comparison", 1))
	if err := os.WriteFile(candidate, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runLocalConflictWorker(t, vault, conceptConflictWorkerConfig(t, root, operationFile)); err != nil {
		t.Fatalf("stale attempt should be durably reported, not fail source recheck: %v\n%s", err, localConflictLog(vault))
	}
	localConflictFixture(t, "verify-stale", vault, operationFile)
}

func TestLocalConceptConflictRenameFailureRollsBackBeforeReceipt(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "users", "fixture-user", "projects", "fixture-project")
	operationFile := filepath.Join(root, "operation.json")
	localConflictFixture(t, "seed", vault, operationFile, "publish_candidate")
	oldRename := publishRename
	t.Cleanup(func() { publishRename = oldRename })
	publishRename = func(root *os.Root, oldName, newName string) error {
		if strings.HasPrefix(oldName, ".lwc-worker-stage-") && newName == "wiki" {
			return errors.New("injected Local publish rename failure")
		}
		return oldRename(root, oldName, newName)
	}
	if err := runLocalConflictWorker(t, vault, conceptConflictWorkerConfig(t, root, operationFile)); err == nil || !strings.Contains(err.Error(), "injected Local publish rename failure") {
		t.Fatalf("rename failure = %v, want injected publish failure\n%s", err, localConflictLog(vault))
	}
	localConflictFixture(t, "verify-failed", vault, operationFile)
}

func TestLocalConceptConflictJournalWriteFailureRollsBackBeforeReceipt(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "users", "fixture-user", "projects", "fixture-project")
	operationFile := filepath.Join(root, "operation.json")
	localConflictFixture(t, "seed", vault, operationFile, "publish_candidate")
	oldWriter := publishJournalWrite
	t.Cleanup(func() { publishJournalWrite = oldWriter })
	writes := 0
	publishJournalWrite = func(vault string, journal publishJournalRecord) error {
		writes++
		if writes == 3 {
			return errors.New("injected Local publish journal failure")
		}
		return writePublishJournal(vault, journal)
	}
	if err := runLocalConflictWorker(t, vault, conceptConflictWorkerConfig(t, root, operationFile)); err == nil || !strings.Contains(err.Error(), "injected Local publish journal failure") {
		t.Fatalf("journal failure = %v, want injected journal write failure\n%s", err, localConflictLog(vault))
	}
	if writes < 3 {
		t.Fatalf("journal writer called %d times, fault point was not reached", writes)
	}
	localConflictFixture(t, "verify-failed", vault, operationFile)
}
