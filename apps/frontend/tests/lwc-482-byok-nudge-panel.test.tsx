import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import type { NudgeView } from '@/lib/nudges';

const mocks = vi.hoisted(() => ({
  getNudges: vi.fn(),
  reconcileNudges: vi.fn(),
  setNudgeAttention: vi.fn(),
  resolveNudgeTarget: vi.fn((target: { project_id: string }) => `/profile?project_id=${target.project_id}#project-ai-service`),
  auth: { user: { id: 'owner-a' }, sessionIdentityEpoch: 1, sessionEpoch: 1, isDemoSession: false },
}));

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => mocks.auth }));
vi.mock('@/lib/nudges', () => ({
  getNudges: mocks.getNudges,
  reconcileNudges: mocks.reconcileNudges,
  setNudgeAttention: mocks.setNudgeAttention,
  resolveNudgeTarget: mocks.resolveNudgeTarget,
}));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: () => true }),
  NavigationLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));

import { NudgePanel } from '@/components/NudgePanel';

const view: NudgeView = {
  schema_version: 1, partial: false, failures: [], notices: [], items: [{
    category: 'byok-actionable', attention_id: 'byok-attention', attention_state: 'unread', severity: 'attention',
    observations: [
      {
        source_key: 'byok:query-wiki', source_revision: 1, active: true,
        observation: {
          project_id: 'query-wiki', project_name: 'Query Wiki', title: 'Custom LLM Provider needs attention',
          body: 'The provider rejected authentication for the custom LLM Provider used by this Project.', severity: 'attention',
          actions: [{ id: 'review-ai-settings', kind: 'link', label: 'Open LLM Provider settings', target: { kind: 'project-ai-service', project_id: 'query-wiki' } }],
        },
      },
      {
        source_key: 'byok:pipeline-wiki', source_revision: 1, active: true,
        observation: {
          project_id: 'pipeline-wiki', project_name: 'Pipeline Wiki', title: 'Custom LLM Provider needs attention',
          body: 'The provider reported a billing restriction for this Project.', severity: 'attention',
          actions: [{ id: 'review-ai-settings', kind: 'link', label: 'Open LLM Provider settings', target: { kind: 'project-ai-service', project_id: 'pipeline-wiki' } }],
        },
      },
    ],
  }],
};

describe('LWC-482 BYOK Nudge Project isolation', () => {
  beforeEach(() => {
    mocks.getNudges.mockResolvedValue(view);
    mocks.reconcileNudges.mockResolvedValue(view);
    mocks.setNudgeAttention.mockReset();
    mocks.auth.user = { id: 'owner-a' };
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('shows Query and Pipeline reminders with each Wiki’s own settings action', async () => {
    render(<NudgePanel open onOpen={() => {}} onClose={() => {}} />);

    expect(await screen.findByText('Query Wiki')).toBeDefined();
    expect(await screen.findByText('Pipeline Wiki')).toBeDefined();
    const links = await screen.findAllByRole('link', { name: 'Open LLM Provider settings' });
    await waitFor(() => expect(links.map((link) => link.getAttribute('href')).sort()).toEqual([
      '/profile?project_id=pipeline-wiki#project-ai-service', '/profile?project_id=query-wiki#project-ai-service',
    ]));
  });
});
