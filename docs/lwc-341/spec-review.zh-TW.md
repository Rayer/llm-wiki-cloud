# LWC-341 規格凍結、實作摘要與測試矩陣

調查日期：2026-11-25。Owner 決議來源：YouTrack item 137/e2390，明確選擇「是，捨棄此候選」。候選處置範圍僅限使用者逐篇選擇保留發布版時的目前候選，不影響其他候選；日後新候選仍可再次出現。對照程式基準：`develop` `33c1a5a7a6139cdd4ab91dbdd859bf7b4f0b9e79`。

## 判讀標記與已決事項

- **已確認事實**：由原 LWC-341 調查附件、其 disposable fixture，或下列 `develop` 程式來源直接支持。
- **Owner 已決**：同一路徑已有發布文章時保留原發布原文，衝突交由使用者逐篇決定；使用者選擇保留發布版時，捨棄目前這一份候選。這只移除所選候選，其他候選不受影響；稍後若再有新候選，仍可重新進入待決。
- **實作內容**：本工作樹新增持久衝突 ledger、唯讀清單／比較 API、逐篇決定 API、介面與隔離測試；這些實作細節不等於 Owner 對展示欄位或文案的決定。
- **待產品確認**：兩版展示欄位、按鈕／摘要文字與 pipeline run 狀態文案；不能當作 Owner 決議。

## 已確認事實

### 原票與 Synto 重現

- 附件報告核對 Synto 0.7.0 與 LWC runtime pin；wheel SHA-256 為 `4bc8dcf…10afa77`，所讀的 Synto 原始碼與 `fdefbb227f657cdfb926b1b19d69adbc947acc80` 相同。重現只在 `/private/tmp/lwc341-bMYiZE` disposable fixture 進行，native 與 LWC adapter 各十案通過；原報告未讀寫 Helios 真實 Wiki，也未核對目前遠端 YouTrack 狀態。
- 原票快照（internal ID `2-982`，來源快照 mtime 2026-10-18）記載 Helios pilot 的 321 篇草稿、12 個衝突，這是歷史票面陳述，不代表目前部署或目前資料狀態。票面所稱裸 `synto approve` 會核准全部草稿不正確：此版本需 `--all` 或明確路徑；明確路徑與 `--all` 都能進入有覆寫問題的 publisher。
- `publish_drafts` 由 `.drafts` 相對路徑構成 `wiki/<相對路徑>`，先寫發布檔，再更新 Synto DB，最後刪草稿（附件報告引用 `compile.py:1738-1833`）。`publish_article` 只有 draft row 存在時才更新 DB；存在同路徑 published row 時會刪舊 row 再晉升 draft row。已重現發布檔存在但沒有 published DB row、DB content hash 落後手動編輯等情形仍被覆寫。
- `verify_drafts` 不是安全的衝突解法：已有 published DB row 時會刪 collision candidate；沒有 row 但目標發布檔已存在時也會刪 candidate（`compile.py:1698-1727`）。本規格的掃描、呈現、逐篇決定都不得呼叫這個刪除候選的流程。
- 同標題不同相對路徑不構成路徑衝突。衝突鍵應以正規化後的專案相對路徑判定，不能以 title 判定。

### LWC 的發布入口與 run 資料

- `develop 33c1a5a7` 的一般 pipeline、Admin trigger 和排程／自動化 intent 最終走同一 worker pipeline；完整 stage 命令是 `run --auto-approve`（`endpoints.go:47, 1280-1415`、`profile_runtime.go:492-493`、`pipeline_intent.go:136`、`scheduled_pipeline.go:303-305`）。本機 manager 亦以相同命令啟動（`localpipeline/manager.go:1158-1163`）；worker 接受 `run` 或 `run --auto-approve`，不接受通用 `approve`（`olw_worker/main.go:1508-1518`）。目前 adapter 未替 publisher 加路徑衝突保護。
- Synto orchestrator 的 auto-approve 只把本次 compile 回傳的 `draft_paths` 傳給 `approve_drafts`，再填 `published`/`held_back`（`pipeline/orchestrator.py:228-239`）；Synto compile 會略過既有待審草稿（附件報告所引 `compile.py:1050` 起）。所以舊草稿不會自然成為本輪新產生項，也不能把 `run --auto-approve` 當作「列出／決定所有待處理草稿」的介面。低 confidence 草稿保留但不是路徑衝突狀態。
- LWC 現有文章讀取不足以比較相同 slug 的發布版與候選版：前端 `getConcepts()` 不傳 `include_drafts`（`frontend/src/lib/api.ts:1556-1558`）；`listConceptsCacheFirst` 命中 cache 時不理會 `includeDrafts`（`endpoints.go:675-690`）；GCS fallback 按 slug 去重，`GetPage` 優先回 published（`gcs/client.go:357-394, 473-489`）。目前沒有逐篇 approve/resolve API，worker 也不開放 Synto `approve`。
- 目前 Project 權限邊界有 `ProjectRead` 與 `ProjectEdit`，`AuthorizeProject` 從已驗證 Project 文件與登入 principal 授權（`profile.go:56-69, 329-351`）；既有受保護讀取範例使用 `ProjectRead`（`content_context_api.go:79`）。新比較讀取與決定寫入應沿用此邊界，不能信任 request body 的 project/user 作為授權來源。

