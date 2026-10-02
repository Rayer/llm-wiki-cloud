# LWC-366 demo-credential-fix-r1 實作證據

## 範圍與狀態

依照 canonical spec `LWC-358-deploy-convergence/docs/lwc-358/demo-credential-fix-spec-r1.md`、review disposition `demo-credential-fix-review-r1.md` 及已凍結合約實作；checkout 基底及本次開始時 HEAD 為 `e9ccf490251ed0262481fca94af7387107be6b5f`。只修改本 LWC-366 checkout。先前 staging 嘗試因共用 `.git/worktrees/.../index.lock` 回報 `Operation not permitted` 而停止；依本次指示不重試 staging/commit/push/PR，也不繞過 sandbox。Parent 處理 publication-boundary disposition。本次只做本地 source/test/evidence 修改，未執行 merge、Actions、部署或 live GCP／IAM／Secret／帳號密碼操作。

程式路徑已接通：前端 Demo 呼叫為零參數，Auth client 對 `/api/v1/auth/demo` 發出無 body 的 POST；Auth 依 `AUTH_DEMO_USER_ID` 查詢既有使用者、要求帳號 active，並沿用 access token claims、持久 refresh session issuer、cookie policy 和既有登入回應。缺 UID、查無帳號、停用帳號或儲存層錯誤時 fail closed。Local synthetic Demo endpoint 獨立使用本地合成身份，不會回退至一般登入。Auth route 使用既有 10 次／分鐘 IP limiter。

Request body 接受空 body、空白 body 或 `{}`；超限回 413（包含讀取時才發現超限的情況），非空物件、畸形或尾隨 JSON 回 400。Auth 設定已經過 loader、schema/render input、環境 component render 與 readback/effective config 傳遞。DEV Auth UID 已依 readonly authority evidence 設為 `e492f6bdaf1735e12b2de96d`；Production UID 仍留空，等待獨立核實，不沿用 DEV 值。

## 證據對照

| 合約項目 | 實作／證據 |
| --- | --- |
| 前端不傳帳密，無一般登入 fallback | `LoginModal.tsx`、`WorkspaceProvider.tsx`、`auth.tsx`；元件、provider、Auth fetch 測試檢查零參數、目標路徑、無 body、storage 與既有導向。 |
| Auth 以設定 UID 選既有 active 使用者 | `demo.go`、`identity.go`、Auth router；測試驗證設定 UID lookup、停用／缺漏／儲存錯誤 fail closed。 |
| 沿用既有 token、耐久 refresh 與 cookie | 測試解碼 access claims 的 role/auth_version、檢查 durable issuer 輸入及 Host cookie；nil issuer 不會退回記憶體 session。 |
| body/error/limiter 契約 | production Firestore-unavailable `/demo` 分支沿用 `DemoLoginHandlerWithRepository` 的既有 body parser，nil repository 對有效空 body／`{}` 回 503；router 測試驗證非空／錯誤／尾隨 JSON 400、已知及未知長度超限 413、無 token/cookie，並保留 10/minute limiter。 |
| Local 與 production authority 分離 | local route 使用 synthetic 本地 handler；production route 綁定 Firestore repository 和設定 UID；Firestore 不可用時 503，無 fallback。 |
| config 由輸入傳至 Auth 部署與 readback | Go deploy config、Python Auth component render/readback 及 development/production contract 測試；UID 不會傳入 BFF。 |
| 一般登入、Google、refresh、logout 未改寫 | 一般 login handler 未修改；後端回歸測試和前端全套測試涵蓋原有流程。 |
| 合成密碼輪替 fixture | 同一個 fixture UID/email 的 Auth 與 BFF 相容登入 router，在替換 fixture hash 後拒絕舊值且允許新值。這是合成資料測試，不是 live DB 身份或 live 密碼輪替證明。 |

