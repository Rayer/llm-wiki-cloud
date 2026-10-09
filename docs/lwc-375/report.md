# LWC-375 hermes-key-contract-r1 report

## Outcome

已完成此 dispatch 授權的文件交付：`spec.md` 定義 LWC-377 可直接採用的 project key 產品/工程契約、Firestore schema、HTTP 接口、owner-only Web flow、拒絕條件、寫入結果不明恢復規則，以及逐項驗收矩陣。修改僅限 `docs/lwc-375/`；沒有修改 BFF、Auth、前端、CaC 或其他產品檔案，也沒有發出真 key。

## Source and authority

- Worktree: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-375-project-key-contract`
- Dispatch 起始 HEAD: `26527e79376e57756842b210e5a57eaa48526d9f`; 起始 `git status --short` 為乾淨。
- `apps/bff/cmd/bff/main.go` 顯示 BFF v1 先掛 `AccountAuth`，再由同一 group 掛 `ProjectMiddleware`；因此 Query-only key 必須是只註冊在 `POST /api/v1/query` 的獨立 auth seam，不能掛回一般 v1 group。
- `apps/bff/internal/auth/project.go` 是現有 Firestore project-owner authority，處理 stable project ID 與舊文件格式；`auth/jwt.go`、`auth/account.go` 和 `auth/user_store.go` 提供 Web/CLI 分流與現行帳戶狀態。
- `apps/bff/internal/handler/v1/endpoints.go`、`query_profile.go` 將 query 上下文固定為 `userID` + `projectID`，所以 key middleware 從已驗證記錄寫入這兩個 context 值，並在 query executor 前拒絕任何 header scope mismatch。
- `apps/bff/internal/firestore/scope.go` 的 `Collection` 可讓新 `project_keys` 頂層記錄沿用目前 Firestore database 及 worktree scope。動態 key record 不需要新增 CaC 設定；不把 key 或 secret 放入 Pkl/generated config。
- `apps/frontend/src/components/WorkspaceProvider.tsx` 暴露 `currentProject`，`AccountSettingsModal.tsx` 是既有 Web 自助管理界面，`src/lib/api.ts` 是 BFF Bearer/project header seam；`src/lib/cli-auth.ts` 則屬 Auth CLI API，契約明確不共用。

## Ticket and review limits

指定 YouTrack reader `python3 /Users/rayer/.hermes/workflows/lwc/handoff.py youtrack LWC-375` 回報 inherited environment 缺少 `YOUTRACK_BASE_URL` 與 `YOUTRACK_TOKEN`。依 dispatch 指示未搜尋其他 profile；本票提供的 Owner 決策足以完成契約，文件將來源限制記錄為執行脈絡，不將 ticket 尚未讀取升為 gate。

獨立 review `dfeb023d714ebe33a9a61d09` 對本設計/scope 的結果是 **PASS**；它不是產品實作、測試或 live acceptance PASS。

## Verification status

- 文件內容已按目前 source 接縫對照；沒有更動產品程式碼，也沒有新增或執行產品測試。
- `spec.md` 內完整驗收矩陣目前全部標為 **NOT RUN**，等待 LWC-377 實作後填入真實結果。
- 未讀取任何 key/secret、未登入、未執行付費 Query/provider/IAM、未部署，亦未 commit/push/開 PR/merge；未碰 `main`、dirty AGENTS/DS_Store、`lwc-sync` 或 370 DEV lane。

## Handoff

LWC-377 可依 `spec.md` 的精確 token regex/長度/hash、`project_keys/{key_id}` schema、三組 owner 管理 endpoints、只限 `/api/v1/query` 的 key middleware，以及負向矩陣開始實作。若遇到 storage write unknown，依文件 reconciliation 規則，不重播不明寫入且不回傳未確定的 secret。當前交付不是產品完成證據。
