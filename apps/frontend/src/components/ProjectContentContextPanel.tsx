'use client';

import { useEffect, useState } from 'react';
import { getProjectContentContext, type ProjectContentContext } from '@/lib/projects';
import { useT } from '@/lib/i18n';

type PanelState =
  | { kind: 'loading' }
  | { kind: 'error' }
  | { kind: 'loaded'; context: ProjectContentContext };

export function ProjectContentContextPanel({ projectId }: { projectId: string }) {
  return <ProjectContentContextPanelSession key={projectId} projectId={projectId} />;
}

function ProjectContentContextPanelSession({ projectId }: { projectId: string }) {
  const { t } = useT();
  const [state, setState] = useState<PanelState>({ kind: 'loading' });

  useEffect(() => {
    let active = true;
    getProjectContentContext(projectId)
      .then((context) => {
        if (active) setState({ kind: 'loaded', context });
      })
      .catch(() => {
        if (active) setState({ kind: 'error' });
      });
    return () => { active = false; };
  }, [projectId]);

  const context = state.kind === 'loaded' ? state.context : null;
  const summary = context?.summary?.text.trim() || null;
  const overview = context?.summary?.overview_schema_version === 1 ? context.summary : null;
  const currentRecipe = context?.summary?.prompt_revision === 'project-summary-v2-overview'
    && context?.summary?.selection_version === 'selection-v2-source-spread-15';
  const isFailed = context?.status === 'failed';
  const isMissing = context?.status === 'missing' || (context?.status === 'ready' && !summary);
  const isKnownStatus = context?.status === 'pending' || isFailed || isMissing || context?.status === 'ready';

  return (
    <section
      className="rounded border border-white/10 bg-black/15 p-4"
      aria-labelledby={`content-context-${projectId}`}
    >
      <h2 id={`content-context-${projectId}`} className="text-sm font-semibold text-white">
        {t('Profile.contentContext.title')}
      </h2>
      <p className="mt-1 text-sm leading-6 text-zinc-400">{t('Profile.contentContext.description')}</p>

      {state.kind === 'loading' ? (
        <p role="status" className="mt-3 text-sm text-zinc-400">{t('Profile.contentContext.loading')}</p>
      ) : null}

      {state.kind === 'error' ? (
        <div className="mt-3" role="alert">
          <p className="text-sm text-red-200">{t('Profile.contentContext.loadError')}</p>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="mt-2 min-h-10 rounded-md border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/10"
          >
            {t('Profile.contentContext.reload')}
          </button>
        </div>
      ) : null}

      {context ? (
        <div className="mt-3">
          {context.status === 'pending' ? (
            <p role="status" className="text-sm text-amber-100">{t('Profile.contentContext.pending')}</p>
          ) : null}
          {isFailed ? (
            <div role="alert">
              <p className="text-sm text-red-200">{t('Profile.contentContext.failed')}</p>
              {context.error_class ? (
                <p className="mt-1 break-all text-xs text-zinc-400">
                  {t('Profile.contentContext.errorClass')} <code>{context.error_class}</code>
                </p>
              ) : null}
            </div>
          ) : null}
          {isMissing ? (
            <p role="status" className="text-sm text-zinc-400">{t('Profile.contentContext.missing')}</p>
          ) : null}
          {!isKnownStatus ? (
            <p role="alert" className="text-sm text-amber-100">
              {t('Profile.contentContext.unknownStatus', { status: context.status })}
            </p>
          ) : null}
          {summary ? (
            <div className="mt-3 rounded border border-white/10 bg-zinc-950/40 p-3">
              {overview ? <>
                <p className="text-xs font-semibold text-emerald-200">{t('Profile.contentContext.overviewTitle')}</p>
                <h3 className="mt-2 text-lg font-semibold text-white">{overview.short_title}</h3>
                <ul className="mt-2 flex flex-wrap gap-2" aria-label={t('Profile.contentContext.topicTags')}>
                  {overview.topic_tags?.map((tag) => <li key={tag} className="rounded border border-emerald-200/20 px-2 py-1 text-xs text-emerald-100">{tag}</li>)}
                </ul>
                <p className="mt-3 whitespace-pre-wrap text-sm leading-6 text-zinc-200">{summary}</p>
                <p className="mt-2 text-xs text-zinc-400">{t('Profile.contentContext.coverage', { selected: overview.selected_concepts ?? 0, total: overview.total_concepts ?? 0 })}</p>
                {(overview.title_truncated_concepts ?? 0) > 0 || (overview.body_truncated_concepts ?? 0) > 0 ? <p className="mt-1 text-xs text-amber-100">{t('Profile.contentContext.truncated', { titles: overview.title_truncated_concepts ?? 0, bodies: overview.body_truncated_concepts ?? 0 })}</p> : null}
                <details className="mt-2 text-xs text-zinc-500"><summary className="cursor-pointer">{t('Profile.contentContext.selectedConcepts')}</summary><ul className="mt-1 flex flex-wrap gap-x-3 gap-y-1">{overview.selected_slugs?.map((slug) => <li key={slug}>{slug}</li>)}</ul></details>
              </> : <>
                <h3 className="text-xs font-semibold text-zinc-400">{t('Profile.contentContext.savedSummary')}</h3>
                <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-200">{summary}</p>
                {!currentRecipe ? <p role="status" className="mt-2 text-xs text-amber-100">{t('Profile.contentContext.newOverviewPending')}</p> : null}
              </>}
            </div>
          ) : null}
          {context.stale && summary ? (
            <p className="mt-2 text-xs leading-5 text-amber-100">{t('Profile.contentContext.stale')}</p>
          ) : null}
          {context.status === 'pending' || isFailed ? (
            <button
              type="button"
              onClick={() => window.location.reload()}
              className="mt-2 min-h-10 rounded-md border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/10"
            >
              {t('Profile.contentContext.reload')}
            </button>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
