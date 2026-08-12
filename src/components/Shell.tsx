'use client';

import { useEffect, useState } from 'react';
import { usePathname } from 'next/navigation';
import { Search, FileText, Brain, Activity, Menu, X, ChevronUp, Shield, Pencil } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useAuth } from '@/lib/auth';
import { LoginModal } from './LoginModal';
import { NewProjectModal } from './NewProjectModal';
import { ProjectRenameModal } from './ProjectRenameModal';
import { ProjectEmptyState } from './ProjectEmptyState';
import { WorkspaceProvider, useWorkspace, type NavCounts } from './WorkspaceProvider';
import { Badge } from './ui/Badge';
import { ProjectSelect } from './ui/ProjectSelect';
import { CommandPalette, useCommandPalette } from './ui/CommandPalette';
import { NavigationBlockerProvider, NavigationLink } from './NavigationBlocker';

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <NavigationBlockerProvider>
      <WorkspaceProvider>
        <ShellContent>{children}</ShellContent>
      </WorkspaceProvider>
    </NavigationBlockerProvider>
  );
}

type NavCountKey = keyof NavCounts;

function ShellContent({ children }: { children: React.ReactNode }) {
  const { t } = useT();
  const pathname = usePathname();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const { user } = useAuth();
  const { open: paletteOpen, setOpen: setPaletteOpen } = useCommandPalette();

  const {
    hydrated,
    token,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    isDemoSession,
    navCounts,
    selectProject,
    renameProject,
    refreshProjects,
    openNewProject,
    signOut,
  } = useWorkspace();
  const [renameTarget, setRenameTarget] = useState<{ id: string; name: string } | null>(null);

  const navItems: {
    href: string;
    label: string;
    icon: typeof Search;
    exact?: boolean;
    countKey?: NavCountKey;
  }[] = [
    { href: '/', label: t('Shell.search'), icon: Search, exact: true },
    { href: '/sources', label: t('Shell.sources'), icon: FileText, countKey: 'sources' },
    { href: '/concepts', label: t('Shell.concepts'), icon: Brain, countKey: 'concepts' },
    { href: '/status', label: t('Shell.status'), icon: Activity },
    ...(user?.role === 'admin'
      ? [{ href: '/admin', label: 'Admin', icon: Shield }]
      : []),
  ];

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setMobileNavOpen(false);
    }, 0);
    return () => window.clearTimeout(timeout);
  }, [pathname]);

  useEffect(() => {
    if (renameTarget && renameTarget.id !== currentProject?.id) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- close a rename target when selection changes
      setRenameTarget(null);
    }
  }, [currentProject?.id, renameTarget]);

  useEffect(() => {
    if (!mobileNavOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMobileNavOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [mobileNavOpen]);

  const isActive = (href: string, exact?: boolean) => {
    if (exact) return pathname === href;
    return pathname === href || pathname.startsWith(`${href}/`);
  };
  const isAdminRoute = pathname === '/admin' || pathname.startsWith('/admin/');

  return (
    <div className="min-h-dvh text-zinc-100 lg:flex lg:items-stretch">
      {token ? (
        <header className="sticky top-0 z-50 flex min-h-14 items-center justify-between border-b border-white/8 bg-zinc-950/85 px-4 backdrop-blur lg:hidden">
          <NavigationLink href="/" className="min-w-0 text-sm font-semibold tracking-tight text-white">
            {t('Shell.brand')}
          </NavigationLink>
          <button
            type="button"
            aria-controls="mobile-navigation"
            aria-expanded={mobileNavOpen}
            aria-label={mobileNavOpen ? t('Shell.closeNavigation') : t('Shell.openNavigation')}
            onClick={() => setMobileNavOpen((open) => !open)}
            className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-white/10 bg-white/5 text-zinc-200 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
          >
            {mobileNavOpen ? (
              <X className="size-5" aria-hidden="true" />
            ) : (
              <Menu className="size-5" aria-hidden="true" />
            )}
          </button>
        </header>
      ) : null}

      {token ? (
        <button
          type="button"
          aria-label={t('Shell.closeNavigation')}
          onClick={() => setMobileNavOpen(false)}
          className={`fixed inset-0 z-30 bg-black/60 transition-opacity lg:hidden ${
            mobileNavOpen ? 'opacity-100' : 'pointer-events-none opacity-0'
          }`}
        />
      ) : null}

      {token ? (
        <aside
          id="mobile-navigation"
          className={`glass-sidebar fixed inset-y-0 left-0 z-40 flex h-dvh w-64 shrink-0 flex-col pt-14 transition-transform duration-200 lg:sticky lg:top-0 lg:translate-x-0 lg:pt-0 ${
            mobileNavOpen ? 'translate-x-0' : '-translate-x-full'
          }`}
        >
          <NavigationLink href="/" className="block px-5 py-5">
            <div className="text-sm font-semibold tracking-tight text-white">
              {t('Shell.brand')}
            </div>
            <div className="mt-0.5 text-xs text-zinc-500">{t('Shell.subtitle')}</div>
          </NavigationLink>

          <div className="flex-1 overflow-y-auto">
            <nav aria-label={t('Shell.navigation')} className="flex flex-col gap-0.5 px-3 pb-3">
              {navItems.map((item) => {
                const active = isActive(item.href, item.exact);
                const Icon = item.icon;
                const count = item.countKey ? navCounts[item.countKey] : null;
                return (
                  <NavigationLink
                    key={item.href}
                    href={item.href}
                    className={`relative flex min-h-11 items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition ${
                      active
                        ? 'bg-white/8 text-white'
                        : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
                    }`}
                  >
                    {active ? (
                      <span className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-emerald-400" />
                    ) : null}
                    <Icon className={`size-4 shrink-0 ${active ? 'text-emerald-400' : 'text-zinc-500'}`} />
                    <span className="min-w-0 flex-1 truncate">{item.label}</span>
                    {count !== null ? (
                      <Badge variant="muted" className="ml-auto shrink-0 tabular-nums">
                        {count}
                      </Badge>
                    ) : null}
                  </NavigationLink>
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
              {isAdminRoute ? null : (
                <button
                  type="button"
                  onClick={() => {
                    if (currentProject) {
                      setRenameTarget({ id: currentProject.id, name: currentProject.name });
                    }
                  }}
                  disabled={isDemoSession || !currentProject}
                  className="mt-1.5 min-h-11 w-full rounded-lg px-3 py-2.5 text-left text-sm text-zinc-500 transition hover:bg-white/5 hover:text-zinc-300 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  <span className="inline-flex items-center gap-2">
                    <Pencil className="size-4" />
                    Rename project
                  </span>
                </button>
              )}
              {!isDemoSession ? (
                <button
                  type="button"
                  onClick={openNewProject}
                  className="mt-1.5 min-h-11 w-full rounded-lg px-3 py-2.5 text-left text-sm text-zinc-500 transition hover:bg-white/5 hover:text-zinc-300"
                >
                  {t('Shell.newProject')}
                </button>
              ) : null}
            </div>
          </div>

          <div className="shrink-0 border-t border-white/8 px-3 py-3">
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
                  className="inline-flex min-h-11 items-center text-xs text-zinc-500 transition hover:text-zinc-300"
                >
                  {t('Shell.logout')}
                </button>
              </div>
              <button
                type="button"
                onClick={() => setPaletteOpen(true)}
                className="hidden min-h-11 min-w-11 items-center justify-center rounded-md border border-white/10 bg-white/5 px-2 text-[10px] text-zinc-500 transition hover:bg-white/10 hover:text-zinc-300 lg:inline-flex"
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
        ) : token && isAdminRoute ? (
          <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:px-10">
            {children}
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
      {renameTarget ? (
        <ProjectRenameModal
          project={renameTarget}
          onSubmit={async (name) => {
            await renameProject(renameTarget.id, name);
          }}
          onClose={() => setRenameTarget(null)}
        />
      ) : null}
      {token ? <ScrollToTopButton /> : null}
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
            sources: t('Shell.sources'),
            concepts: t('Shell.concepts'),
            status: t('Shell.status'),
          }}
        />
      ) : null}

    </div>
  );
}

function ScrollToTopButton() {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const updateVisibility = () => setVisible(window.scrollY > 320);
    updateVisibility();
    window.addEventListener('scroll', updateVisibility, { passive: true });
    return () => window.removeEventListener('scroll', updateVisibility);
  }, []);

  if (!visible) return null;

  return (
    <button
      type="button"
      aria-label="Scroll to top"
      onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
      className="fixed bottom-5 right-5 z-20 inline-flex size-12 items-center justify-center rounded-full bg-emerald-400 text-zinc-950 shadow-lg shadow-emerald-500/20 transition hover:-translate-y-0.5 hover:bg-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-200"
    >
      <ChevronUp className="size-5" aria-hidden="true" />
    </button>
  );
}
