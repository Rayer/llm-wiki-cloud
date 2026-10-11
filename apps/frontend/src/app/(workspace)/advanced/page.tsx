'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import Link from 'next/link';
import { useAuth } from '@/lib/auth';
import { ProjectKeysSection } from '@/components/ProjectKeysSection';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { useT } from '@/lib/i18n';
import {
  listMCPOAuthGrants,
  mcpResourceURL,
  MCPOAuthApiError,
  revokeMCPOAuthGrant,
  type MCPOAuthGrant,
} from '@/lib/mcp-oauth';

export default function AdvancedPage() {
  const { t } = useT();
  const { accessToken, sessionEpoch, user, isDemoSession } = useAuth();
  const { hydrated, currentProject, projectsLoading } = useWorkspace();
  const [grants, setGrants] = useState<MCPOAuthGrant[]>([]);
  const [grantsError, setGrantsError] = useState('');
  const [grantsContextKey, setGrantsContextKey] = useState('');
  const [grantsErrorContextKey, setGrantsErrorContextKey] = useState('');
  const [busyGrant, setBusyGrant] = useState('');
  const [notice, setNotice] = useState('');
  const [copied, setCopied] = useState(false);
  const requestIdRef = useRef(0);
  const identityRef = useRef({ projectId: currentProject?.id ?? '', userId: user?.id ?? '', sessionEpoch });
  const contextKey = `${currentProject?.id ?? ''}:${user?.id ?? ''}:${sessionEpoch}`;
  const visibleGrants = grantsContextKey === contextKey ? grants : [];
  const visibleGrantsError = grantsErrorContextKey === contextKey ? grantsError : '';
  const grantsLoading = !visibleGrantsError && grantsContextKey !== contextKey;

  const resourceURL = currentProject ? mcpResourceURL(currentProject.id) : '';
  const configSnippet = useMemo(() => resourceURL ? [
    'mcp_servers:',
    '  llm_wiki:',
    `    url: ${JSON.stringify(resourceURL)}`,
    '    auth: oauth',
    '    oauth:',
    '      cimd: false',
    '      scope: mcp:query',
  ].join('\n') : '', [resourceURL]);

  const loadGrants = useCallback(async () => {
    const requestId = ++requestIdRef.current;
    const identity = { projectId: currentProject?.id ?? '', userId: user?.id ?? '', sessionEpoch };
    const identityKey = `${identity.projectId}:${identity.userId}:${identity.sessionEpoch}`;
    if (!currentProject || !accessToken || isDemoSession) {
      setGrants([]);
      return;
    }
    try {
      const result = await listMCPOAuthGrants(currentProject.id, accessToken);
      if (requestId !== requestIdRef.current || identityRef.current.projectId !== identity.projectId
        || identityRef.current.userId !== identity.userId || identityRef.current.sessionEpoch !== identity.sessionEpoch) return;
      setGrants(result);
      setGrantsContextKey(identityKey);
      setGrantsError('');
      setGrantsErrorContextKey('');
    } catch (error) {
      if (requestId !== requestIdRef.current || identityRef.current.projectId !== identity.projectId
        || identityRef.current.userId !== identity.userId || identityRef.current.sessionEpoch !== identity.sessionEpoch) return;
      setGrants([]);
      setGrantsError(error instanceof MCPOAuthApiError && error.status === 403
        ? t('MCPOAuth.notAllowed')
        : t('MCPOAuth.grantsLoadFailed'));
      setGrantsErrorContextKey(identityKey);
    }
  }, [accessToken, currentProject, isDemoSession, sessionEpoch, t, user?.id]);

  useEffect(() => {
    identityRef.current = { projectId: currentProject?.id ?? '', userId: user?.id ?? '', sessionEpoch };
    requestIdRef.current += 1;
  }, [currentProject?.id, sessionEpoch, user?.id]);

  useEffect(() => { void Promise.resolve().then(() => loadGrants()); }, [loadGrants]);

  const copyConfig = async () => {
    setNotice('');
    try {
      await navigator.clipboard.writeText(configSnippet);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setNotice(t('MCPOAuth.copyFailed'));
    }
  };

  const revoke = async (grant: MCPOAuthGrant) => {
    if (!accessToken || busyGrant || grant.status !== 'active') return;
    const identity = { ...identityRef.current };
    setBusyGrant(grant.id);
    setNotice('');
    try {
      await revokeMCPOAuthGrant(grant.id, accessToken);
      if (identityRef.current.projectId !== identity.projectId || identityRef.current.userId !== identity.userId
        || identityRef.current.sessionEpoch !== identity.sessionEpoch) return;
      setNotice(t('MCPOAuth.grantRevoked'));
      await loadGrants();
    } catch {
      if (identityRef.current.projectId === identity.projectId && identityRef.current.userId === identity.userId
        && identityRef.current.sessionEpoch === identity.sessionEpoch) {
        setGrantsError(t('MCPOAuth.revokeFailed'));
        setGrantsErrorContextKey(`${identity.projectId}:${identity.userId}:${identity.sessionEpoch}`);
      }
    } finally {
      setBusyGrant('');
    }
  };

  if (!hydrated || projectsLoading) return <p role="status" className="text-sm text-zinc-400">{t('MCPOAuth.loading')}</p>;
  if (!user || isDemoSession) return <p className="text-sm text-zinc-400">{t('MCPOAuth.signInRequired')}</p>;
  if (!currentProject) return <p className="text-sm text-zinc-400">{t('MCPOAuth.selectProject')}</p>;

  let environmentLabel = t('MCPOAuth.configuredEnvironment');
  try {
    const host = new URL(resourceURL).hostname;
    if (host === 'localhost' || host === '127.0.0.1' || host === '[::1]') environmentLabel = t('MCPOAuth.localEnvironment');
  } catch { /* Runtime config validation already gates the workspace. */ }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-serif text-2xl font-medium text-[#eeeae4]">{t('MCPOAuth.advancedTitle')}</h1>
          <p className="mt-1 break-words text-sm text-zinc-400">{currentProject.name}</p>
        </div>
        <Link href="/profile" className="inline-flex min-h-11 items-center rounded-lg border border-white/10 px-4 text-sm text-zinc-300 hover:bg-white/5">{t('MCPOAuth.profileLink')}</Link>
      </header>

      <section className="rounded-2xl border border-white/10 bg-[#151515] p-5 sm:p-6" aria-labelledby="mcp-oauth-setup-title">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-emerald-300">{t('MCPOAuth.setupEyebrow')}</p>
        <h2 id="mcp-oauth-setup-title" className="mt-2 text-lg font-semibold text-white">{t('MCPOAuth.setupTitle')}</h2>
        <p className="mt-2 text-sm leading-6 text-zinc-400">{t('MCPOAuth.setupIntro')}</p>
        <dl className="mt-5 grid gap-3 rounded-xl border border-white/8 bg-black/20 p-4 sm:grid-cols-2">
          <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.environment')}</dt><dd className="mt-1 text-sm font-medium text-zinc-200">{environmentLabel}</dd></div>
          <div><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.scope')}</dt><dd className="mt-1 font-mono text-sm text-emerald-200">mcp:query</dd></div>
          <div className="sm:col-span-2"><dt className="text-xs uppercase tracking-wide text-zinc-500">{t('MCPOAuth.resource')}</dt><dd className="mt-1 break-all font-mono text-xs leading-5 text-zinc-200">{resourceURL}</dd></div>
        </dl>
        <ol className="mt-5 list-decimal space-y-2 pl-5 text-sm leading-6 text-zinc-300">
          <li>{t('MCPOAuth.stepOne')}</li><li>{t('MCPOAuth.stepTwo')}</li><li>{t('MCPOAuth.stepThree')}</li>
        </ol>
        <pre className="mt-4 overflow-x-auto rounded-lg border border-white/10 bg-black/30 p-4 text-xs leading-5 text-zinc-200"><code>{configSnippet}</code></pre>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <button type="button" onClick={() => void copyConfig()} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2 text-sm font-semibold text-black hover:bg-emerald-200">{copied ? t('MCPOAuth.copied') : t('MCPOAuth.copyConfig')}</button>
          <span role="status" className="text-sm text-zinc-400">{notice || t('MCPOAuth.nativeCheck')}</span>
        </div>
      </section>

      <section className="rounded-2xl border border-white/10 bg-[#151515] p-5 sm:p-6" aria-labelledby="mcp-oauth-grants-title">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div><h2 id="mcp-oauth-grants-title" className="text-lg font-semibold text-white">{t('MCPOAuth.grantsTitle')}</h2><p className="mt-1 text-sm text-zinc-400">{t('MCPOAuth.grantsIntro')}</p></div>
          <button type="button" onClick={() => void loadGrants()} disabled={grantsLoading} className="min-h-11 rounded-lg border border-white/10 px-4 text-sm text-zinc-200 hover:bg-white/5 disabled:opacity-50">{t('MCPOAuth.refreshGrants')}</button>
        </div>
        {grantsLoading ? <p role="status" className="mt-5 text-sm text-zinc-400">{t('MCPOAuth.loading')}</p> : null}
        {visibleGrantsError ? <p role="alert" className="mt-5 text-sm text-amber-100">{visibleGrantsError}</p> : null}
        {!grantsLoading && !visibleGrantsError && visibleGrants.length === 0 ? <p className="mt-5 rounded-lg border border-white/8 bg-black/20 p-4 text-sm text-zinc-400">{t('MCPOAuth.noGrants')}</p> : null}
        <ul className="mt-4 space-y-3">
          {visibleGrants.map((grant) => (
            <li key={grant.id} className="rounded-xl border border-white/8 bg-black/20 p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="break-words font-medium text-white">{grant.client_name}</p>
                  <p className="mt-1 break-all font-mono text-xs text-zinc-400">{grant.resource}</p>
                  <p className="mt-2 text-xs text-zinc-400">{grant.scope} · {t(`MCPOAuth.status.${grant.status}`)}</p>
                  <p className="mt-1 text-xs text-zinc-500">{t('MCPOAuth.expires')}: {new Date(grant.expires_at).toLocaleString()}</p>
                </div>
                {grant.status === 'active' ? <button type="button" disabled={busyGrant === grant.id} onClick={() => void revoke(grant)} className="min-h-11 shrink-0 rounded-lg border border-red-300/20 px-3 text-sm text-red-100 hover:bg-red-300/10 disabled:opacity-50">{busyGrant === grant.id ? t('MCPOAuth.processing') : t('MCPOAuth.revoke')}</button> : null}
              </div>
            </li>
          ))}
        </ul>
      </section>

      <section className="rounded-2xl border border-white/10 bg-[#151515] p-5 sm:p-6" aria-labelledby="mcp-manual-key-title">
        <h2 id="mcp-manual-key-title" className="text-lg font-semibold text-white">{t('MCPOAuth.manualFallback')}</h2>
        <p className="mt-1 text-sm text-zinc-400">{t('MCPOAuth.manualFallbackIntro')}</p>
        <ProjectKeysSection key={`${user.id}:${currentProject.id}:${sessionEpoch}`} currentProject={currentProject} accessToken={accessToken} t={t} />
      </section>
    </div>
  );
}
