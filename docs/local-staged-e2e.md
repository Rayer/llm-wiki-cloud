# Local staged E2E 使用說明

用一個本機 container，從 Markdown 生成 Wiki，或匯入 DEV snapshot 後測試 Query。可跑完整流程，也可只重跑指定階段。結果留在實驗目錄，不部署、不寫回 GCS、不改個人 Synto 設定。

## 1. 目錄結構

```text
experiment/
  raw/                 # 輸入：原始 Markdown
  snapshot/            # 輸入：既有 Wiki snapshot，與 raw 擇一
  query.json           # Query 模型與執行設定
  cases.jsonl          # 測試問題，一行一筆 JSON
  image.json           # 啟動 metadata，記錄 image digest 與平台
  image-inspect.json   # image inspect 原始輸出
  run/                 # 一次執行的輸出，自動建立
    run.json           # 階段狀態、耗時、錯誤
    input/             # 凍結的輸入副本
    work/              # 工作中的 Wiki
    snapshots/         # 各階段 checkpoint
    artifacts/         # Query 中間結果與回答
  retry/               # 另一次執行，不覆寫 run/
```

**每次 `--output` 必須是不存在的新子目錄。** 不要用實驗根目錄、輸入目錄或既有 run 當輸出；輸入內不要放 symlink。

## 2. 第一次準備

需要 macOS Apple Container、Git；只有匯入 DEV 時需要 Go 與既有 Google Cloud ADC。以下從 repository 根目錄執行，以附帶的 DNS 資料為例。

```sh
experiment=$(mktemp -d /private/tmp/lwc-experiment.XXXXXX)
cp -R scripts/local_e2e/fixtures/raw-dns "$experiment/raw"
cp scripts/local_e2e/fixtures/query-dns.json "$experiment/query.json"
cp scripts/local_e2e/fixtures/cases-dns.jsonl "$experiment/cases.jsonl"
container system start
build_id=$(openssl rand -hex 16)
image="lwc-experiment:$build_id"
source_dirty=false
if test -n "$(git status --porcelain --untracked-files=normal)"; then source_dirty=true; fi
container build --platform linux/arm64 --target experiment \
  --file apps/bff/cmd/olw_worker/Dockerfile \
  --build-arg BUILD_NONCE="$build_id" \
  --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" \
  --build-arg SOURCE_DIRTY="$source_dirty" --tag "$image" apps/bff
```

建置成功後取得 image 資訊：

```sh
container image inspect "$image" > "$experiment/image-inspect.json"
```

查看該檔案的 **OCI image/index descriptor digest**（不是 layer digest），再執行：

```sh
read -r image_digest  # 貼上 inspect 顯示的 sha256:…，按 Enter
printf '%s' "$image_digest" | LC_ALL=C grep -Eq '^sha256:[0-9a-f]{64}$' || exit 1
printf '{"schema":"lwc-container-launch-v1","image_digest":"%s","platform":"linux/arm64"}\n' \
  "$image_digest" > "$experiment/image.json"
```

定義共用命令，後面只需換情境參數。同一 shell 內使用；開新 shell 要重新設定 `experiment`、`image` 與函式。

```sh
run_experiment() {
  container run --rm --read-only --platform linux/arm64 \
    --volume "$experiment:/experiment" --env DEEPSEEK_API_KEY "$image" \
    --launcher-metadata /experiment/image.json \
    --query-config /experiment/query.json \
    --query-profile corpus-derived-tech-document-v1 \
    --query-prompt domain-neutral-technical-v1 "$@"
}
```

會呼叫 LLM 的情境需用既有安全方式將 `DEEPSEEK_API_KEY` 注入 shell 環境。不要把 key 寫入命令、檔案或 image；不要掛載個人 HOME／雲端憑證。非推論情境不需要 key。

## 3. 階段與參數

| 階段 | 名稱 | 用途 |
| --- | --- | --- |
| 10 | source | 準備 raw 與隔離的 Synto 目錄 |
| 20 | synto-run | 透過公開 Synto CLI 生成 Wiki |
| 50 | index | 建立 LWC index 與 identity mapping |
| 60 | suggested-queries | 產生建議問題 |
| 70 | expansion | 展開查詢 |
| 80 | matching | 搜尋匹配文章 |
| 90 | selection | 選取回答依據 |
| 100 | synthesis | 生成回答與引用 |

沒有 30／40；Synto 內部流程統一由 20 負責。

