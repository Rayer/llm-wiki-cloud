---
aliases:
- durable execution
- durable systems
- workflow
confidence: 0.5
created: '2026-06-03'
sources:
- raw/2026-05-29-sqlite-durable-workflows.md
status: draft
tags:
- durable-workflows
- sqlite
- postgres
- database
- ai-agents
- distributed-systems
title: Durable Workflows
updated: '2026-06-03'
---

持久化工作流（Durable Workflows）旨在確保即使在計算節點故障或重啟的情況下，工作流的進度也能得到保存和恢復。傳統上，這類系統常依賴強大的分佈式數據庫如[[Postgres]]。然而，有觀點認為，對於許多持久化系統，輕量級的[[SQLite]]數據庫可能足以滿足需求，甚至提供獨特的優勢，這一概念在「[[SQLite Durable Workflows]]」中有所闡述。

<!-- olw-auto: single-source — cross-reference recommended -->

## 核心邏輯

持久化工作流的核心在於其**工作流狀態**的持久化，而非計算本身。這意味著執行工作流的Worker（工作節點）可以隨時重啟、重放（replay）或重試，因為工作流的進度（通常記錄在一個[[Operation Log]]中）是持久存儲的。

[[SQLite]]因其以下特性，被認為是實現持久化工作流的有力選擇：
*   **事務性持久狀態**：SQLite 提供本地的、事務性的持久化狀態存儲，確保數據一致性。
*   **零額外基礎設施**：與需要獨立數據庫服務的[[Postgres]]不同，SQLite 數據庫是一個文件，無需網絡跳轉或額外的服務器基礎設施，極大簡化了部署和管理。
*   **備份與遷移**：雖然SQLite是本地文件，但Litestream等工具可以實現異步（async）將SQLite數據流式傳輸到S3等雲存儲，以滿足備份、遷移和檢查需求。需要注意的是，異步流傳可能存在數據丟失的風險。
*   **適用於AI代理**：對於像AI代理這樣的工作負載，它們通常具有爆發性（bursty）、實驗性（experimental）的特點。為每個代理分配一個獨立的SQLite文件，可以實現簡單、廉價的故障隔離（fault isolation）和資源管理。

## 與Postgres的對比

在選擇持久化解決方案時，[[SQLite]]和[[Postgres]]各有優勢，適用於不同場景：

| 特性 | [[SQLite]] 方案 | [[Postgres]] 方案 |
|---|---|---|
| **基礎設施** | 零額外基礎設施，本地文件 | 需要獨立的數據庫服務器 |
| **隔離性** | Per-agent 隔離，故障影響範圍小 | 共享數據庫，需要管理連接和資源隔離 |
| **延遲** | 本地磁盤存取，超低延遲 | 存在網絡跳轉，延遲相對較高 |
| **備份/複製** | Litestream 異步流傳到雲存儲 | 原生支持數據複製 (replication) |
| **適用場景** | 爆發性/實驗性工作負載、輕量級、單機部署 | 高可用（HA）、共享可擴展性、需要強一致性的分佈式系統 |

## 關鍵洞察

歸根結底，持久化工作流的關鍵在於**狀態的持久化**，而非計算資源。工作流的進度存儲在執行日誌（execution log）中，使Worker能夠隨時重啟、重放和重試。儘管[[Postgres]]在需要高可用性和共享擴展性（HA and shared scalability）的場景中仍然是優選，但對於許多系統而言，尤其是在初期階段，對此類複雜性的需求並不明顯。在這些情況下，[[SQLite]]提供了一個簡單、高效且成本低廉的替代方案，足以實現穩健的持久化工作流。

## Sources
- [[SQLite Durable Workflows]]

## See Also
- [[Operation Log]]
- [[Postgres]]
- [[SQLite]]
- [[SQLite Durable Workflows]]