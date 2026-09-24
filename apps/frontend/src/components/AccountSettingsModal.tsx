'use client';

import { FormEvent, useEffect, useRef, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { beginGoogleLink, readGoogleIdentitySummary } from '@/lib/google-auth';
import { useLocale } from '@/lib/i18n';
import {
  listCLISessions,
  listSyncBindings,
  reauthorizeSyncBinding,
  revokeCLISession,
  revokeSyncBinding,
  type CLISession,
  type SyncBinding,
} from '@/lib/cli-auth';

export function AccountSettingsModal({ onClose }: { onClose: () => void }) {
  const { accessToken, refreshAccessToken, user } = useAuth();
  const { t } = useLocale();
  const [linkOpen, setLinkOpen] = useState(false);
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [primaryEmail, setPrimaryEmail] = useState(user?.email ?? '');
  const [linkedGoogleEmail, setLinkedGoogleEmail] = useState<string | null>(null);
  const [cliSessions, setCliSessions] = useState<CLISession[]>([]);
  const [syncBindings, setSyncBindings] = useState<SyncBinding[]>([]);
  const [controlError, setControlError] = useState('');
  const [controlNotice, setControlNotice] = useState('');
  const [busyControl, setBusyControl] = useState('');
  const passwordRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!accessToken || !user) return;
    let active = true;
    const accountId = user.id;
    void readGoogleIdentitySummary(accessToken).then((summary) => {
      if (!active || accountId !== user.id) return;
      setPrimaryEmail(summary.primary_email);
      setLinkedGoogleEmail(summary.linked_providers.find((provider) => provider.provider === 'google')?.provider_email || null);
    }).catch(() => {
      // The authenticated session remains usable if this optional summary read fails.
    });
    return () => { active = false; };
  }, [accessToken, user]);

  useEffect(() => {
    if (!accessToken || !user) return;
    let active = true;
    const accountId = user.id;
    const context = { accessToken, refreshAccessToken };
    void (async () => {
      try {
        const sessions = await listCLISessions(context);
        if (active && accountId === user.id) setCliSessions(sessions);
      } catch {
        if (active) setControlError('Unable to load CLI sessions.');
      }
      try {
        const bindings = await listSyncBindings(context);
        if (active && accountId === user.id) setSyncBindings(bindings);
      } catch {
        if (active) setControlError('Unable to load sync bindings.');
      }
    })();
    return () => { active = false; };
  }, [accessToken, refreshAccessToken, user]);

  useEffect(() => {
    if (linkOpen) passwordRef.current?.focus();
  }, [linkOpen]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !loading) onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [loading, onClose]);

  if (!user) return null;

  const handleLink = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (loading || !password || !accessToken) return;
    setLoading(true);
    setError('');
    try {
      await beginGoogleLink(password, accessToken);
    } catch (linkError) {
      setError(linkError instanceof Error ? linkError.message : 'Unable to link this Google account.');
      setPassword('');
      setLoading(false);
    }
  };

  const refreshControlLists = async () => {
    if (!accessToken) return;
    const context = { accessToken, refreshAccessToken };
    setCliSessions(await listCLISessions(context));
    setSyncBindings(await listSyncBindings(context));
  };

  const handleRevokeSession = async (session: CLISession) => {
    if (!accessToken || !window.confirm(t('AccountSettings.confirmRevokeSession'))) return;
    setBusyControl(session.id);
    setControlError('');
    setControlNotice('');
    try {
      await revokeCLISession({ accessToken, refreshAccessToken }, session.id);
      await refreshControlLists();
      setControlNotice(t('AccountSettings.sessionRevoked'));
    } catch (requestError) {
      setControlError(requestError instanceof Error ? requestError.message : 'Unable to revoke the CLI session.');
    } finally {
      setBusyControl('');
    }
  };

  const handleRevokeBinding = async (binding: SyncBinding) => {
    if (!accessToken || !window.confirm(t('AccountSettings.confirmRevokeBinding'))) return;
    setBusyControl(binding.binding_id);
    setControlError('');
    setControlNotice('');
    try {
      await revokeSyncBinding({ accessToken, refreshAccessToken }, binding);
      await refreshControlLists();
      setControlNotice(t('AccountSettings.bindingRevokedNotice'));
    } catch (requestError) {
      setControlError(requestError instanceof Error ? requestError.message : 'Unable to revoke the sync binding.');
    } finally {
      setBusyControl('');
    }
  };

  const handleReauthorizeBinding = async (binding: SyncBinding) => {
    if (!accessToken || !window.confirm(t('AccountSettings.confirmReauthorizeBinding'))) return;
    setBusyControl(binding.binding_id);
    setControlError('');
    setControlNotice('');
    try {
      await reauthorizeSyncBinding({ accessToken, refreshAccessToken }, binding);
      await refreshControlLists();
      setControlNotice(t('AccountSettings.bindingReauthorized'));
    } catch (requestError) {
      setControlError(requestError instanceof Error ? requestError.message : 'Unable to reauthorize the sync binding.');
    } finally {
      setBusyControl('');
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-settings-title"
        className="max-h-[calc(100dvh-2rem)] w-full max-w-3xl overflow-y-auto rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 id="account-settings-title" className="text-2xl font-semibold text-white">{t('AccountSettings.title')}</h2>
            <p className="mt-1 text-sm text-zinc-400">{t('AccountSettings.subtitle')}</p>
          </div>
          <button type="button" onClick={onClose} className="min-h-11 min-w-11 rounded-md p-2 text-zinc-400 hover:bg-white/10 hover:text-white" aria-label="Close account settings">×</button>
        </div>

        <dl className="mt-6 space-y-4 text-sm">
          <div className="rounded-lg border border-white/10 bg-black/20 p-4">
            <dt className="text-zinc-400">{t('AccountSettings.primaryEmail')}</dt>
            <dd className="mt-1 break-all font-medium text-white">{primaryEmail}</dd>
            <p className="mt-2 text-xs leading-5 text-zinc-500">{t('AccountSettings.primaryHint')}</p>
          </div>
          <div className="rounded-lg border border-white/10 bg-black/20 p-4">
            <dt className="text-zinc-400">{t('AccountSettings.googleEmail')}</dt>
            <dd className="mt-1 break-all text-zinc-300">{linkedGoogleEmail ?? t('AccountSettings.googleNotLinked')}</dd>
            <p className="mt-2 text-xs leading-5 text-zinc-500">{t('AccountSettings.googleHint')}</p>
          </div>
        </dl>

        {!linkOpen ? (
          <button type="button" onClick={() => setLinkOpen(true)} className="mt-6 min-h-11 w-full rounded-lg bg-emerald-300 px-4 py-3 font-semibold text-black hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">
            {t('AccountSettings.linkGoogle')}
          </button>
        ) : (
          <form onSubmit={handleLink} className="mt-6 space-y-4 rounded-lg border border-emerald-300/20 bg-emerald-300/5 p-4">
            <p className="text-sm leading-6 text-zinc-300">{t('AccountSettings.reauthHint')}</p>
            <label className="block text-sm font-medium text-zinc-300">
              {t('AccountSettings.currentPassword')}
              <input ref={passwordRef} type="password" autoComplete="current-password" required value={password} onChange={(event) => setPassword(event.target.value)} className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
            </label>
            {error ? <p role="alert" className="text-sm text-red-300">{error}</p> : null}
            <div className="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <button type="button" disabled={loading} onClick={() => { setLinkOpen(false); setPassword(''); setError(''); }} className="min-h-11 rounded-lg border border-white/10 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/10 disabled:opacity-60">{t('AccountSettings.cancel')}</button>
              <button type="submit" disabled={loading || !password || !accessToken} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60">{loading ? t('AccountSettings.openingGoogle') : t('AccountSettings.continue')}</button>
            </div>
          </form>
        )}

        <section className="mt-8 border-t border-white/10 pt-6" aria-labelledby="cli-sessions-title">
          <h3 id="cli-sessions-title" className="text-lg font-semibold text-white">{t('AccountSettings.cliSessions')}</h3>
          <p className="mt-1 text-sm text-zinc-400">{t('AccountSettings.cliSessionsHint')}</p>
          {cliSessions.length === 0 ? (
            <p className="mt-4 rounded-lg border border-white/10 bg-black/20 p-4 text-sm text-zinc-400">{t('AccountSettings.cliSessionsEmpty')}</p>
          ) : (
            <ul className="mt-4 space-y-3">
              {cliSessions.map((session) => (
                <li key={session.id} className="flex flex-col gap-3 rounded-lg border border-white/10 bg-black/20 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0 text-sm">
                    <p className="font-medium text-white">{session.client_name || 'CLI'} <span className="text-zinc-500">· {session.status}</span></p>
                    <p className="mt-1 break-all font-mono text-xs text-zinc-500">{session.id}</p>
                    <p className="mt-1 text-xs text-zinc-500">{new Date(session.created_at).toLocaleString()}</p>
                  </div>
                  {session.status === 'active' ? (
                    <button type="button" disabled={Boolean(busyControl)} onClick={() => void handleRevokeSession(session)} className="min-h-11 shrink-0 rounded-lg border border-red-300/20 px-3 py-2 text-sm text-red-200 hover:bg-red-300/10 disabled:opacity-50">
                      {busyControl === session.id ? t('CLIPairing.processing') : t('AccountSettings.revokeSession')}
                    </button>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="mt-8 border-t border-white/10 pt-6" aria-labelledby="sync-bindings-title">
          <h3 id="sync-bindings-title" className="text-lg font-semibold text-white">{t('AccountSettings.syncBindings')}</h3>
          <p className="mt-1 text-sm leading-6 text-zinc-400">{t('AccountSettings.syncBindingsHint')}</p>
          {syncBindings.length === 0 ? (
            <p className="mt-4 rounded-lg border border-white/10 bg-black/20 p-4 text-sm text-zinc-400">{t('AccountSettings.syncBindingsEmpty')}</p>
          ) : (
            <ul className="mt-4 space-y-3">
              {syncBindings.map((binding) => (
                <li key={binding.project_id} className="rounded-lg border border-white/10 bg-black/20 p-4">
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                    <div className="min-w-0 text-sm">
                      <p className="font-medium text-white">{binding.project_id} <span className="text-zinc-500">· {binding.status === 'active' ? t('AccountSettings.bindingActive') : t('AccountSettings.bindingRevoked')}</span></p>
                      <p className="mt-1 break-all font-mono text-xs text-zinc-500">{binding.wiki_id} · {binding.binding_id}</p>
                      <p className="mt-1 break-all text-xs text-zinc-500">{binding.host}</p>
                    </div>
                    <div className="flex shrink-0 flex-wrap gap-2">
                      {binding.status === 'active' ? (
                        <button type="button" disabled={Boolean(busyControl)} onClick={() => void handleRevokeBinding(binding)} className="min-h-11 rounded-lg border border-red-300/20 px-3 py-2 text-sm text-red-200 hover:bg-red-300/10 disabled:opacity-50">
                          {busyControl === binding.binding_id ? t('CLIPairing.processing') : t('AccountSettings.revokeBinding')}
                        </button>
                      ) : null}
                      <button type="button" disabled={Boolean(busyControl)} onClick={() => void handleReauthorizeBinding(binding)} className="min-h-11 rounded-lg border border-white/15 px-3 py-2 text-sm text-zinc-200 hover:bg-white/10 disabled:opacity-50">
                        {busyControl === binding.binding_id ? t('CLIPairing.processing') : t('AccountSettings.reauthorizeBinding')}
                      </button>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        {controlNotice ? <p role="status" className="mt-5 rounded-lg border border-emerald-300/20 bg-emerald-300/5 p-3 text-sm text-emerald-100">{controlNotice}</p> : null}
        {controlError ? <p role="alert" className="mt-5 rounded-lg border border-red-300/20 bg-red-300/5 p-3 text-sm text-red-100">{controlError}</p> : null}
      </div>
    </div>
  );
}
