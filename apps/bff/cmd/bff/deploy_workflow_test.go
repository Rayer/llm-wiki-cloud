package main

import (
	"os"
	"os/exec"
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

func TestBFFServiceCDContractUsesImageOnlyMutation(t *testing.T) {
	script := readBFFCDFile(t, "deploy/cd.sh")
	common := readBFFCDFile(t, "deploy/components/common.sh")
	bff := readBFFCDFile(t, "deploy/components/bff.sh")
	contract := script + common + bff
	for _, marker := range []string{
		"service_image_handle", "service_image_readback", "gcloud run services update", "--image \"$image\"", "@sha256:",
	} {
		if !strings.Contains(contract, marker) {
			t.Fatalf("shared BFF service path missing %q", marker)
		}
	}
	start := strings.Index(bff, "bff_mutate() {")
	end := strings.Index(bff, "\nbff_verify() {")
	if start < 0 || end < start {
		t.Fatal("BFF image-only mutation function is missing")
	}
	if !strings.Contains(bff[start:end], "gcloud run services update-traffic") {
		t.Fatal("BFF service mutation must explicitly converge traffic")
	}
	for _, forbidden := range []string{"gcloud run deploy", "--update-env-vars", "--update-secrets", "--service-account", "--network", "--subnet", "--vpc-egress", "--ingress", "--max"} {
		if strings.Contains(bff[start:end], forbidden) {
			t.Fatalf("BFF mutation must not contain runtime flag %q", forbidden)
		}
	}
	imageStart := strings.Index(common, "image_for()")
	imageEnd := strings.Index(common, "\nredact_evidence()")
	if !strings.Contains(bff, "image=$(image_for bff)") || imageStart < 0 || imageEnd < imageStart || strings.Contains(common[imageStart:imageEnd], ":latest") || !strings.Contains(common[imageStart:imageEnd], "@sha256:") {
		t.Fatal("BFF deployment image identity must be digest-pinned")
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
		contents := []byte(readBFFCDFile(t, "deploy/environments/"+environment+".yaml"))
		var config map[string]any
		if err := yaml.Unmarshal(contents, &config); err != nil {
			t.Fatalf("%s environment config is invalid YAML: %v", environment, err)
		}
		bff, ok := config["bff"].(map[string]any)
		if !ok {
			t.Fatalf("%s BFF deployment metadata is missing", environment)
		}
		for _, removed := range []string{"query_config", "allowed_origins", "firestore_database_id", "secret_references", "profile_runtime_audience"} {
			if _, present := bff[removed]; present {
				t.Fatalf("%s still contains BFF application config field %q", environment, removed)
			}
		}
	}
	if !strings.Contains(readBFFCDFile(t, "deploy/cac/ssot.pkl"), "query-dev-2026-09-12.1.json") {
		t.Fatal("BFF query config is not sourced from the selected typed SSOT")
	}
	script := readBFFCDFile(t, "deploy/cd.sh")
	if strings.Contains(script, "gcloud projects add-iam-policy-binding") || strings.Contains(script, "gcloud run services set-iam-policy") || strings.Contains(script, "run jobs execute") {
		t.Fatal("BFF CD path contains forbidden IAM or Worker execution mutation")
	}
}

func TestDeploymentEngineR2WorkflowContract(t *testing.T) {
	command := exec.Command("python3", "../../../../scripts/test_engine_workflow.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("r2 workflow contract: %v\n%s", err, output)
	}
}
