# LWC-420 r2 implementation report

## Scope

This report tracks the isolated LWC-420 r2 work in the Project context worktree. The worker adds a bounded local publication descriptor inside the existing local publish journal; it does not introduce a cloud generation or archive. Local Query identity remains explicitly legacy, while the Project context GET and authenticated MCP metadata use an optional publication reader that validates the descriptor against the committed concepts bytes.

The descriptor captures the bounded selection-v1 summary input in the same journal transaction as `cache/concepts.jsonl`. A same-digest publication advances the current local transaction identity while preserving the original summary source identity, input, and metadata. The local worker uses the existing vault lease for reconciliation, claim, input validation, and finalization; model work runs after the publication lease is released. Summary failures remain outside the knowledge publication result.

## Local checks completed

- `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./cmd/olw_worker ./internal/localfs ./internal/contentcontext ./internal/cache -count=1` passed.
- Focused local publication, Firestore carry-forward/fencing, local GET/MCP readback, and precommit no-inference tests passed.
- Frontend `npm test` reported 568/568 Node tests and 604/604 component tests passed; `npm run typecheck` passed.
- After integrating source-stable LWC-443 commit `446b613a91faf39255408404bc980dc88d1b038a`, `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./cmd/olw_worker ./internal/localfs ./internal/contentcontext ./internal/cache ./internal/llm ./internal/queryruntime -count=1` passed; a rerun of `./cmd/olw_worker ./internal/contentcontext` also passed after adding the digest-work identity assertion.
- `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -count=1` still has the known held LWC-443 failure: `TestProfileRuntimeConnectedManualThenCompileTagging/retained-candidate-exhaustion-retries-through-activation` reports `calls=3` at `profile_runtime_e2e_test.go:385`.
- Final LWC-445 head `c66e6f863cee77c4d2aedce25b6747a4abf8b4c5` is integrated; `npm exec vitest run tests/project-description-editor.test.tsx` passed (6 tests).

## Pending immutable dependency

The final LWC-445 description head `c66e6f863cee77c4d2aedce25b6747a4abf8b4c5` is integrated. Its editor change preserves edits made while a latest-revision request is in flight; `npm exec vitest run tests/project-description-editor.test.tsx` passed (6 tests).

The source-stable LWC-443 commit `446b613a91faf39255408404bc980dc88d1b038a` is integrated as an exact commit. It supplies `revalidatePipelineProfileCandidates(ctx, cfg, pinned)`, which checks current shared-pool eligibility and returns original pinned candidates. The outer worker remains the sole owner of credential cleanup so Fast credentials remain available through local/cloud summary finalization. This checkpoint is unreviewed and does not yet supply the response-preserving resolver wrapper proving actual source plus platform Fast identity needed by 420; the current 420 summary selector is not treated as final. Do not add a copied 443 wrapper, invent a profile selector, or enable a bootstrap adapter without that proof. The next discriminating check is the exact parent-supplied 443 wrapper/final head; bind its proven shared, personal, or bootstrap result to the claimed SummaryWork, add 420-owned eligibility/identity tests, and rerun worker, handler, and consumer checks.
