import React from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentConnectionCheck } from '@/components/AgentConnectionCheck';
import { translate } from '@/lib/i18n';

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  get: vi.fn(),
}));

vi.mock('@/lib/agent-connection', () => ({
  createAgentConnectionCheck: mocks.create,
  getAgentConnectionCheck: mocks.get,
}));

const now = '2026-11-02T12:30:00Z';
const waiting = (setupId = 'setup-a') => ({
  project_id: 'project-a', status: 'waiting' as const, setup_id: setupId,
  created_at: now, expires_at: '2026-11-03T12:30:00Z', confirmed_at: null,
  confirmation_token: 'A'.repeat(43),
});
const t = (key: string, params?: Record<string, string | number>) => translate('en', key, params);

function renderCheck(projectId = 'project-a', identityKey = `user-a:${projectId}`) {
  return render(<AgentConnectionCheck projectId={projectId} identityKey={identityKey} locale="en" t={t} />);
}

beforeEach(() => {
  mocks.create.mockReset();
  mocks.get.mockReset().mockResolvedValue({ project_id: 'project-a', status: 'not_started', confirmed_at: null });
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  Reflect.deleteProperty(document, 'visibilityState');
  vi.clearAllMocks();
});

describe('LWC-468 Agent connection check', () => {
  it('does not create a challenge automatically and copy is not confirmation', async () => {
    mocks.get.mockResolvedValue(waiting());
    renderCheck();
    expect(await screen.findByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(screen.queryByText(t('MCPOAuth.connection.confirmed'))).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.copyPrompt') }));
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
      t('MCPOAuth.connection.confirmPrompt', { setup_id: 'setup-a', confirmation_token: 'A'.repeat(43) }),
    ));
    expect(await screen.findByText(t('MCPOAuth.connection.promptCopied'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
  });

  it('creates a challenge only after an explicit action and reports the matching setup prompt', async () => {
    mocks.create.mockResolvedValue(waiting('setup-new'));
    mocks.get.mockResolvedValue({ project_id: 'project-a', status: 'not_started', confirmed_at: null });
    renderCheck();
    await screen.findByText(t('MCPOAuth.connection.notStarted'));
    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.createSetup') }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith('project-a'));
    expect(await screen.findByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmPrompt', { setup_id: 'setup-new', confirmation_token: 'A'.repeat(43) }))).toBeDefined();
  });

  it('shows the server receipt time and explicitly says Query was not tested', async () => {
    mocks.get.mockResolvedValue({
      project_id: 'project-a', status: 'confirmed', setup_id: 'setup-a', created_at: now,
      expires_at: '2026-11-03T12:30:00Z', confirmed_at: now,
    });
    renderCheck();
    expect(await screen.findByText(t('MCPOAuth.connection.confirmed'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmedAt'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmed'))).toBeDefined();
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it('clears a false status on read failure and allows a retry', async () => {
    mocks.get.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ project_id: 'project-a', status: 'not_started', confirmed_at: null });
    renderCheck();
    expect(await screen.findByText(t('MCPOAuth.connection.unavailable'))).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.retry') }));
    expect(await screen.findByText(t('MCPOAuth.connection.notStarted'))).toBeDefined();
  });

  it('recognizes another tab replacing the current setup', async () => {
    mocks.get.mockResolvedValueOnce(waiting('setup-a')).mockResolvedValueOnce(waiting('setup-b'));
    renderCheck();
    expect(await screen.findByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
    await act(async () => { fireEvent(window, new Event('focus')); });
    expect(await screen.findByText(t('MCPOAuth.connection.replaced'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmPrompt', { setup_id: 'setup-b', confirmation_token: 'A'.repeat(43) }))).toBeDefined();
  });

  it('stops polling while hidden and refreshes immediately when visible again', async () => {
    vi.useFakeTimers();
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    mocks.get.mockResolvedValue(waiting());
    renderCheck();
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(mocks.get).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(mocks.get).toHaveBeenCalledTimes(1);
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    await act(async () => { fireEvent(document, new Event('visibilitychange')); await Promise.resolve(); await Promise.resolve(); });
    expect(mocks.get).toHaveBeenCalledTimes(2);
  });
});
