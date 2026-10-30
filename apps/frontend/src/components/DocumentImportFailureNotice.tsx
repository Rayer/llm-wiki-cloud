'use client';

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { X } from 'lucide-react';
import { ApiError, dismissDocumentImportFailureReminder, getDocumentImportFailureSummary, type DocumentImportSummary } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { NavigationLink } from './NavigationBlocker';

type ScopedSummary = { projectId: string; value: DocumentImportSummary };

export function DocumentImportFailureNotice() {
  const { t } = useT();
  const { currentProject, token, user, sessionEpoch } = useWorkspace();
  const projectId = currentProject?.id;
  const ownerId = user?.id ?? '';
  const identityRef = useRef({ projectId, ownerId, sessionEpoch, token });
  useLayoutEffect(() => {
    identityRef.current = { projectId, ownerId, sessionEpoch, token };
  }, [ownerId, projectId, sessionEpoch, token]);
  const [summary, setSummary] = useState<(ScopedSummary & { ownerId: string; sessionEpoch: number }) | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dismissPending, setDismissPending] = useState(false);

  const refresh = useCallback(async () => {
    if (!projectId || !token) return;
    const requestIdentity = { projectId, ownerId, sessionEpoch, token };
    try {
      const value = await getDocumentImportFailureSummary(projectId);
      const current = identityRef.current;
      if (current.projectId !== requestIdentity.projectId || current.ownerId !== requestIdentity.ownerId || current.sessionEpoch !== requestIdentity.sessionEpoch || current.token !== requestIdentity.token) return;
      setSummary({ projectId, ownerId, sessionEpoch, value });
      setError(null);
    } catch (cause) {
      const current = identityRef.current;
      if (current.projectId === requestIdentity.projectId && current.ownerId === requestIdentity.ownerId && current.sessionEpoch === requestIdentity.sessionEpoch && current.token === requestIdentity.token) setError(cause instanceof Error ? cause.message : t('Source.documentImport.refreshFailed'));
    }
  }, [ownerId, projectId, sessionEpoch, t, token]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- load the server-owned reminder for the current Project.
    void refresh();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void refresh();
    }, 10000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const activeSummary = summary && summary.projectId === projectId && summary.ownerId === ownerId && summary.sessionEpoch === sessionEpoch ? summary.value : null;
  const dismiss = async () => {
    if (!projectId || !activeSummary || dismissPending) return;
    setDismissPending(true);
    setError(null);
    try {
      const updated = await dismissDocumentImportFailureReminder(projectId, activeSummary.latest_failure_sequence);
      const current = identityRef.current;
      if (current.projectId === projectId && current.ownerId === ownerId && current.sessionEpoch === sessionEpoch) setSummary({ projectId, ownerId, sessionEpoch, value: updated });
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : t('Source.documentImport.dismissFailed'));
    } finally {
      setDismissPending(false);
    }
  };

  if (!activeSummary?.reminder_visible || activeSummary.failed_count <= 0) return null;

  return (
    <section className="rounded-[var(--radius-lg)] border border-red-400/30 bg-red-950/35 p-4 sm:p-5" aria-labelledby="document-import-failure-title">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <h2 id="document-import-failure-title" className="text-base font-semibold text-red-100">
            {t('Source.documentImport.failureTitle')}
          </h2>
          <p className="mt-1 text-sm text-red-100/80">
            {t('Source.documentImport.failureCount', { count: activeSummary.failed_count })}
          </p>
          <NavigationLink href={activeSummary.site_url} className="mt-3 inline-flex min-h-11 items-center rounded-md bg-red-300 px-3 py-2 text-sm font-semibold text-red-950 hover:bg-red-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-red-200">
            {t('Source.documentImport.viewFailures')}
          </NavigationLink>
          {error ? <p className="mt-2 text-sm text-red-100" role="alert">{error}</p> : null}
        </div>
        <button type="button" onClick={() => void dismiss()} disabled={dismissPending} aria-label={t('Source.documentImport.dismiss')} title={t('Source.documentImport.dismiss')} className="inline-flex size-11 shrink-0 items-center justify-center rounded-md text-red-100 hover:bg-red-200/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-red-200 disabled:opacity-50">
          <X className="size-4" aria-hidden="true" />
        </button>
      </div>
    </section>
  );
}
