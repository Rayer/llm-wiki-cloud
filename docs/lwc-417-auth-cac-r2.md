# LWC-417 engineering contract r2

Status: proposed, reconciled revision for independent review. It is not a freeze, implementation, or acceptance result. This revision addresses the independent r1 HOLD and the accepted repairs in live comment 4-3112; it makes no new Owner product gate.

Prepared against LWC source HEAD `90d69094ce876ff3bc0a92998cf7c22a1bfa146c`. The Daily Brief consumer source was inspected read-only at HEAD `1cc9cedae386121eeaa30e3f9cd30bd4c64cf0ab`; that workspace contains dirty and untracked work and is not a deployable revision. Preserve `spec-r1.md`, `brief-consumer-contract-r1.md`, and `spec-review-r1.json` unchanged. No product source was edited. All formal acceptance is NOT RUN.

## Authority and canonical bindings

Live LWC-417 comment 4-3093 remains controlling for ownership: Auth and its CaC stay in LWC; Daily Brief owns its Auth URL, resource, callback, and required-scope reference; there is no shared configuration distribution mechanism. Auth validates the runtime caller and user credential, account state, expiry, revocation, and returns the verified subject, actual OAuth client, scopes, resource, environment, and expiry. Brief enforces its required scope, resource, account-private tenant, credential class, and individual operation authority.

The feature adds exactly one Auth scope, `brief:manage`, for one account-bound private Brief space. There are no teams, fake LWC Project, auto-expansion of old grants, second scope registry, copied Auth signing material, or broad Firestore permission. Existing `mcp:query` and `mcp:raw:write` rules, defaults, legal combinations, grants, and project behavior remain compatible.

The canonical consumer bindings confirmed by live comments 4-3111 and 4-3112 and peer message `msg_c51f96f365a4` are:

- `PUBLIC_ORIGIN=https://brief.rayer.idv.tw`
- `AUTH_RESOURCE=https://brief.rayer.idv.tw/mcp`
- OAuth callback `https://brief.rayer.idv.tw/auth/callback`
- Initial Auth target `AUTH_URL=https://auth.dev.rayer.idv.tw`, canonical Auth environment `dev`
- Auth verifier ID-token audience `https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app`

These canonical Brief values are the resource and callback bindings. The generated Brief Cloud Run hostname is not substituted for them and is not an activation prerequisite. The actual Brief runtime service-account email remains a deployment readback input; until configured, the Brief verifier resource stays disabled. No DNS, IAM, provider, or credential mutation is authorized by this spec.

INNO-45 confirms one initial Brief cloud environment may target Auth DEV and no persistent Brief staging environment is required. The initial Brief Cloud Run process can run with `APP_ENV=production` while explicitly setting `AUTH_ENVIRONMENT=dev`; do not add an `APP_ENV=development` mode.

## Source-confirmed implementation surface

