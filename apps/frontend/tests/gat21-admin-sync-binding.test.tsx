import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AdminClient } from '@/components/AdminClient';

const mocks = vi.hoisted(() => ({
  projects: vi.fn(),
  binding: vi.fn(),
  receipt: vi.fn(),
  revoke: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error { constructor(message: string, public status: number) { super(message); } },
  clearPublicConfigCache: vi.fn(),
  deleteAdminProject: vi.fn(), deleteAdminUser: vi.fn(),
  getAdminAnnouncement: async () => null,
  getAdminManagementCatalog: async () => [],
  getAdminPipelineStatus: vi.fn(),
  getAdminPipelineAutomation: async () => ({ allow_auto_pipeline: false, revision: 0, backlog_scan_status: 'idle' }),
  getAdminProjects: mocks.projects,
  getAdminProjectSyncBinding: mocks.binding,
  getAdminSyncBindingRevocation: mocks.receipt,
  getAdminSettings: async () => ({ registration_enabled: true }),
  getAdminUsers: async () => [],
  publishAnnouncement: vi.fn(), rebuildAdminProjectIndex: vi.fn(), renameAdminProject: vi.fn(),
  revokeAdminProjectSyncBinding: mocks.revoke,
  triggerAdminProjectPipeline: vi.fn(), updateAdminSettings: vi.fn(), updateAdminPipelineAutomation: vi.fn(),
  updateAdminUserManagementScope: vi.fn(), updateAdminUserStatus: vi.fn(),
}));
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ hydrated: true, user: { id: 'admin-a', email: 'admin@example.test', management_scopes: ['lwc:admin'] } }),
}));
vi.mock('@/lib/i18n', () => ({
  useLocale: () => ({ locale: 'en', t: (key: string) => key }),
  useT: () => ({ t: (key: string) => key }),
}));
vi.mock('@/components/NavigationBlocker', () => ({ useNavigationBlocker: () => ({ setBlocked: vi.fn() }) }));
vi.mock('@/components/AdminSharedProfilesPanel', () => ({ AdminSharedProfilesPanel: () => null }));
vi.mock('@/components/AnnouncementBoard', () => ({ AnnouncementBoard: () => null }));

const project = {
  id: 'owner-a_project-record-a', name: 'Project A', projectId: 'project-a', userId: 'owner-a',
  userName: 'Owner A', userEmail: 'owner@example.test', conceptCount: 1, sourceCount: 2,
};

function binding(status: 'active' | 'revoked', revision = '2026-11-03T00:00:00Z') {
  return {
    binding_id: 'binding-a', host: 'https://auth.example.test', wiki_id: 'wiki-a', project_id: 'project-a',
    environment: 'demo-gat21', authorized_by: 'owner-a', status,
    created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-03T00:00:00Z',
    revoked_at: status === 'revoked' ? '2026-11-03T00:01:00Z' : null,
    revoked_by: status === 'revoked' ? 'admin-a' : null,
    revocation_reason: status === 'revoked' ? 'security incident' : null,
    revision,
  };
}

function bindingDetails(value: ReturnType<typeof binding>) {
  return {
    schema_version: 1 as const, environment: 'firestore-demo-gat21', wire_environment: 'dev',
    owner_id: 'owner-a', project_id: 'project-a', project_record_id: project.id, binding: value,
  };
}

function receipt(operationId: string) {
  return {
    schema_version: 1 as const, operation_id: operationId, authority: 'lwc' as const,
    environment: 'firestore-demo-gat21', actor_id: 'admin-a', subject: 'owner-a',
    target_kind: 'sync_binding' as const, target_id: 'binding-a', client_id: null, project_id: 'project-a',
    reason: 'security incident', result: 'revoked' as const, committed_at: '2026-11-03T00:01:00Z',
    management_authorized_at: '2026-11-03T00:00:59Z', previous_cli_epoch: null, current_cli_epoch: null,
    affected_count: 1, count_complete: true,
  };
}

async function openBindingDialog() {
  const trigger = await screen.findByRole('button', { name: 'Admin.syncBindingOpen' });
  trigger.focus();
  fireEvent.click(trigger);
  return trigger;
}

