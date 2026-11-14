package releasenotes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion   = 1
	MaxIndexBytes   = 1 << 20
	MaxContentBytes = 64 << 10
	MaxVersions     = 1000
	ProductName     = "llm-wiki-cloud"
)

var (
	ErrUnavailable = errors.New("release notes unavailable")
	ErrNotFound    = errors.New("release notes not found")
	ErrInvalid     = errors.New("invalid release notes data")
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
)

type BuildInfo struct {
	Environment    string `json:"environment"`
	ProductVersion string `json:"product_version"`
	Commit         string `json:"commit"`
	Branch         string `json:"branch"`
	Tag            string `json:"tag"`
	ImageTag       string `json:"image_tag"`
	Service        string `json:"service"`
	Revision       string `json:"revision"`
}

type Index struct {
	SchemaVersion     int                `json:"schema_version"`
	Versions          []VersionRow       `json:"versions"`
	CurrentDeployment *CurrentDeployment `json:"current_deployment,omitempty"`
}

type VersionRow struct {
	ProductVersion string                `json:"product_version"`
	PublishedAt    string                `json:"published_at"`
	Locales        map[string]LocaleFile `json:"locales"`
}

type LocaleFile struct {
	SHA256     string `json:"sha256"`
	ObjectPath string `json:"object_path"`
}

// CurrentDeployment is the complete, closed publisher marker. It is
// intentionally separate from the TPM-authored package and public content.
type CurrentDeployment struct {
	SchemaVersion            int    `json:"schema_version"`
	Environment              string `json:"environment"`
	ProductVersion           string `json:"product_version"`
	Commit                   string `json:"commit"`
	Branch                   string `json:"branch"`
	Tag                      string `json:"tag"`
	ImageTag                 string `json:"image_tag"`
	ImageDigest              string `json:"image_digest"`
	Service                  string `json:"service"`
	ServiceUID               string `json:"service_uid"`
	ServiceGeneration        int64  `json:"service_generation"`
	Revision                 string `json:"revision"`
	Operation                string `json:"operation"`
	DeploymentEvidenceDigest string `json:"deployment_evidence_digest"`
}

type Content struct {
	SchemaVersion  int     `json:"schema_version"`
	Product        string  `json:"product"`
	ProductVersion string  `json:"product_version"`
	Locale         string  `json:"locale"`
	Entries        []Entry `json:"entries"`
}

type Entry struct {
	Title       string   `json:"title"`
	WhatChanged string   `json:"what_changed"`
	UserBenefit string   `json:"user_benefit"`
	WhereToUse  string   `json:"where_to_use"`
	Limitations []string `json:"limitations"`
}

type VersionSummary struct {
	ProductVersion string   `json:"product_version"`
	PublishedAt    string   `json:"published_at"`
	Locales        []string `json:"locales"`
}

type IndexResponse struct {
	SchemaVersion         int              `json:"schema_version"`
	Environment           string           `json:"environment"`
	RuntimeProductVersion string           `json:"runtime_product_version"`
	CurrentStatus         string           `json:"current_status"`
	CurrentProductVersion string           `json:"current_product_version,omitempty"`
	Current               *VersionSummary  `json:"current,omitempty"`
	History               []VersionSummary `json:"history"`
}

type ContentResponse struct {
	SchemaVersion   int     `json:"schema_version"`
	Product         string  `json:"product"`
	ProductVersion  string  `json:"product_version"`
	PublishedAt     string  `json:"published_at"`
	Locale          string  `json:"locale"`
	RequestedLocale string  `json:"requested_locale"`
	LocaleFallback  bool    `json:"locale_fallback"`
	IsCurrent       bool    `json:"is_current"`
	Entries         []Entry `json:"entries"`
}

func decodeStrict(data []byte, limit int, target any) error {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return ErrInvalid
	}
	if err := validateUniqueJSON(data); err != nil {
		return fmt.Errorf("%w: duplicate or malformed JSON", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed JSON", ErrInvalid)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrInvalid)
	}
	return nil
}

func validateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := readJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func readJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalid
			}
			if _, exists := seen[key]; exists {
				return ErrInvalid
			}
			seen[key] = struct{}{}
			if err := readJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for decoder.More() {
			if err := readJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func DecodeIndex(data []byte) (Index, error) {
	var index Index
	if err := decodeStrict(data, MaxIndexBytes, &index); err != nil {
		return Index{}, err
	}
	if err := ValidateIndex(index); err != nil {
		return Index{}, err
	}
	return index, nil
}

// DecodeIndexForReader keeps validated immutable history available when the
// publisher's optional current marker is absent or malformed. Mutation paths
// and the Nudge adapter must continue to use strict DecodeIndex.
func DecodeIndexForReader(data []byte) (Index, error) {
	var envelope struct {
		SchemaVersion     int             `json:"schema_version"`
		Versions          []VersionRow    `json:"versions"`
		CurrentDeployment json.RawMessage `json:"current_deployment,omitempty"`
	}
	if err := decodeStrict(data, MaxIndexBytes, &envelope); err != nil {
		return Index{}, err
	}
	if envelope.Versions == nil {
		return Index{}, ErrInvalid
	}
	index := Index{SchemaVersion: envelope.SchemaVersion, Versions: envelope.Versions}
	if err := ValidateIndex(index); err != nil {
		return Index{}, err
	}
	if len(envelope.CurrentDeployment) == 0 || string(envelope.CurrentDeployment) == "null" {
		return index, nil
	}
	var marker CurrentDeployment
	if err := decodeStrict(envelope.CurrentDeployment, MaxIndexBytes, &marker); err != nil || ValidateMarker(marker) != nil {
		return index, nil
	}
	index.CurrentDeployment = &marker
	return index, nil
}

func ValidateIndex(index Index) error {
	if index.SchemaVersion != SchemaVersion || len(index.Versions) > MaxVersions {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(index.Versions))
	for _, row := range index.Versions {
		if !ValidProductVersion(row.ProductVersion) || len(row.Locales) == 0 || len(row.Locales) > 2 {
			return ErrInvalid
		}
		if _, ok := seen[row.ProductVersion]; ok {
			return ErrInvalid
		}
		seen[row.ProductVersion] = struct{}{}
		if published, err := time.Parse(time.RFC3339, row.PublishedAt); err != nil || published.IsZero() {
			return ErrInvalid
		}
		for locale, file := range row.Locales {
			if locale != "en" && locale != "zh-TW" || !validSHA(file.SHA256) || file.ObjectPath != contentPath(row.ProductVersion, locale, file.SHA256) {
				return ErrInvalid
			}
		}
	}
	if index.CurrentDeployment != nil {
		if err := ValidateMarker(*index.CurrentDeployment); err != nil {
			return err
		}
	}
	return nil
}

func ValidateMarker(marker CurrentDeployment) error {
	if marker.SchemaVersion != SchemaVersion || (marker.Environment != "local" && marker.Environment != "dev" && marker.Environment != "prod") ||
		!ValidProductVersion(marker.ProductVersion) || marker.Commit == "" || marker.Branch == "" || marker.ImageTag == "" ||
		!strings.HasPrefix(marker.ImageDigest, "sha256:") || !validSHA(strings.TrimPrefix(marker.ImageDigest, "sha256:")) ||
		marker.Service == "" || marker.ServiceUID == "" || marker.ServiceGeneration < 1 || marker.Revision == "" ||
		!validTransitionOperation(marker.Operation) || !strings.HasPrefix(marker.DeploymentEvidenceDigest, "sha256:") ||
		!validSHA(strings.TrimPrefix(marker.DeploymentEvidenceDigest, "sha256:")) {
		return ErrInvalid
	}
	return nil
}

func DecodeContent(data []byte, version, locale string) (Content, error) {
	var content Content
	if err := decodeStrict(data, MaxContentBytes, &content); err != nil {
		return Content{}, err
	}
	if err := ValidateContent(content, version, locale); err != nil {
		return Content{}, err
	}
	return content, nil
}

func ValidateContent(content Content, version, locale string) error {
	if content.SchemaVersion != SchemaVersion || content.Product != ProductName || content.ProductVersion != version ||
		!ValidProductVersion(version) || content.Locale != locale || (locale != "en" && locale != "zh-TW") ||
		len(content.Entries) < 1 || len(content.Entries) > 20 {
		return ErrInvalid
	}
	for _, entry := range content.Entries {
		if !validText(entry.Title, 160) || !validText(entry.WhatChanged, 2000) || !validText(entry.UserBenefit, 2000) ||
			!validText(entry.WhereToUse, 2000) || len(entry.Limitations) > 6 {
			return ErrInvalid
		}
		for _, limitation := range entry.Limitations {
			if !validText(limitation, 1000) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func validText(value string, max int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max
}

func ValidProductVersion(value string) bool {
	if !versionPattern.MatchString(value) {
		return false
	}
	// SemVer forbids leading zeroes in numeric prerelease identifiers.
	match := versionPattern.FindStringSubmatch(value)
	if match[4] == "" {
		return true
	}
	for _, part := range strings.Split(match[4], ".") {
		if len(part) > 1 && part[0] == '0' {
			allDigits := true
			for _, r := range part {
				allDigits = allDigits && r >= '0' && r <= '9'
			}
			if allDigits {
				return false
			}
		}
	}
	return true
}

func validTransitionOperation(operation string) bool {
	switch operation {
	case "release", "deploy", "reactivate", "promote", "rollback":
		return true
	default:
		return false
	}
}

func validSHA(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contentPath(version, locale, digest string) string {
	return "release-notes/v1/versions/" + version + "/" + locale + "/" + digest + ".json"
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func MarkerMatches(marker *CurrentDeployment, runtime BuildInfo) bool {
	return marker != nil && ValidateMarker(*marker) == nil && runtime.Environment == marker.Environment &&
		runtime.ProductVersion == marker.ProductVersion && runtime.Commit == marker.Commit && runtime.Branch == marker.Branch &&
		runtime.Tag == marker.Tag && runtime.ImageTag == marker.ImageTag && runtime.Service == marker.Service && runtime.Revision == marker.Revision &&
		runtime.Environment != "unknown" && runtime.Service != "unknown" && runtime.Revision != "unknown"
}

func summarize(row VersionRow) VersionSummary {
	locales := make([]string, 0, len(row.Locales))
	for locale := range row.Locales {
		locales = append(locales, locale)
	}
	if len(locales) == 2 && locales[0] > locales[1] {
		locales[0], locales[1] = locales[1], locales[0]
	}
	return VersionSummary{ProductVersion: row.ProductVersion, PublishedAt: row.PublishedAt, Locales: locales}
}
