# CLI release metadata

The producer writes a version manifest at `cli/versions/<version>/manifest.json` and copies the same complete manifest to `cli/stable/latest.json` only after all four archives are uploaded, read back, and reachable through their unauthenticated download URLs. The stable object uses `Cache-Control: no-store`; immutable version assets and manifests can use long-lived caching.

The JSON shape is shared by the publisher and Frontend loader: `schema_version` is `1`; `version` is full SemVer; `source_commit` is a full Git SHA; `channel` is `stable` or `prerelease`; and `assets` has exactly one entry for `darwin/arm64`, `darwin/amd64`, `windows/amd64`, and `linux/amd64`. Each entry contains `os`, `arch`, `asset_name`, `size`, and lowercase `sha256`. `download_url` is omitted until an unauthenticated readback confirms that exact archive is downloadable over HTTPS.

The Frontend reads only `https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/stable/latest.json` in DEV through `NEXT_PUBLIC_CLI_RELEASE_METADATA_URL`, with `credentials: omit` and `cache: no-store`. The loader is separate from runtime-config schema 1. Missing configuration, failed fetch, invalid metadata, and entries without a verified URL all render as unavailable. Build-only manifests never update the stable object.

The existing `gs://llm-wiki-frontend-config-dev` bucket and DEV browser CORS/read path were verified by the coordinator. This source implementation does not claim CLI objects or write authorization already exist; publication still requires the manual Actions/WIF path and anonymous archive readback.
