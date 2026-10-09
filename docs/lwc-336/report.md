# LWC-336 實作與驗收報告

## 狀態與來源

本次完成 LWC-336 的 BFF MCP adapter、共享 Query 執行路徑、原生 Hermes 隔離驗收器與設定指南。程式與文件仍是未提交候選；本報告記錄本機合成資料和 loopback 測試，不代表已發布或 production-ready。

- Git base：26527e79376e57756842b210e5a57eaa48526d9f；此 SHA 只代表 base，不是目前 dirty candidate 的識別碼。
- LWC-336 開始前的完整 LWC-375/377 source candidate：docs/lwc-336/source-manifest.md，共 19 個來源檔；manifest digest 為 5cf4f58953c88e6cddc76a70421e9ae1cf65f1ac867368d7154eb2b8208c9364。manifest 保留 LWC-375 spec/report、LWC-377 report 與依賴程式檔的完整 SHA-256。
- 本報告的 LWC-336 delta 以該候選為起點，列在下方；delta digest 只計算表中路徑與 SHA-256 行，報告自身不納入，以避免自我雜湊。
- 6750ffceada0474ca8ade5f4 的 PASS 僅適用 LWC-336 設計契約。LWC-375 contract review cb79f1ab7e71989471d0bf9e 與更早的 Spike scope review dfeb023d714ebe33a9a61d09 是不同範圍，並非本次產品實作 review。

## 實作

- BFF production router 掛載 Go MCP SDK github.com/modelcontextprotocol/go-sdk/mcp v1.8.0 的 Streamable HTTP endpoint /mcp，提供單一 query_project 工具。輸入只有必填 q 和可選 mode=wiki|full（預設 wiki），Schema 有唯讀提示；不暴露 project、user、capability 或 Query required_tag_ids 選擇器。
- 每個 MCP HTTP request 在 SDK transport 前重新驗證 project key、account 與 project owner。驗證後的 user/project/key ID 綁定到該次 tool call 的 context；stateless transport 不保存 Mcp-Session-Id，另一把有效 key 的同一 session header 不能沿用舊身份。X-Project-ID 若提供必須符合 key 綁定的 project。
- HTTP Query 與 MCP 共用既有 store/profile-generation resolver、Query executor、mapQueryResult 與 QueryResponse.MarshalJSON。HTTP required_tag_ids、profile generation pinning 與 runtime identity headers 維持既有行為。MCP structured content 與文字內容使用相同 QueryResponse JSON projection。
- 正常空結果、insufficient evidence 與 model-prior disclosure 保持正常 Query 結果；executor/profile/storage 錯誤回傳 isError: true 與安全文字，不洩漏內部錯誤。docs/lwc-336/errata.md 收錄四項 owner-accepted 修正。
- 沒有新增 BFF app setting，沒有將動態 key 資料放進 CaC。Hermes 說明只使用 LWC_PROJECT_KEY 的 secret/environment reference；未讀取既有 Hermes profile 或真實憑證，也未把 key 寫入 repo、log 或證據檔。
- docs/lwc-336/harness/run.sh 以真實 production router、loopback Firestore emulator、synthetic account/project/key 和受控 Query executor，呼叫安裝環境中的原生 Hermes registry；使用隔離 HOME/HERMES_HOME，不呼叫模型或付費 provider。

## Acceptance 矩陣

