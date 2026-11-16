import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
const mocks = vi.hoisted(() => ({
  reconcile: vi.fn(),
  t: (key: string) => key,
  user: { id: 'review-user' },
}));
vi.mock('@/lib/nudges', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/nudges')>(),
  reconcileNudges: mocks.reconcile,
}));
vi.mock('next/navigation', () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: mocks.t }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: mocks.user, sessionIdentityEpoch: 1, sessionEpoch: 1, isDemoSession: false }) }));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ confirmNavigation: () => true }) }));
import { NudgePanel } from '@/components/NudgePanel';
afterEach(cleanup);
beforeEach(() => mocks.reconcile.mockReset());

it('obsolete mount rejection cannot overwrite successful manual reconcile', async () => {
  let rejectOld!: (reason: Error) => void;
  const old = new Promise((_, reject) => { rejectOld = reject; });
  mocks.reconcile.mockReturnValueOnce(old).mockResolvedValueOnce({ schema_version: 1, partial: false, failures: [], notices: [], items: [] });
  render(<NudgePanel open onOpen={() => {}} onClose={() => {}} />);
  await waitFor(() => expect(mocks.reconcile).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole('button', { name: 'Nudge.refresh' }));
  await screen.findByText('Nudge.empty');
  expect(screen.queryByRole('alert')).toBeNull();
  await act(async () => { rejectOld(new Error('obsolete source failure')); await old.catch(() => {}); });
  expect(screen.queryByRole('alert')).toBeNull();
});

it('current mount reconciliation failure remains visible', async () => {
  mocks.reconcile.mockRejectedValueOnce(new Error('current source failure'));
  render(<NudgePanel open onOpen={() => {}} onClose={() => {}} />);
  expect((await screen.findByRole('alert')).textContent).toBe('Nudge.loadFailed');
});
