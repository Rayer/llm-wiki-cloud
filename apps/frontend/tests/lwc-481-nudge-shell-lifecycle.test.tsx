import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { NudgeView } from '@/lib/nudges';
import { installShellIntersectionObserver } from './shell-intersection-observer-helper';

const mocks = vi.hoisted(() => ({
  pathname: '/status',
  auth: {
    user: { id: 'shell-user', email: 'owner@example.test' },
    sessionIdentityEpoch: 2,
    sessionEpoch: 4,
    isDemoSession: false,
  },
  workspace: {
    hydrated: true,
    token: 'synthetic-session',
    loginOpen: false,
    projects: [{ id: 'project-a', name: 'Project Alpha' }],
    currentProject: { id: 'project-a', name: 'Project Alpha' },
    projectsLoading: false,
    projectsError: null,
    isDemoSession: false,
    navCounts: { sources: 0, concepts: 0, documentImportFailures: 0 },
    selectProject: vi.fn(),
    renameProject: vi.fn(),
    refreshProjects: vi.fn(),
    openNewProject: vi.fn(),
    signOut: vi.fn(),
  },
  getBuildInfo: vi.fn(),
  reconcileNudges: vi.fn(),
  getNudges: vi.fn(),
  setNudgeAttention: vi.fn(),
  resolveNudgeTarget: vi.fn(),
  setPaletteOpen: vi.fn(),
}));

vi.mock('next/navigation', () => ({ usePathname: () => mocks.pathname }));
vi.mock('next/image', () => ({ default: (props: React.ImgHTMLAttributes<HTMLImageElement> & { priority?: boolean }) => {
  const imageProps = { ...props };
  delete imageProps.priority;
  return React.createElement('img', imageProps);
} }));
vi.mock('next/link', () => ({
  default: ({ href, onNavigate, onClick, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement> & {
    href: string;
    onNavigate?: (event: { preventDefault: () => void }) => void;
  }) => React.createElement('a', {
    ...props,
    href,
    onClick: (event: React.MouseEvent<HTMLAnchorElement>) => {
      onClick?.(event);
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey
          || props.target && props.target !== '_self' || props.download) return;
      event.preventDefault();
      onNavigate?.({ preventDefault: () => event.preventDefault() });
    },
  }, children),
}));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => mocks.auth }));
vi.mock('@/lib/api', () => ({ getBuildInfo: mocks.getBuildInfo }));
vi.mock('@/lib/nudges', () => ({
  getNudges: mocks.getNudges,
  reconcileNudges: mocks.reconcileNudges,
  setNudgeAttention: mocks.setNudgeAttention,
  resolveNudgeTarget: mocks.resolveNudgeTarget,
}));
vi.mock('@/components/WorkspaceProvider', () => ({
  WorkspaceProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useWorkspace: () => mocks.workspace,
}));
vi.mock('@/components/LoginModal', () => ({ LoginModal: () => null }));
vi.mock('@/components/NewProjectModal', () => ({ NewProjectModal: () => null }));
vi.mock('@/components/ProjectRenameModal', () => ({ ProjectRenameModal: () => null }));
vi.mock('@/components/ProjectEmptyState', () => ({ ProjectEmptyState: () => null }));
vi.mock('@/components/ui/Badge', () => ({ Badge: ({ children }: { children: React.ReactNode }) => <span>{children}</span> }));
vi.mock('@/components/ui/ProjectSelect', () => ({ ProjectSelect: () => null }));
vi.mock('@/components/ui/CommandPalette', () => ({
  CommandPalette: () => null,
  useCommandPalette: () => ({ open: false, setOpen: mocks.setPaletteOpen }),
}));
vi.mock('@/components/AccountSettingsModal', () => ({ AccountSettingsModal: () => null }));
vi.mock('@/components/RequestIDPanel', () => ({ RequestIDPanel: () => null }));
vi.mock('@/components/LocaleControl', () => ({ LocaleControl: () => null }));
vi.mock('@/components/ReleaseNotesReader', () => ({ ReleaseNotesReader: () => null }));

import { Shell } from '@/components/Shell';

