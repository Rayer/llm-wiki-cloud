# BYOK profile pool delivery evidence

Scope: LWC-434, LWC-435, LWC-436, and LWC-437, frozen by `byok-pool-r1` after independent PASS request `dc629629399e7aea30630602`.

Final source commit: `b5babaf1f7cf3acbd926fe530e601a0cf5be897c`.

## Implementation

- Added owner-only personal profile CRUD and explicit Project binding. Profile saves remain separate from selection; returning from profile creation now selects the new profile in the unsaved Project draft. Demo cannot mutate personal profiles or bindings.
- Personal API keys are encrypted at rest and bound to environment and credential revision. Metadata edits preserve the stored credential and revision; replacement advances the revision. Admin-managed profiles retain deployment-owned Secret Manager references.
- Routed Query, URL intake, Jev tagging/evaluation, and Pipeline runtime through the selected Project profile at their real call sites. Synto's pinned runtime supports OpenAI Chat Completions and Anthropic Messages; outbound endpoint validation rejects loopback, private, and metadata targets.
- Shared-profile quota fallback is limited to an explicit quota signal, resolves fresh candidates, restarts from the immutable seed, and does not replay partial output. An intentionally empty managed pool fails closed without falling back to personal spend.
- Added the shared-service notice and Account Settings navigation with the accepted Traditional Chinese copy and dismissal behavior. Shared profiles use the same managed pool in Demo without creating a separate budget.

## Connected disposable run

- In the connected local UI, Project `BYOK disposable fixture` (`478a24f973f7`) is saved to personal profile `No network test` (`d4u034ek2plenct1qkog`), custom/OpenAI Chat Completions, revision 2, model `fixture-no-network-model`.
- The UI displayed the shared-service notice with the requested copy, and its CTA opened Account Settings focused on My BYOK. The final return-flow fix was reproduced through the local UI; the new profile was selected in the unsaved Project draft and required an explicit Save before binding changed.
- The sanitized no-network Query trace `910e9cb47de06ac3d9003167aacb5cde` records successful generation `g_f7c5260c131542f3737298e68ae61524` with the personal profile identity, revision, protocol, and model. Pipeline receipts show one fixture `/v1/models` health check and two fixture `/v1/chat/completions` calls; Query receipts are retained separately. The fixture rejects network access and includes no provider key.
- Evidence files: `evidence/query-trace.json`, `evidence/query-fixture-receipts.tsv`, `evidence/pipeline-fixture-receipts.tsv`, `evidence/ui-desktop.jpg`, and `evidence/ui-mobile.jpg`.
- The Query/Pipeline receipts were captured against the already-running local stack, which was not restarted for the final source commit. They demonstrate the connected flow on the disposable fixture; current source behavior is separately covered by the gates below.
- UI QA created a synthetic `BYOK return fixture` profile in the disposable local account. It was never selected for Query/Pipeline requests and the Project binding is restored to the original profile. No real provider credential was entered or provisioned. The browser automation timed out while handling the profile-delete confirmation, so the unused synthetic local profile remains for cleanup.

## Verification

- Frontend `npm test`: passed, 559 Node tests and 567 component tests.
- Frontend `npm run lint && npm run typecheck`: passed.
- Frontend `npm run build`: passed.
- Focused Go profile, credential revision, Demo, truncation, identity receipt, and shared-pool tests: passed.
- `apps/bff` `make test` with the pinned Python 3.13 environment: passed, including the full Go race suite and Python suites.
- `apps/bff` `make vet`, `make build`, and `make local-synto-runtime-test`: passed. The runtime test uses pinned Synto 0.7.0 / Python 3.13.16 and makes no provider call.
- Firestore state persistence test was skipped because no local Firestore emulator is available (`FIRESTORE_EMULATOR_HOST` unset and port 8085 closed).

No merge, deployment, production/provider call, or real credential provisioning was performed.