本 worker 的 source-only 比對曾確認 base 版本原有兩個 Demo credential literals 都不在 frontend source，且未輸出或記錄 literal 值。之後 Parent 在 LWC-366 frontend source 執行 native `npm run build` 成功，並對該次 `.next/static` 的 39 個 browser JS 檔做 memory-only scan；兩個原有 literals 均 absent、0 artifact hits，未保存 raw bundle。證據摘要在 shared package checkout 的 `LWC-358-deploy-convergence/docs/lwc-358/demo-credential-parent-build-scan.json`；Parent execution 記錄及 process reference 在同目錄 `demo-credential-parent-verification-1c2a.md`。Parent 未修改產品程式碼。以上 build/scan 由 Parent 執行，本 worker 未重跑或冒稱執行。

Parent 的 readonly DEV authority evidence 位於 shared package checkout 同目錄的 `demo-credential-dev-identity-authority-readback.json` 與 `demo-credential-dev-uid-readback.json`：Auth 與 BFF services 的 project/database 設定一致，UID 唯一且符合既有 Demo source identity；email 僅作 memory-only match，未輸出。Password hash 未請求，mutation 為 false。Production 尚未做獨立 authority lookup。

本次 P3 repair 對應 TPM review `LWC-358-deploy-convergence/docs/lwc-358/demo-credential-tpm-review-1c2a.md`：Firestore unavailable 分支之前固定回 503，未經 body validator。現在僅 `/demo` unavailable branch 改接現有 Demo handler 並傳 nil repository；一般 login、其他 unavailable routes、session/auth policy 均未改。Frontend 測試則用 `NEXT_PUBLIC_AUTH_URL` 指定測試 origin，驗證精確 Auth URL、無 body，並驗證 Demo 503 被呈現為錯誤、不呼叫 `/login` 且不寫入 session storage。

## 執行結果

以下 Go 命令在 `apps/bff` 執行，使用 `GOCACHE=/private/tmp/lwc366-demo-go-cache`；前兩項由本 worker 執行：

- `go test -race ./internal/auth -run 'TestDemo|TestSyntheticPasswordRotation|TestLoginHandler|TestRefreshHandler|TestLogout|TestGoogleOAuthConfigRejectsIncompleteProviderConfiguration|TestGoogleOAuthAuthorizationURLUsesS256AndFixedRedirect|TestGoogleOAuthFailureResponseDoesNotExposeSensitiveProviderValues|TestGoogleOAuthFailureEventsIncludeBoundedFlowKind' -count=1`：通過。Firestore emulator 專用持久 session 測試因未設定 emulator 而 skip。
- `go test -race ./internal/config ./cmd/auth ./cmd/deploy_config -count=1`：通過（本 worker 先前執行）。
- P3 repair 後，`go test -race ./cmd/auth -run 'TestAuthDemoRouteUsesLocalFixtureAndUnavailableProductionDoesNotFallBack|TestUnavailableProductionDemoValidatesBodyBeforeReturning503' -count=1`：通過；涵蓋 unavailable router 的 body/status/cookie/rate-limit 回歸。
- DEV UID 更新後，`go test ./internal/config ./cmd/auth ./cmd/deploy_config -count=1`：通過，三個 packages 全數通過。
- `go build ./...`：通過。
- `git diff --check`：通過。
- 本 worker 先前較廣 Go 測試曾因 sandbox 禁止 OIDC 測試 bind `[::1]:0` 失敗。Parent 隨後在相同 LWC-366 source paths 執行完整 `go test -race ./internal/auth ./internal/config ./cmd/auth ./cmd/deploy_config -count=1`，4 packages 全數通過、exit 0，解決該環境缺口。Parent command/process 記錄在 shared package checkout 的 `demo-credential-parent-verification-1c2a.md`。此項由 Parent 執行，本 worker 未重跑。
- Parent 使用本地 Firestore emulator/JRE 執行 `TestDemoLoginIssuesDurableSessionWithFirestoreEmulator`，測試為 PASS 而非 SKIP，並確認相關 refresh-session regressions PASS。Emulator 已停止，未接觸 live GCP。執行來自 Parent 的後續驗證；目前 shared package checkout 的 verification markdown 還留有較早的 emulator-pending 段落，因此該檔對此項為過時狀態，應以 Parent 最新驗證回報為準。測試 source 為 `apps/bff/internal/auth/demo_test.go`。

