# LWC-375 hermes-key-contract-r1

Status: Owner-directed product contract and implementation handoff for LWC-377 under Owner decisions 4-2091, 4-2092, and 4-2095. This dispatch is the LWC-375 contract deliverable in the four-vote implementation sequence arranged by the orchestrator. It resolves engineering choices using the current BFF/Auth source seams; it does not claim product-code implementation or live acceptance.

## Product contract

- A project key is one opaque bearer credential bound at creation to exactly one current user and one stable project ID.
- v1 grants the single fixed capability `query`. There is no capability/scope selector, reference-detail tool, or other BFF access through a key. The Web create flow uses the workspace's current project context as the fixed target.
- Keys have no fixed expiry. An owner can revoke each key individually. Rotation is: create a new key, update the client, verify it, then revoke the old key. There is no refresh operation.
- Key access is checked against current account and project-owner authority on every request. A revoked key, suspended account, unavailable authority, or user who no longer owns the bound project is denied according to the status rules below.
- A project key is not a CLI session, access token, or refresh token. Existing `lwc-sync` pairing/session behavior stays separate.
- The full secret is returned once by successful creation. List, revoke, logs, errors, and later reads never return the secret or its hash.

## Credential format

Use this exact grammar (ASCII, case-sensitive):

```text
lwc_pk_<random-key-id>.<random-secret>
```

```regex
^lwc_pk_([0-9a-f]{32})\.([A-Za-z0-9_-]{43})$
```

- `random-key-id`: 16 bytes from `crypto/rand`, encoded as 32 lowercase hexadecimal characters. It is the Firestore document ID and public metadata ID.
- `random-secret`: 32 bytes from `crypto/rand`, encoded with unpadded Base64 URL encoding (`base64.RawURLEncoding`); the canonical encoding is exactly 43 characters. Reject non-canonical encodings, including invalid trailing bits.
- Total token length is exactly 83 ASCII characters: 7 prefix + 32 ID + 1 dot + 43 secret.
- Persist `secret_hash` as 64 lowercase hex characters: `hex(SHA-256(decoded 32-byte random-secret))`. Compare decoded secret digests with constant-time comparison. Do not persist the raw or encoded secret. The 256-bit random secret makes a plain SHA-256 verifier adequate; it does not require a new pepper or app setting.
- Accept the token only as `Authorization: Bearer <token>` over the existing HTTPS BFF origin. Never accept it in a URL, query parameter, cookie, or request body. Do not log the Authorization header or token.

Synthetic test fixture only (not a live credential):

```text
token:       lwc_pk_00112233445566778899aabbccddeeff.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8
secret_hash: 630dcd2966c4336691125448bbb25b4ff412a49c732db2c8abc1b8581bd710dd
```

The fixture secret decodes to bytes `0x00` through `0x1f` and is only for deterministic local tests.

## Durable record

Use the already configured BFF Firestore client and `internal/firestore.Collection(fs, "project_keys")` so local worktree scope is respected. Store one document at `project_keys/{key_id}` with exactly these application fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer `1` | Record schema; reject unsupported versions. |
| `key_id` | string | 32-character lowercase hex ID; must equal the document ID. |
| `user_id` | string | Authenticated user ID that owned the project when created. |
| `project_id` | string | Stable project ID at creation; never taken from a query request to establish authority. |
| `name` | string | Owner-provided display label, trimmed, 1–64 Unicode code points. |
| `secret_hash` | string | Lowercase 64-character SHA-256 hex digest described above. |
| `capabilities` | string array | Exactly `["query"]`; no client-supplied capability values. |
| `state` | string | `active` or `revoked`. Only `active` authenticates. |
| `created_at` | Firestore timestamp | Server-assigned creation time. |
| `revoked_at` | Firestore timestamp, optional | Server-assigned on first revoke; absent while active. |

