'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useAuth } from '@/lib/auth';
import { AgentMcpTabs } from '@/components/AgentMcpTabs';
import { ProjectKeysSection } from '@/components/ProjectKeysSection';
import { Badge } from '@/components/ui/Badge';
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
  const [busyGrant, setBusyGrant] = useState<{ id: string; contextKey: string } | null>(null);
  const [notice, setNotice] = useState<{ message: string; contextKey: string } | null>(null);
  const requestIdRef = useRef(0);
  const identityRef = useRef({ projectId: currentProject?.id ?? '', userId: user?.id ?? '', sessionEpoch });
  const contextKey = `${currentProject?.id ?? ''}:${user?.id ?? ''}:${sessionEpoch}`;
  const visibleGrants = grantsContextKey === contextKey ? grants : [];
  const visibleGrantsError = grantsErrorContextKey === contextKey ? grantsError : '';
  const visibleBusyGrant = busyGrant?.contextKey === contextKey ? busyGrant.id : '';
  const visibleNotice = notice?.contextKey === contextKey ? notice.message : '';
  const grantsLoading = !visibleGrantsError && grantsContextKey !== contextKey;

  const resourceURL = currentProject ? mcpResourceURL(currentProject.id) : '';

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

  const revoke = async (grant: MCPOAuthGrant) => {
    if (!accessToken || busyGrant?.contextKey === contextKey || grant.status !== 'active') return;
    const identity = { ...identityRef.current };
    const identityKey = `${identity.projectId}:${identity.userId}:${identity.sessionEpoch}`;
    setBusyGrant({ id: grant.id, contextKey: identityKey });
    setNotice(null);
    try {
      await revokeMCPOAuthGrant(grant.id, accessToken);
      if (identityRef.current.projectId !== identity.projectId || identityRef.current.userId !== identity.userId
        || identityRef.current.sessionEpoch !== identity.sessionEpoch) return;
      setNotice({ message: t('MCPOAuth.grantRevoked'), contextKey: identityKey });
      await loadGrants();
    } catch {
      if (identityRef.current.projectId === identity.projectId && identityRef.current.userId === identity.userId
        && identityRef.current.sessionEpoch === identity.sessionEpoch) {
        setGrantsError(t('MCPOAuth.revokeFailed'));
        setGrantsErrorContextKey(`${identity.projectId}:${identity.userId}:${identity.sessionEpoch}`);
      }
    } finally {
      setBusyGrant((current) => current?.contextKey === identityKey ? null : current);
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
          <h1 aria-label={`${t('MCPOAuth.advancedTitle')} ${t('Shell.betaBadge')}`} className="flex flex-wrap items-center gap-x-3 gap-y-2 font-serif text-2xl font-medium text-[#eeeae4]">
            <span>{t('MCPOAuth.advancedTitle')}</span>
            <Badge variant="accent">{t('Shell.betaBadge')}</Badge>
          </h1>
          <p className="mt-1 text-sm text-zinc-400">{t('MCPOAuth.agentMcpIntro')}</p>
          <p className="mt-2 break-words text-sm text-zinc-200"><span className="text-zinc-400">{t('MCPOAuth.currentProject')}: </span>{currentProject.name}</p>
          <div className="mt-2 flex flex-wrap gap-2 text-xs">
            <span className="rounded-full border border-white/10 px-3 py-1 text-zinc-300">{t('MCPOAuth.readOnlyLabel')}</span>
            <span className="rounded-full border border-white/10 px-3 py-1 text-zinc-300">{t('MCPOAuth.revocableLabel')}</span>
          </div>
        </div>
        <Link href="/profile" className="inline-flex min-h-11 items-center rounded-lg border border-white/10 px-4 text-sm text-zinc-300 hover:bg-white/5">{t('MCPOAuth.profileLink')}</Link>
      </header>

      <AgentMcpTabs resourceURL={resourceURL} environmentLabel={environmentLabel} identityKey={`${contextKey}:${resourceURL}`} t={t} />

      <details className="rounded-2xl border border-white/10 bg-[#151515]">
        <summary className="flex min-h-16 cursor-pointer list-none items-center justify-between gap-4 px-5 py-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-inset focus-visible:outline-emerald-300 sm:px-6">
          <span className="min-w-0"><span className="block font-semibold text-white">{t('MCPOAuth.grantsTitle')}</span><span className="mt-1 block text-sm text-zinc-400">{t('MCPOAuth.grantsIntro')}</span></span>
          <span className="flex shrink-0 items-center gap-2">
            <span className="rounded-full border border-white/10 px-2.5 py-1 text-xs text-zinc-300" aria-label={t('MCPOAuth.grantCount')}>{visibleGrants.length}</span>
            <span aria-hidden="true" className="text-zinc-400">⌄</span>
          </span>
        </summary>
        <section className="border-t border-white/10 p-5 sm:p-6" aria-labelledby="mcp-oauth-grants-title">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div><h2 id="mcp-oauth-grants-title" className="text-lg font-semibold text-white">{t('MCPOAuth.grantsTitle')}</h2><p className="mt-1 text-sm text-zinc-400">{t('MCPOAuth.grantsIntro')}</p></div>
            <button type="button" onClick={() => void loadGrants()} disabled={grantsLoading} className="min-h-11 rounded-lg border border-white/10 px-4 text-sm text-zinc-200 hover:bg-white/5 disabled:opacity-50">{t('MCPOAuth.refreshGrants')}</button>
          </div>
          {visibleNotice ? <p role="status" className="mt-4 text-sm text-emerald-100">{visibleNotice}</p> : null}
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
                  {grant.status === 'active' ? <button type="button" disabled={visibleBusyGrant === grant.id} onClick={() => void revoke(grant)} className="min-h-11 shrink-0 rounded-lg border border-red-300/20 px-3 text-sm text-red-100 hover:bg-red-300/10 disabled:opacity-50">{visibleBusyGrant === grant.id ? t('MCPOAuth.processing') : t('MCPOAuth.revoke')}</button> : null}
                </div>
              </li>
            ))}
          </ul>
        </section>
      </details>

      <details className="rounded-2xl border border-white/10 bg-[#151515]">
        <summary className="min-h-16 cursor-pointer px-5 py-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-inset focus-visible:outline-emerald-300 sm:px-6">
          <span className="block font-semibold text-white">{t('MCPOAuth.manualFallback')}</span>
          <span className="mt-1 block text-sm text-zinc-400">{t('MCPOAuth.manualFallbackIntro')}</span>
        </summary>
        <div className="border-t border-white/10 px-5 pb-5 sm:px-6 sm:pb-6">
          <ProjectKeysSection key={`${user.id}:${currentProject.id}:${sessionEpoch}`} currentProject={currentProject} accessToken={accessToken} t={t} />
        </div>
      </details>
    </div>
  );
}
