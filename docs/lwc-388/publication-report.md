# LWC-388 發布檢查點報告

## 發布資訊

- Branch：`Rayer/LWC-388-profile-ux`
- Base：`develop`，目前觀察到的 remote develop SHA：`7713976360fe3573c7e36d56ceb2d31daf2751a5`
- 已測試並發布的實作／證據檢查點 SHA：`10029bb4a1ce5cfa63f6d2defe6143c6e8586c6a`
- PR：[#117 — LWC-388: add Profile templates and product walkthrough](https://github.com/Rayer/llm-wiki-cloud/pull/117)
- GitHub readback：`isDraft=false`、base `develop`、head `Rayer/LWC-388-profile-ux`；建立時 PR head SHA 與上述檢查點相同。
- PR 建立前已搜尋此 branch 的既有 PR，結果為空；remote branch 建立前也不存在。只推送本 task branch，沒有合併其他 lane 的 bytes。
- 契約仍是 `profile-local-contract-r1`，spec review PASS `8e1135e4996d14d723e1a2d8` attempt 1；Project Settings external-key 區保留。

## 修正與本機驗證

Coordinator review 指出 pipeline 和 concept 固定故事未充分呈現偏好如何改變後續整理重點。於同一 editor 修正：pipeline 一般與個人偏好示意不同；concept 依一般、停車、親子或兩種偏好呈現相符事實，同時保留台北市大安區與「未提及嬰兒車通行狀況」的未知狀態。raw 素材不變。新增 component assertions 驗證狀態差異、偏好往返保留及專案草稿/API 隔離。

完整命令輸出與 exit 記錄：`evidence/publication-npm-test.log`、`publication-lint.log`、`publication-typecheck.log`、`publication-build-config.log`、`publication-config-artifacts.log`、`publication-build.log`、`publication-diff-check-final.log`。Story 修正後 focused Profile component run 記錄於 `evidence/repair-focused-profile.log`。

| 命令 | 結果 |
| --- | --- |
| `npm run test:component -- tests/lwc-211-profile.test.tsx` | Exit 0；34 個 Profile component tests 通過。 |
| `npm test` | Exit 0；529 個 Node tests、345 個 component tests 通過。 |
| `npm run lint` | Exit 0。 |
| `npm run typecheck` | Exit 0。 |
| `python3 ../../scripts/test_frontend_build_config.py` | Exit 0；4 個 tests 通過。 |
| `python3 ../../scripts/test_frontend_config_artifacts.py` | Exit 0；9 個 tests 通過。 |
| `npm run build` | Exit 0；Next.js 16.2.7 production build 成功。 |
| `git diff --check` | Exit 0；最終結果見 `publication-diff-check-final.log`。 |
| `curl -sS -o /dev/null -w 'profile_http=%{http_code}\n' http://127.0.0.1:3000/profile` | Exit 0；`profile_http=200`。 |
| `curl -sS http://127.0.0.1:8089/api/v1/public/config` | Exit 0；fixture 回傳 registration/demo 功能停用設定。 |

`npm test` 等 frontend workflow commands 是本機檢查，並非 GitHub CI。沒有執行 `make verify`、BFF/backend tests 或真實 BFF E2E；不要把 fixture 結果當作上述驗證。

## Fixture 與瀏覽器證據

- 本機 fixture 以 `bash docs/lwc-388/start-fixture.sh` 啟動，URL 為 <http://127.0.0.1:3000/profile>；API 假資料只存在 process 記憶體，不連接真實 Project、BFF、provider credentials 或 Production。
- 原有 desktop 與 390px full-page 截圖保留於 `evidence/profile-desktop.png`、`evidence/profile-mobile-390.png`，以及第一輪人工互動與 viewport 檢查紀錄保留於上方實作報告。它們不是 E2E。
- 修正後沒有新增互動截圖。Chrome 新分頁沒有 fixture session，顯示登入畫面；Orca computer 無法讀取 Safari accessibility tree，回報 macOS accessibility permission denied。未改系統權限，未將先前截圖或 component tests 宣稱為修正後的新瀏覽器互動驗證。

## PR 檢查狀態與剩餘工作

PR 初始 source SHA `10029bb4a1ce5cfa63f6d2defe6143c6e8586c6a` 的 GitHub checks 查詢結果：

- Pass：`frontend-lint`、`frontend-typecheck`、`workflow-source-guards`、`actionlint/schema`、`GitGuardian Security Checks`、Vercel preview checks。
- Pending：`frontend-test`、`local-vertical-smoke`、`bff`。
- PR readback：`state=OPEN`、尚無 code review decision、`mergeStateStatus=UNSTABLE`。Vercel 顯示 preview deployment completed；本 worker 沒有手動 deploy。
- 本報告隨 docs-only commit 推至同一 PR，不更動實作程式碼。文件提交後的 PR head SHA 與 CI 狀態須重新 read back 並回報；本文記錄實作／本機檢查 SHA，該 SHA 後實作程式碼未再更改。

交付仍未完成：需由 parent/lane 對最終 PR head SHA 做 same-SHA code review 與 CI；parent run `run_57435001605a` 是唯一 merge/DEV coordinator。此 worker 沒有 merge、部署、操作 Production/IAM/provider credential 或真實 Project。
