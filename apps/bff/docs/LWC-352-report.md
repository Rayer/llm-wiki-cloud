# LWC-352 implementation report

**Status: incomplete for production execution.** The artifact, derivation, bootstrap API, callable attempt executor, compile evidence fence, and emulator test paths are implemented. The persisted debounce is not connected to a delayed dispatcher or production provider, and the successful publisher result is not yet wired to the compile reconciliation caller, so this report does not claim a runnable end-to-end LWC-352 flow.

## Implemented

- `internal/profileartifacts` encodes immutable bootstrap, dictionary, and guidance envelopes. It validates exact-byte SHA-256 references, strict UTF-8/JSON, duplicate and unknown keys, trailing data, and the 1 MiB limit; writes are create-only and verified by exact-byte readback.
- `internal/profilederive` consumes the full ordered requirements set, accounts for every requirement in previews, validates provider output and returned model identity, validates the canonical concepts snapshot, and provides bootstrap, manual, neutral-clear, and compile-auto derivation functions. Clearing an active Profile to neutral reads the pinned concepts snapshot only to bind its exact digest; it does not call a provider.
- `Handler.RunProfileDerivation` is a callable attempt executor. It reauthorizes the Project, claims the persisted attempt, pins the current manifest when generation context is needed, reads only the manifest-listed ID map and concepts file, writes immutable artifacts, and commits through revision/attempt/digest-fenced Firestore transitions. A duplicate claim that finds the attempt already running returns `already_running` without calling the provider again.
- `Handler.RunProfileCompileDerivation` requires a typed `ProfileCompileSuccess` built from a successful publisher result. It matches the committed manifest object generation and concepts digest to the current pinned generation; first-generation promotion also requires the exact typed bootstrap ref retained by the worker at compile start. With an active Profile it writes only the new dictionary and preserves the active guidance ref.
- Bootstrap GET and explicit confirmation handlers expose `{ "bootstrap_guidance": null|... }` and require matching immutable revision, expected Profile revision, and current ordered input digest. A new save clears the bootstrap pointer and supersedes prior pending work.
- The shared BFF route table currently registers the bootstrap handlers. The 353 worker reads the confirmed bootstrap object at first compile and blocks nonempty Profiles when guidance is unconfirmed.
- `internal/llm.ChatWithMetadata` exposes the provider-returned model identifier while preserving existing `Chat` behavior.

## Runnable and verified

- The focused Firestore emulator test calls the actual `Handler.RunProfileDerivation` entrypoint with a deliberately labeled deterministic fake provider and an in-memory create-only object store. It verifies the generation-free artifact and transactional preview pointer without creating candidate, active, job, manifest, or concept state.
- A Firestore emulator test verifies that the first successful generation stages an idempotent compile-auto candidate, retains the confirmed writing-guidance ref, leaves `Active` null, and clears the guidance diff before the Tag coverage job runs.
- The loopback Firestore emulator was available at `127.0.0.1:8585`; bootstrap confirmation, stale attempt/save fencing, duplicate confirmation, and superseding save tests passed.
- Commands passed from `apps/bff`:

  ```text
  go test ./internal/profileartifacts ./internal/profilederive ./internal/llm ./internal/handler/v1 -count=1
  go test ./cmd/olw_worker -run 'ProfileGuidance' -count=1
  go test ./cmd/bff -run 'Profile|Route' -count=1
  ```

- These tests use deterministic fake provider output; no live provider call or cloud mutation was made.

## Remaining blockers

- **Delayed dispatch is unconnected.** Firestore persists `scheduled_for` and attempt identity, and `RunProfileDerivation` validates and claims that work, but no approved dispatcher/service-claim endpoint or dedicated Profile Cloud Run Job URL is wired. The current BFF handler is constructed with a nil LLM client. `pipeline/run` is not an acceptable substitute. A shared scheduler/config seam and production binding to the existing configured `internal/llm` client are still required.
- **Crash recovery after claim is not defined.** A duplicate invocation of a currently running attempt is now suppressed before any provider or object work. If the process exits after changing the attempt to running, however, there is no lease timeout/reclaim contract; a durable runner must define how it detects and retries that abandoned attempt without starting overlapping provider calls.
- **Provider behavior is unverified live.** Only the fake-provider path is exercised; live model availability, response shape, latency, and quality were not checked.

