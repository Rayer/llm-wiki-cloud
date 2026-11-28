'use client';

import { apiFetch } from './api.ts';

export type NudgeTarget =
  | { kind: 'pipeline-run'; project_id: string; execution_id: string }
  | { kind: 'project-description'; project_id: string }
  | { kind: 'project-ai-service'; project_id: string }
  | { kind: 'release-notes'; mode: 'current' }
  | { kind: 'release-notes'; mode: 'published-version'; version: string };

export type NudgeAction = {
  id: string;
  kind: 'link' | 'copy' | 'copy-then-link';
  label: string;
  copy_text?: string;
  target?: NudgeTarget;
};

export type NudgeObservation = {
  project_id?: string;
  project_name?: string;
  execution_id?: string;
  title: string;
  body: string;
  declared_purpose?: string;
  published_summary?: string;
  coverage?: {
    total_concepts: number;
    selected_concepts: number;
    sampled: boolean;
    title_truncated_concepts: number;
    body_truncated_concepts: number;
  };
  severity: 'urgent' | 'attention' | 'info';
  stale?: boolean;
  observed_at?: string;
  actions: NudgeAction[];
};

export type NudgeSlot = {
  source_key: string;
  source_revision: number;
  active: boolean;
  observation?: NudgeObservation;
};

export type NudgeItem = {
  category: string;
  attention_id?: string;
  attention_state?: 'unread' | 'read' | 'dismissed';
  severity: 'urgent' | 'attention' | 'info';
  observations: NudgeSlot[];
};

export type NudgeView = {
  schema_version: number;
  partial: boolean;
  failures: Array<{ project_id?: string; safe_class: string }>;
  notices: Array<{ source_key: string; state: string; safe_class?: string }>;
  items: NudgeItem[];
};

async function errorMessage(response: Response): Promise<string> {
  const payload: unknown = await response.json().catch(() => null);
  if (payload && typeof payload === 'object' && typeof (payload as Record<string, unknown>).error === 'string') {
    return (payload as Record<string, string>).error;
  }
  return `Nudge request failed (${response.status}).`;
}

export async function getNudges(): Promise<NudgeView> {
  const response = await apiFetch('/api/v1/nudges', { method: 'GET', requireProject: false });
  if (!response.ok) throw new Error(await errorMessage(response));
  const value: unknown = await response.json();
  if (!value || typeof value !== 'object' || !Array.isArray((value as NudgeView).items)) throw new Error('Nudge response is invalid.');
  return value as NudgeView;
}

export async function reconcileNudges(): Promise<NudgeView> {
  const response = await apiFetch('/api/v1/nudges/reconcile', { method: 'POST', requireProject: false });
  if (!response.ok) throw new Error(await errorMessage(response));
  const value: unknown = await response.json();
  if (!value || typeof value !== 'object' || !Array.isArray((value as NudgeView).items)) throw new Error('Nudge response is invalid.');
  return value as NudgeView;
}

export async function setNudgeAttention(category: string, attentionId: string, action: 'read' | 'unread' | 'dismiss'): Promise<void> {
  const response = await apiFetch(`/api/v1/nudges/${encodeURIComponent(category)}/attention`, {
    method: 'PATCH', requireProject: false,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ attention_id: attentionId, action }),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
}

export function resolveNudgeTarget(target: NudgeTarget): string | null {
  const segment = (value: string) => value.length > 0 && value.length <= 256 && !/[\\/\0]/.test(value);
  switch (target.kind) {
    case 'pipeline-run':
      if (!segment(target.project_id) || !segment(target.execution_id)) return null;
      return `/status?project_id=${encodeURIComponent(target.project_id)}&execution_id=${encodeURIComponent(target.execution_id)}`;
    case 'project-description':
      if (!segment(target.project_id)) return null;
      return `/profile?project_id=${encodeURIComponent(target.project_id)}#project-description`;
    case 'project-ai-service':
      if (!segment(target.project_id)) return null;
      return `/profile?project_id=${encodeURIComponent(target.project_id)}#project-ai-service`;
    case 'release-notes':
      if (target.mode === 'current') return '/release-notes?version=current';
      if (target.mode === 'published-version' && target.version.length > 0 && target.version.length <= 64 && !/[\\/?#\0]/.test(target.version)) {
        return `/release-notes?version=${encodeURIComponent(target.version)}`;
      }
      return null;
    default:
      return null;
  }
}
