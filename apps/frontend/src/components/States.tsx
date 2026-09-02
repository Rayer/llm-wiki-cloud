import { Inbox } from 'lucide-react';
import { Skeleton, SkeletonLines } from './ui/Skeleton';
import { Surface } from './ui/Surface';

export function LoadingState({ label = 'Loading wiki data' }: { label?: string }) {
  return (
    <Surface className="p-6" variant="glass" aria-live="polite">
      <p className="mb-4 text-sm text-zinc-500">{label}...</p>
      <div className="space-y-5">
        <div className="flex items-center gap-3">
          <Skeleton className="size-9 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="h-3 w-24" />
          </div>
        </div>
        <SkeletonLines lines={4} />
      </div>
    </Surface>
  );
}

export function ErrorState({ message }: { message: string }) {
  return (
    <div className="rounded-[var(--radius-lg)] border border-red-400/30 bg-red-500/10 p-6 text-red-100">
      {message}
    </div>
  );
}

export function EmptyState({ message }: { message: string }) {
  return (
    <Surface className="flex flex-col items-center p-8 text-center" variant="glass">
      <Inbox className="mb-3 size-8 text-zinc-600" />
      <p className="text-sm text-zinc-400">{message}</p>
    </Surface>
  );
}
