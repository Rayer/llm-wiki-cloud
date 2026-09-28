# LWC-351 — Profile V1 工程契約（授權後續草案）

基準：`origin/develop` = 工作樹 HEAD `36212d7e385f25ad3349c48fbe8258fd7c053bca`（2026-09-25）。本文件是下一階段可實作的接口提案，**不是現有 API 或已驗證的模型能力**。Owner 已確認：Profile 屬於 Project，任何具 Project edit 權限的 principal 均可編輯 Profile；目前 Project 單一 owner 規則只是現行 Project 權限政策，並非 Profile 額外的 owner 規則。依 [已接受交付計畫](/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-301-profile-discussion/.hermes/plans/2026-09-25-profile-delivery.md) 與 LWC-351 快照整理；未讀 DEV／Prod，未呼叫模型。334／349 的有界實驗已完成，結論與尚缺的生產品質證據見下文。

## 票務邊界

LWC-351 只交付契約、JSON schema、API fixtures 與票務 reconciliation；不在本票實作產品整合。LWC-209 負責 Project 授權、Profile 持久化與狀態轉移；LWC-211 負責 UI；LWC-352 負責衍生器與 compile hook；LWC-353 負責 Synto adapter 與 recompile 後端 gate；LWC-354 負責 Tag jobs；LWC-355 負責 Query；LWC-356 負責真實 E2E 與 DEV 驗收。

## 現況與最小 Project 權限接點