### Cloud 與 Local 的不同發布提交機制

- **Cloud 已確認事實**：GCS project lease 以 create-only 建立並用 generation 條件處理回收（`lease_liveness.go:96-143`）；它負責 writer 序列化。Cloud worker 將工作區輸出上傳為 immutable generation 物件，最後才以舊 manifest 的 `GenerationMatch`（初次建立則 `DoesNotExist`）更新 current manifest（`cloud_publish.go:1137-1203`）。manifest CAS 是整代可見性的提交點，與 lease 是不同機制。CAS 失敗時新物件不成為 current generation；manifest 寫入結果不明時程式會讀回確認。這不是宣稱 SQLite 自身和檔案寫入是單一 DB 交易。
- **Local 已確認事實**：mounted vault 使用 `.olw/lwc-worker-lease.json` 的 `O_EXCL` lease；舊 lease 不因逾時自動搶占（`workspace_publish.go:17-81`）。worker 在 lease 下先回復舊 journal，再 snapshot、跑 workspace、stage 輸出（`main.go:699-770`；stage 包含 `wiki/`、`.synto/state.db`、`.synto/INDEX.json` 和 cache，`workspace_publish.go:191-259`）。發布以 journal 記錄、逐項 rename 至 backup 再換入 stage；未到 committed phase 的錯誤會 rollback，重啟時回復未完成 journal，已 committed journal 只清理 backup/stage（`workspace_publish.go:370-593`）。這是本地檔案樹交換與回復，不是 Cloud manifest CAS。
- 本機 manager 的 Firestore `local_pipeline_locks` 另負責同 user/project pipeline admission（`localpipeline/manager.go:696-740`）；mounted vault 寫入仍以 vault lease 保護。逐篇決定必須加入與對應 pipeline 共用的發布序列：Cloud 用 project GCS lease，Local 用 vault lease；不能只靠 UI 停用按鈕。
- Synto `.synto/INDEX.json` 由已發布 DB rows 建立（已安裝 Synto `indexer.py:148-203`）；LWC worker 再使用 Synto identity authority 產生 `cache/id_map.json`、`cache/concepts.jsonl` 等（`main.go:1818-1847`、`wikiindex.go:301-346, 918-942`）。因此檔案、`state.db`、Synto INDEX 與 LWC derived cache 都必須從同一選定版本重建並一起提交。

## 本次隔離實作摘要

