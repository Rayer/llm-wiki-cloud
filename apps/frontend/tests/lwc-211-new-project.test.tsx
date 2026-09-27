import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const { act, cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');

const mocks = vi.hoisted(() => ({
  addProject: vi.fn(),
  closeNewProject: vi.fn(),
  confirmNavigation: vi.fn(),
  getProfile: vi.fn(),
  saveProfile: vi.fn(),
  push: vi.fn(),
  user: { id: 'user-1' },
  sessionEpoch: 1,
  open: true,
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    newProjectOpen: mocks.open,
    closeNewProject: mocks.closeNewProject,
    addProject: mocks.addProject,
    user: mocks.user,
  }),
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ sessionEpoch: mocks.sessionEpoch }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }),
}));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getProfile: mocks.getProfile, saveProfile: mocks.saveProfile };
});

import { NewProjectModal } from '@/components/NewProjectModal';

const newProject = { id: 'project-new', name: 'Research' };

function emptyProfile(requirements: { id: string; text: string }[] = []) {
  return {
    project_id: 'project-new',
    revision: requirements.length ? 1 : 0,
    requirements,
    derivation_status: requirements.length ? 'pending' : null,
    scheduled_for: requirements.length ? '2026-09-25T03:00:00Z' : null,
    derivation_error_code: null,
    candidate: null,
    confirmed_candidate_id: null,
    active: null,
    job: null,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

beforeEach(() => {
  mocks.addProject.mockResolvedValue(newProject);
  mocks.closeNewProject.mockReset();
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.getProfile.mockResolvedValue(emptyProfile());
  mocks.saveProfile.mockResolvedValue(emptyProfile());
  mocks.push.mockReset();
  mocks.user = { id: 'user-1' };
  mocks.sessionEpoch = 1;
  mocks.open = true;
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-211 project creation Profile requirements', () => {
  it('accepts all blank optional Profile fields without submitting Profile work', async () => {
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value).toBe('');
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() => expect(mocks.addProject).toHaveBeenCalledTimes(1));
    expect(mocks.saveProfile).not.toHaveBeenCalled();
    expect(mocks.closeNewProject).toHaveBeenCalledTimes(1);
    expect(mocks.push).toHaveBeenCalledWith('/');
  });

  it('preserves explicit blank requirement text, IDs, and order in a mixed initial list', async () => {
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add requirement' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add requirement' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 2' }), { target: { value: 'Invoices' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));
    const [projectId, expectedRevision, requirements] = mocks.saveProfile.mock.calls[0];
    expect(projectId).toBe('project-new');
    expect(expectedRevision).toBe(0);
    expect(requirements).toHaveLength(2);
    expect(requirements[0]).toEqual({ id: expect.any(String), text: '' });
    expect(requirements[1]).toEqual({ id: expect.any(String), text: 'Invoices' });
    expect(requirements[0].id).not.toBe(requirements[1].id);
  });

  it('keeps the draft and retries the existing project after an ambiguous Profile save failure', async () => {
    mocks.saveProfile.mockRejectedValueOnce(new Error('temporary network failure'));
    mocks.getProfile.mockImplementation(async () => emptyProfile(mocks.saveProfile.mock.calls[0]?.[2] ?? []));
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    const requirement = screen.getByRole('textbox', { name: 'Requirement 1' });
    fireEvent.change(requirement, { target: { value: 'invoice source' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    expect(await screen.findByText(/Project created, but Profile save was not confirmed/i)).toBeDefined();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value).toBe('invoice source');
    expect(screen.getByRole('button', { name: 'Retry Profile save' })).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Retry Profile save' }));

    await waitFor(() => expect(mocks.closeNewProject).toHaveBeenCalledTimes(1));
    expect(mocks.addProject).toHaveBeenCalledTimes(1);
    expect(mocks.saveProfile).toHaveBeenCalledTimes(1);
    expect(mocks.getProfile).toHaveBeenCalledWith('project-new');
  });

  it('does not save, restore, close, or navigate after an account switch during project creation', async () => {
    const pendingCreate = deferred<typeof newProject>();
    mocks.addProject.mockReturnValue(pendingCreate.promise);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'invoice source' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    mocks.user = { id: 'user-2' };
    mocks.sessionEpoch = 2;
    view.rerender(<NewProjectModal />);
    pendingCreate.resolve(newProject);

    await waitFor(() => expect(mocks.addProject).toHaveBeenCalledTimes(1));
    expect(mocks.saveProfile).not.toHaveBeenCalled();
    expect(mocks.closeNewProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value).toBe('');
  });

  it('does not finish an old Profile save after the authenticated session changes', async () => {
    const pendingSave = deferred<ReturnType<typeof emptyProfile>>();
    mocks.saveProfile.mockReturnValue(pendingSave.promise);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'invoice source' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));
    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));

    mocks.user = { id: 'user-2' };
    mocks.sessionEpoch = 2;
    view.rerender(<NewProjectModal />);
    pendingSave.resolve(emptyProfile([{ id: 'req-old', text: 'invoice source' }]));

    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));
    expect(mocks.getProfile).not.toHaveBeenCalled();
    expect(mocks.closeNewProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
    expect(screen.queryByText(/Project created, but Profile save/)).toBeNull();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value).toBe('');
  });

  it('invalidates a pending save when the modal closes and reopens in a new lifetime', async () => {
    const pendingSave = deferred<ReturnType<typeof emptyProfile>>();
    mocks.saveProfile.mockReturnValue(pendingSave.promise);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'invoice source' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));
    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledTimes(1));

    mocks.open = false;
    view.rerender(<NewProjectModal />);
    expect(screen.queryByRole('dialog', { name: 'Create project' })).toBeNull();
    mocks.open = true;
    view.rerender(<NewProjectModal />);
    await act(async () => {
      pendingSave.resolve(emptyProfile([{ id: 'req-old', text: 'invoice source' }]));
      await Promise.resolve();
    });

    expect(screen.getByRole('button', { name: 'Retry Profile save' })).toBeDefined();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLTextAreaElement).value).toBe('invoice source');
    expect(mocks.addProject).toHaveBeenCalledTimes(1);
    expect(mocks.closeNewProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });
});
