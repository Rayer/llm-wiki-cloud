---
aliases:
- sqlite durable workflows
created: '2026-06-02'
quality: high
source_file: raw/2026-05-29-sqlite-durable-workflows.md
source_url: https://obeli.sk/blog/sqlite-is-all-you-need-for-durable-workflows/
status: published
tags:
- source
title: SQLite Durable Workflows
---

# SQLite Durable Workflows

## Summary
Obelisk針對DBOS「Postgres is all you need for durable execution」的主張，提出大多數durable systems使用SQLite即可的觀點。其核心邏輯在於workflow state的耐用性而非計算，並強調SQLite能提供具事務性的持久狀態且無需額外基礎設施。特別適合於AI agents等bursty、實驗性的應用，因其具備低延遲、每代理獨立檔案等優勢。

## Concepts
- [[Durable Workflows]]
- [[SQLite]]
- [[Postgres]]
- [[Retrieval-Augmented Generation]]
- [[End-to-end brittleness]]
- [[Inline vs Programmatic delivery]]
- [[LLM Wiki]]
- [[Literal witnesses]]

## Source Info
- **Quality:** high
- **Raw file:** raw/2026-05-29-sqlite-durable-workflows.md
- **Ingested:** 2026-06-02
- **URL:** https://obeli.sk/blog/sqlite-is-all-you-need-for-durable-workflows/