- 新增受 `ProjectRead` 保護的 `GET /concept-conflicts`、`GET /concept-conflicts/compare`，及受 `ProjectEdit` 保護的 `POST /concept-conflicts/resolve`、`GET /concept-conflicts/operations/:operationID`。清單／比較只讀；比較回傳正規相對路徑、兩版 bytes hash、DB row identity、Synto index hash、confidence 與適用門檻。`new` 是 first-seen/run ledger 狀態；`pending` 以 distinct conflict 數計；冷啟動清單可直接顯示既存候選而不發布。
- Worker 對一般 Synto auto-approve 的發布路徑增加目標已存在保護；只略過會碰撞的候選，其他候選照既有流程。逐篇動作只接受一個 conflict、兩個明確 action，先重新授權；同 operation ID 先查同 payload 的耐久收據，不同 payload 回衝突；新 operation 才比對快照與 confidence。不得以 Synto `verify` 清除候選，也不放行低於適用門檻的決定。
- `keep_published` 保留發布原檔，依 Owner 決議只刪目前候選檔與 draft row；若 published row 缺失，只有候選 row 有可信 article/entity identity 時才以發布檔正文修復 row，否則 fail closed。`publish_candidate` 走 Synto publisher 語義；分開記錄候選完整輸入 hash、發布後實際 bytes hash，以及從實際發布正文計算的 Synto `content_hash`。Worker 後續會匯出 Synto INDEX 並重建 LWC cache。
- Cloud 操作在現有 GCS project lease 下從 manifest snapshot 執行，收據跟 state DB 同入 immutable generation，manifest CAS 成功才可見。Local 操作沿用 mounted-vault lease、stage、journal、rename／rollback，收據隨 `.synto/state.db` 同一 stage 提交。Local 最後的 source recheck 只能發現已完成的外部修改；lease 只序列化遵守同一 lease 的合作 writer，不能消除 recheck 後到 rename 的外部編輯競態。
- 以上屬本次工作樹實作，不是 `develop 33c1a5a7` 原有功能；未修改 Synto 套件、未連真實 Wiki，也未部署。

## 規格凍結（Owner 決議與實作行為）

### 1. 跨 run 的待處理狀態

- 以穩定衝突身分記錄 `project + 相對路徑 + published SHA-256 + candidate SHA-256`；另存 first-seen run、last-seen run、狀態及決定結果。版本任一側改變即形成新版本身分，舊項標為 stale，不能沿用舊同意。
- 每次 run 持久保存四份分開的項目／計數：**本輪新發現衝突**（first seen 是本 run）、**目前待決衝突**（重啟後仍由最新已提交狀態列出）、**既存草稿**、**低 confidence holdback**。後兩者可有多種原因，報表不得把它們併稱為衝突或重複計數。使用者在第二次 run 後仍能找到第一輪衝突。
- 冷啟動（尚無衝突 ledger/run history）時，inventory 可唯讀掃描既存 vault：同路徑候選只有達目前 run 的 confidence 門檻才列入可決定的 pending；掃描不發布、不呼叫會刪候選的 `verify`。正常 run 另將合格衝突持久化入 ledger；first-seen 的 `new` 表示「首次偵測」，不表示候選是本輪新生成。既存不碰撞草稿不自動核准；低於適用門檻的候選只列為 held-back、留在 draft，不計入 pending，也不能經 auto-approve 或逐篇決定放行。門檻沿用該 run 的既有設定，預設 `min_confidence=0.0`；非預設設定仍按其既有值。
- 掃描／列出待決項只讀取並比較發布檔、候選檔、Synto DB 與 index；不得以 `verify` 更新狀態或清理候選。未衝突的既存草稿與低 confidence 草稿仍沿用各自狀態。
- `new` 與 `pending` 不是相加的分區：當期新發現通常是 pending 的子集合；run 的 pending 總數以 distinct conflict ID 計，不得用 `new_count + pending_count`。案例 22/25 規定冷啟動第一次偵測合格的既存 A 是 `new=1,pending=1,total_pending=1`；下一輪仍有 A 是 `new=0,pending=1,total_pending=1`。
- 最小跨重啟驗收：run 1 同時發現 A、B，持久紀錄 `new=[A,B]`、`pending=[A,B]`；程序重啟後 run 2 是 `new=[]`、`pending=[A,B]`；只決定 A 後 `pending=[B]`，B 的檔案 bytes、DB row、identity 與狀態完全不變。A 的操作須可重送且不重複刪除／發布。

### 2. 發布保護與逐篇決定

