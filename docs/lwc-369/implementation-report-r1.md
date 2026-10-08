# LWC-369 Implementation Report (r1)

## Execution identity

- Task: `task_b2de5bddc467`; Dispatch: `ctx_5ff8a750d464`.
- Model / effort: GPT-6-Luna / xhigh.
- Orca terminal session: `term_a1c5e73c-c9ba-4604-86a3-314a800b72c1`.
- Base SHA: `9a16947b9db2e56adf76e47f93a7a6b202b7d926`.
- Branch: `Rayer/LWC-369-implementation-r1`.
- Implementation commit: `362bfdbe30ff1b40a5e185d21147bdaa817e6db4`.
- PR: [#106](https://github.com/Rayer/llm-wiki-cloud/pull/106), open against `develop` (base SHA at creation: `55fa221c91daac4da7f3e7b4b30a642848469df8`).
- Execution stayed local. No DEV or Prod provider action, GSM payload read/write, IAM/resource operation, paid Pipeline, or LLM inference was run.

## Acceptance matrix

| Acceptance | Result | Local evidence / limit |
| --- | --- | --- |
| AC1: Full schema-2 BFF config from real Pkl prepare for local/dev/prod, selected secret references only, compatibility with existing Pipeline config | PASS | `TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction` passed all three named subtests against Pkl 0.32.1. |
| AC2: Strict file loader, validation, bounded size, no stale app-environment fallback, and secret-safe errors | PASS | `go test ./internal/config ./cmd/pipeline_config -count=1` passed; full Go suite passed. |
| AC3: Startup and runtime consumers use the file config, retain exact cloud scopes, query/profile/LLM options, and local JWT interoperability | PASS locally | Full Go suite and the real-Pkl test passed, including `TestConfiguredProductionQueryCompositionLoadsImmutableRuntime`, `TestNewClientWithDatabaseAndScopeIgnoresInheritedScope`, `TestNewClientWithScopeIgnoresInheritedEnvironmentScope`, and `TestClientOptionsPreserveBasePathAndRequestTimeout`; fixtures do not call a paid LLM. |
| AC4: Fake adapter prepare/publish/pin/reconcile/rollback boundary; no payload in plan/artifacts; uncertain publication is conservative | PASS locally | Four named deployment-engine acceptance tests passed, covering numeric version pinning and rollback, private preparation, unavailable file, and uncertain publication. |
| AC5: Authorized DEV non-root readability and numeric mount adoption | NOT RUN | Requires the separately authorized DEV provider action; no cloud action was performed. |

## Verification run

All commands below ran in the isolated implementation worktree. A nonzero exit is called out explicitly.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test ./cmd/pipeline_config -run '^TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction$' -count=1 -v` | 0 | 1 test, 3 subtests: local, dev, prod passed. |
| `go test ./internal/config ./cmd/pipeline_config -count=1` | 0 | Both named Go packages passed. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY go test ./... -count=1` | 0 | All Go test packages passed. |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_engine.Acceptance.test_bff_config_version_is_pinned_and_rollback_retains_the_prior_mount test_engine.Acceptance.test_bff_config_adapter_prepares_private_file_and_pins_published_numeric_version test_engine.Acceptance.test_bff_config_adapter_unavailable_file_fails_before_publication test_engine.Acceptance.test_bff_config_adapter_uncertain_publication_is_retained_without_retry` | 0 | 4 named engine acceptance tests passed. |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_engine.Acceptance` | 0 | All 44 deployment-engine acceptance tests passed. |
| `python3 deploy/engine/tests/test_engine.py` | 0 | All 45 deployment-engine acceptance and source-applicability tests passed. |
| `python3 deploy/engine/tests/test_build_submission.py` | 0 | All 25 deployment-engine build-submission tests passed. |
| `python3 scripts/test_bff_auth_config_contract.py` | 0 | 3 tests passed. |
| `python3 scripts/test_auth_config_contract.py` | 0 | 9 tests passed. |
| `python3 scripts/test_production_auth_config_contract.py` | 0 | 9 tests passed. |
| `python3 scripts/test_engine_workflow.py` | 0 | 8 tests passed. |
| `python3 apps/bff/scripts/test_local_dev_makefile.py` | 0 | 19 tests passed. |
| `python3 -m py_compile deploy/components/auth_config.py deploy/engine/engine.py deploy/engine/providers.py deploy/engine/tests/fake_provider.py deploy/engine/tests/test_build_submission.py deploy/engine/tests/test_engine.py scripts/local-services.py scripts/test_auth_config_contract.py scripts/test_bff_auth_config_contract.py scripts/test_cd_contract.py scripts/test_engine_workflow.py scripts/test_production_auth_config_contract.py apps/bff/scripts/test_local_dev_makefile.py` | 0 | Syntax compilation passed. |
| `python3 -m py_compile deploy/components/auth_config.py scripts/test_cd_contract.py` | 0 | Rechecked the final legacy version-reuse changes. |
| `bash -n deploy/components/auth_config.sh` | 0 | Shell syntax passed. |
| `make lint` | 0 | ESLint passed after installing declared frontend dev dependencies with `npm --prefix apps/frontend ci --include=dev`. |
| `make typecheck` | 0 | TypeScript check passed. |
| `make vet` | 0 | `go vet ./...` passed. |
| `make build` | 0 | Go build and Next.js production build passed. |
| `make workflow-yaml` | 0 | All four checked workflow YAML files passed. |
| `make smoke BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000` | 0 | Local smoke passed using a temporary Git context; the configured worktree local state/key and default ports were preserved. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY npm --prefix apps/frontend test` | 0 | 524 Node tests and 292 component tests passed. |
| `git diff --check` | 0 | No whitespace errors. |
| `python3 scripts/test_cd_contract.py` | 0 | All 70 deployment contract tests passed, including legacy exact-version reuse, freeze, rollback, and retry readback. |
| `make test` | First captured call 2; two later calls 0 | The first captured invocation returned 2 without a matched failure line; two later full invocations exited 0, so that failure was not reproduced. The latest run executed the BFF race suite, 70 CD contract tests, 21 config contract tests, 19 local Makefile tests, 524 Node tests, and 292 component tests. Its Go output reported 90 skipped test cases and 25 skipped subtests because local emulator/environment prerequisites such as `FIRESTORE_EMULATOR_HOST` were unset; skips are not counted as passes or acceptance evidence. |

## Legacy path compatibility

The official `.github/workflows/cd.yml` release path uses the deployment engine, which publishes and pins the exact numeric BFF config version. The legacy `deploy/components/bff.sh` path performs image-only mutation and now carries forward the exact numeric file version mounted on its active revision. After asking and escalating without a coordinator reply, I chose exact-version reuse to preserve compatibility; it reads only revision metadata and does not read or republish the secret payload. `scripts/test_cd_contract.py` passes all 70 cases, and the latest full `make test` run exits 0.

## Cloud limits and release checkpoint

AC5 remains NOT RUN. No cloud credentials, resources, configuration payloads, or provider state were accessed. Implementation commit `362bfdbe30ff1b40a5e185d21147bdaa817e6db4` is included in PR #106; the branch was pushed and the open PR was read back against `develop`. No merge or deployment was attempted.

## PR #106 canonical CI compatibility repair r2

### Execution identity and source attribution

- Task: `task_6bd30a24d36e`; Dispatch: `ctx_f7be577b3011`.
- Model / effort: GPT-6-Luna / xhigh; retained YOLO session, Orca terminal `term_a1c5e73c-c9ba-4604-86a3-314a800b72c1`.
- Worktree / branch: isolated `LWC-369-implementation-r1`; `Rayer/LWC-369-implementation-r1`.
- Repair base/head before changes: `c6e8ec69c7f95880ee701aa5237fff46ec35a15d`.
- Tested code commit: `de29df58c9d7077776d4a0a00baac1cdb60f54c6` (the report-only follow-up is committed separately).
- PR: [#106](https://github.com/Rayer/llm-wiki-cloud/pull/106), open against `develop`, base SHA `55fa221c91daac4da7f3e7b4b30a642848469df8`. Before publication, the complete existing PR body was read back; it matched the summary, verification, limits, and report link recorded above.
- The reported canonical CI run `37716490331` failed at the repair base in `PipelineConfigContract.test_config_only_uses_real_normalizer_with_controlled_providers` for both development and production. The real `pipeline_config_only.run_config_only` selected `worker`; the normalizer incorrectly required a generated BFF descriptor for that Worker-only projection.

### Repair and acceptance impact

The normalized Worker component consumes Worker fields from the reviewed environment config and does not consume `BFF.RuntimeInputs`; the config-only Pipeline caller uses the Worker job, bucket, and secret binding while preserving the digest-pinned image. The normalizer and engine admission now prepare/apply the generated BFF descriptor only when `bff` is selected. BFF selections still require and strictly validate the descriptor, and a Worker-only plan rejects an unrelated descriptor; no BFF secret resolution, publication, mount, or config-target mutation was added to Pipeline config-only.

| Acceptance | r2 result |
| --- | --- |
| AC1–AC4 | Prior local PASS evidence remains in the r1 matrix; this compatibility repair does not change their implementation. |
| AC5 | NOT RUN; no DEV runtime, provider, GSM, IAM, or resource actions were performed. |
| Canonical CI for new PR head | Pending main-owned same-SHA TPM/reviewer and canonical CI checkpoints. |

### r2 verification

All commands ran locally in this isolated worktree. Exit 0 denotes a command pass; skips are called out separately.

| Command | Exit | Result |
| --- | ---: | --- |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_pipeline_config.PipelineConfigContract.test_config_only_uses_real_normalizer_with_controlled_providers -v` | 0 | 1 named test passed for development and production against the real normalizer and controlled provider fixtures. |
| `go test ./cmd/deploy_config -count=1` | 0 | Go normalizer tests passed, including Worker-only selection without BFF input and rejection of an unrelated descriptor. |
| `python3 scripts/test_engine_workflow.py` | 0 | 8 workflow/engine contract tests passed. |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 0 | All 118 deployment-engine tests passed. |
| `python3 scripts/test_cd_contract.py` | 0 | All 70 deployment contract tests passed. |
| `go test ./internal/config ./cmd/deploy_config -count=1` | 0 | Both Go packages passed. |
| `go test ./cmd/pipeline_config -run '^TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction$' -count=1 -v` | 0 | 1 named real-Pkl test and all 3 local/dev/prod subtests passed. |
| `make test` | 0 | Retained repository suite completed, including Go race tests, 70 CD contracts, 21 auth-config contracts, 19 local Makefile tests, 524 Node tests, and 292 component tests. Local emulator/environment-gated skips are not passes; the prior full-suite record above reports 90 Go test cases and 25 Go subtests skipped with emulator prerequisites unset. |
| `make lint` | 0 | ESLint passed. |
| `make typecheck` | 0 | TypeScript check passed. |
| `make build` | 0 | Go and Next.js production builds passed. |
| `git diff --check` | 0 | No whitespace errors. |

The code repair was committed as `de29df58c9d7077776d4a0a00baac1cdb60f54c6`; this report is the report-only follow-up so the tested code SHA remains explicit. The dispatched completion records the exact final remote PR head readback. Same-SHA TPM, independent reviewer, and canonical CI remain with main. No PR merge, deployment, or cloud action was attempted.

## PR #106 bounded compatibility repair r3

### Execution and source attribution

- Task: `task_e649b942a992`; Dispatch: `ctx_3de906e81fc1`; model / effort: GPT-6-Luna / xhigh, retained YOLO session; Orca terminal: `term_a1c5e73c-c9ba-4604-86a3-314a800b72c1`.
- Worktree / branch: isolated `LWC-369-implementation-r1`; `Rayer/LWC-369-implementation-r1`. Repair base: `2bd5b975e591c945a401ad9d7fd8e7733465aaaa`. Tested implementation commit: `c7abda140fe14e211a47d99ead5e6e1970c7fd53`.
- PR: [#106](https://github.com/Rayer/llm-wiki-cloud/pull/106), open against `develop` at base `55fa221c91daac4da7f3e7b4b30a642848469df8`. The complete PR body was read before implementation publication. Before this report-only follow-up, the implementation head was read back from both `git ls-remote` and the PR API as `c7abda140fe14e211a47d99ead5e6e1970c7fd53`.
- The prior canonical CI failure was run `37726191265` at the repair base: retained Apps BFF discovery ran 117 tests and returned 3 errors. The reproduced caller failure was the stale `--bff-config` flag and schema-1 fixture in `apps/bff/scripts/test_bff_explicit_cutover.py`; the current normalizer requires a generated descriptor via `--bff-inputs`. Source evidence is `/Users/rayer/.hermes/workflows/lwc/lwc369-ci-caller-evidence-2bd5b975.md`.
- Review provenance: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/369-review-attempt2-recovered.json` records attempt 2 as `invalid_output`, with no formal review result. Its recovered report said `HOLD`, but the trailing verifier warning means that was recovered evidence, not a formal validated HOLD. It identified three source findings: Worker-only release omitted the nonsecret Firestore database compatibility value; retained BFF safety fixtures were stale; and legacy numeric-version reuse could pair another secret resource with the planned destination.

### Bounded repair

1. Worker-only normalization now copies the already-reviewed `auth.firestore_database_id` into the existing nonsecret BFF compatibility projection consumed by `Providers.deploy`. The actual Worker adapter is exercised for development and production with controlled transport and exact database assertions; the plan has no BFF runtime inputs or BFF secret update. The real config-only caller test still passes with `image_unchanged=true`.
2. The explicit cutover suite now obtains its normalized plan from real Pkl `pipeline_config prepare --descriptor` output and passes `--bff-inputs`. Its synthetic current revision uses the generated resource and numeric, read-only file mount while retaining readback, secret-redaction, accepted-mutation, freeze, and rollback coverage. The shared `bff-service-before.json` and `bff-revision-before.json` remain legacy retained-revision fixtures for the separate deployment-evidence suite; the new numeric mount is built locally by the explicit cutover test.
3. Legacy numeric-version lookup now requires the mounted secret resource to equal the plan’s complete destination resource before returning its numeric version. A synthetic different-resource/same-version case fails closed without exposing the resource name; exact resource/version reuse still produces args for the planned numeric mount.

### r3 acceptance matrix

| Repair / acceptance | Result | Evidence |
| --- | --- | --- |
| Worker-only normalizer carries the correct nonsecret Firestore database for dev and prod, and actual `Providers.deploy` uses it | PASS | New named engine acceptance test passed both environment subtests with one controlled Worker update and no secret update. |
| Config-only Pipeline caller keeps the image unchanged and does not require BFF runtime inputs | PASS | `test_config_only_uses_real_normalizer_with_controlled_providers` passed for development and production. |
| Retained Apps BFF safety suite migrated to generated descriptor and numeric mount | PASS | Exact Apps BFF discovery passed all 118 tests, including cutover readback/redaction, accepted mutation, freeze, and rollback. |
| Legacy version reuse binds only the exact planned secret resource and numeric version | PASS | New synthetic wrong-resource/same-version control rejected; exact resource was accepted and `args` bound the numeric version to the plan resource. Full CD suite passed 70 tests. |
| Frozen cloud acceptance AC5 | NOT RUN | No provider, live GSM payload, IAM/resource, paid Pipeline, or deployment action was performed. |
| Final-head TPM / independent reviewer / canonical CI | PENDING | Main owns these checkpoints for the final PR head; no merge or deploy was attempted. |

### r3 verification

Commands ran in this isolated worktree. “Skip” results below remain skips and are not counted as passing acceptance evidence.

| Command | Exit | Result |
| --- | ---: | --- |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (cwd `apps/bff`) | 0 | 118 tests passed. The first diagnostic run returned 56 failures and 1 error because I temporarily rewrote two shared legacy evidence fixtures; those changes were reverted and the new numeric mount was isolated inside the explicit cutover test before the passing rerun. |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 0 | 119 tests passed, including the new Worker normalizer-to-provider integration test. |
| `python3 scripts/test_cd_contract.py` | 0 | All 70 CD contracts passed. |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_engine.Acceptance.test_worker_only_normalizer_drives_real_deploy_with_compatibility_database_scope -v` | 0 | 1 named test passed for development and production. |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_pipeline_config.PipelineConfigContract.test_config_only_uses_real_normalizer_with_controlled_providers -v` | 0 | 1 named test passed for development and production; image stayed unchanged. |
| `go test ./cmd/deploy_config -count=1` | 0 | Normalizer tests passed, including Worker-only projection without BFF descriptor. |
| `go test ./internal/config ./cmd/deploy_config -count=1` | 0 | Both Go packages passed. |
| `go test ./cmd/pipeline_config -run '^TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction$' -count=1 -v` | 0 | 1 real-Pkl test and all 3 local/dev/prod subtests passed. |
| `python3 scripts/test_bff_auth_config_contract.py`; `python3 scripts/test_auth_config_contract.py`; `python3 scripts/test_production_auth_config_contract.py`; `python3 scripts/test_engine_workflow.py` | 0 | 3, 9, 9, and 8 tests passed, respectively. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY go test ./... -v -count=1 -race` | 0 | 46 Go packages passed; 9 packages had no test files. Local environment gates reported 90 skipped test cases and 25 skipped subtests (including 70 emulator checks where `FIRESTORE_EMULATOR_HOST` was unset); skips are not passes. |
| `make test` | 0 | Full root suite passed: CD 70, auth configuration 21, local Makefile 19, frontend 524, component 292, plus the raced Go suite above. |
| `make lint` | 0 | Frontend ESLint passed. |
| `make typecheck` | 0 | TypeScript passed. |
| `make vet` | 0 | `go vet ./...` passed. |
| `make build` | 0 | Go and Next.js production builds passed. |
| `make test-flash-execution` (cwd `apps/bff`, retained CI invocation) | 0 | Pinned Flash wire contract passed using local test fixtures; no paid provider call. |
| `git diff --check` | 0 | No whitespace errors. |

The initial explicit-cutover rollback fixture also needed its prior image digest restored after generating the numeric mount; the focused freeze/rollback test and the full Apps BFF discovery then passed. Invoking `make test-flash-execution` at repository root returned 2 because that target exists only in `apps/bff`; the retained CI invocation from `apps/bff` passed. The new implementation is pushed as `c7abda140fe14e211a47d99ead5e6e1970c7fd53`; final-head review and canonical CI remain main-owned. No merge, deployment, or cloud action was performed.

## r4 bounded frozen-AC1/AC2 repair

This repair was performed in the retained `GPT-6-Luna`, `xhigh`, YOLO session and worktree, terminal `term_a1c5e73c-c9ba-4604-86a3-314a800b72c1`, task `task_5e20d237dc03`, dispatch `ctx_79b4ce0aee10`. The formal independent review artifact `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/369-supervisor-99c8fe67.json` remains a HOLD for its reviewed source `99c8fe674a91be8c6d9be946eeece5b9d0d11cd6`; it independently reproduced both new blockers below. The requested `369-ci-caller-evidence-2bd5b975.md` path was unavailable; I read the matching prior review record at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/lwc369-pr-review-2bd5b975.md`, the full formal HOLD, and verified the actual caller paths and regressions locally. The previous three compatibility findings remain fixed and are not counted as the r4 blockers.

| Acceptance | r4 result | Evidence / boundary |
| --- | --- | --- |
| AC1: Full schema-2 BFF config from real Pkl prepare for local/dev/prod, selected shared secret references only, and compatibility with existing Pipeline config | PASS locally | BFF `prepare` now forwards the selected `LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE` property to the real Pkl evaluation. The synthetic selected resource is preserved exactly through BFF descriptor, resolver, generated private BFF file, and Pipeline config; no payload appears in descriptor, public Pipeline files, logs, or error text. Existing local Pkl prepare with no selected resource retains the env binding. |
| AC2: Strict file loader, validation, bounded size, no stale app-environment fallback, and secret-safe errors | PASS locally | Raw JSON schema validation now rejects null/wrong JSON types for root and nested values, including all six independently reproduced scalar fields. Loader tests confirm invalid files yield no runtime config; `auth_session_environment` must be explicit and nonempty. `registration_enabled`, `local`, and `query.legacy` legal nulls and contract-permitted empty optional strings remain accepted. Legacy non-file configuration paths are unchanged. |
| AC3: Startup and runtime consumers use file config, retaining exact scopes and query/profile/LLM/local JWT behavior | PASS locally, carried from r3 | Full raced Go suite and real-Pkl local/dev/prod prepare passed; no live provider or paid LLM access was used. |
| AC4: Controlled prepare/publish/pin/reconcile/rollback boundary; no payload in plan/artifacts; uncertain publication is conservative | PASS locally, carried from r3 | Full 119-test deployment-engine discovery and 70 CD contracts passed. Named config-only normalizer contract passed and reported `image_unchanged=true`. |
| AC5: Authorized DEV non-root readability and numeric mount adoption | NOT RUN | Requires a separately authorized DEV provider action. No DEV/Prod provider operation, live GSM payload access, IAM/resource operation, paid Pipeline, or deployment action was performed. |

### r4 code and verification

Implementation and test changes are committed at source SHA `b6aaf21e877ee0126306ae50580378241b13940c` on `Rayer/LWC-369-implementation-r1`. The PR is #106, targeting `develop` at base `55fa221c91daac4da7f3e7b4b30a642848469df8`; before push its remote head was the prior r3 SHA `99c8fe674a91be8c6d9be946eeece5b9d0d11cd6`. The complete PR body was read before publication. The exact final PR head/base readback is included in the worker completion record; the source/test SHA above identifies the implementation commit.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test ./cmd/pipeline_config ./internal/config -count=1` | 0 | Changed Pkl prepare and strict BFF file-loader packages passed. |
| `go test ./cmd/pipeline_config -run 'TestRealPklBFFPrepareRendersSchema2ForLocalDevelopmentAndProduction|TestRealPklBFFLocalPrepareUsesSelectedSharedSecretVersion' -count=1 -v` | 0 | Real Pkl local/dev/prod subtests and selected synthetic GSM shared-binding test passed. |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (cwd `apps/bff`) | 0 | All 118 retained BFF tests passed. |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 1 before implementation commit; 0 after | The precommit run had 9 `dirty-build-input` errors because engine provenance checks require committed source. After committing source SHA `b6aaf21…`, all 119 tests passed; this was not a code assertion failure. |
| `python3 scripts/test_cd_contract.py` | 0 | All 70 CD contracts passed. |
| `PYTHONPATH=deploy/engine/tests:deploy/engine python3 -m unittest test_pipeline_config.PipelineConfigContract.test_config_only_uses_real_normalizer_with_controlled_providers -v` | 0 | Named real normalizer config-only case passed; image remained unchanged. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (cwd `apps/bff`) | 0 | All 21 auth configuration contracts passed. |
| `python3 scripts/test_engine_workflow.py` | 0 | All 8 workflow/engine contract tests passed. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY go test ./... -v -count=1 -race` | 0 | 46 Go packages passed; 9 had no test files. Environment-gated local tests reported 90 skipped cases and 25 skipped subtests, including emulator-dependent coverage; skips are not counted as passes. |
| `go vet ./...`; `go build ./...` (cwd `apps/bff`) | 0 | Go vet and build passed. |
| `make test-flash-execution` (cwd `apps/bff`) | 0 | Pinned Flash wire contract passed with local synthetic fixtures; no paid provider call. |
| `make test` (repository root) | 0 | Root suite passed: Go race tests, 70 CD contracts, 21 auth contracts, 19 local Makefile tests, 524 Node tests, and 292 component tests. The same Go run had 90 skipped cases and 25 skipped subtests listed above. |
| `make lint`; `make typecheck`; `make vet`; `make build` (repository root) | 0 | Frontend lint/typecheck, Go vet, and Go/Next production builds passed. |
| `git diff --check` | 0 | No whitespace errors. |

The formal HOLD and its original two negative probes remain attributed to the reviewed `99c8fe…` source; local r4 evidence does not relabel that review as PASS. The new source SHA requires same-SHA TPM review, independent reviewer review, and canonical CI, all owned by main. No merge or deployment was attempted.
