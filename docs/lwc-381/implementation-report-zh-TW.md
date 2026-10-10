# LWC-381 / LWC-382-383 實作與驗證報告

## 來源身分

- 分支：<code>Rayer/LWC-382-383-account-ui</code>
- HEAD / 派送基底：<code>457fefe9ec66e46caf0df91def63ef66d9893300</code>（與指定 base 完全一致）
- 候選來源 manifest：<a href="source-manifest.md">source-manifest.md</a>，19 個程式、測試與文件檔，SHA-256 <code>8d6fd9cde06681b13663bb424d2088315d046851f251911224776e4626e700ce</code>
- 工作樹保持未提交。保留原有 382/383 共享編輯器修改；沒有納入 380 的 Query/MCP 診斷、HomeClient 或 QueryErrors 路徑。
- LWC-381 r2 前置規格檢視為 PASS、runtime 當時 NOT RUN；本報告記錄本次 emulator 與隔離 Hermes runtime 證據。LWC-382/383 舊 source-r1 HOLD 報告與原始 1300-file 候選封存維持原樣，請以此 manifest 對目前合併候選重新審查；本報告不代替獨立審查。

## 完成內容

### LWC-381 project-key usability

- 新增獨立 <code>project_key_usage</code> Firestore collection，使用 document ID 作為 key ID，僅寫入 <code>schema_version</code>、<code>key_id</code>、<code>user_id</code>、<code>project_id</code>、<code>last_used_at</code> 五欄；走現有 scoped Firestore。
- 只有完整通過 key、account 與 Project owner 權限檢查後才同步嘗試記錄使用時間。寫入綁定原 request context、最多 500 ms、best-effort；錯誤不改變驗證結果。交易只接受較新 timestamp，身份不符或欄位異常不覆寫。
- List 僅把目前選定 Project 的 key ID 傳入 usage join。讀取失敗、缺少或格式錯誤的 usage metadata 會顯示 unavailable；沒有紀錄顯示 no_record；不影響 key list/create/revoke，也不改動舊 authority record 或其 strict reader。
- 將 Project API keys 移到現有 /profile 的 Project Settings，保留 Profile 頁標題、編輯器和 rename；導覽標籤本地化為 Project Settings／專案設定。登入、非 Demo、有選定 Project 才顯示；元件 key 含 account ID、Project ID 和 session epoch，context 變更會重掛並丟棄延遲回應。
- 加入 Hermes 官方 MCP、secrets/profile 文件連結和設定步驟，MCP URL 取自 runtime BFF API URL 加 /mcp，範例只放環境變數參照。Grok 網頁支援明示尚未驗證；跨 Project 金鑰明示不支援。Query 結果與已發布 generation 分開說明。

### 382/383 source-r1 follow-up

- GoogleAuthError.localReason 明確標記缺少後端錯誤訊息及缺少／不合法授權 URL 兩種本地 fallback；帳戶對話框只翻譯具此型別標記的本地文案。後端提供的任意錯誤文字仍原樣顯示。
- 加入正式 beginGoogleLink helper 到對話框的測試：英文及繁中覆蓋缺少/錯誤 URL、non-2xx 缺後端 error；另驗證非空後端錯誤原文保留。測試以 mock fetch 阻止任何 provider 導航。
- 登出測試使用 logout spy，不呼叫真實登出；瀏覽器檢查以 local API route fixtures 完成，未送出登入、登出、provider、金鑰 create/revoke 等變更。
- 點擊關閉按鈕後焦點返回 Account Settings 觸發鈕；沒有增加 focus 管理依賴。保留已有人類狀態翻譯與未知後端狀態/識別碼原文顯示。

## 驗證結果

以下命令均在目前候選程式碼上執行，括號內為實際 exit code。