- auto-approve 前依安全正規化的相對路徑分流。若 `wiki/<rel>` 的發布檔存在，即使沒有 published DB row，保留其 bytes/hash、published row（若有）及候選檔／draft row，建立待決衝突；其他無衝突新草稿維持現有發布流程。相同 title、不同 `<rel>` 照常處理。若 DB 有 published row 但目標檔缺失，視為檔案／DB 不一致，先 fail closed 並回報，不可直接覆寫或假設該列等同檔案。
- Cloud worker、Local mounted-vault worker 與新逐篇決定操作使用相同 per-article 規則。決定只作用於一個衝突 ID，request 帶候選完整輸入檔 bytes 的 SHA-256、預期快照 token 及發布版／候選版兩份 identity；操作要在發布 lease 內重讀並比對。檢視後任一版本或快照改變回 `409 stale`，兩邊皆保留，不部分寫入；不得批次決定。
- 發布候選時依目前 Synto `publish_drafts` 語義轉換 candidate，而非把輸入 hash 當成輸出 hash：解析 metadata/body、設定 `status=published`、更新 `updated` 日期、sanitize tags、移除 draft annotations，再 `write_note` 到 target（Synto 0.7.0 `compile.py:1780-1787`）。記錄完整輸入 hash；寫出後重新讀取實際 target bytes 計算輸出檔 hash，並以實際已寫正文計算 Synto `content_hash` 再存 published DB row（`compile.py:1795-1825`）。
- 逐篇動作為「發布此候選版」或「保留發布版」。依 Owner 對 item 137/e2390 的決議，選擇保留發布版時只刪除目前 candidate file 及其 draft DB row，保留發布檔與 published row；只將該 candidate identity 從 pending 移除，其他 candidate 原封不動。若日後產生不同 candidate hash，建立新的 pending identity。按鈕文字、標題、摘要、來源、時間、diff 等展示欄位仍是提案，不是 Owner 已決。

### 3. 同一快照、專案權限與兩版 identity

- 衝突清單／兩版內容讀取先以登入 principal 和授權 Project 執行 `ProjectRead`；逐篇決定以同一 Project 執行 `ProjectEdit`。project/user 由授權 context 得出，不採信 client 自帶身份；跨專案 ID 一律不回內容。
- 一次比較綁定同一專案、相對路徑與兩版 identity。Cloud response 帶 current manifest generation identity。Local response 使用 `legacy` view 並把實際讀得的兩版 bytes/hash、相關 DB rows 與 index hash 放進 pair digest；這能讓決定重驗預期值，但不聲稱檔案系統讀取是原子快照。POST 先驗證 view/pair，再由 worker 於對應 lease 下重讀。
- `published_identity` 至少含專案、`wiki/<rel>`、完整檔案 bytes SHA-256、DB row 的存在/狀態/content_hash 及可得的 Synto article/entity identity；`candidate_identity` 至少含專案、`wiki/.drafts/<rel>`、完整 bytes SHA-256 及 draft row 的存在/狀態/content_hash 與可得 identity。明確標記 row 缺失，不可用 title 代替版本 identity。
- 前端不得由 slug-only 詳情 API 偽造比較結果；一般概念列表、搜尋與詳情仍只反映已發布版。衝突呈現可展示兩版正文與 diff，但實際欄位集合、標籤和文案需產品確認。

### 4. DB 與 index 一致性（技術提案）

`wiki/<rel>` 存在但沒有 published DB row 是已重現的情形，兩種逐篇決定的結果須明文定義：

- **保留發布版**：原發布檔 bytes 不變；若 published row 缺失，提案是建立／修復 row，`content_hash` 對應原檔正文，identity 沿用可信的同路徑概念 identity；重建 `.synto/INDEX.json` 與 LWC concept cache，使已發布索引指向原版本。依 Owner 已決候選處置，只刪目前 candidate file 及其 draft DB row，並將該 identity 移出 pending；不得觸碰其他候選。若找不到可信 published identity，fail closed，候選及原內容都保留。不同 hash 的日後新候選仍可再次入列。
- **發布候選版**：將候選經 Synto 語義轉換後寫至 target；將候選 row 晉升或 upsert 為 `wiki/<rel>` 的 published row，DB body `content_hash` 對應實際發布正文，完整輸入 hash 與轉換後完整輸出 bytes hash 分開記錄。沒有舊 published row 時不可假設要先刪列。重建 `.synto/INDEX.json`、`id_map` 與 concept cache，使它們指向候選版 path、identity 與實際內容；僅在所有結果能同代提交後才完成。
- 兩個動作都驗證 `.synto/state.db`、`.synto/INDEX.json`、`cache/id_map.json`、`cache/concepts.jsonl` 與實際檔案一致；錯誤時依 Cloud 或 Local 的相應提交邊界回到舊快照。候選 disposal 與無 identity 時的修復體驗仍待產品確認。

### 5. Cloud／Local 的決定提交與失敗語意

