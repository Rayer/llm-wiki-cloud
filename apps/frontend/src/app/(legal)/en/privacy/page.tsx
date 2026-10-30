import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Privacy Policy | LLM Wiki Cloud',
  description: 'How LLM Wiki Cloud processes account, browser, project, and AI data, and handles retention, deletion requests, and contact.',
};

export default function EnglishPrivacyPage() {
  return (
    <div lang="en">
      <p className="text-xs text-zinc-400">English · <a href="/privacy" lang="zh-Hant" className="text-emerald-200 underline underline-offset-4">繁體中文</a></p>
      <h1>Privacy Policy</h1>
      <p className="text-sm text-zinc-400">This policy takes effect when it is published.</p>
      <p>LLM Wiki Cloud is a personal project operated by Rayer Tung. This page explains how information is handled when you sign in, organize knowledge, and search. You can read this page and the Terms of Service without signing in.</p>

      <section aria-labelledby="account-data-en">
        <h2 id="account-data-en">Account and sign-in information</h2>
        <p>When you register or sign in with email, the service receives your email address and password to create an account and verify your identity. Account records store your email address, password hash, role, account status, and project associations.</p>
        <p>If you choose Google sign-in or link a Google account, the service requests the basic OpenID Connect scopes openid, email, and profile to verify your identity and create or link your account. Stored Google identity data currently includes the provider, issuer, user identifier, email address, email verification status, and account association. The current sign-in flow does not store your name or photo and does not request access to Gmail messages or Google Drive files.</p>
      </section>

      <section aria-labelledby="browser-storage-en">
        <h2 id="browser-storage-en">Cookies and browser storage</h2>
        <p>The service uses cookies to maintain sign-in refresh and complete Google sign-in, linking, and account completion flows. Browser local storage saves access tokens, account cache, sign-in flow state, the most recently selected project, language, and some interface preferences, such as dismissed announcements and suggested-query settings.</p>
        <p>You can sign out or clear cookies and site data in your browser settings. After clearing them, you may need to sign in again or reset preferences. Clearing browser data does not delete server-side account or project data.</p>
      </section>

      <section aria-labelledby="project-data-en">
        <h2 id="project-data-en">Project content and AI processing</h2>
        <p>The service processes project information, uploaded files and file names, URLs and fetched content, edits and annotations, query text, generated knowledge entries, indexes, and execution records to provide organization, search, and answer features. When fetching a URL, the service sends a request to that website.</p>
        <p>When an AI feature runs, the service sends query text, relevant retrieved content, source material, or derived knowledge to the third-party AI provider configured for that feature. This supports query expansion, answer generation, content organization, and suggested-query generation. The recipient depends on the feature&apos;s service configuration, and its handling and retention are governed by the terms of the service used.</p>
        <p>Before submitting content, make sure you are allowed to have the service and relevant providers process it. Avoid submitting unnecessary personal information, confidential information, or another person&apos;s sensitive information.</p>
      </section>

      <section aria-labelledby="processors-en">
        <h2 id="processors-en">Infrastructure and records</h2>
        <p>The frontend is hosted by Vercel. Account and backend features use Google Cloud Platform (GCP) Cloud Run, Firestore, and Cloud Storage for compute, account and project metadata, and file storage. These providers process network requests needed to serve the website and its features.</p>
        <p>The service also creates records of sign-in results, errors, project operations, and processing jobs for status checks and troubleshooting. Some operation records include identifiers, URLs, file names, or titles. Do not put confidential information in those fields.</p>
        <p>Google sign-in, cloud infrastructure, and AI providers involve third-party processing governed by the terms of the services used. Deletion by this service does not mean that copies held by third parties are deleted at the same time.</p>
      </section>

      <section aria-labelledby="data-controls-en">
        <h2 id="data-controls-en">Account controls, retention, and deletion</h2>
        <p>Account settings currently show your primary email and any linked Google email, and provide a flow to link a Google account. Signing out clears the local sign-in cache and asks the sign-in service to revoke the current refresh session.</p>
        <p>Account and project data are retained while the service is provided. The current interface does not let users delete their account or unlink Google themselves. Disabling an account and signing out do not delete data.</p>
        <p>You can request deletion by email below. After verifying your identity, Rayer Tung will handle the request manually and explain the actual scope and limits in the reply. Deletion may not be immediate and may not remove every identity association, record, backup, or copy retained by a third party.</p>
      </section>
      <section aria-labelledby="privacy-contact-en">
        <h2 id="privacy-contact-en">Contact and data requests</h2>
        <p>For service, privacy, or data deletion requests, contact Rayer Tung at <a href="mailto:rayershih@gmail.com" className="rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">rayershih@gmail.com</a>.</p>
      </section>
    </div>
  );
}
