import Link from 'next/link';
import type { WikiEntry } from '@/lib/api';

export function EntryCard({ entry, href }: { entry: WikiEntry; href: string }) {
  return (
    <Link
      href={href}
      className="block rounded-lg border border-white/10 bg-[#1a1a1a] p-5 transition hover:border-emerald-300/50 hover:bg-[#202020]"
    >
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold text-white">{entry.title}</h2>
        {entry.date ? (
          <span className="rounded bg-white/10 px-2 py-1 text-xs text-zinc-300">
            {entry.date}
          </span>
        ) : null}
      </div>
      {entry.description ? (
        <p className="mt-3 line-clamp-3 text-sm leading-6 text-zinc-400">
          {entry.description}
        </p>
      ) : null}
      <div className="mt-4 text-xs font-medium text-emerald-300">{entry.slug}</div>
    </Link>
  );
}
