---
aliases:
- RAG
confidence: 0.75
created: '2026-06-03'
sources:
- raw/2026-06-01-llm-wiki-vs-rag.md
- raw/2026-05-29-sqlite-durable-workflows.md
status: published
tags:
- retrieval-augmented-generation
- rag
- large-language-models
- information-retrieval
- artificial-intelligence
- llm-applications
title: Retrieval-Augmented Generation
updated: '2026-06-03'
---

Retrieval-Augmented Generation (RAG) 是一種在大型語言模型（LLM）應用中提升準確性和可靠性的技術。其核心思想是在語言模型生成回應之前，從外部知識庫中檢索相關資訊，並將其作為額外上下文提供給LLM。

## 運作原理與哲學
RAG 的處理時機在於**查詢階段**（Query time）。它預先將大量原始資料分割成raw chunks，並為這些區塊生成embedding vectors以儲存。當用戶提出查詢時，RAG 會利用這些embedding vectors，通過vector similarity（語意模糊匹配）的方式，從知識庫中檢索出與查詢最相關的數個區塊（top-k chunks）。隨後，這些檢索到的資訊會與原始查詢一同作為上下文，輸入到LLM中以生成最終回應。

RAG 的本質是將「理解內容」的責任放在查詢階段。它每次都依賴embedding來「猜測」哪些區塊與查詢相關，然後將這些碎片化的資訊拼湊起來構成上下文。

## 與 [[LLM Wiki]] 的比較
RAG 與 [[LLM Wiki]] 代表了兩種不同的內容處理哲學：
*   **RAG**: 在查詢時進行處理，儲存raw chunks和embedding vectors，透過vector similarity進行檢索，並將多個頂級區塊拼湊成上下文。它旨在「幫你找」相關資訊。
*   **[[LLM Wiki]]**: 在**攝入階段**（Ingest time）進行處理，將內容整理成結構化的wiki articles和概念條目，使用full-text search（字面精準）進行檢索，並直接提供整篇完整的文章作為上下文。它旨在「幫你懂」內容。

這兩種方法並無優劣之分，而是適用於不同的工作負載：
*   如果你的文件數量達到百萬級，RAG 通常是唯一的實用選擇。
*   如果你的文件數量在幾百篇左右，且你希望LLM能「真正讀懂」並綜合這些文章，那麼[[LLM Wiki]]所能提供的品質是 RAG 難以匹敵的。

## 實際應用效果
以處理關於Agent Retrieval的三篇論文為例：
*   當使用 RAG 詢問「grep 與 vector 誰更優」時，它可能會檢索到相關片段，但有可能會遺漏關鍵的「programmatic delivery」實驗細節。
*   而通過[[LLM Wiki]]的編譯階段，這三篇論文的內容可能已被合成一篇「Agent Harness Study」文章，其中完整記錄了所有實驗和結論，無論問什麼都能提供全面且連貫的回答。

簡而言之，Retrieval-Augmented Generation（RAG）主要負責幫助用戶「找到」相關資訊，而[[LLM Wiki]]則側重於幫助大型語言模型「理解」內容的深層含義。

## Sources
- [[LLM Wiki vs RAG]]
- [[SQLite Durable Workflows]]

## See Also
- [[LLM Wiki]]