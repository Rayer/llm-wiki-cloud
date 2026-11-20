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
  t: vi.fn((key: string, params?: Record<string, string | number>) => { void params; return key; }),
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

function expectNoInternalStatus(messages: typeof enMessages | typeof zhTWMessages) {
  const messagesToHide = [messages.Nudge.partial, messages.Nudge.partialCue, ...Object.values(messages.Nudge.status)];
  for (const message of messagesToHide) expect(screen.queryByText(message)).toBeNull();
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
    mocks.t.mockImplementation((key, params) => {
      const messages = mocks.locale === 'zh-TW' ? zhTWMessages : enMessages;
      if (key === 'Nudge.empty') return messages.Nudge.empty;
      if (key === 'Nudge.stale') return messages.Nudge.stale;
      if (key === 'Nudge.partial') return messages.Nudge.partial;
      if (!key.startsWith('Nudge.status.')) return key;
      const value = key.split('.').reduce<unknown>((current, part) => (
        current && typeof current === 'object' ? (current as Record<string, unknown>)[part] : undefined
      ), messages);
      if (typeof value !== 'string') return key;
      return value.replace(/\{(\w+)\}/g, (_, name: string) => String(params?.[name] ?? `{${name}}`));
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it.each([
    { locale: 'en' as const, messages: enMessages },
    { locale: 'zh-TW' as const, messages: zhTWMessages },
  ])('keeps unresolved history partial out of the closed and open Nudge in $locale', async ({ locale, messages }) => {
    mocks.locale = locale;
    const unresolved: NudgeView = {
      schema_version: 1,
      partial: true,
      failures: [
        { project_id: 'project-a', safe_class: 'pipeline_history_partial' },
        { project_id: 'project-a', safe_class: 'pipeline_history_partial' },
        { project_id: 'project-b', safe_class: 'pipeline_history_partial' },
        { project_id: 'project-c', safe_class: 'pipeline_history_partial' },
      ],
      notices: [],
      items: [],
    };
    const pendingResponse = deferred<Response>();
    enqueueHandlerResponses(pendingResponse.promise);
    const onOpen = vi.fn();
    const onClose = vi.fn();
    const panel = render(<NudgePanel open={false} onOpen={onOpen} onClose={onClose} projects={[
      { id: 'project-a', name: 'Lifestyle Wiki', createdAt: null, lastUsedAt: null },
      { id: 'project-b', name: 'project-b', createdAt: null, lastUsedAt: null },
      { id: 'project-c', name: '', createdAt: null, lastUsedAt: null },
    ]} projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);

    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeNull();
    expectNoInternalStatus(messages);

    await act(async () => {
      pendingResponse.resolve(handlerResponse(unresolved));
      await pendingResponse.promise;
    });
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    expectNoInternalStatus(messages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.open' }));
    expect(onOpen).toHaveBeenCalledOnce();
    panel.rerender(<NudgePanel open onOpen={onOpen} onClose={onClose} projects={[
      { id: 'project-a', name: 'Lifestyle Wiki', createdAt: null, lastUsedAt: null },
      { id: 'project-b', name: 'project-b', createdAt: null, lastUsedAt: null },
      { id: 'project-c', name: '', createdAt: null, lastUsedAt: null },
    ]} projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);
    expect(await screen.findByText(messages.Nudge.empty)).toBeTruthy();
    expect(screen.queryByRole('status')).toBeNull();
    expectNoInternalStatus(messages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.close' }));
    expect(onClose).toHaveBeenCalledOnce();
    panel.rerender(<NudgePanel open={false} onOpen={onOpen} onClose={onClose} projects={[
      { id: 'project-a', name: 'Lifestyle Wiki', createdAt: null, lastUsedAt: null },
      { id: 'project-b', name: 'project-b', createdAt: null, lastUsedAt: null },
      { id: 'project-c', name: '', createdAt: null, lastUsedAt: null },
    ]} projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Nudge.open, Nudge.unreadCue' })).toBeNull();
    expectNoInternalStatus(messages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it('keeps saved items and normal attention calls without showing history partial status', async () => {
    const refreshed = structuredClone(partialView);
    refreshed.partial = false;
    refreshed.failures = [];
    const observation = refreshed.items[0]?.observations[0]?.observation;
    if (!observation) throw new Error('test fixture is missing its observation');
    observation.title = 'Refreshed by persisted GET';
    enqueueHandlerResponses(handlerResponse(partialView), handlerResponse(undefined, 204), handlerResponse(refreshed));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText('Pipeline did not publish')).toBeTruthy();
    expectNoInternalStatus(enMessages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await screen.findByText('Refreshed by persisted GET')).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('preserves the three-Wiki POST/GET/reconcile sequence without presenting its internal reasons', async () => {
    const sourcePartial = structuredClone(partialView);
    sourcePartial.failures = [
      { project_id: 'project-a', safe_class: 'pipeline_history_partial' },
      { project_id: 'project-a', safe_class: 'pipeline_history_partial' },
      { project_id: 'project-b', safe_class: 'pipeline_history_partial' },
      { project_id: 'project-c', safe_class: 'pipeline_history_partial' },
    ];
    sourcePartial.notices = [];

    const projectionPartial = structuredClone(sourcePartial);
    projectionPartial.failures = [
      { safe_class: 'project_context_unavailable' },
      { safe_class: 'project_context_unavailable' },
    ];
    projectionPartial.notices = [
      { source_key: 'pipeline-fail:project-a', state: 'stale', safe_class: 'project_context_changed' },
      { source_key: 'pipeline-fail:project-a', state: 'stale', safe_class: 'project_context_changed' },
    ];
    projectionPartial.items[0]!.attention_state = 'read';

    const cleanProjection = structuredClone(projectionPartial);
    cleanProjection.partial = false;
    cleanProjection.failures = [];
    cleanProjection.notices = [];
    const cleanReconcile = structuredClone(cleanProjection);
    cleanReconcile.items = [];

    enqueueHandlerResponses(
      handlerResponse(sourcePartial),
      handlerResponse(undefined, 204),
      handlerResponse(projectionPartial),
      handlerResponse(undefined, 204),
      handlerResponse(cleanProjection),
      handlerResponse(cleanReconcile),
    );
    const projects = [
      { id: 'project-a', name: 'Lifestyle Wiki', createdAt: null, lastUsedAt: null },
      { id: 'project-b', name: 'project-b', createdAt: null, lastUsedAt: null },
      { id: 'project-c', name: '', createdAt: null, lastUsedAt: null },
    ];
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} projects={projects}
      projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);

    const sourceBody = partialView.items[0]!.observations[0]!.observation!.body;
    expect(await screen.findByText(sourceBody)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await within(sourceSection(sourceBody)).findByText(enMessages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(enMessages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markUnread' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(5));
    expect(within(sourceSection(sourceBody)).queryByText(enMessages.Nudge.stale)).toBeNull();
    expectNoInternalStatus(enMessages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    expect(await screen.findByText(enMessages.Nudge.empty)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it('keeps source and projection stale evidence independently until a clean reconcile', async () => {
    const sourcePartial = structuredClone(partialView);
    const item = sourcePartial.items[0]!;
    const sourceObservation = item.observations[0]!;
    const observationB = structuredClone(sourceObservation);
    observationB.source_key = 'pipeline-fail:project-b';
    observationB.observation!.project_id = 'project-b';
    observationB.observation!.project_name = 'Project B';
    observationB.observation!.execution_id = 'execution-b';
    observationB.observation!.title = 'Pipeline failure in project B';
    observationB.observation!.body = 'Project B pipeline evidence is available.';
    item.observations.push(observationB);
    sourcePartial.failures = [{ project_id: 'project-a', safe_class: 'pipeline_history_partial' }];
    sourcePartial.notices = [
      { source_key: 'pipeline-fail:project-a', state: 'stale', safe_class: 'project_context_changed' },
    ];

    const projectionPartial = structuredClone(sourcePartial);
    projectionPartial.notices = [
      { source_key: 'pipeline-fail:project-b', state: 'stale', safe_class: 'project_context_changed' },
    ];
    projectionPartial.items[0]!.attention_state = 'read';

    const cleanProjection = structuredClone(projectionPartial);
    cleanProjection.partial = false;
    cleanProjection.failures = [];
    cleanProjection.notices = [];
    const cleanReconcile = structuredClone(cleanProjection);

    enqueueHandlerResponses(
      handlerResponse(sourcePartial),
      handlerResponse(undefined, 204),
      handlerResponse(projectionPartial),
      handlerResponse(undefined, 204),
      handlerResponse(cleanProjection),
      handlerResponse(cleanReconcile),
    );
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    const bodyA = sourceObservation.observation!.body;
    const bodyB = observationB.observation!.body;
    expect(await screen.findByText(bodyA)).toBeTruthy();
    expect(screen.getByText(bodyB)).toBeTruthy();
    expect(within(sourceSection(bodyA)).getByText(enMessages.Nudge.stale)).toBeTruthy();
    expect(within(sourceSection(bodyB)).queryByText(enMessages.Nudge.stale)).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    expect(await within(sourceSection(bodyA)).findByText(enMessages.Nudge.stale)).toBeTruthy();
    expect(await within(sourceSection(bodyB)).findByText(enMessages.Nudge.stale)).toBeTruthy();
    expect(screen.getByText(bodyA)).toBeTruthy();
    expect(screen.getByText(bodyB)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markUnread' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(5));
    await waitFor(() => {
      expect(within(sourceSection(bodyA)).getByText(enMessages.Nudge.stale)).toBeTruthy();
      expect(within(sourceSection(bodyB)).queryByText(enMessages.Nudge.stale)).toBeNull();
    });
    expect(screen.getByText(bodyA)).toBeTruthy();
    expect(screen.getByText(bodyB)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(6));
    await waitFor(() => {
      expect(within(sourceSection(bodyA)).queryByText(enMessages.Nudge.stale)).toBeNull();
      expect(within(sourceSection(bodyB)).queryByText(enMessages.Nudge.stale)).toBeNull();
    });
    expect(screen.getByText(bodyA)).toBeTruthy();
    expect(screen.getByText(bodyB)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it('hides unknown source classes while preserving the prior view and a true GET failure alert', async () => {
    const unknown = structuredClone(partialView);
    unknown.failures = [{ project_id: 'project-a', safe_class: 'new_unknown_class' }];
    const pendingRead = deferred<Response>();
    enqueueHandlerResponses(handlerResponse(unknown), handlerResponse(undefined, 204), pendingRead.promise);
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);

    const originalTitle = partialView.items[0]!.observations[0]!.observation!.title;
    expect(await screen.findByText(originalTitle)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(screen.queryByText('new_unknown_class')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    pendingRead.reject(new Error('synthetic persisted GET timeout'));

    expect(await screen.findByText(originalTitle)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(await screen.findByText('Nudge.loadFailed', { selector: '[role="alert"]' })).toBeTruthy();
  });

  it('hides inherited technical classes but keeps source-matched stale status on its saved card', async () => {
    const inherited = structuredClone(partialView);
    const inheritedClasses = ['constructor', 'toString', '__proto__'];
    inherited.failures = [
      { project_id: 'project-a', safe_class: 'pipeline_history_partial' },
      ...inheritedClasses.map((safe_class) => ({ project_id: 'project-a', safe_class })),
    ];
    inherited.notices = inheritedClasses.map((safe_class) => ({
      source_key: 'pipeline-fail:project-a', state: 'stale', safe_class,
    }));
    enqueueHandlerResponses(handlerResponse(inherited));
    const sourceBody = partialView.items[0]!.observations[0]!.observation!.body;

    expect(() => render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />)).not.toThrow();
    expect(await screen.findByText(sourceBody)).toBeTruthy();
    expect(within(sourceSection(sourceBody)).getByText(enMessages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    for (const safeClass of inheritedClasses) expect(screen.queryByText(safeClass)).toBeNull();
  });

  it('keeps a valid Release card without turning source failures or unmatched notices into Nudge content', async () => {
    const releaseView = structuredClone(releaseOutageView);
    releaseView.failures = [
      { safe_class: 'release-content-unavailable' },
      { safe_class: 'release-locale-content-unavailable' },
    ];
    releaseView.notices = [
      { source_key: releaseSourceKey, state: 'missing', safe_class: 'current-version-not-indexed' },
      { source_key: 'pipeline-fail:dev', state: 'stale', safe_class: 'project_context_changed' },
      { source_key: 'unknown-source:dev', state: 'stale', safe_class: 'project_context_changed' },
    ];
    const projects = [{ id: 'dev', name: 'Development Wiki', createdAt: null, lastUsedAt: null }];
    enqueueHandlerResponses(handlerResponse(releaseView));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} projects={projects}
      projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);

    expect(await screen.findByText('Release notes are available')).toBeTruthy();
    expect(screen.getAllByRole('article')).toHaveLength(1);
    expect(screen.queryByText('Development Wiki')).toBeNull();
    expectNoInternalStatus(enMessages);
  });

  it('keeps navigation-read and dismiss actions available without a source banner, including the empty last item', async () => {
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
    expect(await screen.findByText('Pipeline did not publish')).toBeTruthy();
    expectNoInternalStatus(enMessages);

    fireEvent.click(screen.getByRole('link', { name: 'Open Project description' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(screen.getByText('Pipeline did not publish')).toBeTruthy();
    expectNoInternalStatus(enMessages);

    fireEvent.click(await screen.findByRole('button', { name: 'Nudge.dismiss' }));
    expect(await screen.findByText(enMessages.Nudge.empty)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
      ['/api/v1/nudges/pipeline-failure/attention', 'PATCH'],
      ['/api/v1/nudges', 'GET'],
    ]);
  });

  it('keeps manual reconciliation available while leaving partial source status out of Nudge', async () => {
    const recovered = structuredClone(partialView);
    recovered.partial = false;
    recovered.failures = [];
    enqueueHandlerResponses(handlerResponse(partialView), handlerResponse(recovered));
    render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByText('Pipeline did not publish')).toBeTruthy();
    expectNoInternalStatus(enMessages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));
    expect(screen.getByText('Pipeline did not publish')).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });

  it.each([
    { locale: 'en' as const, messages: enMessages },
    { locale: 'zh-TW' as const, messages: zhTWMessages },
  ])('keeps source-matched stale state through a clean GET and clears it on accepted reconcile ($locale)', async ({ locale, messages }) => {
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
    const releaseBody = releaseOutageView.items[0]!.observations[0]!.observation!.body;
    expect(await screen.findByText(releaseBody)).toBeTruthy();
    expect(within(sourceSection(releaseBody)).getByText(messages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(messages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await screen.findByText('Persisted Release item after GET')).toBeTruthy();
    expect(within(sourceSection('Version 1.0.0 release notes are ready to read.')).getByText(messages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(messages);

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(screen.queryByText(messages.Nudge.stale)).toBeNull());
    expectNoInternalStatus(messages);
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
    expectNoInternalStatus(enMessages);
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
    expectNoInternalStatus(enMessages);
    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
    expect(await screen.findByText('Recovered latest Release state')).toBeTruthy();
    await waitFor(() => expect(screen.queryByText(enMessages.Nudge.stale)).toBeNull());
    expectNoInternalStatus(enMessages);

    await act(async () => {
      pendingPatch.resolve(handlerResponse(undefined, 204));
      await pendingPatch.promise;
    });

    expect(await screen.findByText('Older partial attention GET')).toBeTruthy();
    expectNoInternalStatus(enMessages);
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
    expect(within(sourceSection('Version 1.0.0 release notes are ready to read.')).getByText(enMessages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(enMessages);

    await act(async () => {
      pendingPatch.reject(new Error('test-only attention write failure'));
      await pendingPatch.promise.catch(() => {});
    });

    expect(await screen.findByText('Older partial GET after failed attention update')).toBeTruthy();
    expect(within(sourceSection('Version 1.0.0 release notes are ready to read.')).getByText(enMessages.Nudge.stale)).toBeTruthy();
    expectNoInternalStatus(enMessages);
    expect(screen.getByRole('alert')).toBeTruthy();
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
    expectNoInternalStatus(enMessages);

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
    expectNoInternalStatus(enMessages);
  });

  it('does not expose internal Wiki status labels before or after a mounted account switch', async () => {
    const priorAccount = structuredClone(partialView);
    priorAccount.failures = [{ project_id: 'private-project', safe_class: 'pipeline_history_partial' }];
    const nextAccount = structuredClone(priorAccount);
    const projects = [{ id: 'private-project', name: 'Prior Account Wiki', createdAt: null, lastUsedAt: null }];
    enqueueHandlerResponses(handlerResponse(priorAccount), handlerResponse(nextAccount));
    const panel = render(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} projects={projects}
      projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);
    expect(await screen.findByText('Pipeline did not publish')).toBeTruthy();
    expect(screen.queryByText('Prior Account Wiki')).toBeNull();
    expectNoInternalStatus(enMessages);

    mocks.auth.user = { id: 'user-b' };
    mocks.auth.sessionIdentityEpoch = 2;
    mocks.auth.sessionEpoch = 2;
    panel.rerender(<NudgePanel open onOpen={vi.fn()} onClose={vi.fn()} projects={projects}
      projectsIdentity={{ ownerId: 'user-a', sessionIdentityEpoch: 1, sessionEpoch: 1 }} />);

    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(2));
    expect(screen.getByText('Pipeline did not publish')).toBeTruthy();
    expect(screen.queryByText('Prior Account Wiki')).toBeNull();
    expectNoInternalStatus(enMessages);
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

    fireEvent.click(within(sourceSection(alphaBody)).getByRole('button', { name: 'Copy run ID and open run' }));

    await waitFor(() => expect(alphaPopup.location.replace).toHaveBeenCalledWith('/status?project_id=qa-alpha&execution_id=legacy-qa-alpha'));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(writeText).toHaveBeenNthCalledWith(3, 'legacy-qa-alpha');
    expect(within(sourceSection(alphaBody)).getByRole('button', { name: 'Nudge.copied' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('button', { name: 'Copy run ID and open run' })).toBeTruthy();

    fireEvent.click(within(sourceSection(betaBody)).getByRole('button', { name: 'Copy run ID and open run' }));

    await waitFor(() => expect(betaPopup.location.replace).toHaveBeenCalledWith('/status?project_id=qa-beta&execution_id=legacy-qa-beta'));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(5));
    expect(writeText).toHaveBeenNthCalledWith(4, 'legacy-qa-beta');
    expect(within(sourceSection(alphaBody)).getByRole('button', { name: 'Copy run ID and open run' })).toBeTruthy();
    expect(within(sourceSection(betaBody)).getByRole('button', { name: 'Nudge.copied' })).toBeTruthy();
  });
});
