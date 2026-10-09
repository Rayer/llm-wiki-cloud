# LWC-375 → LWC-377 contract errata

These four reviewer corrections are accepted and refine `spec.md` for project-key implementation. They do not change Owner-fixed policy: exact key format/hash, one current user/project, no fixed expiry, individual revoke, and the fixed Query-only capability remain as specified.

1. **Preserve public Auth routes.** A project key is rejected by every other protected Auth/BFF route through the existing JWT-only boundary. Do not add a blanket Authorization-header rejection or alter the existing public `/api/v1/auth/login`, `/register`, `/refresh`, and `/logout` behavior. `GET /api/v1/query/config` remains public as today.
2. **Management account lookup errors are unavailable, not invalid credentials.** On the new narrow Web-only key-management routes, an otherwise valid Web JWT whose current account lookup fails must return `503` and perform no mutation. Invalid/missing JWT stays `401`; valid CLI JWT stays `403` after its existing session/account checks. Other Web/CLI routes keep their existing behavior, including Web `AccountAuth`'s current handling.
3. **Absent readback is inconclusive after an ambiguous create.** If create returned an ambiguous storage error and readback currently finds no record, return `503` with the safe `key_id`, no secret, and no retry of the original write. Do not claim the key was permanently not created or unusable: a delayed commit may become visible later. The owner reloads the list and revokes the key if it later appears; if it remains absent, the owner may start a fresh create.
4. **Keep review identities scoped accurately.** Independent review `dfeb023d714ebe33a9a61d09` is PASS only for the earlier Spike/design scope. Review `cb79f1ab7e71989471d0bf9e` is PASS for this concrete LWC-375 project-key contract. Neither result is product implementation, test, or live acceptance; the LWC-377 report must preserve this distinction.

## Implementation consequences

- Keep Auth endpoints unchanged; only project-key authentication support is added to the narrow `POST /api/v1/query` BFF route.
- Do not attach ordinary `hV1.AccountAuth(cfg)` unchanged to management routes if it collapses Web account lookup errors to `401`. Reuse JWT parsing/session checks through a narrow auth variant that preserves `401` for invalid credentials and maps account lookup unavailability to `503` only for these new management routes; then require Web-only and current project ownership before any read/write.
- On ambiguous create errors, include only a non-secret `key_id` correlation value in the safe `503` body and perform one readback. Matching active record permits the original in-memory secret to be returned once; absent/mismatched/unavailable readback never does. Never replay the ambiguous create.
- LWC-377 report records both review IDs and exact scopes, the actual tests run, failures/limitations, and must not say `verified` or `prod ready` without the corresponding evidence.
