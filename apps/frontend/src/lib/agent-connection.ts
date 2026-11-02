import { ApiError, apiFetch, safeWikiRouteSegment } from './api.ts';

export type AgentConnectionStatus = 'not_started' | 'waiting' | 'confirmed' | 'expired';

export type AgentConnectionCheck = {
  project_id: string;
  status: AgentConnectionStatus;
  setup_id?: string;
  created_at?: string;
  expires_at?: string;
  confirmed_at: string | null;
  confirmation_token?: string;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isTimestamp(value: unknown): value is string {
  return typeof value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(value)
    && Number.isFinite(Date.parse(value));
}

function parseCheck(value: unknown, projectId: string): AgentConnectionCheck {
  if (!isRecord(value) || value.project_id !== projectId
    || !['not_started', 'waiting', 'confirmed', 'expired'].includes(String(value.status))) {
    throw new ApiError('Invalid Agent connection response', 500);
  }
  const status = value.status as AgentConnectionStatus;
  if (status === 'not_started') {
    if (Object.hasOwn(value, 'setup_id') || Object.hasOwn(value, 'confirmation_token')) {
      throw new ApiError('Invalid Agent connection response', 500);
    }
    return { project_id: projectId, status, confirmed_at: null };
  }
  if (typeof value.setup_id !== 'string' || !value.setup_id.trim() || !isTimestamp(value.created_at)
    || !isTimestamp(value.expires_at)) {
    throw new ApiError('Invalid Agent connection response', 500);
  }
  const confirmedAt = value.confirmed_at === null || isTimestamp(value.confirmed_at) ? value.confirmed_at : undefined;
  if (confirmedAt === undefined || (status === 'confirmed' && confirmedAt === null)
    || (status !== 'confirmed' && confirmedAt !== null)) {
    throw new ApiError('Invalid Agent connection response', 500);
  }
  const result: AgentConnectionCheck = {
    project_id: projectId,
    status,
    setup_id: value.setup_id,
    created_at: value.created_at,
    expires_at: value.expires_at,
    confirmed_at: confirmedAt,
  };
  if (status === 'waiting') {
    if (typeof value.confirmation_token !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(value.confirmation_token)) {
      throw new ApiError('Invalid Agent connection response', 500);
    }
    result.confirmation_token = value.confirmation_token;
  } else if (Object.hasOwn(value, 'confirmation_token')) {
    throw new ApiError('Invalid Agent connection response', 500);
  }
  return result;
}

async function readResponse(response: Response, projectId: string): Promise<AgentConnectionCheck> {
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const message = isRecord(payload) && typeof payload.error === 'string' ? payload.error : `Agent connection request failed (${response.status})`;
    throw new ApiError(message, response.status, undefined, response.headers.get('X-Request-ID'));
  }
  return parseCheck(payload, projectId);
}

function projectPath(projectId: string): string {
  const safeProjectId = safeWikiRouteSegment(projectId);
  if (!safeProjectId) throw new Error('Invalid project ID.');
  return `/api/v1/projects/${encodeURIComponent(safeProjectId)}/agent-connection-check`;
}

export async function getAgentConnectionCheck(projectId: string): Promise<AgentConnectionCheck> {
  const response = await apiFetch(projectPath(projectId), { projectId, cache: 'no-store' });
  return readResponse(response, projectId);
}

export async function createAgentConnectionCheck(projectId: string): Promise<AgentConnectionCheck> {
  const response = await apiFetch(projectPath(projectId), { method: 'POST', projectId });
  const result = await readResponse(response, projectId);
  if (result.status !== 'waiting' || !result.confirmation_token) {
    throw new ApiError('Invalid Agent connection setup response', 500);
  }
  return result;
}
