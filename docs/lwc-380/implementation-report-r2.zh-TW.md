# LWC-380 SOURCE r1 修復檢查點 r2

## 判定與來源

- 已依序讀取 `/Users/rayer/.hermes/workflows/lwc-local/discussions/lwc-380-source-r1-disposition.md` 與 `lwc-380-source-r1-report.json`；三項修復範圍依該 HOLD disposition。
- 原始來源仍為 `457fefe9ec66e46caf0df91def63ef66d9893300`，分支 `Rayer/LWC-380-query-diagnostics`；完整非文件 source overlay 共 27 個檔案，其排序後的 `path NUL SHA256(file) newline` 清單 SHA-256 為 `070cd2b8d9dc969b691206445181934f39d4f30cdd7ce03002a1a5345c6e1658`。此 fingerprint 包含同一工作樹先前 r1 的未提交 Query 實作；本 Dispatch 僅修復下列列出的接縫，並保留其餘既有位元組。
- 原 r1 immutable archive `/Users/rayer/.hermes/workflows/lwc-local/archives/query-diagnostics-380-r1` 未修改。其 `manifest.json` 仍列出 1296 個檔案，SHA-256 `7eec9d19519f24fd2c5ef96bcae22e9f493cc381a196a91f6611768c9d48749f` 與 HOLD disposition 一致；原始 r1 報告及失敗 probes 也保留。
- 本次沒有建立新的 immutable manifest；修復來源仍待 coordinator 建立新 manifest 與獨立 source review。未 commit、push、開 PR、merge、deploy 或呼叫 live provider。

## 修復內容

1. `apps/bff/internal/observability/tracing.go` 在 stdout export 邊界加入 instrumentation-scope 限制，只輸出 `llm-wiki-bff/query` 的 BFF Query spans。真實 `cloud.google.com/go/storage` SDK 經記憶體 RoundTripper 收到合成 HTTP 403；測試 recorder 確認 SDK span 內含錯誤 body canary，而 stdout 沒有該 canary，且仍輸出相關聯的 Query root/child JSON spans。沒有 credentials 或網路呼叫。
2. `apps/bff/internal/handler/v1/mcp.go` 在外層 project-key 驗證成功後、MCP SDK schema validation 前辨認 `tools/call`/`query_project`。早期 schema rejection 共用單一 diagnostic、root span 與 terminal receipt；SDK 的 `IsError` 留存，回應文字與 structured content 帶安全訊息、`invalid_query` code 與 opaque diagnostic ID，且不建立 runtime/provider spans。request probe 上限沿用 MCP SDK 的 `DefaultMaxRequestBodyBytes`。測試走實際 Streamable HTTP SDK 與 SDK bearer wrapper，使用固定的 authenticated identity fixture；正式 project-key authority 沒有修改或接外部服務。initialize、tools/list 與外層未授權請求不建立 Query diagnostics。
3. `apps/bff/internal/handler/v1/query_profile.go` 在 current generation pin 成功後，從同次回傳的 pinned Store 呼叫 `storage.QueryGenerationIdentityProvider`，不重讀 moving current manifest。pin-switch regression 在 pin 後將 current generation 從舊值切換成新值，再注入 runtime failure；terminal log 與 Query root span 都保留舊 pinned generation，manifest read、identity read 各一次。

本次修復 hunk 位於 `apps/bff/internal/handler/v1/mcp.go`、`query_profile.go`、`query_diagnostics.go`、`query_diagnostics_test.go`、`apps/bff/internal/observability/tracing.go` 與 `tracing_test.go`。未變更 LWC-381 key authority/usage、Shell/Account UI、ProjectKeys UI 或其 locale keys；未變更前端 Query UI/message bytes。

## 驗證紀錄

所有新命令輸出都保存在 `docs/lwc-380/evidence-r2/`，沒有寫入 `/tmp`。

