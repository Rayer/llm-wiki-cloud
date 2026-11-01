# LWC-459 最新基底盤點與執行紀錄

基底：`a3f8ba14407e4cb686b649b397e947411ac71361`。2026-11-01 核對 HEAD、origin/develop 與 `git ls-remote origin refs/heads/develop` 一致。初始實作分支為 `Rayer/LWC-459-uiux-current`。依 Owner 要求，由 parent 完成備份、原分支 fast-forward 與 84 個路徑的雜湊核對後，繼續於 `Rayer/LWC-459-uiux-redesign`，工作路徑 `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign`。session `01a19498-2e8d-76c2-a8c7-90442bb9195b` 不變。

設計判讀：給持續研究與開發的知識工作介面，以品牌 Graphite、Sage 與真實 W 標誌串接來源、整理、引用。DESIGN_VARIANCE 6、MOTION_INTENSITY 3、VISUAL_DENSITY 3。

Taste：high-end-visual-design 的細緻層次與回饋，experimental 的 6/3/3 旋鈕，現行 Taste 的 app-screen route。Owner 的品牌與禁令優先於技能的玻璃、純黑、大圓角、眉標與巢狀 bezel 建議。抽選 True-black flat，seed 311143542；將純黑映射到品牌 #101112。一次 shell redraw：原 No sidebar 不適合新版完整七項導覽，改用 Ruled T-frame 的可折疊側欄。保留既有側欄骨架，以明度區分選取。

## 來源與功能盤點

| 範圍 | 最新來源／契約 | 保存方式 |
|---|---|---|
| 搜尋首頁 `/` | HomeClient：wiki/full、建議查詢、query outcome、模型先驗揭露、AI operation errors、引用對話框與完整頁連結 | 原查詢與引用處理不變，移除光暈、裝飾眉標與 serif |
| 來源 `/sources` | SourceListClient、SourceListItem：rawPath、lifecycle、annotationPresent、annotationAllowed，無日期 | 保留匯入紀錄、原文預覽、深連結、專案授權；加入可組合工具列 |
| 條目 `/concepts` | ListClient、WikiEntry：title、slug、status、description、date | 原 API 與詳情連結保留，日期只稱「資料日期」，無日期置後 |
| 詳情 `/sources/[slug]`、`/concepts/[slug]` | DetailClient、MarkdownView、來源註記、raw link、返回 | 原引用、Markdown、安全圖片、註記與返回保留 |
| 帳號與安全 | AccountSettingsModal、account-password：GET/POST `/api/v1/auth/account/password`，Google identity/link | 三分頁、密碼狀態、當前密碼驗證、dirty 與 focus trap 全保留 |
| LLM Provider | BYOKProfilesPanel：provider/model refresh、CRUD、驗證、operation failure | 保留完整現代 Provider 操作，不以舊 AccountSettingsModal 覆蓋 |
| 連線與同步 | cli-auth：sessions、revoke、bindings、reauthorize，分別走 auth_url/api_url | session 與 binding 資訊及操作保留 |
| 專案設定 `/profile` | ProjectProfilePanel：project-info/pipeline/knowledge/ai-service，hash targeting、dirty blocker | 四個 tab 與深連結全部保留 |
| 專案資訊 | ProjectDescriptionEditor、ProjectContentContextPanel、rename | revision、內容摘要、編輯與儲存保留 |
| Pipeline | ProjectPipelineAutomationSettings、PipelineClient | automation revision/admin policy/backlog/effective status 保留 |
| 知識設定 | requirements、candidate/active、derivation、recompile、教學 | 所有生成與確認流程保留 |
| LLM 來源 | ProjectLLMSourcePanel：shared/personal/profile binding revision | 選擇來源與共享限制保留 |
| `/advanced` | AgentMcpTabs、ProjectKeysSection、ExportPanel | MCP、專案 key 與 export 行為保留 |
| `/cli`、`/cli-pairing` | CLILaneClient、CLIPairingClient | CLI 安裝與指引、pairing approve/deny 保留 |
| `/oauth/consent` | MCPOAuthConsentClient | consent 驗證、scope 與決策保留 |
| `/raw` | RawClient、RawUploadPanel、DocumentImportsPanel | 資料夾與多檔上傳、轉換／失敗狀態及原文保留 |
| `/status`、`/admin` | StatusClient、AdminClient、AdminSharedProfilesPanel | 狀態、quota、管理權限與共享設定保留 |
| `/login`、登入／註冊／公告／新專案／改名 | GoogleCompletionClient、各原有 modal | auth、公告 dismiss、語系、dirty/focus/escape 保留 |
| 法務 `/terms`、`/privacy`、`/en/*` | legal layout | 內容與路由保留，套品牌字色 |

