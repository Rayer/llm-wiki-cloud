'use client';

export type Project = {
  id: string;
  name: string;
  description?: string;
  descriptionRevision?: number;
};

export const MAX_PROJECT_DESCRIPTION_LENGTH = 1000;

export function normalizeProjectDescription(value: string): string {
  return value.replace(/\r\n?/g, '\n');
}

export function projectDescriptionCodePointCount(value: string): number {
  return Array.from(normalizeProjectDescription(value)).length;
}

export function isValidProjectDescription(value: string): boolean {
  const normalized = normalizeProjectDescription(value);
  if (normalized.includes('\0') || projectDescriptionCodePointCount(normalized) > MAX_PROJECT_DESCRIPTION_LENGTH) return false;
  for (let index = 0; index < normalized.length; index += 1) {
    const code = normalized.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = normalized.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function firstString(record: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
}

function projectDescriptionFields(record: Record<string, unknown>) {
  return {
    description: typeof record.description === 'string' ? record.description : '',
    descriptionRevision: typeof record.description_revision === 'number'
      && Number.isSafeInteger(record.description_revision)
      && record.description_revision >= 0
      ? record.description_revision
      : 0,
  };
}

function projectFromRecord(record: Record<string, unknown>): Project | null {
  const id = firstString(record, ['id', 'project_id', 'projectId', 'slug']);
  const name = firstString(record, ['name', 'project_name', 'projectName', 'title', 'id']);
  return id && name ? { id, name, ...projectDescriptionFields(record) } : null;
}

export function normalizeProject(payload: unknown): Project | null {
  if (typeof payload === 'string' && payload.trim()) {
    return { id: payload.trim(), name: payload.trim(), description: '', descriptionRevision: 0 };
  }
  if (!isRecord(payload)) return null;

  if (isRecord(payload.project)) {
    return projectFromRecord(payload.project);
  }

  const direct = projectFromRecord(payload);
  if (direct) return direct;

  const projectId = firstString(payload, ['project']);
  return projectId ? { id: projectId, name: projectId, ...projectDescriptionFields(payload) } : null;
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
