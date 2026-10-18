import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const auth = vi.hoisted(() => ({
  token: 'synthetic-session',
  user: { id: 'synthetic-owner', email: 'qa-owner@example.test', role: 'owner' },
  sessionEpoch: 1,
  push: vi.fn(),
  replace: vi.fn(),
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: auth.push, replace: auth.replace }),
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: auth.token,
    access_token: auth.token,
    user: auth.user,
    hydrated: true,
    isAuthenticated: true,
    isDemoSession: false,
    sessionEpoch: auth.sessionEpoch,
    login: async () => undefined,
    loginAsDemo: async () => undefined,
    register: async () => undefined,
    logout: async () => undefined,
  }),
}));

import { NavigationBlockerProvider } from '@/components/NavigationBlocker';
import { RawClient } from '@/components/RawClient';
import { WorkspaceProvider, useWorkspace } from '@/components/WorkspaceProvider';
import { configureApiAuth } from '@/lib/api';
import { setRuntimeConfigForTests } from '@/lib/runtime-config';

type RequestRecord = { method: string; path: string; projectId: string | null };

let requests: RequestRecord[];
let uploads: RequestRecord[];
let rawFilesByProject: Record<string, Array<Record<string, unknown>>>;
let pendingProjectBRaw: ((response: Response) => void) | null;
let pendingProjectBStatuses: Array<(response: Response) => void>;
let holdProjectBRaw = false;
let holdProjectBStatus = false;

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function ScopeHarness() {
  const { currentProject, addProject, selectProject, navCounts, refreshNavCounts } = useWorkspace();
  return (
    <>
      <p data-testid="current-project-id">{currentProject?.id ?? ''}</p>
      <p data-testid="nav-counts">{navCounts.sources ?? ''}|{navCounts.concepts ?? ''}</p>
      <button type="button" onClick={() => void addProject('Project B')}>Create Project B</button>
      <button type="button" onClick={() => selectProject('project-a')}>Select Project A</button>
      <button type="button" onClick={() => void refreshNavCounts()}>Refresh nav counts</button>
      <RawClient />
    </>
  );
}

function renderWorkspace() {
  return render(
    <NavigationBlockerProvider>
      <WorkspaceProvider>
        <ScopeHarness />
      </WorkspaceProvider>
    </NavigationBlockerProvider>,
  );
}

async function createProjectB() {
  await screen.findByText('project-a', { selector: '[data-testid="current-project-id"]' });
  fireEvent.click(screen.getByRole('button', { name: 'Create Project B' }));
  await screen.findByText('project-b', { selector: '[data-testid="current-project-id"]' });
  await screen.findByRole('heading', { name: 'Add raw files' });
}

