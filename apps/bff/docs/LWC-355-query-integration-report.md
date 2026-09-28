# LWC-355 Query integration report

## Implemented request path

The existing authenticated `POST /api/v1/query` registered by `cmd/bff/main.go` still invokes `v1.Handler.Query`. Its query-specific `queryStore` now reads authorized Profile state once, captures `active`, and reads the immutable historical candidate named by `active.candidate_id` through `firestoreProfileRepository.GetProfileCandidate`. The latter uses the existing Project read authorization transaction and does not reread the moving active state. Candidate ID, content generation and dictionary revision must agree; dictionary validation uses this historical candidate's full derived ref and preview requirement order, never newer current requirements.

An active request scopes the existing store to its user/Project and calls `gcs.Client.PinQueryGeneration` → `PinGeneration`. This reads only `.lwc/publish/generations/{id}/manifest.json`, requires the exact generation ID, and captures the archived manifest's object generation and byte digest. Generation-owned file reads use the manifest's exact object generations, sizes and SHA-256 values. The archive token includes scope, generation ID, manifest object generation and digest; changing Project scope drops a captured view. Missing archives and corrupted declared objects never fall back to current.

`query.LoadProfile` uses `cache.PinnedCanonicalSnapshot` for a verified canonical corpus, reads the pinned ID map and hash-bound retained source inventory. It cross-checks all concept stable IDs/slugs and exact source membership against the active ID map, checks source raw paths against ID-map metadata when present, and uses the inventory content hashes for Tag-set coverage. `SourceStatusDigest` is publisher-attested provenance; no private `cache/source_status.json` receipt is required in the manifest or read by Query. The dictionary is verified against the historical ref, generation and corpus digest. `profiletags.ReadBundle` then reads precisely the aggregate set and rules and validates complete coverage and cross-references. Query reads neither individual decisions nor source byte objects; `ReadSourceBytes` is separately available for consumers needing exact source content.

The same `query.Request` carries the snapshot through either production composition (`queryquality.ProductionExecutor` directly or `queryruntime.Executor` delegating to it). `QueryRetrievalPipeline.execute` uses the captured corpus, preserves the existing body matcher, and adds at most one ranking point for an exact concept `match` on a `preferred` rule before the existing selector. Source matches, unknown/not-applicable judgments and required proposals do not earn that bonus. Eligibility and qualification remain unchanged. Legacy expansion fallback is disabled for active Profile requests so failures cannot bypass this path.

Production synthesis uses the request's already captured corpus and ID map for citation contexts, even if shared cache entries are invalidated during execution. It cannot fetch G2 pages, scan small files, or inherit source Tags. No active Profile uses the existing latest-view/body-search behavior without loading Tag artifacts. A Profile read failure is an error, and Project authorization denial is 404.

## Explicit hard conditions and quality boundary

The wire-level explicit predicate is `required_tag_ids: ["exact_dictionary_tag_id"]`; ordinary text is not classified into hard Tag predicates. The common production/traced execution boundary calls `Bundle.RequireSupported` only for those IDs. With the frozen writer/reader contract, all hard gates are disabled, so explicit requests return HTTP 422 with `required Profile condition is unsupported`; an unrelated query remains usable with the same dictionary. Unknown text remains a body-search query, while an explicitly requested unknown Tag ID is unsupported.

No production geography hard filter is enabled. The frozen reader rejects enabled rules, and there is no reviewed deterministic administrative-code/containment artifact in that contract. Therefore Taipei→Shilin directional hard satisfaction is **not claimed as delivered**; Taipei/Shilin/nearby/unknown explicit hard requests are rejected safely. Implementing an enabled directional gate still requires an accepted deterministic evidence/containment contract and the reviewed quality-policy change, not model-only inference. This preserves the LWC-334/349 quality restriction.

## Local verification

All new fixtures are synthetic and labeled. The production-route fixture uses real dictionary encoding, `profiletags.Build`, the real Gin `POST /api/v1/query` handler, real `ProductionExecutor`, existing matching/selection and real citation assembly, with fake object storage, evaluator and HTTP responses. No live provider or cloud service is called.

Passing checks:

