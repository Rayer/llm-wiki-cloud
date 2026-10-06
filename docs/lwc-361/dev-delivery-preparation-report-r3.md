# LWC-361 DEV delivery preparation — r3

## 狀態與來源身份

本報告記錄原 Implementer 完成本機 worker runtime、repository guide 與離線交付資格準備，以及後續 root gate 對帳、父方同來源 Vitest replay 與交付狀態。這是交付候選的 code/build/test 證據，不代表完整 LWC-361 acceptance、同 SHA review、merge 或 DEV deployment 已完成。

- Worktree：/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-cloud-discussion
- Branch：Rayer/LWC-361-local-cloud-discussion
- 起始 HEAD：2180d48274aec48b89b6ccf35eea5e2b08ff1e25
- Runtime：依 Dispatch 指定延續 GPT-6-Luna xhigh；未切換 model 或 effort。
- 最終 source snapshot SHA-256：`83d5322c1a105ad09be0cccc689fddb26e19e0ad121ca0f155682bea40f221c9`，涵蓋 1,126 個 source/worktree paths；排除 docs/lwc-361/、__pycache__ 和 *.pyc。計算方式為排序 `git ls-files --cached --others --exclude-standard -z` 路徑，逐路徑以 8-byte big-endian 路徑長度、路徑 bytes、8-byte big-endian 內容長度、內容 bytes 更新 SHA-256；刪除檔以 `DELETED` 表示，symlink 以 `SYMLINK\0` 加 link target 表示。本報告更新前的 PR head 為 `3ab348e51343b587eac91e78f3e344d032a372d6`；本輪加入 `apps/bff/docs/LOCAL_DEV.md` 的 Python prerequisite/troubleshooting 文件修正後，新的出版 head 以 PR readback 與 coordinator 狀態訊息為準。
- 舊 Supervisor digest bd7c9c1106b778b8ee8da76574a48f8ff46290b67763250eabb9bf0f2e381c3f 不涵蓋本次變更，沒有移用其 review verdict。此候選仍需 parent／Supervisor 對出版後的同一 SHA 做 review；本報告不宣稱 review PASS。

## 本輪變更

- apps/bff/Makefile 依 worker Dockerfile 的同一公開 wheel URL、Synto 0.7.0 與 SHA-256 建立 worktree 私有 Python venv，真正安裝 wheel，驗證 import synto、版本及 synto --version。不修改全域 Python。
- scripts/local-cloud-env.sh 把該 venv 的 bin/ 放在 local child PATH 前端並匯出 interpreter 路徑；現有 BFF native worker manager 傳遞環境，worker 的正式 python3 adapter 因而用同一 venv。local-start、BFF debugger 及 root make bootstrap 均準備此 runtime，沒有新增 app preflight 或平台。
- 新增 build-tagged TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider，確認 worker 的 python3 解析到 worktree venv；以真實 Synto adapter 跑 --version 為正向、無效 CLI 命令拒絕為負向，兩者都不載入 provider key、不呼叫模型。
- scripts/local-vertical-smoke.sh 統一在 provider key names 全部 unset 的環境跑具名 loopback/auth/scope/Synto regressions；更新 root smoke contract test，確認退休的假登入與 destructive seed 不會回來。
- 補完 apps/bff/docs/LOCAL_DEV.md，說明 ADC、worktree venv、Synto 版本與 provider key names（不含值）、正式登入、ports/Host/cookie、scope/資料保留與清理、啟停、debugger 和成本界線；同步更新 root/BFF README 的 bootstrap 指引。
- 修正既有 public-config 前端測試，讓預設 demo_enabled=false 的回應契約與本票 fail-closed capability 一致；Make local-config regression 明確指定測試 ports，不依賴 parent 當前服務 ports。

實際安裝 readback：Synto 0.7.0，Python 3.14.6，interpreter 為 worktree Git metadata 的 lwc361-local-cloud/python/bin/python3。本機只有 Python 3.14 可用；wheel 與 import/CLI/worker adapter 正反向 regression 均通過。DEV worker image 仍使用原 Dockerfile 的 Python 3.12，沒有更動。

### 新鮮 worktree guide walkthrough 發現

