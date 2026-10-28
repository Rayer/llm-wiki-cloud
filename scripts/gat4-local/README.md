# GateAuth local artifact helper

build-auth.sh stages only gateauth/ in a temporary directory, runs its Go tests, builds a native binary and a static Linux amd64 binary, then writes a standards-conformant OCI image layout archive. GateAuth keeps the versioned artifact schema under its contract tree; the LWC consumer mirrors that protocol at apps/bff/deploy/contracts/gateauth-artifact-v1.schema.json. artifact.py validates against the LWC protocol copy and the clean-checkout test packages only that schema alongside GateAuth source, never the LWC server tree.

Outputs go to $TMPDIR/gat4-local/out/: auth, auth-linux-amd64, go-test.txt, gateauth-image.oci.tar, and gateauth-artifact-v1.json. The OCI archive contains the static binary and the host CA bundle; it expects a runtime config to be mounted at /config/auth.json. The config digest records the exact bytes of the checked-in synthetic local fixture and contains no live credential.

Run make -f scripts/gat4-local/Makefile help, build, validate, test, or clean-checkout from the repository root. The clean-checkout harness copies only gateauth/ and scripts/gat4-local/ into a temporary Git repository, proving that the build does not need the LWC server tree.

The archive is locally packaged and validated; no container runtime executes it. No registry, canonical build receipt, remote repository identity, signature, or publication is created.
