import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: '服務條款 | LLM Wiki Cloud',
  description: 'LLM Wiki Cloud 的使用規範、內容權利與必要處理授權、AI 回答限制、服務可用性及聯絡方式。',
};

export default function TermsPage() {
  return (
    <>
      <h1>服務條款</h1>
      <p className="text-sm text-zinc-400">本條款自公開發布時生效。</p>
      <p>LLM Wiki Cloud 是由 Rayer Tung 經營的個人興趣專案。使用服務時，請遵守以下條款。</p>

      <section aria-labelledby="service">
        <h2 id="service">服務內容</h2>
        <p>LLM Wiki Cloud 提供專案資料上傳、網址擷取、知識內容整理、搜尋及 AI 輔助回答。部分功能需要登入、可用的專案與服務設定，並可能受到註冊開放狀態、使用配額或處理工作狀態限制。</p>
      </section>

      <section aria-labelledby="responsibilities">
        <h2 id="responsibilities">帳號與合理使用</h2>
        <ul>
          <li>請提供你有權使用的帳號資料，妥善保管登入憑證，並在共用裝置使用完畢後登出。</li>
          <li>請勿使用服務從事違法、侵害他人權利、冒用身分或未經授權存取他人資料的行為。</li>
          <li>請勿繞過存取控制或使用限制、上傳惡意內容，或干擾服務及其他使用者的正常使用。</li>
        </ul>
      </section>

      <section aria-labelledby="content">
        <h2 id="content">你提交的內容</h2>
        <p>你保留所提交內容的原有權利，並應確認有權上傳、擷取及處理該內容，包括其中的個人資料與受著作權保護的資料。</p>
        <p>你僅授權服務及其採用的基礎設施與 AI 提供者，在提供服務所必要的範圍內儲存、處理、轉換及傳送內容，並產生與顯示結果。此授權不移轉你的內容權利。</p>
      </section>

      <section aria-labelledby="ai-results">
        <h2 id="ai-results">AI 結果與第三方服務</h2>
        <p>AI 產生的條目、摘要、回答及引用可能包含錯誤、遺漏或不適合你的情境。請核對原始來源，並自行判斷結果是否適合使用，尤其在涉及重要決策時。</p>
        <p>使用第三方登入、雲端處理、AI 或來源網站時，也須遵守適用的第三方規範。資料處理方式請參閱本站隱私權政策。</p>
      </section>

      <section aria-labelledby="availability">
        <h2 id="availability">功能變動與使用限制</h2>
        <p>本服務為個人興趣專案，不保證持續可用。服務可能因維護、故障、容量或第三方服務狀態而中斷，功能與限制也可能調整。請自行備份重要資料。</p>
        <p>對於侵權、濫用或干擾服務安全與運作的行為，管理員可限制或停用帳號。停用與資料刪除是不同操作；資料控制範圍請參閱隱私權政策。</p>
      </section>
      <section aria-labelledby="terms-contact">
        <h2 id="terms-contact">聯絡與資料請求</h2>
        <p>如有服務、隱私或資料刪除相關請求，請聯絡 Rayer Tung：<a href="mailto:rayershih@gmail.com" className="rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">rayershih@gmail.com</a>。</p>
      </section>
    </>
  );
}
