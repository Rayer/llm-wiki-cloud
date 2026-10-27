# BYOK profile pool delivery evidence

Scope: LWC-434, LWC-435, LWC-436, and LWC-437, frozen by `byok-pool-r1` after review request `dc629629399e7aea30630602`.

## Implementation

- Account-owned profiles use encrypted credential storage and reference-only provider secrets in the supported backend; project binding and admin-managed shared profiles resolve through the same server-side profile pool.
- Query, Project URL intake, Jev tagging/evaluation, and Pipeline runtime now use the selected Project profile at real call sites. Synto's pinned adapters cover OpenAI Chat Completions and Anthropic Messages; hostname resolution rejects loopback, private, and metadata targets.
- Shared-profile fallback happens only on an explicit quota marker and restarts from an immutable seed. A connected cloud-worker route had bypassed the profile resolver; it now runs through the profile-pool runner. Fixture `/v1/models` health checks are synthetic too, so the connected proof makes no provider network call.
- Provider secrets for deployed environments remain Secret Manager references; no provider or credential provisioning was performed.

## Connected disposable run

- Project `BYOK disposable fixture` (`478a24f973f7`) is bound to personal custom/OpenAI Chat Completions profile `d4u034ek2plenct1qkog`, revision 2, model `fixture-no-network-model`.
- Pipeline completed from the local UI. UI readback showed one source, one concept, and one raw item; the published concept is `BYOK Profile Routing`.
- Query `BYOK profile routing` returned the cited answer from that concept. The successful `query.runtime.execute` trace confirms personal profile revision 2, `custom`, `openai_chat_completions`, and the fixture model; the request span records generation `g_f7c5260c131542f3737298e68ae61524`.
- Worker receipts show the `/v1/models` health check and two `/v1/chat/completions` calls through the fixture. Query receipts are retained separately. These fixtures reject network access and do not contain a provider key.

Evidence files in `evidence/`:

- `query-trace.json` — sanitized Query trace metadata only.
- `pipeline-fixture-receipts.tsv` and `query-fixture-receipts.tsv` — sanitized no-network transport receipts.
- `ui-desktop.jpg` and `ui-mobile.jpg` — actual browser captures of the Query result and citation.

## Verification status at candidate commit

- Frontend tests: 560 passed.
- Frontend lint and typecheck: passed.
- Focused worker profile-routing, fresh-seed quota fallback, and fixture health-check tests: passed.
- BFF `make test` with the pinned Python 3.13 environment: running; final status will be recorded after completion.
- The full repository gates have not yet completed.

No PR, merge, deployment, provider call, or credential provisioning has occurred. The task dispatch prohibits posting to GitHub; this is a local candidate for independent review.