Parent 的第二 QA worktree `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-acceptance` 依 guide 執行 `make bootstrap` 時，PATH 選到 `/usr/bin/python3` 3.9.6，venv 也因此建立為 3.9.6；pinned Synto dependency `mcp>=1.10` 沒有相容 distribution。Parent 確認這不是 wheel URL 下載失敗或 provider 問題，並在 QA worktree 清除未完成 venv、改用已安裝的 Homebrew Python 3.14.6 繼續 walkthrough；通知來源為本 Dispatch inbox `msg_55699bd814b1`。該通知未附 3.14.6 walkthrough 的最後成功輸出路徑，故此報告只將 3.9.6 問題列為已觀察的 guide gap，不宣稱 fresh-worktree walkthrough 已完成。

本輪僅修正 `apps/bff/docs/LOCAL_DEV.md`：明列 Python 3.12+ prerequisite、`python3` 的 PATH 選擇、3.9.6 常見 `No matching distribution found for mcp>=1.10` 錯誤與只清理該 worktree Synto venv 的重試方式。這是文件修正；沒有改 Make/runtime/source 或擴增產品 gate。Parent 仍需用修正後 guide 完成 fresh-worktree walkthrough 並讀回結果。

## 驗證紀錄

所有測試命令均清除 LLM_API_KEY、DEEPSEEK_API_KEY、SYNTO_API_KEY、OPENAI_API_KEY、ANTHROPIC_API_KEY、GEMINI_API_KEY、TYPESAFE_API_KEY、TYPESAFE_JEV_API_KEY 及 LWC331_TEST_API_KEY。完整 root gate 另外 unset emulator hosts，避免非具名測試連到 loopback emulator；loopback integration 另以明確 endpoint 單獨執行。log 存於 /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-dev-preparation-r3/。

| 工作目錄 | 命令 | Exit / 結果 |
|---|---|---|
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY make -C apps/bff local-synto-runtime</code> | 0；實際安裝後 import/version/CLI 驗證成功。 |
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY make -C apps/bff local-synto-runtime-test</code> | 0；真 worker adapter positive/negative regression PASS。原始輸出 synto-runtime-test.log。 |
| apps/frontend | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY node --experimental-strip-types --test tests/lwc-278-announcement-api.test.mjs</code> | 0；3/3，預設 Demo capability false、公告資料與 fail-safe fallback。原始輸出 frontend-announcement-retry.log。 |
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY python3 -m unittest apps.bff.scripts.test_local_dev_makefile</code> | 0；12/12 Make/runtime/supervisor contracts。 |
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY python3 -m unittest scripts.test_local_vertical_smoke -v</code> | 0；2/2 retired-path/provider-isolation smoke contracts。 |
| repo root（historical, pre test-only edits） | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u STORAGE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST make verify BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000</code> | 0；`make-verify-final2.log` 當時的完整 gate 通過，但該來源沒有本輪兩個 test-only edits；不當成最終 source snapshot 的 gate verdict。 |
| apps/bff | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY STORAGE_EMULATOR_HOST=http://127.0.0.1:14443 FIRESTORE_EMULATOR_HOST=127.0.0.1:18885 GOPROXY=off GOSUMDB=off go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1 -race -v</code> | 0；BFF HTTP trigger/status 與真 native subprocess fixture、loopback GCS/Firestore publisher/readback、失敗路徑測試 PASS。原始輸出 loopback-pipeline-test.log。 |
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u STORAGE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST bash scripts/local-vertical-smoke.sh</code> | 0；具名 auth/scope/supervisor/Make/Synto smoke；provider keys 和 emulator hosts 均未帶入。原始輸出 smoke-targeted-retry.log。 |

Loopback integration 的讀回包含 metadata digest 相等、trigger 202、重入 409、status 200、fixture 成功與失敗、scope cleanup/readback 和另一 scope sentinel 保留等測試斷言。這是具名 BFF/emulator fixture evidence；不是 browser page endpoint 成功、正式 LLM 編譯或 live target acceptance。

