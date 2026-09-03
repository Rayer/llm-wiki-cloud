package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDeployWorkerWorkflowContract(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/deploy-dev.yml")
	script := readWorkflow(t, "deploy/cd.sh")
	shared := readWorkflow(t, ".github/workflows/cd.yml")
	components := readWorkflow(t, "deploy/components/common.sh") + readWorkflow(t, "deploy/components/worker.sh")

	if strings.Contains(workflow, "push:") || !strings.Contains(workflow, "workflow_dispatch:") {
		t.Fatal("DEV entry workflow must be dispatch-only")
	}
	for _, want := range []string{
		"if: github.ref == 'refs/heads/develop'",
		"source_ref: develop",
		"source_sha: ${{ github.sha }}",
		"config_path: deploy/environments/development.yaml",
		"uses: ./.github/workflows/cd.yml",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("DEV entry workflow missing %q", want)
		}
	}
	for _, want := range []string{
		"go run ./cmd/deploy_config",
		"event=push",
		"head_sha=${SOURCE_SHA}",
		"git rev-parse \"origin/$SOURCE_REF\"",
		"cd-images-$SOURCE_SHA",
		"nonce=$(printf '%032x' \"$(date -u +%s%N)\")",
		"--build-arg BUILD_NONCE=\"$nonce\"",
		"--target worker",
		"gcloud run jobs update",
		"--update-secrets",
		"--args",
		"gcloud run jobs replace",
	} {
		if !strings.Contains(script+shared+components, want) {
			t.Fatalf("shared CD contract missing %q", want)
		}
	}
	if strings.Contains(script, "run jobs execute") || strings.Contains(shared, "run jobs execute") {
		t.Fatal("deployment contract must never execute the Worker Job")
	}
	imagePathStart := strings.Index(components, "image_for()")
	imagePathEnd := strings.Index(components, "\nredact_evidence()")
	if imagePathStart < 0 || imagePathEnd < imagePathStart {
		t.Fatal("shared image receipt helper is missing")
	}
	imagePath := components[imagePathStart:imagePathEnd]
	if strings.Contains(imagePath, ":latest") {
		t.Fatal("Worker image identity must be digest-pinned, not latest")
	}
}

func TestWorkerPromotionWorkflowsContract(t *testing.T) {
	production := readWorkflow(t, ".github/workflows/promote-production.yml")
	shared := readWorkflow(t, ".github/workflows/cd.yml")
	script := readWorkflow(t, "deploy/cd.sh")
	components := readWorkflow(t, "deploy/components/common.sh") + readWorkflow(t, "deploy/components/worker.sh")
	for name, source := range map[string]string{"production": production, "shared": shared} {
		var document any
		if err := yaml.Unmarshal([]byte(source), &document); err != nil {
			t.Fatalf("%s workflow is not valid YAML: %v", name, err)
		}
		for _, run := range regexp.MustCompile(`(?m)^\s+run: \|\n((?:\s{10,}.+\n?)+)`).FindAllStringSubmatch(source, -1) {
			body := strings.TrimSpace(run[1])
			body = regexp.MustCompile(`(?m)^ {10}`).ReplaceAllString(body, "")
			body = regexp.MustCompile(`\$\{\{[^}]*\}\}`).ReplaceAllString(body, "workflow-expression")
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(body)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s workflow run block has invalid shell syntax: %v\n%s", name, err, output)
			}
		}
	}
	for _, want := range []string{
		"if: github.ref == 'refs/heads/main'",
		"source_ref: main",
		"source_sha: ${{ github.sha }}",
		"config_environment: production",
		"config_path: deploy/environments/production.yaml",
		"uses: ./.github/workflows/cd.yml",
	} {
		if !strings.Contains(production, want) {
			t.Fatalf("Production entry workflow missing %q", want)
		}
	}
	consumeStart := strings.Index(script, "consume_dev_images()")
	consumeEnd := -1
	if consumeStart >= 0 {
		consumeEnd = consumeStart + strings.Index(script[consumeStart:], "\n}\n\npreflight_shared")
	}
	if consumeStart < 0 || consumeEnd < consumeStart {
		t.Fatal("Production image receipt consumer is missing")
	}
	consume := script[consumeStart:consumeEnd]
	for _, want := range []string{"event=workflow_dispatch", "head_sha=${SOURCE_SHA}", "branch=develop", "gh run download", "cd-images-$SOURCE_SHA"} {
		if !strings.Contains(consume, want) {
			t.Fatalf("Production image receipt contract missing %q", want)
		}
	}
	if strings.Contains(consume, "event=push") || strings.Contains(consume, "docker build") || strings.Contains(consume, "gcloud builds") {
		t.Fatal("Production must consume exact DEV receipts without rebuilding")
	}
	for _, forbidden := range []string{"roles/editor", "roles/owner", "roles/run.admin", "gcloud projects add-iam-policy-binding", "run jobs execute"} {
		if strings.Contains(script+shared, forbidden) {
			t.Fatalf("CD contract must not contain broad IAM or Worker execution %q", forbidden)
		}
	}
	freeze := strings.Index(shared, "      - name: Freeze Auth rollback handle")
	upload := strings.Index(shared, "      - name: Upload durable rollback artifact")
	mutation := strings.Index(shared, "      - name: Mutate Auth")
	if !(freeze >= 0 && freeze < upload && upload < mutation) {
		t.Fatal("durable rollback upload must precede all mutations")
	}
	if !strings.Contains(components, "gcloud run jobs replace") || !strings.Contains(components, "handles.worker.definition") {
		t.Fatal("Worker rollback must restore the frozen complete definition")
	}
}

