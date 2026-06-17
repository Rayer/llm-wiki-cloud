import Link from 'next/link';

const navItems = [
  { href: '/', label: 'Search' },
  { href: '/sources', label: 'Sources' },
  { href: '/concepts', label: 'Concepts' },
  { href: '/status', label: 'Status' },
];

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen bg-[#0a0a0a] text-zinc-100">
      <aside className="fixed inset-y-0 left-0 hidden w-64 border-r border-white/10 bg-[#111111]/95 px-5 py-6 lg:block">
        <Link href="/" className="block">
          <div className="text-sm font-semibold uppercase tracking-[0.28em] text-emerald-300">
            LLM Wiki (Demo)
          </div>
          <div className="mt-3 text-2xl font-semibold text-white">Knowledge Portal</div>
        </Link>
        <nav className="mt-10 space-y-2">
          {navItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="block rounded-md px-3 py-2 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white"
            >
              {item.label}
            </Link>
          ))}
        </nav>
      </aside>

      <header className="sticky top-0 z-20 border-b border-white/10 bg-[#0a0a0a]/90 px-4 py-3 backdrop-blur lg:hidden">
        <div className="flex items-center justify-between">
          <Link href="/" className="font-semibold text-white">
            LLM Wiki (Demo)
          </Link>
          <nav className="flex gap-2 text-sm text-zinc-300">
            {navItems.slice(1).map((item) => (
              <Link key={item.href} href={item.href} className="rounded px-2 py-1">
                {item.label}
              </Link>
            ))}
          </nav>
        </div>
      </header>

      <main className="lg:pl-64">
        <div className="mx-auto min-h-screen max-w-6xl px-4 py-8 sm:px-6 lg:px-10">
          {children}
        </div>
      </main>
    </div>
  );
}