Do not add `expires_at`, raw secret, refresh token, provider/IAM identity, scope list, or client-controlled state. Do not add a per-request `last_used_at` write in v1. Listing queries records by `user_id`, filters the requested `project_id` in the service, and returns deterministic `created_at` descending / `key_id` ascending order. Key lookup by ID is a direct document read. Existing Firestore single-field indexing is sufficient; no config or composite-index addition is needed.

`user_id` and `project_id` in this record are fixed attribution and the starting point for live authorization, not permanent grants. At each key use, load the current account, require `UserRecord.Active()`, and call the shared project owner authority (`auth.FirestoreProjectOwnerAuthorizer`) with the record's `user_id` and `project_id`. Fail closed on read/authority errors. Never resolve access from a caller-supplied project ID alone. No authority cache may outlive a request.

## HTTP interfaces

Management endpoints are BFF routes. They require current Web JWT authentication, an active account, `auth.WebOnly()`, and proof that the authenticated user currently owns the path project. CLI sessions and project keys cannot manage keys. A project mismatch or nonexistent/foreign key is hidden as `404` on management routes.

### List

```http
GET /api/v1/projects/{projectID}/keys
Authorization: Bearer <Web-JWT>
```

`200`:

```json
{"keys":[{"key_id":"00112233445566778899aabbccddeeff","name":"Research client","project_id":"project-a","capabilities":["query"],"state":"active","created_at":"2026-10-09T00:00:00Z"}]}
```

The list projection must omit `secret`, `secret_hash`, bearer tokens, and any credential-recovery material, including for revoked records.

### Create

```http
POST /api/v1/projects/{projectID}/keys
Authorization: Bearer <Web-JWT>
Content-Type: application/json

{"name":"Research client"}
```

The path project is the only target. The request cannot set owner, project ID, capabilities, state, expiration, or secret. `201` returns metadata and the newly generated full key exactly once:

```json
{"key":{"key_id":"00112233445566778899aabbccddeeff","name":"Research client","project_id":"project-a","capabilities":["query"],"state":"active","created_at":"2026-10-09T00:00:00Z"},"secret":"lwc_pk_00112233445566778899aabbccddeeff.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"}
```

The UI shows the `secret` only in the immediate creation result, with an explicit copy action and one-time warning. Closing/dismissing that result does not make the key retrievable. If the browser loses the successful response, the owner revokes that listed key and creates a replacement.

### Revoke

```http
POST /api/v1/projects/{projectID}/keys/{keyID}/revoke
Authorization: Bearer <Web-JWT>
```

`200` returns only public metadata with `state: "revoked"` and `revoked_at`. Revoke is idempotent for the same owner and project. It is an atomic state transition; never delete the record or allow reactivation. An already-authorized in-flight query may finish; requests whose authorization reads observe the committed revoke must fail.

### Query with a project key

Keep the existing route and payload:

```http
POST /api/v1/query
Authorization: Bearer lwc_pk_<random-key-id>.<random-secret>
Content-Type: application/json
X-Project-ID: project-a   # optional for project keys; if supplied it must match

{"q":"coffee","mode":"wiki"}
```

The project-key auth branch must set request context `userID` and `projectID` from the verified record. If `X-Project-ID` is absent, use the bound ID. If present but unequal, return `403` before the query executor runs. Existing query parsing, response, and error behavior remain in `internal/handler/v1/endpoints.go` and `query_profile.go`; this key only reaches that query operation. Normal Web/CLI JWT query access remains supported through the existing account path.

Route composition must move only `POST /api/v1/query` out of the broad `v1.Use(hV1.AccountAuth(cfg))` group and attach a narrow `Web/CLI JWT OR project-key` authenticator followed by project context validation. All other routes keep their existing account middleware. The API-key branch is not mounted on `GET /api/v1/query/config` (which is already public) or any other BFF route. Sending a project key to project listing, source/detail, profile, annotation, import, pipeline, write, auth, or admin routes returns `401` and executes no handler.

## Status and error contract

All error bodies use the existing JSON shape `{"error":"<stable safe message>"}`. Do not include tokens, digests, raw database errors, or account/project existence details where the table says `404`.

