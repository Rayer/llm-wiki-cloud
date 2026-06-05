---
aliases:
- 字面線索
- 字面證據
confidence: 0.75
created: '2026-06-03'
sources:
- raw/2026-05-26-pwc-agent-harness.md
- raw/2026-05-29-sqlite-durable-workflows.md
status: published
tags:
- literal-witnesses
- retrieval
- agent-harness
- rag
- embedding
- search-strategy
title: Literal witnesses
updated: '2026-06-03'
---

## 什么是字面证据？
字面证据（Literal witnesses）指的是信息中具有明确、具体形式的细节，例如日期、命名、特定ID等。这些信息往往是准确回答特定问题（尤其是在事实性检索任务中）的关键。

## 在检索中的重要性
在[[PwC Agent Harness Reshapes Retrieval]]于2026年5月发表的一篇论文中，通过在LongMemEval基准测试中对比grep与向量检索的表现，凸显了字面证据的重要性。该研究发现，对于那些答案依赖于字面证据（如日期、命名、ID）的问题，grep方法全面优于向量检索（[[Retrieval-Augmented Generation]]），最大差距达到23.3个百分点。

研究指出，embedding（嵌入）技术在将文本转化为向量时，本质上是一种有损压缩过程，它可能会模糊或丢失这些对事实性检索至关重要的细微字面信息。这意味着，虽然嵌入在捕捉语义相似性方面表现出色，但对于精确匹配特定字面证据的任务，其效果可能不如直接的字符串匹配（如grep）。

## 对检索增强生成（RAG）和代理系统的启发
这一发现对于[[Retrieval-Augmented Generation]]（RAG）系统和[[LLM Wiki]]等知识库的构建具有重要启示。它表明，在设计检索策略时，不应完全依赖于向量检索。对于需要识别和匹配精确事实的任务，结合或优先考虑能够处理字面证据的方法（例如关键词搜索、正则表达式或全文搜索）至关重要。

论文提出的“[[retrieval-plus-orchestration]]”概念进一步强调，检索组件的效能不能脱离整体的Agent Harness环境进行评估。在代理系统中，如何有效利用检索到的字面证据，并将其以“[[Inline vs Programmatic delivery]]”的方式传递给大型语言模型，将直接影响最终性能，避免出现“[[End-to-end brittleness]]”。因此，一个平衡的检索策略应该能够根据任务需求，灵活选择或结合利用语义相似性和字面证据匹配的方法。

## Sources
- [[PwC Agent Harness Reshapes Retrieval]]
- [[SQLite Durable Workflows]]

## See Also
- [[End-to-end brittleness]]
- [[Inline vs Programmatic delivery]]
- [[LLM Wiki]]
- [[PwC Agent Harness Reshapes Retrieval]]
- [[Retrieval-Augmented Generation]]
- [[retrieval-plus-orchestration]]