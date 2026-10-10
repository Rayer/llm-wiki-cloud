# lwc-sync CLI 認證與 raw sync

`lwc-sync` 的認證使用 Auth/control-plane origin，例如：

```sh
lwc-sync auth login --host https://auth.example.test
lwc-sync auth status
lwc-sync projects
lwc-sync init --vault ~/Wiki --host https://auth.example.test --project-id <PROJECT_ID>
lwc-sync push --vault ~/Wiki
lwc-sync push --vault ~/Wiki --sync
lwc-sync bind --vault ~/Wiki --project-id <PROJECT_ID>
lwc-sync binding list
lwc-sync binding reauthorize --vault ~/Wiki --project-id <PROJECT_ID>
lwc-sync binding revoke --project-id <PROJECT_ID> --binding-id <BINDING_ID>
lwc-sync auth logout
```

登入會顯示短碼與驗證網址，並嘗試開啟瀏覽器。Headless 環境可加 `--no-browser`，在另一台已登入 Web 的裝置輸入短碼並明確核准。核准後 CLI 才會輪詢取得憑證；核准頁不會自動授權。

預設本機設定目錄是 `${XDG_CONFIG_HOME:-~/.config}/lwc-sync/`。`config.json` 只保存 `auth_host`；`credentials.json` 保存 CLI access/refresh 憑證，目錄與檔案分別設為 `0700` 與 `0600`，並以檔案鎖及原子更新處理同機多程序。憑證是明文檔案，由作業系統檔案權限保護。`auth_host` 僅指 Auth/control-plane origin，不是未來 sync data BFF URL；CLI 不會把憑證送往其他 origin。

Vault 根目錄的 `.lwc-sync.json` 只記錄 `host`、`wiki_id`、`project_id`、`binding_id`，不保存 bearer。`init` 會建立缺少的 vault 與 `raw/`，保留已有檔案，並沿既有 Auth binding 授權 Project；相同 active binding 可重跑。若伺服器已建立 binding 但本機 metadata 寫入失敗，再跑 `init` 會先查詢並恢復該 binding，不會盲目建立另一個。不同 host、Project、wiki 或已撤銷的 binding 會拒絕，並提示明確檢查／重新授權。

`push` 同步 `raw/` 下所有巢狀 regular files（包含附件），不傳 `wiki/`、vault 根目錄檔案或其他目錄。它會比對 SHA-256：相同內容跳過；本機新增或修改的檔案以 generation 條件上傳，remote-only 檔案保留。`push --sync` 先完成全部本機上傳，再重新列出 remote，並只把 remote-only 檔案下載到本機不存在的路徑；它不覆寫本機檔案。兩個命令都不傳播刪除、不執行 compile 或 pipeline。傳輸衝突、來源改變或部分失敗會以非零狀態結束，列出完成與失敗項目；已成功的單檔傳輸會保留，修正後可重跑。

raw sync 每檔最多 10 MiB、每個 raw tree 最多 10,000 個檔案及 512 MiB。symlink、特殊檔案、無法安全表示的路徑和超限檔案會明確拒絕。Auth service 以伺服器設定的 locator 提供 sync data origin；CLI 不猜測 BFF hostname。CLI 僅向該明確 origin 傳送 bearer，拒絕 redirect，並在每個 BFF 請求重新驗證 session、帳號、Project owner 與目前 binding。

`bind` 與 `binding` 命令仍保留供既有流程使用；它們只管理授權，不傳輸檔案。未使用 `auth`、`projects`、`bind`、`binding`、`init` 或 `push` 子命令的舊版直接 GCS sync invocation 仍依原有設定執行。