| Operation / condition | HTTP | Result |
| --- | ---: | --- |
| Query: missing, malformed, unknown, wrong-secret, revoked key | `401` | `invalid project key`; no query execution. |
| Query: active key but account suspended/inactive or project no longer owned | `403` | `project key access denied`; no query execution. |
| Query: supplied `X-Project-ID` differs from bound project | `403` | `project key access denied`; no query execution. |
| Query: account/project/key authority storage unavailable or malformed stored record | `503` | `project key authority unavailable`; fail closed. |
| Key used on any protected route other than `POST /api/v1/query` | `401` | Existing JWT auth rejects it; no handler access. |
| Management: valid active Web JWT, current owner | normal | List `200`, create `201`, revoke `200`. |
| Management: CLI token, invalid JSON/name/key ID, or missing Web JWT | `403` for CLI; `400` for invalid input; `401` for missing/invalid JWT | No mutation. |
| Management: requested project/key absent or owned by another user | `404` | `project key not found`; no cross-owner disclosure. |
| Management: Firestore write/read/authority unavailable | `503` | Safe error; no success-shaped partial result. |
| Existing query body validation / executor errors | existing `400`, `422`, or `500` | Preserve existing query contract after authorization. |

### Storage-write uncertainty

Creation generates the ID and secret in memory and calls Firestore `Create` once. Retry ID generation only for a definitive document-ID collision; never repeat a timed-out/ambiguous write. On a write error, read `project_keys/{key_id}`:

1. If the record exactly matches the generated `key_id`, owner, project, label, fixed capability, active state, and secret hash, treat the write as committed and return the single `201` response with the still-in-memory secret.
2. If the document is absent, return `503` without returning the secret; the key is not usable.
3. If the read itself fails or the record differs, return `503` with a safe `key_id` correlation field only; never return the secret and never retry that write. The UI reloads the list. If this key appears, revoke it because its secret was not delivered; if it does not appear, the owner may initiate a new create.

If a revoke write reports an error, read back the record: observed `revoked` is `200`; observed `active` or unavailable read is `503`, and the UI reloads before offering an explicit retry. Every query authorization re-reads key state so storage errors cannot become an allow.

## Web owner flow

Add a **Project API keys** section to the existing Account Settings modal. It is scoped to `useWorkspace().currentProject`, displayed as read-only project context; there is no project picker or capability selector. If no active project is selected, disable create and explain that a project must be selected in the workspace first. The owner can list keys for that one current project, create a label-only `query` key, copy the secret from the one-time result, dismiss it, and revoke listed active keys with confirmation. Refresh after create/revoke. Do not put a key in local storage, session storage, browser logs, analytics, URL, or app configuration.

The current workspace source is `apps/frontend/src/components/WorkspaceProvider.tsx` (`currentProject`) and the account modal is `apps/frontend/src/components/AccountSettingsModal.tsx`. The frontend helper belongs in `apps/frontend/src/lib/project-keys.ts`; it uses the normal BFF `apiFetch` Web JWT/project routing and the current project ID. Do not route this management API through `apps/frontend/src/lib/cli-auth.ts` or Auth service `/api/v1/auth/cli`.

## LWC-377 implementation map