func TestWorkerReleaseEmitsOneNormalizedEvidenceArtifactAfterReadback(t *testing.T) {
	shared := readWorkflow(t, ".github/workflows/cd.yml")
	script := readWorkflow(t, "deploy/cd.sh")
	common := readWorkflow(t, "deploy/components/common.sh")
	for _, want := range []string{
		"Reconcile Auth",
		"Reconcile BFF",
		"Reconcile Worker",
		"Reconcile Frontend",
		"Render normalized redacted evidence",
		"Upload normalized CD evidence",
		"mutation_count",
		"mutation_components",
		"rollback_attempted",
		"rollback_result",
		"rollback_verified",
		"next_action",
	} {
		if !strings.Contains(shared+script, want) {
			t.Fatalf("normalized evidence contract missing %q", want)
		}
	}
	if strings.Contains(script, "provider_readback:true") || strings.Contains(script, "verified:true") {
		t.Fatal("evidence must not fabricate provider or rollback success")
	}
	if !strings.Contains(script+common, "redact_evidence") || !strings.Contains(script+common, "<redacted>") {
		t.Fatal("evidence must redact credential-like fields")
	}
}

func TestWorkerReleaseRollbackUploadFailureOutcomesBlockEvidenceFinalization(t *testing.T) {
	shared := readWorkflow(t, ".github/workflows/cd.yml")
	mutation := strings.Index(shared, "      - name: Mutate Auth")
	upload := strings.Index(shared, "      - name: Upload durable rollback artifact")
	if upload < 0 || mutation < 0 || upload > mutation {
		t.Fatal("rollback upload outcome must gate mutation")
	}
	if !strings.Contains(shared[mutation:], "steps.rollback_upload.outcome == 'success'") {
		t.Fatal("mutation must be conditional on a successful rollback upload")
	}
	if !strings.Contains(shared, "if: always()") || !strings.Contains(shared, "cd-evidence-") {
		t.Fatal("evidence must be rendered/uploaded for failed outcomes")
	}
}

const workerJobShapeFilter = `
.spec.template.spec.template.spec
| if type != "object" then error("missing Cloud Run job template spec") else . end
| if (.containers | type) != "array" or (.containers | length) != 1 then error("expected exactly one container") else . end
| if (.containers[0].image | type) != "string" then error("container image must be a string") else . end
| if (.containers[0].env | type) != "array" then error("container env must be an array") else . end
| if (.containers[0].args | type) != "array" then error("container args must be an array") else . end
| if ((.volumes // []) | type) != "array" then error("volumes must be missing/null or an array") else . end
| if ((.containers[0].volumeMounts // []) | type) != "array" then error("volume mounts must be missing/null or an array") else . end
`

