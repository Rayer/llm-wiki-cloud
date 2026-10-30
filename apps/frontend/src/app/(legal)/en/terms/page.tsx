import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Terms of Service | LLM Wiki Cloud',
  description: 'LLM Wiki Cloud terms covering acceptable use, content rights, AI results, availability, and contact.',
};

export default function EnglishTermsPage() {
  return (
    <div lang="en">
      <p className="text-xs text-zinc-400">English · <a href="/terms" lang="zh-Hant" className="text-emerald-200 underline underline-offset-4">繁體中文</a></p>
      <h1>Terms of Service</h1>
      <p className="text-sm text-zinc-400">These terms take effect when they are published.</p>
      <p>LLM Wiki Cloud is a personal project operated by Rayer Tung. By using the service, you agree to follow these terms.</p>

      <section aria-labelledby="service-en">
        <h2 id="service-en">Service</h2>
        <p>LLM Wiki Cloud provides project data uploads, URL fetching, knowledge organization, search, and AI-assisted answers. Some features require sign-in, an available project, and service configuration. They may also be limited by registration availability, usage quotas, or processing-job status.</p>
      </section>

      <section aria-labelledby="responsibilities-en">
        <h2 id="responsibilities-en">Accounts and acceptable use</h2>
        <ul>
          <li>Provide account information you are allowed to use, protect your sign-in credentials, and sign out after using a shared device.</li>
          <li>Do not use the service for unlawful activity, to violate another person&apos;s rights, impersonate another person, or access someone else&apos;s data without authorization.</li>
          <li>Do not bypass access controls or usage limits, upload malicious content, or interfere with the service or other users.</li>
        </ul>
      </section>

      <section aria-labelledby="content-en">
        <h2 id="content-en">Content you submit</h2>
        <p>You keep the rights you already have in submitted content. You must make sure you are allowed to upload, fetch, and process it, including any personal data or copyrighted material it contains.</p>
        <p>You authorize the service and its selected infrastructure and AI providers to store, process, transform, and transmit content only as needed to provide the service and generate and display results. This authorization does not transfer your content rights.</p>
      </section>

      <section aria-labelledby="ai-results-en">
        <h2 id="ai-results-en">AI results and third-party services</h2>
        <p>AI-generated entries, summaries, answers, and citations can contain errors, omissions, or material that does not fit your situation. Check the original sources and decide whether a result is appropriate, especially for important decisions.</p>
        <p>When using third-party sign-in, cloud processing, AI, or source websites, you must also follow their applicable terms. See the Privacy Policy for information about data processing.</p>
      </section>

      <section aria-labelledby="availability-en">
        <h2 id="availability-en">Changes and limits</h2>
        <p>This is a personal project and is not guaranteed to remain available. The service may be interrupted for maintenance, failures, capacity limits, or third-party service issues. Features and limits may change. Keep your own backups of important data.</p>
        <p>Administrators may restrict or disable accounts for infringement, abuse, or interference with service security or operation. Disabling an account is different from deleting data; see the Privacy Policy for data controls.</p>
      </section>
      <section aria-labelledby="terms-contact-en">
        <h2 id="terms-contact-en">Contact and data requests</h2>
        <p>For service, privacy, or data deletion requests, contact Rayer Tung at <a href="mailto:rayershih@gmail.com" className="rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">rayershih@gmail.com</a>.</p>
      </section>
    </div>
  );
}
