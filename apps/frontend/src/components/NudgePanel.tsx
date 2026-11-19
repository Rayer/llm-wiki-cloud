'use client';

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Sparkles, X } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useAuth } from '@/lib/auth';
import type { Project } from '@/lib/project-core';
import { getNudges, reconcileNudges, resolveNudgeTarget, setNudgeAttention, type NudgeAction, type NudgeItem, type NudgeView } from '@/lib/nudges';
import { NavigationLink, useNavigationBlocker } from './NavigationBlocker';

type NudgeStatus = Pick<NudgeView, 'partial' | 'failures' | 'notices'>;
type ProjectMetadataIdentity = { ownerId: string; sessionIdentityEpoch: number; sessionEpoch: number };
type StatusReason = { key: string; messageKey: string; projectName?: string; projectCount?: number };

const failureMessageKeys: Record<string, string> = {
  pipeline_history_partial: 'Nudge.status.pipelineHistoryPartial',
  pipeline_history_unavailable: 'Nudge.status.pipelineHistoryUnavailable',
  pipeline_evidence_unavailable: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_history_missing: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_legacy_order_ambiguous: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_terminal_unconfirmed: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_pending_reservation: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_sequence_ambiguous: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_bootstrap_raced: 'Nudge.status.pipelineHistoryUnconfirmed',
  pipeline_source_unavailable: 'Nudge.status.pipelineSourceUnavailable',
  pipeline_state_unavailable: 'Nudge.status.pipelineStateUnavailable',
  pipeline_state_conflict: 'Nudge.status.pipelineStateUnavailable',
  pipeline_project_limit: 'Nudge.status.pipelineProjectLimit',
  project_identity_unavailable: 'Nudge.status.projectIdentityUnavailable',
  project_context_unavailable: 'Nudge.status.projectContextUnavailable',
  project_unavailable: 'Nudge.status.projectUnavailable',
  project_publication_unavailable: 'Nudge.status.projectPublicationUnavailable',
  release_source_unavailable: 'Nudge.status.releaseStatusUnavailable',
  release_state_unavailable: 'Nudge.status.releaseStatusUnavailable',
  release_source_result_invalid: 'Nudge.status.releaseStatusUnavailable',
  release_source_revision_conflict: 'Nudge.status.releaseStatusUnavailable',
  release_currentness_unverified: 'Nudge.status.releaseStatusUnavailable',
  'release-transition-unavailable': 'Nudge.status.releaseStatusUnavailable',
  'release-index-unavailable': 'Nudge.status.releaseStatusUnavailable',
  'release-content-unavailable': 'Nudge.status.releaseStatusUnavailable',
  'release-observation-invalid': 'Nudge.status.releaseStatusUnavailable',
  'release-locale-content-unavailable': 'Nudge.status.releaseLocalesPartial',
};

const noticeMessageKeys: Record<string, string> = {
  ...failureMessageKeys,
  project_context_changed: 'Nudge.status.projectContextChanged',
  'currentness-unverified': 'Nudge.status.releaseStatusUnavailable',
  'current-version-not-indexed': 'Nudge.status.releaseVersionNotIndexed',
  'some-current-locales-unavailable': 'Nudge.status.releaseLocalesPartial',
};

function projectLabel(projectID: string | undefined, projects: Project[], metadataIsCurrent: boolean): string | undefined {
  if (!metadataIsCurrent || !projectID) return undefined;
  const project = projects.find((candidate) => candidate.id.trim() === projectID.trim());
  const name = project?.name.trim();
  return name && name !== project?.id.trim() ? name : undefined;
}

function projectIDFromSourceKey(sourceKey: string, projects: Project[], metadataIsCurrent: boolean): string | undefined {
  if (!metadataIsCurrent) return undefined;
  return projects
    .filter((project) => sourceKey.endsWith(`:${project.id}`))
    .sort((left, right) => right.id.length - left.id.length)[0]?.id;
}

