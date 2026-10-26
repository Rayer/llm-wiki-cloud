//go:build lwc_local_synto_runtime

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	handlerV1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

func TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider(t *testing.T) {
	wantPython := os.Getenv("LOCAL_CLOUD_PYTHON")
	if wantPython == "" || !filepath.IsAbs(wantPython) {
		t.Fatal("LOCAL_CLOUD_PYTHON must identify the worktree Synto interpreter")
	}
	gotPython, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("resolve worker python3: %v", err)
	}
	if filepath.Clean(gotPython) != filepath.Clean(wantPython) {
		t.Fatalf("worker python3=%q, want worktree venv %q", gotPython, wantPython)
	}
	for _, key := range []string{"LLM_API_KEY", "DEEPSEEK_API_KEY", "SYNTO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "TYPESAFE_API_KEY", "TYPESAFE_JEV_API_KEY"} {
		if value := os.Getenv(key); value != "" {
			t.Fatalf("%s must be unset for this no-provider runtime regression", key)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := execOLWCommand(context.Background(), t.TempDir(), []string{"--version"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("run pinned Synto version through worker adapter: %v; stderr=%q", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "synto, version 0.7.0" {
		t.Fatalf("worker adapter reported %q, want pinned Synto 0.7.0", got)
	}

	stdout.Reset()
	stderr.Reset()
	err = execOLWCommand(context.Background(), t.TempDir(), []string{"lwc361-command-does-not-exist"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("unknown Synto command unexpectedly succeeded")
	}
	if output := stdout.String() + stderr.String(); !strings.Contains(output, "No such command") {
		t.Fatalf("unknown Synto command did not fail at the CLI boundary: %q", output)
	}
}

func TestLocalSyntoProvenanceRoundTripsThroughPublicationAndBFF(t *testing.T) {
	python := os.Getenv("LOCAL_CLOUD_PYTHON")
	if python == "" || !filepath.IsAbs(python) {
		t.Fatal("LOCAL_CLOUD_PYTHON must identify the worktree Synto interpreter")
	}
	gotPython, err := exec.LookPath("python3")
	if err != nil || filepath.Clean(gotPython) != filepath.Clean(python) {
		t.Fatalf("worker python3=%q, want pinned interpreter %q (err=%v)", gotPython, python, err)
	}
	for _, key := range []string{"LLM_API_KEY", "DEEPSEEK_API_KEY", "SYNTO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "TYPESAFE_API_KEY", "TYPESAFE_JEV_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if os.Getenv(key) != "" {
			t.Fatalf("%s must be unset for the local provenance round trip", key)
		}
	}
	artifactPath := filepath.Join(t.TempDir(), "alpha.md")
	apiResponsePath := os.Getenv("LWC426_API_RESPONSE_OUTPUT")
	if apiResponsePath == "" || !filepath.IsAbs(apiResponsePath) {
		t.Fatal("LWC426_API_RESPONSE_OUTPUT must be an absolute scratch path")
	}
	cmd := exec.Command(python, "testdata/lwc426_provenance_smoke.py")
	cmd.Env = append(os.Environ(), "LWC426_ARTIFACT_OUTPUT="+artifactPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned Synto producer smoke failed: %v\n%s", err, output)
	}
	producerArtifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read pinned Synto article: %v", err)
	}

	workspace := t.TempDir()
	writeFreshSyntoRequiredOutputs(t, workspace)
	if err := os.WriteFile(filepath.Join(workspace, "wiki", "alpha.md"), producerArtifact, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	projectRoot := filepath.Join(root, "users", "user", "projects", "project")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := stageWorkspaceOutputs(workspace, projectRoot, "")
	if err != nil {
		t.Fatalf("stage producer article: %v", err)
	}
	if err := publishStagedOutputs(projectRoot, stage); err != nil {
		t.Fatalf("publish producer article: %v", err)
	}
	publishedArtifact, err := os.ReadFile(filepath.Join(projectRoot, "wiki", "alpha.md"))
	if err != nil || !bytes.Equal(publishedArtifact, producerArtifact) {
		t.Fatalf("published article differs from producer artifact: err=%v", err)
	}

	gin.SetMode(gin.TestMode)
	h := handlerV1.New(localfs.New(root), nil, search.NewIndex(), nil, nil, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/concepts/alpha", nil)
	c.Params = gin.Params{{Key: "id", Value: "alpha"}}
	c.Set("userID", "user")
	c.Set("projectID", "project")
	h.GetConcept(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GetConcept status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response handler.ConceptDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GetConcept response: %v body=%q", err, recorder.Body.String())
	}
	if response.Raw != string(publishedArtifact) {
		t.Fatal("BFF response raw article differs from the published producer artifact")
	}
	lineage, ok := response.Frontmatter["lineage"].([]interface{})
	if !ok || len(lineage) != 3 {
		t.Fatalf("API lineage=%#v", response.Frontmatter["lineage"])
	}
	latest, ok := lineage[len(lineage)-1].(map[string]interface{})
	if !ok || latest["compile_run"] != "01JAZ5N7Y3K8M2Q4R6T9VWXAC2" {
		t.Fatalf("API latest compile_run=%#v", latest)
	}
	generator, ok := latest["generator"].(map[string]interface{})
	if !ok || generator["implementation"] != "synto" || generator["version"] != "0.7.0" {
		t.Fatalf("API latest generator=%#v", latest["generator"])
	}
	if prior, ok := lineage[0].(map[string]interface{}); !ok || prior["compile_run"] != "01JAZ5N7Y3K8M2Q4R6T9VWXAC3" {
		t.Fatalf("API carried lineage=%#v", lineage[0])
	}
	if err := os.MkdirAll(filepath.Dir(apiResponsePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiResponsePath, recorder.Body.Bytes(), 0o600); err != nil {
		t.Fatalf("write same-artifact BFF response for detail UI test: %v", err)
	}
}
