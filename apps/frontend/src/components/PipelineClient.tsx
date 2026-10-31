'use client';

import { FormEvent, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Link2, Cog, Loader2 } from 'lucide-react';
import { Surface } from './ui/Surface';
import { Badge } from './ui/Badge';
import { ExportPanel } from './ExportPanel';
import {
  ApiError,
  getRecompileAllCapability,
  getPipelineStatus,
  getProjectPipelineAutomation,
  retryPipeline,
  scrapeUrl,
  acceptScrapeCandidate,
  rejectScrapeCandidate,
  resolveScrapeCandidate,
  recompileAll,
  triggerPipeline,
  type RecompileAllCapability,
  type ScrapeResult,
  type PipelineQuota,
  type PipelineIntentStatus,
  type PipelineResult,
  type PipelineStatus,
  type ProjectPipelineAutomation,
} from '@/lib/api';
import {
  blockReasonMessage,
  formatQuotaLine,
  isRunBlocked,
} from '@/lib/pipeline-quota';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { RawUploadPanel, type RawUploadsSettled } from './RawUploadPanel';
import { UrlPreviewDialog } from './UrlPreviewDialog';
import { NavigationLink } from './NavigationBlocker';

type Toast = {
  id: number;
  message: string;
  type: 'success' | 'error' | 'info';
};

type PipelineProjectContext = { projectId: string | null; generation: number };
type PipelineRequestScope = { projectId: string; contextGeneration: number; requestGeneration: number };
type PipelineAutomationPollScope = Pick<PipelineRequestScope, 'projectId' | 'contextGeneration'>;
type PipelineExecutionOwner = { projectId: string; contextGeneration: number };

function scrapeErrorMessageKey(error: unknown): 'Pipeline.urlUncertain' | 'Pipeline.urlUnavailable' {
  return error instanceof ApiError && (error.status === 0 || (error.status >= 500 && error.status < 600))
    ? 'Pipeline.urlUncertain'
    : 'Pipeline.urlUnavailable';
}

function isSamePipelineRequestScope(a: PipelineRequestScope, b: PipelineRequestScope): boolean {
  return a.projectId === b.projectId
    && a.contextGeneration === b.contextGeneration
    && a.requestGeneration === b.requestGeneration;
}

let toastId = 0;

function automationCopy(t: (key: string) => string, key: string, fallback: string): string {
  const translated = t(key);
  return translated === key ? fallback : translated;
}

function automationStatusKey(status: string): string {
  const normalized = status.toLowerCase();
  if (['pending', 'preparing', 'ready', 'invoking', 'invocation_unknown', 'unknown'].includes(normalized)) return 'pending';
  if (['running', 'compiling'].includes(normalized)) return 'running';
  if (['succeeded', 'published'].includes(normalized)) return 'succeeded';
  if (normalized === 'failed') return 'failed';
  if (normalized === 'cancelled') return 'cancelled';
  if (normalized === 'not_started_disabled') return 'disabled';
  return 'idle';
}

function pipelineStatusBadge(status: PipelineStatus | null): string | null {
  const executionStatus = status?.last_execution?.status;
  const duration = status?.last_execution?.duration ?? 'pending';

  if (executionStatus === 'RUNNING') return 'Pipeline running...';
  if (executionStatus === 'SUCCEEDED') return `Pipeline complete (${duration})`;
  if (executionStatus === 'FAILED') return `Pipeline failed (${duration})`;

  return null;
}

function isCooldownClear(quota: PipelineQuota | null | undefined, now = new Date()): boolean {
  if (!quota?.enforced) return true;
  if (!quota.cooldown_until) return true;
  const ms = new Date(quota.cooldown_until).getTime() - now.getTime();
  return Number.isNaN(ms) || ms <= 0;
}

function isUnderDailyLimit(quota: PipelineQuota | null | undefined): boolean {
  if (!quota?.enforced) return true;
  return quota.runs_today < quota.daily_limit;
}

function hasNewRaw(quota: PipelineQuota | null | undefined): boolean {
  if (!quota?.enforced) return true;
  return quota.new_raw_files >= quota.min_new_raw;
}

function isPendingPipelineIntent(intent: PipelineIntentStatus | null | undefined): intent is PipelineIntentStatus {
  return Boolean(intent && ['preparing', 'ready', 'invoking', 'invocation_unknown', 'compiling'].includes(intent.status));
}

function isPipelineAutomationInFlight(settings: ProjectPipelineAutomation | null): boolean {
  if (!settings?.effective_enabled || ['failed', 'cancelled'].includes(settings.pipeline_status)) return false;
  return settings.backlog_scan_status === 'pending'
    || ['pending', 'preparing', 'running', 'unknown'].includes(settings.pipeline_status)
    || settings.source_revision > settings.processed_revision;
}

