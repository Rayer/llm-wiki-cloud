'use client';

import { useEffect, useRef, useState } from 'react';
import { CheckCircle2, Circle, Loader2, XCircle } from 'lucide-react';
import {
  getBuildInfo,
  getPipelineLog,
  getStatus,
  type ApiStatus,
  type BuildInfo,
  type PipelineExecution,
} from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { useT } from '@/lib/i18n';
import { ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { getPipelineTimelineState, PIPELINE_STEPS } from '@/lib/pipeline-timeline';
import {
  beginPipelineLogRequest,
  completePipelineLogRequest,
  failPipelineLogRequest,
  getPipelineLogAvailability,
  initialPipelineLogState,
  type PipelineLogPanelState,
  type PipelineLogRequestIdentity,
} from '@/lib/status-log';

const LOG_PREVIEW_BYTES = 10 * 1024;
const LOG_PREVIEW_LINES = 50;

type StatusScope = {
  projectId: string | null;
  status: ApiStatus | null;
  loading: boolean;
  error: string;
};

export function StatusClient() {
  const { t } = useT();
  const { user } = useAuth();
  const { currentProject } = useWorkspace();
  const projectId = currentProject?.id ?? null;
  const [statusScope, setStatusScope] = useState<StatusScope>(() => ({
    projectId,
    status: null,
    loading: Boolean(projectId),
    error: '',
  }));
  const [logState, setLogState] = useState<PipelineLogPanelState>(() => initialPipelineLogState(null));
  const [showFullLog, setShowFullLog] = useState(false);
  const [showRaw, setShowRaw] = useState(false);
  const [buildInfo, setBuildInfo] = useState<BuildInfo | null>(null);
  const [buildInfoError, setBuildInfoError] = useState('');
  const statusRequestNonce = useRef(0);
  const logRequestNonce = useRef(0);
  const activeLogRequest = useRef<PipelineLogRequestIdentity | null>(null);
  const currentProjectId = useRef(projectId);
  const status = statusScope.projectId === projectId ? statusScope.status : null;
  const loading = statusScope.projectId === projectId ? statusScope.loading : Boolean(projectId);
  const error = statusScope.projectId === projectId ? statusScope.error : '';

  useEffect(() => {
    let cancelled = false;

    getBuildInfo()
      .then((info) => {
        if (!cancelled) setBuildInfo(info);
      })
      .catch((err: Error) => {
        if (!cancelled) setBuildInfoError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const requestNonce = ++statusRequestNonce.current;
    let cancelled = false;
    currentProjectId.current = projectId;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset project-scoped status when the selected project changes
    setStatusScope({ projectId, status: null, loading: Boolean(projectId), error: '' });
    setLogState(initialPipelineLogState(projectId));
    setShowFullLog(false);
    activeLogRequest.current = null;
    logRequestNonce.current += 1;

    if (!projectId) {
      return () => {
        cancelled = true;
      };
    }

    getStatus(projectId)
      .then((apiStatus) => {
        if (cancelled || requestNonce !== statusRequestNonce.current || currentProjectId.current !== projectId) return;
        setStatusScope((current) => current.projectId === projectId
          ? { ...current, status: apiStatus }
          : current);
      })
      .catch((err: Error) => {
        if (!cancelled && requestNonce === statusRequestNonce.current && currentProjectId.current === projectId) {
          setStatusScope((current) => current.projectId === projectId
            ? { ...current, error: err.message }
            : current);
        }
      })
      .finally(() => {
        if (!cancelled && requestNonce === statusRequestNonce.current && currentProjectId.current === projectId) {
          setStatusScope((current) => current.projectId === projectId
            ? { ...current, loading: false }
            : current);
        }
      });

    return () => {
      cancelled = true;
      if (currentProjectId.current === projectId) currentProjectId.current = null;
    };
  }, [projectId]);

  const onOpenLog = () => {
    const execution = status?.lastExecution;
    const availability = getPipelineLogAvailability(execution);
    const logUrl = execution?.log_url;
    if (!projectId || !availability.canOpen || !logUrl || activeLogRequest.current) return;

    const identity: PipelineLogRequestIdentity = {
      projectId,
      executionId: execution?.name ?? '',
      logUrl,
      nonce: ++logRequestNonce.current,
    };
    activeLogRequest.current = identity;
    setLogState((current) => beginPipelineLogRequest(current, identity));
    getPipelineLog(logUrl, projectId)
      .then((text) => {
        if (currentProjectId.current !== projectId) return;
        setLogState((current) => completePipelineLogRequest(current, identity, text));
      })
      .catch((err: Error) => {
        if (currentProjectId.current !== projectId) return;
        setLogState((current) => failPipelineLogRequest(current, identity, err.message));
      })
      .finally(() => {
        if (activeLogRequest.current?.nonce === identity.nonce) activeLogRequest.current = null;
      });
  };

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">
          Pipeline status
        </h1>
        <p className="mt-2 max-w-2xl text-zinc-400">
          Current corpus counts and latest pipeline execution.
        </p>
      </header>

      {loading ? <LoadingState label="Loading status" /> : null}
      {error ? <ErrorState message={error} /> : null}

      {status ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Metric label="Sources" value={status.sourcesCount} />
            <Metric label="Concepts" value={status.conceptsCount} />
            <Metric label="Raw" value={status.rawCount} />
          </div>

          <PipelineTimeline execution={status.lastExecution} />
          <PipelineLogPanel
            logState={logState}
            execution={status.lastExecution}
            onOpenLog={onOpenLog}
            showFullLog={showFullLog}
            onToggleFull={() => setShowFullLog((prev) => !prev)}
          />

        </>
      ) : null}

      <DeveloperDetails
        showRaw={showRaw}
        onToggle={() => setShowRaw((prev) => !prev)}
        userId={user?.id}
        projectId={currentProject?.id}
        status={status}
        buildInfo={buildInfo}
        buildInfoError={buildInfoError}
        t={t}
      />
    </div>
  );
}

function DeveloperDetails({
  showRaw,
  onToggle,
  userId,
  projectId,
  status,
  buildInfo,
  buildInfoError,
  t,
}: {
  showRaw: boolean;
  onToggle: () => void;
  userId?: string;
  projectId?: string;
  status: ApiStatus | null;
  buildInfo: BuildInfo | null;
  buildInfoError: string;
  t: (key: string) => string;
}) {
  return (
    <Surface variant="glass" className="p-5">
      <button
        type="button"
        onClick={onToggle}
        className="text-sm font-medium text-zinc-400 transition hover:text-white"
      >
        {showRaw ? 'Hide' : 'Show'} developer details
      </button>
      {showRaw ? (
        <div className="mt-4 space-y-4">
          <dl className="grid gap-2 text-xs sm:grid-cols-2">
            <div>
              <dt className="text-zinc-500">{t('Status.userId')}</dt>
              <dd className="mt-1 font-mono text-zinc-300">{userId ?? '—'}</dd>
            </div>
            <div>
              <dt className="text-zinc-500">{t('Status.projectId')}</dt>
              <dd className="mt-1 font-mono text-zinc-300">{projectId ?? '—'}</dd>
            </div>
            {buildInfo ? (
              <>
                <div>
                  <dt className="text-zinc-500">{t('Status.productVersion')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.product_version}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.commit')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.commit}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.branch')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.branch}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.gitTag')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.tag || '—'}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.imageTag')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.image_tag}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.cloudRunService')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.service}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('Status.cloudRunRevision')}</dt>
                  <dd className="mt-1 font-mono text-zinc-300">{buildInfo.revision}</dd>
                </div>
              </>
            ) : null}
          </dl>
          {buildInfoError ? (
            <p className="text-sm text-amber-300">{t('Status.buildInfoUnavailable')}</p>
          ) : null}
          <pre className="overflow-x-auto rounded-md bg-black/50 p-4 text-xs text-zinc-400">
            {JSON.stringify({ api: status?.raw ?? null, build: buildInfo }, null, 2)}
          </pre>
        </div>
      ) : null}
    </Surface>
  );
}

