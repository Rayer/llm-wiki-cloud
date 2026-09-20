# Go staged-runner migration report

Implemented on `Rayer/JevImpl`, base
`e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d`.
The existing dirty/untracked production work was retained. No commit, push,
deployment, tracker action, cloud write, credential-file access, or global setting
change was performed. The diagram and toolbox handoff were not edited.

## Delivered behavior

- `query_experiment staged` owns 10/20/50/60/70/80/90/100, ONLY/TO/FROM,
  independent run directories, compatible checkpoints/forks, and failure records.
- Query stages call the existing production handler/executor directly, with
  context cancellation/deadlines. Worker index/suggestions retain their algorithms.
- Synto uses public `init`, `run --auto-approve --max-rounds 2 --min-confidence 0`,
  and production agents pack export. Private Python APIs/interpreter overrides
  are removed from this local path. Private HOME/XDG, credential allowlists,
  disabled Python bytecode writes, and process-group termination isolate children.
- Stage20 accepts public producer output: successful run/export exits, production
  INDEX validation, every referenced exported body nonempty, a non-root article,
  and unchanged raw bytes/mtimes. No SQLite schema reads or package-layout probes
  remain. Public INDEX failure counts must be zero. A usable partial run can still pass; exhaustive internal completion is not
  proven and the receipt says so. Fresh-state and opaque snapshot integrity gates
  remain. Old native-schema receipts are rejected by the validation discriminator.
- Copies preserve bytes/mtimes, reject links/special files, and enforce bounds.
  Raw identity is rechecked at stage checkpoints. Atomic receipts retain partial
  runs and safe failure categories. Query no-evidence/citation rejection remains
  an observation, never fabricated success.
- Receipts explicitly use `lwc-local-go-v2`; schema-1/Python forks fail clearly.
  Old evidence stays in place; explicit corpus import plus fresh query stages is
  the migration route. No slug rewriting or LWC-335 citation change was made.
- Removed only `scripts/local_e2e/{run.py,synto_stage.py,test_run.py,test_synto_semantics.py}`.
  All fixtures and unrelated Python files remain.

