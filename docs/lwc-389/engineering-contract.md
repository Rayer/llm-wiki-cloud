# LWC-389 implementation contract

Revision: mcp-project-oauth-r1 (frozen / Submitted)
Base: 7713976360fe3573c7e36d56ceb2d31daf2751a5
Owner: LWC-389 dispatched worker; parent retains merge and formal DEV ownership.

## Canonical resources and endpoints

The issuer is the configured AuthServiceURL with trailing slashes removed. It
is never derived from the incoming Host header. The canonical MCP resource
origin is one configured value projected to Auth as SyncServiceURL and to BFF
as the optional `mcp_resource_origin` runtime field:

- Local: http://localhost:<BFF_PORT>
- Development: https://llm-wiki-bff-dev-580854833715.asia-east1.run.app
- Production: https://llm-wiki-bff-580854833715.asia-east1.run.app

The resource identifier is exactly
{mcp_resource_origin}/mcp/projects/{projectID}; projectID must be one valid
path segment. Auth accepts only the resource origin for the active environment
and a project resource path. BFF independently compares the token resource to
its configured origin plus the requested path. Project authority comes from
the resource and persisted grant together; X-Project-ID is optional and, when
present, must match. The legacy /mcp Project-key endpoint remains unchanged.

Auth endpoints:

- GET /.well-known/oauth-authorization-server
- POST /api/v1/auth/mcp/oauth/register
- GET /api/v1/auth/mcp/oauth/authorize
- POST /api/v1/auth/mcp/oauth/token
- GET /api/v1/auth/mcp/oauth/consent/{transactionID}
- POST /api/v1/auth/mcp/oauth/consent/{transactionID}/decision
- GET /api/v1/auth/mcp/oauth/grants?project_id={projectID}
- DELETE /api/v1/auth/mcp/oauth/grants/{grantID}

The public DCR, authorize, and token endpoints use the existing per-route
limiter (10 requests per minute). Auth CORS permits DELETE for browser grant
revocation.

BFF resource endpoints:

- GET, POST, DELETE /mcp/projects/{projectID}
- GET /.well-known/oauth-protected-resource/mcp/projects/{projectID}

The resource metadata uses the exact resource identifier, the configured Auth
issuer, the mcp:query scope, and the header bearer method. Unauthenticated
resource requests return 401 with a WWW-Authenticate Bearer challenge pointing
at that metadata URL. Metadata is derived from canonical config, never the
request Host.

AS metadata advertises authorization_code and refresh_token, S256 only, public
client authentication method none, the DCR endpoint, the single mcp:query
scope, and the exact configured issuer. No CIMD or device-code flow is added.
The native Hermes profile will explicitly set oauth.cimd: false to exercise
the frozen DCR contract.

## OAuth request rules

DCR accepts public clients only. It stores a trimmed client_name for display
as self-reported text and an exact list of redirect URIs. It returns no client
secret. HTTP redirects are allowed only for localhost, 127.0.0.1, or [::1]
with an explicit valid port; HTTPS redirects are fixed exact URIs. All schemes
other than HTTP/HTTPS, wildcards, userinfo, fragments, and malformed paths
are rejected. Authorize and token requests must use an exact registered URI.
Hermes 0.21.6 uses a loopback callback of the form
http://127.0.0.1:<ephemeral-port>/callback; the registration allowlist must
accept its ephemeral port without weakening exact matching later.

Authorization accepts only response_type=code, a registered client, a
registered redirect URI, a valid S256 code challenge, a non-empty state, the
single canonical resource URI, and mcp:query (or an omitted scope that
defaults to mcp:query). It creates a pending server record and redirects the
browser to the frontend internal route /oauth/consent?transaction_id=<opaque
transaction ID>. The transaction ID is a lookup key, not login or OAuth
authorization. No state-changing decision occurs on GET, login, or page load.
The original client state is echoed exactly to the registered callback only
after an explicit decision.

Consent detail and decision APIs require the existing active Web account JWT
and WebOnly guard. Detail shows the authenticated account, the actual Project
name, the client self-reported label, and query-only scope. The project ID is
read from the pending request, never a browser field. Approval rechecks
account/auth version and current Project ownership in the transaction that
creates the short-lived code. Denial/cancel marks the request denied and
redirects with access_denied and the original state; no grant is created.
Expired, denied, consumed, and missing transactions have distinct server
states and a frontend route back to Advanced to start the native client again.

The code exchange is bound to client, exact redirect URI, resource, project,
scope, S256 challenge, and verifier. Failed PKCE, client, redirect, resource,
scope, or Project checks never produce tokens. A Firestore transaction marks
the code redeemed and creates one grant/token family atomically; concurrent
code exchanges yield exactly one grant, and replay is invalid_grant.

## Lifetimes and durable authority

