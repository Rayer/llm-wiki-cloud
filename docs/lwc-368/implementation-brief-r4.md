# LWC-368 implementation brief — frozen cac-spec-r4

Owner accepted freeze → implementation after spec review PASS. Live issue State Submitted, freeze comment 4-1914 exact-readback verified. Coding owner: this Codex worker; preserve session for subsequent repairs. Reply/report in Traditional Chinese.

## Mandatory inputs and identity

Discussion evidence root: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-362-363-config-discussion/docs/lwc-362-363`.
Read `cac-spec-r4.md` (frozen bytes; SHA256 `645e96c87aca95e90e5327219e2df648cc093ea338e8bff07c7c0f853a87ffd6`), `cac-review-r4-result.md`, and `cac-freeze-implementation-r4.md`. The freeze addendum supersedes historical not-yet-frozen/not-implementation wording, not the acceptance contract. Reference-only examples under `cac-example` are a starting point, not formal values or implemented resolver evidence.
Read repository instructions, manifests and neighbouring code before edits. Shared workflow navigation: `/Users/rayer/.hermes/workflows/lwc/SSOT-INDEX.md` and `recent-changes.md`; roles `development-workflow` and `implementer` are in this package. No need to load Hermes's whole skill catalog. Independently read live ticket if inherited setup works: `python3 /Users/rayer/.hermes/workflows/lwc/handoff.py youtrack LWC-368`; do not dump credentials or search another profile's secrets.

Worktree `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-368-pipeline-cac`; branch `Rayer/LWC-368-pipeline-cac`. Orca default base origin/main currently equals reviewed baseline `2180d48274aec48b89b6ccf35eea5e2b08ff1e25`; verify git status/head before work. Do not touch the discussion or shared-package worktrees. No hard dependency on BFF/Frontend tickets or ongoing LWC-361 worktree; inspect source integration seams without editing its checkout.

## Execute the frozen scope

Implement project-wide SSOT → selected-environment/target prepare resolver → `synto.pkl` fresh `synto.toml`, with renderer-only defaults and explicit SSOT precedence. Preserve present production values/semantics; do not copy example numbers as defaults. Genuine GSM SDK resolver, lazy actual dependencies including shared/local GSM, explicit failures, no cache/fallback/provisioning. Public output only contains nonsecrets/references; private bindings must reach actual consumer across deploy→run, not only prepare-child env.

Make targets `config-local`, `config-dev`, `config-prod` generate only. Integrate shared generator into existing manual deploy Actions/GCS delivery, with config-only branch that does not build/push/replace image. SSOT changes/merge/local generation do not publish automatically. Worker reads deployed GCS TOML once at run start, freezes it for that run; old manifest/legacy TOML and migration must not overwrite it. Preserve project data/state. Include article token consumption and fixed timeout execution/cancellation required for LWC-362/363; Pipeline Worker under apps/bff and necessary Job invocation are in scope, BFF config migration is not.

Relevant source: Makefile; `.github/workflows/{deploy-dev,cd,promote-production}.yml`; `.github/actions/deployment-engine`; `deploy/engine/{engine,providers,profiles}`; `apps/bff/cmd/olw_worker/{main,synto_adapter,synto_execution,cloud_publish,lease_liveness}`; `apps/bff/internal/handler/v1/endpoints.go`; generation manifest restore paths. Trace actual definitions/usages.

Keep effective-config diff/automatic selection/restart/redeploy and new availability scheme Deferred, not a gate. Do not implement LWC-369/370, tune values, add adaptive timeout or assume four-hour timeout. Do not close any ticket.

## Allowed execution and stop boundaries

Source edits, necessary dependencies, local builds/tests and synthetic fixtures are authorized. Run real Pkl compiler, relevant existing complete regression suites and repository Make checks as applicable; record failures honestly and causal coverage. Pkl already available `/Users/rayer/.local/bin/pkl` 0.32.1; existing package cache `/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages`. Use workspace/supplied TMPDIR for test scratch; never put secrets in general artifacts/logs/args. Synthetic payloads must be unmistakably test-only, not fake live evidence.

No commit/push/PR publication, merge, Actions triggering, live GCS/GSM/provider reads or writes, IAM, credential changes or Production authority. No sandbox bypass. Preserve reviewable edits and prepare publication/evidence instructions; TPM handles concrete next authority. If a permission dialog blocks an allowed local operation, identify the exact command, not permanent permission. Missing authorized GSM version/identity, real Actions run/runtime evidence or live outer timeout value are concrete verification prerequisites: report accurately, continue independent local work, do not invent values or mark full acceptance done.

## Delivery checkpoint

Produce `docs/lwc-368/implementation-report-r4.md`: source changes, source/defaults mapping with units, exact commands/exits, named acceptance-to-evidence mapping, synthetic vs live distinction, current git head/status/diff, unresolved prerequisite and next action. Preserve red/green causal evidence for lazy isolation, default precedence, fresh render, config-only no-image commands, manifest/legacy/new/clean-rebuild state preservation, run-start snapshot, timeout cancellation and secret-to-consumer/non-disclosure. Do not say implemented-and-accepted if live criteria remain unverified. Stop at local candidate/evidence ready (pending publication and same-SHA TPM+Reviewer review), or concrete blocked/cancelled checkpoint, not after a stub or plan. Keep this session recoverable for findings.
