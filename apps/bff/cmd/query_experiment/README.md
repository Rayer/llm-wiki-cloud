# query_experiment

`query_experiment` runs query cases against a frozen Project snapshot. It is an
operator tool for designing controlled runs and inspecting attempt-level
results; it is not a quality verdict or an automated control/candidate
comparison tool.

## Container-first staged experiments

### Documentation completion gate

Keep this directory's README, `docs/local-staged-e2e.md`,
`docs/local-staged-container-report.md`, and the
[YouTrack experiment platform portal](https://irisnode.youtrack.cloud/articles/LWC-A-25)
updated together after final verification. An issue comment alone is insufficient.
Record actual source/image identity, commands, outcomes and remaining limitations;
distinguish mock/replay, container/live, browser and deployed evidence.
Post-fix fresh image, mounted/live DNS pipeline, single-stage replay and real
browser citation acceptance now pass; see the dated evidence sections below.
Historical runs retain their original limitations; the parent owns portal sync.

Use one existing experiment directory, one bind-mounted OCI image, and the Go
staged runner. Put raw Markdown (or a host-imported snapshot), sealed config and
cases in the directory; pass a **new child**, such as `/experiment/run`, as output.
The `experiment` target in `../olw_worker/Dockerfile` shares the production worker
runtime and includes query_experiment, worker and public Synto. The default
production target and `/worker` entrypoint are preserved.

Follow the [exact Apple container build, inspect, smoke, live and fork commands](../../../../docs/local-staged-e2e.md#one-directory-one-container).
The image requires external runtime-inspected image metadata, records build/source
identity and actual public Synto version, and keeps inputs, results, checkpoints,
private runtime directories and safe logs under the mount. Supply the key only
through the launch environment. Prepare pinned DEV snapshots read-only with the
native Go importer before launch; mount no host home or cloud credentials.

The parent verified Apple Container client/server 1.4.1, the image build and
mounted smoke under `/private/tmp/jevimpl-experiment.9Vw7HK`. Live DNS passed all
stages in one uninterrupted run and passed the grounded-answer smoke. Imported
DEV stages 60–100 succeeded mechanically but exited 2 on grounded quality
(`citation_routes_rejected`, LWC-335); a cloud ONLY80 fork succeeded without a key.
See the [container implementation report](../../../../docs/local-staged-container-report.md#parent-verified-build-mount-and-live-results)
for exact image digest, versions, receipts and remaining limits. Native modes below remain useful for
tests/debug and existing experiment matrices.

## Unified local staged pipeline/query runner

For raw → Synto → LWC index → production query/synthesis/citations, run
`query_experiment staged --help` and use the
[local staged experiment runner](../../../../docs/local-staged-e2e.md). It also
imports one pinned published GCS generation read-only for local `FROM 60`
execution, and supports `ONLY`, `TO`, `FROM` plus isolated checkpoint forks.
An input snapshot locator identifies a Project, not a user Profile document.

The Go runner directly calls this executable's existing local handler for strict
config/case validation, pinned import and per-case production stage replay.
Synto uses its public executable (`--synto-bin`, default `synto`); worker index
and suggestions use `--worker-bin`. No Python interpreter or `--query-bin` is
needed. Receipts use `lwc-local-go-v2`; old Python forks fail explicitly. Import
an old corpus snapshot with `--input-snapshot ... --from 70` for fresh queries.
It uses sealed `queryruntime` composition; the existing `--service
production` path below keeps its legacy composition and CLI compatibility.
Existing fixture matrices and eight-file receipts remain available. Their
unbound artifacts are not interchangeable with the new runner's replay records.

Offline verification covers public Synto initialization, orchestration and
production execution with mock HTTP. Parent container/live verification is
recorded above; public vendor internal completeness remains `not_proven`. See the [migration report](../../../../docs/local-staged-go-migration-report.md)
for commands/exits and the sandbox listener limitation on the full query suite.

Historically, the parent verified the Python raw-to-query mechanism across an explicit frozen
lineage: `/private/tmp/lwc-live-dns-baseline` passed 10/20/50, its checkpoint 50 fed
successful ONLY60 `/private/tmp/lwc-dns-suggested-diagnostic`, then FROM70 fork
`/private/tmp/lwc-dns-query-gate2` passed the original two-case grounded smoke.
This was not one uninterrupted successful all-stage invocation. The original
stage60 failure cause is unrecoverable; the successful retry does not prove a fix.

Those historical DNS answers have resolvable `.example` and/or `.test` inventory, but inline
`[RFC 2606]` / `[Reserved Top Level DNS Names]` remain unresolved. The oracle is
an answer-plus-inventory smoke, not per-claim or complete inline-citation validation.
Parent reviewed the answers/evidence; production citation compatibility is not fixed.

The real read-only DEV import and stages 60–100 at
`/private/tmp/lwc-cloud-downstream` passed mechanically; its grounded oracle still
failed because the sole `stable alpha` slug violates the existing whitespace
route restriction. Causal tests match direct production synthesis. No identities
were renamed or citations fabricated. Full cloud positive grounded acceptance
remains open. Parent's pre-migration full worker, query_experiment, suggestedqueries,
search, query, queryquality and llm suites all passed without query-test exclusions.

The owner accepted the first-version local experiment mechanism with native
Synto as one stage. Stage20 bundles ingest/compile/validation/approval; independent
substage selection (the original separate 20/30/40 proposal) is outside that scope.
The existing Query citation defect is a separate follow-up,
[LWC-335](https://irisnode.youtrack.cloud/issue/LWC-335) (Bug, Open, Normal;
parent created and read-back verified). Its repair is excluded from first-version
mechanism acceptance, while platform failure diagnostics remain. This decision
does not make the cloud positive grounded test pass or validate every inline
citation. GCS current is pinned once; explicit historical-generation selectors
remain unsupported. Prior tools are retained.
The runner binds explicitly selected existing profiles to the exact generation,
records safe failure categories, and separates completed no-evidence observations
from the optional `--require-grounded` positive oracle. Native cloud downstream needs
no Synto installation; query-only replay needs no worker executable.
See the linked guide for tested build/test commands, model settings, stage
semantics, forks, stop/reset and the parent verification command.

## Start here

The basic production control uses operator-provided files. The repository does
not bundle a snapshot or a case file, so this is a command shape, not a
copy-paste run:

```sh
cd apps/bff
go run ./cmd/query_experiment \
  --service production \
  --snapshot <local-frozen-snapshot> \
  --cases <strict-cases.jsonl> \
  --runs 1 \
  --output <control-results.jsonl>
```

`--cases` and `--suggested-query-mode` are alternatives for supplying cases:
at least one is required. If both are supplied, published suggested queries
from the snapshot are appended to the explicit cases.

The service defaults to `production`. `--service query-retrieval` is a
trusted-local retrieval smoke or fixture run; it returns selected identities,
not production synthesis or citation resolution. A smoke without a fixture
matrix needs no fixture files:

```sh
cd apps/bff
go run ./cmd/query_experiment \
  --service query-retrieval \
  --snapshot <local-frozen-snapshot> \
  --cases <strict-cases.jsonl> \
  --runs 1 \
  --selection-limit 10 \
  --exploration-slots 1 \
  --evidence-threshold 2 \
  --keywords-per-attempt 24 \
  --expansion-attempts 3 \
  --rare-keyword-max-document-frequency 1 \
  --seed 7 \
  --output <smoke-results.jsonl>
```

This path loads optional `config.toml` from `--config-dir` (the current
directory by default). With a configured DeepSeek key it uses the structured
expander; without one it uses the deterministic fallback. Either way, this is
a wiring check, not model-quality evidence. Use the fixture matrix below for
explicit provider/model/prompt/profile comparisons and attempt receipts.

## Experiment design

Freeze the snapshot identity, case set, service, fixture selections, and knobs
for a comparison. Change one factor at a time; keep `--runs` and `--seed`
stable unless they are the factors under test. The CLI runs cases and runs
sequentially. Query-retrieval expansion attempts are bounded and run in
parallel. A missing seed is derived from the query; an explicit signed
64-bit `--seed` makes selection replayable when the selection input is the
same.

The snapshot is an input, not an output. A local snapshot must already contain
`cache/concepts.jsonl`; `cache/suggested_queries.json` is also required when
`--suggested-query-mode` is used. Wiki page bodies are read from `wiki/<slug>.md`
or `wiki/.drafts/<slug>.md` when the selected query mode needs them.

Choose exactly one snapshot locator:

```sh
# local path or canonical gs:// Project-root URI
--snapshot <path-or-gs-uri>

# split GCS identity; all three flags are required
--gcs-bucket <bucket> --gcs-user-id <user> --project-id <project>
```

`--snapshot` cannot be combined with any split GCS flag, including
`--project-id`. A canonical GCS URI has the form
`gs://<bucket>/users/<user-id>/projects/<project-id>`.

## Cases

Cases are strict JSONL: one object per line, no blank lines, duplicate fields,
unknown fields, or trailing JSON. There must be at least one case and no more
than 1000. Required fields are `id`, `query`, and `mode`; `mode` is `wiki` or
`full`. Optional labels are used only by summary metrics:
`known_positive_slugs`, `forbidden_result_slugs`, and `tags`.

```json
{"id":"coffee","query":"coffee shops","mode":"wiki","known_positive_slugs":["coffee-shop"],"forbidden_result_slugs":["espresso-machine-ad"],"tags":["smoke"]}
```

## Query-retrieval fixture matrix

Fixture mode is enabled only for `query-retrieval` when all three fixture
paths and `--artifacts-dir` are supplied. It evaluates the Cartesian product
`profiles × prompts × models`; selectors are comma-separated, preserve the
listed order, and select every fixture entry in fixture-file order when empty.
Unknown or duplicate selector IDs fail before execution. Production ignores
fixture flags, and the no-fixture query-retrieval smoke remains available.

The three fixture files are strict JSON objects with these exact envelopes.
Each model must include `api_key`; it is trusted-local input and is never a
credential source managed by this command. Do not commit it.

```json
{"models":[{"id":"model-id","provider":"deepseek","base_url":"https://api.example.invalid/v1","model":"model-name","api_key":"trusted-local-secret","temperature":0,"reasoning":"none"}]}
```

```json
{"profiles":[{"id":"profile-id","required_when_explicit":["location"],"preferred_by_default":["topic"],"goals_to_expand":["discovery"]}]}
```

```json
{"prompts":[{"id":"built-in-prompt-id","system_template":"<exact built-in system template>","user_template":"<exact built-in user template>","template_digest":"sha256:<64 lowercase hex characters>"}]}
```

Model entries require `id`, `provider`, `base_url`, `model`, and `api_key`;
`temperature` and `reasoning` are optional. Profile entries require all three
arrays. Prompt entries require the built-in prompt ID, exact production-owned
templates, and their matching digest; templates may use only the placeholders
accepted by the built-in prompt contract (`{{raw_query}}` and
`{{criterion_policy}}`). Fixture IDs contain only letters, digits, `.`, `_`,
or `-`.

The matrix requires operator-owned snapshot, cases, and all three fixture
files, so use placeholders until those files exist:

```sh
cd apps/bff
go run ./cmd/query_experiment \
  --service query-retrieval \
  --snapshot <frozen-snapshot> \
  --cases <strict-cases.jsonl> \
  --runs <1-100> \
  --artifacts-dir <artifacts-dir> \
  --summary <summary.json> \
  --output <results.jsonl> \
  --model-fixture <models.json> --models <model-ids> \
  --profile-fixture <profiles.json> --profiles <profile-ids> \
  --prompt-fixture <prompts.json> --prompts <prompt-ids>
```

## Flags and frozen factors

`--runs` is `1..100`. Query-retrieval defaults are selection limit `10`,
exploration slots `1`, keywords per expansion attempt `24`, expansion attempts
`3`, and rare-keyword maximum document frequency `1`. Their flags are bounded
by the CLI; `--evidence-threshold` is non-negative and explicit zero is the
trusted-local legacy control. `--config-dir` selects optional `config.toml`.

Other identity flags are `--generation-id` (explicit local generation identity)
and `--concepts-digest` (expected frozen concepts SHA-256). Output paths are
`--output` (JSONL; stdout when omitted), `--summary` (fixture summary JSON),
and `--artifacts-dir` (fixture receipts).

## Outputs and receipts

Every fixture attempt writes exactly eight files below
`<artifacts-dir>/<variant-id>/<case-id>/run-<n>/` (case IDs containing path
separators are replaced by a bounded digest segment):

```text
request.json
expansion.input.json
expansion.output.json
matching.input.json
matching.output.json
selection.input.json
selection.output.json
final.json
```

The receipts preserve the ordered `expansion`, `matching`, and `selection`
stages, their inputs and outputs, the parsed or fallback plan, candidate
evidence, selection decisions, final identities, frozen-corpus identity,
effective seed, and timing/usage metadata. Retrieval output contains selected
identities only. `--output` contains one result record per case-run attempt.

## Summary metrics

The summary uses these JSON field names. Rates are the stated numerator divided
by denominator; inspect both fields where present, especially for labeled
metrics.

- `zero_result_rate`: `zero_result_count / attempt_count`.
- `under_5_rate`: `under_5_count / attempt_count`.
- `recoverable_under_5_case_rate`: cases with minimum result count `< 5` and maximum `>= 5`, divided by cases.
- `always_under_5_case_rate`: cases whose every run has fewer than 5 results, divided by cases.
- `exact_result_set_match_rate`: runs after the first matching the first run’s result set, divided by `runs - 1` for repeated cases.
- `mean_pairwise_top_5_jaccard` and `mean_pairwise_top_10_jaccard`: mean pairwise Jaccard similarity of the corresponding top-N slug sets; `pairwise_comparison_count` is the denominator.
- `score_changed_candidate_rate`: candidate slugs with differing scores across 2+ runs divided by candidate slugs observed in 2+ runs.
- `exact_selection_replay_rate`: identical selection outputs divided by repeated attempts with identical effective seed and selection input digest.
- `fallback_rate`: `fallback_count / attempt_count`.
- `known_positive_recall_at_5` and `known_positive_recall_at_10`: labeled known-positive slugs found in the top 5 or top 10, using the matching `_numerator` and `_denominator` fields.
- `forbidden_result_violation_rate`: attempts containing a forbidden slug divided by attempts with at least one forbidden label.

Other fields report counts, result-count min/max/mean/stddev, latency
(`latency_min_ms`, `latency_mean_ms`, `latency_p95_ms`), and token totals
(`prompt_tokens_total`, `completion_tokens_total`, `total_tokens_total`,
`token_usage_attempt_count`). A zero denominator currently emits JSON `0`; it
must be interpreted as N/A, not as a measured zero rate.

## Stage-config generation

`--stage-config-output` is a query-retrieval fixture operation requiring
`--config-revision` and exactly one selected profile, prompt, and model. The
selected model must be the allowlisted `deepseek` / `deepseek-v4-flash` with
`reasoning: "none"` and `temperature: 0`; the prompt must be a matching
production-owned built-in template. The sealed canonical file contains no API
key, base URL, prompt text, or snapshot path.

Because `resolveSnapshotLocator` rejects `--snapshot` together with
`--project-id`, generate a stage config through the split GCS identity path:
provide `--gcs-bucket`, `--gcs-user-id`, and `--project-id` (plus the fixture
flags, `--artifacts-dir`, `--config-revision`, and `--stage-config-output`).
The pinned GCS snapshot supplies generation and concepts identity. A local
`--snapshot` run cannot supply the required project binding through this CLI’s
current flag contract.

This command writes the sealed file; it does not publish or promote it.

## Reproducibility limits

For a meaningful comparison, keep the snapshot digest, cases, fixtures,
selectors, service, configuration, and knobs fixed. Explicit seeds make the
selection input replayable, and exact-selection metrics are scoped to identical
effective seed plus selection-input digest. Results can still vary with model
provider nondeterminism, provider availability, network behavior, and any
change to the frozen input or built-in implementation.

## Security boundaries

Fixture `api_key` values are trusted-local inputs. The command sends them as
Bearer credentials to the fixture `base_url`, but scrubs API keys and base URLs
from emitted fixture evidence, result JSON, summaries, and errors where the
sanitizer applies. Receipts can contain rendered prompts and scrubbed raw model
responses, so keep fixture inputs and queries appropriate for local review.
Concept bodies and snippets are not emitted in fixture receipts. The command
does not create, rotate, or publish credentials.

## Troubleshooting

- `snapshot and split GCS flags are mutually exclusive`: remove `--snapshot` or all three split flags; `--project-id` counts as a split flag.
- `snapshot or all split GCS flags are required`: provide one complete locator.
- `cases is required`: provide `--cases`, `--suggested-query-mode`, or both.
- Fixture validation errors: all three fixture files and `--artifacts-dir` are required, envelopes are strict, and selectors must name unique IDs.
- Stage-config validation errors: reduce selections to one profile/prompt/model, add `--config-revision`, use the allowlisted model settings, and use the split GCS identity path.
- Missing snapshot artifacts: provide `cache/concepts.jsonl`; add `cache/suggested_queries.json` for suggested-query generation and required `wiki` pages for the selected mode.

## Related documentation

- [queryquality README](../../internal/queryquality/README.md)
- [queryconfig README](../../internal/queryconfig/README.md)
- [experiment report](EXPERIMENT_REPORT.md)

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
