'use client';

import { useEffect, useState } from 'react';
import { CheckCircle2, Circle, Loader2, XCircle } from 'lucide-react';
import { getPipelineStatus, getStatus, type ApiStatus, type PipelineStatus } from '@/lib/api';
import { ErrorState, LoadingState } from './States';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';

const PIPELINE_STEPS = ['ingest', 'compile', 'lint', 'publish'] as const;

export function StatusClient() {
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [pipeline, setPipeline] = useState<PipelineStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showRaw, setShowRaw] = useState(false);

  useEffect(() => {
    Promise.all([getStatus(), getPipelineStatus()])
      .then(([apiStatus, pipelineStatus]) => {
        setStatus(apiStatus);
        setPipeline(pipelineStatus);
      })
      .catch((err: Error) => setError(err.message))
      .finally(() => setLoading(false));
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
          </div>

          <PipelineTimeline pipeline={pipeline} />

          <Surface variant="glass" className="p-5">
            <button
              type="button"
              onClick={() => setShowRaw((prev) => !prev)}
              className="text-sm font-medium text-zinc-400 transition hover:text-white"
            >
              {showRaw ? 'Hide' : 'Show'} developer details
            </button>
            {showRaw ? (
              <pre className="mt-4 overflow-x-auto rounded-md bg-black/50 p-4 text-xs text-zinc-400">
                {JSON.stringify({ api: status.raw, pipeline }, null, 2)}
              </pre>
            ) : null}
          </Surface>
        </>
      ) : null}
    </div>
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

function PipelineTimeline({ pipeline }: { pipeline: PipelineStatus | null }) {
  const execution = pipeline?.last_execution;
  const execStatus = execution?.status;
  const isRunning = execStatus === 'running';
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