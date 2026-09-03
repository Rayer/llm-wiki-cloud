package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func cdRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readCDFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(cdRepoRoot(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func parseCDWorkflow(t *testing.T, name string) map[string]any {
	t.Helper()
	contents := readCDFile(t, name)
	var document map[string]any
	if err := yaml.Unmarshal([]byte(contents), &document); err != nil {
		t.Fatalf("%s is not valid YAML: %v", name, err)
	}
	if len(document) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return document
}

func yamlMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not a YAML mapping: %#v", label, value)
	}
	return result
}

func TestFixedCDEntryWorkflowsUseCanonicalSourceAndConfig(t *testing.T) {
	for _, tc := range []struct {
		name        string
		job         string
		branch      string
		environment string
		config      string
	}{
		{name: "deploy-dev.yml", job: "deploy", branch: "develop", environment: "Development", config: "development"},
		{name: "promote-production.yml", job: "promote", branch: "main", environment: "Production", config: "production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := readCDFile(t, ".github/workflows/"+tc.name)
			document := parseCDWorkflow(t, ".github/workflows/"+tc.name)
			on := yamlMap(t, document["on"], "on")
			if _, ok := on["push"]; ok {
				t.Fatal("fixed entry workflow must be workflow_dispatch-only")
			}
			inputs := yamlMap(t, yamlMap(t, on["workflow_dispatch"], "workflow_dispatch")["inputs"], "inputs")
			if len(inputs) != 1 {
				t.Fatalf("workflow inputs = %v, want only components", inputs)
			}
			if _, ok := inputs["components"]; !ok {
				t.Fatal("components must be the only workflow input")
			}
			job := yamlMap(t, yamlMap(t, document["jobs"], "jobs")[tc.job], "job")
			if job["if"] != "github.ref == 'refs/heads/"+tc.branch+"'" {
				t.Fatalf("job ref guard = %#v", job["if"])
			}
			if job["uses"] != "./.github/workflows/cd.yml" {
				t.Fatalf("job uses = %#v", job["uses"])
			}
			with := yamlMap(t, job["with"], "with")
			for key, want := range map[string]string{
				"environment": tc.environment, "config_environment": tc.config,
				"source_ref": tc.branch, "config_path": "deploy/environments/" + tc.config + ".yaml",
			} {
				if with[key] != want {
					t.Fatalf("with.%s = %#v, want %q", key, with[key], want)
				}
			}
			if with["source_sha"] != "${{ github.sha }}" {
				t.Fatalf("with.source_sha = %#v", with["source_sha"])
			}
			if strings.Contains(source, "inputs.environment") || strings.Contains(source, "inputs.config") || strings.Contains(source, "inputs.ref") || strings.Contains(source, "inputs.source_sha") {
				t.Fatal("fixed entry workflow exposes mutable authority inputs")
			}
		})
	}
}

func TestSharedCDOrchestratorOrdersValidationRollbackAndMutation(t *testing.T) {
	source := readCDFile(t, ".github/workflows/cd.yml")
	script := readCDFile(t, "deploy/cd.sh")
	document := parseCDWorkflow(t, ".github/workflows/cd.yml")
	jobs := yamlMap(t, document["jobs"], "jobs")
	mutate := yamlMap(t, jobs["mutate"], "mutate")
	if mutate["needs"] != "plan" || mutate["environment"] != "${{ inputs.environment }}" || mutate["if"] != "needs.plan.result == 'success'" {
		t.Fatalf("mutation job gates = %#v", mutate)
	}
	planStart := strings.Index(source, "  plan:")
	planEnd := strings.Index(source, "  mutate:")
	if planStart < 0 || planEnd < 0 || strings.Contains(source[planStart:planEnd], "environment:") {
		t.Fatal("config validation job must not acquire the protected environment")
	}
	for _, marker := range []string{
		"Checkout exact source SHA", "ref: ${{ inputs.source_sha }}",
		"Freeze Auth rollback handle", "id: rollback_upload", "Upload durable rollback artifact",
		"Mutate Auth", "steps.rollback_upload.outcome == 'success'",
		"Reconcile Auth", "Upload normalized CD evidence",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("shared workflow missing %q", marker)
		}
	}
	common := readCDFile(t, "deploy/components/common.sh")
	if !strings.Contains(script+common, "go run ./cmd/deploy_config") {
		t.Fatal("shared CD script must invoke the canonical config loader")
	}
	if !(strings.Index(source, "Upload durable rollback artifact") < strings.Index(source, "      - name: Mutate Auth")) {
		t.Fatal("durable rollback upload must precede every mutation")
	}
	for _, forbidden := range []string{"run jobs execute", "add-iam-policy-binding", "remove-iam-policy-binding", "set-iam-policy"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("shared workflow contains forbidden provider mutation %q", forbidden)
		}
	}
}

