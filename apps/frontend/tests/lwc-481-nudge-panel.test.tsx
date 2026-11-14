import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { NudgeView } from '@/lib/nudges';

const mocks = vi.hoisted(() => ({
  getNudges: vi.fn(),
  reconcileNudges: vi.fn(),
  setNudgeAttention: vi.fn().mockResolvedValue(undefined),
  resolveNudgeTarget: vi.fn(() => '/profile?project_id=project-a#project-description'),
  push: vi.fn(),
  confirmNavigation: vi.fn(() => true),
  t: vi.fn((key: string) => key),
  auth: {
    user: { id: 'user-a' },
    sessionIdentityEpoch: 1,
    sessionEpoch: 1,
    isDemoSession: false,
  },
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: mocks.t }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => mocks.auth }));
vi.mock('@/lib/nudges', () => ({
  getNudges: mocks.getNudges,
  reconcileNudges: mocks.reconcileNudges,
  setNudgeAttention: mocks.setNudgeAttention,
  resolveNudgeTarget: mocks.resolveNudgeTarget,
}));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }) }));

import { NudgePanel } from '@/components/NudgePanel';

const view: NudgeView = {
  schema_version: 1,
  partial: false,
  failures: [],
  notices: [],
  items: [{
    category: 'content-purpose-gap',
    attention_id: 'attention-a',
    attention_state: 'unread',
    severity: 'attention',
    observations: [{
      source_key: 'purpose:project-a',
      source_revision: 1,
      active: true,
      observation: {
        project_id: 'project-a',
        project_name: 'Research archive',
        title: 'Purpose and overview differ',
        body: 'Review the published overview.',
        declared_purpose: 'Local history',
        published_summary: 'Published overview text to preserve',
        severity: 'attention',
        actions: [{
          id: 'copy-open-description',
          kind: 'copy-then-link',
          label: 'Copy and open description',
          copy_text: 'Published overview text to preserve',
          target: { kind: 'project-description', project_id: 'project-a' },
        }],
      },
    }],
  }],
};

function popup() {
  return { location: { replace: vi.fn() }, close: vi.fn(), opener: null } as unknown as Window;
}

