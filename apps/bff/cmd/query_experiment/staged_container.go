//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set only by the experiment build target; native debug builds remain usable.
var experimentRevision, experimentDirty, experimentBuildID string
var stagedSyntoVersion = "0.7.0"

type stagedLaunchMetadata struct {
	Schema      string `json:"schema"`
	ImageDigest string `json:"image_digest"`
	Platform    string `json:"platform"`
}

type stagedEnvironmentReceipt struct {
	Schema         string                `json:"schema"`
	Mode           string                `json:"mode"`
	SourceRevision string                `json:"source_revision"`
	SourceDirty    string                `json:"source_dirty"`
	BuildID        string                `json:"build_id"`
	Platform       string                `json:"platform"`
	Launcher       *stagedLaunchMetadata `json:"launcher,omitempty"`
	MetadataDigest string                `json:"launcher_metadata_sha256,omitempty"`
}

// The OCI digest cannot be discovered from inside the image. The launcher must
// supply runtime-inspected metadata; this is an assertion, not self-attestation.
func stagedContainerBoundary(o stagedOptions) (*stagedEnvironmentReceipt, error) {
	r := &stagedEnvironmentReceipt{Schema: "lwc-experiment-environment-v1", Mode: "native", Platform: runtime.GOOS + "/" + runtime.GOARCH, SourceRevision: "unknown", SourceDirty: "unknown", BuildID: "native"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				r.SourceRevision = s.Value
			case "vcs.modified":
				r.SourceDirty = s.Value
			}
		}
	}
	if experimentBuildID != "" {
		r.Mode, r.SourceRevision, r.SourceDirty, r.BuildID = "container", experimentRevision, experimentDirty, experimentBuildID
		if o.ExperimentRoot != "/experiment" || o.LauncherMetadata == "" {
			return nil, errors.New("experiment image requires /experiment mount and --launcher-metadata from external runtime inspection")
		}
	}
	if o.ExperimentRoot == "" {
		if o.LauncherMetadata != "" {
			return nil, errors.New("launcher metadata requires --experiment-root")
		}
		return r, nil
	}
	root, err := filepath.Abs(o.ExperimentRoot)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("experiment root must be an existing canonical directory")
	}
	for _, path := range []string{o.Output, o.Raw, o.Snapshot, o.Fork, o.Config, o.Cases, o.LauncherMetadata} {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil || strings.HasPrefix(path, "gs://") || !strings.HasPrefix(absolute, root+string(os.PathSeparator)) {
			return nil, errors.New("experiment input/config/metadata/output must be children of the mounted root; prepare cloud snapshots outside the container")
		}
		// Output does not exist yet. Resolve its existing parent instead.
		check := absolute
		if path == o.Output {
			check = filepath.Dir(absolute)
		}
		resolved, err := filepath.EvalSymlinks(check)
		if err != nil || resolved != check {
			return nil, errors.New("experiment paths must be canonical with existing parents")
		}
	}
	if o.LauncherMetadata != "" {
		data, err := stagedReadFile(o.LauncherMetadata)
		if err != nil {
			return nil, errors.New("launcher metadata unavailable")
		}
		var metadata stagedLaunchMetadata
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&metadata) != nil || ensureJSONEOF(dec) != nil || metadata.Schema != "lwc-container-launch-v1" || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(metadata.ImageDigest) || metadata.Platform != r.Platform {
			return nil, errors.New("invalid launcher metadata: require schema, runtime-inspected sha256 image digest and matching OS/architecture")
		}
		r.Launcher, r.MetadataDigest = &metadata, stagedHash(data)
	}
	return r, nil
}

func stagedPublicVersion(data []byte) (string, error) {
	output := strings.TrimSpace(string(data))
	version := strings.TrimPrefix(output, "synto, version ")
	if version == output || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return "", errors.New("unrecognized public Synto version output")
	}
	return version, nil
}

// Reject alternate command modes in the experiment binary before any cloud or
// legacy configuration code can run. ENTRYPOINT is convenient, not a boundary.
func stagedContainerCommand(args []string) bool {
	return experimentBuildID == "" || len(args) > 0 && args[0] == "staged"
}

func stagedContainerTemp(output string) error {
	if experimentBuildID == "" {
		return nil
	}
	if err := os.Mkdir(filepath.Join(output, "tmp"), 0700); err != nil {
		return err
	}
	return os.Setenv("TMPDIR", filepath.Join(output, "tmp"))
}
