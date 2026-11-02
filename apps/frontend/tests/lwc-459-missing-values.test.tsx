import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { HomeClient } from '@/components/HomeClient';
import { RawClient } from '@/components/RawClient';
import { ExportPanel } from '@/components/ExportPanel';
import { StatusClient } from '@/components/StatusClient';
import { NavigationBlockerProvider } from '@/components/NavigationBlocker';
import { translate, catalogStatus } from '@/lib/i18n';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'missing-values', name: 'Missing values' } as { id: string; name: string } | null,
  getConcepts: vi.fn(), getStatus: vi.fn(), getRawFiles: vi.fn(),
  getBuildInfo: vi.fn(), getExportState: vi.fn(),
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({
  currentProject: mocks.currentProject, projects: [], token: 'synthetic-session',
  user: { id: 'synthetic-owner' }, hydrated: true, projectsLoading: false, sessionEpoch: 1,
}) }));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: null }) }));
vi.mock('@/lib/api', async (original) => ({
  ...await original<typeof import('@/lib/api')>(),
  getConcepts: mocks.getConcepts, getStatus: mocks.getStatus,
  getRawFiles: mocks.getRawFiles, getBuildInfo: mocks.getBuildInfo,
}));
vi.mock('@/lib/export-api', async (original) => ({
  ...await original<typeof import('@/lib/export-api')>(), getExportState: mocks.getExportState,
}));
vi.mock('@/components/RawUploadPanel', () => ({ RawUploadPanel: () => null }));
const searchParams = new URLSearchParams();
vi.mock('next/navigation', () => ({ usePathname: () => '/', useSearchParams: () => searchParams }));

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState(null, '', '/');
  mocks.currentProject = { id: 'missing-values', name: 'Missing values' };
  mocks.getConcepts.mockResolvedValue([]);
  mocks.getBuildInfo.mockResolvedValue({ environment: 'unknown', product_version: 'test', commit: 'test', branch: 'test', tag: '', image_tag: 'test', service: 'test', revision: 'test' });
});
afterEach(() => { cleanup(); vi.resetAllMocks(); });

it('resolves the shared missing-value key through the formal generated catalog and valid zh-TW seed', () => {
  expect(translate('en', 'Common.notProvided')).toBe('Not provided');
  expect(translate('zh-TW', 'Common.notProvided')).toBe('未提供');
  expect(catalogStatus.seed_status).toBe('valid');
  expect(catalogStatus.fallback_counts.zh_tw).toBe(0);
});

for (const [locale, missing] of [['en', 'Not provided'], ['zh-TW', '未提供']] as const) {
  it(`${locale}: home status failures use the localized missing value`, async () => {
    localStorage.setItem('locale', locale);
    mocks.getStatus.mockRejectedValue(new Error('synthetic status failure'));
    render(<NavigationBlockerProvider><HomeClient /></NavigationBlockerProvider>);
    expect(await screen.findAllByText(missing)).toHaveLength(3);
    expect(screen.queryByText(locale === 'en' ? '未提供' : 'Not provided')).toBeNull();
  });

  it(`${locale}: raw rows localize absent/invalid dates and absent checksums`, async () => {
    localStorage.setItem('locale', locale);
    mocks.getRawFiles.mockResolvedValue([
      { name: 'absent.md', size: 0, updated: '', sha256: '', ingested: false, raw: {} },
      { name: 'invalid.md', size: 1, updated: 'invalid', sha256: 'synthetic-checksum', ingested: true, raw: {} },
    ]);
    render(<RawClient />);
    const absent = (await screen.findByRole('button', { name: /absent.md/ })).closest('tr')!;
    expect(within(absent).getAllByText(missing)).toHaveLength(2);
    const invalid = screen.getByRole('button', { name: /invalid.md/ }).closest('tr')!;
    expect(within(invalid).getAllByText(missing)).toHaveLength(1);
    expect(within(invalid).getByText('synthetic-checksum')).toBeTruthy();
  });

  it.each([null, -1, Number.NaN])(`${locale}: export localizes absent or invalid metadata (size %s)`, async (size) => {
    localStorage.setItem('locale', locale);
    mocks.getExportState.mockResolvedValue({ latest_job: null, previous: null, eligible: true, rejection_reason: null, next_allowed_at: null,
      current: { export_id: 'synthetic-export', scope: 'raw', status: 'ready', snapshot_at: null, completed_at: null, expires_at: 'invalid', size_bytes: size, download_available: false },
    });
    render(<ExportPanel />);
    expect(await screen.findAllByText(missing)).toHaveLength(3);
    expect(screen.queryByText(locale === 'en' ? '未提供' : 'Not provided')).toBeNull();
  });

  it(`${locale}: missing developer IDs and tag stay collapsed and localize only on expansion`, async () => {
    localStorage.setItem('locale', locale);
    mocks.currentProject = null;
    render(<StatusClient />);
    await screen.findByRole('button', { name: 'Show developer details' });
    expect(screen.queryByText(translate(locale, 'Status.userId'))).toBeNull();
    expect(screen.queryByText(translate(locale, 'Status.projectId'))).toBeNull();
    expect(screen.queryByText(missing)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Show developer details' }));
    expect(await screen.findAllByText(missing)).toHaveLength(3);
    for (const key of ['Status.userId', 'Status.projectId', 'Status.gitTag']) {
      expect(screen.getByText(translate(locale, key)).nextElementSibling?.textContent).toBe(missing);
    }
    fireEvent.click(screen.getByRole('button', { name: 'Hide developer details' }));
    expect(screen.queryByText(missing)).toBeNull();
  });
}
