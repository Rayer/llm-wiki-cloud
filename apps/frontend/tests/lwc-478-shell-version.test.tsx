import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  getBuildInfo: vi.fn(),
  getReleaseNotesIndex: vi.fn(),
  workspace: {} as Record<string, unknown>,
}));

vi.mock('next/image', () => ({ default: (props: React.ImgHTMLAttributes<HTMLImageElement> & { priority?: boolean }) => {
  const { priority, ...imageProps } = props;
  void priority;
  return React.createElement('img', imageProps);
} }));
vi.mock('next/navigation', () => ({ usePathname: () => '/' }));
vi.mock('@/lib/i18n', () => ({
  useT: () => ({
    t: (key: string) => ({
      'Shell.versionInfo': 'Versions and updates',
      'Shell.versionUnavailable': 'Version unavailable',
      'Shell.loadingVersion': 'Loading version',
      'Shell.environment': 'Environment',
      'Shell.productVersion': 'Product version',
      'Shell.bffBuildVersion': 'BFF build version',
      'Shell.retryVersionInfo': 'Retry',
    }[key] ?? key),
  }),
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: null }) }));
vi.mock('../src/components/WorkspaceProvider', () => ({
  WorkspaceProvider: ({ children }: { children: React.ReactNode }) => children,
  useWorkspace: () => mocks.workspace,
}));
vi.mock('../src/components/NavigationBlocker', () => ({
  NavigationBlockerProvider: ({ children }: { children: React.ReactNode }) => children,
  NavigationLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => React.createElement('a', { href, ...props }, children),
}));
vi.mock('../src/components/ui/CommandPalette', () => ({ CommandPalette: () => null, useCommandPalette: () => ({ open: false, setOpen: vi.fn() }) }));
vi.mock('../src/components/LoginModal', () => ({ LoginModal: () => null }));
vi.mock('../src/components/NewProjectModal', () => ({ NewProjectModal: () => null }));
vi.mock('../src/components/ProjectRenameModal', () => ({ ProjectRenameModal: () => null }));
vi.mock('../src/components/ProjectEmptyState', () => ({ ProjectEmptyState: () => null }));
vi.mock('../src/components/ui/Badge', () => ({ Badge: () => null }));
vi.mock('../src/components/ui/ProjectSelect', () => ({ ProjectSelect: () => null }));
vi.mock('../src/components/AccountSettingsModal', () => ({ AccountSettingsModal: () => null }));
vi.mock('../src/components/RequestIDPanel', () => ({ RequestIDPanel: () => null }));
vi.mock('../src/components/NudgePanel', () => ({ NudgePanel: () => null }));
vi.mock('../src/components/LocaleControl', () => ({ LocaleControl: () => null }));
vi.mock('@/lib/mcp-oauth', () => ({ dailyBriefSignedOutLandingURL: () => null }));
vi.mock('@/lib/api', () => mocks);
vi.mock('../src/components/ReleaseNotesReader', () => ({
  ReleaseNotesReader: (props: { buildInformation?: React.ReactNode }) => React.createElement('div', null, props.buildInformation),
}));

import { Shell } from '../src/components/Shell';

describe('Shell public release version', () => {
  beforeEach(() => {
    vi.stubGlobal('IntersectionObserver', class {
      observe() {}
      disconnect() {}
    });
    mocks.workspace = {
      hydrated: true, token: 'test-token', loginOpen: false, projects: [], currentProject: null,
      projectsLoading: false, projectsError: '', isDemoSession: false,
      navCounts: { sources: 0, concepts: 0, raw: 0, documentImportFailures: 0 },
      selectProject: vi.fn(), renameProject: vi.fn(), refreshProjects: vi.fn(),
      openNewProject: vi.fn(), signOut: vi.fn(),
    };
    mocks.getBuildInfo.mockResolvedValue({
      environment: 'dev', product_version: '1.0.0', commit: '0123456789abcdef0123456789abcdef01234567',
      branch: 'develop', tag: 'bff-dev-1.0.0', image_tag: 'bff-dev-01234567', service: 'llm-wiki-cloud-dev', revision: 'dev-00001',
    });
    mocks.getReleaseNotesIndex.mockResolvedValue({
      schema_version: 1, environment: 'dev', runtime_product_version: '1.0.0', current_status: 'available',
      current_product_version: '2026.11.4',
      current: { product_version: '2026.11.4', published_at: '2026-11-13T04:00:00Z', locales: ['en'] },
      history: [], component_details_status: 'complete', current_components: [{
        component: 'bff', product_version: '1.0.0', commit: '0123456789abcdef0123456789abcdef01234567',
        image_tag: 'bff-dev-01234567', image_digest: `sha256:${'a'.repeat(64)}`, revision: 'dev-00001',
      }], compatibility_aliases: [],
    });
  });

  afterEach(() => { cleanup(); vi.clearAllMocks(); vi.unstubAllGlobals(); });

  it('shows the public Notes release separately from the running BFF build', async () => {
    render(<Shell><p>Workspace content</p></Shell>);
    const versionButton = await screen.findByRole('button', { name: /Versions and updates: 2026\.11\.4/ });
    expect(versionButton.textContent).toContain('2026.11.4');
    expect(versionButton.textContent).not.toContain('1.0.0');

    fireEvent.click(versionButton);
    await waitFor(() => expect(screen.getByText('Product version').parentElement?.textContent).toContain('2026.11.4'));
    expect(screen.getByText('BFF build version').parentElement?.textContent).toContain('1.0.0');
    expect(screen.getByText('Environment').parentElement?.textContent).toContain('dev');
  });
});
