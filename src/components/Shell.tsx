'use client';

import Link from 'next/link';
import { LoginModal } from './LoginModal';
import { NewProjectModal } from './NewProjectModal';
import { ProjectEmptyState } from './ProjectEmptyState';
import { WorkspaceProvider, useWorkspace } from './WorkspaceProvider';

const navItems = [
  { href: '/', label: 'Search' },
  { href: '/sources', label: 'Sources' },
  { href: '/concepts', label: 'Concepts' },
  { href: '/status', label: 'Status' },
];

export function Shell({ children }: { children: React.ReactNode }) {
  return (
    <WorkspaceProvider>
      <ShellContent>{children}</ShellContent>
    </WorkspaceProvider>
  );
}

function ShellContent({ children }: { children: React.ReactNode }) {
  const {
    hydrated,
    token,
    user,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    selectProject,
    refreshProjects,
    openNewProject,
    signOut,
  } = useWorkspace();

  return (
    <div className="min-h-screen bg-[#0a0a0a] text-zinc-100">
      <header className="sticky top-0 z-30 border-b border-white/10 bg-[#0a0a0a]/90 backdrop-blur">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-3 px-4 py-3 sm:px-6">
          <Link href="/" className="mr-2 shrink-0">
            <div className="text-xs font-semibold uppercase tracking-[0.24em] text-emerald-300">
              LLM Wiki Cloud
            </div>
          </Link>

          <nav className="order-3 flex w-full items-center gap-1 overflow-x-auto text-sm text-zinc-300 md:order-none md:w-auto">
          {navItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
                className="whitespace-nowrap rounded-md px-3 py-2 font-medium transition hover:bg-white/10 hover:text-white"
            >
              {item.label}
            </Link>
          ))}
          </nav>

          {token ? (
            <div className="ml-auto flex items-center gap-2">
              <label className="sr-only" htmlFor="project-selector">
                Current project
              </label>
              <select
                id="project-selector"
                value={currentProject?.id ?? ''}
                onChange={(event) => selectProject(event.target.value)}
                disabled={projectsLoading || projects.length === 0}
                className="max-w-48 rounded-lg border border-white/10 bg-[#171717] px-3 py-2 text-sm font-medium text-white outline-none transition focus:border-emerald-300 disabled:text-zinc-500"
              >
                {projects.length === 0 ? <option value="">No projects</option> : null}
                {projects.map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </select>

              <details className="group relative">
                <summary className="flex size-9 cursor-pointer list-none items-center justify-center rounded-full bg-emerald-300 font-semibold text-black transition hover:bg-emerald-200">
                  {user?.email.slice(0, 1).toUpperCase() ?? 'U'}
                  <span className="sr-only">Open user menu</span>
                </summary>
                <div className="absolute right-0 mt-2 w-52 rounded-xl border border-white/10 bg-[#171717] p-2 shadow-2xl">
                  <div className="truncate border-b border-white/10 px-3 py-2 text-xs text-zinc-500">
                    {user?.email}
                  </div>
                  <button
                    type="button"
                    onClick={openNewProject}
                    className="mt-1 block w-full rounded-lg px-3 py-2 text-left text-sm text-zinc-200 transition hover:bg-white/10"
                  >
                    New Project
                  </button>
                  <button
                    type="button"
                    onClick={signOut}
                    className="block w-full rounded-lg px-3 py-2 text-left text-sm text-zinc-200 transition hover:bg-white/10"
                  >
                    Logout
                  </button>
                </div>
              </details>
            </div>
          ) : null}
        </div>
      </header>

      <main className="mx-auto min-h-[calc(100vh-65px)] max-w-6xl px-4 py-8 sm:px-6 lg:px-10">
        {!hydrated ? (
          <div className="flex min-h-[60vh] items-center justify-center text-sm text-zinc-500">
            Loading workspace…
          </div>
        ) : token && projectsLoading ? (
          <div className="flex min-h-[60vh] items-center justify-center text-sm text-zinc-500">
            Loading projects…
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
          children
        ) : null}
      </main>

      <LoginModal />
      <NewProjectModal />
    </div>
  );
}
