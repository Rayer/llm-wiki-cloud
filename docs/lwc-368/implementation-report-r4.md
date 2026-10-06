# LWC-368 `cac-spec-r4` 實作與本機驗收報告

日期：2026-10-06  
狀態：限定範圍的本機實作與檢查完成；來源審查可立即開始。Live GSM、GCS、Cloud Run、Actions 與執行期驗收尚未進行，分列於報告末段。

## 凍結身分與候選

- 凍結版 `cac-spec-r4` 的 SHA256 為 `645e96c87aca95e90e5327219e2df648cc093ea338e8bff07c7c0f853a87ffd6`；本次重算一致。採用 Reviewer PASS 結果與 freeze implementation addendum。Live Freeze comment `4-1914` / State `Submitted` 已由交辦提供確認。
- 審查基準及實際 worktree HEAD 仍是 `2180d48274aec48b89b6ccf35eea5e2b08ff1e25`，branch 為 `Rayer/LWC-368-pipeline-cac`。原始 worktree 改動未 stage、commit 或發布。
- 完整引擎測試在獨立快照 `.build/lwc368-test-scratch/candidate-r7` 執行。其私有 Git fixture commit 為 `c3918c30d96b66fa2692616be3d79cb0a1bc3478`，tree 為 `5b3c99d053a11028ba565c18e0e1dc1b11295d0f`，工作樹乾淨；建立與提交只發生在此 disposable copy，沒有寫入原始 `.git`。這是本次可重現的本機候選來源身分，不是產品分支 commit。
- 本機沒有發布、PR、merge、Actions dispatch、GCS/GSM/provider 操作、IAM 或 credential 變更。
- [Changed/intended-untracked manifest](evidence/source-manifest-r4.tsv) 列出 60 個工作樹變更/交付檔案的狀態、POSIX mode、size 與 SHA256；manifest 本身排除以避免自我雜湊循環。

## 實作內容

1. `deploy/cac/ssot.pkl` 選定 `local`、`dev`、`prod`、bucket、secret reference 及可選 article cap；`deploy/cac/synto.pkl` 集中 Pipeline renderer defaults。Go prepare 先選環境、lazy resolve 該環境必要輸入，再把同一份 prepared projection 傳給 Pkl renderer，避免第二次讀 SSOT 或使用不同權威。所有生成皆 fresh render。
2. `apps/bff/cmd/pipeline_config` 使用 Google Secret Manager SDK/ADC；只有選定 binding 是 GSM 時才初始化 client。`latest` 會依 API 回覆固定本次 numeric version。公開 `pipeline.json` / `synto.toml` 不含 payload；secret reference 放在 private bindings。local GSM payload 只放進 mode `0600` 的 local private bindings，供 Worker 真正讀取；錯誤與公開輸出不含 payload。
3. Root Makefile 新增 `config-local`、`config-dev`、`config-prod`，皆只 prepare/compile。既有手動 DEV/Production workflow 接入共用生成器；獨立 `pipeline-config-only` branch 更新 GCS config 與 Worker secret/timeout，readback 驗證，沒有 image build、push 或 `--image`。
4. Worker 每次 run 起始讀一次 `pipeline-config/synto.toml` 並固定 bytes，從設定讀取 task timeout 並以 context 傳至子程序。Manifest generation restore 不再覆寫部署用 `synto.toml`；歷史 manifest 可解碼、legacy `wiki.toml`/OLW migration 與專案 state 保留。
5. `make -C apps/bff pipeline-run` 先呼叫 root `config-local`，再把生成的 TOML 與 private bindings 路徑傳給 `olw_worker run`。移除 Worker defaults 後，local run 不再要求舊專案 TOML；local env binding 仍可用環境變數，local GSM binding 則由 private file 交給同一個 API key consumer。
6. 初次部署時 GCS config object 可以確實不存在：snapshot 只將 gcloud 回報的精確「沒有符合物件」錯誤辨認為 absence，並保存 `pipeline_config_absent`。一般權限錯誤、空檔、非 UTF-8 或 malformed TOML 均為錯誤。Rollback 刪除新 object 並 readback 確認 absence，同時還原先前 secret binding 及 Job timeout。已有合法 TOML 會保存原始 bytes/hash，即使其 timeout 與 Job timeout 不同也不會被新加的 equality gate 擋下或被改寫。
7. 不比較新 timeout 與現有 Job timeout 作為部署 gate；輸入仍必須明確提供並在生成物內一致，更新後仍 readback Job timeout。只在大型輸出診斷測試為其單獨設定較長 synthetic timeout；短 timeout cancellation fixture 保持原值。`apps/frontend` 只有共享 workflow 介面測試更新，沒有前端產品程式或設定變更。

