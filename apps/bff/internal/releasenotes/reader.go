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
	response := IndexResponse{
		SchemaVersion:         SchemaVersion,
		Environment:           r.Runtime.Environment,
		RuntimeProductVersion: r.Runtime.ProductVersion,
		CurrentStatus:         "unavailable",
		History:               make([]VersionSummary, 0, len(index.Versions)),
	}
	markerCurrent := MarkerMatches(index.CurrentDeployment, r.Runtime)
	var currentRow *VersionRow
	if markerCurrent {
		response.CurrentProductVersion = r.Runtime.ProductVersion
		for i := range index.Versions {
			if index.Versions[i].ProductVersion == r.Runtime.ProductVersion {
				currentRow = &index.Versions[i]
				break
			}
		}
		if currentRow == nil {
			response.CurrentStatus = "missing"
		} else {
			verified := false
			for locale, file := range currentRow.Locales {
				body, readErr := r.Store.ReadContent(ctx, file.ObjectPath)
				if readErr != nil || sha256Hex(body) != file.SHA256 {
					continue
				}
				if _, decodeErr := DecodeContent(body, currentRow.ProductVersion, locale); decodeErr == nil {
					verified = true
					break
				}
			}
			if verified {
				response.CurrentStatus = "available"
				summary := summarize(*currentRow)
				response.Current = &summary
			}
		}
	}
	for _, row := range index.Versions {
		if markerCurrent && row.ProductVersion == r.Runtime.ProductVersion {
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
	var row *VersionRow
	for i := range index.Versions {
		if index.Versions[i].ProductVersion == version {
			row = &index.Versions[i]
			break
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
	content, err := DecodeContent(body, version, actualLocale)
	if err != nil {
		return ContentResponse{}, ErrUnavailable
	}
	return ContentResponse{
		SchemaVersion: SchemaVersion, Product: content.Product, ProductVersion: version,
		PublishedAt: row.PublishedAt, Locale: actualLocale, RequestedLocale: requestedLocale,
		LocaleFallback: fallback, IsCurrent: MarkerMatches(index.CurrentDeployment, r.Runtime) && version == r.Runtime.ProductVersion,
		Entries: content.Entries,
	}, nil
}

var ErrLocaleUnavailable = errors.New("release notes locale unavailable")
