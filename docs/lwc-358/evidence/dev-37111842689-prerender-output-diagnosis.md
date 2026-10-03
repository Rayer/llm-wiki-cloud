# DEV 37111842689: `/build-config.json` output trace

This is a bounded, offline source diagnosis of the Frontend prepare failure. It is not a confirmed explanation of the runner’s final directory layout or the incident’s root cause. No implementation files changed and no tests were run for this report.

## Observed run

The recorded result at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37111842689-result/result.json` says the four Frontend prepare commands returned exit 0 without timeout, but strict validation did not find a valid, target-matching `static/build-config.json` under the configured Vercel output directory. The retained build log reports Next 16.2.7 static generation for `/build-config.json` and Vercel CLI 59.11.7 build completion. The actual `.vercel/output` tree was not retained in the result, so its contents and the emitted BuildOutput object are unknown.

## Source trace

1. The repository’s `apps/frontend/tests/lwc-318-built-config.mjs:36-38` verifies that Next writes `.next/server/app/build-config.json.body` and lists `/build-config.json` in the prerender manifest. The route is `force-static` in `apps/frontend/src/app/build-config.json/route.ts`.
2. Cached `@vercel/next` 11.0.2 source (`package/dist/index.js`, npm-cache tarball SHA-256 `14b51426d1ae4b3ec3bc66649d2cad86e806cd9064ebcddf3f34e11f92bd0a61`) maps the route key to `routeFileNoExt` (line 12677). For an App Router path it looks for `${appDir}/${routeFileNoExt}.body` and wraps an existing body as a `FileFsRef` fallback (12814-12821). It computes `outputPathPage = posix.join(entryDirectory, routeFileNoExt)` (12867) and, for a non-metadata prerender, stores `new Prerender({ lambda, fallback: htmlFallbackFsRef, ... })` at that key (13040-13048). The adapter return merges `prerenders` into `output` (18534-18544).
3. With `entryDirectory="."`, route key `/build-config.json`, and no mount/base-path prefix, the resulting BuildOutput key is exactly `build-config.json`. Cached Vercel CLI 59.11.7 source (`package/dist/chunks/chunk-QHS645AZ.js`, npm-cache tarball SHA-256 `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`) iterates `buildResult.output` and dispatches `Prerender` separately from `File` (10444-10445, 10473-10531). The `Prerender` branch writes its lambda to `.vercel/output/functions/build-config.json.func` (10787-10795) and a `.body` fallback to `.vercel/output/functions/build-config.json.prerender-fallback.body` (10488-10511; extension comes from `FileFsRef.fsPath`, 10918-10926), plus `functions/build-config.json.prerender-config.json` (10513-10523). The `static/build-config.json` destination is used by the separate `isFile` branch (10524-10531, 10665-10689).

## Finding and smallest discriminating check

The pinned source shows that this prerender route can be serialized as a function plus prerender fallback under `functions/`, rather than as a plain file under `static/`. That makes the missing static file consistent with a typed `Prerender` output, but the route log and command exit codes do not prove that this branch ran in the failed job. Do not infer the runner’s actual output layout from the source formula or accept a different layout as ready without an explicit contract decision.

If the parent chooses one more bounded read-only runner observation, check only these fixed paths under the recorded configured output root and return presence/type booleans: `functions/build-config.json.func`, `functions/build-config.json.prerender-fallback.body`, `functions/build-config.json.prerender-config.json`, and `static/build-config.json`. Do not list recursively or emit file contents. This distinguishes the pinned `Prerender` serialization from the expected static file while preserving the current strict validator. If none of those candidates is present, the missing emitted artifact remains unexplained; a targeted runner/build-output fact is then required before proposing any contract or code change.

The earlier prepare state remains failed before runtime mutation, with the Auth receipt reused. Historical live cause remains unknown.
