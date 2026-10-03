# DEV 37127575743: latest checkpoint lookup diagnosis and proposal

> **Superseded disposition:** Owner acceptance 3584-1777 and Parent direction 4-1776 replaced the four-page/400-artifact proposal below with complete `per_page=100` pagination and no fixed artifact-count ceiling. The current implementation and exact acceptance are documented in `deployment-engine-spec-r2-accepted-appendix-latest-checkpoint.md` and `latest-checkpoint-implementation.md`. The remainder is retained as the historical diagnosis and unaccepted proposal that preceded that disposition.

## Scope and incident evidence

This is an offline diagnosis under the existing 3584-1774 / 3584-1775 authority. No source, workflow, provider, checkpoint contract, or manifest was changed. No live API, provider, Git, Actions, credential, IAM, or Production operation was run.

Parent-provided run metadata says DEV run `37127575743` completed prepare successfully: checkpoint 3, Frontend receipt and ready/prepared artifacts were produced, and the Auth receipt was reused. Runtime stopped before snapshot or component work with `command-failed`, result stage `ready`, `component=null`, `mutation_may_have_happened=false`, and empty `components`. I read the saved result/state summaries in `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37127575743-result`; they show failed/ready, no component state, and no mutation. Parent's read-only lookup found 333 repository artifacts, returned the first 100, and found no target state artifact among that page. This does not establish whether a matching state artifact exists on a later page or was removed/expired.

## Causal offline reproduction

`dev-37127575743-latest-bounded-causal-red-verified.txt` exercises the actual `Engine.runtime_guard()` through its real `support.run()` subprocess and the actual production `deploy/engine/artifacts.cjs`. The only substituted boundary is `fetch`/the Actions Artifact SDK, both test-only and offline. The mock list response has `total_count=333`, 100 returned non-state artifacts, and zero target state candidates. The real path made one mocked list request, attempted zero downloads, wrote no `.latest.json`, left the checkpoint unchanged, and returned `command-failed`, exit 1, with no mutation.

The earlier `dev-37127575743-latest-bounded-causal-red-final.txt` is retained as raw harness output but is superseded: its evidence formatter indexed Node's `process.argv` slice incorrectly, so the printed operation/target labels were shifted. The verified file above uses the corrected field mapping; both runs exercised the same production failure path.

The call chain is:

1. `Engine.runtime_guard()` removes the transient `.latest.json` and invokes Node with `latest`, the target, and that path (`deploy/engine/engine.py:374-382`).
2. `artifacts.cjs` requests only `actions/artifacts?per_page=100`, filters the returned page by `lwc-state-${target}-`, and throws `Error('target checkpoint outside bounded lookup')` when that page has no candidate while `total_count >= 100` (`deploy/engine/artifacts.cjs:40-47`).
3. Its outer catch discards the original error and emits only `artifact transport failed (details suppressed)` (`deploy/engine/artifacts.cjs:61`). `support.run()` receives exit 1 but has no stage for this call; it therefore does not attach a structured cause (`deploy/engine/support.py:163-203`). `Engine.result()` can serialize a cause when present, but none reaches it (`deploy/engine/engine.py:525-550`).

The confirmed cause is the current single-page bounded-lookup branch treating an incomplete list as an error. It is not evidence of an IAM, provider, or mutation failure. The first page's lack of a matching artifact is not proof that the full repository has no checkpoint.

## Minimal lookup contract proposal — disposition required before implementation

First verify from a cached/official API contract whether the repository artifact-list endpoint supports filtering by artifact name and whether the filter is exact-name or prefix. The current source proves only client-side prefix matching. Artifact names are generated as `lwc-state-<environment>-<plan-id-prefix>-<run-id>-<attempt>-<sequence>` (`deploy/engine/engine.py:103-110`), so an exact-name filter cannot be replaced by an assumed prefix query. Do not send an undocumented filter.

If a documented server-side prefix filter exists, filter only the existing `lwc-state-<target>-` prefix and still read every result page within an explicit bound. If filtering is exact-name only or unsupported, use the documented pagination mechanism instead; do not fabricate an exact name or infer prefix behavior.

For a concrete bounded proposal, keep page size 100 and scan at most four pages / 400 returned artifacts. This covers the reported 333-artifact list. Verify consistent `total_count` and complete page coverage through the calculated final page. Select the matching candidate with the largest artifact ID across the complete scan, preserving current ordering semantics. If the final page is reached and no target checkpoint exists, return “no latest checkpoint” so the existing runtime guard applies its current ready/non-ready rules. If the reported total exceeds 400, a page is missing/inconsistent, or the complete scan cannot be established, fail closed before provider mutation; never choose from a partial scan. The 400-item ceiling is a proposal for Owner/Parent disposition, not an accepted implementation value.

Keep expiration semantics: after choosing the highest-ID matching checkpoint, if it is expired or cannot be downloaded/validated, fail with that error. Do not fall back to an older unexpired checkpoint. Pagination must not weaken the existing trusted-workflow validation on download or the later plan/sequence/status guard.

## Bounded original-cause propagation proposal — disposition required

Preserve the generic result reason, exit code, mutation/status/action behavior, and add a structured cause for the fixed latest-checkpoint subprocess stage. The transport should carry the actual originating exception name and message (for this branch: `Error` and `target checkpoint outside bounded lookup`) through the existing result cause field, with the existing bounded message handling. Keep the original subprocess exit code. Do not include argv, environment values, API response bodies/headers, or credentials. The present generic catch prevents the original cause from reaching Python at all; the local reproduction therefore records only the generic child error. This proposal does not add a general logging or diagnostics framework.

## Runtime guard versus platform retention

The engine's latest guard checks stale plan/sequence and unresolved statuses after a checkpoint is found; it does not clean up artifacts. GitHub artifact retention/expiration is a separate platform lifecycle. The current transport rejects an expired highest-ID candidate and does not fall back (`deploy/engine/artifacts.cjs:49-50`). The new lookup must preserve that behavior. The incident facts do not show that GitHub deleted or expired the relevant state artifact.

The only next discriminating step is Owner/Parent disposition of (a) documented server-side filter semantics, if any, (b) the explicit scan ceiling, and (c) the additive cause stage/fields. No implementation or repeated full-suite run was performed pending that disposition.