各輪 gate 失敗均保留原始輸出，不以舊 PASS 覆蓋新結果：早期 `make-verify-final2.log` 曾 exit 0，但其來源早於下述兩個 test-only edits，不能當成目前 snapshot 的 full gate。`make-verify-delivery.log` exit 2，Go worker 測試把固定的 `execution-secret` 測試 ID 子字串誤當 credential；把拒絕 sentinel 精確為真實 `api-secret` 後，具名 `TestCloudCanceledAndExpiredFailuresStillRecordAllArtifacts` 在 `-race` exit 0。`make-verify-final-r3.log` exit 2，Node suite 523/524，唯一失敗是 LWC-199 interruption test 的 marker 未在 1 秒內出現；擴大此 Node test 的 bounded polling 後，具名測試 exit 0。最終來源完整命令仍見下節：Node 524/524 PASS，但 Vitest 在同一 full root 執行中 15/292 failed（多筆 5 秒 timeout 及後續 DOM 缺失），所以 `make verify` exit 2，不宣稱 full gate PASS。父方隨後對同一 source 在 `apps/frontend` 連續實跑 serial 與未覆寫的 canonical Vitest，兩者皆 32/32 files、292/292 cases PASS；這保留 full-root failure 的原始結果，也不推斷所有原因已被證明為主機競爭。

### 最終來源 gate 與父方 replay

| 目標 | 命令／來源 | Exit / 結果 |
|---|---|---|
| 完整 root gate | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u STORAGE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST make verify BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000</code> | **2**；`make-verify-final-source.log`。Bootstrap、YAML、lint、typecheck、vet、69 BFF contracts、32 auth-config contracts、Go/Race 與 Node 524/524 PASS；Vitest 9/32 files failed、15/292 cases failed、277 passed。常見 failure 是 5 秒測試 timeout，另有 admin UI DOM element 缺失；後續 build/smoke target 因 prerequisite failure 未執行。不得標為 full verify PASS。 |
| Worker failure-artifact regression | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY GOPROXY=off GOSUMDB=off go test -race ./cmd/olw_worker -run '^TestCloudCanceledAndExpiredFailuresStillRecordAllArtifacts$' -count=1 -v</code> | 0；cancelled 與 expired subtests PASS。修的是 `execution-secret` fixture ID 與泛用 `secret` sentinel 衝突；保留 `provider` 與實際 `api-secret` credential redaction assertions。 |
| LWC-199 marker timing regression | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY node --experimental-strip-types --test --test-name-pattern='removes the provider mutation response temp file when interrupted in flight' tests/lwc-199-vercel-alias-promotion.test.mjs</code> | 0；更新 bounded polling 後單項 PASS，raw `lwc199-marker-targeted-final.log`。完整 Node suite 在最終來源 full root 中也為 524/524 PASS。 |
| Same-source component serial diagnostic（parent） | `npm run test:component -- --maxWorkers=1`，provider/YouTrack keys unset | 0；32/32 files、292/292 cases，20.11 秒。真工具輸出 `lwc361-tpm-vitest-replays.tool-output.txt`，工具來源與時間另見 parent note。 |
| Same-source canonical component replay（parent） | 原命令 `npm run test:component`，沒有 worker/timeout 覆寫，provider/YouTrack keys unset | 0；32/32 files、292/292 cases，3.54 秒。與 serial diagnostic 分開記錄；不抹去上一列 full-root `make verify` exit 2。 |
| 父方 Vitest evidence note | `/Users/rayer/.hermes/workflows/lwc/lwc361-component-replay-20261007.md` | 讀回並與原 Dispatch inbox delivery `msg_0416d9212121` 對帳；該 note 在 shared workflow package，不屬此 source snapshot。 |
| 父方 canonical smoke replay | `make smoke BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000`；provider/YouTrack keys unset、明確 loopback emulator endpoints | 0，父方 raw `tpm-smoke-replay.log`；這是獨立 smoke evidence，不替代上述 full verify 或正式 browser AC。 |
| Fresh QA worktree Python prerequisite probe（parent） | `make bootstrap`；worktree `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-acceptance`，`python3=/usr/bin/python3` 3.9.6 | **FAIL**；通知記錄 `mcp>=1.10` 無相容 distribution，未提供數值 exit code 或 raw log 路徑。Parent 認定是選到系統 Python，非 wheel URL/provider 問題，之後清除未完成 venv 並以已安裝的 Homebrew Python 3.14.6 繼續 walkthrough；該後續成功 readback 尚未提供。 |

Live formal Auth API parent evidence 已收錄於 [live-formal-auth-api-evidence-r3.md](live-formal-auth-api-evidence-r3.md)：其註冊／登入／有效 token 存取 BFF／refresh／logout revocation 與負向 token/header bypass checks 是 parent 實際 API 證據；該次服務執行點早於本輪最終來源身份，不能借為 browser journey 或 final-source runtime proof。