describe('LWC-481 Nudge action navigation', () => {
  let writeText: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.getNudges.mockResolvedValue(view);
    mocks.reconcileNudges.mockResolvedValue(view);
    mocks.setNudgeAttention.mockResolvedValue(undefined);
    mocks.resolveNudgeTarget.mockReturnValue('/profile?project_id=project-a#project-description');
    mocks.push.mockReset();
    mocks.confirmNavigation.mockReturnValue(true);
    mocks.auth.user = { id: 'user-a' };
    mocks.auth.sessionIdentityEpoch = 1;
    mocks.auth.sessionEpoch = 1;
    mocks.auth.isDemoSession = false;
    writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    window.open = vi.fn();
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('renders yellow attention while preserving red urgent and green info treatments', async () => {
    const itemWithSeverity = (severity: 'urgent' | 'attention' | 'info', category: string) => {
      const item = structuredClone(view.items[0]!);
      item.category = category;
      item.severity = severity;
      const observation = item.observations[0]?.observation;
      if (!observation) throw new Error('test fixture is missing its observation');
      observation.severity = severity;
      return item;
    };
    const tieredView = structuredClone(view);
    tieredView.items = [
      itemWithSeverity('urgent', 'pipeline-failure'),
      itemWithSeverity('attention', 'content-purpose-gap'),
      itemWithSeverity('info', 'release-notes'),
    ];
    mocks.reconcileNudges.mockResolvedValueOnce(tieredView);
    render(<NudgePanel onClose={vi.fn()} />);

    await screen.findByText('pipeline-failure');
    const article = (category: string) => {
      const result = screen.getByText(category).closest('article');
      if (!result) throw new Error(`${category} card was not rendered`);
      return result;
    };
    const urgentArticle = article('pipeline-failure');
    const attentionArticle = article('content-purpose-gap');
    const infoArticle = article('release-notes');
    const urgentCard = within(urgentArticle);
    const attentionCard = within(attentionArticle);
    const infoCard = within(infoArticle);

    expect(urgentArticle.classList.contains('border-red-300/50')).toBe(true);
    expect(urgentArticle.classList.contains('bg-red-950/30')).toBe(true);
    expect(urgentCard.queryByRole('button', { name: 'Nudge.markRead' })).toBeNull();
    expect(urgentCard.queryByRole('button', { name: 'Nudge.dismiss' })).toBeNull();
    expect(attentionArticle.classList.contains('border-amber-300/60')).toBe(true);
    expect(attentionArticle.classList.contains('bg-amber-950/55')).toBe(true);
    expect(attentionCard.getByRole('button', { name: 'Nudge.markRead' })).toBeTruthy();
    expect(attentionCard.getByRole('button', { name: 'Nudge.dismiss' })).toBeTruthy();
    expect(infoArticle.classList.contains('border-emerald-300/50')).toBe(true);
    expect(infoArticle.classList.contains('bg-emerald-950/30')).toBe(true);
    expect(infoCard.getByRole('button', { name: 'Nudge.markRead' })).toBeTruthy();
    expect(infoCard.getByRole('button', { name: 'Nudge.dismiss' })).toBeTruthy();
  });

  it('opens the actual same-Project target only after the exact copy succeeds', async () => {
    const opened = popup();
    vi.mocked(window.open).mockReturnValue(opened);
    render(<NudgePanel onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    await waitFor(() => expect(opened.location.replace).toHaveBeenCalledWith('/profile?project_id=project-a#project-description'));
    expect(writeText).toHaveBeenCalledWith('Published overview text to preserve');
    expect(window.open).toHaveBeenCalledWith('about:blank', '_blank');
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith('content-purpose-gap', 'attention-a', 'read'));
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('shows selectable exact text and an explicit link when clipboard access is denied', async () => {
    const opened = popup();
    writeText.mockRejectedValueOnce(new Error('clipboard denied'));
    vi.mocked(window.open).mockReturnValue(opened);
    render(<NudgePanel onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Nudge.copyFailed');
    expect((screen.getByLabelText('Nudge.selectCopy') as HTMLTextAreaElement).value).toBe('Published overview text to preserve');
    expect(opened.location.replace).not.toHaveBeenCalled();
    expect(opened.close).toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it('keeps copy success truthful and exposes a target link when the popup is blocked', async () => {
    vi.mocked(window.open).mockReturnValue(null);
    render(<NudgePanel onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Nudge.popupBlocked');
    expect(writeText).toHaveBeenCalledWith('Published overview text to preserve');
    const link = screen.getByRole('link', { name: 'Nudge.openTarget' });
    expect(link.getAttribute('href')).toBe('/profile?project_id=project-a#project-description');
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it('shows explicit partial source status returned by manual reconcile', async () => {
    const partialView = structuredClone(view);
    partialView.partial = true;
    partialView.failures = [{ safe_class: 'pipeline_observer_not_qualified' }];
    mocks.reconcileNudges.mockResolvedValueOnce(partialView);
    render(<NudgePanel onClose={vi.fn()} />);

    expect(await screen.findByText('Nudge.partial')).toBeTruthy();
    expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1);
    expect(mocks.getNudges).not.toHaveBeenCalled();
  });

  it.each(['read', 'navigation-read', 'dismiss'] as const)(
    'keeps source-unavailable status after a %s attention GET',
    async (attentionPath) => {
      const partialView = structuredClone(view);
      partialView.partial = true;
      partialView.failures = [{ safe_class: 'pipeline_observer_not_qualified' }];
      if (attentionPath === 'navigation-read') {
        const action = partialView.items[0]?.observations[0]?.observation?.actions[0];
        if (!action) throw new Error('test fixture is missing its action');
        action.kind = 'link';
      }
      mocks.reconcileNudges.mockResolvedValueOnce(partialView);

      const attentionRead = structuredClone(view);
      if (attentionPath === 'dismiss') {
        attentionRead.items = [];
      } else {
        const refreshedObservation = attentionRead.items[0]?.observations[0]?.observation;
        if (!refreshedObservation) throw new Error('test fixture is missing its observation');
        refreshedObservation.title = 'Refreshed after attention GET';
      }
      mocks.getNudges.mockResolvedValueOnce(attentionRead);
      render(<NudgePanel onClose={vi.fn()} />);
      expect(await screen.findByText('Nudge.partial')).toBeTruthy();

      if (attentionPath === 'read') {
        fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
      } else if (attentionPath === 'navigation-read') {
        fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));
      } else {
        fireEvent.click(screen.getByRole('button', { name: 'Nudge.dismiss' }));
      }

      await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith(
        'content-purpose-gap', 'attention-a', attentionPath === 'dismiss' ? 'dismiss' : 'read',
      ));
      await waitFor(() => expect(mocks.getNudges).toHaveBeenCalledTimes(1));
      expect(await screen.findByText('Nudge.partial')).toBeTruthy();
      if (attentionPath === 'dismiss') {
        expect(await screen.findByText('Nudge.empty')).toBeTruthy();
      } else {
        expect(await screen.findByText('Refreshed after attention GET')).toBeTruthy();
      }
    },
  );

  it('clears source-unavailable status only after a successful manual reconciliation', async () => {
    const partialView = structuredClone(view);
    partialView.partial = true;
    partialView.failures = [{ safe_class: 'pipeline_observer_not_qualified' }];
    mocks.reconcileNudges.mockResolvedValueOnce(partialView);
    render(<NudgePanel onClose={vi.fn()} />);
    expect(await screen.findByText('Nudge.partial')).toBeTruthy();

    mocks.reconcileNudges.mockResolvedValueOnce(view);
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText('Nudge.partial')).toBeNull());
  });

  it('does not let an older reconciliation overwrite a later successful source status', async () => {
    let resolveInitial!: (value: NudgeView) => void;
    const initialReconciliation = new Promise<NudgeView>((resolve) => { resolveInitial = resolve; });
    mocks.reconcileNudges.mockReset().mockReturnValueOnce(initialReconciliation).mockResolvedValueOnce(view);
    render(<NudgePanel onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText('Nudge.partial')).toBeNull());

    const latePartial = structuredClone(view);
    latePartial.partial = true;
    latePartial.failures = [{ safe_class: 'pipeline_observer_not_qualified' }];
    await act(async () => { resolveInitial(latePartial); await initialReconciliation; });
    expect(screen.queryByText('Nudge.partial')).toBeNull();
  });

  it('suppresses an old account read after the session identity changes', async () => {
    let resolveOld!: (value: typeof view) => void;
    let resolveNew!: (value: typeof view) => void;
    const oldRead = new Promise<typeof view>((resolve) => { resolveOld = resolve; });
    const newRead = new Promise<typeof view>((resolve) => { resolveNew = resolve; });
    mocks.reconcileNudges.mockReset().mockImplementationOnce(() => oldRead).mockImplementationOnce(() => newRead);
    const { rerender } = render(<NudgePanel onClose={vi.fn()} />);

    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1));
    mocks.auth.user = { id: 'user-b' };
    mocks.auth.sessionIdentityEpoch = 2;
    mocks.auth.sessionEpoch = 2;
    rerender(<NudgePanel onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(2));

    const oldView = structuredClone(view);
    const oldObservation = oldView.items[0]?.observations[0]?.observation;
    if (!oldObservation) throw new Error('test fixture is missing its observation');
    oldObservation.title = 'Old account purpose';
    resolveOld(oldView);
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Nudge.loading'));
    expect(screen.queryByText('Old account purpose')).toBeNull();

    const newView = structuredClone(view);
    const newObservation = newView.items[0]?.observations[0]?.observation;
    if (!newObservation) throw new Error('test fixture is missing its observation');
    newObservation.title = 'New account purpose';
    resolveNew(newView);
    expect(await screen.findByText('New account purpose')).toBeTruthy();
    expect(screen.queryByText('Old account purpose')).toBeNull();
  });
});
