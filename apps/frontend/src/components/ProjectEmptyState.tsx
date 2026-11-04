'use client';

import { useWorkspace } from './WorkspaceProvider';
import { useT } from '@/lib/i18n';

export function ProjectEmptyState() {
  const { isDemoSession, openNewProject } = useWorkspace();
  const { t } = useT();

  return (
    <section className="mx-auto flex min-h-[65vh] max-w-2xl items-center justify-center">
      <div className="w-full rounded border border-emerald-300/20 bg-emerald-300/[0.05] p-8 text-center sm:p-12">
        <div className="mx-auto flex size-14 items-center justify-center rounded bg-emerald-300 text-2xl font-semibold text-black">
          +
        </div>
        <h1 className="mt-6 text-3xl font-semibold text-white">{t('ProjectEmptyState.title')}</h1>
        <p className="mx-auto mt-3 max-w-md leading-7 text-zinc-400">
          {t('ProjectEmptyState.guidance')}
        </p>
        {!isDemoSession ? (
          <button
            type="button"
            onClick={openNewProject}
            className="mt-7 min-h-11 rounded bg-emerald-300 px-5 py-3 font-semibold text-black transition hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
          >
            {t('ProjectEmptyState.createFirstWiki')}
          </button>
        ) : null}
      </div>
    </section>
  );
}
