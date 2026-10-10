# LWC-380 Query Diagnostics r1 實作檢查點

## 範圍與來源

- 契約：`query-diagnostics-contract-r1`，依使用者指定的 review input 與 follow-up spec report（審查結果 PASS）實作。
- 起始來源：`457fefe9ec66e46caf0df91def63ef66d9893300`；目前工作分支 `Rayer/LWC-380-query-diagnostics`。未建立 commit。
- 實作範圍限於 BFF Query/MCP diagnostics、receipt/tracing/runtime 接縫，以及 Frontend Query parser、HomeClient、ErrorState 與 `QueryErrors` 英文/繁體中文訊息。
- 沒有修改 raw-sync、帳戶/金鑰/其他全站錯誤介面、collector/provider/IAM 或任何 authority。GCS 的現有 `Pin`/`ViewToken` 已能證明本次 current-manifest `exists=false`；本次沒有改 GCS client，也沒有重讀 moving manifest。

## 實作結果

- BFF 保留 wrapped cause 與實際失敗 stage；只有真正的 outbound LLM call 會使用 `ProviderCallError` 與 `SafeErrorCategory`。published generation missing 需要 identity unavailable/unpinned cause 加上本次 pinned GCS view 的 `ViewToken()=="legacy"` provenance；一般 unpinned/LocalFS identity failure 仍分類為 storage/runtime unavailable。
- HTTP Query 錯誤使用 `application/problem+json` Problem Details，包含穩定 URN、status、safe detail、code、opaque diagnostic ID，並保留等於 safe detail 的 legacy `error`。錯誤原因不輸出 `err.Error()`。
- MCP Query 保留 SDK tool failure、`IsError` 與 safe text error，另帶 structured `error`/`code`/`diagnostic_id`。HTTP transport status、MCP `IsError` 和 business outcome 分開記錄；MCP auth 拒絕範圍未變。
- Query request 建立 OTel root span；profile/generation/runtime、receipt stage 和 host call 建立實際 parented spans。BFF 啟動時使用 SDK stdout JSON exporter（service/revision resource）；exporter 建立或輸出失敗不會阻止 Query。`slog` 只寫固定 allowlist 的單筆 terminal summary，不寫 query、prompt、文件、key、JWT、provider body、raw error、URL 或 Authorization。
- Frontend Query `postJson` 可讀 Problem Details、舊版 `error`、非 JSON/null body，並保留 status/code/diagnostic ID 與非空 fallback。HomeClient 顯示 zh-TW/en 的可行動錯誤、診斷 ID 與複製按鈕；錯誤狀態不會顯示 no-results。

主要檔案：`apps/bff/internal/handler/v1/query_diagnostics.go`、`apps/bff/internal/observability/tracing.go`、`apps/bff/internal/query/receipts.go`、`apps/bff/internal/handler/v1/endpoints.go`、`apps/bff/internal/handler/v1/mcp.go`、`apps/frontend/src/lib/api.ts`、`apps/frontend/src/components/HomeClient.tsx`、`apps/frontend/src/components/States.tsx`、`apps/frontend/src/messages/en.json`、`apps/frontend/src/messages/zh-TW.json`。主要新回歸測試位於 `apps/bff/internal/handler/v1/query_diagnostics_test.go` 與 `apps/bff/internal/observability/tracing_test.go`；receipt ownership 測試位於 `apps/bff/internal/queryquality/production_test.go`，missing-manifest source provenance 測試位於 `apps/bff/internal/gcs/query_identity_test.go`。

## 驗證

下列命令均在本機執行，列出實際退出碼：

| 命令 | 結果 |
| --- | --- |
| `make -C apps/bff test` | exit 0；70 個 CD contract、21 個 auth-config contract、20 個 local Makefile contract，以及 `go test ./... -race` 全通過 |
| `go test -race ./internal/gcs ./internal/handler/v1 ./internal/query ./internal/queryruntime ./internal/queryquality ./internal/llm ./internal/observability`（`apps/bff`） | exit 0；涵蓋 missing-manifest provenance 與 production receipt ownership regressions |
| `make -C apps/bff vet` | exit 0 |
| `make -C apps/bff build` | exit 0 |
| `npm test`（`apps/frontend`） | exit 0；528 Node tests、319 component tests、34 個 component test files 全通過 |
| `npm run lint`（`apps/frontend`） | exit 0 |
| `npm run typecheck`（`apps/frontend`） | exit 0 |
| `npm run build`（`apps/frontend`） | exit 0；Next.js compile 與 static generation 成功 |
| `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/api.test.mjs`（`apps/frontend`） | exit 0 |
| `NODE_ENV=test npx vitest run tests/lwc-248-home-search-submission-behavior.test.tsx`（`apps/frontend`） | exit 0；12/12 tests |
| `git diff --check` | exit 0 |
| 兩份 locale JSON 使用 Python `json.load` 解析 | exit 0；en/zh-TW 各含 15 個 `QueryErrors` keys |

安全輸出與實際路徑證據：BFF tests 以本機 `httptest` provider 回覆 503，確認 raw response canary 不進 error/detail；以 mcp-go in-memory transport 呼叫實際 `tools/call`，確認 `IsError=true`、safe `TextContent` 和 structured diagnostics；GCS in-memory backend 的 `query_identity_test.go` 確認空 current manifest 被單次 Pin 記為 `ViewToken()=="legacy"`，其 identity error 為 `ErrQueryGenerationUnpinned`，handler classifier 僅在同時有該 provenance 才分類為 published-generation-missing；受控 span exporter 驗證 root/runtime/receipt-stage/host-call parentage、stdout JSON span tree、diagnostic/trace correlation，以及 exporter failure 下 HTTP Query 仍成功。敏感 canary 僅作測試輸入，passing assertion 確認它不在 response、structured logs 或 spans 中。

本次命令輸出紀錄暫存於 `/tmp/lwc380-bff-make-test.log`、`/tmp/lwc380-bff-vet.log`、`/tmp/lwc380-bff-build.log`、`/tmp/lwc380-bff-race-focused.log`、`/tmp/lwc380-frontend-test.log`、`/tmp/lwc380-frontend-lint.log`、`/tmp/lwc380-frontend-typecheck.log`、`/tmp/lwc380-frontend-build.log`、`/tmp/lwc380-frontend-api.log` 與 `/tmp/lwc380-frontend-home.log`。最終工作區核對命令 `git diff --check` 為 exit 0。

## 未執行項目與界線

本次沒有啟動完整 local-cloud app/emulator，也沒有呼叫外部 provider、DEV/Production、Cloud Trace console 或變更任何資料/憑證；BFF 全套測試使用 repo 的本機 fixtures，另以受控本機 HTTP/MCP/OTel sink 驗證 Query diagnostic path。沒有進行部署、commit、push、PR、merge 或 live-provider 驗收；這些都不屬於 frozen offline implementation slice。
