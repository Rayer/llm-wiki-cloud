import type { PipelineQuota } from './api';

export function formatQuotaLine(q: PipelineQuota | null | undefined, now = new Date()): string {
  if (!q) return '';
  if (!q.enforced) return 'Quota not enforced';
  const cooldown = formatCooldownRemaining(q.cooldown_until, now);
  const parts = [
    `Runs today: ${q.runs_today}/${q.daily_limit}`,
    cooldown ? `Cooldown: ${cooldown}` : 'Cooldown: clear',
    `New files: ${q.new_raw_files}`,
  ];
  return parts.join(' · ');
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
  if (opts.quota && opts.quota.enforced && opts.quota.allowed === false) return true;
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
  if (opts.quota?.message) return opts.quota.message;
  return '';
}
