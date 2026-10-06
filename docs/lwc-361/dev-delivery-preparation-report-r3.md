# LWC-361 DEV delivery preparation — r3

## 狀態與來源身份

本報告記錄原 Implementer 完成本機 worker runtime、repository guide 與離線交付資格準備，以及後續 root gate 對帳、父方同來源 Vitest replay 與交付狀態。這是交付候選的 code/build/test 證據，不代表完整 LWC-361 acceptance、同 SHA review、merge 或 DEV deployment 已完成。

- Worktree：/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-cloud-discussion
- Branch：Rayer/LWC-361-local-cloud-discussion
- 起始 HEAD：2180d48274aec48b89b6ccf35eea5e2b08ff1e25
- Runtime：依 Dispatch 指定延續 GPT-6-Luna xhigh；未切換 model 或 effort。
- 最終 source snapshot SHA-256：c7b0bae8356f069dafe5956d6a72ef19793c558226aff82835724417f8a216af，涵蓋 1,126 個 source/worktree paths；排除 docs/lwc-361/、__pycache__ 和 *.pyc。計算方式為排序 `git ls-files --cached --others --exclude-standard -z` 路徑，逐路徑以 8-byte big-endian 路徑長度、路徑 bytes、8-byte big-endian 內容長度、內容 bytes 更新 SHA-256；刪除檔以 `DELETED` 表示，symlink 以 `SYMLINK\0` 加 link target 表示。候選 Git HEAD 仍為 5454ab762d2268a21e432b6d70d0fa379589daca，加上本輪兩個 test-only working-tree edits；出版後的 Git commit SHA 以 PR head readback 為準。
- 舊 Supervisor digest bd7c9c1106b778b8ee8da76574a48f8ff46290b67763250eabb9bf0f2e381c3f 不涵蓋本次變更，沒有移用其 review verdict。此候選仍需 parent／Supervisor 對出版後的同一 SHA 做 review；本報告不宣稱 review PASS。

## 本輪變更

- apps/bff/Makefile 依 worker Dockerfile 的同一公開 wheel URL、Synto 0.7.0 與 SHA-256 建立 worktree 私有 Python venv，真正安裝 wheel，驗證 import synto、版本及 synto --version。不修改全域 Python。
- scripts/local-cloud-env.sh 把該 venv 的 bin/ 放在 local child PATH 前端並匯出 interpreter 路徑；現有 BFF native worker manager 傳遞環境，worker 的正式 python3 adapter 因而用同一 venv。local-start、BFF debugger 及 root make bootstrap 均準備此 runtime，沒有新增 app preflight 或平台。
- 新增 build-tagged TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider，確認 worker 的 python3 解析到 worktree venv；以真實 Synto adapter 跑 --version 為正向、無效 CLI 命令拒絕為負向，兩者都不載入 provider key、不呼叫模型。
- scripts/local-vertical-smoke.sh 統一在 provider key names 全部 unset 的環境跑具名 loopback/auth/scope/Synto regressions；更新 root smoke contract test，確認退休的假登入與 destructive seed 不會回來。
- 補完 apps/bff/docs/LOCAL_DEV.md，說明 ADC、worktree venv、Synto 版本與 provider key names（不含值）、正式登入、ports/Host/cookie、scope/資料保留與清理、啟停、debugger 和成本界線；同步更新 root/BFF README 的 bootstrap 指引。
- 修正既有 public-config 前端測試，讓預設 demo_enabled=false 的回應契約與本票 fail-closed capability 一致；Make local-config regression 明確指定測試 ports，不依賴 parent 當前服務 ports。

實際安裝 readback：Synto 0.7.0，Python 3.14.6，interpreter 為 worktree Git metadata 的 lwc361-local-cloud/python/bin/python3。本機只有 Python 3.14 可用；wheel 與 import/CLI/worker adapter 正反向 regression 均通過。DEV worker image 仍使用原 Dockerfile 的 Python 3.12，沒有更動。

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
| Guide text/Make/start/debugger/Synto instructions | **PASS（文件/Make contract）** | Repository guide 已更新並由 root gate/Make regression 驗證。完整實機指南 walkthrough 與 guide article 發布仍由 parent readback。 |
| Live formal Auth API | **PASS（parent evidence; API only）** | 對應的限定範圍與執行點見 `live-formal-auth-api-evidence-r3.md`；不是 final-source browser 或 DEV evidence。 |
| 兩 worktree隔離與清理／browser registration & page endpoint／正式 pipeline compile | **NOT RUN by this worker** | Parent owns live/browser acceptance。不得以 emulator page-read、fixture 或 Auth API evidence 換算。 |
| YouTrack LWC-A-17 article readback | **NOT RUN by this worker** | Parent owns article publication/readback。 |
| Same-final-SHA Supervisor/TPM review、required CI、merge、DEV Actions與revision readback | **PENDING parent** | 本輪 test-only changes 出版成 PR 後，需以 PR exact head SHA 做 review/CI；Owner/parent handles merge、DEV Actions 與 readback。 |
| Paid LLM execution、quota、Production/IAM/credentials | **NOT RUN** | 本 worker 未發 provider request、未改 quota、未操作雲端資源或 credentials。 |

下一步以本報告 source snapshot hash 對應的 final PR head SHA 完成 exact-SHA Supervisor/TPM review 與 required CI；parent 接續 live/browser/article evidence，只有符合 Owner 授權與 gates 後才 merge 到 develop 並執行 DEV Actions。Full `make verify` 的 exit 2、單項/同來源 Vitest PASS、已完成的 parent API evidence 分別標示；本報告不代表整票完成。