- **Cloud**：決定操作取得與 pipeline 相同 project GCS lease，從當前 manifest snapshot 讀兩版及 DB/index；檢查 request 的 generation token 和兩份 hashes 後，在私有 workspace 修改單篇及衍生資料。先上傳完整 immutable generation，再以讀到的舊 manifest GCS generation 做 CAS。CAS 成功才是生效；CAS 衝突回 409、保留目前 current generation 與衝突資料。上傳或 index/DB 生成失敗不動 manifest；manifest 結果不明先 read-back，不能盲目重送。
- **Local**：決定操作取得同一 vault `O_EXCL` lease，在 lease 下恢復 journal、校驗 snapshot、stage/驗證 `wiki/`、`.synto/state.db`、`.synto/INDEX.json`、cache 等完整輸出，再透過既有 journal/backup/rename 提交流程替換 worker-owned paths。committed journal 是生效點；commit 前故障 rollback，重啟時未 commit 執行 rollback、已 commit 只清理。外部未持 lease 的編輯仍可能發生，需在 stage/publish 前再次核對兩份 hash；逾時 lease 不得自動搶占。
- 每次決定都先重新授權 `ProjectEdit`，然後進入該後端發布 lease／提交邊界並查耐久收據，再做 stale 檢查。operation ID 以授權 project/principal 為範圍；payload fingerprint 至少含 conflict ID、action、快照 token、published identity、candidate 完整輸入 hash/identity。若已有同 ID 收據且 payload 相同，先回傳收據中的原結果，即使原快照此時已過期；若同 ID payload 不同則拒絕且不改資料。只有找不到收據的新 operation 才檢查 stale，成功結果與耐久收據須在同一提交邊界完成。
- Cloud 收據要放在同一 immutable generation，只有 manifest CAS 成功才可讀為已提交；Local 收據要納入同一次 stage/journal commit。兩條路徑不能在 file/DB 已提交而 receipt 尚未耐久時回成功。
- Local `O_EXCL` vault lease 只保證遵守同一 lease 的合作寫入者（如 pipeline 與逐篇決定）序列化；它不鎖定外部編輯器，也不是對非合作寫入者的 CAS。可在提交前重驗 hash 以發現較早完成的外部修改，但不能宣稱消除了重驗後到 rename 之間的外部競態。不能把 Cloud CAS 說成本地 rename，也不能把 Local journal 說成 Cloud manifest CAS。

## 測試矩陣（完整驗收條件；本輪執行部分 disposable fixture）

每列的 setup/action/assert 是完整驗收條件，不代表本輪每列均已執行。已執行的 Synto disposable fixture 覆蓋普通碰撞保護、新目標、冷啟動 ledger 跨重啟、只處理 A 並保留 B、兩種無 published row 的決定、低 confidence 拒絕、Synto metadata／hash 轉換、收據重送／payload mismatch 與 stale 檢查、既存不碰撞 draft。未直接注入 Cloud object store CAS/read-back 失敗或 Local journal/rename fault 的 LWC operation-specific 情境；下表仍保留這些待驗項。所有 fixture 寫入限制在 temporary vault，不連 provider、不連真實 GCS/Wiki。

