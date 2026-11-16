package releasenotes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

const releaseSourceKeyPrefix = "release-notes:llm-wiki-cloud:"

// ObserveSource is the Notes-owned producer boundary. The common Nudge owner
// captures expectedRevision from its global release-notes slot before calling
// this adapter and remains responsible for reconciliation, state, and actions.
func ObserveSource(ctx context.Context, source ReadStore, runtime BuildInfo, expectedRevision int64) nudge.SourceResult {
	result, _ := ObserveSourceWithSnapshot(ctx, source, runtime, expectedRevision)
	return result
}

// ObserveSourceWithSnapshot reads the index once and returns the same validated
// marker fence for the authenticated handler's read-only visibility projection.
func ObserveSourceWithSnapshot(ctx context.Context, source ReadStore, runtime BuildInfo,
	expectedRevision int64) (nudge.SourceResult, *VisibilitySnapshot) {
	result := nudge.SourceResult{Status: "unavailable", Upserts: []nudge.ObservationInput{},
		AuthoritativeClears: []nudge.ClearInput{}, Failures: []nudge.SourceFailure{}, Notices: []nudge.SourceNotice{}}
	if runtime.Environment != "dev" && runtime.Environment != "prod" && runtime.Environment != "local" ||
		runtime.Environment == "unknown" || runtime.Service == "" || runtime.Service == "unknown" ||
		runtime.Revision == "" || runtime.Revision == "unknown" || expectedRevision < 0 || source == nil {
		return unavailable(result, runtime.Environment, "release-transition-unavailable"), nil
	}
	snapshot, err := ReadVisibilitySnapshot(ctx, source, runtime)
	if err != nil {
		return unavailable(result, runtime.Environment, "release-index-unavailable"), nil
	}
	index := snapshot.Index
	if !MarkerMatches(index.CurrentDeployment, runtime) {
		return unavailable(result, runtime.Environment, "release-transition-unavailable"), snapshot
	}
	publicVersion := markerReleaseVersion(*index.CurrentDeployment)
	witness := nudge.ProducerWitness{Kind: "release-notes", Environment: runtime.Environment,
		IndexRevision: snapshot.IndexRevision, ProductVersion: publicVersion, IndexDigest: snapshot.IndexDigest}
	sourceKey := releaseSourceKeyPrefix + runtime.Environment
	var current *VersionRow
	for i := range index.Versions {
		if index.Versions[i].ProductVersion == publicVersion {
			current = &index.Versions[i]
			break
		}
	}
	if current == nil {
		result.Status = "complete"
		result.AuthoritativeClears = append(result.AuthoritativeClears, nudge.ClearInput{
			Category: nudge.CategoryReleaseNotes, SourceKey: sourceKey, ExpectedSourceRevision: expectedRevision,
			ProducerWitness: witness, Proof: "authoritative-current-missing",
		})
		result.Notices = append(result.Notices, nudge.SourceNotice{SourceKey: sourceKey, State: "missing", SafeClass: "current-version-not-indexed"})
		return result, snapshot
	}
	verified, failed := 0, 0
	for locale, file := range current.Locales {
		body, readErr := source.ReadContent(ctx, file.ObjectPath)
		if readErr != nil || sha256Hex(body) != file.SHA256 {
			failed++
			continue
		}
		if _, decodeErr := DecodeContent(body, publicVersion, locale); decodeErr != nil {
			failed++
			continue
		}
		verified++
	}
	if verified == 0 {
		return unavailable(result, runtime.Environment, "release-content-unavailable"), snapshot
	}
	observation := nudge.Observation{
		Title:    "Release notes are available",
		Body:     fmt.Sprintf("Version %s release notes are ready to read.", publicVersion),
		Severity: nudge.SeverityInfo,
		Actions: []nudge.Action{{ID: "release.open", Kind: "link", Label: "Read release notes",
			Target: &nudge.Target{Kind: "release-notes", Mode: "published-version", Version: publicVersion}}},
	}
	payload, err := canonicalObservationJSON(observation)
	if err != nil {
		return unavailable(result, runtime.Environment, "release-observation-invalid"), snapshot
	}
	digest := sha256.Sum256(payload)
	result.Upserts = append(result.Upserts, nudge.ObservationInput{
		Category: nudge.CategoryReleaseNotes, SourceKey: sourceKey, ExpectedSourceRevision: expectedRevision,
		ProducerWitness: witness, PayloadDigest: hex.EncodeToString(digest[:]),
		AttentionMaterialRevision: "version:" + publicVersion, Observation: observation,
	})
	for _, alias := range index.LegacyAliases {
		if alias.Environment == runtime.Environment && alias.RequestedVersion == LegacyVersion &&
			alias.CanonicalVersion == publicVersion && publicVersion == PublicVersion {
			result.ReleaseAliasTransfer = &nudge.ReleaseAliasTransferProof{
				RequestedVersion: alias.RequestedVersion, CanonicalVersion: alias.CanonicalVersion,
				AliasRecordSHA256: alias.AliasRecordSHA256,
			}
			break
		}
	}
	result.Status = "complete"
	if failed > 0 {
		result.Status = "partial"
		result.Failures = append(result.Failures, nudge.SourceFailure{SafeClass: "release-locale-content-unavailable"})
		result.Notices = append(result.Notices, nudge.SourceNotice{SourceKey: sourceKey, State: "stale",
			SafeClass: "some-current-locales-unavailable"})
	}
	if err := nudge.ValidateObservationInput(result.Upserts[0]); err != nil {
		return unavailable(result, runtime.Environment, "release-observation-invalid"), snapshot
	}
	return result, snapshot
}

func canonicalObservationJSON(observation nudge.Observation) ([]byte, error) {
	encoded, err := json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func unavailable(result nudge.SourceResult, environment, safeClass string) nudge.SourceResult {
	result.Status = "unavailable"
	result.Upserts = []nudge.ObservationInput{}
	result.AuthoritativeClears = []nudge.ClearInput{}
	result.Failures = append(result.Failures, nudge.SourceFailure{SafeClass: safeClass})
	if environment == "local" || environment == "dev" || environment == "prod" {
		result.Notices = append(result.Notices, nudge.SourceNotice{SourceKey: releaseSourceKeyPrefix + environment, State: "stale", SafeClass: "currentness-unverified"})
	}
	return result
}
