'use client';

import { FormEvent, useEffect, useState } from 'react';
import type { Project } from '@/lib/projects';
import { createProjectKey, listProjectKeys, ProjectKeyApiError, revokeProjectKey, type ProjectKey } from '@/lib/project-keys';
import { getRuntimeConfig } from '@/lib/runtime-config';

type Translate = (key: string, params?: Record<string, string | number>) => string;

function projectKeyUsageLabel(key: ProjectKey, t: Translate): string {
  if (key.last_used_status === 'unavailable') return t('ProjectSettings.usageUnavailable');
  if (key.last_used_status === 'no_record') {
    return key.last_used_at ? t('ProjectSettings.usageUnavailable') : t('ProjectSettings.noUsageRecord');
  }
  if (key.last_used_status !== undefined && key.last_used_status !== 'available') {
    return t('ProjectSettings.usageUnavailable');
  }
  if (!key.last_used_at) {
    return key.last_used_status === 'available'
      ? t('ProjectSettings.usageUnavailable')
      : t('ProjectSettings.noUsageRecord');
  }
  const lastUsedAt = new Date(key.last_used_at);
  if (Number.isNaN(lastUsedAt.getTime())) return t('ProjectSettings.usageUnavailable');
  return t('ProjectSettings.lastUsed', { time: lastUsedAt.toLocaleString() });
}

