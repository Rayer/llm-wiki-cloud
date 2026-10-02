# LWC-358 — deployment engine implementation spec r2

Revision: deployment-engine-r2. Status: FROZEN after Supervisor PASS (request e107635133e6ea609a4904cd, attempt 1) and Owner conditional authorization plus explicit success-definition confirmation. Tracker Submitted writeback/readback must complete before dispatch. Scope excludes commit/push/merge/live deployment. Review concerns are evidence duties within scope, not added functional smoke gates.

## Authority and current evidence

Owner explicitly requests formal Supervisor review of the complete draft, then implementation if there is no objection. This conditional authorization covers scoped local implementation/tests, not merge, push, live deployment, tag publication, IAM/provider changes, or destructive recovery. A blocking contract finding stops dispatch for Owner disposition. Recommendations are not automatically new acceptance gates.

Original SSOT/package worktree: /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-deploy-convergence. Preserve it and its shared symlink. Local HEAD is e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d, stale four-component source. Latest fetched origin/main: e9ccf490251ed0262481fca94af7387107be6b5f. Review implementation facts using `git show origin/main:<path>`, not stale checked-out deploy files. Implement in a separate Orca worktree from this latest baseline if approved; no merge/reset of package checkout.

YouTrack access is now verified independently by TPM and Supervisor using their own profile credentials; probe youtrack-access-probe-r1 PASS, request 18954f4e90b2280710f7e41b. Actual live State is To be discussed. Relevant historical ticket/context and Owner direction are under docs/lwc-358; current Owner decisions supersede the historical waiting-for-baseline discussion. After this review PASS, TPM will freeze the exact accepted revision and update the live ticket contract/Submitted with readback before Implementer dispatch. This prerequisite is pending execution, not missing credential setup. Do not claim already Submitted.

## Goal / non-goals

Replace agent-driven normal deployment with deterministic scripts and GitHub Actions; a skill documents entry/input/result/exception operations. Preserve formal Actions runtime authority and existing project/runtime configuration. Deliver an incremental first slice, not Kubernetes, full blue/green, traffic isolation, generic provisioning, data migrations, raw recompilation or all follow-up tickets. No automatic Production trigger or merge authority. The forthcoming small feature is a separate ticket/decision and later live DEV test; not bundled here.

## Inputs and component contract

A mutable release composition chooses a nonempty subset of auth, bff, worker, exportjob, frontend. Pin a release attempt once: engine revision, target environment/config, candidate commit, selected component profile revisions, their build source identities, published artifact handles, applicable DEV provenance, and a release-tag name. CLI supports explicit component stage operations; release orchestration invokes the same operations. Selection changes create a new plan revision, not in-place mutation of a running attempt. Artifacts are per-component receipts, never required to match the entire selected bundle.

DEV chooses develop head once at admission. Production chooses main candidate once and explicitly supplies the successful DEV release/receipt reference; resolve supplied provenance, not poll CI run/attempt/jobs repeatedly or insist exactly one historical successful run exists. Existing required GitHub protected-environment approval remains. Bind candidate/build source correspondence once with evidence; if main source differs through merge, explicit source mapping must not counterfeit provenance. Supplied component artifact can retain its original build SHA, distinct from release SHA. Engine-only or unrelated component edits must not force all components to rebuild: determine build-input applicability from the explicit profile's source inputs, not whole repository SHA alone. If applicability cannot be established, rebuild only affected component or expose an input breakpoint.

Versioned profiles define build inputs/dependencies and deterministic commands; progress/checkpoints are separate mutable records. Keep existing language/tooling and config loader where possible. Do not introduce a general plugin platform. Scripts must validate identifiers/paths/tag names and use argument arrays/quoted variables rather than eval or interpolated shell payloads.

## Stage 1 — prepare artifacts

Build/publish or resolve immutable artifacts for every selected component. Preserve individual successes immediately for failed-stage retry. No target runtime/config/routing mutation in this stage. Four Cloud Run components use exact repository@sha256 digests. Production reuses applicable DEV container artifacts, never rebuilds containers merely for promotion.

