# LWC-459 最新基底上的本機改版

## Owner 最新指示與授權

Owner 指出前次改版基底過舊，要求先修正基底並使用可見的 Sol 6.1 Codex；其後要求整合到同一個 worktree，以便討論。coordinator 已保存兩邊改稿、停止舊程序、將原工作目錄快轉至新版基底，並逐檔核對移入的 84 個修改路徑。後續只在原討論工作目錄繼續：

- Worktree: /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign
- Branch: Rayer/LWC-459-uiux-redesign
- 初始 HEAD 與 origin/develop 相同：a3f8ba14407e4cb686b649b397e947411ac71361
- uiux-current 已停止工作，只保留為轉移來源／備份，不再在該處啟動編輯器或預覽。
- 轉移備份與逐檔雜湊核對：/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-consolidation/。舊稿另有 stash e8c9b94f3647a86f43938c91f080f1f1cd27b76d；不得把它套回新版。
- 舊稿完整備份：/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-current-base/old-design-snapshot.tar.gz
- 14 份 brandkit 原始檔已複製到本工作樹 brand/lwc/，與舊稿逐檔 SHA-256 相同。

## 目標與邊界

先驗證基底身份與新版完整功能，再在本工作樹持續實作全站改版。不要把舊稿整檔覆蓋新版，也不要將舊版測試與 API 倒灌進新版。保留最新帳號設定的帳號與安全／LLM Provider／連線與同步分頁、密碼功能、CLI sessions、sync bindings，以及 /profile 專案設定、LLM 來源、知識設定、Pipeline 自動化與 /advanced、/cli 等新版功能；以實際路由及呼叫契約重新盤點，不限於這份列舉。

Owner 另確認來源與條目列表皆需要排序（Order by）與篩選（Filter by）。桌機列表應回到緊湊雙欄，手機單欄；不要因 anti-slop 禁止行銷 feature cards 就排除合理雙欄資料列表。兩頁共用一致搜尋／篩選／排序工具列，以新版真實 schema 提供有意義選項，不用虛構時間做最近更新。關鍵字、條件篩選、排序應能組合，呈現結果筆數與清除條件；保留專案隔離。缺失日期不可冒充更新時間，必要的欄位或語意決策明確提出，不擅自修改後端契約。

保留目前技術棧、API／路由、資料與權限／憑證契約、產品行為。新版功能全部保留，不是舊版視覺復刻。只在 uiux-redesign 寫入來源。coordinator 已停止舊版 parent-owned 3000/4311；worker 已停止自有 3001/4312，避免轉移中載入不一致來源。不得操作其他 worktree 或無關程序。

不 commit、push、PR、merge、部署、Vercel preview 或 production／provider／IAM／憑證操作。只讀取繼承的已授權設定，不搜尋其他 Hermes profile 或秘密。不為了本機出圖操作真實帳號或金鑰。既有 repo 推送規則不覆蓋 Owner no-push。

## 指定 Codex 與設計

使用互動式 Codex gpt-6.1-sol，model_reasoning_effort=high，保持可見工作階段。輸出一行設計判讀後按 audit-first 執行，不再要求 Owner 重複授權此範圍。

使用 $high-end-visual-design，搭配 $design-taste-frontend-v2-experimental 的旋鈕、$design-taste-frontend 的 house rules 與 app-screen redesign route，以及 $unlazy。

- DESIGN_VARIANCE: 6
- MOTION_INTENSITY: 3
- VISUAL_DENSITY: 3
- 手機優先，768px 以下單欄。桌面適度非對稱，列表用適合資料的雙欄。
- brand/lwc/DESIGN.md、VOICE.md、tokens 與 SVG 為精確品牌依據，brand-board.png 是概念參考。
- Geist／PingFang TC；Graphite #101112、Surface #191B1C、Ink #F0F3F1、Sage #9AC9B2、Muted #A5AFAB。真實 SVG 標誌不改幾何。
- 不使用 em dash；預設 Inter／Roboto／Arial、Fraunces／Instrument Serif；紫／靛配色；背景／按鈕／文字漸層；純黑；靜止陰影或光暈；米色金銅棕高級風；玻璃／blur；漂浮藥丸導覽；眉標、裝飾編號／細線／格線／形狀／emoji；非 code 等寬字；等大三卡 feature、假 dashboard；填充姓名／文案／虛構數字；scroll cue／hero 版本字樣；window scroll listener。

## 執行與證據

先閱讀 repo AGENTS.md、當前 Next 版本內建 docs、相關 native skills 與實際來源；不要先載入所有歷史。先輸出最新功能與舊稿落差清單，再在同一輪實作，不停在計畫。

維持新版 loopback ports：前端 3001、fixture 4312，但程序必須由 uiux-redesign 啟動。coordinator 已將本輪 npm ci 取得的 node_modules 移入原工作目錄，舊版 node_modules 與 .next 保存在 consolidation scratch；須重新生成 i18n／build，不可引用舊程序或舊 .next 的通過紀錄。禁止 pnpm 自動 reconciliation；不改 manifest／lockfile。沒有真實設定就以清楚標示的合成資料驗證畫面，不能假稱已做真實雲端測試。

將最新路由（含新設定／連線）、對話框、390/768/1440px 的實際截圖與操作結果存 /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc459-current-base/。改後看圖、修正、重看；驗證搜尋／專案切換／雙欄列表／排序篩選／引用／詳情返回／新版設定分頁。真實寫入與 fixture 模擬區分。跑有意義 regression、lint、typecheck、build，保留精確命令／退出碼及未執行限制，不以大範圍不相干 suite hang 拖住視覺交付。

最終報告列出基底 SHA、功能保存對照、改檔、測試、截圖、本機 URL、資料與執行限制。完成後維持互動 Codex 可見，不將 enqueue 或 model footer 當作實作開始證據。
