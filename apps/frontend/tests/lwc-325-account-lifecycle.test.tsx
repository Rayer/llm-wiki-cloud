import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AdminClient } from '@/components/AdminClient';

const mocks = vi.hoisted(() => ({ users: vi.fn(), update: vi.fn() }));
vi.mock('@/lib/api', () => ({
  ApiError: class extends Error {},
  clearPublicConfigCache: vi.fn(), deleteAdminProject: vi.fn(), deleteAdminUser: vi.fn(),
  getAdminProjects: async () => [], getAdminUsers: mocks.users,
  getAdminManagementCatalog: async () => [
    { id: 'auth:admin', product: 'auth', resource: 'bff', grant_mode: 'operator-only', display_name: { en: 'Platform administrator', 'zh-TW': '平台管理員' }, description: { en: 'Manage accounts.', 'zh-TW': '管理帳號。' } },
    { id: 'lwc:admin', product: 'lwc', resource: 'bff', grant_mode: 'operator-only', display_name: { en: 'LWC administrator', 'zh-TW': 'LWC 管理員' }, description: { en: 'Manage projects.', 'zh-TW': '管理專案。' } },
  ],
  getAdminSettings: async () => ({ registration_enabled: true }), getAdminPipelineStatus: vi.fn(),
  publishAnnouncement: vi.fn(), rebuildAdminProjectIndex: vi.fn(), renameAdminProject: vi.fn(),
  triggerAdminProjectPipeline: vi.fn(), updateAdminSettings: vi.fn(), updateAdminUserRole: vi.fn(),
  updateAdminUserStatus: mocks.update,
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ hydrated: true, user: { id: 'admin', role: 'admin', management_scopes: ['auth:admin', 'lwc:admin'] } }) }));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ locale: 'en', t: (key: string) => key }) }));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ setBlocked: vi.fn() }) }));

afterEach(cleanup);

describe('account lifecycle administration', () => {
  beforeEach(() => vi.resetAllMocks());
  it('disables self suspension, confirms suspension, and restores after an authoritative refresh', async () => {
    const admin = { id: 'admin', name: 'Admin', email: 'admin@example.test', role: 'admin', status: 'active', scopeStates: {}, projectCount: 0 };
    const owner = { id: 'owner', name: 'Owner', email: 'owner@example.test', role: 'user', status: 'active', scopeStates: {}, projectCount: 2 };
    mocks.users.mockResolvedValueOnce([admin, owner]).mockResolvedValueOnce([admin, { ...owner, status: 'suspended' }]).mockResolvedValue([admin, owner]);
    mocks.update.mockResolvedValue(undefined);
    render(<AdminClient />);
    fireEvent.click(screen.getByRole('button', { name: /Users/ }));
    const row = (await screen.findByText('Owner')).closest('tr')!;
    const self = screen.getByText('admin@example.test').closest('tr');
    expect(self).not.toBeNull();
    expect((within(self!).getByRole('button', { name: 'Suspend user' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(within(row).getByRole('button', { name: 'Suspend user' }));
    expect(screen.getByText(/Data is preserved/)).toBeDefined();
    expect(mocks.update).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Suspend' }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith('owner', 'suspended'));
    fireEvent.click(await screen.findByRole('button', { name: 'Restore user' }));
    expect(screen.getByText(/old sessions stay invalid/)).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith('owner', 'active'));
    expect(await screen.findByText('User restored. They must sign in again.')).toBeDefined();
  });
});

// A committed mutation must not leave stale account state actionable when readback fails.
it.each(['active', 'suspended'] as const)('requires a successful readback after changing %s status', async (status) => {
  const owner = { id: 'owner', name: 'Owner', email: 'owner@example.test', role: 'user', status, scopeStates: {}, projectCount: 2 };
  const nextStatus = status === 'active' ? 'suspended' : 'active';
  mocks.users.mockReset().mockResolvedValueOnce([owner]).mockRejectedValueOnce(new Error('Readback unavailable')).mockResolvedValue([{ ...owner, status: nextStatus }]);
  mocks.update.mockReset().mockResolvedValue(undefined);
  render(<AdminClient />);
  fireEvent.click(screen.getByRole('button', { name: /Users/ }));
  const row = (await screen.findByText('Owner')).closest('tr')!;
  fireEvent.click(within(row).getByRole('button', { name: status === 'active' ? 'Suspend user' : 'Restore user' }));
  fireEvent.click(screen.getByRole('button', { name: status === 'active' ? 'Suspend' : 'Restore' }));
  await screen.findByText(/Update succeeded, but current user state is unknown/);
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(within(row).queryByText(status, { exact: true })).toBeNull();
  expect(within(row).getAllByText('Unknown')).toHaveLength(2);
  for (const button of within(row).getAllByRole('button')) expect((button as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryByText('User suspended.', { exact: true })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  await within(row).findByText(nextStatus, { exact: true });
  expect((within(row).getByRole('button', { name: nextStatus === 'active' ? 'Suspend user' : 'Restore user' }) as HTMLButtonElement).disabled).toBe(false);
  expect(mocks.update).toHaveBeenCalledTimes(1);
});