func TestSharedCDRunBlocksAreShellValid(t *testing.T) {
	source := readCDFile(t, ".github/workflows/cd.yml")
	parseCDWorkflow(t, ".github/workflows/cd.yml")
	blocks := regexp.MustCompile(`(?m)^\s+run: \|\n((?:\s{10,}.+\n?)+)`).FindAllStringSubmatch(source, -1)
	if len(blocks) == 0 {
		t.Fatal("shared workflow has no executable run blocks")
	}
	expression := regexp.MustCompile(`\$\{\{[\s\S]*?\}\}`)
	for _, block := range blocks {
		body := strings.TrimSpace(regexp.MustCompile(`(?m)^ {10}`).ReplaceAllString(block[1], ""))
		body = expression.ReplaceAllString(body, "workflow-expression")
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(body)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("shared run block is not shell-valid: %v\n%s", err, output)
		}
	}
}

func TestCDPreflightIsReadOnlyAndPrecedesRollbackFreeze(t *testing.T) {
	workflow := readCDFile(t, ".github/workflows/cd.yml")
	script := readCDFile(t, "deploy/cd.sh")
	if !strings.Contains(workflow, "run: bash deploy/cd.sh preflight-shared") || !strings.Contains(script, "preflight()") {
		t.Fatal("shared CD must run the common read-only preflight")
	}
	preflight := script + readCDFile(t, "deploy/components/common.sh") + readCDFile(t, "deploy/components/auth.sh") + readCDFile(t, "deploy/components/bff.sh") + readCDFile(t, "deploy/components/worker.sh")
	for _, marker := range []string{
		"gcloud iam service-accounts describe", "gcloud artifacts repositories describe", "gcloud firestore databases describe",
		"secrets get-iam-policy", "run services get-iam-policy", "run jobs get-iam-policy",
		"roles/secretmanager.secretAccessor", "roles/run.invoker", "roles/run.jobsExecutorWithOverrides",
	} {
		if !strings.Contains(preflight, marker) {
			t.Fatalf("read-only preflight missing %q", marker)
		}
	}
	for _, forbidden := range []string{"add-iam-policy-binding", "remove-iam-policy-binding", "set-iam-policy", "run jobs execute"} {
		if strings.Contains(preflight, forbidden) {
			t.Fatalf("read-only preflight contains mutation %q", forbidden)
		}
	}
	if !(strings.Index(workflow, "run: bash deploy/cd.sh preflight-shared") < strings.Index(workflow, "Freeze Auth rollback handle")) {
		t.Fatal("preflight must precede rollback freeze")
	}
}

func TestCDPlanBindsExactSourceCIAndQueryAuthority(t *testing.T) {
	common := readCDFile(t, "deploy/components/common.sh")
	plan := common[strings.Index(common, "validate_inputs()"):strings.Index(common, "iam_binding_is_exact()")]
	for _, marker := range []string{
		"[[ \"$SOURCE_SHA\" =~ ^[0-9a-f]{40}$ ]]", "git fetch origin \"$SOURCE_REF\"",
		"git rev-parse HEAD", "git rev-parse \"origin/$SOURCE_REF\"",
		"actions/workflows/ci.yml/runs?head_sha=${SOURCE_SHA}&event=push&branch=${SOURCE_REF}",
		".event == \"push\"", ".head_branch == $ref", ".head_sha == $sha",
		".status == \"completed\"", ".conclusion == \"success\"", "run_id", "run_attempt",
		"go run ./cmd/deploy_config", "--config \"$ROOT/$CONFIG_PATH\"", "normalized",
	} {
		if !strings.Contains(plan, marker) {
			t.Fatalf("exact source/CI/query plan is missing %q", marker)
		}
	}
	planBody := common[strings.Index(common, "plan() {"):strings.Index(common, "iam_binding_is_exact()")]
	if strings.Contains(planBody, "gcloud run ") || strings.Contains(planBody, "docker ") || strings.Contains(planBody, "curl ") {
		t.Fatal("plan must be provider-mutation-free")
	}
}

func TestCDPreflightRevalidatesPinnedCIAttemptAndCanonicalSource(t *testing.T) {
	common := readCDFile(t, "deploy/components/common.sh")
	start := strings.Index(common, "strict_json()")
	end := strings.Index(common, "iam_binding_is_exact()")
	if start < 0 || end < start {
		t.Fatal("shared preflight is missing")
	}
	preflight := common[start:end]
	for _, marker := range []string{
		"revalidate_ci", "git fetch origin", "git rev-parse \"origin/$SOURCE_REF\"",
		"actions/runs/${run_id}", "attempts/${attempt_id}",
		"actions/runs/${run_id}/attempts/${attempt_id}/jobs",
		"run_attempt", "conclusion == \"success\"", "strict_json", "object_pairs_hook",
	} {
		if !strings.Contains(preflight, marker) {
			t.Fatalf("preflight is missing pinned CI/source revalidation %q", marker)
		}
	}
	if strings.Contains(preflight, "gcloud run deploy") || strings.Contains(preflight, "gcloud run services update-traffic") || strings.Contains(preflight, "gcloud run jobs update") {
		t.Fatal("CI/source revalidation must be provider-mutation-free")
	}
}

