# LWC-358 fixed DEV Frontend read-only diagnostic

## Scope and identity

This local candidate is based on `origin/develop` at `b978fe10fb532e87b5afe5dee7e65712622857dd`. It adds one explicitly selected `diagnose-frontend-deployment` operation to the existing DEV dispatch and routes it to a separate reusable workflow with a single `contents: read` job. The job checks out the current `develop` SHA, then the script verifies that its diagnostic-code SHA equals `GITHUB_SHA`; the historical deployment source SHA remains a separate fixed target value.

The target tuple is run `37157593306`, source SHA `b978fe10fb532e87b5afe5dee7e65712622857dd`, tag `dev-lwc-366-b978fe10fb53`, attempt `142b8a78e39db1d7359d42093e321adab951e1f413f913f4d0df09fbe57a4d92`, artifact SHA-256 `fd4942da5bafb1cf5a16838ad59956537d163a6e5971d114bf550cd0d7640efa`, project `prj_m4r0AIf6l7RgIBsBdpuJvJtUM9th`, team `team_Z6PbTGXwFFzZuOgjYegBojxp`, and stable alias `wiki.dev.rayer.idv.tw`.

## Read-only request path

The diagnostic issues only these Vercel GET requests, using the existing `Development` environment's `VERCEL_TOKEN`:

1. `GET /v6/deployments` scoped to the fixed project and team, with a limit of 100. It reports the API-reported total when present and inspects only the first response page. It matches the fixed `lwcAttempt` and `lwcArtifact` metadata values. No match is reported as an observation; the result explicitly says first-page absence is inconclusive.
2. `GET /v13/deployments/{id}` for exact metadata matches and, if different, the stable alias's deployment ID. Returned fields are reduced to the deployment ID (when it has the expected shape), project/team equality booleans, ready state, target, and URL-presence boolean.
3. `GET /v4/aliases/{fixed-domain}` scoped to the fixed team. The result contains existence, deployment ID, project equality, and whether the ID is among the exact metadata matches.

The script never emits the token, request headers, raw API response/body, or deployment URL. Errors are reported with a fixed stage and bounded structured cause. A missing alias (HTTP 404) is recorded as an observation. This diagnostic does not gate, mutate, deploy, assign aliases, upload artifacts, invoke the release workflow, or change runtime state. The caller and callee grant only `contents: read`; the job receives no Actions, id-token, GCP/WIF, GitHub token, or inherited secrets authority.

## Evidence and limits

Offline tests exercised the actual diagnostic entry function with a fake HTTP transport. They assert every request is GET, the fixed tuple and filters are used, duplicate deployment IDs are deduplicated, details and alias are read back, zero matches remain inconclusive, a 120-row API response is capped at 100 returned rows, context mismatch makes zero requests, and provider error output excludes both a synthetic token and raw response body.

These tests prove the local code path and workflow wiring only. No Actions diagnostic has been dispatched and no Vercel API was called in this implementation. The accepteddeployment's live deployment record, stable-alias target, and provider cause remain unknown until the reviewed workflow runs. The historical failed run's original exception was not preserved; this diagnostic cannot recover it retroactively. The first-100 result cannot establish that a deployment is absent from the full project history.

## Local validation

Commands run from the isolated worktree with `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`:

- `python3 -m unittest deploy.engine.tests.test_frontend_deployment_diagnostic -v` — 5 tests passed.
- `node --test apps/frontend/tests/ci-workflow-contract.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs` — 42 tests passed. The first run stopped at module loading because the isolated worktree has no `js-yaml`. The successful run temporarily linked the existing shared-worktree `apps/frontend/node_modules` for module resolution and removed the link after the run. No package install or network access was used.
- `git diff --check` — passed.

`actionlint` was not present in the environment. The workflow YAML was parsed and its branch, operation, checkout SHA, and permission boundary were checked by the passing workflow-contract tests.

## Content identity

The canonical [implementation-content.json](../implementation-content.json) now contains 57 exact path/mode/content rows, including this round's six implementation/workflow/test files. Its `content_sha256` is `d6963f6a6d1c39db178a82d1701b62df6661ed405208fcdb4d2341bf40203fb2`, and the manifest file SHA-256 is `bb6d4647a9ccaed9d9c54781f5581ad1451fb19f3d4f05b9a8bef7f74acaa483`. The frozen spec SHA-256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. The manifest excludes itself, this report, and raw test outputs. It was recalculated in the isolated `b978fe1` worktree, so it does not include the dirty test/manifest delta from the shared checkout.

Parent also ran the fifth diagnostic test under the host's Python 3.9 and observed a `tempfile` `KeyError`; that environment result is retained as a limitation, not described as a source pass. The source suite's recorded pass used Python 3.14.6.

## Intended reviewed dispatch payload

After this candidate is published, reviewed, and passes CI, the existing `Deploy Development` workflow can be dispatched on `develop` with:

```json
{
  "components": "frontend",
  "release_tag": "dev-lwc-366-b978fe10fb53",
  "artifact_id": "",
  "dev_artifact_id": "",
  "operation": "diagnose-frontend-deployment"
}
```

The diagnostic job checks out the reviewed current `develop` SHA. This dispatch was not performed as part of this local implementation.
