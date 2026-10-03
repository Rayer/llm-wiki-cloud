# Accepted correction — Auth/BFF rollback identity uses image digest

This addendum records Owner correction 3584-1787. It supplements but does not edit the frozen r2 specification. It supersedes the paired-name/canonical-fingerprint proposal in `evidence/auth-revision-container-name-normalization-proposal.md`; that proposal was not implemented.

The accepted correction is the image-digest rollback identity boundary. The source mapping below records the implemented local correction and its regression coverage; it does not add acceptance gates.

It also supersedes the Auth/BFF prior-Service statements in `operator-r2.md:86,110` that required whole-template equality/fingerprints for snapshot, observe, and rollback. The operator text now matches this correction. Earlier implementation evidence remains historical and is superseded by this addendum for current behavior.

## Accepted boundary

For Auth/BFF prior Service image identity, compare the retained revision's immutable image digest with its container image and the Service template container image. The saved old image digest is the rollback basis. Whole `spec` or effective annotation equality between the Service template and the revision is not a precondition for accepting that image rollback state.

Keep the existing representability and runtime-safety conditions that are separate from whole-template equality: a single Auth/BFF container, a ready retained revision with an immutable image digest, and the existing 100-percent untagged traffic representation. Preserve the retained revision identity and traffic needed to address and route rollback. Candidate checks continue to enforce their expected image, readiness, revision identity/routing, and Auth/BFF managed configuration. This correction does not change Job or Frontend contracts.

`restore_service` currently constructs a full restore request from the retained revision spec, its effective non-controller annotations, and the saved traffic. That is a restore payload, not an equality gate. Retain this full payload so rollback can restore the retained revision's prior configuration and secret references without rebuilding; remove whole-template hashes as rollback authorization checks. The old revision's saved image digest remains the image identity used to verify the rollback target.

## Current gate inventory and narrow source delta

- **Snapshot (`providers.py`, `Providers.snapshot`):** retain one untagged 100-percent route, the exact retained revision identity, Ready state, one container in each resource, immutable revision status digest, and equality of revision spec image and Service-template image to that digest. Do not save or compare whole-config fingerprints.
- **Prior observe (`providers.py`, `Providers.observe`):** keep exact retained revision identity, readiness, immutable saved image agreement with revision spec/status and Service-template image, and the existing one-revision route. No other spec/annotation equality is required.
- **Rollback (`providers.py`, `Providers.rollback`):** verify the saved old image against the named retained revision's spec and status digest and require it Ready. The currently active Service template may already contain the candidate image, so rollback does not use it as an old-image precondition. Preserve the saved revision handle and traffic.
- **Full restore (`providers.py`, `Providers.restore_service`):** writes the retained revision's full spec and effective annotations plus traffic. `revision_config` remains for this payload; this payload is not an authorization comparison.
- **Candidate deploy/observe (`providers.py`, `Providers.service_matches` / `service_template_matches`):** preserve expected managed Auth/BFF env/secrets/service account, candidate image/status digest, Ready state, template managed config and image, exact revision identity, and one untagged 100-percent route. Unmanaged spec/annotation differences alone do not block candidate acceptance or cutover. Job and Frontend contracts are unchanged.

## Regression coverage

The migrated Auth/BFF regressions show that one-sided retained container-name and annotation differences still allow snapshot, prior observe, and rollback while image identity, readiness, and route are intact. Negative cases keep wrong saved/revision spec/status/Service images, bad route, and unready revision from satisfying the applicable snapshot/readback/rollback checks. The restore chain asserts the exact retained full spec/annotations/traffic payload, including secret aliases, with no build.

Candidate tests remain field-specific: wrong candidate image/status digest, not-ready revision, wrong managed env/secret/service account, wrong candidate identity, wrong Service-template image or managed configuration, or wrong traffic still fails. Unmanaged container-name/annotation differences do not reinstate a blanket equality gate. `test_retained_annotations_full_chain` keeps full-restore/secret-alias coverage and now treats prior image identity independently from annotations. Worker/Export Job and Frontend assertions remain unchanged.

The exact-shape offline RED in `evidence/auth-revision-container-name-causal-red.txt` records the pre-correction whole-template gate and remains historical. The production provider and TEST ONLY regressions now implement and exercise this accepted correction. Parent's bounded live observations remain separate from these offline fixtures. Frozen r2 bytes remain unchanged.
