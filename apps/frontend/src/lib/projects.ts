'use client';

import { ApiError, apiFetch, safeWikiRouteSegment } from './api.ts';
import { getRuntimeConfig } from './runtime-config.ts';
import { recordRequestResponse } from './request-ids.ts';
import {
  normalizeProject,
  normalizeProjects,
  normalizeProjectDescription,
  selectDefaultProject,
  isValidProjectDescription,
  MAX_PROJECT_DESCRIPTION_LENGTH,
  type Project,
} from './project-core.ts';

export const LAST_PROJECT_KEY = 'llm-wiki-last-project';
export const MAX_PROJECT_NAME_LENGTH = 64;

export type ProjectContentSummary = {
  text: string;
  source_generation: string;
  concepts_sha256: string;
  generated_at: string;
  prompt_revision: string;
  profile_source: 'bootstrap' | 'personal' | 'shared';
  profile_id?: string;
  profile_revision?: number;
  role: string;
  requested_model: string;
  returned_model: string;
  input_digest: string;
  selection_version: string;
};

export type ProjectContentContext = {
  project_id: string;
  status: string;
  current_generation: string | null;
  current_concepts_sha256: string | null;
  summary: ProjectContentSummary | null;
  stale: boolean;
  error_class?: string | null;
};

export type ProjectDescription = {
  projectId: string;
  description: string;
  revision: number;
};

function apiError(payload: unknown, fallback: string): string {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return fallback;
  const record = payload as Record<string, unknown>;
  const message = record.error ?? record.message ?? record.detail;
  return typeof message === 'string' && message.trim() ? message : fallback;
}

function safeProjectRouteSegment(projectId: string): string {
  let decodedProjectId: string;
  try {
    decodedProjectId = decodeURIComponent(projectId);
  } catch {
    throw new Error('Invalid project ID.');
  }
  if (!safeWikiRouteSegment(projectId) || !safeWikiRouteSegment(decodedProjectId)) {
    throw new Error('Invalid project ID.');
  }
  return projectId;
}

export async function getProjects(): Promise<Project[]> {
  const response = await apiFetch('/api/v1/projects', { requireProject: false });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Unable to load projects (${response.status})`));
  }
  return normalizeProjects(payload);
}

export async function bootstrapDefaultProject(accessToken: string): Promise<string> {
  if (!accessToken.trim()) throw new Error('Authentication required.');

  const response = recordRequestResponse(await fetch(
    `${getRuntimeConfig().api_url}/api/v1/onboarding/default-project`,
    {
      method: 'POST',
      credentials: 'omit',
      cache: 'no-store',
      headers: {
        Authorization: `Bearer ${accessToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ schema_version: 1 }),
    },
  ));
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Workspace setup failed (${response.status})`));
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Workspace setup response is invalid.');
  }
  const record = payload as Record<string, unknown>;
  const keys = Object.keys(record).sort();
  if (
    keys.length !== 3
    || keys[0] !== 'default_project_id'
    || keys[1] !== 'schema_version'
    || keys[2] !== 'status'
    || record.schema_version !== 1
    || record.status !== 'ready'
    || typeof record.default_project_id !== 'string'
    || !safeWikiRouteSegment(record.default_project_id)
  ) {
    throw new Error('Workspace setup response is invalid.');
  }
  return record.default_project_id;
}

export async function createProject(name: string, description = '', idempotencyKey = crypto.randomUUID()): Promise<Project> {
  const trimmedName = name.trim();
  const normalizedDescription = normalizeProjectDescription(description);
  if (!isValidProjectDescription(normalizedDescription)) {
    throw new Error(`Project description must be valid text of at most ${MAX_PROJECT_DESCRIPTION_LENGTH} characters.`);
  }
  const response = await apiFetch('/api/v1/init-project', {
    method: 'POST',
    json: true,
    requireProject: false,
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({
      name: trimmedName,
      project: trimmedName,
      project_name: trimmedName,
      description: normalizedDescription,
    }),
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Unable to create project (${response.status})`));
  }

  const project = normalizeProject(payload);
  if (!project) throw new Error('Create project response did not include a project.');
  return project;
}

function parseProjectDescription(payload: unknown, expectedProjectId: string): ProjectDescription {
  if (
    !payload || typeof payload !== 'object' || Array.isArray(payload)
    || !('project_id' in payload) || payload.project_id !== expectedProjectId
    || !('description' in payload) || typeof payload.description !== 'string'
    || !('revision' in payload) || typeof payload.revision !== 'number'
    || !Number.isSafeInteger(payload.revision) || payload.revision < 0
  ) {
    throw new Error('Project description response is invalid.');
  }
  return {
    projectId: expectedProjectId,
    description: payload.description,
    revision: payload.revision,
  };
}

