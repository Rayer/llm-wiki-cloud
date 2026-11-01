# Gates: LWC-459 最新基底全站改版

OWNS: apps/frontend/public/brand/**, .taste-skill/**, apps/frontend/src/**, apps/frontend/tests/**, scripts/lwc459*, brand/lwc/**, docs/uiux-redesign-*.md, GATES.md

Scope: 保留最新功能與契約，完成品牌介面、共用列表排序篩選、三尺寸與本機操作驗證。

- [x] G1: 基底、功能與舊稿落差已從實際來源核對
  EVIDENCE: HEAD a3f8ba14407e4cb686b649b397e947411ac71361；原 worktree 已核對；docs/uiux-redesign-audit.md 完成功能／路由／契約及舊稿落差盤點。
- [x] G2: 列表搜尋、排序、篩選可組合且專案隔離
  CHECK: npx vitest run tests/lwc-459-list-controls.test.tsx
  EXPECT: Test Files  1 passed
  CWD: apps/frontend
  EVIDENCE: automatic-evidence=v1; definition-sha256=6b6d54c316f4bc206cc94ca6a520af42a5984b22c93b7b3c9e689d6e789f41a8; exit=0; EXPECT=matched; output-sha256=d1c674704398953ebd2abf72d085a004c4cd44c9d543c8587d74d64813d8b665; output-bytes=266; shell=/bin/sh; cwd=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign/apps/frontend; path=d591d93d873f/39 entries
- [x] G3: 新版設定與主要操作 regression 通過
  CHECK: npx vitest run tests/lwc-346-account-settings.test.tsx tests/lwc-443-provider-model-operations.test.tsx tests/lwc-406-profile-dirty-blocker.test.tsx tests/lwc-390-391-project-context.test.tsx tests/lwc-451-document-import-source-row.test.tsx tests/lwc-274-query-citation-links.test.tsx tests/cli-lane.test.tsx tests/lwc-317-google-hydration.test.tsx tests/lwc-304-modern-login-home.test.tsx tests/lwc-406-home-navigation.test.tsx tests/lwc-431-account-password.test.tsx tests/lwc-434-project-source-return.test.tsx tests/lwc-454-project-automation-settings.test.tsx tests/lwc-389-agent-mcp-tabs.test.tsx tests/lwc-346-cli-pairing.test.tsx
  EXPECT: Test Files  15 passed
  CWD: apps/frontend
  EVIDENCE: automatic-evidence=v1; definition-sha256=33b6a334c24f97309932d20d03defe94596c00202cf7a2f6fcb714965c4e9823; exit=0; EXPECT=matched; output-sha256=ff3e388ed5d188ea69429379898848e22a5ba5c9ecf9a35847cad6ae4f75961d; output-bytes=275; shell=/bin/sh; cwd=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign/apps/frontend; path=d591d93d873f/39 entries
- [x] G4: TypeScript 檢查通過
  CHECK: npm run typecheck
  EXPECT: tsc --noEmit
  CWD: apps/frontend
  EVIDENCE: automatic-evidence=v1; definition-sha256=d8c0aa0c82d560eb369a905406430d62b1ff5ff43973d8f51540a5a210ada5de; exit=0; EXPECT=matched; output-sha256=cd2466806930fe2a9960952ba8b90148f87b58b451931d17c86d37e9fea53005; output-bytes=637; shell=/bin/sh; cwd=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign/apps/frontend; path=d591d93d873f/39 entries
- [x] G5: ESLint 檢查通過
  CHECK: npm run lint
  EXPECT: eslint
  CWD: apps/frontend
  EVIDENCE: automatic-evidence=v1; definition-sha256=fdec9f897558ea3b66bda892e69724d553613a7004d8f3251aba96295059d46e; exit=0; EXPECT=matched; output-sha256=449d9dee1728346b4a5aa3113c1cf85a198865722e77dc929aa2b6045dc033c4; output-bytes=1222; shell=/bin/sh; cwd=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign/apps/frontend; path=d591d93d873f/39 entries
- [x] G6: 正式 build 完成
  CHECK: NEXT_PUBLIC_CONFIG_URL=http://127.0.0.1:4312/frontend-config.json npm run build
  EXPECT: Generating static pages
  CWD: apps/frontend
  EVIDENCE: automatic-evidence=v1; definition-sha256=44e2fdb3dfc2107247eac0d096b6a5f9d9c039da91ad4f4f2c5c8bf522242889; exit=0; EXPECT=matched; output-sha256=d55aefb959bde5101728a949c2eb6db3178109e69147e49a0717aeae01a50bbc; output-bytes=1680; shell=/bin/sh; cwd=/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-459-uiux-redesign/apps/frontend; path=d591d93d873f/39 entries
- [x] G7: 正式 build 的可見本機瀏覽器操作、所有路由與對話框在 390／768／1440px 經看圖修正
  EVIDENCE: uiux-redesign 的 production build；resumed/browser-results.json 110/110、browser-extra-results.json 44/44、browser-url-results.json 9/9；158 PNG、34 主要場景各三尺寸；已檢視 final-01 至 final-17 sheets 與核心頁分屏，URL 預覽修正後重看三張原圖。Owner 追加的「所有條目」連結已另以桌機／手機及實際導航驗證。
- [x] G8: Taste app inspector 與最終繁中報告包含功能保存、精確命令／退出碼、畫面與限制
  EVIDENCE: docs/uiux-redesign-audit.md 完整繁中報告、改檔／命令／退出碼／限制；四頁 Taste inspector exit 0、0 FAIL，WARN 逐項獨立量測說明；screenshot-manifest.json、changed-files.txt 與 hash 清單。
