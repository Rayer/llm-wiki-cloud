'use client';

import { useEffect, useState } from 'react';
import { getProjectContentContext, type ProjectContentContext } from '@/lib/projects';
import { useT } from '@/lib/i18n';

type PanelState =
  | { kind: 'loading' }
  | { kind: 'error' }
  | { kind: 'loaded'; context: ProjectContentContext };

export function ProjectContentContextPanel({ projectId }: { projectId: string }) {
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
  const isFailed = context?.status === 'failed';
  const isMissing = context?.status === 'missing' || (context?.status === 'ready' && !summary);
  const isKnownStatus = context?.status === 'pending' || isFailed || isMissing || context?.status === 'ready';

  return (
    <section
      className="rounded-xl border border-white/10 bg-black/15 p-4"
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
            <div className="mt-3 rounded-lg border border-white/10 bg-zinc-950/40 p-3">
              <h3 className="text-xs font-semibold uppercase tracking-wide text-zinc-400">
                {t('Profile.contentContext.savedSummary')}
              </h3>
              <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-200">{summary}</p>
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