- `deploy/cac/ssot.pkl` defines Auth targets with `environment=dev|prod` and separate `auth_session_environment` values such as `llm-wiki-cloud-dev` and `llm-wiki-cloud-prod`. Auth CaC generation and strict runtime file adoption are implemented in `apps/bff/cmd/pipeline_config/`, `apps/bff/internal/config/auth_file.go`, and `deploy/engine/providers.py`. Auth exposes config identity through `/api/v1/public/version`.
- `apps/bff/internal/auth/mcp_oauth.go` currently hard-codes `mcp:query` and `mcp:raw:write`, parses only project resources, stores grants/codes/tokens in environment-namespaced Firestore, and validates grant/account/auth-version/expiry/revocation/resource/scope/client state during token use.
- `apps/bff/internal/auth/mcp_oauth_http.go` has the existing OAuth metadata, registration, authorize, token, consent, grant-list, and grant-revoke routes. The verifier endpoint proposed below does not currently exist.
- The existing grant API is `GET /api/v1/auth/mcp/oauth/grants?project_id=...` and `DELETE /api/v1/auth/mcp/oauth/grants/:grantID`. `ListGrants` requires a valid project and project ownership; `RevokeGrant` reads the project and rejects an empty `project_id`. `apps/frontend/src/lib/mcp-oauth.ts` only lists by project, and `apps/frontend/src/app/(workspace)/advanced/page.tsx` returns before rendering grant management when there is no selected project. This does not provide a Brief-only account a reachable grant-management path.
- `apps/frontend/src/components/MCPOAuthConsentClient.tsx` and `apps/frontend/tests/lwc-389-mcp-oauth.test.tsx` currently use the project consent shape and LWC scope display. The consent frontend needs additive structured fixed-resource details while preserving project consent behavior.
- Brief routes call its existing `verifyCredential({credentialClass, credential, expectedResource, environment})` seam using `web-session` and `mcp-bearer`. The credential class is currently a server-supplied parameter, not evidence about the OAuth client; the adapter receives the whole Cookie header for Web today. `service/src/config.mjs` has `APP_ENV=local|production` and no Auth URL. Brief rejects caller-supplied tenant/storage authority, and its Firestore path hashes tenant identifiers.
- `.github/workflows/deploy-dev.yml` dispatches on `develop`. Its `components` input is comma-separated, `operation=release` is the normal release path, and the combined selection is `auth,frontend`.

## CaC, generation, snapshot, and consumers

Extend the existing typed Auth CaC and generated Auth configuration with one `oauth_resources` registry. Do not create a second publisher, copy an Auth configuration file into Brief, or make the BFF load Auth's private runtime file.

Each resource entry contains a stable ID, kind (`project-template` or `fixed`), enabled state, exact URI/template, canonical scopes and consent descriptions, allowed scope sets, and an optional default set. The fixed Brief entry also contains the exact allowed verifier caller service-account email and Auth verifier audience.

The LWC project entry retains the current project URI template and `mcp:query` default. Its only accepted sets remain `[mcp:query]` and `[mcp:query, mcp:raw:write]`; raw-write cannot be requested alone. The Daily Brief entry binds exactly `https://brief.rayer.idv.tw/mcp`, permits only `[brief:manage]`, has no default, and requires an explicit request for `brief:manage`. Its consent description is “Read and manage Daily Brief reports and annotations in your private account space.” Keep this canonical resource binding independent of the Brief Cloud Run hostname. Disable Brief authorization and verifier use until the exact Brief runtime service account is configured; the Auth audience is the confirmed value above.

Validate the registry during generation and load: unique resource IDs and scope names, canonical scope syntax, non-empty duplicate-free accepted sets that are subsets of declared scopes, default equal to an accepted set, exact project-template parsing, and canonical HTTPS fixed URIs without userinfo, query, or fragment. A fixed resource has no project ID. Invalid or missing Brief caller configuration fails closed and never falls back to a project resource or another environment.

Extend the Auth projection, immutable input snapshot, materialized runtime file, and runtime config with the registry. Bump new output to strict schema 2 while retaining schema-1 reads with exactly the existing LWC rules and no Brief entry. Retained schema-1 files/receipts remain readable. Include resource, scope, caller identity, audience, and source SHA in canonical config identity as applicable; keep numeric secret-version references in the snapshot. No token, secret payload, OAuth client secret, or runtime ID token enters the snapshot or config ID.

The adoption path remains: Pkl target evaluation → strict Auth projection → Stage 1 immutable non-secret snapshot/config ID → Stage 2 materialized file with pinned numeric secret versions → strict runtime loader → one loaded registry consumed by all Auth OAuth/verifier paths → retained deployment receipt and `/api/v1/public/version` readback. Each registry field change must change `config_id`; secret payloads must not enter the snapshot. Schema 1 continues old LWC defaults; schema 2 rejects unknown/malformed keys.