npm ci 每次輸出鎖檔套件稽核摘要：17 vulnerabilities（3 moderate、13 high、1 critical）；本票未執行 audit fix，也未改 dependency lockfile。

## Acceptance matrix 與後續

| AC | 狀態 | 證據界線 |
|---|---|---|
| Pinned native Synto runtime、worktree venv、BFF child interpreter wiring | **PASS（offline）** | 實際 wheel 安裝/import/CLI 與真 adapter regression；無 provider request。 |
| Root bootstrap/lint/typecheck/vet/contracts/Go-Race/Node phases | **PASS（offline，phase only）** | 最終來源 root log 中上述 phases PASS，Node 524/524；Vitest failure 使 `make verify` exit 2，該次 build/smoke prerequisite 未執行。較早 source 的 full gate 曾通過，但不代替 final-source CI。 |
| Complete root `make verify` on final source | **BLOCKED（exit 2）** | Vitest full-root run recorded 15 failures/292 cases; parent’s same-source isolated and canonical replays passed, but this does not erase that full-gate result. Required CI remains pending. |
| Component Vitest on same source | **PASS（parent replay）** | Parent serial 及 canonical default 都為 32/32 files、292/292 tests；前一份 full-root failure 仍如實保留，最終 required CI 尚未跑。 |
| BFF → native worker → loopback publisher/status/cleanup | **PASS（emulator fixture）** | 具名 -race integration；fixture 替代 compile/provider boundary，其餘路徑保持真實。 |
| Guide text/Make/start/debugger/Synto instructions | **PASS（guide 修正與 Make contract）；fresh walkthrough 待回報** | `LOCAL_DEV.md` 現明列 Python 3.12+、`python3` PATH 選擇、macOS 系統 Python 3.9.6 的 `mcp>=1.10` 安裝錯誤及 worktree venv 重試步驟。Make contracts 已通過；Parent fresh QA worktree 的 3.9.6 失敗已記錄，3.14.6 walkthrough 成功結果與 guide article readback 仍待 parent 回報。 |
| Live formal Auth API | **PASS（parent evidence; API only）** | 對應的限定範圍與執行點見 `live-formal-auth-api-evidence-r3.md`；不是 final-source browser 或 DEV evidence。 |
| 兩 worktree隔離與清理／browser registration & page endpoint／正式 pipeline compile | **NOT RUN by this worker** | Parent owns live/browser acceptance。不得以 emulator page-read、fixture 或 Auth API evidence 換算。 |
| YouTrack LWC-A-17 article readback | **NOT RUN by this worker** | Parent owns article publication/readback。 |
| Same-final-SHA Supervisor/TPM review、required CI、merge、DEV Actions與revision readback | **PENDING parent** | 本輪 test-only changes 出版成 PR 後，需以 PR exact head SHA 做 review/CI；Owner/parent handles merge、DEV Actions 與 readback。 |
| Paid LLM execution、quota、Production/IAM/credentials | **NOT RUN** | 本 worker 未發 provider request、未改 quota、未操作雲端資源或 credentials。 |

下一步以本報告 source snapshot hash 對應的 final PR head SHA 完成 exact-SHA Supervisor/TPM review 與 required CI；parent 接續 live/browser/article evidence，只有符合 Owner 授權與 gates 後才 merge 到 develop 並執行 DEV Actions。Full `make verify` 的 exit 2、單項/同來源 Vitest PASS、已完成的 parent API evidence 分別標示；本報告不代表整票完成。

## PR #98 develop integration continuation — 2026-10-07

本節記錄後續 PR98 integration；上文所列舊 source snapshot digest、較早 full-root gate 與舊 PR head 保留為歷史紀錄，不代表這次 integration 的新 source identity 或最終驗收。

### Source 與 merge 身份

