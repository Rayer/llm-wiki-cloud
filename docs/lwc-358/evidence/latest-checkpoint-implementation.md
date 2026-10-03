# Latest-checkpoint pagination implementation

Implemented the Owner-accepted 3584-1777 / Parent 4-1776 contract locally on branch `Rayer/LWC-358-frontend-prerender-config`, at the unchanged source HEAD `1008abd5e5daa710fa45ea684a4740f379981687` and tree `57512727a6a4cb66a52b4bb503d91a3be1743db5`. The frozen r2 spec remains unchanged at SHA-256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

The transport now reads every `per_page=100&page=n` response through the first response's `total_count`; it fails closed on inconsistent totals, short/missing coverage, invalid or duplicate IDs, API errors, and subprocess timeout, with no fixed count limit. It selects the highest ID only after a complete listing and preserves expired-artifact failure, trusted workflow validation, download, and the existing engine checkpoint guards. The engine labels this subprocess `latest-checkpoint` while keeping its 120-second timeout. On child failure, the transport passes the bounded originating exception type/message in one private stderr record; `support.run()` turns it into the existing result `cause` using existing safe-message handling. Generic reason/status/mutation/action and exit-code behavior remain unchanged. No API body, argv, or environment values were added to results.

The pre-patch causal RED remains at `evidence/dev-37127575743-latest-bounded-causal-red-verified.txt` (hash `1cf1af189b15dbfc978249b5f617507cac4ee21c2bdab90390e50408dbaac326`): actual `Engine.runtime_guard()` → real Node transport, with only offline fetch/SDK boundaries, made one list request for a 333-artifact response, attempted no download, and returned a failed unmutated guard. The earlier `dev-37127575743-latest-bounded-causal-red-final.txt` is retained as a superseded harness output whose printed argv labels were shifted. The old 4-page/400 proposal is marked superseded in `evidence/dev-37127575743-latest-lookup-proposal.md`.

Final verification used explicit profile scratch paths and no live API/provider access:

```sh
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch node --test deploy/engine/tests/artifacts.test.cjs
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest -v deploy.engine.tests.test_transport_integration
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'
```

The recorded final outputs are `evidence/latest-checkpoint-node-final2.txt` (9 tests passed), `evidence/latest-checkpoint-integration-final4.txt` (4 tests passed), and `evidence/latest-checkpoint-full-engine-final.txt` (87 tests passed). The integration test enters the actual `engine.main()` result path and asserts the fixed stage, exit/status/action, cause type/message, and no download on incomplete pagination; a second case verifies subprocess timeout metadata. The Node tests cover more than 400 records, complete no-match absence, missing/short and inconsistent pages, duplicate IDs, source timeout, newest-expired no-fallback, and trusted-workflow rejection.

Earlier outputs are preserved without being treated as final passes: `latest-checkpoint-focused-final.txt` failed because its initial test plan omitted `tag`; `latest-checkpoint-integration-final2.txt` exposed the same fixture's missing `source`; `latest-checkpoint-integration-final3.txt` passed its then-current three tests before the timeout assertion was added; `latest-checkpoint-node-final.txt` passed the earlier eight-test set before the explicit expired-newest assertion. The final4/node-final2/full-engine outputs above apply to the final source. No provider, Actions, Git publication, credential, IAM, Production, or tag operation was performed.

The canonical content manifest includes 53 implementation/test/accepted-appendix paths and modes; evidence reports and raw test output remain outside the manifest to avoid self-reference. `content_sha256` is `a4a3d5edf34266e1487883bb6364db751d8cf5a78b8b57c0f7bd7afa36bc6bf8`; the manifest file SHA-256 is `e66501368379968980101bec8cc3d12373755765f221b2530f0f4a5fa312889f`.
