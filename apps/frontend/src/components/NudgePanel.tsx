'use client';

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Sparkles, X } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useAuth } from '@/lib/auth';
import { getNudges, reconcileNudges, resolveNudgeTarget, setNudgeAttention, type NudgeAction, type NudgeItem, type NudgeView } from '@/lib/nudges';
import { NavigationLink, useNavigationBlocker } from './NavigationBlocker';

export function NudgePanel({ open, onOpen, onClose }: { open: boolean; onOpen: () => void; onClose: () => void }) {
  const { t } = useT();
  const { confirmNavigation } = useNavigationBlocker();
  const { user, sessionIdentityEpoch, sessionEpoch, isDemoSession } = useAuth();
  const requestIdentity = JSON.stringify([user?.id ?? null, sessionIdentityEpoch, sessionEpoch, isDemoSession]);
  const requestIdentityRef = useRef(requestIdentity);
  const viewRequestSequenceRef = useRef(0);
  const initialReconcileIdentityRef = useRef<string | null>(null);
  const mountedRef = useRef(false);
  useLayoutEffect(() => { requestIdentityRef.current = requestIdentity; }, [requestIdentity]);
  const [viewState, setViewState] = useState<{ identity: string; value: NudgeView } | null>(null);
  const [sourceStatusState, setSourceStatusState] = useState<{
    identity: string; value: Pick<NudgeView, 'partial' | 'failures' | 'notices'>;
  } | null>(null);
  const [errorState, setErrorState] = useState<{ identity: string; value: string } | null>(null);
  const [busyState, setBusyState] = useState<{ identity: string; value: boolean } | null>(null);
  const [copiedState, setCopiedState] = useState<{ identity: string; sourceKey: string; actionId: string } | null>(null);
  const [fallbackState, setFallbackState] = useState<{
    identity: string; text: string; href: string; action: NudgeAction; item: NudgeItem;
  } | null>(null);
  const view = viewState?.identity === requestIdentity ? viewState.value : null;
  const sourceStatus = sourceStatusState?.identity === requestIdentity ? sourceStatusState.value : null;
  const error = errorState?.identity === requestIdentity ? errorState.value : '';
  const busy = busyState?.identity === requestIdentity && busyState.value;
  const fallback = fallbackState?.identity === requestIdentity ? fallbackState : null;
  const isSourceStale = (sourceKey: string) => sourceStatus?.notices?.some(
    (notice) => notice.state === 'stale' && notice.source_key === sourceKey,
  ) ?? false;
  const isActionCopied = (sourceKey: string, actionId: string) => copiedState?.identity === requestIdentity &&
    copiedState.sourceKey === sourceKey && copiedState.actionId === actionId;

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const refresh = useCallback(async (identity: string, reconcile = false, trackBusy = true) => {
    if (requestIdentityRef.current !== identity) return;
    const requestSequence = ++viewRequestSequenceRef.current;
    const isCurrent = () => mountedRef.current && requestIdentityRef.current === identity && viewRequestSequenceRef.current === requestSequence;
    if (trackBusy) setBusyState({ identity, value: true });
    try {
      const saved = reconcile ? await reconcileNudges() : await getNudges();
      if (!isCurrent()) return;
      setViewState({ identity, value: saved });
      if (reconcile) {
        setSourceStatusState({
          identity,
          value: { partial: saved.partial, failures: saved.failures, notices: saved.notices ?? [] },
        });
      } else if (saved.partial) {
        setSourceStatusState((current) => ({
          identity,
          value: {
            partial: true,
            failures: saved.failures,
            // GET is persisted-only. Keep source notices from the latest same-session
            // reconciliation instead of reviving or dropping them from an old closure.
            notices: current?.identity === identity ? current.value.notices ?? [] : saved.notices ?? [],
          },
        }));
      }
      setErrorState({ identity, value: '' });
    } catch {
      if (isCurrent()) {
        setErrorState({ identity, value: t('Nudge.loadFailed') });
      }
    } finally {
      if (trackBusy && isCurrent()) {
        setBusyState({ identity, value: false });
      }
    }
  }, [t]);

  useEffect(() => {
    if (!user || isDemoSession || initialReconcileIdentityRef.current === requestIdentity) return;
    initialReconcileIdentityRef.current = requestIdentity;
    void refresh(requestIdentity, true, false);
  }, [isDemoSession, refresh, requestIdentity, user, user?.id]);

  const markReadAfterNavigation = async (item: NudgeItem, identity = requestIdentity) => {
    if (requestIdentityRef.current !== identity || !item.attention_id || item.severity === 'urgent') return;
    try {
      await setNudgeAttention(item.category, item.attention_id, 'read');
      if (requestIdentityRef.current === identity) await refresh(identity);
    } catch {
      if (requestIdentityRef.current === identity) {
        await refresh(identity);
        if (requestIdentityRef.current === identity) {
          setErrorState({ identity, value: t('Nudge.readNotConfirmed') });
        }
      }
    }
  };

  const acceptTargetNavigation = (item: NudgeItem, identity = requestIdentity) => {
    if (requestIdentityRef.current !== identity) return;
    onClose();
    void markReadAfterNavigation(item, identity);
  };

  const copyThenOpenTarget = async (item: NudgeItem, sourceKey: string, action: NudgeAction, href: string) => {
    const identity = requestIdentity;
    if (requestIdentityRef.current !== identity) return;
    setErrorState({ identity, value: '' });
    setFallbackState(null);
    let popup: Window | null = null;
    try { popup = window.open('about:blank', '_blank'); } catch { /* expose the explicit fallback below */ }
    if (popup) {
      try { popup.opener = null; } catch { /* browsers may make opener read-only */ }
    }
    try {
      await navigator.clipboard.writeText(action.copy_text ?? '');
      if (requestIdentityRef.current !== identity) {
        try { popup?.close(); } catch { /* the gesture may already have closed it */ }
        return;
      }
      setCopiedState({ identity, sourceKey, actionId: action.id });
      if (!popup) {
        setFallbackState({ identity, text: action.copy_text ?? '', href, action, item });
        setErrorState({ identity, value: t('Nudge.popupBlocked') });
        return;
      }
      try {
        popup.location.replace(href);
        void markReadAfterNavigation(item, identity);
      } catch {
        try { popup.close(); } catch { /* the browser may already have closed the tab */ }
        setFallbackState({ identity, text: action.copy_text ?? '', href, action, item });
        setErrorState({ identity, value: t('Nudge.navigationFailed') });
      }
    } catch {
      try { popup?.close(); } catch { /* the gesture may already have closed the tab */ }
      if (requestIdentityRef.current !== identity) return;
      setCopiedState(null);
      setFallbackState({ identity, text: action.copy_text ?? '', href, action, item });
      setErrorState({ identity, value: t('Nudge.copyFailed') });
    }
  };

  const runAction = async (item: NudgeItem, sourceKey: string, action: NudgeAction) => {
    const identity = requestIdentity;
    if (requestIdentityRef.current !== identity) return;
    const href = action.target ? resolveNudgeTarget(action.target) : null;
    if (action.target && !href) {
      setErrorState({ identity, value: t('Nudge.targetUnavailable') });
      return;
    }
    if (action.kind !== 'copy') {
      setErrorState({ identity, value: t('Nudge.targetUnavailable') });
      return;
    }
    setErrorState({ identity, value: '' });
    setFallbackState(null);
    try {
      await navigator.clipboard.writeText(action.copy_text ?? '');
      if (requestIdentityRef.current !== identity) return;
      setCopiedState({ identity, sourceKey, actionId: action.id });
    } catch {
      if (requestIdentityRef.current !== identity) return;
      setCopiedState(null);
      setErrorState({ identity, value: t('Nudge.copyFailed') });
      setFallbackState({ identity, text: action.copy_text ?? '', href: href ?? '', action, item });
    }
  };

  const toggleAttention = async (item: NudgeItem) => {
    const identity = requestIdentity;
    if (requestIdentityRef.current !== identity || !item.attention_id || item.severity === 'urgent') return;
    const action = item.attention_state === 'read' ? 'unread' : 'read';
    setBusyState({ identity, value: true });
    setErrorState({ identity, value: '' });
    try {
      await setNudgeAttention(item.category, item.attention_id, action);
      if (requestIdentityRef.current === identity) await refresh(identity);
    } catch {
      if (requestIdentityRef.current === identity) {
        await refresh(identity);
        if (requestIdentityRef.current === identity) setErrorState({ identity, value: t('Nudge.actionFailed') });
      }
    } finally {
      if (requestIdentityRef.current === identity) setBusyState({ identity, value: false });
    }
  };

  const dismiss = async (item: NudgeItem) => {
    const identity = requestIdentity;
    if (requestIdentityRef.current !== identity || !item.attention_id || item.severity === 'urgent') return;
    setBusyState({ identity, value: true });
    try {
      await setNudgeAttention(item.category, item.attention_id, 'dismiss');
      if (requestIdentityRef.current === identity) await refresh(identity);
    } catch {
      if (requestIdentityRef.current === identity) setErrorState({ identity, value: t('Nudge.actionFailed') });
    } finally {
      if (requestIdentityRef.current === identity) setBusyState({ identity, value: false });
    }
  };

  if (!user || isDemoSession) return null;
  const hasUnread = view?.items.some((item) => item.severity !== 'urgent' && item.attention_state === 'unread') ?? false;
  const closedFeedback = error
    ? { role: 'alert' as const, message: error }
    : !view
      ? { role: 'status' as const, message: t('Nudge.statusUnknown') }
      : sourceStatus?.partial ?? view.partial
        ? { role: 'status' as const, message: t('Nudge.partial') }
        : null;

  return (
    <>
      <div className="order-2 ml-auto flex items-center gap-2">
      <button type="button" onClick={onOpen} aria-label={hasUnread ? `${t('Nudge.open')}, ${t('Nudge.unreadCue')}` : t('Nudge.open')} aria-expanded={open} className="relative inline-flex min-h-11 min-w-11 items-center justify-center rounded border border-white/10 bg-white/5 px-3 text-sm text-zinc-200 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
        <Sparkles className="size-4" aria-hidden="true" />
        <span className="sr-only">{t('Nudge.open')}</span>
        {hasUnread ? <span aria-hidden="true" className="absolute right-1.5 top-1.5 size-2 rounded-full bg-amber-300 ring-2 ring-zinc-950" /> : null}
      </button>
      {!open && closedFeedback ? <p role={closedFeedback.role} className="max-w-56 text-xs text-amber-100">{closedFeedback.message}</p> : null}
      </div>
      {open ? <div className="fixed inset-0 z-[90] flex justify-end bg-black/60" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <section role="dialog" aria-modal="true" aria-labelledby="nudge-title" className="h-full w-full max-w-xl overflow-y-auto border-l border-white/10 bg-[#191B1C] p-5 shadow-2xl sm:p-7">
        <div className="flex items-start justify-between gap-4">
          <div><h2 id="nudge-title" className="text-xl font-semibold text-white">{t('Nudge.title')}</h2><p className="mt-1 text-sm text-zinc-400">{t('Nudge.description')}</p></div>
          <button type="button" onClick={onClose} aria-label={t('Nudge.close')} className="min-h-11 min-w-11 rounded text-zinc-400 hover:bg-white/8 hover:text-white"><X className="mx-auto size-5" /></button>
        </div>
        {error ? <p role="alert" className="mt-5 rounded border border-amber-300/30 bg-amber-400/8 p-3 text-sm text-amber-100">{error}</p> : null}
        {(sourceStatus?.partial ?? view?.partial) ? <p role="status" className="mt-4 text-sm text-amber-100">{t('Nudge.partial')}</p> : null}
        {!view && !error ? <p role="status" className="mt-5 text-sm text-zinc-400">{t('Nudge.loading')}</p> : null}
        {view?.items.length === 0 ? <p className="mt-5 text-sm text-zinc-400">{t('Nudge.empty')}</p> : null}
        <div className="mt-5 space-y-4">
          {view?.items.map((item) => (
            <article key={item.category} className={`rounded border p-4 ${item.severity === 'urgent' ? 'border-red-300/50 bg-red-950/30' : item.severity === 'attention' ? 'border-amber-300/60 bg-amber-950/55' : 'border-emerald-300/50 bg-emerald-950/30'}`}>
              <div className="flex items-start justify-between gap-3">
                <div><p className="text-xs font-semibold uppercase tracking-wide text-zinc-400">{item.category}</p><p className="mt-1 text-sm text-zinc-300">{item.severity === 'urgent' ? t('Nudge.urgent') : item.attention_state === 'read' ? t('Nudge.read') : t('Nudge.unread')}</p></div>
                {item.severity !== 'urgent' ? <div className="flex gap-2">
                  <button disabled={busy} type="button" onClick={() => void toggleAttention(item)} className="min-h-11 rounded border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/5">{t(item.attention_state === 'read' ? 'Nudge.markUnread' : 'Nudge.markRead')}</button>
                  <button disabled={busy} type="button" onClick={() => void dismiss(item)} className="min-h-11 rounded border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/5">{t('Nudge.dismiss')}</button>
                </div> : null}
              </div>
              <div className="mt-3 space-y-3">
                {item.observations.map((slot) => {
                  const observation = slot.observation;
                  if (!observation) return null;
                  return <div key={slot.source_key} className="border-t border-white/8 pt-3">
                    {observation.project_name ? <p className="text-xs text-emerald-200">{observation.project_name}</p> : null}
                    <h3 className="mt-1 font-medium text-white">{observation.title}</h3>
                    <p className="mt-1 whitespace-pre-wrap text-sm leading-6 text-zinc-300">{observation.body}</p>
                    {observation.declared_purpose || observation.published_summary ? <div className="mt-3 grid gap-3 rounded bg-black/20 p-3 text-sm sm:grid-cols-2">
                      {observation.declared_purpose ? <div><h4 className="font-medium text-zinc-200">{t('Nudge.declaredPurpose')}</h4><p className="mt-1 whitespace-pre-wrap break-words leading-6 text-zinc-300">{observation.declared_purpose}</p></div> : null}
                      {observation.published_summary ? <div><h4 className="font-medium text-zinc-200">{t('Nudge.publishedOverview')}</h4><p className="mt-1 whitespace-pre-wrap break-words leading-6 text-zinc-300">{observation.published_summary}</p></div> : null}
                      {observation.coverage ? <p className="text-xs text-zinc-400 sm:col-span-2">{t('Nudge.coverage', { selected: observation.coverage.selected_concepts, total: observation.coverage.total_concepts })}{observation.coverage.sampled ? ` ${t('Nudge.sampled')}` : ''}{observation.coverage.title_truncated_concepts + observation.coverage.body_truncated_concepts > 0 ? ` ${t('Nudge.truncated')}` : ''}</p> : null}
                    </div> : null}
                    {observation.stale || isSourceStale(slot.source_key)
                      ? <p className="mt-2 text-xs text-amber-100">{t('Nudge.stale')}</p>
                      : null}
                    <div className="mt-3 flex flex-wrap gap-2">
                      {observation.actions.map((action) => {
                        const href = action.target ? resolveNudgeTarget(action.target) : null;
                        const label = isActionCopied(slot.source_key, action.id) ? t('Nudge.copied') : action.label;
                        const className = 'inline-flex min-h-11 items-center rounded bg-[#DCE9E1] px-3 text-sm font-medium text-[#17231D] disabled:opacity-60';
                        if (action.kind === 'link' && href) {
                          return <NavigationLink key={action.id} href={href} onNavigate={() => acceptTargetNavigation(item, requestIdentity)} className={className}>{label}</NavigationLink>;
                        }
                        if (action.kind === 'copy-then-link' && href) {
                          return <button key={action.id} type="button" disabled={busy} onAuxClick={(event) => {
                            if (event.button === 1) event.preventDefault();
                          }} onClick={() => {
                            if (!confirmNavigation()) return;
                            void copyThenOpenTarget(item, slot.source_key, action, href);
                          }} className={className}>{label}</button>;
                        }
                        return <button key={action.id} type="button" disabled={busy} onClick={() => void runAction(item, slot.source_key, action)} className={className}>
                          {label}
                        </button>;
                      })}
                    </div>
                  </div>;
                })}
              </div>
            </article>
          ))}
        </div>
        {fallback ? <div className="mt-5 rounded border border-amber-200/30 bg-amber-400/8 p-4">
          <label htmlFor="nudge-copy-fallback" className="text-sm font-medium text-amber-100">{t('Nudge.selectCopy')}</label>
          <textarea id="nudge-copy-fallback" readOnly value={fallback.text} onFocus={(event) => event.currentTarget.select()} className="mt-2 min-h-28 w-full rounded border border-white/10 bg-black/30 p-2 text-sm text-white" />
          {fallback.href ? <NavigationLink href={fallback.href} onNavigate={() => fallback.action.kind === 'copy-then-link' && acceptTargetNavigation(fallback.item, fallback.identity)} className="mt-3 inline-flex min-h-11 items-center text-sm text-emerald-200 underline">{t('Nudge.openTarget')}</NavigationLink> : null}
        </div> : null}
        <button type="button" onClick={() => void refresh(requestIdentity, true)} className="mt-6 min-h-11 rounded border border-white/15 px-4 text-sm text-zinc-200 hover:bg-white/5">{t('Nudge.refresh')}</button>
      </section>
      </div> : null}
    </>
  );
}