The approved bootstrap route contract and artifact decision are recorded in [profile-artifacts-contract.md](profile-artifacts-contract.md). No commits, pushes, deploys, secret reads, or live provider calls were made.

## Remediation follow-up

The coordinator reproduced a RED in `TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt`: bootstrap completion updated `derivations/current` but left the corresponding immutable-key `derivations/current/attempts/{attemptID}` document at `scheduled`. The transaction now reads, validates, and updates both documents atomically for claim (`scheduled`→`running`), normal derivation success (`running`→`ready`), bootstrap success (`running`→`ready`), and failure (`running`→`failed`). New Firestore assertions cover each sibling history transition and preserve the original completion-history assertion.

Changed production history writes: `internal/handler/v1/profile.go:738-765` (claim), `:808-849` (normal success), `:874-897` (failure), and `:1376-1394` (history reader); `internal/handler/v1/profile_bootstrap.go:193-223` (bootstrap success). Test assertions are at `internal/handler/v1/profile_firestore_test.go:136-202` and `:412-428`. Neutral clear now requests concepts when the current Profile has an active tuple, so both neutral envelopes use SHA-256 of the exact pinned concepts JSONL bytes rather than an unconditional empty-byte digest; `internal/profilederive/executor.go:81` and `internal/profilederive/executor_test.go:147-172` cover this case.

At that checkpoint, the compile promotion path's pinned-ref guard was unaccepted; the remediation below adds the transaction and input guard. This slice does not change the worker/publisher ABI or add dispatcher/provider runtime wiring.

### Raw verification

Initial RED, before the fix:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt -count=1 -v
=== RUN   TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt
    profile_firestore_test.go:429: completed attempt history changed unexpectedly: data=map[attempt_id:ASVMkIBuNi4C7565AV0w attempt_number:1 error_code:<nil> requirements_digest:b935a95046bf3a47e76eab94f9f2bace1c9e588b481dbc26771f415f3abbea51 retry:false revision:1 scheduled_for:2026-09-25T01:03:00Z status:scheduled] err=<nil>
--- FAIL: TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt (0.01s)
FAIL
FAIL	github.com/rayer/llm-wiki-bff/internal/handler/v1	0.408s
FAIL
```

GREEN focused tests after the fix:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run 'TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt|TestRunProfileDerivationPersistsBootstrapWithDeterministicFakeProvider' -count=1 -v
=== RUN   TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt
--- PASS: TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt (0.02s)
=== RUN   TestRunProfileDerivationPersistsBootstrapWithDeterministicFakeProvider
--- PASS: TestRunProfileDerivationPersistsBootstrapWithDeterministicFakeProvider (0.01s)
PASS
ok  	github.com/rayer/llm-wiki-bff/internal/handler/v1	0.618s
```

Standalone rerun of the coordinator's exact history test:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt -count=1 -v
=== RUN   TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt
--- PASS: TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt (0.02s)
PASS
ok  	github.com/rayer/llm-wiki-bff/internal/handler/v1	0.425s
```

Full requested package set with the loopback emulator and `go vet`:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/profilederive ./internal/llm ./internal/profileartifacts ./internal/handler/v1 -count=1
ok  	github.com/rayer/llm-wiki-bff/internal/profilederive	0.213s
ok  	github.com/rayer/llm-wiki-bff/internal/llm	0.368s
ok  	github.com/rayer/llm-wiki-bff/internal/profileartifacts	0.237s
ok  	github.com/rayer/llm-wiki-bff/internal/handler/v1	0.701s

go vet ./internal/profilederive ./internal/llm ./internal/profileartifacts ./internal/handler/v1
```

`go vet` exited 0 with no diagnostics. `go test ./internal/profilederive -count=1 -v` also passed every derivation, transition, and duplicate-running test, including the neutral-clear digest assertion.