| 接點 | develop 現況 | Profile 所需變更／界線 |
|---|---|---|
| 身分與路由 | JWT 設 `userID`；`ProjectMiddleware` 只驗 `X-Project-ID` 為安全 path segment，沒有驗證存在或權限（`apps/bff/internal/auth/jwt.go:110-119`, `apps/bff/internal/auth/project.go:9-24`, `apps/bff/cmd/bff/main.go:347-385`）。`GetStore` 直接以兩個 context 字串 scope GCS（`apps/bff/internal/handler/v1/handler.go:151-180`）。 | 抽取可重用 `AuthorizeProject(ctx, authenticatedPrincipal, projectID, action=read|edit) -> authorized {projectID, current owner/storage scope} / not_found / unavailable`。它讀現有 Project metadata，以現行 Project 權限政策決定 action；Profile GET/Job 查詢須 read，PUT/confirm/retry 及服務端更新須 edit 或已驗證的 service claim。無身份 401；不存在與無權一律 404，且回應不含 metadata；metadata/權限無法確認則 default deny、5xx。header 僅驗語法，不能授權。跨使用者 ID 即使語法有效也必須拒絕。未來 Project policy 擴充時修改同一 seam，Profile 不另設 owner policy。 |
| 既有 Project metadata／storage | `InitProject` 建 `projects/{userID}_{projectID}` 並存 `user_id`、`project_id`、`status`（`apps/bff/internal/handler/v1/init_project.go:68-115,117-154,165-224`）；Rename 交易重讀同一文件、用 real-project classifier 比對 owner，無權／不存在皆 404（`apps/bff/internal/handler/v1/project_mutation.go:42-64`, `apps/bff/internal/handler/v1/endpoints.go:206-270,2084-2155`）。`GetStore` 使用 `Scope(userID, projectID)`，GCS 路徑為 `users/{userID}/projects/{projectID}/`（`apps/bff/internal/handler/v1/handler.go:151-180`, `apps/bff/internal/gcs/client.go:751-757`）。 | 延用此 Project metadata identity 與 storage scope，並在 Project 權限 seam 內重用 real-project 驗證與交易時重讀的做法；不可用 idempotency marker、任意 GCS prefix、header 或存入的 Profile owner 字段授權。Profile state 固定放在同一 Project metadata 文件下的 Firestore `projects/{userID}_{projectID}/profile/state`，候選與工作紀錄為該 state 文件下的 `candidates/{candidateID}`、`jobs/{jobID}`；Project metadata 仍是唯一 Project 權限來源。無 registry 重建、全域 ID 改造、實體遷移，LWC-283 非前置。`ready` 是 Project 建立狀態，不是 Profile 工作狀態。現有 `ProjectStatus` 只按複合鍵讀取，尚未提供可重用的 read/edit seam（`apps/bff/internal/handler/v1/init_project.go:117-154`）。 |
| compile commit | Cloud worker 做 source/concept reconcile 才 publish（`apps/bff/cmd/olw_worker/cloud_publish.go:500-525`）；`.lwc/publish/current.json` 以物件 generation CAS 發布 immutable generation（`apps/bff/cmd/olw_worker/cloud_publish.go:823-877`, `apps/bff/internal/generation/manifest.go:17-45`）。失敗或 ambiguous commit 有不同真相（`apps/bff/cmd/olw_worker/DESIGN.md:128-147`）。 | 以已確認 manifest + 成功 receipt 作「整批成功」事件；`errManifestCommitOutcomeUnknown` 必須先 readback，不得推斷成功或失敗。Profile 指標不是此 manifest；編譯 partial failure 不更新詞表，可對已納入舊有效產物的內容按現行詞表打標。 |
| 內容 ID | `cache/id_map.json` 分別映射 source/concept；source reconcile 用 raw path 保留 stable ID（`apps/bff/cmd/olw_worker/source_reconcile.go:32-106`），概念的 Synto identity 僅由明確 `article.entity_id` 採認（`apps/bff/internal/wikiindex/synto_identity.go:14-18,54-103`）；BFF 以 ID map 分型路由（`apps/bff/internal/handler/v1/id_routing.go:39-81`）。 | Tag key = `{kind: source 或 concept, stable_id, content_digest, dictionary_revision}`；source/concept 不互相繼承判斷。刪除、重寫、ID 重映射或 content digest 改變均使舊判斷失效。概念候選輸入固定已確認 generation 的 canonical concept；不能按 slug 猜 ID。 |
| Query | 生產 Query 在 `cache.Search` 取概念正文／metadata 命中，尚無 source 同路徑召回（`apps/bff/internal/query/query.go:169-205`, `apps/bff/internal/cache/cache.go:183-266`）；另有 `search.Index` source/concept 搜尋但不是此條生產 `Execute` 路徑（`apps/bff/internal/search/search.go:128-189`）。`GetStore` 的 `Pin` 讀當下 `current.json`，每次請求固定該 view（`apps/bff/internal/handler/v1/handler.go:151-187`, `apps/bff/internal/gcs/client.go:208-250`）。 | 單次 Query 須 pin active tuple 的 `content_generation`、詞表、tag set 與 Query 規則，再在同一 generation 讀正文與 metadata；必要 Tag 篩選、偏好 Tag 加分，正文召回繼續存在。無 active Tag 版時維持目前 Query。現有 `Pin` 只 pin **最新** GCS view，尚缺按 Profile active generation 讀取與保存舊 manifest 的 seam；不能用它宣稱跨產物一致。 |
| 編寫指引 | 現有 worker 固定 Synto `0.7.0` execution shim，改 `Config.resolve_role` 模型政策（`apps/bff/cmd/olw_worker/synto_execution.py:1-16,39-64`）。此樹內未見 `vault-schema.md`／1500 字元處理。 | adapter 須在每次 compile 起點 pin 已生效 `guidance_revision`，將指引物化為舊 Synto 的 `vault-schema.md` 並驗證實際 consumption。若 adapter 預算超過 1500 字元，拒絕該候選或顯示可見、需重新編輯的失敗；不能靜默截斷。1500 僅 adapter 限制，非 Profile 欄位上限。不得自動全庫重編；BYOK 前 `Recompile all` UI disabled 且 API 拒絕。 |

## 持久版本與 API（延用 Project identity）

單一專案的 Profile state 依現有 Project metadata 文件 `projects/{userID}_{projectID}` 識別，內容仍在既有 `Scope(userID, projectID)` 儲存路徑；環境由部署與既有儲存設定隔離，不採 client header。Profile 的 `revision`、requirements、目前 candidate/Job refs、confirmed ID 與 active 指標寫於 Firestore `projects/{userID}_{projectID}/profile/state`；不可變候選寫在該 state 文件下 `candidates/{candidateID}`，工作紀錄寫在 `jobs/{jobID}`。授權仍查父 Project metadata，不能由子文件推定 Project 存在或權限。使用者 `requirements[]` 是有穩定 item ID、順序、文字、刪除語意的原始要求。`derived` 是另一欄：`dictionary`（Tag／辨識規則）和 `guidance`（後續 compile 指引），記錄 derivation input digest、model/prompt/schema 版本及輸出 revision；不得覆蓋使用者原文。`revision` 單調遞增，server 分配，API 永遠回傳。成功儲存要求可先是 `derivation_status=pending`，背景衍生完成才產生不可變 `candidate_id`，綁 `base_revision`、requirements digest、`content_generation`、候選 dictionary/guidance revision、preview diff、source=`manual|compile_auto`；失敗時是 `derivation_status=failed` 加可重試 error code，並保留原始要求與舊 active。`confirmed_candidate_id` 僅能指向當前且預覽已完成的手動候選。`active` 是單一原子指標 `{candidate_id, content_generation, dictionary_revision, tag_set_revision, query_rule_revision, guidance_revision}`；後續 compile 在啟動時 pin guidance。標記寫入 staging，對 active 要指向的 content generation 所有必需 source/concept item 均有最終判斷後才 CAS 切 `active`。永久歷史／staging 可存其他文件，但讀者只循 `active`。

