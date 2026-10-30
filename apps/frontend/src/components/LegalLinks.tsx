export function LegalLinks() {
  return (
    <nav aria-label="Privacy and service terms / 隱私與服務條款" className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
      <a href="/privacy" lang="zh-Hant" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">繁體中文 · 隱私權政策</a>
      <a href="/terms" lang="zh-Hant" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">繁體中文 · 服務條款</a>
      <a href="/en/privacy" lang="en" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">English · Privacy policy</a>
      <a href="/en/terms" lang="en" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">English · Terms of service</a>
    </nav>
  );
}
