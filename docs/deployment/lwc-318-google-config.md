# LWC-318: DEV Google configuration delivery

The repository enables `auth.google` in `deploy/environments/development.yaml`
with the operator-verified DEV Web client ID and `google-oauth-client-dev:1`
Secret Manager reference. Provider metadata readback confirmed version 1 is
ENABLED, the exact redirects/origin below and secret-level runtime IAM.
This is source configuration, not deployment or real Google UAT acceptance.
Absence of the optional block retains artifact-only delivery; Production rejects
the block and its YAML is unchanged.

## Operator continuation

Parent review, successful CI and a fresh exact-candidate DEV mission are required
before any deployment. This source change does not authorize provider creation,
IAM changes or deployment. Preserve these operator constraints:

- The actual DEV **Web application** OAuth client ID, ending in
  `.apps.googleusercontent.com` (not the test fixture ID).
- Its client secret stored through the approved secret-input mechanism in
  project `llm-wiki-cloud`, Secret Manager secret `google-oauth-client-dev`.
  Supply only the enabled numeric version in source; never put the payload in
  YAML, command arguments, logs, artifacts or chat. The Auth runtime account
  `lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com` needs the existing reviewed
  secretAccessor binding; missing IAM is a separate authorization blocker.
- Provider readback confirming the exact redirect allowlist below and the DEV
  frontend JavaScript origin `https://wiki.dev.rayer.idv.tw`, plus the appropriate
  consent/test-user setup for the separately approved disposable identities.

The configured nonsecret values are:

| `auth.google` field | Required value |
| --- | --- |
| `enabled` | `true` |
| `client_id` | `580854833715-vo7fg6f7f15g1kkgchk1ulccllbc24qg.apps.googleusercontent.com` |
| `client_secret_reference` | `google-oauth-client-dev` |
| `client_secret_version` | `"1"`; `latest` rejected |
| `issuer` | `https://accounts.google.com` |
| `jwks_url` | `https://www.googleapis.com/oauth2/v3/certs` |
| `token_url` | `https://oauth2.googleapis.com/token` |
| `login_redirect_url` | `https://auth.dev.rayer.idv.tw/api/v1/auth/google/callback` |
| `link_redirect_url` | `https://auth.dev.rayer.idv.tw/api/v1/auth/google/link/callback` |
| `completion_url` | `https://wiki.dev.rayer.idv.tw/login` |

The loader checks syntax and exact reviewed environment bindings, not ownership
of an OAuth client. Provider/client-to-secret correspondence must be established
by provisioning evidence and subsequent real Google UAT. Offline fixtures are
not Google acceptance.

## Delivery and recovery contract

The existing canonical DEV wrapper is unchanged. Select Auth and frontend only
when the fresh mission authorizes both; BFF requires a demonstrated integration
need, and Worker is outside this task. Existing preflight, pinned CI, uploaded
rollback artifact, image receipt, journal and reconciliation guards still apply.

The optional DEV block delivers project/database identity, hosts/origins, Auth
URL, `DEV_JWT=false`, session environment and `AUTH_REFRESH_SESSION_MIGRATION=disabled`, plus
Google values and its pinned secret reference when enabled. Disabled means all
Google variables/references are removed, and their absence is verified. JWT
continues to use its existing `jwt-secret-dev:latest` reference. Shared durable
refresh authority is already wired in `cmd/auth/main.go`; no migration or legacy
read-through is enabled here.

Delivery uses `gcloud run services update --no-traffic`, captures the created
revision, reads it back and verifies its immutable image, readiness, runtime
account, exact managed nonsecret values and secret references (resolving provider aliases to their project/name). Only then can an
explicit `--to-revisions REVISION=100` command run, after another pinned-CI /
durable-artifact check. Final readback and reconciliation repeat the effective
config check. A mismatch never authorizes a traffic command.

The existing durable rollback artifact gains the previous effective revision
and a fingerprint of the scoped nonsecret values, secret references and runtime
account alongside its immutable image. Raw revision JSON and secret payloads
are never persisted. Rollback first checks that retained revision against the
frozen image/config fingerprint, routes directly to it, then verifies exact
100% traffic and configuration again. It restores effective configuration,
including previously absent settings; it does not rewrite the latest service
template. Retain that baseline revision until the migration is accepted. An
unavailable baseline or readback must stop recovery for operator reconciliation.
The existing JWT `latest` reference does not freeze secret payloads; do not
rotate/delete referenced secrets during this mission.

Production still uses the original artifact-only image update and image-handle
rollback. No Production runtime configuration is carried over from DEV.

## Offline checks

`make test` includes loader validation, existing CD guard fixtures and the actual
Auth shell provider-order fixtures in `scripts/test_auth_config_contract.py`.
They cover disabled/enabled delivery, missing/mismatched values and references,
secret literal rejection, no traffic on pre-switch mismatch, final readback
failure, and sanitized retained-revision rollback. The provider is an offline
stub; it makes no Google/GCP calls.

Cloud CLI flag contract: [gcloud run services update](https://docs.cloud.google.com/sdk/gcloud/reference/run/services/update).
