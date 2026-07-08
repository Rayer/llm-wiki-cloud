'use client';

export type PipelineExecution = {
  status?: 'RUNNING' | 'SUCCEEDED' | 'FAILED' | string;
  duration?: string | number | null;
  started_at?: string | null;
  finished_at?: string | null;
  log_url?: string | null;
  [key: string]: unknown;
};

export type ApiStatus = {
  sourcesCount: number;
  conceptsCount: number;
  lastExecution?: PipelineExecution | null;
  raw: Record<string, unknown>;
};

export type WikiEntry = {
  slug: string;
  title: string;
  id?: string;
  status?: string;
  description?: string;
  date?: string;
  frontmatter?: Record<string, unknown>;
  content?: string;
  raw: unknown;
};

export type SearchResult = WikiEntry & {
  excerpt?: string;
  score?: number;
  type?: string;
};

export type Citation = {
  text: string;
  slug: string;
  type: 'concept' | 'source';
  path?: string;
  id?: string;
};

export type QueryExpansion = {
  keywords: string[];
};

export type SearchResponse = {
  results: SearchResult[];
  aiAnswer: string;
  citations: Citation[];
  expand?: QueryExpansion;
};

export type AdminProject = {
  id: string;
  name: string;
  userId: string;
  conceptCount: number;
  sourceCount: number;
};

export type AdminUser = {
  id: string;
  email: string;
  role: string;
  projectCount: number;
};

export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

const API_URL =
  process.env.NEXT_PUBLIC_API_URL ??
  'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app';

const LAST_PROJECT_KEY = 'llm-wiki-last-project';

type ApiAuthConfig = {
  getAccessToken: () => string | null;
  refreshAccessToken: () => Promise<string | null>;
  onUnauthorized: () => void;
};

let apiAuthConfig: ApiAuthConfig = {
  getAccessToken: () => null,
  refreshAccessToken: async () => null,
  onUnauthorized: () => undefined,
};

export function configureApiAuth(config: ApiAuthConfig): void {
  apiAuthConfig = config;
}

export function toV1Path(path: string): string {
  if (path.startsWith('/api/v1/')) return path;
  return path.startsWith('/api/') ? `/api/v1/${path.slice('/api/'.length)}` : path;
}

export function buildProjectHeaders(
  projectId: string,
  accessToken: string,
  json = false,
): Record<string, string> {
  return {
    Authorization: `Bearer ${accessToken}`,
    ...(json ? { 'Content-Type': 'application/json' } : {}),
    'X-Project-ID': projectId,
  };
}

type BuildRequestInitOptions = {
  method?: string;
  projectId?: string;
  accessToken?: string | null;
  json?: boolean;
  body?: BodyInit;
};

export function buildRequestInit({
  method,
  projectId,
  accessToken,
  json = false,
  body,
}: BuildRequestInitOptions): RequestInit {
  const headers: Record<string, string> = {
    ...(json ? { 'Content-Type': 'application/json' } : {}),
  };

  if (accessToken) {
    headers.Authorization = `Bearer ${accessToken}`;
  }

  if (projectId) {
    headers['X-Project-ID'] = projectId;
  }

  return {
    ...(method ? { method } : {}),
    credentials: 'include',
    headers,
    ...(body === undefined ? {} : { body }),
  };
}

function selectedProjectId(): string {
  const projectId = window.localStorage.getItem(LAST_PROJECT_KEY);
  if (!projectId) throw new Error('Please select a project first');
  return projectId;
}

async function accessTokenOrRefresh(): Promise<string> {
  const current = apiAuthConfig.getAccessToken();
  if (current) return current;

  const refreshed = await apiAuthConfig.refreshAccessToken();
  if (refreshed) return refreshed;

  apiAuthConfig.onUnauthorized();
  throw new Error('Please log in to continue.');
}

type ApiFetchOptions = {
  method?: string;
  projectId?: string;
  json?: boolean;
  body?: BodyInit;
  requireProject?: boolean;
};

export async function apiFetch(path: string, options: ApiFetchOptions = {}): Promise<Response> {
  const projectId = options.requireProject === false
    ? options.projectId
    : options.projectId ?? selectedProjectId();
  const accessToken = await accessTokenOrRefresh();
  const url = `${API_URL}${toV1Path(path)}`;
  const init = buildRequestInit({ ...options, projectId, accessToken });
  const response = await fetch(url, init);

  if (response.status !== 401) return response;

  const refreshed = await apiAuthConfig.refreshAccessToken();
  if (!refreshed) {
    apiAuthConfig.onUnauthorized();
    return response;
  }

  return fetch(url, buildRequestInit({ ...options, projectId, accessToken: refreshed }));
}