- Worktree/branch 延續原值：`/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-cloud-discussion` / `Rayer/LWC-361-local-cloud-discussion`；此輪起始 PR98 remote head 為 `669e0a96b740224872a0c9a13a1d5251b59c7ed7`。
- 第一段以 normal merge commit `8c5eb54696e7daf16285ec8651b51dd13a354fc4` 整合 `origin/develop` `54a80197e797eafc612acaf43351935a63494d89`，並以相同提交加入本輪 local Pipeline 設定 consumer、測試和指南接線。
- Parent 通知 PR99 已合併；fetch 後 `origin/develop` 為 `af5698f3c0215c71d6c87566cb9361de08c62b91`。第二段以 normal merge commit `dc712c69f18319fcce5b21e3da54b90c24dd4591` 整合，不改寫 PR96/97/99 歷史；保留 PR99 bounded Secret Manager SDK 診斷修正及其回歸。
- 本次程式碼測試的候選 source tip 為 `dc712c69f18319fcce5b21e3da54b90c24dd4591`。本報告與新增 evidence 會在該 source tip 之後以一個 docs/evidence-only commit 發佈；最後 PR head 以正常 push 後的 `git rev-parse HEAD`／PR readback 為準，source changes 沒有在這個 report-only commit 後續再修改。
- `git ls-files -u` 無輸出；`git diff origin/develop...HEAD --check` exit 0。PR98 在 push 前的 GitHub readback 還是舊 head `669e0a9…`、`mergeable=CONFLICTING`，這是 push 前狀態，不能當作新 source 的 mergeability 結果。

### 這次整合內容

- Native BFF 將 `LOCAL_CLOUD_PIPELINE_CONFIG_PATH` 與 `LOCAL_CLOUD_PIPELINE_BINDINGS_PATH` 結構化傳給 worker。Root/app Make 與 `scripts/local-cloud-env.sh` 指向同 worktree 的 `CAC_OUTPUT_DIR/local`（預設 `.build/cac/local`）；`local-start`、`support-frontend`、`bff-local` 傳入相同路徑。
- Local-scope cloud worker 要求兩個 rendered files，讀取本機 `synto.toml`／private binding 並採用該檔 run timeout；缺檔或無效 binding 清楚失敗，不 fallback 到 DEV 的 GCS TOML、不上傳設定，也不呼叫 Cloud Run。無 local scope 的 deployed path 保留既有 GCS `pipeline-config/synto.toml` run-start read。
- BFF→worker fixture 與 manager process-boundary 回歸驗證旗標保留為獨立 argv（檔名包含空白時亦不拆參數）；worker 回歸驗證 local generated files 不觸發 GCS config read、deployed mode 拒絕本機 files，以及 receipt commit/failure 語義。
- `apps/bff/docs/LOCAL_DEV.md` 已明確說明 local config 的產生入口、產物位置、`CAC_OUTPUT_DIR` 一致性、provider key 名稱（不記錄值）、timeout prerequisite、缺檔錯誤及部署模式差異。Parent 隨後從 commit `dc712c69…` 讀取此 guide 並更新／回讀 LWC-A-17 article；parent 訊息 `msg_4f3547ce63a2` 回報 article exact content 雙讀一致，guide SHA-256 前綴 `9732e234…`。fresh-worktree walkthrough 仍未完成，這項 article readback 不能當作 walkthrough pass。

### 本輪驗證：命令與結果

