import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { NudgeView } from '@/lib/nudges';

const mocks = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  push: vi.fn(),
  confirmNavigation: vi.fn(() => true),
  t: vi.fn((key: string) => key),
  auth: { user: { id: 'user-a' }, sessionIdentityEpoch: 1, sessionEpoch: 1, isDemoSession: false },
}));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));
vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: mocks.t }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => mocks.auth }));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }) }));

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

function handlerResponse(value?: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => value } as Response;
}

function enqueueHandlerResponses(...responses: Response[]) {
  const queue = [...responses];
  mocks.apiFetch.mockImplementation(async () => {
    const response = queue.shift();
    if (!response) throw new Error('unexpected Nudge transport request');
    return response;
  });
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
    render(<NudgePanel onClose={vi.fn()} />);
    expect(await screen.findByText('Nudge.partial')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.markRead' }));

    expect(await screen.findByText('Refreshed by persisted GET')).toBeTruthy();
    expect(screen.getByText('Nudge.partial')).toBeTruthy();
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
    render(<NudgePanel onClose={vi.fn()} />);
    expect(await screen.findByText('Nudge.partial')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Open Project description' }));
    await waitFor(() => expect(mocks.push).toHaveBeenCalledWith('/profile?project_id=project-a#project-description'));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(3));
    expect(screen.getByText('Nudge.partial')).toBeTruthy();

    fireEvent.click(await screen.findByRole('button', { name: 'Nudge.dismiss' }));
    expect(await screen.findByText('Nudge.empty')).toBeTruthy();
    expect(screen.getByText('Nudge.partial')).toBeTruthy();
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
    render(<NudgePanel onClose={vi.fn()} />);
    expect(await screen.findByText('Nudge.partial')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));

    await waitFor(() => expect(screen.queryByText('Nudge.partial')).toBeNull());
    expect(mocks.apiFetch.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/v1/nudges/reconcile', 'POST'],
      ['/api/v1/nudges/reconcile', 'POST'],
    ]);
  });
});
