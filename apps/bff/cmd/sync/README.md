# lwc-sync CLI 認證與 Project 綁定

`lwc-sync` 的認證使用 Auth/control-plane origin，例如：

```sh
lwc-sync auth login --host https://auth.example.test
lwc-sync auth status
lwc-sync projects
lwc-sync bind --vault ~/Wiki --project-id <PROJECT_ID>
lwc-sync binding list
lwc-sync binding reauthorize --vault ~/Wiki --project-id <PROJECT_ID>
lwc-sync binding revoke --project-id <PROJECT_ID> --binding-id <BINDING_ID>
lwc-sync auth logout
```

登入會顯示短碼與驗證網址，並嘗試開啟瀏覽器。Headless 環境可加 `--no-browser`，在另一台已登入 Web 的裝置輸入短碼並明確核准。核准後 CLI 才會輪詢取得憑證；核准頁不會自動授權。

預設本機設定目錄是 `${XDG_CONFIG_HOME:-~/.config}/lwc-sync/`。`config.json` 只保存 `auth_host`；`credentials.json` 保存 CLI access/refresh 憑證，目錄與檔案分別設為 `0700` 與 `0600`，並以檔案鎖及原子更新處理同機多程序。憑證是明文檔案，由作業系統檔案權限保護。`auth_host` 僅指 Auth/control-plane origin，不是未來 sync data BFF URL；CLI 不會把憑證送往其他 origin。

Vault 根目錄的 `.lwc-sync.json` 只記錄 `host`、`wiki_id`、`project_id`、`binding_id`。複製 vault 會保留 wiki 與 binding 身分；伺服器仍會逐次驗證登入 session、帳號、Project owner 與目前 binding 狀態。建立或撤銷 binding 都不會登出 CLI 或刪除 wiki 資料。這些命令僅管理登入與授權，不傳輸 wiki 檔案；既有直接 GCS sync 流程仍依原有設定執行。