Frontend 在 `apps/frontend` 執行：

- `npm ci --offline --include=dev`：通過。
- `npm run test:component -- tests/lwc-366-demo-auth.test.tsx tests/lwc-304-modern-login-home.test.tsx tests/lwc-174-workspace-provider.test.tsx`：3 files、21 tests 通過。
- P3 repair 後，`npm run test:component -- tests/lwc-366-demo-auth.test.tsx`：1 file、2 tests 通過，包含 configured Auth origin 與 Demo failure/no-login-fallback。
- P3 repair 後重跑 `npm run typecheck`、`npm run lint`：通過。
- `npm test`：通過，Node 524/524；component 291/291、32 files。
- 本 worker 初次 `npm run build` 因 sandbox DNS `ENOTFOUND` 失敗；Parent 之後對未變動的 frontend source 執行 native build 成功並完成上述 39-file artifact scan。DEV manifest UID 是本次唯一產品設定內容新增，沒有改 frontend source；不重跑已由 Parent 成功覆蓋的 build。

在 repo root 執行：

- DEV UID 更新後重跑 `python3 -m unittest discover -s scripts -p 'test_auth_config_contract.py'`：9 tests 通過。
- DEV UID 更新後重跑 `python3 -m unittest discover -s scripts -p 'test_production_auth_config_contract.py'`：9 tests 通過。
- `python3 -m unittest discover -s apps/bff/scripts -p 'test_auth_promotion_contract.py'`：5 tests 通過。

## 一次性 DEV 密碼輪替工具準備（只限程式、離線測試與審查）

依 Owner 本次授權新增 `apps/bff/cmd/demo_password_rotate/` 一次性 Go operator command。它預設 dry-run；任何替換都必須明確 `--apply` 並由指定 file descriptor 讀取密碼，不接受密碼 flag 或環境變數，不讀取 terminal，也不回顯輸入。目標 preflight 只接受已核實 DEV project/database/UID tuple；模擬器只接受專用 synthetic project/database/user tuple 和明確數字 loopback host，使用明確 insecure gRPC connection（`option.WithGRPCConn`）與 `WithoutAuthentication`，不再以 HTTP endpoint 代替 gRPC transport，也不會回落 cloud。client close 會關閉 SDK 所持 connection；client 建立失敗則立即關閉 connection。空目標、預設資料庫、Production／其他 project、其他 UID、失配的 emulator tuple 或衝突 emulator 環境變數會在 client 建立前拒絕。DEV Firestore client 建立路徑未改。

Firestore read 先要求使用者 active、已有密碼登入 hash、email/canonical email 一致，並透過既有 `IdentityRepository.GetPasswordUserByEmail` 確認 canonical identity 仍解析到同一 UID。替換使用現有 bcrypt DefaultCost。單一 Firestore transaction 重讀完整 user document，要求除 `password_hash` 外所有已知與未知欄位仍符合 baseline，再只更新 `password_hash`；隨後 readback 需要 hash、新密碼比對及 identity 欄位都吻合才回報 replaced。交易錯誤或 readback 不明時只回報 redacted `unknown`，不重試、不當作成功。沒有修改 Auth/session/password-login source；不建立帳號、不變更 AuthVersion、不 revoke session，也不寫 canonical reservation、project 或 session records。

離線驗證已經跑過 command 本身的 fake-store transaction 與既有 password-login handler：dry-run 不寫入；DEV／Production／default DB／空值／其他 UID 在開密碼 FD 與 store 前拒絕；missing、inactive、invalid canonical identity、無 password hash、同密碼 no-op、並行 user drift、注入含敏感字串錯誤、交易後可能已 commit、readback failure 都有測試。合成 replacement 保留同 UID/email/role/AuthVersion/其他 user 欄位、未知欄位、reservation、project、session sidecar；舊密碼在 Auth Host-cookie 與 BFF compatibility-cookie 登入均被拒絕，新密碼沿用成功登入與 cookie contract。Loopback Firestore transaction integration 已加入；後續 emulator transport finding 及重測結果見下。

### Emulator transport finding 與修補