| Concern | Existing source seam | Intended addition/change in 377 |
| --- | --- | --- |
| Current project ownership and project ID validation | `apps/bff/internal/auth/project.go` (`FirestoreProjectOwnerAuthorizer`, `ValidPathSegment`, project record parsing) | `apps/bff/internal/auth/project_key.go`: token grammar, generator, digest verifier, record/repository behavior, live account+owner verification. |
| Current account state | `apps/bff/internal/auth/account.go` (`UserRecord.Active`), `apps/bff/internal/auth/user_store.go` (`GetUser`) | Use the shared account lookup for every key request; do not invent account state. |
| JWT, Web-only and CLI distinction | `apps/bff/internal/auth/jwt.go` (`WebOnly`, `CLIOnly`, JWT middleware) | Narrow query-only combined auth middleware; do not change CLI session or refresh-token formats. |
| BFF routes and query handler | `apps/bff/cmd/bff/main.go` (`newProductionRouter`), `apps/bff/internal/handler/v1/endpoints.go` (`Query`), `query_profile.go` (`queryStore`) | `apps/bff/internal/handler/v1/project_keys.go`; add owner-only management routes and a separate query auth path. Keep the shared `Query` implementation. |
| Firestore local scope | `apps/bff/internal/firestore/scope.go` (`Collection`) | Store/read under `Collection(fs, "project_keys")`; no application config addition. |
| Existing Web project selection and BFF requests | `apps/frontend/src/components/WorkspaceProvider.tsx`, `apps/frontend/src/lib/api.ts` (`apiFetch`, `buildProjectHeaders`) | `apps/frontend/src/lib/project-keys.ts`; add the current-project section to `AccountSettingsModal.tsx` and localized messages. |
| Config-as-Code boundary | `deploy/cac/ssot.pkl` (`BffConfig`) and the current BFF generated config path | No new setting is needed: records are dynamic Firestore data, not deploy config. Never put a key/secret in Pkl, generated config, environment, or a secret manager. If a future technical requirement proves a static BFF setting necessary, follow existing SSOT → prepare → generated loader; do not add it speculatively here. |

## Verification matrix for 377

All rows below are required implementation checks. This contract-only task did not execute product tests; results are **NOT RUN** until 377 implements and records them.

| Area | Required positive case | Required negative/control cases | Result now |
| --- | --- | --- | --- |
| Format and crypto | Generate exact 83-character token; parse the synthetic fixture and match its SHA-256 digest | Bad prefix/case/ID length/dot/secret alphabet/secret length/non-canonical Base64; unknown ID; wrong secret | **NOT RUN** |
| Persistence and projection | Create active record with `schema_version=1`, fixed capability and server timestamp; list returns deterministic safe metadata | No raw/encoded secret or digest in read/list/revoke responses; malformed or unsupported record fails closed | **NOT RUN** |
| Query scope | Key calls existing `POST /api/v1/query`; omitted project header resolves to record project; matching header works | Other project header returns 403 before executor; another user's project, revoked key, inactive/suspended owner denied; storage/account/owner lookup failure returns 503 | **NOT RUN** |
| Route boundary | Existing Web and CLI JWT query remain functional | Key on projects, sources/details, profile, annotations, import, pipeline, write, Auth and admin paths is 401 and cannot reach their handlers; public query config remains public | **NOT RUN** |
| Owner management | Active owning Web user lists/creates/revokes only the selected project; revoked record stays listed; repeated revoke is safe | CLI token, absent/invalid JWT, other owner, invalid name/ID, foreign/missing project/key, Firestore errors | **NOT RUN** |
| Authority changes | Current owner with active account uses key | Account suspension and ownership loss deny; restore/reownership follows current source authority; key-document state/storage failures fail closed | **NOT RUN** |
| Uncertain writes | Simulate create error then exact matching readback and return one-time secret; revoke error then revoked readback returns success | Absent create readback returns 503/no secret; mismatched/unavailable readback returns 503/safe key ID/no retry; active revoke readback is not reported as success | **NOT RUN** |
| Web flow | Current-project create, one-time copy, refresh-safe list, confirmed revoke | No active project; create response lost; secret never restored from list/browser storage; unknown create state prompts list/revoke recovery | **NOT RUN** |
| Regression/config | Existing query response and Web/CLI JWT path retain their contracts; no new config fields or generated config changes | No deployment, live key issuance, external provider, paid query, or IAM change | **NOT RUN** |

## Acceptance for the document handoff

LWC-377 can implement from this contract without reopening the Owner-fixed product choices. The implementation must preserve the source seams above, the exact token/storage/interface contract, and all negative cases. Synthetic IDs/secrets/hashes are allowed in local tests; no real key needs to be issued to validate the cryptographic and repository paths.
