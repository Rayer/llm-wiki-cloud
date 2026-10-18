import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  push: vi.fn(),
  getProfile: vi.fn(),
  markProfileOnboardingPresented: vi.fn(),
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ user: { id: 'owner-1' }, sessionEpoch: 1 }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: { id: 'project-b', name: 'Project B' },
    profileOnboarding: { ownerId: 'owner-1', projectId: 'project-b', sessionEpoch: 1 },
    markProfileOnboardingPresented: mocks.markProfileOnboardingPresented,
  }),
}));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getProfile: mocks.getProfile };
});

import { NavigationBlockerProvider } from '@/components/NavigationBlocker';
import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';

function emptyProfile() {
  return {
    project_id: 'project-b',
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

async function renderPanel() {
  render(
    <NavigationBlockerProvider>
      <ProjectProfilePanel projectId="project-b" />
    </NavigationBlockerProvider>,
  );
  await screen.findByRole('heading', { name: 'Organization and search preferences (optional)' });
}

beforeEach(() => {
  window.localStorage.setItem('locale', 'en');
  mocks.push.mockReset();
  mocks.getProfile.mockReset().mockResolvedValue(emptyProfile());
  mocks.markProfileOnboardingPresented.mockReset().mockReturnValue(true);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

it('allows an unchanged Profile draft to go to Raw without asking for confirmation', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  await renderPanel();

  fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

  expect(confirm).not.toHaveBeenCalled();
  expect(mocks.push).toHaveBeenCalledWith('/raw');
});

it('keeps an edited Profile draft and stays on Profile when the real blocker confirmation is cancelled', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  await renderPanel();

  fireEvent.click(screen.getByRole('button', { name: 'My organization requirements' }));
  const requirement = screen.getByRole('textbox', { name: 'Requirement 1' });
  fireEvent.change(requirement, { target: { value: 'Keep product decisions traceable.' } });
  expect(await screen.findByText('Your requirements have unsaved changes.')).toBeDefined();

  fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

  expect(confirm).toHaveBeenCalledOnce();
  expect(mocks.push).not.toHaveBeenCalled();
  expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value)
    .toBe('Keep product decisions traceable.');
});

it('navigates to Raw only after confirming that an edited Profile draft may be left', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  await renderPanel();

  fireEvent.click(screen.getByRole('button', { name: 'My organization requirements' }));
  fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), {
    target: { value: 'Keep product decisions traceable.' },
  });
  expect(await screen.findByText('Your requirements have unsaved changes.')).toBeDefined();

  fireEvent.click(screen.getByRole('button', { name: 'Add data now' }));

  expect(confirm).toHaveBeenCalledOnce();
  expect(mocks.push).toHaveBeenCalledWith('/raw');
});
