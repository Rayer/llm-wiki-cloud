---
aliases:
- Inline delivery
- Programmatic delivery
- programmatic delivery
- 內聯交付
- 程式化交付
confidence: 1.0
created: '2026-06-03'
sources:
- raw/2026-05-26-pwc-agent-harness.md
- raw/2026-06-01-llm-wiki-vs-rag.md
- raw/2026-05-29-sqlite-durable-workflows.md
status: draft
tags:
- llm-agents
- agent-harness
- retrieval-augmented-generation
- prompt-engineering
- end-to-end-brittleness
title: Inline vs Programmatic delivery
updated: '2026-06-03'
---

「內聯交付」（Inline delivery）與「程式化交付」（Programmatic delivery）是 大型語言模型 代理（LLM Agent）在接收工具結果時的兩種主要方式，尤其在 檢索增強生成（RAG）或 Agent Harness 的背景下。PwC 於 2026 年 5 月發表的一篇論文深入探討了這兩種方法的效能差異。

## 概念定義

*   **內聯交付**：指將工具（如 `grep`）的執行結果直接嵌入到代理模型的對話或提示（prompt）中。模型會直接在對話流程中看到這些結果，就像使用者發言的一部分。這種方法通常更直接，減少了額外的處理步驟。
*   **程式化交付**：指將工具的執行結果寫入到一個文件中（例如，儲存為一個檔案），然後讓代理模型透過讀取該檔案來獲取資訊。這通常涉及代理在其工作流程中「程式化地」打開、讀取並處理這些檔案。

## 效能差異與「端到端脆性」

PwC 的研究在評估 Agent Harness（如 Chronos、Claude Code、Codex、Gemini CLI）效能時，揭示了內聯與程式化交付之間的顯著差異。

在一項實驗中，使用 Codex 搭配 GPT-5.4 進行測試：
*   當採用內聯 grep 方式時，模型的成功率達到 93.1%。
*   當改用程式化交付時，成功率驟降至 55.2%，差距高達 37.9 個百分點。

這項發現表明，儘管各個組件（例如，檢索工具本身）可能表現出色，但將它們整合到 LLM Agent 的迴圈中時，可能會出現嚴重的效能下降。論文將此現象解釋為「[[End-to-end brittleness]]」（端到端脆性），即單一組件看起來很完美，但一旦整合到代理系統中就會崩潰。

因此，程式化交付在某些情況下可能成為一個「陷阱」，它雖然看起來更具結構性或可擴展性，但實際操作中卻會引入額外的複雜性和失敗點，導致代理的整體效能不如直接的內聯方式。

## 啟發

這項研究對於像 Hermes 這樣作為 Agent Harness 的系統具有重要啟發。它強調了工具結果交付方式和提示結構（Prompt Engineering）對最終代理效能的影響，甚至可能比選擇不同 LLM 模型本身更為關鍵。在設計代理系統時，仔細考慮工具結果的交付機制至關重要。

## Sources
- [[PwC Agent Harness Reshapes Retrieval]]
- [[LLM Wiki vs RAG]]
- [[SQLite Durable Workflows]]

## See Also
- [[End-to-end brittleness]]