## 凍結驗收與本機證據

| 凍結驗收 | 本機證據 | 證據界線 |
|---|---|---|
| Project-wide SSOT、selected target、lazy resolver、renderer-only defaults、SSOT precedence、fresh render | 實際 Pkl 0.32.1 local/dev/prod compile/render；`TestRunPrepareRendersOnlyThePreparedProjection` 在 SSOT 首次 resolve 後改動其模組副本，證明 renderer 使用 prepared projection；article override 1,234 tokens 優先於 32,768 default。 | dev/prod 是 render-only；未取用真 GSM。 |
| Secret Manager SDK 接線、未選 binding 不讀取、numeric version、payload 不外洩、secret 到 consumer | `TestGoogleSecretManagerSDKAccessPath`、`TestLatestSecretManagerBindingIsPinnedToResolvedNumericVersion`、`TestPreparedSecretReferenceUpdatesOnlyPublicAndPrivateBindings`；synthetic local GSM bytes 寫入 private file 後由 `TestLocalWorkerRunConsumesRenderedPipelineConfig` 載入，且 private 值蓋過 stale env 值。 | SDK 使用 fake/synthetic payload；未存取真 GSM。 |
| Make 三入口及既有 Actions config-only；不 build/push/換 image | `make config-local` 實際生成；`test_config_only_renders_uploads_and_verifies_without_changing_image` 及 timeout 變更 fixture 證明 GCS/Job 更新與 image digest 不變；`scripts.test_engine_workflow` 7 tests 及 `make workflow-yaml` 通過。 | workflow 沒有 dispatch；provider calls 都是 fixture。 |
| 新/舊專案 Worker 消費 run-start GCS config；manifest/legacy 不還原舊 TOML，保留 state | `TestDeployedPipelineConfigIsReadOnce`、`TestDeployedSyntoReplacesOldTOMLAndPreservesProjectState`（既有 Synto state、legacy OLW migration）、`TestGenerationOutputsExcludeConfigWhileReadingHistoricalManifestIsSupported` 通過。 | 使用 memory object store 與臨時 vault，非 live GCS。 |
| article cap 與固定 timeout 被實際消費；取消 Worker child | 真實 Pkl 輸出檢查 `article_max_tokens=32768`、provider timeout 600 秒、context 16,384/32,768、max concepts 8；`TestCloudRunTimeoutCancelsWorkerChild`、`TestPipelineRunTimeoutRequiresPositiveBoundedTOMLValue` 與 local Worker consumer 測試通過。 | 沒有調整正式預設；沒有推定 live Job timeout。 |
| 初次 CaC adoption 與 rollback 恢復 prior absence；permission/malformed 不是 absence | `test_worker_first_config_adoption_and_rollback_preserve_absence`、`test_worker_snapshot_does_not_treat_permission_or_invalid_toml_as_absence`、`test_worker_snapshot_preserves_prior_config_without_timeout_gate` 通過。 | provider 呼叫由 fake gcloud/provider 提供。 |
| 實際 local Make invocation 不依賴 Worker defaults | `make config-local` 用 synthetic `LLM_API_KEY=TEST_ONLY_LOCAL_SECRET` 產生 timeout 77 秒設定；fresh `make -n -C apps/bff pipeline-run` 顯示它先生成，再把 `synto.toml` 與 `private-bindings.json` 傳入真正 Worker CLI；`TestLocalWorkerRunConsumesRenderedPipelineConfig` 驗證該 CLI 路徑載入設定。 | 沒有執行會呼叫模型/provider 的真實 pipeline run。 |