async function requestJson<T>(path: string): Promise<T> {
  const response = await apiFetch(path);

  if (!response.ok) {
    throw new ApiError(`API request failed (${response.status})`, response.status);
  }

  return response.json() as Promise<T>;
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
  const response = await apiFetch(path, {
    method: 'POST',
    json: true,
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: `API request failed (${response.status})` }));
    throw new Error((error as { error: string }).error);
  }

  return response.json() as Promise<T>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function asString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined;
}

function asNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function firstString(record: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = asString(record[key]);
    if (value) return value;
  }
}

function firstNumber(record: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = asNumber(record[key]);
    if (value !== undefined) return value;
  }
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
    : [];
}

function extractArray(payload: unknown): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];

  for (const key of ['items', 'results', 'sources', 'concepts', 'data']) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }

  return [];
}

function extractNamedArray(payload: unknown, keys: string[]): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];
  for (const key of keys) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }
  return [];
}

function normalizeAdminProject(item: unknown): AdminProject | null {
  const record = isRecord(item) ? item : {};
  const id = firstString(record, ['id', 'project_id', 'projectId']);
  const name = firstString(record, ['name', 'project_name', 'projectName']) ?? id;
  const userId = firstString(record, ['user_id', 'userId', 'uid']) ?? '';
  if (!id || !name) return null;
  return {
    id,
    name,
    userId,
    conceptCount: firstNumber(record, ['concept_count', 'conceptCount', 'concepts_count', 'conceptsCount']) ?? 0,
    sourceCount: firstNumber(record, ['source_count', 'sourceCount', 'sources_count', 'sourcesCount']) ?? 0,
  };
}

function normalizeAdminUser(item: unknown): AdminUser | null {
  const record = isRecord(item) ? item : {};
  const id = firstString(record, ['id', 'user_id', 'userId']);
  const email = firstString(record, ['email']) ?? '';
  if (!id) return null;
  return {
    id,
    email,
    role: firstString(record, ['role']) ?? 'user',
    projectCount: firstNumber(record, ['project_count', 'projectCount', 'projects_count', 'projectsCount']) ?? 0,
  };
}

export function normalizeEntry(item: unknown): WikiEntry {
  if (typeof item === 'string') {
    return { slug: item, title: item.replaceAll('-', ' '), raw: item };
  }

  const record = isRecord(item) ? item : {};
  const frontmatter = isRecord(record.frontmatter)
    ? record.frontmatter
    : isRecord(record.metadata)
      ? record.metadata
      : undefined;
  const title =
    firstString(record, ['title', 'name', 'slug', 'id']) ??
    firstString(frontmatter ?? {}, ['title', 'name']) ??
    'Untitled';
  const slug =
    firstString(record, ['slug', 'id', 'path']) ??
    title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');

  return {
    slug,
    title,
    id: firstString(record, ['id']) ?? firstString(frontmatter ?? {}, ['id']),
    status: firstString(record, ['status']) ??
      firstString(frontmatter ?? {}, ['status']),
    description: firstString(record, ['description', 'summary', 'excerpt']) ??
      firstString(frontmatter ?? {}, ['description', 'summary']),
    date: firstString(record, ['date', 'createdAt', 'updatedAt']) ??
      firstString(frontmatter ?? {}, ['date', 'createdAt', 'updatedAt']),
    frontmatter,
    content: firstString(record, ['content', 'markdown', 'body', 'text']),
    raw: item,
  };
}

export function normalizeSearchResult(item: unknown): SearchResult {
  const entry = normalizeEntry(item);
  const record = isRecord(item) ? item : {};

  return {
    ...entry,
    excerpt: firstString(record, ['excerpt', 'snippet', 'summary', 'description']),
    score: firstNumber(record, ['score', 'rank']),
    type: firstString(record, ['type', 'kind', 'collection']),
  };
}

export function normalizeCitation(item: unknown): Citation | null {
  const record = isRecord(item) ? item : {};
  const text = firstString(record, ['text', 'title', 'name']);
  const slug = firstString(record, ['slug', 'id', 'path']);
  const rawType = firstString(record, ['type', 'kind', 'collection']);

  if (!text || !slug) return null;

  const normalizedType = rawType?.replace(/s$/, '');
  if (normalizedType !== 'concept' && normalizedType !== 'source') return null;

  return {
    text,
    slug,
    type: normalizedType,
    path: firstString(record, ['path', 'href', 'url']),
  };
}

function normalizeQueryExpansion(value: unknown): QueryExpansion | undefined {
  const record = isRecord(value) ? value : {};
  const keywords = stringArray(record.keywords);
  return keywords.length > 0 ? { keywords } : undefined;
}

export function normalizeSearchResponse(payload: unknown): SearchResponse {
  const record = isRecord(payload) ? payload : {};
  const citationItems = Array.isArray(record.citations) ? record.citations : [];

  return {
    results: extractArray(payload).map(normalizeSearchResult),
    aiAnswer: firstString(record, ['ai_synth', 'ai_answer', 'aiAnswer', 'answer']) ?? '',
    citations: citationItems
      .map(normalizeCitation)
      .filter((citation): citation is Citation => citation !== null),
    expand: normalizeQueryExpansion(record.expand),
  };
}

