# Auth prior single-container name discrepancy: offline causal result and proposal

> **Superseded by Owner correction 3584-1787:** do not implement paired-name normalization or canonical fingerprints from this earlier proposal. The accepted correction uses the prior immutable image digest as rollback identity and does not require equality of the complete service template. See `deployment-engine-spec-r2-accepted-appendix-auth-image-rollback.md` for the bounded inventory and test impact. The exact-shape RED below remains diagnostic evidence only.

This is the offline-only follow-up for 3584-1783 / 3664-1784 / Parent direction 4-1785. No provider code, tests, plan, or manifest was changed. No live provider read/write, Actions operation, credential, IAM, Production, or Git publication was performed. Parent's bounded live observation remains the only live evidence; the synthetic result below does not claim to reproduce or repair the live resource.

## Exact-shape causal RED

`auth-revision-container-name-causal-red.txt` uses the production `Providers` adapter and existing test-only fake provider. It starts from the existing single-container Auth fixture, adds a name only to the revision response, and emits presence/equality booleans rather than resource fields. The result is: one container; the service template name is absent; revision name is present; each image equals the revision status digest; complete adapter configs become equal when only that name key is removed; `template_matches` is false; `snapshot('auth')` raises `unrepresentable-prior-service-template`; and prior `observe` is false.

The fixture confirms the decision point in current production code. It is a synthetic causal RED for the exact reported shape, not proof about how Cloud Run produced the live value. Parent's live shape separately records the actual Auth service/revision discrepancy and no mutation.

## Source trace

- `Providers.revision_config` preserves the complete revision spec and effective annotations, excluding only the existing controller/audit annotation list. `template_matches` compares the Service template with the revision using that full config (`deploy/engine/providers.py:656-671`).
- `snapshot('auth'/'bff')` first requires `template_matches`, then records distinct hashes for the current Service template and immutable revision (`providers.py:673-692`). The observed name-presence difference therefore blocks snapshot before runtime mutation.
- Prior and candidate `observe` both rely on `template_matches`; prior observe also compares both saved fingerprints (`providers.py:729-756`). Engine snapshot invokes the provider snapshot before any component deployment; rollback verifies the retained revision fingerprint, restores it, then polls prior observe (`deploy/engine/engine.py:394-401, 404-425`).
- Candidate deploy also uses `template_matches` to choose between restoring the full retained revision config and updating traffic (`providers.py:779-821`). `restore_service` sends the retained revision's full spec and effective annotations; `rollback` checks the exact revision fingerprint before calling it (`providers.py:844-872`). `revision_config` has no other call sites.

## Cached API reference and limit

The local Go module cache contains Google's generated Cloud Run API discovery schemas for `google.golang.org/api v0.280.0`. In both `run/v1/run-api.json` (`schemas.Container.properties.name`) and `run/v2/run-api.json` (`schemas.GoogleCloudRunV2Container.properties.name`), `name` is an optional string property and is not listed as required; the generated Go types use `json:"name,omitempty"`. This supports treating omission as a representable API shape. The cached schema does not establish why the observed Service and Revision responses differ or guarantee provider defaulting behavior; that remains the limit of this diagnosis.

## Minimal normalization proposal — disposition required before provider implementation

Keep `revision_config` exact and preserve its full spec/annotations, including container name, for the immutable revision fingerprint and rollback integrity. Add a narrow paired comparison/canonicalization only for a Service template and its named revision:

1. When both sides have one container and exactly one side omits `name`, use the other side's existing valid API container name as the canonical comparison value for that omitted field. Preserve every other spec field and annotation in the comparison and template fingerprint.
2. If both names are present, require exact equality. An explicit conflict remains unrepresentable. If both are absent, compare unchanged. Do not normalize multi-container templates; keep current strict comparison and the existing one-container snapshot requirement.
3. Compute the retained Service-template fingerprint with that same paired canonical value, while keeping the revision fingerprint byte-for-byte based on its complete current `revision_config`. This lets a successful exact rollback retain the same logical prior fingerprint whether the provider readback omits the single name or returns the retained revision's name. Rollback continues to restore the complete retained revision spec and effective annotations without rebuilding.

This would allow the reported optional-field shape while retaining all existing effective-config, secret-alias, image/digest, traffic, annotation, and prior-revision integrity checks. It adds no new provider gate and does not broaden what constitutes an Auth/BFF prior service. Do not implement this until Parent/Owner dispositions the paired fingerprint rule. The frozen r2 spec is unchanged.

If accepted, the focused regressions should cover: the exact absent-versus-present single-container shape passing snapshot and prior observe; two explicit different names still failing; an unrelated spec/env/annotation difference still failing; no name normalization for multi-container input; and exact rollback → prior observe → next snapshot succeeding with the same retained revision and no build. Preserve the existing full Auth/BFF secret-alias and controller-annotation tests.