`make config-local` 輸出檢查得到 timeout 77、article cap 32,768、private mode `0600`，synthetic secret 不在 public/TOML 產物。local 輸出 hash：`f421afaf7f0a8528074b92542d62f71a576a2777f0498da8362f088ccce31b16`。產物與測試輸出都位於 workspace scratch。

## 執行檢查與原始 log

下列完整 root 命令使用 workspace scratch、`FIRESTORE_EMULATOR_HOST` 未設定，並依 coordinator 的一次性授權在 sandbox 外執行；沒有 cloud CLI 或部署步驟。命令 exit 0，涵蓋 lint、typecheck、BFF/Frontend test、BFF compile 與 Frontend build。BFF 的 emulator-only cases 依測試定義 skip。

完整命令：

`env -u FIRESTORE_EMULATOR_HOST TMPDIR=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-368-pipeline-cac/.build/lwc368-test-scratch/root-tmp GOCACHE=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-368-pipeline-cac/.build/lwc368-test-scratch/root-gocache PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages make lint typecheck test build` — exit 0。

- Root test target中的 BFF Python contract suites 為 69 + 32 tests，`go test ./... -race` 所有 package 通過（含 `cmd/olw_worker`、`cmd/pipeline_config`）；Frontend node tests 523/523 通過。Frontend production build 完成。
- `make vet workflow-yaml` exit 0；檢查四個 workflow YAML。另 `python3 -m unittest scripts.test_engine_workflow` exit 0，7 tests。
- Disposable candidate r7 以獨立 Git metadata/object database 執行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'`：106 tests，exit 0。source identity 測試在此快照正常執行；沒有使用原始 `.git` alternate index。
- `python3 -m unittest deploy.engine.tests.test_pipeline_config deploy.engine.tests.test_engine.Acceptance`：42 tests，exit 0。
- `npm ci --prefix apps/frontend`：exit 0；鎖定依賴已安裝。`gofmt -d` 與 `git diff --check` 無差異。

可重查的 log：

- [root make lint/typecheck/test/build](evidence/root-make-lint-typecheck-test-build-final.log)
- [engine 106-test disposable candidate](evidence/engine-full-final-candidate-r7.log)
- [Pipeline bootstrap/timeout 42-test focused suite](evidence/engine-bootstrap-timeout-focused-final.log)
- [BFF vet 與 workflow YAML](evidence/root-make-vet-workflow-yaml.log)、[workflow contract tests](evidence/engine-workflow-tests.log)
- [npm ci debug exit](evidence/npm-ci-debug.log)、[實際 config-local 生成](evidence/config-local-actual.log)、[local artifact assertions](evidence/config-local-assertions.log)、[local pipeline-run Make expansion](evidence/local-pipeline-run-make-dry-run-final.log)
- [Local Worker consumer test](evidence/go-local-pipeline-run.log)
- [Changed file content/mode manifest](evidence/source-manifest-r4.tsv)

## 已分類的紅燈與修正

最初 engine 紅燈的 6 failures / 3 errors 分開如下；完整 final candidate r7 現為 106/106 綠燈。

