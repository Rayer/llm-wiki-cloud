# LWC-353 implementation report

## Implemented

- Added project-scoped recompile-all capability and request routes. The capability remains denied with `byok_required`; the request path requires Project edit permission and returns 403 before quota or worker admission. Existing incremental pipeline and controlled admin recovery routes are unchanged.
- Added a worker-start Profile guidance resolver. It reads the Profile state once, pins the exact active immutable artifact, or—when there is no active generation—accepts only confirmed bootstrap guidance matching the current Profile revision and ordered requirements digest. Missing, pending, stale, invalid, or unreadable nonempty guidance fails before workspace materialization; an empty Profile still has no overlay.
- Reused `internal/profileartifacts` path and canonical validation helpers. The worker reads the exact project-scoped object with a 1 MiB bound and validates its content hash and reference. A bootstrap pin retains its typed `BootstrapGuidanceRef`, confirmed status, confirmation timestamp, schema version, and exact guidance content; ordinary active-generation pins have no bootstrap ref.
- Added an ephemeral Synto `vault-schema.md` overlay that appends only `compile_guidance` to the existing or pinned-wheel schema. It preserves existing mandatory conventions, rejects a materialized schema over 1,500 Unicode characters before compilation, and never truncates or changes project vault files.
- Installed cloud lease cleanup before the guidance resolver so a resolver failure releases the lease and records the worker failure.
- Registered the recompile and bootstrap routes, added route coverage, annotated bootstrap endpoints, and regenerated Swagger.

## Verification

The Firestore emulator ran only on loopback at `127.0.0.1:8585`, using disposable project ID `lwc353-local` and a command-scoped Homebrew OpenJDK runtime. No remote Firestore or live provider was used.

Passed commands, run from `apps/bff`:

```sh
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 GOOGLE_CLOUD_PROJECT=lwc353-local go test ./cmd/olw_worker -count=1
go test ./internal/profileartifacts -count=1
go vet ./cmd/olw_worker
go build -o /Users/rayer/.hermes/profiles/chatgpt/cache/scratch/lwc353/olw_worker ./cmd/olw_worker
sh scripts/test_flash_execution.sh
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 GOOGLE_CLOUD_PROJECT=lwc353-local go test ./internal/handler/v1 -run 'TestRecompileAllCapabilityAndDenialAreScopedAndFailClosed|TestProfileBootstrap' -count=1
go test ./cmd/bff -run TestProductionRouterRegistersRecompileAllAndBootstrapRoutes -count=1
go build ./...
```

The Synto smoke script installed the pinned `synto==0.7.0` artifact in a temporary virtual environment and intercepted provider traffic with its loopback mock. Its explicit prompt assertion passed for both Profile guidance and the mandatory vault conventions in the actual Synto compile prompt.

`go generate ./cmd/bff` completed and the generated Swagger contains the bootstrap and recompile-all paths. `go vet ./...`, `go build ./...`, and `git diff --check` passed on the final worker, route, and Swagger edits.

The latest full `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 GOOGLE_CLOUD_PROJECT=lwc353-local go test ./... -count=1` run is not green. The remaining failures are in 352-owned files: `internal/handler/v1` tests `TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt` at `profile_firestore_test.go:429` and `TestRunProfileDerivationPersistsBootstrapWithDeterministicFakeProvider` at line 568, plus five `internal/profilederive` executor tests reporting `already_running` or unexpected transition results. The parent was notified; these files were left to 352. Do not treat the full test gate as passing until those tests are fixed and rerun.

## Remaining integration

- Rerun `go test ./...`, `go vet ./...`, and `git diff --check` after the 352-owned test fixes. The parent is coordinating that work.
- The first-generation bootstrap promotion is deliberately not called here. This worker only retains the batch-start pin; a later caller must provide a real successful published generation and canonical concepts digest to 352's guarded promotion API after that interface lands.
- LWC-355's immutable by-generation manifest and retained-snapshot publisher/reader seam remains a separate pending slice. The parent directed this worker not to expand LWC-353 into that work.
- The mock-provider prompt test is deterministic adapter evidence only; no live-provider acceptance was performed.
