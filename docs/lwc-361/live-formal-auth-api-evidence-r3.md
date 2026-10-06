# LWC-361：正式 local Auth／BFF 真服務補驗

## 實際執行與結果

TPM 分別於 `2026-10-06T14:12:52.486376+00:00` 與 `2026-10-06T14:14:38.133536+00:00` 對目前 native local services 實際送 HTTP requests。不是 unit／emulator，也不是 browser操作。服務為 Auth `localhost:18081`、BFF `localhost:18080`，沿用真 local named Firestore與worktree scope。

BFF `GET /api/v1/projects` 三個負向：

| 案例 | 實際 HTTP | 判定 |
|---|---|---|
| 缺 Bearer | 401 | PASS |
| 刻意無效的 synthetic Bearer | 401 | PASS |
| 只有退休 X-User-ID／X-User-Role headers | 401 | PASS |

以既定公開測試 fixture（Make契約、非DEV／Production帳密）新建本次正式 API session，fixture user `d68891aa762d3936d2240bed`：

| 案例 | 實際 HTTP與assertions | 判定 |
|---|---|---|
| 正式登入／loopbackcookie | 200；response身分為fixture；cookie host-only／HttpOnly／SameSite=Lax／non-Secure適用localHTTP | PASS |
| 正式Auth token存取BFF projects | 200 | PASS |
| refresh | 200；同user、refreshcookie值已輪替 | PASS |
| logout | 200／ok=true；clientcookiejar已清 | PASS |
| 手動重送logout前那枚refreshcookie | 401；不是只因cookiejar清空而宣稱revoked | PASS |

兩次程序assertions與exit均0。原始非秘密結果：

- `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-live-bff-auth-negative.json`
- `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-live-formal-auth-api.json`

## 權限／秘密與資料界線

只操作本次新建fixture session；沒有读取或複製Owner Safari的token／cookie，沒有logoutOwner session。正式endpoint返回的本次access／refreshtoken只在程序記憶體使用，不輸出／寫入檔案／tracker；HTTP response bodies不存。logout已撤銷該測試session，沒有刪fixture帳號或重設密碼、沒有停止app。

無Pipeline、quota／cooldown、paidprovider、DEV／Production／IAM／credentialmutation。可能保留正式session/replay/audit歷史是正常Auth durable行為，不冒稱整個日常scope已清理。

## AC2 對帳與限定

### 追加：新帳號正式 API 旅程

TPM 實際 register `lwc361-acceptance-cf9983237a45@example.test` 回201，user `f1a592f0b72a46c3f5dc98b1`／default_project_id=`default`。接著明確用password login而非registration token。第一次 harness 在BFF200後誤加「default必出現在BFFprojectlist」斷言，程序exit1；原artifact `lwc361-live-new-account-auth-api.json`保留，不重標PASS。

追實際source：`internal/auth/identity.go:621–624,681–687` 的default metadata存scoped users/{user}/projects/default；`internal/handler/v1/endpoints.go:276–295` 的BFF列表讀scoped頂層projects、再fallbackGCS，兩者不是同一consumer契約。此票AC2要求有效token可存取，不明定註冊default必出現在該列表；TPM撤回自行增加的list斷言，不要求worker改source迎合harness。

`2026-10-06T14:19:03.443538+00:00` 新執行程序exit0／6具名PASS：正式passwordlogin200、BFF projects200（空列表的實際結果保留）、對註冊回傳的exact scoped default document mask=name讀回200／My First Wiki、refresh200／同user、logout200且cookiejar清空、重送登出前refreshcookie401。結果 `lwc361-live-new-account-auth-api-retry.json`，只含非秘密metadata，與第一次證據分開。

新帳號與第一次harness建立但未走到logout的測試session在API驗收時暫留，後續retry本身的session已logout。這補足newregister的正式API旅程，不冒稱browser新帳號旅程已完成，也不把projectlist觀察自動升為新releasegate。後續精確清理結果如下，不覆寫初次時點。

### 追加：專用新帳號 exact cleanup

`2026-10-06T14:47:56.077261+00:00` TPM在已授權的真local database `llm-wiki-cloud-local`／scope `worktree-8783993b38e351db96630b6c` 完成清理，只針對上述本次專用user `f1a592f0b72a46c3f5dc98b1`。先核帳號email與其reservation的user_id一致、其唯一nestedproject為default且無子collection；sessions用exact user_id查詢、replays用這些session_id查詢，沒有掃其他scope或刪其他user資料。

使用每筆原始updateTime precondition的atomiccommit刪除：user1、emailreservation1、defaultmetadata1、refreshsessions2、refreshreplay1，總6documents。逐筆GET讀回404，重查該user sessions及對應replays均空；Owner user `4dd1da9818489caafbb4538b` 與既有fixture user `d68891aa762d3936d2240bed` 的exact user document仍存在且updateTime不變。沒有Pipeline／quota／cooldown、paidprovider、DEV／Production／IAM／credentials變更。

原始非秘密result：`/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc361-new-account-cleanup-result.json`。Token／cookie／password與session tokenhash未存入artifact。此完成本次API新帳號資料清理，不表示browser先前project／raw／failedrun artifacts或其他驗收資料已清，亦不清Owner帳號／fixture歷史。

這補足正式真Auth API login/refresh/logout／revocation、有效BFFidentity與缺／錯token／headerbypass拒絕，不再將這些列為完全未跑。

仍未證：新帳號browserregistration→login完整UI旅程、跨scope有效token拒絕、真雙worktree隔離與stop、完整新guidewalkthrough。測的是目前runningBFF／Auth，不是正在補的Syntobootstrap finalsource／SHA；新版本仍按既定review／回歸與交付流程核對。不是browserAC4positive、整票PASS、UAT或DEV部署證據。
