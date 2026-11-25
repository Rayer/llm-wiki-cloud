//go:build lwc_local_synto_runtime && byokprofilefixture

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/generation"
)

type competingManifestStore struct {
	objectStore
	manifestPath string
	raced        bool
}

func (s *competingManifestStore) Write(ctx context.Context, name string, data []byte, metadata map[string]string, condition objectConditions) (objectAttrs, error) {
	if name == s.manifestPath && condition.GenerationMatch > 0 && !s.raced {
		s.raced = true
		current, attrs, err := s.objectStore.Read(ctx, name, 0, generation.MaxManifestBytes)
		if err != nil {
			return objectAttrs{}, err
		}
		manifest, err := generation.Decode(current)
		if err != nil {
			return objectAttrs{}, err
		}
		manifest.LocalExecutionID = "competing-manifest-writer"
		competing, err := json.Marshal(manifest)
		if err != nil {
			return objectAttrs{}, err
		}
		if _, err := s.objectStore.Write(ctx, name, competing, nil, objectConditions{GenerationMatch: attrs.Generation}); err != nil {
			return objectAttrs{}, err
		}
	}
	return s.objectStore.Write(ctx, name, data, metadata, condition)
}

func cloudConflictFixture(t *testing.T, action string) (string, *memoryObjects, string, string) {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "seed-vault")
	operationFile := filepath.Join(root, "operation.json")
	localConflictFixture(t, "seed", vault, operationFile, action)
	objects := newMemoryObjects()
	deployedConfig := []byte(testDeployedPipelineConfig + "\n[providers.default]\nname = \"deepseek\"\nurl = \"https://fixture.example/v1\"\n\n[models.fast]\nprovider = \"default\"\nmodel = \"deepseek-chat\"\n\n[models.heavy]\nprovider = \"default\"\nmodel = \"deepseek-chat\"\n")
	if _, err := objects.Write(context.Background(), deployedPipelineConfigObjectPath, deployedConfig, nil, objectConditions{DoesNotExist: true}); err != nil {
		t.Fatalf("seed disposable deployed Synto config: %v", err)
	}
	prefix := "users/fixture-user/projects/fixture-project/"
	if _, _, err := publishCloudGeneration(context.Background(), objects, prefix, vault, nil); err != nil {
		t.Fatalf("seed disposable Cloud generation: %v", err)
	}
	_, attrs, err := objects.Read(context.Background(), prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	operationBytes, err := os.ReadFile(operationFile)
	if err != nil {
		t.Fatal(err)
	}
	var operation map[string]any
	if err := json.Unmarshal(operationBytes, &operation); err != nil {
		t.Fatal(err)
	}
	operation["storage_view"] = "manifest-" + strconv.FormatInt(attrs.Generation, 10)
	operationBytes, err = json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(operationFile, operationBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return vault, objects, prefix, operationFile
}

func cloudConflictConfig(t *testing.T, operationFile, executionID string) workerConfig {
	t.Helper()
	data, err := os.ReadFile(operationFile)
	if err != nil {
		t.Fatal(err)
	}
	oldGuidance := resolveProfileGuidanceAtCompileStart
	resolveProfileGuidanceAtCompileStart = func(context.Context, workerConfig, objectStore) (*profileGuidancePin, error) { return nil, nil }
	t.Cleanup(func() { resolveProfileGuidanceAtCompileStart = oldGuidance })
	oldTemp := cloudMkdirTemp
	root := t.TempDir()
	cloudMkdirTemp = func(_ string, pattern string) (string, error) { return os.MkdirTemp(root, pattern) }
	t.Cleanup(func() { cloudMkdirTemp = oldTemp })
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	python := os.Getenv("LOCAL_CLOUD_PYTHON")
	t.Setenv("PATH", filepath.Dir(python)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return workerConfig{
		Bucket: "disposable-fixture-bucket", UserID: "fixture-user", ProjectID: "fixture-project", ExecutionID: executionID,
		WorkspaceDir: root, Postprocess: true, StopOnError: true, SuppressOutput: true,
		ConceptResolveJSON: string(data),
	}
}

func cloudConflictReadback(t *testing.T, objects objectStore, prefix, operationFile, mode string) {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "readback")
	if _, _, _, _, err := materializeCloudWorkspace(context.Background(), objects, prefix, vault, false); err != nil {
		t.Fatalf("materialize current Cloud snapshot: %v", err)
	}
	localConflictFixture(t, mode, vault, operationFile)
}

func cloudConflictLog(objects objectStore, prefix, executionID string) string {
	data, _, err := objects.Read(context.Background(), prefix+"cache/pipeline-"+executionID+".log", 0, generation.MaxFileBytes)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

func TestCloudConceptConflictManifestCASRejectsConcurrentPointerAndKeepsCandidate(t *testing.T) {
	_, objects, prefix, operationFile := cloudConflictFixture(t, "publish_candidate")
	cfg := cloudConflictConfig(t, operationFile, "lwc341-cloud-cas")
	store := &competingManifestStore{objectStore: objects, manifestPath: prefix + generation.ManifestPath}
	err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, store)
	if !errors.Is(err, errObjectGenerationConflict) || !store.raced {
		t.Fatalf("Cloud CAS result=%v raced=%t, want a competing manifest generation conflict\n%s", err, store.raced, cloudConflictLog(objects, prefix, cfg.ExecutionID))
	}
	cloudConflictReadback(t, objects, prefix, operationFile, "verify-failed")
}

func TestCloudConceptConflictUnknownManifestOutcomeReplaysDurableReceipt(t *testing.T) {
	_, objects, prefix, operationFile := cloudConflictFixture(t, "publish_candidate")
	cfg := cloudConflictConfig(t, operationFile, "lwc341-cloud-unknown")
	store := &commitThenErrorStore{objectStore: objects, manifest: prefix + generation.ManifestPath, unknown: true}
	err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, store)
	if !errors.Is(err, errManifestCommitOutcomeUnknown) {
		t.Fatalf("Cloud publication outcome=%v, want unknown\n%s", err, cloudConflictLog(objects, prefix, cfg.ExecutionID))
	}
	cloudConflictReadback(t, objects, prefix, operationFile, "verify")
	if err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, objects); err != nil {
		t.Fatalf("same operation replay after unknown Cloud result: %v", err)
	}
	cloudConflictReadback(t, objects, prefix, operationFile, "verify")
}

func TestCloudConceptConflictKeepPublishedCommitsOneItem(t *testing.T) {
	_, objects, prefix, operationFile := cloudConflictFixture(t, "keep_published")
	cfg := cloudConflictConfig(t, operationFile, "lwc341-cloud-keep")
	if err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, objects); err != nil {
		t.Fatalf("Cloud keep-published action: %v\n%s", err, cloudConflictLog(objects, prefix, cfg.ExecutionID))
	}
	cloudConflictReadback(t, objects, prefix, operationFile, "verify")
}
