import { useT } from '@/lib/i18n';

export function Skeleton({ className = '' }: { className?: string }) {
  return (
    <div
      className={`animate-pulse rounded-md bg-white/8 ${className}`}
      aria-hidden="true"
    />
  );
}

export function SkeletonLines({ lines = 3 }: { lines?: number }) {
  const { t } = useT();
  return (
    <div className="space-y-3" role="status" aria-label={t('Shell.loading')}>
      {Array.from({ length: lines }).map((_, i) => (
        <Skeleton
          key={i}
          className={`h-4 ${i === lines - 1 ? 'w-2/3' : 'w-full'}`}
        />
      ))}
    </div>
  );
}
