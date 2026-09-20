//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStagedExperimentDirectory(t *testing.T) {
	root, o := stagedTestOptions(t)
	o.ExperimentRoot = root
	o.Output = filepath.Join(root, "run")
	o.LauncherMetadata = filepath.Join(root, "image.json")
	metadata := stagedLaunchMetadata{"lwc-container-launch-v1", "sha256:" + strings.Repeat("a", 64), runtime.GOOS + "/" + runtime.GOARCH}
	stagedTestWrite(t, o.LauncherMetadata, stagedJSON(metadata))
	r := stagedTestRun(t, o, &stagedMock{})
	if r.Environment.Launcher.ImageDigest != metadata.ImageDigest || r.Environment.MetadataDigest == "" || r.Source.RawDigest == "" || r.SourceConfigDigest == "" || r.Implementation["synto_version"] != "0.7.0" {
		t.Fatal("missing environment or input/config provenance")
	}
	var saved stagedReport
	b, err := os.ReadFile(filepath.Join(o.Output, "run.json"))
	if err != nil || json.Unmarshal(b, &saved) != nil || saved.Environment.Launcher.ImageDigest != metadata.ImageDigest {
		t.Fatal("metadata not persisted")
	}
	if _, err := runStaged(context.Background(), o, (&stagedMock{}).hooks(t)); err == nil {
		t.Fatal("existing child output overwritten")
	}
	for _, kind := range []string{"root-output", "outside-output", "outside-config", "cloud", "symlink", "missing-metadata", "tag-as-digest", "wrong-platform", "extra-field"} {
		t.Run(kind, func(t *testing.T) {
			v := o
			v.Output = filepath.Join(root, "new-run")
			stagedTestWrite(t, o.LauncherMetadata, stagedJSON(metadata))
			switch kind {
			case "root-output":
				v.Output = root
			case "outside-output":
				v.Output = filepath.Join(filepath.Dir(root), "escape")
			case "outside-config":
				v.Config = filepath.Join(stagedTestRoot(t), "config")
			case "cloud":
				v.Raw, v.Snapshot = "", "gs://llm-wiki-data-dev/users/u/projects/p"
			case "symlink":
				v.Config = filepath.Join(root, "linked-config")
				if err := os.Symlink(o.Config, v.Config); err != nil {
					t.Fatal(err)
				}
			case "missing-metadata":
				v.LauncherMetadata = filepath.Join(root, "missing")
			case "tag-as-digest":
				bad := metadata
				bad.ImageDigest = "local/experiment:latest"
				stagedTestWrite(t, o.LauncherMetadata, stagedJSON(bad))
			case "wrong-platform":
				bad := metadata
				bad.Platform = "wrong/architecture"
				stagedTestWrite(t, o.LauncherMetadata, stagedJSON(bad))
			case "extra-field":
				stagedTestWrite(t, o.LauncherMetadata, []byte(`{"schema":"lwc-container-launch-v1","secret":"must-not-copy"}`))
			}
			m := &stagedMock{}
			if _, err := runStaged(context.Background(), v, m.hooks(t)); err == nil || len(m.children) != 0 || len(m.stages) != 0 {
				t.Fatal("invalid boundary executed", err)
			}
		})
	}
}

func TestStagedContainerRequiresLauncherAndCommand(t *testing.T) {
	old := experimentBuildID
	experimentBuildID = "test-build"
	t.Cleanup(func() { experimentBuildID = old })
	for _, args := range [][]string{nil, {"local-platform"}, {"--service", "production"}} {
		if stagedContainerCommand(args) {
			t.Fatal("container accepted non-staged mode")
		}
	}
	if !stagedContainerCommand([]string{"staged"}) {
		t.Fatal("staged rejected")
	}
	_, o := stagedTestOptions(t)
	if _, err := stagedContainerBoundary(o); err == nil {
		t.Fatal("missing mount accepted")
	}
	o.ExperimentRoot = "/experiment"
	if _, err := stagedContainerBoundary(o); err == nil {
		t.Fatal("missing external digest accepted")
	}
}

func TestStagedSyntoVersionUpgrade(t *testing.T) {
	old := stagedSyntoVersion
	stagedSyntoVersion = "0.8.1"
	t.Cleanup(func() { stagedSyntoVersion = old })
	_, o := stagedTestOptions(t)
	o.To = "20"
	h := (&stagedMock{}).hooks(t)
	child := h.Child
	h.Child = func(ctx context.Context, argv, env []string, cwd string) ([]byte, error) {
		if argv[1] == "--version" {
			return []byte("synto, version 0.8.1\n"), nil
		}
		return child(ctx, argv, env, cwd)
	}
	r, err := runStaged(context.Background(), o, h)
	if err != nil || r.Implementation["synto_version"] != "0.8.1" {
		t.Fatal(r, err)
	}
	f := stagedTestFork(o, "index-upgrade")
	f.Only = "50"
	f.Fork = ""
	f.Snapshot = filepath.Join(o.Output, "snapshots", "20")
	if _, err := runStaged(context.Background(), f, h); err != nil {
		t.Fatal(err)
	}
	o.Output = filepath.Join(filepath.Dir(o.Output), "version-mismatch")
	r, err = runStaged(context.Background(), o, (&stagedMock{}).hooks(t))
	if err == nil || r == nil || r.Implementation["synto_version"] != "0.7.0" || len(r.Checkpoints) != 0 {
		t.Fatal("version mismatch was accepted or actual version lost", r, err)
	}
	for _, output := range []string{"latest", "unexpected secret", "synto, version 0.8.1\nextra"} {
		if _, err := stagedPublicVersion([]byte(output)); err == nil {
			t.Fatal("unsafe version output accepted")
		}
	}
}

func TestStagedContainerInvocation(t *testing.T) {
	// Verify the actual image entrypoint parses into the existing staged runner.
	b, err := os.ReadFile("../olw_worker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	start := strings.Index(text, "FROM worker-runtime AS experiment\n")
	if start < 0 {
		t.Fatal("missing shared experiment target")
	}
	line := strings.Split(strings.Split(text[start:], "ENTRYPOINT ")[1], "\n")[0]
	var argv []string
	if json.Unmarshal([]byte(line), &argv) != nil {
		t.Fatal("invalid entrypoint")
	}
	argv = append(argv, "--output", "/experiment/run", "--raw", "/experiment/raw", "--query-config", "/experiment/query.json", "--launcher-metadata", "/experiment/image.json", "--to", "10")
	o, err := stagedParse(argv[2:], io.Discard)
	if err != nil || argv[0] != "/query_experiment" || argv[1] != "staged" || o.Output != "/experiment/run" || o.ExperimentRoot != "/experiment" || o.Worker != "/worker" {
		t.Fatal("unusable image invocation", o, err)
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "FROM worker-runtime AS worker") || !strings.Contains(text[:start], `ENTRYPOINT ["/worker"]`) || !strings.Contains(text, `${SYNTO_ARTIFACT_URL}#sha256=${SYNTO_SHA256}`) {
		t.Fatal("production default or verified artifact contract changed")
	}
}