Parent 在 Java 21／Firestore emulator 1.22、`127.0.0.1:8786` 的 synthetic project/database 上執行原 candidate 時，email reservation RPC 30 秒逾時。依 `cloud.google.com/go/firestore@v1.22.0/client.go` 的 SDK 行為及 Parent 的 memory-only 連線對照，HTTP endpoint option 沒有替 gRPC transport 設定 insecure credentials；SDK 只有在 `FIRESTORE_EMULATOR_HOST` 存在時才自動建立 insecure gRPC connection。本 candidate 的 emulator 測試刻意清空該 SDK 環境變數，因此原 factory 連線方式不對。修補只改 emulator branch：對 preflight 已驗證的數字 loopback host 建立 gRPC connection，傳入 `WithGRPCConn`；明確 close lifecycle 綁定 Firestore client。DEV branch 保持原本 `NewClientWithDatabase`，沒有改全域 SDK environment。Parent 的 scratch probe 在同 endpoint 用 explicit loopback gRPC 得到預期 NotFound；這只證明 synthetic transport，不能推論 live DEV client。

修補後本 worker 以 `LWC366_FIRESTORE_EMULATOR_HOST=127.0.0.1:8786` 實際執行了兩個 `TestEmulator`：target-preflight case PASS；交易 case 在準備 synthetic email reservation 時 `context deadline exceeded`（30.00s），exit 1。隨後本 worker shell 的 `curl --max-time 3 http://127.0.0.1:8786/` 得到 connection refused（HTTP 000、curl exit 7）。這表示 endpoint 當時從此 worker shell 不可達；現有證據無法區分 emulator 已停止或 parent／worker shell loopback 隔離，故不把它記成 transport PASS，也不再重試同一 timeout。測試在 seed user 及 password transaction 之前失敗；沒有 password/hash/credential mutation 證據。需由 Parent 在可達該 emulator 的 native context 重跑完整兩個 case。

本次實際執行（都在 `apps/bff`、`GOCACHE=/private/tmp/lwc366-demo-go-cache`）：

- `go test -race ./cmd/demo_password_rotate -count=1 -v`：exit 0；synthetic/unit contracts PASS，2 個 loopback emulator tests 明確 SKIP（`LWC366_FIRESTORE_EMULATOR_HOST is not set`）。
- `LWC366_FIRESTORE_EMULATOR_HOST=127.0.0.1:8786 go test -race ./cmd/demo_password_rotate -run '^TestEmulator' -count=1 -v`：修補後實際執行，交易 case FAIL／30 秒 `context deadline exceeded`；emulator target-preflight case PASS，整體 exit 1。worker shell loopback HTTP connectivity check connection refused，原因未能區分為 emulator 停止或 namespace 隔離。
- `go build -o /private/tmp/lwc366-demo-password-rotate ./cmd/demo_password_rotate`：exit 0，stdout/stderr 空；repo 沒有留下 binary。
- `go vet ./cmd/demo_password_rotate`：exit 0，stdout/stderr 空。
- `go test -race ./internal/auth -run '^TestSyntheticPasswordRotationKeepsUIDAcrossAuthAndBFFCompatibility$' -count=1 -v`：exit 0，Auth 與 BFF 合成 password-login/session-cookie contract PASS。
- repo root `git diff --check`：exit 0；新 Go 檔 `gofmt -l apps/bff/cmd/demo_password_rotate/*.go`：空輸出。

沒有呼叫 operator command 對 DEV 執行 read/write，沒有 live Firestore、provider、credential 或帳號登入操作。emulator transaction test 曾嘗試 synthetic reservation RPC，但 timeout 且 worker shell health/connectivity 未達 endpoint；不得宣稱 emulator transaction PASS。checkpoint 仍是未提交的 `HEAD e9ccf490251ed0262481fca94af7387107be6b5f`；Parent／Supervisor 對修補後 fingerprint 的 independent review 尚未完成；這是待審 candidate，不代表最終 PASS。

### Emulator session fixture 第二次 scoped repair

