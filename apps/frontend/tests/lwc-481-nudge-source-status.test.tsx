import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import enMessages from '@/messages/en.json';
import zhTWMessages from '@/messages/zh-TW.json';
import type { NudgeView } from '@/lib/nudges';
import multiSourceWire from './fixtures/lwc-481-nudge-multisource-wire.json';

const mocks = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  push: vi.fn(),
  confirmNavigation: vi.fn(() => true),
  locale: 'en' as 'en' | 'zh-TW',
  t: vi.fn((key: string) => key),
  auth: { user: { id: 'user-a' }, sessionIdentityEpoch: 1, sessionEpoch: 1, isDemoSession: false },
}));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));
vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: mocks.t }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => mocks.auth }));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }),
  NavigationLink: ({ href, onNavigate, children, ...props }: {
    href: string; onNavigate?: (event: { preventDefault: () => void }) => void; children: React.ReactNode; [key: string]: unknown;
  }) => <a href={href} {...props} onClick={(event) => { event.preventDefault(); if (mocks.confirmNavigation()) onNavigate?.(event); }}>{children}</a>,
}));

import { NudgePanel } from '@/components/NudgePanel';

const partialView: NudgeView = {
  schema_version: 1,
  partial: true,
  failures: [{ project_id: 'project-a', safe_class: 'pipeline_observer_not_qualified' }],
  notices: [],
  items: [{
    category: 'pipeline-failure', attention_id: 'attention-a', attention_state: 'unread', severity: 'attention',
    observations: [{
      source_key: 'pipeline-fail:project-a', source_revision: 1, active: true,
      observation: {
        project_id: 'project-a', project_name: 'Named Project', execution_id: 'execution-1',
        title: 'Pipeline did not publish', body: 'The run ended before publication.', severity: 'attention',
        actions: [{ id: 'open-run', kind: 'link', label: 'Open Project description', target: { kind: 'project-description', project_id: 'project-a' } }],
      },
    }],
  }],
};

const releaseSourceKey = 'release-notes:llm-wiki-cloud:dev';
const releaseOutageView: NudgeView = {
  schema_version: 1,
  partial: true,
  failures: [{ safe_class: 'release-index-unavailable' }],
  notices: [{ source_key: releaseSourceKey, state: 'stale', safe_class: 'currentness-unverified' }],
  items: [{
    category: 'release-notes', attention_id: 'release-attention', attention_state: 'unread', severity: 'info',
    observations: [{
      source_key: releaseSourceKey, source_revision: 1, active: true,
      observation: {
        title: 'Release notes are available', body: 'Version 1.0.0 release notes are ready to read.',
        severity: 'info', actions: [],
      },
    }],
  }],
};

function handlerResponse(value?: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => value } as Response;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function enqueueHandlerResponses(...responses: Array<Response | Promise<Response>>) {
  const queue = [...responses];
  mocks.apiFetch.mockImplementation(async () => {
    const response = queue.shift();
    if (!response) throw new Error('unexpected Nudge transport request');
    return response;
  });
}

function sourceSection(body: string) {
  const section = screen.getByText(body).closest<HTMLElement>('div.border-t');
  if (!section) throw new Error(`Nudge source section not found for: ${body}`);
  return section;
}

function popup() {
  return { location: { replace: vi.fn() }, close: vi.fn(), opener: null } as unknown as Window;
}

