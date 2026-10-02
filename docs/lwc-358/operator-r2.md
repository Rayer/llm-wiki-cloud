# LWC-358 deployment engine r2 操作說明

正式入口為 `.github/workflows/deploy-dev.yml`（develop）及 `promote-production.yml`（main）；兩者呼叫同一 `cd.yml`。單一 protected-environment job 保留既有 approval；同 target 的 release/recovery 共用 concurrency group，不取消執行中的任務。此文件是操作介面說明，不代表已獲准執行 live dispatch。

## 正常入口

Actions 表單輸入：

- `components`：非空、無重複的 `auth,bff,worker,exportjob,frontend` 子集。
- `release_tag`：明確的合法 Git tag 名稱；沒有預設版本或 semver 推算。
- `artifact_id`：選填，上一個準備結果的確切 Actions artifact ID，用於失敗續建、選擇擴大或工具變更後重用。
- `dev_artifact_id`：Production 容器必填，成功 DEV release 的結果 artifact ID。容器保留原 build SHA；source-input identity 必須對得上 main 候選。Frontend 按 Production 設定建置。
- `operation`：Deploy Development 新增的選項，預設 `release`，保持原正常發佈路徑；只有明確選取 `diagnose-auth-image` 才會進入固定唯讀診斷。Production wrapper 不提供這個 operation。

Workflow 在 admission 固定 `github.sha`，Go `cmd/deploy_config` 產生 normalized config。Profile 透過 Go dependency package 與 embed file、Dockerfile、module files 及明確靜態 inputs 計算 identity；部署工具與無關 package 不使既有容器失效。Frontend identity 使用其 source tree 及目標 public config。Plan 的內容 hash 是 attempt ID；選擇改變必須建立新 plan。

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
| rolled_back | 指定的 changed components 已還原；成功 tag 保留，Jobs 的已執行工作與資料不會倒轉 |
| partially_reactivated | 指定 component 已重新啟用，尚未所有 selected components verified |
| stale-checkpoint / stale-ready-artifact-use-latest-checkpoint | 拒絕舊 checkpoint；取得最新 target state artifact 後再判斷 |

`Recover retained deployment` 輸入原 `source_sha`、`release_tag`、target environment、最新 `artifact_id`、受影響 components 及 `operation` (`rollback`, `reactivate`, `deploy`, `tag`, `readback`)。使用 artifact 清單中最新 `lwc-state-<target>-…` ID，而非舊 `lwc-ready-…`。Checkpoint 查詢一次讀最新 100 筆；若無法在界限內確認最新 state、artifact 到期或 provider 不可讀，停止並回报 TPM，不猜測。

### 固定 DEV image 診斷（暫時）

Owner 已接受一個只讀診斷分支，供同一 Development WIF principal 查詢 Auth image metadata。入口是已在 GitHub default branch 註冊的 `Deploy Development` workflow（`.github/workflows/deploy-dev.yml`），明確選取 `operation=diagnose-auth-image`。`operation` 預設為 `release`，所以既有一般 dispatch 不變。先前 `Recover retained deployment` 對 `recover-deployment.yml` 的 dispatch 回 HTTP 404；當時 default-branch workflow list 沒有註冊該檔案，不能拿它當實際入口，也不應藉此更改 default branch。

診斷入口只接受 `develop`、`components=auth`、`release_tag=diagnostic-36992147920`、`artifact_id=diagnostic-no-receipt` 與空的 `dev_artifact_id`。Wrapper 將 `source_sha` 固定為本次 dispatch 的 `github.sha`，並把 component、release tag、artifact sentinel 固定映射至 reusable `cd.yml`。診斷 job 和 shared workflow job 都有獨立條件；release job 只接受 `operation=release`。診斷不下載或製造 receipt/checkpoint、不進一般 runtime。

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

Known failure 只自動逆序復原本次 invocation changed/possibly changed components；不把 release 歷史中已成功、且本次未選取或未更新的 component 納入補償。unknown 先 reconcile，無法確認時停止。獨立 rollback/reactivate 不 build。Service candidate-reactivation 使用保留 revision 的 exact template spec、retained effective annotations（含 secret aliases）與同一 revision 名稱／routing；基本 sanity 同時驗證 service template 與 active revision 一致，才可供下一次 release snapshot 使用。Provider 若拒絕既有 named revision 的 template restore，保留 failure/unknown，不改名建立替代 revision，也不重建 artifact。原 resource 不存在、prior image 非 digest、不可表示的 Service traffic（此版只接受一個 untagged 100% revision）、Export Job 現有 config 不符，均在 mutation 前停止。此版不 provision/delete resource、不執行 Job、不回復 persistent writes。

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

Retained Service 設定的 snapshot、restore、observe 與 fingerprint 共用相同 spec + annotations 投影。僅排除明列的 controller／audit annotations（operation-id、ingress-status、urls、creator、lastModifier、routes、user-image、client-name/version），不排除整個 namespace；secret aliases、autoscaling、網路設定及其他 annotations 均保留。Service-level annotations 不由此 revision restore 改寫。設定不一致在 snapshot 前停止；readback 不一致不能回報成功。舊 engine checkpoint 的 fingerprint 不應手動轉換或繞過 engine-content guard。