## 舊稿落差與實作決策

舊稿 patch 的 AccountSettingsModal 基於早期單一視窗，無目前三分頁、密碼與完整 CLI/sync 操作；其 ProjectProfile 改稿也不是最新四分頁。舊稿只看既有 patch 中的視覺差異，未套 patch、未拷貝元件、API 或測試。新版現有雙欄保留，768 以下單欄。

來源篩選使用真實 lifecycle 與 annotationPresent；排序使用 title/rawPath。條目篩選依實際 status 建立選項，提供日期有無；排序使用 title/slug 與資料日期。`WikiEntry.date` 是 normalizeEntry 對 date/createdAt/updatedAt 的既有兼容欄位，不能保證是修改日，因此不提供「最近更新」文案或補造日期，不改後端契約。

本機資料：scripts/lwc459-local-api-fixture.mjs，兩個明確標示「合成資料」的專案、4 個來源與 4 個條目。僅 loopback 4312，前端 3001。真實雲端、provider、IAM、憑證與正式寫入均未測。

## 驗證紀錄

轉移後已重新生成 i18n，重新執行 regression／typecheck／lint／production build；舊 `.next` 與舊路徑的通過紀錄不作最終驗證。以下紀錄以 scratch 的 `resumed/` 為準。無 commit/push/PR/merge/deploy。


## 最終實作與功能保存

共用品牌層採 Geist／PingFang TC、Graphite #101112、Surface #191B1C、Ink #F0F3F1、Sage #9AC9B2、Muted #A5AFAB。共享 Surface、Badge、分頁、表單與導覽套用同一系統；移除光暈、玻璃、漸層、靜止陰影、裝飾 serif／等寬標籤及重複眉標。圖示沿用已安裝 Lucide，統一 1.5px stroke，未新增依賴。原始 SVG 未改幾何。

首頁保留 wiki/full、建議搜尋、AI 來源揭露、引用與新增內容；搜尋為主要操作。來源／條目新增同一套工具列，可組合關鍵字、兩項條件與排序，顯示結果／總筆數，可清除。來源匯入歷史放在來源列表之後，深連結與原文預覽功能保留。列表維持 768px 起雙欄、以下單欄。條目快取加入 token／session epoch 邊界，避免同一 project ID 在不同 session 重用快取。

專案設定的四分頁、帳號設定三分頁、密碼、Provider Fast/Heavy 模型操作、CLI sessions、sync bindings 與重新授權都保留原元件及事件處理。既有 API／auth／projects／cli-auth／account-password 等 library 與所有路由契約均未修改。只有新列表 helper 為純資料選取與排序。狀態頁三項數量合成單一資訊列，移除原本 2+1 的孤立格。登入品牌區改為居中，縮小標語，手機保留單欄登入表單。返回頂部保留功能，改由 IntersectionObserver 判斷，不再註冊 window scroll listener。

主要檔案：`globals.css`、`Shell.tsx`、`HomeClient.tsx`、`ListClient.tsx`、`SourceListClient.tsx`、新 `ListToolbar.tsx`／`lib/list-controls.ts`、`EntryCard.tsx`、共享 ui/、現有設定與對話框的視覺 class；locales、runtime params registry 與 seed provenance 一起更新。`package.json`／lockfile 未修改。

## 檢查與限制

在 apps/frontend 執行：

