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

## LWC-390 successor 與 combined source checkpoint（2026-10-13）

- 在保留本報告、b86 source 和原始失敗紀錄的基礎上，LWC-390 Profile settings/guide UI 與 LWC-391 PipelineClient project-context generation repair 完成於 source commit `4d73702d2eeb6836690ca7353977ace51c7789d6`。該 commit 的 exact archive 在 worktree Git metadata `lwc390-391-checkpoints/4d73702d-source.tar.gz`，SHA-256 `5701d996570f9b664d3e37c8778fc9a68d2743957b2b8fbb8097fc4b2274c0f1`。
- 新 source commit 上的 frontend full suite 為 539 Node + 381 component tests PASS；lint、typecheck、Next production build 與兩個 frontend build-config suites 皆 exit 0。391 的五個 Project switch / late-response regression 已一併重跑通過，具體命令與 raw logs 見 [`../lwc-390/report.md`](../lwc-390/report.md) 和 `../lwc-390/evidence/`。
- LWC-390 browser fixture 使用實際 Profile component + intercepted local transport；26 項 UI/lifecycle/390px 檢查 PASS。此為 local controlled browser receipt，沒有 live BFF/Project API 或 Human UAT。
- BFF/backend/API/schema 與 quota seam 沒有變更，因此本 successor 未在新 source SHA 重跑 BFF suites；上文 BFF 證據仍準確指向其記錄的原 source snapshot。兩 ticket combined same-head review、PR CI、normal merge 和 formal DEV/QA 由 main 後續處理。

## LWC-391 accepted execution 狀態歸屬修補（2026-10-13）

### 根因與修正

- Parent 在前一個 PR #122 head `56a173a29871f461a11472f1e222aff65adddc1b` 重放 deferred-response 探針：9 個既有 Project/context 案例加 3 個新案例共 10 pass、2 fail、exit 1。兩個失敗都是同 Project 的 upload 或 source-annotation 一般狀態刷新在 full-recompile POST 等待時開始，POST 接受新 execution 後才回傳舊的 `SUCCEEDED`、發布結果與 consumed-guidance，覆蓋畫面上正在執行的新 execution。原始 receipt 與探針保留於 `/Users/rayer/.hermes/workflows/lwc-local/evidence/lwc390391-delivery/parent-56a173a2-refresh-during-post-fail.json` 及 `/Users/rayer/.hermes/workflows/lwc-local/discussions/ux-lanes/profile-review/lwc390391-refresh-during-post-probe.test.tsx`。
- `PipelineClient` 現在在 accepted execution 後，以既有 Project/context generation 記錄該 Project 的 execution-status owner，並遞增既有 request generation。這會讓 POST 等待期間啟動的一般刷新不能回寫 lifecycle；owner 存續至 Project context 改變，因此後續 upload/annotation 一般刷新只能更新 quota/full quota/suggested queries，execution-scoped polling 繼續獨占 lifecycle evidence。一般刷新仍在 Project、mount 與 response Project ID 驗證後執行；沒有改 API、schema、backend、quota policy 或 polling path。
- `setPipelineStatus` 的其他寫入路徑已追查：一般共用刷新、execution-scoped poll、初始 Project 載入、accepted 後樂觀 RUNNING 更新，以及 Project 切換的同步清空。poll 和初始載入保留既有 request/context guards；Project 切換會清空新 Project 的 owner，舊 context response 仍不可寫入。

### 回歸測試與驗證

- 新增 `apps/frontend/tests/tpm-391-refresh-during-post.test.tsx` 四個 deferred-response component 案例：upload/annotation 分別於 POST pending 時開始，並分別於 accepted execution 已追蹤時開始。每例都驗證舊 execution guidance 不出現、目前 RUNNING 狀態保留，並確認一般刷新仍更新 quota。原 9 個 `lwc-390-391-project-context.test.tsx` regression 未改動。
- 影響面 focused command：`cd apps/frontend && NODE_ENV=test npx vitest run tests/lwc-211-profile.test.tsx tests/lwc-390-391-project-context.test.tsx tests/lwc-391-full-recompile.test.tsx tests/tpm-391-refresh-during-post.test.tsx tests/lwc-admin-pipeline-terminal-status.test.tsx tests/lwc-146-main-path-pipelineclient-zh-tw.test.tsx`：exit 0，6 檔／56 tests pass。
- 完整前端命令：`cd apps/frontend && npm test`：exit 0，539 Node tests、389 component tests；`npm run lint`、`npm run typecheck`、`npm run build` 均 exit 0。CI 的 `python3 scripts/test_frontend_build_config.py`（4 tests）與 `python3 scripts/test_frontend_config_artifacts.py`（9 tests）均 exit 0；`git diff --check` exit 0。
- 本次程式碼與新增 regression 對 56a 的 unified diff SHA-256 為 `5d9c2a28452beb55df8273f25c1fc34ce981e29624e9ad5f148ec3d1a8b979c1`，此為 source+test diff fingerprint，不是 Git commit SHA。secret-scrubbed raw command logs、exit code 與最終 Git/PR read-back 保存在 `/Users/rayer/.hermes/workflows/lwc-local/evidence/lwc390391-delivery/accepted-refresh-final/`，其子目錄以最終 commit SHA 命名；commit SHA 與 PR head 以發佈後 read-back 為準，避免在同一文件中自我引用。
- PR #122 在修補前仍是 open、non-draft、base `develop`，branch `Rayer/LWC-390-391-profile-full-recompile` head `56a173a29871f461a11472f1e222aff65adddc1b`。修補前該 SHA 的 GitHub required checks 已讀回全數 pass；這些是舊 head 的狀態，新的 commit 會重新觸發 CI。遠端 develop 為 `14cdc4bd663ec4e7a2ccfbde72f0f919426931c1`，且為目前分支的共同基準。

### 範圍與待辦

- LWC-390 Profile UI、BFF/API/quota/top-k/backend、外部-key 設定、provider、IAM、Production 與真實 Project 均未變更或操作。本次只執行本機 component/frontend gate；沒有執行 BFF、formal DEV、owned QA Project 或 Human UAT，也不把 component tests 當 live BFF E2E。
- 本次是 source/validation checkpoint。Parent/main612 仍負責同一 SHA 雙重審查、PR CI、正常 develop merge/post-merge CI 與 formal DEV/QA；本 dispatch 不 merge 或 deploy。
