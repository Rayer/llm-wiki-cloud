'use client';

import { useT } from '@/lib/i18n';

function pageLabels(page: number, pageCount: number): Array<number | 'ellipsis'> {
  const included = new Set([1, pageCount, page - 1, page, page + 1]);
  const pages = [...included].filter((value) => value >= 1 && value <= pageCount).sort((a, b) => a - b);
  const result: Array<number | 'ellipsis'> = [];
  pages.forEach((value, index) => {
    if (index > 0 && value - pages[index - 1] > 1) result.push('ellipsis');
    result.push(value);
  });
  return result;
}

export function ListPagination({
  page,
  pageCount,
  onPageChange,
}: {
  page: number;
  pageCount: number;
  onPageChange: (page: number) => void;
}) {
  const { t } = useT();
  if (pageCount <= 1) return null;

  return (
    <nav aria-label={t('List.paginationLabel')} className="flex flex-wrap items-center justify-center gap-2" data-testid="list-pagination">
      <button
        type="button"
        onClick={() => onPageChange(page - 1)}
        disabled={page <= 1}
        aria-label={t('List.previousPage')}
        className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
      >
        {t('List.previousPage')}
      </button>
      <span className="sr-only" aria-live="polite">{t('List.pageStatus', { page, pageCount })}</span>
      {pageLabels(page, pageCount).map((label, index) => label === 'ellipsis'
        ? <span key={`ellipsis-${index}`} aria-hidden="true" className="px-1 text-zinc-500">…</span>
        : (
          <button
            key={label}
            type="button"
            onClick={() => onPageChange(label)}
            aria-label={t('List.goToPage', { page: label })}
            aria-current={label === page ? 'page' : undefined}
            className={`min-h-10 min-w-10 rounded-md border px-3 text-sm ${label === page ? 'border-emerald-400/50 bg-emerald-400/10 text-emerald-100' : 'border-white/10 text-zinc-200 hover:bg-white/5'}`}
          >
            {label}
          </button>
        ))}
      <button
        type="button"
        onClick={() => onPageChange(page + 1)}
        disabled={page >= pageCount}
        aria-label={t('List.nextPage')}
        className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
      >
        {t('List.nextPage')}
      </button>
    </nav>
  );
}
