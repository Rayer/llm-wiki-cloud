'use client';

import { useEffect, useRef, useState } from 'react';
import { createAgentConnectionCheck, getAgentConnectionCheck, type AgentConnectionCheck } from '@/lib/agent-connection';
import type { Locale } from '@/lib/i18n-core';

type Translate = (key: string, params?: Record<string, string | number>) => string;

type AgentConnectionCheckProps = {
  projectId: string;
  identityKey: string;
  locale: Locale;
  t: Translate;
};

export function AgentConnectionCheck(props: AgentConnectionCheckProps) {
  return <AgentConnectionCheckSession key={`${props.identityKey}:${props.projectId}`} {...props} />;
}

function AgentConnectionCheckSession({ projectId, identityKey, locale, t }: AgentConnectionCheckProps) {
  const key = `${identityKey}:${projectId}`;
  const stateRef = useRef<AgentConnectionCheck | null>(null);
  const requestSequence = useRef(0);
  const reloadRef = useRef<(() => Promise<void>) | null>(null);
  const startPollingRef = useRef<() => void>(() => {});
  const creatingRef = useRef(false);
  const mountedRef = useRef(false);
  const [state, setState] = useState<AgentConnectionCheck | null>(null);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [createFailed, setCreateFailed] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [copyStatus, setCopyStatus] = useState<'prompt' | 'error' | null>(null);

  useEffect(() => {
    let active = true;
    mountedRef.current = true;
    let interval: number | null = null;
    const isCurrent = () => active && mountedRef.current;
    const stopPolling = () => {
      if (interval !== null) window.clearInterval(interval);
      interval = null;
    };
    const startPolling = () => {
      if (interval !== null) return;
      interval = window.setInterval(() => {
        if (document.visibilityState === 'visible' && stateRef.current?.status === 'waiting') {
          void loadCurrent();
        }
      }, 5000);
    };
    const loadCurrent = async () => {
      if (creatingRef.current) return;
      const sequence = ++requestSequence.current;
      try {
        const next = await getAgentConnectionCheck(projectId);
        if (!isCurrent() || requestSequence.current !== sequence) return;
        const previousId = stateRef.current?.setup_id;
        if (previousId && next.setup_id && previousId !== next.setup_id) setReplaced(true);
        stateRef.current = next;
        setState(next);
        setLoadFailed(false);
        setLoading(false);
        if (next.status === 'waiting') startPolling();
        else stopPolling();
      } catch {
        if (!isCurrent() || requestSequence.current !== sequence) return;
        stateRef.current = null;
        setState(null);
        setLoadFailed(true);
        setLoading(false);
        stopPolling();
      }
    };
    reloadRef.current = loadCurrent;
    startPollingRef.current = startPolling;
    const onVisibilityChange = () => {
      if (document.visibilityState === 'hidden') stopPolling();
      else {
        void loadCurrent();
        if (stateRef.current?.status === 'waiting') startPolling();
      }
    };
    const onFocus = () => { void loadCurrent(); };
    document.addEventListener('visibilitychange', onVisibilityChange);
    window.addEventListener('focus', onFocus);
    void loadCurrent();
    return () => {
      active = false;
      mountedRef.current = false;
      requestSequence.current += 1;
      stopPolling();
      document.removeEventListener('visibilitychange', onVisibilityChange);
      window.removeEventListener('focus', onFocus);
      if (reloadRef.current === loadCurrent) reloadRef.current = null;
      if (startPollingRef.current === startPolling) startPollingRef.current = () => {};
    };
  }, [key, projectId]);

  const createSetup = async () => {
    if (creatingRef.current) return;
    creatingRef.current = true;
    const sequence = ++requestSequence.current;
    setCreating(true);
    setCreateFailed(false);
    setLoadFailed(false);
    setReplaced(false);
    setCopyStatus(null);
    stateRef.current = null;
    setState(null);
    let failed = false;
    try {
      const next = await createAgentConnectionCheck(projectId);
      if (!mountedRef.current || requestSequence.current !== sequence) return;
      stateRef.current = next;
      setState(next);
      setLoading(false);
      startPollingRef.current();
    } catch {
      failed = true;
      if (mountedRef.current && requestSequence.current === sequence) {
        setLoadFailed(true);
        setLoading(false);
        setCreateFailed(true);
      }
    } finally {
      creatingRef.current = false;
      if (mountedRef.current && requestSequence.current === sequence) {
        setCreating(false);
        if (failed) void reloadRef.current?.();
      }
    }
  };

  const copyText = async (text: string) => {
    setCopyStatus(null);
    try {
      await navigator.clipboard.writeText(text);
      if (!mountedRef.current) return;
      setCopyStatus('prompt');
    } catch {
      if (mountedRef.current) setCopyStatus('error');
    }
  };

  const prompt = state?.status === 'waiting' && state.setup_id && state.confirmation_token
    ? t('MCPOAuth.connection.confirmPrompt', { setup_id: state.setup_id, confirmation_token: state.confirmation_token })
    : '';
  const formattedConfirmedAt = state?.status === 'confirmed' && state.confirmed_at
    ? new Date(state.confirmed_at).toLocaleString(locale === 'zh-TW' ? 'zh-TW' : locale === 'zh-CN' ? 'zh-CN' : 'en-US')
    : '';

  return (
    <section className="rounded border border-white/10 bg-black/20 p-5 sm:p-6" aria-labelledby="agent-connection-check-title">
      <h3 id="agent-connection-check-title" className="text-lg font-semibold text-white">{t('MCPOAuth.connection.title')}</h3>
      <p className="mt-2 max-w-3xl text-sm leading-6 text-zinc-300">{t('MCPOAuth.connection.intro')}</p>
      <p className="mt-2 text-xs leading-5 text-zinc-400">{t('MCPOAuth.connection.refreshClient')}</p>

      {loading ? <p role="status" className="mt-4 text-sm text-zinc-400">{t('MCPOAuth.connection.loading')}</p> : null}
      {loadFailed ? <div className="mt-4 flex flex-wrap items-center gap-3"><p role="alert" className="text-sm text-amber-100">{t('MCPOAuth.connection.unavailable')}</p><button type="button" onClick={() => void reloadRef.current?.()} className="min-h-10 rounded border border-white/15 px-3 text-sm text-zinc-100 hover:bg-white/5">{t('MCPOAuth.connection.retry')}</button></div> : null}
      {createFailed ? <p role="alert" className="mt-3 text-sm text-amber-100">{t('MCPOAuth.connection.createFailed')}</p> : null}
      {!loading && !loadFailed && state?.status === 'not_started' ? <p role="status" className="mt-4 text-sm text-zinc-300">{t('MCPOAuth.connection.notStarted')}</p> : null}
      {!loading && !loadFailed && state?.status === 'waiting' ? <p role="status" className="mt-4 text-sm text-amber-100">{t('MCPOAuth.connection.waiting')}</p> : null}
      {!loading && !loadFailed && state?.status === 'expired' ? <p role="status" className="mt-4 text-sm text-amber-100">{t('MCPOAuth.connection.expired')}</p> : null}
      {!loading && !loadFailed && state?.status === 'confirmed' ? (
        <div className="mt-4 rounded border border-emerald-300/20 bg-emerald-300/5 p-4">
          <p role="status" className="text-sm font-medium text-emerald-100">{t('MCPOAuth.connection.confirmed')}</p>
          <p className="mt-2 text-sm text-zinc-300">{t('MCPOAuth.connection.confirmedAt')} <time dateTime={state.confirmed_at ?? undefined}>{formattedConfirmedAt}</time></p>
        </div>
      ) : null}
      {replaced ? <p role="status" className="mt-3 text-sm text-amber-100">{t('MCPOAuth.connection.replaced')}</p> : null}

      {state?.status === 'waiting' && prompt ? (
        <div className="mt-4 rounded border border-white/10 bg-black/20 p-4">
          <details>
            <summary className="cursor-pointer text-sm font-medium text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">{t('MCPOAuth.connection.promptDetails')}</summary>
            <p className="mt-3 break-words whitespace-pre-wrap font-mono text-xs leading-5 text-zinc-300">{prompt}</p>
          </details>
          <div className="mt-4 flex flex-wrap gap-3">
            <button type="button" onClick={() => void copyText(prompt)} className="min-h-11 rounded bg-emerald-300 px-4 py-2 text-sm font-semibold text-black hover:bg-emerald-200">{t('MCPOAuth.connection.copyPrompt')}</button>
          </div>
        </div>
      ) : null}
      {copyStatus ? <p role="status" className="mt-3 text-sm text-zinc-300">{t(`MCPOAuth.connection.${copyStatus === 'error' ? 'copyFailed' : 'promptCopied'}`)}</p> : null}
      <button type="button" disabled={creating || loading} onClick={() => void createSetup()} aria-busy={creating} className="mt-4 min-h-11 rounded border border-white/15 px-4 py-2 text-sm font-medium text-zinc-100 hover:bg-white/5 disabled:cursor-wait disabled:opacity-50">
        {creating ? t('MCPOAuth.connection.creating') : state?.setup_id ? t('MCPOAuth.connection.replaceSetup') : t('MCPOAuth.connection.createSetup')}
      </button>
    </section>
  );
}