func TestProductionConsumesExactDEVReceiptsWithoutRebuild(t *testing.T) {
	script := readCDFile(t, "deploy/cd.sh")
	consumeStart := strings.Index(script, "consume_dev_images()")
	consumeEnd := -1
	if consumeStart >= 0 {
		consumeEnd = consumeStart + strings.Index(script[consumeStart:], "\n}\n\npreflight_shared")
	}
	if consumeStart < 0 || consumeEnd < consumeStart {
		t.Fatal("production receipt consumer is missing")
	}
	consume := script[consumeStart:consumeEnd]
	for _, marker := range []string{
		"deploy-dev.yml/runs?event=workflow_dispatch", "head_sha=${SOURCE_SHA}", "branch=develop",
		".event == \"workflow_dispatch\"", ".head_sha == $sha", ".conclusion == \"success\"",
		"gh run download", "cd-images-$SOURCE_SHA",
	} {
		if !strings.Contains(consume, marker) {
			t.Fatalf("production receipt consumer is missing %q", marker)
		}
	}
	if strings.Contains(consume, "gcloud builds") || strings.Contains(consume, "docker build") {
		t.Fatal("production must not rebuild DEV-built images")
	}
	common := readCDFile(t, "deploy/components/common.sh")
	imageStart := strings.Index(common, "image_for()")
	imageEnd := strings.Index(common, "\nredact_evidence()")
	if imageStart < 0 || imageEnd < imageStart {
		t.Fatal("component image identity helper is missing")
	}
	image := common[imageStart:imageEnd]
	if strings.Contains(image, ":latest") || !strings.Contains(image, "@sha256:") {
		t.Fatal("component image identity must be immutable")
	}
}

func TestCDBuildIdentityWorkerNonceAndNoExecution(t *testing.T) {
	script := readCDFile(t, "deploy/cd.sh")
	components := readCDFile(t, "deploy/components/common.sh") + readCDFile(t, "deploy/components/auth.sh") + readCDFile(t, "deploy/components/bff.sh") + readCDFile(t, "deploy/components/worker.sh")
	for _, marker := range []string{
		"_GIT_SHA=$SOURCE_SHA", "_GIT_BRANCH=$SOURCE_REF", "_GIT_TAG=",
		"nonce=$(printf '%032x' \"$(date -u +%s%N)\")", "--build-arg BUILD_NONCE=\"$nonce\"",
		"--target worker", "gcloud artifacts docker images describe", "@sha256:",
	} {
		if !strings.Contains(script+components, marker) {
			t.Fatalf("CD build identity contract missing %q", marker)
		}
	}
	if strings.Contains(script+components, "run jobs execute") || strings.Contains(script+components, "gcloud artifacts docker tags add") {
		t.Fatal("CD must not execute Worker Jobs or create mutable observability tags")
	}
}

func TestCDReadbackRollbackAndEvidenceContractsRemainTruthful(t *testing.T) {
	script := readCDFile(t, "deploy/cd.sh")
	components := readCDFile(t, "deploy/components/common.sh") + readCDFile(t, "deploy/components/auth.sh") + readCDFile(t, "deploy/components/bff.sh") + readCDFile(t, "deploy/components/worker.sh") + readCDFile(t, "deploy/components/frontend.sh")
	for _, marker := range []string{
		"normalize_service_readback", "runtime_service_account", "secret_references", "allowed_origins",
		"vpc_egress", "max_instances", "component_config", "normalize_worker_definition",
		"handles.worker.definition", "gcloud run jobs replace", "mutation_count", "mutation_components",
		"rollback_attempted", "rollback_result", "rollback_verified", "next_action", "redact_evidence", "<redacted>",
	} {
		if !strings.Contains(script+components, marker) {
			t.Fatalf("CD safety contract missing %q", marker)
		}
	}
	if strings.Contains(script, "provider_readback:true") || strings.Contains(script, "verified:true") {
		t.Fatal("evidence must not hardcode success")
	}
	if strings.Contains(script[strings.Index(script, "\nevidence() {"):], ".value") {
		t.Fatal("final evidence must not serialize provider plaintext values")
	}
}