- GCS retained-generation tests and `go vet ./internal/gcs`: G1 retained while current G2, scoped tokens, cross-Project isolation, wrong/missing archive, source snapshot hash/reference mismatch, missing source bytes and exact object-generation reads.
- `go test ./internal/query ./internal/queryquality ./internal/queryruntime ./internal/cache ./internal/gcs`: existing suites plus new request-policy, preference and citation-context regressions.
- `go test ./internal/handler/v1 -run 'TestProfileQuery|TestV1Query' -count=1`: real HTTP Query path, G1 body/citations with G2 current and newer current candidate, repeat canonical-cache reuse, no concept/page scans or decision/raw-source reads, source-only matches not inherited, no-profile scoped behavior, explicit unsupported hard predicates, repository failures, candidate/artifact corruption.
- `go vet ./internal/gcs ./internal/cache ./internal/query ./internal/queryquality ./internal/queryruntime ./internal/handler/v1` and matching `go build` passed.
- `git diff --check` for owned tracked edits passed.
- The selector regression exercises both `Execute` and `ExecuteWithTrace`: a concept preference changes ranking without changing qualification or admitting an ineligible candidate. Route testing caught and fixed a guard initially present only on the traced path.

## Remaining development integration

The coordinator accepted the existing publisher-attested inventory contract. The previous private-receipt reader requirement is removed; no generation schema or publisher change is needed. Retained inventory hash, generation, ID-map digest/membership and retained raw-byte hash/object version checks remain in their respective readers.

`TestProfileRuntimeConnectedManualThenCompileTagging` now continues from the real local dispatcher, Firestore repository, provider adapters and loopback GCS through the actual Gin Query route and production executor. Before any Profile exists, ordinary Query succeeds. After runtime activation, G1 remains active while current G2 exists; Query returns G1 body, stable result/citation ID, title and slug, and the real runtime-derived preferred concept contributes its soft score. Explicit `required_tag_ids` returns exactly HTTP 422 / `required Profile condition is unsupported`. Deleting or corrupting the retained source snapshot returns exactly HTTP 500 / `generated data unavailable`, with no synthesis or current fallback; restoring the object restores successful G1 Query. The separate queryquality selector regression verifies the score changes ranking without changing eligibility/qualification.

This connected regression caught a real canonical-reader incompatibility. The actual writer chain is `cmd/olw_worker.runCacheIndexStage` → `wikiindex.RebuildWithSyntoIdentity` → `buildSyntoConceptsJSONL` → `json.Marshal(cache.Entry)`. That type emits `slug`, `title`, `body`, `frontmatter`, and `sources`; `updated_at` may be inside frontmatter, not a top-level writer field. With the parent's explicit narrow ownership authorization, `profilederive.conceptRow` now accepts typed `body` and `sources`. Duplicate-key rejection, strict unknown fields, ID coverage, full-input hash and exact row bytes remain unchanged. The focused regression marshals the actual `cache.Entry` type, checks exact row/hash preservation, and rejects top-level `updated_at` plus duplicate body/sources. Both manual and compile-auto derivation and Tag inventory use this shared validator; sibling cache and worker readers already handle these fields.

Final local gates, all passed on 2026-09-25:

```sh
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 ./internal/gcs ./internal/query ./internal/cache ./internal/queryquality ./internal/queryruntime ./internal/profilederive ./internal/wikiindex -count=1
go vet ./internal/handler/v1 ./internal/gcs ./internal/query ./internal/cache ./internal/queryquality ./internal/queryruntime ./internal/profilederive ./internal/wikiindex
git diff --check
```

The full handler suite includes the connected runtime regression and Firestore tests, with no emulator skip. The already-installed Firestore emulator was started locally at `127.0.0.1:8585`; GCS and model responses remain loopback/synthetic. Test output is in `/tmp/lwc-query-full.log` and focused connected evidence in `/tmp/lwc-query-connected.log`. No in-scope local acceptance remains failing.

Local tests cannot prove deployed scheduler/OIDC/IAM wiring, cloud retention/GC configuration or live model quality. Those were not exercised. Hard Tag satisfaction, including Taipei→Shilin directional containment, remains deliberately unsupported under the accepted contract; safe 422 behavior is delivered, not full hard-filter functionality.

The historical Query candidate lookup is implemented in the query-owned file and coordinated with the runtime owner. Cloud provisioning, actual retention/GC policy readback, live model quality evidence, deployment and user DEV UAT were not performed. No commits, pushes, credentials or deployment actions were used.
