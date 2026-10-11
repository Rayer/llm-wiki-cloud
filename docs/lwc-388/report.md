# LWC-388 本機實作報告

## 來源與範圍

- Worktree：`Rayer/LWC-388-profile-ux`，基底為 `7713976360fe3573c7e36d56ceb2d31daf2751a5`。
- 契約：凍結的 `profile-local-contract-r1`；審查產物：`profile-review/profile-local-result.json`（PASS `8e1135e4996d14d723e1a2d8`，attempt 1）。
- 目前 worker 環境缺少繼承的 `YOUTRACK_BASE_URL` 與 `YOUTRACK_TOKEN`，因此無法執行 YouTrack handoff reader；實作依據為 parent 取得的即時 ticket snapshot 與凍結契約。未搜尋其他憑證來源。
- 範圍限於 Profile 編輯器、本機說明與模板元件、相關測試、中英文 Profile 文案，以及隔離的本機 fixture／證據。Project Settings 的 external-key 行為仍保留，未修改 API、schema 或 backend 行為。

## 實作內容

- 新增四種 Profile 範本與偏好調整，內容會填入既有可編輯的 requirements 草稿。取代非空草稿與清除草稿都需要明確確認。選擇或調整範本不會儲存或 bootstrap Profile；只有既有 Save 操作會持久化。
- 新增明確標示為靜態示意的預期效果區塊，並與 API system interpretation 及 active Profile 狀態區分。
- 新增精簡四階段、單一閱讀區的說明，可直接跳到各階段；另有預設收合的一般模式 Profile 範例，範例選擇會保留。假設餐廳故事在各偏好模式都維持「台北市大安區」為必要地點；停車與親子需求是偏好，未知資訊不等同否定。文案未宣稱 GPS、距離或硬性篩選能力。
- 新增斷言涵蓋草稿取代／取消、自由編輯及模板調整在 Save 前維持本機狀態、清除確認、初始空白狀態不自動 bootstrap、地點在不同模式下保持不變、範例故事不影響專案草稿、概念與 tags 的差別、未知值處理，以及 zh-TW 文案。

## 驗證

除非命令列有註明，以下命令皆在 `apps/frontend` 執行。

| 命令 | 結果 |
| --- | --- |
| `npm test` | Exit 0；529 個 Node 測試與 344 個 component 測試通過。 |
| `npm run lint` | Exit 0。 |
| `npm run typecheck` | Exit 0。 |
| `python3 ../../scripts/test_frontend_build_config.py` | Exit 0；4 個測試通過。 |
| `python3 ../../scripts/test_frontend_config_artifacts.py` | Exit 0；9 個測試通過。 |
| `npm run build` | Exit 0；Next.js 16.2.7 production build 編譯成功並產生所有頁面。 |
| `git diff --check` | Exit 0。 |

以上為目前 CI workflow 列出的 frontend suites。未執行 `make verify` 或 BFF/backend 檢查，不代表 backend 已驗證。

## 本機瀏覽器 fixture

- URL：<http://127.0.0.1:3000/profile>
- 執行 `bash docs/lwc-388/start-fixture.sh` 啟動。頁面使用真實 Profile route 與元件，但 API 由 `127.0.0.1:8089` 的合成記憶體 fixture 提供；只使用 `.build/cac/local/` 下被忽略的本機設定。
- 在新的瀏覽器 profile 開啟頁面時，可於瀏覽器 console 設定下列 session 後重新載入：

  ```js
  localStorage.setItem("llm-wiki-access-token", "fixture-access-token");
  localStorage.setItem("llm-wiki-auth-user", JSON.stringify({ id: "fixture-user", email: "fixture@example.test" }));
  location.reload();
  ```

- Fixture 不連接正式 Project 資料、provider 憑證或 BFF 服務；假資料專案為 `profile-fixture`，狀態只存在 fixture process 記憶體，重啟後會清除。這是人工瀏覽器檢查，不是 E2E。
- 桌面 1440px 與手機 390px 寬度下，document/body 寬度均與 viewport 相同，未觀察到水平溢位。瀏覽器互動確認可直接跳階段、開啟個人偏好後查詢地點仍是大安區，以及取消取代後保留自訂草稿。範本與故事互動期間沒有發出 Profile `POST`／`PUT` 請求。
- 截圖為整頁擷取：[桌面版](evidence/profile-desktop.png)（1440 × 2546）與 [390px 手機版](evidence/profile-mobile-390.png)（390 × 4711）。

## 變更檔案

- `apps/frontend/src/components/ProjectProfilePanel.tsx`
- `apps/frontend/src/components/ProfileExpectedEffect.tsx`
- `apps/frontend/src/components/ProfileHowItWorks.tsx`
- `apps/frontend/src/components/ProfileTemplates.tsx`
- `apps/frontend/src/messages/en.json`
- `apps/frontend/src/messages/zh-TW.json`
- `apps/frontend/tests/lwc-211-profile.test.tsx`
- `docs/lwc-388/profile-fixture.mjs`
- `docs/lwc-388/start-fixture.sh`
- `docs/lwc-388/evidence/profile-desktop.png`
- `docs/lwc-388/evidence/profile-mobile-390.png`
- `docs/lwc-388/report.md`

本次本機實作 dispatch 未執行 commit、push、開 PR、merge 或 deploy。

## 發布前的同編輯器修正

- Coordinator review 指出產品故事中的 pipeline 與 concept 示意沒有呈現偏好差異。沿用同一個 Profile editor，將 pipeline 固定示意改為依一般／已選偏好說明整理重點；knowledge concept 在一般、停車、親子、兩者四種狀態下分別突出相關來源事實。raw 素材不變；每種 concept 都保留台北市大安區與嬰兒車通行資訊未知。
- 新增 component assertion 涵蓋 pipeline／concept 狀態差異、偏好往返保留、地點與未知資訊持續可見，以及專案草稿與 API mutation 不受故事互動影響。
- 修正後全套結果與原始輸出位於 `evidence/publication-npm-test.log`、`publication-lint.log`、`publication-typecheck.log`、`publication-build-config.log`、`publication-config-artifacts.log`、`publication-build.log`、`publication-diff-check.log`。另有 `evidence/repair-focused-profile.log` 記錄 Profile focused suite。
- 修正後 `npm test`：529 個 Node 測試與 345 個 component 測試通過；lint、typecheck、兩組 frontend config suite（4、9 個測試）、production build 與 `git diff --check` 均 exit 0。`make verify` 與 BFF/backend 檢查仍未執行。
- 重新啟動後本機 `/profile` 回應 HTTP 200；fixture public config 的 response 與命令結果記錄在 `publication-fixture-route.log`、`publication-fixture-config.log`。此 fixture 僅使用合成 in-memory API。
- 原有桌面／390px 截圖及第一輪人工互動紀錄保留。修正後未宣稱有新的互動截圖：Chrome 新分頁停在未載入 fixture session 的登入畫面，Safari 的 Orca accessibility tree 被 macOS 權限阻擋；未調整系統權限。修正後故事狀態由 component suite 驗證，未把 mock 或先前截圖稱為 E2E。