func TestCDServiceAndWorkerDefinitionsUseReviewedRuntimeValues(t *testing.T) {
	for _, environment := range []string{"development", "production"} {
		source := readCDFile(t, "deploy/environments/"+environment+".yaml")
		if !strings.Contains(source, "network: default") || !strings.Contains(source, "subnet: default") || !strings.Contains(source, "vpc_egress: private-ranges-only") || !strings.Contains(source, "ingress: all") {
			t.Fatalf("%s environment lost reviewed network values", environment)
		}
		if !strings.Contains(source, "runtime_service_account:") || !strings.Contains(source, "secret_references:") || !strings.Contains(source, "args:") {
			t.Fatalf("%s environment lost runtime/secret/Worker definition inputs", environment)
		}
	}
	workflows := readCDFile(t, ".github/workflows/cd.yml")
	if strings.Contains(workflows, "QUERY_STAGE_CONFIG_PATH:") || strings.Contains(workflows, "QUERY_STAGE_CONFIG_REVISION:") || strings.Contains(workflows, "QUERY_STAGE_CONFIG_DIGEST:") {
		t.Fatal("query identity must be owned by the loader/config, not workflow literals")
	}
}

func TestCIWorkflowKeepsCanonicalBranchesAndVersionGate(t *testing.T) {
	source := readCDFile(t, ".github/workflows/ci.yml")
	for _, marker := range []string{
		"on:\n  push:\n    branches: [main, develop]", "pull_request:",
		"go run ./cmd/versioncheck VERSION", "python3 ../../scripts/test_cd_contract.py",
		"go vet ./...", "go test ./... -v -count=1 -race",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("canonical CI workflow missing %q", marker)
		}
	}
}

func TestCloudRunJobIAMValidatorRejectsAmbiguousPoliciesWithoutLeakingMembers(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		wantErr bool
	}{
		{name: "valid", policy: `{"bindings":[{"role":"roles/run.viewer","members":["serviceAccount:lwc-pipeline-dev@llm-wiki-cloud.iam.gserviceaccount.com"]}]}`},
		{name: "wrong role", policy: `{"bindings":[{"role":"roles/run.admin","members":["serviceAccount:lwc-pipeline-dev@llm-wiki-cloud.iam.gserviceaccount.com"]}]}`, wantErr: true},
		{name: "duplicate binding", policy: `{"bindings":[{"role":"roles/run.viewer","members":[]},{"role":"roles/run.viewer","members":["serviceAccount:lwc-pipeline-dev@llm-wiki-cloud.iam.gserviceaccount.com"]}]}`, wantErr: true},
		{name: "malformed", policy: `{`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command("python3", "scripts/validate_cloud_run_job_iam_policy.py")
			command.Stdin = strings.NewReader(tc.policy)
			output, err := command.CombinedOutput()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validator error = %v, wantErr=%t; output=%s", err, tc.wantErr, output)
			}
			if strings.Contains(string(output), "lwc-pipeline-dev@llm-wiki-cloud.iam.gserviceaccount.com") {
				t.Fatal("validator must not print policy members")
			}
		})
	}
}

func TestCloudBuildAndAuthDockerfilesKeepImmutableIdentityInputs(t *testing.T) {
	for _, name := range []string{"apps/bff/cloudbuild-bff.yaml", "apps/bff/cloudbuild-auth.yaml"} {
		source := readCDFile(t, name)
		for _, marker := range []string{"APP_VERSION=${_APP_VERSION}", "GIT_SHA=${_GIT_SHA}", "GIT_BRANCH=${_GIT_BRANCH}", "GIT_TAG=${_GIT_TAG}", "${_IMAGE}"} {
			if !strings.Contains(source, marker) {
				t.Fatalf("%s missing %q", name, marker)
			}
		}
	}
}

func TestDockerfilesPreserveBuildIdentityContracts(t *testing.T) {
	contents := readCDFile(t, "apps/bff/Dockerfile")
	for _, marker := range []string{
		"ARG APP_VERSION=dev", "ARG GIT_SHA=unknown", "ARG GIT_BRANCH=unknown", "ARG GIT_TAG=",
		"org.opencontainers.image.revision=${GIT_SHA}", "io.llm-wiki.image.tag=${GIT_SHA}",
		"internal/buildinfo.ProductVersion=${APP_VERSION}", "internal/buildinfo.GitSHA=${GIT_SHA}",
	} {
		if !strings.Contains(contents, marker) {
			t.Fatalf("BFF Dockerfile missing %q", marker)
		}
	}
	if strings.Contains(contents, "git rev-parse") || strings.Contains(contents, ".git/") {
		t.Fatal("Dockerfile must not derive identity from a Git context")
	}
	auth := readCDFile(t, "apps/bff/Dockerfile.auth")
	if !strings.Contains(auth, "./cmd/auth") || !strings.Contains(auth, "internal/buildinfo.ImageTag=${GIT_SHA}") {
		t.Fatal("Auth Dockerfile must preserve its distinct immutable build identity")
	}
}
