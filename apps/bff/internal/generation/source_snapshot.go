package generation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/annotation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	SourceSnapshotSchema   = "profile.source-snapshot.v1"
	SourceSnapshotPrefix   = ".lwc/profile/tags/source-snapshots/v1/"
	SourceBytesPrefix      = ".lwc/profile/tags/source-bytes/v1/"
	MaxSourceSnapshotBytes = MaxFileBytes
)

type SourceSnapshotRow struct {
	StableID         string `json:"stable_id"`
	RawPath          string `json:"raw_path"`
	ContentDigest    string `json:"content_digest"`
	ObjectGeneration int64  `json:"object_generation"`
}

// SourceSnapshotManifest binds source bytes to one generation and the exact
// source-status receipt observed after that generation's worker run.
type SourceSnapshotManifest struct {
	SchemaVersion      string              `json:"schema_version"`
	ContentGeneration  string              `json:"content_generation"`
	IDMapDigest        string              `json:"id_map_digest"`
	SourceStatusDigest string              `json:"source_status_digest"`
	Rows               []SourceSnapshotRow `json:"rows"`
}

func (m SourceSnapshotManifest) Validate() error {
	if m.SchemaVersion != SourceSnapshotSchema || !safeGenerationID(m.ContentGeneration) || !validLowerDigest(m.IDMapDigest) || !validLowerDigest(m.SourceStatusDigest) || m.Rows == nil {
		return errors.New("invalid source snapshot manifest")
	}
	lastID := ""
	for _, row := range m.Rows {
		if !annotation.ValidSourceID(row.StableID) || row.StableID <= lastID || !storage.SafeRawPath(row.RawPath) || !validLowerDigest(row.ContentDigest) || row.ObjectGeneration <= 0 {
			return errors.New("invalid source snapshot manifest")
		}
		lastID = row.StableID
	}
	return nil
}

func EncodeSourceSnapshot(m SourceSnapshotManifest) ([]byte, string, error) {
	if err := m.Validate(); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxSourceSnapshotBytes {
		return nil, "", errors.New("source snapshot manifest exceeds limit")
	}
	return data, Digest(data), nil
}

func DecodeSourceSnapshot(data []byte) (SourceSnapshotManifest, error) {
	if len(data) > MaxSourceSnapshotBytes {
		return SourceSnapshotManifest{}, errors.New("source snapshot manifest exceeds limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var manifest SourceSnapshotManifest
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
		}
		seen[key] = true
		switch key {
		case "schema_version":
			err = dec.Decode(&manifest.SchemaVersion)
		case "content_generation":
			err = dec.Decode(&manifest.ContentGeneration)
		case "id_map_digest":
			err = dec.Decode(&manifest.IDMapDigest)
		case "source_status_digest":
			err = dec.Decode(&manifest.SourceStatusDigest)
		case "rows":
			err = decodeSourceSnapshotRows(dec, &manifest.Rows)
		default:
			return SourceSnapshotManifest{}, fmt.Errorf("unknown source snapshot field %q", key)
		}
		if err != nil {
			return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
		}
	}
	if _, err := dec.Token(); err != nil {
		return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
	}
	for _, required := range []string{"schema_version", "content_generation", "id_map_digest", "source_status_digest", "rows"} {
		if !seen[required] {
			return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
		}
	}
	if err := EnsureJSONEOF(dec); err != nil {
		return SourceSnapshotManifest{}, errors.New("invalid source snapshot manifest")
	}
	if err := manifest.Validate(); err != nil {
		return SourceSnapshotManifest{}, err
	}
	return manifest, nil
}

func decodeSourceSnapshotRows(dec *json.Decoder, rows *[]SourceSnapshotRow) error {
	token, err := dec.Token()
	if err != nil || token != json.Delim('[') {
		return errors.New("source snapshot rows must be an array")
	}
	*rows = []SourceSnapshotRow{}
	for dec.More() {
		var row SourceSnapshotRow
		token, err := dec.Token()
		if err != nil || token != json.Delim('{') {
			return errors.New("source snapshot row must be an object")
		}
		seen := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("invalid source snapshot row")
			}
			seen[key] = true
			switch key {
			case "stable_id":
				err = dec.Decode(&row.StableID)
			case "raw_path":
				err = dec.Decode(&row.RawPath)
			case "content_digest":
				err = dec.Decode(&row.ContentDigest)
			case "object_generation":
				err = dec.Decode(&row.ObjectGeneration)
			default:
				return fmt.Errorf("unknown source snapshot row field %q", key)
			}
			if err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		for _, required := range []string{"stable_id", "raw_path", "content_digest", "object_generation"} {
			if !seen[required] {
				return fmt.Errorf("missing source snapshot row field %q", required)
			}
		}
		*rows = append(*rows, row)
	}
	_, err = dec.Token()
	return err
}

func SourceSnapshotPath(digest string) (string, error) {
	if !validLowerDigest(digest) {
		return "", errors.New("invalid source snapshot digest")
	}
	return SourceSnapshotPrefix + digest + ".json", nil
}

func SourceBytesPath(digest string) (string, error) {
	if !validLowerDigest(digest) {
		return "", errors.New("invalid source content digest")
	}
	return SourceBytesPrefix + digest + ".txt", nil
}

func validLowerDigest(value string) bool {
	return validDigest(value) && strings.ToLower(value) == value
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