- Pending browser transaction: 10 minutes.
- Authorization code: 5 minutes, single-use.
- Access token: 10 minutes.
- Refresh-token family: 30 days absolute, not extended by rotation.
- Concurrent refresh retry grace: 30 seconds for the immediately previous
  token only, while its exact successor remains current.

Records live in environment-scoped Firestore collections resolved through the
existing local scope helper. Collection names include a stable environment
hash and every record also stores the exact environment. The layout is:

- clients: client ID, self-reported name, exact redirects, created time.
- transactions: client, redirect, state, challenge, resource, project, scope,
  status, timestamps, and after consent the user/auth-version.
- codes: hash-keyed code, all bindings, expiry, and redeemed state.
- grants: grant ID, user/auth-version, project, client, resource, query-only
  scope, status, family expiry, creation, and revocation time.
- access tokens: hash-keyed access token, grant ID, expiry, and bindings.
- refresh tokens: hash-keyed refresh token, grant ID, generation, status,
  expiry, predecessor/successor hashes, retry deadline, and encrypted retry
  token pair when needed for a same-token concurrent retry.

Opaque code, access, and refresh secrets are returned only at their protocol
boundary and never logged or persisted in cleartext. Refresh retry ciphertext
uses AES-GCM with a key derived from the configured Auth JWT secret and
environment. A concurrent request with the immediately previous refresh token
inside the grace window returns the same successor pair without rotating
again. An older or out-of-window replay revokes the grant family in the same
transaction. Each new generation invalidates the prior retry grace.

Every BFF request looks up the access-token hash, checks expiry, grant state,
environment, resource, scope, account active status and auth version, and
current Project ownership before entering the existing stateless MCP SDK
adapter. Revoke is a Firestore transaction; because BFF reads the grant on
every request, the next request fails without executor entry. Account and
project lookups reuse existing authority and the Firestore project document
decoder. OAuth tokens are opaque and are not accepted by account/CLI JWT
middleware or by the legacy Project-key adapter.

## Frontend and owned file manifest

The implementation adds a project-scoped Advanced page with environment and
resource URL, a no-secret Hermes OAuth config snippet, copy feedback, and a
native Hermes test command. It lists current Project grants, scope/status,
and revoke/readback controls. Project keys remain available there as the
existing manual fallback. Nothing writes a Hermes config file or claims that
copying or a web grant created a successful client connection.

Owned files: MCP Auth authority/routes and its tests under apps/bff/internal/auth
and apps/bff/cmd/auth; MCP BFF auth adapter/routes and tests under
apps/bff/internal/handler/v1 and apps/bff/cmd/bff; required optional
mcp_resource_origin fields and tests in existing Auth/BFF runtime config
projection; new Advanced and consent routes/components and an
apps/frontend/src/lib/mcp-oauth.ts client; only MCPOAuth keys in en.json and
zh-TW.json.

Shared hunks: apps/frontend/src/app/(workspace)/profile/page.tsx only moves
ProjectKeysSection rendering to Advanced and adds a clear Advanced entry;
ProjectProfilePanel, ProjectHeading, and surrounding Profile structure remain
untouched. apps/frontend/src/components/Shell.tsx adds /advanced and /cli after
/profile for non-demo users, with the CLI label from CLILane.nav. The /cli
route and locale keys are CLI-lane owned and may be integrated only from the
coordinator-provided immutable CLI commit, with its route/key provenance
checked before touching the shared Shell link. The authorized Google
return-to hunk is limited to
apps/frontend/src/lib/google-auth.ts startGoogleLogin() and
apps/frontend/src/components/GoogleCompletionClient.tsx: save only a validated
same-tab /oauth/consent?transaction_id=... target, then read/remove it after
successful login and fall back to /. No Google provider configuration,
credential, link confirmation, cancellation, JIT signal, or login platform is
changed.

The consent route re-reads transaction details after navigation. Changing
account or Project clears in-flight UI state; navigating to consent never
approves. Denial/cancel and expiry are explicit and return to Advanced/native
Hermes to retry. Localized text uses only the MCPOAuth namespace in both
supported locales, with labeled controls, keyboard-visible focus, and mobile
layout.

## Verification sources

- Frozen contract: /Users/rayer/.hermes/workflows/lwc-local/discussions/ux-lanes/mcp-oauth-contract-r1.md
- Freeze disposition: /Users/rayer/.hermes/workflows/lwc-local/discussions/ux-lanes/mcp-389-freeze-disposition.md
- Installed client: Hermes Agent v0.21.6+121.g99a45ec; isolated profile only.
- Hermes product-local spike is protocol evidence only, never product acceptance.
- RFC 9728: https://www.rfc-editor.org/rfc/rfc9728.html
- RFC 8414: https://www.rfc-editor.org/info/rfc8414/
- RFC 8707: https://www.rfc-editor.org/rfc/rfc8707.html
- Canonical repository gate: make verify (README and root Makefile).
