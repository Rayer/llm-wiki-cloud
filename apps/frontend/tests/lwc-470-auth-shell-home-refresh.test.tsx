import { useEffect } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { installShellIntersectionObserver } from './shell-intersection-observer-helper';

const mocks = vi.hoisted(() => ({
  bootstrapDefaultProject: vi.fn(),
  getProjects: vi.fn(),
  getBuildInfo: vi.fn(),
  getStatus: vi.fn(),
  getDocumentImportFailureSummary: vi.fn(),
  getConcepts: vi.fn(),
  getProjectContentContext: vi.fn(),
  uploadRawFile: vi.fn(),
  pathname: '/',
  authResponses: [] as Array<{ access_token: string; user: { id: string; email: string } }>,
  homeMounts: 0,
  homeUnmounts: 0,
  auth: null as null | ReturnType<typeof import('@/lib/auth').useAuth>,
  workspace: null as null | ReturnType<typeof import('@/components/WorkspaceProvider').useWorkspace>,
}));

vi.mock('next/navigation', () => ({
  usePathname: () => mocks.pathname,
  useSearchParams: () => new URLSearchParams(),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
}));

vi.mock('@/lib/projects', async () => {
  const actual = await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects');
  return {
    ...actual,
    bootstrapDefaultProject: mocks.bootstrapDefaultProject,
    getProjects: mocks.getProjects,
    getProjectContentContext: mocks.getProjectContentContext,
  };
});

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getBuildInfo: mocks.getBuildInfo,
    getStatus: mocks.getStatus,
    getDocumentImportFailureSummary: mocks.getDocumentImportFailureSummary,
    getConcepts: mocks.getConcepts,
    uploadRawFile: mocks.uploadRawFile,
  };
});

import { Shell } from '@/components/Shell';
import { HomeClient } from '@/components/HomeClient';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { AuthProvider, useAuth } from '@/lib/auth';
import { translate } from '@/lib/i18n';
import type { Project } from '@/lib/project-core';
import type { RawUploadResult } from '@/lib/api';

const projectA: Project = {
  id: 'project-a', name: 'Project Alpha', createdAt: null, lastUsedAt: null,
  description: '', descriptionRevision: 0,
};
const projectB: Project = {
  id: 'project-b', name: 'Project Beta', createdAt: null, lastUsedAt: null,
  description: '', descriptionRevision: 0,
};
const userA = { id: 'user-a', email: 'a@example.test' };
const userB = { id: 'user-b', email: 'b@example.test' };

function AuthWorkspaceHome() {
  mocks.workspace = useWorkspace();
  useEffect(() => {
    mocks.homeMounts += 1;
    return () => { mocks.homeUnmounts += 1; };
  }, []);
  return <HomeClient />;
}

function AuthStateProbe() {
  mocks.auth = useAuth();
  return null;
}

function renderPage() {
  return render(<AuthProvider><AuthStateProbe /><Shell><AuthWorkspaceHome /></Shell></AuthProvider>);
}

