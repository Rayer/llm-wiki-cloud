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
  getProfile: vi.fn(),
  getProfileBootstrapGuidance: vi.fn(),
  saveProfile: vi.fn(),
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
    getProfile: mocks.getProfile,
    getProfileBootstrapGuidance: mocks.getProfileBootstrapGuidance,
    saveProfile: mocks.saveProfile,
  };
});

import { Shell } from '@/components/Shell';
import { HomeClient } from '@/components/HomeClient';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { AuthProvider, useAuth } from '@/lib/auth';
import ProfilePage from '@/app/(workspace)/profile/page';
import { translate } from '@/lib/i18n';
import type { Project } from '@/lib/project-core';
import type { ProfileState, RawUploadResult } from '@/lib/api';

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
  const workspace = useWorkspace();
  useEffect(() => {
    mocks.workspace = workspace;
  }, [workspace]);
  useEffect(() => {
    mocks.homeMounts += 1;
    return () => { mocks.homeUnmounts += 1; };
  }, []);
  return <HomeClient />;
}

function AuthStateProbe() {
  const auth = useAuth();
  useEffect(() => { mocks.auth = auth; }, [auth]);
  return null;
}

function AuthWorkspaceProfilePage() {
  const workspace = useWorkspace();
  useEffect(() => { mocks.workspace = workspace; }, [workspace]);
  return <ProfilePage />;
}

function renderPage() {
  return render(<AuthProvider><AuthStateProbe /><Shell><AuthWorkspaceHome /></Shell></AuthProvider>);
}

function renderProfilePage() {
  mocks.pathname = '/profile';
  return render(<AuthProvider><AuthStateProbe /><Shell><AuthWorkspaceProfilePage /></Shell></AuthProvider>);
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

function profileState(projectId: string, requirement: string, pending = false): ProfileState {
  return {
    project_id: projectId,
    revision: 1,
    requirements: [{ id: 'requirement-1', text: requirement }],
    derivation_status: pending ? 'pending' : null,
    scheduled_for: pending ? '2026-11-03T00:00:00Z' : null,
    derivation_error_code: null,
    candidate: null,
    confirmed_candidate_id: null,
    active: null,
    job: null,
  };
}

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
  mocks.getProfile.mockReset().mockImplementation(async (projectId: string) => profileState(projectId, `Saved requirement for ${projectId}`));
  mocks.getProfileBootstrapGuidance.mockReset().mockResolvedValue({ bootstrap_guidance: null });
  mocks.saveProfile.mockReset();
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
  await act(async () => { refresh = mocks.workspace!.refreshProjects(true); });
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

it('preserves Profile draft and pending save through same-user token rotation while a polling GET finishes first', async () => {
  const pollingRead = deferred<ProfileState>();
  const pendingSave = deferred<ProfileState>();
  let profileReads = 0;
  const intervalHandlers: TimerHandler[] = [];
  vi.spyOn(window, 'setInterval').mockImplementation(((handler: TimerHandler) => {
    intervalHandlers.push(handler);
    return 1;
  }) as typeof window.setInterval);
  vi.spyOn(window, 'clearInterval').mockImplementation(() => undefined);
  mocks.getProfile.mockImplementation((projectId: string) => {
    profileReads += 1;
    return profileReads === 1
      ? Promise.resolve(profileState(projectId, 'Server saved requirement', true))
      : pollingRead.promise;
  });
  mocks.saveProfile.mockReturnValue(pendingSave.promise);

  renderProfilePage();
  fireEvent.click(await screen.findByRole('tab', { name: 'Knowledge generation settings' }));
  const requirementsCard = await screen.findByRole('button', { name: 'My organization requirements' });
  fireEvent.click(requirementsCard);
  const input = await screen.findByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement;
  await waitFor(() => expect(input.value).toBe('Server saved requirement'));
  fireEvent.change(input, { target: { value: 'Unsaved Profile draft' } });
  const saveButton = screen.getByRole('button', { name: 'Save requirements' }) as HTMLButtonElement;
  fireEvent.click(saveButton);
  await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));
  expect(input.disabled).toBe(true);

  mocks.authResponses.push({ access_token: 'token-a-2', user: userA });
  await act(async () => { await mocks.auth!.refreshAccessToken(); });
  expect(mocks.auth?.sessionIdentityEpoch).toBe(1);
  expect(mocks.getProfile).toHaveBeenCalledTimes(1);
  expect(input.value).toBe('Unsaved Profile draft');
  expect(input.disabled).toBe(true);

  const profilePoll = intervalHandlers.find((handler) => (
    typeof handler === 'function' && handler.toString().includes('getProfile')
  ));
  expect(profilePoll).toBeTypeOf('function');
  await act(async () => { (profilePoll as () => void)(); await Promise.resolve(); });
  await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(2));
  await act(async () => {
    pollingRead.resolve(profileState('project-a', 'Server saved requirement', true));
    await pollingRead.promise;
  });
  expect(input.value).toBe('Unsaved Profile draft');
  expect(input.disabled).toBe(true);

  await act(async () => {
    pendingSave.resolve(profileState('project-a', 'Unsaved Profile draft'));
    await pendingSave.promise;
  });
  expect(input.value).toBe('Unsaved Profile draft');
  expect(input.disabled).toBe(false);
  expect(saveButton.disabled).toBe(true);
});

