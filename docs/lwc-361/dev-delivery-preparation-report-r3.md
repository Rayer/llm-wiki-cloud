# LWC-361 DEV delivery preparation — r3

## 狀態與來源身份

本報告記錄原 Implementer 完成本機 worker runtime、repository guide 與離線交付資格準備。這是交付候選的 code/build/test 證據，不代表完整 LWC-361 acceptance、同 SHA review、merge 或 DEV deployment 已完成。

- Worktree：/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-361-local-cloud-discussion
- Branch：Rayer/LWC-361-local-cloud-discussion
- 起始 HEAD：2180d48274aec48b89b6ccf35eea5e2b08ff1e25
- Runtime：依 Dispatch 指定延續 GPT-6-Luna xhigh；未切換 model 或 effort。
- 本報告所對應的 source snapshot SHA-256：df9f95887d7002ca1310a3c4fe8dee4d6d27b7ddb268e1de44078771fcc370f8，涵蓋 1,129 個 source/worktree paths；排除 docs/lwc-361/、__pycache__ 和 *.pyc。計算方式為排序 git ls-files --cached --others --exclude-standard -z 路徑，逐路徑以 8-byte big-endian 路徑長度、路徑 bytes、8-byte big-endian 內容長度、內容 bytes 更新 SHA-256；刪除檔以 DELETED 表示，symlink 以 SYMLINK\0 加 link target 表示。
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
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u STORAGE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST make verify BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000</code> | 0；bootstrap、workflow YAML、lint、typecheck、vet、test、build、smoke 與 git diff --check 全通過。原始輸出 make-verify-final2.log。 |
| apps/bff | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY STORAGE_EMULATOR_HOST=http://127.0.0.1:14443 FIRESTORE_EMULATOR_HOST=127.0.0.1:18885 GOPROXY=off GOSUMDB=off go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1 -race -v</code> | 0；BFF HTTP trigger/status 與真 native subprocess fixture、loopback GCS/Firestore publisher/readback、失敗路徑測試 PASS。原始輸出 loopback-pipeline-test.log。 |
| repo root | <code>env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY -u STORAGE_EMULATOR_HOST -u FIRESTORE_EMULATOR_HOST bash scripts/local-vertical-smoke.sh</code> | 0；具名 auth/scope/supervisor/Make/Synto smoke；provider keys 和 emulator hosts 均未帶入。原始輸出 smoke-targeted-retry.log。 |

Loopback integration 的讀回包含 metadata digest 相等、trigger 202、重入 409、status 200、fixture 成功與失敗、scope cleanup/readback 和另一 scope sentinel 保留等測試斷言。這是具名 BFF/emulator fixture evidence；不是 browser page endpoint 成功、正式 LLM 編譯或 live target acceptance。

完整 gate 修復過程的失敗均保留在各自 raw log：初次 Make dry-run assertion 預期了不會出現在展開 recipe 的目標標籤（exit 1）；第一次 make verify 發現既有 public-config 測試缺少 demo_enabled:false（exit 2，相關具名測試首次修補後仍因 fallback expectation 缺欄位 exit 1，第二次具名測試 3/3 PASS）；第二次完整 verify 於 ambient AUTH_PORT=18081 與測試預期 8081 不同而 exit 2；第三次完整 verify 發現 root smoke 的 unittest module path 相對於 apps/bff cwd 不正確而 exit 2。修正後 named regressions 與最後一次 full make verify 均 exit 0。不曾 skip/delete 驗收斷言。

npm ci 每次輸出鎖檔套件稽核摘要：17 vulnerabilities（3 moderate、13 high、1 critical）；本票未執行 audit fix，也未改 dependency lockfile。

## Acceptance matrix 與後續

| AC | 狀態 | 證據界線 |
|---|---|---|
| Pinned native Synto runtime、worktree venv、BFF child interpreter wiring | **PASS（offline）** | 實際 wheel 安裝/import/CLI 與真 adapter regression；無 provider request。 |
| Root tests/lint/typecheck/vet/build/smoke | **PASS（offline）** | 最後一次 make verify exit 0，所有 test subprocess provider keys unset。 |
| BFF → native worker → loopback publisher/status/cleanup | **PASS（emulator fixture）** | 具名 -race integration；fixture 替代 compile/provider boundary，其餘路徑保持真實。 |
| Guide text/Make/start/debugger/Synto instructions | **PASS（文件/Make contract）** | Repository guide 已更新並由 root gate/Make regression 驗證。完整實機指南 walkthrough 與 guide article 發布仍由 parent readback。 |
| Live local formal Auth／兩 worktree隔離與清理／browser page endpoint／正式 pipeline compile | **NOT RUN by this worker** | Parent owns live/browser acceptance。不得以 emulator page-read或本 fixture 換算。 |
| YouTrack LWC-A-17 article readback | **NOT RUN by this worker** | Parent owns article publication/readback。 |
| Same-final-SHA Supervisor/TPM review、required CI、merge、DEV Actions與revision readback | **PENDING parent** | 舊 digest 不適用；PR source 必須以最終 candidate SHA 同步 review。 |
| Paid LLM execution、quota、Production/IAM/credentials | **NOT RUN** | 本 worker 未發 provider request、未改 quota、未操作雲端資源或 credentials。 |

下一步由 parent 在此分支 exact final SHA 做 TPM/Supervisor review 與 CI，並完成 live/browser/article evidence；只有符合 Owner 授權與 required gates 後，才由 parent merge 到 develop 並執行正式 DEV Actions。這份報告和離線 gate 不代表全票完成。
