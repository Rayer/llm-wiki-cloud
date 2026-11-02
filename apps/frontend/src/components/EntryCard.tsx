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
}: {
  entry: WikiEntry;
  href: string;
  entryType?: 'source' | 'concept';
  index?: number;
}) {
  const { t } = useT();

  return (
    <NavigationLink href={href} className="group block">
      <Surface
        variant="glass"
        className="lwc-entry p-5"
      >
        <div className="flex flex-wrap items-center gap-2">
          {entryType ? (
            <Badge variant={entryType}>
              {entryType === 'source' ? t('Source.singular') : t('Entry.singular')}
            </Badge>
          ) : null}
          <h2 className="text-lg font-semibold text-white group-hover:text-emerald-50">
            {entry.title}
          </h2>
          <StatusBadge status={entry.status} />
          {entry.date ? <Badge variant="muted">{entry.date}</Badge> : null}
        </div>
        {entryType === 'concept' ? (
          <p className="mt-3 text-xs text-zinc-500">
            {t('List.objectUpdated')}: {entry.objectUpdatedAt ?? t('List.objectUpdatedMissing')}
          </p>
        ) : null}
        {entry.description ? (
          <p className="mt-3 line-clamp-3 text-sm leading-6 text-zinc-400">
            {entry.description}
          </p>
        ) : null}
      </Surface>
    </NavigationLink>
  );
}
