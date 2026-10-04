# LWC-358 deployment engine r2 操作說明

正式入口為 `.github/workflows/deploy-dev.yml`（develop）及 `promote-production.yml`（main）；兩者呼叫同一 `cd.yml`。單一 protected-environment job 保留既有 approval；同 target 的 release/recovery 共用 concurrency group，不取消執行中的任務。此文件是操作介面說明，不代表已獲准執行 live dispatch。

## 正常入口

Actions 表單輸入：

- `components`：非空、無重複的 `auth,bff,worker,exportjob,frontend` 子集。
- `release_tag`：明確的合法 Git tag 名稱；沒有預設版本或 semver 推算。
- `artifact_id`：選填，上一個準備結果的確切 Actions artifact ID，用於失敗續建、選擇擴大或工具變更後重用。
- `dev_artifact_id`：Production 容器必填，成功 DEV release 的結果 artifact ID。容器保留原 build SHA；source-input identity 必須對得上 main 候選。Frontend 按 Production 設定建置。
- `operation`：Deploy Development 預設 `release`；也可明確選 `deploy`、`rollback`、`reactivate`、`tag` 或 `readback`，使用 retained artifact 執行現有 runtime 操作。`diagnose-auth-image` 仍是獨立固定唯讀分支。Production wrapper 不提供 recovery operations 或 force。
- `source_sha`：DEV 普通 `release` 留空時使用目前 workflow `github.sha`。跨 executor 續跑 Stage 1 時，選 `release` 並同時填原 plan 的 deployment source SHA 與 retained `artifact_id`；DEV recovery operations 也填原 plan source。Executor checkout 一律使用目前 reviewed `github.sha`。
- `force`：選填布林值，預設 `false`。僅新 ready DEV attempt 的 `deploy` 可使用；細節見下方。

Workflow 把目前 reviewed `github.sha` 當作 `executor_sha` 來 checkout engine；它與 plan 的 `source`（deployment source SHA）分開。Go `cmd/deploy_config` 產生 normalized config。Profile 透過 Go dependency package 與 embed file、Dockerfile、module files 及明確靜態 inputs 計算 identity；部署工具與無關 package 不使既有容器失效。Frontend identity 使用其 source tree 及目標 public config。Schema 3 plan ID 由穩定 release identity 計算，包含 source、target/config、tag、selected components、輸入 identity 與 DEV provenance，不含 executor SHA；選擇或 release identity 改變必須建立新 plan。