- apps/frontend：<code>npm run lint</code>（0）。
- apps/frontend：<code>npm test</code>（0）：Node 528/528、Vitest component 332/332；34 個 component test files。
- apps/frontend：<code>NEXT_PUBLIC_CONFIG_URL=/__preview-runtime-config npm run build</code>（0），接著 <code>npm run typecheck</code>（0）。Build 完成後才單獨執行 typecheck，避免 .next/types 重建競態。
- apps/frontend：<code>python3 ../../scripts/test_frontend_build_config.py && python3 ../../scripts/test_frontend_config_artifacts.py</code>（0，4 + 9 tests）。
- apps/bff：<code>go vet ./internal/auth ./cmd/bff</code>（0）；<code>git diff --check</code>（0）。
- apps/bff：<code>FIRESTORE_EMULATOR_HOST=127.0.0.1:8087 go test ./internal/auth -run TestProjectKey -count=1</code>（0）；涵蓋 scoped collection、authority bytes 不變、嚴格舊 reader、並行 monotonic timestamp、身份不符和 metadata status。
- apps/bff：<code>FIRESTORE_EMULATOR_HOST=127.0.0.1:8087 go test ./cmd/bff -run TestProjectKeyManagementAndQueryRoutes -count=1</code>（0）；驗證新 key 的 no_record、HTTP Query executor 失敗後仍記錄授權使用、MCP initialize 後 usage 可見，以及 MCP Query 錯誤路徑。
- apps/bff：<code>FIRESTORE_EMULATOR_HOST=127.0.0.1:8087 go test -race ./internal/auth -run TestProjectKey -count=1</code>（0）。
- apps/bff：<code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test -race ./internal/auth ./internal/handler/v1 ./cmd/bff</code>（0）。provider key 變數已移除；三個完整套件皆通過。
- <code>node /tmp/lwc381-rendered-qa.cjs</code>（0）：實際 Next production build 配 Chromium，桌面 1280×900、手機 390×844，英文與繁中各一組。路由 API 全部由本機 fixture 攔截；四組均顯示三種使用狀態，Project Settings link 指向 /profile；Tab 實際到達帳號設定按鈕並顯示 focus-visible 外框，Enter/Space 開啟，Escape/關閉鈕關閉並返回焦點。沒有 page errors 或非預期 API mutation；刷新 token 的 POST 僅由同源 fixture 攔截並回 401。
- 真實 Hermes registry + production BFF router + loopback Firestore emulator 驗證（0）：MCP protocol 2025-11-25、Hermes MCP SDK 2.0.0、14 種呼叫/錯誤/恢復案例、5 種 HTTP/MCP parity 案例、zero model calls。使用 env allowlist 及臨時 HOME/HERMES_HOME/TMPDIR，僅用 emulator 合成金鑰；沒有讀寫使用者現有 Hermes profile。

實際錯誤與重跑也保留如下：第一次完整 BFF race 測試把 emulator 放在 8087，handler 測試要求固定 loopback 127.0.0.1:8585，exit 1；改用 8585 後上述完整 race 命令 exit 0。第一次 typecheck 與 Next build 並行，.next/types 暫時缺檔而 exit 2；build 完成後分開 typecheck exit 0。最初定向 UI 測試的 matcher/list query 測試寫法失敗，修正後前端完整測試如上通過。第一次 browser QA 顯示關閉鈕點擊會失焦；加上觸發鈕焦點返回後四組桌面/手機檢查皆通過。最初 gcloud 預設 Java launcher 指向 macOS 未安裝 runtime 的 stub；用已安裝的 /opt/homebrew/opt/openjdk/bin/java 啟動 loopback emulator 後測試通過。8087 emulator log 曾出現 concurrent transaction lock timeout 警告；usage emulator assertions、race suite 與完整 BFF race suite 最終皆 exit 0。

## 渲染證據與界限

- 桌面/手機 Project Settings 與帳戶對話框截圖及互動量測在 <a href="evidence/rendered/">evidence/rendered/</a>，彙整於 <a href="evidence/rendered/rendered-qa.json">rendered-qa.json</a>。
- Hermes runtime 脫敏結果：<a href="evidence/native/native-hermes-results.json">native-hermes-results.json</a>；命令與隔離方式：<a href="evidence/native/commands.log">commands.log</a>。
- 目前證據只涵蓋本機 emulator、browser fixture 和隔離 Hermes harness；沒有真實帳戶登入/登出、外部 provider、production 或使用者資料操作。Grok 網頁流程未驗證，multi-Project key 仍依規格 deferred。
- 所有修改保留在工作樹供同編輯器審查與修正，未 commit、push、開 PR、merge 或 deploy。
