---
aliases:
- R+O
- 檢索與編排
confidence: 0.5
created: '2026-06-03'
sources:
- raw/2026-05-26-pwc-agent-harness.md
status: published
tags:
- retrieval
- orchestration
- agent-harness
- llm
- benchmarking
- ai-agents
title: Retrieval-plus-orchestration
updated: '2026-06-03'
---


## 概述
「檢索加編排」（Retrieval-plus-orchestration）是一個強調在代理時代中，檢索的效能評估必須將代理線束（Agent Harness）視為首要變因的概念。這意味著，檢索策略的成功不僅取決於其本身的精確性，更深受代理如何編排、利用和呈現檢索結果的影響。

## 背景與緣起
此概念源自 [[PwC Agent Harness Reshapes Retrieval]] 於2026年5月發表的一篇論文。該研究在 LongMemEval 基準測試上，以116道題目實測了 grep 與向量檢索（Vector Retrieval），並搭配四種不同的代理線束（Chronos、Claude Code、Codex、Gemini CLI），旨在探討不同檢索方法和代理線束對代理搜尋（Agentic Search）表現的影響。

## 核心發現與概念
該論文指出，在代理時代，檢索的評估不能脫離代理線束的編排能力。其核心發現與相關概念包括：

1.  **代理線束差異大於檢索差異**：研究發現，即使使用相同的語言模型與檢索方法（例如 Claude Opus 4.6 搭配 grep），不同的代理線束會導致顯著的性能差異。舉例來說，Chronos 代理線束的表現為 93.1%，而 Claude Code 僅為 76.7%，兩者之間存在 16.4 個百分點的巨大差距。這項發現直接促成了「檢索加編排」概念的提出，強調代理線束的設計和執行方式，對最終效能的影響可能比單純的檢索方法選擇更為關鍵。

2.  **Grep 在特定任務中優於向量檢索**：研究發現，對於依賴「[[Literal witnesses|字面證據]]」（[[Literal witnesses]]），如日期、命名、ID 等任務，grep 檢索方法在所有五組「線束+模型」配對中均優於基於檢索增強生成（[[Retrieval-Augmented Generation]]，簡稱 RAG）的向量檢索，最大差距達到 23.3 個百分點。這是因為嵌入式檢索（embedding retrieval）可能會對這些細節造成有損壓縮，導致重要資訊丟失。

3.  **程序化交付的陷阱**：論文發現，將檢索結果以程序化交付（Programmatic delivery）的方式提供給模型（例如將內容寫成檔案，讓模型自行讀取），相較於[[Inline vs Programmatic delivery|內聯交付]]（Inline delivery，直接塞回對話中），會導致嚴重的性能下降。例如，Codex 搭配 GPT-5.4 時，內聯 grep 的表現為 93.1%，而程序化交付則跌至 55.2%（下降 37.9 個百分點）。此現象被解釋為「端到端脆弱性」（[[End-to-end brittleness]]），意指單個組件表現良好，但整合進代理循環後卻容易崩潰。

這些發現共同闡釋了「檢索加編排」的核心思想，即檢索與代理線束編排之間密不可分的關係。

## 局限性
該研究存在一些局限性，包括僅有 116 道題目和一個基準測試（對字面證據任務有利），未測試混合檢索（grep + 向量），以及對代理線束差異的歸因不夠詳細。

## 啟示
對於 Agent Harness 的設計者而言，這篇論文直接指出其設計選擇（例如工具結果的交付方式、提示結構）對最終效能的影響，可能比更換大型語言模型（LLM）本身還要顯著。

## Sources
- [[PwC Agent Harness Reshapes Retrieval]]

## See Also
- [[End-to-end brittleness]]
- [[Inline vs Programmatic delivery]]
- [[Literal witnesses]]
- [[PwC Agent Harness Reshapes Retrieval]]
- [[Retrieval-Augmented Generation]]