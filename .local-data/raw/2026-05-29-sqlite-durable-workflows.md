---
title: "SQLite Durable Workflows"
date: "2026-05-29"
source: "https://obeli.sk/blog/sqlite-is-all-you-need-for-durable-workflows/"
language: "en"
---

# SQLite is All You Need for Durable Workflows

Obelisk 回應 DBOS「Postgres is all you need for durable execution」的主張，認為可以推得更遠：對多數 durable systems，SQLite 就夠了。

## 核心邏輯

- Durable 的是 **workflow state**，不是 compute。Workers 可以隨時重啟、replay、retry。
- SQLite 提供 transactional durable state，但沒有 network hop、沒有獨立資料庫服務 —— 零額外 infra。
- Litestream async streaming 到 S3 解決備份/遷移/檢查需求（注意：async，可能遺失最後幾筆寫入）。
- 特別適合 AI agents：bursty、experimental、per-agent 獨立 SQLite 檔案 = 簡單、便宜、fault isolation 好。

## 對比

| SQLite 方案 | Postgres 方案 |
|---|---|
| 零 infra | 需要獨立資料庫服務 |
| Per-agent 隔離 | Shared DB，需要管理連線 |
| Local disk，超低延遲 | Network hop |
| Litestream to S3 | 原生 replication |
| 適合 bursty/experimental | 適合 HA + shared scalability |

## 關鍵觀點

Durable 的是 state，不是 compute。Workflow progress 存在 execution log 裡，workers 可以隨時重啟、replay、retry。Postgres 仍是對的選擇當你需要 HA 和 shared scalability，但多數系統第一天不需要。
