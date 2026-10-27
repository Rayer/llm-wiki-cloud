//go:build byokprofilefixture

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileFixturePythonHookCompiles(t *testing.T) {
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	python := os.Getenv("SYNTO_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), '<fixture>', 'exec')")
	cmd.Stdin = bytes.NewBufferString(profileFixtureScript(syntoExecutionAdapter))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile profile fixture adapter: %v: %s", err, output)
	}
}

func TestProfileFixtureTransportHandlesOpenAIHealthcheck(t *testing.T) {
	t.Setenv("LWC_PROFILE_FIXTURE_TRANSPORT", "1")
	receipt := filepath.Join(t.TempDir(), "receipts.tsv")
	t.Setenv("LWC_PROFILE_FIXTURE_RECEIPT_FILE", receipt)
	python := os.Getenv("SYNTO_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	probe := profileFixturePythonHook + `
import httpx
response = httpx.get("https://fixture.example/v1/models")
assert response.status_code == 200, response.status_code
assert response.json() == {"object": "list", "data": []}, response.text
`
	cmd := exec.Command(python, "-c", probe)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture OpenAI healthcheck: %v: %s", err, output)
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("read fixture healthcheck receipt: %v", err)
	}
	if !strings.Contains(string(data), "openai_models_healthcheck\tfixture-embedding-model\t/v1/models") {
		t.Fatalf("fixture healthcheck receipt = %q", string(data))
	}
}
