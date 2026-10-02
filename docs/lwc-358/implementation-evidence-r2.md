# LWC-358 implementation evidence

Status: PR74 權限與測試同步修正、Owner 接受的 Auth/BFF async Cloud Build completion-contract、numeric project resource-name schema，以及 independent intake B1 Stage 1 cross-job checkpoint resume 均已完成離線候選驗證。此工作樹仍未發佈；Parent 負責後續 exact-head review/CI。歷史 failed logs 保留，LWC-366 Auth prepare 歷史 root cause 仍 UNKNOWN。本輪未 commit/push/更新 PR/dispatch Actions/操作 provider、IAM、credentials、Production 或 tag。

- Baseline/HEAD: e9ccf490251ed0262481fca94af7387107be6b5f
- Branch: Rayer/LWC-358-engine-r2
- Session: Codex Implementer, isolated LWC-358-engine-r2 checkout, 2026-10-01T22:12:20.415561+00:00
- Frozen revision: deployment-engine-r2
- Frozen SHA256: 838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b
- Supervisor PASS: e107635133e6ea609a4904cd attempt 1; dispatch reports comment 4-1573 and Submitted readback.
- Initial git status: clean. No repository root AGENTS.md found; supplied AGENTS instructions apply.
- Original spec/shared worktree remains untouched. No commit/push/live dispatch authorized.

## Source findings

