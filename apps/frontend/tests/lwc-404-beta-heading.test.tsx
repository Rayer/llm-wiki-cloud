import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listGrants: vi.fn() }));

vi.mock('next/link', () => ({
  default: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }) =>
    React.createElement('a', { href, ...props }, children),
}));
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ accessToken: 'local-token', sessionEpoch: 1, user: { id: 'user-1', email: 'owner@example.test' }, isDemoSession: false }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ hydrated: true, currentProject: { id: 'project-1', name: 'Synthetic Project' }, projectsLoading: false }),
}));
vi.mock('@/lib/mcp-oauth', () => ({
  listMCPOAuthGrants: mocks.listGrants,
  mcpResourceURL: () => 'https://api.example.test/mcp/projects/project-1',
  MCPOAuthApiError: class MCPOAuthApiError extends Error {},
  revokeMCPOAuthGrant: vi.fn(),
}));
vi.mock('@/components/AgentMcpTabs', () => ({ AgentMcpTabs: () => <div>Agent setup tabs</div> }));
vi.mock('@/components/ProjectKeysSection', () => ({ ProjectKeysSection: () => <div>Project keys</div> }));

import AdvancedPage from '@/app/(workspace)/advanced/page';

const { cleanup, render, screen } = await import('@testing-library/react');

beforeEach(() => {
  mocks.listGrants.mockReset().mockResolvedValue([]);
});

afterEach(() => {
  cleanup();
  localStorage.removeItem('locale');
});

describe('LWC-404 Agent & MCP Beta heading', () => {
  it.each([
    ['en', 'Agent & MCP Beta'],
    ['zh-TW', 'Agent & MCP Beta'],
  ] as const)('renders the localized Agent & MCP heading badge in %s', (locale, heading) => {
    localStorage.setItem('locale', locale);
    render(<AdvancedPage />);

    expect(screen.getByRole('heading', { level: 1, name: heading })).toBeTruthy();
    expect(screen.getByText(locale === 'en'
      ? "Connect AI tools to read this Project's knowledge and, with explicit consent, add new raw sources. Access can be revoked at any time."
      : '連結 AI 工具以讀取此專案知識；經明確授權後也可新增 raw 來源。存取權可隨時撤銷。')).toBeTruthy();
  });
});