| 條件 | 結果與證據 |
| --- | --- |
| AC1：production router、原生 Hermes initialize/list/call | 本機 loopback PASS。原生 Hermes 2.0.0 透過實際 BFF router 完成 MCP initialize、列出唯一工具並呼叫工具；Go SDK v1.8.0，協商版本 2025-11-25。model_calls=0。見 evidence/native/native-hermes-results.json。 |
| AC2：HTTP/MCP Query 一致性與錯誤 | 本機 PASS：wiki、full、default-wiki、empty、model-prior 五種 case 的 structured JSON 與 text JSON 都和 HTTP 相同；insufficient 與 model-prior 為正常結果，model-prior 保留空 citations 和 disclosure_required。合成 executor failure 為 isError=true 且回傳 bounded safe text。未跑真實模型/provider。 |
| AC3：授權邊界 | 本機 PASS：缺少/錯誤/撤銷 key、suspended account、失去 owner、錯 project header、跨 key 同 session header、其他 protected route 與既有 Web/CLI Query 相容性有測試。修正後的原生 Hermes 同一 registry 在撤銷前成功取得 MCP HTTP 200；owner 撤銷後該 registry 再送 POST /mcp，test-only server observation 計數增加且實際 status=401。observer 僅記錄 count/method/status，不讀寫 Authorization。 |
| AC4：MCP/HTTP transport 與生命週期 | 本機 PASS：POST initialize 使用 application/json、成功回 application/json，不發 session ID；缺失/錯誤 auth 的 POST/GET/DELETE 為 401；有效 auth 的 GET/DELETE 在 stateless transport 後為 405。協商版本實測為 2025-11-25，server 未假設固定版本。 |
| AC5：操作指南 | 完成。README.md 說明 Hermes URL/header secret reference、query 使用、換 key、撤銷行為、故障狀態與傳統 HTTP Query 範例。 |
| AC6：本機測試與 CI | 下表列出實際命令、結果及限制。BFF、前端、lint、typecheck 與相應 CI checks 有成功結果；8596 的 profile-runtime 初次嘗試是 port 設定錯誤，已在要求的 8585 補跑通過。deployment-engine suite 的 untracked-build-input errors 仍需在乾淨整合候選重跑。 |

## 命令與結果

以下除明確標示失敗者外均為 exit 0。provider/API key 環境變數已 unset；Go 全套測試時 FIRESTORE_EMULATOR_HOST 也 unset，專用 emulator 測試使用 worker 自己啟動的 127.0.0.1:8596。

| 命令／範圍 | 實際結果 |
| --- | --- |
| cd apps/bff && make test | exit 0；Pkl ensure 成功，CD contract 70 tests、Auth config contract 21 tests、local-dev Makefile 20 tests 均通過；go test ./... -v -count=1 -race 有 47 個 ok package、1,590 個 PASS 記錄（含 subtests）、94 個 SKIP 記錄，沒有 FAIL package。 |
| cd apps/frontend && npm test | exit 0；Node 527 passed；Vitest 34 files、312 passed；總計 839 tests passed、0 failed、0 skipped。 |
| cd apps/frontend && npm run lint | exit 0（workflow frontend-lint）。 |
| cd apps/frontend && npm run typecheck | exit 0（workflow frontend-typecheck）。 |
| python3 -m unittest discover -s scripts -p 'test_*.py'（apps/bff） | exit 0；119 tests passed（BFF retained legacy CD safety）。 |
| go run ./cmd/versioncheck VERSION（apps/bff） | exit 0（workflow product-version check）。 |
| go vet ./...（apps/bff） | exit 0。 |
| go build ./...（apps/bff） | exit 0。 |
| make test-flash-execution（apps/bff） | exit 0；CI 精確 pinned wire gate 使用 loopback synthetic peer，沒有呼叫付費/provider endpoint。 |
| FIRESTORE_EMULATOR_HOST=127.0.0.1:8596 go test ./internal/auth ./cmd/bff -count=1 -race -v（apps/bff；provider keys unset） | exit 0；auth 與 production-router packages 通過；BFF package 有 3 個與本票無關的 local service tests 按環境條件 SKIP。包含 Project Key owner routes、Query 相容性和 MCP 真實路由邊界。 |
| go test ./internal/handler/v1 -count=1 -race -v（apps/bff，emulator host unset） | exit 0；MCP/HTTP shared Query、profile generation pinning 等測試通過；21 個既有 emulator-only cases 按環境條件 SKIP。 |
| FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/auth ./internal/handler/v1 ./cmd/bff -count=1 -race -v | exit 0；三個 affected packages 通過，499 個 PASS 記錄（含 subtests）、4 個無關 local-service/emulator SKIP、0 FAIL；先前拒絕 8596 的 profile-runtime suite 在要求的 8585 通過。完整輸出保存在 evidence/affected-emulator-8585.log。 |
| go test ./cmd/bff -run '^TestProjectKeyManagementAndQueryRoutes$' -count=1 -v（apps/bff，Firestore 127.0.0.1:8596） | exit 0；透過 owner create/list/revoke 路由與真實 BFF router 驗證 project-key MCP/HTTP authority。 |
| LWC336_EMULATOR_HOST=127.0.0.1:8585 docs/lwc-336/harness/run.sh（撤銷證明修正版） | exit 0；同一 native Hermes registry 在 owner revoke 前成功取得 POST /mcp 200，撤銷後再送 POST /mcp；test-only server counter 從 35 增至 36，觀測到 HTTP 401。結果與無憑證 observation 存於 evidence/native/native-hermes-results.json。 |
| python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'（apps/bff） | exit 1；Ran 135 tests，9 errors。錯誤是 untracked-build-input，deployment-engine source-identity gate 拒絕本次按要求保留為未提交狀態的 Go build inputs；沒有 staging/commit 去繞過 gate。完整測試輸出保存在 evidence/deploy-engine-untracked-input.log；日後的判別檢查是在乾淨整合候選上重跑此 suite。 |
| 初次 FIRESTORE_EMULATOR_HOST=127.0.0.1:8596 go test ./internal/auth ./internal/handler/v1 ./cmd/bff -count=1 -race -v | 歷史嘗試 exit 1；internal/auth 與 cmd/bff 通過，internal/handler/v1 的 15 個既有 profile-runtime cases 要求 emulator 127.0.0.1:8585，故拒絕 8596。失敗原始輸出保存在 evidence/affected-emulator-mismatch.log。 |

