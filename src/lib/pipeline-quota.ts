import type { PipelineQuota } from './api';

type QuotaLineMessages = {
  quotaLine: string;
  quotaNotEnforced: string;
  cooldownClear: string;
};

const defaultQuotaLineMessages: QuotaLineMessages = {
  quotaLine: 'Runs today: {runs}/{limit} · Cooldown: {cooldown} · New files: {newFiles}',
  quotaNotEnforced: 'Quota not enforced',
  cooldownClear: 'clear',
};

export function formatQuotaLine(
  q: PipelineQuota | null | undefined,
  now = new Date(),
  messages: QuotaLineMessages = defaultQuotaLineMessages,
): string {
  if (!q) return '';
  if (!q.enforced) return messages.quotaNotEnforced;
  const cooldown = formatCooldownRemaining(q.cooldown_until, now) ?? messages.cooldownClear;
  return messages.quotaLine.replace(/\{(\w+)\}/g, (match, name: string) => {
    const params: Record<string, string | number> = {
      runs: q.runs_today,
      limit: q.daily_limit,
      cooldown,
      newFiles: q.new_raw_files,
    };
    return params[name] === undefined ? match : String(params[name]);
  });
}

export function formatCooldownRemaining(
  until: string | null | undefined,
  now = new Date(),
): string | null {
  if (!until) return null;
  const ms = new Date(until).getTime() - now.getTime();
  if (Number.isNaN(ms) || ms <= 0) return null;
  const mins = Math.max(1, Math.ceil(ms / 60000));
  if (mins >= 60) {
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return m ? `${h}h ${m}m` : `${h}h`;
  }
  return `${mins}m`;
}

export function isRunBlocked(opts: {
  isDemoSession: boolean;
  loading: boolean;
  hasProject: boolean;
  executionRunning: boolean;
  quota?: PipelineQuota | null;
}): boolean {
  if (opts.isDemoSession) return true;
  if (opts.loading) return true;
  if (!opts.hasProject) return true;
  if (opts.executionRunning) return true;
  if (opts.quota?.enforced) {
    if (opts.quota.runs_today >= opts.quota.daily_limit) return true;
    if (formatCooldownRemaining(opts.quota.cooldown_until)) return true;
    if (opts.quota.new_raw_files < opts.quota.min_new_raw) return true;
    const staleAlreadyRunning = opts.quota.already_running === true && !opts.executionRunning;
    if (opts.quota.allowed === false && !staleAlreadyRunning) return true;
  }
  return false;
}

export function blockReasonMessage(opts: {
  isDemoSession: boolean;
  hasProject: boolean;
  executionRunning: boolean;
  quota?: PipelineQuota | null;
  demoMessage: string;
  noProjectMessage: string;
}): string {
  if (opts.isDemoSession) return opts.demoMessage;
  if (!opts.hasProject) return opts.noProjectMessage;
  if (opts.executionRunning) return opts.quota?.message || 'Pipeline is running';
  if (opts.quota?.already_running === true && opts.quota.allowed === false) return '';
  if (opts.quota?.message) return opts.quota.message;
  return '';
}