所有 Go/Python/Make regression subprocess 均移除 `LLM_API_KEY`、`DEEPSEEK_API_KEY`、`SYNTO_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`GEMINI_API_KEY`、`TYPESAFE_API_KEY`、`TYPESAFE_JEV_API_KEY`、`LWC331_TEST_API_KEY`；需離線 Go 測試另設 `GOPROXY=off GOSUMDB=off`。下列 raw output 收錄於同目錄 `evidence/`。

| 目的 | 精確命令 | Exit / raw evidence |
|---|---|---|
| Local scope Pipeline input／deployed GCS separation、committed receipt regression、worker/localpipeline canonical race coverage | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST -u VERCEL_TOKEN GOPROXY=off GOSUMDB=off go test ./... -v -count=1 -race`（workdir `apps/bff`） | **0**；所有執行套件通過，預期的 test-level skip 保留在輸出。`bff-canonical-go-test-race-final.log`。 |
| BFF HTTP→native worker→fake GCS/Firestore publisher、receipt/status/readback | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY FIRESTORE_EMULATOR_HOST=127.0.0.1:18885 STORAGE_EMULATOR_HOST=http://127.0.0.1:14443 GOPROXY=off GOSUMDB=off go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1 -race -v`（workdir `apps/bff`） | **0**；HTTP trigger 202、重入 409、成功與增量 status 200、child failure 500、other-scope sentinel 與 cleanup/readback assertions 通過。`bff-local-pipeline-loopback-final-race.log`。只替代 Synto compile/provider 邊界；無付費 LLM。 |
| PR99 bounded Secret Manager SDK diagnostics | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY GOPROXY=off GOSUMDB=off go test ./cmd/pipeline_config -run '^TestRunPreparePreservesBoundedSecretManagerSDKMessage|TestRunPrepareDistinguishesEmptySecretManagerPayload|TestRunPrepareDistinguishesSecretManagerClientInitializationFailure|TestGoogleSecretManagerSDKAccessPath$' -count=1 -v`（workdir `apps/bff`） | **0**；4 tests pass，SDK HTTP 是 test transport/fake reader，無 Secret Manager live read。`pr99-sdk-diagnostic-go-r1.log`。 |
| Worker manager process-boundary／worker pipeline-config fixture與失敗後修正 | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY GOPROXY=off GOSUMDB=off go test ./cmd/olw_worker -run '^TestCommittedLocalExecutionSurvivesReceiptWriteFailureAndLaterCommitIsNotMisattributed$' -count=1 -v`（workdir `apps/bff`） | **0**；committed manifest 即使 receipt write failure 仍歸屬原 execution；test-only rendered config fixture 已補齊。`worker-local-execution-receipt-regression-r1.log`。第一次整包輸出因 fixture 只有最小 TOML 而顯示 `pipeline execution failed`，原 red `go-local-pipeline-config-regressions-r1.log` 保留；改用既有完整 deployed TOML test fixture 後本具名因果回歸通過，沒有移除 receipt assertion。 |
| Root Make/local supervisor、IPv6 readiness、support targets/provider isolation contracts | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY python3 -m unittest test_local_dev_makefile.py -v`（workdir `apps/bff/scripts`） | **0**；12/12。修正 stale test assertion，使其檢查真實分行 `LOCAL_CLOUD_*`／`PATH` exports，不改 runtime 以迎合舊字串。`local-dev-make-contract-r1.log`。 |
| Apps/BFF existing Python contract suite | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY python3 -m unittest discover -s scripts -p 'test_*.py' -v`（workdir `apps/bff`） | **0**；110 tests。`bff-script-contracts-python-final.log`。 |
| Deploy engine／PR99 Python prepare diagnostics | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u VERCEL_TOKEN python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py' -v`（workdir `apps/bff`）；並以 `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_prepare_diagnostics.py' -v` 單獨重跑該類 | **0**；完整 engine suite 110 tests；diagnostic subset 32 tests。`deploy-engine-python-final.log`、`pr99-pipeline-prepare-diagnostics-python-r1.log`。 |
| Canonical Auth config／CD source contract | `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py' -v` 與 `python3 ../../scripts/test_cd_contract.py`（workdir `apps/bff`） | **0**；Auth config 32 tests，CD contract 69 tests。`auth-config-contract-python-final.log`、`cd-contract-python-final.log`。 |
| Canonical BFF job static/build gates | `go vet ./...`；`go build ./...`（workdir `apps/bff`） | 各 **0**。`bff-canonical-go-vet-final.log`、`bff-canonical-go-build-final.log`。 |
| Canonical pinned Synto Flash wire test | `env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u VERCEL_TOKEN -u PIP_EXTRA_INDEX_URL -u PIP_USER -u PIP_PASSWORD PIP_CONFIG_FILE=/dev/null PIP_INDEX_URL=https://pypi.org/simple PIP_NO_INPUT=1 make test-flash-execution`（workdir `apps/bff`） | **0**；exact hashed Synto 0.7.0 wheel；test server 僅 loopback，測試內 provider key 為 synthetic fake，未呼叫付費 provider。`bff-test-flash-execution-final.log`。 |
| Feature diff whitespace/unmerged checks | `git ls-files -u`；`git diff origin/develop...HEAD --check` | **0**；無 unmerged entries、feature diff 無 whitespace errors。 |

### 驗收界線與尚待項目

