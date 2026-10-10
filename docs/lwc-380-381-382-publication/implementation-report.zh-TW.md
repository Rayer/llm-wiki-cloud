# LWC-380／381／382 整合與 PR 發佈檢查點

## 發佈狀態與來源

本報告記錄 frozen LWC-380 query-diagnostics-r1、LWC-381 project-key-usability-r2、LWC-382 account-ui-contract-r2 的整合候選。feature branch `Rayer/LWC-382-383-account-ui` 以 `origin/develop` `b1d6c1bb3af1a5b5962dbcd7927a09fe72ba8f8f` 為基底；最後一次產品程式碼快照是 `097ecf8f17b8da2edddf07cd0f057d8d970b1153`。此 Dispatch 的交付點是推送 branch 並建立 PR；PR 後的 same-SHA review、normal merge 與 DEV Actions 由 coordinator 接手。

整合順序為來源修復 commit `0069d5f67e1aa3afd3c7067f3054825e716cb5cd`、合併含 LWC-384 的 develop commit `e6c437b5397fdc2b5ec9a5cc9a2a7b386afa0f1f`，以及 harness 期望文案修正 `097ecf8f17b8da2edddf07cd0f057d8d970b1153`。LWC-384 的五個 `apps/bff/cmd/sync` 檔案隨 develop 合併保留。LWC-383 先前授權的共用 account button／focus 修復位元組仍在 branch；本次沒有擴張 383 需求或宣稱其獨立結案。

另有 0069 的獨立 source-review 結果記錄在 `/Users/rayer/.hermes/workflows/lwc-local/discussions/mcp-followup-source-0069-report.json`。這不替代本報告的 canonical verify 結果，也不代表 remote CI、DEV、人類 UAT 或 live provider 驗證。

## 封存來源

在整合前，已建立兩份唯讀文字封存；逐檔大小及 SHA-256 均與 manifest 相符，重新計算的 candidate digest 也與 manifest 相符，零個複製不一致。

| 封存 | 檔案數 | Candidate digest | Manifest SHA-256 |
| --- | ---: | --- | --- |
| `/Users/rayer/.hermes/workflows/lwc-local/archives/lwc-380-381-382-publication-ui-20261010T094343Z-taskbf44ffd8` | 25 | `8137fc68ec8e81cdd0d0de1acdfdd28bef69caab75149b777d16f44f365b91a9` | `84994ad460885b1bb353a9854471e252ede279bdb4d3aad8a919fef015db80ad` |
| `/Users/rayer/.hermes/workflows/lwc-local/archives/lwc-380-381-382-publication-380-20261010T094344Z-taskbf44ffd8` | 41 | `3d5deb4707ffe117777d9d8e90fe103622ca2ca1fb9a40eea3693729d55b6df9` | `b76eb15a5b9b5a3de5b8d02e2b329b2cada36ea20606a9bfe641aa202ec327f4` |

兩份 manifest 都以 `457fefe9ec66e46caf0df91def63ef66d9893300` 為指定基底。封存只收 UTF-8 原始碼、測試、文件及文字證據；快取、憑證與二進位截圖未封存。

## 變更摘要

- **LWC-380：** stdout span exporter 僅輸出受限 Query scope；SDK schema rejection 使用單一安全診斷且不虛構 runtime/provider spans；失敗診斷保留同次 pinned manifest 的 generation。新增實際 storage SDK memory-403 canary、MCP SDK Accept rejection 與 pin-switch 測試。
- **LWC-381：** key 使用時間存於獨立 scoped `project_key_usage` collection，不更動 authority record 或 strict reader；更新採有界 best-effort、單調 timestamp 與安全 metadata join。管理入口移至 Project Settings，保留 context reset／late callback 防護，並提供 Hermes-first 教學；Grok web 未驗證、multi-Project key deferred。
- **LWC-382：** account settings 的 local fallback 以 typed provenance 區分本地訊息與後端原文；支援 session revoke、binding revoke、reauthorize 的英文／繁中 fallback。Email 下方維持中性次要設定／登出按鈕、原 handlers 與既有鍵盤焦點行為。
- **LWC-383：** 僅揭露先前授權並共享的 account button／focus 修復，不增加新範圍。
- **LWC-384：** 五個 sync 檔案由 `b1d6c1b` 正常合併帶入，沒有在整合工作樹重寫該 lane。

PR 的完整檔案狀態列於 [changed-files.txt](changed-files.txt)。桌面／手機 PNG 仍在此共用工作樹的 `docs/lwc-381/evidence/rendered/` 與 `docs/lwc-382-383/evidence/`；文字化 QA 記錄已在 repo 文件中。PNG 未加入文字型來源封存或本次 PR 檔案清單。

## 驗證結果

### 通過