同一 Stage 1 attempt 跨 Actions job 續跑時，保持 source、target/config、release tag、components 選擇與輸入 identity 相同，並把該 attempt 最新的 prepared/ready checkpoint artifact ID 傳回 `artifact_id`。在已註冊 DEV workflow 選 `release`，填原 `source_sha` 和該 `artifact_id`；留空 `source_sha` 的一般 release 仍使用目前 `github.sha`。共用 workflow 用目前 reviewed `github.sha` 當 executor checkout，並以指定 artifact 驗證及續跑原 deployment source。Schema 2 舊 plan 的原 ID/hash 保持不變；已驗證的 schema-less 舊 state 視為 checkpoint schema 1。Schema 3 的 executor SHA 可更新，不改 release plan ID。Engine 驗證 retained plan/hash 與 state 綁定、runtime 尚未開始，再帶入原 build checkpoint；pending/status unknown 只查原 build ID，ID-less submit unknown 停止，SUCCESS 後只重試 digest/receipt。改變 release identity 時 Engine 不搬移舊 build state：只可重用 identity/config 相符且 provider 可讀的已完成 receipts。不同 release identity 若有尚未完成且沒有可用 receipt 的 build handle，會以 `cross-plan-build-checkpoint-unresolved` 停止；要回到原 attempt 的 source、tag、selection/config/input identity 與 artifact 續跑，不能用新 identity 帶走 handle。已明確觀察為 terminal failure 或 pre-create rejection 的 handle，可由之後明確 prepare invocation 重試。完整判斷表見 [accepted Stage 1 checkpoint appendix](deployment-engine-spec-r2-accepted-appendix-build-submit.md#stage-1-checkpoint-resume-boundary)。

### DEV recovery 與明確 force

使用已註冊的 `Deploy Development` workflow 進行 retained-release recovery；它把目前 reviewed `github.sha` 作為 `executor_sha` checkout，再由 `cd.yml` 下載並驗證指定 artifact。`source_sha` 仍須是 retained plan 的原 deployment source SHA。`rollback`、`reactivate`、`deploy`、`tag`、`readback` 都使用現有共享 Actions runtime；不依賴 `recover-deployment.yml` 在 GitHub default branch 的註冊狀態。

正常 `force=false` 行為不變。只在已經有新且完整 `ready` DEV attempt、準備執行該 attempt 的 `deploy` 時，`force=true` 可略過唯一的 `target-has-unresolved-attempt` 擋點，且只有 latest state 成功讀取並屬於不同 plan 時才生效。一般 `release` 可把 force 傳到 Stage 2，但必須先完成正常 Stage 1 與 ready barrier。force 不重播或修改舊 unknown attempt、不自動 rollback、不略過 latest lookup、artifact/receipt/provenance/checkpoint 檢查、snapshot、provider readback 或其他失敗條件。新 attempt 仍先保存當下 provider state，再做 mutation；舊 mutation 可能稍後可見，因此操作者明確承擔重複或競態 mutation 風險。只有實際略過上述擋點時，result 才包含 `force_bypass: target-has-unresolved-attempt`。不支援 Production force，也不支援 force 搭配 `rollback`、`reactivate`、`tag` 或 `readback`；沒有額外確認提示。

Latest checkpoint 必須是可讀的 schema 1 記錄，包含有效的 plan ID、已知 engine status、components map 與非負 sequence；舊的 schema-less 記錄按 schema 1 解讀。缺欄位、未知 status 或不支援的 schema 會以 `latest-checkpoint-invalid` 停止，force 不會略過這項檢查。

Mutating runtime operations use the complete latest-checkpoint lookup and stale-state guard. `readback` remains Actions-owned but is read-only: it polls the retained plan's component receipts/candidates without consulting that mutation guard or latest-checkpoint endpoint, and does not save a checkpoint or issue provider writes. Within one target/plan-prefix/workflow-run/attempt, latest selection uses the greatest checkpoint sequence rather than artifact ID; existing selection between attempts is unchanged.

Stage 1 每完成一個 component 即保存獨立 receipt/checkpoint；build failure 不做 runtime mutation、不 rollback、不標記成功 tag。所有 selected receipts usable 才過 barrier。Stage 2 保存可用的 pre-state，再逐一 deploy/readback，順序 `exportjob → auth → bff → worker → frontend`（只執行 selected）。成功須所有 selected provider sanity 通過，接著寫入 tag；功能 smoke/UAT 不在這個 gate。

Frontend stage 1 只 `vercel pull`、`vercel build` 並封存 `.vercel/output` 與 project identity；檢查輸出的 build-config.json。Stage 2 使用 `deploy --prebuilt`；Production 加上 `--prod --skip-domain`（官方限定 skip-domain 與 prod 搭配），DEV 使用 `--target=preview`，在無 Git metadata 的封存目錄執行，排除 branch-domain 自動指派，保存 deployment ID，再逐一指派已存在的 target aliases；readback 驗證 READY、target、artifact metadata、實際 build-config 與 alias identity。依據 [Vercel build](https://vercel.com/docs/cli/build) 與 [deploy](https://vercel.com/docs/cli/deploy)；使用 pinned CLI 59.11.7。

## 結果與續跑

`plan.json` 固定 attempt；`receipts/<component>.json` 保存 build SHA、source inputs/profile identity、不可變 handle；`state.json` 保存 prior/candidate、component status、sequence；`result.json` 為 bounded redacted breakpoint。Artifact 留存 90 天，限 repository Actions 權限；不存 plaintext credentials 或 raw Service restore specification。各 pending marker 在 mutation 前透過 Actions artifact SDK durable upload；傳輸失敗即停止。

| 狀態 | 意義與下一步 |
| --- | --- |
| ready | 所有 selected artifacts 可用，尚未 runtime 更新 |
| success | 所有 selected sanity 與 tag 完成 |
| incomplete_metadata / tag_failed | Runtime 已成功；只執行 recovery `tag`，不得 rebuild/redeploy |
| failed_rolled_back | 原部署失敗，已驗證還原；保留候選，可明確決定 reactivate |
| unknown | Provider 結果不可確認；使用最新 checkpoint `deploy` 先 reconcile，不能從 ready artifact 盲重送 |
| recovery_failed | 至少一個 rollback failed/unknown；仍是失敗，檢查 checkpoint，不可宣称成功 |

`provider-result-unreadable` 仍是 unknown，並維持 `reconcile-before-replay`。結果可另外含 `causes` 陣列，將 deploy 與 reconcile 階段的既有 bounded 原因分開呈現；它只補充診斷，不改變重播或復原行為。
| rolled_back | 指定的 changed components 已還原；成功 tag 保留，Jobs 的已執行工作與資料不會倒轉 |
| partially_reactivated | 指定 component 已重新啟用，尚未所有 selected components verified |
| stale-checkpoint / stale-ready-artifact-use-latest-checkpoint | 拒絕舊 checkpoint；取得最新 target state artifact 後再判斷 |

### Auth/BFF Cloud Build 狀態

Auth/BFF 的 Stage 1 先用唯讀 `gcloud projects describe PROJECT_ID --format=json --quiet` 驗證設定的 project ID 與權威 project number，再用 `gcloud builds submit --async --region=global` 取得 Cloud Build ID，之後只以 `gcloud builds describe` 查狀態。Cloud Build resource name 可使用 project ID 或該 project 對應的數字 project number；不匹配的數字名稱會被拒絕。`global` 延續原本 Cloud Build 預設 location；每筆建置仍綁定精確的 project ID、location 與 build ID。取得並驗證 ID 後，engine 會先上傳 checkpoint，再開始狀態輪詢。固定 600 秒期限、5 秒間隔；狀態查詢每次最多 30 秒並受整體期限限制。不讀 log，不把 log tail 成敗當作 ready gate；此身份查詢不修改 IAM。

失敗或待 reconcile 的 `result.json` 會在 `builds[component]` 保存安全欄位：`project_id`、`location`、`build_id`、`identity_verified`、狀態與輪詢結果；階段、數字 exit code 與 timeout 類別另列在 `failure_diagnostic`。只有 `SUCCESS` 會進入既有 tag-to-digest 查詢及 immutable digest 驗證；都通過後才寫 component receipt。所有 selected receipts usable 才成為 `ready`。若 SUCCESS 後 digest 查詢失敗，續跑會重用同一 build ID，只重試 digest/receipt 路徑。

- `PENDING` / `QUEUED` / `WORKING` 到達期限：結果為 unknown；續跑只查相同 ID，不重送。
- status API error、timeout 或 `STATUS_UNKNOWN`：保留 ID 與 unknown 狀態，先 reconcile 相同 ID；不得重送。
- `FAILURE`、`INTERNAL_ERROR`、`TIMEOUT`、`CANCELLED`、`EXPIRED`：保存終止狀態及 ID。該次 prepare 不會自動重送；只有另一次明確 prepare invocation 在已觀察到舊 build 終止後，才可開始替代建置。
- Submit 未回傳可驗證 ID：保留 `submitting`/unknown checkpoint 並停止；不得猜測 ID、補造 receipt 或自動重送。
- Project ID/project number 對應、location、resource name 或 ID 不一致：標記 identity 未驗證，停止，不以不匹配的 tuple 查詢狀態。Numeric resource-name segment 必須與唯讀 project identity lookup 的 project number 完全相等。

需要查 log 時，只能使用 result 中已驗證的 build ID。Cloud Logging 有該筆資料時，以 project 限定並套用精確 build ID filter；預設只列時間、severity、logName，不輸出或保存 raw payload：

```sh
gcloud logging read \
  'resource.type="build" AND resource.labels.build_id="BUILD_ID"' \
  --project="PROJECT_ID" --limit=100 \
  --format='table(timestamp,severity,logName)'
```

`LEGACY` logging 不保證有 Cloud Logging entries；查無資料不改變 Cloud Build status，也不表示可改 logging mode、IAM 或 log bucket。完整契約記於 [r2 accepted build-submit appendix](deployment-engine-spec-r2-accepted-appendix-build-submit.md)。

在已註冊 `Deploy Development` 選擇 recovery operation 時，輸入原 `source_sha`、`release_tag`、environment、最新 `artifact_id`、受影響 components 及 operation。使用 artifact 清單中最新 `lwc-state-<target>-…` ID，而非舊 `lwc-ready-…`；workflow 使用目前 reviewed SHA 作 executor checkout，下載後仍驗 retained plan 的 source/target/tag/components。Checkpoint 清單以每頁 100 筆完整分頁至 `total_count`，確認所有頁面後才選最新 artifact；不設 100 筆總量上限。若清單不完整、artifact 到期或 provider 不可讀，停止並回報 TPM，不猜測。`recover-deployment.yml` 不是此 DEV 入口的註冊前提。

### 固定 DEV image 診斷（暫時）

Owner 已接受一個只讀診斷分支，供同一 Development WIF principal 查詢 Auth image metadata。入口是已在 GitHub default branch 註冊的 `Deploy Development` workflow（`.github/workflows/deploy-dev.yml`），明確選取 `operation=diagnose-auth-image`。`operation` 預設為 `release`，所以既有一般 dispatch 不變。先前 `Recover retained deployment` 對 `recover-deployment.yml` 的 dispatch 回 HTTP 404；當時 default-branch workflow list 沒有註冊該檔案，不能拿它當實際入口，也不應藉此更改 default branch。

診斷入口只接受 `develop`、`components=auth`、`release_tag=diagnostic-36992147920`、`artifact_id=diagnostic-no-receipt` 與空的 `dev_artifact_id`。兩個 wrapper 都將 operation/目標鎖定後，呼叫獨立的 reusable workflow `.github/workflows/cd-auth-image-diagnostic.yml`；它只含低權限診斷 job，source SHA 必須等於呼叫端的 `github.sha`。正常 DEV release 與一般 recovery 繼續呼叫 `.github/workflows/cd.yml`。唯讀呼叫端不再載入同時含 contents-write release job 的整份 reusable workflow。診斷不下載或製造 receipt/checkpoint、不進一般 runtime。

Owner 的 standing DEV read-only diagnostic authority 已涵蓋這個固定操作，不需要逐次重複申請同等授權。本輪範圍明確限於本機實作與離線驗證，因此記錄 payload 供 Parent 後續協調，沒有送出 dispatch：

```sh
gh workflow run deploy-dev.yml --ref develop \
  -f operation=diagnose-auth-image \
  -f components=auth \
  -f release_tag=diagnostic-36992147920 \
  -f artifact_id=diagnostic-no-receipt
```

`dev_artifact_id` 使用 wrapper 預設空字串；不傳入 `source_sha`，workflow 固定使用 `github.sha`。GitHub 文件要求 `workflow_dispatch` workflow 存在 default branch，且指定 `--ref` 選擇實際執行的 branch/ref；本次已觀察到 `deploy-dev.yml` 已註冊、`recover-deployment.yml` 未註冊。尚待 Parent publication 後，在這個已註冊 workflow 上確認 GitHub 對新 `operation` choice input 的實際接受與 branch/job 路由；本機不嘗試 live dispatch。[GitHub manual workflow dispatch 文件](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。

固定診斷參數為 `operation=diagnose-auth-image`、`environment=development`、`components=auth`、`release_tag=diagnostic-36992147920`、`artifact_id=diagnostic-no-receipt`。`source_sha` 是所選 `develop` workflow code 的 SHA，等於 `github.sha` 並控制 checkout；被查詢的 Auth image 仍固定為失敗 run 36992147920 的 f6 source tag 與 immutable digest，不會 checkout f6 舊 engine。

這是獨立 read-only job，不讀取或要求 ready/recovery checkpoint，不走 artifact download、runtime、build、deploy、traffic、alias、tag、Firestore/password write 或 rollback。只執行兩次固定 `gcloud artifacts docker images describe`，捕捉並丟棄 provider stdout/stderr，結果限於 operation label、exit code/timeout class、digest-format 布林值、比對布林值與 bounded conclusion；Actions 保存同樣 redacted 的 result artifact。Job 只授予 `contents: read`、`actions: read` 與既有 WIF 所需的 `id-token: write`。這個暫時分支僅在精確新 code SHA 通過兩方 review 和 CI 後使用；診斷結果不等於 root-cause 確認、artifact receipt、release success 或部署授權。

Known failure 只自動逆序復原本次 invocation changed/possibly changed components；不把 release 歷史中已成功、且本次未選取或未更新的 component 納入補償。unknown 先 reconcile，無法確認時停止。獨立 rollback/reactivate 不 build。Auth/BFF prior image 身分由保留 revision 名稱及 immutable image digest 決定：revision spec image 與 status digest 必須相同，revision 必須 Ready；snapshot/readback 也要求單一 container 的 Service image 相同，且 routing 為一個 untagged 100% revision。prior snapshot、observe 或 rollback 不要求 Service template 與 revision 的其他 spec 欄位或 annotations 全等。 Candidate 仍須符合預期 managed env/secrets/service account、image、readiness、revision 身分及 routing。Rollback/reactivation 使用保留 revision 的完整 spec、有效 annotations（含 secret aliases）及原 traffic 作 restore payload，不 build；這份完整 payload 不作額外相等授權 gate。Provider 若拒絕既有 named revision 的 restore，保留 failure/unknown，不改名建立替代 revision，也不重建 artifact。原 resource 不存在、prior image 非 digest、不可表示的 Service traffic（此版只接受一個 untagged 100% revision）、Export Job 現有 config 不符，均在 mutation 前停止。此版不 provision/delete resource、不執行 Job、不回復 persistent writes。

## 本機與離線驗證

Runtime 前提：Python **3.12 以上**（Frontend archive extraction 使用 `tarfile.extractall(filter="data")`），本次明確以 **Python 3.14.6** 驗證。Python 3.9 不支援此介面，不能以 host 的 `python3` 名稱推定相容；執行前確認 `python3 --version`。Actions 的 `python3` 也必須符合此要求。下列本機指令明確選用 `python3.14`。

只準備一個 component 的 CLI 範例（建置／publish 仍需另有授權；本次實作只執行 TEST ONLY fakes）：

```sh
python3.14 deploy/engine/engine.py prepare --directory /tmp/lwc-attempt \
  --environment development --source <full-checked-out-sha> \
  --components worker --tag <explicit-tag>
```

同一 directory 可續建；改變 selection/source/config 使用新 directory 和 `--reuse <retained-directory>`。Production 加上 `--dev <successful-dev-directory> --dev-reference <explicit-artifact-id>`。runtime CLI 只在 Actions 的 JS action 中執行，禁止直接以 agent 身分呼叫 provider。

```sh
python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'
python3.14 scripts/test_engine_workflow.py
python3.14 scripts/test_cd_contract.py
```

`deploy/engine/tests/fake_provider.py` 是 TEST ONLY executable，替代 command boundary，沒有 production fallback。測試使用真實 engine/adapters；不能當成 live provider、IAM、部署或 UAT 證據。旧 `deploy/cd.sh` 與 composite component runtime entrypoints 僅保留供既有 regression；正式 r2 workflow 不呼叫它們，也不呼叫原 Export bootstrap provisioning。

Auth/BFF 的 `revision_config()` 只供 restore payload 擷取完整 retained spec 與有效 annotations；明列的 controller/audit annotations 仍不重播，secret aliases 等有效 annotations 保留。它不參與 prior image 身分 fingerprint 或整份 template equality gate。prior snapshot/readback 以 retained revision identity、Ready、spec/status/service-template image digest 及既有 traffic 條件判斷；candidate readback 另核對 Owner 要求的 managed configuration、image、readiness、identity 與 routing。Service-level annotations 不由 revision restore 改寫。舊 schema-2 plan 與 schema-less checkpoint 由 engine 依相容規則讀取；不要手動改 plan ID、executor/source 或 checkpoint 欄位。
