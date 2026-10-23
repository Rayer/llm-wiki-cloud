import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { RawClient } from '@/components/RawClient';

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-a' },
  projects: [
    { id: 'project-a', name: 'Project A' },
    { id: 'project-b', name: 'Project B' },
  ],
  token: 'session-a',
}));
const navigation = vi.hoisted(() => ({ search: '' }));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({
  useT: () => ({
    t: (key: string, params?: Record<string, string | number>) =>
      key === 'Raw.linkedProject' ? `Showing raw content for ${params?.project}.` : key,
  }),
}));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(navigation.search) }));

type RequestRecord = { path: string; projectId: string | null };

function rawFile(name: string) {
  return { name, size: 20, updated: '', sha256: 'fixture', ingested: false };
}

function configureAuth() {
  configureApiAuth({
    getAccessToken: () => workspace.token,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
}

beforeEach(() => {
  workspace.currentProject = { id: 'project-a' };
  workspace.projects = [
    { id: 'project-a', name: 'Project A' },
    { id: 'project-b', name: 'Project B' },
  ];
  workspace.token = 'session-a';
  navigation.search = '';
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  configureAuth();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it('opens an exact Project link while another Project is selected', async () => {
  navigation.search = '?project=project-b&file=article.md';
  const requests: RequestRecord[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ path: url.pathname + url.search, projectId });
    if (url.pathname === '/api/v1/raw') {
      return Response.json({ files: projectId === 'project-b' ? [rawFile('article.md')] : [rawFile('article.md')] });
    }
    if (url.pathname === '/api/v1/raw/article.md') {
      return new Response(projectId === 'project-b' ? 'Project B saved article' : 'Project A same-name article');
    }
    return Response.json({});
  });

  render(<RawClient />);
  expect(await screen.findByText('Project B saved article')).toBeTruthy();
  expect(screen.getByText('Showing raw content for Project B.')).toBeTruthy();
  expect(workspace.currentProject.id).toBe('project-a');
  expect(requests.length).toBe(2);
  expect(requests.every((request) => request.projectId === 'project-b')).toBe(true);
  expect(document.querySelector('#raw-file-upload')).toBeNull();
});

it('does not fall back to the current Project when the linked Project is unauthorized', async () => {
  navigation.search = '?project=missing-project&file=article.md';
  const requests: RequestRecord[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ path: url.pathname + url.search, projectId });
    if (url.pathname === '/api/v1/raw') {
      return projectId === 'missing-project'
        ? Response.json({ error: 'Project access denied' }, { status: 403 })
        : Response.json({ files: [rawFile('article.md')] });
    }
    if (url.pathname === '/api/v1/raw/article.md') return new Response('wrong Project content');
    return Response.json({});
  });

  render(<RawClient />);
  await waitFor(() => expect(requests.length).toBe(1));
  expect(requests[0].projectId).toBe('missing-project');
  expect(requests.some((request) => request.projectId === 'project-a')).toBe(false);
  expect(requests.some((request) => request.path.startsWith('/api/v1/raw/article.md'))).toBe(false);
  expect(screen.queryByText('wrong Project content')).toBeNull();
});

it('ignores a delayed preview after the raw file query changes', async () => {
  navigation.search = '?project=project-b&file=first.md';
  const requests: RequestRecord[] = [];
  let resolveFirstPreview!: (response: Response) => void;
  const firstPreview = new Promise<Response>((resolve) => { resolveFirstPreview = resolve; });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ path: url.pathname + url.search, projectId });
    if (url.pathname === '/api/v1/raw') return Response.json({ files: [rawFile('first.md'), rawFile('second.md')] });
    if (url.pathname === '/api/v1/raw/first.md') return firstPreview;
    if (url.pathname === '/api/v1/raw/second.md') return new Response('second Project B article');
    return Response.json({});
  });

  const mounted = render(<RawClient />);
  await waitFor(() => expect(requests.some((request) => request.path.startsWith('/api/v1/raw/first.md'))).toBe(true));
  navigation.search = '?project=project-b&file=second.md';
  mounted.rerender(<RawClient />);
  await waitFor(() => expect(requests.some((request) => request.path.startsWith('/api/v1/raw/second.md'))).toBe(true));
  expect(await screen.findByText('second Project B article')).toBeTruthy();
  await act(async () => {
    resolveFirstPreview(new Response('stale first Project B article'));
    await firstPreview;
  });
  expect(screen.queryByText('stale first Project B article')).toBeNull();
  expect(screen.getByText('second Project B article')).toBeTruthy();
  expect(requests.every((request) => request.projectId === 'project-b')).toBe(true);
});

it('does not fall back to the selected Project after a linked Project query becomes empty', async () => {
  navigation.search = '?project=project-b&file=article.md';
  const requests: RequestRecord[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ path: url.pathname + url.search, projectId });
    if (url.pathname === '/api/v1/raw') return Response.json({ files: [rawFile('article.md')] });
    if (url.pathname === '/api/v1/raw/article.md') {
      return new Response(projectId === 'project-b' ? 'Project B saved article' : 'wrong selected Project article');
    }
    return Response.json({});
  });

  const mounted = render(<RawClient />);
  expect(await screen.findByText('Project B saved article')).toBeTruthy();
  requests.length = 0;
  navigation.search = '?project=&file=article.md';
  mounted.rerender(<RawClient />);
  expect(await screen.findByText('This raw link does not identify a Project.')).toBeTruthy();
  await act(async () => new Promise((resolve) => window.setTimeout(resolve, 20)));
  expect(requests).toEqual([]);
  expect(screen.queryByText('Project B saved article')).toBeNull();
  expect(screen.queryByText('wrong selected Project article')).toBeNull();
  expect(workspace.currentProject.id).toBe('project-a');
});

it('ignores a delayed raw list from a previous linked Project', async () => {
  navigation.search = '?project=project-b&file=old.md';
  const requests: RequestRecord[] = [];
  let resolveProjectBList!: (response: Response) => void;
  const projectBList = new Promise<Response>((resolve) => { resolveProjectBList = resolve; });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ path: url.pathname + url.search, projectId });
    if (url.pathname === '/api/v1/raw') {
      return projectId === 'project-b'
        ? projectBList
        : Response.json({ files: [rawFile('new.md')] });
    }
    if (url.pathname === '/api/v1/raw/new.md') return new Response('Project C saved article');
    if (url.pathname === '/api/v1/raw/old.md') return new Response('stale Project B article');
    return Response.json({});
  });

  const mounted = render(<RawClient />);
  await waitFor(() => expect(requests.some((request) => request.path === '/api/v1/raw' && request.projectId === 'project-b')).toBe(true));
  navigation.search = '?project=project-c&file=new.md';
  mounted.rerender(<RawClient />);
  expect(await screen.findByText('Project C saved article')).toBeTruthy();
  await act(async () => {
    resolveProjectBList(Response.json({ files: [rawFile('old.md')] }));
    await new Promise((resolve) => window.setTimeout(resolve, 20));
  });
  expect(screen.queryByText('stale Project B article')).toBeNull();
  expect(requests.some((request) => request.path === '/api/v1/raw/old.md')).toBe(false);
  expect(requests).toContainEqual({ path: '/api/v1/raw/new.md?preview=true', projectId: 'project-c' });
  expect(workspace.currentProject.id).toBe('project-a');
});