Parent 對 transport 修補後的 candidate 執行原生 emulator race 測試，來源記錄為 `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc366-rotation-native/emulator-post-fix-focused.txt`，同目錄 `emulator-post-fix-identity.json` 記載當時三個 Go 檔案的精確 SHA-256：`firestore_emulator_test.go` `2b629ce6df1349d825c336ec3c703119c48ea47767ff606b82f14130c45ff17d`、`main.go` `cec2a43b1d29b85e34307bf1bf9df80db608bc82a17b8d2ca107d1e1f1a47a23`、`main_test.go` `a836b00f44060157e14163e9fbd11774eb00f3c5f2c3e9c033bb99f469eb39a3`。測試已通過 reservation 與密碼替換 transaction；Auth、BFF 兩個 synthetic login 子測試都拒絕舊密碼並接受新密碼。之後在 `firestore_emulator_test.go:156` 失敗，因 helper 對同一 user/environment 假設只能有一筆 session。既有 Auth/BFF login issuer 會各建立新的隨機 ID session document；Parent 已確認這是 fixture 假設錯誤，不是 rotation revoke。

此修補只改 `apps/bff/cmd/demo_password_rotate/firestore_emulator_test.go`：基線保存原 session 的 DocumentRef、document path、完整欄位 map 與 Firestore `UpdateTime`；rotation 完成後及 Auth/BFF login 子測試後都直接讀回該 DocumentRef，要求 path、UpdateTime 與全欄位維持原值。其他 login 新建的 session 記錄不會被誤判。新增比較器測試，確認欄位 mutation、刪除、不同 path 的替代文件及相同 path 但 UpdateTime 改變的重建文件都會失敗；baseline 必須有非零 UpdateTime。沒有改動 production issuer、session、account、RBAC 或 revoke 邏輯。

Parent 前次原生 emulator 執行與本 worker 本回合離線結果分開記錄：前次 Parent execution 是原生環境，並非本 worker 執行；本 worker 本回合未重試 loopback。worker shell 先前的 endpoint check 為 connection refused／curl exit 7，故不宣稱修補後 emulator transaction PASS。Parent 需以本文件最新 manifest 所列 source hash 重跑兩個 `TestEmulator` cases，才能更新 emulator 證據。

本回合實際命令（於 `apps/bff`，`GOCACHE=/private/tmp/lwc366-demo-go-cache`）：

- `env -u LWC366_FIRESTORE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST GOCACHE=/private/tmp/lwc366-demo-go-cache go test -race ./cmd/demo_password_rotate -count=1 -v`：exit 0；command unit/race tests 與 baseline session mutation/deletion/replacement coverage PASS。兩個需 emulator 的 cases 因未設定 `LWC366_FIRESTORE_EMULATOR_HOST` 明確 SKIP，本 worker 沒有連線到 Parent emulator。
- `GOCACHE=/private/tmp/lwc366-demo-go-cache go build -o /private/tmp/lwc366-demo-password-rotate ./cmd/demo_password_rotate`：exit 0。
- `GOCACHE=/private/tmp/lwc366-demo-go-cache go vet ./cmd/demo_password_rotate`：exit 0。
- `GOCACHE=/private/tmp/lwc366-demo-go-cache go test -race ./internal/auth -run '^TestSyntheticPasswordRotationKeepsUIDAcrossAuthAndBFFCompatibility$' -count=1 -v`：exit 0；合成 UID 的舊密碼拒絕、新密碼 Auth/BFF 相容登入 PASS。
- `gofmt -l cmd/demo_password_rotate/*.go` 無輸出；repo root `git diff --check` exit 0。

本回合保持 `HEAD e9ccf490251ed0262481fca94af7387107be6b5f`，沒有 stage／commit／push／PR。先前 index.lock `Operation not permitted` 發生後，依指示沒有重試或繞過 sandbox；Parent 負責 publication-boundary disposition。

## 尚未授權／待核實事項與下一個判別檢查

