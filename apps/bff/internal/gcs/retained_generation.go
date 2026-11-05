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
		if objectNotFound(err) {
			return generation.SourceSnapshotManifest{}, fmt.Errorf("%w: %w", store.ErrDeclaredObjectUnavailable, err)
		}
		return generation.SourceSnapshotManifest{}, fmt.Errorf("read retained source snapshot: %w", err)
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
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotInvalid
	}
	if inventory.ContentGeneration != manifest.GenerationID || inventory.IDMapDigest != idMap.SHA256 {
		return generation.SourceSnapshotManifest{}, ErrSourceSnapshotInvalid
	}
	// SourceStatusDigest is publisher-attested provenance; consumers validate
	// exact membership against the pinned ID map without reading private receipts.
	return inventory, nil
}

// RetainedSourceSnapshotSize returns the authenticated inventory size from the
// pinned generation manifest so callers can reserve a conservative working set
// before reading or decoding the inventory body.
func (c *Client) RetainedSourceSnapshotSize(ctx context.Context) (int64, error) {
	if c.view == nil || c.view.manifest == nil {
		return 0, store.ErrQueryGenerationUnpinned
	}
	manifest := c.view.manifest
	if manifest.SourceSnapshotDigest == "" {
		return 0, ErrSourceSnapshotUnavailable
	}
	path, err := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	if err != nil {
		return 0, ErrSourceSnapshotInvalid
	}
	object, err := c.objectAttrs(ctx, c.prefix()+"/"+path, 0)
	if err != nil {
		if objectNotFound(err) {
			return 0, fmt.Errorf("%w: %w", store.ErrDeclaredObjectUnavailable, err)
		}
		return 0, fmt.Errorf("read retained source snapshot attributes: %w", err)
	}
	if object.Size < 0 || object.Size > generation.MaxSourceSnapshotBytes {
		return 0, ErrSourceSnapshotInvalid
	}
	return object.Size, nil
}

// ReadSourceBytes resolves a stable ID from the verified pinned inventory and
// reads its content-addressed bytes at the exact retained object generation.
func (c *Client) ReadSourceBytes(ctx context.Context, stableID string) ([]byte, error) {
	inventory, err := c.ReadSourceSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return c.ReadSourceBytesFromSnapshot(ctx, inventory, stableID)
}

// RetainedSourceBytesSize reads metadata for the exact object generation
// declared by a previously verified same-generation inventory. Callers can
// reserve working set before materializing the retained source body.
func (c *Client) RetainedSourceBytesSize(ctx context.Context, inventory generation.SourceSnapshotManifest, stableID string) (int64, error) {
	if err := c.validatePinnedSourceInventory(inventory); err != nil {
		return 0, err
	}
	for _, row := range inventory.Rows {
		if row.StableID != stableID {
			continue
		}
		path, err := generation.SourceBytesPath(row.ContentDigest)
		if err != nil {
			return 0, ErrSourceSnapshotInvalid
		}
		attrs, err := c.objectAttrs(ctx, c.prefix()+"/"+path, row.ObjectGeneration)
		if err != nil {
			if objectNotFound(err) {
				return 0, fmt.Errorf("%w: %w", store.ErrDeclaredObjectUnavailable, err)
			}
			return 0, fmt.Errorf("read retained source attributes: %w", err)
		}
		if attrs.Generation != row.ObjectGeneration || attrs.Size < 0 || attrs.Size > generation.MaxFileBytes {
			return 0, store.ErrDeclaredObjectUnavailable
		}
		return attrs.Size, nil
	}
	return 0, store.ErrObjectNotExist
}

// ReadSourceBytesFromSnapshot uses a caller-reserved, previously verified
// inventory and preserves exact object generation and digest checks.
func (c *Client) ReadSourceBytesFromSnapshot(ctx context.Context, inventory generation.SourceSnapshotManifest, stableID string) ([]byte, error) {
	if err := c.validatePinnedSourceInventory(inventory); err != nil {
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
			if objectNotFound(err) {
				return nil, fmt.Errorf("%w: %w", store.ErrDeclaredObjectUnavailable, err)
			}
			return nil, fmt.Errorf("read retained source bytes: %w", err)
		}
		if object.Generation != row.ObjectGeneration || generation.Digest(object.Data) != row.ContentDigest {
			return nil, store.ErrDeclaredObjectUnavailable
		}
		return object.Data, nil
	}
	return nil, store.ErrObjectNotExist
}

func (c *Client) validatePinnedSourceInventory(inventory generation.SourceSnapshotManifest) error {
	if c.view == nil || c.view.manifest == nil || inventory.ContentGeneration != c.view.manifest.GenerationID {
		return ErrSourceSnapshotInvalid
	}
	if err := inventory.Validate(); err != nil {
		return ErrSourceSnapshotInvalid
	}
	idMap, hasIDMap := c.view.manifest.File("cache/id_map.json")
	if !hasIDMap || inventory.IDMapDigest != idMap.SHA256 || c.view.manifest.SourceSnapshotDigest == "" {
		return ErrSourceSnapshotInvalid
	}
	_, digest, err := generation.EncodeSourceSnapshot(inventory)
	if err != nil || digest != c.view.manifest.SourceSnapshotDigest {
		return ErrSourceSnapshotInvalid
	}
	return nil
}
