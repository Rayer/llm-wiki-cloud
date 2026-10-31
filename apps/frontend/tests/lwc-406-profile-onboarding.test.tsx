import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  confirmNavigation: vi.fn(),
  createLLMProfile: vi.fn(),
  getProfile: vi.fn(),
  getProjectDescription: vi.fn(),
  saveProjectDescription: vi.fn(),
  getProjectContentContext: vi.fn(),
  getProjectPipelineAutomation: vi.fn(),
  updateProjectPipelineAutomation: vi.fn(),
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
    getProjectPipelineAutomation: mocks.getProjectPipelineAutomation,
    updateProjectPipelineAutomation: mocks.updateProjectPipelineAutomation,
    getProjectLLMBinding: mocks.getProjectLLMBinding,
    listLLMProfiles: mocks.listLLMProfiles,
    saveProjectLLMBinding: mocks.saveProjectLLMBinding,
    saveProfile: mocks.saveProfile,
  };
});

vi.mock('@/lib/projects', async () => ({
  ...await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects'),
  getProjectDescription: mocks.getProjectDescription,
  saveProjectDescription: mocks.saveProjectDescription,
  getProjectContentContext: mocks.getProjectContentContext,
}));

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

function selectKnowledgeTab() {
  const name = localStorage.getItem('locale') === 'zh-TW' ? '知識庫產出設定' : 'Knowledge generation settings';
  fireEvent.click(screen.getByRole('tab', { name }));
}

