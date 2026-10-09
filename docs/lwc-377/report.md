# LWC-377 project-key implementation report

## Delivery

Implemented the LWC-375 project-key contract in the BFF and Web Account Settings flow. Work started from worktree HEAD `26527e79376e57756842b210e5a57eaa48526d9f`; changes remain uncommitted for parent review and the later LWC-336 handoff.

Review identity is scoped as follows: `cb79f1ab7e71989471d0bf9e` passed the concrete LWC-375 contract review; `dfeb023d714ebe33a9a61d09` passed only the earlier Spike/design scope. Neither is evidence of product acceptance, and the contract document's **NOT RUN** labels describe the earlier contract-only state.

## Implementation

- `apps/bff/internal/auth/project_key.go` implements `lwc_pk_<32 lowercase hex id>.<43 unpadded Base64URL secret>` (83 ASCII characters), 16-byte random IDs, 32-byte random secrets, SHA-256 secret verifiers, canonical parsing and constant-time digest comparison. Firestore records use `internal/firestore.Collection(fs, "project_keys")` with schema version 1, a fixed `query` capability and only `active`/`revoked` states. Public metadata never includes the secret or digest.
- Each project-key query checks the current account and current project-owner authority from the record's bound user and project. Key authentication is mounted only on `POST /api/v1/query`; it fills the query context from the record and rejects a mismatched `X-Project-ID` before the executor. Web/CLI JWT query behavior remains available, and other protected BFF routes retain JWT-only auth.
- `apps/bff/internal/handler/v1/project_keys.go` adds Web-owner management APIs: `GET` and `POST /api/v1/projects/:pid/keys`, plus `POST /api/v1/projects/:pid/keys/:keyID/revoke`. Creation accepts only a bounded JSON `name`; management rejects CLI sessions and hides foreign/missing project or key as 404. For these new routes only, a valid Web JWT with an unavailable current-account lookup returns 503 without mutation. Existing public Auth routes, including login/register/refresh/logout, keep their existing behavior.
- Creation performs one Firestore `Create` and one readback on a write error. A matching readback may return the in-memory secret once; absent, mismatched, or unavailable readback returns 503 with only a safe key ID and never retries the write. Absence is treated as inconclusive because a delayed commit can appear later; the UI instructs the owner to refresh and revoke the key if it appears. Revoke is idempotent and reads back an uncertain transition.
- `apps/frontend/src/components/ProjectKeysSection.tsx` adds the current-project-only, query-only key section inside Account Settings. It supports list/refresh, create, one-time copy/dismiss and confirmed revoke. The secret stays in component memory and is cleared when the modal closes or the user/project context changes; list responses do not restore it. English and Traditional Chinese messages are included. No scope selector or project picker was added.
- No application setting, Pkl/SSOT entry, generated config, provider integration, or deployment change was needed. Dynamic key records remain Firestore data; no key or secret is placed in configuration, logs, URLs, local storage or session storage.

## Verification results

All commands below ran in the local worktree. Go test commands unset the configured LLM/provider API key variables; BFF emulator tests used only the local Firestore emulator at `127.0.0.1:8585` with synthetic fixture data.

| Command | Result |
| --- | --- |
| `go test ./internal/auth ./internal/handler/v1 ./cmd/bff -count=1` (`apps/bff`, local emulator) | PASS: all three affected packages; auth 63.169s, handler/v1 1.231s, cmd/bff 0.815s. Covers key format/hash/schema, authorization failures, uncertain writes, management/query routes, Web/CLI compatibility and public Auth route behavior. |
| `go test ./... -count=1 -race` (`apps/bff`, local emulator) | **FAIL on two full-suite runs** in unrelated `cmd/demo_password_rotate`: `TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry/transaction_error_after_possible_commit` reported “ambiguous transaction was reported as success or retried”. All other packages printed PASS, including `internal/auth`, `internal/handler/v1`, and `cmd/bff`. |
| `go test ./cmd/demo_password_rotate -count=1 -race -v` (`apps/bff`, no LWC366 emulator target) | PASS in one isolated run; the two LWC366 emulator tests skipped because `LWC366_FIRESTORE_EMULATOR_HOST` was unset. This does not resolve the repeated full-suite-only failure; its cause remains unknown and the package is outside this change's ownership. A discriminating follow-up is to investigate its fake-store transaction/readback behavior under full-suite scheduling. |
| `go vet ./...`; `go build ./...` (`apps/bff`) | PASS. |
| `npm run test:node` (`apps/frontend`) | PASS: 527 tests, 0 failures. |
| `npm run test:component -- --maxWorkers=1 --no-file-parallelism` (`apps/frontend`) | PASS: 34 test files, 312 tests, 0 failures. Includes project-key component/API flow and Account Settings regression coverage. |
| `npm run lint`; `npm run typecheck`; `npm run build` (`apps/frontend`) | PASS. Production build compiled, typechecked and generated all 15 static pages. |
| `python3 ../../scripts/test_cd_contract.py` (`apps/bff`) | PASS (exit 0). |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (`apps/bff`) | PASS: 70 tests. |
| `python3 scripts/test_local_dev_makefile.py` (`apps/bff`) | PASS: 20 tests. |

