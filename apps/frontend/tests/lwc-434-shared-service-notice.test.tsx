import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';

const mocks = vi.hoisted(() => ({ getProjectLLMBinding: vi.fn() }));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getProjectLLMBinding: mocks.getProjectLLMBinding };
});

import { SharedServiceOnboardingNotice } from '@/components/SharedServiceNotice';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('locale', 'zh-TW');
  mocks.getProjectLLMBinding.mockReset();
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-434 shared service onboarding notice', () => {
  it('waits for the current Project binding and opens personal BYOK settings from the primary CTA', async () => {
    const binding = deferred<{ source: 'shared' | 'personal' }>();
    mocks.getProjectLLMBinding.mockReturnValue(binding.promise);
    const openSettings = vi.fn();
    window.addEventListener('lwc-open-account-settings', openSettings);
    render(<SharedServiceOnboardingNotice projectId="project-a" ownerId="owner-a" sessionEpoch={1} isDemoSession={false} />);

    expect(screen.queryByRole('heading', { name: '目前使用的是平台共用服務。' })).toBeNull();
    binding.resolve({ source: 'shared' });
    fireEvent.click(await screen.findByRole('button', { name: '前往個人設定' }));

    expect(openSettings).toHaveBeenCalledOnce();
    expect((openSettings.mock.calls[0][0] as CustomEvent<{ focusBYOK: boolean }>).detail.focusBYOK).toBe(true);
    expect(mocks.getProjectLLMBinding).toHaveBeenCalledWith('project-a');
    window.removeEventListener('lwc-open-account-settings', openSettings);
  });

  it.each([
    ['personal binding', { source: 'personal' as const }, false],
    ['unavailable binding', null, false],
    ['Demo session', { source: 'shared' as const }, true],
  ])('does not show for a %s', async (_name, binding, isDemoSession) => {
    mocks.getProjectLLMBinding.mockResolvedValue(binding);
    render(<SharedServiceOnboardingNotice projectId="project-a" ownerId="owner-a" sessionEpoch={1} isDemoSession={isDemoSession} />);

    await Promise.resolve();
    await Promise.resolve();
    expect(screen.queryByRole('heading', { name: '目前使用的是平台共用服務。' })).toBeNull();
  });

  it('dismisses the notice and remembers that choice for the owner', async () => {
    mocks.getProjectLLMBinding.mockResolvedValue({ source: 'shared' });
    const view = render(<SharedServiceOnboardingNotice projectId="project-a" ownerId="owner-a" sessionEpoch={1} isDemoSession={false} />);
    await screen.findByRole('heading', { name: '目前使用的是平台共用服務。' });
    fireEvent.click(screen.getByRole('button', { name: '關閉共用服務提示' }));
    expect(screen.queryByRole('heading', { name: '目前使用的是平台共用服務。' })).toBeNull();
    expect(localStorage.getItem('lwc:byok-shared-notice:v1:owner-a')).toBe('1');

    view.rerender(<SharedServiceOnboardingNotice projectId="project-b" ownerId="owner-a" sessionEpoch={2} isDemoSession={false} />);
    await Promise.resolve();
    expect(screen.queryByRole('heading', { name: '目前使用的是平台共用服務。' })).toBeNull();
  });
});
