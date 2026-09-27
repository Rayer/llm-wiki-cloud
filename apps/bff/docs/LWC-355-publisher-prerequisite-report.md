# LWC-355 generation publisher prerequisite report

## Delivered

- `generation.ArchivedManifestPath(id)` resolves `.lwc/publish/generations/{id}/manifest.json`. The publisher writes the exact validated manifest create-only and verifies its bytes, digest and object generation before CAS-advancing `.lwc/publish/current.json`.
- `generation.Manifest.SourceSnapshotDigest` optionally names the exact compact inventory bytes. `generation.SourceSnapshotPath(digest)` and `generation.SourceBytesPath(digest)` resolve the versioned Tag inventory and content-addressed source bytes. `EncodeSourceSnapshot` / `DecodeSourceSnapshot` enforce the `profile.source-snapshot.v1` shape, lowercase digests, strict fields, sorted unique stable IDs, safe raw paths and positive object generations.
- The worker builds the inventory from batch-start `sourceSnapshot.RawBytes`/`RawSHA256`, the generation's exact `cache/id_map.json`, and a private post-run `cache/source_status.json` receipt created from those same pinned snapshots before publication. Each `LastIngestedRawSHA256` must match the exact raw-byte digest. Raw objects are not reread after compilation.
- Source bytes and inventory use create-only writes plus exact readback. A compile generation retains a prior row only when the current ID map and receipt still match the old path and digest; deleted source IDs leave the new inventory. The suggested-query-only publisher carries the prior inventory only when all other generation files, the ID map, and the source-ID set are unchanged. CAS losers can leave unreachable immutable objects but are returned as conflicts, never as committed generations.
- The v1 contract and LWC-355 integration map now distinguish `cache/source_status.json` (the `LastIngestedRawSHA256` receipt) from `cache/raw_status.json` (OLW's state-db status output). A missing source snapshot digest stays absent for legacy/baseline generations; a Profile reader that requires source inventory must fail visibly rather than invent one.

## Verification

Run from `apps/bff`:

```sh
go test ./internal/generation ./cmd/olw_worker -run 'Test(PublishCloudGeneration|PublishCloudGenerationArchivesSourceSnapshotsByGeneration|PublishCloudGenerationRetainsUnchangedSourceAndBindsReceipt|PublishCloudGenerationRejectsMissingOrMismatchedSourceReceipt|PublishCloudGenerationRejectsUningestedRawChangeAndDropsDeletedIDs|PublishCloudGenerationFailureAndCASLoserDoNotAdvanceCurrent|DecodeRejects|GenerationManifestArchivesAndSourceSnapshotReference|SourceSnapshotCompactStrictManifestAndPaths)' -count=1
go test ./cmd/olw_worker -run '^TestCloudSuggestedQueriesStageOnlyPublishesNewChips|TestCloudSuccessUsesExactStartAndConcurrentChangesStayDirty|TestCloudTwoGenerationsReconcileStableSourceAndAnnotation|TestExactSlugStabilityThroughWorkspaceAndCloudCompose$' -count=1
go test ./cmd/olw_worker ./internal/generation -count=1
go vet ./cmd/olw_worker ./internal/generation
```

All four commands passed. Tests use the worker's in-memory object store and local fixtures; no live provider, cloud project, deployment, commit or IAM mutation was used.

## Handoff and remaining integration

LWC-355 can read the archived generation manifest at `generation.ArchivedManifestPath(id)`, require its exact `generation_id`, and obtain the source inventory digest from `Manifest.SourceSnapshotDigest`. It can then fetch and hash-check the inventory and read source bytes at the row's `object_generation`; every object remains inside the caller's Project prefix. A missing archive, inventory, byte object, or digest mismatch must fail closed. No fallback to `current.json` or mutable `raw/` is supported by this slice.

This does not implement the LWC-355 by-ID GCS view/cache reader, Query adapter, Tag worker, Profile activation, or retention/GC policy. Legacy generations are not backfilled or migrated; if Profile later requires a snapshot from one, the absent reference is an explicit error. The parent still owns the shared review and tracker updates, and any use of these helpers by another owner must follow the frozen paths and `source_status_digest` field recorded above.