| 原失敗 | 原因及處置 |
|---|---|
| `SourceApplicability.test_actual_go_inputs_include_embeds_exclude_engine_and_unrelated_packages`（error） | 原 worktree 的新 Go inputs 是 untracked，source identity 正確拒絕它們。沒有改測試或改原始 Git index；將候選複製到獨立 repo，對該副本建立 test-only fixture commit，再執行完整套件。r7 中該檢查通過。 |
| `test_real_action_cross_plan_unresolved_handle_fails_closed`（error） | cold `go run ./cmd/deploy_config` admission 在 180 秒上限逾時；Action 測試使用臨時 HOME 但未傳 Go module/build cache。測試 harness 現明確傳遞 `go env` 回報的 GOCACHE/GOMODCACHE；candidate r7 全套通過。 |
| `test_real_action_download_blocks_idless_unknown_without_submit`（error） | Action admission 因上述隔離 cache 問題失敗，所以後續 `result.json` 不存在；不是引擎錯誤結果。cache 傳遞後通過。 |
| `test_real_action_download_fresh_release_resumes_same_build_id`，WORKING 子測試（failure） | 新 Action admission 的 Go subprocess 沒有可用 cache，回報 input rejected；傳遞 cache 後通過。 |
| 同一測試，STATUS_UNKNOWN 子測試（failure） | 同上，resume 前 admission 失敗；傳遞 cache 後通過。 |
| `test_real_action_download_success_digest_failure_only_retries_digest`（failure） | 同上，resume admission 失敗；傳遞 cache 後通過。 |
| `test_real_action_download_v2_checkpoint_keeps_legacy_id_sequence_and_handle`（failure） | 同上，resume admission 失敗；傳遞 cache 後通過。 |
| `test_real_action_terminal_failure_explicit_retry_and_receipt_expansion`（failure） | 同上，retry admission 失敗；傳遞 cache 後通過。 |
| `test_registered_dev_release_resumes_legacy_source_with_current_executor`（failure） | 同上，registered release resume admission 失敗；傳遞 cache 後通過。 |

第一個 sandbox 內 root suite 另有 6 個既有 `httptest.NewServer` 測試因 loopback bind `operation not permitted` 失敗；一次性授權的 root rerun 能正常 bind，整個 test target 通過。Worker 的 oversized-output 診斷測試先前因共用 fake TOML 的 15 秒 synthetic task timeout 在併行測試負載下得到 timeout diagnostic；現在只對該測試提供 300 秒 fixture，短 timeout cancellation 測試維持原設定，完整 Worker package 與 root tests 通過。Workflow 變更後曾有 5 個舊 contract assertions 預期舊 inputs/jobs；已只更新 workflow interface 測試，沒有變更 Frontend 產品程式，523 個 tests 現全數通過。Sandbox 中 Frontend build 另曾因 Google Fonts DNS `ENOTFOUND` 失敗；授權的完整 root rerun 成功編譯與建置。診斷用紅 log 均保留，綠色證據為本報告所列 `*-final*` logs。

## 尚未執行的 live 驗收與下一步

以下是明確缺少的 live 證據，亦受本次禁止 live provider access 的範圍限制；它們不阻擋現在開始來源審查：

1. **DEV / Production Worker Job timeout 現值**：需由有授權的人 readback `olw-pipeline-dev` 與 `olw-pipeline` 的實際 `timeoutSeconds`，再把各環境秒數作為 `pipeline_run_timeout_seconds` 明確輸入。不從舊 export job 的 23 小時值推定，也不在程式新增 timeout equality/diff gate。
2. **真實 GSM**：需要授權的 Secret Manager 資源/身分執行實際 Access，確認 numeric pin、private binding 到 Worker 的 runtime 消費與不洩漏。local fake SDK/Payload fixture 不代表真實 GSM 已驗收。
3. **真實 Actions/GCS/Cloud Run runtime**：後續在另行允許的 DEV 操作中驗證 config-only 不改 image、object/Job readback 與已啟動 Worker 對 run-start bytes 的消費。本次未 dispatch Actions，也未讀寫 GCS、Secret Manager 或 Cloud Run。

目前可先依 disposable candidate `c3918c30d96b66fa2692616be3d79cb0a1bc3478` 開始 source review；live 驗收應在其後另列，不需等待才開始 code review。沒有宣稱 LWC-368、LWC-362 或 LWC-363 已經 live 驗收或關閉。
