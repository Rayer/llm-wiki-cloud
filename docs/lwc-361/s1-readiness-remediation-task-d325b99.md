# PR98 S1 occupied-port readiness repair

## Scope and identity

- Dispatch: `task_d325b99cb151` / `ctx_a5d075e35caa`.
- Repair baseline: PR98 branch `Rayer/LWC-361-local-cloud-discussion`, published HEAD `10907cfa87c1778fd6b81ecba4580f245ae904d8`, tree `67b3960d3f7518a84a8ea72dd34dfabec51fc38c`.
- The recovered same-head Supervisor HOLD and TPM intake identify one blocking source issue: `scripts/local-services.py` treated any loopback listener as readiness while the newly launched Go/npm child was still starting. The independent reproduction and original red output remain unchanged at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-readiness-parent-reproduction-fixed.{log,json}`; it recorded `start_return=0`, `startup_state=ready`, `child_bind_failed=true`, and `other_listener_intact=true`.
- This repair is limited to occupied-port readiness. It does not include the nonblocking stderr/UNKNOWN UI observations, provider preflight, port killing, new platform support, or changes to running local services.

## Repair

`scripts/local-services.py` now binds temporary loopback probe sockets for the selected service readiness addresses before launching any child. An occupied port produces a named startup failure with service, port, and address; the probe closes its own socket and does not signal or terminate the existing listener. The probe sets `SO_REUSEADDR` so a recently closed service port in TIME_WAIT remains restartable. IPv6 address-family/loopback-unavailable errors are skipped as unsupported, while the existing IPv6 connection readiness check remains unchanged.

The causal CLI regression starts a real loopback listener and configures a delayed fake `go` launcher whose later bind would fail. It asserts `start auth` exits 1 before that launcher runs, the startup record says failed for the occupied port, no supervisor/child ownership records remain, and the unrelated listener still accepts connections. A focused regression also verifies a recently closed port can be reused. Existing tests for IPv6 readiness, partial-start cleanup and worktree-scoped stop remain in the full suite.

Source file SHA-256 after the repair:

- `scripts/local-services.py`: `589016b9fa44fa2a0c900d96c40a17a7373fbbf33eceee2e2bee25b742a291b5`
- `apps/bff/scripts/test_local_dev_makefile.py`: `f235ec83b9ea789c7520908465dc639cd25dfd8e6567c818da9f1862001877dd`

The probe is a point-in-time rejection of ports already occupied when startup begins; it does not hold a reservation between the check and the service bind, so a separate process racing to claim a port after the probe is outside this bounded fix.

## Verification

Both test commands below explicitly unset provider API keys, emulator endpoints, and the Vercel token. Their listeners and fake launcher use loopback and temporary files under the TPM scratch `TMPDIR`; syntax/whitespace checks do not invoke services. No provider, cloud, credential, quota, or running main-service operation occurred.

| Check | Exact command | Exit / evidence |
|---|---|---|
| Causal occupied-port and recent-close regressions (`apps/bff/scripts`) | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST -u VERCEL_TOKEN TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3 -m unittest test_local_dev_makefile.LocalDevMakefileTests.test_start_rejects_existing_listener_before_delayed_child_can_fake_readiness test_local_dev_makefile.LocalDevMakefileTests.test_startup_port_check_allows_recently_closed_listener_port -v` | **0**; 2 tests PASS. Raw: `evidence/s1-occupied-port-focused-task-d325b99.log`; same run in scratch: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-s1-d325b99-focused-r3.log`. |
| Complete existing Make/supervisor contract suite (`apps/bff/scripts`) | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST -u VERCEL_TOKEN TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3 -m unittest test_local_dev_makefile.py -v` | **0**; 14 tests PASS, including IPv6 readiness, partial-start cleanup and stop isolation. Raw: `evidence/s1-make-contract-suite-task-d325b99.log`; scratch copy: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-s1-d325b99-make-suite-r2.log`. |
| Python syntax | `env PYTHONPYCACHEPREFIX=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-s1-d325b99-pycache-r2 python3 -m py_compile scripts/local-services.py apps/bff/scripts/test_local_dev_makefile.py` | **0**. |
| Whitespace | `git diff --check` | **0**. |

## Publication and remaining review

This source delta passed the named offline regressions; no full-root suite was rerun. The preceding `10907cfa…` canonical CI success remains evidence only for that old head. After this repair is committed and normally pushed to PR98, record the new full commit/tree identity, remote head convergence, mergeability, and the fresh canonical CI run in the task handoff; the new head requires new same-head Supervisor/TPM reviews. Parent retains PR merge, DEV Actions, and all live/browser/article acceptance responsibilities.
