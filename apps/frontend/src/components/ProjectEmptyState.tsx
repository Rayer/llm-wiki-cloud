'use client';

import { useWorkspace } from './WorkspaceProvider';

export function ProjectEmptyState() {
  const { isDemoSession, openNewProject } = useWorkspace();

  return (
    <section className="mx-auto flex min-h-[65vh] max-w-2xl items-center justify-center">
      <div className="w-full rounded-2xl border border-emerald-300/20 bg-emerald-300/[0.05] p-8 text-center sm:p-12">
        <div className="mx-auto flex size-14 items-center justify-center rounded-2xl bg-emerald-300 text-2xl font-semibold text-black">
          +
        </div>
        <h1 className="mt-6 text-3xl font-semibold text-white">
          Welcome to LLM Wiki Cloud
        </h1>
        <p className="mx-auto mt-3 max-w-md leading-7 text-zinc-400">
          Create your first project to start building a searchable knowledge workspace.
        </p>
        {!isDemoSession ? (
          <button
            type="button"
            onClick={openNewProject}
            className="mt-7 rounded-lg bg-emerald-300 px-5 py-3 font-semibold text-black transition hover:bg-emerald-200"
          >
            + Create Project
          </button>
        ) : null}
      </div>
    </section>
  );
}
