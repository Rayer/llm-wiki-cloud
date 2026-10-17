'use client';

import { useState, useSyncExternalStore } from 'react';
import { useT } from '@/lib/i18n';
import { recentRequestSnapshot, subscribeRecentRequests } from '@/lib/request-ids';

export function RequestIDPanel() {
  const { t } = useT();
  const requests = useSyncExternalStore(subscribeRecentRequests, recentRequestSnapshot, recentRequestSnapshot);
  const [copiedID, setCopiedID] = useState<string | null>(null);

  const copyID = async (id: string) => {
    if (!navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(id);
      setCopiedID(id);
    } catch {
      setCopiedID(null);
    }
  };

  return (
    <details className="mb-4 rounded-lg border border-white/10 bg-zinc-950/70 px-3 py-2 text-xs text-zinc-300">
      <summary className="cursor-pointer select-none font-medium text-zinc-200">
        {t('Shell.requestIDs')} <span className="ml-1 text-zinc-500">({requests.length})</span>
      </summary>
      <div className="mt-2 space-y-2">
        {requests.length === 0 ? (
          <p className="text-zinc-500">{t('Shell.requestIDsEmpty')}</p>
        ) : requests.map((request) => (
          <div key={`${request.id}-${request.observedAt}`} className="flex flex-wrap items-center gap-2">
            <code className="select-all break-all rounded bg-white/5 px-2 py-1">{request.id}</code>
            <span className="text-zinc-500">{t('Shell.requestIDStatus')}: {request.status}</span>
            <button
              type="button"
              className="min-h-8 rounded border border-white/10 px-2 text-zinc-300 hover:bg-white/10"
              onClick={() => { void copyID(request.id); }}
            >
              {copiedID === request.id ? t('Shell.requestIDCopied') : t('Shell.copyRequestID')}
            </button>
          </div>
        ))}
      </div>
    </details>
  );
}
