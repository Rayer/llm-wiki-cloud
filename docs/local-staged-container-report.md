# Container directory simplification — 2026-09-20

Implemented in the existing dirty `Rayer/JevImpl` tree at base
`e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d`. No commits, deploy/workflow/push changes,
global/profile/skill edits, or token audit in the implementation pass.
The parent subsequently performed the runtime/build/live checks recorded below.
The pre-existing production diffs, fixtures, reports and diagrams were retained.

## Result

The existing worker Dockerfile now exposes a shared `worker-runtime`, an
`experiment-build`, and an `experiment` target containing query_experiment,
worker and public Synto. Final/default target remains `worker` and inherits the
unchanged `/worker` entrypoint. The original nonce requirement remains.
Synto version, artifact URL and checksum are explicit build inputs with the same
0.7.0 defaults; the build verifies the wheel checksum and public version.

Container execution remains the existing staged Go orchestration. It requires
`/experiment` plus external launcher image metadata and confines input/config/
metadata/output paths to children of the mount. Existing roots are usable;
outputs remain new children. Cloud locators and alternate query command modes
are rejected in the experiment binary. Native import/debug modes remain.
The worker's local public export now keeps temporary/cache files in its private
directory too. No private Synto API or SQLite schema reads were introduced.

`run.json.environment` records source revision, dirty flag, unique build identity,
binary platform, launcher-supplied image digest and metadata hash. The image
digest is explicitly a launcher assertion from external runtime inspection,
never inferred from build arguments. Existing input/config/checkpoint provenance
is retained. The public Synto version is observed in every container run and
recorded even on a recognized version mismatch. Vendor internal completeness
remains `not_proven`; public output acceptance semantics are unchanged.

## Executed checks

Parent subsequently ran the unrestricted final container-revision suites:
`go test ./cmd/query_experiment ./cmd/olw_worker ./internal/queryquality -count=1`.
All passed (5.523s, 24.378s, 0.276s respectively); no listener tests were excluded.

Parent also inspected the fresh DNS stage100 answers and resolved every returned
citation to a nonempty snapshot article. `dns-testing` returned three citations;
`example-domains` returned one. Both still contain the unresolved inline label
`[Reserved Top Level DNS Names]`. Thus the minimal grounded oracle passed, but
complete inline citation acceptance did not in that pre-LWC-335 run. The local
LWC-335 follow-up below records changed behavior without reclassifying that run.

Commands below run from `apps/bff` unless noted. These implementation checks used
local/mock providers; the staged suite also exercises installed public Synto init
with an isolated private HOME, without inference. Parent mounted/live evidence
is recorded separately below.

```sh
GOCACHE=/private/tmp/jevimpl-container-go-cache go test ./cmd/query_experiment \
  -run 'TestStaged|TestLocalPlatform|TestMaterializePinned' -count=1
# exit 0: ok github.com/rayer/llm-wiki-bff/cmd/query_experiment 6.709s

GOCACHE=/private/tmp/jevimpl-container-go-cache go test ./cmd/query_experiment \
  -run 'TestStagedSyntoVersionUpgrade' -count=1
# exit 0: ok github.com/rayer/llm-wiki-bff/cmd/query_experiment 0.654s
# Final added assertion also preserves the observed version on mismatch.

GOCACHE=/private/tmp/jevimpl-container-go-cache go test ./cmd/olw_worker -count=1
# exit 0: ok github.com/rayer/llm-wiki-bff/cmd/olw_worker 23.787s

# queryquality ran in the broader three-package command below:
# ok github.com/rayer/llm-wiki-bff/internal/queryquality 0.467s

GOCACHE=/private/tmp/jevimpl-container-go-cache CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags '-X main.experimentRevision=e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d -X main.experimentDirty=true -X main.experimentBuildID=11111111111111111111111111111111 -X main.stagedSyntoVersion=0.7.0' \
  -o /private/tmp/jevimpl-container-build/query_experiment-linux ./cmd/query_experiment
# exit 0; test build identity only, not a real OCI image identity

GOCACHE=/private/tmp/jevimpl-container-go-cache CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags '-X main.buildNonce=11111111111111111111111111111111' \
  -o /private/tmp/jevimpl-container-build/worker-linux ./cmd/olw_worker
# exit 0

# Repository root:
git diff --check
# exit 0
```

