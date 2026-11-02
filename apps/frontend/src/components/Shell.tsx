'use client';

import { useEffect, useRef, useState } from 'react';
import Image from 'next/image';
import { usePathname } from 'next/navigation';
import { Search, FileText, Brain, Activity, Menu, X, ChevronUp, Shield, Pencil, Terminal, Settings as SettingsIcon, LogOut } from 'lucide-react';
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
import { AccountSettingsModal } from './AccountSettingsModal';
import { RequestIDPanel } from './RequestIDPanel';
import { LocaleControl } from './LocaleControl';
import { dailyBriefSignedOutLandingURL } from '@/lib/mcp-oauth';
import { getBuildInfo, type BuildInfo } from '@/lib/api';

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
    loginOpen,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    productSetupPending,
    isDemoSession,
    navCounts,
    selectProject,
    renameProject,
    refreshProjects,
    openNewProject,
    signOut,
  } = useWorkspace();
  const [renameTarget, setRenameTarget] = useState<{ id: string; name: string } | null>(null);
  const [accountSettingsOpen, setAccountSettingsOpen] = useState(false);
  const [accountSettingsFocusBYOK, setAccountSettingsFocusBYOK] = useState(false);
  const accountSettingsTriggerRef = useRef<HTMLButtonElement>(null);
  const accountSettingsWasOpenRef = useRef(false);
  const [jitAccountId, setJitAccountId] = useState<string | null>(null);
  const [buildInfo, setBuildInfo] = useState<BuildInfo | null>(null);
  const [buildInfoError, setBuildInfoError] = useState('');
  const [versionDialogOpen, setVersionDialogOpen] = useState(false);
  const [versionCopied, setVersionCopied] = useState(false);
  const [versionCopyFailed, setVersionCopyFailed] = useState(false);
  const versionButtonRef = useRef<HTMLButtonElement>(null);
  const dialogCloseRef = useRef<HTMLButtonElement>(null);
  const versionWasOpenRef = useRef(false);

  const reloadBuildInfo = async () => {
    try {
      const info = await getBuildInfo();
      setBuildInfo(info);
      setBuildInfoError('');
    } catch (error) {
      setBuildInfoError(error instanceof Error ? error.message : String(error));
    }
  };

  useEffect(() => {
    let active = true;
    getBuildInfo()
      .then((info) => {
        if (!active) return;
        setBuildInfo(info);
        setBuildInfoError('');
      })
      .catch((error: unknown) => {
        if (active) setBuildInfoError(error instanceof Error ? error.message : String(error));
      });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!versionDialogOpen) {
      if (versionWasOpenRef.current) versionButtonRef.current?.focus();
      versionWasOpenRef.current = false;
      return;
    }
    versionWasOpenRef.current = true;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setVersionDialogOpen(false);
      if (event.key === 'Tab') {
        const dialog = document.getElementById('build-info-dialog');
        const focusable = Array.from(dialog?.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], textarea, [tabindex]:not([tabindex="-1"])') ?? []);
        const first = focusable[0];
        const last = focusable.at(-1);
        if (!first || !last) event.preventDefault();
        else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    };
    window.addEventListener('keydown', onKeyDown);
    dialogCloseRef.current?.focus();
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [versionDialogOpen]);

  const versionSnapshot = buildInfo ? [
    'LLM Wiki Cloud',
    `Environment: ${buildInfo.environment}`,
    `API product version: ${buildInfo.product_version}`,
    `BFF commit: ${buildInfo.commit}`,
    `BFF revision: ${buildInfo.revision}`,
  ].join('\n') : '';

  const copyVersionSnapshot = async () => {
    if (!versionSnapshot) return;
    try {
      await navigator.clipboard.writeText(versionSnapshot);
      setVersionCopied(true);
      setVersionCopyFailed(false);
    } catch {
      setVersionCopied(false);
      setVersionCopyFailed(true);
    }
  };

  const environmentStamp = buildInfo?.environment === 'dev'
    ? 'DEV'
    : buildInfo?.environment === 'local'
      ? 'LOCAL'
      : '';

  useEffect(() => {
    if (accountSettingsWasOpenRef.current && !accountSettingsOpen) accountSettingsTriggerRef.current?.focus();
    accountSettingsWasOpenRef.current = accountSettingsOpen;
  }, [accountSettingsOpen]);

  useEffect(() => {
    const openAccountSettings = (event: Event) => {
      if (isDemoSession) return;
      const focusBYOK = (event as CustomEvent<{ focusBYOK?: boolean }>).detail?.focusBYOK === true;
      setAccountSettingsFocusBYOK(focusBYOK);
      setAccountSettingsOpen(true);
    };
    window.addEventListener('lwc-open-account-settings', openAccountSettings);
    return () => window.removeEventListener('lwc-open-account-settings', openAccountSettings);
  }, [isDemoSession]);

  useEffect(() => {
    const onGoogleJITCompleted = (event: Event) => {
      const userId = (event as CustomEvent<{ userId?: unknown }>).detail?.userId;
      if (typeof userId === 'string' && userId) setJitAccountId(userId);
      else if (user?.id) setJitAccountId(user.id);
    };
    window.addEventListener('lwc-google-jit-completed', onGoogleJITCompleted);
    return () => window.removeEventListener('lwc-google-jit-completed', onGoogleJITCompleted);
  }, [user?.id]);

  useEffect(() => {
    if (!token || !user || isDemoSession || jitAccountId !== user.id || !currentProject || currentProject.name !== 'Default Project' || renameTarget) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- consume server-owned JIT signal at the first project boundary.
    setJitAccountId(null);
    setRenameTarget({ id: currentProject.id, name: currentProject.name });
  }, [currentProject, isDemoSession, jitAccountId, renameTarget, token, user]);

  const navItems: {
    href: string;
    label: string;
    icon: typeof Search;
    exact?: boolean;
    countKey?: NavCountKey;
    beta?: boolean;
  }[] = [
    { href: '/', label: t('Shell.home'), icon: Search, exact: true },
    ...(!isDemoSession ? [{ href: '/profile', label: t('Shell.profile'), icon: Pencil }] : []),
    ...(!isDemoSession ? [{ href: '/advanced', label: t('MCPOAuth.nav'), icon: Shield, beta: true }] : []),
    ...(!isDemoSession ? [{ href: '/cli', label: t('CLILane.nav'), icon: Terminal, beta: true }] : []),
    { href: '/sources', label: t('Shell.sources'), icon: FileText, countKey: 'sources' },
    { href: '/concepts', label: t('Shell.concepts'), icon: Brain, countKey: 'concepts' },
    { href: '/status', label: t('Shell.status'), icon: Activity },
    ...(user?.management_scopes?.some((scope) => scope === 'auth:admin' || scope === 'lwc:admin')
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
  const isGoogleCompletionRoute = pathname === '/login';
  const isCLIPairingRoute = pathname === '/cli-pairing';
  const isMCPConsentRoute = pathname === '/oauth/consent';
  const dailyBriefLanding = hydrated && !token && isMCPConsentRoute
    ? dailyBriefSignedOutLandingURL()
    : null;

  return (
    <div className="min-h-dvh text-zinc-100 lg:flex lg:items-stretch">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[80] focus:rounded focus:bg-emerald-300 focus:px-4 focus:py-2 focus:text-sm focus:font-semibold focus:text-black"
      >
        {t('Shell.skipToContent')}
      </a>
      {dailyBriefLanding ? (
        <a href={dailyBriefLanding} className="fixed right-4 top-4 z-[60] rounded border border-white/20 bg-zinc-900 px-4 py-2.5 text-sm font-medium text-emerald-200 underline underline-offset-4 shadow-lg focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
          {t('MCPOAuth.returnToDailyBrief')}
        </a>
      ) : null}
      {token ? (
        <header className="sticky top-0 z-50 flex min-h-14 items-center justify-between border-b border-white/8 bg-zinc-950/85 px-4 lg:hidden">
          <NavigationLink href="/" className="min-w-0 text-sm font-semibold tracking-tight text-white">
            <span className="flex items-center gap-3"><Image src="/brand/lwc/mark-sage.svg" width={40} height={34} style={{ height: 'auto' }} alt="" />{t('Shell.brand')}{environmentStamp ? <span className={environmentStamp === 'DEV' ? 'rotate-[-8deg] border border-red-400/80 px-1.5 py-0.5 text-[10px] font-bold tracking-[0.16em] text-red-300' : 'border border-white/20 px-1.5 py-0.5 text-[10px] font-semibold tracking-[0.12em] text-zinc-300'}>{environmentStamp}</span> : null}</span>
          </NavigationLink>
          <button
            type="button"
            aria-controls="mobile-navigation"
            aria-expanded={mobileNavOpen}
            aria-label={mobileNavOpen ? t('Shell.closeNavigation') : t('Shell.openNavigation')}
            onClick={() => setMobileNavOpen((open) => !open)}
            className="inline-flex min-h-11 min-w-11 items-center justify-center rounded border border-white/10 bg-white/5 text-zinc-200 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
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
            <div className="flex items-center gap-3 text-base font-medium tracking-tight text-[#F0F3F1]">
              <Image src="/brand/lwc/mark-sage.svg" width={40} height={34} style={{ height: 'auto' }} alt="" priority />
              <span>{t('Shell.brand')}</span>
              {environmentStamp ? <span className={environmentStamp === 'DEV' ? 'rotate-[-8deg] border border-red-400/80 px-1.5 py-0.5 text-[10px] font-bold tracking-[0.16em] text-red-300' : 'border border-white/20 px-1.5 py-0.5 text-[10px] font-semibold tracking-[0.12em] text-zinc-300'}>{environmentStamp}</span> : null}
            </div>
            <div className="mt-0.5 text-xs text-zinc-400">{t('Shell.subtitle')}</div>
          </NavigationLink>

          <div className="flex-1 overflow-y-auto">
            <nav aria-label={t('Shell.navigation')} className="flex flex-col gap-0.5 px-3 pb-3">
              {navItems.map((item) => {
                const active = isActive(item.href, item.exact);
                const Icon = item.icon;
                const count = item.countKey ? navCounts[item.countKey] : null;
                const failures = item.href === '/sources' ? navCounts.documentImportFailures : null;
                return (
                  <NavigationLink
                    key={item.href}
                    href={item.href}
                    aria-label={item.beta ? `${item.label} ${t('Shell.betaBadge')}` : undefined}
                    aria-current={active ? 'page' : undefined}
                    className={`relative flex min-h-11 items-center gap-3 rounded px-3 py-2.5 text-sm font-medium transition ${
                      active
                        ? 'bg-white/8 text-white'
                        : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
                    }`}
                  >
                    <Icon className={`size-4 shrink-0 ${active ? 'text-white' : 'text-zinc-500'}`} />
                    <span className="min-w-0 flex-1 truncate">{item.label}</span>
                    {item.beta ? <Badge variant="accent" className="shrink-0 px-1.5 py-0 text-xs">{t('Shell.betaBadge')}</Badge> : null}
                    {count !== null ? (
                      <Badge variant="muted" className="ml-auto shrink-0 tabular-nums">
                        {count}
                      </Badge>
                    ) : null}
                    {failures !== null && failures > 0 ? (
                      <Badge variant="draft" className="ml-1 shrink-0 tabular-nums" aria-label={t('Source.documentImport.navBadge', { count: failures })}>
                        {failures}
                      </Badge>
                    ) : null}
                  </NavigationLink>
                );
              })}
            </nav>

            <div className="mx-3 my-2 border-t border-white/8" />

            <div className="px-3 pb-2">
              <p className="px-1 pb-2 text-xs font-semibold text-zinc-400">
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
                  searchPlaceholder={t('Shell.searchProjects')}
                  noMatches={t('Shell.noProjectMatches')}
                  onOpen={() => { void refreshProjects(); }}
                />
              )}
              {!isDemoSession ? (
                <button
                  type="button"
                  onClick={openNewProject}
                  className="mt-1.5 min-h-11 w-full rounded px-3 py-2.5 text-left text-sm text-zinc-500 transition hover:bg-white/5 hover:text-zinc-300"
                >
                  {t('Shell.newProject')}
                </button>
              ) : null}
            </div>
          </div>

          <div className="shrink-0 border-t border-white/8 px-3 py-3">
            <div className="flex items-center gap-3 px-2">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-zinc-800 text-xs font-semibold text-zinc-200">
                {user?.email.slice(0, 1).toUpperCase() ?? 'U'}
              </div>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-white">
                  {user?.email ?? 'User'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => setPaletteOpen(true)}
                className="hidden min-h-11 min-w-11 items-center justify-center rounded-md border border-white/10 bg-white/5 px-2 text-xs text-zinc-500 transition hover:bg-white/10 hover:text-zinc-300 lg:inline-flex"
                title={t('Command.title')}
              >
                ⌘K
              </button>
            </div>
            <div className="mt-2 grid gap-0.5">
              <button ref={accountSettingsTriggerRef} type="button" onClick={() => { setAccountSettingsFocusBYOK(false); setAccountSettingsOpen(true); }} className="flex min-h-11 items-center gap-3 rounded-md px-3 text-sm text-zinc-300 transition hover:bg-white/5 hover:text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
                <SettingsIcon className="size-4" aria-hidden="true" />{t('AccountSettings.title')}
              </button>
              <button type="button" onClick={() => void signOut()} className="flex min-h-11 items-center gap-3 rounded-md px-3 text-sm text-zinc-300 transition hover:bg-white/5 hover:text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
                <LogOut className="size-4" aria-hidden="true" />{t('Shell.logout')}
              </button>
              <button ref={versionButtonRef} type="button" onClick={() => { setVersionDialogOpen(true); setVersionCopied(false); setVersionCopyFailed(false); }} className="flex min-h-11 items-center justify-between gap-3 rounded-md px-3 text-left text-xs text-zinc-500 transition hover:bg-white/5 hover:text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400">
                <span>{buildInfo ? `${t('Shell.versionInfo')}: ${buildInfo.product_version}` : buildInfoError ? t('Shell.versionUnavailable') : t('Shell.loadingVersion')}</span>
                {environmentStamp ? <span className={environmentStamp === 'DEV' ? 'rotate-[-8deg] font-bold tracking-wider text-red-300' : 'font-semibold tracking-wider text-zinc-300'}>{environmentStamp}</span> : null}
              </button>
            </div>
          </div>
        </aside>
      ) : null}

      <main id="main-content" className="min-w-0 flex-1">
        <div className="lwc-utilities mx-auto max-w-6xl px-4 pt-4 sm:px-6 lg:px-10">
          {pathname === '/login' || !loginOpen ? <LocaleControl /> : null}
          <RequestIDPanel />
        </div>
        {!hydrated ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : isGoogleCompletionRoute ? (
          children
        ) : isCLIPairingRoute ? (
          <div className="mx-auto max-w-3xl px-4 py-8 sm:px-6 lg:px-10">{children}</div>
        ) : token && isAdminRoute ? (
          <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:px-10">
            {children}
          </div>
        ) : token && isMCPConsentRoute ? (
          <div className="mx-auto max-w-3xl px-4 py-8 sm:px-6 lg:px-10">
            {children}
          </div>
        ) : token && projectsLoading ? (
          <div className="flex min-h-screen items-center justify-center text-sm text-zinc-500">
            {t('Shell.loading')}
          </div>
        ) : token && projectsError && projects.length === 0 ? (
          <div className="mx-auto mt-20 max-w-lg rounded border border-red-400/20 bg-red-400/10 p-6 text-center animate-scale-in">
            <h1 className="text-xl font-semibold text-white">
              {productSetupPending ? t('Shell.workspaceSetupPending') : t('Shell.projectsUnavailable')}
            </h1>
            <p className="mt-2 text-sm text-red-100">{projectsError}</p>
            {productSetupPending ? (
              <p className="mt-2 text-sm text-red-100">{t('Shell.workspaceSetupPendingHint')}</p>
            ) : null}
            <button
              type="button"
              onClick={() => void refreshProjects()}
              className="mt-5 rounded bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-emerald-200"
            >
              {t('Shell.retry')}
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

      {pathname === '/login' ? null : <LoginModal />}
      <NewProjectModal />
      {renameTarget ? (
        <ProjectRenameModal
          project={renameTarget}
          onSubmit={async (name) => {
            await renameProject(renameTarget.id, name);
          }}
          provisional={renameTarget.name === 'Default Project'}
          onClose={() => {
            setRenameTarget(null);
          }}
        />
      ) : null}
      {accountSettingsOpen && token ? <AccountSettingsModal onClose={() => setAccountSettingsOpen(false)} focusBYOK={accountSettingsFocusBYOK} /> : null}
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
            home: t('Shell.home'),
            search: t('Shell.search'),
            sources: t('Shell.sources'),
            concepts: t('Shell.concepts'),
            status: t('Shell.status'),
            palette: t('Command.title'),
            noMatches: t('Command.noMatches'),
          }}
        />
      ) : null}

      {versionDialogOpen ? (
        <div className="fixed inset-0 z-[80] flex items-center justify-center bg-black/70 p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) setVersionDialogOpen(false); }}>
          <section id="build-info-dialog" role="dialog" aria-modal="true" aria-labelledby="build-info-title" className="relative max-h-[85dvh] w-full max-w-lg overflow-y-auto rounded border border-white/12 bg-[#191B1C] p-6 shadow-2xl">
            <button ref={dialogCloseRef} type="button" onClick={() => setVersionDialogOpen(false)} className="absolute right-4 top-4 min-h-11 min-w-11 rounded text-zinc-400 hover:bg-white/8 hover:text-white" aria-label={t('Shell.closeVersionInfo')}>×</button>
            <h2 id="build-info-title" className="pr-10 text-lg font-semibold text-[#F0F3F1]">{t('Shell.versionInfo')}</h2>
            {buildInfoError ? (
              <div role="alert" className="mt-4 rounded border border-amber-300/30 bg-amber-400/8 p-4 text-sm text-amber-100">
                <p>{t('Shell.versionUnavailable')}</p>
                <button type="button" onClick={() => void reloadBuildInfo()} className="mt-3 min-h-11 rounded border border-amber-200/40 px-3">{t('Shell.retryVersionInfo')}</button>
              </div>
            ) : buildInfo ? (
              <>
                <dl className="mt-5 grid gap-4 text-sm sm:grid-cols-2">
                  <div><dt className="text-xs text-zinc-500">{t('Shell.environment')}</dt><dd className="mt-1 text-zinc-100">{buildInfo.environment === 'unknown' ? t('Shell.environmentNotProvided') : buildInfo.environment}</dd></div>
                  <div><dt className="text-xs text-zinc-500">{t('Shell.productVersion')}</dt><dd className="mt-1 text-zinc-100">{buildInfo.product_version}</dd></div>
                  <div className="sm:col-span-2"><dt className="text-xs text-zinc-500">{t('Shell.commit')}</dt><dd className="mt-1 break-all font-mono text-xs text-zinc-200">{buildInfo.commit}</dd></div>
                  <div className="sm:col-span-2"><dt className="text-xs text-zinc-500">{t('Shell.revision')}</dt><dd className="mt-1 break-all font-mono text-xs text-zinc-200">{buildInfo.revision}</dd></div>
                </dl>
                <details className="mt-4 text-sm text-zinc-400">
                  <summary className="min-h-11 cursor-pointer py-2">{t('Shell.additionalBuildDetails')}</summary>
                  <dl className="grid gap-2 pb-3 sm:grid-cols-2"><div><dt>{t('Shell.branch')}</dt><dd className="break-all">{buildInfo.branch}</dd></div><div><dt>{t('Shell.tag')}</dt><dd className="break-all">{buildInfo.tag || t('Common.notProvided')}</dd></div><div><dt>{t('Shell.imageTag')}</dt><dd className="break-all">{buildInfo.image_tag}</dd></div><div><dt>{t('Shell.service')}</dt><dd className="break-all">{buildInfo.service}</dd></div></dl>
                </details>
                <p className="mt-3 text-xs leading-5 text-zinc-500">{t('Shell.buildSourceNote')}</p>
                <button type="button" onClick={() => void copyVersionSnapshot()} className="mt-4 min-h-11 rounded bg-[#DCE9E1] px-4 text-sm font-semibold text-[#17231D]">{versionCopied ? t('Shell.versionCopied') : t('Shell.copyVersionInfo')}</button>
                {versionCopyFailed ? <p role="status" className="mt-2 text-sm text-amber-100">{t('Shell.versionCopyFailed')}</p> : null}
                {versionCopyFailed ? <textarea readOnly value={versionSnapshot} aria-label={t('Shell.versionInfo')} onFocus={(event) => event.currentTarget.select()} className="mt-2 min-h-24 w-full rounded border border-white/10 bg-black/20 p-2 font-mono text-xs text-zinc-200" /> : null}
              </>
            ) : <p className="mt-5 text-sm text-zinc-400">{t('Shell.loadingVersion')}</p>}
          </section>
        </div>
      ) : null}

    </div>
  );
}

function ScrollToTopButton() {
  const { t } = useT();
  const [visible, setVisible] = useState(false);
  const marker = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const observer = new IntersectionObserver(([entry]) => setVisible(!entry.isIntersecting && entry.boundingClientRect.top < 0));
    if (marker.current) observer.observe(marker.current);
    return () => observer.disconnect();
  }, []);

  return (
    <>
    <span ref={marker} aria-hidden="true" className="pointer-events-none absolute left-0 top-[320px] h-px w-px" />
    {visible ? (
    <button
      type="button"
      aria-label={t('Shell.scrollToTop')}
      onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
      className="fixed bottom-5 right-5 z-20 inline-flex size-12 items-center justify-center rounded-full bg-emerald-400 text-zinc-950 transition hover:-translate-y-0.5 hover:bg-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-200"
    >
      <ChevronUp className="size-5" aria-hidden="true" />
    </button>
    ) : null}
    </>
  );
}
