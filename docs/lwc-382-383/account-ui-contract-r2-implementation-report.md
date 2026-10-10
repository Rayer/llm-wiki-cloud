# LWC-382 / LWC-383 account-ui-contract-r2 實作報告

## 來源與範圍

- 工作目錄：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-382-383-account-ui`
- 分支：`Rayer/LWC-382-383-account-ui`
- 實作前 HEAD：`457fefe9ec66e46caf0df91def63ef66d9893300`（符合指定 base）
- 實作範圍：Submitted 帳戶設定入口、Owner 批次入口的中性次要按鈕、在地化的設定對話框文案與本地錯誤 fallback，以及已知人類可讀狀態的翻譯。

## 實作內容

- `apps/frontend/src/components/Shell.tsx`：帳戶設定和登出按鈕置於 Email 下方，使用中性淺底、細邊框、間距、可換行、44px 最小高度，以及 hover、active、focus-visible 樣式；保留既有開啟設定與登出 handler。
- `apps/frontend/src/components/AccountSettingsModal.tsx`：設定標題、關閉按鈕名稱、固定的本地載入與操作 fallback 都使用翻譯；CLI 的 active/revoked/expired 及 Sync 的已知狀態使用已知翻譯。未知狀態、後端錯誤訊息、Email 和識別碼仍保留原始值。
- `apps/frontend/src/messages/en.json`、`apps/frontend/src/messages/zh-TW.json`：新增英文與繁體中文的設定關閉名稱、已知狀態及本地 fallback 文案。
- 測試更新：`apps/frontend/tests/lwc-174-shell.test.tsx`、`apps/frontend/tests/lwc-346-account-settings.test.tsx`、`apps/frontend/tests/lwc-377-project-keys.test.tsx`、`apps/frontend/tests/i18n.test.mjs`。

未搬移 LWC-381 key 入口，也未更動 auth。視覺測試使用本機合成帳戶資料；沒有執行真實登入、登出、Google 綁定或 CLI/Sync 變更。

## 驗證結果

以下命令均在 `apps/frontend` 執行，除非另有標示：

| 命令 | 結果 |
| --- | --- |
| `npm ci` | 通過，exit 0；安裝完成。npm audit 回報 17 項相依套件弱點（3 moderate、13 high、1 critical），本次未處理相依套件更新。 |
| `npm run lint` | 通過，exit 0。 |
| `npm run typecheck` | 通過，exit 0。 |
| `npm run test:component -- tests/lwc-174-shell.test.tsx tests/lwc-346-account-settings.test.tsx tests/lwc-377-project-keys.test.tsx` | 通過，3 個檔案、24 個測試，exit 0。 |
| `npm test` | 最後一次通過：Node 528/528，Vitest 34 個檔案、325/325 測試，exit 0。 |
| `python3 ../../scripts/test_frontend_build_config.py` | 通過，4 項，exit 0。 |
| `python3 ../../scripts/test_frontend_config_artifacts.py` | 通過，9 項，exit 0。 |
| `npm run build` | Next.js webpack production build、TypeScript 與靜態頁面產生均通過，exit 0。 |

失敗與修正紀錄：第一次聚焦元件測試有一項因 Testing Library 無法用 `getByText` 匹配巢狀 span 文字而失敗，改以段落 `textContent` 驗證後通過。第一次全量 `npm test` 有一項 LWC-377 測試仍使用舊的英文關閉按鈕名稱；更新為翻譯 key 後，聚焦三檔測試 24/24 通過，完整 `npm test` 也通過。上述為已修正的測試斷言，不是尚存失敗。

## 桌面、手機與鍵盤檢視

以本機 Next.js 開發伺服器、合成帳戶 mock 和 headless Chrome Playwright 檢視英文及繁體中文，在 1280×577 桌面與 390×844 手機尺寸各截取 Shell 和設定對話框。確認 Email 在兩個操作按鈕上方；按鈕標籤完整，英文手機版會換成兩行，繁體中文在同一行並保有間距。也驗證按鈕高度、淺色背景、邊框、hover/active 顏色、鍵盤 focus-visible 外框、Tab 後 Enter 開啟對話框，以及 Escape 關閉。

視覺證據位於 `docs/lwc-382-383/evidence/`：

- `desktop-en-shell.png`、`desktop-en-modal.png`
- `desktop-zh-TW-shell.png`、`desktop-zh-TW-modal.png`
- `mobile-en-shell.png`、`mobile-en-modal.png`
- `mobile-zh-TW-shell.png`、`mobile-zh-TW-modal.png`

所有截圖使用本機合成資料，不代表真實服務內容。可用的 GUI/CUA 視窗控制無法取得瀏覽器視窗，因此以 headless Chrome 完成渲染和鍵盤檢視；手機尺寸是 viewport 模擬，未在實體觸控裝置上檢查。Playwright 沒有回報頁面錯誤。測試過程的 mock refresh 請求回傳 401；帳戶清單資料由 mock GET 提供，未送出 logout、Google link、CLI 或 Sync 變更請求。

## 交付檔案

- 元件與文案：`apps/frontend/src/components/Shell.tsx`、`apps/frontend/src/components/AccountSettingsModal.tsx`、`apps/frontend/src/messages/en.json`、`apps/frontend/src/messages/zh-TW.json`
- 測試：`apps/frontend/tests/lwc-174-shell.test.tsx`、`apps/frontend/tests/lwc-346-account-settings.test.tsx`、`apps/frontend/tests/lwc-377-project-keys.test.tsx`、`apps/frontend/tests/i18n.test.mjs`
- 渲染證據與本報告：`docs/lwc-382-383/evidence/`、`docs/lwc-382-383/account-ui-contract-r2-implementation-report.md`