Focused coverage includes the actual image ENTRYPOINT argument shape, new-child
output and overwrite rejection, out-of-root/symlink/cloud path rejection,
mandatory container launcher metadata, malformed digest/platform/unknown-field
rejection, persisted provenance, command-mode restriction, explicit Synto version
upgrade and successful stage20 receipt reuse by stage50. The existing worker
public CLI test verifies private temp/cache variables and export key isolation.
Mocks use obviously synthetic digests only inside tests.

The broader command was also attempted:

```sh
GOCACHE=/private/tmp/jevimpl-container-go-cache go test \
  ./cmd/query_experiment ./cmd/olw_worker ./internal/queryquality -count=1
```

It exited 1: query_experiment's
`TestFixturePersistenceDeepScrubsProviderEchoWithoutMutatingTrace` panicked when
`httptest` tried to bind `[::1]:0` (`operation not permitted`). The original worker
Dockerfile assertion expected a literal URL/hash string; it was updated to check
the exact pinned ARG defaults and hash-verified install. The full worker rerun
above passed. An initial new upgrade test attempted a fork lacking donor worker
identity; corrected to the existing supported stage20 snapshot-import path,
without weakening fork checks. The staged suite subsequently passed.

Logs are `/private/tmp/jevimpl-container-focused-tests.log`,
`/private/tmp/jevimpl-container-worker-tests.log`, and
`/private/tmp/jevimpl-container-go-tests.log` (the last contains the blocked
broader attempt). These are test evidence, not live inference evidence.

## Parent-verified build, mount and live results