Use this single loaded registry for authorization-server scope metadata, registration validation, authorize/resource/scope checks, consent details, code exchange, token issuance, refresh, revoke, fixed-resource verification, and existing Auth-side LWC project authorization. Keep the BFF's existing independent LWC-only protected-resource metadata and checks. Brief serves `/.well-known/oauth-protected-resource` with the canonical `AUTH_RESOURCE`, configured Auth URL, and `brief:manage`.

Dynamic registration has no resource argument in the current protocol. Validate its declared scopes against enabled supported sets, preserving the current omitted-scope LWC default. Authorization validates the requested scopes against the exact resource. Brief authorization always supplies `resource=https://brief.rayer.idv.tw/mcp` and `scope=brief:manage`; omitted Brief scope is rejected and never auto-expanded. Existing project behavior is unchanged.

Persist Brief OAuth codes, grants, access tokens, and refresh tokens in the existing environment-namespaced Firestore collections. A Brief grant records the exact resource, `brief:manage`, client ID, authenticated user ID, auth version, timestamps, status, and canonical wire environment. Its legacy `project_id` field is empty, not a fabricated Brief tenant. Do not migrate existing project grants or widen old scopes. Refresh retains the stored client/resource/scope and rechecks grant and account state.

Keep the Auth Firestore namespace stable: `auth_session_environment` continues to select the existing `llm-wiki-cloud-dev` or `llm-wiki-cloud-prod` collections. Do not replace it with wire environment `dev`/`prod`. The verifier response environment is the lowercase canonical Auth target value `dev` or `prod`, derived from the Auth config `environment` field. Never send `DEV`, `Prod`, or `development` on the shared verifier wire. Local-only fixtures may use `local` only against local Auth and cannot be accepted by shared Auth.

## Account-owned fixed-resource grant API and UI

Retain the existing authenticated routes and add a fixed-resource selector to the list route; do not add a new grant-management service:

- `GET /api/v1/auth/mcp/oauth/grants?project_id=<projectID>` retains current project filtering and project-owner validation for existing LWC grants.
- `GET /api/v1/auth/mcp/oauth/grants?resource=https%3A%2F%2Fbrief.rayer.idv.tw%2Fmcp` lists the authenticated account's Brief grants. The caller is identified by the existing authenticated web principal, never by a query/body user ID. Require exactly one supported filter; reject both filters, unknown resources, and an unfiltered request. Select records matching both `grant.user_id == principal.userID` and the exact enabled fixed Brief resource, and require `grant.project_id == ""`. Do not read or require an LWC Project document for this branch.
- The grant response retains the existing `grants` array and fields (`id`, `client_name`, `project_id`, `resource`, `scope`, `status`, `created_at`, `expires_at`, optional `revoked_at`). Fixed-resource rows use empty `project_id`. Never return another account's grant.
- `DELETE /api/v1/auth/mcp/oauth/grants/:grantID` remains the revoke route. In one transaction, require active authenticated account/current auth version and exact grant ownership. For a Brief fixed-resource grant, require its canonical resource and empty project ID and do not read a Project. For an LWC project grant, retain current Project ownership checks. Return 404 for absent or other-account grant IDs without revealing which check failed; return 204 for a successful/idempotent owner revoke.
- Revoke changes the authoritative grant state. The next verifier call denies the Brief token, and refresh returns the existing `invalid_grant` error. No verification-success cache or Brief-local substitute revocation state is permitted.

Make the existing workspace Advanced page's account grant panel reachable for a signed-in, non-demo user with no selected LWC Project. Give its list/loading/error state an account/session key independent of `currentProject`, so its loader does not take the current no-project early return; call the resource-filtered helper for the canonical Brief resource and let the account revoke its Brief client grants. Do not show fake project labels. Keep the current selected-project list/state and project-specific MCP/manual-key sections on their existing `project_id` filter and owner checks; hide only those project-bound sections when there is no current project. In `apps/frontend/src/lib/mcp-oauth.ts`, add an explicit resource-list helper and preserve the current project-list helper.