## Compile evidence remediation

The initial compile reconciliation accepted a generation ID plus Profile revision/digest, then treated the current confirmed bootstrap pointer as sufficient proof that the worker consumed its guidance. `NewProfileCompileSuccess` now builds the in-process, non-JSON `ProfileCompileSuccess` from the publisher's committed `generation.Manifest` and object-generation result (rejecting any nonnil compile error), plus the typed `BootstrapGuidanceRef` retained on the worker's compile-start guidance pin. It binds the exact manifest byte digest and canonical concepts digest; the handler compares these with the current pinned snapshot and validated concepts bytes. The Firestore promotion transaction also compares the consumed typed ref with the still-confirmed bootstrap pointer for the same Profile revision and ordered requirements digest.

Changed guard lines are `internal/handler/v1/profile_derivation.go:20-91` and `:112-214` for the typed success input and pinned-generation comparison, `internal/handler/v1/profile.go:1216-1221` and `:1305-1320` for transactional consumed-ref validation, and `:1910-1918` for the completion seam. Tests at `internal/handler/v1/profile_test.go:18-74` reject failed/incomplete publisher evidence and mismatched generation/digest evidence; `profile_firestore_test.go:676-719` covers unconfirmed, absent, mismatched, valid/idempotent, and superseded-ref transitions. Valid transaction promotion leaves `Active` nil until Tag coverage activation. The active-Profile branch continues to stage only Tags and preserves guidance.

The actual publisher functions return `(generation.Manifest, int64 manifestObjectGeneration, error)`, while `runCloudWorkerBatch` currently discards those values and returns only an error. No worker/publisher caller was changed in this slice; the runtime must pass the successful publisher result and `cfg.pinnedProfileGuidance.BootstrapRef` through `NewProfileCompileSuccess`, then call `RunProfileCompileDerivation`. The constructor rejects a nonnil batch error and the handler cross-checks the manifest with the current pinned snapshot, but trustworthy delivery of the actual result and distinguishing other batch types remain integration work while the caller is unconnected; the input is not exposed over HTTP.

The missing-pin regression was first run against the old transition and failed because promotion succeeded without worker pin evidence:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run TestFirestorePromotesConfirmedBootstrapAfterFirstCompile -count=1 -v
=== RUN   TestFirestorePromotesConfirmedBootstrapAfterFirstCompile
    profile_firestore_test.go:676: bootstrap compile promotion succeeded without an explicit consumed worker pin
--- FAIL: TestFirestorePromotesConfirmedBootstrapAfterFirstCompile (0.02s)
FAIL
FAIL    github.com/rayer/llm-wiki-bff/internal/handler/v1  0.628s
FAIL
```

After the fix, the focused and owned package commands passed:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run 'TestProfileCompileSuccessCarriesOnlySuccessfulCommittedManifest|TestFirestorePromotesConfirmedBootstrapAfterFirstCompile' -count=1 -v
=== RUN   TestFirestorePromotesConfirmedBootstrapAfterFirstCompile
--- PASS: TestFirestorePromotesConfirmedBootstrapAfterFirstCompile (0.03s)
=== RUN   TestProfileCompileSuccessCarriesOnlySuccessfulCommittedManifest
--- PASS: TestProfileCompileSuccessCarriesOnlySuccessfulCommittedManifest (0.00s)
PASS
ok      github.com/rayer/llm-wiki-bff/internal/handler/v1  0.652s

FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 ./internal/profilederive ./internal/profileartifacts ./internal/llm -count=1
ok      github.com/rayer/llm-wiki-bff/internal/handler/v1       0.687s
ok      github.com/rayer/llm-wiki-bff/internal/profilederive    0.387s
ok      github.com/rayer/llm-wiki-bff/internal/profileartifacts 0.241s
ok      github.com/rayer/llm-wiki-bff/internal/llm              0.367s

go vet ./internal/handler/v1 ./internal/profilederive ./internal/profileartifacts ./internal/llm
```

`go vet` exited 0 with no diagnostics. No live provider, cloud, credential, commit, or deploy operation was performed.