afterEach(cleanup);

describe('GAT-21 Admin sync binding', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mocks.projects.mockResolvedValue([project]);
  });

  it('queries the selected Project, confirms its exact binding, and waits for receipt plus target readback', async () => {
    const operationId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
    vi.spyOn(window.crypto, 'randomUUID').mockReturnValue(operationId);
    mocks.binding.mockResolvedValueOnce(bindingDetails(binding('active')))
      .mockResolvedValueOnce(bindingDetails(binding('active')))
      .mockResolvedValueOnce(bindingDetails(binding('revoked')));
    mocks.receipt.mockResolvedValue(receipt(operationId));
    let resolveRevoke!: (value: unknown) => void;
    mocks.revoke.mockReturnValue(new Promise((resolve) => { resolveRevoke = resolve; }));

    render(<AdminClient />);
    const trigger = await openBindingDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Admin.syncBindingTitle' });
    await screen.findByText('binding-a');
    expect(document.activeElement).toBe(dialog);
    expect(screen.getByText('wiki-a')).toBeDefined();
    expect(screen.getByText('firestore-demo-gat21')).toBeDefined();

    const closeButtons = screen.getAllByRole('button', { name: 'Common.close' });
    closeButtons[closeButtons.length - 1].focus();
    fireEvent.keyDown(dialog, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Admin.syncBindingRevoke' }));
    fireEvent.click(screen.getByRole('button', { name: 'Admin.syncBindingRevoke' }));
    const reason = await screen.findByRole('textbox');
    fireEvent.change(reason, { target: { value: 'security incident' } });

    fireEvent.click(screen.getByRole('button', { name: 'Admin.syncBindingRevoke' }));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledTimes(1));
    expect(mocks.revoke).toHaveBeenCalledWith(project.id, 'binding-a', {
      schema_version: 1,
      operation_id: operationId,
      environment: 'firestore-demo-gat21',
      subject: 'owner-a',
      reason: 'security incident',
      expected_revision: '2026-11-03T00:00:00Z',
    });
    expect(screen.queryByText('Admin.syncBindingComplete')).toBeNull();

    await act(async () => { resolveRevoke(receipt(operationId)); });
    expect(await screen.findByText('Admin.syncBindingComplete')).toBeDefined();
    expect(mocks.receipt).toHaveBeenCalledWith(operationId);
    expect(mocks.binding).toHaveBeenCalledTimes(3);

    fireEvent.keyDown(dialog, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });

  it('retries an unknown outcome with the same operation ID and exact request body', async () => {
    const operationId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
    vi.spyOn(window.crypto, 'randomUUID').mockReturnValue(operationId);
    mocks.binding.mockResolvedValueOnce(bindingDetails(binding('active')))
      .mockResolvedValueOnce(bindingDetails(binding('active')))
      .mockResolvedValueOnce(bindingDetails(binding('revoked')));
    mocks.receipt.mockRejectedValueOnce(new Error('readback timeout')).mockResolvedValueOnce(receipt(operationId));
    mocks.revoke.mockRejectedValueOnce(new Error('request timeout')).mockResolvedValueOnce(receipt(operationId));

    render(<AdminClient />);
    await openBindingDialog();
    await screen.findByText('binding-a');
    fireEvent.click(screen.getByRole('button', { name: 'Admin.syncBindingRevoke' }));
    fireEvent.change(await screen.findByRole('textbox'), { target: { value: 'security incident' } });
    fireEvent.click(screen.getByRole('button', { name: 'Admin.syncBindingRevoke' }));
    await screen.findByRole('button', { name: 'Admin.syncBindingRetrySame' });
    const firstBody = mocks.revoke.mock.calls[0][2];
    expect(firstBody.operation_id).toBe(operationId);

    fireEvent.click(screen.getByRole('button', { name: 'Admin.syncBindingRetrySame' }));
    await screen.findByText('Admin.syncBindingComplete');
    expect(mocks.revoke).toHaveBeenCalledTimes(2);
    expect(mocks.revoke.mock.calls[1][2]).toEqual(firstBody);
  });
});
