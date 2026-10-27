'use client';

import { useEffect, useState } from 'react';
import { getProjectLLMBinding } from '@/lib/api';
import { useT } from '@/lib/i18n';

type LLMSource = 'shared' | 'personal' | 'loading' | 'unknown';

export function SharedServiceOnboardingNotice({ projectId, ownerId, sessionEpoch, isDemoSession }: {
  projectId: string;
  ownerId: string;
  sessionEpoch: number;
  isDemoSession: boolean;
}) {
  const scopeKey = `${ownerId}\u0000${projectId}\u0000${sessionEpoch}`;
  const [loaded, setLoaded] = useState<{ key: string; source: LLMSource } | null>(null);

  useEffect(() => {
    if (!projectId || !ownerId || isDemoSession) return;
    let active = true;
    let requestId = 0;
    const loadBinding = () => {
      const currentRequest = ++requestId;
      setLoaded({ key: scopeKey, source: 'loading' });
      void getProjectLLMBinding(projectId).then((binding) => {
        if (active && currentRequest === requestId) setLoaded({ key: scopeKey, source: binding.source });
      }).catch(() => {
        if (active && currentRequest === requestId) setLoaded({ key: scopeKey, source: 'unknown' });
      });
    };
    const handleBindingUpdated = (event: Event) => {
      const detail = (event as CustomEvent<{ projectId?: string }>).detail;
      if (detail?.projectId === projectId) loadBinding();
    };
    loadBinding();
    window.addEventListener('lwc-project-llm-binding-updated', handleBindingUpdated);
    return () => {
      active = false;
      window.removeEventListener('lwc-project-llm-binding-updated', handleBindingUpdated);
    };
  }, [isDemoSession, ownerId, projectId, scopeKey]);

  return (
    <SharedServiceNotice
      ownerId={ownerId}
      source={loaded?.key === scopeKey ? loaded.source : 'loading'}
      isDemoSession={isDemoSession}
    />
  );
}

export function SharedServiceNotice({ ownerId, source, isDemoSession = false, onManageProviders }: {
  ownerId: string;
  source: LLMSource;
  isDemoSession?: boolean;
  onManageProviders?: () => void;
}) {
  const { t } = useT();
  const storageKey = `lwc:byok-shared-notice:v1:${ownerId}`;
  const [dismissal, setDismissal] = useState<{ key: string; dismissed: boolean } | null>(null);

  useEffect(() => {
    let active = true;
    queueMicrotask(() => {
      if (!active) return;
      let dismissed = false;
      try {
        dismissed = window.localStorage.getItem(storageKey) === '1';
      } catch {
        // The notice remains available when local storage is blocked.
      }
      setDismissal({ key: storageKey, dismissed });
    });
    return () => { active = false; };
  }, [storageKey]);

  function dismiss() {
    try {
      window.localStorage.setItem(storageKey, '1');
    } catch {
      // The in-memory dismissal still works for this mount.
    }
    setDismissal({ key: storageKey, dismissed: true });
  }

  function openBYOKSettings() {
    if (onManageProviders) onManageProviders();
    else window.dispatchEvent(new CustomEvent('lwc-open-account-settings', { detail: { focusBYOK: true } }));
  }

  if (isDemoSession || source !== 'shared' || dismissal?.key !== storageKey || dismissal.dismissed) return null;

  return (
    <aside className="relative max-w-4xl rounded-xl border border-emerald-200/20 bg-emerald-200/[0.05] p-4 pr-16 sm:p-5" aria-labelledby="llm-shared-notice-heading">
      <h2 id="llm-shared-notice-heading" className="text-sm font-semibold text-emerald-50">{t('LLMSource.sharedNoticeTitle')}</h2>
      <p className="mt-1 text-sm leading-6 text-zinc-300">{t('LLMSource.sharedNoticeBody')}</p>
      <button type="button" onClick={openBYOKSettings} className="mt-3 min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300">
        {t('LLMSource.personalSettingsCTA')}
      </button>
      <button type="button" onClick={dismiss} aria-label={t('LLMSource.dismiss')} className="absolute right-2 top-2 inline-flex min-h-11 min-w-11 items-center justify-center rounded-md text-lg text-zinc-400 hover:bg-white/10 hover:text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">×</button>
    </aside>
  );
}
