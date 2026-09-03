package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func bffRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readBFFCDFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(bffRepoRoot(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestBFFServiceCDContractUsesImmutableQueryAndRuntimeConfig(t *testing.T) {
	script := readBFFCDFile(t, "deploy/cd.sh")
	common := readBFFCDFile(t, "deploy/components/common.sh")
	bff := readBFFCDFile(t, "deploy/components/bff.sh")
	contract := script + common + bff
	for _, marker := range []string{
		"QUERY_STAGE_CONFIG_PATH=$(plan_json '.query_config.runtime_path')",
		"--remove-env-vars",
		"QUERY_EXPANSION_MODEL", "QUERY_SELECTION_LIMIT", "--update-secrets",
		"--service-account", "--network", "--subnet", "--vpc-egress", "--ingress", "--max",
		"gcloud run deploy", "gcloud run services update-traffic", "normalize_service_readback",
	} {
		if !strings.Contains(contract, marker) {
			t.Fatalf("shared BFF service path missing %q", marker)
		}
	}
	imageStart := strings.Index(common, "image_for()")
	imageEnd := strings.Index(common, "\nredact_evidence()")
	if !strings.Contains(bff, "image=$(image_for bff)") || imageStart < 0 || imageEnd < imageStart || strings.Contains(common[imageStart:imageEnd], ":latest") || !strings.Contains(common[imageStart:imageEnd], "@sha256:") {
		t.Fatal("BFF deployment image identity must be digest-pinned")
	}
}

func TestProductionBFFUsesDEVReceiptAndNoRebuild(t *testing.T) {
	production := readBFFCDFile(t, ".github/workflows/promote-production.yml")
	script := readBFFCDFile(t, "deploy/cd.sh")
	if !strings.Contains(production, "source_ref: main") || !strings.Contains(production, "config_environment: production") || !strings.Contains(production, "environment: Production") {
		t.Fatal("production wrapper is not fixed to main/Production")
	}
	start := strings.Index(script, "consume_dev_images()")
	end := -1
	if start >= 0 {
		end = start + strings.Index(script[start:], "\n}\n\npreflight_shared")
	}
	if start < 0 || end < start {
		t.Fatal("production receipt consumer is missing")
	}
	consume := script[start:end]
	for _, marker := range []string{"event=workflow_dispatch", "head_sha=${SOURCE_SHA}", "branch=develop", "gh run download", "cd-images-$SOURCE_SHA"} {
		if !strings.Contains(consume, marker) {
			t.Fatalf("production BFF receipt path missing %q", marker)
		}
	}
	if strings.Contains(consume, "gcloud builds submit") || strings.Contains(consume, "docker build") {
		t.Fatal("production BFF must not rebuild DEV artifacts")
	}
}

func TestBFFWorkflowAndConfigAreValidAndMutationSafe(t *testing.T) {
	for _, path := range []string{".github/workflows/cd.yml", ".github/workflows/deploy-dev.yml", ".github/workflows/promote-production.yml"} {
		contents := readBFFCDFile(t, path)
		var document any
		if err := yaml.Unmarshal([]byte(contents), &document); err != nil {
			t.Fatalf("%s is not valid YAML: %v", path, err)
		}
	}
	for _, environment := range []string{"development", "production"} {
		contents := readBFFCDFile(t, "deploy/environments/"+environment+".yaml")
		if !strings.Contains(contents, "query_config: apps/bff/configs/query/dev/query-dev-2026-08-31.1.json") {
			t.Fatalf("%s does not point to the sealed Query config", environment)
		}
	}
	script := readBFFCDFile(t, "deploy/cd.sh")
	if strings.Contains(script, "gcloud projects add-iam-policy-binding") || strings.Contains(script, "gcloud run services set-iam-policy") || strings.Contains(script, "run jobs execute") {
		t.Fatal("BFF CD path contains forbidden IAM or Worker execution mutation")
	}
}
