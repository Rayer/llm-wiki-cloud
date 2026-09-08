export function LegalLinks() {
  return (
    <nav aria-label="隱私與服務條款" className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
      <a href="/privacy" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">隱私權政策</a>
      <a href="/terms" className="inline-flex min-h-11 items-center rounded-sm text-emerald-200 underline underline-offset-4 hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-300">服務條款</a>
    </nav>
  );
}
