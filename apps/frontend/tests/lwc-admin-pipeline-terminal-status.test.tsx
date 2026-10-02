import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { AdminClient } from '@/components/AdminClient';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const mocks = vi.hoisted(() => ({
  getAdminProjects: vi.fn(),
  getAdminUsers: vi.fn(),
  getAdminSettings: vi.fn(),
  triggerAdminProjectPipeline: vi.fn(),
  getAdminPipelineStatus: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  clearPublicConfigCache: vi.fn(),
  deleteAdminProject: vi.fn(), deleteAdminUser: vi.fn(),
  getAdminProjects: mocks.getAdminProjects, getAdminUsers: mocks.getAdminUsers,
  getAdminSettings: mocks.getAdminSettings, getAdminPipelineStatus: mocks.getAdminPipelineStatus,
  publishAnnouncement: vi.fn(), rebuildAdminProjectIndex: vi.fn(), renameAdminProject: vi.fn(),
  triggerAdminProjectPipeline: mocks.triggerAdminProjectPipeline,
  updateAdminSettings: vi.fn(), updateAdminUserRole: vi.fn(), updateAdminUserStatus: vi.fn(),
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ hydrated: true, user: { role: 'admin' } }) }));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/AnnouncementBoard', () => ({ AnnouncementBoard: () => null }));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ setBlocked: vi.fn() }) }));