建議 Project Profile API：`GET /api/v1/projects/:projectID/profile` 回 requirements、revision、derivation status、candidate/preview（若已完成）、active、工作進度與錯誤；`PUT /api/v1/projects/:projectID/profile` JSON 收完整 requirements 與整數 `expected_revision`，僅持久化輸入並回新 revision、`derivation_status=pending`、`scheduled_for=last_save+3m`，**不在 BFF 等模型預覽或做大量打標**。衍生 worker 完成後 GET 才回 candidate/preview；失敗回持久 `failed` 狀態，保存成功不回滾。`POST .../profile/candidates/:id/confirm` JSON 收 `expected_revision`，只確認仍為當前且預覽完成的候選；`POST .../profile/candidates/:id/retry` JSON 收 `expected_revision`，只重排缺漏部分；`GET .../profile/jobs/:id` 讀精確工作狀態。所有寫入只用 JSON `expected_revision`，缺少回 428，版本錯或舊候選回 409 並附最新 revision，未認證 401，無 Project read/edit 權限／不存在 404。API 5xx 只表示本次寫入／讀取未確認，客戶端應重讀。寫入交易經 Project 授權 seam 重讀 real-project metadata、action 權限與 revision，再修改；Job 使用受服務身分驗證的 scoped claim，重讀同一 Project metadata、candidate/revision、input digest，永不接受用戶傳來的 owner ID 直接執行。路由應放在既有 JWT 路由下；URL `:projectID` 與 header 若並存須一致，不能由 header 取代權限驗證（`apps/bff/cmd/bff/main.go:347-385`）。

### JSON wire types（209 與 211 的共同契約）