r1 原生結果雖標示整體 passed，AC3 撤銷斷言實為 false positive：evidence/native/native-hermes-results.r1-false-positive.json 保留該歷史 JSON，最後的 safe_error_text 明確是 Hermes local circuit breaker 的「last 3 calls」暫停訊息，沒有證明請求到達 BFF。修正後的 evidence/native/native-hermes-results.json 記錄同一 registry 撤銷前 POST /mcp 200、撤銷後請求計數 35→36 且 POST /mcp 回 401；不記錄 Authorization、key 或 request body。commands.log 保留歷史失敗 probe 及後續真實 exit 記錄；isolated evidence secret-pattern scan 無命中。

## 未執行與限制

- 未連 live Firestore、provider、IAM、公開網路隧道或付費 Query；未登入、未讀取任何真實 credentials/profile，模型呼叫數為 0。
- 未執行 deploy/release、部署、commit、push、PR、merge 或發布。
- 初次使用 8596 時有 15 個 profile-runtime cases 按測試要求拒絕錯誤 emulator port；其後先用 lsof 確認 127.0.0.1:8585 無 listener，再啟動本 worker 自己的 emulator，三個 affected packages 全數通過。由本 worker 啟動的 emulator 已停止，8585 不再有 listener。
- deployment-engine suite 的 9 errors 是 dirty working tree 的 source-identity guard 結果；需在之後乾淨整合候選中重跑，不能把它記為 PASS。
- r1 撤銷 probe 的錯誤 PASS 已明確標記為 false positive 並保留原始 JSON；只有本次帶有 BFF 401 observation 的修正版作為 AC3 原生 Hermes 證據。
- 全套 Go race test 的 94 SKIP 與 handler/v1 專用 suite 的 21 SKIP 都是按現有 emulator/service 條件略過，不代表覆蓋通過。

## LWC-336 delta fingerprints

相對 source-manifest.md 記載的 375/377 起始候選，本票直接新增或修改的來源、測試、指南與驗收證據如下。以下 SHA-256 是各檔案最終 bytes 的 fingerprint。delta manifest digest 使用路徑排序的「repo-relative-path sha256」文字行，每行以 LF 結尾。