Frontend is NOT a fifth container. Prepare a deployment/build artifact suitable for the target Vercel environment without assigning customer aliases or changing active production routing; record immutable deployment/build identity and environment-specific configuration/provenance. Production public env differs from DEV; do not pretend a DEV bundle with baked-in DEV values can be reused unchanged. Reuse valid target-configured frontend output where possible. Provider-specific Vercel prepare/activation and current auto-assignment behavior must be checked from source/official commands before implementation; inability to achieve a no-runtime-mutation frontend preparation boundary is a concrete blocking design finding, not hidden behind the generic image barrier.

A release selecting two components waits only for those two receipts. All selected artifacts ready is the barrier to Stage 2. Selection four-to-five can reuse the valid existing four receipts and prepare the fifth. Failed build has zero runtime mutations, no rollback, no successful-release tag.

## Stage 2 — runtime update owned by Actions

Dispatch one formal Actions runtime workflow with pinned input and ready receipts. It performs simple manifest/handle validation, authenticates using the existing configured principal, reads and durably saves actual pre-update state close to mutation, then invokes independently executable component operations. Never import repeated CI/head/run/jobs polling, broad exact-IAM-shape verification or agent reapproval loops. A real permission denial is a precise typed breakpoint, not permission to seek alternate credentials.

Snapshots include exact usable restore state: Service revision/image plus changed config and traffic; Job image/template; Vercel aliases to retained deployments. Snapshot is target environment state, not branch head or prior Git tag. If necessary restore information cannot be read/save before mutation, stop without changing runtime. Store redacted receipts and restrict raw restore state appropriately; never expose secret values. Existing secrets references may be retained, not their plaintext.

Profiles support deploy, readback, rollback and candidate-reactivation independently. Reuse actual existing provider/config logic rather than add a parallel ad hoc agent deployment authority. Dependency ordering preserves exportjob-before-dependent-bff where applicable. True independent operations may run in parallel; initial serial scheduling is acceptable if per-component contracts/checkpoint ownership are independent and no false atomicity is claimed. Avoid shared read/modify/write journal races and fixed temporary filenames when parallel execution is supported.

Owner explicitly accepts: all selected components deployed plus provider readback/basic sanity success = successful release, then tag. Functional tests and human UAT are separate, not initial release/tag gates. Sanity assertions: Auth/BFF Service candidate Ready, exact expected immutable image and managed config, expected revision routing; Worker/Export Job exact expected image and managed template/config (no execution implied); Frontend deployment Ready and recorded target aliases resolve to expected retained deployment identity. Read back changed/managed fields only; do not require untouched fields to match an invented profile. Bounded readback: at most 12 observations at 5-second intervals, each provider call with a 30-second process timeout; report exhaustion/mismatch or unavailable state explicitly, no open-ended agent QA. Offline fixtures test each assertion failure as well as success; no credentials or synthetic live evidence.

## Failure and recovery

Owner defines (1) a component deployment failure, including partial mutation; (2) all selected components deployed but a serious issue detected afterwards. Both use the retained pre-state and candidate handles. On known deployment/readback failure, attempt rollback of affected changed/possibly changed components in reverse dependency order; unchanged/unstarted components need no redeploy. Preserve successful prepared artifacts. Do not claim cross-component atomicity.

Unknown provider outcome first reconciles observed state. Do not blindly replay a possibly accepted mutation. An unreadable result leads to a structured breakpoint retaining exact attempt/checkpoints, not a fabricated success. Rollback failure/unknown remains explicit. Later serious-issue recovery is an explicit Actions recovery invocation with affected release/checkpoint and Owner decision or an already-defined assertion; agent cannot broaden 'serious' to every improvement concern. Candidate-reactivation reuses retained artifacts/config, no new build. Running Jobs and persistent writes are not reversed by image rollback; report that boundary without implementing destructive data recovery.

Success means all selected deploy/readback basic sanity assertions pass; functional smoke is explicitly not a first-slice success gate per Owner disposition. Only then create an immutable source Git tag for DEV or Production. Tag naming/version policy deferred: require explicit valid tag input rather than invent semver. Same tag/same commit is idempotent; different target commit is a conflict, never force move. Subsequent rollback preserves tag and appends rolled-back release status. Tag failure after runtime success is incomplete metadata; retry tagging only, not rebuild/runtime mutation.

## Skill / breakpoint output