function authResponse(accessToken: string, user = userA) {
  return new Response(JSON.stringify({ access_token: accessToken, user }), {
    status: 200, headers: { 'Content-Type': 'application/json' },
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

const savedReceipt: RawUploadResult = {
  filename: 'note.md', path: 'raw/note.md', bytes: 11, sha256: 'a'.repeat(64), status: 'created',
};

beforeEach(() => {
  installShellIntersectionObserver();
  mocks.pathname = '/';
  mocks.homeMounts = 0;
  mocks.homeUnmounts = 0;
  mocks.auth = null;
  mocks.workspace = null;
  mocks.authResponses = [{ access_token: 'token-a-1', user: userA }];
  mocks.bootstrapDefaultProject.mockReset().mockResolvedValue('project-a');
  mocks.getBuildInfo.mockReset().mockResolvedValue({ product_version: '2026.11.2', api_version: 'test', environment: 'test', git_sha: 'test' });
  mocks.getStatus.mockReset().mockResolvedValue({ sourcesCount: 0, conceptsCount: 0, rawCount: 0, suggestedQueries: [], lastExecution: null, raw: {} });
  mocks.getDocumentImportFailureSummary.mockReset().mockResolvedValue({ failed_count: 0 });
  mocks.getConcepts.mockReset().mockResolvedValue([]);
  mocks.getProjectContentContext.mockReset().mockResolvedValue({
    project_id: 'project-a', status: 'missing', current_generation: null,
    current_concepts_sha256: null, summary: null, stale: false,
  });
  mocks.uploadRawFile.mockReset();
  mocks.getProjects.mockReset().mockResolvedValue([projectA]);
  localStorage.clear();
  localStorage.setItem('locale', 'en');
  localStorage.setItem('llm-wiki-last-project', 'project-a');
  window.dispatchEvent(new Event('locale-change'));
  vi.stubGlobal('fetch', vi.fn(async (input: Parameters<typeof fetch>[0]) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    if (url.endsWith('/api/v1/auth/refresh')) {
      const next = mocks.authResponses.shift();
      return next ? authResponse(next.access_token, next.user) : new Response('{}', { status: 401 });
    }
    if (url.endsWith('/api/v1/auth/logout')) return new Response('{}', { status: 200 });
    throw new Error(`Unexpected fetch in synthetic integration test: ${url}`);
  }));
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

it('keeps Shell and Home mounted, query drafts, and an in-flight material receipt across same-user credential rotation', async () => {
  const backgroundProjects = deferred<Project[]>();
  const upload = deferred<RawUploadResult>();
  let projectReads = 0;
  mocks.getProjects.mockImplementation(() => {
    projectReads += 1;
    if (projectReads === 1) return Promise.resolve([projectA]);
    if (projectReads === 2) return backgroundProjects.promise;
    return Promise.resolve([projectA]);
  });
  mocks.uploadRawFile.mockReturnValue(upload.promise);

  renderPage();
  await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(2));
  expect(mocks.homeMounts).toBe(1);
  expect(mocks.homeUnmounts).toBe(0);
  expect(mocks.workspace?.projectsLoading).toBe(false);

  const query = screen.getByRole('textbox', { name: translate('en', 'Demo.search') });
  fireEvent.change(query, { target: { value: 'query draft survives refresh' } });
  fireEvent.click(screen.getByRole('button', { name: translate('en', 'AddMaterial.open') }));
  fireEvent.change(screen.getByLabelText(translate('en', 'AddMaterial.textTitle')), { target: { value: 'Draft title' } });
  fireEvent.change(screen.getByLabelText(translate('en', 'AddMaterial.textBody')), { target: { value: 'Draft body' } });
  fireEvent.click(screen.getByRole('button', { name: translate('en', 'AddMaterial.saveText') }));
  await waitFor(() => expect(mocks.uploadRawFile).toHaveBeenCalledTimes(1));

  mocks.authResponses.push({ access_token: 'token-a-2', user: userA });
  await act(async () => { await mocks.auth!.refreshAccessToken(); });
  await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(3));
  await waitFor(() => expect(mocks.workspace?.currentProject?.id).toBe('project-a'));

  expect(mocks.auth?.sessionIdentityEpoch).toBe(1);
  expect(mocks.homeMounts).toBe(1);
  expect(mocks.homeUnmounts).toBe(0);
  expect(mocks.workspace?.projectsLoading).toBe(false);
  expect((screen.getByRole('textbox', { name: translate('en', 'Demo.search') }) as HTMLInputElement).value).toBe('query draft survives refresh');
  expect((screen.getByLabelText(translate('en', 'AddMaterial.textTitle')) as HTMLInputElement).value).toBe('Draft title');
  expect((screen.getByLabelText(translate('en', 'AddMaterial.textBody')) as HTMLTextAreaElement).value).toBe('Draft body');
  expect(screen.getByRole('button', { name: translate('en', 'AddMaterial.saving') })).toBeTruthy();

  await act(async () => { upload.resolve(savedReceipt); await upload.promise; });
  expect(await screen.findByText(new RegExp(savedReceipt.path))).toBeTruthy();

  const failedRefresh = deferred<Project[]>();
  mocks.getProjects.mockReturnValueOnce(failedRefresh.promise);
  let refresh!: Promise<void>;
  await act(async () => { refresh = mocks.workspace!.refreshProjects(); });
  await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(4));
  await act(async () => { failedRefresh.reject(new Error('temporary project list outage')); await refresh; });

  expect(mocks.workspace?.projects).toEqual([projectA]);
  expect(mocks.workspace?.currentProject?.id).toBe('project-a');
  expect(mocks.workspace?.projectsLoading).toBe(false);
  expect(mocks.homeMounts).toBe(1);
  expect(mocks.homeUnmounts).toBe(0);
  expect(await screen.findByText(new RegExp(savedReceipt.path))).toBeTruthy();
  await act(async () => { backgroundProjects.resolve([projectA]); await backgroundProjects.promise; });
});

it('clears Home state when a refresh changes the authenticated account', async () => {
  let projectReads = 0;
  mocks.getProjects.mockImplementation(async () => {
    projectReads += 1;
    return [projectReads < 3 ? projectA : projectB];
  });
  renderPage();
  await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(2));
  fireEvent.change(screen.getByRole('textbox', { name: translate('en', 'Demo.search') }), { target: { value: 'account A private draft' } });
  fireEvent.click(screen.getByRole('button', { name: translate('en', 'AddMaterial.open') }));
  fireEvent.change(screen.getByLabelText(translate('en', 'AddMaterial.textTitle')), { target: { value: 'Private A title' } });

  mocks.authResponses.push({ access_token: 'token-b-1', user: userB });
  await act(async () => { await mocks.auth!.refreshAccessToken(); });
  await waitFor(() => expect(mocks.workspace?.currentProject?.id).toBe('project-b'));
  await waitFor(() => expect(mocks.homeMounts).toBe(2));
  expect(mocks.auth?.user?.id).toBe('user-b');
  expect(mocks.homeUnmounts).toBe(1);
  expect(screen.queryByDisplayValue('account A private draft')).toBeNull();
  expect(screen.queryByDisplayValue('Private A title')).toBeNull();
});

it('clears private Home state on logout', async () => {
  renderPage();
  await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(2));
  fireEvent.change(screen.getByRole('textbox', { name: translate('en', 'Demo.search') }), { target: { value: 'private query' } });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  await act(async () => { await mocks.workspace!.signOut(); });

  await waitFor(() => expect(mocks.homeUnmounts).toBe(1));
  expect(mocks.auth?.user).toBeNull();
  expect(mocks.auth?.sessionIdentityEpoch).toBe(2);
  expect(screen.queryByDisplayValue('private query')).toBeNull();
});