Consent response details add structured resource and scope display without breaking legacy project consent: retain the old project fields and `scope` string for project resources, and add `resource_kind`, `resource_name`, and structured `scope_details` from the loaded registry. For fixed Brief consent, set `resource_kind=fixed`, `resource_name=Daily Brief`, exact canonical resource, `scope=brief:manage`, and a single description in `scope_details`; omit `project_id` and `project_name`. The updated consent component normalizes both shapes: old project response keeps existing Project name and scope display; fixed response displays Daily Brief and its scope description with no Project label. Do not hard-code policy text in the frontend.

## HTTP verifier and mutually exclusive client classification

Add one app-authenticated route:

    POST /api/v1/auth/consumers/daily-brief/verify-credential

Auth remains publicly reachable at Cloud Run for OAuth, login, and discovery; do not put a service-level IAM restriction on the whole Auth service. The Brief runtime obtains a short-lived Google-signed OIDC ID token with audience exactly `https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app` and calls the Auth URL `https://auth.dev.rayer.idv.tw`. Auth validates signature, issuer, expiry, exact audience, verified email, and exact configured Brief runtime service-account email. Caller email configuration remains disabled until its deployment identity is read back. Do not add a static shared verifier secret or Firestore access for Brief.

Request headers are:

    Authorization: Bearer <Google-signed Brief runtime ID token>
    X-LWC-User-Credential: <opaque Auth OAuth access token>
    Accept: application/json

Do not define or accept `X-LWC-Credential-Class`; it is supplied by the caller and cannot prove token class. There is no user token in a URL or body. Redact both credential headers from logs/traces, return `Cache-Control: no-store`, and do not enable browser CORS.

Auth validates the opaque token against its stored access-token/grant state and current account: token active and unexpired; grant active/unexpired/unrevoked; account active; stored auth version current; grant/client record consistent; and token belongs to the running Auth environment. It returns the actual client ID, scope set, and resource read from that verified Auth grant, not caller-supplied values. It does not discard a valid credential merely because the resource or scope is not suitable for Brief: Brief checks exact resource, required scope, canonical environment, client class, tenant, and operation authority before executing data work. Auth does not infer or echo a credential class and does not return a Brief tenant or Brief capabilities.

Success is HTTP 200 JSON, `Cache-Control: no-store`:

    {
      "user_id": "stable Auth account subject",
      "client_id": "client ID from verified Auth grant",
      "scopes": ["brief:manage"],
      "resource": "https://brief.rayer.idv.tw/mcp",
      "environment": "dev",
      "expires_at": "RFC3339 timestamp"
    }

Failure behavior: 400 `invalid_request` for malformed/missing required headers; 401 `invalid_token` for invalid, expired, revoked, inactive-account, stale-auth-version, or credential not valid in this Auth environment; 403 `untrusted_caller` for invalid caller ID token/wrong service account; 429 for rate limit; 503 `temporarily_unavailable` for Auth config/Firestore failure. For a valid token, return its actual scope/resource/environment even when Brief will reject them. Brief returns forbidden before service execution for a verified resource, scope, environment, or client-class mismatch. Do not expose email, token, grant record, signing material, or Firestore details. Every failure is fail-closed.

Brief preserves its current in-process `verifyCredential({credentialClass, credential, expectedResource, environment})` interface. The HTTP and MCP route choose the class internally: Web API extracts only the dedicated access-cookie value and passes `web-session`; MCP `/mcp` extracts only a Bearer token and passes `mcp-bearer`. Do not forward a full Cookie header; do not send a class header to Auth. The route-derived class selects a check, while the Auth-verified `client_id` proves which OAuth client issued the token:

- For `web-session`, require returned `client_id === AUTH_WEB_CLIENT_ID`.
- For `mcp-bearer`, require returned `client_id !== AUTH_WEB_CLIENT_ID` and an active grant verified by Auth. Both classes then pass the same exact Brief resource, `dev` environment, `brief:manage`, expiry, and operation checks against the actual Auth response. A caller-supplied ID is never used.
- Therefore a Web token relabeled as MCP and sent in Authorization is rejected, and a native MCP token relabeled as Web and sent in the cookie is rejected before any data operation. The Auth caller cannot override this with an HTTP class header.

For accepted requests, require the exact configured resource, lowercase `dev` environment, and `brief:manage` in the Auth-verified response; then map verified `user_id` to Brief's one private account tenant, use the existing tenant hashing, and map `brief:manage` into Brief's existing per-operation capabilities. Web retains its current Web operation set; MCP retains its existing six-tool operation set. Never trust caller tenant/storage path. On Auth 401 return unauthenticated; on verified resource/scope/environment/client-class/capability mismatch return forbidden; on caller-auth/configuration/malformed-response errors return a fail-closed authorization error; on timeout/network/429/503 return service unavailable. Do not cache successful verification, so grant revocation affects the next request.

## Web login, session, and native MCP flows

Register one dedicated public Daily Brief Web OAuth client through `POST ${AUTH_URL}/api/v1/auth/mcp/oauth/register`, using `token_endpoint_auth_method=none`, authorization-code and refresh-token grants, code response type, exact redirect URI `https://brief.rayer.idv.tw/auth/callback`, and explicit `brief:manage`. Store its returned ID as `AUTH_WEB_CLIENT_ID` in Brief's environment configuration. Native MCP clients use their own dynamic registrations and client IDs.

Web login uses Auth discovery and the existing authorize route with the Web client ID, exact canonical callback, one-time state, PKCE S256 challenge, `resource=https://brief.rayer.idv.tw/mcp`, and `scope=brief:manage`. The callback consumes state once and exchanges the code server-side at the existing Auth token endpoint with exact client, callback, verifier, and resource. Set access and refresh tokens only in host-only `Secure`, `HttpOnly`, `SameSite=Lax` cookies with explicit expiry; JavaScript never receives either token. Keep access cookie path `/api/v1` and refresh cookie path `/api/v1/auth/session/refresh`. A same-origin refresh endpoint rotates cookies on success and clears them on `invalid_grant`; logout clears both. The existing Auth account grant-management route is the user-visible revoke authority.

Brief protected-resource metadata advertises only the canonical resource, Auth URL, and `brief:manage`. Native MCP uses its registered OAuth client and PKCE, and `/mcp` accepts only bearer credentials. Both the Web and MCP service route call the same verifier endpoint, but their route-derived credential class and verified client ID checks are mutually exclusive as defined above.

## Exact Brief runtime configuration and consumer handoff

Use these names consistently in `service/src/config.mjs`, adapter code, and deployment configuration:

- `APP_ENV`: existing `local` or `production`; no `development` value. Initial Cloud Run uses `production`.
- `AUTH_MODE`: `local-test` only for local fixtures; `shared` for Cloud Run.
- `AUTH_URL`: `https://auth.dev.rayer.idv.tw` for the initial cloud environment.
- `AUTH_ENVIRONMENT`: canonical lowercase `dev` or `prod`; initial cloud value is `dev`, explicit even though `APP_ENV=production`.
- `AUTH_RESOURCE`: `https://brief.rayer.idv.tw/mcp`.
- `AUTH_REQUIRED_SCOPE`: `brief:manage`.
- `AUTH_WEB_CLIENT_ID`: exact public Daily Brief Web client registered in Auth DEV.
- `AUTH_VERIFIER_AUDIENCE`: `https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app`.
- `AUTH_VERIFIER_CALLER_EMAIL`: exact Brief Cloud Run runtime service-account email, pending deployment readback.
- `PUBLIC_ORIGIN`: `https://brief.rayer.idv.tw`.
- `AUTH_VERIFIER_MODULE`: existing module seam.

