# LWC-358 implementation evidence

Status: PR71 CI acceptance repair 已完成本機驗證，待 Parent + Supervisor 對新 final PR SHA 複審。歷史：Supervisor f581daec8e88d84cb4432ea3 對 exact bbc0 implementation snapshot PASS；Owner 已授權本次 commit / push / PR 至 develop，供 Parent + Supervisor 審閱相同 final PR SHA。以下保留各輪歷史證據；live Actions / provider / tag / UAT NOT RUN，未授權 merge 或 dispatch。

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
| Selected shared-CD Frontend authority/alias workflow tests | 4 pass / 0 fail / 0 skip | `evidence/dev-diagnostic-frontend-workflow.txt` |
| `bash -n deploy/components/auth.sh && python3.14 -m py_compile deploy/engine/diagnostics.py deploy/engine/support.py deploy/engine/providers.py deploy/engine/engine.py deploy/engine/tests/test_diagnostics.py deploy/engine/tests/test_prepare_diagnostics.py scripts/test_engine_workflow.py && git diff --check` | exit 0、無輸出 | `evidence/dev-diagnostic-static.txt` |
| cwd `/Users/rayer/go/pkg/mod/github.com/rhysd/actionlint@v1.7.12`: `env GOPROXY=off GOMODCACHE=/Users/rayer/go/pkg/mod GOCACHE=/private/tmp/lwc358-actionlint-cache go run ./cmd/actionlint /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/cd.yml /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/recover-deployment.yml` | exit 0、無錯誤 | `evidence/dev-diagnostic-actionlint.txt` |

Frontend workflow regression 的精確命令：

```sh
node --test --test-name-pattern='frontend aliases use the shared CD|DEV authority uses the fixed|DEV workflow invokes|production workflow invokes' apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs
```

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
| `node --test --test-name-pattern='frontend aliases use the shared CD|DEV authority uses the fixed|DEV workflow invokes|production workflow invokes' apps/frontend/tests/lwc-199-vercel-alias-promotion.test.mjs apps/frontend/tests/lwc-253-authority-reconciliation.test.mjs apps/frontend/tests/lwc-253-vercel-dev-authority.test.mjs apps/frontend/tests/lwc-258-vercel-production-auth-env.test.mjs` | 4 pass / 0 fail / 0 skip | `evidence/dev-diagnostic-download-token-frontend.txt` |
| `python3.14 scripts/test_cd_contract.py` | 67 tests OK | `evidence/dev-diagnostic-download-token-cd.txt` |
| `GOPROXY=off GOMODCACHE=/Users/rayer/go/pkg/mod GOCACHE=/private/tmp/lwc358-actionlint-cache go run ./cmd/actionlint /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/cd.yml /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/.github/workflows/recover-deployment.yml`（cwd `/Users/rayer/go/pkg/mod/github.com/rhysd/actionlint@v1.7.12`） | exit 0 | `evidence/dev-diagnostic-download-token-actionlint.txt` |

另執行 `python3.14 -m py_compile scripts/test_engine_workflow.py && git diff --check`，exit 0、無輸出。精確整合 diff 更新於 `evidence/dev-diagnostic-implementation.diff`。環境為 Python 3.14.6；Frontend workflow tests 使用 Node v22.23.2。改動後的 `cd.yml` 和 recovery wrapper 通過 actionlint；未觸發 Actions。測試 log 和此段 evidence 不納入 implementation-content manifest。現行 manifest 為 39 個檔案，內容 SHA256 `4f44306d203c39b76d41f6e4eb3952c2cdcbd0e9f5a5447b091fe4e0b0f3a5ca`。本地修改仍未 commit/push/PR/merge，未 dispatch Actions、未做 provider/credential/IAM/Production/tag 操作。HEAD/tree 仍是 `a90590974722248893a4676084093cf29951e1a4` / `1c69404ed0303bf7ea94457163ca09a88e1a6b6a`，base `e9ccf490251ed0262481fca94af7387107be6b5f`；此 HEAD 不含稍後 publication integration 的 LWC-366 merged source。
