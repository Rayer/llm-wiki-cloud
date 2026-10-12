# LWC-391 上傳完成狀態刷新隔離修補

## 範圍與來源

本次延續既有 LWC-390/391 編輯分支，只修復審查回報的 PipelineClient 上傳完成後狀態刷新競態，並保留 LWC-390 Profile 介面、LWC-391 五項既有 Project/context 回歸，以及既有文件與失敗紀錄。

- 分支：`Rayer/LWC-390-391-profile-full-recompile`
- 實作來源 checkpoint：`1ab09fb9f97614ae967f7d609249e708a3df4896`
- PR：[#122](https://github.com/Rayer/llm-wiki-cloud/pull/122)，base `develop`
- 原審查與 deferred-response 探針：`/Users/rayer/.hermes/workflows/lwc-local/discussions/ux-lanes/profile-review/lwc390391-4d73702d-recovered-review.json`、`lwc390391-reviewer-upload-refresh-probe.test.tsx`

## 修補內容

上傳佇列完成時，狀態刷新現在明確呼叫 `getPipelineStatus(projectId)`，並在請求開始與回應返回時檢查元件仍掛載、最新 Project ID/context generation、pipeline request generation，以及回應的 `project_id`。同一個最佳努力刷新 helper 也供 source annotation 儲存後的刷新使用；新一輪同 Project 執行會令舊刷新失效。上傳佇列、導航計數刷新、API、配額和後端行為未更動。

已逐一追蹤 `PipelineClient` 的 `setPipelineStatus` 寫入：共用刷新 helper、execution poll、初始 Project 載入、執行接受後的樂觀 RUNNING 狀態都有 context/request guard；Project 切換時清空狀態是同步重設，不是非同步回應寫入。source annotation 和 upload refresh 共用 helper，沒有保留另一條 unkeyed upload status writer。

新增四個 deferred-response 回歸：原審查的 A 上傳刷新延遲後切到 B、同一 Project ID 的 A→B→A context generation、錯誤 `response.project_id`，以及同 Project 新 execution 已開始後舊 upload refresh 才返回。既有五項 Project/context 回歸仍在同一測試檔，未刪除或放寬。

## 本機驗證

以下程式碼測試均綁定來源 checkpoint `1ab09fb9f97614ae967f7d609249e708a3df4896`；輸出、實際命令及 exit code 保存在 [`evidence/upload-refresh-1ab09fb`](evidence/upload-refresh-1ab09fb/)，並以 `SHA256SUMS` 固定 log bytes。建置輸出僅去除進度列的行尾空白以符合 diff hygiene：

- `cd apps/frontend && NODE_ENV=test npx vitest run tests/lwc-390-391-project-context.test.tsx`：exit 0，1 檔／9 測試通過。
- `cd apps/frontend && npm test`：exit 0，539 Node 與 385 component 測試通過。
- `cd apps/frontend && npm run lint`：exit 0。
- `cd apps/frontend && npm run typecheck`：第一次 exit 2，原因是被忽略的 `.next/dev/types/validator.ts` 與 `routes.d.ts` 仍引用已不存在的本機 fixture route；保留原始失敗 log。確認沒有 fixture source 後，只移除這兩個產生的型別快取檔，再執行同一命令 exit 0。
- `cd apps/frontend && npm run build`：exit 0。
- `python3 scripts/test_frontend_build_config.py && python3 scripts/test_frontend_config_artifacts.py`：exit 0，共 9 測試通過。
- `git diff --check` 與兩個變更檔的 ESLint：exit 0。

來源 checkpoint `1ab09fb` 的 PR 檢查讀回時，frontend-test、frontend-lint、frontend-typecheck、local-vertical-smoke、actionlint/schema、workflow-source-guards、GitGuardian 及 Vercel checks 為 pass；frontend-build 與 BFF 當時仍 pending。新的 evidence-only commit 會使 PR head 前進，最終 head 的檢查狀態以發布後重新讀回為準。

## 限制與待辦

本次沒有修改後端，也沒有重新執行 BFF/Firestore emulator、正式 DEV、真實 Project、provider、IAM 或 Production 操作。既有 390px/桌面 UI receipts 未改動；本次只改 async 狀態刷新與 component 回歸，沒有重跑本機 UI fixture，也不把本機測試當成 live BFF E2E。Main612 仍負責最終同 SHA 雙重審查、PR CI、正常 develop merge/post-merge CI，以及正式 DEV/QA 驗收；本次不合併或部署。