Reject missing/mixed shared config, non-HTTPS public/Auth/resource values, callback/resource mismatch, an unsupported Auth environment, or local-test credentials in Cloud Run. A valid shared pairing is `APP_ENV=production`, `AUTH_MODE=shared`, `AUTH_ENVIRONMENT=dev`, and the explicit Auth DEV URL/audience above. Production Auth (`prod`) is out of scope. Local fixtures stay isolated and synthetic; they cannot claim Cloud Run OIDC or DEV acceptance.

LWC implementation paths:

- `deploy/cac/ssot.pkl`, pipeline-config projection/generator, snapshot and materializer: type the scope/resource registry and exact canonical binding.
- `apps/bff/internal/config/auth_file.go` and config tests: schema 2 plus strict schema-1 compatibility.
- `apps/bff/cmd/auth/main.go`, `apps/bff/internal/auth/mcp_oauth.go`, and `mcp_oauth_http.go`: registry consumers, verifier route, verified client ID response, mutually exclusive grant list/revoke branches, account ownership, and fixed resource with empty project ID.
- `apps/frontend/src/components/MCPOAuthConsentClient.tsx`, `apps/frontend/src/lib/mcp-oauth.ts`, `apps/frontend/src/app/(workspace)/advanced/page.tsx`, `apps/frontend/messages/en.json`, `apps/frontend/messages/zh-TW.json`: consent normalization, resource list helper, no-project account grant panel, project-specific compatibility, and localized copy.
- Existing OAuth/config/Firestore tests plus `apps/frontend/tests/lwc-389-mcp-oauth.test.tsx`; add the focused cases below.

Daily Brief owner implementation paths, read-only inspected for this task: `service/src/config.mjs`, `service/src/auth.mjs`, `service/src/http.mjs`, `service/src/mcp.mjs`, and focused `service/test/auth.test.mjs`, `config.test.mjs`, `http.test.mjs`, `mcp.test.mjs`, and `emulator.test.mjs`. Do not edit Brief from this LWC lane.

## Verification plan and acceptance limits

Run only after the r2 contract is independently reviewed/frozen and implementation exists. All checks here are NOT RUN.

Auth/config tests:

- Schema-1 read compatibility and old LWC defaults; schema-2 strict validation, canonical resource validation, legal/duplicate scope sets, disabled Brief behavior, caller email/audience checks, and config ID adoption for every registry field.
- Verify all Auth consumers use one loaded policy: metadata, register, authorize, consent, exchange, refresh, list, revoke, credential verify. Existing `mcp:query` defaults and legal `mcp:raw:write` set remain exact; no old grant is widened.
- Test verifier output environment is lowercase `dev`/`prod` from Auth target while Firestore still uses the exact old `auth_session_environment`. Exercise `dev` Pkl → pipeline snapshot → loader → verifier response → Brief config validation; do not use mismatched-case fixtures.
- Test fixed-resource grants write empty `project_id` and do not read or fabricate Project docs. Test account-owned `resource=` list/revoke without a Project; other accounts cannot list or revoke; old `project_id=` listing and owner checks stay compatible. On revoke, the next access verification returns 401 and refresh returns `invalid_grant`. A valid token for the wrong resource or without `brief:manage` is returned with its actual Auth attributes and then denied by Brief before any operation.
- Use the real Firestore emulator without skips for successful Brief consent/code/refresh/verify and negative controls: wrong resource/environment, absent scope, expired access/grant, revoked grant, disabled account, stale auth version, missing/corrupt records, and Firestore unavailable. For valid tokens with wrong Brief resource or absent `brief:manage`, verify Auth returns the actual grant attributes and Brief rejects them before any operation. Preserve current project grant emulator coverage.
- Test caller ID token issuer/audience/email/expiry/service-account validation and ensure logs contain neither credential header. Run config-adoption checks against the actual Pkl Auth target and generated snapshot; prove no secret payload inclusion and exact runtime `/api/v1/public/version`/receipt identity.

Frontend tests:

