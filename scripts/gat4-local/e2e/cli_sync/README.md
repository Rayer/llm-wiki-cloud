# GAT-4 CLI local qualification

This harness builds the checked-out GateAuth, BFF, and `lwc-sync` sources, plus
the actual CLI from baseline `4d290cb7d2f5d9d8be9523f0ddc02b8ce1b621c9`. It
materializes local Auth and BFF config through the repository CaC producer,
starts owned loopback Firestore and GCS emulators, and runs the clients as
separate CLI processes against the real HTTP services.

Run from the repository root with Go, Pkl, Python 3, gcloud, and Java 21
installed. Set `TMPDIR` to an existing writable local scratch directory first;
the runner fails if it is unset or unavailable and does not fall back to the
system temporary directory. For example:

```sh
mkdir -p "$HOME/.cache/gat4-cli-sync-scratch"
chmod 700 "$HOME/.cache/gat4-cli-sync-scratch"
TMPDIR="$HOME/.cache/gat4-cli-sync-scratch" python3 scripts/gat4-local/e2e/cli_sync/run.py
```

The runner does not read `.env` or existing local credentials. It sets
`GOOGLE_APPLICATION_CREDENTIALS=/dev/null`, disables ambient metadata lookup,
uses a private `CLOUDSDK_CONFIG`, and creates a mode `0700` run directory under
the caller supplied `TMPDIR`. Configs, credentials, service logs, and audit
data stay in that run directory; private logs and audit fixtures are mode
`0600`. The prior `report.json` is retained as the original 51bfc0e run; this
runner writes the new nonsecret `report-reconciliation.json`.

Firestore registration policy and Project ACL fixtures are seeded directly
into this run's emulator; Auth creates the synthetic account through its real
registration API. Pairing approval, session creation and refresh, Project
listing and binding operations, raw sync uploads/downloads, reauthorization,
and revoke are performed through the real product binaries. The GCS emulator
uses its private in-memory backend because its filesystem backend does not
support the generation pinned reads used by raw sync. The storage service stays
running across fresh CLI processes, and its XML public host is pinned to the
numeric loopback listener so the Storage SDK's generation pinned XML reads
reach the emulator. The clients upload and read back the exact synthetic raw
bytes through the BFF's real Storage SDK operations.

The report records exact before and after reauthorization identities: the
established flow rotates the binding generation while preserving the issuer,
WikiID, and ProjectID, updates local metadata to the new binding ID, and rejects
the stale pre-reauthorization ID. It also records blank and header-only Auth
audit logs rejected by the same evidence guard before crediting the real Auth
audit's positive RPC evidence and zero forbidden collection access. The
historical `report.json` remains unchanged.
