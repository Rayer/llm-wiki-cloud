import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  confirmNavigation: vi.fn(),
  createLLMProfile: vi.fn(),
  getProfile: vi.fn(),
  getProjectLLMBinding: vi.fn(),
  listLLMProfiles: vi.fn(),
  saveProjectLLMBinding: vi.fn(),
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
  return {
    ...actual,
    createLLMProfile: mocks.createLLMProfile,
    getProfile: mocks.getProfile,
    getProjectLLMBinding: mocks.getProjectLLMBinding,
    listLLMProfiles: mocks.listLLMProfiles,
    saveProjectLLMBinding: mocks.saveProjectLLMBinding,
    saveProfile: mocks.saveProfile,
  };
});

import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';

const originalScrollIntoView = HTMLElement.prototype.scrollIntoView;

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
  if (typeof HTMLElement.prototype.scrollIntoView !== 'function') {
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
  }
  localStorage.setItem('locale', 'en');
  localStorage.removeItem('lwc:byok-shared-notice:v1:user-1');
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.createLLMProfile.mockReset().mockResolvedValue({
    id: 'created-profile', name: 'New custom provider', provider: 'custom', protocol: 'openai_chat_completions',
    endpoint: 'https://fixture.example/v1', model: 'fixture-model', revision: 1, secret_configured: false,
  });
  mocks.getProfile.mockResolvedValue(emptyProfile('project-new'));
  mocks.getProjectLLMBinding.mockResolvedValue({ source: 'shared', revision: 1 });
  mocks.listLLMProfiles.mockResolvedValue([{
    id: 'provider-1', name: 'Private provider', provider: 'custom', protocol: 'openai_chat_completions',
    endpoint: 'https://fixture.example/v1', model: 'fixture-model', revision: 1, secret_configured: true,
  }]);
  mocks.saveProjectLLMBinding.mockReset().mockResolvedValue({ source: 'personal', profile_id: 'created-profile', revision: 2 });
  mocks.markProfileOnboardingPresented.mockReturnValue(true);
  mocks.push.mockReset();
  mocks.saveProfile.mockReset();
  mocks.ownerId = 'user-1';
  mocks.sessionEpoch = 1;
  mocks.profileOnboarding = { ownerId: 'user-1', projectId: 'project-new', sessionEpoch: 1 };
});

afterEach(() => {
  cleanup();
  if (typeof originalScrollIntoView !== 'function') Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView');
  vi.clearAllMocks();
});

describe('LWC-406 optional Profile onboarding', () => {
  it('keeps the Project source draft alive while switching settings tabs', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });

    fireEvent.keyDown(screen.getByRole('tab', { name: 'Knowledge settings' }), { key: 'ArrowRight' });
    expect(screen.getByRole('tab', { name: 'AI service' }).getAttribute('aria-selected')).toBe('true');
    const source = await screen.findByRole('combobox', { name: 'Service source' }) as HTMLSelectElement;
    fireEvent.change(source, { target: { value: 'personal' } });
    const provider = await screen.findByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement;
    fireEvent.change(provider, { target: { value: 'provider-1' } });

    expect(screen.queryByRole('heading', { name: 'Organization and search preferences' })).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Knowledge settings' }));
    expect(await screen.findByRole('heading', { name: 'Organization and search preferences' })).toBeDefined();
    fireEvent.click(screen.getByRole('tab', { name: 'AI service' }));

    expect((screen.getByRole('combobox', { name: 'Service source' }) as HTMLSelectElement).value).toBe('personal');
    expect((screen.getByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement).value).toBe('provider-1');
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();
  });

  it.each([
    ['Project notice CTA', 'notice'],
    ['Add provider button', 'add'],
  ] as const)('returns from the %s to the original AI service tab with a selected unsaved profile', async (_name, entry) => {
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('tab', { name: 'AI service' }));
    const serviceSource = await screen.findByRole('combobox', { name: 'Service source' });
    if (entry === 'add') {
      fireEvent.change(serviceSource, { target: { value: 'personal' } });
    }

    const providerButtons = await screen.findAllByRole('button', { name: 'Open custom LLM providers' });
    fireEvent.click(providerButtons[entry === 'notice' ? 0 : 1]);
    expect(await screen.findByRole('heading', { name: 'Custom LLM providers' })).toBeDefined();
    fireEvent.change(screen.getByRole('combobox', { name: 'Provider' }), { target: { value: 'custom' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Provider name' }), { target: { value: 'New custom provider' } });
    fireEvent.change(screen.getByRole('textbox', { name: /Model Enter the model name/ }), { target: { value: 'fixture-model' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Account settings' })).toBeNull());
    expect(screen.getByRole('tab', { name: 'AI service' }).getAttribute('aria-selected')).toBe('true');
    await waitFor(() => expect((screen.getByRole('combobox', { name: 'Service source' }) as HTMLSelectElement).value).toBe('personal'));
    await waitFor(() => expect((screen.getByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement).value).toBe('created-profile'));
    expect(mocks.createLLMProfile).toHaveBeenCalledWith(expect.objectContaining({ name: 'New custom provider', model: 'fixture-model', provider: 'custom' }));
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Save AI service settings' }));
    await waitFor(() => expect(mocks.saveProjectLLMBinding).toHaveBeenCalledWith('project-new', {
      source: 'personal', profile_id: 'created-profile', revision: 1,
    }));
  });

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