- On `/advanced`, a signed-in account with no selected LWC Project can list and revoke its Brief grant through the exact `resource` filter; no `project_id` is required. A second account cannot see or revoke it.
- Existing selected-project list path continues to send `project_id` and preserves current UI behavior.
- Consent renders both legacy project fields/scope and the new fixed Daily Brief resource/scope details. Fixed consent never renders a Project ID/name and can be approved or denied. Verify English and Traditional Chinese strings.

Brief consumer tests:

- Same verified Web-client token sent as MCP bearer is rejected; native-client token supplied as Web cookie is rejected. Web accepts only `client_id === AUTH_WEB_CLIENT_ID`; MCP accepts only a verified non-Web client ID for the exact resource/scope. No `X-LWC-Credential-Class` is sent or trusted.
- Verify exact resource/scope/`dev` environment, `APP_ENV=production` plus `AUTH_ENVIRONMENT=dev` config, cookie extraction, PKCE state, refresh/logout/revoke, timeout/network/401/403/429/503 behavior, account-private tenant mapping, and no operation/storage write on negative controls.
- Use actual Firestore emulator with local Auth runtime and fake Brief storage to verify grant/account/revoke behavior. Local checks cannot prove Cloud Run service identity or external OAuth acceptance.

DEV acceptance, after coordinator-owned deployment and runtime SA readback: real Auth DEV caller OIDC; canonical Web callback/cookies; native MCP registration/access; resource/scope/environment denial; refresh; grant list and revoke from an account with no LWC Project; cross-account denial; and unchanged Brief data on every negative control. A healthy endpoint, disabled resource, emulator pass, or Auth-only deployment is not product acceptance. Production is excluded. All formal acceptance remains NOT RUN.

## Canonical DEV release and rollback

This is the later coordinator-owned path after independent review/freeze, implementation, same-head reviews/CI, and the reviewed revision is on `develop`. Do not deploy this evidence-preparation branch or any unreviewed source.

1. Dispatch `.github/workflows/deploy-dev.yml` on `refs/heads/develop`, `operation=release`, `components=auth,frontend` (comma-separated actual selected-component input). This selection includes Auth/config and the changed consent UI; do not invent an `auth-config` component.
2. Use a unique immutable `release_tag`. For a normal release, leave `source_sha`, `artifact_id`, `dev_artifact_id`, and unrelated config-only IDs empty; the workflow binds `source_sha` to `github.sha` and `executor_sha` to the workflow SHA. Set `force=false`.
3. Record both selected component identities in the retained release receipt. Auth generates and publishes its pinned config snapshot as part of the Auth component. Keep the Brief fixed resource disabled until its configured caller service account is read back and bound.
4. Read back both Auth and frontend from the exact retained artifact. Confirm selected component/source/artifact identities, Auth image/revision/traffic, pinned numeric config version, receipt `config_id`, `/api/v1/public/version` commit/schema/config ID, enabled OAuth metadata, and frontend artifact/source. Verify the consent response/resource view against the canonical bindings.
5. Roll back the same retained release artifact/plan with `operation=rollback` and `components=auth,frontend`, restoring the captured prior Auth/config and frontend identities together. Then use `operation=readback` with `components=auth,frontend` and the same retained identity; confirm both prior revisions/artifacts plus Auth config ID and traffic. A service rollback does not reverse dynamic OAuth grants or Brief data. For uncertain state, inspect the retained receipt/checkpoint before retrying; do not create a new release over an unresolved attempt.

No production change, DNS/provider mutation, IAM grant, or credential operation is part of this task. The only external activation input still pending is the actual Brief runtime service-account email. Read it from the authorized Brief deployment state, bind it to Auth CaC and Brief's separately owned config, regenerate the Auth snapshot/config ID, and verify caller OIDC plus the emulator negative controls before enabling the resource. The canonical origin, callback, resource, Auth DEV URL, and Auth audience above are already fixed; do not replace them with generated Brief hostnames.
