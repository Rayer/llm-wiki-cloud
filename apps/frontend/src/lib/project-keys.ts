import { apiFetch } from './api.ts';

export type ProjectKey = {
  key_id: string;
  name: string;
  project_id: string;
  capabilities: string[];
  state: 'active' | 'revoked';
  created_at: string;
  revoked_at?: string;
  last_used_at?: string;
  last_used_status?: 'available' | 'no_record' | 'unavailable';
};

export type CreatedProjectKey = {
  key: ProjectKey;
  secret: string;
};

export class ProjectKeyApiError extends Error {
  readonly status: number;
  readonly keyId?: string;

  constructor(message: string, status: number, keyId?: string) {
    super(message);
    this.name = 'ProjectKeyApiError';
    this.status = status;
    this.keyId = keyId;
  }
}

function route(projectId: string, suffix = ''): string {
  if (!projectId.trim()) throw new Error('Project ID is required.');
  return `/api/v1/projects/${encodeURIComponent(projectId)}/keys${suffix}`;
}

async function readResponse<T>(response: Response): Promise<T> {
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const data = payload && typeof payload === 'object' && !Array.isArray(payload)
      ? payload as Record<string, unknown>
      : {};
    const message = typeof data.error === 'string' ? data.error : `Project key request failed (${response.status})`;
    const keyId = typeof data.key_id === 'string' ? data.key_id : undefined;
    throw new ProjectKeyApiError(message, response.status, keyId);
  }
  return payload as T;
}

export async function listProjectKeys(projectId: string): Promise<ProjectKey[]> {
  const response = await apiFetch(route(projectId), { method: 'GET', projectId });
  const payload = await readResponse<{ keys?: ProjectKey[] }>(response);
  return Array.isArray(payload.keys) ? payload.keys : [];
}

export async function createProjectKey(projectId: string, name: string): Promise<CreatedProjectKey> {
  const response = await apiFetch(route(projectId), {
    method: 'POST',
    projectId,
    json: true,
    body: JSON.stringify({ name }),
  });
  return readResponse<CreatedProjectKey>(response);
}

export async function revokeProjectKey(projectId: string, keyId: string): Promise<ProjectKey> {
  const response = await apiFetch(route(projectId, `/${encodeURIComponent(keyId)}/revoke`), {
    method: 'POST',
    projectId,
  });
  const payload = await readResponse<{ key: ProjectKey }>(response);
  return payload.key;
}
