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
