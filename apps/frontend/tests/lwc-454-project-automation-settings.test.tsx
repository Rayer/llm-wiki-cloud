import React from 'react';
import { afterEach, expect, beforeEach, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  isDemoSession: false,
  translate: (key: string) => key,
  getProjectPipelineAutomation: vi.fn(),
  updateProjectPipelineAutomation: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getProjectPipelineAutomation: mocks.getProjectPipelineAutomation,
  updateProjectPipelineAutomation: mocks.updateProjectPipelineAutomation,
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ isDemoSession: mocks.isDemoSession }) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: mocks.translate }) }));

import { ProjectPipelineAutomationSettings } from '@/components/ProjectPipelineAutomationSettings';

beforeEach(() => {
  mocks.isDemoSession = false;
  mocks.getProjectPipelineAutomation.mockReset().mockResolvedValue({
    enabled: false, revision: 3, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
    source_revision: 1, processed_revision: 0,
  });
  mocks.updateProjectPipelineAutomation.mockReset().mockResolvedValue({
    enabled: true, revision: 4, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
    source_revision: 1, processed_revision: 0,
  });
});

afterEach(() => cleanup());

it('keeps the Project switch editable while clearly reserving the global switch for administrators', async () => {
  render(<ProjectPipelineAutomationSettings projectId="synthetic-project" />);

  const toggle = await screen.findByTestId('pipeline-automation-project-toggle') as HTMLInputElement;
  expect(toggle.disabled).toBe(false);
  expect(screen.getByTestId('pipeline-automation-blocked').textContent).toContain('Pipeline.automationAdminDisabled');
  expect(screen.getByRole('status').textContent).toContain('Pipeline.automationState.disabled');

  fireEvent.click(toggle);
  await waitFor(() => expect(mocks.updateProjectPipelineAutomation).toHaveBeenCalledWith('synthetic-project', true, 3));
  await waitFor(() => expect(toggle.checked).toBe(true));
  expect(screen.getAllByRole('checkbox')).toHaveLength(1);
});

it('reports Admin and Project blockers from their authoritative settings fields', async () => {
  mocks.getProjectPipelineAutomation.mockReset()
    .mockResolvedValueOnce({
      enabled: true, revision: 5, admin_allowed: false, effective_enabled: false,
      blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
      source_revision: 2, processed_revision: 1,
    })
    .mockResolvedValueOnce({
      enabled: false, revision: 6, admin_allowed: true, effective_enabled: false,
      blocked_reason: 'project_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled',
      source_revision: 2, processed_revision: 1,
    });

  const adminBlocked = render(<ProjectPipelineAutomationSettings projectId="admin-blocked-project" />);
  expect(await screen.findByTestId('pipeline-automation-blocked')).toBeTruthy();
  expect(screen.queryByText('Pipeline.automationProjectDisabled')).toBeNull();
  adminBlocked.unmount();

  render(<ProjectPipelineAutomationSettings projectId="project-paused" />);
  expect(await screen.findByText('Pipeline.automationProjectDisabled')).toBeTruthy();
  expect(screen.queryByTestId('pipeline-automation-blocked')).toBeNull();
});
