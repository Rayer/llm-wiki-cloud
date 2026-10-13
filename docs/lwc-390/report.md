# LWC-390 Profile 設定與引導 UI 實作報告

## 來源與交付範圍

- Worktree：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-390-391-profile-full-recompile`
- Branch：`Rayer/LWC-390-391-profile-full-recompile`
- develop 基準：`14cdc4bd663ec4e7a2ccfbde72f0f919426931c1`；最後 fetch 的 `origin/develop` 仍是此 SHA。
- LWC-391 commit `b86a6487bc144c6b42f00270e91ccb681b290144` 上完成 Profile UI 與 PipelineClient project-context repair 的實作 commit：`4d73702d2eeb6836690ca7353977ace51c7789d6`。
- 精確提交內容的本機封存：`/Users/rayer/Develop/llm-wiki-cloud/.git/worktrees/LWC-390-391-profile-full-recompile/lwc390-391-checkpoints/4d73702d-source.tar.gz`，SHA-256 `5701d996570f9b664d3e37c8778fc9a68d2743957b2b8fbb8097fc4b2274c0f1`。封存不在 PR 內容中。
- 已有 PR #122（base `develop`）：https://github.com/Rayer/llm-wiki-cloud/pull/122。程式碼 source commit 已完成；最終 PR head、CI 與 main 的同一 SHA 雙重 review 由出版 read-back 及 main 後續處理確認。
- 範圍限於既有 Profile 編輯面板、三張設定卡、同頁教學、英文與繁體中文文案、測試／本機 fixture，以及 LWC-391 PipelineClient 的 project-context generation 防護與五個回歸測試。未改 BFF、API schema、quota/top-k、provider、IAM、credentials、Production 或 live Project 資料。

## 實作摘要

- 設定頁預設三張卡全收合且單開；full-recompile 永久提示和依真實 Profile/job 讀回的狀態摘要保留在卡片外。一般設定或既有 active 不被儲存、確認、教學或單一標記工作改寫成已生效。
- 親子與停車例句僅追加到同一份 requirements 編輯器，或填入空白欄；保留原有文字。輸入範例本身不會儲存或確認，唯一寫入由現有「儲存需求」操作觸發。
- 教學改成同頁三屏：固定來源筆記、同一案例的 Without／With Profile concept 與 tags、單一台北市大安區查詢。可以直接跳屏、上一頁／下一頁、返回設定；教學始終標示預寫示例，unknown 保持未知，不宣稱即時 AI、GPS、距離、硬篩選、查詢結果或即時改寫。
- 391 的 PipelineClient 以 project-context generation 檢查跨 POST、poll、capability read 與晚到錯誤，避免切換 Project 後套用前一 Project 的請求／輪詢結果；無 backend cancellation 或額外 POST。

## AC 證據

| 項目 | 驗證 | 結果 |
| --- | --- | --- |
| A：卡片、常駐說明、雙語與鍵盤焦點 | `lwc-211-profile.test.tsx` 測試初始全收合、單開／再按收合、繁中狀態及 guide focus；瀏覽器測試與桌面／手機截圖 | PASS；desktop 1280×900、手機 390×844，初始 notice 可讀，沒有水平溢位。 |
| B：自由文字例句、draft 保留、無隱性 mutation | component 測試檢查自訂文字不被覆蓋、例句追加、save／confirm 未自動呼叫；真 `ProjectProfilePanel` + 本地 mock transport 瀏覽器檢查寫入數為 0，明確 Save 後才增加至 1 | PASS；mock transport 實際記錄 Profile PUT，不連 BFF。 |
| C：固定教學、同案例、unknown、Query 邊界 | component 與瀏覽器檢查 source → concepts → query，concept/tag 分列、未知 stroller 資訊保持未知、stage jump 將 focus 移至標題、不出現結果連結或選取控件 | PASS；Desktop comparison 2 欄，390px 1 欄。 |
| D：真實生命週期展示 | 瀏覽器 fixture 顯示舊 active 與 pending tagging 同時存在；bootstrap 預覽明示未確認，確認後仍不是 generation-bound active；原 bootstrap/candidate/error 回歸測試繼續通過 | PASS；全程為受控 mock state，非 BFF E2E。 |
| E：project 切換的非同步隔離 | `lwc-390-391-project-context.test.tsx` 五個測試涵蓋延遲 full POST、晚到 A poll、A→B→A generation、舊 capability refresh、舊錯誤；`lwc-211-profile.test.tsx` 保留 Profile project/account scope regression | PASS；frontend full suite 全數通過。 |

## 本機 fixture 與視覺證據

Fixture 使用實際 `ProjectProfilePanel` 和既有 API helper；`fetch` 僅接受 fixture 的 Profile 路徑，其餘路徑明確封鎖。頁面頂端顯示「LOCAL FIXTURE ONLY」。執行下列 script 時會暫時建立 `/lwc390-local-fixture` 路由，結束 server 時自動移除：

```sh
cd /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-390-391-profile-full-recompile
./scripts/lwc390-profile-fixture.sh
```

本次實際 URL 為 `http://127.0.0.1:4173/lwc390-local-fixture`，受控瀏覽器檢查前 `curl` 得 HTTP 200。瀏覽器檢查命令為 `node scripts/lwc390-browser-check.mjs`；Chrome 預設路徑可用 `LWC390_CHROME_PATH` 指定。檢查完成後 fixture server 與臨時路由已停止／移除，沒有把 fixture route 放入產品 commit。