| # | Setup + action | 驗收條件 |
|---|---|---|
| 1 | 新候選且發布路徑不存在 | 照常發布；檔案、DB、Synto INDEX、LWC cache 同代指向新版本，draft 清除。 |
| 2 | 同路徑已發布且 DB row 存在 | auto-approve 不改發布 bytes/hash、row 或 index；candidate file/row 成為可見 pending conflict。 |
| 3 | 同路徑發布檔存在但 published DB row 缺失 | 仍偵測衝突；比較資料標 row absent；測試兩個決定後的 DB/index 結果（#14、#15）。 |
| 4 | published row 存在但目標檔缺失 | fail closed 並回報一致性問題；不直接寫入候選或刪 row。 |
| 5 | 發布檔被人工修改、DB hash 舊 | 原 bytes 不變並顯示版本 stale/identity；決定使用舊預期 hash 時回 409。 |
| 6 | 同 title、不同相對路徑 | 不因 title 衝突；新路徑發布，舊路徑保留。 |
| 7 | run 混有普通草稿、衝突、既存草稿與低 confidence | 分類欄位分開；只有合格無衝突草稿發布；既存/低 confidence 不被稱為新衝突，重疊原因不重複計數。 |
| 8 | A、B 第一次 run 後程序重啟再 run | 第一輪 durable `new=[A,B], pending=[A,B]`；第二輪 `new=[]`, `pending=[A,B]`；候選 bytes 仍在，不呼叫 `verify`。 |
| 9 | 重啟後僅決定 A | A 按選定語意完成；B 的 file、DB row、hash、identity、pending 狀態不變。 |
| 10 | 未授權 GET/跨專案讀取 | 無 `ProjectRead` 回拒絕且不洩漏任一版本內容或 identity。 |
| 11 | 有讀權但無寫權決定 | GET 比較可成功；決定操作因沒有 `ProjectEdit` 被拒，檔案/DB/index 不變。 |
| 12 | 同一快照兩版本比較 | response 明列同一 project、canonical relative path、snapshot token、published 與 candidate 各自完整 hash/row identity；title 相同不會混淆版本。 |
| 13 | GET 後任一檔、DB row 或 Cloud manifest 變更 | POST 重讀發現任何預期 identity 不同則 409 stale；兩版與 index 不被局部修改。 |
| 14 | 無 published row；逐篇選擇「保留發布版」 | 原檔 bytes 不變；published row/hash/identity 以候選 row 中可信 identity 修復，Synto INDEX 與 LWC cache 指向原版本；僅所選 candidate file/row 刪除且其 pending identity 消失，其他 candidate bytes/rows/pending 不變。候選 row 缺少可信 identity 時 fail closed，不刪候選；後續不同 candidate hash 可重新入列。 |
| 15 | 無 published row，選擇發布候選版 | row 建立/晉升至 published，hash/identity 對應 candidate；Synto INDEX、LWC cache、檔案皆指向 candidate；無舊 row 可刪仍成功。 |
| 16 | Cloud pipeline 與單篇決定競爭 | 共用 GCS lease 序列化；另以 stale manifest generation 注入 CAS 競態，失敗操作不改 current manifest；不 current 的 immutable 上傳物可安全留存/清理。 |
| 17 | Cloud 上傳/DB-index 生成失敗與 manifest 回應不明 | commit 前失敗時 current manifest 不變；不明結果 read-back 判斷；重送同 operation ID 不重複提交。 |
| 18 | Local pipeline 與單篇決定競爭、lease 已持有 | 共用 vault lease；第二寫入者不重疊。 stale lease fail closed，不自動偷取。 |
| 19 | Local stage、rename、journal commit 前故障 | rollback 後原 `wiki/`、state DB、INDEX、cache 一致；journal 可在重啟時恢復，無混合版本。 |
| 20 | Local committed 後故障／重啟與重送 | committed journal 只清理 backup/stage；已決定版本仍完整，重送相同 operation ID 回原結果，不再次作用於其他候選。 |
| 21 | per-article command/API boundary | 不開放通用 `approve --all`；只可授權單一 conflict ID 和預期兩版 identity 的專用操作。 |
| 22 | **冷啟動**：建立同路徑已發布頁與達 confidence 門檻的既存候選，清空 ledger；呼叫 `inventory`，再以回傳 conflict ID 明確呼叫 `resolve` | inventory 可逐篇呈現既存候選，但掃描前後發布檔、候選、DB、INDEX/cache hash 相同；只有明確決定才可改動。首次 `new=1,pending=1,total_pending=1`，不得宣稱候選由本輪新生成。 |
| 23 | 建立同路徑低 confidence 既存候選；呼叫 `inventory`、`autoApproveRun` 與嘗試 `resolve(action=publish)` | 候選仍在 draft，confidence holdback 有獨立狀態；發布操作拒絕，發布檔／DB／INDEX 不變。 |
| 24 | 建立既存但不碰撞的 draft，冷啟動後呼叫 `inventory` 與沒有新 compile paths 的 `autoApproveRun` | inventory 可列為 draft；自動核准不碰候選 file/row、無新發布、沒有衝突 pending。 |
| 25 | 冷啟動 A 為既存合格衝突、B 是本輪產生衝突；執行 inventory 並讀取本輪與 pending 清單 | `new=[A,B]`（首次偵測），`pending=[A,B]`，distinct total 是 2 而不是 4；下輪兩者仍 pending 時 `new=[]`, total 仍是 2。 |
| 26 | 候選輸入 frontmatter 帶 `status=draft`、舊 `updated`、待清理 tags，正文帶 draft annotation；記錄完整輸入 hash 後明確決定發布候選 | target 依 Synto 規則得到 `status=published`、測試時鐘的日期、sanitized tags、移除 annotation；分別計 input full-file hash 與 output full-file hash；DB `content_hash` 等於從實際 target 重新 parse 後 Synto hash 的 body；`INDEX.json` path/identity 與 concept cache 內容皆由實際 target 重建，不取 draft source 的舊 metadata/body。 |
| 27 | **Cloud 與 Local 各跑一次**：成功提交 action A，之後推進專案快照；重新授權後以相同 ID/相同 payload 重送，再以相同 ID 改 action/hash 重送，再以新 ID 帶舊 token 重送 | 相同 payload 先從同 commit-boundary receipt 回 A 原結果，不因舊 token stale；改 payload 優先回 operation-ID/payload conflict（不是 stale）且無寫入；新 ID 才回 stale。每次請求先重新授權：撤銷 `ProjectEdit` 後同 ID 重送應拒絕，且不讀回或洩露 receipt。 |
| 28 | Local 兩個遵守 vault lease 的 writer 競爭；另於 final hash recheck 前由測試 actor（不取 lease）改檔 | 合作 writer 不重疊，recheck 前已完成的外部改動可回 stale；測試與文件不得聲稱 lease/CAS 保護所有外部 editor，也不把 recheck 後到 rename 的競態列為已防護。 |

