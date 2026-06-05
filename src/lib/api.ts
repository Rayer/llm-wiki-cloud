'use client';

export type ApiStatus = {
  sourcesCount: number;
  conceptsCount: number;
  raw: Record<string, unknown>;
};

export type WikiEntry = {
  slug: string;
  title: string;
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

const API_URL =
  process.env.NEXT_PUBLIC_API_URL ??
  'https://llm-wiki-bff-580854833715.asia-east1.run.app';

async function requestJson<T>(path: string): Promise<T> {
  const response = await fetch(`${API_URL}${path}`);

  if (!response.ok) {
    throw new Error(`API request failed (${response.status})`);
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
  const params = new URLSearchParams({ q: query, mode });
  return extractArray(await requestJson<unknown>(`/api/query?${params}`)).map(
    normalizeSearchResult,
  );
}
