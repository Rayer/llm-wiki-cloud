'use client';

import { useEffect, useState } from 'react';
import { CheckCircle2, Circle, Loader2, XCircle } from 'lucide-react';
import {
  getPipelineLog,
  getStatus,
  type ApiStatus,
  type PipelineExecution,
} from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { useT } from '@/lib/i18n';
import { ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';

const PIPELINE_STEPS = ['ingest', 'compile', 'lint', 'publish'] as const;
const LOG_PREVIEW_BYTES = 10 * 1024;
const LOG_PREVIEW_LINES = 50;

export function StatusClient() {
  const { t } = useT();
  const { user } = useAuth();
  const { currentProject } = useWorkspace();
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [pipelineLog, setPipelineLog] = useState('');
  const [logLoading, setLogLoading] = useState(false);
  const [logError, setLogError] = useState('');
  const [showFullLog, setShowFullLog] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showRaw, setShowRaw] = useState(false);

  useEffect(() => {
    let cancelled = false;

    getStatus()
      .then((apiStatus) => {
        if (cancelled) return;
        setStatus(apiStatus);

        const logUrl = apiStatus.lastExecution?.log_url;
        if (!logUrl) return;

        setLogLoading(true);
        getPipelineLog(logUrl)
          .then((logText) => {
            if (!cancelled) setPipelineLog(logText);
          })
          .catch((err: Error) => {
            if (!cancelled) setLogError(err.message);
          })
          .finally(() => {
            if (!cancelled) setLogLoading(false);
          });
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, []);

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
            logText={pipelineLog}
            loading={logLoading}
            error={logError}
            showFullLog={showFullLog}
            onToggleFull={() => setShowFullLog((prev) => !prev)}
          />

          <Surface variant="glass" className="p-5">
            <button
              type="button"
              onClick={() => setShowRaw((prev) => !prev)}
              className="text-sm font-medium text-zinc-400 transition hover:text-white"
            >
              {showRaw ? 'Hide' : 'Show'} developer details
            </button>
            {showRaw ? (
              <div className="mt-4 space-y-4">
                <dl className="grid gap-2 text-xs sm:grid-cols-2">
                  <div>
                    <dt className="text-zinc-500">{t('Status.userId')}</dt>
                    <dd className="mt-1 font-mono text-zinc-300">{user?.id ?? '—'}</dd>
                  </div>
                  <div>
                    <dt className="text-zinc-500">{t('Status.projectId')}</dt>
                    <dd className="mt-1 font-mono text-zinc-300">{currentProject?.id ?? '—'}</dd>
                  </div>
                </dl>
                <pre className="overflow-x-auto rounded-md bg-black/50 p-4 text-xs text-zinc-400">
                  {JSON.stringify({ api: status.raw }, null, 2)}
                </pre>
              </div>
            ) : null}
          </Surface>
        </>
      ) : null}
    </div>
  );
}

function PipelineLogPanel({
  logText,
  loading,
  error,
  showFullLog,
  onToggleFull,
}: {
  logText: string;
  loading: boolean;
  error: string;
  showFullLog: boolean;
  onToggleFull: () => void;
}) {
  const isLarge = logText.length > LOG_PREVIEW_BYTES;
  const visibleLog = isLarge && !showFullLog
    ? logText.split(/\r?\n/).slice(-LOG_PREVIEW_LINES).join('\n')
    : logText;

  return (
    <Surface variant="glass" className="p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-white">Pipeline log</h2>
        {isLarge ? (
          <button
            type="button"
            onClick={onToggleFull}
            className="text-sm font-medium text-zinc-400 transition hover:text-white"
          >
            {showFullLog ? 'Show latest lines' : 'Show full log'}
          </button>
        ) : null}
      </div>

      {loading ? <p className="mt-4 text-sm text-zinc-500">Loading log...</p> : null}
      {error ? <p className="mt-4 text-sm text-amber-300">{error}</p> : null}
      {!loading && !error && !logText ? (
        <p className="mt-4 text-sm text-zinc-500">No pipeline log available.</p>
      ) : null}
      {logText ? (
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

  const completedSteps = isSuccess
    ? PIPELINE_STEPS.length
    : isFailed
      ? 2
      : isRunning
        ? 1
        : 0;

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
                  ) : isFailed && index === completedSteps ? (
                    <XCircle className="size-4 shrink-0 text-amber-400" />
                  ) : (
                    <Circle className="size-4 shrink-0" />
                  )}
                  {step}
                </li>
              );
            })}
          </ol>

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