function collectStatusReasons(
  statuses: Array<NudgeStatus | null>,
  projects: Project[],
  metadataIsCurrent: boolean,
): StatusReason[] {
  const failures = new Map<string, NudgeView['failures'][number]>();
  const notices = new Map<string, NudgeView['notices'][number]>();
  for (const status of statuses) {
    for (const failure of status?.failures ?? []) {
      failures.set(JSON.stringify([failure.project_id ?? '', failure.safe_class]), failure);
    }
    for (const notice of status?.notices ?? []) {
      notices.set(JSON.stringify([notice.source_key, notice.state, notice.safe_class ?? '']), notice);
    }
  }

  const groups = new Map<string, {
    key: string;
    messageKey: string;
    projectName?: string;
    projectIDs: Set<string>;
    otherProjectKeys: Set<string>;
  }>();
  const addReason = (messageKey: string, identity: string, projectID?: string, countAsProject = false) => {
    const name = projectLabel(projectID, projects, metadataIsCurrent);
    const groupKey = JSON.stringify([messageKey, name ?? '']);
    let group = groups.get(groupKey);
    if (!group) {
      group = { key: groupKey, messageKey, projectName: name, projectIDs: new Set(), otherProjectKeys: new Set() };
      groups.set(groupKey, group);
    }
    if (projectID) group.projectIDs.add(projectID);
    else if (countAsProject) group.otherProjectKeys.add(identity);
  };

  for (const [identity, failure] of failures) {
    const safeClass = failure.safe_class || '';
    addReason(failureMessageKeys[safeClass] ?? 'Nudge.status.generic', identity, failure.project_id || undefined);
  }
  for (const [identity, notice] of notices) {
    const safeClass = notice.safe_class || '';
    const projectID = projectIDFromSourceKey(notice.source_key, projects, metadataIsCurrent);
    const messageKey = noticeMessageKeys[safeClass]
      ?? (!safeClass && notice.state === 'stale' ? 'Nudge.status.stale' : 'Nudge.status.generic');
    addReason(messageKey, identity, projectID, safeClass === 'project_context_changed');
  }

  return [...groups.values()].map((group) => {
    const projectCount = group.projectIDs.size + group.otherProjectKeys.size;
    return {
      key: group.key,
      messageKey: group.messageKey,
      ...(group.projectName && projectCount === 1 ? { projectName: group.projectName } : {}),
      ...(projectCount > 0 && (!group.projectName || projectCount > 1) ? { projectCount } : {}),
    };
  });
}

export function NudgePanel({
  open, onOpen, onClose, projects = [], projectsIdentity = null,
}: {
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  projects?: Project[];
  projectsIdentity?: ProjectMetadataIdentity | null;
}) {
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
    identity: string; value: NudgeStatus;
  } | null>(null);
  const [projectionStatusState, setProjectionStatusState] = useState<{
    identity: string; value: NudgeStatus;
  } | null>(null);
  const [errorState, setErrorState] = useState<{ identity: string; value: string } | null>(null);
  const [busyState, setBusyState] = useState<{ identity: string; value: boolean } | null>(null);
  const [copiedState, setCopiedState] = useState<{ identity: string; sourceKey: string; actionId: string } | null>(null);
  const [fallbackState, setFallbackState] = useState<{
    identity: string; text: string; href: string; action: NudgeAction; item: NudgeItem;
  } | null>(null);
  const view = viewState?.identity === requestIdentity ? viewState.value : null;
  const sourceStatus = sourceStatusState?.identity === requestIdentity ? sourceStatusState.value : null;
  const projectionStatus = projectionStatusState?.identity === requestIdentity ? projectionStatusState.value : null;
  const currentNotices = [...(sourceStatus?.notices ?? []), ...(projectionStatus?.notices ?? [])];
  const statusReasons = collectStatusReasons([sourceStatus, projectionStatus], projects,
    projectsIdentity?.ownerId === (user?.id ?? '') &&
    projectsIdentity.sessionIdentityEpoch === sessionIdentityEpoch &&
    projectsIdentity.sessionEpoch === sessionEpoch);
  const hasPartialStatus = Boolean(sourceStatus?.partial || projectionStatus?.partial || view?.partial || statusReasons.length);
  const error = errorState?.identity === requestIdentity ? errorState.value : '';
  const busy = busyState?.identity === requestIdentity && busyState.value;
  const fallback = fallbackState?.identity === requestIdentity ? fallbackState : null;
  const isSourceStale = (sourceKey: string) => currentNotices.some(
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
      setProjectionStatusState({
        identity,
        value: { partial: saved.partial, failures: saved.failures, notices: saved.notices ?? [] },
      });
      if (reconcile) {
        setSourceStatusState({
          identity,
          value: { partial: saved.partial, failures: saved.failures, notices: saved.notices ?? [] },
        });
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
      : hasPartialStatus
        ? { role: 'status' as const, message: t('Nudge.partialCue') }
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
        {hasPartialStatus ? <div role="status" className="mt-4 rounded border border-amber-300/20 bg-amber-400/5 p-3 text-sm text-amber-100">
          <p>{t('Nudge.partial')}</p>
          <ul className="mt-2 list-disc space-y-1 pl-5">
            {(statusReasons.length ? statusReasons : [{ key: 'unclassified', messageKey: 'Nudge.status.unclassified' }]).map((reason) => (
              <li key={reason.key}>
                {t(reason.messageKey)}{' '}
                {reason.projectName ? t('Nudge.status.affectedProject', { project: reason.projectName }) : null}
                {reason.projectCount ? t('Nudge.status.affectedCount', { count: reason.projectCount }) : null}
              </li>
            ))}
          </ul>
        </div> : null}
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