The parent verified the Apple Container client and server upgrade to **1.4.1**,
built the `experiment` image and ran it with the experiment directory mounted at
`/experiment`. Evidence root: `/private/tmp/jevimpl-experiment.9Vw7HK`.
The [operator guide](local-staged-e2e.md#one-directory-one-container) retains the
build/inspect/mount commands and environment-only provider key boundary.

`image.json` and all four run receipts agree on platform `linux/arm64` and image
`sha256:0af31bf3a85f9959300e151a98e974845c54ed966eeeb92694a08073bcc7f742`.
Run environment provenance records source revision
`e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d`, `source_dirty: "true"`, build ID
`d9ba4ada801e00156a2dba423ef04443`, and launcher metadata SHA-256
`a907c787958da3a686c5aba20e6717a8dcdb4921db9d5400c12dd1173f706f7f`.
All runs record public Synto **0.7.0** and runner schema `lwc-local-go-v2`.
The image digest remains an externally inspected launcher assertion; build ID
is not a source-content hash, and the dirty source tree is needed for review.

| Evidence relative to the root | Verified result |
| --- | --- |
| `prepared-runtime141-verified/run.json` | Mounted provider-free source stage10 succeeded; quality not requested. Client/server 1.4.1 verification is parent evidence, not a runtime-version field in this receipt. |
| `live-dns/run.json`, `live-dns/artifacts/100.json` | One uninterrupted live invocation: fresh stages 10/20/50/60/70/80/90/100 all succeeded; grounded-answer quality passed for both DNS cases. |
| `cloud-run/run.json`, `cloud-run/artifacts/100.json` | Imported DEV snapshot stages 60/70/80/90/100 all completed mechanically (`status: success`); grounded quality failed, exit 2. `stable-alpha` is `no_grounded_answer` with `citation_routes_rejected` and an empty citation inventory. |
| `cloud-matching-only/run.json` | Fresh ONLY80 fork from cloud checkpoint70 succeeded without a key (parent launch evidence); quality not requested. The generic `live_providers` label does not imply a provider call. |

Current DNS artifacts contain nonempty answers and resolvable inventories:
`dns-testing` has `.example`, `.invalid`, and `.test`; `example-domains` has
`.example`. Both answers still contain `[Reserved Top Level DNS Names]` without
a matching inventory route. The passed oracle is an answer-plus-inventory smoke,
not factual accuracy, per-claim support or complete inline-citation validation.
At the time of that container verification, the cloud `stable alpha` defect was
tracked as [LWC-335](https://irisnode.youtrack.cloud/issue/LWC-335) outside the
mechanism scope. The local follow-up below fixes canonical citation resolution;
the unchanged legacy cloud fixture still fails and is not positive acceptance.

Public vendor acceptance remains `public-exit-and-validated-agents-export` with
`internal_completeness: "not_proven"`; these live results do not establish
exhaustive Synto internal completion. Existing model/version pins, source/import
limits, frozen checkpoint compatibility, credential isolation and historical
Python evidence remain as documented in the operator guide. These runs do not
supersede the sandbox limitation on the broader Go test command above.

The separate one-time audit artifact is
`/Users/rayer/.hermes/profiles/chatgpt/artifacts/skill-token-audit/ONE-TIME-FOLLOWUP.md`;
this documentation update does not perform or change that audit.


## LWC-335 local implementation follow-up — 2026-09-20

Shared production synthesis now resolves the request snapshot's existing ID map
before citation issuance. The response carries canonical IDs and escaped detail
paths while retaining titles/slugs. Missing/ambiguous/malformed/unsafe map data
fails explicitly; no legacy IDs are guessed and the migration side map is not
used as a normal identity source. Source hydration uses source pages. The HTTP
ID-only redirect now escapes whitespace/Unicode in its target. Frontend tests
confirm the existing ID-first modal lookup and the escaped full-article link.
The experiment reader only exposes the required bounded map/source artifacts;
there is no experiment-specific citation repair.

Observed local checks:

- Portable RED: valid Synto and legacy concept maps for `台北 coffee` both yielded
  no citation before implementation (`/tmp/lwc335-red.log`).
- Full `internal/query`, `internal/search`, `internal/queryquality`, and
  `internal/wikiindex` suites pass with explicit mock transports and mapped test
  fixtures (`/tmp/lwc335-go-core.log`).
- Focused HTTP/detail and routing regressions pass
  (`/tmp/lwc335-go-http.log`); focused staged/platform/snapshot tests pass
  (`/tmp/lwc335-go-experiment.log`).
- Frontend node tests: 517 passed; citation components: 15 passed; typecheck
  passed. Dependencies were copied locally from a sibling checkout with an
  identical lockfile and Next 16.2.7; no package/lockfile edits.
- `go vet` for query/search/wikiidentity/wikiindex/handler/experiment passed.
  The earlier sandbox TCP listener denial was superseded by the parent
  unrestricted six-package PASS (`/tmp/lwc335-parent-tests.log`):
  `go test ./internal/query ./internal/search ./internal/handler/v1 ./internal/queryquality ./cmd/query_experiment ./internal/wikiindex -count=1`.
- Parent citation component rerun: 10 tests PASS
  (`/tmp/lwc335-parent-component.log`). These use component mocks; navigation
  is not implemented in the test environment. No real browser acceptance.

Replaying the saved DNS stage90 selections and stage100 answers through direct
and staged production synthesis with explicit mock HTTP gives 4 and 2 citations
for `dns-testing` and `example-domains`, respectively. Both answers now have zero
unresolved inline labels, and all citations resolve to nonempty snapshot pages.
The imported cloud fixture fails explicitly with no LLM call: its active map has
invalid IDs `stable-alpha` and `s1`. Its historical three `[stable alpha]` labels
remain unresolved. The compatibility `concept_entity_id` value was not promoted
or substituted. This negative result is expected fixture characterization, not
cloud grounded acceptance.

Fresh replay output: `/tmp/lwc335-replay-final.6mGtyk/`.
Detailed verification commands and evidence inventory:
`/tmp/lwc335-verification.md`; bounded review result: `/tmp/lwc335-review-report.md`. Existing `/private/tmp/jevimpl-experiment.9Vw7HK/`
snapshots and artifacts were read only. No new image build, mounted run, live
provider request, credential use, cloud write, deployment, commit, push, or
tracker update occurred in this implementation. Parent owns review and final
acceptance; the earlier live/container results above remain historical.


LWC-335 bounded review: context building now validates and indexes the active ID
map once per request, then resolves exact type/slug keys with ambiguity and
cross-type collision rejection. No global cache or additional file reads.
Optional frozen replay requires `LWC_E2E_CITATION_EXPECT=mapped` (one citation
per selected result) or `rejected` (identity failure before inference); directory
names do not select expectations. Focused tests and original/renamed snapshot
replays are recorded in `/tmp/lwc335-verification.md`. That review did not include fresh image/live/browser acceptance; the later
delivery sections below record their completion.

## Real browser citation acceptance — 2026-09-20

Fresh isolated evidence: `/private/tmp/lwc-platform-delivery.Wbhyab`.
The real Next frontend and production BFF HTTP router passed inline concept and
source citation clicks, modal article loading, and full-page navigation for mapped
Unicode/space slugs `台北 café` and `來源 café`. Canonical IDs were
`01JAZ5N7Y3K8M2Q4R6T9VWXABC` and `abcdef123456`; ID-only BFF requests redirected
to the same percent-escaped paths emitted by synthesis and returned the intended
nonempty article bodies. `browser-http-evidence.json`, `browser-*-article.json`
and `browser-*-article.png` preserve HTTP responses, browser snapshots and screenshots.

Reproduction from repository root (use a fresh local directory):

```sh
LWC_BROWSER_ROOT=/private/tmp/lwc-browser-new go -C apps/bff test ./cmd/bff \
  -run '^TestLocalCitationBrowserServer$' -count=1 -timeout 22m
NEXT_PUBLIC_API_URL=http://localhost:18081 NEXT_PUBLIC_AUTH_URL=http://localhost:18081 \
  npm --prefix apps/frontend run dev -- --port 18080
```

Open `http://localhost:18080`, choose the existing local **試用 Demo** login,
search `coffee`, click each inline citation, then its full-page link. The opt-in
fixture harness creates only isolated local data and uses existing local login;
it makes no authentication changes. **Retrieval selections and LLM transport are
explicitly mocked**; production synthesis, identity mapping, HTTP query/detail
routes, rendered frontend and navigation are real. This browser check is separate
from live provider/container acceptance. Stop the harness by creating
`$LWC_BROWSER_ROOT/stop`; without opt-in it skips normal test runs.

The existing legacy cloud snapshot was rechecked read-only with the explicit
`rejected` expectation: PASS for invalid-map rejection before inference, not
positive cloud grounded acceptance (`legacy-negative.log`). Comprehensive
current delivery evidence: `/tmp/lwc-platform-delivery-report.md`.

## Fresh post-LWC-335 container acceptance — 2026-09-20

Evidence root: `/private/tmp/lwc-platform-delivery.Wbhyab` (the earlier
`jevimpl-experiment.9Vw7HK` directory was not modified). Apple Container client
and server **1.4.1**, public Synto **0.7.0**, platform **linux/arm64**.
Fresh image `jevimpl-experiment:1f3c19a0011501f7d4f6b76813b2f419` has inspected OCI
index digest `sha256:dbdece09d93afb96b8fac7ef04f06c04c25dc6e7b538354a3dc4c056f8e7763a`.
Source revision is `e8bf80491cfcc7f4c79f541ecc850d4e290f0b2d`, dirty; build ID
`1f3c19a0011501f7d4f6b76813b2f419` is not a content hash. Runtime inspection,
launcher metadata, starting diff/status and source-file SHA-256 manifest are
retained alongside the image build log. The browser-only test harness and final
documentation were added after the build; production source remained frozen.

| Evidence | Observed result |
| --- | --- |
| `build.exit`, `build.log`, `image-inspect.json` | Fresh experiment target build exit 0; external image identity recorded. |
| `prepared/run.json`, `prepared.exit` | Mounted, read-only-rootfs source stage10 exit 0; no inference. |
| `live-dns/run.json`, `live-dns.exit` | Exit 0; fresh stages 10/20/50/60/70/80/90/100 succeeded; both grounded DNS cases passed. Coordinator injected the authorized key only into launch environment. |
| `live-dns-citation-audit.json` | Four canonical mapped citations per case; all point to nonempty snapshot articles. Five inline labels in `dns-testing`, two in `example-domains`, zero unresolved labels. Inventories may include selected but unused references. |
| `matching-only/run.json`, `matching-only-audit.json` | Key-free ONLY80 fork exit 0; only stage80 executed, artifact identical to live stage80. Quality not requested. |

The fork used the same image, mount, launcher metadata, cases, config, profile and
prompt as live, with `--fork /experiment/live-dns --only 80 --output
/experiment/matching-only` and no provider environment injection. Exact build,
live and replay commands, exits, browser evidence and changed files are recorded
in `/tmp/lwc-platform-delivery-report.md` and the experiment `README.md`.

Public Synto acceptance remains `public-exit-and-validated-agents-export`, with
`internal_completeness: not_proven`. These checks establish local platform and
citation routing behavior, not exhaustive vendor completion, factual/per-claim
answer quality, deployed acceptance, or positive legacy-cloud acceptance. No
commit, deployment, cloud write or credential-file access by this worker.
The parent owns review and synchronization of portal LWC-A-25.