function PipelineLogPanel({
  logState,
  execution,
  onOpenLog,
  showFullLog,
  onToggleFull,
}: {
  logState: PipelineLogPanelState;
  execution?: PipelineExecution | null;
  onOpenLog: () => void;
  showFullLog: boolean;
  onToggleFull: () => void;
}) {
  const availability = getPipelineLogAvailability(execution);
  const isLoading = logState.phase === 'loading';
  const isLarge = logState.text.length > LOG_PREVIEW_BYTES;
  const visibleLog = isLarge && !showFullLog
    ? logState.text.split(/\r?\n/).slice(-LOG_PREVIEW_LINES).join('\n')
    : logState.text;
  const canOpen = availability.canOpen && ['never-opened', 'loading', 'error'].includes(logState.phase);

  return (
    <Surface variant="glass" className="p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-white">Pipeline log</h2>
        {canOpen ? (
          <button
            type="button"
            onClick={onOpenLog}
            disabled={isLoading}
            aria-busy={isLoading}
            className="text-sm font-medium text-emerald-200 transition hover:text-white"
          >
            {isLoading ? 'Loading pipeline log...' : 'Open pipeline log'}
          </button>
        ) : isLarge ? (
          <button
            type="button"
            onClick={onToggleFull}
            className="text-sm font-medium text-zinc-400 transition hover:text-white"
          >
            {showFullLog ? 'Show latest lines' : 'Show full log'}
          </button>
        ) : null}
      </div>

      {isLoading ? <p className="mt-4 text-sm text-zinc-500" role="status" aria-live="polite">Loading log...</p> : null}
      {logState.phase === 'error' ? <p className="mt-4 text-sm text-amber-300" role="alert">{logState.error}</p> : null}
      {!isLoading && logState.phase !== 'error' && !availability.canOpen && availability.message ? (
        <p className="mt-4 text-sm text-zinc-500">{availability.message}</p>
      ) : null}
      {!isLoading && logState.phase === 'loaded-empty' ? (
        <p className="mt-4 text-sm text-zinc-500" role="status" aria-live="polite">Pipeline log is empty.</p>
      ) : null}
      {logState.text ? (
        <pre className="mt-4 max-h-96 overflow-x-auto overflow-y-auto rounded-md border border-white/10 bg-black/60 p-4 font-mono text-xs leading-5 text-zinc-300">
          {visibleLog}
        </pre>
      ) : null}
    </Surface>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <Surface variant="glass" className="p-6">
      <div className="text-sm text-zinc-500">{label}</div>
      <div className="mt-2 text-4xl font-semibold tabular-nums text-white">{value}</div>
    </Surface>
  );
}

