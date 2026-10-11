# CLI Lane Implementation Checkpoint

Date: 2026-10-12
Branch: `Rayer/LWC-386-387-cli-delivery`
Source base: `7713976360fe3573c7e36d56ceb2d31daf2751a5`

## Frozen scope

- LWC-385 (`cli-onboarding-ux-r2`): independent four-step CLI onboarding/download component, owned locale namespace, shell-safe command generation, and component tests.
- LWC-386 (`cli-version-examples-r2`): offline top-level `lwc-sync --version`, build-time version/commit injection, and shell workflow examples; existing raw/auth/binding behavior remains unchanged.
- LWC-387 (`cli-gcs-distribution-r1`): reproducible four-target packaging, checksum/manifest validation, manual release workflow, and conditional object-store publishing.

## Shared contract and ownership

`schema_version: 1` metadata contains `version`, full `source_commit`, `channel`, and exactly one asset for `darwin/arm64`, `darwin/amd64`, `windows/amd64`, and `linux/amd64`. Each asset has `os`, `arch`, `asset_name`, `size`, and lowercase `sha256`; `download_url` is omitted until the anonymous HTTPS object readback and DEV-origin CORS check verify the exact bytes. The producer writes immutable objects under `cli/versions/<version>/` and updates `cli/stable/latest.json` only after all four assets, manifest, and checksums are consistent and verified. Prereleases do not update stable latest; an older stable release cannot replace a newer one.

The independent frontend loader reads `NEXT_PUBLIC_CLI_RELEASE_METADATA_URL`, uses `credentials: omit` and `cache: no-store`, and fails closed for missing, invalid, unavailable, or unverified assets. DEV build wiring is limited to `deploy/engine/providers.py`, `deploy/components/frontend.sh`, and `scripts/local-cloud-env.sh`; Production receives an empty CLI metadata URL. No strict runtime-config schema, CaC schema, Profile/MCP panel, or shared Shell/navigation file was edited. The coordinator remains sole editor for the shared `/cli` navigation slot.

## Validation

- Frontend: `npm run lint`, `npm test` (536 Node tests and 343 component tests passed), `npm run typecheck`, and `npm run build` passed.
- Go: `go vet ./...`, `go test ./... -count=1 -race`, and `go build ./...` passed. After the final checksum consistency guard, `go test ./internal/clirelease -count=1 -race` passed.
- Python and deployment: CLI packaging/source suite (6 tests), `scripts/test_engine_workflow.py`, full `deploy/engine/tests` suite, `apps/bff/scripts/test_local_dev_makefile.py`, and `scripts/test_cd_contract.py` passed; `bash -n scripts/local-cloud-env.sh deploy/components/frontend.sh` passed.
- Release packaging: cross-compiled and packaged all four target binaries; the extracted native Darwin/arm64 archive returned exactly `lwc-sync dev`. The POSIX shell argv fixture executed the generated command and verified the exact argument vector.
- Workflow and route: `actionlint` passed for `.github/workflows/cli-release.yml`; the built `/cli` route returned HTTP 200. The frontend component tests exercise the lane and its accordion behavior.
- Not run locally: native Windows/PowerShell, Linux, and Darwin/amd64 execution (the worker host is Darwin/arm64; `pwsh` is unavailable). Their native smoke/argv checks are present in the manual workflow but that workflow was not dispatched.

## Publication and remaining gates

No GCS write, credential, IAM, public-access, Production, or release Action was performed. The coordinator verified the existing DEV bucket and frontend-config read/CORS path, but CLI-prefix object write permissions and anonymous reads of newly published CLI objects remain unverified. The existing manual Actions/WIF path requires conditional object creation, readback for idempotent retries, generation-conditioned update of `cli/stable/latest.json`, and anonymous HTTPS/CORS verification for each asset; the coordinator owns any trigger and publication decision.

Source PR URL: pending branch publication. This bounded implementation checkpoint does not mark LWC-385/386/387 complete; same-editor review/repair and coordinator-owned merge/DEV remain outstanding.