beforeEach(() => {
  if (typeof HTMLElement.prototype.scrollIntoView !== 'function') {
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
  }
  localStorage.setItem('locale', 'en');
  window.history.replaceState({}, '', '/profile');
  localStorage.removeItem('lwc:byok-shared-notice:v1:user-1');
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.createLLMProfile.mockReset().mockResolvedValue({
    id: 'created-profile', name: 'New custom provider', provider: 'custom', protocol: 'openai_chat_completions',
    endpoint: 'https://fixture.example/v1', models: { fast: 'fixture-model', heavy: 'fixture-model' }, model: 'fixture-model', revision: 1, secret_configured: false,
  });
  mocks.getProfile.mockResolvedValue(emptyProfile('project-new'));
  mocks.getProjectDescription.mockResolvedValue({ projectId: 'project-new', description: 'Fixture description', revision: 2 });
  mocks.getProjectContentContext.mockResolvedValue({
    project_id: 'project-new', status: 'ready', current_generation: 'generation-2',
    current_concepts_sha256: 'fixture-digest', stale: false,
    summary: {
      text: 'Read-only fixture summary', source_generation: 'generation-2', concepts_sha256: 'fixture-digest',
      generated_at: '2026-10-30T00:00:00Z', prompt_revision: 'prompt-1', profile_source: 'bootstrap',
      role: 'summary', requested_model: 'fixture', returned_model: 'fixture', input_digest: 'fixture-input',
      selection_version: 'selection-1',
    },
  });
  mocks.getProjectPipelineAutomation.mockResolvedValue({
    enabled: false, revision: 3, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
    source_revision: 1, processed_revision: 0,
  });
  mocks.updateProjectPipelineAutomation.mockResolvedValue({
    enabled: true, revision: 4, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
    source_revision: 1, processed_revision: 0,
  });
  mocks.getProjectLLMBinding.mockResolvedValue({ source: 'shared', revision: 1 });
  mocks.listLLMProfiles.mockResolvedValue([{
    id: 'provider-1', name: 'Private provider', provider: 'custom', protocol: 'openai_chat_completions',
    endpoint: 'https://fixture.example/v1', models: { fast: 'fixture-model', heavy: 'fixture-model' }, model: 'fixture-model', revision: 1, secret_configured: true,
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
    await screen.findByRole('heading', { name: 'Project description' });
    expect(await screen.findByText('Adjust the content direction and writing style of your knowledge base. Changes apply to future generations and recompilations.')).toBeDefined();
    await screen.findByText('Read-only fixture summary');
    const tabs = ['Project information', 'Pipeline', 'Knowledge generation settings', 'LLM Provider'];
    for (const name of tabs) {
      const tab = screen.getByRole('tab', { name });
      const panel = document.getElementById(tab.getAttribute('aria-controls') ?? '');
      expect(panel?.getAttribute('role')).toBe('tabpanel');
      expect(panel?.getAttribute('aria-labelledby')).toBe(tab.id);
    }
    const infoPanel = document.getElementById('project-settings-panel-project-info')!;
    const pipelinePanel = document.getElementById('project-settings-panel-pipeline')!;
    const knowledgePanel = document.getElementById('project-settings-panel-knowledge')!;
    expect(infoPanel.hidden).toBe(false);
    expect(pipelinePanel.hidden).toBe(true);
    expect(knowledgePanel.textContent).not.toContain('ProjectDescription.label');
    expect(knowledgePanel.querySelector('[data-testid="pipeline-automation-project-toggle"]')).toBeNull();

    const description = await screen.findByRole('textbox', { name: 'What is this Wiki for? (optional)' }) as HTMLTextAreaElement;
    fireEvent.change(description, { target: { value: 'Unsaved project description draft' } });
    fireEvent.click(screen.getByRole('tab', { name: 'Pipeline' }));
    await waitFor(() => expect(pipelinePanel.hidden).toBe(false));
    expect(pipelinePanel.querySelector('[data-testid="pipeline-automation-project-toggle"]')).not.toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Knowledge generation settings' }));
    expect(knowledgePanel.hidden).toBe(false);
    expect(knowledgePanel.querySelector('#project-description-input')).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
    const source = await screen.findByRole('combobox', { name: 'Service source' }) as HTMLSelectElement;
    fireEvent.change(source, { target: { value: 'personal' } });
    const provider = await screen.findByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement;
    fireEvent.change(provider, { target: { value: 'provider-1' } });

    fireEvent.click(screen.getByRole('tab', { name: 'Project information' }));
    expect((await screen.findByRole('textbox', { name: 'What is this Wiki for? (optional)' }) as HTMLTextAreaElement).value).toBe('Unsaved project description draft');
    expect(infoPanel.textContent).toContain('Read-only fixture summary');
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));

    expect((screen.getByRole('combobox', { name: 'Service source' }) as HTMLSelectElement).value).toBe('personal');
    expect((screen.getByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement).value).toBe('provider-1');
    expect(mocks.saveProjectDescription).not.toHaveBeenCalled();
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();
    expect(mocks.updateProjectPipelineAutomation).not.toHaveBeenCalled();
  });

  it('routes direct hash targets to the correct visible settings panel and focus target', async () => {
    window.history.replaceState({}, '', '/profile#project-pipeline-automation');
    render(<ProjectProfilePanel projectId="project-new" />);

    await waitFor(() => expect(screen.getByRole('tab', { name: 'Pipeline' }).getAttribute('aria-selected')).toBe('true'));
    await waitFor(() => expect(document.activeElement?.id).toBe('project-pipeline-automation'));
    expect(document.getElementById('project-settings-panel-pipeline')?.hidden).toBe(false);

    window.history.replaceState({}, '', '/profile#project-description');
    fireEvent(window, new HashChangeEvent('hashchange'));
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Project information' }).getAttribute('aria-selected')).toBe('true'));
    await waitFor(() => expect(document.activeElement?.id).toBe('project-description-input'));
    expect(document.getElementById('project-settings-panel-project-info')?.hidden).toBe(false);
  });

  it('supports Arrow, Home, and End navigation across all settings tabs', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Project description' });
    const info = screen.getByRole('tab', { name: 'Project information' });
    fireEvent.keyDown(info, { key: 'ArrowRight' });
    expect(screen.getByRole('tab', { name: 'Pipeline' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Pipeline' }), { key: 'ArrowRight' });
    expect(screen.getByRole('tab', { name: 'Knowledge generation settings' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Knowledge generation settings' }), { key: 'End' });
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'LLM Provider' }), { key: 'Home' });
    expect(info.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(info);
  });

  it('clears tab drafts when the route replaces the Project-scoped settings panel', async () => {
    mocks.getProfile.mockImplementation(async (projectId: string) => emptyProfile(projectId));
    mocks.getProjectDescription.mockImplementation(async (projectId: string) => ({
      projectId, description: `Description ${projectId}`, revision: 1,
    }));
    mocks.getProjectContentContext.mockImplementation(async (projectId: string) => ({
      project_id: projectId, status: 'ready', current_generation: null,
      current_concepts_sha256: null, stale: false,
      summary: {
        text: `Summary ${projectId}`, source_generation: 'generation', concepts_sha256: 'digest',
        generated_at: '2026-10-30T00:00:00Z', prompt_revision: 'prompt-1', profile_source: 'bootstrap',
        role: 'summary', requested_model: 'fixture', returned_model: 'fixture', input_digest: 'fixture-input',
        selection_version: 'selection-1',
      },
    }));
    const view = render(<ProjectProfilePanel key="project-new" projectId="project-new" />);
    const oldDescription = await screen.findByRole('textbox', { name: 'What is this Wiki for? (optional)' }) as HTMLTextAreaElement;
    fireEvent.change(oldDescription, { target: { value: 'Unsaved old Project draft' } });

    view.rerender(<ProjectProfilePanel key="project-b" projectId="project-b" />);
    await waitFor(() => expect((screen.getByRole('textbox', { name: 'What is this Wiki for? (optional)' }) as HTMLTextAreaElement).value).toBe('Description project-b'));
    expect(screen.getByText('Summary project-b')).toBeDefined();
    expect(mocks.getProjectDescription).toHaveBeenCalledWith('project-b');
    expect(mocks.getProjectContentContext).toHaveBeenCalledWith('project-b');
    expect(mocks.getProjectPipelineAutomation).toHaveBeenCalledWith('project-b');
  });

  it('localizes the Project settings tabs and knowledge-generation timing in Traditional Chinese', async () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectProfilePanel projectId="project-new" />);

    expect(await screen.findByRole('tab', { name: '專案資訊' })).not.toBeNull();
    expect(screen.getByRole('tab', { name: 'Pipeline' }).getAttribute('aria-selected')).toBe('false');
    expect(await screen.findByRole('tab', { name: '知識庫產出設定' })).not.toBeNull();
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('false');
    fireEvent.click(screen.getByRole('tab', { name: '知識庫產出設定' }));
    expect(await screen.findByText('調整知識庫的內容方向與編寫方式，於後續產生或重新編譯時套用。')).not.toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
  });

  it.each([
    ['Project notice CTA', 'notice'],
    ['Add provider button', 'add'],
  ] as const)('returns from the %s to the original AI service tab with a selected unsaved profile', async (_name, entry) => {
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));
    const serviceSource = await screen.findByRole('combobox', { name: 'Service source' });
    if (entry === 'add') {
      fireEvent.change(serviceSource, { target: { value: 'personal' } });
    }

    const providerButtons = await screen.findAllByRole('button', { name: 'Open LLM Provider' });
    fireEvent.click(providerButtons[entry === 'notice' ? 0 : 1]);
    const accountSettingsDialog = await screen.findByRole('dialog', { name: 'Account settings' });
    expect(within(accountSettingsDialog).getByRole('heading', { name: 'LLM Provider' })).toBeDefined();
    expect(within(accountSettingsDialog).getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.change(screen.getByRole('combobox', { name: 'Provider' }), { target: { value: 'custom' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Provider name' }), { target: { value: 'New custom provider' } });
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fixture-model' } });
    fireEvent.change(screen.getByLabelText('Heavy model'), { target: { value: 'fixture-model' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Account settings' })).toBeNull());
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
    await waitFor(() => expect((screen.getByRole('combobox', { name: 'Service source' }) as HTMLSelectElement).value).toBe('personal'));
    await waitFor(() => expect((screen.getByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement).value).toBe('created-profile'));
    expect(mocks.createLLMProfile).toHaveBeenCalledWith(expect.objectContaining({ name: 'New custom provider', model: 'fixture-model', provider: 'custom' }));
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Save LLM Provider settings' }));
    await waitFor(() => expect(mocks.saveProjectLLMBinding).toHaveBeenCalledWith('project-new', {
      source: 'personal', profile_id: 'created-profile', revision: 1,
    }));
  });

  it('returns from Account settings cancellation without losing the unsaved provider draft', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));
    const serviceSource = await screen.findByRole('combobox', { name: 'Service source' });
    fireEvent.change(serviceSource, { target: { value: 'personal' } });
    fireEvent.change(await screen.findByRole('combobox', { name: 'Custom LLM provider' }), { target: { value: 'provider-1' } });
    fireEvent.click((await screen.findAllByRole('button', { name: 'Open LLM Provider' }))[0]);

    const dialog = await screen.findByRole('dialog', { name: 'Account settings' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close account settings' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Account settings' })).toBeNull());
    expect(screen.getByRole('tab', { name: 'LLM Provider' }).getAttribute('aria-selected')).toBe('true');
    expect((screen.getByRole('combobox', { name: 'Service source' }) as HTMLSelectElement).value).toBe('personal');
    expect((screen.getByRole('combobox', { name: 'Custom LLM provider' }) as HTMLSelectElement).value).toBe('provider-1');
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();
  });

  it('waits for a valid Profile response before presenting and consuming the notice', async () => {
    const pendingProfile = deferred<ReturnType<typeof emptyProfile>>();
    mocks.getProfile.mockReturnValue(pendingProfile.promise);
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();

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
    selectKnowledgeTab();

    expect(await screen.findByRole('button', { name: 'Retry loading Profile' })).toBeDefined();
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading Profile' }));

    expect(await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeDefined();
    expect(mocks.getProfile).toHaveBeenCalledTimes(2);
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();
  });

  it('adds data without saving Profile and keeps the original dirty-navigation guard', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith('/raw');
    expect(mocks.saveProfile).not.toHaveBeenCalled();
  });

  it('keeps the original dirty guard when Add data now is blocked', async () => {
    mocks.confirmNavigation.mockReturnValue(false);
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).not.toHaveBeenCalled();
    expect(screen.getByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeDefined();
  });

  it('opens the existing preferences card without navigating or saving', async () => {
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
    fireEvent.click(screen.getByRole('button', { name: 'Review settings on this page' }));

    expect(screen.getByRole('button', { name: 'My organization requirements' }).getAttribute('aria-expanded')).toBe('true');
    expect(mocks.push).not.toHaveBeenCalled();
    expect(mocks.saveProfile).not.toHaveBeenCalled();
  });

  it('keeps the new Project intro pending until the knowledge settings panel is visible', async () => {
    const pendingIntent = { ownerId: 'user-1', projectId: 'project-new', sessionEpoch: 1 };
    mocks.profileOnboarding = pendingIntent;
    mocks.markProfileOnboardingPresented.mockImplementation(() => {
      mocks.profileOnboarding = null;
      return true;
    });

    const firstEntry = render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Project description' });
    expect(document.getElementById('project-settings-panel-project-info')?.hidden).toBe(false);
    expect(document.getElementById('project-settings-panel-knowledge')?.hidden).toBe(true);
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(mocks.profileOnboarding).toEqual(pendingIntent);

    firstEntry.unmount();
    const reentered = render(<ProjectProfilePanel projectId="project-new" />);
    await screen.findByRole('heading', { name: 'Project description' });
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(mocks.profileOnboarding).toEqual(pendingIntent);

    selectKnowledgeTab();
    const intro = await screen.findByText('You can review how this project organizes information before adding data. These preferences are optional: change them later, or add data now without editing or saving anything.');
    const knowledgePanel = document.getElementById('project-settings-panel-knowledge')!;
    expect(knowledgePanel.hidden).toBe(false);
    expect(knowledgePanel.contains(intro)).toBe(true);
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledWith('user-1', 'project-new', 1);
    expect(mocks.profileOnboarding).toBeNull();

    fireEvent.click(screen.getByRole('tab', { name: 'Pipeline' }));
    fireEvent.click(screen.getByRole('tab', { name: 'Knowledge generation settings' }));
    expect(knowledgePanel.hidden).toBe(false);
    expect(knowledgePanel.contains(intro)).toBe(true);
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();

    reentered.rerender(<ProjectProfilePanel projectId="project-new" />);
    expect(knowledgePanel.hidden).toBe(false);
    expect(knowledgePanel.contains(intro)).toBe(true);
    expect(mocks.markProfileOnboardingPresented).toHaveBeenCalledOnce();
  });

  it('keeps the notice dismissed across rerender and revisit after presenting once', async () => {
    mocks.markProfileOnboardingPresented.mockImplementationOnce(() => true).mockReturnValue(false);
    const view = render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
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
    selectKnowledgeTab();
    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(1));
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    selectKnowledgeTab();
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
    selectKnowledgeTab();
    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(1));

    mocks.ownerId = 'user-2';
    mocks.sessionEpoch = 2;
    view.rerender(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();
    await screen.findByRole('heading', { name: 'Organization and search preferences' });
    oldProfile.resolve(emptyProfile('project-new'));

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledTimes(2));
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
  });

  it('does not present onboarding for a normal existing Project', async () => {
    mocks.profileOnboarding = null;
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();

    await screen.findByRole('heading', { name: 'Organization and search preferences' });
    expect(mocks.markProfileOnboardingPresented).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Organization and search preferences (optional)' })).toBeNull();
  });

  it('uses the accepted optional copy and actions in Traditional Chinese', async () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectProfilePanel projectId="project-new" />);
    selectKnowledgeTab();

    expect(await screen.findByRole('heading', { name: '整理與搜尋偏好（選填）' })).toBeDefined();
    expect(screen.getByText('可以先看一下這個專案的整理方式。這是選填設定；你可以稍後調整，不修改或儲存也能直接加入資料。')).toBeDefined();
    expect(screen.getByRole('button', { name: '查看本頁設定' })).toBeDefined();
    expect(screen.getByRole('button', { name: '先加入資料' })).toBeDefined();
  });
});
