'use client';

import { rawFileNameFromSource } from './raw-file-name.ts';
import { normalizeAnnotationBody, normalizeAnnotationGeneration } from './source-annotation.ts';

export type PipelineDiagnostic = {
  stage?: string | null;
  error_class?: string | null;
  detail_code?: string | null;
  child_command?: string | null;
  exit_code?: number | null;
};

export type PipelineExecution = {
  name?: string | null;
  status?: 'RUNNING' | 'SUCCEEDED' | 'FAILED' | string;
  duration?: string | number | null;
  started_at?: string | null;
  finished_at?: string | null;
  log_url?: string | null;
  log_state?: 'pending' | 'available' | 'unavailable' | 'missing' | string | null;
  log_state_reason?: string | null;
  diagnostic?: PipelineDiagnostic | null;
  [key: string]: unknown;
};

export type ApiStatus = {
  sourcesCount: number;
  conceptsCount: number;
  rawCount: number;
  suggestedQueries: string[];
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
  annotationAllowed?: boolean;
  raw: unknown;
};

export type SourceLifecycle = 'new' | 'synced' | 'notes_pending' | 'content_pending' | 'error';

export type SourceListItem = {
  id?: string;
  slug: string;
  title: string;
  rawPath: string;
  lifecycle: SourceLifecycle;
  annotationPresent: boolean;
  annotationAllowed: boolean;
  error?: string;
  raw: unknown;
};

export type SourceAnnotation = {
  body: string;
  expectedGeneration: string;
  hasAnnotation: boolean;
  annSha256?: string;
  annotationDirty: boolean;
  rawDirty: boolean;
  dirty: boolean;
  lifecycleStatus?: string;
  updatedAt?: string;
  updatedBy?: string;
};

export type RawFile = {
  name: string;
  size: number;
  updated: string;
  sha256: string;
  ingested: boolean;
  raw: unknown;
};

export type SearchResult = WikiEntry & {
  excerpt?: string;
  score?: number;
  type?: string;
};

