import Link from 'next/link';
import type { WikiEntry } from '@/lib/api';

function StatusBadge({ status }: { status?: string }) {
  if (status === 'published') {
    return (
      <span className="inline-flex items-center gap-1 rounded bg-emerald-500/15 px-2 py-1 text-xs font-medium text-emerald-300 ring-1 ring-emerald-400/20">
        <span aria-hidden="true">✓</span>
        Published
      </span>
    );
  }

  if (status === 'draft') {
    return (
      <span className="inline-flex items-center gap-1 rounded bg-amber-500/15 px-2 py-1 text-xs font-medium text-amber-300 ring-1 ring-amber-400/20">
        <span aria-hidden="true">✎</span>
        Draft
      </span>
    );
  }

  return null;
}

export function EntryCard({ entry, href }: { entry: WikiEntry; href: string }) {
  return (
    <Link
      href={href}
      className="block rounded-lg border border-white/10 bg-[#1a1a1a] p-5 transition hover:border-emerald-300/50 hover:bg-[#202020]"
    >
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold text-white">{entry.title}</h2>
        <StatusBadge status={entry.status} />
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
