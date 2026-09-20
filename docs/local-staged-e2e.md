# Local staged LWC experiments (Go runner)

The current entry point is `query_experiment staged`. It replaces the two local
Python scripts and their two Python test files; fixtures and historical external
runs remain intact. It invokes the existing production query executor directly.
The worker remains the binary boundary for index and suggested-query generation.
Synto remains one stage, executed through the installed **public `synto` CLI**.

## One directory, one container

Prepare one existing experiment directory, then bind-mount it at `/experiment`.
The image contains `query_experiment`, `/worker`, and public `synto`. The Go
runner owns all stages; there are no services, Compose files, or per-stage
containers. Each invocation creates a **new child output**, such as `run/`.
Never pass the existing experiment root itself as `--output`.

```text
experiment/
  raw/                       # or snapshot/, prepared outside the container
  query.json
  cases.jsonl
  image-inspect.json         # external runtime inspection, image only
  image.json                 # launcher metadata derived from that inspection
  build.log
  run.log                    # safe CLI result/error, no provider console logs
  prepared/                  # optional source-only smoke output
  run/                       # run.json, input, work, snapshots, artifacts, home, xdg, tmp
  synthesis-comparison/      # optional isolated fork output
```

### Parent: prepare and build on Apple container

Run from the repository root. The parent verified the image build, mounted smoke,
and live runs under `/private/tmp/jevimpl-experiment.9Vw7HK`, and verified the
Apple Container client/server upgrade to **1.4.1**. The source-only receipt is
`prepared-runtime141-verified/run.json`; current results and exact image identity
are in the [container report](local-staged-container-report.md#parent-verified-build-mount-and-live-results).
Start the runtime before building. Each rerun needs new child outputs.

```sh
experiment=$(mktemp -d /private/tmp/jevimpl-experiment.XXXXXX)
cp -R scripts/local_e2e/fixtures/raw-dns "$experiment/raw"
cp scripts/local_e2e/fixtures/query-dns.json "$experiment/query.json"
cp scripts/local_e2e/fixtures/cases-dns.jsonl "$experiment/cases.jsonl"
revision=$(git rev-parse HEAD)
source_dirty=false
if test -n "$(git status --porcelain --untracked-files=normal)"; then source_dirty=true; fi
build_id=$(openssl rand -hex 16)
image="jevimpl-experiment:$build_id"
# Parent only: start the already installed runtime before building.
container system start
container build --platform linux/arm64 --target experiment \
  --file apps/bff/cmd/olw_worker/Dockerfile \
  --build-arg BUILD_NONCE="$build_id" \
  --build-arg SOURCE_REVISION="$revision" \
  --build-arg SOURCE_DIRTY="$source_dirty" \
  --tag "$image" apps/bff > "$experiment/build.log" 2>&1
```

Check the build exit before continuing. `SOURCE_REVISION` is the current Git
revision, `SOURCE_DIRTY` includes untracked work, and `BUILD_NONCE` is a unique
build identity, **not a source-content hash**. The actual inspected image digest
identifies the built content. Retain this dirty source tree for review; a commit
alone cannot reproduce it. Record one build per experiment and do not retag it
between inspection and execution.

The last/default Dockerfile target remains `worker`, with `ENTRYPOINT ["/worker"]`
and the existing nonce requirement. `experiment` reuses `worker-runtime` and the
worker build. It adds only the query runner. No deployment/workflow/push change
is needed. Base images and Synto dependencies are not claimed byte-reproducible;
retain the inspected OCI image when exact replay matters.

### Parent: inspect outside the container, then launch

```sh
container image inspect "$image" > "$experiment/image-inspect.json"
cat "$experiment/image-inspect.json"
# Enter the OCI image/index descriptor digest shown by the runtime above.
# Do not use a layer digest, build nonce, source hash, tag, or fabricated value.
printf 'Runtime-inspected image digest (sha256:...): '
read -r image_digest
printf '%s' "$image_digest" | LC_ALL=C grep -Eq '^sha256:[0-9a-f]{64}$' || exit 1
printf '{"schema":"lwc-container-launch-v1","image_digest":"%s","platform":"linux/arm64"}\n' \
  "$image_digest" > "$experiment/image.json"

# Provider-free mounted smoke: public Synto version + init + source checkpoint.
container run --rm --read-only --platform linux/arm64 \
  --volume "$experiment:/experiment" "$image" \
  --launcher-metadata /experiment/image.json \
  --output /experiment/prepared --raw /experiment/raw --to 10 \
  --query-config /experiment/query.json > "$experiment/prepared.log" 2>&1
```

Stop if inspection cannot supply a digest. The image cannot self-discover its OCI
digest; launcher metadata is mandatory and recorded as a launcher assertion,
not cryptographic self-attestation. Keep the original inspection beside it.
Only inspect the **image**: container inspection may include launch environment
values. The runner checks metadata schema/digest syntax/platform and records its
SHA-256; it cannot verify the launcher's honesty from inside the container.

For the full DNS live smoke, inject `DEEPSEEK_API_KEY` into the parent's launch
environment using its existing process. Do not type the key into these commands,
write it into files, pass it as a build argument, or enable shell tracing.

```sh
container run --rm --read-only --platform linux/arm64 \
  --volume "$experiment:/experiment" --env DEEPSEEK_API_KEY "$image" \
  --launcher-metadata /experiment/image.json \
  --output /experiment/run --raw /experiment/raw --to 100 \
  --cases /experiment/cases.jsonl --require-grounded --timeout 600 \
  --query-config /experiment/query.json \
  --query-profile corpus-derived-tech-document-v1 \
  --query-prompt domain-neutral-technical-v1 > "$experiment/run.log" 2>&1
```

Each `container run` is one invocation of the same staged runner. The only host
mount is this directory: no cloud credentials, host home, sockets, or source-tree
mount. Rootfs is read-only; container temporary files and private child HOME/XDG
live under the new output. Provider stdout/stderr is discarded; safe outcomes,
stage timing, failure categories and child exit codes are in `run.json` / the
redirected CLI log. Read exit codes and receipts before declaring success; exit
2 is a failed grounded oracle even if execution completed. Repeat with a new
child output and log name. Stop with Ctrl-C; successful checkpoints remain.

### Explicit Synto upgrades

Defaults remain version `0.7.0`, the existing wheel URL, and SHA-256
`4bc8dcf14b53f45fac32ce737ecf878f1a46d6d0b010c7decbe6c3b7b10afa77`.
To upgrade, supply **all three reviewed inputs** to the same build command:

```sh
--build-arg SYNTO_VERSION="$reviewed_version" \
--build-arg SYNTO_ARTIFACT_URL="$reviewed_wheel_url" \
--build-arg SYNTO_SHA256="$reviewed_wheel_sha256"
```

The build requires a concrete numeric version, a PyPI HTTPS wheel and a lowercase
SHA-256, verifies downloaded wheel bytes through pip's hash fragment, and checks
public `synto --version`. The runner checks that same expected version and records
the actual public output version, including on mismatch. No floating `latest`
lookup exists. A new version still needs parent public-CLI compatibility and live
validation; receipt semantics never claim vendor internal completion.

### Prepare a pinned DEV snapshot outside the inference container

Use the existing Go importer with the parent's existing ADC setup. This is the
only command here allowed to use cloud credentials, and it runs **on the host**.
It pins current once, reads exact GCS object generations, verifies manifest
size/digests and corpus bodies, and writes local provenance. It performs no
cloud writes. The specific generation read is in `snapshot/import-provenance.json`;
the Project locator does not promise a historical generation.

```sh
(cd apps/bff && GOCACHE=/private/tmp/jevimpl-go-cache go build \
  -o "$experiment/query_experiment-host" ./cmd/query_experiment)
printf '{"operation":"import","snapshot":"gs://llm-wiki-data-dev/users/test-user/projects/synto-migration-e2e-20260726-v12b","destination":"%s/snapshot"}\n' \
  "$experiment" | "$experiment/query_experiment-host" local-platform \
  > "$experiment/import-receipt.json"
# Supply the parent's existing corpus-specific cases; DNS cases are unrelated.
cp /private/tmp/lwc-gcs-cases.jsonl "$experiment/cloud-cases.jsonl"
container run --rm --read-only --platform linux/arm64 \
  --volume "$experiment:/experiment" --env DEEPSEEK_API_KEY "$image" \
  --launcher-metadata /experiment/image.json \
  --output /experiment/cloud-run --input-snapshot /experiment/snapshot --from 60 \
  --cases /experiment/cloud-cases.jsonl --require-grounded --timeout 600 \
  --query-config /experiment/query.json \
  --query-profile corpus-derived-tech-document-v1 \
  --query-prompt domain-neutral-technical-v1 > "$experiment/cloud-run.log" 2>&1
```

The local snapshot bytes (including import provenance) are copied into the run
and content-bound by the source digest. Local imports retain the existing local
Project/content-derived generation semantics; the cloud receipt is provenance,
not a claim of remotely revalidating the frozen directory. There is no ADC/home
mount and `gs://` input is rejected in the experiment image.

### Receipts, forks and native debugging

`run.json.environment` is versioned `lwc-experiment-environment-v1`: source
revision, dirty flag, build ID, actual binary OS/architecture and versioned launcher
metadata with image digest. Existing `source`, query-config digests, copied cases,
config, input and checkpoints bind input/config provenance. Public Synto version
is recorded for every container run, including query-only runs. The core runner
receipt remains `lwc-local-go-v2`, preserving compatible native forks; environment
metadata is additive and does not replace binary/checkpoint identity checks.

```sh
container run --rm --read-only --platform linux/arm64 \
  --volume "$experiment:/experiment" --env DEEPSEEK_API_KEY "$image" \
  --launcher-metadata /experiment/image.json \
  --fork /experiment/run --only 100 --output /experiment/synthesis-comparison \
  --cases /experiment/cases.jsonl --query-config /experiment/query.json \
  --query-profile corpus-derived-tech-document-v1 \
  --query-prompt domain-neutral-technical-v1 --require-grounded \
  > "$experiment/synthesis-comparison.log" 2>&1
```

Native execution remains for tests/debug: build `./cmd/query_experiment` and
`./cmd/olw_worker` from `apps/bff`, then use `query_experiment staged` with the same
flags and `--worker-bin` pointing to the host worker. Without `--experiment-root`,
existing native path semantics apply. Native receipts explicitly say `native`,
use available Go VCS metadata (or `unknown`), and make no image-digest claim.
`--query-bin` and `--synto-python` remain absent. The experiment binary rejects
legacy and `local-platform` command modes; use the native binary for host import.

## Stages and dependencies

Exactly one selection mode is required; names and ordinals are accepted.
`--only 90,70,80` runs 70,80,90. `--to selection` runs through 90;
`--from suggested-queries` runs 60–100. Duplicate, unknown, ambiguous, and
missing-dependency selections fail. Skipped stages are never implicitly run.

| Ordinal | Name | Executes / required frozen predecessor |
| --- | --- | --- |
| 10 | source | Public init and exact raw Markdown copy; requires `--raw` |
| 20 | synto-run | Public ingest/compile/lint/retry/approval pipeline; prepared raw/config with **no DB** |
| 50 | index | Public agents pack export plus existing worker identity join/materialization; successful Go stage20 receipt and native DB |
| 60 | suggested-queries | Existing worker generator; valid concepts/wiki snapshot; requires fresh valid 20-query output |
| 70 | expansion | Production structured expansion; corpus plus strict cases |
| 80 | matching | Production lexical matching; compatible saved expansion |
| 90 | selection | Production evidence selection; compatible saved matching |
| 100 | synthesis | Production answer/citation resolution; compatible saved selection |

There are no 30/40 stages. Public `synto run --auto-approve --max-rounds 2
--min-confidence 0` owns its native ordering; `--fix` is absent. Initialization,
configuration, import and reporting are platform operations. All query stages
call the existing `executeLocalPlatform` / sealed `queryruntime.NewExecutor`
composition in-process, with the stage context and deadline. No subprocess calls
query_experiment itself. Timeouts are per stage/child, including all query cases.

Manual `--cases` is independent of 60; omit suggestions with
`--only 10,20,50,70,80,90,100`. `--suggested-cases` is exclusive with `--cases`
and uses newly produced or explicitly saved suggestions. Synto is neither
required nor probed for native 60–100; container runs always probe the bundled
public version for environment provenance. Worker is required only for 50/60.

## Public Synto contract and isolation

`--synto-bin` defaults to `synto` on PATH. The characterized installed version is
0.7.0. Public `init --help`, `run --help`, and `pack export --help` were inspected.
There is **no `synto index` command**; production index export is
`synto pack export --target agents --out ...`.

Real isolated `synto init <private-vault> --non-interactive` was verified offline:
it preserves an existing `synto.toml` with an empty private config home, creates
layout/schema/index and a local Git repository, but creates neither a commit nor
a state DB. `--default` is never passed. The runner sets private HOME and
XDG_CONFIG_HOME; initialization also disables system/global Git config reads.
No global setting is written. It does not import private Python helpers, select
a Python interpreter, install anything, or assume a user's home path.

Public `run` may exit zero after partial concept failures. Stage20 therefore
requires a successful run exit, a successful public agents pack export, an INDEX
accepted by production's `wikiindex.DecodeSyntoIdentityPlan`, nonempty actual
bodies at every exported article path, at least one non-root article, and unchanged
raw bytes/mtimes. Missing, empty, malformed, unsafe or unusable exports fail.
The temporary export is removed after validation; stage50 retains production's
export/enrichment/materialization algorithm.

**This is producer output acceptance, not exhaustive Synto internal success.**
The installed 0.7.0 `run --help` has no structured failure-report option;
`status --help` offers human-readable `--failed` diagnostics but no structured
format. The runner does not parse those logs or infer that every raw compiled,
that every pending concept completed, or that all drafts were published. A partial
run with usable output and no reported failures can pass. Positive public INDEX
`failed_note_count` or `failed_concept_count` fails acceptance; zero counts do not
prove exhaustive completion. INDEX statistics are not treated as a complete
per-input execution report. Exhaustive completion needs a supported public
machine-readable contract; it is currently **not proven**.

`.synto/local-run.json` records
`validation: "public-exit-and-validated-agents-export"` and
`internal_completeness: "not_proven"`. It binds opaque native DB/raw/wiki/config
bytes, the public version, and resolved executable identity. DB contents are never
queried. Older receipts with native-schema validation cannot authorize stage50.
Pack export failure still fails stage20. Native DB presence/freshness and snapshot
integrity checks remain filesystem safety gates, not SQL schema assumptions.

The private config disables `auto_commit` and `auto_maintain`. Credential-free
HTTPS provider/model flags are `--synto-provider`, `--synto-url`, `--synto-fast`,
`--synto-heavy`, with the existing `LWC_SYNTO_*` environment defaults. Defaults:
custom / `https://api.deepseek.com/v1` / `deepseek-flash` for both Synto models.
The provider's key name is `DEEPSEEK_API_KEY`. Child environments allow only
explicit execution settings and, for inference children, DeepSeek/Synto key
variables. Provider stdout/stderr is discarded; bounded worker categories and
exact exit codes survive. Child process groups are terminated on deadline,
SIGINT, or SIGTERM; descendants cannot remain running after a stage returns.

Query config is independently sealed using existing production contracts.
Explicit `--query-profile` / `--query-prompt` must be supplied together and bind
the chosen existing profile to exact Project/generation/concepts identity.
The DNS fixture uses query expansion `deepseek-v4-flash` and synthesis
`deepseek-v4-pro`; worker suggestions retain `deepseek-chat`. These do not borrow
Synto's model configuration. No Profile/Tagger/router schema is introduced.

## Sources, checkpoints and forks

Choose exactly one source: `--raw`, `--input-snapshot`, or `--fork`.
Local sources must use canonical paths, contain only regular files/directories,
and stay within 512 MiB / 10,000 filesystem entries / 128 directory levels.
Symlinks, special files, changing files, and output beneath a donor are rejected.
Copies use new files and preserve nanosecond mtimes; DBs are never hardlinked.

A local snapshot requires `cache/concepts.jsonl` and every referenced wiki or
draft body. It gets an explicit local Project identity and content-derived
generation. A GCS locator identifies a **Project**, not a Profile. The importer
reuses `PinCurrentGeneration`, pins the published manifest once, reads exact
object generations, verifies declared sizes/digests, and validates every concept
body before publication. Manifest limits reject >10,000 files, >64 MiB per
object, or >512 MiB total before object reads. Import failure discards partial
local publication. Imported mtimes explicitly use manifest `created_at`; they
cannot reproduce unavailable original cloud mtime ordering. No moving raw
paths are fetched. Historical generation selectors and legacy cloud layouts
remain unsupported. Parent previously observed
`g_5356a1f994816ccd852563990d2d79c6`; compare the new pinned receipt, which may differ.

New receipts declare `schema: "lwc-local-go-v2"`. Go JSON hashing intentionally
has a different version from Python's canonical hashing. **Python/schema-1 forks
are rejected**, without modifying their evidence. To use an old corpus, explicitly
import its `snapshots/50` or `snapshots/60` with `--input-snapshot ... --from 70`
and run fresh query stages. Old native stage20 receipts cannot authorize new
stage50 execution. Never relabel old receipts to bypass compatibility checks.

A Go fork uses the last successful checkpoint before its first selected stage.
For native debugging, fresh synthesis after a compatible new native run uses
this command shape (the container fork command is above):

```sh
/private/tmp/jevimpl-go-e2e-tools/query_experiment staged \
  --fork /private/tmp/lwc-go-dns-baseline-20260920 --only 100 \
  --output /private/tmp/lwc-go-dns-synthesis-comparison-20260920 \
  --cases scripts/local_e2e/fixtures/cases-dns.jsonl \
  --query-config scripts/local_e2e/fixtures/query-dns.json \
  --query-profile corpus-derived-tech-document-v1 \
  --query-prompt domain-neutral-technical-v1 --require-grounded
```

Fork checks bind the runner/binary implementation, relevant worker/Synto
implementation, snapshot bytes/mtimes, exact cases, upstream query-config
semantics, and predecessor digests. Synto identity uses the public version plus resolved executable path and SHA-256.
For a launcher, this does **not** fingerprint the installed package, interpreter or
dependencies; same-version package changes behind an unchanged launcher are not
detected. There are no site-packages assumptions or install-wide hashes. Changing
only synthesis settings permits retrieval reuse; changing upstream semantics
requires fresh expansion. Gapped selections retain compatible saved artifacts;
changed predecessors invalidate descendants and block skipped dependencies.

Run layout: `run.json`, immutable `input/`, copied `query-config.json` and
`cases.jsonl`, mutable `work/users/<uid>/projects/<pid>/`, `snapshots/<ordinal>/`,
`artifacts/70.json` through `100.json`, and private `home/` / `xdg/`.
Failure and checkpoint receipts are written atomically. An interrupted run keeps
successful earlier checkpoints. Existing outputs are never overwritten and there
is no resume/reset-in-place; explicitly choose a new output directory.

Mechanism failure exits 1, preserving typed query observations and safe failure
diagnostics. A truthful `no_grounded_answer` is a completed observation.
`--require-grounded` requires stage100 and exits 2 when any case lacks an answer
or resolvable citation inventory. This is a minimal smoke, not factual accuracy,
per-claim support, or complete inline-marker validation. Mechanism, oracle and
execution mode are separate fields; a `live_providers` label is not proof a
provider call occurred. Source-only execution makes none.

## Verification and historical evidence

Current Go verification and command exits are recorded in
[the migration report](local-staged-go-migration-report.md). Offline tests cover
modes, dependencies, native artifacts, forks/siblings, corruption, exact binding,
config changes, gapped reuse, cloud cleanup, bounded files, process-tree timeout,
negative observations and direct production execution with **mock HTTP**.
Real public init is provider-free. The parent subsequently verified the container
build/mount, uninterrupted live DNS stages 10/20/50/60/70/80/90/100 with grounded
quality passed, and imported DEV stages 60–100 with mechanism success but grounded
exit 2 (`citation_routes_rejected`, LWC-335). A cloud ONLY80 fork from checkpoint70
succeeded without a key. See the [current evidence table](local-staged-container-report.md#parent-verified-build-mount-and-live-results). Parent reports the full unrestricted seven-package test command passed before
this public-boundary revision; focused revision checks are in the migration report.

Historical **Python runner** results remain valid only for their original code:

| Existing `/private/tmp/` evidence | Recorded result |
| --- | --- |
| `lwc-live-dns-baseline` | 10/20/50 passed; native ingest1/compile4/publish4; 60 failed |
| `lwc-dns-suggested-diagnostic` | Imported baseline snapshot50; fresh ONLY60 passed with 20 candidates |
| `lwc-dns-query-gate2` | Fork FROM70; original two DNS cases passed minimal grounded smoke |
| `lwc-cloud-downstream` | Real pinned DEV import and 60–100 passed mechanically; grounded oracle failed |

This DNS lineage was not one uninterrupted all-stage success. The original
stage60 failure cause is unrecoverable; later stochastic success does not prove
what fixed it. DNS answers have `.example` and/or `.test` citations, but inline
`[RFC 2606]` / `[Reserved Top Level DNS Names]` remain unresolved. Before LWC-335, the cloud case's `stable alpha` slug hit the production
whitespace restriction, and direct/staged mocked synthesis reproduced an empty
inventory. That historical `citation_routes_rejected` diagnosis remains in the
original artifacts. Current behavior is described below; no frozen slug or map
was renamed or repaired.

The owner accepted Synto as a single stage for the first mechanism version.
[LWC-335](https://irisnode.youtrack.cloud/issue/LWC-335) implements the shared
citation fix after this migration; its local verification does not turn the
legacy cloud fixture or live acceptance positive.
Parent previously passed 16 Python tests and full original Go suites with frozen
evidence; those are historical checks, not verification of this Go migration.
All external runs, fixtures, previous production diffs, diagram and handoff stay
intact. Existing query experiment matrices/eight-file receipts, worker publishing,
Makefile pipeline tools and local API/frontend smoke retain their original roles.


## LWC-335 canonical citation acceptance — 2026-09-20

Query synthesis reads `cache/id_map.json` through the same request-bound reader
used for retrieval. It resolves exact `(type, slug)` matches before granting
citation authority, returns the canonical `id` plus escaped ID-and-slug `path`,
and preserves retrieval slugs and display labels. Hydrated result cards also
receive the resolved ID. Concept IDs come directly from `concept` (normal Synto
entity IDs or existing valid legacy IDs); `concept_entity_id` is not a substitute.
Source citations use `source` and hydrate source pages. Missing, malformed,
ambiguous or unsafe mappings fail synthesis explicitly before any LLM call.
Existing traversal/injection checks remain; ordinary internal Unicode spaces
are accepted only with valid mapped identity. Wiki target/label rewriting is
unchanged. HTTP ID redirects now escape the path segment. HomeClient already
prefers `citation.id`; new component tests exercise that behavior and full-page
links for both concept and source Unicode/whitespace slugs.

The local snapshot reader now permits the bounded `cache/id_map.json` artifact
and source pages, and freezes the map (including a missing-map error) with
the corpus at preflight. It performs no citation conversion: direct and staged paths
both use the production query synthesizer.

Local deterministic acceptance includes map/route negatives, request-map
isolation, explicit mocked LLM calls, direct/staged citation equality, HTTP
ID-only redirect followed by article detail, and frontend inline-click/detail
link behavior. Original rejection evidence is retained in the starting-file
archive and historical run artifacts; portable fixtures now carry explicit
canonical maps rather than weakening production validation.

Frozen downstream replay results (saved stage90 selection and stage100 answer,
explicit mock HTTP, no new provider inference):

| Read-only snapshot | Current observed result |
| --- | --- |
| `live-dns` | `dns-testing`: 4 citations; `example-domains`: 2. Both saved answers have zero unresolved bracket labels, including `Reserved Top Level DNS Names`; every citation has a nonempty snapshot article. |
| `cloud-run` | Explicit mapping failure before LLM invocation. Its `concept` map uses `stable-alpha`, not a valid route ID, and source ID `s1` is also invalid. No legacy-ID guess or use of the compatibility side map. Historical answer still has three unresolved `[stable alpha]` occurrences. |

Evidence: `/tmp/lwc335-dns-replay-final.log`,
`/tmp/lwc335-cloud-replay-final.log`, fresh JSON under
`/tmp/lwc335-replay-final.6mGtyk/`, and `/tmp/lwc335-verification.md`.
These are native local tests, not a new container image, browser E2E, or live run.
Parent unrestricted six-package Go tests passed (`/tmp/lwc335-parent-tests.log`);
parent citation component tests passed 10 tests (`/tmp/lwc335-parent-component.log`).
Component mocks do not verify real browser navigation. Those checks did not include fresh image/live/browser acceptance; the later
delivery sections below record their completion. Original snapshots remain read-only.
Optional replay requires `LWC_E2E_CITATION_EXPECT=mapped` (selected citation
count) or `rejected` (identity failure), alongside `LWC_E2E_CITATION_RUN`.
The expectation is independent of the directory name. Bounded review results:
`/tmp/lwc335-review-report.md`.

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
