# LWC-358 Production Auth empty demo-user environment compatibility

## Observed deployment result

The supplied Production run `37244181796` reached ready, then returned `provider-response-unrepresentable` at checkpoint sequence 7 after creating Auth candidate revision `00009-rzw`; traffic remained on prior revision `00008-9jn` at 100%, and Frontend remained unstarted. The retained result is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/prod-37244181796-result/result.json`. This worker made no live call and performed no provider mutation.

The Production config source sets `auth.demo_user_id` to the empty string. The actual Auth readback returned an env entry with `name=AUTH_DEMO_USER_ID` and no `value`. `Providers.service_matches` calls `auth_config.effective`, which required every managed env entry to contain both `name` and a string `value`; that raised the reported config mismatch before candidate verification completed.

## Local repair and evidence

`deploy/components/auth_config.py` now treats exactly a name-only `AUTH_DEMO_USER_ID` entry as the empty string. Explicit values still require strings; all other managed env entries still require both fields. Expected Production config remains empty. No Demo UID, password, secret, Provider config, or IAM value was added or changed.

The regression exercises the actual `Providers.service_matches` → `auth_config.effective` path with normalized Production config from `deploy_config`. The pre-fix output is `prod-empty-auth-demo-env-causal-red.txt` (1 error at the actual adapter call); the post-fix output is `prod-empty-auth-demo-env-causal-green.txt` (1 passed). A control case removes the value from `GCP_PROJECT` and still requires rejection.

Commands and final results:

- `PYTHONDONTWRITEBYTECODE=1 TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 scripts/test_production_auth_config_contract.py ProductionConfigContractTests.test_empty_demo_user_id_accepts_cloud_run_omitted_value_only -v` — **1 passed** after the fix.
- `PYTHONDONTWRITEBYTECODE=1 TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 scripts/test_auth_config_contract.py` — **9 passed**.
- `PYTHONDONTWRITEBYTECODE=1 TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 scripts/test_production_auth_config_contract.py` — **10 passed**.

## Existing Production continuation entry

`.github/workflows/promote-production.yml` is release-only: it always calls `cd.yml` with operation `release`, so using it again would start the prepare path. The already registered `.github/workflows/recover-deployment.yml` accepts Production `readback` and recovery operations with an explicit retained artifact ID, source SHA, tag, and components; its executor remains the current reviewed `github.sha`. The bounded continuation is to have Parent select the existing retained artifact for checkpoint sequence 7 and use that registered recovery entry to read back the already-created candidate before choosing any further action. This proposal does not dispatch Actions, create a new attempt, or change workflow authority.

Canonical `implementation-content.json` records 63 exact path/hash/mode rows; `content_sha256=15b86374aebad0cb807b093c017ac1a7034f2271a7033d2718020240fa7162b4`; manifest-file SHA-256 is `54bb3f180abd8cf00928f4658a8ae67fdd41f9a60ae0dfd263e25c19aa97bf9e`. Frozen r2 remains byte-identical at SHA-256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.