During implementation, the first route test caught a Gin wildcard-name conflict (`:projectID` versus the existing `:pid`); the management routes were aligned to `:pid`, after which the affected Go packages passed. A first frontend component run also exposed an unstable workspace test mock; it was corrected and the final single-worker component suite above passed. These resolved attempts do not change the outstanding full Go-suite failure.

## Scope and remaining acceptance

Tests used only local emulators and synthetic secrets; no live Firestore, actual key issuance, login session, paid Query, external provider, IAM change, public tunnel or deployment was used. There is no new config to generate. No commit, push, PR or merge was created. The implementation is ready for code review; the repeated full-suite failure in the unrelated `cmd/demo_password_rotate` package remains the only observed suite-level failure and should be investigated by its owner before treating the repository-wide Go race suite as green.

## Revoke readback recovery repair addendum (LWC-377)

The previous UI behavior could leave a revoked key's stale active row actionable when the revoke call had an ambiguous result or when the revoke succeeded but its follow-up list read failed. `ProjectKeysSection.tsx` now marks the key list stale before a revoke, performs one list read after either a successful or failed mutation, and hides the old list/actions if any list read fails. A successful read replaces the list and restores actions based on the returned state; an ambiguous mutation whose fresh list still shows the key active displays the mutation error and leaves a user-confirmed retry available. The UI never replays the revoke automatically, and a failed read after a successful mutation does not display the revoke-success notice. If the mutation errors but the fresh list shows the key revoked, the notice reflects that readback state.

The component tests cover (1) an ambiguous mutation whose committed revoke appears in the fresh list, (2) mutation success followed by list HTTP 503, and (3) mutation HTTP 503 plus list HTTP 503 followed by a successful explicit refresh. They assert that no stale revoke action is available while the list is unavailable and that there is only one mutation request. The show-once secret and Account Settings user/project remount boundaries are unchanged.

### Verification for this repair

All commands ran in `apps/frontend` against local test fixtures; no live service, login, provider, paid request, or deployment was used.

| Command | Result |
| --- | --- |
| `npm run test:component -- tests/lwc-377-project-keys.test.tsx --maxWorkers=1 --no-file-parallelism` | PASS, exit 0: 1 test file, 8 tests. |
| `npm run test:component -- --maxWorkers=1 --no-file-parallelism` | PASS, exit 0: 34 test files, 315 tests. |
| `npm run test:node` | PASS, exit 0: 527 tests, 0 failures, 0 skipped. |
| `npm run lint` | PASS, exit 0. |
| `npm run typecheck` | PASS, exit 0. |

This addendum records local UI recovery verification only. The independent review of the revised candidate remains pending; prior review identities in this report retain their original, narrower scopes. No build, backend suite, full repository suite, deployment, or production acceptance was run for this repair.

### Repair artifact identity

Base Git HEAD remains `26527e79376e57756842b210e5a57eaa48526d9f`. SHA-256 after the repair:

| File | SHA-256 |
| --- | --- |
| `apps/frontend/src/components/ProjectKeysSection.tsx` | `b45e1be6b21f445f9f85fbc1f5cb3d5a925e0f780bf0d5b55719d666820702f6` |
| `apps/frontend/tests/lwc-377-project-keys.test.tsx` | `9bbe2e6c3ab8c479b527f40e1d55395a0a18f6ba894a67261ff279b487414ddc` |

## Lost create-response recovery and test reliability addendum (LWC-377)

A create failure with no returned `key_id` can now be treated as uncertain when the client sees a transport/response exception or an HTTP 408/5xx response. The UI clears any prior one-time secret before sending, issues exactly one metadata-list read after the uncertain result, and never retries the create mutation. When that read succeeds, the owner sees the current list and a localized English/Traditional Chinese message that says the outcome is unknown, the secret was not shown, and any newly listed key with the requested name should be revoked before another attempt. No ID is invented. When the read fails, the list is cleared and hidden, create is disabled, the notice remains uncertain, and a load error directs the owner to explicit refresh; a successful refresh restores the current list and actions. Known-key-ID uncertainty continues to use its existing recovery message. Definite 4xx errors without a key ID remain ordinary create errors.

