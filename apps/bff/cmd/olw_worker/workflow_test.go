package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestDeploymentEngineR2WorkflowContract(t *testing.T) {
	command := exec.Command("python3", "../../../../scripts/test_engine_workflow.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("r2 workflow contract: %v\n%s", err, output)
	}
}
