# LWC-402 DEV request-ID probe

From the repository root, the local command is:

```sh
python3 scripts/verify_request_id_trace.py --artifact /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc402-request-id-probe.json
```

The command first checks the approved isolated project, current fixture
readiness, and read-only Logs/Trace access. Missing prerequisites produce a
nonzero `NOT RUN` report before any REST, MCP, Query, or Pipeline product
request. The current `dev-prerequisites.json` intentionally leaves
`query_project_id`, `pipeline_project_id`, and `fixture_project_id` unset and
records `fixture_readiness` as `not_verified`; live readiness is **NOT RUN**.
The historical synthetic project ID is reference only and does not establish
current readiness.

After Main separately authorizes a real DEV fixture confirmation, the
non-secret static configuration must describe that confirmed fixture:

- In `dev-prerequisites.json`, set `query_project_id` to the approved isolated
  QA project, `fixture_project_id` to that same project, `fixture_readiness`
  to `verified`, and `fixture_marker` to the marker confirmed in the project.
- In `sanitized.json`, keep `query` tied to that marker and set the expected
  answer and citation title/type to the confirmed synthetic fixture evidence.
  Do not treat the current historical fixture description as proof of a
  currently published source.
- To opt into Pipeline later, separately approve the isolated QA project and
  set `pipeline_project_id` in `dev-prerequisites.json` to its exact project
  ID. Leave it unset when Pipeline is not approved.

The environment and identity prerequisites for the required REST, MCP, and
Query checks are:

- `LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID`: exactly matches the static
  `query_project_id`.
- `LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY`: the query-only application key for
  that isolated QA project.
- A local read-only `gcloud` identity, or the existing GitHub Development WIF
  secrets `WIF_PROVIDER` and `WIF_SERVICE_ACCOUNT`, with Logs/Trace read
  access. Actual IAM permissions have not been verified.

Use approved secret storage for keys and tokens. Never pass them in command
arguments, workflow inputs, logs, or the JSON artifact. The default command
checks REST 404, an HTTP 200 MCP tool error, and a full Query response. Query
success requires `ai_synth` to contain `20:00`, a result for the Harbor
Library source title, and a citation for that same source. The expected query
is in `sanitized.json`; the probe does not set up, publish, or mutate the
project.

## Optional Pipeline check

Pipeline remains skipped unless explicitly enabled. An enabled run always
requires an independently supported bearer identity, even when the Pipeline
and Query project IDs are the same; the query-only application key is never a
Pipeline credential. Configure the optional
`LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN` only through approved secret
storage, and set `LWC_DEV_PIPELINE_QA_PROJECT` to exactly match the separately
approved static `pipeline_project_id`. The independent token is passed to
both submit and status requests. Without it, the probe reports
`pipeline_identity_missing` and exits nonzero before Cloud or product calls.

Only after fixture confirmation and the authorized static prerequisite update
is present on `develop`, dispatch the current development revision:

```sh
gh workflow run verify-request-id-trace-dev.yml --ref develop -f enable_pipeline=false
```

The workflow accepts dispatches from `develop` only and checks out that
dispatch's SHA. Confirm the run SHA is the revision containing the approved
prerequisite update before interpreting its artifact. To test Pipeline, use
`-f enable_pipeline=true` only after its separate project allowlist and
independent bearer token are configured.

An enabled Pipeline run waits for a terminal provider state and requires
separate worker trace evidence linked to the submitting request. Native
stored-Link verification remains **MANUAL** in Trace Console and is never
inferred from same-ID or bridge logs.
