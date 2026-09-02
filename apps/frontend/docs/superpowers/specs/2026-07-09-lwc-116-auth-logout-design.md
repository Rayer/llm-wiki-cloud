# LWC-116 Auth Logout Semantics Design

## Goal

Force logout only on real authorization failure. Page reload, dead links, network
errors, and non-401 API failures must not clear the session.

## Problem

Users are kicked out after reloads or refresh failures. Investigation found:

1. `accessTokenOrRefresh()` calls `onUnauthorized()` whenever refresh returns
   null (not only on 401).
2. `refreshAccessToken()` calls `clearSession()` on any error (network, 5xx,
   parse failures) and when the refresh payload lacks a user.
3. Access token lives only in React state, so every reload must refresh and is
   exposed to (2).

Note: `apiFetch` already returns non-401 responses without logout. The ticket
title ("any non-200") is imprecise; the real issue is refresh-failure semantics
plus missing access-token persistence.

## Scope

In scope:

- `src/lib/auth.tsx` — refresh failure classification, session clear rules,
  access-token persistence.
- `src/lib/api.ts` — stop logging out from `accessTokenOrRefresh`; keep 401-only
  logout in `apiFetch`.
- Tests covering logout vs non-logout paths.

Out of scope:

- BFF refresh API changes.
- New session-expired UI / toast.
- Storing refresh tokens in localStorage (refresh remains cookie-based).
- Auth library migration or full session redesign.

## Approach

**Approach B — fix session-invalid semantics + persist access token.**

| Change | Purpose |
|--------|---------|
| Classify refresh failures | Only auth failures clear the session |
| Remove `onUnauthorized` from `accessTokenOrRefresh` | Failed refresh throws; does not logout |
| Keep `apiFetch` 401 + failed refresh → logout | Real unauthorized path stays |
| Persist access token in localStorage | Fewer forced refreshes on reload |

## Logout Rules

| Situation | Logout? |
|-----------|---------|
| Business API 404 / 500 / network error | No |
| Refresh network error / 5xx / non-auth failure | No |
| Refresh HTTP 401 (or equivalent invalid session) | Yes |
| Business API 401 and refresh fails as auth failure | Yes |
| User clicks Logout | Yes |
| Hard reload with valid refresh cookie | No — restore session |
| Hard reload with persisted access token | Prefer token first; soft-refresh in background |

## Component Design

### `refreshAccessToken` (`auth.tsx`)

Responsibilities:

- POST `/api/v1/auth/refresh` with credentials (cookie).
- On success: update access token (state + localStorage), update user if
  present, return the new access token.
- On **auth failure** (HTTP 401): `clearSession()`, return null.
- On **other failure** (network, 5xx, malformed body, non-401 HTTP error):
  return null **without** clearing the session.

`postAuth` currently throws on any non-OK response. Implementation may either:

- Specialize refresh to inspect `response.status` before treating as auth
  failure, or
- Extend the auth POST helper so callers can distinguish 401 from other errors.

Requirement: callers can tell "session is dead" from "request failed".

If the refresh response is OK but lacks a valid user:

- Keep the new access token if present (session is still valid).
- Do not clear the existing user solely because user was omitted.
- Only clear session when the server indicates unauthorized (401 path above).

### Access token persistence

- Storage key: `llm-wiki-access-token` (same naming style as
  `llm-wiki-last-project`).
- Write on successful login, register, and refresh.
- Read on provider mount into state/ref before or as part of hydrate.
- Clear in `clearSession` and logout.
- `getAccessToken` for the API layer: return in-memory token; after hydrate
  wiring, memory is seeded from localStorage so API calls see it.

Security note: access token is short-lived JWT-style bearer already used in
memory; localStorage matches the project’s existing client-side project id
storage pattern. Refresh token stays HTTP-only cookie.

### Hydrate on load

1. Read access token from localStorage into state/ref if present.
2. Soft-refresh via cookie to rotate token and load user.
3. If soft-refresh is an auth failure → clear session (including storage).
4. If soft-refresh is a non-auth failure → keep any restored access token;
   mark hydrated. UI may show errors on subsequent data fetches without
   forced login redirect from this path alone.
5. If no storage token and soft-refresh auth-fails → unauthenticated (current
   expected outcome).

### `accessTokenOrRefresh` (`api.ts`)

```ts
// Intended behavior
const current = apiAuthConfig.getAccessToken();
if (current) return current;

const refreshed = await apiAuthConfig.refreshAccessToken();
if (refreshed) return refreshed;

// Do NOT call onUnauthorized() here
throw new Error('Authentication required');
```

### `apiFetch` (`api.ts`)

Unchanged policy:

- Non-401 responses: return as-is (no logout).
- 401: try refresh once.
- Refresh succeeds: retry request with new token.
- Refresh fails (returns null): call `onUnauthorized()`, return original 401
  response.

Combined with the new refresh classification, `onUnauthorized` runs only when
refresh itself treated the session as dead (or refresh returned null after an
auth failure that already cleared). Non-auth refresh failure leaves session
intact; the request fails without forced logout.

## Error Handling

- Non-auth refresh failure: current request throws or surfaces existing
  ErrorState; session remains.
- No new global toast or dedicated “session expired” surface in this change.
- Explicit logout still calls logout endpoint then `clearSession`.

## Testing

Add or extend unit tests (node:test style used in this repo):

1. **`accessTokenOrRefresh`**: no token + refresh null → throws; does **not**
   call `onUnauthorized`.
2. **`apiFetch`**: response 404 → does not call `onUnauthorized`.
3. **`apiFetch`**: response 401 + refresh null → calls `onUnauthorized`.
4. **`apiFetch`**: response 401 + refresh returns token → retries and does not
   logout.
5. **`refreshAccessToken` / auth layer**: HTTP 401 → session cleared and
   storage cleared.
6. **`refreshAccessToken` / auth layer**: HTTP 500 or network error → session
   and storage **not** cleared.
7. **Persistence**: successful auth writes storage; `clearSession` / logout
   removes it; mount can restore token from storage.

Prefer exporting small pure helpers if that makes auth classification testable
without mounting React (e.g. “is auth failure status”, storage get/set/clear).
Do not require browser E2E for this ticket.

## Success Criteria

- [ ] Business 404/500/network failure does not logout.
- [ ] Refresh 5xx/network failure does not logout.
- [ ] Refresh 401 clears session and storage.
- [ ] Business 401 + successful refresh retries silently.
- [ ] Hard reload with valid cookie keeps the user signed in when refresh works.
- [ ] Hard reload with persisted access token does not immediately force logout
      when soft-refresh has a transient non-auth failure.
- [ ] Automated tests cover the cases above.

## Implementation Notes

- Touch only auth/API client and tests unless a tiny export is needed for
  testability.
- Follow existing code style in `auth.tsx` / `api.ts`.
- Feature branch / git worktree at implementation time
  (e.g. `fix/lwc-116-auth-logout`); this spec may land on `develop/1.0`.
