'use client';

import { type ComponentType, type ReactNode, useCallback, useEffect, useRef, useState } from 'react';
import { MessageSquareText, Pencil, Play, RefreshCw, RotateCcw, ShieldAlert, Trash2 } from 'lucide-react';
import {
  ApiError,
  clearPublicConfigCache,
  deleteAdminProject,
  deleteAdminUser,
  getAdminAnnouncement,
  getAdminManagementCatalog,
  getAdminProjects,
  getAdminPipelineStatus,
  getAdminPipelineAutomation,
  getAdminSettings,
  getAdminUsers,
  publishAnnouncement,
  rebuildAdminProjectIndex,
  renameAdminProject,
  triggerAdminProjectPipeline,
  updateAdminSettings,
  updateAdminPipelineAutomation,
  updateAdminUserManagementScope,
  updateAdminUserStatus,
  type AdminProject,
  type AdminUser,
  type AdminManagementScope,
  type AdminPipelineAutomation,
} from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { useLocale } from '@/lib/i18n';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { AnnouncementBoard } from './AnnouncementBoard';
import { AdminSharedProfilesPanel } from './AdminSharedProfilesPanel';
import { useNavigationBlocker } from './NavigationBlocker';

type Tab = 'projects' | 'users' | 'settings' | 'shared-llm';
type Notice = { tone: 'success' | 'error' | 'pending'; message: string } | null;
const userReadbackFailure: Notice = { tone: 'error', message: 'Update succeeded, but current user state is unknown. Retry loading users before making another change.' };

function adminAutomationCopy(t: (key: string) => string, key: string, fallback: string): string {
  const translated = t(key);
  return translated === key ? fallback : translated;
}
type Action =
  | { kind: 'rename-project'; project: AdminProject }
  | { kind: 'delete-project'; project: AdminProject }
  | { kind: 'rebuild-project'; project: AdminProject }
  | { kind: 'trigger-project'; project: AdminProject }
  | { kind: 'suggest-queries-project'; project: AdminProject }
  | { kind: 'change-scope'; user: AdminUser }
  | { kind: 'delete-user'; user: AdminUser }
  | { kind: 'set-user-status'; user: AdminUser; status: 'active' | 'suspended' };

