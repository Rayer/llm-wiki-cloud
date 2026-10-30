'use client';

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useAuth } from '@/lib/auth';
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
  const { t, locale } = useT();
  const { accessToken, user, isDemoSession } = useAuth();
  const authRef = useRef({ accessToken, userId: user?.id, isDemoSession });
  const translatorRef = useRef(t);
  const [details, setDetails] = useState<MCPOAuthConsent | null>(null);
  const [detailsContextKey, setDetailsContextKey] = useState('');
  const [error, setError] = useState('');
  const [errorContextKey, setErrorContextKey] = useState('');
  const [busyContextKey, setBusyContextKey] = useState('');
  const [terminalContextKey, setTerminalContextKey] = useState('');
  const requestIdRef = useRef(0);
  const contextKey = `${transactionId}:${user?.id ?? ''}`;
  const contextRef = useRef({ key: contextKey, generation: 0 });
  const mountedRef = useRef(false);
  const visibleDetails = detailsContextKey === contextKey ? details : null;
  const visibleError = errorContextKey === contextKey ? error : '';
  const loading = !visibleDetails && !visibleError;
  const busy = busyContextKey === contextKey;
  const terminal = terminalContextKey === contextKey;
  const fixedResource = visibleDetails?.resource_kind === 'fixed';
  const legacyProjectScopeDescription = visibleDetails && !fixedResource && !visibleDetails.scope_details?.length
    ? visibleDetails.scope === 'mcp:query'
      ? t('MCPOAuth.queryOnly')
      : visibleDetails.scope === 'mcp:query mcp:raw:write'
        ? t('MCPOAuth.queryAndRawWrite')
        : ''
    : '';

  useLayoutEffect(() => {
    authRef.current = { accessToken, userId: user?.id, isDemoSession };
    translatorRef.current = t;
  }, [accessToken, isDemoSession, t, user?.id]);

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
  }, [contextKey]);

  useEffect(() => {
    let active = true;
    const requestId = ++requestIdRef.current;
    const identityKey = contextKey;
    const generation = contextRef.current.generation;
    const isCurrent = () => active && mountedRef.current && requestId === requestIdRef.current
      && contextRef.current.key === identityKey && contextRef.current.generation === generation;
    void Promise.resolve().then(async () => {
      if (!transactionPattern.test(transactionId)) throw new Error(translatorRef.current('MCPOAuth.invalidTransaction'));
      const auth = authRef.current;
      if (!auth.accessToken || !auth.userId || auth.isDemoSession) throw new Error(translatorRef.current('MCPOAuth.signInRequired'));
      const result = await readMCPOAuthConsent(transactionId, auth.accessToken);
      if (!isCurrent()) return;
      if (result.status === 'completed') {
        if (result.requires_consent !== false || !result.redirect_url || !isSafeMCPOAuthReturnURL(result.redirect_url)) {
          throw new Error(translatorRef.current('MCPOAuth.invalidReturn'));
        }
        setTerminalContextKey(identityKey);
        window.location.assign(result.redirect_url);
        return;
      }
      const currentAuth = authRef.current;
      if (currentAuth.userId !== auth.userId || !currentAuth.accessToken || currentAuth.isDemoSession) {
        throw new Error(translatorRef.current('MCPOAuth.signInRequired'));
      }
      const continuation = await decideMCPOAuthConsent(transactionId, 'continue', currentAuth.accessToken);
      if (!isCurrent()) return;
      if (continuation.requires_consent === true) {
        setDetails(result);
        setDetailsContextKey(identityKey);
        setError('');
        setErrorContextKey('');
        setBusyContextKey('');
        return;
      }
      if (continuation.requires_consent !== false || !isSafeMCPOAuthReturnURL(continuation.redirect_url)) {
        throw new Error(translatorRef.current('MCPOAuth.invalidReturn'));
      }
      setTerminalContextKey(identityKey);
      window.location.assign(continuation.redirect_url);
    }).catch((loadError: unknown) => {
      if (!isCurrent()) return;
      const status = loadError instanceof MCPOAuthApiError ? loadError.status : 0;
      setDetails(null);
      setDetailsContextKey('');
      setBusyContextKey('');
      setError(status === 410
        ? translatorRef.current('MCPOAuth.expired')
        : [403, 404].includes(status)
          ? translatorRef.current('MCPOAuth.notAllowed')
          : loadError instanceof Error && (loadError.message === translatorRef.current('MCPOAuth.invalidTransaction')
            || loadError.message === translatorRef.current('MCPOAuth.signInRequired'))
            ? loadError.message
            : translatorRef.current('MCPOAuth.loadFailed'));
      setErrorContextKey(identityKey);
      if ([403, 404, 410].includes(status)) setTerminalContextKey(identityKey);
    });
    return () => { active = false; };
  }, [contextKey, transactionId]);

  const decide = async (decision: 'allow' | 'deny') => {
    const auth = authRef.current;
    if (!auth.accessToken || auth.isDemoSession || !visibleDetails || busy || loading || terminal
      || !mountedRef.current || contextRef.current.key !== contextKey) return;
    const generation = contextRef.current.generation;
    const requestId = requestIdRef.current;
    const isCurrent = () => mountedRef.current && requestId === requestIdRef.current
      && contextRef.current.key === contextKey && contextRef.current.generation === generation;
    setBusyContextKey(contextKey);
    setError('');
    try {
      const currentAuth = authRef.current;
      if (currentAuth.userId !== auth.userId || !currentAuth.accessToken || currentAuth.isDemoSession) return;
      const result = await decideMCPOAuthConsent(transactionId, decision, currentAuth.accessToken);
      if (!isCurrent()) return;
      setTerminalContextKey(contextKey);
      if (!isSafeMCPOAuthReturnURL(result.redirect_url)) throw new Error(translatorRef.current('MCPOAuth.invalidReturn'));
      window.location.assign(result.redirect_url);
    } catch (decisionError) {
      if (!isCurrent()) return;
      const message = decisionError instanceof Error && decisionError.message === translatorRef.current('MCPOAuth.invalidReturn')
        ? decisionError.message
        : decisionError instanceof MCPOAuthApiError && decisionError.status === 410
          ? translatorRef.current('MCPOAuth.expired')
          : decisionError instanceof MCPOAuthApiError && [401, 403, 404].includes(decisionError.status)
            ? translatorRef.current('MCPOAuth.notAllowed')
            : translatorRef.current('MCPOAuth.decisionFailed');
      setError(message);
      setErrorContextKey(contextKey);
      setTerminalContextKey(contextKey);
      setBusyContextKey('');
    }
  };

  return (
    <section className="mx-auto max-w-2xl rounded-2xl border border-white/10 bg-[#151515] p-5 shadow-2xl sm:p-8" aria-busy={loading || busy}>
      {loading ? <p role="status" className="mt-6 text-sm text-zinc-400">{t('MCPOAuth.loading')}</p> : null}
      {visibleError ? (
        <div className="mt-6 rounded-xl border border-amber-300/20 bg-amber-300/5 p-4">
          <p role="alert" className="text-sm leading-6 text-amber-100">{visibleError}</p>
          <Link href="/advanced" className="mt-4 inline-flex min-h-11 items-center text-sm font-medium text-emerald-300 underline underline-offset-4">{t('MCPOAuth.backToAdvanced')}</Link>
        </div>
      ) : null}
      {visibleDetails ? (
        <>
          <p className="text-xs font-semibold uppercase tracking-[0.18em] text-emerald-300">{t('MCPOAuth.consentEyebrow')}</p>
          <h1 className="mt-3 font-serif text-2xl font-medium text-[#eeeae4]">{t('MCPOAuth.consentTitle')}</h1>
          <p className="mt-3 text-sm leading-6 text-zinc-400">{t('MCPOAuth.consentIntro')}</p>
          <dl className="mt-6 grid gap-4 rounded-xl border border-white/8 bg-black/20 p-4 sm:grid-cols-2">
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.client')}</dt><dd className="mt-1 break-words text-sm font-medium text-white">{visibleDetails.client_name}</dd></div>
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.account')}</dt><dd className="mt-1 break-all text-sm font-medium text-white">{visibleDetails.account_email}</dd></div>
            {fixedResource ? (
              <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.resourceName')}</dt><dd className="mt-1 break-words text-sm font-medium text-white">{visibleDetails.resource_name}</dd></div>
            ) : (
              <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.project')}</dt><dd className="mt-1 break-words text-sm font-medium text-white">{visibleDetails.project_name}<span className="mt-1 block break-all font-mono text-xs text-zinc-500">{visibleDetails.project_id}</span></dd></div>
            )}
            <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.scope')}</dt><dd className="mt-1 font-mono text-sm text-emerald-200">{visibleDetails.scope}</dd></div>
            {visibleDetails.scope_details?.length ? visibleDetails.scope_details.map((scope) => (
              <div key={scope.name} className="sm:col-span-2"><dt className="font-mono text-xs text-emerald-200">{scope.name}</dt><dd className="mt-1 text-sm leading-6 text-zinc-300">{scope.description}</dd></div>
            )) : legacyProjectScopeDescription ? <p className="sm:col-span-2 text-sm leading-6 text-zinc-300">{legacyProjectScopeDescription}</p> : null}
            <div className="sm:col-span-2"><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.resource')}</dt><dd className="mt-1 break-all font-mono text-xs leading-5 text-zinc-200">{visibleDetails.resource}</dd></div>
            <div className="sm:col-span-2"><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.expires')}</dt><dd className="mt-1 text-sm text-zinc-300">{new Date(visibleDetails.expires_at).toLocaleString(locale === 'zh-TW' ? 'zh-TW' : locale === 'zh-CN' ? 'zh-CN' : 'en-US')}</dd></div>
          </dl>
          <p className="mt-3 text-xs leading-5 text-zinc-500">{t('MCPOAuth.clientUnverified')}</p>
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
