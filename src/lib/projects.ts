'use client';

export type Project = {
  id: string;
  name: string;
};

export const LAST_PROJECT_KEY = 'llm-wiki-last-project';
const API_URL =
  process.env.NEXT_PUBLIC_API_URL ?? 'https://llm-wiki-bff-dev.rayer.idv.tw';

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function firstString(record: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
}

function projectFromRecord(record: Record<string, unknown>): Project | null {
  const id = firstString(record, ['id', 'project_id', 'projectId', 'slug', 'name']);
  const name = firstString(record, ['name', 'project_name', 'projectName', 'title', 'id']);
  return id && name ? { id, name } : null;
}

export function normalizeProject(payload: unknown): Project | null {
  if (typeof payload === 'string' && payload.trim()) {
    return { id: payload.trim(), name: payload.trim() };
  }
  if (!isRecord(payload)) return null;

  if (isRecord(payload.project)) {
    return projectFromRecord(payload.project);
  }

  const direct = projectFromRecord(payload);
  if (direct) return direct;

  const projectId = firstString(payload, ['project']);
  return projectId ? { id: projectId, name: projectId } : null;
}

export function normalizeProjects(payload: unknown): Project[] {
  const items = Array.isArray(payload)
    ? payload
    : isRecord(payload) && Array.isArray(payload.projects)
      ? payload.projects
      : isRecord(payload) && Array.isArray(payload.items)
        ? payload.items
        : isRecord(payload) && Array.isArray(payload.data)
          ? payload.data
          : [];

  return items
    .map(normalizeProject)
    .filter((project): project is Project => project !== null);
}

export function selectDefaultProject(
  projects: Project[],
  lastProjectId: string | null,
): Project | null {
  if (projects.length === 0) return null;
  const restored = projects.find((project) => project.id === lastProjectId);
  if (restored) return restored;
  return [...projects].sort((a, b) =>
    a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }),
  )[0];
}

function authHeaders(token: string): HeadersInit {
  return {
    Authorization: `Bearer ${token}`,
    'Content-Type': 'application/json',
  };
}

function apiError(payload: unknown, fallback: string): string {
  if (!isRecord(payload)) return fallback;
  const message = payload.error ?? payload.message ?? payload.detail;
  return typeof message === 'string' && message.trim() ? message : fallback;
}

export async function getProjects(token: string): Promise<Project[]> {
  const response = await fetch(`${API_URL}/api/v1/projects`, {
    headers: authHeaders(token),
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(apiError(payload, `Unable to load projects (${response.status})`));
  }
  return normalizeProjects(payload);
}

export async function createProject(name: string, token: string): Promise<Project> {
  const trimmedName = name.trim();
  const response = await fetch(`${API_URL}/api/v1/init-project`, {
    method: 'POST',
    headers: authHeaders(token),
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