| AC / 外部結果 | 狀態 | 界線 |
|---|---|---|
| current develop/PR99 正常整合、無 unmerged、361/368 同存 | **PASS（source merge）** | 兩個 normal merge commit 如上；未 rebase、force-push 或刪分支。 |
| BFF local config 來源與 native child argv、private binding／timeout consumer | **PASS（offline + loopback fixture）** | 所有程式 consumer 與 publisher/readback 的具名 regressions 在表中；不是實際 provider-backed generation。 |
| deployed no-scope GCS config reader、receipt commit/failure publisher語義 | **PASS（offline regression）** | full worker race suite、GCS read-once 和 execution-specific manifest tests通過；正式 DEV job 未執行。 |
| Local config generation (`make config-local`) | **NOT RUN** | 此命令依既定 renderer 需要 provider key/Secret Manager resolver 與 timeout config；本任務禁止讀 credential/呼叫 live provider，所以只執行離線 renderer tests與 loopback fixture。 |
| Fresh QA worktree `make bootstrap` / browser walkthrough | **NOT RUN / 尚未 PASS** | Parent 保留系統 Python 3.9.6 的 `mcp>=1.10` 失敗、Homebrew 3.14.6 的 fresh bootstrap timeout；不把既有原 worktree venv/loopback tests 換算成 walkthrough PASS。 |
| Browser registration/session → local page → real provider pipeline | **NOT RUN** | 需 parent 的 browser/live acceptance；本輪沒有 paid LLM 或真 provider request。 |
| Parent article LWC-A-17 | **PASS at prior guide revision; final sync pending** | Parent 由 `dc712c69…` 的 `LOCAL_DEV.md` 更新後做 exact content/readback；本節之後新增 Pkl CLI prerequisite，故 parent 需將此最後一行同步到文章並 read back。這不證明 fresh walkthrough。 |
| Remote PR98 final head/mergeability/same-SHA required GitHub CI | **PENDING publication readback** | push 前遠端仍在 `669e0a9…` 並標 `CONFLICTING`；normal push 後另回報完整 PR head、mergeability 與 CI run status。Parent 執行 exact-final-SHA TPM/Supervisor review、CI 對帳與遠端 PR merge/DEV。 |
| Full `make verify`／整票驗收 | **NOT PASS / PENDING** | Parent 原 `make-verify-final-source.log` 為 exit 2（Node 524 pass；Vitest 15/292 failed），後續同來源 component replay 32/32 files、292/292 cases PASS。該 red log仍保留；本輪不重跑 root verify、不宣稱 full gate 或整票完成。 |

此交付只完成 LWC-361 PR98 本機 worker config consumer 的 develop integration、離線/loopback回歸與程式指南；Owner live/browser/DEV 流程、fresh worktree walkthrough、exact final SHA review、required GitHub CI 和 Parent merge/DEV Actions 尚待完成。

### Pkl CLI guide clarification — 2026-10-07

Parent 依 `cmd/pipeline_config/main.go` 確認 `make config-local` 直接以 `exec.CommandContext` 執行 `PKL_BIN` 或 PATH 中的 `pkl`。因此 `apps/bff/docs/LOCAL_DEV.md` 的 Pipeline generation step 現要求 Pkl CLI 可從 `PATH` 執行或用 `PKL_BIN` 指定，並建議以 `pkl --version` 確認；沒有指定或猜測 minimum version，沒有新增 installer 或 bootstrap/preflight gate。這只改文件，依 parent 指示不重跑完整 root suite。

之前 parent 的 LWC-A-17 exact article readback 對應 `dc712c69f18319fcce5b21e3da54b90c24dd4591` 上的舊 guide 文字。該 readback 對原 guide 有效；Pkl prerequisite 是其後新增的一行，故最終文章同步/readback 必須由 parent 在本次 guide commit 後重做。本次 guide 修正的 source/worktree digest 為 `10ee0f0d50cb70ed26000d03d356c781b5a73ecd1a5f9fb5361268ad329ecc65`（1,204 paths，沿用本報告前述路徑與內容長度編碼算法，排除 `docs/lwc-361/`、`__pycache__` 和 `*.pyc`）；已測試的程式碼 source tip 仍為 `dc712c69f18319fcce5b21e3da54b90c24dd4591`。本次沒有 source code 或測試變更。Pkl CLI 可用 `PKL_BIN` 覆寫路徑，此項已依 source inspection 核對；本輪未另外執行 `pkl --version`，也不將該命令列為本 worker 的 runtime pass。