Current instructions and exact fresh **DNS baseline** and **DEV import/downstream**
commands are in [the operator guide](local-staged-e2e.md#build-and-fresh-parent-validation).
The query README links this interface and separates historical verification.

## Installed public CLI characterization

Commands used the executable discovered on PATH, with a clean environment and
`PYTHONDONTWRITEBYTECODE=1`. No provider or credential access was needed.

| Command/check | Exit / observation |
| --- | --- |
| `synto --version` | 0; `synto, version 0.7.0` |
| `synto init --help` | 0; `--non-interactive`, optional `--existing`/`--default` |
| `synto run --help` | 0; public approval, max-rounds, min-confidence, fix/dry-run options |
| `synto index --help` | 2; no such command |
| `synto pack export --help` | 0; `--target agents`, `--out`, `--vault` |
| Isolated `synto init ... --non-interactive` | 0; config unchanged, local Git initialization, no commit/state DB/private-home config writes |
| Agents export from that unrun vault | 1, expected missing native state DB; no export published |

Standalone characterization evidence is under
`/private/tmp/jev-synto-public.E2TqPT`; command help/error logs are
`/private/tmp/jev-synto-{init,run,index,export}-help.txt` and
`/private/tmp/jev-synto-missing-db.log`.
Inspection of the installed CLI established that native `run` can return zero
with reported failures. Rechecked `run --help`, `status --help`, and
`pack export --help` for this revision: no structured run failure reporting or
structured status format is advertised. Human-readable `status --failed` is not
a stable exhaustive per-input contract and is not parsed. Public INDEX stats do
not prove every raw source compiled; output acceptance intentionally makes no
such claim.
No private imports were executed for characterization or the new runner.

## Verification commands and exits

Unless indicated otherwise, commands below run in `apps/bff` with
`GOCACHE=/private/tmp/jevimpl-go-cache`. The focused tests contain **17 new Go
orchestration tests**, plus existing production-seam/citation/import tests and a
new worker public-export boundary test. Provider/orchestration fakes are labeled;
mock HTTP is not evidence of live inference.

| Command/check | Exit / result |
| --- | --- |
| `go test ./cmd/query_experiment -run '^TestStaged\|^TestLocalPlatform\|^TestMaterialize\|^TestImportManifest\|^TestLocalIdentity' -count=1` | 0; focused orchestration/production checks |
| Same focused selection with `go test -race` | 0; no race finding |
| `go test ./cmd/olw_worker -count=1` after final export-environment fix | 0; 21.903s, full worker suite |
| `go test ./cmd/query_experiment ./cmd/olw_worker ./internal/queryquality ./internal/queryruntime ./internal/queryconfig ./internal/gcs ./internal/localfs -count=1` | 1 overall: query fixture listener panic; other six packages passed |
| Query suite with the seven listener tests explicitly excluded (command below) | 0; sandbox-compatible suite |
| Frozen-evidence command below | 0 for worker and query packages; explicit mocks, read-only old inputs |
| `go vet ./...` after final code changes | 0 |
| `go build -o /private/tmp/jevimpl-go-e2e-tools/query_experiment ./cmd/query_experiment` | 0 |
| `go build -o /private/tmp/jevimpl-go-e2e-tools/olw_worker ./cmd/olw_worker` | 0, including final worker isolation change |
| `/private/tmp/jevimpl-go-e2e-tools/query_experiment staged --help` | 0 |
| Source-only command below | 0; mechanism success, schema `lwc-local-go-v2`, checkpoint10, oracle not requested |
| `gofmt -l` on changed Go files | 0, no files listed |
| `git diff --check` | 0 |

The unrestricted query suite is **not claimed to pass here**. Its first existing
listener test, `TestFixturePersistenceDeepScrubsProviderEchoWithoutMutatingTrace`,
panics on `listen tcp6 [::1]:0: bind: operation not permitted`. Seven existing
HTTP-listener tests were excluded only in the explicitly named sandbox run;
none were changed or weakened:

```sh
GOCACHE=/private/tmp/jevimpl-go-cache go test ./cmd/query_experiment \
  -skip 'TestFixture(PersistenceDeepScrubsProviderEchoWithoutMutatingTrace|ProviderFailuresPersistScrubbedTruthfulAttempts|ModelCallSendsSelectedEndpointModelAndKeyOnlyOnHTTP|RunWritesEightReceiptsAndSummaryWithoutKey|AttemptWritesTimingFieldsInRecordAndFinalReceipt|ZeroQualifiedAttemptWritesStatusReasonInResultsAndFinalReceipt|NonemptyAttemptWritesOkAndQualifiedEvidenceInReceipts)$' \
  -count=1
```

Read-only frozen-evidence checks, reusing the original parent paths:

```sh
LWC_E2E_SUGGESTED_SNAPSHOT=/private/tmp/lwc-live-dns-baseline/snapshots/50 \
LWC_E2E_CITATION_RUN=/private/tmp/lwc-cloud-downstream \
GOCACHE=/private/tmp/jevimpl-go-cache go test ./cmd/olw_worker ./cmd/query_experiment \
  -run 'TestSuggestedDiagnostics|TestSuggestedCorpusFailure|TestLocalSuggestedCommand|TestFrozen|TestStagedCitation|TestLocalIndex' -count=1
```

Actual final source-only smoke from the repository root:

```sh
/private/tmp/jevimpl-go-e2e-tools/query_experiment staged \
  --output /private/tmp/lwc-go-prepared-20260920-v2 \
  --raw scripts/local_e2e/fixtures/raw-dns --to 10 \
  --query-config scripts/local_e2e/fixtures/query-dns.json
```

This output already exists; keep it and choose a new path to repeat. Earlier
source-only evidence at `/private/tmp/lwc-go-prepared-20260920` is also retained.
Its `execution: live_providers` adapter label does **not** mean inference ran:
only source preparation was selected, and no inference credential was passed.

Logs: `/private/tmp/jev-go-{focused-final,race-focused,worker-final,query-sandbox-final,frozen-checks,vet-final}.log`.
An early new test failed because its test helper supplied both snapshot and fork;
that helper was corrected and the test passed, including the race run. Two early
Go invocations from the repository root exited 1 (`go.mod` not found), then were
rerun from `apps/bff`. One shell wrapper exited 1 after a successful test because
`status` is readonly in zsh; subsequent wrappers use `check_exit`. These were not
counted as passing command invocations.

## Remaining parent validation

The new implementation is verified offline. Parent reports the unrestricted seven-package test command passed before this
revision. Parent still owns scope review, fresh public-Synto DNS inference, and new pinned
DEV import/downstream validation. Producer output acceptance is tested with
controlled public INDEX/body fixtures; **a new full native producer run was not performed**.
Public initialization was exercised for real; production query reuse was
exercised with mocked HTTP, including resolved citations and synthesis-only
forks with no fresh expansion. No new live provider or cloud-import success is
claimed. Historical DNS/cloud evidence and the negative LWC-335 diagnostics keep
their original meaning.

## Public-boundary revision (parent review)

Removed the orchestrator's private SQL schema reader and installation-wide Python
package hashing. Identity is public version 0.7.0 plus resolved executable path
and SHA-256. An unchanged launcher/version cannot detect changed package,
interpreter, or dependency contents; no full implementation digest is claimed.
The receipt explicitly records `internal_completeness: "not_proven"`. Public
export failure counters reject reported failures, but a usable partial output
with zero reported failures can pass. No public contract verified here proves
that every raw compiled, every concept completed, or all drafts were published.
This scope limitation needs parent assessment before live verification.

Physical line counts (`wc -l`, including comments/blanks): runner 934, bounded
filesystem/process helpers 300, public Synto adapter 167, unsupported-platform
stub 13: **1,414 production lines**, down from 1,458 (44 fewer). Staged tests:
826 lines. Existing local query and worker adapters add 341 and 112 lines;
including those seams gives 1,867 production lines. No new framework, interface,
dependency or executable was added. The redundant native-validation test hook
was removed. Retained overhead implements explicit stage selection, provenance,
forks, bounded snapshots, isolation, deadlines, receipts and acceptance checks.
Stage20 adds one offline export and bounded INDEX/body read; stage50 still does
its existing production export and identity/materialization work. Temporary
stage20 exports are removed. Query execution remains direct product reuse.

Final focused verification, from `apps/bff` (exit 0):

```sh
GOCACHE=/private/tmp/jevimpl-go-cache go test ./cmd/query_experiment ./internal/wikiindex -run 'TestStaged|TestDecodeSyntoIdentityPlan' -count=1
GOCACHE=/private/tmp/jevimpl-go-cache go build -o /private/tmp/jevimpl-public-boundary-tools/ ./cmd/query_experiment ./cmd/olw_worker
```

Test log: `/private/tmp/jev-public-boundary-tests.log`. Tests exercise actual public
export validation behind mocked child calls, missing/invalid/empty bodies,
reported failures, unrepresented raw inputs, raw bytes/mtime mutation, child exit
failures, launcher identity, and retained orchestration/safety gates. The installed
public init test also passes without inference. During revision, an obsolete hook
reference caused an initial compile failure; a new test initially expected
`failed` instead of existing `partial`/`failure` receipt statuses. Both test issues
were corrected. One invocation from repository root failed to find go.mod and
was rerun from `apps/bff`. These are not counted as passing invocations.

No new live inference, cloud verification, credential access, global changes or
remote writes were performed. Parent's prior full unrestricted seven-package
pass remains prior evidence, not a full-suite claim for this revision.