export function ProjectKeysSection({
  currentProject,
  accessToken,
  t,
}: {
  currentProject: Project | null;
  accessToken: string | null;
  t: Translate;
}) {
  const [projectKeys, setProjectKeys] = useState<ProjectKey[]>([]);
  const [projectKeysStale, setProjectKeysStale] = useState(true);
  const [projectKeyName, setProjectKeyName] = useState('');
  const [projectKeySecret, setProjectKeySecret] = useState('');
  const [projectKeyBusy, setProjectKeyBusy] = useState(false);
  const [projectKeyError, setProjectKeyError] = useState('');
  const [projectKeyNotice, setProjectKeyNotice] = useState('');
  const projectId = currentProject?.id ?? null;
  const keyNameLength = [...projectKeyName.trim()].length;
  const mcpUrl = getRuntimeConfig().api_url + '/mcp';
  const hermesConfigSnippet = t('ProjectSettings.hermesConfigSnippet', {
    mcpUrl,
    secretRef: '$' + '{LWC_PROJECT_KEY}',
  });

  useEffect(() => {
    if (!accessToken || !projectId) return;
    let active = true;
    void listProjectKeys(projectId).then((keys) => {
      if (active) {
        setProjectKeys(keys);
        setProjectKeysStale(false);
      }
    }).catch(() => {
      if (active) {
        setProjectKeys([]);
        setProjectKeysStale(true);
        setProjectKeyError(t('AccountSettings.projectKeysLoadError'));
      }
    });
    return () => { active = false; };
  }, [accessToken, projectId, t]);

  const reloadProjectKeys = async (targetProjectId: string): Promise<ProjectKey[]> => {
    try {
      const keys = await listProjectKeys(targetProjectId);
      setProjectKeys(keys);
      setProjectKeysStale(false);
      return keys;
    } catch (error) {
      setProjectKeys([]);
      setProjectKeysStale(true);
      throw error;
    }
  };

  const handleRefreshProjectKeys = async () => {
    if (!projectId || projectKeyBusy) return;
    setProjectKeyBusy(true);
    setProjectKeyError('');
    try {
      await reloadProjectKeys(projectId);
    } catch {
      setProjectKeyError(t('AccountSettings.projectKeysLoadError'));
    } finally {
      setProjectKeyBusy(false);
    }
  };

  const handleCreateProjectKey = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const name = projectKeyName.trim();
    if (!projectId || !accessToken || projectKeyBusy || projectKeysStale || keyNameLength < 1 || keyNameLength > 64) return;
    setProjectKeyBusy(true);
    setProjectKeyError('');
    setProjectKeyNotice('');
    setProjectKeySecret('');
    try {
      const created = await createProjectKey(projectId, name);
      if (!created || !created.key || typeof created.key.key_id !== 'string' || !created.key.key_id || typeof created.secret !== 'string' || !created.secret) {
        throw new Error('Project API key creation response was incomplete.');
      }
      setProjectKeys((keys) => [created.key, ...keys.filter((key) => key.key_id !== created.key.key_id)]);
      setProjectKeySecret(created.secret);
      setProjectKeyName('');
    } catch (requestError) {
      if (requestError instanceof ProjectKeyApiError && requestError.keyId) {
        setProjectKeyNotice(t('AccountSettings.projectKeyUnknownOutcome', { keyId: requestError.keyId }));
        setProjectKeysStale(true);
        try {
          await reloadProjectKeys(projectId);
        } catch {
          setProjectKeyError(t('AccountSettings.projectKeysLoadError'));
        }
      } else if (!(requestError instanceof ProjectKeyApiError) || requestError.status === 408 || requestError.status >= 500) {
        setProjectKeyNotice(t('AccountSettings.projectKeyUnknownOutcomeNoId', { name }));
        setProjectKeysStale(true);
        try {
          await reloadProjectKeys(projectId);
        } catch {
          setProjectKeyError(t('AccountSettings.projectKeysLoadError'));
        }
      } else {
        setProjectKeyError(requestError instanceof Error ? requestError.message : t('AccountSettings.projectKeyCreateError'));
      }
    } finally {
      setProjectKeyBusy(false);
    }
  };

  const handleRevokeProjectKey = async (key: ProjectKey) => {
    if (!projectId || !accessToken || !window.confirm(t('AccountSettings.confirmRevokeProjectKey', { name: key.name }))) return;
    setProjectKeyBusy(true);
    setProjectKeyError('');
    setProjectKeyNotice('');
    setProjectKeysStale(true);
    let revokeError: unknown;
    let revokeFailed = false;
    try {
      await revokeProjectKey(projectId, key.key_id);
    } catch (requestError) {
      revokeFailed = true;
      revokeError = requestError;
    }
    try {
      const refreshedKeys = await reloadProjectKeys(projectId);
      const refreshedKey = refreshedKeys.find((item) => item.key_id === key.key_id);
      if (!revokeFailed || refreshedKey?.state === 'revoked') {
        setProjectKeyNotice(t('AccountSettings.projectKeyRevoked'));
      } else {
        setProjectKeyError(revokeError instanceof Error ? revokeError.message : t('AccountSettings.projectKeyRevokeError'));
      }
    } catch {
      const revokeMessage = revokeFailed
        ? revokeError instanceof Error ? `${revokeError.message} ` : `${t('AccountSettings.projectKeyRevokeError')} `
        : '';
      setProjectKeyError(`${revokeMessage}${t('AccountSettings.projectKeysLoadError')}`);
    } finally {
      setProjectKeyBusy(false);
    }
  };

  const handleCopyProjectKey = async () => {
    if (!projectKeySecret) return;
    try {
      await navigator.clipboard.writeText(projectKeySecret);
      setProjectKeyNotice(t('AccountSettings.projectKeyCopied'));
    } catch {
      setProjectKeyError(t('AccountSettings.projectKeyCopyError'));
    }
  };

  return (
    <div className="mt-8 border-t border-white/10 pt-6">
      <h2 id="project-settings-external-access" className="text-xl font-semibold text-white">{t('ProjectSettings.externalAccess')}</h2>
      <section className="mt-5" aria-labelledby="project-keys-title">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="project-keys-title" className="text-lg font-semibold text-white">{t('ProjectSettings.projectKeys')}</h3>
        <button type="button" disabled={!projectId || !accessToken || projectKeyBusy} onClick={() => void handleRefreshProjectKeys()} className="min-h-11 rounded-lg border border-white/15 px-3 py-2 text-sm text-zinc-200 hover:bg-white/10 disabled:opacity-50">{t('AccountSettings.projectKeysRefresh')}</button>
      </div>
      <p className="mt-1 text-sm leading-6 text-zinc-400">{t('AccountSettings.projectKeysHint')}</p>
      {currentProject ? (
        <p className="mt-3 break-all rounded-lg border border-white/10 bg-black/20 p-3 text-xs text-zinc-400">
          {t('AccountSettings.projectKeyBoundProject', { name: currentProject.name, id: currentProject.id })}
        </p>
      ) : (
        <p className="mt-3 rounded-lg border border-white/10 bg-black/20 p-3 text-sm text-zinc-400">{t('AccountSettings.projectKeyNoProject')}</p>
      )}
      <form onSubmit={(event) => void handleCreateProjectKey(event)} className="mt-4 flex flex-col gap-3 sm:flex-row">
        <label className="min-w-0 flex-1 text-sm text-zinc-300">
          {t('AccountSettings.projectKeyName')}
          <input value={projectKeyName} onChange={(event) => setProjectKeyName(event.target.value)} className="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
          <span className="mt-1 block text-xs text-zinc-500">{t('AccountSettings.projectKeyNameHint')}</span>
        </label>
        <button type="submit" disabled={!accessToken || !projectId || projectKeyBusy || projectKeysStale || keyNameLength < 1 || keyNameLength > 64} className="min-h-11 self-end rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">
          {projectKeyBusy ? t('AccountSettings.projectKeyProcessing') : t('AccountSettings.projectKeyCreate')}
        </button>
      </form>
      {projectKeySecret ? (
        <div className="mt-4 rounded-lg border border-amber-300/30 bg-amber-300/5 p-4">
          <p className="text-sm font-semibold text-amber-100">{t('AccountSettings.projectKeyShowOnce')}</p>
          <p className="mt-1 text-xs leading-5 text-zinc-300">{t('AccountSettings.projectKeyShowOnceHint')}</p>
          <code data-testid="project-key-secret" className="mt-3 block max-h-24 select-all overflow-auto break-all rounded bg-black/40 p-3 font-mono text-xs text-white">{projectKeySecret}</code>
          <div className="mt-3 flex flex-wrap gap-2">
            <button type="button" onClick={() => void handleCopyProjectKey()} className="min-h-11 rounded-lg border border-white/15 px-3 py-2 text-sm text-white hover:bg-white/10">{t('AccountSettings.projectKeyCopy')}</button>
            <button type="button" onClick={() => setProjectKeySecret('')} className="min-h-11 rounded-lg border border-white/15 px-3 py-2 text-sm text-zinc-300 hover:bg-white/10">{t('AccountSettings.projectKeyDismiss')}</button>
          </div>
        </div>
      ) : null}
      {projectKeysStale ? null : projectKeys.length === 0 ? (
        <p className="mt-4 rounded-lg border border-white/10 bg-black/20 p-4 text-sm text-zinc-400">{t('AccountSettings.projectKeysEmpty')}</p>
      ) : (
        <ul aria-label={t('ProjectSettings.projectKeys')} className="mt-4 space-y-3">
          {projectKeys.map((key) => (
            <li key={key.key_id} className="flex flex-col gap-3 rounded-lg border border-white/10 bg-black/20 p-4 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0 text-sm">
                <p className="font-medium text-white">{key.name} <span className="text-zinc-500">· {t(key.state === 'active' ? 'AccountSettings.projectKeyActive' : 'AccountSettings.projectKeyRevokedState')}</span></p>
                <p className="mt-1 break-all font-mono text-xs text-zinc-500">{key.key_id}</p>
                <p className="mt-1 text-xs text-zinc-500">{t('AccountSettings.projectKeyQueryOnly')} · {new Date(key.created_at).toLocaleString()}</p>
                <p className="mt-1 text-xs text-zinc-500">{projectKeyUsageLabel(key, t)}</p>
              </div>
              {key.state === 'active' ? (
                <button type="button" disabled={projectKeyBusy} onClick={() => void handleRevokeProjectKey(key)} className="min-h-11 shrink-0 rounded-lg border border-red-300/20 px-3 py-2 text-sm text-red-200 hover:bg-red-300/10 disabled:opacity-50">
                  {projectKeyBusy ? t('AccountSettings.projectKeyProcessing') : t('AccountSettings.projectKeyRevoke')}
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {projectKeyNotice ? <p role="status" className="mt-4 rounded-lg border border-emerald-300/20 bg-emerald-300/5 p-3 text-sm text-emerald-100">{projectKeyNotice}</p> : null}
      {projectKeyError ? <p role="alert" className="mt-4 rounded-lg border border-red-300/20 bg-red-300/5 p-3 text-sm text-red-100">{projectKeyError}</p> : null}
      </section>

      <section className="mt-8 border-t border-white/10 pt-6" aria-labelledby="project-keys-hermes-title">
        <h3 id="project-keys-hermes-title" className="text-lg font-semibold text-white">{t('ProjectSettings.hermesTitle')}</h3>
        <p className="mt-2 text-sm leading-6 text-zinc-400">{t('ProjectSettings.hermesIntro')}</p>
        <ol className="mt-4 list-decimal space-y-2 pl-5 text-sm leading-6 text-zinc-300">
          <li>{t('ProjectSettings.hermesProfileStep')}</li>
          <li>{t('ProjectSettings.hermesSecretStep')}</li>
          <li>{t('ProjectSettings.hermesConfigStep')}</li>
          <li>{t('ProjectSettings.hermesDiscoveryStep')}</li>
        </ol>
        <pre className="mt-4 overflow-x-auto rounded-lg border border-white/10 bg-black/30 p-4 text-xs leading-5 text-zinc-200"><code>{hermesConfigSnippet}</code></pre>
        <p className="mt-4 text-sm leading-6 text-zinc-400">{t('ProjectSettings.usageHint')}</p>
        <p className="mt-2 text-sm leading-6 text-zinc-400">{t('ProjectSettings.hermesQueryHint')}</p>
        <p className="mt-2 text-sm leading-6 text-zinc-400">{t('ProjectSettings.hermesRotationHint')}</p>
        <p role="note" className="mt-4 rounded-lg border border-white/10 bg-black/20 p-3 text-xs leading-5 text-zinc-500">
          {t('ProjectSettings.grokUnverified')} {t('ProjectSettings.multiProjectDeferred')}
        </p>
        <div className="mt-4 flex flex-wrap gap-4 text-sm">
          <a href="https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp/" target="_blank" rel="noreferrer" className="text-emerald-300 underline decoration-emerald-300/40 underline-offset-4 hover:text-emerald-200">{t('ProjectSettings.mcpDocs')}</a>
          <a href="https://hermes-agent.nousresearch.com/docs/user-guide/secrets/" target="_blank" rel="noreferrer" className="text-emerald-300 underline decoration-emerald-300/40 underline-offset-4 hover:text-emerald-200">{t('ProjectSettings.secretDocs')}</a>
        </div>
      </section>
    </div>
  );
}
