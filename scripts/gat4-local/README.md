# GateAuth local artifact helper

build-auth.sh stages only gateauth/ in a temporary directory, runs its Go tests, builds a native binary and a static Linux amd64 binary, then writes a standards-conformant OCI image layout archive. The schema.json file is the sole authority for the local artifact JSON shape; artifact.py checks it and verifies source, config, binary, OCI manifest, layer and archive digests.

Outputs go to $TMPDIR/gat4-local/out/: auth, auth-linux-amd64, go-test.txt, gateauth-image.oci.tar, and gateauth-artifact-v1.json. The OCI archive contains the static binary and the host CA bundle; it expects a runtime config to be mounted at /config/auth.json. The config digest records the exact bytes of the checked-in synthetic local fixture and contains no live credential.

Run make -f scripts/gat4-local/Makefile help, build, validate, test, or clean-checkout from the repository root. The clean-checkout harness copies only gateauth/ and scripts/gat4-local/ into a temporary Git repository, proving that the build does not need the LWC server tree.

The archive is locally packaged and validated; no container runtime executes it. No registry, canonical build receipt, remote repository identity, signature, or publication is created.
