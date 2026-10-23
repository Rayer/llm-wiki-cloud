# Daily Brief consumer contract r2

Status: proposed implementation handoff aligned with `spec-r2.md`. No Brief source was edited. This reconciles the independent HOLD findings accepted in LWC-417 comment 4-3112.

## Canonical cloud configuration

The initial Brief Cloud Run process runs in its existing `APP_ENV=production` mode and explicitly targets LWC Auth DEV. Do not add an `APP_ENV=development` mode. Use the owner-confirmed canonical bindings throughout:

- `PUBLIC_ORIGIN=https://brief.rayer.idv.tw`
- `AUTH_RESOURCE=https://brief.rayer.idv.tw/mcp`
- Callback `https://brief.rayer.idv.tw/auth/callback`
- `AUTH_URL=https://auth.dev.rayer.idv.tw`
- `AUTH_ENVIRONMENT=dev`
- `AUTH_VERIFIER_AUDIENCE=https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app`

The generated Brief Cloud Run hostname is not the OAuth resource or callback origin. The actual Brief runtime service-account email remains a deployment readback input; until then the Auth fixed resource remains disabled. No DNS, IAM, provider, or credential change is part of this contract.

Brief owns and validates these environment values; LWC Auth owns the scope/resource registry and the verifier. There is no shared config file or config distribution. Use the exact names above in `service/src/config.mjs`, tests, and runtime setup:

- `APP_ENV`: `local` or `production` only.
- `AUTH_MODE`: `local-test` only for isolated local fixtures; `shared` for Cloud Run.
- `AUTH_URL`, `AUTH_ENVIRONMENT`, `AUTH_RESOURCE`, and `AUTH_VERIFIER_AUDIENCE`: exact values above for the first cloud deployment.
- `AUTH_REQUIRED_SCOPE=brief:manage`.
- `AUTH_WEB_CLIENT_ID`: exact public Brief Web client registered in Auth DEV.
- `AUTH_VERIFIER_CALLER_EMAIL`: exact Brief Cloud Run runtime service-account email, pending readback.
- `AUTH_VERIFIER_MODULE`: retain the existing injectable adapter module.

The valid initial cloud pairing is `APP_ENV=production`, `AUTH_MODE=shared`, and `AUTH_ENVIRONMENT=dev`; do not infer `prod` from the process mode. Shared wire environment values are lowercase `dev` or `prod`; never send `DEV`, `Prod`, or `development`. Local-only fixtures can use `local` only against local Auth and are not cloud acceptance.

## Adapter and verifier request

Preserve the current `verifyCredential({credentialClass, credential, expectedResource, environment})` module interface and its principal output fields: `userId`, `tenantId`, `capabilities`, `resource`, `environment`, and `credentialClass`.

1. The Brief Web API route extracts only the dedicated access-token cookie and invokes the adapter with `credentialClass=web-session`. Never forward the whole Cookie header.
2. The Brief MCP `/mcp` route accepts only `Authorization: Bearer <Auth access token>` and invokes the adapter with `credentialClass=mcp-bearer`. Reject a session cookie on MCP and bearer credentials on Web API routes.
3. The class comes from the server-selected route. It is not client evidence and must not be copied to an Auth request header. In particular, do not send or trust `X-LWC-Credential-Class`.
4. The Brief server obtains a short-lived Google-signed OIDC ID token for the Auth Cloud Run audience `AUTH_VERIFIER_AUDIENCE` using the Cloud Run metadata identity endpoint or supported Google auth library. Browser and native MCP clients never receive or supply this caller token.
5. Call `POST ${AUTH_URL}/api/v1/auth/consumers/daily-brief/verify-credential` with:

       Authorization: Bearer <Brief runtime OIDC ID token>
       X-LWC-User-Credential: <opaque Auth OAuth access token>
       Accept: application/json

   Do not put either token in a URL or request body, do not follow cross-origin redirects, redact both credential headers from logs/traces, and require `Cache-Control: no-store`.
6. Require HTTP 200 JSON containing `user_id`, `client_id`, actual `scopes`, actual `resource`, canonical `environment`, and `expires_at`. These OAuth facts come from the Auth-verified grant; never send a client ID, user ID, tenant, scope, resource, environment, or credential class as proof in the request.
7. Validate the returned principal against Brief's configured `AUTH_RESOURCE`, `AUTH_ENVIRONMENT`, and `AUTH_REQUIRED_SCOPE` before any data/tool operation. A valid OAuth token may belong to another resource or lack `brief:manage`; Auth returns its actual attributes and Brief rejects that principal before service execution.
8. Classify exclusively using the server route plus the verified `client_id` returned by Auth:
   - `web-session`: require `client_id === AUTH_WEB_CLIENT_ID`.
   - `mcp-bearer`: require `client_id !== AUTH_WEB_CLIENT_ID` and an active grant verified by Auth; step 7 then requires its returned actual resource, scope, and environment to match Brief configuration.
   Thus a Web access token copied into MCP Authorization is rejected, and a native MCP token copied into the Web cookie is rejected. Changing its transport or server-selected class cannot reclassify an Auth grant.