export function PipelineClient() {
  const { t, locale } = useT();
  const { isDemoSession, currentProject, token } = useWorkspace();
  const projectId = currentProject?.id ?? null;
  const pipelineProjectContextRef = useRef<PipelineProjectContext>({ projectId, generation: 0 });
  const scrapeRequestGenerationRef = useRef(0);
  const [scrapeLoading, setScrapeLoading] = useState(false);
  const [scrapeError, setScrapeError] = useState<string | null>(null);
  const [scrapeResult, setScrapeResult] = useState<ScrapeResult | null>(null);
  const [scrapeModalOpen, setScrapeModalOpen] = useState(false);
  const [pipelineStatusReadFailedProjectId, setPipelineStatusReadFailedProjectId] = useState<string | null>(null);
  const [pipelineAutomation, setPipelineAutomation] = useState<ProjectPipelineAutomation | null>(null);
  const [pipelineAutomationError, setPipelineAutomationError] = useState<string | null>(null);
  const pipelineExecutionOwnerRef = useRef<PipelineExecutionOwner | null>(null);
  const pipelineTerminalPollScopeRef = useRef<PipelineRequestScope | null>(null);
  const pipelinePollErrorNotifiedRef = useRef(false);
  useLayoutEffect(() => {
    if (pipelineProjectContextRef.current.projectId !== projectId) {
      pipelineProjectContextRef.current = {
        projectId,
        generation: pipelineProjectContextRef.current.generation + 1,
      };
      pipelineExecutionOwnerRef.current = null;
      setPipelineStatusReadFailedProjectId(null);
      setPipelineAutomation(null);
      setPipelineAutomationError(null);
      scrapeRequestGenerationRef.current += 1;
      setScrapeLoading(false);
      setScrapeError(null);
      setScrapeResult(null);
      setScrapeModalOpen(false);
    }
  }, [projectId]);
  const pipelineRequestGenerationRef = useRef(0);
  const pipelineMountedRef = useRef(false);
  const isPipelineRequestCurrent = useCallback((scope: PipelineRequestScope) => {
    const latest = pipelineProjectContextRef.current;
    return pipelineMountedRef.current
      && latest.projectId === scope.projectId
      && latest.generation === scope.contextGeneration
      && pipelineRequestGenerationRef.current === scope.requestGeneration;
  }, []);
  const [scrapeUrlText, setScrapeUrlText] = useState('');
  const [pipelineResult, setPipelineResult] = useState<PipelineResult | null>(null);
  const [pipelineStatus, setPipelineStatus] = useState<PipelineStatus | null>(null);
  const [compileMode, setCompileMode] = useState<'incremental' | 'full'>('incremental');
  const [fullCapability, setFullCapability] = useState<RecompileAllCapability | null>(null);
  const [loading, setLoading] = useState<string | null>(null); // 'pipeline'
  const [retryingPipeline, setRetryingPipeline] = useState(false);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [showPrereq, setShowPrereq] = useState(false);
  const refreshPipelineStatus = useCallback(async (scope: PipelineRequestScope) => {
    if (!isPipelineRequestCurrent(scope)) return;
    try {
      const status = await getPipelineStatus(scope.projectId);
      const latest = pipelineProjectContextRef.current;
      if (
        !pipelineMountedRef.current
        || latest.projectId !== scope.projectId
        || latest.generation !== scope.contextGeneration
        || status.project_id !== scope.projectId
      ) return;
      setPipelineStatusReadFailedProjectId(null);
      const executionOwner = pipelineExecutionOwnerRef.current;
      if (
        executionOwner?.projectId === scope.projectId
        && executionOwner.contextGeneration === scope.contextGeneration
      ) {
        // Generic refreshes can update quota, but only execution-scoped polls own lifecycle evidence.
        setPipelineStatus((current) => current?.project_id === scope.projectId
          ? {
              ...current,
              ...(status.quota !== undefined ? { quota: status.quota } : {}),
              ...(status.full_quota !== undefined ? { full_quota: status.full_quota } : {}),
              ...(status.scheduled_pipeline !== undefined ? { scheduled_pipeline: status.scheduled_pipeline } : {}),
              ...(status.suggested_queries !== undefined ? { suggested_queries: status.suggested_queries } : {}),
            }
          : current);
        return;
      }
      if (pipelineRequestGenerationRef.current !== scope.requestGeneration) return;
      setPipelineStatus(status);
    } catch {
      // Keep the current status when a best-effort refresh fails.
    }
  }, [isPipelineRequestCurrent]);

  const refreshPipelineAutomation = useCallback(async (scope: PipelineAutomationPollScope): Promise<ProjectPipelineAutomation | null> => {
    try {
      const settings = await getProjectPipelineAutomation(scope.projectId);
      const latest = pipelineProjectContextRef.current;
      if (!pipelineMountedRef.current || latest.projectId !== scope.projectId || latest.generation !== scope.contextGeneration) return null;
      setPipelineAutomation(settings);
      setPipelineAutomationError(null);
      return settings;
    } catch (error) {
      const latest = pipelineProjectContextRef.current;
      if (!pipelineMountedRef.current || latest.projectId !== scope.projectId || latest.generation !== scope.contextGeneration) return null;
      setPipelineAutomationError(error instanceof Error ? error.message : automationCopy(t, 'Pipeline.automationStatusUnavailable', 'Automatic processing status is unavailable.'));
      return null;
    }
  }, [setPipelineAutomation, setPipelineAutomationError, t]);

  const pipelinePollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pipelineAutomationPollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pipelineAutomationPollScopeRef = useRef<PipelineAutomationPollScope | null>(null);
  const pipelineAutomationPollInFlightRef = useRef(false);
  const pipelineAutomationPollTickRef = useRef<(scope: PipelineAutomationPollScope) => void>(() => {});

  const addToast = useCallback((message: string, type: Toast['type']) => {
    const id = ++toastId;
    setToasts((prev) => [...prev, { id, message, type }]);
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 4000);
  }, []);

  const stopPipelinePolling = useCallback(() => {
    if (pipelinePollRef.current) {
      clearInterval(pipelinePollRef.current);
      pipelinePollRef.current = null;
    }
  }, []);

  const stopPipelineAutomationPolling = useCallback(() => {
    if (pipelineAutomationPollRef.current) {
      clearInterval(pipelineAutomationPollRef.current);
      pipelineAutomationPollRef.current = null;
    }
    pipelineAutomationPollScopeRef.current = null;
  }, []);

  useEffect(() => {
    pipelineMountedRef.current = true;
    return () => {
      pipelineMountedRef.current = false;
      pipelineRequestGenerationRef.current += 1;
      stopPipelinePolling();
      stopPipelineAutomationPolling();
    };
  }, [stopPipelineAutomationPolling, stopPipelinePolling]);

  const pollPipelineStatus = useCallback(async (scope: PipelineRequestScope, executionId?: string, intentId?: string) => {
    if (!isPipelineRequestCurrent(scope)) return;
    const terminalScope = pipelineTerminalPollScopeRef.current;
    if (terminalScope && isSamePipelineRequestScope(terminalScope, scope)) return;
    try {
      const status = intentId
        ? await getPipelineStatus(scope.projectId, executionId, intentId)
        : executionId
          ? await getPipelineStatus(scope.projectId, executionId)
          : await getPipelineStatus(scope.projectId);
      if (!isPipelineRequestCurrent(scope) || status.project_id !== scope.projectId) return;
      const currentTerminalScope = pipelineTerminalPollScopeRef.current;
      if (currentTerminalScope && isSamePipelineRequestScope(currentTerminalScope, scope)) return;
      const executionStatus = status.last_execution?.status;
      const intentStatus = status.pipeline_intent?.status;
      const isTerminal = intentStatus
        ? intentStatus === 'succeeded' || intentStatus === 'failed' || intentStatus === 'cancelled'
        : executionStatus === 'SUCCEEDED' || executionStatus === 'FAILED';
      if (isTerminal) {
        // A later interval tick may already be in flight; it cannot regress this terminal result.
        pipelineTerminalPollScopeRef.current = scope;
        stopPipelinePolling();
      }
      pipelinePollErrorNotifiedRef.current = false;
      setPipelineStatusReadFailedProjectId(null);
      setPipelineStatus(status);
      void refreshPipelineAutomation({ projectId: scope.projectId, contextGeneration: scope.contextGeneration }).then((settings) => {
        if (isPipelineRequestCurrent(scope) && settings && !isPipelineAutomationInFlight(settings)) {
          stopPipelineAutomationPolling();
        }
      });

      if (isTerminal) {
        if (status.last_execution?.compile_mode === 'full') {
          const capability = await getRecompileAllCapability(scope.projectId).catch(() => null);
          if (isPipelineRequestCurrent(scope) && capability) setFullCapability(capability);
        }
      }
    } catch (err) {
      const currentTerminalScope = pipelineTerminalPollScopeRef.current;
      if (
        !isPipelineRequestCurrent(scope)
        || (currentTerminalScope && isSamePipelineRequestScope(currentTerminalScope, scope))
      ) return;
      // Keep the execution-scoped interval alive so a transient status failure can recover.
      if (!pipelinePollErrorNotifiedRef.current) {
        pipelinePollErrorNotifiedRef.current = true;
        addToast(err instanceof Error ? err.message : 'Pipeline status failed', 'error');
      }
    }
  }, [addToast, isPipelineRequestCurrent, refreshPipelineAutomation, stopPipelineAutomationPolling, stopPipelinePolling]);

  useEffect(() => stopPipelinePolling, [currentProject?.id, stopPipelinePolling]);
  useEffect(() => stopPipelineAutomationPolling, [currentProject?.id, stopPipelineAutomationPolling]);

  const pollPipelineAutomationLifecycle = useCallback(async (scope: PipelineAutomationPollScope) => {
    const context = pipelineProjectContextRef.current;
    const isCurrent = () => pipelineMountedRef.current
      && pipelineAutomationPollScopeRef.current?.projectId === scope.projectId
      && pipelineAutomationPollScopeRef.current.contextGeneration === scope.contextGeneration
      && pipelineProjectContextRef.current.projectId === scope.projectId
      && pipelineProjectContextRef.current.generation === scope.contextGeneration;
    if (!isCurrent() || context.projectId !== scope.projectId || pipelineAutomationPollInFlightRef.current) return;
    pipelineAutomationPollInFlightRef.current = true;
    try {
      const settings = await refreshPipelineAutomation(scope);
      let status: PipelineStatus | null = null;
      try {
        status = await getPipelineStatus(scope.projectId);
      } catch {
        // Keep the independent monitor alive so a later interval can recover.
      }
      if (!isCurrent()) return;
      if (status?.project_id === scope.projectId) {
        const currentIntent = pipelineStatus?.pipeline_intent;
        const nextIntent = status.pipeline_intent;
        if (isPendingPipelineIntent(nextIntent)) {
          // Let an already-running intent finish before taking ownership of a newer one.
          if (!pipelinePollRef.current || !isPendingPipelineIntent(currentIntent)) {
            stopPipelinePolling();
            pipelineTerminalPollScopeRef.current = null;
            pipelinePollErrorNotifiedRef.current = false;
            const requestScope: PipelineRequestScope = {
              projectId: scope.projectId,
              contextGeneration: scope.contextGeneration,
              requestGeneration: ++pipelineRequestGenerationRef.current,
            };
            pipelineExecutionOwnerRef.current = { projectId: scope.projectId, contextGeneration: scope.contextGeneration };
            setPipelineStatusReadFailedProjectId(null);
            setPipelineStatus(status);
            const poll = () => void pollPipelineStatus(requestScope, nextIntent.execution_id, nextIntent.id);
            pipelinePollRef.current = setInterval(poll, 5000);
            poll();
          }
        } else if (!pipelinePollRef.current) {
          setPipelineStatusReadFailedProjectId(null);
          setPipelineStatus(status);
        }
      }
      if (settings && !isPipelineAutomationInFlight(settings)) stopPipelineAutomationPolling();
    } finally {
      pipelineAutomationPollInFlightRef.current = false;
    }
  }, [pipelineStatus?.pipeline_intent, pollPipelineStatus, refreshPipelineAutomation,
    setPipelineStatus, setPipelineStatusReadFailedProjectId, stopPipelineAutomationPolling, stopPipelinePolling]);

  useEffect(() => {
    pipelineAutomationPollTickRef.current = (scope) => { void pollPipelineAutomationLifecycle(scope); };
  }, [pollPipelineAutomationLifecycle]);

  const startPipelineAutomationPolling = useCallback((scope: PipelineAutomationPollScope) => {
    if (!scope.projectId) return;
    const current = pipelineAutomationPollScopeRef.current;
    if (current && (current.projectId !== scope.projectId || current.contextGeneration !== scope.contextGeneration)) {
      stopPipelineAutomationPolling();
    }
    pipelineAutomationPollScopeRef.current = scope;
    if (!pipelineAutomationPollRef.current) {
      pipelineAutomationPollRef.current = setInterval(() => {
        const latest = pipelineAutomationPollScopeRef.current;
        if (latest) pipelineAutomationPollTickRef.current(latest);
      }, 5000);
    }
  }, [stopPipelineAutomationPolling]);

  useEffect(() => {
    if (!projectId) return;
    const scope = { projectId, contextGeneration: pipelineProjectContextRef.current.generation };
    let cancelled = false;
    void refreshPipelineAutomation(scope).then((settings) => {
      if (!cancelled && settings && isPipelineAutomationInFlight(settings)) {
        startPipelineAutomationPolling(scope);
      }
    });
    return () => { cancelled = true; };
  }, [projectId, refreshPipelineAutomation, startPipelineAutomationPolling]);

  const handleUploadQueueSettled = useCallback(({ changed, projectId: uploadedProjectId }: RawUploadsSettled) => {
    if (!changed) return;
    const context = pipelineProjectContextRef.current;
    if (context.projectId === uploadedProjectId) {
      void refreshPipelineStatus({
        projectId: uploadedProjectId,
        contextGeneration: context.generation,
        requestGeneration: pipelineRequestGenerationRef.current,
      });
      void refreshPipelineAutomation({ projectId: uploadedProjectId, contextGeneration: context.generation });
      startPipelineAutomationPolling({ projectId: uploadedProjectId, contextGeneration: context.generation });
    }
  }, [refreshPipelineAutomation, refreshPipelineStatus, startPipelineAutomationPolling]);

  useEffect(() => {
    const refreshAfterAnnotationSave = () => {
      const context = pipelineProjectContextRef.current;
      if (context.projectId) {
        void refreshPipelineStatus({
          projectId: context.projectId,
          contextGeneration: context.generation,
          requestGeneration: pipelineRequestGenerationRef.current,
        });
      }
    };
    window.addEventListener('source-annotation-saved', refreshAfterAnnotationSave);
    return () => window.removeEventListener('source-annotation-saved', refreshAfterAnnotationSave);
  }, [currentProject?.id, refreshPipelineStatus]);

  // Load quota + execution status on mount and when workspace project changes.
  useEffect(() => {
    const context = pipelineProjectContextRef.current;
    const projectId = context.projectId;
    const requestGeneration = pipelineRequestGenerationRef.current;
    let cancelled = false;
    const isCurrentContext = () => {
      const latest = pipelineProjectContextRef.current;
      return !cancelled
        && latest.projectId === context.projectId
        && latest.generation === context.generation
        && pipelineRequestGenerationRef.current === requestGeneration;
    };
    if (!projectId) {
      setPipelineStatus(null);
      setPipelineStatusReadFailedProjectId(null);
      setFullCapability(null);
      setPipelineResult(null);
      setLoading(null);
      return;
    }

    setPipelineStatus(null);
    setPipelineStatusReadFailedProjectId(null);
    setFullCapability(null);
    setPipelineResult(null);
    setLoading(null);
    void (async () => {
      let resumableIntent: PipelineIntentStatus | null = null;
      const resumeIntent = (intent: PipelineIntentStatus) => {
        if (!isCurrentContext()) return;
        const resumedScope: PipelineRequestScope = {
          projectId,
          contextGeneration: context.generation,
          requestGeneration: ++pipelineRequestGenerationRef.current,
        };
        stopPipelinePolling();
        pipelineTerminalPollScopeRef.current = null;
        pipelineExecutionOwnerRef.current = { projectId, contextGeneration: context.generation };
        const poll = () => void pollPipelineStatus(resumedScope, intent.execution_id, intent.id);
        pipelinePollRef.current = setInterval(poll, 5000);
        poll();
      };
      let initialStatusReadFailed = false;
      let recoveryReadInFlight = false;
      const retryInitialStatus = async () => {
        if (!isCurrentContext() || recoveryReadInFlight) return;
        recoveryReadInFlight = true;
        try {
          const status = await getPipelineStatus(projectId);
          if (!isCurrentContext()) return;
          if (status.project_id !== projectId) {
            setPipelineStatusReadFailedProjectId(projectId);
            return;
          }
          setPipelineStatus(status);
          setPipelineStatusReadFailedProjectId(null);
          stopPipelinePolling();
          if (isPendingPipelineIntent(status.pipeline_intent)) resumeIntent(status.pipeline_intent);
        } catch {
          if (isCurrentContext()) setPipelineStatusReadFailedProjectId(projectId);
        } finally {
          recoveryReadInFlight = false;
        }
      };
      try {
        const status = await getPipelineStatus(projectId);
        if (isCurrentContext()) {
          if (status.project_id === projectId) {
            setPipelineStatus(status);
            setPipelineStatusReadFailedProjectId(null);
            resumableIntent = status.pipeline_intent ?? null;
          } else {
            initialStatusReadFailed = true;
            setPipelineStatusReadFailedProjectId(projectId);
          }
        }
      } catch {
        if (isCurrentContext()) {
          initialStatusReadFailed = true;
          setPipelineStatusReadFailedProjectId(projectId);
        }
      }
      if (initialStatusReadFailed && isCurrentContext()) {
        pipelinePollRef.current = setInterval(() => void retryInitialStatus(), 5000);
      }
      try {
        const capability = await getRecompileAllCapability(projectId);
        if (isCurrentContext()) setFullCapability(capability);
      } catch {
        if (isCurrentContext()) setFullCapability(null);
      }
      if (isCurrentContext() && isPendingPipelineIntent(resumableIntent)) {
        resumeIntent(resumableIntent);
      }
    })();

    return () => {
      cancelled = true;
      stopPipelinePolling();
    };
  }, [currentProject?.id, pollPipelineStatus, stopPipelinePolling]);

  const handleScrape = useCallback(async (event: FormEvent) => {
    event.preventDefault();
    const context = pipelineProjectContextRef.current;
    if (!context.projectId || window.localStorage.getItem('llm-wiki-last-project') !== context.projectId) {
      addToast(t('Pipeline.noProject'), 'error');
      return;
    }
    const requestGeneration = ++scrapeRequestGenerationRef.current;
    const isCurrent = () => {
      const latest = pipelineProjectContextRef.current;
      return pipelineMountedRef.current
        && latest.projectId === context.projectId
        && latest.generation === context.generation
        && scrapeRequestGenerationRef.current === requestGeneration;
    };
    setScrapeLoading(true);
    setScrapeError(null);
    setScrapeResult(null);
    setScrapeModalOpen(true);
    try {
      const result = await scrapeUrl(scrapeUrlText, context.projectId);
      if (!isCurrent()) {
        if (result.candidate_id && result.candidate_revision) void rejectScrapeCandidate(result.candidate_id, result.candidate_revision, context.projectId);
        return;
      }
      setScrapeResult(result);
    } catch {
      if (isCurrent()) setScrapeError(t('Pipeline.urlPreviewUnavailable'));
    } finally {
      if (isCurrent()) setScrapeLoading(false);
    }
  }, [addToast, scrapeUrlText, setScrapeError, setScrapeLoading, setScrapeResult, t]);

  const handleAcceptScrape = useCallback(async (title: string) => {
    const candidateId = scrapeResult?.candidate_id;
    const revision = scrapeResult?.candidate_revision;
    const context = pipelineProjectContextRef.current;
    if (!candidateId || !revision || !context.projectId) return;
    const requestGeneration = ++scrapeRequestGenerationRef.current;
    const isCurrent = () => {
      const latest = pipelineProjectContextRef.current;
      return pipelineMountedRef.current
        && latest.projectId === context.projectId
        && latest.generation === context.generation
        && scrapeRequestGenerationRef.current === requestGeneration;
    };
    setScrapeLoading(true);
    setScrapeError(null);
    try {
      const result = await acceptScrapeCandidate(candidateId, revision, title, context.projectId);
      if (!isCurrent()) return;
      setScrapeResult(result);
      if (result.status === 'stored') {
        const scope = {
          projectId: context.projectId,
          contextGeneration: context.generation,
        };
        void refreshPipelineStatus({
          ...scope,
          requestGeneration: pipelineRequestGenerationRef.current,
        });
        void refreshPipelineAutomation(scope);
        startPipelineAutomationPolling(scope);
      }
    } catch (error) {
      if (isCurrent()) setScrapeError(t(scrapeErrorMessageKey(error)));
    } finally {
      if (isCurrent()) setScrapeLoading(false);
    }
  }, [refreshPipelineAutomation, refreshPipelineStatus, scrapeResult?.candidate_id, scrapeResult?.candidate_revision,
    setScrapeError, setScrapeLoading, setScrapeResult, startPipelineAutomationPolling, t]);

  const handleResolveScrape = useCallback(async () => {
    const candidateId = scrapeResult?.candidate_id;
    const context = pipelineProjectContextRef.current;
    if (!candidateId || !context.projectId) return;
    const requestGeneration = ++scrapeRequestGenerationRef.current;
    const isCurrent = () => {
      const latest = pipelineProjectContextRef.current;
      return pipelineMountedRef.current && latest.projectId === context.projectId && latest.generation === context.generation && scrapeRequestGenerationRef.current === requestGeneration;
    };
    setScrapeLoading(true);
    setScrapeError(null);
    try {
      const result = await resolveScrapeCandidate(candidateId, context.projectId);
      if (isCurrent()) {
        setScrapeResult(result);
        if (result.status === 'stored') {
          const scope = { projectId: context.projectId, contextGeneration: context.generation };
          void refreshPipelineStatus({ ...scope, requestGeneration: pipelineRequestGenerationRef.current });
          void refreshPipelineAutomation(scope);
          startPipelineAutomationPolling(scope);
        }
      }
    } catch (error) {
      if (isCurrent()) setScrapeError(t(scrapeErrorMessageKey(error)));
    } finally {
      if (isCurrent()) setScrapeLoading(false);
    }
  }, [refreshPipelineAutomation, refreshPipelineStatus, scrapeResult?.candidate_id, setScrapeError, setScrapeLoading,
    setScrapeResult, startPipelineAutomationPolling, t]);

  const handleDeclineScrape = useCallback(async () => {
    const candidateId = scrapeResult?.candidate_id;
    const revision = scrapeResult?.candidate_revision;
    const context = pipelineProjectContextRef.current;
    if (!candidateId || !revision || !context.projectId) { setScrapeModalOpen(false); return; }
    setScrapeLoading(true);
    setScrapeError(null);
    try {
      const result = await rejectScrapeCandidate(candidateId, revision, context.projectId);
      setScrapeResult(result);
      if (result.status === 'rejected') setScrapeModalOpen(false);
    } catch (error) {
      setScrapeError(t(scrapeErrorMessageKey(error)));
    } finally {
      setScrapeLoading(false);
    }
  }, [scrapeResult?.candidate_id, scrapeResult?.candidate_revision, setScrapeError, setScrapeLoading, setScrapeResult, t]);

  const handleCloseScrape = useCallback(() => {
    if (scrapeLoading) return;
    if (scrapeResult?.status === 'preview') { void handleDeclineScrape(); return; }
    setScrapeModalOpen(false);
    setScrapeError(null);
  }, [handleDeclineScrape, scrapeLoading, scrapeResult?.status]);

  const handleRunPipeline = useCallback(async () => {
    // Belt-and-suspenders: button is disabled for known blocks; toast only if click races through.
    if (isDemoSession) {
      addToast(t('Demo.restricted'), 'info');
      return;
    }

    const context = pipelineProjectContextRef.current;
    const projectId = context.projectId;
    if (!projectId || window.localStorage.getItem('llm-wiki-last-project') !== projectId) {
      addToast(t('Pipeline.noProject'), 'error');
      return;
    }

    if (compileMode === 'full' && !fullCapability?.allowed) {
      addToast(t('Pipeline.fullUnavailable'), 'error');
      return;
    }
    if (compileMode === 'full' && !window.confirm(t('Pipeline.fullConfirm'))) return;

    const requestScope: PipelineRequestScope = {
      projectId,
      contextGeneration: context.generation,
      requestGeneration: ++pipelineRequestGenerationRef.current,
    };
    let latestRequestScope = requestScope;
    setLoading('pipeline');
    try {
      const result = compileMode === 'full'
        ? await recompileAll(projectId)
        : await triggerPipeline();
      if (!isPipelineRequestCurrent(requestScope)) return;
      setPipelineResult(result);
      if (result.message) addToast(result.message, 'info');
      if (result.status === 'accepted') {
        stopPipelinePolling();
        pipelineTerminalPollScopeRef.current = null;
        pipelinePollErrorNotifiedRef.current = false;
        latestRequestScope = {
          ...requestScope,
          requestGeneration: ++pipelineRequestGenerationRef.current,
        };
        pipelineExecutionOwnerRef.current = { projectId, contextGeneration: context.generation };
        setPipelineStatus((prev) => ({
          ...prev,
          project_id: projectId,
          ...(result.intent_id ? {
            pipeline_intent: {
              id: result.intent_id,
              status: result.intent_status ?? 'preparing',
              compile_mode: result.compile_mode ?? compileMode,
              ...(result.execution_id ? { execution_id: result.execution_id } : {}),
            },
          } : {}),
          quota: prev?.quota
            ? { ...prev.quota, already_running: true, allowed: false }
            : prev?.quota,
          full_quota: compileMode === 'full' && result.quota
            ? result.quota
            : prev?.full_quota,
        }));
        const pollThisExecution = () => {
          void pollPipelineStatus(latestRequestScope, result.execution_id, result.intent_id);
        };
        pipelinePollRef.current = setInterval(pollThisExecution, 5000);
        void pollPipelineStatus(latestRequestScope, result.execution_id, result.intent_id);
      }
    } catch (err) {
      // Network / 5xx / stale UI race: show server message.
      if (isPipelineRequestCurrent(requestScope)) {
        addToast(err instanceof Error ? err.message : 'Pipeline trigger failed', 'error');
      }
    } finally {
      if (isPipelineRequestCurrent(latestRequestScope)) setLoading(null);
    }
  }, [addToast, compileMode, fullCapability?.allowed, isDemoSession, isPipelineRequestCurrent, pollPipelineStatus, stopPipelinePolling, t]);

  const handleRetryPipeline = useCallback(async () => {
    const context = pipelineProjectContextRef.current;
    const projectId = context.projectId;
    const intent = pipelineStatus?.pipeline_intent;
    if (!projectId || !intent?.id || intent.status !== 'failed') return;
    const requestScope: PipelineRequestScope = {
      projectId,
      contextGeneration: context.generation,
      requestGeneration: ++pipelineRequestGenerationRef.current,
    };
    let latestRequestScope = requestScope;
    setRetryingPipeline(true);
    try {
      const result = await retryPipeline(intent.id, projectId);
      if (!isPipelineRequestCurrent(requestScope)) return;
      setPipelineResult(result);
      stopPipelinePolling();
      pipelineTerminalPollScopeRef.current = null;
      pipelinePollErrorNotifiedRef.current = false;
      latestRequestScope = { ...requestScope, requestGeneration: ++pipelineRequestGenerationRef.current };
      pipelineExecutionOwnerRef.current = { projectId, contextGeneration: context.generation };
      setPipelineStatus((prev) => ({
        ...prev,
        project_id: projectId,
        ...(result.intent_id ? {
          pipeline_intent: {
            id: result.intent_id,
            status: result.intent_status ?? 'preparing',
            compile_mode: result.compile_mode ?? intent.compile_mode,
            ...(result.execution_id ? { execution_id: result.execution_id } : {}),
          },
        } : {}),
        quota: result.quota ?? prev?.quota,
      }));
      const poll = () => void pollPipelineStatus(latestRequestScope, result.execution_id, result.intent_id);
      pipelinePollRef.current = setInterval(poll, 5000);
      poll();
    } catch (err) {
      if (isPipelineRequestCurrent(requestScope)) addToast(err instanceof Error ? err.message : t('Pipeline.retryFailed'), 'error');
    } finally {
      if (isPipelineRequestCurrent(latestRequestScope)) setRetryingPipeline(false);
    }
  }, [addToast, isPipelineRequestCurrent, pipelineStatus?.pipeline_intent, pollPipelineStatus, stopPipelinePolling, t]);

  const pipelineIntentLabel = (() => {
    switch (pipelineStatus?.pipeline_intent?.status) {
      case 'preparing': return t('Pipeline.intentPreparing');
      case 'ready': return t('Pipeline.intentReady');
      case 'invoking': return t('Pipeline.intentInvoking');
      case 'invocation_unknown': return t('Pipeline.intentUnknown');
      case 'compiling': return t('Pipeline.intentCompiling');
      case 'succeeded': return t('Pipeline.intentSucceeded');
      case 'failed': return t('Pipeline.intentFailed');
      case 'cancelled': return t('Pipeline.intentCancelled');
      default: return null;
    }
  })();
  const pipelineStatusText = pipelineIntentLabel ?? pipelineStatusBadge(pipelineStatus);

  const lastExecutionStatus = pipelineStatus?.last_execution?.status;
  const executionRunning = lastExecutionStatus === 'RUNNING';
  const staleAlreadyRunning =
    pipelineStatus?.quota?.already_running === true && lastExecutionStatus !== 'RUNNING';
  const hasProject = Boolean(currentProject);
  const quota = compileMode === 'full' ? fullCapability?.quota : pipelineStatus?.quota;
  const pipelineIntentPending = ['preparing', 'ready', 'invoking', 'invocation_unknown', 'compiling'].includes(
    pipelineStatus?.pipeline_intent?.status ?? '',
  );
  const pipelineStatusReadFailed = Boolean(projectId && pipelineStatusReadFailedProjectId === projectId);
  const effectiveQuota = useMemo(
    () => (
      staleAlreadyRunning && quota
        ? { ...quota, allowed: true, already_running: false }
        : quota
    ),
    [quota, staleAlreadyRunning],
  );
  const blocked = pipelineIntentPending || pipelineStatusReadFailed || (compileMode === 'full' && !fullCapability
      ? true
      : (compileMode === 'full' && !fullCapability?.allowed) || isRunBlocked({
    isDemoSession,
    loading: loading === 'pipeline',
    hasProject,
    executionRunning,
    quota: effectiveQuota,
    requiresNewRaw: compileMode === 'incremental',
  }));
  const helper = blockReasonMessage({
    isDemoSession,
    hasProject,
    executionRunning,
    quota: effectiveQuota,
    demoMessage: t('Demo.restricted'),
    noProjectMessage: t('Pipeline.noProject'),
  });
  const blockMessage = compileMode === 'full' && !fullCapability?.allowed && !helper
    ? t('Pipeline.fullUnavailable')
    : helper;
  const quotaLine = formatQuotaLine(effectiveQuota, new Date(), {
    quotaLine: t('Pipeline.quotaLine'),
    quotaNotEnforced: t('Pipeline.quotaNotEnforced'),
    cooldownClear: t('Pipeline.cooldownClear'),
  });
  const prereqRows = useMemo(
    () => [
      { ok: !isDemoSession, label: t('Pipeline.prereqDemo') },
      { ok: isUnderDailyLimit(effectiveQuota), label: t('Pipeline.prereqDaily') },
      { ok: isCooldownClear(effectiveQuota), label: t('Pipeline.prereqCooldown') },
      { ok: !executionRunning, label: t('Pipeline.prereqRunning') },
      ...(compileMode === 'incremental'
        ? [{ ok: hasNewRaw(effectiveQuota), label: t('Pipeline.prereqRaw') }]
        : [{ ok: true, label: t('Pipeline.prereqFull') }]),
    ],
    [compileMode, effectiveQuota, executionRunning, isDemoSession, t],
  );

  return (
    <>
      <Surface variant="glass" as="section" className="p-5">
        <h2 className="text-lg font-semibold text-white">{t('Pipeline.addContent')}</h2>
        <p className="mt-1 text-sm text-zinc-400">
          {t('Pipeline.addContentDescription')}
        </p>

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          <RawUploadPanel
            key={token ?? 'no-session'}
            title={t('Pipeline.uploadFiles')}
            hint={t('Pipeline.uploadHint')}
            onBlocked={() => addToast(t('Demo.restricted'), 'info')}
            onQueueSettled={handleUploadQueueSettled}
          />

          {/* URL Scrape */}
          <div className="rounded-[var(--radius-md)] border border-white/10 bg-zinc-950/50 p-4">
            <h3 className="flex items-center gap-2 text-sm font-semibold text-zinc-300">
              <Link2 className="size-4 text-blue-400" /> {t('Pipeline.scrapeUrl')}
            </h3>
            <p className="mt-1 text-xs text-zinc-500">{t('Pipeline.scrapeHint')}</p>
            <p className="mt-1 text-xs text-zinc-500">{t('Pipeline.urlManualCapture')}</p>
            <form onSubmit={handleScrape} className="mt-3 flex gap-2">
              <input
                type="url"
                value={scrapeUrlText}
                onChange={(e) => setScrapeUrlText(e.target.value)}
                placeholder="https://example.com/article"
                className="flex-1 rounded-md border border-white/10 bg-black/30 px-3 py-2 text-sm text-white outline-none transition placeholder:text-zinc-400 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
              />
              <button
                type="submit"
                disabled={scrapeLoading || !projectId}
                className="inline-flex items-center justify-center rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
              >
                {scrapeLoading ? <Loader2 className="size-4 animate-spin" /> : t('Pipeline.scrape')}
              </button>
            </form>
          </div>
        </div>

        <section className="mt-4 rounded-[var(--radius-md)] border border-white/10 bg-zinc-950/40 p-4" aria-labelledby="pipeline-automation-status-title" data-testid="pipeline-automation-settings">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <h3 id="pipeline-automation-status-title" className="text-sm font-semibold text-zinc-200">{t('Pipeline.automationStatus')}</h3>
            <NavigationLink href="/profile#project-pipeline-automation" className="min-h-10 rounded-md border border-white/10 px-3 py-2 text-sm text-emerald-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
              {t('Pipeline.automationSettingsGuide')}
            </NavigationLink>
          </div>
          {pipelineAutomationError ? (
            <p className="mt-2 text-xs text-amber-200" role="status">{pipelineAutomationError}</p>
          ) : null}
          {pipelineAutomation ? (
            <div className="mt-3 space-y-1 text-xs text-zinc-400" aria-live="polite">
              {!pipelineAutomation.admin_allowed ? (
                <p className="text-amber-200/90" data-testid="pipeline-automation-blocked">
                  {automationCopy(t, 'Pipeline.automationAdminDisabled', 'Automatic processing is disabled by the Admin. Saved sources remain pending until the Admin allows processing.')}
                </p>
              ) : null}
              {!pipelineAutomation.enabled && pipelineAutomation.admin_allowed ? (
                <p className="rounded-md border border-red-300/20 bg-red-950/30 p-3 text-red-100" role="status">
                  {t('Pipeline.automationProjectDisabled')}
                </p>
              ) : null}
              <p>
                {automationCopy(t, 'Pipeline.automationStatus', 'Processing status')}: <span className="font-medium text-zinc-200">{t(`Pipeline.automationState.${automationStatusKey(pipelineAutomation.pipeline_status || 'idle')}`)}</span>
              </p>
              {pipelineAutomation.failure_stage || pipelineAutomation.failure_code ? (
                <p className="text-red-200/90">{automationCopy(t, 'Pipeline.automationFailure', 'Last failure')}: {t('Pipeline.automationState.failed')}</p>
              ) : null}
              <details className="pt-1 text-zinc-500">
                <summary className="w-fit cursor-pointer">{t('Pipeline.automationDiagnostics')}</summary>
                <div className="mt-1 space-y-1 break-all font-mono">
                  <p>{pipelineAutomation.pipeline_status || 'idle'} · {t('Pipeline.automationSourceRevision')}: {pipelineAutomation.source_revision} · {t('Pipeline.automationProcessedRevision')}: {pipelineAutomation.processed_revision}</p>
                  {pipelineAutomation.failure_stage || pipelineAutomation.failure_code ? <p data-testid="pipeline-automation-failure">{[pipelineAutomation.failure_stage, pipelineAutomation.failure_code].filter(Boolean).join(' · ')}</p> : null}
                </div>
              </details>
            </div>
          ) : null}
        </section>

        {/* Pipeline Trigger */}
        <div className="mt-4 rounded-[var(--radius-md)] border border-emerald-400/20 bg-emerald-400/[0.04] p-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h3 className="flex items-center gap-2 text-sm font-semibold text-emerald-300">
                <Cog className="size-4" /> {t('Pipeline.pipeline')}
              </h3>
              <p className="mt-1 text-xs text-zinc-400">{t('Pipeline.runDescription')}</p>
            </div>
            <button
              type="button"
              onClick={handleRunPipeline}
              disabled={blocked}
              title={pipelineStatusReadFailed ? t('Pipeline.statusReadFailed') : helper || undefined}
              aria-disabled={blocked}
              className="rounded-md bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {loading === 'pipeline'
                ? t('Pipeline.running')
                : compileMode === 'full' ? t('Pipeline.recompileAll') : t('Pipeline.runPipeline')}
            </button>
          </div>
          <label className="mt-3 flex flex-wrap items-center gap-2 text-xs text-zinc-300">
            <span>{t('Pipeline.compileMode')}</span>
            <select
              value={compileMode}
              onChange={(event) => setCompileMode(event.target.value as 'incremental' | 'full')}
              className="rounded border border-white/10 bg-zinc-900 px-2 py-1 text-white"
              data-testid="pipeline-compile-mode"
            >
              <option value="incremental">{t('Pipeline.incrementalMode')}</option>
              <option value="full">{t('Pipeline.fullMode')}</option>
            </select>
          </label>
          {compileMode === 'full' ? (
            <p className="mt-2 rounded border border-amber-300/20 bg-amber-300/[0.06] p-2 text-xs text-amber-100" data-testid="pipeline-full-notice">
              {t('Pipeline.fullNotice')}
            </p>
          ) : null}
          <p className="mt-2 text-xs text-zinc-500" data-testid="pipeline-quota-line">
            {quotaLine}
          </p>
          {pipelineStatusReadFailed ? (
            <p className="mt-2 text-xs text-amber-200/90" data-testid="pipeline-status-read-failed" role="status">
              {t('Pipeline.statusReadFailed')}
            </p>
          ) : null}
          {pipelineStatus?.scheduled_pipeline ? (
            <section className="mt-2 space-y-1 rounded border border-white/5 bg-zinc-950/40 p-2 text-xs text-zinc-400" data-testid="pipeline-scheduled-pipeline">
              <h4 className="font-medium text-zinc-200">{t('Pipeline.scheduledTitle')}</h4>
              <p>{t('Pipeline.scheduledDescription')}</p>
              <p>{t('Pipeline.scheduledProfileNote')}</p>
              <p>
                {t('Pipeline.scheduledSchedule')}: <span className="font-mono">{pipelineStatus.scheduled_pipeline.schedule}</span> · {pipelineStatus.scheduled_pipeline.timezone}
                {pipelineStatus.scheduled_pipeline.next_check_at ? ` · ${t('Pipeline.scheduledNextCheck')}: ${formatScheduledTime(pipelineStatus.scheduled_pipeline.next_check_at, pipelineStatus.scheduled_pipeline.timezone, locale)}` : ''}
              </p>
              {pipelineStatus.scheduled_pipeline.availability === 'available' ? (
                <>
                  <p>
                    {t('Pipeline.scheduledPending')}: {pipelineStatus.scheduled_pipeline.pending_work} · {t('Pipeline.scheduledNewRaw')}: {pipelineStatus.scheduled_pipeline.new_raw_files} · {t('Pipeline.scheduledRawDirty')}: {pipelineStatus.scheduled_pipeline.raw_dirty_files} · {t('Pipeline.scheduledAnnotationDirty')}: {pipelineStatus.scheduled_pipeline.annotation_dirty_files}
                  </p>
                  <p>
                    {t('Pipeline.scheduledBudget')}: {pipelineStatus.scheduled_pipeline.budget_used ? t('Pipeline.scheduledUsed') : t('Pipeline.scheduledAvailable')}
                    {pipelineStatus.scheduled_pipeline.day_key ? ` · ${pipelineStatus.scheduled_pipeline.day_key} UTC` : ''}
                  </p>
                  <p>{t('Pipeline.scheduledReason')}: {scheduledReasonText(pipelineStatus.scheduled_pipeline.skip_reason, t)}</p>
                </>
              ) : pipelineStatus.scheduled_pipeline.availability === 'disabled' ? (
                <p>{t('Pipeline.scheduledDisabled')}</p>
              ) : pipelineStatus.scheduled_pipeline.availability === 'not_checked' ? (
                <p>{t('Pipeline.scheduledNotChecked')}</p>
              ) : (
                <p>{t('Pipeline.scheduledUnavailable')}</p>
              )}
              <p>
                {t('Pipeline.scheduledLastAttempt')}: {pipelineStatus.scheduled_pipeline.last_attempt_at
                  ? formatScheduledTime(pipelineStatus.scheduled_pipeline.last_attempt_at, pipelineStatus.scheduled_pipeline.timezone, locale)
                  : t('Pipeline.scheduledNoAttempt')}
                {pipelineStatus.scheduled_pipeline.last_result ? ` · ${pipelineStatus.scheduled_pipeline.last_result}` : ''}
              </p>
              {pipelineStatus.scheduled_pipeline.last_execution_id ? (
                <p className="break-all">{t('Pipeline.scheduledExecution')}: {pipelineStatus.scheduled_pipeline.last_execution_id}</p>
              ) : null}
              {pipelineStatus.scheduled_execution ? (
                <div className="border-t border-white/5 pt-1" data-testid="pipeline-scheduled-execution-evidence">
                  <p>{t('Pipeline.scheduledExecutionStatus')}: {pipelineStatus.scheduled_execution.status}</p>
                  <p>{t('Pipeline.scheduledPublication')}: {pipelineStatus.scheduled_execution.publication_outcome ?? t('Pipeline.scheduledEvidenceUnavailable')}</p>
                  <p>{t('Pipeline.scheduledGuidance')}: {pipelineStatus.scheduled_execution.consumed_guidance?.status === 'available'
                    ? `${pipelineStatus.scheduled_execution.consumed_guidance.kind ?? 'unknown'}${pipelineStatus.scheduled_execution.consumed_guidance.revision ? ` · ${pipelineStatus.scheduled_execution.consumed_guidance.revision}` : ''}${pipelineStatus.scheduled_execution.consumed_guidance.content_generation ? ` · ${pipelineStatus.scheduled_execution.consumed_guidance.content_generation}` : ''}`
                    : t('Pipeline.scheduledEvidenceUnavailable')}</p>
                </div>
              ) : pipelineStatus.scheduled_pipeline.last_execution_id ? (
                <p>{t('Pipeline.scheduledEvidenceUnavailable')}</p>
              ) : null}
            </section>
          ) : null}
          {blocked && blockMessage ? (
            <p className="mt-1 text-xs text-amber-200/90" data-testid="pipeline-block-reason">
              {blockMessage}
            </p>
          ) : null}
          <div className="mt-2">
            <button
              type="button"
              onClick={() => setShowPrereq((open) => !open)}
              className="text-xs font-medium text-zinc-400 transition hover:text-zinc-200"
              aria-expanded={showPrereq}
              data-testid="pipeline-prereq-toggle"
            >
              {showPrereq ? '▾' : '▸'} {t('Pipeline.prerequisites')}
            </button>
            {showPrereq ? (
              <ul className="mt-1.5 space-y-1" data-testid="pipeline-prereq-list">
                {prereqRows.map((row) => (
                  <li
                    key={row.label}
                    className="flex items-center gap-2 text-xs text-zinc-400"
                  >
                    <span aria-hidden="true">{row.ok ? '✓' : '✗'}</span>
                    <span className={row.ok ? 'text-zinc-400' : 'text-amber-200/90'}>
                      {row.label}
                    </span>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
          {pipelineResult?.message ? (
            <p className="mt-2 text-xs text-zinc-500">{pipelineResult.message}</p>
          ) : null}
          {pipelineStatus?.pipeline_intent ? (
            <div className="mt-2 space-y-1 rounded border border-white/5 bg-zinc-950/40 p-2 text-xs text-zinc-400" data-testid="pipeline-intent-status">
              <p>{t('Pipeline.intentStatus')}: {pipelineIntentLabel ?? pipelineStatus.pipeline_intent.status}</p>
              {pipelineStatus.pipeline_intent.saved_profile_revision !== undefined ? (
                <p className="break-all">
                  {t('Pipeline.savedProfileSnapshot')}: v{pipelineStatus.pipeline_intent.saved_profile_revision}
                  {pipelineStatus.pipeline_intent.saved_profile_input_digest ? ` · ${pipelineStatus.pipeline_intent.saved_profile_input_digest}` : ''}
                </p>
              ) : null}
              {pipelineStatus.pipeline_intent.prepared_guidance ? (
                <p className="break-all">
                  {t('Pipeline.preparedGuidance')}: {pipelineStatus.pipeline_intent.prepared_guidance.kind}
                  {pipelineStatus.pipeline_intent.prepared_guidance.revision ? ` · ${pipelineStatus.pipeline_intent.prepared_guidance.revision}` : ''}
                  {pipelineStatus.pipeline_intent.prepared_guidance.source_profile_revision !== undefined
                    ? ` · ${t('Pipeline.sourceProfileRevision')} ${pipelineStatus.pipeline_intent.prepared_guidance.source_profile_revision}` : ''}
                  {pipelineStatus.pipeline_intent.prepared_guidance.source_input_digest
                    ? ` · ${pipelineStatus.pipeline_intent.prepared_guidance.source_input_digest}` : ''}
                </p>
              ) : null}
              {pipelineStatus.last_execution?.compile_mode !== 'full' && pipelineStatus.last_execution?.consumed_guidance?.status === 'available' ? (
                <p className="break-all">
                  {t('Pipeline.consumedGuidance')}: {pipelineStatus.last_execution.consumed_guidance.kind}
                  {pipelineStatus.last_execution.consumed_guidance.revision ? ` · ${pipelineStatus.last_execution.consumed_guidance.revision}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_profile_revision !== undefined
                    ? ` · ${t('Pipeline.sourceProfileRevision')} ${pipelineStatus.last_execution.consumed_guidance.source_profile_revision}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_content_generation
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_content_generation}` : ''}
                </p>
              ) : null}
              {pipelineStatus.pipeline_intent.error_message ? (
                <p className="text-amber-200/90" role="alert">{pipelineStatus.pipeline_intent.error_message}</p>
              ) : null}
              {pipelineStatus.pipeline_intent.status === 'failed' ? (
                <button
                  type="button"
                  onClick={() => void handleRetryPipeline()}
                  disabled={retryingPipeline}
                  className="mt-1 rounded border border-white/10 px-2 py-1 font-medium text-zinc-200 transition hover:bg-white/5 disabled:opacity-50"
                  data-testid="pipeline-retry"
                >
                  {retryingPipeline ? t('Pipeline.intentRetrying') : t('Pipeline.intentRetry')}
                </button>
              ) : null}
            </div>
          ) : null}
          {pipelineStatus?.last_execution?.compile_mode === 'full' ? (
            <div className="mt-2 space-y-1 rounded border border-white/5 bg-zinc-950/40 p-2 text-xs text-zinc-400" data-testid="pipeline-full-evidence">
              <p>
                {t('Pipeline.publicationOutcome')}: {pipelineStatus.last_execution.publication_outcome === 'confirmed_published'
                  ? t('Pipeline.publicationPublished')
                  : pipelineStatus.last_execution.publication_outcome === 'confirmed_not_published'
                    ? t('Pipeline.publicationNotPublished')
                    : t('Pipeline.publicationUnknown')}
              </p>
              <p>
                {t('Pipeline.consumedGuidance')}: {pipelineStatus.last_execution.consumed_guidance?.status !== 'available'
                  ? t('Pipeline.guidanceUnavailable')
                  : pipelineStatus.last_execution.consumed_guidance.kind === 'none'
                    ? t('Pipeline.guidanceNone')
                    : `${t(pipelineStatus.last_execution.consumed_guidance.kind === 'bootstrap'
                      ? 'Pipeline.guidanceBootstrap'
                      : 'Pipeline.guidanceActive')} ${pipelineStatus.last_execution.consumed_guidance.revision ?? ''}`}
              </p>
              {pipelineStatus.last_execution.consumed_guidance?.status === 'available' ? (
                <p className="break-all">
                  {pipelineStatus.last_execution.consumed_guidance.source_profile_revision !== undefined
                    ? `${t('Pipeline.sourceProfileRevision')} ${pipelineStatus.last_execution.consumed_guidance.source_profile_revision}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_content_generation
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_content_generation}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_candidate_id
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_candidate_id}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_profile_input_digest
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_profile_input_digest}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_model_version
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_model_version}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_prompt_version
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_prompt_version}` : ''}
                  {pipelineStatus.last_execution.consumed_guidance.source_schema_version
                    ? ` · ${pipelineStatus.last_execution.consumed_guidance.source_schema_version}` : ''}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>

        {/* Pipeline Info */}
        <div className="mt-3 rounded-[var(--radius-md)] border border-white/5 bg-zinc-950/40 p-3">
          <p className="flex flex-wrap items-center gap-2 text-xs text-zinc-500">
            <span>{t('Pipeline.pipelineDesc')}</span>
            {pipelineStatusText ? (
              <Badge variant="accent">{pipelineStatusText}</Badge>
            ) : (
              <span>{t('Pipeline.pipelineStages')}</span>
            )}
          </p>
        </div>

        <ExportPanel />
      </Surface>

      {scrapeModalOpen ? (
        <UrlPreviewDialog
          key={scrapeResult?.candidate_id ?? 'url-preview-preparing'}
          result={scrapeResult}
          loading={scrapeLoading}
          error={scrapeError}
          labels={{
            heading: t('Pipeline.urlPreviewTitle'), title: t('Pipeline.urlTitleLabel'),
            save: t('Pipeline.urlConfirmCandidate'), decline: t('Pipeline.urlDeclineCandidate'),
            close: t('Pipeline.urlClosePreview'), checkStatus: t('Pipeline.urlCheckSaveStatus'),
            manualCapture: t('Pipeline.urlManualCapture'), processingSeparate: automationCopy(t, 'Pipeline.urlProcessingAutomatic', 'When both auto-pipeline switches are on, changed content is processed automatically; saved content is not searchable until publication succeeds.'),
            rawArticle: t('Pipeline.urlRawArticle'), created: t('Pipeline.urlCreated'), updated: t('Pipeline.urlUpdated'),
            unchanged: t('Pipeline.urlUnchanged'), digest: t('Pipeline.urlCandidateDigest'), loading: t('Pipeline.urlPreviewLoading'),
            qualityUnavailable: t('Pipeline.urlQualityCheckUnavailable'), qualityUnavailableSaved: t('Pipeline.urlQualityCheckUnavailableSaved'),
          }}
          onAccept={handleAcceptScrape}
          onDecline={handleDeclineScrape}
          onResolve={handleResolveScrape}
          onClose={handleCloseScrape}
        />
      ) : null}

      {/* Toast notifications */}
      <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2">
        {toasts.map((toast) => (
          <div
            key={toast.id}
            className={`rounded-lg border px-4 py-2 text-sm shadow-lg backdrop-blur ${
              toast.type === 'success'
                ? 'border-emerald-300/30 bg-emerald-900/80 text-emerald-200'
                : toast.type === 'error'
                  ? 'border-red-300/30 bg-red-900/80 text-red-200'
                  : 'border-zinc-300/30 bg-zinc-800/80 text-zinc-200'
            }`}
          >
            {toast.message}
          </div>
        ))}
      </div>

    </>
  );
}

function formatScheduledTime(value: string, timezone: string, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(locale === 'zh-TW' ? 'zh-TW' : locale === 'zh-CN' ? 'zh-CN' : 'en-US', {
    timeZone: timezone || 'UTC', dateStyle: 'medium', timeStyle: 'short',
  });
}

function scheduledReasonText(reason: string, t: (key: string) => string): string {
  if (!reason) return t('Pipeline.scheduledReasonNone');
  const key = `Pipeline.scheduledSkip.${reason}`;
  const translated = t(key);
  return translated === key ? reason : translated;
}