Update repository deployment operation skill/docs alongside the engine. Document one normal entry and typed statuses, source/artifact traceability, safe retry/recovery and authority boundaries. Agent intervenes only at explicit result breakpoint: missing/invalid input; artifact/config incompatibility; actual missing permission; unknown provider state; failed readback/recovery; serious functional issue; scope/contract decision. Include release/attempt, stage/component, reason, expected/observed redacted facts, whether mutation may have happened, prior/candidate handles, last verified checkpoint and allowed next action. Deterministic bounded retries stay in script; resume preserves checkpoints and never restarts everything by default.

## Observable acceptance before the small-feature live test

Use offline command fakes/provider fixtures clearly labelled TEST ONLY; they must exercise actual production orchestration/adapters and workflow interfaces, not invent production evidence. Verify at minimum:

1. Selected-subset readiness: failed second build prevents every runtime mutation, retains first output; retry invokes only missing/invalid build.
2. Four-to-five selection reuses applicable existing four artifact receipts; Frontend handled separately; tool-only change does not invalidate unaffected build inputs.
3. Production resolves explicitly supplied DEV artifacts without container builds or CI historical polling; rejects wrong source/config/expired-unusable handles with typed evidence.
4. One runtime workflow consumes ready artifacts; no build commands inside runtime mutation; config/dependency ordering verified.
5. Component A success, B partial failure causes verified restoration of changed components, preserves candidates; independent A rollback/reactivation performs no build.
6. Timeout-after-acceptance reconciles without duplicate update; unreadable readback emits unknown breakpoint; failed rollback cannot become success.
7. Deployment sanity assertion failure and later explicit severe-issue recovery use same retained snapshot; unchanged components unaffected; Job/data limitations reported.
8. Tags only after success; idempotence, conflicting existing tag, tag-only retry and preserved tag after later rollback.
9. Vercel preparation has no alias/active-routing mutation; deploy/rollback/reactivation consume retained identity, target environment config verified.
10. Workflow lint/contracts, scripts tests and relevant existing deployment regression suite pass; document any old tests intentionally superseded by accepted removal of repetitive machinery rather than mechanically retaining obsolete expectations. Test environment failures must be reported, not substituted with fake pass.

Delivery: real edited artifacts, test commands/output, diff against pinned baseline, deployment skill and operator entry examples. No live small-feature test yet; no claim of deployed/UAT/released. Reviewer source review uses exact implementation revision/content before any optional commit/PR. Do not commit/push without subsequent explicit authority.

## Supervisor review request

Return PASS only if this complete draft is sufficiently specified for scoped implementation under Owner's conditional permission, or HOLD with concrete blocking contract/setup findings and next discriminating action. Inspect latest source independently. Distinguish first-slice blockers from later improvements. In particular assess frontend preparation, provenance/reuse matching, the explicit Owner release-success definition, rollback snapshot coverage and planned tracker Submitted/writeback before dispatch. The previous smoke finding is resolved by Owner rejection of that unapproved draft gate, not by inventing functional QA acceptance. The credential setup finding is resolved by independent live access probes; actual freeze/Submitted remains a TPM action after PASS, not review approval itself. Do not edit files/tracker/providers/git refs, or freeze/dispatch for TPM. Prior deployment-principles-r1 consultation is not approval of this revision.

## r1 findings disposition for this review

- Undefined functional smoke: Rejected as initial gate by explicit Owner confirmation; replace with the provider basic-sanity definition above. Functional QA remains separate and later serious-issue rollback remains available.
- Tracker setup: Resolved access, not workflow bypass. TPM must persist accepted frozen contract and live Submitted then read back before dispatch. Conditional Owner permission authorizes continuation when Supervisor has no blocking objection; no additional implementation/merge/deploy authority inferred.
- Frontend preparation note: Accept target-configured immutable `.vercel/output` archive as Stage 1 artifact; Stage 2 creates provider deployment and updates aliases. No Vercel deployment command in Stage 1. Target/config artifact applicability remains explicit.
- Provenance note: Accepted within existing artifact-validity scope; use independently verifiable source-input identity, not an unsupported mapping assertion.
- Snapshot note: First slice excludes creating/deleting absent runtime resources; absent or unrepresentable prior state stops before mutation with precise breakpoint. Recovery operations serialize same target and reject stale checkpoint overwriting a later release. This scope avoids provisioning/destructive expansion.