| 檢查 | 精確命令／證據 | 結果 |
|---|---|---|
| 列表行為 | `npx vitest run tests/lwc-459-list-controls.test.tsx` | exit 0，6 案例，G2 |
| 主要 regression | GATES.md G3 所列完整 15 個檔案命令 | exit 0，15 檔通過，含帳號、密碼、Provider、CLI、專案設定、引用、文件來源 |
| 最後視覺回歸 | `npx vitest run tests/lwc-389-mcp-oauth.test.tsx tests/status-client.test.tsx tests/lwc-346-cli-pairing.test.tsx` | exit 0，3 檔／88 案例；與主 suite 有重疊 |
| Node regression | `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/lwc-429-local-fonts.test.mjs tests/list-client-cache.test.mjs tests/lwc-139-source-raw-link.test.mjs tests/lwc-169-sources-annotation.test.mjs` | exit 0，15 案例 |
| 型別 | `npm run typecheck` | exit 0 |
| ESLint | `npm run lint` | exit 0；既有 NavigationBlocker useMemo dependency 警告 1 項 |
| i18n／build | `NEXT_PUBLIC_CONFIG_URL=http://127.0.0.1:4312/frontend-config.json npm run build` | exit 0，使用正式離線 i18n generator；zh_tw seed valid |
| Taste source | `node /Users/rayer/.codex/skills/design-taste-frontend/scripts/lint.mjs --app apps/frontend/src` | exit 0，0 FAIL；7 處實際 code／字體定義的 mono 與 2 處 shadow:none／reduced-motion focus 提示 |
| 本機 production | `npm run start -- --hostname 127.0.0.1 --port 3001` | 從此 worktree 啟動 |
| 合成 API | 從 repo root `node scripts/lwc459-local-api-fixture.mjs` | loopback 4312；資料與帳號明確標示合成 |

精確 G2–G6 命令與當前 cwd／結果指紋見 `GATES.md`，執行紀錄在 scratch `resumed/gates-final.log`。轉移後首次 G3 因尚未生成的 i18n catalog 不能載入模組而失敗，生成後整組通過；未加入 shim。早期 QA 的來源引用誤期待 `/concepts/`，以及隱藏控制項／不正確的關閉按鈕文案，均為 harness 問題，修正成真實可見控制項後重測。

所有瀏覽器資料與寫入均在 fixture。描述儲存是一次 UI 發出的合成 PUT；未提交真實密碼、Provider key、OAuth allow、CLI 撤銷、新專案建立或正式文件上傳。Provider discovery/test、session/binding 的安全邊界由現有 component regression 驗證。沒有真實雲端端到端、正式服務、provider、IAM 或憑證測試。Admin 僅驗證無權限畫面，未授予或操作真實管理權限。fixture 沒有 exports、recompile capability、Google OAuth completion 成功端點，相關頁面保留誠實錯誤／缺資料狀態。

## 看圖判讀

問題與主操作：首頁回答「如何在這個專案搜尋與接續知識」，列表回答「有哪些來源／條目符合條件」，設定回答「此專案或帳號目前怎麼設定」。不引入假 KPI、假活動或虛構成效。

1. 最大元素及先後順序：標題 32px／手機 26px，搜尋與列表在主要內容前段；設定各領域以真實分頁進入。
2. 對齊：1440px 中央內容 1072px，雙欄 `(1072 - 12) / 2 = 530px`；768px 內容 720px，雙欄 354px；390px 內容 358px、單欄。左右使用相同 gutter，沒有頁面水平溢出。
3. 品牌：原 W 標誌與產品名在 shell，明度區分表面與選取；Sage 只作主要互動、狀態與 focus。沒有圖表，不補造數據來填空。
4. 事實：筆數由當前 rows 計算，專案切換後 4 筆變 1 筆；合成資料日期只標資料日期，無日期的草稿保持無日期。
5. 零／缺值：0 筆結果、0 個匯入失敗如實顯示；資料不足與 API fixture 未實作端點有明確狀態，不補造 timestamp。
6. 引用：回答與來源分列，引用可開 preview、完整頁與返回列表；原文 Markdown／表格／code 區保留各自可讀性。
7. 手機：側欄折疊、列表單欄、工具列換行、設定 tab 分成適合觸控的列；長表單和對話框可捲動，無內容被切掉而無法操作。

