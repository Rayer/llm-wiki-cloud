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

function renderCheck(projectId = 'project-a', identityKey = `user-a:${projectId}`, locale: 'en' | 'zh-TW' = 'en') {
  return render(<AgentConnectionCheck projectId={projectId} identityKey={identityKey} locale={locale} t={(key, params) => translate(locale, key, params)} />);
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
    expect(screen.queryByRole('button', { name: /Query/i })).toBeNull();
    expect(screen.getByRole('region', { name: 'Confirm this Agent connection' }).textContent).not.toMatch(/Query/i);

    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.copyPrompt') }));
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
      t('MCPOAuth.connection.confirmPrompt', { setup_id: 'setup-a', confirmation_token: 'A'.repeat(43) }),
    ));
    expect(await screen.findByText(t('MCPOAuth.connection.promptCopied'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
  });

  it('creates, copies, and reads back confirmation for the same setup', async () => {
    mocks.create.mockResolvedValue(waiting('setup-new'));
    mocks.get
      .mockResolvedValueOnce({ project_id: 'project-a', status: 'not_started', confirmed_at: null })
      .mockResolvedValueOnce({
        project_id: 'project-a', status: 'confirmed', setup_id: 'setup-new', created_at: now,
        expires_at: '2026-11-03T12:30:00Z', confirmed_at: now,
      });
    renderCheck();
    await screen.findByText(t('MCPOAuth.connection.notStarted'));
    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.createSetup') }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith('project-a'));
    expect(await screen.findByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmPrompt', { setup_id: 'setup-new', confirmation_token: 'A'.repeat(43) }))).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.copyPrompt') }));
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith(expect.stringContaining('setup_id=setup-new')));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(expect.stringContaining(`confirmation_token=${'A'.repeat(43)}`));

    await act(async () => { fireEvent(window, new Event('focus')); });
    expect(await screen.findByText('Agent connection confirmation received.')).toBeDefined();
    const connection = screen.getByRole('region', { name: 'Confirm this Agent connection' });
    expect(connection.querySelector('time')?.getAttribute('datetime')).toBe(now);
    expect(connection.textContent).not.toMatch(/Query/i);
    expect(screen.queryByRole('button', { name: /Query/i })).toBeNull();
    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect(mocks.create).toHaveBeenCalledTimes(1);
  });

  it.each([
    { locale: 'en' as const, title: 'Confirm this Agent connection', waitingLabel: 'Waiting for the selected Agent client to call confirm_connection.', copyLabel: 'Copy confirmation prompt' },
    { locale: 'zh-TW' as const, title: '確認 Agent 連線', waitingLabel: '正在等待目前選取的 Agent 用戶端呼叫 confirm_connection。', copyLabel: '複製確認提示' },
  ])('copies only the connection prompt without Query steps in $locale', async ({ locale, title, waitingLabel, copyLabel }) => {
    mocks.get.mockResolvedValue(waiting());
    renderCheck('project-a', 'user-a:project-a', locale);
    expect(await screen.findByText(waitingLabel)).toBeDefined();
    const connection = screen.getByRole('region', { name: title });
    expect(connection.textContent).not.toMatch(/Query/i);

    fireEvent.click(screen.getByRole('button', { name: copyLabel }));
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledTimes(1));
    const copiedPrompt = vi.mocked(navigator.clipboard.writeText).mock.calls[0][0];
    expect(copiedPrompt).toContain('setup_id=setup-a');
    expect(copiedPrompt).toContain(`confirmation_token=${'A'.repeat(43)}`);
    expect(copiedPrompt).not.toMatch(/Query/i);
    expect(screen.queryByRole('button', { name: /Query/i })).toBeNull();
  });

  it('does not let a focus read overtake an in-flight create receipt', async () => {
    let resolveCreate!: (value: ReturnType<typeof waiting>) => void;
    mocks.create.mockReturnValue(new Promise((resolve) => { resolveCreate = resolve; }));
    mocks.get.mockResolvedValue({ project_id: 'project-a', status: 'not_started', confirmed_at: null });
    renderCheck();
    await screen.findByText(t('MCPOAuth.connection.notStarted'));

    fireEvent.click(screen.getByRole('button', { name: t('MCPOAuth.connection.createSetup') }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith('project-a'));
    await act(async () => { fireEvent(window, new Event('focus')); });
    expect(mocks.get).toHaveBeenCalledTimes(1);

    await act(async () => { resolveCreate(waiting('setup-created')); });
    expect(await screen.findByText(t('MCPOAuth.connection.waiting'))).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.connection.confirmPrompt', {
      setup_id: 'setup-created', confirmation_token: 'A'.repeat(43),
    }))).toBeDefined();
  });

  it.each([
    { locale: 'en' as const, confirmedLabel: 'Agent connection confirmation received.' },
    { locale: 'zh-TW' as const, confirmedLabel: '已收到 Agent 連線確認。' },
  ])('renders a localized confirmation and exact receipt time in $locale', async ({ locale, confirmedLabel }) => {
    mocks.get.mockResolvedValue({
      project_id: 'project-a', status: 'confirmed', setup_id: 'setup-a', created_at: now,
      expires_at: '2026-11-03T12:30:00Z', confirmed_at: now,
    });
    renderCheck('project-a', 'user-a:project-a', locale);
    expect(await screen.findByText(confirmedLabel)).toBeDefined();
    const connection = screen.getByRole('region', { name: locale === 'en' ? 'Confirm this Agent connection' : '確認 Agent 連線' });
    expect(connection.querySelector('time')?.getAttribute('datetime')).toBe(now);
    expect(connection.textContent).not.toMatch(/Query|尚未測試/i);
    expect(screen.queryByRole('button', { name: /Query|Query 範例/i })).toBeNull();
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
