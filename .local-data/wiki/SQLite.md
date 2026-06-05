---
aliases:
- SQLite方案
- SQLite檔案
confidence: 0.5
created: '2026-06-03'
sources:
- raw/2026-05-29-sqlite-durable-workflows.md
status: published
tags:
- sqlite
- durable-workflows
- database
- ai-agents
- infrastructure
- litestream
title: SQLite
updated: '2026-06-03'
---

SQLite 是一個輕量級的關係型資料庫管理系統，以其零配置、無伺服器架構和內嵌特性而聞名。在處理[[Durable Workflows]]時，它被提出為一個強大的替代方案，尤其是在許多情況下足以替代更重量級的資料庫如[[Postgres]]。


## 核心概念

對於持久性工作流，關鍵在於**工作流狀態**的持久性，而非計算本身的持久性。這意味著工作負載的執行者（workers）可以隨時重啟、重播或重試，因為它們的進度儲存在持久性的執行日誌中。

SQLite透過其事務性持久狀態（transactional durable state）提供此能力，且無需網路跳轉或獨立的資料庫服務。這代表著「零額外基礎設施」：沒有獨立的資料庫伺服器需要管理。

為了備份、遷移和檢查數據，可以利用Litestream工具將SQLite資料庫異步串流到像S3這樣的雲端儲存服務。需要注意的是，由於是異步操作，在極端情況下可能會丟失最後幾筆寫入的數據。

## 優勢與適用場景

SQLite方案特別適合以下場景：
*   **AI agents**：對於間歇性（bursty）、實驗性的AI代理，每個代理可以擁有一個獨立的SQLite檔案。這種「per-agent」隔離帶來了簡單性、成本效益和良好的故障隔離。
*   **低延遲需求**：由於資料庫直接儲存在本地磁碟上，SQLite能提供超低的延遲。
*   **簡單的基礎設施**：無需獨立的資料庫服務，部署和管理成本極低。

## 對比 Postgres

雖然[[Postgres]]在需要高可用性（HA）和共享可擴展性時仍然是合適的選擇，但SQLite在許多方面提供了引人注目的替代方案，特別是對於那些不需要第一天就具備這些特性的系統：

| SQLite 方案 | [[Postgres]] 方案 |
|---|---|
| 零基礎設施 | 需要獨立資料庫服務 |
| 每代理隔離 | 共享資料庫，需要管理連接 |
| 本地磁碟，超低延遲 | 網路跳轉 |
| Litestream 至 S3 | 原生複製功能 |
| 適合間歇性/實驗性負載 | 適合高可用性 + 共享可擴展性 |

總結來說，當系統需要持久化狀態而無需複雜的共享基礎設施或極端可擴展性時，SQLite提供了一個高效、低成本且易於管理的解決方案。

## Sources
- [[SQLite Durable Workflows]]

## See Also
- [[Durable Workflows]]
- [[Postgres]]