以下是 JSON Schema 2020-12 的欄位型別；`GET profile` 與成功 PUT/confirm/retry 回同一 `ProfileState`，`GET jobs/:id` 回 `Job`。PUT request 為 `{ "expected_revision": integer, "requirements": Requirement[] }`，confirm/retry request 為 `{ "expected_revision": integer }`。`expected_revision=0` 表示尚無 Profile state；要求清單依陣列順序排序，移除既有 item ID 表示刪除，不另創隱藏刪除旗標。候選／Job ID 由 server 分配；revision refs 是不透明字串，內容與實體 Tag schema 由對應實作票凍結。成功儲存的回應可有 `candidate=null`、`active=null`；所有 JSON 欄位名稱在 API 與 Firestore Profile state 一致。

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$defs": {
    "Requirement": {
      "type": "object", "additionalProperties": false,
      "required": ["id", "text"],
      "properties": {"id": {"type": "string"}, "text": {"type": "string"}}
    },
    "RequirementAccounting": {
      "type": "object", "additionalProperties": false,
      "required": ["id", "disposition", "explanation"],
      "properties": {
        "id": {"type": "string"},
        "disposition": {"enum": ["compile_guidance", "dictionary_or_query", "both", "limitation"]},
        "explanation": {"type": "string"}
      }
    },
    "DerivedRef": {
      "type": "object", "additionalProperties": false,
      "required": ["revision", "input_digest", "model_version", "prompt_version", "schema_version"],
      "properties": {
        "revision": {"type": "string"}, "input_digest": {"type": "string"},
        "model_version": {"type": "string"}, "prompt_version": {"type": "string"},
        "schema_version": {"type": "string"}
      }
    },
    "Candidate": {
      "type": "object", "additionalProperties": false,
      "required": ["candidate_id", "source", "base_revision", "requirements_digest", "content_generation", "dictionary", "guidance", "preview"],
      "properties": {
        "candidate_id": {"type": "string"},
        "source": {"enum": ["manual", "compile_auto"]},
        "base_revision": {"type": "integer", "minimum": 0},
        "requirements_digest": {"type": "string"},
        "content_generation": {"type": "string"},
        "dictionary": {"$ref": "#/$defs/DerivedRef"},
        "guidance": {"$ref": "#/$defs/DerivedRef"},
        "preview": {
          "type": "object", "additionalProperties": false,
          "required": ["dictionary_diff", "guidance_diff", "requirements"],
          "properties": {
            "dictionary_diff": {"type": "string"}, "guidance_diff": {"type": "string"},
            "requirements": {"type": "array", "items": {"$ref": "#/$defs/RequirementAccounting"}}
          }
        }
      }
    },
    "BootstrapGuidance": {
      "type": "object", "additionalProperties": false,
      "required": ["revision", "input_digest", "profile_revision", "status", "model_version", "prompt_version", "schema_version", "confirmed_at", "preview"],
      "properties": {
        "revision": {"type": "string"}, "input_digest": {"type": "string"},
        "profile_revision": {"type": "integer", "minimum": 1},
        "status": {"enum": ["preview_ready", "confirmed"]},
        "model_version": {"type": "string"}, "prompt_version": {"type": "string"},
        "schema_version": {"const": "profile.bootstrap-guidance.v1"},
        "confirmed_at": {"type": ["string", "null"], "format": "date-time"},
        "preview": {
          "type": "object", "additionalProperties": false,
          "required": ["guidance_diff", "requirements"],
          "properties": {
            "guidance_diff": {"type": "string"},
            "requirements": {"type": "array", "items": {"$ref": "#/$defs/RequirementAccounting"}}
          }
        }
      }
    },
    "Job": {
      "type": "object", "additionalProperties": false,
      "required": ["job_id", "candidate_id", "content_generation", "status", "missing_count", "error_code"],
      "properties": {
        "job_id": {"type": "string"}, "candidate_id": {"type": "string"},
        "content_generation": {"type": "string"},
        "status": {"enum": ["scheduled", "running", "retry_wait", "incomplete", "ready", "superseded"]},
        "missing_count": {"type": "integer", "minimum": 0},
        "error_code": {"type": ["string", "null"]}
      }
    },
    "Active": {
      "type": "object", "additionalProperties": false,
      "required": ["candidate_id", "content_generation", "dictionary_revision", "tag_set_revision", "query_rule_revision", "guidance_revision"],
      "properties": {
        "candidate_id": {"type": "string"}, "content_generation": {"type": "string"},
        "dictionary_revision": {"type": "string"}, "tag_set_revision": {"type": "string"},
        "query_rule_revision": {"type": "string"}, "guidance_revision": {"type": "string"}
      }
    },
    "ProfileState": {
      "type": "object", "additionalProperties": false,
      "required": ["project_id", "revision", "requirements", "derivation_status", "scheduled_for", "derivation_error_code", "candidate", "bootstrap_guidance", "confirmed_candidate_id", "active", "job"],
      "properties": {
        "project_id": {"type": "string"}, "revision": {"type": "integer", "minimum": 0},
        "requirements": {"type": "array", "items": {"$ref": "#/$defs/Requirement"}},
        "derivation_status": {"enum": ["pending", "ready", "failed", "superseded", null]},
        "scheduled_for": {"type": ["string", "null"], "format": "date-time"},
        "derivation_error_code": {"type": ["string", "null"]},
        "candidate": {"anyOf": [{"$ref": "#/$defs/Candidate"}, {"type": "null"}]},
        "bootstrap_guidance": {"anyOf": [{"$ref": "#/$defs/BootstrapGuidance"}, {"type": "null"}]},
        "confirmed_candidate_id": {"type": ["string", "null"]},
        "active": {"anyOf": [{"$ref": "#/$defs/Active"}, {"type": "null"}]},
        "job": {"anyOf": [{"$ref": "#/$defs/Job"}, {"type": "null"}]}
      }
    }
  }
}
```

`Candidate` 的兩個 `DerivedRef` 指向另外保存的不可變衍生物，不把未凍結的 Tag payload 塞進 209 API。完成的 preview 以 `requirements[]` 按輸入順序逐項說明意圖歸入 `compile_guidance`、`dictionary_or_query`、`both` 或明確的 `limitation`。空白初態回 `revision=0`、`requirements=[]`、`candidate/bootstrap_guidance/active/job=null`；`derivation_status=null` 表示沒有待衍生輸入。過期 candidate 與已完成 Job 保存在歷史文件；`ProfileState.candidate` 與 `job` 只投影目前版本。GET/PUT 不回 owner ID 或其他 Project metadata。

後端延遲排程承接三分鐘 debounce；保存時間以 server 時間為準，連續保存重設 `scheduled_for`。衍生與打標是獨立背景工作，打標不得早於最後儲存後三分鐘。舊排程送達只記 `superseded`。衍生狀態為 `pending|ready|failed|superseded`；打標工作狀態為 `scheduled|running|retry_wait|incomplete|ready|superseded`；後者 `ready` 表示此候選在指定 content generation 完整且已可啟用，**不是已啟用**。UI 投影另給 `awaiting_confirmation`、`active`、`update_incomplete`、`derive_failed`。每 item 判斷集合須區分 `match`、`no_match`、`unknown`、`not_applicable`；後二者是完成值，`transient_failure`／`permanent_failure` 才是缺漏。暫時失敗有限重試，重試僅處理欠缺項；重試耗盡後 `incomplete` 並保留舊 `active`。

| fixture | 初態與事件 | 必須結果 |
|---|---|---|
| F0 空白 bootstrap | 新 project 沒有 requirements/candidate/active；選填欄位全空 | 原 ingest／compile／Query 照舊；不啟動 Tag Job，無 Tag 過濾。 |
| F1 儲存連擊 | r1 成功儲存並待衍生 C1；兩分鐘後 r2 成功儲存並待衍生 C2；r1 timer 到 | C1／r1 衍生與打標均 `superseded`，r2 從第二次儲存再等三分鐘；不可發布 C1。 |
| F1a 儲存／衍生分離 | PUT(r1) 已提交，模型預覽仍在跑或失敗 | PUT 已回 r1 與 `pending`，不等待模型；GET 後續回 preview/C1 或 `failed`／可重試 error code，原要求與 A0 均保留。 |
| F2 先確認 | C2 已儲存，confirm(C2,r2)，Job 尚未完成 | 確認持久化，active 保持 A0；Job 完整後一次 CAS A0→C2。 |
| F3 先完成 | C2 Job `ready`，尚未確認；之後 confirm(C2,r2) | ready 時 active 保持 A0；確認交易一次 CAS A0→C2。 |
| F4 重複／過期 | 同 Job 重送；C1 工作執行中被 C2 取代 | 相同工作鍵冪等；C1 可完成 staging 但發布 CAS 失敗，active 不變。 |
| F5 部分失敗 | C2 的一項 timeout，其餘完成 | 有限重試缺漏；耗盡為 `incomplete`／`update_incomplete`，A0 仍供 Query，UI 顯示可重試與缺漏數。首次候選時 A0=null，顯示「尚未生效／更新未完成」，沿用舊 Query，不能標成 active 或成功。 |
| F6 有效未知 | `unknown`／`not_applicable` 完成，其他項也完成 | 可以 `ready`；必要 Tag 條件對 unknown 不放行。 |
| F7 手動撞 compile | compile 已確認 G2 且讀 r2 開始推導；使用者先存 r3；compile 推導返回 | `expected_revision=r2` CAS 拒絕，不能蓋 r3；將「G2 尚未 reconcile」以 `(project metadata identity, G2)` 持久去重排程，記錄最新待處理 revision=r3，worker 讀回最新 revision，按 r3 與 G2 重推導／打標；若期間又有 r4，更新同一待處理水位再重排，直至已完成或明示失敗，不能只等待可能永不發生的下一次 compile。 |
| F8 compile 成功 | 整批成功 manifest/receipt 確認且 generation G2；同時無手動編輯 | 以有效 requirements 和 G2 canonical concept 自動產生 **Tags／辨識規則** 候選；不需 confirm，但須 G2 source/concept Tag coverage 全完成才原子啟用含 G2 的 active tuple 並通知顯示 diff；guidance_revision 不改。 |
| F9 compile 部分失敗／結果不明 | batch 部分失敗或 current manifest commit outcome unknown | 不更新詞表；unknown commit 先 readback 定真相。成功納入舊有效 generation 的內容可按當前詞表打標，不發布不確定的新規則。 |
| F10 內容改變 | source/concept 刪除或 digest 改；舊 Tag row 仍在 | 舊 row 不符合 active `kind/id/digest/dictionary_revision`，不可當有效判斷；缺漏重標完成前不得切新 active。 |
| F11 切換失敗 | 全部標記完成，但 active 指標 CAS／儲存失敗 | active 保持 A0，顯示更新未完成／可重試；讀者不能看見混合版本。重試先讀回，避免已提交卻誤報失敗。 |
| F12 詞表不變而內容增加 | G1/A1 已生效，compile 發布 G2 且詞表 revision 不變，G2 新 source/concept 未打標 | 仍需以現行詞表完成 G2 全量／增量 coverage，形成新 tag set，然後 CAS 將 active 的 `content_generation` 與 `tag_set_revision` **一起**切至 G2；不可把 G1 標記套給 G2 新內容。切換前 Query 繼續讀 coherent G1/A1 view；若無法 pin G1，這是整合 blocker，不能在 G2 上套用硬篩選並宣稱一致。 |

手動候選的確認和 Job 完成是交換順序的兩個獨立條件；只有 `current candidate && confirmed && ready && complete coverage of pinned content_generation && active expected revision` 同時成立才切換。compile_auto 候選免確認，但仍須 current、ready 與同一 generation 完整 coverage。新內容即使詞表未變也要重建 coverage，Query 在一次請求中只可讀一個 active tuple；現有 GCS `Pin` 只讀最新 manifest，若 compile 已發布 G2 而 A1 仍指 G1，必須先提供舊 manifest 保留／按 generation pin 或改變 publish gate，否則 F12 不可上線。自動更新不得改編寫指引。通知只在確認切換後送一次（event key = active candidate ID），不能把通知成功作為切換前提。

## 334／349 實驗結論與尚需凍結的 Tag 讀寫格式

334 已完成有界實驗，後續 schema 決策仍需明列 `kind`、stable ID、content digest、dictionary revision、Tag ID、判斷四值、confidence／evidence（若可校準）、判斷器 model/prompt/schema 版本與錯誤分類；並用人工 ground truth/holdout 給出必要 Tag 誤放／誤擋界限。349 已顯示歧義／不確定地名不可靠；確定性行政區代碼解析作 baseline，未解析保留 unknown，Jev 至多作不阻擋的候選／排序提示，**不得獨自作 geo 硬 gate**。Tag metadata 獨立於正文，具體 Tag metadata writer→reader schema 尚未在產品凍結，須按 334 證據再決定；無論落點如何，writer 提供 immutable `tag_set_revision`、逐項完整性與 digest，reader 只循 active 指標讀取並拒絕 version/digest 不符的 row。此為 phase-ready 介面，物理 Tag schema 尚未凍結。334 已有 23 次實際 Jev 呼叫、23/23 raw response 驗證有效與 11/11 離線 runner 測試通過；合成 holdout 的必要地區 Tag 僅 3 正例、1 反例且沒有 production writer→reader 證據，尚不足支持生產硬篩選（`../LWC-334-profile-exec/experiments/lwc334-profile-tagging/REPORT.md`）。349 完成 49 次實際呼叫；16 案例的 48 次評估中，歧義／不確定案例出現 6 次 false accept，已否定 Jev 單獨作二元地理硬 gate（`/Users/rayer/.hermes/profiles/chatgpt/artifacts/profile-wave1/LWC-349/report-final.md`）。

## 建議協調者修訂既有票（僅草稿，未改票）

- **LWC-301**：將已接受的手動確認＋三分鐘 debounce、compile 全成功自動 Tags 調整、partial failure 保留詞表、原子切換、空白 bootstrap、BYOK 前禁 Recompile all、分開 DEV／Prod 驗收列為總契約；209/211/334/349 與後續衍生器／Job／Query／adapter 依賴以計畫順序寫明。
- **LWC-209**：限定為依既有 Project metadata／storage identity 和 Project read/edit 權限 seam 授權、requirements/derivation/candidate/active revision 持久化、JSON `expected_revision` API、確認與 CAS 工作狀態；不得建立 competing Project authority，亦不得將 tagger/Query 實作混入。驗收 F0–F7、F11 與 F1a；F7 的 revision 與去重狀態由 209 提供，compile hook／重排執行由 352 負責。
- **LWC-211**：初建選填且全空可通過；顯示 preview/diff、明確確認、三分鐘等待、失敗／缺漏／重試、active vs candidate、通知；首次候選失敗不可顯示已生效。儲存失敗須保留未提交草稿，跨 Project 導覽不得顯示前一 Project 的 Profile；Mock UI 測試不算真實 API E2E。
- **LWC-334**：23 次 live calls、11/11 離線測試已完成；合成 holdout 仍不足支持 production 硬篩選。後續需補獨立人工審核樣本與 writer→reader schema／production Query 證據，品質不足先回報。
- **LWC-208**：移除初版前置語意；Query history 與 history-derived chips 標為後續，不阻塞 Profile V1。

## LWC-209 implementation clarification (accepted by coordinator)

Derivation can fail before a `Candidate` exists, so the candidate-scoped tagging retry route cannot retry that failure. The BFF adds `POST /api/v1/projects/:projectID/profile/derivation/retry` with `{ "expected_revision": integer }`; it is valid only for the current failed derivation with no current candidate. `expected_revision` is the current requirements revision and does not change for retry scheduling. A duplicate request while that retry attempt is scheduled or running returns the same state and preserves the original `scheduled_for`; after another failure, the same current revision may schedule a new server-issued attempt ID. Stale attempt completions are ignored by checking the current attempt ID, revision, and requirements digest transactionally.

`POST .../candidates/:id/retry` remains scoped to missing tagging work for the current candidate. Compile-auto candidate creation uses a separate internal versioned state transition, pins the existing guidance ref unchanged, and stays outside this BFF API's provider/compile execution path.

When an existing active Profile is cleared to `requirements=[]`, PUT follows the regular three-minute pending derivation path with the empty requirements digest and preserves the old active pointer until the neutral manual candidate is confirmed and its job is ready. An initially empty/no-active Profile remains no-work. The BFF does not fabricate neutral candidate artifacts or coverage: LWC-352 supplies empty-input dictionary/guidance artifacts and LWC-354 validates coverage before the candidate can become ready. Compile reconciliation records `waiting_manual` while derivation, confirmation, or activation is still outstanding, and can resume after the manual candidate becomes active.

## 驗證與剩餘阻塞

本次為文件與替換票草稿，未改產品程式、本次文件修訂未新增模型或 provider 呼叫（334／349 的既有實驗另有 23／49 次收據）。依實際程式核對：`ProjectMiddleware` 僅驗 header 語法（`apps/bff/internal/auth/project.go:9-24`），`InitProject` 寫既有複合鍵 metadata（`apps/bff/internal/handler/v1/init_project.go:165-224`），Rename 在 transaction 重讀並以 real-project classifier 檢驗（`apps/bff/internal/handler/v1/project_mutation.go:42-64`, `apps/bff/internal/handler/v1/endpoints.go:249-270,2084-2155`），`GetStore` 與 GCS 延用 user/project scope（`apps/bff/internal/handler/v1/handler.go:151-180`, `apps/bff/internal/gcs/client.go:751-757`）。本機驗證 JSON 有 209／211／351 三張完整替換描述、14 個 fixture ID 唯一、引用行號在檔案範圍內，未殘留舊版先決語句。整合 gate：可調用且預設拒絕的 Project read/edit 授權 seam（沿用現有 metadata identity 與儲存路徑）；G1/G2 內容與 Tag 同讀的舊 manifest pin／publish gate；334 尚缺生產品質與實體 schema；349 已確立不得以 Jev 獨自作 geo 硬 gate；Synto adapter 對 pinned wheel 的 `vault-schema.md` 實際 consumption／預算驗證。任何一項未確認時，相關實作不得宣稱已完成或硬篩選安全。351 僅凍結契約、schema、API fixtures 與票務 reconciliation；352 執行衍生／compile hook，353 執行 Synto adapter 與 recompile backend gate，354 執行 Tag jobs，355 執行 Query，356 執行 E2E。產品程式驗證在各實作票執行：後端 Go tests／vet／build、前端 lint／typecheck／build；DEV 交付只經專責 `lwc-deployer`，DEV UAT 由使用者驗收。無待協調者回答的產品問題。
