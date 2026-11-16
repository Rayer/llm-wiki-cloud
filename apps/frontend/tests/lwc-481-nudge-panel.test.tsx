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
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }),
  NavigationLink: ({ href, onNavigate, children, ...props }: {
    href: string; onNavigate?: (event: { preventDefault: () => void }) => void; children: React.ReactNode; [key: string]: unknown;
  }) => <a href={href} {...props} onClick={(event) => { event.preventDefault(); if (mocks.confirmNavigation()) onNavigate?.(event); }}>{children}</a>,
}));

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

  it('reconciles once while closed, shows the unread cue, and reuses that view when opened', async () => {
    const onOpen = vi.fn();
    const onClose = vi.fn();
    const { rerender } = render(<NudgePanel open={false} onOpen={onOpen} onClose={onClose} />);

    const trigger = await screen.findByRole('button', { name: 'Nudge.open, Nudge.unreadCue' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1);
    fireEvent.click(trigger);
    expect(onOpen).toHaveBeenCalledTimes(1);

    rerender(<NudgePanel open onOpen={onOpen} onClose={onClose} />);
    expect(await screen.findByText('content-purpose-gap')).toBeTruthy();
    expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1);
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it('toggles ordinary attention read and unread with the same captured ID and persisted readback', async () => {
    const readView = structuredClone(view);
    readView.items[0]!.attention_state = 'read';
    const unreadView = structuredClone(view);
    mocks.getNudges.mockResolvedValueOnce(readView).mockResolvedValueOnce(unreadView);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    await screen.findByText('content-purpose-gap');

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    expect(await screen.findByRole('button', { name: 'Nudge.markUnread' })).toBeTruthy();
    expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(1, 'content-purpose-gap', 'attention-a', 'read');

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markUnread' }));
    expect(await screen.findByRole('button', { name: 'Nudge.markRead' })).toBeTruthy();
    expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(2, 'content-purpose-gap', 'attention-a', 'unread');
    expect(screen.getByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeTruthy();
  });

  it('keeps the Shell unread cue until every ordinary item is read', async () => {
    const first = structuredClone(view.items[0]!);
    const second = structuredClone(view.items[0]!);
    first.attention_id = 'attention-first';
    second.category = 'release-notes';
    second.attention_id = 'attention-second';
    second.observations[0]!.source_key = 'release-notes:test';
    second.observations[0]!.observation!.title = 'Second unread item';
    const twoUnread = structuredClone(view);
    twoUnread.items = [first, second];
    const oneUnread = structuredClone(twoUnread);
    oneUnread.items[0]!.attention_state = 'read';
    const allRead = structuredClone(oneUnread);
    allRead.items[1]!.attention_state = 'read';
    mocks.reconcileNudges.mockResolvedValueOnce(twoUnread);
    mocks.getNudges.mockResolvedValueOnce(oneUnread).mockResolvedValueOnce(allRead);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    await screen.findByText('Second unread item');
    expect(screen.getByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeTruthy();
    fireEvent.click(within(screen.getByText('content-purpose-gap').closest('article')!).getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(1, 'content-purpose-gap', 'attention-first', 'read'));
    expect(screen.getByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeTruthy();

    fireEvent.click(within(screen.getByText('release-notes').closest('article')!).getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenNthCalledWith(2, 'release-notes', 'attention-second', 'read'));
    expect(screen.getByRole('button', { name: 'Nudge.open' })).toBeTruthy();
  });

  it('does not mark a typed target read when NavigationBlocker cancels it', async () => {
    const linkView = structuredClone(view);
    const action = linkView.items[0]?.observations[0]?.observation?.actions[0];
    if (!action) throw new Error('test fixture is missing its action');
    action.kind = 'link';
    action.label = 'Open Project description';
    mocks.reconcileNudges.mockResolvedValueOnce(linkView);
    mocks.confirmNavigation.mockReturnValueOnce(false);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('link', { name: 'Open Project description' }));
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
    expect(screen.getByText('Nudge.unread')).toBeTruthy();
  });

  it('does not copy, open, or mark seen when NavigationBlocker cancels copy-then-link', async () => {
    mocks.confirmNavigation.mockReturnValueOnce(false);
    const opened = popup();
    vi.mocked(window.open).mockReturnValue(opened);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    expect(writeText).not.toHaveBeenCalled();
    expect(window.open).not.toHaveBeenCalled();
    expect(opened.location.replace).not.toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it.each([
    { gesture: 'plain click', modifiers: {} },
    { gesture: 'Cmd-click', modifiers: { metaKey: true } },
    { gesture: 'Ctrl-click', modifiers: { ctrlKey: true } },
  ])('keeps copy-then-link copy-first for $gesture', async ({ modifiers }) => {
    const opened = popup();
    vi.mocked(window.open).mockReturnValue(opened);
    let finishCopy!: () => void;
    writeText.mockReturnValueOnce(new Promise<void>((resolve) => { finishCopy = resolve; }));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    const action = await screen.findByRole('button', { name: 'Copy and open description' });
    expect(action.getAttribute('href')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Copy and open description' })).toBeNull();
    fireEvent.click(action, modifiers);

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(window.open).toHaveBeenCalledWith('about:blank', '_blank');
    expect(writeText).toHaveBeenCalledWith('Published overview text to preserve');
    expect(opened.location.replace).not.toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();

    await act(async () => {
      finishCopy();
      await Promise.resolve();
    });
    await waitFor(() => expect(opened.location.replace).toHaveBeenCalledWith('/profile?project_id=project-a#project-description'));
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith('content-purpose-gap', 'attention-a', 'read'));
  });

  it('does not navigate, copy, or mark seen on a middle-button action', async () => {
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    const action = await screen.findByRole('button', { name: 'Copy and open description' });
    expect(action.getAttribute('href')).toBeNull();
    fireEvent(action, new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 }));

    expect(mocks.confirmNavigation).not.toHaveBeenCalled();
    expect(writeText).not.toHaveBeenCalled();
    expect(window.open).not.toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
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
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

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
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    await waitFor(() => expect(opened.location.replace).toHaveBeenCalledWith('/profile?project_id=project-a#project-description'));
    expect(writeText).toHaveBeenCalledWith('Published overview text to preserve');
    expect(window.open).toHaveBeenCalledWith('about:blank', '_blank');
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith('content-purpose-gap', 'attention-a', 'read'));
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('keeps the accepted destination blank until clipboard success', async () => {
    const opened = popup();
    vi.mocked(window.open).mockReturnValue(opened);
    let finishCopy!: () => void;
    writeText.mockReturnValueOnce(new Promise<void>((resolve) => { finishCopy = resolve; }));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));
    expect(window.open).toHaveBeenCalledWith('about:blank', '_blank');
    expect(opened.location.replace).not.toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();

    await act(async () => {
      finishCopy();
      await Promise.resolve();
    });
    await waitFor(() => expect(opened.location.replace).toHaveBeenCalledWith('/profile?project_id=project-a#project-description'));
    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith('content-purpose-gap', 'attention-a', 'read'));
  });

  it('shows selectable exact text and an explicit link when clipboard access is denied', async () => {
    const opened = popup();
    writeText.mockRejectedValueOnce(new Error('clipboard denied'));
    vi.mocked(window.open).mockReturnValue(opened);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Nudge.copyFailed');
    expect((screen.getByLabelText('Nudge.selectCopy') as HTMLTextAreaElement).value).toBe('Published overview text to preserve');
    expect(opened.location.replace).not.toHaveBeenCalled();
    expect(opened.close).toHaveBeenCalled();
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it('keeps copy success truthful and exposes a target link when the popup is blocked', async () => {
    vi.mocked(window.open).mockReturnValue(null);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy and open description' }));

    expect((await screen.findByRole('alert')).textContent).toContain('Nudge.popupBlocked');
    expect(writeText).toHaveBeenCalledWith('Published overview text to preserve');
    const link = screen.getByRole('link', { name: 'Nudge.openTarget' });
    expect(link.getAttribute('href')).toBe('/profile?project_id=project-a#project-description');
    expect(mocks.setNudgeAttention).not.toHaveBeenCalled();
  });

  it('reports when an accepted Notes navigation cannot save the seen read state', async () => {
    const releaseView = structuredClone(view);
    const item = releaseView.items[0]!;
    item.category = 'release-notes';
    item.attention_id = 'release-attention-a';
    const action = item.observations[0]?.observation?.actions[0];
    if (!action) throw new Error('test fixture is missing its action');
    action.kind = 'link';
    action.label = 'Open Release notes';
    action.target = { kind: 'release-notes', mode: 'published-version', version: '1.0.0' };
    mocks.reconcileNudges.mockResolvedValueOnce(releaseView);
    mocks.getNudges.mockResolvedValueOnce(releaseView);
    mocks.resolveNudgeTarget.mockReturnValue('/release-notes?version=1.0.0');
    mocks.setNudgeAttention.mockRejectedValueOnce(new Error('HTTP 503'));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('link', { name: 'Open Release notes' }));

    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledWith(
      'release-notes', 'release-attention-a', 'read',
    ));
    expect(await screen.findByText('release-notes')).toBeTruthy();
    expect(await screen.findByText('Nudge.readNotConfirmed', { selector: '[role="alert"]' })).toBeTruthy();
    expect(screen.getByText('Nudge.unread')).toBeTruthy();
  });

  it('keeps a stale read conflict on the original ID and reports the refreshed item as unread', async () => {
    const releaseView = structuredClone(view);
    const item = releaseView.items[0]!;
    item.category = 'release-notes';
    item.attention_id = 'release-attention-old';
    const action = item.observations[0]?.observation?.actions[0];
    if (!action) throw new Error('test fixture is missing its action');
    action.kind = 'link';
    action.label = 'Open Release notes';
    action.target = { kind: 'release-notes', mode: 'published-version', version: '1.0.0' };
    const refreshedView = structuredClone(releaseView);
    refreshedView.items[0]!.attention_id = 'release-attention-new';
    mocks.reconcileNudges.mockResolvedValueOnce(releaseView);
    mocks.getNudges.mockResolvedValueOnce(refreshedView);
    mocks.resolveNudgeTarget.mockReturnValue('/release-notes?version=1.0.0');
    mocks.setNudgeAttention.mockRejectedValueOnce(new Error('HTTP 409'));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('link', { name: 'Open Release notes' }));

    await waitFor(() => expect(mocks.setNudgeAttention).toHaveBeenCalledTimes(1));
    expect(mocks.setNudgeAttention).toHaveBeenCalledWith('release-notes', 'release-attention-old', 'read');
    expect(await screen.findByText('Nudge.readNotConfirmed', { selector: '[role="alert"]' })).toBeTruthy();
    expect(screen.getByText('Nudge.unread')).toBeTruthy();
    expect(mocks.setNudgeAttention).toHaveBeenCalledTimes(1);
  });

  it('shows explicit partial source status returned by manual reconcile', async () => {
    const partialView = structuredClone(view);
    partialView.partial = true;
    partialView.failures = [{ safe_class: 'pipeline_observer_not_qualified' }];
    mocks.reconcileNudges.mockResolvedValueOnce(partialView);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

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
      render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
      expect(await screen.findByText('Nudge.partial')).toBeTruthy();

      if (attentionPath === 'read') {
        fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
      } else if (attentionPath === 'navigation-read') {
        fireEvent.click(await screen.findByRole('link', { name: 'Copy and open description' }));
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
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
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
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
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
    const { rerender } = render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    await waitFor(() => expect(mocks.reconcileNudges).toHaveBeenCalledTimes(1));
    mocks.auth.user = { id: 'user-b' };
    mocks.auth.sessionIdentityEpoch = 2;
    mocks.auth.sessionEpoch = 2;
    rerender(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
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
