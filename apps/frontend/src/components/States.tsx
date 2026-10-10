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

export function ErrorState({
  message,
  diagnosticId,
  diagnosticIdLabel,
  copyDiagnosticIdLabel,
  diagnosticIdCopiedLabel,
  diagnosticIdCopied = false,
  onCopyDiagnosticId,
}: {
  message: string;
  diagnosticId?: string;
  diagnosticIdLabel?: string;
  copyDiagnosticIdLabel?: string;
  diagnosticIdCopiedLabel?: string;
  diagnosticIdCopied?: boolean;
  onCopyDiagnosticId?: () => void;
}) {
  return (
    <div className="rounded-[var(--radius-lg)] border border-red-400/30 bg-red-500/10 p-6 text-red-100">
      <p>{message}</p>
      {diagnosticId ? (
        <div className="mt-3 flex flex-wrap items-center gap-3 text-sm">
          {diagnosticIdLabel ? <span className="text-red-100/80">{diagnosticIdLabel}</span> : null}
          <code className="select-all rounded bg-black/20 px-2 py-1">{diagnosticId}</code>
          {onCopyDiagnosticId && copyDiagnosticIdLabel ? (
            <button
              type="button"
              className="min-h-11 rounded border border-red-100/20 px-3 text-xs hover:bg-white/10"
              onClick={() => { void onCopyDiagnosticId(); }}
            >
              {diagnosticIdCopied && diagnosticIdCopiedLabel ? diagnosticIdCopiedLabel : copyDiagnosticIdLabel}
            </button>
          ) : null}
        </div>
      ) : null}
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
