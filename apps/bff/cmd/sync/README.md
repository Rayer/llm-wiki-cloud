# lwc-sync CLI 認證與 raw sync

解壓後請在 CLI 所在資料夾執行工具。`--version` 是唯一的頂層旗標；它不讀取設定、Vault 或網路：

```sh
./lwc-sync --version
```

Windows PowerShell 使用：

```powershell
.\lwc-sync.exe --version
```

下載封存檔後，先解壓縮並在解壓資料夾開啟終端機，再執行上方對應平台的 `--version` 命令。

以下命令是帶有明確替換欄位的操作範例，不代表已執行。POSIX shell 請使用絕對 Vault 根目錄；引號中的 `~` 不會展開：

```sh
AUTH_ORIGIN='<Auth origin for this environment>'
PROJECT_ID='<current Project ID>'
WIKI_ROOT='/absolute/path/to/your wiki'
./lwc-sync auth login --host "$AUTH_ORIGIN" --no-browser
./lwc-sync auth status
./lwc-sync projects
./lwc-sync init --vault "$WIKI_ROOT" --host "$AUTH_ORIGIN" --project-id "$PROJECT_ID"
./lwc-sync push --vault "$WIKI_ROOT"
./lwc-sync push --vault "$WIKI_ROOT" --sync
./lwc-sync binding list --host "$AUTH_ORIGIN"
./lwc-sync binding recover --vault "$WIKI_ROOT" --host "$AUTH_ORIGIN" --project-id "$PROJECT_ID" --binding-id '<verified binding ID>' --confirm-same-vault
./lwc-sync binding reauthorize --vault "$WIKI_ROOT" --project-id "$PROJECT_ID"
./lwc-sync auth logout
```

Windows PowerShell uses its own quoting:

```powershell
$AuthOrigin = '<Auth origin for this environment>'
$ProjectId = '<current Project ID>'
$WikiRoot = 'C:\Users\your-name\Documents\Your Wiki'
.\lwc-sync.exe auth login --host $AuthOrigin --no-browser
.\lwc-sync.exe auth status
.\lwc-sync.exe projects
.\lwc-sync.exe init --vault $WikiRoot --host $AuthOrigin --project-id $ProjectId
.\lwc-sync.exe push --vault $WikiRoot
.\lwc-sync.exe push --vault $WikiRoot --sync
.\lwc-sync.exe binding list --host $AuthOrigin
.\lwc-sync.exe binding recover --vault $WikiRoot --host $AuthOrigin --project-id $ProjectId --binding-id '<verified binding ID>' --confirm-same-vault
.\lwc-sync.exe binding reauthorize --vault $WikiRoot --project-id $ProjectId
.\lwc-sync.exe auth logout
```

登入會顯示短碼與驗證網址，並嘗試開啟瀏覽器。Headless 環境可加 `--no-browser`，在另一台已登入 Web 的裝置輸入短碼並明確核准。核准後 CLI 才會輪詢取得憑證；核准頁不會自動授權。

預設本機設定目錄是 `${XDG_CONFIG_HOME:-~/.config}/lwc-sync/`。`config.json` 只保存 `auth_host`；`credentials.json` 保存 CLI access/refresh 憑證，目錄與檔案分別設為 `0700` 與 `0600`，並以檔案鎖及原子更新處理同機多程序。憑證是明文檔案，由作業系統檔案權限保護。`auth_host` 僅指 Auth/control-plane origin，不是未來 sync data BFF URL；CLI 不會把憑證送往其他 origin。

Vault 根目錄的 `.lwc-sync.json` 只記錄 `host`、`wiki_id`、`project_id`、`binding_id`，不保存 bearer。`init` 會建立缺少的 vault 與 `raw/`，保留已有檔案，並沿既有 Auth binding 授權 Project；相同 active binding 可重跑。若伺服器已建立 binding 但本機 metadata 寫入失敗，再跑 `init` 會先查詢並恢復該 binding，不會盲目建立另一個。不同 host、Project、wiki 或已撤銷的 binding 會拒絕，並提示明確檢查／重新授權。

`push` 同步 `raw/` 下所有巢狀 regular files（包含附件），不傳 `wiki/`、vault 根目錄檔案或其他目錄。它會比對 SHA-256：相同內容跳過；本機新增或修改的檔案以 generation 條件上傳，remote-only 檔案保留。`push --sync` 先完成全部本機上傳，再重新列出 remote，並只把 remote-only 檔案下載到本機不存在的路徑；它不覆寫本機檔案。兩個命令都不傳播刪除、不執行 compile 或 pipeline。傳輸衝突、來源改變或部分失敗會以非零狀態結束，列出完成與失敗項目；已成功的單檔傳輸會保留，修正後可重跑。

raw sync 每檔最多 10 MiB、每個 raw tree 最多 10,000 個檔案及 512 MiB。symlink、特殊檔案、無法安全表示的路徑和超限檔案會明確拒絕。Auth service 以伺服器設定的 locator 提供 sync data origin；CLI 不猜測 BFF hostname。CLI 僅向該明確 origin 傳送 bearer，拒絕 redirect，並在每個 BFF 請求重新驗證 session、帳號、Project owner 與目前 binding。

`bind` 與 `binding` 命令仍保留供既有流程使用；它們只管理授權，不傳輸檔案。未使用 `auth`、`projects`、`bind`、`binding`、`init` 或 `push` 子命令的舊版直接 GCS sync invocation 仍依原有設定執行。

若本機 `.lwc-sync.json` 遺失或只有未完成的 metadata，且 Auth 仍列出原本 Vault 的 active binding，先核對本機 host、Project、Wiki 與 binding ID。只有確認是同一個 Vault 且 identity 相符時才 recover；只有 binding 已撤銷且 identity 相符時才 reauthorize。若 host、Project、Wiki 或 binding 不符，請停止並核對，不要 recover 或重新授權。Recover 必須帶 `--confirm-same-vault`，只會原子更新本機 metadata，不會建立、撤銷或重新授權 server binding，也不會傳輸 raw 檔案。

POSIX shell 的核對與恢復命令（在 CLI 所在資料夾執行）：

```sh
./lwc-sync binding list --host "$AUTH_ORIGIN"
./lwc-sync binding recover --vault "$WIKI_ROOT" --host "$AUTH_ORIGIN" --project-id "$PROJECT_ID" --binding-id '<verified binding ID>' --confirm-same-vault
./lwc-sync binding reauthorize --vault "$WIKI_ROOT" --project-id "$PROJECT_ID"
```

Windows PowerShell 的對應命令：

```powershell
.\lwc-sync.exe binding list --host $AuthOrigin
.\lwc-sync.exe binding recover --vault $WikiRoot --host $AuthOrigin --project-id $ProjectId --binding-id '<verified binding ID>' --confirm-same-vault
.\lwc-sync.exe binding reauthorize --vault $WikiRoot --project-id $ProjectId
```
