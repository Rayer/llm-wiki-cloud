# LWC-401 request ID lookup checkpoint report

**Checkpoint:** local implementation only; source remains uncommitted. This is not ticket completion, publication, merge, deployment, live DEV UAT, or a `Verified` decision for LWC-393 through LWC-399.

## Result

Implemented `python3 scripts/trace_lookup.py <request-id>` as a stdlib-only, read-only Cloud Logging/Cloud Trace lookup with an injectable shared query core. It validates inputs before auth, searches JSON and text log formats, paginates with visible bounds, retries delayed export/transient reads, deduplicates logs and trace IDs, and returns per-trace parent trees, separate direct/origin request-attribute evidence, finite outcomes, service/revision, attempt IDs, issues, and Console URLs. Output is restricted to safe IDs and allowlisted metadata; native Link verification is always marked manual.

Added a sanitized multi-trace fixture and offline `unittest` coverage, wired the suite into the existing BFF CI Python contract test step, and documented the CLI, actual Logs Explorer filter, Trace Explorer inspection, read-only permissions, limits, and separate UAT mapping for all seven source tickets. The authoritative source handoff was read in full: 110,081 bytes, 483 lines, SHA-256 `da8efda40171198faecd3e071d4292d53880083767d2e768abca964f1c057fe7`; the runbook cites the public YouTrack tickets rather than private handoff paths.

## Evidence

Local checks:

- `python3 scripts/test_trace_lookup.py -v` — PASS, 16 tests.
- From `apps/bff`: `python3.11 -m unittest discover -s ../../scripts -p 'test_trace_lookup.py' -v` — PASS, 16 tests.
- From `apps/bff`: `python3.11 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py' -v` — PASS, 23 tests. This is the existing contract suite from the same CI step that now invokes the lookup tests.
- `python3.11 -m py_compile scripts/trace_lookup.py scripts/test_trace_lookup.py` — PASS.
- `git diff --check` — PASS.
- The equivalent auth-config discovery with this workstation's default Python 3.9.6 — FAIL, 3 import errors because existing `deploy/engine/providers.py` imports `tomllib`, unavailable in Python 3.9. The suite passed with installed Python 3.11.15; no existing tests or runtime configuration were changed.

**Local status:** offline parser, tree, retry, privacy, truncation, input-rejection, and CI-discovery tests PASS. **Live status: NOT RUN.** No Google Cloud API, provider, IAM, credential, product, or Console request was made. Native Link verification is therefore **MANUAL / NOT RUN**. The report and runbook do not treat synthetic fixtures or historical ticket evidence as live UAT.

## Changed files

- `.github/workflows/ci.yml`
- `scripts/trace_lookup.py`
- `scripts/test_trace_lookup.py`
- `scripts/fixtures/trace_lookup/sanitized.json`
- `docs/observability/request-id-runbook.md`
- `docs/observability/request-id-lookup-checkpoint-report.md`

All changes are local and uncommitted. Main owns source review and any continuation or publication decision.
