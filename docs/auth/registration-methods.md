# LWC-324 registration methods

`email_registration_enabled` and `google_registration_enabled` independently control **new accounts**. They are booleans in Firestore `system/settings`, Admin settings GET/PATCH, and anonymous `GET /api/v1/public/config`.

| Email | Google | New accounts allowed |
| --- | --- | --- |
| false | false | Neither |
| true | false | Email/password only |
| false | true | Google only |
| true | true | Both |

Existing email/password login and login by an already linked Google issuer/subject remain available in every combination, including settings read failures. The login screen keeps one **Continue with Google** button. Its email signup button follows only the email capability.

Explicit Google linking is an authenticated Account Settings operation, not new onboarding. It retains the existing current-password proof, fresh chooser, ownership checks, and confirmation. Signup settings are not consulted. A matching email never automatically links identities.

## Resolution and migration

Each method resolves independently, using one settings-document snapshot:

1. Its explicit boolean field wins.
2. If the method field is absent, inherit the legacy document `registration_enabled` boolean. A present document with a missing or malformed legacy value is closed.
3. If the document does not exist, inherit `REGISTRATION_ENABLED`. Invalid nonempty environment values close both methods.
4. **Pending product decision:** this branch currently retains the repository's existing `true` default when both document and environment are absent. No change to this branch is authorized until that policy is confirmed.

A present but malformed method field is closed; it never falls back to an open legacy value. Firestore read/transaction errors do not use environment fallback: public/Admin reads fail, new registration fails before identity provisioning, and the frontend closes signup capabilities. The anonymous API contains only registration capabilities and published announcement content/digest.

Admin PATCH accepts either one or both method fields. The transaction reads the existing posture and writes both resolved method fields plus the conservative legacy value (`email && google`), preserving announcement fields. Thus a partial update to a legacy closed installation does not reopen the untouched method. Admin GET and public config expose both method booleans and the legacy AND value.

For compatibility, a PATCH containing only `registration_enabled` explicitly sets both methods. Mixing legacy and method fields, unknown fields, empty changes, nonboolean values, or trailing JSON is rejected. Existing JWT/Admin middleware protects writes.

**Mixed-version/rollback limitation:** once method fields are stored, a legacy binary or direct legacy-field-only Firestore write cannot override them in the new binary. In particular, writing only `registration_enabled=false` outside the new API does not close explicit method fields. Use the new Admin controls/API to close both methods before any rollback. An old binary reads the persisted AND value conservatively, so a mixed-method configuration becomes closed for both methods there. Avoid concurrent old/new Admin writers during rollout. No migration, rollback, deployment, or environment mutation was performed by this change.

## Verification

Causal RED/GREEN:

- The four-combination Admin/public test failed on the baseline: every new-method PATCH returned 400. It passes with method persistence and capabilities implemented.
- The frontend public-capabilities tests failed on the baseline because method values were undefined. They now cover all combinations, legacy fallback, malformed fields, HTTP/JSON/network failure, and pass.

Ordinary tests use in-memory repositories/configuration. Emulator tests require explicit `FIRESTORE_EMULATOR_HOST`; the new persistence test additionally requires loopback and uses `WithoutAuthentication`. Local emulator coverage checks atomic partial migration/reload, Google callback combinations, rejected onboarding with no user/project/identity/reservation/refresh session, existing Google login despite config failure, and explicit linking despite unavailable signup settings. Failed OAuth attempts may still consume the OAuth transaction and store their existing one-time failure result; they do not issue an authenticated session.

Verified locally: `make test` passed (Go race suite, 515 frontend Node tests, 208 component tests); `make lint`, `make typecheck`, and `make build` passed. Focused emulator registration/linking tests passed with the race detector. A final focused settings race run also covers strict partial-update null rejection.

Commands:

```sh
make test
make lint
make typecheck
make build
# Separate, explicitly local emulator verification:
FIRESTORE_EMULATOR_HOST=127.0.0.1:8944 go -C apps/bff test ./internal/syssettings ./internal/auth ./internal/config -count=1 -race
```

The frontend component tests cover all combinations, existing login actions, fail-closed loading/reopening, independent Admin writes, reload persistence, and save failure. LWC-149/150 registration behavior and LWC-254/317 identity/linking regressions remain covered.

Live DEV acceptance remains outstanding because deployment and real account/data operations are not authorized. After the default policy is resolved and the parent completes final review, an authorized DEV acceptance should verify Admin persistence plus each matrix row with new email/Google accounts and existing login/linking. This change does not include account suspension (LWC-314), deployment, production/main changes, or merge.