function requestProjectIds(method: string, path: string) {
  return [
    ...requests,
    ...uploads,
  ].filter((request) => request.method === method && request.path === path).map((request) => request.projectId);
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  localStorage.setItem('locale', 'en');
  localStorage.setItem('llm-wiki-last-project', 'project-a');
  auth.token = 'synthetic-session';
  auth.user = { id: 'synthetic-owner', email: 'qa-owner@example.test', role: 'owner' };
  auth.sessionEpoch = 1;
  auth.push.mockReset();
  auth.replace.mockReset();
  requests = [];
  uploads = [];
  rawFilesByProject = { 'project-a': [], 'project-b': [] };
  pendingProjectBRaw = null;
  pendingProjectBStatuses = [];
  holdProjectBRaw = false;
  holdProjectBStatus = false;
  setRuntimeConfigForTests({
    schema_version: 1,
    api_url: 'https://api.runtime.test',
    auth_url: 'https://auth.runtime.test',
  });
  configureApiAuth({
    getAccessToken: () => auth.token,
    getSessionEpoch: () => auth.sessionEpoch,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });

  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    const projectId = new Headers(init?.headers).get('X-Project-ID');
    requests.push({ method, path: url.pathname + url.search, projectId });

    if (url.pathname === '/api/v1/projects') {
      return jsonResponse([{ id: 'project-a', name: 'Project A' }]);
    }
    if (url.pathname === '/api/v1/init-project' && method === 'POST') {
      return jsonResponse({ id: 'project-b', name: 'Project B' }, 201);
    }
    if (url.pathname === '/api/v1/status') {
      if (holdProjectBStatus && projectId === 'project-b') {
        return new Promise<Response>((resolve) => { pendingProjectBStatuses.push(resolve); });
      }
      return jsonResponse(projectId === 'project-b'
        ? { sources_count: 9, concepts_count: 8, raw_count: 0 }
        : { sources_count: 1, concepts_count: 2, raw_count: 0 });
    }
    if (url.pathname === '/api/v1/raw') {
      if (holdProjectBRaw && projectId === 'project-b') {
        return new Promise<Response>((resolve) => { pendingProjectBRaw = resolve; });
      }
      return jsonResponse({ files: rawFilesByProject[projectId ?? ''] ?? [] });
    }
    if (url.pathname.startsWith('/api/v1/raw/')) {
      return new Response('Synthetic project-scoped preview.', { status: 200 });
    }
    if (url.pathname === '/api/v1/sources') return jsonResponse({ sources: [] });
    return jsonResponse({});
  });

  class SyntheticXMLHttpRequest {
    headers: Record<string, string> = {};
    upload: XMLHttpRequestUpload = {} as XMLHttpRequestUpload;
    status = 0;
    responseText = '';
    method = '';
    url = '';
    body: FormData | null = null;
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    ontimeout: (() => void) | null = null;
    onabort: (() => void) | null = null;

    open(method: string, url: string) {
      this.method = method;
      this.url = url;
    }

    setRequestHeader(name: string, value: string) {
      this.headers[name] = value;
    }

    getResponseHeader() {
      return null;
    }

    send(body: FormData) {
      this.body = body;
      const file = body.get('file') as File;
      const projectId = this.headers['X-Project-ID'] ?? null;
      uploads.push({ method: this.method, path: new URL(this.url).pathname, projectId });
      if (projectId) {
        rawFilesByProject[projectId] = [{
          name: file.name,
          size: file.size,
          updated: '2026-10-18T15:00:00Z',
          sha256: 'synthetic-digest-from-upload',
          ingested: false,
        }];
      }
      this.status = 201;
      this.responseText = JSON.stringify({
        filename: file.name,
        path: `users/synthetic-owner/projects/${projectId}/raw/${file.name}`,
        bytes: file.size,
        sha256: 'synthetic-digest-from-upload',
        status: 'created',
      });
      this.onload?.();
    }
  }

  vi.stubGlobal('XMLHttpRequest', SyntheticXMLHttpRequest as unknown as typeof XMLHttpRequest);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

async function uploadAndPreview() {
  const file = new File(['Project B raw content'], 'uploaded.md', { type: 'text/markdown' });
  const input = document.querySelector<HTMLInputElement>('#raw-file-upload');
  if (!input) throw new Error('Raw upload input was not mounted');
  await act(async () => fireEvent.change(input, { target: { files: [file] } }));
  await screen.findByText('Created');
  fireEvent.click(await screen.findByRole('button', { name: 'Preview uploaded.md' }));
  expect(await screen.findByText('Synthetic project-scoped preview.')).toBeDefined();
}

function makeLastProjectWritesFail() {
  const original = Storage.prototype.setItem;
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key, value) {
    if (this === localStorage && key === 'llm-wiki-last-project') throw new Error('storage write blocked');
    return original.call(this, key, value);
  });
}

function makeLastProjectReadsFail() {
  const original = Storage.prototype.getItem;
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (this: Storage, key) {
    if (this === localStorage && key === 'llm-wiki-last-project') throw new Error('storage read blocked');
    return original.call(this, key);
  });
}

