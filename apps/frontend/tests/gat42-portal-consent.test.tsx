import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { portalConsentURL } from '@/components/PortalConsentRedirect';
import PortalConsentRedirect from '@/app/oauth/consent/page';
import RootLayout from '@/app/layout';
import WorkspaceLayout from '@/app/(workspace)/layout';

const lifecycle = vi.hoisted(() => ({ calls: [] as string[], loadRuntimeConfig: vi.fn() }));
vi.mock('@/lib/runtime-config', () => ({ loadRuntimeConfig: lifecycle.loadRuntimeConfig }));
vi.mock('next/font/local', () => ({ default: () => ({ variable: 'font' }) }));
vi.mock('@/components/RuntimeConfigBoundary', () => ({
  RuntimeConfigBoundary: ({ children }: { children: React.ReactNode }) => {
    lifecycle.calls.push('runtime-config');
    return <>{children}</>;
  },
}));
vi.mock('@/lib/auth', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => {
    lifecycle.calls.push('auth-hydration', 'session-storage-write');
    return <>{children}</>;
  },
}));
vi.mock('@/components/Shell', () => ({
  Shell: ({ children }: { children: React.ReactNode }) => {
    lifecycle.calls.push('workspace-list', 'project-bootstrap', 'login-modal');
    return <>{children}</>;
  },
}));

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const transactionId = `lwc_mcp_tx_${'A'.repeat(43)}`;

function layoutChain(route: string): string[] {
  const appRoot = resolve(frontendRoot, 'src/app');
  let directory = dirname(resolve(frontendRoot, route));
  const files: string[] = [];
  while (directory.startsWith(appRoot)) {
    const layout = join(directory, 'layout.tsx');
    if (existsSync(layout)) files.push(relative(frontendRoot, layout).replaceAll('\\', '/'));
    if (directory === appRoot) break;
    directory = dirname(directory);
  }
  return files.reverse();
}

async function renderPage(searchParams: Record<string, string | string[] | undefined>) {
  const page = await PortalConsentRedirect({ searchParams: Promise.resolve(searchParams) });
  return render(RootLayout({ children: page }));
}

beforeEach(() => {
  lifecycle.calls.length = 0;
  lifecycle.loadRuntimeConfig.mockReset();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('GAT-42 Auth-domain consent handoff', () => {
  it('builds the fixed Auth consent path with only the canonical transaction id', () => {
    const destination = new URL(portalConsentURL('https://auth.dev.example.test', transactionId));
    expect(destination.origin).toBe('https://auth.dev.example.test');
    expect(destination.pathname).toBe('/oauth/consent');
    expect([...destination.searchParams.entries()]).toEqual([['transaction_id', transactionId]]);
  });

  it('rejects invalid transaction ids and non-origin Auth config', () => {
    expect(() => portalConsentURL('https://auth.dev.example.test', `${transactionId}&next=/admin`)).toThrow();
    expect(() => portalConsentURL('https://auth.dev.example.test/nested', transactionId)).toThrow();
  });

  it('routes the actual consent URL through the root layout only', async () => {
    const route = 'src/app/oauth/consent/page.tsx';
    const oldRoute = 'src/app/(workspace)/oauth/consent/page.tsx';
    const source = readFileSync(resolve(frontendRoot, route), 'utf8');
    const root = readFileSync(resolve(frontendRoot, 'src/app/layout.tsx'), 'utf8');

    expect(existsSync(resolve(frontendRoot, route))).toBe(true);
    expect(existsSync(resolve(frontendRoot, oldRoute))).toBe(false);
    expect(layoutChain(route)).toEqual(['src/app/layout.tsx']);
    expect(root).not.toMatch(/RuntimeConfigBoundary|AuthProvider|WorkspaceProvider|<Shell/);
    expect(source).toContain('PortalConsentRedirect');
    expect(source).not.toMatch(/MCPOAuthConsentClient|useAuth|LoginModal/);

    lifecycle.loadRuntimeConfig.mockReturnValue(new Promise(() => {}));
    await renderPage({ transaction_id: transactionId });
    expect(await screen.findByText('正在開啟 Auth 授權頁…')).toBeTruthy();
    expect(lifecycle.calls).toEqual([]);
  });

  it.each([
    ['missing', {}],
    ['malformed', { transaction_id: 'invalid' }],
    ['repeated', { transaction_id: [transactionId, transactionId] }],
    ['extra parameter', { transaction_id: transactionId, next: '/admin' }],
  ])('stays on the same page with no LWC provider side effects for %s input', async (_name, searchParams) => {
    await renderPage(searchParams);
    expect(screen.getByRole('status').textContent).toContain('無法確認這筆授權');
    expect(lifecycle.loadRuntimeConfig).not.toHaveBeenCalled();
    expect(lifecycle.calls).toEqual([]);
  });

  it('stays on the same page when public Auth config cannot load', async () => {
    lifecycle.loadRuntimeConfig.mockRejectedValue(new Error('runtime config unavailable'));
    await renderPage({ transaction_id: transactionId });
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('目前無法開啟 Auth 授權頁');
    expect(lifecycle.calls).toEqual([]);
  });

  it('fails the old workspace-parent negative control before LWC handoff', async () => {
    const wrongRoute = 'src/app/(workspace)/oauth/consent/page.tsx';
    const workspaceLayout = readFileSync(resolve(frontendRoot, 'src/app/(workspace)/layout.tsx'), 'utf8');

    expect(layoutChain(wrongRoute)).toEqual(['src/app/layout.tsx', 'src/app/(workspace)/layout.tsx']);
    expect(workspaceLayout).toContain('AuthProvider');
    expect(workspaceLayout).toContain('<Shell>');

    lifecycle.loadRuntimeConfig.mockReturnValue(new Promise(() => {}));
    const page = await PortalConsentRedirect({ searchParams: Promise.resolve({ transaction_id: transactionId }) });
    render(WorkspaceLayout({ children: page }));
    expect(lifecycle.calls).toEqual([
      'runtime-config', 'auth-hydration', 'session-storage-write', 'workspace-list', 'project-bootstrap', 'login-modal',
    ]);
  });
});