export function normalizeStatus(payload: unknown): ApiStatus {
  const record = isRecord(payload) ? payload : {};
  const sourcesCount =
    firstNumber(record, ['sourcesCount', 'sourceCount', 'sources_count']) ??
    (Array.isArray(record.sources) ? record.sources.length : 0);
  const conceptsCount =
    firstNumber(record, ['conceptsCount', 'conceptCount', 'concepts_count']) ??
    (Array.isArray(record.concepts) ? record.concepts.length : 0);

  const lastExecution = isRecord(record.last_execution)
    ? record.last_execution as PipelineExecution
    : null;

  return { sourcesCount, conceptsCount, lastExecution, raw: record };
}

export async function getStatus() {
  return normalizeStatus(await requestJson<unknown>('/api/v1/status'));
}

export async function getSources() {
  return extractArray(await requestJson<unknown>('/api/v1/sources')).map(normalizeEntry);
}

export async function getSource(slug: string) {
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/sources/${encodeURIComponent(slug)}`),
  );
}

export async function getConcepts() {
  return extractArray(await requestJson<unknown>('/api/v1/concepts')).map(normalizeEntry);
}

export async function getConcept(slug: string) {
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/concepts/${encodeURIComponent(slug)}`),
  );
}

export async function searchWiki(query: string, mode: 'wiki' | 'full') {
  return normalizeSearchResponse(
    await postJson<unknown>('/api/query', { q: query, mode }),
  );
}

// ── Raw content management ──

export type RawUploadResult = {
  message: string;
  filename: string;
  path: string;
  digest: string;
  bytes: number;
};

export type ScrapeResult = {
  message: string;
  filename: string;
  path: string;
  title: string;
  digest: string;
  bytes: number;
};

export type PipelineResult = {
  message: string;
  rawFiles: number;
  scheduled: boolean;
  status?: 'accepted' | string;
};

export type PipelineStatus = {
  last_execution?: PipelineExecution | null;
  [key: string]: unknown;
};

export async function uploadRawFile(file: File): Promise<RawUploadResult> {
  const formData = new FormData();
  formData.append('file', file);
  const response = await apiFetch('/api/v1/raw/upload', {
    method: 'POST',
    body: formData,
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Upload failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function scrapeUrl(url: string, filename?: string): Promise<ScrapeResult> {
  const response = await apiFetch('/api/v1/raw/scrape', {
    method: 'POST',
    json: true,
    body: JSON.stringify({ url, filename }),
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Scrape failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function triggerPipeline(): Promise<PipelineResult> {
  const response = await apiFetch('/api/v1/pipeline/run', {
    method: 'POST',
    json: true,
    requireProject: true,
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Pipeline trigger failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function getPipelineStatus(): Promise<PipelineStatus> {
  return requestJson<PipelineStatus>('/api/v1/pipeline/status');
}

export async function getPipelineLog(logUrl: string): Promise<string> {
  const response = await apiFetch(logUrl);
  if (!response.ok) {
    throw new Error(`Pipeline log request failed (${response.status})`);
  }
  return response.text();
}

export async function generateTitle(content: string): Promise<string> {
  const response = await apiFetch('/api/v1/raw/generate-title', {
    method: 'POST',
    json: true,
    body: JSON.stringify({ content }),
  });
  if (!response.ok) return 'Untitled';
  const data = await response.json() as { title: string };
  return data.title ?? 'Untitled';
}

async function adminJson(path: string, options: Omit<ApiFetchOptions, 'requireProject'> = {}): Promise<unknown> {
  const response = await apiFetch(path, { ...options, requireProject: false });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: `API request failed (${response.status})` }));
    throw new ApiError(
      (error as { error?: string }).error ?? `API request failed (${response.status})`,
      response.status,
    );
  }
  return response.json().catch(() => ({}));
}

export async function getAdminProjects(): Promise<AdminProject[]> {
  const payload = await adminJson('/api/v1/admin/projects');
  return extractNamedArray(payload, ['projects', 'items', 'results', 'data'])
    .map(normalizeAdminProject)
    .filter((project): project is AdminProject => project !== null);
}

export async function renameAdminProject(id: string, name: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ name }),
  });
}

export async function deleteAdminProject(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function rebuildAdminProjectIndex(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/rebuild-index`, { method: 'POST' });
}

export async function triggerAdminProjectPipeline(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/pipeline`, { method: 'POST' });
}

export async function getAdminUsers(): Promise<AdminUser[]> {
  const payload = await adminJson('/api/v1/admin/users');
  return extractNamedArray(payload, ['users', 'items', 'results', 'data'])
    .map(normalizeAdminUser)
    .filter((user): user is AdminUser => user !== null);
}

export async function updateAdminUserRole(id: string, role: string): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ role }),
  });
}

export async function deleteAdminUser(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
