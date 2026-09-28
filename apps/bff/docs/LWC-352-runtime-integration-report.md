# Profile runtime integration report

Status: owned runtime implementation and connected local verification complete; no live provider acceptance or deployment claim.

## Connected runtime

- `/internal/profile/dispatch` is an authenticated POST entrypoint in the production BFF router. Google ID-token validation requires the configured audience, exact scheduler service-account email, and verified email. Unconfigured identity returns 503; ordinary application JWTs do not grant dispatch authority. The HTTP body cannot choose a project or owner.
- `profile_runtime_work` is a durable Firestore outbox. Saves enqueue the exact derivation attempt and persisted three-minute due time in the same transaction; candidate creation/retry enqueues tagging; a successful compiler persists the SHA-256 of its actual manifest, committed object generation, concepts digest and consumed bootstrap pin and queues compile reconciliation atomically.
- Bounded external scheduler ticks process due work, with five-minute leases, four-minute request budgets, a three-attempt retry bound, persisted backoff, and transaction-level lease-token validation. A crashed attempt can be reclaimed after lease expiry; the old execution cannot mutate Profile state. No background in-process polling timer substitutes for durable scheduling. Superseded revisions are terminal and active tuples remain unchanged on incomplete work.
- Derivation binds the existing configured DeepSeek `llm.Client`, including provider-reported model metadata. Tagging uses the existing LWC-334 TypeSafe System One HTTP request pattern (`https://api.typesafe.ai/v1/systemone`, existing `TYPESAFE_JEV_API_KEY` / `TYPESAFE_API_KEY` names). Requested `jev-latest` accepts only explicitly pinned `jev-1.13.0`; a changed returned identity is a missing result. Independent record applicability/evidence/match questions preserve unknown and not-applicable. Experimental probability thresholds are not copied to calibrated confidence or authorized hard filters.
- The compiler captures Profile revision/digest and bootstrap guidance at compile start. Only a fully successful publisher plus successful receipt recording emits a compile receipt; partial failures and unresolved ambiguous commits do not. Reconciliation preserves active writing guidance, derives dictionary changes from canonical concepts, and queues tag coverage.
- Tagging uses immutable generation/source inventory, canonical concepts, per-key durable checkpoints and prior immutable decisions. Complete coverage and deep readback precede the existing Profile activation CAS. Manual confirmation activates only a complete ready set, while pending confirmed work remains automatically queued.

## Resource/config boundary

The source binds existing runtime configuration and requires explicit `PROFILE_RUNTIME_AUDIENCE` and `PROFILE_RUNTIME_SERVICE_ACCOUNT`. External Scheduler configuration, identity/IAM, TypeSafe secret mapping, and Firestore composite index for `profile_runtime_work(pending,due)` remain unprovisioned. No cloud mutation, secret-file read, live provider call, commit, push or deployment occurred.

## Current verification

- Production packages compiled locally after integration.
- `go test ./internal/profiletags -count=1` passed including explicit local Jev HTTP transport tests, independent exact record input, missing evidence, malformed responses, returned-model rejection, cancellation, bounded response size and sanitized errors.
- Emulator tests `TestProfileRuntime*` passed through actual SaveProfile/outbox, authenticated HTTP entrypoint, existing `llm.Client` with an explicit intercepted transport, due-time boundary, crash recovery, stale token/revision fencing, forged ownership, bounded retries and no bootstrap activation.
- Worker `go test ./cmd/olw_worker ./internal/profileruntime -count=1` passed with actual publisher callback fake cases covering success, confirmed readback, failed/ambiguous publish and receipt failure.
- The initially claimed emulator was not running; restored the already-installed Firebase emulator jar using local OpenJDK on `127.0.0.1:8585`, without network/cloud access.

## Connected local acceptance

The production dispatcher, Firestore repository, existing GCS client and real provider adapters run together using only the loopback Firestore emulator and explicit HTTP fakes:

1. Save → persisted three-minute debounce → generation-bound manual dictionary/guidance → confirmation → independent source/concept tagging → G1 activation.
2. Compact successful-compile receipt/outbox → G2 dictionary derivation → reuse unchanged source decisions and evaluate changed concepts → complete G2 activation, with the guidance reference unchanged.
3. Neutral clear → confirmation → explicit complete empty tag set → later empty-active G3 compile, without any additional provider call and with the empty guidance preserved.
4. Generation-free bootstrap derivation → explicit confirmation → first successful compile receipt carrying the consumed bootstrap pin → dictionary and complete tag coverage → first activation; the exact confirmed writing instruction is retained.

Other checks cover authenticated/forged/absent identity, early and duplicate ticks, production `llm.Client` HTTP transport and reported-model provenance, scoped authorization, stale saves, expired-token fencing, recovery after a running claim, bounded retries, abandoned final leases becoming visible failures, retry checkpoints, incomplete coverage preserving active, CAS conflict, corrupt artifacts, independent source/concept judgments and replaced source-object versions. First confirmation restarts failed tagging once; duplicate confirmation does not reset a live lease. The actual compiler receipt transaction/outbox is tested in a named local emulator database, including matching duplicates preserving the queue lease and conflicts failing closed.

The tagging reader follows the frozen publisher contract: the publisher validates private post-run `source_status.json` against pinned raw input before committing the hash-bound immutable inventory. `SourceStatusDigest` is that publisher-attested provenance; the private mutable receipt is not a reader input. The reader verifies snapshot hash, generation, ID map, every source identity/path/digest/object version and canonical concepts. It reuses existing `ReadFileWithGeneration`, which reads bytes and their actual version together under the existing size bound; a version other than the snapshot row is rejected, never silently substituted. No GCS or generation-schema edit was made by this worker.

## Final owned gates

From `apps/bff`, all passed with the loopback emulator and synthetic provider responses:

```text
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/profilederive ./internal/profileartifacts ./internal/profiletags ./internal/profileruntime ./internal/llm ./internal/handler/v1 ./cmd/bff ./cmd/olw_worker -count=1
go vet ./internal/profilederive ./internal/profileartifacts ./internal/profiletags ./internal/profileruntime ./internal/handler/v1 ./cmd/bff ./cmd/olw_worker
go build ./cmd/bff ./cmd/olw_worker
```

Raw local gate logs: `/tmp/profile-runtime-final-tests.log`, `/tmp/profile-runtime-final-vet.log`, `/tmp/profile-runtime-final-build.log`. No live provider request or cloud mutation was performed.

## Remaining parent-owned work

- The parallel Query reader currently expects `cache/source_status.json` to be manifest-listed, contrary to the existing publisher's private-receipt contract. Its reader/fixture alignment remains with the parent/Query owner; the runtime's connected GCS tests already use the real publisher-compatible manifest shape without that file. This report does not claim whole-product Query acceptance.
- Scheduler/IAM, runtime environment and secret mappings, the Firestore outbox index, deployment and live-provider quality/DEV acceptance remain unprovisioned or unperformed as required. The dispatcher is callable locally; no external scheduled resource was fabricated.
- Parent review and overall gates remain separate from the successful owned gates. All nested workers settled and their terminals were released.
