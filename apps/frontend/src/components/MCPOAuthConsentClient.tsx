'use client';

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useAuth } from '@/lib/auth';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { useT } from '@/lib/i18n';
import {
  decideMCPOAuthConsent,
  isSafeMCPOAuthReturnURL,
  MCPOAuthApiError,
  readMCPOAuthConsent,
  type MCPOAuthConsent,
} from '@/lib/mcp-oauth';

const transactionPattern = /^lwc_mcp_tx_[A-Za-z0-9_-]{43}$/;

export function MCPOAuthConsentClient({ transactionId }: { transactionId: string }) {
  const { t } = useT();
  const { accessToken, sessionEpoch, user, isDemoSession } = useAuth();
  const { currentProject } = useWorkspace();
  const [details, setDetails] = useState<MCPOAuthConsent | null>(null);
  const [detailsContextKey, setDetailsContextKey] = useState('');
  const [error, setError] = useState('');
  const [errorContextKey, setErrorContextKey] = useState('');
  const [busyContextKey, setBusyContextKey] = useState('');
  const [terminalContextKey, setTerminalContextKey] = useState('');
  const requestIdRef = useRef(0);
  const contextKey = `${transactionId}:${user?.id ?? ''}:${sessionEpoch}:${currentProject?.id ?? ''}`;
  const contextRef = useRef({ key: contextKey, generation: 0 });
  const mountedRef = useRef(false);
  const visibleDetails = detailsContextKey === contextKey ? details : null;
  const visibleError = errorContextKey === contextKey ? error : '';
  const loading = !visibleDetails && !visibleError;
  const busy = busyContextKey === contextKey;
  const terminal = terminalContextKey === contextKey;

  useLayoutEffect(() => {
    const current = contextRef.current;
    if (current.key !== contextKey) {
      contextRef.current = { key: contextKey, generation: current.generation + 1 };
    }
    if (requestIdRef.current > 0) {
      setDetails(null);
      setDetailsContextKey('');
      setError('');
      setErrorContextKey('');
      setBusyContextKey('');
      setTerminalContextKey('');
    }
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestIdRef.current += 1;
    };
  }, [accessToken, contextKey, isDemoSession, t, transactionId, user?.id]);

  useEffect(() => {
    let active = true;
    const requestId = ++requestIdRef.current;
    const identityKey = contextKey;
    const generation = contextRef.current.generation;
    const isCurrent = () => active && mountedRef.current && requestId === requestIdRef.current
      && contextRef.current.key === identityKey && contextRef.current.generation === generation;
    void Promise.resolve().then(async () => {
      if (!transactionPattern.test(transactionId)) throw new Error(t('MCPOAuth.invalidTransaction'));
      if (!accessToken || !user?.id || isDemoSession) throw new Error(t('MCPOAuth.signInRequired'));
      return readMCPOAuthConsent(transactionId, accessToken);
    }).then((result) => {
      if (!isCurrent()) return;
      setDetails(result);
      setDetailsContextKey(identityKey);
      setError('');
      setErrorContextKey('');
    }).catch((loadError: unknown) => {
      if (!isCurrent()) return;
      const status = loadError instanceof MCPOAuthApiError ? loadError.status : 0;
      setDetails(null);
      setDetailsContextKey('');
      setBusyContextKey('');
      setError(status === 410
        ? t('MCPOAuth.expired')
        : [403, 404].includes(status)
          ? t('MCPOAuth.notAllowed')
          : loadError instanceof Error && (loadError.message === t('MCPOAuth.invalidTransaction')
            || loadError.message === t('MCPOAuth.signInRequired'))
            ? loadError.message
            : t('MCPOAuth.loadFailed'));
      setErrorContextKey(identityKey);
      if ([403, 404, 410].includes(status)) setTerminalContextKey(identityKey);
    });
    return () => { active = false; };
  }, [accessToken, contextKey, isDemoSession, t, transactionId, user?.id]);

  const decide = async (decision: 'allow' | 'deny') => {
    if (!accessToken || !visibleDetails || busy || loading || terminal || isDemoSession
      || !mountedRef.current || contextRef.current.key !== contextKey) return;
    const generation = contextRef.current.generation;
    const requestId = requestIdRef.current;
    const isCurrent = () => mountedRef.current && requestId === requestIdRef.current
      && contextRef.current.key === contextKey && contextRef.current.generation === generation;
    setBusyContextKey(contextKey);
    setError('');
    try {
      const result = await decideMCPOAuthConsent(transactionId, decision, accessToken);
      if (!isCurrent()) return;
      setTerminalContextKey(contextKey);
      if (!isSafeMCPOAuthReturnURL(result.redirect_url)) throw new Error(t('MCPOAuth.invalidReturn'));
      window.location.assign(result.redirect_url);
    } catch (decisionError) {
      if (!isCurrent()) return;
      const message = decisionError instanceof Error && decisionError.message === t('MCPOAuth.invalidReturn')
        ? decisionError.message
        : decisionError instanceof MCPOAuthApiError && decisionError.status === 410
          ? t('MCPOAuth.expired')
          : decisionError instanceof MCPOAuthApiError && [401, 403, 404].includes(decisionError.status)
            ? t('MCPOAuth.notAllowed')
            : t('MCPOAuth.decisionFailed');
      setError(message);
      setErrorContextKey(contextKey);
      setTerminalContextKey(contextKey);
      setBusyContextKey('');
    }
  };

  return (
    <section className="mx-auto max-w-2xl rounded-2xl border border-white/10 bg-[#151515] p-5 shadow-2xl sm:p-8" aria-busy={loading || busy}>
      <p className="text-xs font-semibold uppercase tracking-[0.18em] text-emerald-300">{t('MCPOAuth.consentEyebrow')}</p>
      <h1 className="mt-3 font-serif text-2xl font-medium text-[#eeeae4]">{t('MCPOAuth.consentTitle')}</h1>
      <p className="mt-3 text-sm leading-6 text-zinc-400">{t('MCPOAuth.consentIntro')}</p>

      {loading ? <p role="status" className="mt-6 text-sm text-zinc-400">{t('MCPOAuth.loading')}</p> : null}
      {visibleError ? (
        <div className="mt-6 rounded-xl border border-amber-300/20 bg-amber-300/5 p-4">
          <p role="alert" className="text-sm leading-6 text-amber-100">{visibleError}</p>
          <Link href="/advanced" className="mt-4 inline-flex min-h-11 items-center text-sm font-medium text-emerald-300 underline underline-offset-4">{t('MCPOAuth.backToAdvanced')}</Link>
        </div>
      ) : null}
      {visibleDetails ? (
        <>
          <dl className="mt-6 grid gap-4 rounded-xl border border-white/8 bg-black/20 p-4 sm:grid-cols-2">
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.client')}</dt><dd className="mt-1 break-words text-sm font-medium text-white">{visibleDetails.client_name}</dd></div>
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.account')}</dt><dd className="mt-1 break-all text-sm font-medium text-white">{visibleDetails.account_email}</dd></div>
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.project')}</dt><dd className="mt-1 break-words text-sm font-medium text-white">{visibleDetails.project_name}<span className="mt-1 block break-all font-mono text-xs text-zinc-500">{visibleDetails.project_id}</span></dd></div>
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.scope')}</dt><dd className="mt-1 font-mono text-sm text-emerald-200">{visibleDetails.scope}</dd></div>
            <div className="sm:col-span-2"><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.resource')}</dt><dd className="mt-1 break-all font-mono text-xs leading-5 text-zinc-200">{visibleDetails.resource}</dd></div>
            <div className="sm:col-span-2"><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.expires')}</dt><dd className="mt-1 text-sm text-zinc-300">{new Date(visibleDetails.expires_at).toLocaleString()}</dd></div>
          </dl>
          <p className="mt-3 text-xs leading-5 text-zinc-500">{t('MCPOAuth.clientUnverified')}</p>
          <p className="mt-4 rounded-lg border border-emerald-300/10 bg-emerald-300/5 p-3 text-xs leading-5 text-zinc-300">{t('MCPOAuth.queryOnly')}</p>
          {terminal ? (
            <Link href="/advanced" className="mt-6 inline-flex min-h-11 items-center text-sm font-medium text-emerald-300 underline underline-offset-4">{t('MCPOAuth.backToAdvanced')}</Link>
          ) : (
            <div className="mt-6 flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <button type="button" disabled={busy || loading || terminal} onClick={() => void decide('deny')} className="min-h-11 rounded-lg border border-white/10 px-4 py-2.5 text-sm font-medium text-zinc-300 transition hover:bg-white/10 disabled:opacity-50">{busy ? t('MCPOAuth.processing') : t('MCPOAuth.deny')}</button>
              <button type="button" disabled={busy || loading || terminal} onClick={() => void decide('allow')} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:opacity-50">{busy ? t('MCPOAuth.processing') : t('MCPOAuth.allow')}</button>
            </div>
          )}
        </>
      ) : null}
    </section>
  );
}