| 範圍與來源 | 命令／檢查 | 結果 |
| --- | --- | --- |
| 前端，修復候選 `0069d5f` | `npm test` | exit 0；Node 529/529、component 341/341（35 files）。 |
| 前端，`0069d5f` | `npm run lint`、`npm run typecheck` | 均 exit 0。整合後的 `make verify` 也再次通過這兩階段。 |
| 前端，`0069d5f` | `NEXT_PUBLIC_CONFIG_URL=/__preview-runtime-config npm run build` | exit 0。 |
| 整合後 `097ecf8` | `make build` | exit 0；BFF `go build ./...` 與 Next production build 均完成。 |
| 整合後 `097ecf8` | `make smoke`，並移除 provider keys 與 Firestore／Storage emulator host 變數 | exit 0；loopback/auth-boundary、scope、離線 Synto 及 smoke-contract 測試通過。 |
| BFF，`0069d5f` | `go test ./internal/gcs ./internal/handler/v1 -count=1`；`go vet ./...`（root verify） | 受影響 GCS／handler packages 通過；canonical verify 的 BFF vet 階段通過。 |
| BFF repair probes，`0069d5f` | `go test ./internal/gcs -run '^TestPinnedGenerationIDSurvivesMissingConceptsAndCurrentSwitch$' -count=1`；`go test ./internal/handler/v1 -run '^TestQueryDiagnosticsFinalizeSafelyAndExportActualSpanTree$' -count=1` | 均 exit 0。覆蓋 pinned generation、真實 SDK schema／transport failure 與 diagnostic 終結。 |
| LWC-381 emulator，`0069d5f` | `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test -race ./internal/auth ./internal/handler/v1 ./cmd/bff` | exit 0；使用 loopback Firestore emulator 與合成資料，未呼叫外部 provider。 |
| LWC-384 sync，整合後 `097ecf8` | `go test -race ./cmd/sync -count=1`；`go vet ./cmd/sync` | 均 exit 0。 |
| LWC-382 helper／modal，`0069d5f` | `npx vitest run tests/lwc-382-cli-local-fallback.test.tsx` | exit 0，8/8 tests；覆蓋三種操作、兩語系及缺漏／空白／相同或不同的後端訊息。 |
| CI 相關 contracts，`0069d5f` | `python3 docs/lwc-380-381-382-publication/scratch/run_validation.py canonical_extras` | 9/9 exit 0，含 workflow YAML、frontend build/config contracts、artifact transport、workflow guards、actionlint 及 pinned Synto wire contract。 |
| Native Hermes，整合後 `097ecf8` | `python3 docs/lwc-380-381-382-publication/scratch/run_native_hermes.py` | exit 0；隔離 HOME/HERMES_HOME、loopback emulator、合成 key、0 model calls；包含 MCP/HTTP parity 與撤銷後既有 client 被伺服器拒絕。結果見 [native-hermes-results.json](evidence/native-hermes-results.json)。 |

### 渲染與鍵盤證據

LWC-381 的 [rendered-qa.json](../lwc-381/evidence/rendered/rendered-qa.json) 記錄 desktop `1280×900`、mobile `390×844`、英文與繁中各一組：Project Settings 指向 `/profile`；可見 available／no-record／unavailable 三種使用狀態；按鈕最小高度 44px、可換行；Tab focus-visible、Enter／Space 開啟、Escape／關閉鈕關閉並回復焦點；沒有 page errors 或非預期 API mutations。這份視覺記錄的 candidate identity 是原始 UI 工作樹 `HEAD 457fefe` 加 manifest digest `8d6fd9cde06681b13663bb424d2088315d046851f251911224776e4626e700ce`，不是整合後的 `097ecf8`。LWC-382 的四組語言／viewport 截圖與量測記錄位於 `docs/lwc-382-383/evidence/`；PNG 僅留在共用工作樹，沒有複製進文字封存。

### 保留的 canonical FAIL 與歷史

整合後 `097ecf8` 的 root canonical `make verify` **exit 2**。Bootstrap、workflow YAML、frontend lint/typecheck、BFF vet 階段通過；BFF race suite 在 `apps/bff/cmd/demo_password_rotate/main_test.go:365` 的 `TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry/transaction_error_after_possible_commit` 失敗，錯誤為「ambiguous transaction was reported as success or retried」，因此同一 target 後續 build／smoke 沒有由該次 `make verify` 執行；本報告已另外在相同 `097ecf8` 執行 `make build` 與 `make smoke`，兩者 exit 0。

為確認原因，對整合候選 `097ecf8` 及 detached exact-base `b1d6c1b` 分別執行 `go test -race ./cmd/demo_password_rotate -count=1 -v`；兩者在同一行、同一 subtest 出現相同 FAIL。沒有修改該測試、放寬 assertion 或把候選 FAIL 標成 PASS。對應輸出保留於 `evidence/demo-password-candidate-b1d6-race.log` 與 `evidence/demo-password-develop-b1d6-race.log`。早期 `0069` canonical FAIL、修復迭代錯誤、首次 Hermes stale-copy assertion 與已取消的 Synto 安裝嘗試也仍分別保留在 `evidence/`；它們沒有被當作最終 PASS 或覆寫。

## 限制與交接

目前證據涵蓋本機測試、loopback emulator、隔離 Hermes harness 與瀏覽器 mock；沒有真實帳戶登入／登出、Google/provider 變更、production 或使用者資料操作。Grok web 流程未驗證，multi-Project key 仍 deferred；MCP 真實 answer/citation 需要已發布 fixture，沒有以本機結果冒稱 live 驗證。canonical `make verify` 的 baseline-reproduced FAIL 仍需 coordinator 在 PR review／CI 階段決定如何處置；本 worker 的交付僅到 PR checkpoint。
