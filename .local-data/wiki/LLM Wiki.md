---
confidence: 0.75
created: '2026-06-03'
sources:
- raw/2026-06-01-llm-wiki-vs-rag.md
- raw/2026-05-29-sqlite-durable-workflows.md
status: published
tags:
- llm-wiki
- rag
- ai-agents
- durable-workflows
- sqlite
- knowledge-management
title: LLM Wiki
updated: '2026-06-03'
---

LLM Wiki 是一種知識管理哲學與系統，其核心目的在於將非結構化資訊轉化為結構化、可理解的維基文章，以便大型語言模型（LLM）更有效地學習與檢索。它與傳統的[[Retrieval-Augmented Generation]]（RAG）方法在處理哲學與實際應用上存在顯著差異。

## LLM Wiki 與 RAG 的核心差異

LLM Wiki 與 RAG 代表了兩種不同的內容處理與檢索哲學，適用於不同的工作負載：

### 處理時機與方式
*   **LLM Wiki**：在內容攝入階段（ingest time）完成理解與重組。此時，LLM 負責閱讀原始資料，並將其編譯成結構化的維基文章及概念條目。查詢時，模型直接使用完整的結構化文章作為上下文。
*   **[[Retrieval-Augmented Generation|RAG]]**：在查詢階段（query time）進行內容理解與拼湊。它將原始文件切割成小塊（raw chunks），並為其生成嵌入向量（embedding vectors）。查詢時，透過向量相似度（語意模糊）檢索相關的 top-k 分塊，再將這些碎片拼湊成上下文。

### 儲存與檢索
*   **LLM Wiki**：內容以結構化維基文章和概念條目的形式儲存，檢索方式為全文搜尋（字面精準），確保檢索到的資訊是完整且經過組織的整篇內容。
*   **[[Retrieval-Augmented Generation|RAG]]**：儲存方式為原始分塊與嵌入向量。檢索依賴向量相似度，可能導致檢索到的內容語意模糊且不連續。

### 關鍵哲學：理解與尋找

本質上，LLM Wiki 旨在「幫助模型理解內容」，將內容理解的責任前置到攝入階段。它將多篇相關文獻綜合、提煉並結構化為一篇完整的文章，讓模型在回答問題時能直接獲取全面、深度的知識。例如，將三篇關於 Agent Retrieval 的論文合成一篇「Agent Harness Study」，完整記錄了實驗與結論，無論問何問題，LLM 都能基於這篇綜合文章給出全面回答。

相比之下，[[Retrieval-Augmented Generation|RAG]] 則旨在「幫助模型尋找相關片段」。它在查詢時動態地檢索並拼湊內容，依賴嵌入向量的猜測能力。這種方法在處理海量文件時具有優勢，但可能導致上下文的零碎化，甚至遺漏關鍵資訊，如忽略「programmatic delivery」等重要實驗細節。

### 適用場景

選擇 LLM Wiki 或 [[Retrieval-Augmented Generation|RAG]] 並非優劣之分，而是取決於具體需求：
*   **LLM Wiki** 更適合處理數量有限（例如幾百篇）但需要深度理解和整合的「你真的想讀懂」的文章。其生成內容的品質遠超 [[Retrieval-Augmented Generation|RAG]]。
*   **[[Retrieval-Augmented Generation|RAG]]** 則更適用於處理百萬篇文件規模的場景，是此類大規模知識庫的唯一可行選擇。

## 與持久化工作流的結合

在構建利用 LLM Wiki 知識庫的 AI agents 或其他系統時，[[Durable Workflows]] 的設計至關重要。對於許多需要持久化工作流狀態的系統，特別是那些具有突發性（bursty）、實驗性（experimental）且每個代理具有獨立狀態的場景，[[SQLite]] 是一個極具吸引力的選擇。它能提供事務性的持久化狀態，且無需獨立的資料庫服務或網路跳轉，極大簡化了基礎設施管理。這種「[[SQLite Durable Workflows]]」模式透過將工作流進度儲存在執行日誌中，允許 workers 隨時重啟、重放和重試，而無需共享複雜的資料庫資源，為 AI agents 等應用提供了簡單、便宜且高故障隔離的解決方案。

## Sources
- [[LLM Wiki vs RAG]]
- [[SQLite Durable Workflows]]

## See Also
- [[Durable Workflows]]
- [[Retrieval-Augmented Generation]]
- [[SQLite]]
- [[SQLite Durable Workflows]]