## 真實產品取捨、假設與待產品確認

- **事實**：目前工作樹已實作持久待處理清單、兩版比較、受權的單篇操作及 Cloud/Local 各自的既有提交路徑整合；矩陣中的 operation-specific 故障注入與端到端回歸仍待補驗。
- **產品取捨／提案**：一般 pipeline 仍自動發布無衝突候選，path collision 留待人工逐篇決定。這會使 auto-approve run 出現 pending 項；Owner 尚未決定 run 顯示成功、部分完成或待處理，以及錯誤／重試文案。介面現有展示欄位與按鈕文案也不是 Owner 決定。
- **已決**：Owner item 137/e2390 明確選「是，捨棄此候選」；套用範圍只及逐篇選中的當前候選。未來新的候選仍可進入 pending。
- **已知邊界**：Local lease 不保護外部 editor；LWC worker 也不攔截使用者直接執行外部 Synto CLI。手動 CLI 是否納入本票沒有證據支持，不能宣稱已防止該入口改寫。
- **待獨立審查**：Cloud operation-specific CAS／結果不明故障注入、Local journal／rename 故障注入，以及 API 權限與 receipt 回應完整整合測試。

## 證據來源與限制

- 前次調查報告及結果：`/Users/rayer/Library/Application Support/orca/agent-session-attachments/5ce9be05-7b05-4b4c-997d-b5679381509a/REPORT.zh-TW.md`；fixture `/private/tmp/lwc341-bMYiZE`。本輪回歸結果另列於交付摘要；fixture 在新建 `/tmp/lwc341-fixture.*` disposable vault 執行。
- 本輪隔離驗證：`go test ./internal/handler/v1 ./internal/localpipeline ./cmd/olw_worker` 通過；`go test -tags lwc_local_synto_runtime . -run '^TestLocalSyntoPublishGuardUsesDisposableFixtures$' -count=1` 通過；Synto Python disposable fixture 通過；前端 `npm run typecheck` 通過；`npm run lint` 無錯誤，僅有既存 `HomeClient.tsx` unused-variable warning；`npm test` 通過（Node 588/588、component 900/900）。完整測試使用已安裝的 Synto Python 環境與允許本機 `httptest` listener 的隔離 runner。
- 前端 i18n catalog 準備結果 `seed_status=valid`、`zh_tw` fallback 為 0；Simplified Chinese 原有 fallback 維持 1683。Cloud CAS/read-back 與 Local journal fault injection 尚未以 LWC operation-specific fixture 覆蓋，見上表 #16–20、#27–28。
- 原票 description 僅有 `/private/tmp/lwc341-bMYiZE/ticket-original-snapshot.json` 的歷史快照；沒有今日 YouTrack readback。本文件不推論 DEV/Production 部署版本或真實 Helios 當前資料。
- 本次程式基準是 `develop 33c1a5a7a6139cdd4ab91dbdd859bf7b4f0b9e79`；本 worktree 相關 handler、worker、Cloud/Local publish、lease、Project authorization、GCS client 檔案與該基準無差異。附件重現的 LWC commit `8e3e59b9…` 與本次規格 checkout 不同；前者支持 Synto publisher 缺陷，本次 develop source 支持 LWC 入口、權限與發布提交機制。
