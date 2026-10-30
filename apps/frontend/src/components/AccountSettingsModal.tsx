'use client';

import { type FormEvent, type KeyboardEvent as ReactKeyboardEvent, type MutableRefObject, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { beginGoogleLink, GoogleAuthError, readGoogleIdentitySummary } from '@/lib/google-auth';
import { changeAccountPassword, PasswordSelfServiceError, readPasswordSelfServiceStatus, type PasswordSelfServiceAuthContext, type PasswordSelfServiceStatus } from '@/lib/account-password';
import { useLocale } from '@/lib/i18n';
import type { LLMProfile } from '@/lib/api';
import { BYOKProfilesPanel } from './BYOKProfilesPanel';
import { ProjectLLMSourcePanel } from './ProjectLLMSourcePanel';
import { useWorkspace } from './WorkspaceProvider';
import {
  CLIAuthError,
  listCLISessions,
  listSyncBindings,
  reauthorizeSyncBinding,
  revokeCLISession,
  revokeSyncBinding,
  type CLISession,
  type SyncBinding,
} from '@/lib/cli-auth';

type AuthSessionSnapshot = { userID: string | null; accessToken: string | null; sessionEpoch: number };
const accountSettingsTabs = [
  { id: 'account', label: 'AccountSettings.accountTab' },
  { id: 'provider', label: 'AccountSettings.providerTab' },
  { id: 'connections', label: 'AccountSettings.connectionsTab' },
] as const;
type AccountSettingsTab = typeof accountSettingsTabs[number]['id'];

export function AccountSettingsModal({ onClose, focusBYOK = false, onProfileCreated }: {
  onClose: () => void;
  focusBYOK?: boolean;
  onProfileCreated?: (profile: LLMProfile) => void;
}) {
  const { accessToken, refreshAccessToken, sessionEpoch, user, isDemoSession } = useAuth();
  const authSessionRef = useRef<AuthSessionSnapshot>({ userID: user?.id ?? null, accessToken, sessionEpoch });
  useLayoutEffect(() => {
    authSessionRef.current = { userID: user?.id ?? null, accessToken, sessionEpoch };
  }, [accessToken, sessionEpoch, user?.id]);
  if (!user) return null;
  return (
    <AccountSettingsModalContent
      key={user.id}
      onClose={onClose}
      focusBYOK={focusBYOK}
      onProfileCreated={onProfileCreated}
      isDemoSession={isDemoSession}
      accessToken={accessToken}
      refreshAccessToken={refreshAccessToken}
      sessionEpoch={sessionEpoch}
      authSessionRef={authSessionRef}
      user={user}
    />
  );
}

function AccountSettingsModalContent({
  onClose,
  focusBYOK,
  onProfileCreated,
  isDemoSession,
  accessToken,
  refreshAccessToken,
  sessionEpoch,
  authSessionRef,
  user,
}: {
  onClose: () => void;
  focusBYOK: boolean;
  onProfileCreated?: (profile: LLMProfile) => void;
  isDemoSession: boolean;
  accessToken: string | null;
  refreshAccessToken: ReturnType<typeof useAuth>['refreshAccessToken'];
  sessionEpoch: number;
  authSessionRef: MutableRefObject<AuthSessionSnapshot>;
  user: NonNullable<ReturnType<typeof useAuth>['user']>;
}) {
  const { t, locale } = useLocale();
  const { projects = [] } = useWorkspace();
  const userID = user.id;
  const [activeSettingsTab, setActiveSettingsTab] = useState<AccountSettingsTab>(() => focusBYOK ? 'provider' : 'account');
  const settingsTabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const initialSettingsTabRef = useRef<AccountSettingsTab>(focusBYOK ? 'provider' : 'account');
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
  const [passwordStatus, setPasswordStatus] = useState<{ userID: string; value: PasswordSelfServiceStatus } | null>(null);
  const [passwordStatusError, setPasswordStatusError] = useState<{ userID: string; message: string } | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [currentPassword, setCurrentPassword] = useState('');
  const [passwordPending, setPasswordPending] = useState(false);
  const [passwordError, setPasswordError] = useState('');
  const [passwordNotice, setPasswordNotice] = useState('');
  const passwordRef = useRef<HTMLInputElement>(null);
  const mountedRef = useRef(false);
  const settingsDialogRef = useRef<HTMLDivElement>(null);

  const clearPasswordSecrets = useCallback(() => {
    setPassword('');
    setCurrentPassword('');
    setNewPassword('');
    setConfirmPassword('');
  }, []);
  const selectSettingsTab = (tab: AccountSettingsTab) => {
    if (tab === activeSettingsTab) return;
    if (activeSettingsTab === 'account') {
      clearPasswordSecrets();
      setLinkOpen(false);
      setError('');
      setPasswordError('');
    }
    setActiveSettingsTab(tab);
  };
  const handleSettingsTabKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    const nextIndex = event.key === 'ArrowRight' ? (index + 1) % accountSettingsTabs.length
      : event.key === 'ArrowLeft' ? (index + accountSettingsTabs.length - 1) % accountSettingsTabs.length
        : event.key === 'Home' ? 0
          : event.key === 'End' ? accountSettingsTabs.length - 1
            : -1;
    if (nextIndex < 0) return;
    event.preventDefault();
    selectSettingsTab(accountSettingsTabs[nextIndex].id);
    settingsTabRefs.current[nextIndex]?.focus();
  };
  // Bind refresh retries to the account and session that issued each request.
  const createPasswordAuthContext = useCallback((): PasswordSelfServiceAuthContext => {
    let currentAccessToken = accessToken ?? '';
    let currentSessionEpoch = sessionEpoch;
    return {
      get accessToken() { return currentAccessToken; },
      refreshAccessToken: async () => {
        const before = authSessionRef.current;
        if (
          !mountedRef.current
          || !currentAccessToken
          || before.userID !== userID
          || before.accessToken !== currentAccessToken
          || before.sessionEpoch !== currentSessionEpoch
        ) return null;

        const refreshed = await refreshAccessToken();
        if (!mountedRef.current || !refreshed) return null;
        const after = authSessionRef.current;
        if (after.userID !== userID) return null;
        const renderStillPending = after.accessToken === currentAccessToken && after.sessionEpoch === currentSessionEpoch;
        const renderApplied = after.accessToken === refreshed && after.sessionEpoch === currentSessionEpoch + 1;
        if (!renderStillPending && !renderApplied) return null;

        currentAccessToken = refreshed;
        currentSessionEpoch += 1;
        return refreshed;
      },
    };
  }, [accessToken, authSessionRef, refreshAccessToken, sessionEpoch, userID]);
  const closeSettings = useCallback(() => {
    clearPasswordSecrets();
    onClose();
  }, [clearPasswordSecrets, onClose]);
  const controlErrorMessage = (requestError: unknown, fallback: string) => (
    requestError instanceof CLIAuthError && requestError.localReason === 'missing_backend_error'
      ? fallback
      : requestError instanceof Error ? requestError.message : fallback
  );

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

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
        if (active) setControlError(t('AccountSettings.cliSessionsLoadError'));
      }
      try {
        const bindings = await listSyncBindings(context);
        if (active && accountId === user.id) setSyncBindings(bindings);
      } catch {
        if (active) setControlError(t('AccountSettings.syncBindingsLoadError'));
      }
    })();
    return () => { active = false; };
  }, [accessToken, refreshAccessToken, t, user]);

  useEffect(() => {
    if (!accessToken || !userID) return;
    let active = true;
    const accountId = userID;
    void readPasswordSelfServiceStatus(createPasswordAuthContext()).then((status) => {
      if (active) {
        setPasswordStatus({ userID: accountId, value: status });
        setPasswordStatusError(null);
      }
    }).catch(() => {
      if (active) setPasswordStatusError({ userID: accountId, message: t('AccountSettings.passwordStatusError') });
    });
    return () => { active = false; };
  }, [accessToken, createPasswordAuthContext, t, userID]);

  useEffect(() => {
    if (linkOpen) passwordRef.current?.focus();
  }, [linkOpen]);

  useLayoutEffect(() => {
    const activeElement = document.activeElement;
    returnFocusRef.current = activeElement instanceof HTMLElement ? activeElement : null;
    const initialTabIndex = accountSettingsTabs.findIndex((tab) => tab.id === initialSettingsTabRef.current);
    settingsTabRefs.current[initialTabIndex]?.focus();
    return () => returnFocusRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!focusBYOK) return;
    const target = document.getElementById('byok-profiles-heading');
    target?.scrollIntoView?.({ block: 'start' });
    target?.focus();
  }, [focusBYOK]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !loading && !passwordPending) {
        closeSettings();
        return;
      }
      if (event.key !== 'Tab') return;
      const dialog = settingsDialogRef.current;
      if (!dialog) return;
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(
        'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => !element.closest('[hidden]'));
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) {
        event.preventDefault();
      } else if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [loading, passwordPending, closeSettings]);

  const handleLink = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (loading || !password || !accessToken) return;
    setLoading(true);
    setError('');
    try {
      await beginGoogleLink(password, accessToken);
    } catch (linkError) {
      setError(linkError instanceof GoogleAuthError && linkError.localReason
        ? t('AccountSettings.googleLinkError')
        : linkError instanceof Error ? linkError.message : t('AccountSettings.googleLinkError'));
      setPassword('');
      setLoading(false);
    }
  };

  const handlePasswordUpdate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (passwordPending || !accessToken || !user || !passwordStatus || passwordStatus.userID !== user.id || !passwordStatus.value.allowed) return;
    if (newPassword !== confirmPassword) {
      setPasswordError(t('AccountSettings.passwordMismatch'));
      return;
    }
    if (passwordStatus.value.configured && !currentPassword) {
      setPasswordError(t('AccountSettings.currentPasswordRequired'));
      return;
    }
    setPasswordPending(true);
    setPasswordError('');
    setPasswordNotice('');
    const passwordAuthContext = createPasswordAuthContext();
    try {
      if (passwordStatus.value.configured) {
        await changeAccountPassword(passwordAuthContext, newPassword, confirmPassword, currentPassword);
      } else {
        await changeAccountPassword(passwordAuthContext, newPassword, confirmPassword);
      }
    } catch (requestError) {
      setPasswordError(requestError instanceof PasswordSelfServiceError
        ? requestError.code === 'current_password_required'
          ? t('AccountSettings.currentPasswordRequired')
          : t('AccountSettings.currentPasswordIncorrect')
        : requestError instanceof Error ? requestError.message : t('AccountSettings.passwordUpdateError'));
      setPasswordPending(false);
      return;
    }

    const accountId = user.id;
    clearPasswordSecrets();
    setPasswordStatus({ userID: accountId, value: { ...passwordStatus.value, configured: true } });
    setPasswordNotice(t('AccountSettings.passwordUpdated'));
    setPasswordPending(false);

    // The POST is authoritative. Refresh optional account metadata after the
    // completed write, but never turn a refresh failure into a write failure.
    void readPasswordSelfServiceStatus(passwordAuthContext).then((refreshed) => {
      if (accountId === user.id) {
        setPasswordStatus({ userID: accountId, value: { ...refreshed, configured: true } });
      }
    }).catch(() => {});
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
      setControlError(controlErrorMessage(requestError, t('AccountSettings.cliSessionRevokeError')));
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
      setControlError(controlErrorMessage(requestError, t('AccountSettings.syncBindingRevokeError')));
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
      setControlError(controlErrorMessage(requestError, t('AccountSettings.syncBindingReauthorizeError')));
    } finally {
      setBusyControl('');
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm" onMouseDown={closeSettings}>
      <div
        ref={settingsDialogRef}
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
          <button type="button" onClick={closeSettings} className="min-h-11 min-w-11 rounded-md p-2 text-zinc-400 hover:bg-white/10 hover:text-white" aria-label={t('AccountSettings.close')}>×</button>
        </div>

        <div role="tablist" aria-label={t('AccountSettings.tabs')} className="mt-6 flex flex-wrap gap-2 rounded-xl border border-white/10 bg-black/20 p-1.5">
          {accountSettingsTabs.map((tab, index) => (
            <button
              key={tab.id}
              ref={(element) => { settingsTabRefs.current[index] = element; }}
              id={`account-settings-tab-${tab.id}`}
              type="button"
              role="tab"
              aria-selected={activeSettingsTab === tab.id}
              aria-controls={`account-settings-panel-${tab.id}`}
              tabIndex={activeSettingsTab === tab.id ? 0 : -1}
              onClick={() => selectSettingsTab(tab.id)}
              onKeyDown={(event) => handleSettingsTabKeyDown(event, index)}
              className={`min-h-11 min-w-0 flex-1 basis-32 rounded-lg px-3 py-2 text-sm font-medium transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300 ${activeSettingsTab === tab.id ? 'bg-emerald-300 text-zinc-950' : 'text-zinc-300 hover:bg-white/5 hover:text-white'}`}
            >
              {t(tab.label)}
            </button>
          ))}
        </div>

        <div id="account-settings-panel-account" role="tabpanel" aria-labelledby="account-settings-tab-account" hidden={activeSettingsTab !== 'account'} tabIndex={0}>
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

          <section className="mt-6 border-t border-white/10 pt-6" aria-labelledby="account-password-title">
            <h3 id="account-password-title" className="text-lg font-semibold text-white">{t('AccountSettings.passwordTitle')}</h3>
            {passwordStatusError?.userID === user.id ? <p role="alert" className="mt-2 text-sm text-red-300">{passwordStatusError.message}</p> : null}
            {passwordStatusError?.userID !== user.id && (!passwordStatus || passwordStatus.userID !== user.id) ? (
              <p role="status" className="mt-2 text-sm text-zinc-400">{t('AccountSettings.passwordLoading')}</p>
            ) : null}
            {passwordStatus?.userID === user.id && !passwordStatus.value.allowed ? (
              <p className="mt-2 text-sm leading-6 text-zinc-400">{t('AccountSettings.passwordDemoDisabled')}</p>
            ) : null}
            {passwordStatus?.userID === user.id && passwordStatus.value.allowed ? (
              <form onSubmit={handlePasswordUpdate} className="mt-3 space-y-4 rounded-lg border border-white/10 bg-black/20 p-4">
                <p className="text-sm leading-6 text-zinc-400">
                  {passwordStatus.value.configured ? t('AccountSettings.passwordChangeHint') : t('AccountSettings.passwordSetupHint')}
                </p>
                {passwordStatus.value.configured ? (
                  <label className="block text-sm font-medium text-zinc-300">
                    {t('AccountSettings.currentPassword')}
                    <input type="password" autoComplete="current-password" aria-required="true" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
                  </label>
                ) : null}
                <label className="block text-sm font-medium text-zinc-300">
                  {t('AccountSettings.newPassword')}
                  <input type="password" autoComplete="new-password" minLength={8} required value={newPassword} onChange={(event) => setNewPassword(event.target.value)} className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
                </label>
                <label className="block text-sm font-medium text-zinc-300">
                  {t('AccountSettings.confirmNewPassword')}
                  <input type="password" autoComplete="new-password" minLength={8} required value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
                </label>
                {passwordError ? <p role="alert" className="text-sm text-red-300">{passwordError}</p> : null}
                {passwordNotice ? <p role="status" className="text-sm text-emerald-200">{passwordNotice}</p> : null}
                <p className="text-xs leading-5 text-zinc-500">{t('AccountSettings.forgotPasswordHint')}</p>
                <div className="flex justify-end">
                  <button type="submit" disabled={passwordPending || newPassword.length < 8 || !confirmPassword} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60">
                    {passwordPending ? t('AccountSettings.passwordSaving') : passwordStatus.value.configured ? t('AccountSettings.changePassword') : t('AccountSettings.setPassword')}
                  </button>
                </div>
              </form>
            ) : null}
          </section>

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
        </div>

        <div id="account-settings-panel-provider" role="tabpanel" aria-labelledby="account-settings-tab-provider" hidden={activeSettingsTab !== 'provider'} tabIndex={0}>
          {!isDemoSession ? <BYOKProfilesPanel active={activeSettingsTab === 'provider'} onProfileCreated={onProfileCreated} /> : null}

          <section className="mt-6 border-t border-white/10 pt-6" aria-labelledby="account-project-llm-title">
            <h3 id="account-project-llm-title" className="text-lg font-semibold text-white">{t('LLMSource.accountAssignments')}</h3>
            <p className="mt-1 text-sm leading-6 text-zinc-400">{t('LLMSource.accountAssignmentsHint')}</p>
            {projects.length ? projects.map((project) => (
              <ProjectLLMSourcePanel key={project.id} projectId={project.id} projectName={project.name} ownerId={user.id} sessionEpoch={sessionEpoch} compact allowPersonalProfiles={!isDemoSession} />
            )) : <p className="mt-3 text-sm text-zinc-400">{t('LLMSource.noProjects')}</p>}
          </section>
        </div>

        <div id="account-settings-panel-connections" role="tabpanel" aria-labelledby="account-settings-tab-connections" hidden={activeSettingsTab !== 'connections'} tabIndex={0}>
        <section className="mt-8 border-t border-white/10 pt-6" aria-labelledby="cli-sessions-title">
          <h3 id="cli-sessions-title" className="text-lg font-semibold text-white">{t('AccountSettings.cliSessions')}</h3>
          <p className="mt-1 text-sm text-zinc-400">{t('AccountSettings.cliSessionsHint')}</p>
          {cliSessions.length === 0 ? (
            <p className="mt-4 rounded-lg border border-white/10 bg-black/20 p-4 text-sm text-zinc-400">{t('AccountSettings.cliSessionsEmpty')}</p>
          ) : (
            <ul className="mt-4 space-y-3">
              {cliSessions.map((session) => {
                const statusLabel = session.status === 'active'
                  ? t('AccountSettings.cliSessionStatusActive')
                  : session.status === 'revoked'
                    ? t('AccountSettings.cliSessionStatusRevoked')
                    : session.status === 'expired'
                      ? t('AccountSettings.cliSessionStatusExpired')
                      : session.status;
                return (
                  <li key={session.id} className="flex flex-col gap-3 rounded-lg border border-white/10 bg-black/20 p-4 sm:flex-row sm:items-center sm:justify-between">
                    <div className="min-w-0 text-sm">
                      <p className="font-medium text-white">{session.client_name || 'CLI'} <span className="text-zinc-500">· {statusLabel}</span></p>
                      <p className="mt-1 break-all font-mono text-xs text-zinc-500">{session.id}</p>
                      <p className="mt-1 text-xs text-zinc-500">{new Date(session.created_at).toLocaleString(locale === 'zh-TW' ? 'zh-TW' : locale === 'zh-CN' ? 'zh-CN' : 'en-US')}</p>
                    </div>
                    {session.status === 'active' ? (
                      <button type="button" disabled={Boolean(busyControl)} onClick={() => void handleRevokeSession(session)} className="min-h-11 shrink-0 rounded-lg border border-red-300/20 px-3 py-2 text-sm text-red-200 hover:bg-red-300/10 disabled:opacity-50">
                        {busyControl === session.id ? t('CLIPairing.processing') : t('AccountSettings.revokeSession')}
                      </button>
                    ) : null}
                  </li>
                );
              })}
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
                      <p className="font-medium text-white">{binding.project_id} <span className="text-zinc-500">· {binding.status === 'active' ? t('AccountSettings.bindingActive') : binding.status === 'revoked' ? t('AccountSettings.bindingRevoked') : binding.status}</span></p>
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
    </div>
  );
}