1. **Production UID 尚未核實。** 只有 DEV 得到 Parent readonly authority lookup；Production `auth.demo_user_id` 維持空值。下一個判別檢查是另行授權並唯讀核對 Production Auth/BFF project、database、既有 Demo UID。不得把 DEV UID 搬至 Production。
2. **live replacement 尚未授權執行。** 此 continuation 只修補 emulator transport、離線測試及準備審查；DEV apply 仍不在本次範圍。工具內容經 Parent／Supervisor 對修補後 fingerprint same-content review 之後，依 Owner 已接受的 DEV 順序，還要納入 Engine PR 71 已合併的 develop config 並跑 integration／CI／同 SHA reviews；之後才進入另行確認的 controlled rotation 與舊 pair rejection／Demo flow。Owner 已預授權最終合約所述自動 merge/DEV 順序，但本回合未做 publication 或 deployment；不得把該授權擴張到 Production、tag、provider 或憑證寫入。

合成 password rotation fixture 使用相同 UID/email 並驗證舊值拒絕、新值允許，但它不證明 live Auth/BFF 指向同一個實際資料庫 identity，也不證明實際輪替。DEV readonly lookup 已證明 Auth/BFF project/database 對齊並核對出既有 UID；Parent／Supervisor 最終 same-SHA review、Engine/config integration 和其 CI 仍未完成。本工作持續由同一 worker checkout 負責 reviewer findings 和最小修正。

## 實作檔案內容 manifest

