import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: '隱私權政策 | LLM Wiki Cloud',
  description: 'LLM Wiki Cloud 的帳號資料、登入儲存、專案與 AI 處理，以及資料保留、人工刪除請求與聯絡方式。',
};

export default function PrivacyPage() {
  return (
    <>
      <h1>隱私權政策</h1>
      <p className="text-sm text-zinc-400">本政策自公開發布時生效。</p>
      <p>LLM Wiki Cloud 是由 Rayer Tung 經營的個人興趣專案。本頁說明帳號登入、知識資料整理與查詢過程中的資料處理。你可以不登入，直接閱讀本頁及服務條款。</p>

      <section aria-labelledby="account-data">
        <h2 id="account-data">帳號與登入資料</h2>
        <p>使用電子郵件註冊或登入時，服務會接收電子郵件地址及密碼，以建立帳號並驗證身分。帳號紀錄保存電子郵件地址、密碼雜湊、角色、帳號狀態及專案關聯。</p>
        <p>選擇 Google 登入或連結帳號時，服務會向 Google 要求 OpenID Connect 的 openid、email、profile 基本範圍，用於確認身分及建立或連結本站帳號。目前保存的 Google 身分資料包含提供者、發行者、使用者識別碼、電子郵件地址、電子郵件驗證狀態及帳號關聯；目前的登入流程不保存姓名或頭像，也未要求 Gmail 郵件或 Google Drive 檔案的存取範圍。</p>
      </section>

      <section aria-labelledby="browser-storage">
        <h2 id="browser-storage">Cookie 與瀏覽器儲存</h2>
        <p>服務使用 Cookie 維持登入更新，以及確認 Google 登入、連結與完成流程。瀏覽器的本機儲存（localStorage）保存存取權杖、帳號快取、登入流程狀態、最近選擇的專案、語言及部分介面偏好，例如公告關閉紀錄與建議問題顯示設定。</p>
        <p>你可登出，或透過瀏覽器設定清除 Cookie 與網站資料。清除後可能需要重新登入或重新設定偏好；清除瀏覽器資料不會刪除伺服器上的帳號或專案。</p>
      </section>

      <section aria-labelledby="project-data">
        <h2 id="project-data">專案內容與 AI 處理</h2>
        <p>服務會處理你建立的專案資訊、上傳檔案及檔名、提供的網址與擷取內容、編輯與註記、查詢文字，以及產生的知識條目、索引與執行紀錄，以提供資料整理、搜尋及回答功能。擷取網址時，服務會向該網站發出請求。</p>
        <p>執行 AI 功能時，服務會依所使用的功能，將查詢文字、相關檢索內容、原始資料或衍生的知識內容，傳送至服務設定使用的第三方 AI 提供者，供查詢擴展、回答生成、資料整理與建議問題生成使用。接收資料的提供者取決於該功能的服務設定，其處理與保存方式受所採用服務的條件及約定影響。</p>
        <p>提交內容前，請確認你有權讓服務及相關提供者處理其中的資料，並避免提交不必要的個人資料、機密或他人的敏感資訊。</p>
      </section>

      <section aria-labelledby="processors">
        <h2 id="processors">服務基礎設施與紀錄</h2>
        <p>前端網頁使用 Vercel 託管；帳號與後端功能使用 Google Cloud Platform（GCP）的 Cloud Run、Firestore 與 Cloud Storage，處理運算、帳號與專案中繼資料，以及檔案儲存。使用網站時，這些基礎設施會處理提供網頁或功能所需的網路請求。</p>
        <p>服務也會產生登入結果、錯誤、專案操作及處理工作等紀錄，用於執行狀態確認與問題排查。部分操作紀錄包含識別碼、網址、檔名或標題；請勿將機密資訊放入這些欄位。</p>
        <p>Google 登入、雲端基礎設施及 AI 提供者均涉及第三方處理，相關資料的使用與保存受所採用服務的條件及約定影響。本站的刪除處理不代表第三方保存的副本已同步刪除。</p>
      </section>

      <section aria-labelledby="data-controls">
        <h2 id="data-controls">帳號控制、資料保留與刪除</h2>
        <p>目前帳號設定可查看主要電子郵件及已連結的 Google 電子郵件，並提供連結 Google 帳號的流程。登出會清除本機登入快取，並向登入服務提出撤銷目前更新工作階段的請求。</p>
        <p>在提供服務期間，服務會保留帳號及專案資料。目前介面未提供使用者自行刪除帳號或解除 Google 連結的功能。停用帳號與登出不等同刪除資料。</p>
        <p>你可透過下方電子郵件提出刪除請求。Rayer Tung 會在確認申請人身分後人工處理，並在回覆中說明實際處理範圍與限制。刪除不保證即時完成，也不保證全面清除所有身分關聯、紀錄、備份或第三方保留的副本。</p>
      </section>
      <section aria-labelledby="privacy-contact">
        <h2 id="privacy-contact">聯絡與資料請求</h2>
        <p>如有服務、隱私或資料刪除相關請求，請聯絡 Rayer Tung：<a href="mailto:rayershih@gmail.com" className="rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">rayershih@gmail.com</a>。</p>
      </section>
    </>
  );
}