export type Citation = {
  text: string;
  slug?: string;
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

export type BuildInfo = {
  product_version: string;
  commit: string;
  branch: string;
  tag: string;
  image_tag: string;
  service: string;
  revision: string;
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

  throw new Error('Authentication required');
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

async function requestJson<T>(path: string, options: Pick<ApiFetchOptions, 'projectId'> = {}): Promise<T> {
  const response = await apiFetch(path, options);

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

export function citationPathSegment(type: 'concept' | 'source', path: unknown): string | null {
  if (typeof path !== 'string') return null;

  const match = path.match(/^\/(concepts|sources)\/([^/?#]+)(?:[?#].*)?$/);
  if (!match) return null;

  if (
    (type === 'concept' && match[1] !== 'concepts')
    || (type === 'source' && match[1] !== 'sources')
  ) {
    return null;
  }

  const segment = match[2];
  if (!segment) return null;

  try {
    const decoded = decodeURIComponent(segment);
    return safeWikiRouteSegment(decoded);
  } catch {
    return null;
  }
}

export function safeWikiRouteSegment(value: unknown): string | null {
  if (typeof value !== 'string' || !value || value !== value.trim()) return null;
  if (
    value === '.'
    || value === '..'
    || value.includes('/')
    || value.includes('\\')
    || /[\u0000-\u001f\u007f]/.test(value)
  ) {
    return null;
  }
  return value;
}

function asExitCode(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= 255
    ? value
    : null;
}

function normalizePipelineDiagnostic(value: unknown): PipelineDiagnostic | null {
  if (!isRecord(value)) return null;
  return {
    stage: asString(value.stage) ?? null,
    error_class: asString(value.error_class) ?? null,
    detail_code: asString(value.detail_code) ?? null,
    child_command: asString(value.child_command) ?? null,
    exit_code: asExitCode(value.exit_code),
  };
}

function normalizePipelineExecution(value: unknown): PipelineExecution | null {
  if (!isRecord(value)) return null;
  return {
    ...value,
    name: asString(value.name) ?? null,
    status: asString(value.status),
    log_url: asString(value.log_url) ?? null,
    log_state: typeof value.log_state === 'string' ? value.log_state : null,
    log_state_reason: asString(value.log_state_reason) ?? null,
    diagnostic: normalizePipelineDiagnostic(value.diagnostic),
  };
}

function asBoolean(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined;
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
    annotationAllowed: asBoolean(record.annotation_allowed) ?? asBoolean(record.annotationAllowed),
    raw: item,
  };
}

function normalizeSourceLifecycle(value: unknown): SourceLifecycle {
  switch (value) {
    case 'new':
    case 'synced':
    case 'notes_pending':
    case 'content_pending':
    case 'error':
      return value;
    default:
      return 'synced';
  }
}

export function normalizeSourceListItem(item: unknown): SourceListItem {
  const entry = normalizeEntry(item);
  const record = isRecord(item) ? item : {};
  const rawPath = firstString(record, ['raw_path', 'rawPath', 'raw_file_path', 'rawFilePath', 'source_file', 'path']) ?? entry.slug;

  return {
    id: firstString(record, ['id', 'source_id', 'sourceId']),
    slug: entry.slug,
    title: firstString(record, ['title', 'name']) ?? rawPath,
    rawPath,
    lifecycle: normalizeSourceLifecycle(firstString(record, ['lifecycle_status', 'lifecycleStatus', 'lifecycle', 'lifecycle_state', 'lifecycleState', 'state', 'status'])),
    annotationPresent: asBoolean(record.annotation_present) ?? asBoolean(record.annotationPresent) ?? asBoolean(record.has_annotation) ?? false,
    annotationAllowed: asBoolean(record.annotation_allowed) ?? asBoolean(record.annotationAllowed) ?? false,
    error: firstString(record, ['error', 'error_message', 'errorMessage']),
    raw: item,
  };
}

function normalizeAnnotation(payload: unknown): SourceAnnotation {
  const record = isRecord(payload) ? payload : {};
  const generation = normalizeAnnotationGeneration(
    record.expected_generation ?? record.expectedGeneration ?? record.generation,
  );
  if (!generation) {
    throw new ApiError('Invalid annotation response: expected_generation is required', 500);
  }
  return {
    body: firstString(record, ['body', 'annotation']) ?? '',
    expectedGeneration: generation,
    hasAnnotation: asBoolean(record.has_annotation) ?? asBoolean(record.hasAnnotation) ?? false,
    annSha256: firstString(record, ['ann_sha256', 'annSha256']),
    annotationDirty: asBoolean(record.annotation_dirty) ?? asBoolean(record.annotationDirty) ?? false,
    rawDirty: asBoolean(record.raw_dirty) ?? asBoolean(record.rawDirty) ?? false,
    dirty: asBoolean(record.dirty) ?? false,
    lifecycleStatus: firstString(record, ['lifecycle_status', 'lifecycleStatus']),
    updatedAt: firstString(record, ['updated_at', 'updatedAt']),
    updatedBy: firstString(record, ['updated_by', 'updatedBy']),
  };
}

export { normalizeAnnotationBody };

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
  const rawType = firstString(record, ['type', 'kind', 'collection']);
  const path = firstString(record, ['path', 'href', 'url']);
  const explicitSlug = safeWikiRouteSegment(firstString(record, ['slug']));
  const explicitId = safeWikiRouteSegment(firstString(record, ['id', 'concept_id', 'source_id', 'sourceId', 'conceptId']));

  const normalizedType = rawType?.replace(/s$/, '');
  if (normalizedType !== 'concept' && normalizedType !== 'source') return null;

  const pathSlug = citationPathSegment(normalizedType, path);
  const slug = explicitSlug || pathSlug;
  if (!text || (!explicitId && !slug)) return null;

  const safePath = pathSlug ? path : undefined;

  return {
    text,
    slug: slug ?? undefined,
    type: normalizedType,
    id: explicitId ?? undefined,
    path: safePath,
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
  const rawCount =
    firstNumber(record, ['rawCount', 'raw_count', 'filesCount', 'files_count']) ?? 0;

  const lastExecution = normalizePipelineExecution(record.last_execution);
  const suggestedQueries = stringArray(record.suggested_queries ?? record.suggestedQueries);

  return { sourcesCount, conceptsCount, rawCount, suggestedQueries, lastExecution, raw: record };
}

export function normalizeRawFile(item: unknown): RawFile {
  const record = isRecord(item) ? item : {};
  const name = firstString(record, ['name', 'filename', 'path']) ?? 'unnamed';

  return {
    name,
    size: firstNumber(record, ['size', 'bytes']) ?? 0,
    updated: firstString(record, ['updated', 'updated_at', 'updatedAt']) ?? '',
    sha256: firstString(record, ['sha256', 'digest']) ?? '',
    ingested: asBoolean(record.ingested) ?? false,
    raw: item,
  };
}

export async function getStatus(projectId?: string) {
  return normalizeStatus(await requestJson<unknown>('/api/v1/status', { projectId }));
}

export async function getRawFiles() {
  return extractNamedArray(await requestJson<unknown>('/api/v1/raw'), ['files', 'items', 'results', 'data'])
    .map(normalizeRawFile);
}

export async function getRawFilePreview(filename: string): Promise<string> {
  const rawFileName = rawFileNameFromSource(filename);
  const response = await apiFetch(`/api/v1/raw/${encodeURIComponent(rawFileName)}?preview=true`);

  if (!response.ok) {
    throw new ApiError(`Raw preview request failed (${response.status})`, response.status);
  }

  return response.text();
}

export async function getSources() {
  return extractArray(await requestJson<unknown>('/api/v1/sources')).map(normalizeSourceListItem);
}

export async function getSource(slug: string) {
	const safeSlug = safeWikiRouteSegment(slug);
	if (!safeSlug) throw new ApiError('Invalid source lookup segment', 400);
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/sources/${encodeURIComponent(safeSlug)}`),
  );
}

export async function getSourceAnnotation(sourceId: string): Promise<SourceAnnotation> {
  const response = await apiFetch(`/api/v1/sources/${encodeURIComponent(sourceId)}/annotation`);
  if (!response.ok) {
    throw new ApiError(`Annotation request failed (${response.status})`, response.status);
  }
  return normalizeAnnotation(await response.json());
}

export async function updateSourceAnnotation(
  sourceId: string,
  body: string,
  expectedGeneration: string,
): Promise<SourceAnnotation> {
  const response = await apiFetch(`/api/v1/sources/${encodeURIComponent(sourceId)}/annotation`, {
    method: 'PUT',
    json: true,
    body: JSON.stringify({ body: normalizeAnnotationBody(body), expected_generation: String(expectedGeneration) }),
  });
  if (!response.ok) {
    throw new ApiError(`Annotation update failed (${response.status})`, response.status);
  }
  return normalizeAnnotation(await response.json());
}

export async function getConcepts() {
  return extractArray(await requestJson<unknown>('/api/v1/concepts')).map(normalizeEntry);
}

export async function getConcept(slug: string) {
	const safeSlug = safeWikiRouteSegment(slug);
	if (!safeSlug) throw new ApiError('Invalid concept lookup segment', 400);
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/concepts/${encodeURIComponent(safeSlug)}`),
  );
}

export async function searchWiki(query: string, mode: 'wiki' | 'full') {
  return normalizeSearchResponse(
    await postJson<unknown>('/api/query', { q: query, mode }),
  );
}

// ── Raw content management ──

export type RawUploadStatus = 'created' | 'already_exists';

export type RawUploadResult = {
  filename: string;
  path: string;
  bytes: number;
  sha256: string;
  status: RawUploadStatus;
};

export const RAW_UPLOAD_MAX_BYTES = 5 << 20;

export type ScrapeResult = {
  message: string;
  filename: string;
  path: string;
  title: string;
  digest: string;
  bytes: number;
};

export type PipelineQuota = {
  enforced: boolean;
  allowed: boolean;
  reason?: string;
  message?: string;
  runs_today: number;
  daily_limit: number;
  cooldown_until?: string | null;
  next_reset?: string | null;
  new_raw_files: number;
  min_new_raw: number;
  already_running: boolean;
};

export type PipelineResult = {
  status?: 'accepted' | string;
  execution_id?: string;
  project_id?: string;
  message?: string;
  command?: string;
  quota?: PipelineQuota | null;
  rawFiles?: number;
  scheduled?: boolean;
};

export type PipelineStatus = {
  last_execution?: PipelineExecution | null;
  project_id?: string;
  quota?: PipelineQuota | null;
  suggested_queries?: string[];
};

export async function uploadRawFile(file: File): Promise<RawUploadResult> {
  if (file.size > RAW_UPLOAD_MAX_BYTES) {
    throw new Error('file too large (max 5 MiB)');
  }
  if (file.size === 0) {
    throw new Error('empty file');
  }

  const formData = new FormData();
  formData.append('file', file);
  const response = await apiFetch('/api/v1/raw/upload', {
    method: 'POST',
    body: formData,
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Upload failed' }));
    const message = (error as { error?: string }).error || 'Upload failed';
    throw new ApiError(message, response.status);
  }

  const body = (await response.json()) as Partial<RawUploadResult> & Record<string, unknown>;
  const status = body.status === 'already_exists' ? 'already_exists' : 'created';
  return {
    filename: typeof body.filename === 'string' ? body.filename : file.name,
    path: typeof body.path === 'string' ? body.path : '',
    bytes: typeof body.bytes === 'number' ? body.bytes : file.size,
    sha256: typeof body.sha256 === 'string' ? body.sha256 : '',
    status: response.status === 200 ? 'already_exists' : status,
  };
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

export async function getPipelineLog(logUrl: string, projectId?: string): Promise<string> {
  const response = await apiFetch(logUrl, { projectId });
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

export type PublicConfig = {
  registration_enabled: boolean;
};

export type AdminSettings = {
  registration_enabled: boolean;
};

let publicConfigCache: PublicConfig | null = null;

export function clearPublicConfigCache(): void {
  publicConfigCache = null;
}

/**
 * Public config — no auth.
 * Fail-closed: network/invalid/error → registration_enabled false (safer for 1.0).
 */
export async function getPublicConfig(options?: { refresh?: boolean }): Promise<PublicConfig> {
  if (!options?.refresh && publicConfigCache) return publicConfigCache;

  try {
    const response = await fetch(`${API_URL}/api/v1/public/config`, {
      method: 'GET',
      credentials: 'omit',
    });
    if (!response.ok) {
      const closed = { registration_enabled: false };
      publicConfigCache = closed;
      return closed;
    }
    const payload = (await response.json().catch(() => null)) as unknown;
    const record = isRecord(payload) ? payload : {};
    const enabled = asBoolean(record.registration_enabled);
    const config = { registration_enabled: enabled === true };
    publicConfigCache = config;
    return config;
  } catch {
    const closed = { registration_enabled: false };
    publicConfigCache = closed;
    return closed;
  }
}

export async function getBuildInfo(): Promise<BuildInfo> {
  let response: Response;

  try {
    response = await fetch(`${API_URL}/api/v1/public/version`, {
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : 'unknown network error';
    throw new Error(`Build info request failed: ${message}`);
  }

  if (!response.ok) {
    throw new ApiError(`Build info request failed (${response.status})`, response.status);
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new Error('Invalid build info response JSON');
  }

  if (!isRecord(payload)) {
    throw new Error('Invalid build info response: expected an object');
  }

  const field = (key: keyof BuildInfo): string => {
    const value = payload[key];
    if (typeof value !== 'string') {
      throw new Error(`Invalid build info response: ${key} must be a string`);
    }
    return value;
  };

  return {
    product_version: field('product_version'),
    commit: field('commit'),
    branch: field('branch'),
    tag: field('tag'),
    image_tag: field('image_tag'),
    service: field('service'),
    revision: field('revision'),
  };
}

export async function getAdminSettings(): Promise<AdminSettings> {
  const payload = await adminJson('/api/v1/admin/settings');
  const record = isRecord(payload) ? payload : {};
  const enabled = asBoolean(record.registration_enabled);
  if (enabled === undefined) {
    throw new ApiError('Invalid admin settings response', 500);
  }
  return { registration_enabled: enabled };
}

export async function updateAdminSettings(
  settings: Partial<AdminSettings>,
): Promise<AdminSettings> {
  const payload = await adminJson('/api/v1/admin/settings', {
    method: 'PATCH',
    json: true,
    body: JSON.stringify(settings),
  });
  const record = isRecord(payload) ? payload : {};
  const enabled = asBoolean(record.registration_enabled);
  if (enabled === undefined) {
    throw new ApiError('Invalid admin settings response', 500);
  }
  clearPublicConfigCache();
  return { registration_enabled: enabled };
}