it('keeps create/select B, upload, list, preview, and count refresh scoped to B when persisting A→B fails', async () => {
  renderWorkspace();
  await screen.findByText('project-a', { selector: '[data-testid="current-project-id"]' });
  await waitFor(() => expect(requestProjectIds('GET', '/api/v1/status')).toContain('project-a'));
  makeLastProjectWritesFail();
  const requestStart = requests.length;

  await createProjectB();
  await uploadAndPreview();

  const scopedRequests = [...requests.slice(requestStart), ...uploads];
  expect(scopedRequests.filter((request) => (
    request.path === '/api/v1/raw'
    || request.path === '/api/v1/raw/upload'
    || request.path === '/api/v1/raw/uploaded.md?preview=true'
    || request.path === '/api/v1/status'
  )).every((request) => request.projectId === 'project-b'), JSON.stringify(scopedRequests)).toBe(true);
  expect(requestProjectIds('GET', '/api/v1/raw').length).toBeGreaterThanOrEqual(2);
  expect(requestProjectIds('POST', '/api/v1/init-project')).toEqual([null]);
  expect(requests.slice(requestStart).filter((request) => request.path === '/api/v1/init-project')).toHaveLength(1);
  expect(uploads).toEqual([{ method: 'POST', path: '/api/v1/raw/upload', projectId: 'project-b' }]);
});

it('keeps selected B usable when localStorage getItem for the remembered project throws', async () => {
  renderWorkspace();
  await screen.findByText('project-a', { selector: '[data-testid="current-project-id"]' });
  makeLastProjectWritesFail();
  makeLastProjectReadsFail();

  await createProjectB();
  await uploadAndPreview();

  expect(requestProjectIds('GET', '/api/v1/raw').slice(-2)).toEqual(['project-b', 'project-b']);
  expect(uploads.at(-1)?.projectId).toBe('project-b');
  expect(requests.filter((request) => request.path === '/api/v1/raw/uploaded.md?preview=true').at(-1)?.projectId)
    .toBe('project-b');
});

it('ignores a late Raw list response after switching from B back to A', async () => {
  renderWorkspace();
  await screen.findByText('project-a', { selector: '[data-testid="current-project-id"]' });
  rawFilesByProject['project-a'] = [{ name: 'project-a.md', size: 10, sha256: 'a', ingested: false }];
  holdProjectBRaw = true;
  await createProjectB();
  await waitFor(() => expect(pendingProjectBRaw).not.toBeNull());

  fireEvent.click(screen.getByRole('button', { name: 'Select Project A' }));
  expect(await screen.findByText('project-a.md')).toBeDefined();
  const resolveProjectB = pendingProjectBRaw;
  if (!resolveProjectB) throw new Error('Project B Raw list did not start');
  await act(async () => resolveProjectB(jsonResponse({
    files: [{ name: 'project-b.md', size: 20, sha256: 'b', ingested: false }],
  })));

  expect(screen.getByText('project-a.md')).toBeDefined();
  expect(screen.queryByText('project-b.md')).toBeNull();
});

it('ignores a late nav-count refresh from B after switching back to A', async () => {
  renderWorkspace();
  await screen.findByText('project-a', { selector: '[data-testid="current-project-id"]' });
  await waitFor(() => expect(requestProjectIds('GET', '/api/v1/status')).toContain('project-a'));
  await waitFor(() => expect(screen.getByTestId('nav-counts').textContent).toBe('1|2'));

  holdProjectBStatus = true;
  await createProjectB();
  await waitFor(() => expect(pendingProjectBStatuses).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: 'Refresh nav counts' }));
  await waitFor(() => expect(pendingProjectBStatuses).toHaveLength(2));

  fireEvent.click(screen.getByRole('button', { name: 'Select Project A' }));
  await waitFor(() => expect(screen.getByTestId('nav-counts').textContent).toBe('1|2'));
  await act(async () => {
    pendingProjectBStatuses.forEach((resolve) => resolve(jsonResponse({ sources_count: 9, concepts_count: 8, raw_count: 0 })));
  });

  expect(screen.getByTestId('nav-counts').textContent).toBe('1|2');
});
