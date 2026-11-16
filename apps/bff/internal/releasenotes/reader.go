package releasenotes

import (
	"context"
	"errors"
	"sort"
	"time"
)

type IndexSnapshot struct {
	Bytes      []byte
	Generation int64
}

// ReadStore exposes only the fixed Notes index and immutable content objects.
// Implementations must not list bucket contents or accept caller paths.
type ReadStore interface {
	ReadIndex(context.Context) (IndexSnapshot, error)
	ReadContent(context.Context, string) ([]byte, error)
}

type Reader struct {
	Store   ReadStore
	Runtime BuildInfo
}

// VisibilitySnapshot is the single bounded metadata observation shared by
// Release Notes reconciliation and authenticated Nudge visibility projection.
// It contains no locale bodies or public/private publisher receipts.
type VisibilitySnapshot struct {
	IndexRevision int64
	IndexDigest   string
	Index         Index
}

func ReadVisibilitySnapshot(ctx context.Context, source ReadStore, runtime BuildInfo) (*VisibilitySnapshot, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	snapshot, err := source.ReadIndex(ctx)
	if err != nil || snapshot.Generation < 1 {
		return nil, ErrUnavailable
	}
	index, err := DecodeIndex(snapshot.Bytes)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, alias := range index.LegacyAliases {
		if alias.Environment != runtime.Environment {
			return nil, ErrUnavailable
		}
	}
	return &VisibilitySnapshot{IndexRevision: snapshot.Generation, IndexDigest: sha256Hex(snapshot.Bytes), Index: index}, nil
}

type GCSObjectStore interface {
	ReadIndex(context.Context) ([]byte, int64, error)
	ReadContent(context.Context, string) ([]byte, error)
}

type gcsReadStore struct{ store GCSObjectStore }

func NewGCSReadStore(store GCSObjectStore) ReadStore { return gcsReadStore{store: store} }

func (s gcsReadStore) ReadIndex(ctx context.Context) (IndexSnapshot, error) {
	if s.store == nil {
		return IndexSnapshot{}, ErrUnavailable
	}
	data, generation, err := s.store.ReadIndex(ctx)
	return IndexSnapshot{Bytes: data, Generation: generation}, err
}

func (s gcsReadStore) ReadContent(ctx context.Context, path string) ([]byte, error) {
	if s.store == nil {
		return nil, ErrUnavailable
	}
	return s.store.ReadContent(ctx, path)
}

func (r Reader) Index(ctx context.Context) (IndexResponse, error) {
	if r.Store == nil {
		return IndexResponse{}, ErrUnavailable
	}
	snapshot, err := r.Store.ReadIndex(ctx)
	if err != nil {
		return IndexResponse{}, ErrUnavailable
	}
	index, err := DecodeIndexForReader(snapshot.Bytes)
	if err != nil {
		return IndexResponse{}, ErrUnavailable
	}
	for _, alias := range index.LegacyAliases {
		if alias.Environment != r.Runtime.Environment {
			return IndexResponse{}, ErrUnavailable
		}
	}
	response := IndexResponse{
		SchemaVersion:          SchemaVersion,
		Environment:            r.Runtime.Environment,
		RuntimeProductVersion:  r.Runtime.ProductVersion,
		CurrentStatus:          "unavailable",
		History:                make([]VersionSummary, 0, len(index.Versions)),
		ComponentDetailsStatus: "unavailable",
		CurrentComponents:      []PublicComponent{},
		CompatibilityAliases:   make([]PublicAlias, 0, len(index.LegacyAliases)),
	}
	for _, alias := range index.LegacyAliases {
		response.CompatibilityAliases = append(response.CompatibilityAliases, PublicAlias{
			RequestedVersion: alias.RequestedVersion, CanonicalVersion: alias.CanonicalVersion,
			LegacyRowVersion: alias.LegacyRowVersion,
		})
	}
	markerCurrent := MarkerMatches(index.CurrentDeployment, r.Runtime)
	currentVersion := ""
	if index.CurrentDeployment != nil {
		currentVersion = markerReleaseVersion(*index.CurrentDeployment)
	}
	var currentRow *VersionRow
	if markerCurrent {
		for i := range index.Versions {
			if index.Versions[i].ProductVersion == currentVersion {
				currentRow = &index.Versions[i]
				break
			}
		}
		if currentRow == nil {
			response.CurrentStatus = "missing"
			response.CurrentProductVersion = currentVersion
		} else {
			verified := false
			for locale, file := range currentRow.Locales {
				body, readErr := r.Store.ReadContent(ctx, file.ObjectPath)
				if readErr != nil || sha256Hex(body) != file.SHA256 {
					continue
				}
				if _, decodeErr := DecodeContent(body, currentVersion, locale); decodeErr == nil {
					verified = true
					break
				}
			}
			if verified {
				response.CurrentStatus = "available"
				response.CurrentProductVersion = currentVersion
				summary := summarize(*currentRow)
				response.Current = &summary
				if index.CurrentDeployment.SchemaVersion == MarkerSchemaV2 {
					response.CurrentComponents = publicComponents(*index.CurrentDeployment)
					response.ComponentDetailsStatus = componentDetailsStatus(*index.CurrentDeployment)
				}
			}
		}
	}
	legacyRows := map[string]struct{}{}
	for _, alias := range index.LegacyAliases {
		legacyRows[alias.LegacyRowVersion] = struct{}{}
	}
	for _, row := range index.Versions {
		if markerCurrent && row.ProductVersion == currentVersion {
			continue
		}
		if _, legacy := legacyRows[row.ProductVersion]; legacy {
			continue
		}
		response.History = append(response.History, summarize(row))
	}
	sort.SliceStable(response.History, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339, response.History[i].PublishedAt)
		right, _ := time.Parse(time.RFC3339, response.History[j].PublishedAt)
		if left.Equal(right) {
			return response.History[i].ProductVersion > response.History[j].ProductVersion
		}
		return left.After(right)
	})
	return response, nil
}

