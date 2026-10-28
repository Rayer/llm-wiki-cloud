# GAT-4 CLI local qualification

This harness builds the checked-out GateAuth, BFF, and `lwc-sync` sources, plus
the actual CLI from baseline `4d290cb7d2f5d9d8be9523f0ddc02b8ce1b621c9`. It
materializes local Auth and BFF config through the repository CaC producer,
starts owned loopback Firestore and GCS emulators, and runs the
clients as separate CLI processes against the real HTTP services.

Run from the repository root with Go, Pkl, Python 3, gcloud, and Java 21
installed:

```sh
python3 scripts/gat4-local/e2e/cli_sync/run.py
```

The runner does not read `.env` or existing local credentials. It sets
`GOOGLE_APPLICATION_CREDENTIALS=/dev/null`, disables ambient metadata lookup,
uses a private `CLOUDSDK_CONFIG`, and keeps configs, credentials, service logs,
and audit data under `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/`.
The only checked-in output it writes is this directory's nonsecret
`report.json`; private terminal and service logs remain mode `0600` in scratch.

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

The report records observed reauthorization IDs. The explicit reauthorization
flow predates this extraction and has a replacement-generation behavior in the
baseline; compare that lifecycle result with the frozen acceptance wording.
