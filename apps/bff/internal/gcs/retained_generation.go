package gcs

import (
	"context"
	"errors"
	"fmt"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

var ErrSourceSnapshotUnavailable = errors.New("source snapshot unavailable")
var ErrSourceSnapshotInvalid = errors.New("source snapshot invalid")

// PinGeneration resolves only the requested retained archive. Generated file
// reads verify size, digest and exact object generation through the pinned view.
func (c *Client) PinGeneration(ctx context.Context, id string) (*Client, GenerationSnapshot, error) {
	path, err := generation.ArchivedManifestPath(id)
	if err != nil {
		return nil, GenerationSnapshot{}, fmt.Errorf("%w: invalid archive ID", store.ErrGenerationStateUnavailable)
	}
	object, err := c.readObject(ctx, c.prefix()+"/"+path, 0, generation.MaxManifestBytes)
	if err != nil {
		return nil, GenerationSnapshot{}, fmt.Errorf("%w: read archive: %w", store.ErrGenerationStateUnavailable, err)
	}
	manifest, err := generation.Decode(object.Data)
	if err != nil || manifest.GenerationID != id || object.Generation <= 0 {
		return nil, GenerationSnapshot{}, fmt.Errorf("%w: invalid archive", store.ErrGenerationStateUnavailable)
	}
	digest := generation.Digest(object.Data)
	// Include scope and content identity even when object generation numbers repeat.
	token := generation.Digest([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", c.prefix(), id, object.Generation, digest)))
	pinned := c.WithScope(c.userID, c.projectID)
	pinned.view = &generationView{manifest: &manifest, token: "archive-" + token}
	snapshot := GenerationSnapshot{Manifest: manifest, ManifestGeneration: object.Generation, ManifestSHA256: digest}
	// The returned manifest must not allow callers to mutate the pinned view.
	snapshot.Manifest.Files = append([]generation.File(nil), manifest.Files...)
	return pinned, snapshot, nil
}

// PinQueryGeneration exposes the retained view to storage-neutral consumers.
func (c *Client) PinQueryGeneration(ctx context.Context, id string) (store.Store, generation.Manifest, error) {
	pinned, snapshot, err := c.PinGeneration(ctx, id)
	if err != nil {
		return nil, generation.Manifest{}, err
	}
	return pinned, snapshot.Manifest, nil
}

// ReadSourceSnapshot verifies the inventory against an already pinned manifest.
// It never reads current.json, mutable raw files, or individual concept pages.
func (c *Client) ReadSourceSnapshot(ctx context.Context) (generation.SourceSnapshotManifest, error) {
	if c.view == nil || c.view.manifest == nil {
		return generation.SourceSnapshotManifest{}, store.ErrQueryGenerationUnpinned
	}
	manifest := c.view.manifest
	if manifest.SourceSnapshotDigest == "" {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotUnavailable
	}
	path, err := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	if err != nil {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotInvalid
	}
	object, err := c.readObject(ctx, c.prefix()+"/"+path, 0, generation.MaxSourceSnapshotBytes)
	if err != nil {
		return generation.SourceSnapshotManifest{}, fmt.Errorf("%w: %w", ErrSourceSnapshotUnavailable, err)
	}
	if generation.Digest(object.Data) != manifest.SourceSnapshotDigest {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotInvalid
	}
	inventory, err := generation.DecodeSourceSnapshot(object.Data)
	if err != nil {
		return generation.SourceSnapshotManifest{}, fmt.Errorf("%w: %w", ErrSourceSnapshotInvalid, err)
	}
	idMap, hasIDMap := manifest.File("cache/id_map.json")
	if !hasIDMap {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotUnavailable
	}
	if inventory.ContentGeneration != manifest.GenerationID || inventory.IDMapDigest != idMap.SHA256 {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotInvalid
	}
	// SourceStatusDigest is publisher-attested provenance; consumers validate
	// exact membership against the pinned ID map without reading private receipts.
	return inventory, nil
}

// ReadSourceBytes resolves a stable ID from the verified pinned inventory and
// reads its content-addressed bytes at the exact retained object generation.
func (c *Client) ReadSourceBytes(ctx context.Context, stableID string) ([]byte, error) {
	inventory, err := c.ReadSourceSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range inventory.Rows {
		if row.StableID != stableID {
			continue
		}
		path, err := generation.SourceBytesPath(row.ContentDigest)
		if err != nil {
			return nil, ErrSourceSnapshotInvalid
		}
		object, err := c.readObject(ctx, c.prefix()+"/"+path, row.ObjectGeneration, generation.MaxFileBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", store.ErrDeclaredObjectUnavailable, err)
		}
		if object.Generation != row.ObjectGeneration || generation.Digest(object.Data) != row.ContentDigest {
			return nil, store.ErrDeclaredObjectUnavailable
		}
		return object.Data, nil
	}
	return nil, store.ErrObjectNotExist
}
