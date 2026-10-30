import { useLayoutEffect, useRef, useState } from 'react';
import { Inbox } from 'lucide-react';
import { Skeleton, SkeletonLines } from './ui/Skeleton';
import { Surface } from './ui/Surface';

export function displayErrorId(requestId?: string, diagnosticId?: string) {
  if (requestId) return { value: requestId, kind: 'request' as const };
  if (diagnosticId) return { value: diagnosticId, kind: 'diagnostic' as const };
  return null;
}

export function useErrorIdCopy(owner: object | null, requestId?: string, diagnosticId?: string) {
  const [copiedOwner, setCopiedOwner] = useState<object | null>(null);
  const ownerRef = useRef(owner);

  useLayoutEffect(() => {
    ownerRef.current = owner;
    return () => {
      if (ownerRef.current === owner) ownerRef.current = null;
    };
  }, [owner]);

  const copyErrorId = async () => {
    const errorId = displayErrorId(requestId, diagnosticId);
    if (!owner || !errorId || !navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(errorId.value);
    } catch {
      return;
    }
    if (ownerRef.current === owner) setCopiedOwner(owner);
  };

  return { copyErrorId, errorIdCopied: owner !== null && copiedOwner === owner };
}

export function ErrorIdDetails({
  requestId,
  diagnosticId,
  requestIdLabel,
  diagnosticIdLabel,
  copyErrorIdLabel,
  errorIdCopiedLabel,
  errorIdCopied = false,
  onCopyErrorId,
  className,
}: {
  requestId?: string;
  diagnosticId?: string;
  requestIdLabel?: string;
  diagnosticIdLabel?: string;
  copyErrorIdLabel?: string;
  errorIdCopiedLabel?: string;
  errorIdCopied?: boolean;
  onCopyErrorId?: () => void;
  className: string;
}) {
  const errorId = displayErrorId(requestId, diagnosticId);
  if (!errorId) return null;

  return (
    <div className={className}>
      {(errorId.kind === 'request' ? requestIdLabel : diagnosticIdLabel) ? (
        <span className="text-red-100/80">{errorId.kind === 'request' ? requestIdLabel : diagnosticIdLabel}</span>
      ) : null}
      <code className="select-all rounded bg-black/20 px-2 py-1">{errorId.value}</code>
      {onCopyErrorId && copyErrorIdLabel ? (
        <button
          type="button"
          className="min-h-11 rounded border border-red-100/20 px-3 text-xs hover:bg-white/10"
          onClick={() => { void onCopyErrorId(); }}
        >
          {errorIdCopied && errorIdCopiedLabel ? errorIdCopiedLabel : copyErrorIdLabel}
        </button>
      ) : null}
    </div>
  );
}

export function LoadingState({ label }: { label: string }) {
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
  requestId,
  diagnosticId,
  requestIdLabel,
  diagnosticIdLabel,
  copyErrorIdLabel,
  errorIdCopiedLabel,
  errorIdCopied = false,
  onCopyErrorId,
}: {
  message: string;
  requestId?: string;
  diagnosticId?: string;
  requestIdLabel?: string;
  diagnosticIdLabel?: string;
  copyErrorIdLabel?: string;
  errorIdCopiedLabel?: string;
  errorIdCopied?: boolean;
  onCopyErrorId?: () => void;
}) {
  return (
    <div className="rounded-[var(--radius-lg)] border border-red-400/30 bg-red-500/10 p-6 text-red-100">
      <p>{message}</p>
      <ErrorIdDetails
        requestId={requestId}
        diagnosticId={diagnosticId}
        requestIdLabel={requestIdLabel}
        diagnosticIdLabel={diagnosticIdLabel}
        copyErrorIdLabel={copyErrorIdLabel}
        errorIdCopiedLabel={errorIdCopiedLabel}
        errorIdCopied={errorIdCopied}
        onCopyErrorId={onCopyErrorId}
        className="mt-3 flex flex-wrap items-center gap-3 text-sm"
      />
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
