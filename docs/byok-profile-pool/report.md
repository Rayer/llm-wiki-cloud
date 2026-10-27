# BYOK profile pool delivery status

**Status: IN PROGRESS / HOLD.** Scope is LWC-434, LWC-435, LWC-436, and LWC-437, frozen by `byok-pool-r1` after independent PASS request `dc629629399e7aea30630602`.

Implementation source commit: `3739b16d1e8f635525d0db7c10e546d94974c2db`, on the existing open, non-draft [PR #167](https://github.com/Rayer/llm-wiki-cloud/pull/167) targeting `develop`. The current code passes the gates below; same-SHA rereview is still pending. No merge or deployment is authorized.

## Implementation

- Owner-scoped BYOK credentials are encrypted and associated with owner, environment, profile, and credential revision. Profile metadata remains write-only; Project binding changes require explicit Save. The shared pool remains managed after its last profile is removed.
- Query and MCP URL intake use the selected Project source. Query records successful call identities by purpose and preserves Expansion and Synthesis identities separately. Producer transport and consumer aggregation have separate regression coverage.
- Jev Project evaluation consumes the resolved Project chat client. The new tagged fixture creates and binds an encrypted profile through BFF APIs, reloads it through `projectLLMClient`, and proves `JevEvaluator` uses that profile without its static TypeSafe fallback.
- Local/cloud suggested-query generation resolves the selected Project source, preserves never-managed bootstrap, fails closed for unavailable managed sources, and publishes actual model/profile provenance.
- Shared Pipeline retry requires explicit provider quota and fresh candidate resolution. A new success marker stops whole-job Synto replay after any successful selected-profile response; absent, unreadable, or existing marker state fails closed. A retry starts with a fresh workspace only when no selected-profile response succeeded.
- Pinned Synto 0.7.0 retains native OpenAI Chat Completions and Anthropic Messages protocols. Project settings use the `知識庫設定` and `AI 服務` subtabs, retain both drafts across switching, return newly created profiles to the unsaved AI service draft, and bind only after explicit Save.

## Verification on implementation source commit

- `GOPROXY=off GOSUMDB=off go test ./... -count=1 -race` passed across all BFF packages.
- `GOPROXY=off GOSUMDB=off go vet ./...` and `GOPROXY=off GOSUMDB=off go build ./...` passed.
- `SYNTO_TEST_PYTHON=/Users/rayer/Develop/llm-wiki-cloud/.git/worktrees/LWC-BYOK-profile-pool/lwc361-local-cloud/python/bin/python3 TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch sh scripts/test_flash_execution.sh` passed: OpenAI and Anthropic wire protocols, private/loopback/metadata DNS rejection, and `insufficient_quota` versus `rate_limit_exceeded` marker behavior. Requests used mock transports.
- `FIRESTORE_EMULATOR_HOST=127.0.0.1:58773 SYNTO_TEST_PYTHON=/Users/rayer/Develop/llm-wiki-cloud/.git/worktrees/LWC-BYOK-profile-pool/lwc361-local-cloud/python/bin/python3 TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch GOPROXY=off GOSUMDB=off go test -tags byokprofilefixture ./cmd/olw_worker ./internal/handler/v1 -run 'TestPersistedProjectProfileConnectsQueryAndPipelinePublication|TestJevEvaluatorUsesPersistedProjectProfile' -count=1 -v` passed against a task-owned disposable Firestore emulator. The first fixture creates a profile and Project binding through actual BFF API handlers, then exercises Query, the Pipeline profile resolver, the worker, pinned Synto, postprocess, and publication with synthetic transport receipts. It verifies the selected model/protocol and artifact provenance without returning the test credential. The second fixture exercises Jev through a saved Project profile and confirms the static TypeSafe path is not called.
- The no-replay regression is included in the full race suite. Secure credential storage and profile revision tests are also included there.
- The coordinator independently reports 559 frontend Node tests, 572 component tests, ESLint, typecheck, and build passing on the UI source candidate; this implementation commit changes no frontend source. The coordinator also ran the last-shared-profile Firestore test against a disposable emulator and reports PASS.

## Connected evidence limits and remaining work

- The connected backend fixture proves the persisted binding → Query → Pipeline/Synto path with disposable Firestore and mock provider transports. Current authenticated browser interaction from the visible Project tabs through that binding is **NOT RUN**: the local browser was signed out. The two untracked screenshots in this worktree are from an earlier UI checkpoint and use older Save copy; they are not current-source evidence and are not included in this report.
- The current UI tab, copy, draft-preservation, and create-return behavior is covered by the coordinator-run frontend component suites. Same-SHA rereview remains pending; PR #167 stays IN PROGRESS / HOLD until review and the browser evidence limit are resolved.

No merge, deployment, production/provider call, or real credential provisioning has occurred.