func (r Reader) Content(ctx context.Context, version, requestedLocale string) (ContentResponse, error) {
	if !ValidProductVersion(version) {
		return ContentResponse{}, ErrNotFound
	}
	if requestedLocale != "en" && requestedLocale != "zh-TW" {
		return ContentResponse{}, ErrInvalid
	}
	if r.Store == nil {
		return ContentResponse{}, ErrUnavailable
	}
	snapshot, err := r.Store.ReadIndex(ctx)
	if err != nil {
		return ContentResponse{}, ErrUnavailable
	}
	index, err := DecodeIndexForReader(snapshot.Bytes)
	if err != nil {
		return ContentResponse{}, ErrUnavailable
	}
	for _, alias := range index.LegacyAliases {
		if alias.Environment != r.Runtime.Environment {
			return ContentResponse{}, ErrUnavailable
		}
	}
	requestedVersion := version
	canonicalVersion := version
	bodyVersion := version
	legacyAlias := false
	legacyFallback := false
	for _, alias := range index.LegacyAliases {
		if alias.RequestedVersion == requestedVersion {
			legacyAlias = true
			canonicalVersion = alias.CanonicalVersion
			break
		}
	}
	row := findVersionRow(index.Versions, bodyVersion)
	if legacyAlias {
		row = findVersionRow(index.Versions, canonicalVersion)
		if row == nil {
			legacyFallback = true
			bodyVersion = LegacyVersion
			row = findVersionRow(index.Versions, bodyVersion)
		} else {
			bodyVersion = canonicalVersion
		}
	}
	if row == nil {
		return ContentResponse{}, ErrNotFound
	}
	file, ok := row.Locales[requestedLocale]
	actualLocale, fallback := requestedLocale, false
	if !ok && requestedLocale != "en" {
		file, ok = row.Locales["en"]
		actualLocale, fallback = "en", ok
	}
	if !ok {
		return ContentResponse{}, ErrLocaleUnavailable
	}
	body, err := r.Store.ReadContent(ctx, file.ObjectPath)
	if err != nil {
		return ContentResponse{}, ErrUnavailable
	}
	if sha256Hex(body) != file.SHA256 {
		return ContentResponse{}, ErrUnavailable
	}
	content, err := DecodeContent(body, bodyVersion, actualLocale)
	if err != nil {
		return ContentResponse{}, ErrUnavailable
	}
	markerCurrent := MarkerMatches(index.CurrentDeployment, r.Runtime)
	return ContentResponse{
		SchemaVersion: SchemaVersion, Product: content.Product, ProductVersion: bodyVersion,
		PublishedAt: row.PublishedAt, Locale: actualLocale, RequestedLocale: requestedLocale,
		LocaleFallback: fallback, IsCurrent: markerCurrent && !legacyFallback && bodyVersion == currentMarkerVersion(index.CurrentDeployment),
		Entries: content.Entries, RequestedProductVersion: requestedVersion,
		CanonicalProductVersion: canonicalVersion, LegacyAlias: legacyAlias, LegacyFallback: legacyFallback,
	}, nil
}

func findVersionRow(rows []VersionRow, version string) *VersionRow {
	for i := range rows {
		if rows[i].ProductVersion == version {
			return &rows[i]
		}
	}
	return nil
}

func markerReleaseVersion(marker CurrentDeployment) string {
	if marker.SchemaVersion == MarkerSchemaV2 {
		return marker.ReleaseVersion
	}
	return marker.ProductVersion
}

func currentMarkerVersion(marker *CurrentDeployment) string {
	if marker == nil {
		return ""
	}
	return markerReleaseVersion(*marker)
}

func publicComponents(marker CurrentDeployment) []PublicComponent {
	items := []PublicComponent{{Component: "bff", ProductVersion: marker.ProductVersion, Commit: marker.Commit,
		ImageTag: marker.ImageTag, ImageDigest: marker.ImageDigest, Revision: marker.Revision}}
	for _, build := range marker.ComponentBuilds {
		item := PublicComponent{Component: build.Component, SourceSHA: build.SourceSHA}
		if build.Component == "frontend" {
			item.DeploymentID = build.DeploymentID
		} else {
			item.JobName = build.JobName
			item.ImageDigest = build.ImageDigest
		}
		items = append(items, item)
	}
	return items
}

func componentDetailsStatus(marker CurrentDeployment) string {
	if marker.SchemaVersion != MarkerSchemaV2 {
		return "unavailable"
	}
	for _, build := range marker.ComponentBuilds {
		if build.SourceSHA == "" || (build.Component == "frontend" && build.BuildConfigSHA256 == "") {
			return "partial"
		}
	}
	return "complete"
}

var ErrLocaleUnavailable = errors.New("release notes locale unavailable")
