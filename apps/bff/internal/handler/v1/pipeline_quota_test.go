package v1

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
)

func TestHTTPStatusForReason(t *testing.T) {
	cases := []struct {
		reason pipelinequota.Reason
		want   int
	}{
		{pipelinequota.ReasonDemo, http.StatusForbidden},
		{pipelinequota.ReasonDailyLimit, http.StatusTooManyRequests},
		{pipelinequota.ReasonCooldown, http.StatusTooManyRequests},
		{pipelinequota.ReasonAlreadyRunning, http.StatusConflict},
		{pipelinequota.ReasonNoNewRaw, http.StatusConflict},
		{pipelinequota.ReasonNone, http.StatusConflict},
		{pipelinequota.Reason("unknown"), http.StatusConflict},
	}
	for _, tc := range cases {
		if got := httpStatusForReason(tc.reason); got != tc.want {
			t.Errorf("httpStatusForReason(%q) = %d, want %d", tc.reason, got, tc.want)
		}
	}
}

func TestIsDemoUser(t *testing.T) {
	h := &Handler{}
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{" demo-1 ", "demo-2", ""})
	if !h.isDemoUser("demo-1") {
		t.Fatal("expected demo-1 to be demo")
	}
	if !h.isDemoUser("demo-2") {
		t.Fatal("expected demo-2 to be demo")
	}
	if h.isDemoUser("other") {
		t.Fatal("other should not be demo")
	}
	if h.isDemoUser("") {
		t.Fatal("empty user should not be demo")
	}
}

func TestPipelineLimitsDefaults(t *testing.T) {
	h := &Handler{}
	lim := h.pipelineLimits()
	if lim.DailyLimit != 2 || lim.Cooldown != time.Hour || lim.MinNewRaw != 1 {
		t.Fatalf("defaults = %+v, want daily=2 cooldown=1h min=1", lim)
	}

	h.SetPipelineQuotaConfig(4, 90, 2, nil)
	lim = h.pipelineLimits()
	if lim.DailyLimit != 4 || lim.Cooldown != 90*time.Second || lim.MinNewRaw != 2 {
		t.Fatalf("configured = %+v", lim)
	}
}

func TestLoadedCooldownOverrideReachesQuotaConsumer(t *testing.T) {
	cooldown := os.Getenv("PIPELINE_COOLDOWN_SECONDS")
	if os.Getenv("LWC373_CHILD_ENV_TEST") == "1" {
		if cooldown != "60" {
			t.Fatalf("managed child cooldown=%q, want 60", cooldown)
		}
	} else {
		cooldown = "60"
		t.Setenv("PIPELINE_COOLDOWN_SECONDS", cooldown)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("pipeline_cooldown_seconds = 900\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	if cfg.PipelineCooldownSeconds != 60 {
		t.Fatalf("config cooldown=%d, want managed environment override 60", cfg.PipelineCooldownSeconds)
	}
	h := &Handler{}
	h.SetPipelineQuotaConfig(cfg.PipelineDailyLimit, cfg.PipelineCooldownSeconds, cfg.PipelineMinNewRaw, nil)
	if got := h.pipelineLimits().Cooldown; got != 60*time.Second {
		t.Fatalf("quota consumer cooldown=%s, want 60s", got)
	}
}

func TestEvaluateQuotaUnenforcedWithoutStore(t *testing.T) {
	h := &Handler{}
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{"demo-user"})

	snap, reserved, err := h.evaluateQuota(t.Context(), "demo-user", "proj", "")
	if err != nil {
		t.Fatalf("evaluateQuota: %v", err)
	}
	if reserved {
		t.Fatal("reserved should be false without quota store")
	}
	if snap.Enforced {
		t.Fatal("expected Enforced=false without firestore/quota store")
	}
	if !snap.Allowed {
		t.Fatalf("expected Allowed when unenforced, got reason=%q", snap.Reason)
	}
}

func TestPipelineRunningFallbackLogIsSanitized(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(previous)
		log.SetFlags(previousFlags)
	}()

	h := &Handler{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider denied users/tenant-secret/projects/project-secret/executions/execution-secret")
	})}}
	running, err := h.isPipelineRunning(t.Context(), "tenant-secret", "project-secret")
	if err != nil {
		t.Fatalf("isPipelineRunning() error = %v, want lock-only fallback", err)
	}
	if running {
		t.Fatal("isPipelineRunning() = true, want false without a lock")
	}
	if got := output.String(); got != "pipeline activity unavailable; using lock only\n" {
		t.Fatalf("log = %q, want fixed sanitized event", got)
	}
}
