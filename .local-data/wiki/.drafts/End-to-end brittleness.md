---
aliases:
- E2E brittleness
- 端到端脆性
confidence: 0.75
created: '2026-06-03'
sources:
- raw/2026-05-26-pwc-agent-harness.md
- raw/2026-05-29-sqlite-durable-workflows.md
status: draft
tags:
- end-to-end-brittleness
- agent-harness
- llm-agents
- retrieval
- programmatic-delivery
- system-design
title: End-to-end brittleness
updated: '2026-06-03'
---

## 定義

端到端脆弱性是指在複雜系統中，單個組件或模塊在獨立測試時表現出色，但當其整合到整個系統（例如，LLM的代理循環）中時，效能卻顯著下降甚至崩潰的現象。

## 背景與案例

普華永道（PwC）於2026年5月發表的一篇論文深入探討了Agent Harnesses如何重塑Agentic Search，並揭示了端到端脆弱性在其中扮演的關鍵角色。論文指出，雖然某些檢索方法（如針對[[Literal witnesses|literal witnesses]]的grep）在數據集中表現優異，但其交付方式對最終效能影響巨大。

### 程式化交付的陷阱

該研究發現，「程式化交付」（programmatic delivery）是一種容易導致端到端脆弱性的陷阱。例如，在使用Codex與GPT-5.4進行測試時，[[Inline vs Programmatic delivery|inline grep]]（將檢索結果直接插入對話）的準確率高達93.1%，而「程式化交付」（將檢索結果寫入文件讓模型自行讀取）的準確率卻驟降至55.2%，跌幅高達37.9個百分點。這表明，即使底層的retrieval組件本身很強大，其與Agent Harness的互動方式（即「交付」機制）也可能導致整個Agent loop的失敗。

## 關鍵概念與啟示

這一發現促使論文提出了「retrieval-plus-orchestration」的概念，強調在LLM代理時代，評估檢索系統效能時，必須將Agent Harness視為一級變因，而不僅僅是檢索算法本身。換言之，組件的獨立效能不再是唯一指標，其在整個系統上下文中的表現才是決定性的。

對於像Hermes這樣的Agent Harness設計者和使用者而言，這項研究具有重要啟發。它直接指出Hermes的設計選擇，如工具結果的交付方式（tool result delivery）和prompt structure，對最終效能的影響可能遠大於僅僅更換底層的LLM。因此，優化Agent Harness與各組件的整合，以避免端到端脆弱性，是提升LLM代理系統效能的關鍵。

## Sources
- [[PwC Agent Harness Reshapes Retrieval]]
- [[SQLite Durable Workflows]]

## See Also
- [[Inline vs Programmatic delivery]]
- [[Literal witnesses]]