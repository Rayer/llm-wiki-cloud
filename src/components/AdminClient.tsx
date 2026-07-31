'use client';

import { type ComponentType, type ReactNode, useCallback, useEffect, useState } from 'react';
import { Pencil, Play, RefreshCw, RotateCcw, ShieldAlert, Trash2 } from 'lucide-react';
import {
  ApiError,
  clearPublicConfigCache,
  deleteAdminProject,
  deleteAdminUser,
  getAdminProjects,
  getAdminSettings,
  getAdminUsers,
  rebuildAdminProjectIndex,
  renameAdminProject,
  triggerAdminProjectPipeline,
  updateAdminSettings,
  updateAdminUserRole,
  type AdminProject,
  type AdminUser,
} from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { useLocale } from '@/lib/i18n';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';

type Tab = 'projects' | 'users' | 'settings';
type Notice = { tone: 'success' | 'error'; message: string } | null;
type Action =
  | { kind: 'rename-project'; project: AdminProject }
  | { kind: 'delete-project'; project: AdminProject }
  | { kind: 'rebuild-project'; project: AdminProject }
  | { kind: 'trigger-project'; project: AdminProject }
  | { kind: 'change-role'; user: AdminUser }
  | { kind: 'delete-user'; user: AdminUser };

export function AdminClient() {
  const { hydrated, user } = useAuth();
  const { t } = useLocale();
  const [tab, setTab] = useState<Tab>('projects');
  const [projects, setProjects] = useState<AdminProject[]>([]);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [usersLoading, setUsersLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [usersError, setUsersError] = useState('');
  const [adminDenied, setAdminDenied] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [action, setAction] = useState<Action | null>(null);
  const [actionError, setActionError] = useState('');
  const [actionPending, setActionPending] = useState(false);
  const [registrationEnabled, setRegistrationEnabled] = useState(true);
  const [settingsLoading, setSettingsLoading] = useState(false);
  const [settingsError, setSettingsError] = useState('');
  const [settingsPending, setSettingsPending] = useState(false);
  const isAdmin = user?.role === 'admin';
  const accessDenied = user?.role !== 'admin';

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
    try {
      setUsers(await getAdminUsers());
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        setAdminDenied(true);
        return;
      }
      setUsersError(error instanceof Error ? error.message : 'Unable to load users.');
    } finally {
      setUsersLoading(false);
    }
  }, []);

  const loadSettings = useCallback(async () => {
    setSettingsLoading(true);
    setSettingsError('');
    try {
      const settings = await getAdminSettings();
      setRegistrationEnabled(settings.registration_enabled);
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Unable to load settings.');
    } finally {
      setSettingsLoading(false);
    }
  }, []);

  const handleRegistrationToggle = async () => {
    const previous = registrationEnabled;
    setRegistrationEnabled(!registrationEnabled);
    setSettingsError('');
    setSettingsPending(true);
    try {
      await updateAdminSettings({ registration_enabled: !previous });
      clearPublicConfigCache();
      setNotice({
        tone: 'success',
        message: !previous ? 'Registration enabled.' : 'Registration disabled.',
      });
    } catch (error) {
      setRegistrationEnabled(previous);
      setSettingsError(error instanceof Error ? error.message : 'Settings update failed.');
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

  const submitRoleChange = async (role: string) => {
    if (!action || action.kind !== 'change-role') return;
    setActionPending(true);
    setActionError('');
    try {
      await updateAdminUserRole(action.user.id, role);
      await loadUsers();
      setNotice({ tone: 'success', message: 'User role updated.' });
      setAction(null);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Role update failed.');
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
        await triggerAdminProjectPipeline(action.project.id);
        await loadProjects();
        setNotice({ tone: 'success', message: 'Pipeline triggered.' });
      } else if (action.kind === 'delete-user') {
        await deleteAdminUser(action.user.id);
        await loadUsers();
        setNotice({ tone: 'success', message: 'User deleted.' });
      }
      setAction(null);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Action failed.');
    } finally {
      setActionPending(false);
    }
  };

  useEffect(() => {
    if (!hydrated || !isAdmin) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- kick off initial admin reads after auth hydration
    void loadProjects();
    void loadUsers();
    void loadSettings();
  }, [hydrated, isAdmin, loadProjects, loadSettings, loadUsers]);

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
          <TabButton active={tab === 'projects'} onClick={() => setTab('projects')}>
            Projects
          </TabButton>
          <TabButton active={tab === 'users'} onClick={() => setTab('users')}>
            Users
          </TabButton>
          <TabButton active={tab === 'settings'} onClick={() => setTab('settings')}>
            Settings
          </TabButton>
        </div>
      </Surface>

      {notice ? (
        <Surface
          variant="default"
          className={`px-4 py-3 text-sm ${notice.tone === 'error' ? 'text-amber-200' : 'text-emerald-200'}`}
        >
          {notice.message}
        </Surface>
      ) : null}

      {tab === 'projects' ? (
        <ProjectsTable
          projects={projects}
          loading={projectsLoading}
          error={projectsError}
          onRetry={loadProjects}
          onAction={setAction}
        />
      ) : tab === 'users' ? (
        <UsersTable
          users={users}
          loading={usersLoading}
          error={usersError}
          onRetry={loadUsers}
          onAction={setAction}
        />
      ) : (
        <SettingsPanel
          registrationEnabled={registrationEnabled}
          loading={settingsLoading}
          pending={settingsPending}
          error={settingsError}
          label={t('Admin.registrationEnabled')}
          onRetry={loadSettings}
          onToggle={() => void handleRegistrationToggle()}
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
      {action?.kind === 'change-role' ? (
        <RoleActionModal
          user={action.user}
          pending={actionPending}
          error={actionError}
          onSubmit={(role) => void submitRoleChange(role)}
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
  label,
  onRetry,
  onToggle,
}: {
  registrationEnabled: boolean;
  loading: boolean;
  pending: boolean;
  error: string;
  label: string;
  onRetry: () => void;
  onToggle: () => void;
}) {
  return (
    <Surface variant="glass" className="p-5">
      {loading ? (
        <p className="text-sm text-zinc-500">Loading...</p>
      ) : error ? (
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
      ) : (
        <label className="flex items-center justify-between gap-4">
          <span className="text-sm font-medium text-white">{label}</span>
          <input
            type="checkbox"
            checked={registrationEnabled}
            disabled={pending}
            onChange={onToggle}
            className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400"
          />
        </label>
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
  onRetry,
  onAction,
}: {
  projects: AdminProject[];
  loading: boolean;
  error: string;
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
                        onClick={() => onAction({ kind: 'rename-project', project })}
                      />
                      <IconAction
                        label="Rebuild index"
                        icon={RotateCcw}
                        onClick={() => onAction({ kind: 'rebuild-project', project })}
                      />
                      <IconAction
                        label="Trigger pipeline"
                        icon={Play}
                        onClick={() => onAction({ kind: 'trigger-project', project })}
                      />
                      <IconAction
                        label="Delete"
                        icon={Trash2}
                        danger
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
  loading,
  error,
  onRetry,
  onAction,
}: {
  users: AdminUser[];
  loading: boolean;
  error: string;
  onRetry: () => void;
  onAction: (action: Action) => void;
}) {
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
              <th className="px-4 py-3 font-medium">Role</th>
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
                    <Badge variant={user.role === 'admin' ? 'accent' : 'muted'}>{user.role}</Badge>
                  </td>
                  <td className="px-4 py-3 tabular-nums text-zinc-300">{user.projectCount}</td>
                  <td className="px-4 py-3">
                    <div className="flex justify-end gap-1">
                      <IconAction
                        label="Change role"
                        icon={Pencil}
                        onClick={() => onAction({ kind: 'change-role', user })}
                      />
                      <IconAction
                        label="Delete user"
                        icon={Trash2}
                        danger
                        onClick={() => onAction({ kind: 'delete-user', user })}
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
  onClick,
}: {
  label: string;
  icon: ComponentType<{ className?: string; 'aria-hidden'?: boolean }>;
  danger?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
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
          className="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus:border-emerald-400"
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

function RoleActionModal({
  user,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  user: AdminUser;
  pending: boolean;
  error: string;
  onSubmit: (role: string) => void;
  onClose: () => void;
}) {
  const [role, setRole] = useState(user.role === 'admin' ? 'admin' : 'user');
  return (
    <ModalFrame title="Change role" onClose={onClose}>
      <p className="mt-3 text-sm text-zinc-400">{user.email || user.id}</p>
      <select
        value={role}
        onChange={(event) => setRole(event.target.value)}
        className="mt-4 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus:border-emerald-400"
      >
        <option value="user">User</option>
        <option value="admin">Admin</option>
      </select>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel="Update role"
        pendingLabel="Updating..."
        onSubmit={() => onSubmit(role)}
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
  onSubmit: () => void;
  onClose: () => void;
}) {
  return (
    <ModalFrame title={title} onClose={onClose}>
      <p className="mt-3 text-sm leading-6 text-zinc-400">{description}</p>
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
