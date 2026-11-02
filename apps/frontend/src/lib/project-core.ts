'use client';

export type Project = {
  id: string;
  name: string;
  createdAt: string | null;
  lastUsedAt: string | null;
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

function validRFC3339Epoch(value: unknown): number | null {
  if (typeof value !== 'string') return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|([+-])(\d{2}):(\d{2}))$/.exec(value);
  if (!match) return null;

  const [, yearText, monthText, dayText, hourText, minuteText, secondText, zone, , offsetHourText, offsetMinuteText] = match;
  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  const offsetHour = Number(offsetHourText ?? 0);
  const offsetMinute = Number(offsetMinuteText ?? 0);
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (
    month < 1 || month > 12 || day < 1 || day > daysInMonth[month - 1]
    || hour > 23 || minute > 59 || second > 59
    || (zone !== 'Z' && (offsetHour > 23 || offsetMinute > 59))
  ) return null;

  const epoch = Date.parse(value);
  return Number.isFinite(epoch) ? epoch : null;
}

function projectTimestampFields(record: Record<string, unknown>) {
  const createdAt = validRFC3339Epoch(record.created_at) === null ? null : record.created_at as string;
  const lastUsedAt = validRFC3339Epoch(record.last_used_at) === null ? null : record.last_used_at as string;
  return { createdAt, lastUsedAt };
}

function projectFromRecord(record: Record<string, unknown>): Project | null {
  const id = firstString(record, ['id', 'project_id', 'projectId', 'slug']);
  const name = firstString(record, ['name', 'project_name', 'projectName', 'title', 'id']);
  return id && name ? { id, name, ...projectTimestampFields(record), ...projectDescriptionFields(record) } : null;
}

export function normalizeProject(payload: unknown): Project | null {
  if (typeof payload === 'string' && payload.trim()) {
    return { id: payload.trim(), name: payload.trim(), createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0 };
  }
  if (!isRecord(payload)) return null;

  if (isRecord(payload.project)) {
    return projectFromRecord(payload.project);
  }

  const direct = projectFromRecord(payload);
  if (direct) return direct;

  const projectId = firstString(payload, ['project']);
  return projectId
    ? { id: projectId, name: projectId, ...projectTimestampFields(payload), ...projectDescriptionFields(payload) }
    : null;
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
    .filter((project): project is Project => project !== null)
    .sort(compareProjectsByLastUse);
}

function ordinalCompare(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

export function compareProjectsByLastUse(left: Project, right: Project): number {
  const leftLastUsed = validRFC3339Epoch(left.lastUsedAt);
  const rightLastUsed = validRFC3339Epoch(right.lastUsedAt);
  if (leftLastUsed !== null || rightLastUsed !== null) {
    if (leftLastUsed === null) return 1;
    if (rightLastUsed === null) return -1;
    return rightLastUsed - leftLastUsed || ordinalCompare(left.id, right.id);
  }

  const leftCreated = validRFC3339Epoch(left.createdAt);
  const rightCreated = validRFC3339Epoch(right.createdAt);
  if (leftCreated === null && rightCreated !== null) return 1;
  if (leftCreated !== null && rightCreated === null) return -1;
  if (leftCreated !== null && rightCreated !== null) {
    return rightCreated - leftCreated || ordinalCompare(left.id, right.id);
  }
  return ordinalCompare(left.id, right.id);
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
