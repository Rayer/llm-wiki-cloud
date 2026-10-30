import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  getProjectDescription: vi.fn(),
  saveProjectDescription: vi.fn(),
  updateProjectDescription: vi.fn(),
  setBlocked: vi.fn(),
}));

vi.mock('@/lib/projects', () => ({
  getProjectDescription: mocks.getProjectDescription,
  saveProjectDescription: mocks.saveProjectDescription,
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ user: { id: 'owner' }, sessionEpoch: 1 }),
}));

vi.mock('@/lib/i18n', () => ({
  useT: () => ({
    t: (key: string, params?: Record<string, string | number>) => {
      const template = {
        'ProjectDescription.loadFailed': 'ProjectDescription.loadFailed {error}',
        'ProjectDescription.saveFailed': 'ProjectDescription.saveFailed {error}',
        'ProjectDescription.latestFailed': 'ProjectDescription.latestFailed {error}',
      }[key] ?? key;
      return params
        ? Object.entries(params).reduce((text, [name, value]) => text.replace(`{${name}}`, String(value)), template)
        : template;
    },
  }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ updateProjectDescription: mocks.updateProjectDescription }),
}));

vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ setBlocked: mocks.setBlocked }),
}));

import { ApiError } from '../src/lib/api.ts';
import { ProjectDescriptionEditor } from '../src/components/ProjectDescriptionEditor';

describe('ProjectDescriptionEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => cleanup());

  it('shows a distinct read failure and then renders a saved empty description after retry', async () => {
    mocks.getProjectDescription
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ projectId: 'project-a', description: '', revision: 0 });

    render(<ProjectDescriptionEditor projectId="project-a" />);
    expect(await screen.findByText('ProjectDescription.loadFailed offline')).toBeTruthy();
    expect(screen.queryByText('ProjectDescription.empty')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.retry' }));
    expect((await screen.findAllByText('ProjectDescription.empty')).length).toBe(2);
    expect((screen.getByRole('textbox', { name: 'ProjectDescription.label' }) as HTMLTextAreaElement).value).toBe('');
  });

  it('saves the draft, updates the workspace projection, and allows clearing it', async () => {
    mocks.getProjectDescription.mockResolvedValue({ projectId: 'project-a', description: '', revision: 0 });
    mocks.saveProjectDescription
      .mockResolvedValueOnce({ projectId: 'project-a', description: 'Purpose', revision: 1 })
      .mockResolvedValueOnce({ projectId: 'project-a', description: '', revision: 2 });

    render(<ProjectDescriptionEditor projectId="project-a" />);
    const textarea = await screen.findByRole('textbox', { name: 'ProjectDescription.label' });
    fireEvent.change(textarea, { target: { value: 'Purpose' } });
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.save' }));
    await screen.findByText('ProjectDescription.saved');
    expect(mocks.saveProjectDescription).toHaveBeenNthCalledWith(1, 'project-a', 'Purpose', 0);
    expect(mocks.updateProjectDescription).toHaveBeenNthCalledWith(1, 'project-a', 'Purpose', 1);

    fireEvent.change(textarea, { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.save' }));
    await waitFor(() => expect(mocks.updateProjectDescription).toHaveBeenNthCalledWith(2, 'project-a', '', 2));
    expect(mocks.saveProjectDescription).toHaveBeenNthCalledWith(2, 'project-a', '', 1);
  });

  it('keeps the saved value and draft after a write failure', async () => {
    mocks.getProjectDescription.mockResolvedValue({ projectId: 'project-a', description: 'Old value', revision: 1 });
    mocks.saveProjectDescription.mockRejectedValue(new Error('storage unavailable'));

    render(<ProjectDescriptionEditor projectId="project-a" />);
    const textarea = await screen.findByRole('textbox', { name: 'ProjectDescription.label' });
    fireEvent.change(textarea, { target: { value: 'Unsaved draft' } });
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.save' }));

    expect(await screen.findByText('ProjectDescription.saveFailed storage unavailable')).toBeTruthy();
    expect((textarea as HTMLTextAreaElement).value).toBe('Unsaved draft');
    expect(screen.getByText('Old value')).toBeTruthy();
    expect(mocks.updateProjectDescription).not.toHaveBeenCalled();
  });

  it('requires reviewing the latest revision before retrying a conflicted draft', async () => {
    mocks.getProjectDescription
      .mockResolvedValueOnce({ projectId: 'project-a', description: 'Before', revision: 1 })
      .mockResolvedValueOnce({ projectId: 'project-a', description: 'Latest', revision: 2 });
    mocks.saveProjectDescription
      .mockRejectedValueOnce(new ApiError('revision conflict', 409))
      .mockResolvedValueOnce({ projectId: 'project-a', description: 'My draft', revision: 3 });

    render(<ProjectDescriptionEditor projectId="project-a" />);
    const textarea = await screen.findByRole('textbox', { name: 'ProjectDescription.label' });
    fireEvent.change(textarea, { target: { value: 'My draft' } });
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.save' }));

    expect(await screen.findByText('ProjectDescription.conflict')).toBeTruthy();
    expect(await screen.findByText('Latest')).toBeTruthy();
    expect((textarea as HTMLTextAreaElement).value).toBe('My draft');
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.useLatest' }));
    fireEvent.click(screen.getByRole('button', { name: 'ProjectDescription.save' }));

    await waitFor(() => expect(mocks.saveProjectDescription).toHaveBeenNthCalledWith(2, 'project-a', 'My draft', 2));
    expect(await screen.findByText('ProjectDescription.saved')).toBeTruthy();
  });
});