| Path | SHA-256 |
| --- | --- |
| apps/bff/cmd/bff/main.go | bcbde3b3bc968ec7d7877f6da91b7d334698edd9e15089ec5b3bd879d1927d9a |
| apps/bff/cmd/bff/project_keys_test.go | 79318cf26263ff3281ae543035336e948e3f618165a0660390f75acab13918de |
| apps/bff/cmd/bff/lwc336_hermes_harness_test.go | 3d19214e7965e6e1ed89b0e02dfe1a2aac8c5f88485ea00f3f5a949e447671be |
| apps/bff/go.mod | a2f33affa770f61e1346a496f81adc735a5dd75a100659c608582e688fb68b2a |
| apps/bff/go.sum | 1bbd0eba385808a5ceaea45577e56a0e08b4adf730cedeee1786348df3b828f8 |
| apps/bff/internal/handler/v1/endpoints.go | b6d51e0dcfdb318c33a5575354adcc5f62874b3cbe418a0ccdd27f9a9b89ed54 |
| apps/bff/internal/handler/v1/query_profile.go | 403c74c3944c3988f3b70676276802838d2882802d920acf20b5047a8272f9a8 |
| apps/bff/internal/handler/v1/query_profile_integration_test.go | 61d6437027bc16f956963d39ec2b4d0a5f1f98912a5537534f1508324a234f8d |
| apps/bff/internal/handler/v1/mcp.go | b3b71b2a23be6abe5bcb4902be0e7fd6bd1a937864435adc0524de298eb8484d |
| apps/bff/internal/handler/v1/mcp_test.go | 2e779b1ceafdaebffe306795d9b15ef82189ae31e946b1c18aead3ef4c31ed32 |
| apps/bff/internal/handler/v1/query_execution.go | e39bae9e82a9ae795d3b28da8f033333bf8953e706367fe9fe912de587036ad3 |
| docs/lwc-336/README.md | d026908ec680530fbeea69fa9e465b20aacb6b8c187f4645c117cc377806c722 |
| docs/lwc-336/errata.md | 3a843b34901865e6bea30e1d2336cf1b0ae2ea37e1499d96190e0e6fee1e483c |
| docs/lwc-336/evidence/affected-emulator-mismatch.log | 00ff649850235cb741b84999add5ddf60822b61b7f9a788f5207cbdcaa17685c |
| docs/lwc-336/evidence/affected-emulator-8585.log | f1284290eec1e44b32114301bfff61240f9b74e5132b6bb868f66b3de1744638 |
| docs/lwc-336/evidence/deploy-engine-untracked-input.log | e344e02b04da1bf3be60caaa3ff38cabd8b87f4002592dcd0b8ecf1423f489f4 |
| docs/lwc-336/evidence/native/commands.log | 86a40f8f6ba2898f7c5c17cfc46323effc1e2c7cadf124cdb8fbde041d205ae1 |
| docs/lwc-336/evidence/native/native-hermes-results.json | e3fecdd60f57e9f2fd60a8158b69a86efb61f2e87e82586a8a27995be575bd0e |
| docs/lwc-336/evidence/native/native-hermes-results.r1-false-positive.json | 55b6ef508012c014774b4b7bdaa0ad0efa2db875986d8eb67572894a572349b4 |
| docs/lwc-336/harness/probe.py | f21cc850856478d8dc72b735901865b3909130d9f1ec0ea1ce47c76e3972d113 |
| docs/lwc-336/harness/run.sh | 72709ada1e57472fe78cf0df890559a4e15f44321cd03e0a17ece786df850817 |
| docs/lwc-336/source-manifest.md | 9f232345670a317327354d91abec8b697223f8f855bf4dcb19e14eeca245bf06 |

Delta manifest SHA-256: 5ed2bfc4dc99a4c87e36f72c3c7d382230d41a20efde861702c772d88fc83729