實際截圖（PNG，非合成圖）：

- [桌面設定 1280×900](evidence/lwc390-profile-desktop.png)
- [390px 手機設定](evidence/lwc390-profile-mobile-390.png)
- [390px 手機 guide concepts](evidence/lwc390-guide-mobile-390.png)
- [390px 首次編譯確認後狀態](evidence/lwc390-first-compile-mobile-390.png)

瀏覽器 attempt 1–10 的原始失敗原因均保留；attempt 11 為 26 項 PASS、exit 0。失敗皆為 runner 或 fixture 觀測設計（分欄 regex、fixture 尚處理中時錯誤嘗試儲存、mock 使用錯 HTTP method、fixture 控制項把內容推離初始 viewport、確認寫入計數漏記），沒有隱藏產品失敗或以假 E2E 替代。

## 驗證紀錄

| 命令 | 結果 | 原始輸出 |
| --- | --- | --- |
| `cd apps/frontend && npm test` | exit 0；539 Node tests、381 component tests | `evidence/frontend-test-attempt2.log` |
| `cd apps/frontend && npm run lint` | exit 0 | `evidence/frontend-lint-attempt3.log` |
| `cd apps/frontend && npm run typecheck` | exit 0 | `evidence/frontend-typecheck-attempt5.log` |
| `cd apps/frontend && npm run build` | exit 0；移除臨時 fixture route 後完成 Next production build | `evidence/frontend-build-attempt2.log` |
| `python3 scripts/test_frontend_build_config.py && python3 scripts/test_frontend_config_artifacts.py` | exit 0；合計 9 tests | `evidence/frontend-config-suites-attempt3.log` |
| `node scripts/lwc390-browser-check.mjs` | exit 0；26 項本機瀏覽器檢查，無 E2E 聲稱 | `evidence/browser-check-attempt11.log` |
| `git show --format= --check 4d73702d2eeb6836690ca7353977ace51c7789d6` | exit 0 | `evidence/git-source-check-attempt1.log` |

LWC-391 BFF/Go 測試結果留在原 `docs/lwc-391/report.md` 與 evidence 歷史；此 successor 沒有改 BFF，也沒有把舊 SHA 的 BFF 執行冒稱新 SHA 測試。完整同 SHA CI、main 的雙重 review、develop 正常 merge 與正式 DEV/QA 仍待 main 完成。
