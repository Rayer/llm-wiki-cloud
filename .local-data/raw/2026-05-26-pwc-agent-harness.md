---
title: "PwC Agent Harness Reshapes Retrieval"
date: "2026-05-26"
source: "https://arxiv.org/abs/2605.15184"
language: "zh"
---

# Is Grep All You Need? How Agent Harnesses Reshape Agentic Search

PwC 在 2026 年 5 月發表了一篇論文，在 LongMemEval 上用 116 題實測 grep vs vector retrieval，搭配四個 Agent Harness（Chronos、Claude Code、Codex、Gemini CLI）。

## 核心發現

1. **Grep 全面壓制 Vector RAG** — 5 組 harness+model 配對，grep 全勝。最大差距 +23.3pp。原因是 LongMemEval 的答案依賴「literal witnesses」——日期、命名、ID 等字面證據，embedding 反而是有損壓縮。

2. **Harness 差異 > 檢索差異** — 同樣 Claude Opus 4.6 + grep：Chronos 93.1% vs Claude Code 76.7%（差 16.4pp）。論文提出「retrieval-plus-orchestration」概念：在 Agent 時代，retrieval benchmark 必須把 harness 當成一級變因。

3. **Programmatic delivery 是陷阱** — Codex + GPT-5.4：inline grep 93.1% → programmatic 55.2%（跌 37.9pp）。論文解釋為「end-to-end brittleness」：component 漂亮但接進 Agent loop 就崩。

## 關鍵概念

- **Literal witnesses**：字面證據（日期、命名、ID），embedding 糊掉細節
- **Retrieval-plus-orchestration**：檢索不能脫離 Harness 評估
- **End-to-end brittleness**：單看 component 漂亮，接進 Agent loop 就崩
- **Inline vs Programmatic delivery**：直接塞回對話 vs 寫成檔案讓模型自己讀

## 限制

- 116 題，一個 benchmark（literal 任務對 grep 有利）
- 沒測 hybrid（grep + vector）
- Harness 差異歸因不夠細

## 對 Hermes 使用者的啟發

Hermes 本身就是一個 Agent Harness——這篇論文直接點出 Hermes 的設計選擇（tool result delivery、prompt structure）對最終效能影響可能比換模型還大。