| 工作目錄 | 命令 | 結果 | 紀錄 |
| --- | --- | --- | --- |
| `apps/bff` | `go test ./internal/observability -run '^TestActualStorageSDKFailureCannotReachStdoutExporter$' -count=1` | exit 0；真實 storage SDK memory 403 canary 與 Query root/child correlation 通過 | [observability-sdk-canary-iteration-2.log](evidence-r2/observability-sdk-canary-iteration-2.log) |
| `apps/bff` | `go test ./internal/handler/v1 -run '^TestQueryDiagnosticsFinalizeSafelyAndExportActualSpanTree$' -count=1` | exit 0；包含空 q、缺 q、錯誤型別、非法 mode、initialize/list/auth 邊界與 pin-switch failure | [mcp-query-schema-iteration-3.log](evidence-r2/mcp-query-schema-iteration-3.log) |
| `apps/bff` | `go test -race ./internal/handler/v1 ./internal/gcs ./internal/query ./internal/queryruntime ./internal/queryquality ./internal/llm ./internal/observability` | exit 0；全部受影響 package 通過 | [bff-race-final-auth-boundary.log](evidence-r2/bff-race-final-auth-boundary.log) |
| `apps/bff` | `go vet ./internal/handler/v1 ./internal/observability` | exit 0 | [bff-vet-final.log](evidence-r2/bff-vet-final.log) |
| `apps/frontend` | `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/api.test.mjs` | exit 0；55/55 passed | [frontend-api-affected.log](evidence-r2/frontend-api-affected.log) |
| repository root | `git diff --check` | exit 0 | 最終 source overlay 檢查 |

## 保留的失敗歷史

- 原 source-r1 reviewer 的 `TestReviewerActualSDKSpanSafety` 實際 exit 1，確認未過濾 stdout span 含 `PRIVATE_STORAGE_BODY_CANARY`；`TestReviewerSDKValidationFinalizes` 實際 exit 1，確認 SDK 回 `IsError=true` 但沒有 StructuredContent、span 或 terminal summary。這兩項原始因果證據留在 `/Users/rayer/.hermes/workflows/lwc-local/discussions/lwc-380-source-r1-report.json`，未覆寫。
- 修復第一輪 observability 編譯測試 exit 1（unused import 與測試區域變數名稱衝突），保留於 [observability-sdk-canary-iteration-1.log](evidence-r2/observability-sdk-canary-iteration-1.log)；修正後 iteration 2 通過。
- MCP iteration 1 exit 1，因實際 Streamable HTTP SDK 要求 Accept 同時包含 `application/json` 與 `text/event-stream`；保留於 [mcp-query-schema-iteration-1.log](evidence-r2/mcp-query-schema-iteration-1.log)。修正 header 後 iteration 2 通過，將測試身份移至 post-auth adapter 並撤回 Handler auth 欄位後，iteration 3 再通過。
- 第一次受影響 race gate exit 1，發現既有 stdout span 測試使用非 Query instrumentation scope，保留於 [bff-race-affected.log](evidence-r2/bff-race-affected.log)；將 fixture 改用實際 Query scope 後，[bff-race-affected-repair.log](evidence-r2/bff-race-affected-repair.log) exit 0，最終 post-auth 邊界改動後的 race gate 也再次 exit 0。
- 每項 seam 均在三輪內修復並通過；沒有尚未解決的因果 blocker。既有 r1 green evidence 與 emulator SKIP 限制依原報告保留，沒有將 emulator、Cloud console、DEV/UAT 或 billing 驗證升為本次 gate。

## 執行環境界線

本次沿用同一 worker terminal `term_80bcbe4e-70a3-4de6-b55e-391d22d1bfc3` 與工作樹；`orca status --json` 顯示 local runtime `0100b312-acbf-4bd0-9bdf-beca541e351e` 持續連線。該狀態不提供 Luna/xhigh/YOLO 的模型或啟動旗標，因此這份紀錄只確認 terminal/runtime 延續，不另行宣稱已獨立驗證那些旗標。所有 provider/storage probe 均限於 memory transport，沒有外部網路、credentials 或資料變更。
