# LLM Wiki Cloud — MVP

> 🚧 **施工中** — 下面講的是 mvp-1 的目標，有些東西還沒做完。[目前進度看這邊](#目前進度)
>
> **Demo**：[llm-wiki-frontend.vercel.app](https://llm-wiki-frontend.vercel.app)（前端）｜ BFF 跑在 Cloud Run asia-east1

---

## 這到底是什麼

老實講，就是一個「AI 幫你維護知識庫」的服務。

你丟素材進去（文章、論文、筆記、網頁存下來的內容），它自動幫你讀懂、消化、重組成結構化的 wiki。然後你可以用自然語言問它問題，它會從你的 wiki 裡找答案，並且告訴你答案是從哪幾篇文章來的——點進去就能看原文。

核心想法很簡單：**先讓 AI 把知識整理好，再讓 AI 幫你查。** 不是那種「丟一堆文件進去、查詢時才匆匆翻幾頁」的 RAG。

---

## 為什麼這樣做

這個點子是 Andrej Karpathy 2024 年提出的 LLM Wiki 概念。

他說了一句我很 buy 的話：傳統 RAG 就像圖書館的書全部堆在地上，每次有人問問題才匆匆翻幾頁——運氣好找到相關段落，運氣不好漏掉關鍵上下文。

LLM Wiki 的做法相反：素材進來的當下，就讓 LLM 讀懂、消化、重寫成結構化知識。查詢時整篇相關文章直接餵給 LLM，不用 chunk、不用 embedding、不用擔心 retrieval 漏掉什麼。

說真的，我自己用了 OLW 半年多，最深的體感就是這個：**compile 的成本是一次性的，但 query 的品質是永久的。** 跟 RAG 那種每次查詢都在賭 retrieval 品質的體驗完全不一樣。

| | RAG | LLM Wiki |
|---|---|---|
| **LLM 什麼時候工作** | 每次查詢都要跑 | 素材進庫時跑一次 |
| **知識的狀態** | 原始素材，沒消化 | 結構化 wiki，已重組 |
| **查詢的時候** | 向量檢索 → 撈碎片 → 拼答案 | grep 找文章 → 整篇餵 LLM |
| **LLM 看到的** | 幾個 chunk（可能缺上下文） | 整篇文章（脈絡完整） |
| **東西變多的時候** | 檢索品質下降，越難找到對的 | 知識網路越密，越有價值 |

---

## 跟 NotebookLM 比

NotebookLM 是 Google 的 AI 筆記工具。功能很強，UI 很漂亮，但有一個我覺得是結構性限制的東西：**50 篇上限**。

我的 LifestyleWiki 目前 303 條目。直接卡死。

| | NotebookLM | LLM Wiki Cloud |
|---|---|---|
| **容量** | 50 sources / notebook | GCS 幾乎無限 |
| **跨來源查詢** | 單 notebook 內 | 全庫 |
| **入庫方式** | 手動上傳文件 | Pipeline 自動化（貼文就 compile） |
| **知識結構** | 文件列表 | wiki 文章 + concept + 內部連結 |
| **累積效果** | 線性（每個 notebook 獨立的） | 複利（越多 interconnect 越密） |
| **API key** | Google 出算力 | 你自己的 key |
| **資料** | 在 Google 伺服器 | 在你的 GCS bucket |

一句話：NotebookLM 是「AI 幫你讀 50 篇文件」，LLM Wiki Cloud 是「AI 幫你養一套活的知識庫」。前者有天花板，後者沒有。

---

## 跟 RAG 資料庫比

市面上很多 RAG 知識庫（Anything LLM、Dify、ChatPDF…）。它們的做法幾乎一樣：丟文件 → chunk → embedding → vector DB → 查詢時檢索 → LLM 回答。

我不禁思考，這架構有三個我覺得很致命的問題：

### Chunking 把上下文切爛了

一篇文章切成 500-token 的 chunk，每個 chunk 都不知道自己前後是什麼。LLM 看到的是一堆碎片，問它一個需要跨段落理解的問題，它可能把兩段不相干的拼在一起。

LLM Wiki 不切。compile 的時候 LLM 整篇讀懂、重寫成更有結構的版本。查詢時整篇餵，脈絡是完整的。

### Retrieval 是瓶頸，而且你還看不到它在幹嘛

向量檢索的品質直接等於答案的品質。但 embedding 是黑盒子——相關的沒 match 到，你不知道；不相干的 match 到了，你也不知道。只能相信那個分數。

LLM Wiki 用 grep。關鍵字匹配比向量更可預測，也更容易 debug——你用一樣的關鍵字搜，一定拿到一樣的結果。

### 知識不會累積

RAG 資料庫 = 一堆原始文件的 embedding。加越多文件，檢索雜訊越多。知識量跟品質成反比。這很諷刺——你花時間累積知識，系統卻越難幫你找到對的東西。

LLM Wiki 的知識庫 = compile 過的 wiki。加越多文章，concept 之間的連結越密。知識量跟品質是正相關的。

---

## 怎麼做

### Pipeline：素材 → wiki

```
raw/*.md  →  OLW ingest  →  OLW compile  →  wiki/source/*.md
                                              wiki/concept/*.md
```

OLW（obsidian-llm-wiki）是我自己用了一年多的 CLI 工具。讀 raw markdown → LLM 理解重組 → 輸出結構化 wiki 文章 + concept 條目。全部的 LLM 工作在 compile 時做完，查詢的時候除了最後的 synthesis 之外零 LLM 成本。

### Query：不只是 Q&A

```
你問：「RAG 跟 LLM Wiki 的核心差異是什麼？」
     │
     ▼
BFF 用 grep 搜 wiki/source/ + wiki/concept/
     │
     ▼
取 top 3-5 篇整篇文章
     │
     ▼
整篇餵 LLM → 生成答案，附 citations 連回原文
```

不 chunk、不 embedding、不用 vector DB。LLM 看到的是完整文章。

### 答案不只是答案，是入口

這跟一般 AI chatbot 最大的差別：你拿到答案之後，可以沿著 citation 追回去看原文。而且原文不是終點——wiki 裡面的 concept 彼此有連結，你可以從一篇文章逛到相關主題。

```
🤖 AI Answer
「RAG 在查詢時才從原始素材撈片段，
  LLM Wiki 則在素材入庫時就讓 LLM 讀懂、
  重寫成結構化 wiki [1]。」

📎 [1] RAG-vs-LLM-Wiki  ← 點進去就是那篇 wiki 文章
                        看完還可以從 concept 連結逛下去
```

三個關鍵：

- **LLM 合成**：答案不是關鍵字匹配的結果，是 LLM 讀完相關 wiki 文章後用自己的話總結的
- **Citation 溯源**：每句論點都標來源，點擊直達原始 wiki 頁面。trust but verify
- **Wiki 探索**：點進 citation 之後不是死路——從 concept 連結繼續逛整個知識庫

NotebookLM 會給你引用，但你沒辦法沿著引用逛整個知識庫（它沒有 wiki 結構）。RAG 資料庫會給你片段拼湊的答案，但沒有完整原文可以回頭讀。LLM Wiki Cloud 兩個都要。

### 兩種查詢模式

看你要完全信任 wiki，還是讓 LLM 自由補充：

| Mode | 行為 | 什麼時候用 |
|------|------|-----------|
| **Wiki Only** | 只用 wiki 內容回答。wiki 裡沒有的就說「我不知道」。每句話都 cite | 你要零幻覺的時候——每個主張都有 wiki 文章背書 |
| **Full** | wiki 當基礎，LLM 可補充外部知識。wiki 來的會 cite，外部的不 cite | 你要最完整答案的時候——wiki 沒 cover 的讓 LLM 補 |

```
Wiki Only:  「根據你的 wiki，RAG 的做法是先做 embedding [1]...」
            └─ 全部都有 citation

Full:       「你的 wiki 提到 RAG 用 embedding [1]。
             補充一下，2026 有些團隊開始用 late interaction
             取代 embedding...」
            └─ [1] 來自 wiki，補充段落是 LLM 外部知識
```

重點是**可追溯**——你永遠知道答案的每一句話是從 wiki 來的、還是 LLM 自己講的。

### 技術棧

| 層 | 用什麼 | 說明 |
|---|---|---|
| Pipeline | Cloud Run Job (Python) | OLW CLI + GCS sync |
| BFF | Go + Gin (Cloud Run) | grep search + LLM synthesis |
| Frontend | Next.js 16 (Vercel) | Query + Browse |
| Storage | GCS | `users/{uid}/projects/{pid}/` |
| Lock | Firestore | 避免 concurrent worker 打架 |
| LLM | DeepSeek | compile: v4-pro / query: v4-pro |

---

## 目前進度

| 模組 | 狀態 | 說明 |
|------|------|------|
| **Pipeline worker** | ✅ | Cloud Run Job，手動觸發 |
| **BFF** | ✅ | 6 endpoints + LLM query + citations |
| **Frontend** | ✅ | Query tab + Browse tab + AI answer |
| **Storage** | ✅ | GCS multi-tenant，demo project 有 300+ 條目 |
| **Worker 參數化** | ❌ | 目前寫死 test-user/demo，要改成動態 |
| **Pipeline API** | ❌ | BFF 還沒有上傳 raw / 觸發 pipeline 的 endpoint |
| **Manage UI** | ❌ | 前端還沒有 Manage tab |

### mvp-1 剩什麼

1. BFF 加 `POST /api/raw` 和 `POST /api/pipeline/run`
2. Worker 支援動態 UID/PID
3. Frontend Manage tab（貼 raw + 觸發 compile + 看狀態）
4. End-to-end 驗證：丟一篇 raw → compile → wiki 出現 → query 打得到
5. 三個 repo 打 mvp-1 tag

---

## 名字的由來

- **LLM** — 核心能力
- **Wiki** — 產出是結構化、可逛、內部有連結的知識庫
- **Cloud** — 雲端，任何裝置都能用

跟 OLW 的關係：OLW 是 local CLI，LLM Wiki Cloud 是它的雲端版。同一套 compile 邏輯，從本機 Obsidian vault 搬到 GCS。
