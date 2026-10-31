'use client';

import { useEffect, useRef, useState } from 'react';
import { getProjectPipelineAutomation, updateProjectPipelineAutomation, type ProjectPipelineAutomation } from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { useT } from '@/lib/i18n';
import { Surface } from './ui/Surface';

type State = { settings: ProjectPipelineAutomation | null; error: string; loading: boolean; updating: boolean };

function statusKey(status: string): string {
  const normalized = status.toLowerCase();
  if (['pending', 'preparing', 'ready', 'invoking', 'invocation_unknown', 'unknown'].includes(normalized)) return 'pending';
  if (['running', 'compiling'].includes(normalized)) return 'running';
  if (['succeeded', 'published'].includes(normalized)) return 'succeeded';
  if (normalized === 'failed') return 'failed';
  if (normalized === 'cancelled') return 'cancelled';
  if (normalized === 'not_started_disabled') return 'disabled';
  return 'idle';
}

export function ProjectPipelineAutomationSettings({ projectId }: { projectId: string }) {
  const { t } = useT();
  const { isDemoSession } = useAuth();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<State>({ settings: null, error: '', loading: true, updating: false });
  const projectIdRef = useRef(projectId);
  projectIdRef.current = projectId;

  useEffect(() => {
    let active = true;
    setState({ settings: null, error: '', loading: true, updating: false });
    getProjectPipelineAutomation(projectId).then((settings) => {
      if (active) setState({ settings, error: '', loading: false, updating: false });
    }).catch((error: unknown) => {
      if (active) setState({ settings: null, error: error instanceof Error ? error.message : t('Pipeline.automationStatusUnavailable'), loading: false, updating: false });
    });
    return () => { active = false; };
  }, [attempt, projectId, t]);

  async function toggleAutomation() {
    const settings = state.settings;
    if (!settings || state.updating || isDemoSession) return;
    const scope = projectId;
    setState((current) => ({ ...current, error: '', updating: true }));
    try {
      const next = await updateProjectPipelineAutomation(scope, !settings.enabled, settings.revision);
      if (projectIdRef.current === scope) setState({ settings: next, error: '', loading: false, updating: false });
    } catch (error) {
      if (projectIdRef.current === scope) setState((current) => ({
        ...current,
        error: error instanceof Error ? error.message : t('Pipeline.automationUpdateFailed'),
        updating: false,
      }));
    }
  }

  const settings = state.settings;

  return (
    <Surface id="project-pipeline-automation" tabIndex={-1} variant="glass" as="section" className="p-5" aria-labelledby="project-automation-settings-title" data-testid="project-automation-settings">
      <h3 id="project-automation-settings-title" className="text-lg font-semibold text-white">{t('Pipeline.automationTitle')}</h3>
      <p className="mt-1 text-sm leading-6 text-zinc-400">{t('Pipeline.automationDescription')}</p>
      {state.loading ? <p role="status" className="mt-3 text-sm text-zinc-400">{t('Pipeline.automationLoading')}</p> : null}
      {state.error ? (
        <div className="mt-3" role="alert">
          <p className="text-sm text-red-200">{t('Pipeline.automationStatusUnavailable')}</p>
          <button type="button" onClick={() => setAttempt((current) => current + 1)} className="mt-2 min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5">
            {t('Pipeline.automationRetry')}
          </button>
        </div>
      ) : null}
      {settings ? (
        <>
          <label className="mt-4 flex min-h-11 max-w-lg items-center justify-between gap-3 rounded-md border border-white/10 px-3 py-2 text-sm text-zinc-200">
            <span>{t('Pipeline.automationProjectToggle')}</span>
            <input
              type="checkbox"
              checked={settings.enabled}
              disabled={state.updating || isDemoSession}
              onChange={() => void toggleAutomation()}
              className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400"
              data-testid="pipeline-automation-project-toggle"
            />
          </label>
          {state.updating ? <p role="status" className="mt-2 text-sm text-zinc-400">{t('Pipeline.automationSaving')}</p> : null}
          {!settings.admin_allowed ? (
            <p className="mt-3 rounded-md border border-amber-200/20 bg-amber-100/5 p-3 text-sm leading-6 text-amber-100" role="note" data-testid="pipeline-automation-blocked">
              {t('Pipeline.automationAdminDisabled')}
            </p>
          ) : null}
          {!settings.enabled && settings.admin_allowed ? (
            <p className="mt-3 rounded-md border border-amber-200/20 bg-amber-100/5 p-3 text-sm leading-6 text-amber-100" role="status">
              {t('Pipeline.automationProjectDisabled')}
            </p>
          ) : null}
          <p className="mt-3 text-sm text-zinc-300" role="status" aria-live="polite">
            {t('Pipeline.automationStatus')}: {t(`Pipeline.automationState.${statusKey(settings.pipeline_status)}`)}
          </p>
          <details className="mt-2 text-xs text-zinc-500">
            <summary className="w-fit cursor-pointer">{t('Pipeline.automationDiagnostics')}</summary>
            <div className="mt-1 space-y-1">
              <p>{t('Pipeline.automationSourceRevision')}: {settings.source_revision} · {t('Pipeline.automationProcessedRevision')}: {settings.processed_revision}</p>
              {(settings.failure_stage || settings.failure_code) ? <code className="block break-all">{[settings.failure_stage, settings.failure_code].filter(Boolean).join(' · ')}</code> : null}
            </div>
          </details>
          <p className="mt-3 text-xs leading-5 text-zinc-400">{t('Pipeline.automationManualNote')}</p>
        </>
      ) : null}
    </Surface>
  );
}
