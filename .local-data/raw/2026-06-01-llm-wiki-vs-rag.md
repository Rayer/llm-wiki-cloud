---
title: "LLM Wiki vs RAG"
date: "2026-06-01"
source: "internal"
language: "zh"
---

# LLM Wiki 與 RAG 的核心差異

## 兩種哲學

### RAG（Retrieval-Augmented Generation）
- **處理時機**：Query time
- **儲存方式**：Raw chunks + embedding vectors
- **檢索方式**：Vector similarity（語意模糊）
- **Context**：拼湊 top-k chunks

### LLM Wiki
- **處理時機**：Ingest time
- **儲存方式**：結構化 wiki 文章 + concept 條目
- **檢索方式**：Full-text search（字面精準）
- **Context**：整篇完整文章

## 關鍵差異

RAG 把「理解內容」的責任放在 query 階段——每次問都要靠 embedding 去猜哪些 chunk 相關，然後把碎片拼起來。LLM Wiki 把「理解內容」放在 ingest 階段——compile 的時候就讓 LLM 讀懂、重組、寫成結構化文章。Query 的時候直接整篇餵進去。

這不是「誰比較好」的問題——是 workload 適合哪種的差別：
- 如果你有一百萬篇文件，RAG 是唯一選擇
- 如果你有幾百篇「你真的想讀懂」的文章，LLM Wiki 的品質無法被 RAG 追趕

## 實際效果

假設你存了三篇關於 Agent Retrieval 的論文：
- RAG 問「grep 跟 vector 誰贏」會撈到相關片段，可能漏掉關鍵的「programmatic delivery」實驗
- LLM Wiki 的 compile 階段已經把三篇合成一篇「Agent Harness Study」，裡面完整記錄了三個實驗和結論。問什麼都答得出來

本質上：**RAG 幫你找，LLM Wiki 幫你懂。**