export function AdminClient() {
  const { hydrated, user } = useAuth();
  const { locale, t } = useLocale();
  const [tab, setTab] = useState<Tab>('projects');
  const [projects, setProjects] = useState<AdminProject[]>([]);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [managementCatalog, setManagementCatalog] = useState<AdminManagementScope[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [usersLoading, setUsersLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [usersError, setUsersError] = useState('');
  const [adminDenied, setAdminDenied] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [action, setAction] = useState<Action | null>(null);
  const [actionError, setActionError] = useState('');
  const [actionPending, setActionPending] = useState(false);
  const [cleanRebuild, setCleanRebuild] = useState(false);
  const [registrationEnabled, setRegistrationEnabled] = useState({ registration_enabled: false, email_registration_enabled: false, google_registration_enabled: false });
  const [pipelineAutomation, setPipelineAutomation] = useState<AdminPipelineAutomation | null>(null);
  const [settingsLoading, setSettingsLoading] = useState(false);
  const [settingsError, setSettingsError] = useState('');
  const [settingsPending, setSettingsPending] = useState(false);
  const [announcementMarkdown, setAnnouncementMarkdown] = useState('');
  const [announcementBaseline, setAnnouncementBaseline] = useState('');
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pollResolver = useRef<(() => void) | null>(null);
  const pollAbortController = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const { setBlocked } = useNavigationBlocker();
  const announcementDirty = announcementMarkdown !== announcementBaseline;
  const isAuthAdmin = user?.management_scopes?.includes('auth:admin') === true;
  const isLWCAdmin = user?.management_scopes?.includes('lwc:admin') === true;
  const isAdmin = isAuthAdmin || isLWCAdmin;
  const accessDenied = !isAdmin;

  const loadProjects = useCallback(async () => {
    setProjectsLoading(true);
    setProjectsError('');
    try {
      setProjects(await getAdminProjects());
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        setAdminDenied(true);
        return;
      }
      setProjectsError(error instanceof Error ? error.message : 'Unable to load projects.');
    } finally {
      setProjectsLoading(false);
    }
  }, []);

  const loadUsers = useCallback(async () => {
    setUsersLoading(true);
    setUsersError('');
    setNotice(null);
    try {
      const [nextUsers, catalog] = await Promise.all([getAdminUsers(), getAdminManagementCatalog()]);
      setUsers(nextUsers);
      setManagementCatalog(catalog);
      return true;
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        setAdminDenied(true);
        return false;
      }
      setUsersError(error instanceof Error ? error.message : 'Unable to load users.');
      return false;
    } finally {
      setUsersLoading(false);
    }
  }, []);

  const loadSettings = useCallback(async () => {
    setSettingsLoading(true);
    setSettingsError('');
    try {
      const [settings, announcement, autoPipeline] = await Promise.all([
        isAuthAdmin ? getAdminSettings() : Promise.resolve(null),
        isLWCAdmin ? getAdminAnnouncement() : Promise.resolve(null),
        isLWCAdmin ? getAdminPipelineAutomation() : Promise.resolve(null),
      ]);
      if (autoPipeline) setPipelineAutomation(autoPipeline);
      if (settings) setRegistrationEnabled({ registration_enabled: settings.registration_enabled, email_registration_enabled: settings.email_registration_enabled === true, google_registration_enabled: settings.google_registration_enabled === true });
      const markdown = announcement ?? settings?.announcement_markdown ?? '';
      setAnnouncementMarkdown(markdown);
      setAnnouncementBaseline(markdown);
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Unable to load settings.');
    } finally {
      setSettingsLoading(false);
    }
  }, [isAuthAdmin, isLWCAdmin]);

  const handleRegistrationToggle = async (method: 'registration_enabled' | 'email_registration_enabled' | 'google_registration_enabled') => {
    setSettingsError('');
    setSettingsPending(true);
    try {
      const settings = await updateAdminSettings({ [method]: !registrationEnabled[method] });
      setRegistrationEnabled({ registration_enabled: settings.registration_enabled, email_registration_enabled: settings.email_registration_enabled === true, google_registration_enabled: settings.google_registration_enabled === true });
      clearPublicConfigCache();
      setNotice({ tone: 'success', message: 'Registration settings updated.' });
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Settings update failed.');
    } finally {
      setSettingsPending(false);
    }
  };

  const handlePipelineAutomationToggle = async () => {
    if (!pipelineAutomation || settingsPending) return;
    setSettingsError('');
    setSettingsPending(true);
    try {
      const updated = await updateAdminPipelineAutomation(!pipelineAutomation.allow_auto_pipeline, pipelineAutomation.revision);
      setPipelineAutomation(updated);
      setNotice({ tone: 'success', message: 'Auto-pipeline settings updated. Enabling it starts a backlog scan for Projects with saved pending source changes.' });
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Auto-pipeline settings update failed.');
      void getAdminPipelineAutomation().then(setPipelineAutomation).catch(() => undefined);
    } finally {
      setSettingsPending(false);
    }
  };

  const publishCurrentAnnouncement = async () => {
    if (!announcementDirty || (announcementMarkdown.trim() === '' && !window.confirm('Clear the public announcement?'))) return;
    setSettingsError('');
    setSettingsPending(true);
    try {
      const settings = await publishAnnouncement(announcementMarkdown);
      if (typeof settings.announcement_markdown !== 'string') {
        throw new Error('Invalid announcement publish response');
      }
      const markdown = settings.announcement_markdown;
      setAnnouncementMarkdown(markdown);
      setAnnouncementBaseline(markdown);
      setNotice({ tone: 'success', message: 'Announcement published.' });
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Announcement publish failed.');
    } finally {
      setSettingsPending(false);
    }
  };

  const refreshActive = useCallback(() => {
    if (tab === 'projects') return loadProjects();
    if (tab === 'users') return loadUsers();
    return loadSettings();
  }, [loadProjects, loadSettings, loadUsers, tab]);

  const closeAction = () => {
    if (actionPending) return;
    setAction(null);
    setActionError('');
    setCleanRebuild(false);
  };

  const submitRenameProject = async (name: string) => {
    if (!action || action.kind !== 'rename-project') return;
    setActionPending(true);
    setActionError('');
    try {
      await renameAdminProject(action.project.id, name);
      await loadProjects();
      setNotice({ tone: 'success', message: 'Project renamed.' });
      setAction(null);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Rename failed.');
    } finally {
      setActionPending(false);
    }
  };

  const submitScopeChange = async (scope: 'auth:admin' | 'lwc:admin', scopeAction: 'grant' | 'remove') => {
    if (!action || action.kind !== 'change-scope') return;
    setActionPending(true);
    setActionError('');
    try {
      await updateAdminUserManagementScope(action.user.id, scope, scopeAction);
      const refreshed = await loadUsers();
      setNotice(refreshed ? { tone: 'success', message: 'Management scope updated.' } : userReadbackFailure);
      setAction(null);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Management scope update failed.');
    } finally {
      setActionPending(false);
    }
  };

  const submitConfirmAction = async () => {
    if (!action) return;
    setActionPending(true);
    setActionError('');
    try {
      if (action.kind === 'delete-project') {
        await deleteAdminProject(action.project.id);
        await loadProjects();
        setNotice({ tone: 'success', message: 'Project deleted.' });
      } else if (action.kind === 'rebuild-project') {
        await rebuildAdminProjectIndex(action.project.id);
        await loadProjects();
        setNotice({ tone: 'success', message: 'Index rebuild started.' });
      } else if (action.kind === 'trigger-project') {
        await triggerAdminProjectPipeline(action.project.id, { cleanRebuild });
        await loadProjects();
        setNotice({
          tone: 'success',
          message: cleanRebuild ? 'Pipeline triggered (clean rebuild).' : 'Pipeline triggered.',
        });
      } else if (action.kind === 'suggest-queries-project') {
        const accepted = await triggerAdminProjectPipeline(action.project.id, { stage: 'suggested-queries' });
        if (!mounted.current) return;
        const executionId = accepted.execution_id;
        if (!executionId) throw new Error('Regeneration accepted without an execution ID.');
        setAction(null);
        setNotice({ tone: 'pending', message: 'Query chips regeneration started; waiting for completion.' });
        for (let attempt = 0; attempt < 30; attempt += 1) {
          if (attempt > 0) {
            await new Promise<void>((resolve) => {
              let done = false;
              const finish = () => {
                if (done) return;
                done = true;
                if (pollTimer.current) {
                  clearTimeout(pollTimer.current);
                  pollTimer.current = null;
                }
                pollResolver.current = null;
                resolve();
              };
              pollResolver.current = finish;
              pollTimer.current = setTimeout(finish, 1000);
            });
          }
          if (!mounted.current) return;
          const controller = new AbortController();
          pollAbortController.current = controller;
          const timeout = setTimeout(() => controller.abort(), 5000);
          let status: Awaited<ReturnType<typeof getAdminPipelineStatus>>;
          try {
            status = await getAdminPipelineStatus(action.project.id, executionId, controller.signal);
          } finally {
            clearTimeout(timeout);
            if (pollAbortController.current === controller) pollAbortController.current = null;
          }
          if (!mounted.current) return;
          const execution = status.last_execution;
          if (execution?.status === 'SUCCEEDED') {
            setNotice({ tone: 'success', message: 'Query chips regeneration completed.' });
            break;
          }
          if (execution?.status === 'FAILED' || execution?.status === 'CANCELLED') {
            const diagnostic = execution.diagnostic?.detail_code || execution.diagnostic?.error_class || 'generation failed';
            setNotice({ tone: 'error', message: `Query chips regeneration failed: ${diagnostic.slice(0, 120)}.` });
            break;
          }
          if (attempt === 29) setNotice({ tone: 'error', message: 'Query chips regeneration is still pending.' });
        }
      } else if (action.kind === 'set-user-status') {
        await updateAdminUserStatus(action.user.id, action.status);
        const refreshed = await loadUsers();
        setNotice(refreshed ? { tone: 'success', message: action.status === 'suspended' ? 'User suspended.' : 'User restored. They must sign in again.' } : userReadbackFailure);
      } else if (action.kind === 'delete-user') {
        await deleteAdminUser(action.user.id);
        const refreshed = await loadUsers();
        setNotice(refreshed ? { tone: 'success', message: 'User deleted.' } : userReadbackFailure);
      }
      setAction(null);
    } catch (error) {
      if (mounted.current) {
        if (action?.kind === 'suggest-queries-project') {
          setNotice({
            tone: 'error',
            message: `Query chips regeneration failed: ${error instanceof Error ? error.message : 'status unavailable'}.`,
          });
        } else {
          setActionError(error instanceof Error ? error.message : 'Action failed.');
        }
      }
    } finally {
      if (mounted.current) {
        setActionPending(false);
      }
    }
  };

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      if (pollTimer.current) {
        clearTimeout(pollTimer.current);
        pollTimer.current = null;
      }
      pollAbortController.current?.abort();
      pollAbortController.current = null;
      if (pollResolver.current) {
        pollResolver.current();
        pollResolver.current = null;
      }
    };
  }, []);

  useEffect(() => {
    if (!hydrated || !isAdmin) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- kick off initial admin reads after auth hydration
    if (isLWCAdmin) void loadProjects();
    if (isAuthAdmin) void loadUsers();
    if (isAuthAdmin || isLWCAdmin) void loadSettings();
  }, [hydrated, isAdmin, isAuthAdmin, isLWCAdmin, loadProjects, loadSettings, loadUsers]);

  useEffect(() => {
    if (!hydrated || tab !== 'projects' || isLWCAdmin) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- choose the first tab this scope can read
    setTab(isAuthAdmin ? 'users' : 'settings');
  }, [hydrated, isAuthAdmin, isLWCAdmin, tab]);

  useEffect(() => {
    setBlocked(announcementDirty);
    return () => setBlocked(false);
  }, [announcementDirty, setBlocked]);

  if (!hydrated) {
    return <div className="py-20 text-center text-sm text-zinc-500">Loading...</div>;
  }

  if (accessDenied || adminDenied) {
    return (
      <Surface variant="glass" className="mx-auto max-w-lg p-6 text-center">
        <ShieldAlert className="mx-auto size-8 text-amber-300" aria-hidden="true" />
        <h1 className="mt-4 text-xl font-semibold text-white">Admin access required</h1>
        <p className="mt-2 text-sm text-zinc-400">
          Your account does not have permission to use this console.
        </p>
      </Surface>
    );
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">Admin</h1>
          <p className="mt-2 max-w-2xl text-zinc-400">
            Manage projects, users, and pipeline operations.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void refreshActive()}
          className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-white/10 bg-white/5 px-3 text-sm font-medium text-zinc-200 transition hover:bg-white/10"
        >
          <RefreshCw className="size-4" aria-hidden="true" />
          Refresh
        </button>
      </header>

      <Surface variant="glass" className="p-2">
        <div className="flex gap-1">
          {isLWCAdmin ? <TabButton active={tab === 'projects'} onClick={() => setTab('projects')}>Projects</TabButton> : null}
          {isLWCAdmin ? <TabButton active={tab === 'shared-llm'} onClick={() => setTab('shared-llm')}>Shared LLM</TabButton> : null}
          {isAuthAdmin ? <TabButton active={tab === 'users'} onClick={() => setTab('users')}>Users</TabButton> : null}
          {(isAuthAdmin || isLWCAdmin) ? <TabButton active={tab === 'settings'} onClick={() => setTab('settings')}>Settings</TabButton> : null}
        </div>
      </Surface>

      {notice ? (
        <Surface
          variant="default"
          className={`px-4 py-3 text-sm ${notice.tone === 'error' ? 'text-amber-200' : notice.tone === 'pending' ? 'text-amber-200' : 'text-emerald-200'}`}
        >
          {notice.message}
        </Surface>
      ) : null}

      {tab === 'shared-llm' ? (
        <AdminSharedProfilesPanel />
      ) : tab === 'projects' ? (
        <ProjectsTable
          projects={projects}
          loading={projectsLoading}
          error={projectsError}
          pending={actionPending}
          onRetry={loadProjects}
          onAction={setAction}
        />
      ) : tab === 'users' ? (
        <UsersTable
          users={users}
          currentUserId={user?.id}
          loading={usersLoading}
          error={usersError}
          pending={actionPending}
          onRetry={loadUsers}
          onAction={setAction}
          managementCatalog={managementCatalog}
          locale={locale}
          canManageScopes={isAuthAdmin && managementCatalog.length === 2}
          canManageStatus={isAuthAdmin}
          canDeleteUser={isAuthAdmin && isLWCAdmin}
        />
      ) : (
        <SettingsPanel
          registrationEnabled={registrationEnabled}
          loading={settingsLoading}
          pending={settingsPending}
          error={settingsError}
          canManageRegistration={isAuthAdmin}
          canManageAnnouncement={isLWCAdmin}
          canManagePipelineAutomation={isLWCAdmin}
          pipelineAutomation={pipelineAutomation}
          pipelineAutomationLabel={adminAutomationCopy(t, 'Admin.autoPipelineEnabled', 'Allow automatic Pipeline runs')}
          pipelineAutomationDescription={adminAutomationCopy(t, 'Admin.autoPipelineDescription', 'This global switch must be on with a Project switch for source commits to start incremental runs. Enabling scans existing Projects for pending saved sources. Manual runs and the daily schedule stay independent.')}
          masterLabel={t('Admin.registrationEnabled')}
          emailLabel={t('Admin.emailRegistrationEnabled')}
          googleLabel={t('Admin.googleRegistrationEnabled')}
          onRetry={loadSettings}
          onToggle={(method) => void handleRegistrationToggle(method)}
          announcementMarkdown={announcementMarkdown}
          announcementDirty={announcementDirty}
          onAnnouncementChange={setAnnouncementMarkdown}
          onPublishAnnouncement={() => void publishCurrentAnnouncement()}
          onTogglePipelineAutomation={() => void handlePipelineAutomationToggle()}
        />
      )}

      {action?.kind === 'rename-project' ? (
        <RenameProjectModal
          project={action.project}
          pending={actionPending}
          error={actionError}
          onSubmit={(name) => void submitRenameProject(name)}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'change-scope' ? (
        <ScopeActionModal
          user={action.user}
          scopes={managementCatalog}
          locale={locale}
          pending={actionPending}
          error={actionError}
          onSubmit={(scope, scopeAction) => void submitScopeChange(scope, scopeAction)}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'delete-project' ? (
        <ConfirmActionModal
          title="Delete project"
          description={`Delete ${action.project.name} (${action.project.id})? This cannot be undone.`}
          submitLabel="Delete project"
          pendingLabel="Deleting..."
          danger
          pending={actionPending}
          error={actionError}
          onSubmit={() => void submitConfirmAction()}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'rebuild-project' ? (
        <ConfirmActionModal
          title="Rebuild index"
          description={`Rebuild the search index for ${action.project.name} (${action.project.id}).`}
          submitLabel="Rebuild index"
          pendingLabel="Starting..."
          pending={actionPending}
          error={actionError}
          onSubmit={() => void submitConfirmAction()}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'trigger-project' ? (
        <ConfirmActionModal
          title="Trigger pipeline"
          description={`Trigger the pipeline for ${action.project.name} (${action.project.id}).`}
          submitLabel="Trigger pipeline"
          pendingLabel="Triggering..."
          pending={actionPending}
          error={actionError}
          checkboxLabel="Clean rebuild (discard current wiki/Synto state; keep raw). Full LLM recompile."
          checkboxChecked={cleanRebuild}
          onCheckboxChange={setCleanRebuild}
          onSubmit={() => void submitConfirmAction()}
          onClose={() => {
            setCleanRebuild(false);
            closeAction();
          }}
        />
      ) : null}
      {action?.kind === 'suggest-queries-project' ? (
        <ConfirmActionModal
          title="Regenerate query chips"
          description={`Regenerate suggested query chips only for ${action.project.name} (${action.project.id}). Does not re-run Synto or rebuild the index.`}
          submitLabel="Regenerate chips"
          pendingLabel="Triggering..."
          pending={actionPending}
          error={actionError}
          onSubmit={() => void submitConfirmAction()}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'set-user-status' ? (
        <ConfirmActionModal
          title={action.status === 'suspended' ? 'Suspend user' : 'Restore user'}
          description={action.status === 'suspended'
            ? `Suspend ${action.user.email || action.user.id}? Access ends immediately. Data is preserved and previously admitted Pipeline work will finish.`
            : `Restore ${action.user.email || action.user.id}? Their role and data are preserved. They must sign in again; old sessions stay invalid.`}
          submitLabel={action.status === 'suspended' ? 'Suspend' : 'Restore'}
          pendingLabel="Updating..."
          danger={action.status === 'suspended'}
          pending={actionPending}
          error={actionError}
          onSubmit={() => void submitConfirmAction()}
          onClose={closeAction}
        />
      ) : null}
      {action?.kind === 'delete-user' ? (
        <ConfirmActionModal
          title="Delete user"
          description={`Delete ${action.user.email || action.user.id}? This deletes their user record and projects.`}
          submitLabel="Delete user"
          pendingLabel="Deleting..."
          danger
          pending={actionPending}
          error={actionError}
          onSubmit={() => void submitConfirmAction()}
          onClose={closeAction}
        />
      ) : null}
    </div>
  );
}

function SettingsPanel({
  registrationEnabled,
  loading,
  pending,
  error,
  canManageRegistration,
  canManageAnnouncement,
  canManagePipelineAutomation,
  pipelineAutomation,
  pipelineAutomationLabel,
  pipelineAutomationDescription,
  masterLabel,
  emailLabel,
  googleLabel,
  onRetry,
  onToggle,
  announcementMarkdown,
  announcementDirty,
  onAnnouncementChange,
  onPublishAnnouncement,
  onTogglePipelineAutomation,
}: {
  registrationEnabled: { registration_enabled: boolean; email_registration_enabled: boolean; google_registration_enabled: boolean };
  loading: boolean;
  pending: boolean;
  error: string;
  canManageRegistration: boolean;
  canManageAnnouncement: boolean;
  canManagePipelineAutomation: boolean;
  pipelineAutomation: AdminPipelineAutomation | null;
  pipelineAutomationLabel: string;
  pipelineAutomationDescription: string;
  masterLabel: string;
  emailLabel: string;
  googleLabel: string;
  onRetry: () => void;
  onToggle: (method: 'registration_enabled' | 'email_registration_enabled' | 'google_registration_enabled') => void;
  announcementMarkdown: string;
  announcementDirty: boolean;
  onAnnouncementChange: (value: string) => void;
  onPublishAnnouncement: () => void;
  onTogglePipelineAutomation: () => void;
}) {
  return (
    <Surface variant="glass" className="p-5">
      {loading ? (
        <p className="text-sm text-zinc-500">Loading...</p>
      ) : (
        <div className="space-y-6">
          {error ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-sm text-amber-300">{error}</p>
              <button
                type="button"
                onClick={onRetry}
                className="text-sm font-medium text-zinc-300 transition hover:text-white"
              >
                Retry
              </button>
            </div>
          ) : null}
          {canManageRegistration ? <div className="space-y-4">
            <label className="flex items-center justify-between gap-4">
              <span className="text-sm font-medium text-white">{masterLabel}</span>
              <input type="checkbox" checked={registrationEnabled.registration_enabled} disabled={pending || Boolean(error)} onChange={() => onToggle('registration_enabled')} className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400" />
            </label>
            <div className={`space-y-4 border-l border-white/10 pl-5 ${registrationEnabled.registration_enabled ? '' : 'opacity-40'}`}>
              {(['email_registration_enabled', 'google_registration_enabled'] as const).map((method) => (
                <label key={method} className="flex items-center justify-between gap-4">
                  <span className="text-sm font-medium text-white">{method === 'email_registration_enabled' ? emailLabel : googleLabel}</span>
                  <input type="checkbox" checked={registrationEnabled[method]} disabled={!registrationEnabled.registration_enabled || pending || Boolean(error)} onChange={() => onToggle(method)} className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400" />
                </label>
              ))}
            </div>
          </div> : null}
          {canManagePipelineAutomation ? <div className="border-t border-white/10 pt-5" data-testid="admin-pipeline-automation">
            <h2 className="text-lg font-semibold text-white">{pipelineAutomationLabel}</h2>
            <p className="mt-1 text-sm text-zinc-400">{pipelineAutomationDescription}</p>
            {pipelineAutomation ? (
              <>
                <label className="mt-4 flex max-w-xl items-center justify-between gap-4 rounded-lg border border-white/10 bg-black/20 px-4 py-3">
                  <span className="text-sm font-medium text-zinc-200">{pipelineAutomationLabel}</span>
                  <input
                    type="checkbox"
                    checked={pipelineAutomation.allow_auto_pipeline}
                    disabled={pending || Boolean(error)}
                    onChange={onTogglePipelineAutomation}
                    className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400"
                    data-testid="admin-pipeline-automation-toggle"
                  />
                </label>
                <p className="mt-2 text-xs text-zinc-500" role="status">
                  Backlog scan: {pipelineAutomation.backlog_scan_status} · Revision {pipelineAutomation.revision}
                </p>
              </>
            ) : <p className="mt-3 text-sm text-zinc-500">Auto-pipeline settings are unavailable.</p>}
          </div> : null}
          {canManageAnnouncement ? <div>
            <h2 className="text-lg font-semibold text-white">Announcement</h2>
            <p className="mt-1 text-sm text-zinc-400">Edit the published announcement and preview it live.</p>
            <textarea
              aria-label="Announcement Markdown"
              value={announcementMarkdown}
              onChange={(event) => onAnnouncementChange(event.target.value)}
              disabled={pending}
              rows={8}
              className="mt-3 w-full rounded-lg border border-white/10 bg-black/30 p-3 font-mono text-sm text-zinc-100 outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 focus:border-emerald-300"
            />
            <div className="mt-3 flex flex-wrap gap-3">
              <button type="button" disabled={pending || !announcementDirty} onClick={onPublishAnnouncement} className="rounded-lg border border-emerald-300/30 px-4 py-2 text-sm font-semibold text-emerald-200 disabled:opacity-50">Publish</button>
            </div>
            <h3 className="mt-5 text-sm font-semibold uppercase tracking-wide text-zinc-400">Preview</h3>
            <AnnouncementBoard markdown={announcementMarkdown} />
          </div> : null}
        </div>
      )}
    </Surface>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`min-h-10 rounded-md px-4 text-sm font-medium transition ${
        active ? 'bg-white/10 text-white' : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
      }`}
    >
      {children}
    </button>
  );
}

function adminOwnerPrimary(project: AdminProject): string {
  return project.userName || project.userEmail || project.userId || '—';
}

function adminOwnerSecondary(project: AdminProject, primary: string): string | undefined {
  const parts: string[] = [];
  if (project.userId && project.userId !== primary) {
    parts.push(project.userId);
  }
  if (project.userEmail && project.userEmail !== primary && !parts.includes(project.userEmail)) {
    parts.push(project.userEmail);
  }
  return parts.length > 0 ? parts.join(' · ') : undefined;
}

function adminUserPrimary(user: AdminUser): string {
  return user.name || user.email || user.id || '—';
}

function IdentityCell({ primary, secondary }: { primary: string; secondary?: string }) {
  return (
    <div className="min-w-0">
      <div className="truncate font-medium text-white">{primary}</div>
      {secondary ? (
        <div className="mt-0.5 truncate font-mono text-xs text-zinc-500" title={secondary}>
          {secondary}
        </div>
      ) : null}
    </div>
  );
}

function ProjectsTable({
  projects,
  loading,
  error,
  pending,
  onRetry,
  onAction,
}: {
  projects: AdminProject[];
  loading: boolean;
  error: string;
  pending: boolean;
  onRetry: () => void;
  onAction: (action: Action) => void;
}) {
  return (
    <Surface variant="glass" className="overflow-hidden">
      <TableStatus
        loading={loading}
        error={error}
        empty={!loading && projects.length === 0}
        emptyLabel="No projects found."
        onRetry={onRetry}
      />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[900px] text-left text-sm">
          <thead className="border-b border-white/10 text-xs uppercase text-zinc-500">
            <tr>
              <th className="px-4 py-3 font-medium">Project</th>
              <th className="px-4 py-3 font-medium">Project ID</th>
              <th className="px-4 py-3 font-medium">Owner</th>
              <th className="px-4 py-3 font-medium">Concept count</th>
              <th className="px-4 py-3 font-medium">Source count</th>
              <th className="px-4 py-3 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-white/8">
            {projects.map((project) => {
              const ownerPrimary = adminOwnerPrimary(project);
              return (
                <tr key={project.id}>
                  <td className="px-4 py-3">
                    <IdentityCell primary={project.name} />
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-zinc-400" title={project.projectId || project.id}>
                    {project.projectId || '—'}
                  </td>
                  <td className="px-4 py-3">
                    <IdentityCell
                      primary={ownerPrimary}
                      secondary={adminOwnerSecondary(project, ownerPrimary)}
                    />
                  </td>
                  <td className="px-4 py-3 tabular-nums text-zinc-300">{project.conceptCount}</td>
                  <td className="px-4 py-3 tabular-nums text-zinc-300">{project.sourceCount}</td>
                  <td className="px-4 py-3">
                    <div className="flex justify-end gap-1">
                      <IconAction
                        label="Rename"
                        icon={Pencil}
                        disabled={pending}
                        onClick={() => onAction({ kind: 'rename-project', project })}
                      />
                      <IconAction
                        label="Rebuild index"
                        icon={RotateCcw}
                        disabled={pending}
                        onClick={() => onAction({ kind: 'rebuild-project', project })}
                      />
                      <IconAction
                        label="Trigger pipeline"
                        icon={Play}
                        disabled={pending}
                        onClick={() => onAction({ kind: 'trigger-project', project })}
                      />
                      <IconAction
                        label="Regenerate query chips"
                        icon={MessageSquareText}
                        disabled={pending}
                        onClick={() => onAction({ kind: 'suggest-queries-project', project })}
                      />
                      <IconAction
                        label="Delete"
                        icon={Trash2}
                        danger
                        disabled={pending}
                        onClick={() => onAction({ kind: 'delete-project', project })}
                      />
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Surface>
  );
}

function UsersTable({
  users,
  currentUserId,
  loading,
  error,
  pending,
  onRetry,
  onAction,
  canManageScopes,
  canManageStatus,
  canDeleteUser,
  managementCatalog,
  locale,
}: {
  users: AdminUser[];
  currentUserId?: string;
  loading: boolean;
  error: string;
  pending: boolean;
  onRetry: () => void;
  onAction: (action: Action) => void;
  canManageScopes: boolean;
  canManageStatus: boolean;
  canDeleteUser: boolean;
  managementCatalog: AdminManagementScope[];
  locale: 'zh-TW' | 'en';
}) {
  const unknown = loading || !!error;
  return (
    <Surface variant="glass" className="overflow-hidden">
      <TableStatus
        loading={loading}
        error={error}
        empty={!loading && users.length === 0}
        emptyLabel="No users found."
        onRetry={onRetry}
      />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[900px] text-left text-sm">
          <thead className="border-b border-white/10 text-xs uppercase text-zinc-500">
            <tr>
              <th className="px-4 py-3 font-medium">User</th>
              <th className="px-4 py-3 font-medium">User ID</th>
              <th className="px-4 py-3 font-medium">Management scopes</th>
              <th className="px-4 py-3 font-medium">Status</th>
              <th className="px-4 py-3 font-medium">Project count</th>
              <th className="px-4 py-3 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-white/8">
            {users.map((user) => {
              const primary = adminUserPrimary(user);
              const secondary =
                user.email && primary !== user.email ? user.email : undefined;
              return (
                <tr key={user.id}>
                  <td className="px-4 py-3">
                    <IdentityCell primary={primary} secondary={secondary} />
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-zinc-400" title={user.id}>
                    {user.id || '—'}
                  </td>
                  <td className="px-4 py-3">
                    {unknown ? 'Unknown' : <div className="flex flex-wrap gap-1">
                      {managementCatalog.map((scope) => user.scopeStates[scope.id]?.state === 'granted'
                        ? <Badge key={scope.id} variant="accent">{scope.display_name[locale] || scope.id}</Badge>
                        : null)}
                      {!Object.values(user.scopeStates).some((scope) => scope?.state === 'granted') ? <span className="text-zinc-500">None</span> : null}
                    </div>}
                  </td>
                  <td className="px-4 py-3"><Badge variant={user.status === 'active' ? 'muted' : 'accent'}>{unknown ? 'Unknown' : user.status}</Badge></td>
                  <td className="px-4 py-3 tabular-nums text-zinc-300">{user.projectCount}</td>
                  <td className="px-4 py-3">
                    <div className="flex justify-end gap-1">
                      {canManageScopes ? <IconAction
                        label="Manage scopes"
                        icon={ShieldAlert}
                        disabled={pending || unknown}
                        onClick={() => onAction({ kind: 'change-scope', user })}
                      /> : null}
                      {canManageStatus ? <IconAction
                        label={user.status === 'suspended' ? 'Restore user' : 'Suspend user'}
                        icon={user.status === 'suspended' ? RotateCcw : ShieldAlert}
                        disabled={pending || unknown || (user.id === currentUserId && user.status !== 'suspended')}
                        onClick={() => onAction({ kind: 'set-user-status', user, status: user.status === 'suspended' ? 'active' : 'suspended' })}
                      /> : null}
                      {canDeleteUser ? <IconAction
                        label="Delete user"
                        icon={Trash2}
                        danger
                        disabled={pending || unknown}
                        onClick={() => onAction({ kind: 'delete-user', user })}
                      /> : null}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Surface>
  );
}

function TableStatus({
  loading,
  error,
  empty,
  emptyLabel,
  onRetry,
}: {
  loading: boolean;
  error: string;
  empty: boolean;
  emptyLabel: string;
  onRetry: () => void;
}) {
  if (loading) {
    return <p className="border-b border-white/10 px-4 py-3 text-sm text-zinc-500">Loading...</p>;
  }

  if (error) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/10 px-4 py-3">
        <p className="text-sm text-amber-300">{error}</p>
        <button
          type="button"
          onClick={onRetry}
          className="text-sm font-medium text-zinc-300 transition hover:text-white"
        >
          Retry
        </button>
      </div>
    );
  }

  if (empty) {
    return <p className="border-b border-white/10 px-4 py-3 text-sm text-zinc-500">{emptyLabel}</p>;
  }

  return null;
}

function IconAction({
  label,
  icon: Icon,
  danger = false,
  disabled = false,
  onClick,
}: {
  label: string;
  icon: ComponentType<{ className?: string; 'aria-hidden'?: boolean }>;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
      className={`inline-flex min-h-10 min-w-10 items-center justify-center rounded-md border transition ${
        danger
          ? 'border-red-400/20 text-red-300 hover:bg-red-400/10'
          : 'border-white/10 text-zinc-300 hover:bg-white/10 hover:text-white'
      }`}
    >
      <Icon className="size-4" aria-hidden />
    </button>
  );
}

function ModalFrame({
  title,
  children,
  onClose,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 px-4 backdrop-blur-sm">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="w-full max-w-md rounded-lg border border-white/10 bg-zinc-950 p-5 shadow-2xl"
      >
        <h2 className="text-lg font-semibold text-white">{title}</h2>
        {children}
        <button type="button" onClick={onClose} className="sr-only">
          Close
        </button>
      </div>
    </div>
  );
}

function RenameProjectModal({
  project,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  project: AdminProject;
  pending: boolean;
  error: string;
  onSubmit: (name: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(project.name);
  return (
    <ModalFrame title="Rename project" onClose={onClose}>
      <label className="mt-4 block text-sm text-zinc-400">
        Project name
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          className="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 focus:border-emerald-400"
        />
      </label>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel="Rename"
        pendingLabel="Renaming..."
        disabled={!name.trim()}
        onSubmit={() => onSubmit(name.trim())}
        onClose={onClose}
      />
    </ModalFrame>
  );
}

function ScopeActionModal({
  user,
  scopes,
  locale,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  user: AdminUser;
  scopes: AdminManagementScope[];
  locale: 'zh-TW' | 'en';
  pending: boolean;
  error: string;
  onSubmit: (scope: 'auth:admin' | 'lwc:admin', action: 'grant' | 'remove') => void;
  onClose: () => void;
}) {
  const [scope, setScope] = useState<'auth:admin' | 'lwc:admin'>('auth:admin');
  const granted = user.scopeStates[scope]?.state === 'granted';
  const selectedScope = scopes.find((entry) => entry.id === scope);
  return (
    <ModalFrame title="Manage scopes" onClose={onClose}>
      <p className="mt-3 text-sm text-zinc-400">{user.email || user.id}</p>
      <select
        value={scope}
        onChange={(event) => setScope(event.target.value as 'auth:admin' | 'lwc:admin')}
        className="mt-4 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 focus:border-emerald-400"
      >
        {scopes.map((entry) => <option key={entry.id} value={entry.id}>{entry.display_name[locale] || entry.id}</option>)}
      </select>
      {selectedScope ? <p className="mt-2 text-sm text-zinc-400">{selectedScope.description[locale] || selectedScope.description.en}</p> : null}
      <p className="mt-2 text-sm text-zinc-400">Current state: {user.scopeStates[scope]?.state ?? 'not granted'}</p>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel={granted ? 'Remove scope' : 'Grant scope'}
        pendingLabel="Updating..."
        onSubmit={() => onSubmit(scope, granted ? 'remove' : 'grant')}
        onClose={onClose}
      />
    </ModalFrame>
  );
}

function ConfirmActionModal({
  title,
  description,
  submitLabel,
  pendingLabel,
  pending,
  error,
  danger = false,
  checkboxLabel,
  checkboxChecked = false,
  onCheckboxChange,
  onSubmit,
  onClose,
}: {
  title: string;
  description: string;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  error: string;
  danger?: boolean;
  checkboxLabel?: string;
  checkboxChecked?: boolean;
  onCheckboxChange?: (checked: boolean) => void;
  onSubmit: () => void;
  onClose: () => void;
}) {
  return (
    <ModalFrame title={title} onClose={onClose}>
      <p className="mt-3 text-sm leading-6 text-zinc-400">{description}</p>
      {checkboxLabel && onCheckboxChange ? (
        <label className="mt-4 flex cursor-pointer items-start gap-3 rounded-lg border border-white/10 bg-white/[0.03] p-3 text-sm text-zinc-300">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 rounded border-white/20 bg-transparent"
            checked={checkboxChecked}
            disabled={pending}
            onChange={(event) => onCheckboxChange(event.target.checked)}
          />
          <span className="leading-5">{checkboxLabel}</span>
        </label>
      ) : null}
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel={submitLabel}
        pendingLabel={pendingLabel}
        danger={danger}
        onSubmit={onSubmit}
        onClose={onClose}
      />
    </ModalFrame>
  );
}

function ModalActions({
  pending,
  submitLabel,
  pendingLabel,
  disabled = false,
  danger = false,
  onSubmit,
  onClose,
}: {
  pending: boolean;
  submitLabel: string;
  pendingLabel: string;
  disabled?: boolean;
  danger?: boolean;
  onSubmit: () => void;
  onClose: () => void;
}) {
  return (
    <div className="mt-5 flex justify-end gap-2">
      <button
        type="button"
        disabled={pending}
        onClick={onClose}
        className="min-h-11 rounded-lg border border-white/10 px-4 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-60"
      >
        Cancel
      </button>
      <button
        type="button"
        disabled={pending || disabled}
        onClick={onSubmit}
        className={`min-h-11 rounded-lg px-4 text-sm font-semibold transition disabled:cursor-not-allowed disabled:opacity-60 ${
          danger
            ? 'bg-red-400 text-zinc-950 hover:bg-red-300'
            : 'bg-emerald-400 text-zinc-950 hover:bg-emerald-300'
        }`}
      >
        {pending ? pendingLabel : submitLabel}
      </button>
    </div>
  );
}
