package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBYOKFailureDiagnosticKeepsOnlyActionablePersonalReason(t *testing.T) {
	command := exec.Command("python3", "testdata/byok_failure_classification_test.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("embedded BYOK classifier test failed: %v\n%s", err, output)
	}

	markerPath := filepath.Join(t.TempDir(), "byok-failure")
	if err := os.WriteFile(markerPath, []byte(`{"reason":"authentication"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	failure := &workerFailure{
		cause: errors.New("synthetic-secret"),
		Stage: failureStageSyntoRun, Class: failureClassChildExit, Child: failureChildRun,
	}
	cfg := workerConfig{PipelineProfileSource: "personal", BYOKFailureMarkerPath: markerPath}
	raw, err := marshalFailureDiagnosticMetaWithConfig(failure, "exec-1", []string{"synthetic-secret"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret") || strings.Contains(string(raw), `"error":`) {
		t.Fatalf("diagnostic leaked provider response: %s", raw)
	}
	diagnostic, err := decodeFailureDiagnostic(raw)
	if err != nil || diagnostic.BYOKFailureReason != "authentication" {
		t.Fatalf("diagnostic reason = %q, err = %v", diagnostic.BYOKFailureReason, err)
	}

	for _, test := range []struct {
		name   string
		config workerConfig
		stage  failureStage
		class  failureErrorClass
	}{
		{name: "shared profile", config: workerConfig{PipelineProfileSource: "shared", BYOKFailureMarkerPath: markerPath}, stage: failureStageSyntoRun, class: failureClassChildExit},
		{name: "timeout", config: cfg, stage: failureStageSyntoRun, class: failureClassTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &workerFailure{cause: errors.New("synthetic failure"), Stage: test.stage, Class: test.class, Child: failureChildRun}
			got := failureDiagnosticForConfig(err, test.config)
			if got.BYOKFailureReason != "" {
				t.Fatalf("non-actionable failure acquired BYOK reason %q", got.BYOKFailureReason)
			}
		})
	}

	if err := os.WriteFile(markerPath, []byte(`{"reason":"rate_limited"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := failureDiagnosticForConfig(failure, cfg); got.BYOKFailureReason != "" {
		t.Fatalf("transient marker acquired BYOK reason %q", got.BYOKFailureReason)
	}
}
