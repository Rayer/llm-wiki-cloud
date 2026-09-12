# LWC-331 DeepSeek execution inventory

Canonical outbound model: `deepseek-flash`. Model migration changes no prompts,
retrieval settings, token budgets, timeouts, provider URLs or engine version.

| Workload / authority | Execution seam | Explicit thinking |
| --- | --- | --- |
| BFF parallel Query expansion; sealed artifact or legacy env/defaults | `cmd/bff/main.go` → `internal/llm.Client` | disabled; temperature 0, existing three-attempt fallback preserved |
| BFF grounded synthesis and full-mode model-prior fallback | same client; selected sealed/legacy synthesis reasoning | `none` → disabled; low/high/max → enabled with unchanged effort |
| Worker suggested query chips, including generation fallback | `newSuggestedQueryClient` → `llm.NewClient` | disabled, unchanged |
| Worker ingest/compile and all allowed Synto CLI children | embedded `synto_execution.py`, after pinned `Config.resolve_role` | existing native options win; otherwise chat → disabled, reasoner/V4/Flash → enabled |
| New Worker Synto and legacy template | fast/heavy model profiles | explicit disabled/enabled respectively; context limits unchanged |
| Existing Synto config and coherent legacy wiki migration | same execution seam after normal config/migration loading | preserves effective native options and legacy model semantics; no persistent TOML rewrite |
| Query experiment default Query service and retrieval executor | `llm.NewClient`; record metadata uses canonical name | disabled, unchanged |
| Query experiment selected fixture HTTP caller | fixture decoding and `callFixtureModel` | none → disabled, explicit effort → enabled; omitted effort preserves chat disabled / other known models enabled |
| Query experiment stage-config exporter | `buildStageConfig` | canonical stages with existing none/0 contract |

`CanonicalDeepSeekModel` accepts only the observed migration inputs: Flash,
V4 Flash, V4 Pro, chat, reasoner (and empty default). Legacy BFF stage env
validation accepts only its previously valid stage alias plus Flash. Historical
sealed config decoding accepts its known stage aliases; effective resolver and
client identities are canonical, including receipts/readback. Unknown models
remain invalid. Fixture files and their historical outputs are untouched; newly
decoded DeepSeek variants derive identity from effective model/reasoning.
Non-DeepSeek fixture providers and Synto roles retain their existing behavior.

## Pinned engine contract

Production wheel (unchanged): Synto 0.7.0,
SHA-256 `4bc8dcf14b53f45fac32ce737ecf878f1a46d6d0b010c7decbe6c3b7b10afa77`,
[exact wheel](https://files.pythonhosted.org/packages/4a/e9/41c6b61338d98820780a43ed075cd77525674c38242110435330771d771b/synto-0.7.0-py3-none-any.whl).
Inspected wheel sources:

- `synto/config.py:702-792`: resolves provider, profile and CLI overrides; native
  provider options are merged before role options.
- `synto/client_factory.py:83-106`: resolved model/options feed role endpoints.
- `synto/openai_compat_client.py:557-610`: `think` is a no-op; native options are
  merged last into the actual request. A model-only CLI override cannot preserve
  chat/reasoner semantics. Existing `options.model` is also normalized.
- `synto/cli.py:250-300`: native CLI overrides expose model/provider, not options.
  The wrapper uses the original CLI entry point, `Config.resolve_role`, and
  `Config.model_name` for effective provenance. Version mismatch fails immediately;
  no engine file is patched on disk.

The wrapper rejects unknown DeepSeek models, thinking objects, effort values and
embedding roles before inference. Other native options retain their original
precedence; the adapter does not invent token budgets or remap effort. New `model_name()` compile/checkpoint metadata uses the resolved model, matching
role endpoints and HTTP payloads. A DeepSeek-only resolved-model subclass includes
effective thinking/effort in both client deduplication and cache namespace, so
alias collapse cannot reuse a non-thinking answer for a thinking request. Existing
cache rows stay untouched; same-policy cache hits remain available. Other native
options keep the pinned engine's cache behavior. No forced pipeline run/rebuild
is introduced; the engine evaluates checkpoints normally during an authorized run.

[DeepSeek model documentation](https://api-docs.deepseek.com/quick_start/pricing/)
and [thinking documentation](https://api-docs.deepseek.com/guides/thinking_mode)
were checked on 2026-09-12. Flash supports explicit enabled/disabled thinking;
low/high/max are supported effort values. Temperature/presence/frequency settings
are ignored by the provider in thinking mode; this migration does not alter them.

## Artifact and rollback boundary

DEV and Production YAML explicitly select `query-dev-2026-09-12.1.json`, digest
`sha256:645404d90133ba8adabed71e83b22560093dabaf3e8136953961392be7b33da0`.
An invariant test proves only revision, digest and both models differ from the
prior `query-dev-2026-08-31.1.json`; policies, prompts and knobs are identical.
Older sealed files, reports, frozen experiment fixtures and immutable generations
remain historical evidence. The demo's unused `[llm]` legacy placeholder and old
dated design documents are not active routing authorities.

Config-only rollback on this code still executes Flash. A code/image rollback
must separately verify provider-model compatibility; a retained old alias is not
proof that the provider will serve it. Freeze exact previous image/revision and
config before any deployment. LWC-332 separately delivers YAML selection through
canonical BFF deployment/readback/rollback; a YAML edit alone does not change the
live service. Only the dedicated deployer may operate DEV/Production or move main.

## Runnable evidence

- `go test ./internal/llm ./internal/config ./internal/queryconfig ./internal/queryruntime ./cmd/bff ./cmd/query_experiment ./cmd/olw_worker`
- `make test-flash-execution` (from `apps/bff`; installs the exact Docker wheel in
  a temporary environment, then exercises the original CLI and loopback HTTP).
- `LWC_FLASH_RED=1` runs that same synthetic CLI gate without the wrapper and must
  fail on old model/implicit-thinking payloads. No credentials or user data are used.
- Repository `make verify`, retained Python CD suites and canonical CI remain
  required. Wire tests are not evidence of live Worker execution. A Job image
  update is not a pipeline run, and no project/corpus mutation is implied.