Existing Go deploy_config and auth_config.py are reusable. Existing component builders publish digests but runtime operations mix builds and mutation. Existing workflow CI/head polling and absent Export provisioning are superseded for r2. Vercel official CLI docs confirm build writes .vercel/output, deploy --prebuilt consumes it, --skip-domain avoids domain assignment (https://vercel.com/docs/cli/build; https://vercel.com/docs/cli/deploy).

## Superseded workflow assertions

Initial legacy CD run: 78 tests, 12 failures / 6 errors, all old workflow graph/entry assumptions. Replaced by scripts/test_engine_workflow.py plus production-adapter acceptance tests. Legacy provider/config tests retained. Removed old workflow assertions:

- test_fixed_workflows_delegate_to_one_orchestrator
- test_reusable_workflow_secret_forwarding_contract
- test_reusable_callers_grant_mutation_permissions
- test_orchestrator_validates_before_environment_and_gates_mutation_on_rollback_upload
- test_receipt_boundaries_are_environment_gated_and_causally_ordered
- test_main_eligibility_producer_contract_is_causal_and_read_only
- test_main_eligibility_gate_mutations_are_caught
- test_main_eligibility_rejects_missing_github_cli
- test_no_legacy_workflow_owns_deployment_literals
- test_export_prerequisite_workflow_isolated_to_develop_and_reuses_existing_auth
- test_protected_mutation_maps_environment_scoped_vercel_credentials
- test_backend_rollbacks_use_only_retained_immutable_image_handles
- test_each_component_has_a_real_independent_action_boundary
- test_wrappers_require_explicit_components_and_inherit_secrets
- test_reusable_workflow_validates_the_complete_fixed_tuple_before_protected_environment
- test_production_is_serialized_without_a_shared_development_lock

## Focused recovery invariant review

Unresolved decision: active prior routing alone cannot establish restoration after a no-traffic partial update. Discriminating check: production-adapter fixture leaves old traffic but changes the Service template; rollback must replace/verify template too. Added template fingerprint and regression. No runtime reasoning-setting control was used or claimed.

Vercel documentation readback (2026-10-02): --skip-domain MUST accompany --prod. Adapter now uses it only for Production; DEV uses explicit --target=preview from detached archive without GitHub/VERCEL_GIT metadata. Stage 1 has no deploy/alias command. Source: https://vercel.com/docs/cli/deploy (updated 2026-09-18).

## 交付內容與驗收對照

實作位於 deploy/engine（orchestrator、provider adapters、profiles、Actions artifact transport）；沿用 Go deploy_config、auth_config.py、frontend_build_config.py 與四個現有 container builders。正式 DEV / Production / recovery wrappers 共用 cd.yml；單一 protected job、同 target serialization、durable pending checkpoint、最新 checkpoint fencing。Operator 文件與 repository skill 已附。

| Frozen AC | 可重現測試 / 證據 |
| --- | --- |
| 1 | test_01_stage_barrier_and_retry_retains_first；test_unusable_receipt_rebuilds_only_affected_component |
| 2 | test_02_four_to_five_reuses_four_and_frontend_build_only；實際 Go dependency/embed identity 測試排除 deploy tooling / 無關 packages |
| 3 | test_03_production_explicit_dev_no_build_source_and_handle_rejection；wrong repository / missing DEV reference 拒絕 |
| 4 | test_04_order_barrier_and_real_config；workflow contract、actionlint；pending upload failure 為零 runtime mutation |
| 5 | test_05_partial_failure_restores_changed_and_reactivation_no_build；Service no-traffic partial update 必須還原 template |
| 6 | test_06_timeout_after_acceptance_and_unknown_and_rollback_failure；unknown resume 不重送已接受的 Job update |
| 7 | test_07_severe_recovery_unchanged_not_redeployed；逐一故障注入 Service / Job / Frontend sanity 欄位；Job/data boundary |
| 8 | test_08_tag_only_retry_idempotence_conflict；test_07 保留成功 tag |
| 9 | test_09_frontend_alias_restore_and_config_applicability；Frontend target / actual build-config / Ready / artifact / alias 負向測試；prepare 沒有 deploy/alias |
| 10 | 以下真實命令與輸出；既有 workflow assertions 已以 r2 contract 取代，provider/config regression 保留 |

## 最終本機測試命令與輸出

所有命令均為本機 / TEST ONLY provider boundary，無 live provider 操作。以下路徑相對 repository root，除註明 cwd 外。

| 精確命令 | 輸出 | 完整輸出檔 |
| --- | --- | --- |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | Ran 20 tests; OK | evidence/engine-tests.txt |
| `node --test deploy/engine/tests/artifacts.test.cjs` | tests 4; pass 4; fail 0 | evidence/artifact-transport.txt |
| `python3 scripts/test_cd_contract.py` | Ran 65 tests; OK | evidence/legacy-cd.txt |
| `python3 scripts/test_engine_workflow.py` | Ran 3 tests; OK | evidence/workflow-contract.txt |
| `python3 -m unittest discover -s scripts -p 'test_*config*.py'` | Ran 34 tests; OK | evidence/config-contracts.txt |
| `go test ./cmd/deploy_config ./cmd/bff ./cmd/olw_worker .`（cwd apps/bff） | 四個 packages OK | evidence/go-deployment.txt |
| `go vet ./cmd/deploy_config ./cmd/bff ./cmd/olw_worker`（cwd apps/bff） | exit 0，無輸出 | evidence/go-vet.txt |
| `node --test apps/frontend/tests/ci-workflow-contract.test.mjs apps/frontend/tests/lwc-306-frontend-cd.test.mjs` | tests 61; pass 61; fail 0 | evidence/frontend-deployment.txt |
| `node --test apps/frontend/tests/ci-workflow-contract.test.mjs`（最後參數修正後） | tests 5; pass 5; fail 0 | evidence/frontend-workflow.txt |
| `python3 -m unittest discover -s apps/bff/scripts -p 'test_*deployment_evidence.py'` | Ran 49 tests; OK | evidence/evidence-renderers.txt |
| `python3 -m unittest scripts.test_exportjob_provision_contract deploy.provision.test_exportjob_dev` | Ran 40 tests; OK | evidence/provision-regression.txt |
| `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`（cwd apps/bff） | exit 0，無輸出 | evidence/actionlint.txt |
| `npx --no-install eslint tests/ci-workflow-contract.test.mjs`（cwd apps/frontend） | exit 0，無輸出 | evidence/eslint.txt |

另通過 `git diff --check`、`python3 -m py_compile deploy/engine/*.py deploy/engine/tests/*.py scripts/test_engine_workflow.py`、`bash -n deploy/components/auth.sh deploy/components/bff.sh deploy/components/worker.sh deploy/components/exportjob.sh`、兩個 production `.cjs` 的 `node --check`。Skill validator：`python3 /Users/rayer/.codex/skills/.system/skill-creator/scripts/quick_validate.py .agents/skills/deployment-operator` → `Skill is valid!`。

## 曾遇到的失敗與環境處理

- 初始 legacy CD：78 tests，12 failures / 6 errors，舊 workflow graph 斷言；原始 `/tmp/lwc358-legacy-cd.log` 保留。被取代項目列於上文，現行 65 tests 包含新增 workflow contract。
- 初始 Go workflow tests：舊 source_ref/config_environment、mutate job、rollback upload step 名稱斷言失敗；原始 `/tmp/lwc358-go.log` 保留。更新 workflow tests 後通過，無 production test fake 替代真實 Go 結果。
- 初始 actionlint：job env 不允許 runner context；改由 step 設定 NODE_PATH。原始 `/tmp/lwc358-actionlint.log` 保留。
- 初始 Frontend workflow suite：本 checkout 尚未安裝 js-yaml（ERR_MODULE_NOT_FOUND）。實際執行 `npm ci --ignore-scripts --prefix apps/frontend` 後重跑；又識別並更新四個舊 workflow graph assertions。原始 `/tmp/lwc358-frontend.log`、`/tmp/lwc358-frontend-workflow-r2.log` 保留，最終 61 / 61 通過。
- npm ci 輸出既有 lockfile 的 11 vulnerabilities（3 moderate、7 high、1 critical）；未變更 dependency/lockfile 或擅自 audit fix。輸出 `/tmp/lwc358-npm-ci.log`。
- 初始新測試暴露 fixture cwd/partial rollback fake 的問題，修正 TEST ONLY fixture；實際 Go source-input 測試另暴露 template newline 與 embed coverage，修正 production identity code 後通過。

## 明確限制與待審閱風險

未執行 live Actions、GCP、Vercel、tag publication 或 UAT；這是本次權限邊界，不以 offline fake 宣稱 live success。Actions artifact SDK 2.3.2 已實際安裝於 `/tmp/lwc358-artifact-sdk` 並檢查 runtime token / results URL 與 findBy 介面；正式執行使用 JS action 注入 runner runtime context，離線 transport tests 驗證 API/SDK boundary，仍須日後獲准 live test 才能證實實際 principal 與 provider retention。

此版只還原可表示的既有資源：Service 必須一個 untagged 100% Ready revision、template 與 retained immutable revision 一致；Job 必須 immutable image，Export config 必須已符合 target。其他狀態 pre-mutation breakpoint，不 provision/delete。最新 checkpoint 查詢界限 100 artifacts；到期、找不到或超出界限即停止。Jobs 既有 execution / persistent writes 不會被 rollback。Vercel DEV 使用 detached Preview archive、Production 使用 --prod --skip-domain；實際 provider 基本 sanity 與 alias readback 由 engine 執行，未把 functional smoke/UAT 加成 release gate。

原 shared workflow-package checkout 未修改。未安裝 skill 到其他 profile。所有變更保持在本隔離 checkout，可由同一 implementer 接續審閱修正。

## 首次 TPM 審閱版本鎖定（已由下列 P1 修正版取代）

- Base / HEAD: `e9ccf490251ed0262481fca94af7387107be6b5f`
- Branch: `Rayer/LWC-358-engine-r2`
- Implementation content SHA256: `f156e66c59ce3f2283cefc70989d45c029fa79c5bd0b9554d3e61048404fb352`
- Frozen spec SHA256: `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`
- 精確檔案與 mode/hash：`implementation-content.json`（28 個檔案；排除 manifest 自身、此報告及測試輸出以避免自我雜湊）。
- tracked diff: `git diff e9ccf490251ed0262481fca94af7387107be6b5f --`；新檔案由 manifest 一併鎖定，不能只看 tracked diff。

## TPM P1 remediation — engine / transport argv

TPM 在前一份內容 `f156e66c59ce3f2283cefc70989d45c029fa79c5bd0b9554d3e61048404fb352` 找到真實 P1：engine.save / runtime_guard 將 JS action 的 index.cjs、action.yml 誤當作 artifacts.cjs 的 positional arguments。Parser 因而讀到錯誤 op，upload / latest 都在 SDK / HTTP 前失敗。先前 isolated transport tests 與 mocked engine.run 沒有涵蓋這個接線邊界；此前的離線通過紀錄不能證明這兩條 runtime 路徑可用。

本次 production fix 僅修改 engine.py 的兩個 argv，保留 JS action 檔案在 engine_fingerprint 的覆蓋。新增 `test_transport_integration.py` 與明確 TEST ONLY `transport_sdk_stub.cjs`：實際 Engine method → 未 mock 的 support.run / subprocess → 真實 node / artifacts.cjs parser；只以 Node preload 替換 SDK / HTTP 邊界。Upload 驗證精確 argv、一筆 SDK upload 與實際 checkpoint bytes；latest 驗證精確 argv、三筆離線 API 呼叫、一筆 SDK download、落盤 checkpoint 與 guard 成功。沒有手工重建正確的 transport argv 取代 Engine 呼叫。

### 本次測試（Python 3.14.6 / Node v22.23.2）

| 精確命令 | 結果與證據 |
| --- | --- |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_transport_integration.py'`（production fix 前） | 2 errors，實際 subprocess command-failed；`evidence/p1-red.txt` |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_transport_integration.py' -v`（修正後） | 2 tests，OK；`evidence/p1-integration.txt` |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 22 tests，OK；`evidence/p1-engine.txt` |
| `node --test deploy/engine/tests/artifacts.test.cjs` | 4 tests，pass 4 / fail 0；`evidence/p1-transport.txt` |
| `python3.14 scripts/test_engine_workflow.py` | 3 tests，OK；`evidence/p1-workflow.txt` |
| `python3.14 -m py_compile deploy/engine/engine.py deploy/engine/tests/test_transport_integration.py` | exit 0，無輸出 |
| `node --check deploy/engine/tests/transport_sdk_stub.cjs` | exit 0，無輸出 |
| `git diff --check` | exit 0，無輸出 |

第一次 green run 的新測試另因 macOS `/var` → `/private/var` canonical path 差異出現兩個 assertion failures；fixture 改用 resolved temp path，沒有更動 production path 行為。保留 `evidence/p1-path-fixture.txt`，修正後完整重跑通過。

Runtime 前提已補入 operator 文件：Python 3.12+，因 Frontend 使用 `tarfile.extractall(filter="data")`；本次驗證明確使用 `/opt/homebrew/bin/python3.14` 3.14.6。本 lane 當前 `python3` 也解析為 3.14.6；TPM 回報其 host default Python 3.9 不支援 filter，這是已知不相容前提，不能宣稱任意 `python3` interpreter 都通過。先前表格中的 python3 命令是當時環境紀錄，不是 Python 3.9 相容性承諾。

與前次內容相比，僅 engine.py、operator-r2.md 兩個既有 manifest 檔案變更，新增兩個整合測試檔；報告、manifest、證據同步更新。精確 remediation diff：`evidence/p1-remediation.diff`；前次 manifest：`evidence/p1-prior-content.json`。未修改 LWC-366，shared package spec 逐 byte 相同；未 commit / push / PR / merge / live Actions / provider / credential / tag 操作。仍待同一 TPM lane 審閱。

### P1 修正版精確身分（已由下列 Supervisor HOLD 修正版取代）

- Base / HEAD: `e9ccf490251ed0262481fca94af7387107be6b5f`
- Branch: `Rayer/LWC-358-engine-r2`
- Implementation content SHA256: `e0946d191cad57c9fe93c99e36cfe9a3eee9a96635db766205c2702b5a0ce0b8`
- Frozen spec SHA256: `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`
- `implementation-content.json` 共 30 個檔案；雜湊算法與排除 evidence/report 自我參照規則沿用前版。

## Supervisor HOLD c6aac43288a0ddab01c06988 — scoped compensation / retained Service reactivation

Reviewed input: `e0946d191cad57c9fe93c99e36cfe9a3eee9a96635db766205c2702b5a0ce0b8`。兩項 blocker 已在本 worker 以 production engine/adapters 與 TEST ONLY command fakes 重現並修正，待同一 TPM / Supervisor 複審；不宣稱已取得 Supervisor PASS。

1. Compensation：原本 failure 走訪整份 release history 的 non-unstarted entries。現在 deploy() 為本次 invocation 收集 changed/possibly-changed components，只將這份清單交給逆序 restore；未選取、或 verified 而本次跳過的 component 不加入。Regression 執行 auth+worker 成功 → worker-only rollback → worker-only reactivate partial failure，確認零 Auth service update/replace/update-traffic、Auth provider resource 與 checkpoint entry 均完全不變，worker 為 rolled_back。
2. Service reactivation：原本只切回 candidate traffic，而 Service template 仍為 prior。現在共用 restore_service() 以 retained revision exact spec、明確相同 template.metadata.name 與該 revision 100% routing 還原；candidate basic sanity 同時要求 service template spec 與 active retained revision 一致、template revision name 相同。沒有新 build 或替代 revision fallback。Provider 若拒絕 named retained template，仍經 readback / failure / recovery 路徑，不偽報成功。

Auth 與 BFF regression 都走完整 deploy → rollback → reactivate → **不同 plan ID / tag 的下一個 release** prepare(reuse) → snapshot；驗證 snapshot 可用、相同 immutable image、完整 receipt（含 build/source identity）不變、保留 revision identity / revision內容不變、replace payload 指定相同 revision spec/name/routing，且沒有 build/submit。TEST ONLY provider fake 現在會拒絕 nonexistent retained revision 或與其 spec 不同的 replace payload；另增加 template-only sanity mismatch 負向子案例。

本次僅修改 engine.py、providers.py、兩個 engine test/fixture 檔案與 operator-r2.md。精確增量 diff：`evidence/hold-remediation.diff`；原審閱 manifest：`evidence/hold-prior-content.json`。

### 先失敗、後通過的實際驗證

環境 Python 3.14.6 / Node v22.23.2；沿用 Python 3.12+ prerequisite，不聲稱 Python 3.9 相容。

| 精確命令 | 結果 | 證據 |
| --- | --- | --- |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_engine.py' -k test_hold -v`（fix 前） | 2 methods：1 failure（未選取 Auth replace）、2 subcase errors（Auth/BFF snapshot unrepresentable-prior-service-template） | evidence/hold-red.txt |
| 同上（fix 後） | 2 methods，Auth/BFF subcases 均 OK | evidence/hold-green.txt |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 24 tests，OK；包含先前 real-engine upload/latest integration | evidence/hold-engine.txt |
| `node --test deploy/engine/tests/artifacts.test.cjs` | 4 tests，pass 4 / fail 0 | evidence/hold-transport.txt |
| `python3.14 scripts/test_engine_workflow.py` | 3 tests，OK | evidence/hold-workflow.txt |
| `python3.14 -m unittest discover -s scripts -p 'test_*auth_config_contract.py'` | 31 tests，OK | evidence/hold-auth-config.txt |

另執行 `python3.14 -m py_compile deploy/engine/engine.py deploy/engine/providers.py deploy/engine/tests/test_engine.py deploy/engine/tests/fake_provider.py`、`bash -n deploy/components/auth.sh deploy/components/bff.sh`、`node --check deploy/engine/tests/transport_sdk_stub.cjs`、`git diff --check`，均 exit 0 / 無輸出。未修改 workflow，沒有為此重跑不受影響的 broad suites；前輪結果保留且不冒稱為本輪重跑。

Service payload 仍使用現有 Cloud Run v1 Service / RevisionTemplate 介面（https://docs.cloud.google.com/run/docs/reference/rest/v1/RevisionTemplate），明確指定 retained name；offline 檢查涵蓋命令、payload 與 readback，不冒充 live provider 驗證。若實際 provider 無法接受該 retained name/config，會保留 breakpoint，不能刪 revision、改名新建或 rebuild 來繞過。

Owner 尚未裁定的 historical DEV receipt promotion revocation **未實作**；未加入 current-head equality。Vercel auto-promotion/alias live readback 維持日後授權驗證，未新增本機 gate。未碰 LWC-366、shared package；frozen spec byte compare 相同。未 commit / push / PR / merge / live Actions / provider / credential / tag 操作。

### Supervisor HOLD 修正版精確身分

- Base / HEAD: `e9ccf490251ed0262481fca94af7387107be6b5f`
- Branch: `Rayer/LWC-358-engine-r2`
- Implementation content SHA256: `9628280bd89e5d2584a048f15cf95fd7c5c29e9d6cb74d9bc9cb344e1412d860`
- Frozen spec SHA256: `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`
- Manifest: `implementation-content.json`，30 個檔案，沿用相同內容 hash / evidence 排除規則。

## Supervisor re-review 5a872b361ffa29c24bfd3207 — retained effective annotations

本次起點為 reviewed content 9628280bd89e5d2584a048f15cf95fd7c5c29e9d6cb74d9bc9cb344e1412d860。
原 worker/session checkout：/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2；branch Rayer/LWC-358-engine-r2；Base/HEAD e9ccf490251ed0262481fca94af7387107be6b5f。
編輯前已讀 source、查核 branch/HEAD 並備份 reviewed source/manifest 至 /tmp/lwc358-annotations-before；frozen SHA256 838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b 不變。根目錄無實體 AGENTS.md，遵循對話提供之 AGENTS 指示。

根因與整批 call-class audit：restore_service() 原先混用 retained spec 與 current template annotations。現在 revision_config() 共用投影包含 spec 與 retained annotations，僅明列排除 controller/audit outputs，不按 namespace 全部排除。snapshot 的可表示性、template fingerprint、retained revision fingerprint、rollback fingerprint check、deploy/reactivate 是否需 restore、prior/candidate observe 均使用相同投影；名稱與單一 untagged 100% revision routing 的原有檢查保留。auth_config.effective() 仍以完整 retained revision 解析 secret aliases，未改寫其既有語義。reconcile_candidate() 只發現 identity，後續 deploy/observe 仍經上述驗證。Jobs/Frontend 無使用此 Service template call class，未改動。

排除的 controller/audit keys：run.googleapis.com/operation-id、ingress-status、urls、client-name、client-version；serving.knative.dev/creator、lastModifier、routes；client.knative.dev/user-image。其餘 annotations（包含 secrets、autoscaling、網路及未知 keys）保留，不靜默丟棄有效設定。metadata uid/resourceVersion/generation/timestamps/ownerReferences 不複製到 template。Service-level annotations 維持現狀，因這是 revision config restore。API 依據：[RevisionTemplate](https://docs.cloud.google.com/run/docs/reference/rest/v1/RevisionTemplate)、[Revision](https://docs.cloud.google.com/run/docs/reference/rest/v1/namespaces.revisions)。不宣稱 offline fake 等同 live API 接受；拒絕仍回報 breakpoint，不建立替代 revision 或 rebuild。

TEST ONLY subprocess fake 會拒絕 named retained revision 的錯誤 spec 或 effective annotations。新回歸分別對 Auth/BFF 設置不同 prior/candidate aliases 與 scaling annotations，走 prepare/deploy → rollback → reactivate → 新 release snapshot，驗證相同 receipt/artifact/revision、無 build/submit、controller annotation 不重播。另驗證 template-only annotation 缺失讓 prior/candidate observe 與 snapshot 失敗；controller-only churn 不改變 fingerprint；有效 annotation 即使在 revision/template 同時被改動，也會因 retained fingerprint 不符在 replace 前停止。所有 fixture refs 都為 TEST ONLY，未讀取或輸出 live secrets。

### 實際 red/green 與受影響 suites

環境 Python 3.14.6 / Node v22.23.2；Python >=3.12 prerequisite 不變，未宣稱 host Python3.9 可通過。

| 精確命令 | 結果 | 原始輸出 |
| --- | --- | --- |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_engine.py' -k test_retained_annotations -v`（production fix 前） | 1 method / Auth、BFF 2 subcase errors：rollback-not-verified，strict fake 拒絕 annotations 不符 | evidence/annotations-red.txt |
| 同上（fix 後，含 fingerprint 負向檢查） | 1 method / 2 components OK | evidence/annotations-green.txt |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 25 tests OK | evidence/annotations-engine.txt |
| `python3.14 -m unittest discover -s scripts -p 'test_*auth_config_contract.py'` | 31 tests OK | evidence/annotations-auth-config.txt |
| `node --test deploy/engine/tests/artifacts.test.cjs` | 4 pass / 0 fail | evidence/annotations-transport.txt |
| `python3.14 scripts/test_engine_workflow.py` | 3 tests OK | evidence/annotations-workflow.txt |

另執行 `python3.14 -m py_compile deploy/engine/providers.py deploy/engine/tests/test_engine.py deploy/engine/tests/fake_provider.py`、`bash -n deploy/components/auth.sh deploy/components/bff.sh`、`node --check deploy/engine/tests/transport_sdk_stub.cjs`、`git diff --check`，exit 0 無輸出。完整 engine 首輪 25 tests 已過，新增 fingerprint assertions 後再重跑，保存最終輸出。

精確 incremental diff：evidence/annotations-remediation.diff（providers.py、test_engine.py、fake_provider.py、operator-r2.md）；reviewed manifest：evidence/annotations-prior-content.json。新 manifest content SHA256：`bbc0ae53655fc05d4977cb128b28c673551cd0659ee9932d186fd0f92a1a1b65`（30 files，沿用 canonical JSON rows hash；排除 evidence/report/manifest 自我參照）。本節取代前次 9628 content 身分，待同 TPM/Supervisor lane 複審，未宣稱 PASS。

剩餘限制：live provider 接受 retained named revision restore、Vercel auto-promotion/alias readback 仍待授權驗證。舊 engine checkpoint 不手動轉換或繞過 pinned-engine-mismatch。Owner historical DEV promotion revocation 仍 NONBLOCKING 未裁定，未加 gate/current-head equality/IAM gate。未修改 shared package 或 LWC-366；frozen source byte compare 相同。未 commit/push/PR/merge/live Actions/provider/credential/tag 操作。

Publication staging check: 30 manifest implementation files 均通過 git diff --cached --check；完整 staged check 僅指出原始 red-test logs 與 unified diff 證據的 whitespace（context 空白行／原始 runner 行尾）。為保留 exact 原始輸出未改寫證據；無 implementation whitespace 問題。

## Independent intake HOLD 3584-1663 — accepted Stage 1 checkpoint-resume boundary

Intake source: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/async-build-numeric-intake-review.md`, especially §§20–70. The inspected local candidate identity before this remediation remains HEAD `8ee9bed045c79dc72ad2d7d9ff551359e15b54c5`, tree `fe1afa8d0b262a27402ce0b1cdb8429b36e71719`, branch `Rayer/LWC-358-engine-r2`; the last completed manifest was 43 files / `44e51ca5158a1340aed7f848d29fcefed620b3fa5a4f0f59e344c15d43105b7d`. Frozen r2 spec SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. This is a local, uncommitted candidate; no source has yet been changed for B1 at the time of this contract note.

The intake's concrete causal chain is: formal `artifact_id` downloads to `$RUNNER_TEMP/reuse`, while Action prepare always creates `$RUNNER_TEMP/release`; engine admission initializes empty builds there, and current `prepare(reuse=...)` imports receipts but not `state.json`. Thus a fresh Actions job loses verified build IDs, ID-less submit-unknown markers, and SUCCESS-without-digest checkpoints and may submit again. Same-directory tests did not cover this boundary.

Accepted bounded repair: exact same-attempt resume requires a valid retained plan digest, a state bound to that plan, exact full plan equality (therefore source, target/config, release tag, selection, and engine identity/content), and a Stage 1 state with no runtime component checkpoints and status `prepared`/`ready`. Restore its complete build checkpoint into the new release directory before continuing prepare. A changed plan is receipt reuse only: do not import its state. Reuse compatible completed receipts as before; if a recorded build has no applicable completed receipt and its outcome is unresolved or unrecognized, fail closed before submit. Known terminal failure / explicit pre-create `SUBMIT_REJECTED` remain safe for a later explicit prepare attempt. `--dev` provenance remains separate. This clarification is recorded in `deployment-engine-spec-r2-accepted-appendix-build-submit.md`; frozen r2 bytes are not edited. No new replay, IAM, runtime, or provider authority is introduced.

Planned discriminating regression before production edit: run the real `.github/actions/deployment-engine/index.cjs` prepare entrypoint and real engine CLI with a TEST ONLY artifact-download transport that copies a retained artifact into the actual `reuse` path. The retained attempt will be created through admission with one verified Cloud Build ID and pending state; the fresh release must status-read that same tuple to SUCCESS, resolve its digest, publish a receipt, and leave submit count at one. Additional action-path scenarios cover verified ID/status unknown, no-ID submit unknown, SUCCESS/digest failure, terminal failure followed by explicit retry, valid receipt reuse, four-to-five expansion, and mismatched/corrupt/runtime-started checkpoint rejection. No live Actions, Cloud Build, provider, credential, IAM, Production, Git publication, or tag operation is in scope. Final manifest and evidence will be regenerated from actual file bytes/modes after implementation and targeted tests; no final SHA is claimed here.

### B1 implementation and final offline evidence

`deploy/engine/engine.py` now validates both retained `plan.json` and `state.json`, verifies the plan digest and state binding, checks selected/source/engine/configuration shape plus safe build checkpoint tuple fields, and treats full plan equality as same-attempt identity. Only a `prepared`/`ready` retained state with no runtime component entries is copied into the fresh release directory. That restores pending/unknown IDs, ID-less submit markers, and SUCCESS checkpoints before `prepare_container()` can decide whether to poll, stop, resolve digest, or retry a known terminal failure.

When plan identity differs, state is never copied. Existing compatible receipts remain reusable after provider usability readback. A nonterminal or unrecognized old build without such a receipt stops before any new submit with `cross-plan-build-checkpoint-unresolved`, including a bounded safe build tuple and next action `resume-original-plan-checkpoint`. Known terminal Cloud Build failures and pre-create rejection remain eligible only in a later explicit invocation. Production's container source remains the explicit DEV provenance artifact; an unrelated `artifact_id` checkpoint is not used as its container state. The frozen r2 spec remains byte-identical.

The new test-only Node transport intercepts only the Action's artifact `download` child command and copies an artifact fixture to the actual `$RUNNER_TEMP/reuse` path. The test executes the real `.github/actions/deployment-engine/index.cjs` and real `engine.py prepare` CLI in a new `$RUNNER_TEMP/release`; fake `gcloud`, `go`, and `timeout` executables provide offline build/status/digest behavior. It proves: WORKING/deadline and explicit STATUS_UNKNOWN resume the same verified ID without submit; ID-less SUBMIT_UNKNOWN performs zero retries; SUCCESS plus digest failure only repeats digest resolution; explicit retry after terminal FAILURE creates the next build; a valid completed Auth receipt survives plan selection expansion while only BFF builds; changed source, target, tag, selected set, or engine content cannot import an unresolved handle; runtime-started, bad plan digest, and mismatched state-plan artifacts fail closed. Existing acceptance four-to-five/Frontend-only and Production provenance tests also ran in the full engine suite.

The red and failed harness runs are intentionally retained as separate evidence; the final pass is a different file, not an overwrite:

| Exact command | Actual outcome | Evidence |
| --- | --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_build_submission.py' -k test_real_action -v` (before production fix) | 5 tests, 5 assertion failures: fresh Action resubmitted the second ID, ignored no-ID unknown, and allowed changed-plan pending state to submit. The terminal retry/receipt expansion control passed. | `evidence/stage1-action-resume-red.txt`; SHA256 `6e34e1842770aa6b6a41a7d1edace254997174e324023355e11395f645019e86` |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` (first full attempt) | 59 tests; 8 errors from the new test's missing `support.read` import. | `evidence/stage1-action-resume-engine.txt`; SHA256 `e55c5dd81df66f54e081a63abac58d7108435fabdfc426e4dc8b0a5a72860d9a` |
| Same full command (second full attempt) | 59 tests; 8 fixture `Path` concatenation errors. Original bytes preserved at SHA256 `6d0c8ead522449f8b2b0aebb9ea078a0105b706cf12e0c7777b0416e0511a9a8`. | `evidence/stage1-action-resume-engine-final.txt` |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_build_submission.py' -k fail_closed -v` (after fixture correction) | 2 methods, OK; covers five plan identity dimensions plus runtime-started/corrupt checkpoint handling. | `evidence/stage1-action-resume-identity-tests.txt`; SHA256 `0e32e2b379c7f99fe3f9334f5f663611f264905343cb84b2ac88ed3ae7075ae1` |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` (final) | 59 tests in 133.604s, `OK`. This includes all five real Action download→new release→prepare scenarios, four-to-five/Frontend-only, runtime recovery, and Production DEV provenance. | `evidence/stage1-action-resume-engine-green.txt`; SHA256 `13bbe69c185017a7fee854b8be65a15616effb0b4f1198d2d7291df6abe25d8f` |

Also ran `git diff --check` (exit 0) and AST parsing of `deploy/engine/engine.py` plus `deploy/engine/tests/test_build_submission.py` (exit 0). No unchanged Go/Frontend suites were repeated. Test process used only local fakes; no live build, Actions, provider, credential, IAM, Production, or tag call occurred. Scratch and earlier evidence were not cleaned.

Final content manifest was regenerated from actual file bytes/modes after the full engine pass. It records 43 scoped implementation/test/operator/appendix/frozen-spec files; the manifest and evidence/log files remain excluded to avoid self-reference. Frozen spec SHA256 is unchanged at `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. Git HEAD remains the pre-existing base `8ee9bed045c79dc72ad2d7d9ff551359e15b54c5` / tree `fe1afa8d0b262a27402ce0b1cdb8429b36e71719`; there is no new commit or final SHA. No publication or live operation is claimed.

## PR71 actual CI acceptance repair

起點 PR71 head 8eaa7cb687cd24dfbdec360861c0c6ee92c1c94f / content bbc0ae53655fc05d4977cb128b28c673551cd0659ee9932d186fd0f92a1a1b65。
已讀 Parent findings：LWC-358-deploy-convergence/docs/lwc-358/deployment-engine-pr71-ci-findings.md，及其指定完整 CI log out-1790921421-57743-3360.log。Actual failed run：https://github.com/Rayer/llm-wiki-cloud/actions/runs/36972022713 。沒有把 prepublication PASS 視為新 head acceptance。

根因已實際重現：Acceptance clear=False 繼承 GITHUB_ACTIONS=true，save() 進入真實 artifact transport。新 test_ci_environment.py 用 subprocess 啟動真實 Acceptance，注入 TEST ONLY Actions 環境與 node 攔截器，修正前 marker 證實 transport 被呼叫而失敗；攔截器在任何 transport/network 執行前 exit，無 live 操作。修正為 clear=True + 最小工具/cache/locale 環境 allowlist，另驗證 CI authority/credential/Node preload 不會繼承。Production engine、provider、durability upload、transport adapter 及 dedicated Engine→subprocess→Node SDK-stub integration 逐 byte 未改。

四個 Frontend superseded workflow assertions 均保留測試並重寫：DEV/Production 固定 branch、source SHA、explicit release tag/receipt inputs、shared engine + inherited secrets；protected runtime 與 per-target concurrency；selected prepare→durable ready artifact→runtime 順序；pinned recovery artifact、pending checkpoint、final failure retention、alias snapshot/readback/restore。既有 provider 功能案例未刪除或 skip；沒有恢復 legacy polling/reapproval machinery。

環境 Python 3.14.6 / Node v22.23.2。精確命令：
- `python3.14 -m unittest discover -s deploy/engine/tests -p test_ci_environment.py -v`：red 1 failure，green 1 OK；evidence/pr71-ci-red.txt / pr71-ci-green.txt。
- `node --test --test-name-pattern='frontend aliases use the shared CD|DEV authority uses the fixed|DEV workflow invokes|production workflow invokes' apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs`：修改前 4 fail / 0 pass / 0 skip，evidence/pr71-ci-frontend-red.txt。
- `GITHUB_ACTIONS=true GITHUB_RUN_ID=42 GITHUB_RUN_ATTEMPT=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'`：27 tests OK，含 dedicated actual transport 2 tests；evidence/pr71-ci-engine.txt。
- `npm test`（cwd apps/frontend）：Node 523 pass / 0 fail / 0 skip；Vitest 31 files、288 tests pass；evidence/pr71-ci-frontend.txt。jsdom 印出兩次 navigation not implemented 非失敗，保留原始輸出。
- `npx --no-install eslint tests/lwc-199-vercel-alias-promotion.test.mjs tests/lwc-253-authority-reconciliation.test.mjs tests/lwc-253-vercel-dev-authority.test.mjs tests/lwc-258-vercel-production-auth-env.test.mjs`（cwd apps/frontend）：exit 0，evidence/pr71-ci-eslint.txt。
- `python3.14 scripts/test_cd_contract.py`：65 OK，evidence/pr71-ci-cd.txt。
- `python3.14 scripts/test_engine_workflow.py`：3 OK，evidence/pr71-ci-workflow.txt。
- `node --test deploy/engine/tests/artifacts.test.cjs`：4 pass，evidence/pr71-ci-transport.txt。
- `python3.14 -m py_compile deploy/engine/tests/test_ci_environment.py deploy/engine/tests/test_engine.py` 與 `git diff --check`：exit 0（新 evidence 加入前；原始 red logs 保留行尾空白）。

新 manifest 35 files：原30 + 四個 Frontend test files + CI environment regression。Content SHA256：47ff72090d28ca644454c0a0e4a08742148742c1619bad13724201f63cd3e986。Base/frozen SHA 不變，原 manifest 保留 evidence/pr71-ci-prior-content.json。仅 tests + manifest/evidence 改動；Parent+Supervisor 必須審閱相同新 head。未變更 model/effort、未改 Bug366 或 shared worktree；未 merge/dispatch/tag/provider/credential write。Live engine/provider acceptance 仍 NOT RUN。


## PR71 BFF workflow-contract acceptance repair

起點 head `0b40ec0617d44bf0a2b3de5a853888fe50827b67` / content `47ff72090d28ca644454c0a0e4a08742148742c1619bad13724201f63cd3e986`。已閱讀 Parent / Supervisor review：`LWC-358-deploy-convergence/docs/lwc-358/deployment-engine-pr71-review-0b40.md`。Sibling audit 在 apps/bff/scripts/test_*.py 只找到 test_bff_explicit_cutover.py 中該組 retired workflow-step assertions；其他 workflow-run/promotion contract tests 驗證實際 provenance parser，並非舊 shared-CD step graph，保留原樣。

`test_shared_bff_path_preserves_cutover_safety_boundaries` 仍保留並重寫為：DEV/Production 固定 branch 與 explicit release_tag、兩者與 recovery wrapper 共用 cd.yml、single protected runtime/per-target serialization、prepare → durable upload barrier → runtime 順序與 90-day retention/failure stop。它還 subprocess 執行真實 Engine acceptance regressions：`test_04_order_barrier_and_real_config`、`test_durable_pending_failure_prevents_provider_mutation`、Auth partial Service restore、Auth/BFF reactivation→next snapshot。既有 BFF alias/candidate freshness/cutover、immutable DEV receipt 與 readback/redaction cases 未刪改。無舊 rollback-upload/Mutate Auth step shape，無 legacy orchestration 復活。

先前 sandbox 首跑全 suite：109 tests 中 107 pass，兩個既有 local-dev tests 因 bind loopback PermissionError（Operation not permitted）失敗，原始輸出 evidence/pr71-ci2-bff-scripts-sandbox.txt。依 sandbox 指示取得 escalation 並以相同命令在 sandbox 外重跑；最終 **109 tests OK**，兩個 port-binding tests 正常執行，無 skip。這是環境權限，不是 acceptance workaround。

精確命令與輸出：
- `python3.14 -m unittest discover -s apps/bff/scripts -p test_bff_explicit_cutover.py -v`：6 tests OK；包含 nested production engine regressions，evidence/pr71-ci2-bff-target-green.txt。
- `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_*.py'`（經核准 outside sandbox，以同一命令重跑）：109 tests OK，47.298s；evidence/pr71-ci2-bff-scripts-escalated.txt。
- `GITHUB_ACTIONS=true GITHUB_RUN_ID=42 GITHUB_RUN_ATTEMPT=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'`：27 tests OK，29.307s；上一節 evidence/pr71-ci-engine.txt。
- `python3.14 scripts/test_engine_workflow.py`：3 tests OK；上一節 evidence/pr71-ci-workflow.txt。
- `node --test deploy/engine/tests/artifacts.test.cjs`：4 pass；上一節 evidence/pr71-ci-transport.txt。
- `python3.14 -m py_compile apps/bff/scripts/test_bff_explicit_cutover.py`、`git diff --check -- apps/bff/scripts/test_bff_explicit_cutover.py`：exit 0。

新 manifest **36 files**，僅加此 BFF regression test；Content SHA256 `8c92173b50e3b55b9a27f4b9f288d430934a7fd6305d1e10ae0e31ca305bff39`。Base/frozen/runtime implementation 未改。本輪僅限既有 BFF contract regression 與 evidence/manifest；未改 Bug366/shared checkout、engine runtime、production provider 或 credentials。待 Parent + Supervisor 對相同下一 PR SHA 審閱；未 merge/dispatch/live/tag。

## Owner-approved fixed DEV image diagnostic implementation

本輪由原 Implementer 在同一 checkout 實作 Owner 接受的唯讀 DEV Actions 診斷與固定 Auth prepare stage/exit-code metadata。Accepted contract：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-deploy-convergence/docs/lwc-358/deployment-engine-dev-diagnostic-owner-direction-r1.md`；暫時 DEV 診斷授權記錄：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-deploy-convergence/docs/lwc-358/deployment-diagnostics-standing-owner-direction.md`。兩份 source records 均未修改。編輯前 HEAD `a90590974722248893a4676084093cf29951e1a4`、tree `1c69404ed0303bf7ea94457163ca09a88e1a6b6a`、branch `Rayer/LWC-358-engine-r2`；merge-base/base `e9ccf490251ed0262481fca94af7387107be6b5f`；frozen spec SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`。目前仍為未提交本地差異，HEAD/tree 未變。

Diagnostic 有獨立 `auth-image-diagnostic` Actions job；wrapper 與 reusable workflow 兩處都只接受 develop、Development、auth、operation `diagnose-auth-image`、release-tag sentinel `diagnostic-36992147920`、artifact sentinel `diagnostic-no-receipt`，並要求輸入 `source_sha == github.sha`。因此 checkout 是日後同一 workflow dispatch ref 的已審查診斷程式碼，而查詢目標固定仍為失敗 run 36992147920 的 Auth tag 與 digest；不再把舊 f6 SHA 當成 engine checkout。這條 job 使用獨立 Development WIF setup，GitHub token 僅 `contents: read` / `actions: read` 與 WIF 所需 `id-token: write`；不帶 GH/Vercel token，不安裝 Node/artifact transport，不下載 checkpoint/receipt，不進正常 recovery validation 或 runtime，不 build/deploy/traffic/alias/tag/write。它捕捉並丟棄 gcloud stdout/stderr，只印固定 operation、exit code/timeout class、digest-format boolean、tag-digest comparison boolean 與 bounded conclusion；成功或失敗均只保留 redacted diagnostic result artifact。沒有任何測試或本地動作接觸真實 provider。

Auth prepare 的真實 `auth.sh → Providers.prepare → support.run → Engine.result()` 路徑現在只傳 allowlisted `build-submit`、`tag-digest-resolve`、`digest-validate` stage 與 exit code；malformed digest 使用固定 exit code 0 表示命令成功但驗證失敗。既有 reason/status/mutation/allowed-next-action 語意保留，permission-denied 只帶出既有安全 action；provider stderr、args、環境或哨兵值不進結果。Shell 分類採 Bash 3 可用的 `tr`，不依賴 Bash 4 小寫擴展。

### 本機驗證

環境為 Python 3.14.6。新 workflow-branch regression 以假 gcloud 執行實際 diagnostic helper，逐一驗證精確兩個 describe calls、沒有 mutation verb、舊 f6 checkout/錯誤 target/selection/ref/tag/artifact sentinel 在 gcloud 前拒絕、digest mismatch/invalid/read error/timeout/tool-unavailable 的有界輸出，以及 stdout/result 不含 provider/token 哨兵。Auth prepare tests 以 TEST ONLY gcloud/go/timeout executables 執行真實 shell 與 production adapters，驗證三個 stage、inner exit code、reason/action 保留及完整 result redaction。未使用 network fallback。

| 精確命令 | 結果 | 保存輸出 |
| --- | --- | --- |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 36 tests OK，29.310s | `evidence/dev-diagnostic-engine.txt` |
| `python3.14 -m unittest -v test_diagnostics test_prepare_diagnostics`（cwd `deploy/engine/tests`） | 9 targeted diagnostic/redaction tests OK，1.494s | `evidence/dev-diagnostic-auth-redaction.txt` |
| `python3.14 scripts/test_engine_workflow.py` | 4 tests OK | `evidence/dev-diagnostic-workflow.txt` |
| `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py'` | 6 tests OK，13.092s | `evidence/dev-diagnostic-bff.txt` |
| Selected shared-CD Frontend authority/alias tests (legacy name filter) | TAP reported 4 pass entries, but 2 were file-level results with `1..0`; only 2 named tests executed. Historical limited coverage; superseded below. | `evidence/dev-diagnostic-frontend-workflow.txt` |
| `bash -n deploy/components/auth.sh && python3.14 -m py_compile deploy/engine/diagnostics.py deploy/engine/support.py deploy/engine/providers.py deploy/engine/engine.py deploy/engine/tests/test_diagnostics.py deploy/engine/tests/test_prepare_diagnostics.py scripts/test_engine_workflow.py && git diff --check` | exit 0、無輸出 | `evidence/dev-diagnostic-static.txt` |
| cwd `/Users/rayer/go/pkg/mod/github.com/rhysd/actionlint@v1.7.12`: `env GOPROXY=off GOMODCACHE=/Users/rayer/go/pkg/mod GOCACHE=/private/tmp/lwc358-actionlint-cache go run ./cmd/actionlint /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/cd.yml /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/recover-deployment.yml` | exit 0、無錯誤 | `evidence/dev-diagnostic-actionlint.txt` |

先前 Frontend workflow regression 使用下列 legacy name filter；它保留為歷史命令，不能把 TAP 的四個 pass entries 解讀為四個 named cases：

```sh
node --test --test-name-pattern='frontend aliases use the shared CD|DEV authority uses the fixed|DEV workflow invokes|production workflow invokes' apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs
```

兩個舊 alternatives 已不符合現行 test names。保留的輸出中，lwc-199 與 authority-reconciliation 檔案各輸出 `1..0`；實際執行的只有後兩個 workflow-invocation named cases。對應 log 未刪改，現已由 PR73 修正版的 4/4 named-case 命令與未過濾完整 suites 取代。

Workflow contract 測試首輪保留一筆失敗：舊 assertion 假設 shared CD 只有一個 `environment` job；現在額外 read-only job 使此數量變為二。測試已改為驗證唯一 runtime action、release protected job 與單獨低權限 Development diagnostic job；後續 4 tests 全通過。原失敗輸出保留在 `evidence/dev-diagnostic-workflow-contract-red.txt`。Actionlint CLI 不在 PATH；第一次 module@version 呼叫受 sandbox DNS 限制，之後使用已快取 actionlint source、`GOPROXY=off` 執行同版檢查，通過。沒有為此擴權或連網。

Live 事實未變：失敗 run 36992147920 的 Cloud Build 成功且 image published，prepare 失敗、runtime skipped；既有 result artifact 沒有 ready receipt。離線測試只能辨識程式各失敗分支及保護 workflow 入口，**不能證實該 live run root cause**。本輪未 dispatch Actions、未 retry、未造 manual receipt、未讀取 provider；Parent/Supervisor 尚需對同一新 content/CI review 後，才可依 Owner 已接受範圍協調一次唯讀診斷。沒有 Production/IAM/credential/provider write、commit、push、PR publish、merge、tag 操作；Bug366 merged code與原 shared worktree未改。

可供後續 PR 使用的完整說明草稿：`evidence/pr-dev-image-diagnostic-body-draft.md`。目前精確 implementation manifest 為 39 個檔案，內容 SHA256 `694bff9b59dbb1d91fcd477e0d68b1f322221ece31852be2fc01a21c997cd823`；base `e9ccf490251ed0262481fca94af7387107be6b5f`、frozen SHA 不變。本地候選尚無新 commit/tree SHA。

## Recovery artifact download token regression repair

Parent 在實際工作差異檢查中發現 release job 曾把 `GH_TOKEN` 從 job env 收窄到 prepare/runtime action steps，但正常 rollback/reactivate/deploy/tag/readback recovery 的 `Download pinned ready artifact or checkpoint` shell step 沒有 token。該步執行 `node deploy/engine/artifacts.cjs download`；transport 的 GitHub API lookup 與 Actions artifact SDK 都讀 `process.env.GH_TOKEN`，所以 recovery 在取得 checkpoint 前會失敗。

修正只在這個 recovery-only download step 加入 `GH_TOKEN: ${{ github.token }}`。下載條件仍是 `inputs.operation != 'release'`，release job 仍排除 `diagnose-auth-image`；diagnostic 保持獨立 job，沒有 GH/Vercel token 或 checkpoint download。新增的 regression 解析真實 `cd.yml`，合併 release job 與 download step env 後檢查有效 token、release/diagnostic job guards、recover-only step condition 和實際 artifacts.cjs download 實作讀取 GH_TOKEN 並呼叫 API/SDK。

先以未修正 workflow 執行 causal regression，斷言實際有效環境的 `GH_TOKEN` 為空而失敗；結果保留於 `evidence/dev-diagnostic-download-token-red.txt`。修正後驗證：

| 精確命令 | 結果 | 保存輸出 |
| --- | --- | --- |
| `python3.14 scripts/test_engine_workflow.py -k pinned_recovery_download_has_effective_github_token`（修正前） | 1 failure；`effective_env.get('GH_TOKEN')` 為 `None` | `evidence/dev-diagnostic-download-token-red.txt` |
| `python3.14 scripts/test_engine_workflow.py -v` | 5 tests OK | `evidence/dev-diagnostic-download-token-workflow.txt` |
| `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py'` | 6 tests OK | `evidence/dev-diagnostic-download-token-bff.txt` |
| Legacy filtered `node --test` invocation on the four Frontend files | 4 TAP entries, but only 2 named tests ran; 2 file results had `1..0`. Historical limited coverage, superseded by PR73 complete suites. | `evidence/dev-diagnostic-download-token-frontend.txt` |
| `python3.14 scripts/test_cd_contract.py` | 67 tests OK | `evidence/dev-diagnostic-download-token-cd.txt` |
| `GOPROXY=off GOMODCACHE=/Users/rayer/go/pkg/mod GOCACHE=/private/tmp/lwc358-actionlint-cache go run ./cmd/actionlint /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/cd.yml /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/recover-deployment.yml`（cwd `/Users/rayer/go/pkg/mod/github.com/rhysd/actionlint@v1.7.12`） | exit 0 | `evidence/dev-diagnostic-download-token-actionlint.txt` |

另執行 `python3.14 -m py_compile scripts/test_engine_workflow.py && git diff --check`，exit 0、無輸出。精確整合 diff 更新於 `evidence/dev-diagnostic-implementation.diff`。環境為 Python 3.14.6；Frontend workflow tests 使用 Node v22.23.2。改動後的 `cd.yml` 和 recovery wrapper 通過 actionlint；未觸發 Actions。測試 log 和此段 evidence 不納入 implementation-content manifest。現行 manifest 為 39 個檔案，內容 SHA256 `4f44306d203c39b76d41f6e4eb3952c2cdcbd0e9f5a5447b091fe4e0b0f3a5ca`。本地修改仍未 commit/push/PR/merge，未 dispatch Actions、未做 provider/credential/IAM/Production/tag 操作。HEAD/tree 仍是 `a90590974722248893a4676084093cf29951e1a4` / `1c69404ed0303bf7ea94457163ca09a88e1a6b6a`，base `e9ccf490251ed0262481fca94af7387107be6b5f`；此 HEAD 不含稍後 publication integration 的 LWC-366 merged source。

## PR73 same-head CI HOLD — workflow contract assertion repair

PR73 reviewed base HEAD 為 `0650fb49608872cf683a9367684e9535e215fe3d`，其 tree `a6ab26815bd2b4a7fca5b88cc18d28d5f1a4fd36` 已包含 `origin/develop`/LWC-366 dependency `f6e6a5e588088294c8829192b308b0813356f9da`。本輪只改既有 Frontend tests 與 manifest/evidence，沒有重置或改寫這個整合基線。

Parent 回報 same-head Actions run `37000610609` 在 `0650fb49608872cf683a9367684e9535e215fe3d` 失敗：Frontend `npm test` 的 Node suite 523 tests 為 520 pass / 3 fail / 0 skip；三個失敗是以下兩個過期 workflow-shape checks 及 LWC-253 case。canonical-CI downstream failed、Frontend build skipped；BFF、lint、typecheck、actionlint、local vertical smoke 成功。Node 20/Ubuntu 訊息是 annotations/warnings，非這三個 assertion failures。此 worker 的 sandbox 無法連到 api.github.com 讀回 raw log；本輪以 Parent 提供的 run summary 為同-head CI 事實來源，並在 local HEAD 重現各 test failures。沒有 rerun CI。

保留原有安全 coverage，只把 expectation 改成現行隔離 diagnostic interface：`lwc-253-authority-reconciliation` 現在要求 release + `auth-image-diagnostic` 兩個 job，唯一 runtime operation 屬於 protected release job，並驗證 per-target concurrency、protected environment、prepare → durable ready → runtime barrier。Diagnostic job 必須獨立限定於 `diagnose-auth-image`，Development environment、read-only contents/actions 加 WIF 所需 id-token permission、無 GH/Vercel credentials、無 runtime engine action 及 artifact checkpoint download。`ci-workflow-contract.test.mjs` 的 recovery options 現在明列 `diagnose-auth-image`，同時要求一般 recovery 排除該 operation， diagnostic wrapper 使用 shared CD 且維持 read-only permission；shared-engine case 同樣驗證唯一 runtime authority 和低權限隔離。沒有刪除、skip 或放寬原有 deployment safeguards。

兩個 red checks 已在修正前本機重現：精確 LWC-253 case 在 `Object.keys(source.jobs) == ['release']` 失敗；完整四套件為 243 tests、242 pass / 1 fail / 0 skip；`ci-workflow-contract.test.mjs` 為 5 tests、3 pass / 2 fail / 0 skip，失敗正是 recovery operation allowlist 與只有 release job 的假設。原始輸出保留於 `evidence/pr73-workflow-repair-focused-red.txt`、`evidence/pr73-frontend-four-red.txt`、`evidence/pr73-ci-workflow-contract-red.txt`。

舊 filtered evidence 的兩個過期 patterns 已以 current named cases 取代。使用以下精確命令時，四個 intended tests 都逐名出現在 TAP 且實際執行通過：`frontend aliases use engine retained recovery and read-back with durable checkpoints`、`DEV authority uses the protected shared engine and durable selected stage barrier`、`DEV workflow invokes the shared engine with fixed authority and explicit receipt inputs`、`production workflow invokes the shared engine with fixed authority and DEV provenance`。原 log 保留作歷史資料，明確標示兩個 `1..0` unmatched file outputs 不代表 named tests 通過。

```sh
node --test --test-name-pattern='frontend aliases use engine retained recovery and read-back with durable checkpoints|DEV authority uses the protected shared engine and durable selected stage barrier|DEV workflow invokes the shared engine with fixed authority and explicit receipt inputs|production workflow invokes the shared engine with fixed authority and DEV provenance' apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs
```

### PR73 local red/green 與受影響 suites

環境 Node v22.23.2、Python 3.14.6。所有命令均在目前整合工作樹上執行；`npm test` cwd 為 `apps/frontend`，其餘 cwd 為 repository root。

| 精確命令 | 結果 | 輸出 |
| --- | --- | --- |
| `node --test --test-name-pattern='DEV authority uses the protected shared engine and durable selected stage barrier' apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs`（修正前） | 0 pass / 1 fail；job list 多出已接受的 `auth-image-diagnostic` | `evidence/pr73-workflow-repair-focused-red.txt` |
| 同上（修正後） | 1 pass / 0 fail / 0 skip | `evidence/pr73-workflow-repair-focused-green.txt` |
| `node --test apps/frontend/tests/ci-workflow-contract.test.mjs`（修正前） | 3 pass / 2 fail / 0 skip | `evidence/pr73-ci-workflow-contract-red.txt` |
| 同上（修正後） | 5 pass / 0 fail / 0 skip | `evidence/pr73-ci-workflow-contract-green.txt` |
| 上方 corrected four-name `node --test` 命令 | 四個完整 named cases 確認逐一出現於 TAP，4 pass / 0 fail / 0 skip | `evidence/pr73-frontend-named-cases.txt` |
| `node --test apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs` | **243 pass / 0 fail / 0 skip** | `evidence/pr73-frontend-four-green.txt` |
| `npm test`（cwd `apps/frontend`） | Node **523 pass / 0 fail / 0 skip**；Vitest **32 files、292 tests pass** | `evidence/pr73-frontend-npm-green.txt` |
| `node --test deploy/engine/tests/artifacts.test.cjs` | 4 pass / 0 fail / 0 skip | `evidence/pr73-transport.txt` |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 36 tests OK | `evidence/pr73-engine.txt` |
| `python3.14 scripts/test_engine_workflow.py -v` | 5 tests OK | `evidence/pr73-engine-workflow.txt` |
| `python3.14 scripts/test_cd_contract.py` | 67 tests OK | `evidence/pr73-cd-contract.txt` |
| `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py'` | 6 tests OK | `evidence/pr73-bff-focused.txt` |
| `npx --no-install eslint tests/lwc-253-authority-reconciliation.test.mjs tests/ci-workflow-contract.test.mjs`（cwd `apps/frontend`） | exit 0 | `evidence/pr73-eslint.txt` |

Canonical manifest 仍採原規則，source/test/operator/frozen-spec file rows 記錄實際 SHA256 與 mode；evidence report 和執行 log 維持 manifest 外，避免自我參照。此輪兩個變更 test 檔已列於 39-file manifest 並重算；其實際 identity 為 `ci-workflow-contract.test.mjs` SHA256 `b08ab715a3f1ca259dfde7fd2626be2571aa798920118cb18213ddfb5d44a73d`, mode `0o644`；`lwc-253-authority-reconciliation.test.mjs` SHA256 `516731f7693f7996dfb365cad1c7c37963c1e4003d8671fafd374d421ab4c57a`, mode `0o644`。目前 manifest 為 39 files，content SHA256 `9336288cd5ae6ee4bc720371bc460f4149cc0b9c1df0b7a683eca69f17ac0a03`，frozen spec SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`。

本地 candidate 的基線 HEAD 為 `0650fb49608872cf683a9367684e9535e215fe3d`，tree `a6ab26815bd2b4a7fca5b88cc18d28d5f1a4fd36`；branch `Rayer/LWC-358-engine-r2`，merge-base `f6e6a5e588088294c8829192b308b0813356f9da`。LWC-366/f6 整合 tree 保持原樣；source diff 只有上述兩個測試檔，另更新 manifest/evidence。完整 tracked candidate diff 已保存至 `evidence/pr73-hold-remediation.diff`。此輪未 commit/push/PR update/merge、未重跑 Actions、未 dispatch 或操作 provider/credential/IAM/Production/tag。待 Parent publication phase 檢視 exact diff 並取得同一新 commit SHA 的 Parent + Supervisor reviews/CI。

## PR73a default-branch workflow discovery blocker — registered DEV wrapper

Owner/Parent 診斷 preflight 記錄（comment `3584-1641`）提供的線上事實：`gh workflow run recover-deployment.yml --ref develop` 回 HTTP 404，沒有建立 run；read-only workflow listing 只列出 canonical `ci`、`cd`、`deploy-dev`、`promote-production`、`provision`，沒有 `recover-deployment.yml`。沒有 provider call。依此改用已註冊 `Deploy Development` wrapper 暴露相同固定唯讀診斷；沒有改 default branch、註冊 `recover` 或擴大 Production/provider/credential/IAM 權限。這是 workflow discovery 的阻塞，並沒有確認先前 Auth prepare 的 root cause。

實作前 HEAD `a042d4150e35830fbc36dbb7cf2a3bbce8ab1ec0` 的 tree 為 `76ff9524855a51f4fa3ef90e7abef73eff6a3643`，與 squash merge `b60dde9fb0318fa327ba2477fb0b796658e71a42` 的 tree 完全相同；branch `Rayer/LWC-358-engine-r2`，merge-base `f6e6a5e588088294c8829192b308b0813356f9da`，frozen spec SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`。保留現有整合 tree，未 reset。只修改已列於 canonical manifest 的 deploy-dev wrapper、既有 workflow/engine/BFF contract tests、operator 文件與 repository skill。正常 DEV release 明確要求 `operation=release` 並維持 default `release`；獨立 diagnostic job 固定 develop/auth/sentinel 輸入，以 `github.sha` 作 checkout source，僅授予 contents/actions read + WIF 所需 id-token，呼叫既有 shared CD diagnostic job。Wrapper 和 shared workflow contract 都要求固定 inputs；診斷不傳 GitHub/Vercel runtime token、不下載 checkpoint/receipt、不進一般 runtime，僅保留 redacted diagnostic result。沒有改 shared engine、CD/recovery workflows 或 provider adapter。

新增的 contract 解析實際 YAML 並檢查 deploy-dev dispatch input、正常 release guard、診斷條件、固定 `with` payload、workflow permissions 與 shared-CD route；shared workflow assertion 另外確認 WIF secret 範圍、無 GH/Vercel credential env、無 engine/receipt download、唯一保存項是 redacted diagnostic result。先前 wrapper 尚無 `operation`/diagnostic job 時，ci-workflow、DEV authority、BFF cutover、engine workflow contract focused regression 分別失敗，輸出保留於 `evidence/dev-wrapper-ci-contract-red.txt`、`dev-wrapper-frontend-red.txt`、`dev-wrapper-bff-contract-red.txt`、`dev-wrapper-engine-contract-red.txt`；修正後四組都通過。完整 suite 均未使用 name filter，並確認 skipped 為零。

精確本機命令與輸出（Python 3.14.6、Node v22.23.2）：

| 命令 | 結果 | 輸出 |
| --- | --- | --- |
| `python3.14 scripts/test_engine_workflow.py -v` | 5 tests OK | `evidence/dev-wrapper-engine-contract-final.txt` |
| `node --test apps/frontend/tests/ci-workflow-contract.test.mjs` | 5 pass / 0 fail / 0 skip | `evidence/dev-wrapper-ci-contract-final.txt` |
| `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py' -v` | 6 tests OK | `evidence/dev-wrapper-bff-contract-final.txt` |
| `go test ./cmd/bff`（cwd `apps/bff`） | package tests OK，含 deploy workflow YAML / r2 contract invocation | `evidence/dev-wrapper-bff-go-final.txt` |
| `node --test apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs` | 243 pass / 0 fail / 0 skip | `evidence/dev-wrapper-frontend-four-final.txt` |
| `npm test`（cwd `apps/frontend`） | Node 523 pass / 0 fail / 0 skip；Vitest 32 files、292 tests pass | `evidence/dev-wrapper-frontend-npm-final.txt` |
| `python3.14 scripts/test_cd_contract.py` | 67 tests OK | `evidence/dev-wrapper-cd-contract-final.txt` |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 36 tests OK | `evidence/dev-wrapper-engine-final.txt` |
| `npx --no-install eslint tests/ci-workflow-contract.test.mjs tests/lwc-253-vercel-dev-authority.test.mjs`（cwd `apps/frontend`） | exit 0 | 無輸出 |
| cached actionlint v1.7.12，offline；檢查 `deploy-dev.yml`、`cd.yml`、`recover-deployment.yml` | exit 0 | `evidence/dev-wrapper-actionlint-final.txt` |
| `python3.14 -m py_compile scripts/test_engine_workflow.py apps/bff/scripts/test_bff_explicit_cutover.py && git diff --check` | exit 0 | 無輸出 |

未執行建議的 live payload：

```sh
gh workflow run deploy-dev.yml --ref develop \
  -f operation=diagnose-auth-image \
  -f components=auth \
  -f release_tag=diagnostic-36992147920 \
  -f artifact_id=diagnostic-no-receipt
```

`dev_artifact_id` 留空並採 wrapper default；不提供 `source_sha`，workflow 固定使用所選 `develop` run 的 `github.sha`。GitHub 官方手動 dispatch 文件指出 workflow 必須存在 default branch，且 `--ref` 指定執行 ref；本次確認的阻塞是 `recover-deployment.yml` 不在註冊清單，因此修復只新增到既有註冊 wrapper。尚待遠端確認的細節：當已註冊 default-branch workflow YAML 的 input schema 尚未包含新 choice 時，GitHub 是否接受 develop ref 版本新增的 `operation=diagnose-auth-image`，並使用所選 develop revision 的 workflow 定義進行 job routing。這要等 Parent 發布同一 exact content 並 review/CI 後，透過受控 diagnostic dispatch 判定；本輪沒有送出 dispatch。Owner 的 standing DEV read-only 診斷範圍不需逐次重複申請，本輪明確限於 local-only。

候選增量差異：`evidence/dev-wrapper-registered-entry.diff`。Canonical `implementation-content.json` 已依 39 個實際檔案的 bytes/modes 重算，content SHA256 為 `0d89071427956ad2d10724bfe1965e790a4a6e02cfb1fe6be6e8d8196fb73434`；frozen spec SHA256 仍為 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`。本候選仍是未提交工作樹 HEAD `a042d4150e35830fbc36dbb7cf2a3bbce8ab1ec0`、tree `76ff9524855a51f4fa3ef90e7abef73eff6a3643`，reviewed merge commit `b60dde9fb0318fa327ba2477fb0b796658e71a42` tree 相同。新 exact content 尚待 Parent publication、Parent + Supervisor 對同一 SHA 審閱與 CI；沒有 commit/push/PR/merge/Actions dispatch/provider/IAM/credential/Production/tag 操作。LWC-366 merged source 和 shared worktree 均未修改。Auth prepare root cause 仍 UNKNOWN，沒有 retry 或人工 receipt。

## PR74 Supervisor HOLD — reusable-workflow permission ceiling

Reviewed exact head `7f39d4e0a4a9508dc263db9c63d62a4f8adec6c9` received formal Supervisor HOLD `2bbcf5666991dc294952438c`; CI `37009238533` was reported in progress and does not supersede that decision. The Supervisor finding is a static cross-workflow permissions conflict: Deploy Development and recovery diagnostic jobs grant `contents: read`, but both previously called the entire `cd.yml` reusable workflow, which contains the normal release job requesting `contents: write`. A caller's GITHUB_TOKEN permissions are a ceiling for the called workflow and may only be reduced, so a skipped release `if` does not remove its permission declaration from the called workflow contract. GitHub's reusable workflow reference states this caller-to-callee restriction: https://docs.github.com/en/actions/reference/workflows-and-actions/reusing-workflow-configurations. This was an offline contract finding, not an independently reproduced live project dispatch failure.

Before changing the workflow graph, added a regression that reads both real wrapper YAML files, follows each diagnostic job's `uses` path, and compares caller permissions with called-workflow and child-job permissions. Against the old graph it failed with: `deploy-dev.yml grants read but called ./.github/workflows/cd.yml release requests write for contents`; captured in `evidence/pr74-permission-ceiling-red.txt`. It also requires both callers to reference the same dedicated workflow containing only `auth-image-diagnostic`, with no permission level above `contents: read`, `actions: read`, `id-token: write`.

The minimal structure fix extracts the fixed Auth diagnostic into `.github/workflows/cd-auth-image-diagnostic.yml`, a standalone reusable workflow with one job and only read/read/WIF-id-token permissions. Both `deploy-dev.yml` and `recover-deployment.yml` diagnostic jobs call this standalone file and pass only `source_sha`; the workflow enforces `develop` plus equality to caller `github.sha` and hardcodes the Auth target, operation, tag, no-receipt sentinel and empty DEV artifact. It references only the two WIF secrets, has no GH/Vercel token, artifact transport, receipt/checkpoint download, deployment-engine action, or normal runtime. The duplicate diagnostic job was removed from `cd.yml`. Normal Deploy Development release still calls `cd.yml` with the same explicit inputs; normal recovery still calls `cd.yml` with its full existing environment/source/components/tag/operation/artifact tuple. Tests assert both call maps and the recovery download token contract, so the extraction does not narrow normal release/recovery behavior.

The retained failing early-integration logs are also useful history: `pr74-engine-initial.txt` caught an incomplete normal recovery `with` map during test editing, and the two `*-initial.txt` Node logs caught an assertion that mistook the required `diagnostic-no-receipt` sentinel for an actual receipt operation. The workflow/test assertions were corrected; final contracts below exercise the complete map and test only for receipt/checkpoint operations in diagnostic steps.

### PR74 local verification

| Exact command | Result | Output |
| --- | --- | --- |
| `python3.14 scripts/test_engine_workflow.py -k diagnostic_callers_obey_readonly_reusable_workflow_permission_ceiling -v` before extraction | 1 expected failure showing `contents: write` above both diagnostic callers' read ceiling | `evidence/pr74-permission-ceiling-red.txt` |
| `python3.14 scripts/test_engine_workflow.py -v` | 6 tests OK, including cross-caller/callee permission comparison and both call paths | `evidence/pr74-engine-final.txt` |
| `node --test apps/frontend/tests/ci-workflow-contract.test.mjs` | 5 pass / 0 fail / 0 skip; checks both callers and standalone workflow | `evidence/pr74-ci-contract-final.txt` |
| `node --test apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs` | 243 pass / 0 fail / 0 skip, no name filter | `evidence/pr74-frontend-four-green.txt` |
| `python3.14 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py' -v` | 6 tests OK; both diagnostic call sites and normal recovery call map checked | `evidence/pr74-bff-final.txt` |
| `go test ./cmd/bff` (cwd `apps/bff`) | package tests OK, including the shared workflow contract | `evidence/pr74-bff-go-final.txt` |
| `python3.14 scripts/test_cd_contract.py` | 68 tests OK | `evidence/pr74-cd-contract-green.txt` |
| `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 36 tests OK | `evidence/pr74-engine-full-green.txt` |
| `npm run test:component -- --no-file-parallelism` (cwd `apps/frontend`) | all 32 Vitest files / 292 tests pass | `evidence/pr74-vitest-serial-final.txt` |
| `npx --no-install vitest run tests/lwc-admin-pipeline-terminal-status.test.tsx` (cwd `apps/frontend`) | 5 tests pass in isolation | `evidence/pr74-vitest-admin-isolated.txt` |
| cached actionlint v1.7.12 on deploy-dev/recovery/cd/cd-auth-image-diagnostic workflows, with `GOPROXY=off` | exit 0 | `evidence/pr74-actionlint-green.txt` |
| ESLint on `ci-workflow-contract`, `lwc-253-authority-reconciliation`, `lwc-253-vercel-dev-authority` | exit 0 | command output empty |

Two unfiltered `npm test` attempts both ran all 523 Node tests successfully with zero skips, then had one failure each in different, unchanged Vitest files: run 1 `lwc-admin-pipeline-terminal-status.test.tsx` (that file passed when run alone); run 2 `lwc-345-export-panel.test.tsx`. The full serial Vitest suite passed all 32 files / 292 tests without filtering. These default-parallel suite failures remain visible rather than being reported as `npm test` success; their outputs are `evidence/pr74-frontend-npm-default-run1.txt` and `evidence/pr74-frontend-npm-default-run2.txt`. No changed workflow test failed in the full four-suite run.

Prior local candidate snapshot (superseded by the latest test-only remediation below) contained 40 files with content SHA256 `280c5deec4bf1d864a2c929c4d6d9ac099cad1cb71d9bc748a3e803e88e7168f`; its frozen spec SHA256 was `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. Current worktree remains on branch `Rayer/LWC-358-engine-r2`, HEAD `7f39d4e0a4a9508dc263db9c63d62a4f8adec6c9`, tree `e7301a7fc380030287515094fa27ce7edacdffad`, merge-base `b60dde9fb0318fa327ba2477fb0b796658e71a42`. Exact permission remediation diff: `evidence/pr74-permission-ceiling-remediation.diff`. Parent owns publication and same-new-head reviews/CI. This worker did not commit, push, update PR, merge, dispatch Actions, access providers, or write IAM/credentials/Production/tags. Original Auth prepare root cause remains UNKNOWN; no retry or manual receipt.


## PR74 default npm test synchronization remediation

Parent accepted a narrowly scoped test-harness correction. In both `polls the accepted execution` cases, the first status request now signals explicitly and holds its `RUNNING` response until the test releases it. Under fake timers, the test releases that response inside React `act`, advances the next scheduled poll timer, and asserts the second request arguments plus the correct terminal UI state. A `finally` block releases the held response, clears fake timers, and restores real timers. The success and failure paths both use this schedule. `AdminClient` polling, its 1000 ms interval, API behavior, Vitest parallel settings, and all other tests remain unchanged.

The first implementation attempt used Testing Library `waitFor` while Vitest fake timers were active and timed out all five tests in this file. That raw output is preserved as `evidence/pr74-admin-terminal-sync-experiment1-red.txt`; the harness now uses Vitest `vi.waitFor` only to observe the first immediate status call. The earlier complete 7f39 baseline output was copied into `evidence/pr74-frontend-baseline-7f39-npm.txt` without removing its original `/private/tmp` copy. Baseline HEAD/tree were `7f39d4e0a4a9508dc263db9c63d62a4f8adec6c9` / `e7301a7fc380030287515094fa27ce7edacdffad`; default `npm test` completed with Node 523 pass / 0 fail / 0 skip and Vitest 32 files, 292 pass / 0 fail / 0 skip. The baseline was archived from HEAD into a disposable directory and used the same installed Frontend `node_modules` and inherited test environment.

### Current local verification

| Exact command | Result | Full output |
| --- | --- | --- |
| `npx --no-install vitest run tests/lwc-admin-pipeline-terminal-status.test.tsx` (cwd `apps/frontend`) | 1 file; 5 pass / 0 fail / 0 skip | `evidence/pr74-admin-terminal-sync-focused-final.txt` |
| `npx --no-install eslint tests/lwc-admin-pipeline-terminal-status.test.tsx` (cwd `apps/frontend`) | exit 0 | `evidence/pr74-admin-terminal-sync-eslint.txt` |
| `npm run typecheck` (cwd `apps/frontend`) | exit 0 (`tsc --noEmit`) | `evidence/pr74-admin-terminal-sync-typecheck.txt` |
| `npm test` (cwd `apps/frontend`, default parallel Vitest entrypoint) | exit 0; Node 523 pass / 0 fail / 0 skip; Vitest 32 files, 292 pass / 0 fail / 0 skip | `evidence/pr74-frontend-npm-terminal-sync.txt` |

The prior unfiltered failures remain intact in `evidence/pr74-frontend-npm-default-run1.txt` and `evidence/pr74-frontend-npm-default-run2.txt`, and Parent's reproduction remains at the recorded profile scratch path. The latest full default run passed; this does not establish the separate LWC-345 failure's cause, so it is retained as historical evidence without a flake claim. The canonical manifest then covered 41 implementation/test/operator/spec files with content SHA256 `86d50983d9d3073646665d6a258d4dddb12222bc30ff8cbfad17196c7a3d34a1`; the new local candidate fingerprint is recorded below. No commit, push, PR edit, merge, Actions dispatch, provider/IAM/credential write, Production operation, or tag operation occurred.

## Owner-accepted Auth/BFF asynchronous Cloud Build completion contract

Owner accepted the completion-contract change at `3584-1660` and SSOT. The frozen r2 spec remains byte-identical (SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`); the separate accepted appendix documents the change before implementation. Auth/BFF now submit Cloud Build asynchronously with explicit project and `global` location, `--async --format=json --quiet`; the response must supply a verified `(project_id, location, build_id)`. The engine persists a submitting checkpoint before create, persists the verified ID before any status read, polls that exact build by ID with bounded status-only calls, and proceeds to existing tag/digest validation and ready receipt only after SUCCESS. Failed/unknown/pending results retain the actual build ID when known; resume reconciles the same ID and never blindly resubmits. Unknown submission without a validated ID remains a no-ID marker. Logs are outside the readiness gate and are queried only on demand using the fixed build-ID filter documented in the accepted appendix. No raw log payloads, IAM changes, logging-mode changes, or live provider reads/writes were used.

The deleted direct-mutation positive assertions in `scripts/test_cd_contract.py` were not dropped. The remaining CD shortcut case now verifies that direct Auth/BFF build/deploy invocation fails closed. Their positive coverage moved to `deploy/engine/tests/test_engine.py::test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback`, which runs the actual Engine against ready Auth and BFF receipts, asserts there are no new build submissions, captures actual `state.json` writes to prove the accepted revision is durable while the component is pending and before strict readback, checks the selected immutable revision digests and 100% traffic readback, verifies BFF environment values, and rejects `--remove-env-vars`. Actual transport wiring remains covered separately by `test_save_reaches_sdk_with_actual_engine_upload_argv` and `test_runtime_guard_reads_checkpoint_with_actual_engine_latest_argv`; those tests pass the real engine-generated arguments into the SDK stub for both upload and latest.

### Prior candidate verification (before provider schema correction)

Every Python test command below explicitly sets `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`; Python 3.9 is not a valid substitute because its `tarfile.extractall` behavior lacks the required `filter` parameter. Results:

| Exact command | Actual result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` | 48 tests in 35.490s, `OK`; includes 11 async submission/identity/status/digest/recovery tests and the migrated Auth/BFF positive runtime test as it stood before the additional accepted-checkpoint assertion. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest test_engine.Acceptance.test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback -v` (cwd `deploy/engine/tests`) | 1 test in 1.378s, `OK`; rerun after adding the accepted-revision durable checkpoint assertion. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 scripts/test_cd_contract.py` | 68 tests in 25.162s, `OK`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s scripts -p 'test_*auth_config_contract.py' -v` | 31 tests in 74.499s, `OK` across Auth, BFF, and Production configuration contracts. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch go test ./... -v -count=1 -race` (cwd `apps/bff`) | Previously completed successfully after product changes; the suite covers BFF packages and deployment source applicability. Parent canceled a redundant rerun; no Go source changed in this async-build update. Existing package evidence: `evidence/go-deployment.txt`, `evidence/pr74-bff-go-final.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch node --test deploy/engine/tests/artifacts.test.cjs` | 4 pass / 0 fail; unchanged transport artifact helper. |
| `git diff --check` | exit 0. |
| `bash -n deploy/components/auth.sh deploy/components/bff.sh` | exit 0. |
| `python3.14 -c 'import ast,pathlib; paths=[pathlib.Path("deploy/engine/engine.py"),pathlib.Path("deploy/engine/providers.py"),pathlib.Path("deploy/engine/support.py"),pathlib.Path("deploy/engine/tests/fake_provider.py"),pathlib.Path("deploy/engine/tests/test_engine.py"),pathlib.Path("deploy/engine/tests/test_prepare_diagnostics.py"),pathlib.Path("deploy/engine/tests/test_build_submission.py"),pathlib.Path("scripts/test_cd_contract.py")]; [ast.parse(p.read_text(), filename=str(p)) for p in paths]; print("AST OK:", len(paths), "files")'` | `AST OK: 8 files`. |

The canceled redundant Go rerun is not recorded as a failure. Earlier Python 3.9 `extractall(filter=...)` failures remain an environment limitation, not a pass for all Python interpreters. The historical live Auth prepare cause for release `37018547533` remains UNKNOWN; this local change improves the recorded submit/status stage and does not assert that the old failure's cause was identified or live-fixed. No live build, provider, Actions dispatch, credentials, IAM, Production, tag, or Git publication was exercised.

The superseded local candidate at branch `Rayer/LWC-358-engine-r2`, HEAD `8ee9bed045c79dc72ad2d7d9ff551359e15b54c5`, tree `fe1afa8d0b262a27402ce0b1cdb8429b36e71719` had a 43-file manifest fingerprint `87e2a94dc67a738de1f875d01ca1a80a2a6adeb6a73ae2fbaeff7e5128596944`. The current numeric-name correction and identity regressions are recorded below.

## Parent intake 3584-1661 — numeric Cloud Build project resource name

Parent's bounded read-only result showed a Cloud Build response whose `projectId` is the configured `llm-wiki-cloud` ID while its canonical resource name uses the numeric project number in `projects/{number}/locations/global/builds/{id}`. The previous adapter required the resource-name segment to equal the project ID, so it rejected this valid provider shape. This worker made no live provider call.

The adapter now runs `gcloud projects describe CONFIGURED_PROJECT_ID --format=json --quiet` as a read-only identity lookup before Auth/BFF submit. It requires exact `projectId` equality and a canonical decimal `projectNumber`. A Cloud Build resource name is accepted only when the project segment is either the configured ID or that exact verified number; the response `projectId`, required resource name, global location, and exact build ID remain independently checked. An arbitrary number, wrong resource ID, missing name, or contradictory project ID fails closed. If identity lookup fails before submit, no build is created; on resume the existing ID stays unknown, status is not queried under an unverified mapping, and no replacement build is submitted. No IAM or Cloud Build setting is changed.

Offline fake CLIs use numeric project-number resource names for both submit and describe, and return the configured-ID-to-number mapping from an explicit project-identity lookup. Regressions cover numeric names on submit and status success; a different valid mapped project number to prevent hard-coding; continued acceptance of project-ID names; wrong number, wrong resource ID, missing resource name, wrong response project, wrong/malformed lookup identity; and lookup failure on resume followed by reconciliation of the same build ID without another submit.

### Current offline verification

All Python test commands below explicitly set `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and use Python 3.14. Python 3.9's missing `tarfile.extractall` filter support remains an environment limitation.

| Exact command | Actual result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_build_submission.py' -v` | 15 tests in 7.850s, `OK`; includes positive/negative submit and status numeric-name cases, lookup mismatch/failure, missing name, wrong ID, and same-ID resume. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` | 52 tests in 38.482s, `OK`; run after production adapter/engine changes and numeric fakes, before the final missing-name fake assertions were added. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 scripts/test_engine_workflow.py -v` | 6 tests in 0.018s, `OK`. |
| `python3.14 -c 'import ast,pathlib; paths=[pathlib.Path("deploy/engine/providers.py"),pathlib.Path("deploy/engine/engine.py"),pathlib.Path("deploy/engine/tests/fake_provider.py"),pathlib.Path("deploy/engine/tests/test_prepare_diagnostics.py"),pathlib.Path("deploy/engine/tests/test_build_submission.py")]; [ast.parse(p.read_text(),filename=str(p)) for p in paths]; print("AST OK",len(paths))'` | `AST OK 5`. |
| `git diff --check` | exit 0. |

The async-build test file was rerun after the final fixture additions and passed all 15 tests; the full 52-test engine suite passed with the production code unchanged by those last test-only cases. Workflow contracts passed on the numeric-name production code. Parent's earlier Go race result remains applicable because no Go source changed; no redundant Go/frontend suites were rerun. No live provider/Actions/build, credentials, IAM, Production, tag, or Git publication occurred.

The updated canonical manifest records exact bytes and modes for 43 files; final content SHA256 `44e51ca5158a1340aed7f848d29fcefed620b3fa5a4f0f59e344c15d43105b7d`. The frozen r2 spec remains byte-identical at SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. The branch remains on the unchanged base commit/tree `8ee9bed045c79dc72ad2d7d9ff551359e15b54c5` / `fe1afa8d0b262a27402ce0b1cdb8429b36e71719`; the candidate is still local and uncommitted. Parent must verify that the runtime principal already has the required read-only project identity visibility; this worker made no live lookup and made no IAM change.

## PR75 CI 37049594672 — BFF cutover contract migration

PR75's exact reviewed parent head was `402269fa21fe1564d6cb32ffdbf341283b349696`. CI run `37049594672` failed the retained BFF suite because `test_bff_candidate_cutover_revalidates_after_candidate_creation` called `deploy/components/bff.sh mutate`; that legacy path reached `bff_build_image()` and correctly stopped with `BFF builds must be prepared by the deployment engine` before the test's expected candidate update. The original raw CI output remains at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/pr75-ci-37049594672-failed.txt`, SHA256 `f69e7dfede5ce80a06df41be9d1554042ef2f0507f1a686ee0969900d9cdc848` (109 discovered, four after-candidate subtests failed). It was read only and left unchanged.

The old test grouped seven cases: `branch-before` / `branch` checked `git rev-parse origin/develop` before and after candidate creation; `rerun-before` / `rerun` checked the pinned Actions run attempt; `failure-before` / `failure` checked run/job failure; `success` exercised the old direct component path. Under accepted r2, repeated mutable-branch and canonical CI run/attempt/job polling is explicitly removed (frozen spec Stage 2 and accepted Owner clarification 3584-1669). Those six freshness predicates are retired; they are not equivalent to `runtime_guard` or its per-target runtime checkpoint protection. No branch re-poll or CI-attempt gate was restored. The old success-path behavior is covered on the actual production Engine path below. The component entry remains fail-closed when called directly.

Coverage mapping in the current candidate:

- `test_retired_bff_build_entry_fails_closed` invokes the real legacy `bff.sh build` entry with test-only `docker`/`gcloud` sentinels and verifies the explicit rejection happens before either provider tool runs.
- `test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback` runs the production Engine and provider adapter with retained Auth/BFF ready receipts. For BFF it checks the candidate update uses `--no-traffic`, desired environment config is sent without `--remove-env-vars`, exact revision and service readbacks occur between candidate creation and `update-traffic`, the prior revision still holds 100% traffic before cutover, the final route names the candidate, and strict image/config/observation checks pass. Stage 2 creates no build.
- `test_bff_incompatible_receipt_identity_fails_before_provider_mutation` uses real Engine receipt validation and separately changes the BFF profile, input, and file identity. Each mismatch returns `artifact-source-incompatible` before any service update, traffic update, or replacement. This is current artifact applicability coverage; it is not presented as equivalent to the retired Git branch or Actions freshness assertions.
- `test_bff_candidate_readback_failure_compensates_without_unverified_cutover` makes the fake provider return a candidate whose image readback is invalid. The Engine observes the failure, restores and verifies the retained pre-state, leaves prior traffic intact, records `failed_rolled_back`, and issues no `update-traffic`. This covers the accepted no-unverified-cutover and compensation behavior for readback failure, not the removed mutable-branch/CI polling behavior.
- `test_shared_bff_path_preserves_cutover_safety_boundaries` in the retained suite invokes all three Engine acceptance tests above as actual unittest cases; they are also run directly in the focused command below. The fake provider's `traffic_before_cutover` record is test-only evidence of the route immediately before activation.

Offline verification used Python 3.14.6 and explicitly set the designated scratch directory. The first sandbox-contained focused Engine attempt failed in `setUpClass` before running a test because Go could not create its work directory under that scratch path. The first sandbox-contained canonical run discovered 109 tests but exited 1 with one failure and five errors: the Go-dependent checks could not create the same Go work directory, and two unrelated local-development tests could not bind loopback sockets. Those sandbox-blocked outcomes are not acceptance passes and are retained here as environment-blocked attempts. The same exact offline commands were then run with one-use sandbox escalation; no network/provider operation was involved.

| Exact command and working directory | Actual result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest test_engine.Acceptance.test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback test_engine.Acceptance.test_bff_incompatible_receipt_identity_fails_before_provider_mutation test_engine.Acceptance.test_bff_candidate_readback_failure_compensates_without_unverified_cutover -v` (cwd `deploy/engine/tests`) | 3 tests in 2.795s, `OK`; each named regression passed. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch python3.14 -m unittest discover -s scripts -p 'test_*.py' -v` (cwd `apps/bff`) | Full canonical retained suite: 109 tests in 38.137s, `OK`; no skip/filter, including the fail-closed legacy entry and nested production-Engine BFF success/applicability/compensation cases. |
| `python3.14` AST parse of `apps/bff/scripts/test_bff_explicit_cutover.py`, `deploy/engine/tests/test_engine.py`, and `deploy/engine/tests/fake_provider.py` | `BFF test AST OK`; engine/fake cases also loaded and passed in the named tests. |
| `git diff --check` | exit 0, no output. |

The current edits touch only `apps/bff/scripts/test_bff_explicit_cutover.py`, `deploy/engine/tests/test_engine.py`, and test-only `deploy/engine/tests/fake_provider.py`; this evidence report and the manifest were also updated. The canonical manifest lists 43 files, with exact bytes/modes verified and content SHA256 `afe3464d75fb245b49776d9407b25831d1975e8a3a56336f9ccda3615d20cdaf`. Frozen r2 remains byte-identical at SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. This is an uncommitted local candidate on branch `Rayer/LWC-358-stage1-resume-8a38c7f2`; HEAD remains the historical PR75 head `402269fa21fe1564d6cb32ffdbf341283b349696` (no new source SHA is claimed). No Git metadata/publication, Actions dispatch, live provider/build, credential/IAM, Production, or tag operation occurred.

## DEV run 37055245620 — Frontend stage metadata repair

The separate causal report is `evidence/dev-37055245620-frontend-prepare-diagnostic.md`. This bounded offline repair adds only four fixed stage labels to the actual Frontend project-readback, npm CI, Vercel pull, and Vercel build calls in `deploy/engine/providers.py`. Optional stage parameters thread the first label through `Providers.project()` and `Providers.api()`; other callers keep the default and command behavior is unchanged. Command order, argv, cwd, environment construction, timeout values, and output handling were not modified. The run's underlying Frontend failure remains UNKNOWN; no cwd/configuration or timeout hypothesis was applied.

The new real-Engine regression drives test-only child-process fakes for exit and timeout at all four stages. It checks stage-only bounded serialization/redaction (including argv, token, environment, and child-output sentinels), stops before later commands or the ready barrier, creates no Frontend receipt/archive, enters no runtime path, and retains a compatible Auth fixture receipt byte-for-byte without Auth resubmission. The fake Auth receipt exists only in the offline test.

Actual checks on Python 3.14.6 with explicit `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`:

| Exact command | Actual result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_prepare_diagnostics.py' -v` | 6 tests in 1.625s, `OK`; includes all 8 new stage × failure-mode cases. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` | 62 tests in 136.391s, `OK`, including production adapter/orchestration and transport integration cases. The full run preceded only extra test assertions for forbidden serialized values; production source was unchanged, and the final diagnostics suite passed after those assertions. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 scripts/test_engine_workflow.py -v` | 6 workflow contract tests in 0.018s, `OK`. |
| `git diff --check` | exit 0, no output after all source/test edits. |

The full BFF script suite was not repeated because this repair changed no BFF-owned source or tests; the full engine suite exercised its retained BFF acceptance coverage. Early red outputs were confined to two newly written test-fixture expectation mistakes (Auth digest read omitted from the fake trace, then expected cwd not accounting for the fake repository root); both were corrected before the green runs. No live release, provider, Cloud Build, Vercel/npm network, credential, IAM, Production, Git metadata, or publication operation occurred. Current manifest `implementation-content.json` has 43 exact byte/mode rows and content SHA256 `8eb0b114ffddc038e1132585f4e8d379d68fd4e6c90da8d75df0ee97fb2eec86`; frozen r2 SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. HEAD/tree are still `09fc2435e1e16593ab38ec57259a0b9bc229d0ae` / `d989e999371a98944b18ff53612c71548c700d18`; edits remain local and uncommitted. Existing compatible Auth artifact `11248677083` was not rebuilt, and this worker did not dispatch or retry any Action.

## DEV 37061649417 — pinned Vercel project context

The separate offline source reproduction and candidate record is [dev-37061649417-vercel-env-mapping.md](evidence/dev-37061649417-vercel-env-mapping.md). It verifies the exact cached Vercel 59.11.7 linked-project gate and build-utils 14.9.1 alias resolver, then tests the production Frontend prepare adapter's child argv/environment against that source boundary. The local change maps the API-readback-validated Vercel team to `VERCEL_ORG_ID`, fixes `VERCEL_PROJECT_ID` to the already-validated selected project, and removes conflicting legacy NOW aliases only from pull/build subprocess environments.

The initial full 64-test run had one test-harness serialization failure; it is preserved separately. After the projection was corrected, the focused provider integration passed and the full deployment-engine suite passed 64 tests in 136.678s. All four Frontend diagnostics stages retain exit/timeout coverage, and Auth receipt retention/no-rebuild assertions remain green. Exact outputs are in `evidence/dev-37061649417-vercel-env-focused.txt`, `evidence/dev-37061649417-vercel-env-engine.txt`, and `evidence/dev-37061649417-vercel-env-engine-green.txt`.

This source-contract discrepancy is not a proven cause of live run 37061649417: the historical result does not record ORG/NOW variable presence and no later Actions run has exercised the patch. The updated manifest contains 46 byte/mode rows, content SHA256 `3dfb6aefcaa9c37a63a8280d64c2f7b499d45b53e53653e1872ff6a1882359bc`, and unchanged frozen spec SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. Current HEAD remains pre-edit `95c466eb4ccd71302a11ff416de8180d55182d9d`, tree `3216e0b20e8e0567ae655c82a506ddf46a7c7857`; changes are local and uncommitted. No provider, Action, credential/IAM, Production, tag, or Git publication operation was performed.
