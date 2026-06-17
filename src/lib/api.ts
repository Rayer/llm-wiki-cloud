'use client';

export type ApiStatus = {
  sourcesCount: number;
  conceptsCount: number;
  raw: Record<string, unknown>;
};

export type WikiEntry = {
  slug: string;
  title: string;
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
};

export type SearchResponse = {
  results: SearchResult[];
  aiAnswer: string;
  citations: Citation[];
};

const API_URL =
  process.env.NEXT_PUBLIC_API_URL ??
  'https://llm-wiki-cloud-bff.rayer.idv.tw';

async function requestJson<T>(path: string): Promise<T> {
  const response = await fetch(`${API_URL}${path}`);

  if (!response.ok) {
    throw new Error(`API request failed (${response.status})`);
  }

  return response.json() as Promise<T>;
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(`${API_URL}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
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

function extractArray(payload: unknown): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];

  for (const key of ['items', 'results', 'sources', 'concepts', 'data']) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }

  return [];
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

export function normalizeSearchResponse(payload: unknown): SearchResponse {
  const record = isRecord(payload) ? payload : {};
  const citationItems = Array.isArray(record.citations) ? record.citations : [];

  return {
    results: extractArray(payload).map(normalizeSearchResult),
    aiAnswer: firstString(record, ['ai_synth', 'ai_answer', 'aiAnswer', 'answer']) ?? '',
    citations: citationItems
      .map(normalizeCitation)
      .filter((citation): citation is Citation => citation !== null),
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

  return { sourcesCount, conceptsCount, raw: record };
}

export async function getStatus() {
  return normalizeStatus(await requestJson<unknown>('/api/status'));
}

export async function getSources() {
  return extractArray(await requestJson<unknown>('/api/sources')).map(normalizeEntry);
}

export async function getSource(slug: string) {
  return normalizeEntry(
    await requestJson<unknown>(`/api/sources/${encodeURIComponent(slug)}`),
  );
}

export async function getConcepts() {
  return extractArray(await requestJson<unknown>('/api/concepts')).map(normalizeEntry);
}

export async function getConcept(slug: string) {
  return normalizeEntry(
    await requestJson<unknown>(`/api/concepts/${encodeURIComponent(slug)}`),
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
};

export async function uploadRawFile(file: File): Promise<RawUploadResult> {
  const formData = new FormData();
  formData.append('file', file);
  const response = await fetch(`${API_URL}/api/raw/upload`, {
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
  const response = await fetch(`${API_URL}/api/raw/scrape`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url, filename }),
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Scrape failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function triggerPipeline(): Promise<PipelineResult> {
  const response = await fetch(`${API_URL}/api/pipeline/run`, {
    method: 'POST',
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Pipeline trigger failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function generateTitle(content: string): Promise<string> {
  const response = await fetch(`${API_URL}/api/raw/generate-title`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  if (!response.ok) return 'Untitled';
  const data = await response.json() as { title: string };
  return data.title ?? 'Untitled';
}