describe('LWC-481 source status and attention transport', () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    mocks.push.mockReset();
    mocks.confirmNavigation.mockReturnValue(true);
    mocks.auth.user = { id: 'user-a' };
    mocks.auth.sessionIdentityEpoch = 1;
    mocks.auth.sessionEpoch = 1;
    mocks.auth.isDemoSession = false;
    mocks.locale = 'en';
    mocks.t.mockImplementation((key) => {
      const messages = mocks.locale === 'zh-TW' ? zhTWMessages : enMessages;
      if (key === 'Nudge.stale') return messages.Nudge.stale;
      if (key === 'Nudge.partial') return messages.Nudge.partial;
      return key;
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('keeps partial status through real GET, PATCH and GET client calls after read', async () => {
    const refreshed = structuredClone(partialView);
    refreshed.partial = false;
    refreshed.failures = [];
    const observation = refreshed.items[0]?.observations[0]?.observation;
    if (!observation) throw new Error('test fixture is missing its observation');
    observation.title = 'Refreshed by persisted GET';
    enqueueHandlerResponses(handlerResponse(partialView), handlerResponse(undefined, 204), handlerResponse(refreshed));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(enMessages.Nudge.partial)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await screen.findByText('Refreshed by persisted GET')).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.partial)).toBeTruthy();
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('keeps partial status through navigation-read and dismiss, including the empty last item', async () => {
    const afterRead = structuredClone(partialView);
    afterRead.partial = false;
    afterRead.failures = [];
    const afterDismiss = structuredClone(afterRead);
    afterDismiss.items = [];
    enqueueHandlerResponses(
      handlerResponse(partialView), handlerResponse(undefined, 204), handlerResponse(afterRead),
      handlerResponse(undefined, 204), handlerResponse(afterDismiss),
    );
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(enMessages.Nudge.partial)).toBeTruthy();

    fireEvent.click(screen.getByRole('link', { name: 'Open Project description' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(screen.getByText(enMessages.Nudge.partial)).toBeTruthy();

    fireEvent.click(await screen.findByRole('button', { name: 'Nudge.dismiss' }));
    expect(await screen.findByText('Nudge.empty')).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.partial)).toBeTruthy();
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('clears partial status only when the actual refresh button receives a successful reconcile response', async () => {
    const recovered = structuredClone(partialView);
    recovered.partial = false;
    recovered.failures = [];
    enqueueHandlerResponses(handlerResponse(partialView), handlerResponse(recovered));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(enMessages.Nudge.partial)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(screen.queryByText(enMessages.Nudge.partial)).toBeNull());
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it.each([
    { locale: 'en' as const, staleLabel: enMessages.Nudge.stale, partialLabel: enMessages.Nudge.partial },
    { locale: 'zh-TW' as const, staleLabel: zhTWMessages.Nudge.stale, partialLabel: zhTWMessages.Nudge.partial },
  ])('shows a matching Release stale notice from outage through read GET, then clears on recovery ($locale)', async ({ locale, staleLabel, partialLabel }) => {
    mocks.locale = locale;
    const afterRead = structuredClone(releaseOutageView);
    afterRead.partial = false;
    afterRead.failures = [];
    afterRead.notices = [];
    const item = afterRead.items[0];
    if (!item) throw new Error('test fixture is missing its Release item');
    item.attention_state = 'read';
    const observation = item.observations[0]?.observation;
    if (!observation) throw new Error('test fixture is missing its Release observation');
    observation.title = 'Persisted Release item after GET';
    const recovered = structuredClone(afterRead);

    enqueueHandlerResponses(
      handlerResponse(releaseOutageView),
      handlerResponse(undefined, 204),
      handlerResponse(afterRead),
      handlerResponse(recovered),
    );
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(staleLabel)).toBeTruthy();
    expect(screen.getByText(partialLabel)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await screen.findByText('Persisted Release item after GET')).toBeTruthy();
    expect(screen.getByText(staleLabel)).toBeTruthy();
    expect(screen.getByText(partialLabel)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(screen.queryByText(staleLabel)).toBeNull());
    expect(screen.queryByText(partialLabel)).toBeNull();
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/release-notes/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it('continues to render a persisted stale observation without a transient source notice', async () => {
    const persistedStale = structuredClone(releaseOutageView);
    persistedStale.partial = false;
    persistedStale.failures = [];
    persistedStale.notices = [];
    const item = persistedStale.items[0];
    if (!item) throw new Error('test fixture is missing its Release item');
    const observation = item.observations[0]?.observation;
    if (!observation) throw new Error('test fixture is missing its Release observation');
    observation.stale = true;
    enqueueHandlerResponses(handlerResponse(persistedStale));

    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    expect(await screen.findByText(enMessages.Nudge.stale)).toBeTruthy();
  });

  it('does not resurrect a stale notice when an older partial attention GET follows successful recovery', async () => {
    const initialStale = structuredClone(releaseOutageView);
    const recovered = structuredClone(releaseOutageView);
    recovered.partial = false;
    recovered.failures = [];
    recovered.notices = [];
    recovered.items[0]!.observations[0]!.observation!.title = 'Recovered latest Release state';
    const oldPartialGet = structuredClone(releaseOutageView);
    oldPartialGet.failures = [{ project_id: 'project-other', safe_class: 'project_context_unavailable' }];
    oldPartialGet.notices = [];
    oldPartialGet.items[0]!.observations[0]!.observation!.title = 'Older partial attention GET';
    const pendingPatch = deferred<Response>();
    enqueueHandlerResponses(
      handlerResponse(initialStale),
      pendingPatch.promise,
      handlerResponse(recovered),
      handlerResponse(oldPartialGet),
    );

    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(enMessages.Nudge.stale)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    expect(await screen.findByText('Recovered latest Release state')).toBeTruthy();
    await waitFor(() => expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull());
    expect(screen.queryByText(enMessages.Nudge.partial)).toBeNull();

    await act(async () => {
      pendingPatch.resolve(handlerResponse(undefined, 204));
      await pendingPatch.promise;
    });

    expect(await screen.findByText('Older partial attention GET')).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.partial)).toBeTruthy();
    expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull();
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/release-notes/attention', 'PATCH'],
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('keeps a newer stale notice when an older partial attention GET follows a source outage', async () => {
    const initialCurrent = structuredClone(releaseOutageView);
    initialCurrent.partial = false;
    initialCurrent.failures = [];
    initialCurrent.notices = [];
    initialCurrent.items[0]!.observations[0]!.observation!.title = 'Initially current Release';
    const outage = structuredClone(releaseOutageView);
    outage.items[0]!.observations[0]!.observation!.title = 'Latest Release outage';
    const oldPartialGet = structuredClone(releaseOutageView);
    oldPartialGet.notices = [];
    oldPartialGet.failures = [{ project_id: 'project-other', safe_class: 'project_unavailable' }];
    oldPartialGet.items[0]!.observations[0]!.observation!.title = 'Older partial GET after failed attention update';
    const pendingPatch = deferred<Response>();
    enqueueHandlerResponses(
      handlerResponse(initialCurrent),
      pendingPatch.promise,
      handlerResponse(outage),
      handlerResponse(oldPartialGet),
    );

    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText('Initially current Release')).toBeTruthy();
    expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    expect(await screen.findByText('Latest Release outage')).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.stale)).toBeTruthy();

    await act(async () => {
      pendingPatch.reject(new Error('test-only attention write failure'));
      await pendingPatch.promise.catch(() => {});
    });

    expect(await screen.findByText('Older partial GET after failed attention update')).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.partial)).toBeTruthy();
    expect(screen.getByText(enMessages.Nudge.stale)).toBeTruthy();
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/release-notes/attention', 'PATCH'],
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('ignores a pending old-session attention continuation after the mounted user switches', async () => {
    const oldUserView = structuredClone(releaseOutageView);
    const newUserView = structuredClone(releaseOutageView);
    newUserView.partial = false;
    newUserView.failures = [];
    newUserView.notices = [];
    newUserView.items[0]!.observations[0]!.observation!.title = 'New session Release item';
    const pendingPatch = deferred<Response>();
    enqueueHandlerResponses(handlerResponse(oldUserView), pendingPatch.promise, handlerResponse(newUserView));

    const panel = render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText(enMessages.Nudge.stale)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));

    mocks.auth.user = { id: 'user-b' };
    mocks.auth.sessionIdentityEpoch = 2;
    panel.rerender(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText('New session Release item')).toBeTruthy();
    expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull();

    await act(async () => {
      pendingPatch.resolve(handlerResponse(undefined, 204));
      await pendingPatch.promise;
    });

    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/release-notes/attention', 'PATCH'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
    expect(screen.getByText('New session Release item')).toBeTruthy();
    expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull();
  });

  it('keeps same-id Pipeline copy and copy-then-link feedback bound to each actual source', async () => {
    const wire = structuredClone(multiSourceWire) as unknown as NudgeView;
    const alphaBody = 'Named qa-alpha run legacy-qa-alpha ended without publishing. Open the run to review its safe diagnostic.';
    const betaBody = 'Named qa-beta run legacy-qa-beta ended without publishing. Open the run to review its safe diagnostic.';
    const alphaPopup = popup();
    const betaPopup = popup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    window.open = vi.fn().mockReturnValueOnce(alphaPopup).mockReturnValueOnce(betaPopup);
    enqueueHandlerResponses(
      handlerResponse(wire),
      handlerResponse(undefined, 204), handlerResponse(wire),
      handlerResponse(undefined, 204), handlerResponse(wire),
    );
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    await screen.findByText(alphaBody);
    await screen.findByText(betaBody);

    fireEvent.click(within(sourceSection(alphaBody)).getByRole('button', { name: 'Copy run ID' }));

    await waitFor(() => expect(writeText).toHaveBeenNthCalledWith(1, 'legacy-qa-alpha'));
    expect(within(sourceSection(alphaBody)).getByRole('button', { name: 'Nudge.copied' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('button', { name: 'Copy run ID' })).toBeTruthy();

    fireEvent.click(within(sourceSection(betaBody)).getByRole('button', { name: 'Copy run ID' }));

    await waitFor(() => expect(writeText).toHaveBeenNthCalledWith(2, 'legacy-qa-beta'));
    expect(within(sourceSection(alphaBody)).getByRole('button', { name: 'Copy run ID' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('button', { name: 'Nudge.copied' })).toBeTruthy();

    fireEvent.click(within(sourceSection(alphaBody)).getByRole('link', { name: 'Copy run ID and open run' }));

    await waitFor(() => expect(alphaPopup.location.replace).toHaveBeenCalledWith('/status?project_id=qa-alpha&execution_id=legacy-qa-alpha'));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(writeText).toHaveBeenNthCalledWith(3, 'legacy-qa-alpha');
    expect(within(sourceSection(alphaBody)).getByRole('link', { name: 'Nudge.copied' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('link', { name: 'Copy run ID and open run' })).toBeTruthy();

    fireEvent.click(within(sourceSection(betaBody)).getByRole('link', { name: 'Copy run ID and open run' }));

    await waitFor(() => expect(betaPopup.location.replace).toHaveBeenCalledWith('/status?project_id=qa-beta&execution_id=legacy-qa-beta'));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(5));
    expect(writeText).toHaveBeenNthCalledWith(4, 'legacy-qa-beta');
    expect(within(sourceSection(alphaBody)).getByRole('link', { name: 'Copy run ID and open run' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('link', { name: 'Nudge.copied' })).toBeTruthy();
  });
});
