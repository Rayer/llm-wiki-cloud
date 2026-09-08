# LWC-324 registration master and methods

`registration_enabled` is the **new-account registration master switch**. `email_registration_enabled` and `google_registration_enabled` are independent saved method preferences in Firestore `system/settings` and Admin GET/PATCH. A method permits a new account only when **master AND preference** are true.

Admin shows the master above two checkboxes. Turning the master off grays and disables the method controls without clearing their checked states. Turning it on restores those selections. Both methods may be selected; leaving both unselected permits no new accounts even with the master on.

| Master | Saved email | Saved Google | Effective new accounts |
| --- | --- | --- | --- |
| false | any | any | Neither; preferences retained |
| true | false | false | Neither |
| true | true | false | Email/password only |
| true | false | true | Google only |
| true | true | true | Both |

Admin responses expose the master and saved preferences. Anonymous `GET /api/v1/public/config` exposes the master and **effective** method capabilities, alongside the existing published announcement content/digest. The frontend additionally masks public method fields with the master, including inconsistent responses that contain `master=false` and `method=true`.

Existing email/password login and login by an already linked Google issuer/subject remain available in every combination, including settings read failures. The login screen keeps one **Continue with Google** button. Its email signup button follows the effective email capability.

Explicit Google linking is an authenticated Account Settings operation, not new onboarding. It retains the existing current-password proof, fresh chooser, ownership checks, and confirmation. Signup settings are not consulted. A matching email never automatically links identities.

## Resolution and persistence

The master and preferences resolve from one settings-document snapshot:

1. When a document exists, its boolean `registration_enabled` is the master. A missing or malformed master closes registration, regardless of preferences or environment.
2. When the document does not exist, the master inherits `REGISTRATION_ENABLED`. Invalid nonempty environment values close registration. With neither document nor environment, the existing repository default remains `true`; production launch closure is an explicit environment posture, not a different global default.
3. A missing method preference defaults to `true`. This preserves legacy behavior: an existing `registration_enabled=false` installation remains effectively closed, while reopening its master restores both legacy methods. A present but malformed preference is `false` and never falls back to open.
4. Firestore read/transaction errors never fall back to environment values. Reads fail, and backend new-account registration fails before identity provisioning. The frontend closes public capabilities on HTTP, JSON, network, or missing-master errors.

Admin PATCH accepts any nonempty subset of the three boolean fields, including master and preferences together. A transaction preserves omitted values and writes the complete resolved state without changing announcement fields. Master-only updates, including legacy `registration_enabled` PATCHes, change only the master and retain preferences. Turning the master off never rewrites preferences to false; reopening evaluates the saved selections. A method preference may be changed through the API while the master is off, but remains ineffective until the master is on. The Admin UI disables those child controls while off.

Unknown fields, empty changes, nonboolean/null values, and trailing JSON are rejected. Existing JWT/Admin middleware protects writes.

**Rollback limitation:** legacy binaries honor the master but cannot enforce per-method restrictions when it is on. Set the master to false before a rollback to legacy code, and keep it off until method-aware code is restored. A legacy master-only write cannot erase saved preferences in the new implementation. No migration, rollback, deployment, or environment mutation was performed by this change.

## Verification

Causal RED/GREEN:

- Initial method tests failed on the baseline because method PATCHes returned 400 and frontend capabilities were undefined.
- Master-retention tests failed on the earlier split-only implementation because master toggles overwrote all four saved preference combinations.
- Public masking tests failed when a false master and true method were incorrectly normalized as open. The corrected implementation preserves the master and masks effective capabilities.

Ordinary tests use in-memory repositories/configuration. Emulator tests require explicit `FIRESTORE_EMULATOR_HOST`; the new persistence test additionally requires loopback and uses `WithoutAuthentication`. Local emulator coverage checks atomic partial migration, saved preference reload across master toggles, all eight master/preference combinations, rejected Google onboarding with no user/project/identity/reservation/refresh session, existing Google login despite config failure, and explicit linking despite unavailable signup settings. Failed OAuth attempts may still consume the OAuth transaction and store their existing one-time failure result; they do not issue an authenticated session.

The frontend tests cover raw Admin preferences versus public capabilities, all combinations, existing login actions, fail-closed loading/reopening, gray disabled child controls retaining checks, master reopen/reload, independent method writes, and save failure. LWC-149/150 registration behavior and LWC-254/317 identity/linking regressions remain covered.

Verified locally on 2026-09-08: `make test` passed (Go race suite, 517 frontend Node tests, 216 component tests); `make lint`, `make typecheck`, and `make build` passed. The full local emulator auth/settings/config suite also passed with the race detector. Swagger JSON/YAML semantics were checked for consistency.

```sh
make test
make lint
make typecheck
make build
# Separate, explicitly local emulator verification:
FIRESTORE_EMULATOR_HOST=127.0.0.1:8944 go -C apps/bff test ./internal/syssettings ./internal/auth ./internal/config -count=1 -race
```

Live DEV acceptance remains outstanding because deployment and real account/data operations are not authorized. After parent final review, an authorized DEV acceptance should verify Admin persistence, disabled retained selections, and each matrix row with new email/Google accounts plus existing login/linking. This change does not include account suspension (LWC-314), deployment, production/main changes, or merge.
