# BYOK profile pool delivery status

**Status: INTEGRATED CANDIDATE / REVIEW AND CI PENDING.** Scope is LWC-434, LWC-435, LWC-436, and LWC-437, frozen by `byok-pool-r1` after independent PASS request `dc629629399e7aea30630602`.

The BYOK source was published at `86c784a4a3a68a214796f9a4b9e46b6a2062fc47` on the existing open, non-draft [PR #167](https://github.com/Rayer/llm-wiki-cloud/pull/167) targeting `develop`; focused review request `0fa0494b0abc3a66bf12bc1c` passed for that exact source. Owner has since authorized integrating the current `develop` tip into this feature branch. The integrated feature-branch candidate still needs same-SHA review and CI; the parent owns PR merge and DEV publication, and Production remains paused.

## Implementation

- Owner-scoped BYOK credentials are encrypted and associated with owner, environment, profile, and credential revision. Profile metadata remains write-only; Project binding changes require explicit Save. The shared pool remains managed after its last profile is removed.
- Query and MCP URL intake use the selected Project source. Query records successful call identities by purpose and preserves Expansion and Synthesis identities separately. Producer transport and consumer aggregation have separate regression coverage.
- Jev Project evaluation consumes the resolved Project chat client. The new tagged fixture creates and binds an encrypted profile through BFF APIs, reloads it through `projectLLMClient`, and proves `JevEvaluator` uses that profile without its static TypeSafe fallback.
- Local/cloud suggested-query generation resolves the selected Project source, preserves never-managed bootstrap, fails closed for unavailable managed sources, and publishes actual model/profile provenance.
- Shared Pipeline retry requires explicit provider quota and fresh candidate resolution. A new success marker stops whole-job Synto replay after any successful selected-profile response; absent, unreadable, or existing marker state fails closed. A retry starts with a fresh workspace only when no selected-profile response succeeded.
- Pinned Synto 0.7.0 retains native OpenAI Chat Completions and Anthropic Messages protocols. Project settings use the `知識庫設定` and `AI 服務` subtabs, retain both drafts across switching, return newly created profiles to the unsaved AI service draft, and bind only after explicit Save.

## Verification on published BYOK source `86c784a4`

- The all-package BFF `go test ./... -count=1 -race`, `go vet ./...`, and `go build ./...` each exited 0. Exact commands, logs, and pinned executable path are recorded in `/Users/rayer/.hermes/workflows/lwc-local/discussions/byok-account-mock/current-source-86c784a/evidence-index.md` and its referenced logs.
- Pinned Synto 0.7.0 protocol/quota/DNS checks and the healthcheck/no-replay worker regression exited 0. Logs show OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`, GET health checks without success markers, explicit quota versus non-quota classification, local A→B first-generation fallback request counts, and no cloud B replay after A succeeded.
- Connected disposable-emulator tests exited 0 for API-created profile/binding → Query → Pipeline worker/Synto → publication and saved-profile Jev evaluation. They use synthetic provider responses and do not claim formal login middleware, live provider, or deployment qualification.
- Parent exact-source sanity at `/Users/rayer/.hermes/workflows/lwc-local/discussions/byok-account-mock/sanity-20261027-2115/` records 559 Node tests, 86 focused UI tests, 7 backend race packages, 8 connected tests with 0 skips, typecheck, and pinned Synto checks passing on `86c784a4a3a68a214796f9a4b9e46b6a2062fc47`.

## Develop integration and combined-tree qualification

- The feature branch integrates live `develop` tip `567e32bdab7d486c8fff00f90b0d8a6f28569d41`. The two conflicts were `AccountSettingsModal.tsx` and `lwc-346-account-settings.test.tsx`; the resolution preserves the develop-side password refresh/session-rotation guard and its tests, plus BYOK panels, Demo suppression, Project assignment, and profile-create return behavior.
- On the combined tree, 11 account, CLI, BYOK, Project-source, and settings suites passed (113 tests); full frontend lint, typecheck, and production build passed. Workflow engine contracts (8 tests) and dispatch-only/immutable-workflow checks (2 tests) passed. Raw logs and the exact merged commit SHA are in `/Users/rayer/.hermes/workflows/lwc-local/discussions/byok-account-mock/integration-567e32b/`.
- Current frontend Node test inputs and all BFF source files are unchanged from the exact-86 tree, so the 559 Node tests and backend race/vet/build logs above are reused by tree identity rather than rerun. The integrated tree still requires parent same-SHA review and CI before PR merge.

## Connected evidence limits and remaining work

- The connected backend fixture proves the persisted binding → Query → Pipeline/Synto path with disposable Firestore and mock provider transports. Current authenticated browser interaction from the visible Project tabs through that binding is **NOT RUN**: the local browser was signed out. The parent owns DEV publication and browser login/sanity. The two untracked screenshots in this worktree are from an earlier UI checkpoint and use older Save copy; they are not current-source evidence and are not included in this report.
- Source-level DEV preflight confirms the deployment path is `bff`, `worker` (the `olw-pipeline-dev` job), and `frontend`; the `pipeline_config` helper generates BFF runtime config, and no separate Pipeline deploy component exists. The live BFF URL/service account and worker job service account match the configured identities. However, read-only Secret Manager metadata lookup returned `NOT_FOUND` for `byok-credential-encryption-key-dev` version `1`, which CaC pins and BFF config generation must resolve before BYOK works. No payload was read, and no secret, IAM, provider, or deployment mutation was performed; provisioning authority is still required.
- Profile credentials and bindings are additive Firestore data in the new LLM-profile collections; this change includes no data migration or deletion step. A code/config rollback does not erase those documents, and the pinned BYOK encryption key must be retained to decrypt them when the feature is restored.
- PR #167 remains open and non-draft. The integrated branch awaits same-SHA review/CI; no PR merge or DEV deployment has occurred.

The feature-branch integration is authorized and included in the candidate; no PR merge, DEV deployment, Production change, live provider call, or real credential provisioning has occurred.
