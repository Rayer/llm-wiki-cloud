import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({ getReleaseNotesIndex: vi.fn(), getReleaseNote: vi.fn() }));
vi.mock('@/lib/api', () => mocks);

import { ReleaseNotesReader } from '@/components/ReleaseNotesReader';

const currentIndex = {
  schema_version: 1 as const, environment: 'dev' as const, runtime_product_version: '2.0.0',
  current_status: 'available' as const, current_product_version: '2.0.0',
  current: { product_version: '2.0.0', published_at: '2026-11-13T04:00:00Z', locales: ['en'] },
  history: [{ product_version: '1.2.3', published_at: '2026-10-02T04:00:00Z', locales: ['en'] }],
};

function note(version: string, isCurrent: boolean) {
  return {
    schema_version: 1 as const, product: 'llm-wiki-cloud' as const, product_version: version,
    published_at: version === '2.0.0' ? '2026-11-13T04:00:00Z' : '2026-10-02T04:00:00Z',
    locale: 'en' as const, requested_locale: 'en' as const, locale_fallback: false, is_current: isCurrent,
    entries: [{ title: `Notes ${version}`, what_changed: 'Changed', user_benefit: 'Benefit', where_to_use: 'Workspace', limitations: [] }],
  };
}

describe('ReleaseNotesReader', () => {
  beforeEach(() => {
    localStorage.setItem('locale', 'en');
    mocks.getReleaseNotesIndex.mockResolvedValue(currentIndex);
    mocks.getReleaseNote.mockImplementation(async (version: string) => note(version, version === '2.0.0'));
  });
  afterEach(() => { cleanup(); vi.clearAllMocks(); });

  it('opens on current notes, switches to exact history, and uses read-only public requests', async () => {
    render(<ReleaseNotesReader presentation="page" initialVersion="current" />);
    expect(await screen.findByRole('heading', { name: 'Notes 2.0.0' })).toBeTruthy();
    expect(mocks.getReleaseNote).toHaveBeenCalledWith('2.0.0', 'en');
    expect(screen.getAllByText(/Current · 2.0.0/).length).toBeGreaterThan(0);

    fireEvent.change(screen.getByRole('combobox', { name: 'Version' }), { target: { value: '1.2.3' } });
    expect(await screen.findByRole('heading', { name: 'Notes 1.2.3' })).toBeTruthy();
    expect(mocks.getReleaseNote).toHaveBeenLastCalledWith('1.2.3', 'en');
    expect(screen.getByText(/Historical release · 1.2.3/)).toBeTruthy();
    expect(mocks).not.toHaveProperty('patchNudgeAttention');
  });

  it('keeps stale history unavailable as current and never auto-loads a historical row', async () => {
    mocks.getReleaseNotesIndex.mockResolvedValue({
      ...currentIndex, current_status: 'unavailable', current_product_version: undefined, current: undefined,
    });
    render(<ReleaseNotesReader presentation="page" initialVersion="current" />);
    expect(await screen.findByRole('status')).toBeTruthy();
    expect(screen.getByText('Release notes are temporarily unavailable.')).toBeTruthy();
    expect(mocks.getReleaseNote).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole('combobox', { name: 'Version' }), { target: { value: '1.2.3' } });
    expect(await screen.findByRole('heading', { name: 'Notes 1.2.3' })).toBeTruthy();
    expect(screen.getByText(/Historical release · 1.2.3/)).toBeTruthy();
  });

  it('dialog starts on Notes tab; direct view and copy do not create reading state', async () => {
    render(<ReleaseNotesReader presentation="dialog" initialVersion="1.2.3" showBuildInformation buildInformation={<p>Build identity</p>} />);
    expect(await screen.findByRole('heading', { name: 'Notes 1.2.3' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Notes' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.queryByRole('tabpanel', { name: 'Build info' })).toBeNull();
    await waitFor(() => expect(mocks.getReleaseNote).toHaveBeenCalledWith('1.2.3', 'en'));
    expect(mocks).not.toHaveProperty('markRead');
  });
});