### Parent native verification after final session-fixture repair
Parent actually executed `go test -race ./cmd/demo_password_rotate -count=1 -json` with numeric-loopback synthetic emulator at127.0.0.1:8786. Exit0, 12 top-level cases PASS, zero SKIP/fail; both real emulator transaction/target cases and baseline mutation/deletion/replacement comparator pass. All3 tool Go files unchanged across execution; exact source hashes match the manifest below, including final fixture2edf49c0b4d46bf25fc9defd8d2f59a7cda08caf4563f4b2a196b3b603a89a0f. Actual bounded logs/identity: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc366-rotation-native/session-fixture-repaired-race.jsonl` and `session-fixture-repaired-identity.json`. This supersedes pending Parent emulator verification above, not historical worker SKIPs/failures.

Parent latest Frontend focused4files57tests PASS; actual Next16.2.7 build exit0/15routes, built39browserJSfiles contain neither original Demo credential literal (memory-only comparison, no rawbundle/credentials saved). Current7Frontend source/test hashes match delivered manifest. First scanner wrapper exited1 because it searched original `login` rather than actual `signInAsDemo`; after redacted call-shape lookup scanner succeeded, existing build/test success logs preserved. Evidence `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc366-frontend-latest/` focused-components.txt/build.txt/browser-scan-and-source.json. Parent native cmd/auth/internal/config/cmd/deploy_config race3packages and toolvet PASS.

Parent fetched exact developmerge e031dbab5ee347bd1322d77583e08928c9689386: enginebase→merge changedpaths intersect these25implementationpaths is EMPTY. Do not pre-assume config conflicts; integrate base and verify interfaces/CI. DEV Auth readonly current active revision llm-wiki-auth-dev-00029-rkw, one untagged100percenttraffic, prior service/revision image pinned to same digest; not a deployment/restore acceptance. Local Vercel token absent; Development Actions secret names present, no secret values read. Vercel Git/alias/provider authority still requires actual Actions/live readback, not inferred from metadata names.

Current DEV execution order is superseded by Owner-approved shared SSOT appendix `docs/lwc-358/demo-credential-dev-execution-order-owner-direction.md` in TPM package: allselectedAuth/Frontendready→Actionsdeployboth→controlledpasswordrotation→oldpairrejection/Demoflow. Original frozenr1 bytes retained; no globalmergepermission, unrelated/Production/credential authority inferred. Same-final-PR-SHA TPM/Supervisor reviews and CI remain pending; this native evidence is not finalPR/livePASS.

原 candidate 的 `f0e0afc119d701347d9f6ba8543feca682949cd9405bc6e75a85f56f6a7e55a2` 是 worker 定義的 LF/TAB content fingerprint，並非先前 canonical JSON fingerprint；transport remediation 後的 `484a3fa90df7f1a12f67a9772ac68aa96c249c84b6457077250aae3d66070a6a` 已被本次 session fixture remediation supersede。最新 Implementation-scope fingerprint（依相對 path 排序後對 `path<TAB>mode<TAB>SHA-256` 各列以 LF 分隔，不加尾端 LF 計算）為 `8b8cd4f3232b7edecccdcbfe43cd239d5a7ec026512d6d92b7db7f5a60f8ae4f`。Git blob OID 由目前檔案內容計算；SHA-256 為原始位元組摘要；mode 是 filesystem permission mode（八進位）。列出本次 25 個程式、設定及測試實作檔案，含本 extension 新增的 3 個 command/test files。`implementation-evidence-r1.md` 是證據文件，故不納入自身內容 manifest；下列內容未 stage 或 commit。

| 檔案 | Git blob OID | SHA-256 | mode |
| --- | --- | --- | --- |
| `apps/bff/cmd/auth/main.go` | `6e625854ccff569e24fa2e4fa00769c25a6209fd` | `38eb38969f980778ae263fdd644c2848a4bf631007df10dd747bdfd5408c5428` | `0644` |
| `apps/bff/cmd/auth/router_test.go` | `a024a09245cf1093245ed99c0eeb5a6cd42448b4` | `c2821bed3acc77a1c798d93baf19c4ded58d76d4aecc19377d6e6e5c56dc74af` | `0644` |
| `apps/bff/cmd/demo_password_rotate/firestore_emulator_test.go` | `8df780cbd35ddd080ee2f29df890c1a2d0085531` | `2edf49c0b4d46bf25fc9defd8d2f59a7cda08caf4563f4b2a196b3b603a89a0f` | `0644` |
| `apps/bff/cmd/demo_password_rotate/main.go` | `4f6693ffe68c2c3bf915b076a910915dbfc758e8` | `cec2a43b1d29b85e34307bf1bf9df80db608bc82a17b8d2ca107d1e1f1a47a23` | `0644` |
| `apps/bff/cmd/demo_password_rotate/main_test.go` | `aa32f8470bb87a9d2d39d65f4329e89b5b77f9bf` | `a836b00f44060157e14163e9fbd11774eb00f3c5f2c3e9c033bb99f469eb39a3` | `0644` |
| `apps/bff/cmd/deploy_config/main.go` | `936b280eea41db898927b32e4381d92fd6f7a977` | `6b7b41832b0c98ae0a0b06b1ecb3617318173f59169afcd597c014bf0b82c0e2` | `0644` |
| `apps/bff/cmd/deploy_config/main_test.go` | `528ffbcd8eba30e39fd0489f40fa8e4650146d54` | `a936027835baf91319fba8593afd9d44aef3de3f081d05717cfc3500cad6f021` | `0644` |
| `apps/bff/internal/auth/identity.go` | `5f3159daf74bbf1770d19ab7a674d75d180d311e` | `1f0d00636028c2eb3cde6d016ac2c6eef36d5fbc439a278a4b91b3ca868c9749` | `0644` |
| `apps/bff/internal/auth/local.go` | `380c1691a58c53671d04d98317060755bd714f8a` | `86ec02037f183b8d56ba4a9c4790bfd1797ec675f530ae815c5040f9202877e2` | `0644` |
| `apps/bff/internal/auth/demo.go` | `ce0559ae86b8c3719693657de8c0c285dbc53cde` | `492d098b2217ef66caca47b8590fd96b5f688c0a4244e45e2c77bad3bb647a1c` | `0644` |
| `apps/bff/internal/auth/demo_test.go` | `9398a8c2c231d851af82b3e793bd483099145a77` | `932512de0531b6b8ffd550ef2b98c62325720d2217175f0d79acc2b520e7d289` | `0644` |
| `apps/bff/internal/config/config.go` | `d3d3075c06f5be5af18149034602533b22d070af` | `f3dc6861621615484634b8c70b4e784526d609262807f1987b8bb7addd86168f` | `0644` |
| `apps/bff/internal/config/config_test.go` | `bf7d519280a46888eac4d096e5f7af16251e7ecf` | `b4bc0f5a19332e1d3978baa1f77707d52e6200064c2911fa48d7e643025e77c7` | `0644` |
| `apps/frontend/src/components/LoginModal.tsx` | `0b6b570967a1715d7648125a9d17d3fdd164be58` | `d0221a15e546faafa43011f37b01a8d16a3b00095bf8e324f5af30cbe6fd9a4e` | `0644` |
| `apps/frontend/src/components/WorkspaceProvider.tsx` | `fd9326ca2050cf02924cff8ba305eb7675a78478` | `dc4857f2a65e637966032a3cbcf97c01957663388dec591e4c90de878f72cb36` | `0644` |
| `apps/frontend/src/lib/auth.tsx` | `1a113a4a4bceb74593805a9a0ad7a72cf739e099` | `4616cce2ad26d8fe1cad604c2944799b6cb2f932a794b10e565d00092455b6fb` | `0644` |
| `apps/frontend/tests/lwc-174-workspace-provider.test.tsx` | `2783fd119b161e8401c2863dce94f03ac92f1875` | `4fb7a52bcaec657f7e208635530fef7d177880c10fe072a56a290919aba0a30a` | `0644` |
| `apps/frontend/tests/lwc-221-auth-redirect-behavior.test.tsx` | `e7eb96666f4b7c7bba5608fd88d93710071bb374` | `1797ec985a5bc4ac3268acb6293b368ec2740042bf644bc8e25be5ce2dbcd922` | `0644` |
| `apps/frontend/tests/lwc-304-modern-login-home.test.tsx` | `f90bb6541ad3aed688fbab8a897b3abb1a42590e` | `aba5e3d63557972b5d9beecdd3b54cc095c0cb6e91f9009e7fc08bd77a7e66c4` | `0644` |
| `apps/frontend/tests/lwc-366-demo-auth.test.tsx` | `33fcf722e940daa058e77a15bc26df7dfc1c9cd2` | `09569a87a4fc7b978d4ddb080a156fe8e74c1cd17240ce6dd367be322f980b7d` | `0644` |
| `deploy/components/auth_config.py` | `ef57074809ea3925f89e8cb46aecbe312215bde9` | `841d5a416e49894405960f5b04eb33809d81e9f7f59ee55d7068a4ccdfe1e16c` | `0644` |
| `deploy/environments/development.yaml` | `df0135e67b02d23476efb2939821486029e45870` | `18dc84c3e82ee260903343f53ed9b2ca2964c0974b6f5e014df73c2644ce0a6e` | `0644` |
| `deploy/environments/production.yaml` | `84117ccf9bf593345bc44321cfbf7dca858046ed` | `fe564a95b5a0346a5b434e80f174439e9527c83b589df4c97353f53436b993af` | `0644` |
| `scripts/test_auth_config_contract.py` | `98cb676586425458ac3b56792f23bab1f49d0ca5` | `ef95dcd878b1fde5bf8d27e03f375615c02889b882a3aa96d4ab4399547ccc4d` | `0644` |
| `scripts/test_production_auth_config_contract.py` | `3750cfa508d784f0b681e5c9e36d49c5f3890d7a` | `833b1cbddac37a3cf858c32c7d6297c50b79af5d8e7ea5f78c6b4a38531af714` | `0644` |

## 整合與 bounded publication 準備（未執行）

目前 intended stage list 為上方 25 個 implementation paths，加上本報告 `docs/lwc-366/implementation-evidence-r1.md` 和本次摘要 `LWC-366-codex-session-fixture-remediation-report.md`，共 27 個精確路徑；不包含其他 root Codex 報告、cache、binary 或 secret。此清單只供後續 bounded publication review，不代表已 stage；舊 `LWC-366-codex-rotation-tool-report.md` 不納入本次清單。Engine PR 71 已 merge 到 develop（`e031dbab5ee347bd1322d77583e08928c9689386`），但本 checkout 仍在原 base；Auth config／engine overlap integration 與整合後測試尚待同 worker 在後續 bounded integration 完成，之後需重建 manifest 並做最終 SHA review/CI。此次沒有執行任何 publication、merge 或 deployment。
