'use client';

import { apiFetch, safeWikiRouteSegment } from './api.ts';
import {
  normalizeProject,
  normalizeProjects,
  selectDefaultProject,
  type Project,
} from './project-core.ts';

export const LAST_PROJECT_KEY = 'llm-wiki-last-project';
export const MAX_PROJECT_NAME_LENGTH = 64;

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

export async function createProject(name: string): Promise<Project> {
  const trimmedName = name.trim();
  const response = await apiFetch('/api/v1/init-project', {
    method: 'POST',
    json: true,
    requireProject: false,
    body: JSON.stringify({
      name: trimmedName,
      project: trimmedName,
      project_name: trimmedName,
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

export {
  normalizeProject,
  normalizeProjects,
  selectDefaultProject,
  type Project,
};
