---
aliases:
- Postgres方案
confidence: 0.5
created: '2026-06-03'
sources:
- raw/2026-05-29-sqlite-durable-workflows.md
status: published
tags:
- database
- durable-workflows
- relational-database
- high-availability
- scalability
title: Postgres
updated: '2026-06-03'
---


## Postgres 在持久化工作流中的應用

Postgres 是一個廣受使用的開源關聯式資料庫管理系統，在各種應用中提供強大的資料持久化和可靠性。在持久化工作流 (durable workflows) 的背景下，有觀點主張「Postgres 是你唯一需要的持久化執行方案」(Postgres is all you need for durable execution)，這強調了其在確保系統狀態持久性方面的核心作用。

### 核心特性與優勢

在需要高可用性 (High Availability, HA) 和共享可擴展性 (shared scalability) 的情境下，Postgres 通常是首選：

*   **獨立資料庫服務**：Postgres 作為一個獨立的資料庫服務運行，能夠為多個應用或工作流提供集中式的資料管理。這與將資料庫嵌入應用程式的方案（如 [[SQLite]]）形成對比。
*   **共享資料庫**：它支援共享資料庫模型，意味著多個工作流或應用組件可以存取同一個資料庫實例，這需要進行有效的連線管理。
*   **網路跳躍 (Network Hop)**：由於它是一個獨立的服務，應用程式與Postgres 之間的通訊會涉及網路跳躍，這可能會引入輕微的延遲，但在大多數大規模部署中，其優勢足以彌補這點。
*   **原生複製 (Native Replication)**：Postgres 提供原生的資料複製機制，確保資料冗餘和故障轉移能力，這對於實現高可用性至關重要。
*   **高可用性與共享可擴展性**：這些特性使Postgres 非常適合需要高彈性、大規模共享資源以及嚴格資料一致性保證的系統。它能夠處理高併發負載並在多個應用組件之間共享持久化狀態。

### 持久化工作流中的角色

在持久化工作流中，關鍵在於工作流狀態的持久化，而不是計算本身的持久化。工作流的進度通常記錄在執行日誌 (execution log) 中，這類似於一個[[Operation Log]]。即使工作者節點 (workers) 重新啟動、重播或重試，也能從上次已知狀態恢復。Postgres 透過其事務性保證，能夠可靠地儲存這些工作流狀態和操作日誌，從而確保即使在系統故障時，工作流也能從上次已知狀態恢復。

儘管在許多小型或試驗性場景中，[[SQLite]] 因其零基礎設施需求和低延遲而被認為足夠，但對於需要企業級穩定性、高併發支援和複雜資料管理能力的系統，Postgres 仍然是實現持久化工作流的堅實選擇。

## Sources
- [[SQLite Durable Workflows]]

## See Also
- [[Operation Log]]
- [[SQLite]]