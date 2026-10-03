# Runtime cause passthrough

## Change

This local candidate adds an optional `causes` array to the existing formal result when a runtime deploy fails and its reconciliation also fails. The deploy and reconcile failures remain separate phase entries. Frontend deploy subprocess failures now use the existing bounded cause handling under `frontend-vercel-deploy`; the frontend v6 deployment read uses `frontend-deployment-reconcile`. The engine keeps the outer `provider-result-unreadable`, `unknown`, mutation flag, `reconcile-before-replay` action, checkpoint, and no-replay behavior unchanged. Successful reconciliation continues into the existing poll path.

The change follows the existing code path at `deploy/engine/engine.py` (`deploy`, `reconcile`, `result`), `deploy/engine/providers.py` (`deploy`, `reconcile_candidate`), and `deploy/engine/support.py` (`Breakpoint`, `run`). JSON parse messages retain parser location without copying the response document; child errors use the existing bounded/redacted message handling. No parser behavior or runtime gate changed.

## Causal evidence

Before the implementation, `runtime-cause-integration-red.txt` exercised `engine.main` through the production provider methods and result writer. It failed at the assertion for `result['causes']` with `KeyError`, demonstrating that the formal result discarded both phases. The corrected integration test passes and checks the deploy exit code and bounded cause, the reconcile `JSONDecodeError` and stage, the unchanged outer result fields, a single deploy and reconcile attempt, and absence of the synthetic credential from result/stdout.

An earlier fixture iteration is preserved in `runtime-cause-red.txt` and `runtime-cause-red-detail.txt`; it failed during test setup before the intended deploy/reconcile boundary and is not counted as causal evidence.

## Verification

Commands ran in this isolated worktree with `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`:

- `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_engine.py' -k test_frontend_runtime_deploy_and_reconcile_causes_reach_formal_result -v` — **1 test passed**; output: `runtime-cause-integration-green.txt`.
- `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` — **90 tests passed**; output: `runtime-cause-engine-green.txt`.

The pre-fix causal failure is `runtime-cause-integration-red.txt`. The separate fixture-setup outputs above remain historical failures and were not overwritten.

## Live incident boundary

The historic DEV runtime failure remains **unknown**. Its result did not contain the underlying deploy/reconcile causes, and this offline change cannot recover them. Parent's read-only local CLI GET evidence separately found zero matching records in the v6 first-page query and zero records in the v7 since/until query for the fixed project/team and time window; the alias readback identified an older READY deployment with production target. Those results do not establish this attempt's cause or acceptance. This task made no live API, provider, Actions, credential, or deployment calls.

This is an unpublished local candidate based on `b978fe10fb532e87b5afe5dee7e65712622857dd`. Canonical manifest: `docs/lwc-358/implementation-content.json`, 54 file rows, aggregate `7fbf5e07c9bb8745fc10386b64e46811d4bffe8e9c89b9f989b4034b1ce39d6b`. PR86 diagnostic-worktree changes and its test repair are excluded.
