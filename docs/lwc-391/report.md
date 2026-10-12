# LWC-391 full-recompile-r2 實作與驗證報告

## 來源與範圍

- 來源 worktree：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-390-391-profile-full-recompile`
- 分支：`Rayer/LWC-390-391-profile-full-recompile`
- develop 基準與目前 `origin/develop`：`14cdc4bd663ec4e7a2ccfbde72f0f919426931c1`；最後確認兩者相同。
- 本次只改 LWC-391 full-recompile-r2 的 BFF、既有 PipelineClient 最小模式選擇、相關測試與產生的 Swagger 文件；沒有改 LWC-390/Profile 全頁、Project/使用者資料、API schema、top-k、provider、IAM、credentials 或 Production。
- 主分支實作前的 tracked diff SHA-256：`e4ddccd860c9114a1c90d45bfa0f5b174f2d06ea8b5b0f0b5c07d38369c06874`。本報告與證據檔在同一候選工作樹內；最終 Git commit/PR head 由提交及遠端 read-back 回報。

## 實作摘要

- Full mode 經既有 Project authorization、pipeline quota reservation、Cloud Run worker admission；worker 使用 `CLEAN_REBUILD=true` 與 full stage。明確 mode 傳過 quota read/evaluate 與交易前後評估；full 只略過 pending-work/min-new-raw 門檻，保留真實檔案計數、daily/cooldown/demo/already-running 與 reservation/settlement 行為。Incremental 預設和舊 caller 相容。
- Capability 回傳 full quota snapshot 與實際 denial reason，不再用 BYOK 缺少當拒絕原因；本地/mounted pipeline 對 full 明確回報 unsupported。
- Frontend 加入增量／全面選擇、正文修訂不保證保留的確認文案、取消時不 POST；accepted response 必須帶正確 Project、execution ID 和 full mode，之後只輪詢該 execution，Project 切換時丟棄舊回應。
- Execution status 依 `CLEAN_REBUILD` 標示 mode；Cloud Run 成敗狀態與內容發布結果分開。UI 只讀同一 execution 的發布及 consumed-guidance 證據；receipt 尚無、legacy 或讀取不可用時顯示 unavailable，不猜測為 none。
- Worker receipt 記錄 compile start 時實際 pin 的 active/bootstrap/none guidance kind 與 revision，原有 requirements digest、bootstrap receipt 與候選 authority 不變。Swagger 由 `go generate ./cmd/bff` 更新，移除已撤銷的 BYOK gate 描述。

## Acceptance 證據

| AC | 本機證據 | 結果與邊界 |
| --- | --- | --- |
| A | `TestRecompileAllUsesFullQuotaAndSharedCleanRebuildAdmission`；既有 handler 授權／status／admin 回歸測試 | loopback Firestore 下驗證 active account、Project scope/header mismatch、未登入/foreign owner、full 202 identity、single invoke、`CLEAN_REBUILD=true` 與 full stage。未連正式 BFF/Cloud Run。 |
| B–C | `TestCloudMaterializationCleanRebuildSkipsPriorGenerationOutputs`、`TestCloudPreflightAndPublicationAcceptFreshSyntoOutputWithoutLegacyState` 等 worker tests | 在合成 object store/worker fixture 驗證 cold workspace 不帶舊衍生輸出並仍由既有 publication path 處理；沒有真實 Synto/provider/DEV 全流程，因此不宣稱實際 Project 正文重編成功。 |
| D | `TestFirestoreReserveQuotaFullModeAndIncrementalRemainDistinct`、`TestPipelineQuota…` 單元與 emulator 測試 | 127.0.0.1:8585 loopback Firestore 驗證零新 raw 時 full 可 reservation、增量仍 `no_new_raw` 阻擋，post-reserve cooldown snapshot 不否定已保留的這次執行；其餘 quota 回歸由完整 BFF suite 覆蓋。 |
| E | `apps/frontend/tests/api.test.mjs`、`lwc-391-full-recompile.test.tsx` | API malformed 202 身份拒用且不重送；component 測試確認取消不 POST、合法 full request 按指定 Project/execution 輪詢與顯示本次證據。這是 component test，不是瀏覽器 E2E。 |
| F | `TestProfileReceiptActualPublisherCallback`、`TestProfileCompileReceiptRecordsActualGuidanceKind`、`TestRecompileAllUsesFullQuotaAndSharedCleanRebuildAdmission` | 合成 worker fixture 驗證 pin 後更新不改 consumed revision；active/bootstrap/none producer 與 active/none/legacy-unavailable BFF 讀取區分均有覆蓋。未接 live Profile。 |
| G | `TestCloudManifestCommitTimeoutReadbackControlsReceipts`、`TestCloudPostCommitReceiptFailureDoesNotRollbackManifest`、`TestCommittedLocalExecutionSurvivesReceiptWriteFailureAndLaterCommitIsNotMisattributed`、`TestFullExecutionStateKeepsPublicationEvidenceSeparate` | 驗證 ambiguous readback、commit 後 receipt 失敗不回滾、以及 Cloud Run `FAILED` 不被發布結果覆寫；沿用既有合成 worker/store fixture。 |
| H | 上述 loopback Firestore 與 memory/object-store fixture、下列 source/test logs | 所有新資料只在本機 synthetic fixture。沒有 DEV deployment、owned QA Project 或 Human UAT；這些仍由 main 後續正式流程負責。 |

## 驗證紀錄

每項命令的原始（secret-scrubbed）輸出及 exit code 保存在 `docs/lwc-391/evidence/`：

| 命令 | 結果 | 證據檔 |
| --- | --- | --- |
| `cd apps/frontend && npm test` | exit 0；539 Node tests、375 component tests 全通過 | `frontend-tests-attempt4.log` |
| `cd apps/frontend && npm run lint` | exit 0 | `frontend-lint-attempt3.log` |
| `cd apps/frontend && npm run typecheck` | exit 0 | `frontend-typecheck-attempt3.log` |
| `python3 scripts/test_frontend_build_config.py && python3 scripts/test_frontend_config_artifacts.py` | exit 0；4 + 9 tests 通過 | `frontend-config-suites-attempt2.log` |
| `make build` | exit 0；BFF Go build 與 Next production build 完成 | `build-attempt1.log` |
| `make vet` | exit 0 | `vet-attempt1.log` |
| `env -u FIRESTORE_EMULATOR_HOST make -C apps/bff test` | exit 0；BFF scripts 與 `go test ./... -race` 通過 | `bff-test-attempt3.log` |
| loopback focused command：`FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 ./cmd/olw_worker -run "TestFirestoreReserveQuotaFullModeAndIncrementalRemainDistinct|TestRecompileAllUsesFullQuotaAndSharedCleanRebuildAdmission|TestProfileReceiptFirestoreAtomicOutboxAndDuplicate|TestProfileReceiptActualPublisherCallback|TestProfileCompileReceiptRecordsActualGuidanceKind" -count=1 -v` | exit 0；相關 quota、admission、receipt 測試通過 | `focused-firestore-attempt3.log` |
| `go generate ./cmd/bff` | exit 0；Swagger 文件已重生 | `swagger-generation-attempt1.log` |
| `git diff --check` | exit 0 | `diff-check-final.log` |

### 保留的失敗歷史與外圍環境限制

- 早期 frontend attempt 1–3 保留；其中一輪 component test 使用缺少 `project_id` 的舊 mock，更新為符合新 scoped status contract 後，attempt 4 全通過。focused Firestore attempt 1 保留了 emulator admission fixture 少 active account 的失敗；補足 synthetic account 後，attempt 3 通過。
- `focused-firestore-attempt2.log` 也保留，但其 shell wrapper 把 `-run` pattern 的 pipe 字元誤當 shell pipeline，含 command-not-found 訊息；不列作有效驗證，正確引用的 attempt 3 才是通過結果。
- `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 GOOGLE_CLOUD_PROJECT=lwc391-local-test make -C apps/bff test` 因將 project 環境變數傳入兩個 local Makefile contract tests 而失敗；去除該變數後單獨重跑 `test_local_dev_makefile.py` exit 0。該 broad run 未用於宣稱通過。
- 另外一次 broad emulator BFF run 只在範圍外 `cmd/demo_password_rotate/TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry/transaction_error_after_possible_commit` 失敗。依 main 指示，在相同 loopback emulator、provider test keys unset、`-v -count=1 -race` 下，完整 package 測試分別於 exact base 與 candidate snapshot 重放：兩邊皆只在相同 assertion 失敗，證明是既有 baseline，不是 LWC-391 引入。原始紀錄：`demo-password-baseline-14cdc-test.log`、`demo-password-candidate-diff-e4dd-test.log`；candidate source snapshot 為 exact base 加 tracked diff SHA-256 `e4dd…06874`。
- 本機 system Java launcher 無 JRE；使用已安裝 `/opt/homebrew/opt/openjdk@21/bin/java`（OpenJDK 21.0.12.1）啟動 loopback Firestore emulator。emulator 已停止；Firestore 測試資料與 gcloud config 僅在本機 cache，沒有匯入資料、live credentials 或雲端 Project。JDK 21 的 gcloud deprecation warning 不影響 emulator 測試。

## 尚待 main 接手

- 遠端 PR CI、same-HEAD 雙 review、正常 develop merge、post-merge CI 與正式 DEV/QA 路徑尚未執行；本報告與 mock/component/emulator 測試不代表 live BFF/Cloud Run、正式 DEV 成功或 Human UAT。
- 候選 commit/PR head、URL 與遠端 CI 狀態由本 dispatch 的 push/PR read-back 補充回報；本 dispatch 不 merge、不 deploy。
