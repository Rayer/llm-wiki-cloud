import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', { configurable: true, value: (callback: () => unknown) => Promise.resolve(callback()) });
}

const { cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');
const mocks = vi.hoisted(() => ({
  decideCLIPairing: vi.fn(),
  accessToken: 'web-access-token' as string | null,
  user: { id: 'user-1', email: 'owner@example.test' } as { id: string; email: string } | null,
  hydrated: true,
  demo: false,
  refreshAccessToken: vi.fn(),
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: mocks.accessToken,
    user: mocks.user,
    hydrated: mocks.hydrated,
    isDemoSession: mocks.demo,
    refreshAccessToken: mocks.refreshAccessToken,
  }),
}));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }) }));
vi.mock('@/lib/cli-auth', () => ({ decideCLIPairing: mocks.decideCLIPairing }));

import { CLIPairingClient } from '@/components/CLIPairingClient';

beforeEach(() => {
  mocks.decideCLIPairing.mockReset().mockResolvedValue(undefined);
  mocks.refreshAccessToken.mockReset().mockResolvedValue('fresh-web-access-token');
  mocks.accessToken = 'web-access-token';
  mocks.user = { id: 'user-1', email: 'owner@example.test' };
  mocks.hydrated = true;
  mocks.demo = false;
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-346 CLI pairing approval', () => {
  it('does not approve on page load and requires an explicit Web action', async () => {
    render(<CLIPairingClient initialUserCode="234567AB" />);
    expect(mocks.decideCLIPairing).not.toHaveBeenCalled();
    expect(screen.getByLabelText('CLIPairing.codeLabel').getAttribute('value')).toBe('234567AB');

    fireEvent.click(screen.getByRole('button', { name: 'CLIPairing.approve' }));
    await waitFor(() => expect(mocks.decideCLIPairing).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      '234567AB',
      'approve',
    ));
    expect(screen.getByRole('status').textContent).toBe('CLIPairing.approved');
  });

  it('keeps an invalid code from reaching the server', () => {
    render(<CLIPairingClient initialUserCode="bad-code" />);
    expect((screen.getByRole('button', { name: 'CLIPairing.approve' }) as HTMLButtonElement).disabled).toBe(true);
    expect(mocks.decideCLIPairing).not.toHaveBeenCalled();
  });

  it('does not let a Demo session approve a CLI login', () => {
    mocks.demo = true;
    render(<CLIPairingClient initialUserCode="234567AB" />);
    expect(screen.getByText('CLIPairing.demoDisabled')).toBeDefined();
    expect(screen.queryByRole('button', { name: 'CLIPairing.approve' })).toBeNull();
    expect(mocks.decideCLIPairing).not.toHaveBeenCalled();
  });
});
