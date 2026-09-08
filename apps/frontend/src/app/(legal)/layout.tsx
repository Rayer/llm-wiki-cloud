import type { Metadata } from 'next';
import Link from 'next/link';
import { LegalLinks } from '@/components/LegalLinks';

// Keep public policies static and outside every application/auth provider.
export const dynamic = 'error';
export const metadata: Metadata = { robots: { index: true, follow: true } };

export default function LegalLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-3xl px-5 py-8 sm:px-8 sm:py-12">
      <a href="#main-content" className="sr-only focus:not-sr-only focus:rounded-sm focus:text-emerald-200 focus:underline">跳至主要內容</a>
      <header className="border-b border-white/10 pb-6">
        <Link href="/" prefetch={false} className="inline-flex min-h-11 items-center rounded-sm font-serif text-xl text-[#eeeae4] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">LLM Wiki Cloud · 返回首頁</Link>
        <LegalLinks />
      </header>
      <main id="main-content" tabIndex={-1} className="py-8 focus:outline-none sm:py-10">
        <article className="space-y-8 break-words text-base leading-8 text-zinc-300 [&_h1]:font-serif [&_h1]:text-3xl [&_h1]:leading-tight [&_h1]:text-[#eeeae4] sm:[&_h1]:text-4xl [&_h2]:mb-3 [&_h2]:text-xl [&_h2]:font-semibold [&_h2]:text-zinc-100 [&_p+p]:mt-3 [&_li]:pl-1 [&_ul]:list-disc [&_ul]:space-y-2 [&_ul]:pl-6">
          {children}
        </article>
      </main>
      <footer className="border-t border-white/10 pt-6">
        <LegalLinks />
      </footer>
    </div>
  );
}
