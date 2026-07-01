'use client';

import { apiFetch } from './api';
import {
  normalizeProject,
  normalizeProjects,
  type Project,
} from './project-core';

export const LAST_PROJECT_KEY = 'llm-wiki-last-project';

function apiError(payload: unknown, fallback: string): string {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return fallback;
  const record = payload as Record<string, unknown>;
  const message = record.error ?? record.message ?? record.detail;
  return typeof message === 'string' && message.trim() ? message : fallback;
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

export {
  normalizeProject,
  normalizeProjects,
  selectDefaultProject,
  type Project,
} from './project-core';
