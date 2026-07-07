'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { Search, FileText, Brain, Activity } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useAuth } from '@/lib/auth';
import { LoginModal } from './LoginModal';
import { NewProjectModal } from './NewProjectModal';
import { ProjectEmptyState } from './ProjectEmptyState';
import { WorkspaceProvider, useWorkspace } from './WorkspaceProvider';
import { ProjectSelect } from './ui/ProjectSelect';
import { CommandPalette, useCommandPalette } from './ui/CommandPalette';

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <WorkspaceProvider>
      <ShellContent>{children}</ShellContent>
    </WorkspaceProvider>
  );
}

function ShellContent({ children }: { children: React.ReactNode }) {
  const { t } = useT();
  const pathname = usePathname();
  const [demoMessage, setDemoMessage] = useState('');
  const { user } = useAuth();
  const { open: paletteOpen, setOpen: setPaletteOpen } = useCommandPalette();

  const navItems = [
    { href: '/', label: t('Shell.search'), icon: Search, exact: true },
    { href: '/sources', label: t('Shell.sources'), icon: FileText },
    { href: '/concepts', label: t('Shell.concepts'), icon: Brain },
    { href: '/status', label: t('Shell.status'), icon: Activity },
  ];

  const {
    hydrated,
    token,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    isDemoUser,
    selectProject,
    refreshProjects,
    openNewProject,
    signOut,
  } = useWorkspace();

  useEffect(() => {
    if (!demoMessage) return;
    const timeout = window.setTimeout(() => setDemoMessage(''), 3000);
    return () => window.clearTimeout(timeout);
  }, [demoMessage]);

  const handleNewProjectClick = () => {
    if (isDemoUser) {
      setDemoMessage(t('Shell.demoDisabled'));
      return;
    }
    openNewProject();
  };

  const isActive = (href: string, exact?: boolean) => {
    if (exact) return pathname === href;
    return pathname === href || pathname.startsWith(`${href}/`);
  };

  return (
    <div className="flex min-h-screen text-zinc-100">
      {token ? (
        <aside className="glass-sidebar flex w-64 shrink-0 flex-col">
          <Link href="/" className="block px-5 py-5">
            <div className="text-sm font-semibold tracking-tight text-white">
              {t('Shell.brand')}
            </div>
            <div className="mt-0.5 text-xs text-zinc-500">Knowledge workspace</div>
          </Link>

          <nav className="flex flex-col gap-0.5 px-3 pb-3">
            {navItems.map((item) => {
              const active = isActive(item.href, item.exact);
              const Icon = item.icon;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition ${
                    active
                      ? 'bg-white/8 text-white'
                      : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
                  }`}
                >
                  {active ? (
                    <span className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-emerald-400" />
                  ) : null}
                  <Icon className={`size-4 shrink-0 ${active ? 'text-emerald-400' : 'text-zinc-500'}`} />
                  {item.label}
                </Link>
              );
            })}
          </nav>

          <div className="mx-3 my-2 border-t border-white/8" />

          <div className="px-3 pb-2">
            <p className="px-1 pb-2 text-[10px] font-semibold uppercase tracking-wider text-zinc-600">
              {t('Shell.projects')}
            </p>
            {projectsLoading ? (
              <p className="px-1 py-2 text-xs text-zinc-500">{t('Shell.loading')}</p>
            ) : projects.length === 0 ? (
              <p className="px-1 py-2 text-xs text-zinc-500">{t('Shell.noProjects')}</p>
            ) : (
              <ProjectSelect
                projects={projects}
                value={currentProject?.id ?? ''}
                onChange={(projectId) => selectProject(projectId)}
                placeholder={t('Shell.noProjects')}
              />
            )}
            <button
              type="button"
              onClick={handleNewProjectClick}
              className="mt-1.5 w-full rounded-lg px-3 py-2 text-left text-sm text-zinc-500 transition hover:bg-white/5 hover:text-zinc-300"
            >
              {t('Shell.newProject')}
            </button>
          </div>

          <div className="mt-auto border-t border-white/8 px-3 py-3">
            <div className="mt-2 border-t border-white/10 pt-2">
              <p className="font-mono text-[10px] text-zinc-600 truncate">User: {user?.id ?? '—'}</p>
              <p className="font-mono text-[10px] text-zinc-600 truncate">Project: {currentProject?.id ?? '—'}</p>
            </div>
            <div className="mt-3 flex items-center gap-3">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-emerald-300 to-teal-400 text-xs font-semibold text-zinc-900">
                {user?.email.slice(0, 1).toUpperCase() ?? 'U'}
              </div>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-white">
                  {user?.email ?? 'User'}
                </p>
                <button
                  type="button"
                  onClick={() => void signOut()}
                  className="text-xs text-zinc-500 transition hover:text-zinc-300"
                >
                  {t('Shell.logout')}
                </button>
              </div>
              <button
                type="button"
                onClick={() => setPaletteOpen(true)}
                className="hidden rounded-md border border-white/10 bg-white/5 px-1.5 py-0.5 text-[10px] text-zinc-500 transition hover:bg-white/10 hover:text-zinc-300 sm:block"
                title="Command palette"
              >
                ⌘K
              </button>
            </div>
          </div>
        </aside>
      ) : null}

      <main className="min-w-0 flex-1">
        {!hydrated ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : token && projectsLoading ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : token && projectsError && projects.length === 0 ? (
          <div className="mx-auto mt-20 max-w-lg rounded-xl border border-red-400/20 bg-red-400/10 p-6 text-center animate-scale-in">
            <h1 className="text-xl font-semibold text-white">Projects unavailable</h1>
            <p className="mt-2 text-sm text-red-100">{projectsError}</p>
            <button
              type="button"
              onClick={() => void refreshProjects()}
              className="mt-5 rounded-lg bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-emerald-200"
            >
              Try again
            </button>
          </div>
        ) : token && projects.length === 0 ? (
          <ProjectEmptyState />
        ) : token && currentProject ? (
          <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:px-10">
            {children}
          </div>
        ) : null}
      </main>

      <LoginModal />
      <NewProjectModal />
      {paletteOpen ? (
        <CommandPalette
          open
          onClose={() => setPaletteOpen(false)}
          projects={projects}
          onSelectProject={selectProject}
          labels={{
            placeholder: t('Command.placeholder'),
            navigate: t('Command.navigate'),
            projects: t('Shell.projects'),
            search: t('Shell.search'),
          }}
        />
      ) : null}

      {demoMessage ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm animate-fade-in"
          onClick={() => setDemoMessage('')}
        >
          <div
            className="w-80 rounded-xl border border-zinc-300/20 bg-zinc-900 px-6 py-5 text-center shadow-2xl animate-scale-in"
            onClick={(e) => e.stopPropagation()}
          >
            <p className="mb-3 text-sm text-zinc-200">{demoMessage}</p>
            <button
              onClick={() => setDemoMessage('')}
              className="rounded-lg bg-white px-4 py-2 text-xs font-medium text-black transition hover:bg-emerald-200"
            >
              {t('Shell.ok')}
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}