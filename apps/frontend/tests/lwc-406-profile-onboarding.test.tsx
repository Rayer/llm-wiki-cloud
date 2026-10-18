import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  confirmNavigation: vi.fn(),
  getProfile: vi.fn(),
  markProfileOnboardingPresented: vi.fn(),
  setBlocked: vi.fn(),
  push: vi.fn(),
  saveProfile: vi.fn(),
  ownerId: 'user-1',
  sessionEpoch: 1,
  profileOnboarding: null as { ownerId: string; projectId: string; sessionEpoch: number } | null,
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    profileOnboarding: mocks.profileOnboarding,
    markProfileOnboardingPresented: mocks.markProfileOnboardingPresented,
    currentProject: { id: 'project-new', name: 'New Project' },
  }),
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: { id: mocks.ownerId }, sessionEpoch: mocks.sessionEpoch }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation, setBlocked: mocks.setBlocked }),
}));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getProfile: mocks.getProfile, saveProfile: mocks.saveProfile };
});

import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';

function emptyProfile(projectId: string) {
  return {
    project_id: projectId,
    revision: 0,
    requirements: [],
    derivation_status: null,
    scheduled_for: null,
    derivation_error_code: null,
    candidate: null,
    confirmed_candidate_id: null,
    active: null,
    job: null,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.getProfile.mockResolvedValue(emptyProfile('project-new'));
  mocks.markProfileOnboardingPresented.mockReturnValue(true);
  mocks.push.mockReset();
  mocks.saveProfile.mockReset();
  mocks.ownerId = 'user-1';
  mocks.sessionEpoch = 1;
  mocks.profileOnboarding = { ownerId: 'user-1', projectId: 'project-new', sessionEpoch: 1 };
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-406 optional Profile onboarding', () => {
  it('waits for a valid Profile response before presenting and consuming the notice', async () => {
    const pendingProfile = deferred<ReturnType<typeof emptyProfile>>();
    mocks.getProfile.mockReturnValue(pendingProfile.promise);
    render(<ProjectProfilePanel projectId="project-new" />);

    expect(screen.getByText('Loading Profile…')).toBeDefined();
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    pendingProfile.resolve(emptyProfile('project-new'));

    expect(await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeDefined();
    expect(screen.getByText('You can review how this project organizes information before adding data. These preferences are optional: change them later, or add data now without editing or saving anything.')).toBeDefined();
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledWith('user-1', 'project-new', 1);
    expect(screen.getByRole('button', { name: 'Review settings on this page' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Add data now' })).toBeDefined();
    expect(mocks.saveProfile).not.toHaveBeenCalled();
  });

  it('does not consume the notice on load failure and shows it after a successful retry', async () => {
    mocks.getProfile
      .mockRejectedValueOnce(new Error('profile unavailable'))
      .mockResolvedValueOnce(emptyProfile('project-new'));
    render(<ProjectProfilePanel projectId="project-new" />);

    expect(await screen.findByRole('button', { name: 'Retry loading Profile' })).toBeDefined();
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading Profile' }));

    expect(await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeDefined();
    expect(mocks.getProfile).toHaveBeenCalledTimes(2);
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();
  });

  it('adds data without saving Profile and keeps the original dirty-navigation guard', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith('/raw');
    expect(mocks.saveProfile).not.toHaveBeenCalled();
  });

  it('keeps the original dirty guard when Add data now is blocked', async () => {
    mocks.confirmNavigation.mockReturnValue(false);
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).not.toHaveBeenCalled();
    expect(screen.getByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeDefined();
  });

  it('opens the existing preferences card without navigating or saving', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Review settings on this page' }));

    expect(screen.getByRole('button', { name: 'My organization requirements' }).getAttribute('aria-expanded')).toBe('true');
    expect(mocks.push).not.toHaveBeenCalled();
    expect(mocks.saveProfile).not.toHaveBeenCalled();
  });

  it('keeps the notice dismissed across rerender and revisit after presenting once', async () => {
    mocks.markProfileOnboardingPresented.mockImplementationOnce(() => true).mockReturnValue(false);
    const view = render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss optional settings introduction' }));
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
    view.rerender(<ProjectProfilePanel projectId="project-new" />);
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();
  });

  it('does not claim an A intent from a late response after switching to B', async () => {
    const pendingA = deferred<ReturnType<typeof emptyProfile>>();
    mocks.getProfile
      .mockReturnValueOnce(pendingA.promise)
      .mockResolvedValueOnce(emptyProfile('project-b'));
    const view = render(<ProjectProfilePanel projectId="project-new" />);
    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(1));
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences' });
    pendingA.resolve(emptyProfile('project-new'));

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(2));
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
  });

  it('fences an old Profile response after the account and session change', async () => {
    const oldProfile = deferred<ReturnType<typeof emptyProfile>>();
    mocks.getProfile
      .mockReturnValueOnce(oldProfile.promise)
      .mockResolvedValueOnce(emptyProfile('project-new'));
    const view = render(<ProjectProfilePanel projectId="project-new" />);
    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(1));

    mocks.ownerId = 'user-2';
    mocks.sessionEpoch = 2;
    view.rerender(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences' });
    oldProfile.resolve(emptyProfile('project-new'));

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(2));
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
  });

  it('does not present onboarding for a normal existing Project', async () => {
    mocks.profileOnboarding = null;
    render(<ProjectProfilePanel projectId="project-new" />);

    await screen.findByRole('heading', { name: 'Organization and search preferences' });
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
  });

  it('uses the accepted optional copy and actions in Traditional Chinese', async () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectProfilePanel projectId="project-new" />);

    expect(await screen.findByRole('heading', { name: '整理與搜尋偏好（選填）' })).toBeDefined();
    expect(screen.getByText('可以先看一下這個專案的整理方式。這是選填設定；你可以稍後調整，不修改或儲存也能直接加入資料。')).toBeDefined();
    expect(screen.getByRole('button', { name: '查看本頁設定' })).toBeDefined();
    expect(screen.getByRole('button', { name: '先加入資料' })).toBeDefined();
  });
});