func TestWorkerJobContractFixtures(t *testing.T) {
	valid := map[string]struct {
		image            string
		envNames         []string
		artifactEnvNames []string
		volumeCount      int
		mountCount       int
	}{
		"legacy-prod.json": {
			image:            "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline:latest",
			envNames:         []string{"BUCKET", "DATA_DIR", "WORKSPACE", "VAULT_PATH", "WORKSPACE_DIR", "LLM_API_KEY", "DEEPSEEK_API_KEY", "USER_ID", "PROJECT_ID", "TASK_TYPE", "UNRELATED"},
			artifactEnvNames: []string{"BUCKET", "DATA_DIR", "WORKSPACE", "VAULT_PATH", "WORKSPACE_DIR"},
			volumeCount:      1,
			mountCount:       1,
		},
		"executable-legacy.json": {
			image:            "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline:latest",
			envNames:         []string{"WORKSPACE", "WORKSPACE_DIR", "LLM_API_KEY", "DEEPSEEK_API_KEY", "USER_ID", "PROJECT_ID", "TASK_TYPE", "UNRELATED"},
			artifactEnvNames: []string{"WORKSPACE", "WORKSPACE_DIR"},
			volumeCount:      1,
			mountCount:       1,
		},
		"desired-prod.json": {
			image:            "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			envNames:         []string{"BUCKET"},
			artifactEnvNames: []string{"BUCKET"},
			volumeCount:      0,
			mountCount:       0,
		},
		"desired-prod-omitted.json": {
			image:            "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			envNames:         []string{"BUCKET"},
			artifactEnvNames: []string{"BUCKET"},
			volumeCount:      0,
			mountCount:       0,
		},
	}
	for name, want := range valid {
		var got struct {
			Image string `json:"image"`
			Env   []struct {
				Name string `json:"name"`
			} `json:"env"`
			Volumes      []json.RawMessage `json:"volumes"`
			VolumeMounts []json.RawMessage `json:"volumeMounts"`
		}
		out := runJQFixture(t, name, workerJobShapeFilter+` | {image: .containers[0].image, env: .containers[0].env, volumes: (.volumes // []), volumeMounts: (.containers[0].volumeMounts // [])}`)
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%s extraction was not JSON: %v", name, err)
		}
		if got.Image != want.image || len(got.Env) != len(want.envNames) || len(got.Volumes) != want.volumeCount || len(got.VolumeMounts) != want.mountCount {
			t.Fatalf("%s extracted contract mismatch: image=%q env=%d volumes=%d mounts=%d", name, got.Image, len(got.Env), len(got.Volumes), len(got.VolumeMounts))
		}
		for i, env := range got.Env {
			if env.Name != want.envNames[i] {
				t.Fatalf("%s env[%d] = %q, want %q", name, i, env.Name, want.envNames[i])
			}
		}

		var artifact struct {
			Image string `json:"image"`
			Env   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			Args         []string          `json:"args"`
			Volumes      []json.RawMessage `json:"volumes"`
			VolumeMounts []json.RawMessage `json:"volumeMounts"`
		}
		artifactFilter := workerJobShapeFilter + ` | {
			image: .containers[0].image,
			env: [.containers[0].env[] | select(.name == "BUCKET" or .name == "DATA_DIR" or .name == "WORKSPACE" or .name == "VAULT_PATH" or .name == "WORKSPACE_DIR") | {name, value}],
			args: .containers[0].args,
			volumes: (.volumes // []),
			volumeMounts: (.containers[0].volumeMounts // [])
		}`
		artifactOut := runJQFixture(t, name, artifactFilter)
		if err := json.Unmarshal([]byte(artifactOut), &artifact); err != nil {
			t.Fatalf("%s artifact extraction was not JSON: %v", name, err)
		}
		if artifact.Image != want.image || len(artifact.Env) != len(want.artifactEnvNames) || len(artifact.Args) != 2 || len(artifact.Volumes) != want.volumeCount || len(artifact.VolumeMounts) != want.mountCount {
			t.Fatalf("%s artifact contract mismatch: image=%q env=%d args=%d volumes=%d mounts=%d", name, artifact.Image, len(artifact.Env), len(artifact.Args), len(artifact.Volumes), len(artifact.VolumeMounts))
		}
		for i, env := range artifact.Env {
			if env.Name != want.artifactEnvNames[i] {
				t.Fatalf("%s artifact env[%d] = %q, want %q", name, i, env.Name, want.artifactEnvNames[i])
			}
		}
	}

	for _, name := range []string{
		"malformed-missing-spec.json",
		"malformed-two-containers.json",
		"malformed-image.json",
		"malformed-env.json",
		"malformed-args.json",
		"malformed-volumes.json",
		"malformed-volume-mounts.json",
	} {
		if _, err := runJQFixtureE(name, workerJobShapeFilter); err == nil {
			t.Fatalf("%s unexpectedly passed mandatory Cloud Run shape validation", name)
		}
	}
}

func runJQFixture(t *testing.T, name, filter string) string {
	t.Helper()
	out, err := runJQFixtureE(name, filter)
	if err != nil {
		t.Fatalf("jq fixture %s failed: %v\n%s", name, err, out)
	}
	return out
}

func runJQFixtureE(name, filter string) (string, error) {
	path := filepath.Join("testdata", "lwc179", name)
	cmd := exec.Command("jq", "-e", filter, path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readWorkflow(t *testing.T, path string) string {
	t.Helper()
	if strings.HasPrefix(path, ".github/workflows/") || path == "deploy/cd.sh" || strings.HasPrefix(path, "deploy/components/") {
		path = filepath.Join("..", "..", "..", "..", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
