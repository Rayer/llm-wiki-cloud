# LWC-209 implementation report

## Delivered

Implemented the Profile persistence and API scope from the accepted [LWC-351 profile contract](LWC-351-profile-contract.md). The production BFF now wires a Firestore-backed repository, exposes the profile and job routes, checks Project metadata through the existing owner-only Project read/edit seam, and reauthorizes the parent Project inside Firestore transactions. Profile state uses typed Firestore documents so `int64` revisions are not round-tripped through floating-point JSON maps.

The API includes `GET` and `PUT /api/v1/projects/:projectID/profile`, candidate confirm and retry, derivation retry, and exact job lookup. Revision preconditions are required (`428` when absent), stale writes return `409` with the latest revision, and inaccessible, missing, corrupt, marker, or foreign Project metadata returns `404` without metadata disclosure. Client input cannot set derived candidate or job state. Requirement order and text, including explicit blank text, are preserved.

PUT persists debounce scheduling intent transactionally with state. An initially empty/no-active Profile schedules no work; clearing requirements while a Profile is active uses the ordinary three-minute derivation path with the empty digest, keeps the old active tuple, and waits for the neutral manual candidate to be confirmed and its job to be ready before switching. The BFF does not synthesize neutral artifacts or coverage; the accepted clarification assigns empty-input artifact creation to 352 and coverage validation to 354. Derived results and candidate/job transitions are server-side methods with attempt identity and stale-attempt protection; repeated failed derivation attempts are supported.

Compile reconciliation now persists `waiting_manual` while a current derivation is pending/failed or a manual candidate is unconfirmed/not active. It only takes guidance from the current active candidate, then can resume the same reconcile after manual activation. All repository transaction callbacks reset captured result/decision state on each Firestore callback invocation so transaction retries do not retain stale flags or response values. These are persistence seams only: no background provider, tagger, or compiler was invoked.

The accepted contract copy documents the agreed derived-retry route, `POST /api/v1/projects/:projectID/profile/derivation/retry`, because candidate retry cannot be used before the first candidate exists. Repeated requests for the current scheduled/running attempt are idempotent; each subsequent retry after failure receives a new attempt identity.

OpenAPI was regenerated through `go generate ./cmd/bff`; the generated Swagger YAML, JSON, and Go docs include the Profile routes and schemas. Unrelated generated documentation drift was removed from the artifact diff.

## Verification

Commands were run from `apps/bff`:

| Command | Result |
| --- | --- |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 ./cmd/bff -run 'Profile|FirestoreProfile|ProductionRouter' -count=1` | Passed; handler and router packages passed. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run 'Profile|FirestoreProfile' -count=1` | Passed after the new RED test initially reproduced `waiting_guidance` instead of `waiting_manual`. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./...` | Passed across all BFF packages, including Firestore emulator transaction tests. |
| `go vet ./...` | Passed. |
| `go build ./...` | Passed. |
| `git diff --check` | Passed. |
| `go generate ./cmd/bff` | Passed; Profile OpenAPI additions generated. |

The Firestore emulator tests exercise repository transactions for metadata reauthorization, scoped access, large revision precision, debounced writes, retry attempts and stale completions, candidate/job state transitions, idempotent activation, guidance preservation, empty bootstrap versus active Profile clearing through an empty-digest candidate/confirmation/ready path, manual-pending/unconfirmed/active-edit compile conflicts, resumed compile reconciliation, and exact historical job lookup.

## Follow-on work and limits

The worker/scheduler and downstream compile/tagging integrations remain for their owning follow-on tickets (including 352/354/355/356). The current implementation stores scheduling intent and provides versioned internal transition methods; it does not claim that provider execution or live environment acceptance has occurred. No commit, push, deployment, production, provider, IAM, or credential mutation was performed.