已修正前三項視覺問題：首頁裝飾與過大開場、來源列表前的空匯入歷史、專案設定巢狀外框；第二輪修正登入品牌區過低與多餘眉標。沒有使用生成照片、外部圖片或假姓名；教學中的固定餐廳案例是原有產品教學，保留非 AI 生成／非即時資料說明。


## 最終量測、截圖與交付狀態

四個 production 頁面使用同一命令形式：
`node /Users/rayer/.codex/skills/design-taste-frontend/scripts/inspect.mjs --app http://127.0.0.1:3001[/sources|/concepts|/profile] --out <scratch>/resumed/after-<page>`，完整 URL 及日期在各 report.json。

| 頁面 | 結果 | 警告判讀 |
|---|---|---|
| 首頁 | exit 0，0 FAIL／1 WARN | heavy-borders 的 meter 把透明邊框 alpha 改為 1；實際 10% 邊框在 #101112 上合成為 #262828，並非輸出的白色強對比框 |
| 來源 | exit 0，0 FAIL／0 WARN | 無 |
| 條目 | exit 0，0 FAIL／1 WARN | 僅四筆合成條目，短列表後的剩餘高度保留，不為填滿畫面虛構資料 |
| 專案設定 | exit 0，0 FAIL／2 WARN | 邊框同上；768px ragged-row 實際把已移到畫外的側欄帳號區與「專案內容摘要」配成同列，不是可見資料卡不齊 |

獨立量測見 `resumed/warning-measurements.json`，未修改 inspector 規則、未隱藏其結果、未使用 allow 旗標。改版前為 3 個使用中字體、h1 43px、首頁 8 WARN；改後核心首頁只使用 Geist、h1 32px／手機 26px、1 個已解釋的量測警告。app-screen route 沒有行銷頁動效最低數量要求，保留低強度 hover／press／focus 與 reduced-motion。

最終重新看圖包含 17 張三尺寸對照 sheet、四個核心頁的完整分屏 sheet，以及修正 fixture schema 後的 URL 預覽三張原圖。畫面沒有水平溢出。`resumed/browser-results.json` 110/110、`browser-extra-results.json` 44/44、`browser-url-results.json` 9/9，共 163 項操作／幾何斷言通過。URL preview 明確是 fixture 合成內容，沒有抓取外網，也沒有接受儲存。

共 158 張轉移後的原始 PNG，包含 34 個主要路由／對話框場景的完整 390／768／1440px 組，以及篩選、捲動、手機選單等補充狀態；部分 tab 截圖為 focus 後的面板視角。索引與 SHA-256：

- `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-current-base/resumed/screenshot-manifest.json`
- `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-current-base/resumed/screenshots/`
- `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-current-base/resumed/review-sheets/`
- 完整改檔名單 `resumed/changed-files.txt`，逐檔指紋 `resumed/changed-files.sha256`，目前 84 個路徑，含既有轉入品牌素材與 brief。

本機預覽 http://127.0.0.1:3001，合成 API http://127.0.0.1:4312。兩者均從 uiux-redesign 啟動，保持可見互動 session。G1–G8 均已以本路徑的來源、測試或人工看圖證據完成；沒有未完成的本機改版工作。真實雲端／provider／管理權限成功流程仍屬本次未執行限制，不能將這些 fixture 結果當作雲端驗收。


## Owner 後續調整：最新條目入口

保留「最新條目」區塊，右上角數字 badge 改為「所有條目」連結，使用既有 NavigationLink 到 `/concepts`；英文為 All entries。已同步正式 i18n 與 seed provenance。`npx vitest run tests/lwc-304-modern-login-home.test.tsx`：exit 0、6 案例；production build：exit 0；瀏覽器確認桌機／手機無數字 badge，且連結確實開啟條目列表。新截圖 `resumed/all-entries/` 優先於先前 158 張基線截圖中的此區塊。預覽已更新。