| 參數 | 用法 |
| --- | --- |
| `--raw DIR` | 從原始 Markdown 開始 |
| `--input-snapshot DIR` | 從既有 Wiki snapshot 開始 |
| `--fork RUN` | 沿用先前 run 的相容 checkpoint |
| `--output DIR` | 新輸出目錄，必填 |
| `--to N` | 跑到 N，包含 N |
| `--from N` | 從 N 跑到 100，包含 N |
| `--only N[,N…]` | 只跑指定階段，依階段順序執行 |
| `--cases FILE` | 自訂問題 JSONL |
| `--suggested-cases` | 改用 stage 60 的問題，不可與 `--cases` 並用 |
| `--query-config FILE` | Query 設定，可從範例複製調整 |
| `--query-profile` / `--query-prompt` | 既有 Query profile／prompt ID，須成對提供；不是 Wiki Project Profile |
| `--timeout SECONDS` | 每階段／子程序時限，預設 600 秒，範圍 1–3600 |
| `--require-grounded` | 要求每題有回答與可解析引用；需包含 stage 100 |
| `--synto-fast` / `--synto-heavy` | Synto 模型，與 Query 設定分開；預設皆為 `deepseek-flash` |

來源三選一：`--raw`、`--input-snapshot`、`--fork`。模式三選一：`--to`、`--from`、`--only`。缺前置資料會報錯，**不會自動補跑**。

自訂 `cases.jsonl` 的一行範例；問題應與資料內容相關：

```json
{"id":"dns-testing","query":"Why does RFC 2606 reserve DNS names?","mode":"wiki","tags":["smoke"]}
```

## 4. 常見情境

### A. 從原始資料跑完整流程

```sh
run_experiment --raw /experiment/raw --to 100 \
  --cases /experiment/cases.jsonl --require-grounded --output /experiment/run
```

只想生成 Wiki：改成 `--to 50` 並移除 `--require-grounded`。只檢查初始化：用 `--to 10`，不呼叫 LLM。

### B. 只測 Query，不重跑 Synto

```sh
run_experiment --input-snapshot /experiment/snapshot --from 70 \
  --cases /experiment/cases.jsonl --require-grounded --output /experiment/query-run
```

也可把上次的 `/experiment/run/snapshots/50` 作為 snapshot。snapshot 要有 concepts index、有效 `cache/id_map.json` 與相關文章，不能只複製 Markdown。

### C. 只重跑某一階段

```sh
# 沿用 expansion，只重跑 matching；不需要 LLM key。
run_experiment --fork /experiment/run --only 80 \
  --cases /experiment/cases.jsonl --output /experiment/matching-again

# 沿用已選好的依據，只重新生成回答；會呼叫 LLM。
run_experiment --fork /experiment/run --only 100 \
  --cases /experiment/cases.jsonl --require-grounded --output /experiment/answer-again
```

fork 須使用相容的 image、輸入、題目與上游設定。只改 synthesis 設定可重用 retrieval；改上游設定，就要從相應上游重新跑。不要修改 checkpoint 繞過檢查。

### D. 匯入 DEV 資料，在本地接續

先在 **host** 使用既有 ADC 唯讀匯入，再用情境 B 執行：

```sh
go -C apps/bff build -o "$experiment/query_experiment-host" ./cmd/query_experiment
# 替換為有權讀取的 DEV Project 路徑，不是 generation 路徑。
snapshot_uri='gs://llm-wiki-data-dev/users/USER_ID/projects/PROJECT_ID'
printf '{"operation":"import","snapshot":"%s","destination":"%s/snapshot"}\n' \
  "$snapshot_uri" "$experiment" | "$experiment/query_experiment-host" local-platform
```

匯入固定當時 published generation，記錄在 `snapshot/import-provenance.json`。container 只接收本地 snapshot，不接收 `gs://` 或 ADC；不寫回 GCS、不重跑雲端 pipeline。換 corpus 時也要換題目。

## 5. 結果與錯誤怎麼看

- **`run.json`**：階段是否完成、哪裡失敗、耗時多少。
- **`artifacts/70.json`～`100.json`**：依序為 expansion、matching、selection、回答與引用。
- **接續／比較**：保留原 run，用 `--fork` 與新的 `--output`。Ctrl-C 後已成功的 checkpoint 保留；不支援原地覆寫或 resume。

| 結果 | 意義／處理 |
| --- | --- |
| exit 0 | 已選階段及啟用的檢查通過；沒啟用 grounded 不代表品質通過 |
| exit 1 | 執行失敗；查看 `run.json` 的階段與錯誤 |
| exit 2 | grounded 未通過：至少一題缺回答或可解析引用 |
| output 已存在 | 換新的子目錄 |
| 缺前置／fork 不相容 | 從更早階段重跑，或選正確的 snapshot／run |
| ID map 缺失或不合法 | 使用有效 mapping 與完整文章的 snapshot，不要用改 slug 掩蓋 |

這是 pipeline 工具，不是整站瀏覽器測試。Grounded 不保證事實正確或逐句引用完整；Synto 有可用輸出不代表內部所有工作都完成。個別執行紀錄與驗收證據留在對應工作票，不放本使用說明。