const unreadView: NudgeView = {
  schema_version: 1,
  partial: false,
  failures: [],
  notices: [],
  items: [{
    category: 'pipeline-failure',
    attention_id: 'attention-run-b',
    attention_state: 'unread',
    severity: 'attention',
    observations: [{
      source_key: 'pipeline-fail:project-b',
      source_revision: 1,
      active: true,
      observation: {
        project_id: 'project-b',
        project_name: 'Project Beta',
        execution_id: 'run-b',
        title: 'Pipeline did not publish',
        body: 'The run ended before publication.',
        severity: 'attention',
        actions: [{
          id: 'open-run',
          kind: 'link',
          label: 'Open run',
          target: { kind: 'pipeline-run', project_id: 'project-b', execution_id: 'run-b' },
        }],
      },
    }],
  }],
};

const readView = structuredClone(unreadView);
readView.items[0]!.attention_state = 'read';

function renderShell() {
  return render(<Shell><p>workspace page</p></Shell>);
}

async function openNudge() {
  fireEvent.click(await screen.findByRole('button', { name: 'Nudge.open, Nudge.unreadCue' }));
  return screen.findByRole('dialog', { name: 'Nudge.title' });
}

describe('LWC-481 closed Shell Nudge feedback and accepted navigation lifecycle', () => {
  beforeEach(() => {
    installShellIntersectionObserver();
    mocks.pathname = '/status';
    mocks.auth.user = { id: 'shell-user', email: 'owner@example.test' };
    mocks.auth.sessionIdentityEpoch = 2;
    mocks.auth.sessionEpoch = 4;
    mocks.auth.isDemoSession = false;
    mocks.reconcileNudges.mockReset().mockResolvedValue(unreadView);
    mocks.getNudges.mockReset().mockResolvedValue(unreadView);
    mocks.setNudgeAttention.mockReset().mockResolvedValue(undefined);
    mocks.resolveNudgeTarget.mockReset().mockReturnValue('/status?project_id=project-b&execution_id=run-b');
    mocks.getBuildInfo.mockReset().mockResolvedValue({
      environment: 'local', product_version: 'test', commit: 'test', revision: 'test', branch: 'test', tag: '', image_tag: '', service: 'test',
    });
    mocks.workspace.selectProject.mockReset();
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('announces unknown while the closed initial reconciliation is pending, then removes it on confirmed read', async () => {
    let resolveReconcile!: (value: NudgeView) => void;
    mocks.reconcileNudges.mockReturnValueOnce(new Promise<NudgeView>((resolve) => { resolveReconcile = resolve; }));
    renderShell();

    expect(screen.getByRole('status').textContent).toBe('Nudge.statusUnknown');
    await resolveReconcile(unreadView);
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    expect(screen.getByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keeps a closed, accessible error when initial reconciliation fails', async () => {
    mocks.reconcileNudges.mockRejectedValueOnce(new Error('local synthetic failure'));
    renderShell();

    expect((await screen.findByRole('alert')).textContent).toBe('Nudge.loadFailed');
    expect(screen.getByRole('button', { name: 'Nudge.open' })).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it.each([
    { readback: 'succeeds', get: () => Promise.resolve(unreadView) },
    { readback: 'fails', get: () => Promise.reject(new Error('synthetic GET failure')) },
  ])('shows failed read confirmation beside the real closed Shell trigger when GET $readback and recovers', async ({ get }) => {
    mocks.getNudges.mockImplementationOnce(get).mockResolvedValueOnce(readView);
    mocks.setNudgeAttention.mockRejectedValueOnce(new Error('synthetic PATCH 503')).mockResolvedValueOnce(undefined);
    renderShell();

    await openNudge();
    expect(await screen.findByText('Pipeline did not publish')).toBeTruthy();
    const openRun = screen.getByRole('link', { name: 'Open run' });
    expect(openRun.getAttribute('href')).toBe('/status?project_id=project-b&execution_id=run-b');
    fireEvent.click(openRun);

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Nudge.readNotConfirmed');
    expect(screen.getByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeTruthy();
    expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(1, 'pipeline-failure', 'attention-run-b', 'read');
    expect(mocks.getNudges).toHaveBeenCalledOnce();
    expect(mocks.workspace.selectProject).not.toHaveBeenCalled();

    await openNudge();
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(screen.getByRole('button', { name: 'Nudge.open' })).toBeTruthy();
    expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(2, 'pipeline-failure', 'attention-run-b', 'read');
    expect(mocks.workspace.selectProject).not.toHaveBeenCalled();
  });
});
