.PHONY: help build validate test clean-checkout

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

help:
	@printf '%s\n' \
	  'build          Run clean GateAuth Go tests/build and package the OCI archive' \
	  'validate       Validate the current local manifest, binaries, source, and OCI archive' \
	  'test           Run local artifact and receipt contract tests (requires build)' \
	  'clean-checkout Copy only gateauth/ and this helper into an isolated Git checkout and rebuild'

build:
	@test -n "$$TMPDIR" || { echo 'set TMPDIR to local scratch'; exit 2; }
	"$(ROOT)/scripts/gat4-local/build-auth.sh"

validate:
	@test -n "$$TMPDIR" || { echo 'set TMPDIR to local scratch'; exit 2; }
	python3 -B "$(ROOT)/scripts/gat4-local/artifact.py" verify \
	  --manifest "$$TMPDIR/gat4-local/out/gateauth-artifact-v1.json" \
	  --artifacts-dir "$$TMPDIR/gat4-local/out" \
	  --source-root "$(ROOT)/gateauth" \
	  --config-input "$(ROOT)/gateauth/contracts/gateauth-artifact/v1/local-auth-config.test.json"

test:
	@test -n "$$TMPDIR" || { echo 'set TMPDIR to local scratch'; exit 2; }
	TMPDIR="$$TMPDIR" python3 -B -m unittest discover \
	  -s "$(ROOT)/scripts/gat4-local/tests" -p 'test_*.py'

clean-checkout:
	@test -n "$$TMPDIR" || { echo 'set TMPDIR to local scratch'; exit 2; }
	"$(ROOT)/scripts/gat4-local/test-clean-checkout.sh"
