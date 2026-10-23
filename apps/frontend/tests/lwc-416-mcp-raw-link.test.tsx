import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { RawClient } from '@/components/RawClient';

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-a' },
  token: 'session-a',
}));
const navigation = vi.hoisted(() => ({ search: '' }));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
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