describe('admin query-chip regeneration', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('polls the accepted execution and reports success only after SUCCEEDED', async () => {
    mocks.getAdminProjects.mockResolvedValue([{
      id: 'owner_project', name: 'Project', projectId: 'project', userId: 'owner',
      userName: 'Owner', userEmail: 'owner@example.com', conceptCount: 1, sourceCount: 1,
    }]);
    mocks.getAdminUsers.mockResolvedValue([]);
    mocks.getAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    mocks.triggerAdminProjectPipeline.mockResolvedValue({
      status: 'accepted', execution_id: 'exec-chip', stage: 'suggested-queries',
    });

    let notifyFirstPoll!: () => void;
    const firstPollStarted = new Promise<void>((resolve) => {
      notifyFirstPoll = resolve;
    });
    let releaseRunningStatus!: () => void;
    const runningStatus = new Promise<{ last_execution: { status: 'RUNNING' } }>((resolve) => {
      releaseRunningStatus = () => resolve({ last_execution: { status: 'RUNNING' } });
    });
    mocks.getAdminPipelineStatus
      .mockImplementationOnce(() => {
        notifyFirstPoll();
        return runningStatus;
      })
      .mockResolvedValueOnce({ last_execution: { status: 'SUCCEEDED' } });

    render(<AdminClient />);
    await screen.findByRole('button', { name: 'Regenerate query chips' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Regenerate query chips' })[0]);
    await screen.findByRole('button', { name: 'Regenerate chips' });

    vi.useFakeTimers();
    try {
      fireEvent.click(screen.getByRole('button', { name: 'Regenerate chips' }));
      await vi.waitFor(() => expect(mocks.getAdminPipelineStatus).toHaveBeenCalledTimes(1));
      await firstPollStarted;
      await act(async () => {
        releaseRunningStatus();
        await Promise.resolve();
      });
      await act(async () => {
        await vi.advanceTimersToNextTimerAsync();
      });

      expect(mocks.getAdminPipelineStatus).toHaveBeenCalledTimes(2);
      expect(mocks.getAdminPipelineStatus).toHaveBeenNthCalledWith(2, 'owner_project', 'exec-chip', expect.any(AbortSignal));
      expect(screen.getByText('Query chips regeneration completed.')).toBeDefined();
      expect(screen.queryByText(/triggered/)).toBeNull();
    } finally {
      releaseRunningStatus();
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });

  it('polls the accepted execution and reports FAILED with diagnostic details', async () => {
    mocks.getAdminProjects.mockResolvedValue([{
      id: 'owner_project', name: 'Project', projectId: 'project', userId: 'owner',
      userName: 'Owner', userEmail: 'owner@example.com', conceptCount: 1, sourceCount: 1,
    }]);
    mocks.getAdminUsers.mockResolvedValue([]);
    mocks.getAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    mocks.triggerAdminProjectPipeline.mockResolvedValue({
      status: 'accepted', execution_id: 'exec-chip', stage: 'suggested-queries',
    });
    let notifyFirstPoll!: () => void;
    const firstPollStarted = new Promise<void>((resolve) => {
      notifyFirstPoll = resolve;
    });
    let releaseRunningStatus!: () => void;
    const runningStatus = new Promise<{ last_execution: { status: 'RUNNING' } }>((resolve) => {
      releaseRunningStatus = () => resolve({ last_execution: { status: 'RUNNING' } });
    });
    mocks.getAdminPipelineStatus
      .mockImplementationOnce(() => {
        notifyFirstPoll();
        return runningStatus;
      })
      .mockResolvedValueOnce({ last_execution: {
        status: 'FAILED',
        diagnostic: { detail_code: 'entity_mapping_article_source_missing' },
      } });

    render(<AdminClient />);
    await screen.findByRole('button', { name: 'Regenerate query chips' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Regenerate query chips' })[0]);
    await screen.findByRole('button', { name: 'Regenerate chips' });

    vi.useFakeTimers();
    try {
      fireEvent.click(screen.getByRole('button', { name: 'Regenerate chips' }));
      await vi.waitFor(() => expect(mocks.getAdminPipelineStatus).toHaveBeenCalledTimes(1));
      await firstPollStarted;
      await act(async () => {
        releaseRunningStatus();
        await Promise.resolve();
      });
      await act(async () => {
        await vi.advanceTimersToNextTimerAsync();
      });

      expect(mocks.getAdminPipelineStatus).toHaveBeenCalledTimes(2);
      expect(mocks.getAdminPipelineStatus).toHaveBeenNthCalledWith(2, 'owner_project', 'exec-chip', expect.any(AbortSignal));
      expect(screen.getByText(/Query chips regeneration failed: entity_mapping_article_source_missing/)).toBeDefined();
      expect(screen.queryByText('Query chips regeneration completed.')).toBeNull();
    } finally {
      releaseRunningStatus();
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });

  it('shows a visible bounded error when status polling rejects', async () => {
    mocks.getAdminProjects.mockResolvedValue([{ id: 'owner_project', name: 'Project', projectId: 'project', userId: 'owner', userName: 'Owner', userEmail: 'owner@example.com', conceptCount: 1, sourceCount: 1 }]);
    mocks.getAdminUsers.mockResolvedValue([]);
    mocks.getAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    mocks.triggerAdminProjectPipeline.mockResolvedValue({ status: 'accepted', execution_id: 'exec-chip', stage: 'suggested-queries' });
    mocks.getAdminPipelineStatus.mockRejectedValue(new Error('status offline'));

    const { container } = render(<AdminClient />);
    const query = within(container);
    await query.findByRole('button', { name: 'Regenerate query chips' });
    fireEvent.click(query.getByRole('button', { name: 'Regenerate query chips' }));
    fireEvent.click(query.getByRole('button', { name: 'Regenerate chips' }));

    expect(await query.findByText('Query chips regeneration failed: status offline.')).toBeDefined();
    expect(query.queryByText('Query chips regeneration started; waiting for completion.')).toBeNull();
  });

  it('disables project actions while query-chip polling owns the action state', async () => {
    mocks.getAdminProjects.mockResolvedValue([{ id: 'owner_project', name: 'Project', projectId: 'project', userId: 'owner', userName: 'Owner', userEmail: 'owner@example.com', conceptCount: 1, sourceCount: 1 }]);
    mocks.getAdminUsers.mockResolvedValue([]);
    mocks.getAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    mocks.triggerAdminProjectPipeline.mockResolvedValue({ status: 'accepted', execution_id: 'exec-chip', stage: 'suggested-queries' });
    mocks.getAdminPipelineStatus.mockReturnValue(new Promise(() => {}));

    const { container, unmount } = render(<AdminClient />);
    const query = within(container);
    await query.findByRole('button', { name: 'Regenerate query chips' });
    fireEvent.click(query.getByRole('button', { name: 'Regenerate query chips' }));
    fireEvent.click(query.getByRole('button', { name: 'Regenerate chips' }));

    await waitFor(() => expect(query.getByRole('button', { name: 'Trigger pipeline' }).hasAttribute('disabled')).toBe(true));
    unmount();
  });

  it('stops polling for query-chip regeneration after unmount', async () => {
    let cleanupTimers: (() => void) | null = null;
    try {
      let statusCallCount = 0;
      let firstPollResolved: (() => void) | null = null;
      const firstPoll = new Promise<void>((resolve) => {
        firstPollResolved = resolve;
      });
      mocks.getAdminProjects.mockResolvedValue([{
        id: 'owner_project', name: 'Project', projectId: 'project', userId: 'owner',
        userName: 'Owner', userEmail: 'owner@example.com', conceptCount: 1, sourceCount: 1,
      }]);
      mocks.getAdminUsers.mockResolvedValue([]);
      mocks.getAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
      mocks.triggerAdminProjectPipeline.mockResolvedValue({
        status: 'accepted', execution_id: 'exec-chip', stage: 'suggested-queries',
      });
      mocks.getAdminPipelineStatus.mockImplementation(() => {
        statusCallCount += 1;
        if (firstPollResolved) {
          firstPollResolved();
          firstPollResolved = null;
        }
        return Promise.resolve({ last_execution: { status: 'RUNNING' } });
      });

      const { container, unmount } = render(<AdminClient />);
      const query = within(container);
      const regenButtons = await query.findAllByRole('button', { name: 'Regenerate query chips' });
      const regenButton = regenButtons[0];
      await act(async () => {
        fireEvent.click(regenButton);
      });
      expect(query.getByRole('button', { name: 'Regenerate chips' })).toBeDefined();
      vi.useFakeTimers();
      cleanupTimers = () => vi.useRealTimers();
      await act(async () => {
        fireEvent.click(query.getByRole('button', { name: 'Regenerate chips' }));
      });
      await firstPoll;
      const callsAtUnmount = statusCallCount;
      await act(async () => {
        unmount();
        vi.advanceTimersByTime(1200);
      });
      expect(statusCallCount).toBe(callsAtUnmount);
    } finally {
      cleanupTimers?.();
    }
  });
});
