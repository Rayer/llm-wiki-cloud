package clirelease

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const SchemaVersion = 1

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

type Asset struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	AssetName   string `json:"asset_name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url,omitempty"`
}

type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	Version       string  `json:"version"`
	SourceCommit  string  `json:"source_commit"`
	Channel       string  `json:"channel"`
	Assets        []Asset `json:"assets"`
}

var targets = map[string]struct{}{
	"darwin/arm64":  {},
	"darwin/amd64":  {},
	"windows/amd64": {},
	"linux/amd64":   {},
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", manifest.SchemaVersion)
	}
	if !commitPattern.MatchString(manifest.SourceCommit) {
		return errors.New("source_commit must be a full lowercase Git SHA")
	}
	if manifest.Version == "dev" && manifest.Channel == "dev" {
		for _, asset := range manifest.Assets {
			if asset.DownloadURL != "" {
				return errors.New("dev assets cannot have download URLs")
			}
		}
	} else {
		prerelease, err := IsPrerelease(manifest.Version)
		if err != nil {
			return err
		}
		switch manifest.Channel {
		case "stable":
			if prerelease {
				return errors.New("stable channel cannot use a prerelease version")
			}
		case "prerelease":
			if !prerelease {
				return errors.New("prerelease channel requires a prerelease version")
			}
		default:
			return fmt.Errorf("unsupported release channel %q", manifest.Channel)
		}
	}
	if len(manifest.Assets) != len(targets) {
		return fmt.Errorf("manifest must contain exactly %d platform assets", len(targets))
	}
	seen := make(map[string]struct{}, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		platform := asset.OS + "/" + asset.Arch
		if _, ok := targets[platform]; !ok {
			return fmt.Errorf("unsupported asset target %q", platform)
		}
		if _, ok := seen[platform]; ok {
			return fmt.Errorf("duplicate asset target %q", platform)
		}
		seen[platform] = struct{}{}
		if asset.AssetName == "" || path.Base(asset.AssetName) != asset.AssetName {
			return fmt.Errorf("invalid asset_name %q", asset.AssetName)
		}
		extension := ".tar.gz"
		if asset.OS == "windows" {
			extension = ".zip"
		}
		if !strings.Contains(asset.AssetName, manifest.Version) || !strings.HasSuffix(asset.AssetName, extension) {
			return fmt.Errorf("asset_name %q does not match version and target package format", asset.AssetName)
		}
		if asset.Size <= 0 {
			return fmt.Errorf("asset %q must have a positive size", asset.AssetName)
		}
		if !hashPattern.MatchString(asset.SHA256) {
			return fmt.Errorf("asset %q has an invalid SHA-256", asset.AssetName)
		}
		if asset.DownloadURL != "" {
			parsed, err := url.ParseRequestURI(asset.DownloadURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("asset %q has an invalid download_url", asset.AssetName)
			}
		}
	}
	return nil
}

func IsPrerelease(version string) (bool, error) {
	match := semverPattern.FindStringSubmatch(version)
	if match == nil {
		return false, fmt.Errorf("invalid SemVer %q", version)
	}
	return match[4] != "", nil
}

func CompareVersions(left, right string) (int, error) {
	leftParts, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range leftParts.core {
		if leftParts.core[index] < rightParts.core[index] {
			return -1, nil
		}
		if leftParts.core[index] > rightParts.core[index] {
			return 1, nil
		}
	}
	if len(leftParts.pre) == 0 && len(rightParts.pre) > 0 {
		return 1, nil
	}
	if len(leftParts.pre) > 0 && len(rightParts.pre) == 0 {
		return -1, nil
	}
	for index := 0; index < len(leftParts.pre) && index < len(rightParts.pre); index++ {
		leftID, rightID := leftParts.pre[index], rightParts.pre[index]
		leftNumber, leftErr := strconv.ParseUint(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightID, 10, 64)
		if leftErr == nil && rightErr == nil {
			if leftNumber < rightNumber {
				return -1, nil
			}
			if leftNumber > rightNumber {
				return 1, nil
			}
			continue
		}
		if leftErr == nil {
			return -1, nil
		}
		if rightErr == nil {
			return 1, nil
		}
		if leftID < rightID {
			return -1, nil
		}
		if leftID > rightID {
			return 1, nil
		}
	}
	if len(leftParts.pre) < len(rightParts.pre) {
		return -1, nil
	}
	if len(leftParts.pre) > len(rightParts.pre) {
		return 1, nil
	}
	return 0, nil
}

type parsedVersion struct {
	core [3]uint64
	pre  []string
}

func parseVersion(version string) (parsedVersion, error) {
	match := semverPattern.FindStringSubmatch(version)
	if match == nil {
		return parsedVersion{}, fmt.Errorf("invalid SemVer %q", version)
	}
	var parsed parsedVersion
	for index := 0; index < 3; index++ {
		value, err := strconv.ParseUint(match[index+1], 10, 64)
		if err != nil {
			return parsedVersion{}, fmt.Errorf("invalid SemVer %q", version)
		}
		parsed.core[index] = value
	}
	if match[4] != "" {
		parsed.pre = strings.Split(match[4], ".")
	}
	return parsed, nil
}