function PipelineTimeline({ execution }: { execution?: PipelineExecution | null }) {
  const execStatus = execution?.status;
  const isRunning = execStatus === 'RUNNING';
  const isSuccess = execStatus === 'SUCCEEDED';
  const isFailed = execStatus === 'FAILED';

  const { completedSteps, failedStep, stageLabel } = getPipelineTimelineState(execution);

  return (
    <Surface variant="glass" className="p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-white">Pipeline timeline</h2>
        {execStatus ? (
          <Badge variant={isFailed ? 'draft' : isSuccess ? 'published' : 'accent'}>
            {execStatus}
          </Badge>
        ) : (
          <Badge variant="muted">Idle</Badge>
        )}
      </div>

      {!execution ? (
        <p className="mt-4 text-sm text-zinc-500">No pipeline runs recorded yet.</p>
      ) : (
        <>
          <ol className="mt-6 grid gap-3 sm:grid-cols-4">
            {PIPELINE_STEPS.map((step, index) => {
              const done = index < completedSteps;
              const active = isRunning && index === completedSteps;
              return (
                <li
                  key={step}
                  className={`flex items-center gap-2 rounded-lg border px-3 py-2 text-sm capitalize ${
                    done
                      ? 'border-emerald-400/30 bg-emerald-400/10 text-emerald-200'
                      : active
                        ? 'border-emerald-400/20 bg-white/5 text-white'
                        : 'border-white/8 text-zinc-500'
                  }`}
                >
                  {done ? (
                    <CheckCircle2 className="size-4 shrink-0 text-emerald-400" />
                  ) : active ? (
                    <Loader2 className="size-4 shrink-0 animate-spin text-emerald-400" />
                  ) : isFailed && failedStep === step ? (
                    <XCircle className="size-4 shrink-0 text-amber-400" />
                  ) : (
                    <Circle className="size-4 shrink-0" />
                  )}
                  {step}
                </li>
              );
            })}
          </ol>

          {isFailed ? (
            <dl className="mt-5 grid gap-3 text-xs text-zinc-500 sm:grid-cols-2">
              <div>
                <dt className="uppercase tracking-wider">Failed stage</dt>
                <dd className="mt-1 text-zinc-300">
                  {stageLabel === 'stage unavailable' ? 'Failed — stage unavailable' : stageLabel}
                </dd>
              </div>
              {execution.diagnostic?.error_class ? (
                <div>
                  <dt className="uppercase tracking-wider">Error class</dt>
                  <dd className="mt-1 text-zinc-300">{execution.diagnostic.error_class}</dd>
                </div>
              ) : null}
              {execution.diagnostic?.detail_code ? (
                <div>
                  <dt className="uppercase tracking-wider">Detail code</dt>
                  <dd className="mt-1 text-zinc-300">{execution.diagnostic.detail_code}</dd>
                </div>
              ) : null}
              {execution.diagnostic?.child_command ? (
                <div>
                  <dt className="uppercase tracking-wider">Child command</dt>
                  <dd className="mt-1 break-words font-mono text-zinc-300">{execution.diagnostic.child_command}</dd>
                </div>
              ) : null}
              {execution.diagnostic?.exit_code != null ? (
                <div>
                  <dt className="uppercase tracking-wider">Exit code</dt>
                  <dd className="mt-1 text-zinc-300">{String(execution.diagnostic.exit_code)}</dd>
                </div>
              ) : null}
            </dl>
          ) : null}

          <dl className="mt-5 grid gap-3 text-xs text-zinc-500 sm:grid-cols-3">
            {execution.started_at ? (
              <div>
                <dt className="uppercase tracking-wider">Started</dt>
                <dd className="mt-1 text-zinc-300">{String(execution.started_at)}</dd>
              </div>
            ) : null}
            {execution.finished_at ? (
              <div>
                <dt className="uppercase tracking-wider">Finished</dt>
                <dd className="mt-1 text-zinc-300">{String(execution.finished_at)}</dd>
              </div>
            ) : null}
            {execution.duration != null ? (
              <div>
                <dt className="uppercase tracking-wider">Duration</dt>
                <dd className="mt-1 text-zinc-300">{String(execution.duration)}</dd>
              </div>
            ) : null}
          </dl>
        </>
      )}
    </Surface>
  );
}