Three component cases model a generic transport exception after a server commit, an unreadable successful response body after a commit, and the unavailable-list control. The GET mock returns the new active key for the first two; during the outage control, old rows/actions and create stay unavailable until explicit refresh. They assert exactly one POST, no secret display, no fabricated key ID, no automatic retry, and create remains disabled until a successful explicit refresh. The component validates the response shape before queuing UI state so an unreadable body is handled as an uncertain outcome. Independent review had reproduced 3 wrong-secret fixture failures in 256 repetitions; the deterministic guard in `project_key_test.go` now chooses `A` or `B` based on the generated secret's first character, so the synthetic wrong secret is guaranteed to differ while remaining canonical. No production auth code changed.

### Verification for this addendum

Commands ran against local tests/fixtures. Go commands unset exactly the provider variables listed by `apps/bff/Makefile` (`LLM_API_KEY`, `DEEPSEEK_API_KEY`, `SYNTO_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `TYPESAFE_API_KEY`, `TYPESAFE_JEV_API_KEY`, `LWC331_TEST_API_KEY`). No live credentials, provider calls, login, paid Query, deploy, or external service was used.

| Command | Result |
| --- | --- |
| `npm run test:component -- tests/lwc-377-project-keys.test.tsx --maxWorkers=1 --no-file-parallelism` (`apps/frontend`) | PASS, exit 0: 1 file, 11 tests, final source. |
| `npm run test:component -- --maxWorkers=1 --no-file-parallelism` (`apps/frontend`) | PASS, exit 0: 34 files, 318 tests, final source. |
| `npm run test:node` (`apps/frontend`) | PASS, exit 0: 527 tests, 0 failures, 0 skipped. |
| `npm run lint` (`apps/frontend`) | PASS, exit 0 after removing a redundant synchronous state update from the load effect. An earlier run exited 1 on `react-hooks/set-state-in-effect`; the final run passed. |
| `npm run build` (`apps/frontend`) | PASS, exit 0: optimized Next build, TypeScript, and 15 static pages completed. |
| `npm run typecheck` (`apps/frontend`) | PASS, exit 0 after build. An initial run overlapped `next build` and exited 2 while `.next/types` files were being recreated; a later run caught an unsafe test-fixture `Response` cast (TS2352), changed it to `as unknown as Response`, and the final run passed. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY go test ./internal/auth -run '^TestProjectKeyCreateAuthenticateListAndRevoke$' -count=256` (`apps/bff`) | PASS, exit 0: 256 repetitions, 0.478s. |
| `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY go test ./internal/auth -count=1` (`apps/bff`) | PASS, exit 0: complete affected auth package, 1.187s. |

### Final focused manifest and review status

Base Git HEAD is `26527e79376e57756842b210e5a57eaa48526d9f`; all candidate edits remain uncommitted. The earlier independent review `60fb062b2c71310ed98dba08` returned HOLD for immutable manifest `1d8f1a81a2977af7`. The source changes below address its create-recovery finding and the separate flaky-test finding. This worker performed a focused re-read against the manifest below; it is not an independent reviewer result, and the revised candidate must receive a new focused review. No source PASS is claimed.

The focused source manifest uses SHA-256 rows in the order shown; its aggregate SHA-256 is `e4424ff379e40ef09ef89c56feb6cac3d3ad272e4625efdc18f09ba181067ea3`.

| File | SHA-256 |
| --- | --- |
| `apps/frontend/src/components/ProjectKeysSection.tsx` | `a9ed761e2c0a1a3b95c4faa866f8c71aff1745b7a92366b18b2fa5be56844d5f` |
| `apps/frontend/tests/lwc-377-project-keys.test.tsx` | `3deffaf37b713886eb8a5b40df8584dc32141e1b5c661b61613d2a9d3f03f47c` |
| `apps/frontend/src/messages/en.json` | `8d5f50f0f1ae8b168bec6cf871f19f62713c500f3d6ccce37df6fef5e6dd3c4f` |
| `apps/frontend/src/messages/zh-TW.json` | `0c9b1ba3ee5cf09f27c023e7fd040356c84342fadd1a8c75f8725e1d4f901c33` |
| `apps/bff/internal/auth/project_key.go` (unchanged) | `59a10e4a13d098dfa5db963c50c474ac5a313685c113227c815b9be71c4f8acb` |
| `apps/bff/internal/auth/project_key_test.go` | `1a7ab83c2422078f7cfa69a4191b6e627e94d5d514a1d7ad163c7a728fe6fbf2` |

No LWC-336 report/evidence, auth product policy, service config, or other profile was changed by this follow-up. The separately observed deployment-engine metadata errors (1359) remain out of scope; no engine code was changed. No commit, stage, push, PR, merge, or deployment was performed.