export async function getProjectDescription(projectId: string): Promise<ProjectDescription> {
  const targetProjectId = safeProjectRouteSegment(projectId);
  const response = await apiFetch(`/api/v1/projects/${encodeURIComponent(targetProjectId)}/description`, {
    projectId: targetProjectId,
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(apiError(payload, `Unable to load project description (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  return parseProjectDescription(payload, targetProjectId);
}

export async function saveProjectDescription(projectId: string, description: string, revision: number): Promise<ProjectDescription> {
  const targetProjectId = safeProjectRouteSegment(projectId);
  const normalizedDescription = normalizeProjectDescription(description);
  if (!isValidProjectDescription(normalizedDescription) || !Number.isSafeInteger(revision) || revision < 0) {
    throw new Error(`Project description must be valid text of at most ${MAX_PROJECT_DESCRIPTION_LENGTH} characters.`);
  }
  const response = await apiFetch(`/api/v1/projects/${encodeURIComponent(targetProjectId)}/description`, {
    method: 'PUT',
    projectId: targetProjectId,
    json: true,
    body: JSON.stringify({ description: normalizedDescription, revision }),
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(apiError(payload, `Unable to save project description (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  return parseProjectDescription(payload, targetProjectId);
}

export async function renameProject(projectId: string, name: string): Promise<string> {
  const targetProjectId = safeProjectRouteSegment(projectId);
  const trimmedName = name.trim();
  const length = [...trimmedName].length;

  if (!trimmedName) {
    throw new Error('Project name is required.');
  }
  if (length < 1 || length > MAX_PROJECT_NAME_LENGTH) {
    throw new Error(`Project name must be 1-${MAX_PROJECT_NAME_LENGTH} characters.`);
  }

  const response = await apiFetch(`/api/v1/projects/${encodeURIComponent(targetProjectId)}`, {
    method: 'PATCH',
    projectId: targetProjectId,
    json: true,
    body: JSON.stringify({ name: trimmedName }),
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Unable to rename project (${response.status})`));
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Rename response did not include a project name.');
  }
  const result = payload as { name?: unknown };
  const nextName = typeof result.name === 'string' ? result.name.trim() : '';
  if (!nextName) {
    throw new Error('Rename response did not include a project name.');
  }
  return nextName;
}

export async function getProjectContentContext(projectId: string): Promise<ProjectContentContext> {
  const targetProjectId = safeProjectRouteSegment(projectId);
  const response = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(targetProjectId)}/content-context`,
    { projectId: targetProjectId },
  );
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Unable to load Project content context (${response.status})`));
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Project content context response is invalid.');
  }

  const record = payload as Record<string, unknown>;
  const summary = record.summary;
  const summaryRecord = summary && typeof summary === 'object' && !Array.isArray(summary)
    ? summary as Record<string, unknown>
    : null;
  const profileSource = summaryRecord?.profile_source;
  const profileID = summaryRecord?.profile_id;
  const profileRevision = summaryRecord?.profile_revision;
  const validProfileIdentity = (
    (profileSource === 'personal' || profileSource === 'shared')
    && typeof profileID === 'string' && profileID.trim() !== ''
    && typeof profileRevision === 'number' && Number.isInteger(profileRevision) && profileRevision > 0
  ) || (profileSource === 'bootstrap' && profileID === undefined && profileRevision === undefined);
  const validSummary = summary === null || (
    summaryRecord !== null
    && typeof summaryRecord.text === 'string'
    && typeof summaryRecord.source_generation === 'string'
    && typeof summaryRecord.concepts_sha256 === 'string'
    && typeof summaryRecord.generated_at === 'string'
    && typeof summaryRecord.prompt_revision === 'string'
    && (profileSource === 'bootstrap' || profileSource === 'personal' || profileSource === 'shared')
    && validProfileIdentity
    && typeof summaryRecord.role === 'string'
    && typeof summaryRecord.requested_model === 'string'
    && typeof summaryRecord.returned_model === 'string'
    && typeof summaryRecord.input_digest === 'string'
    && typeof summaryRecord.selection_version === 'string'
  );
  if (
    record.project_id !== targetProjectId
    || typeof record.status !== 'string'
    || (record.current_generation !== null && typeof record.current_generation !== 'string')
    || (record.current_concepts_sha256 !== null && typeof record.current_concepts_sha256 !== 'string')
    || !validSummary
    || typeof record.stale !== 'boolean'
    || (record.error_class !== undefined && record.error_class !== null && typeof record.error_class !== 'string')
  ) {
    throw new Error('Project content context response is invalid.');
  }

  return record as ProjectContentContext;
}

export {
  normalizeProject,
  normalizeProjects,
  selectDefaultProject,
  MAX_PROJECT_DESCRIPTION_LENGTH,
  type Project,
};
