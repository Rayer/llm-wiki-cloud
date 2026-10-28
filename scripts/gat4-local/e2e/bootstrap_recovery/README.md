# GAT-4 bootstrap/recovery local qualification

This standalone harness builds the checked-out GateAuth and BFF binaries, starts
its own loopback Firestore emulator, in-memory GCS emulator, Auth HTTP process,
and BFF HTTP process, then exercises password, Demo, and Google identity
bootstrap paths. It does not invoke or modify the accepted `cli_sync` harness.

Run from the repository root with Python 3, Go, Pkl, gcloud's local Firestore
emulator, and Java 21 installed. Set `TMPDIR` to an existing private writable
directory first; the runner fails clearly if it is absent and has no system
temporary-directory fallback.

```sh
mkdir -p "$HOME/.cache/gat4-bootstrap-recovery"
chmod 700 "$HOME/.cache/gat4-bootstrap-recovery"
TMPDIR="$HOME/.cache/gat4-bootstrap-recovery" python3 scripts/gat4-local/e2e/bootstrap_recovery/run.py
```

The runner isolates `CLOUDSDK_CONFIG`, sets
`GOOGLE_APPLICATION_CREDENTIALS=/dev/null`, denies metadata lookup, chooses new
numeric-loopback ports and a fresh Firestore scope, and uses only synthetic
credentials and configuration. Configs, signing keys, terminal/service logs,
the emulator evidence, and the Go test's intermediate result stay in the
mode-0700 run directory under the caller's `TMPDIR`; private files are mode
0600. The only durable output is this directory's nonsecret `report.json`.

The password case registers through Auth, verifies the returned `default`
pointer and an empty BFF project list, stops only this run's Auth process, and
observes the real BFF request fail while Auth is unavailable. It restarts Auth
with the same config and emulator, logs in as the same user, retries the same
BFF endpoint, and reads the resulting Project through `/api/v1/projects` and
both Firestore document shapes. Separate cases issue six concurrent first
bootstrap requests, repeat an existing bootstrap, reject a seeded metadata
identity conflict with HTTP 409 and zero registry write, and verify that
name, `created_at`, status, and post-bootstrap binding/profile sentinels remain
unchanged. The conflict record and registration policy are explicitly seeded
synthetic fixtures; no completed Project is seeded to make the first
bootstrap succeed.

Auth starts with its configured fixed non-admin Demo identity and the real
`/api/v1/auth/demo` route is exercised before the same BFF bootstrap endpoint.
For Google, an overlay-only Go test reuses the repository's existing fake
provider and production callback/identity repository code against the same
emulator namespace. It verifies a new JIT identity and a separately prepared
existing Google identity, then uses each resulting access token against the
actual running BFF. This is local synthetic-provider business-flow coverage,
not external Google OIDC or browser verification.

Three Auth Firestore audit logs must contain positive identity RPC evidence,
full forbidden-field coverage, and no Project/binding collection accesses.
The same guard rejects actual blank and header-only logs. The report records
the exact tested component tree hashes and compares the GateAuth/BFF Go source
trees and module manifests with Main reference `701b125`.

The frontend login, Demo, Google completion, and session hydration callers and
their focused unit tests are source-inspected and hashed, not run in a browser.
The current frontend snapshot persists Auth state and calls `/api/v1/projects`
on workspace re-entry but has no caller for `/api/v1/onboarding/default-project`
or a product setup pending/retry state; the report therefore marks frontend
re-entry acceptance incomplete even when local Auth/BFF endpoint checks pass.

The report explicitly excludes external Google credentials/provider/browser
verification, frontend tests/browser automation, cloud or live accounts, IAM,
provider/LLM calls, deployment, publication, production readback, and all
GAT-4 acceptance outside the identity/bootstrap slice of acceptance 1 and
acceptance 5.
