# DEV 37111842689: executable probe status and validation proposal

## Probe result

The source trace in [the focused output diagnosis](dev-37111842689-prerender-output-diagnosis.md) identifies the candidate chain, but I could not execute the full Next → adapter → CLI serialization chain from this machine’s cached dependencies. The exact offline preparation attempt was:

```sh
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-probe-20261003-01/tmp \
  npm install --offline --ignore-scripts --no-audit --no-fund --loglevel=error \
  --prefix /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-probe-20261003-01
```

It stopped with `ENOTCACHED`: `https://registry.npmjs.org/undici/-/undici-7.30.0.tgz` is absent from npm’s cache. The command was offline; no install, adapter call, or CLI writer call completed. I did not substitute a handwritten writer or claim that the cached-source path formula proves the failed runner’s output. The existing Frontend integration test exercises real Next output, but it does not exercise the adapter and CLI writer together, and was not rerun in this bounded attempt.

The pinned-source candidate remains: BuildOutput key `build-config.json` with a `Prerender` value and `.next/server/app/build-config.json.body` fallback; CLI 59.11.7 serializes that value to `functions/build-config.json.func`, `functions/build-config.json.prerender-fallback.body`, and `functions/build-config.json.prerender-config.json`. A plain `File` value instead writes `static/build-config.json`. Package tarball hashes and relevant source lines are recorded in the linked diagnosis. The actual `37111842689` BuildOutput type and runner directory remain unknown.

## Validation contract proposal — pending Owner disposition

Keep the existing exact-target JSON comparison and accept only two explicitly recognized layouts beneath the configured Vercel output root:

1. **Static file:** `static/build-config.json` is a regular file containing the exact supported schema and requested target values.
2. **Official prerender:** the route-keyed function directory, CLI prerender descriptor, and the descriptor-bound fallback body are all present at the pinned writer’s exact paths. Parse the fallback body with the same strict schema and compare its target values exactly. Verify that the descriptor binds the expected route basename to that fallback basename and function; do not find a substitute by scanning.

Malformed descriptors or JSON, missing members, mismatched route/fallback binding, wrong target values, ambiguous conflicting layouts, and unsupported serialization versions remain fail-closed. No arbitrary `.body` acceptance, alternate-path search, target coercion, archive change, or runtime change is proposed. The descriptor’s precise accepted schema and binding fields should be finalized from an executable run of the pinned package chain before implementation; I am not asserting those fields from an unexecuted source-only inference.

This proposal changes the accepted validation contract and therefore needs Owner disposition before any engine/source edit. Current worktree source remains unchanged at HEAD `e8989d4abf7072cfc3945812904219e2175c3551` (tree `087fca54193f6556910c4bb920e3a01d46adbd64`). The historical run’s actual output layout and cause remain unknown.
