'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { useT } from '@/lib/i18n';
import { LoginModal } from './LoginModal';
import { NewProjectModal } from './NewProjectModal';
import { ProjectEmptyState } from './ProjectEmptyState';
import { WorkspaceProvider, useWorkspace } from './WorkspaceProvider';

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <WorkspaceProvider>
      <ShellContent>{children}</ShellContent>
    </WorkspaceProvider>
  );
}

function ShellContent({ children }: { children: React.ReactNode }) {
  const { t } = useT();
  const [demoMessage, setDemoMessage] = useState('');
  const navItems = [
    { href: '/', label: t('Shell.search') },
    { href: '/sources', label: t('Shell.sources') },
    { href: '/concepts', label: t('Shell.concepts') },
    { href: '/status', label: t('Shell.status') },
  ];
  const {
    hydrated,
    token,
    user,
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

  return (
    <div className="flex min-h-screen bg-[#0a0a0a] text-zinc-100">
      {/* Sidebar */}
      {token ? (
        <aside className="flex w-64 shrink-0 flex-col border-r border-white/10 bg-[#0d0d0d]">
          <Link href="/" className="block px-5 py-4">
            <div className="text-xs font-semibold uppercase tracking-[0.24em] text-emerald-300">
              LLM Wiki Cloud
            </div>
          </Link>

          <nav className="flex flex-col gap-1 px-3 pb-3">
            {navItems.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                className="rounded-lg px-3 py-2 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white"
              >
                {item.label}
              </Link>
            ))}
          </nav>

          <div className="mx-3 my-2 border-t border-white/10" />

          <div className="px-3 pb-2">
            <p className="px-1 pb-1 text-[10px] font-semibold uppercase tracking-wider text-zinc-600">
              {t('Shell.projects')}
            </p>
            {projectsLoading ? (
              <p className="px-3 py-2 text-xs text-zinc-500">{t('Shell.loading')}</p>
            ) : projects.length === 0 ? (
              <p className="px-3 py-2 text-xs text-zinc-500">{t('Shell.noProjects')}</p>
            ) : (
              <select
                value={currentProject?.id ?? ''}
                onChange={(event) => selectProject(event.target.value)}
                className="w-full rounded-lg border border-white/10 bg-[#151515] px-3 py-2 text-sm font-medium text-zinc-100 outline-none transition hover:bg-white/5 focus:border-emerald-300"
              >
                {projects.map((project) => (
                  <option key={project.id} value={project.id} className="bg-zinc-900 text-zinc-100">
                    {project.name}
                  </option>
                ))}
              </select>
            )}
            <button
              type="button"
              onClick={handleNewProjectClick}
              className="mt-1 w-full rounded-lg px-3 py-2 text-left text-sm text-zinc-500 transition hover:bg-white/10 hover:text-zinc-300"
            >
              {t('Shell.newProject')}
            </button>
          </div>

          <div className="mt-auto border-t border-white/10 px-3 py-3">
            <div className="flex items-center gap-3">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-emerald-300 text-xs font-semibold text-black">
                {user?.email.slice(0, 1).toUpperCase() ?? 'U'}
              </div>
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-white">
                  {user?.email ?? 'User'}
                </p>
                <button
                  type="button"
                  onClick={() => void signOut()}
                  className="text-xs text-zinc-500 hover:text-zinc-300"
                >
                  {t('Shell.logout')}
                </button>
              </div>
            </div>
          </div>
        </aside>
      ) : null}

      {/* Main content */}
      <main className="flex-1 min-w-0">
        {!hydrated ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : token && projectsLoading ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : token && projectsError && projects.length === 0 ? (
          <div className="mx-auto mt-20 max-w-lg rounded-xl border border-red-400/20 bg-red-400/10 p-6 text-center">
            <h1 className="text-xl font-semibold text-white">Projects unavailable</h1>
            <p className="mt-2 text-sm text-red-100">{projectsError}</p>
            <button
              type="button"
              onClick={() => void refreshProjects()}
              className="mt-5 rounded-lg bg-white px-4 py-2 text-sm font-semibold text-black"
            >
              Try again
            </button>
          </div>
        ) : token && projects.length === 0 ? (
          <ProjectEmptyState />
        ) : token && currentProject ? (
          <div className="px-4 py-8 sm:px-6 lg:px-10 max-w-6xl mx-auto">
            {children}
          </div>
        ) : null}
      </main>

      <LoginModal />
      <NewProjectModal />
      {demoMessage ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm" onClick={() => setDemoMessage('')}>
          <div className="rounded-xl border border-zinc-300/20 bg-zinc-900 px-6 py-5 shadow-2xl w-80 text-center" onClick={(e) => e.stopPropagation()}>
            <p className="text-sm text-zinc-200 mb-3">{demoMessage}</p>
            <button onClick={() => setDemoMessage('')} className="rounded-lg bg-white px-4 py-2 text-xs font-medium text-black hover:bg-emerald-200 transition">{t('Shell.ok')}</button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
