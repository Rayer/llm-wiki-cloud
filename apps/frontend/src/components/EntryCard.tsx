import type { WikiEntry } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { NavigationLink } from './NavigationBlocker';

function StatusBadge({ status }: { status?: string }) {
  const { t } = useT();

  if (status === 'published') {
    return <Badge variant="published">{t('Entry.published')}</Badge>;
  }
  if (status === 'draft') {
    return <Badge variant="draft">{t('Entry.draft')}</Badge>;
  }
  return null;
}

export function EntryCard({
  entry,
  href,
  entryType,
  index = 0,
}: {
  entry: WikiEntry;
  href: string;
  entryType?: 'source' | 'concept';
  index?: number;
}) {
  const { t } = useT();
  const typeBorderClass = entryType === 'source'
    ? 'border-l-[3px] border-l-blue-400'
    : entryType === 'concept'
      ? 'border-l-[3px] border-l-emerald-400'
      : '';

  return (
    <NavigationLink href={href} className="group block focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
      <Surface
        variant="glass"
        className={`animate-fade-in p-5 transition duration-200 [animation-fill-mode:backwards] hover:-translate-y-0.5 hover:border-emerald-400/30 hover:shadow-lg hover:shadow-emerald-500/5 ${typeBorderClass}`}
        style={{ animationDelay: `${index * 50}ms` }}
      >
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-lg font-semibold text-white group-hover:text-emerald-50">
            {entry.title}
          </h2>
          {entry.status === 'draft' ? <StatusBadge status={entry.status} /> : null}
          <span className="sr-only">{entryType === 'source' ? t('Source.singular') : t('Entry.singular')}</span>
          {entry.date ? <Badge variant="muted">{entry.date}</Badge> : null}
        </div>
        {entry.description ? (
          <p className="mt-3 line-clamp-3 text-sm leading-6 text-zinc-400">
            {entry.description}
          </p>
        ) : null}
      </Surface>
    </NavigationLink>
  );
}
