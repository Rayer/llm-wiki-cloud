# LWC-319: Production Google configuration prerequisite

`production.yaml` now enables the reviewed Production Google configuration using
real nonsecret provisioning handles verified by the parent/deployer on
2026-09-08. This is source integration, not runtime rollout or Google acceptance.
The supplied evidence records a separate Production Web client, exactly the two
callback URLs below, no JavaScript origins, ENABLED secret version 1 and an exact
secret-resource accessor binding for the Production Auth runtime account.
No secret payload was read during source integration.

Consent was External/Testing in that evidence. Live `/privacy` and `/terms`,
Branding completion and shared consent publication remain deployer-owned gates.
Parent must independently review the final source candidate before merge or
promotion; provisioning alone does not satisfy that gate.

Absence of `auth.google` still retains image-only delivery. An explicit disabled
block (`auth.google.enabled: false`, with no other Google fields) opts into the
config contract and removes Google settings; it is not an enablement placeholder.

## Integrated nonsecret configuration

| Field | Reviewed Production contract |
| --- | --- |
| `enabled` | `true` (source only; rollout gated) |
| `client_id` | `580854833715-1b37asap0uocbdcrighjaorflvj2n94m.apps.googleusercontent.com` |
| `client_secret_reference` | `google-oauth-client-prod` |
| `client_secret_version` | `"1"`, verified ENABLED; `latest` is rejected |
| `issuer` | `https://accounts.google.com` |
| `jwks_url` | `https://www.googleapis.com/oauth2/v3/certs` |
| `token_url` | `https://oauth2.googleapis.com/token` |
| `login_redirect_url` | `https://auth.rayer.idv.tw/api/v1/auth/google/callback` |
| `link_redirect_url` | `https://auth.rayer.idv.tw/api/v1/auth/google/link/callback` |
| `completion_url` | `https://wiki.rayer.idv.tw/login` |

The reference is a Secret Manager secret ID in project `llm-wiki-cloud`, whose
full resource is `projects/llm-wiki-cloud/secrets/<secret ID>`. If provisioning
returns a different dedicated Production secret name, review that exact binding
in the validator and tests before inserting it in YAML. The numeric version and
secret-level accessor for `lwc-auth-prod@llm-wiki-cloud.iam.gserviceaccount.com`
are checked through metadata/IAM only; the deploy path does not access payloads.
Client ownership and secret correspondence require deployer evidence; syntax
validation and fixtures cannot establish them.

## Delivery and recovery

The optional Production Google block binds Auth, BFF and frontend to the existing
Production service names, runtime accounts, database, hosts/origins and public
URLs. DEV's configuration and behavior are retained. Production never obtains
its runtime configuration from a DEV image receipt.

When the block is present (enabled or disabled), selected Auth and BFF components
use the existing config-aware revision path:

1. Freeze the effective revision, immutable image and sanitized fingerprint of
   managed configuration, secret references and runtime account in the durable
   rollback artifact. Retain that revision through acceptance.
2. Consume the exact DEV immutable image/provenance receipt for Production.
   Preserve canonical CI, exact-source, component-selection and journal guards.
3. Update with `--no-traffic`, capture the exact created revision and verify
   readiness, image, runtime account and complete managed configuration.
4. Revalidate before switching traffic explicitly to that revision. Verify
   effective 100% traffic and configuration again, and record journal acceptance.
5. On rollback, verify the retained baseline against both frozen image and config
   fingerprint before routing traffic back; verify both again afterward.

Rollback restores the effective retained revision, including settings that were
previously absent. It does not recreate a revision by combining the old image
with the new service template. Missing baseline revisions, changed fingerprints,
readback failures or split traffic require operator reconciliation. Raw revision
JSON and credential payloads are not persisted in rollback evidence.

Google secrets are pinned to an immutable numeric version. The existing
`jwt-secret-prod:latest` reference is preserved, as is DEV's JWT binding; this
prerequisite does not invent a JWT version. The fingerprint freezes references,
not secret payloads. Do not rotate or remove baseline secrets during the release
and rollback window.

## Runtime and frontend implications

Auth delivers `GCP_PROJECT`, `FIRESTORE_DATABASE_ID`, `ALLOWED_HOSTS`,
`ALLOWED_ORIGINS`, `AUTH_SERVICE_URL=https://auth.rayer.idv.tw`, `DEV_JWT=false`,
`AUTH_SESSION_ENVIRONMENT=llm-wiki-cloud-prod`, and
`AUTH_REFRESH_SESSION_MIGRATION=disabled`, plus enabled Google settings and the
pinned secret reference. Disabled delivery removes all managed Google settings.
The session namespace equals `cmd/auth/main.go`'s database-derived default; it is
not `production` or the DEV namespace. No legacy refresh import, registration
setting change, data migration, fixture or Worker execution is introduced.
Sessions minted by the new runtime are not guaranteed usable by the retained old
runtime after rollback; users may need to sign in again. Rollback changes traffic
and config, and does not undo session records written by a deployed runtime.

BFF delivers its Production project/database, allowed origins, JWT reference,
`DEV_JWT=false` and `AUTH_SERVICE_URL=https://auth.rayer.idv.tw`, with the same
config-aware readback and rollback. Current `registerPublicRoutes` accepts but
does not use that URL. BFF verifies JWTs and account state; Auth owns the durable
refresh authority and host-only cookies. No BFF session namespace or Google
credential is added.

The frontend calls Auth directly. Before traffic, the existing frontend build
config gate and deployer readback must establish the canonical API
`https://llm-wiki-bff-580854833715.asia-east1.run.app`, Auth URL
`https://auth.rayer.idv.tw`, served build config, aliases, callbacks and host-only
cookie origin. A BFF environment update does not repair an incorrect frontend
build. Auth/BFF/frontend component selection must exactly match the producer DEV
receipt; Worker remains outside LWC-319.

## Verification and release boundary

`make test` and canonical CI run offline DEV/Production provider fixtures,
loader validation, existing deployment guards and Go tests. Fixtures cover
config enable/disable, strict environment separation, pinned refs, no traffic
before exact config readback, journal/provenance guard failures and retained
config+image rollback. They do not call Google, GCP or Firestore.

Parent final source review, full exact-candidate CI, a fresh
live DEV gate and a new exact-candidate Production mission remain required.
This document supplies no deployment authorization or live acceptance evidence.