it('fences late Profile saves across A→B→A and logout session boundaries', async () => {
  const staleAccountSave = deferred<ProfileState>();
  const logoutSave = deferred<ProfileState>();
  let activeAccount = 'user-a';
  let projectAReads = 0;
  mocks.getProjects.mockImplementation(async () => (activeAccount === 'user-b' ? [projectB] : [projectA]));
  mocks.getProfile.mockImplementation(async (projectId: string) => {
    if (projectId === 'project-b') return profileState(projectId, 'B private requirement');
    projectAReads += 1;
    return profileState(projectId, projectAReads === 1 ? 'A initial requirement' : 'A returned requirement');
  });
  mocks.saveProfile.mockReturnValueOnce(staleAccountSave.promise).mockReturnValueOnce(logoutSave.promise);

  renderProfilePage();
  fireEvent.click(await screen.findByRole('tab', { name: 'Knowledge generation settings' }));
  fireEvent.click(await screen.findByRole('button', { name: 'My organization requirements' }));
  const firstInput = await screen.findByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement;
  await waitFor(() => expect(firstInput.value).toBe('A initial requirement'));
  fireEvent.change(firstInput, { target: { value: 'A stale in-flight draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));
  await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));

  activeAccount = 'user-b';
  mocks.authResponses.push({ access_token: 'token-b-1', user: userB });
  await act(async () => { await mocks.auth!.refreshAccessToken(); });
  fireEvent.click(await screen.findByRole('tab', { name: 'Knowledge generation settings' }));
  fireEvent.click(await screen.findByRole('button', { name: 'My organization requirements' }));
  const bInput = await screen.findByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement;
  await waitFor(() => expect(bInput.value).toBe('B private requirement'));

  activeAccount = 'user-a';
  mocks.authResponses.push({ access_token: 'token-a-3', user: userA });
  await act(async () => { await mocks.auth!.refreshAccessToken(); });
  fireEvent.click(await screen.findByRole('tab', { name: 'Knowledge generation settings' }));
  fireEvent.click(await screen.findByRole('button', { name: 'My organization requirements' }));
  const returnedAInput = await screen.findByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement;
  await waitFor(() => expect(returnedAInput.value).toBe('A returned requirement'));
  await act(async () => {
    staleAccountSave.resolve(profileState('project-a', 'A stale in-flight draft'));
    await staleAccountSave.promise;
  });
  expect(returnedAInput.value).toBe('A returned requirement');
  expect(mocks.auth?.sessionIdentityEpoch).toBe(3);

  fireEvent.change(returnedAInput, { target: { value: 'A logout pending draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));
  await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(2));
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  await act(async () => { await mocks.workspace!.signOut(); });
  await waitFor(() => expect(mocks.auth?.user).toBeNull());
  await act(async () => {
    logoutSave.resolve(profileState('project-a', 'A logout pending draft'));
    await logoutSave.promise;
  });
  expect(mocks.auth?.sessionIdentityEpoch).toBe(4);
  expect(screen.queryByRole('textbox', { name: 'Requirement 1' })).toBeNull();
});