9. Map accepted `user_id` to the account's single private Brief tenant using existing tenant hashing. Map `brief:manage` to Brief's existing per-operation capabilities; preserve the current smaller MCP operation set and current Web operation set. Never trust caller-supplied tenant/storage path.
10. Return the existing principal shape. Auth 401 maps to unauthenticated; verified resource/scope/environment/client-class/capability mismatch maps to forbidden; caller-auth or malformed-response errors fail closed; timeout, network failure, 429, or 503 maps to service unavailable. Do not convert verifier failure into anonymous success or cache successful verification; a revoke must affect the next request.

Auth itself validates the runtime ID token's signature, issuer, expiry, exact audience, verified email, and exact configured caller service-account email. It validates the user token, active grant, active account, current auth version, expiry, revocation, and that the credential belongs to the current Auth environment. It returns the grant's actual client ID, scopes, resource, and canonical Auth environment; it does not return Brief tenant/capabilities or claim a credential class. It does not apply Brief's required resource/scope policy on Brief's behalf.

Success response example for a valid Brief grant:

    HTTP 200
    Cache-Control: no-store
    Content-Type: application/json

    {
      "user_id": "stable Auth account subject",
      "client_id": "ID read from the verified Auth grant",
      "scopes": ["brief:manage"],
      "resource": "https://brief.rayer.idv.tw/mcp",
      "environment": "dev",
      "expires_at": "RFC3339 timestamp"
    }

For a different valid grant, Auth returns that grant's true scope/resource; Brief rejects it. No error response may include a credential, signing material, email, grant record, or Firestore details.

## Web OAuth and session handling

Register one public Web OAuth client at `POST ${AUTH_URL}/api/v1/auth/mcp/oauth/register` with `token_endpoint_auth_method=none`, authorization-code and refresh-token grants, code response type, exact callback `https://brief.rayer.idv.tw/auth/callback`, and `brief:manage`. Store the returned client ID as `AUTH_WEB_CLIENT_ID`. Native MCP clients use separate dynamic OAuth client IDs.

Login uses Auth discovery and the existing authorize endpoint with that Web client ID, exact callback, one-time state, PKCE S256, `resource=https://brief.rayer.idv.tw/mcp`, and explicit `scope=brief:manage`. At the callback, consume state once and exchange the code server-side at the existing Auth token endpoint with the exact client ID, callback, code verifier, and resource.

Store access and refresh tokens only in host-only `Secure`, `HttpOnly`, `SameSite=Lax` cookies with explicit expiry; JavaScript never sees them. Keep the access cookie path `/api/v1` and refresh cookie path `/api/v1/auth/session/refresh`. Refresh rotates cookies on success and clears them on `invalid_grant`; logout clears both. Auth checks the stored grant and current account on refresh. Users revoke the underlying Brief grant from the LWC account grant panel described in `spec-r2.md`; no Brief-local revocation substitute is needed.

## Native MCP

Serve protected-resource metadata from Brief with exact resource `https://brief.rayer.idv.tw/mcp`, the configured Auth authorization server URL, and `brief:manage`. Native clients use their own registered OAuth client and PKCE. MCP `/mcp` accepts only an OAuth bearer token, then verifies that the Auth-returned client ID is not the Web client ID and that actual resource, scope, and `dev` environment match Brief configuration before tool dispatch. Keep authorization of each of the six existing MCP tools at the Brief boundary.

## Consumer files and tests

Implement in the Daily Brief workspace only after the r2 spec is frozen:

- `service/src/config.mjs`: preserve `APP_ENV=local|production`; add explicit shared Auth URL/environment/resource/scope, Web client, verifier audience, caller email, and strict pair validation. Allow `APP_ENV=production` with explicit `AUTH_ENVIRONMENT=dev`.
- `service/src/auth.mjs`: preserve the adapter seam; call Auth using runtime OIDC identity and user credential separately; validate response fields; compare verified client ID to `AUTH_WEB_CLIENT_ID` in opposite directions for Web and MCP; check resource/scope/environment; map user to private tenant and capabilities; fail closed.
- `service/src/http.mjs`: state/PKCE callback, exact canonical callback, cookie-only Web extraction, refresh/logout, protected-resource metadata, and error mapping.
- `service/src/mcp.mjs`: bearer-only `mcp-bearer` class; retain existing six tools and per-operation authority.
- Focus tests in `service/test/auth.test.mjs`, `config.test.mjs`, `http.test.mjs`, `mcp.test.mjs`, and `emulator.test.mjs`.

Required negative controls: verified Web-client access token presented as MCP bearer; verified native-client token presented as Web cookie; mismatched resource/scope/environment; Auth caller ID token with wrong issuer/audience/email/expiry; revoked/expired token or grant; inactive account/stale auth version; malformed response; timeout/network/429/503; bad callback state; refresh after revoke; cookie on MCP; bearer on Web; caller-supplied tenant. Each negative request fails before reads/writes or tool dispatch. For valid Web and native credentials, verify one account-private tenant and current separate Web/MCP operation caps. Exercise a local Auth runtime against the actual Firestore emulator and fake Brief storage for account/grant/revoke behavior. Local tests do not establish Cloud Run identity or DEV acceptance. All tests and acceptance are NOT RUN for this handoff.
