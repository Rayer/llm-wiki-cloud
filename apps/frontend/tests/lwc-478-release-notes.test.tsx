import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({ getReleaseNotesIndex: vi.fn(), getReleaseNote: vi.fn() }));
vi.mock('@/lib/api', () => mocks);

import { ReleaseNotesReader } from '@/components/ReleaseNotesReader';

const currentIndex = {
  schema_version: 1 as const, environment: 'dev' as const, runtime_product_version: '1.0.0',
  current_status: 'available' as const, current_product_version: '2026.11.4',
  current: { product_version: '2026.11.4', published_at: '2026-11-13T04:00:00Z', locales: ['en'] },
  history: [{ product_version: '2026.11.1', published_at: '2026-11-01T14:58:30Z', locales: ['en'] }],
  component_details_status: 'complete' as const,
  current_components: [{ component: 'bff' as const, product_version: '1.0.0', commit: '0123456789abcdef0123456789abcdef01234567',
    image_tag: 'bff-dev-01234567', image_digest: `sha256:${'a'.repeat(64)}`, revision: 'llm-wiki-cloud-dev-00001-abc' }],
  compatibility_aliases: [{ requested_version: '1.0.0', canonical_version: '2026.11.4', legacy_row_version: '1.0.0' }],
};

function note(version: string, isCurrent: boolean, requestedVersion = version, canonicalVersion = version, legacyAlias = false) {
  return {
    schema_version: 1 as const, product: 'llm-wiki-cloud' as const, product_version: version,
    published_at: version === '2026.11.4' ? '2026-11-13T04:00:00Z' : '2026-11-01T14:58:30Z',
    locale: 'en' as const, requested_locale: 'en' as const, locale_fallback: false, is_current: isCurrent,
    requested_product_version: requestedVersion, canonical_product_version: canonicalVersion,
    legacy_alias: legacyAlias, legacy_fallback: false,
    entries: [{ title: `Notes ${version}`, what_changed: 'Changed', user_benefit: 'Benefit', where_to_use: 'Workspace', limitations: [] }],
  };
}

describe('ReleaseNotesReader', () => {
  beforeEach(() => {
    localStorage.setItem('locale', 'en');
    mocks.getReleaseNotesIndex.mockResolvedValue(currentIndex);
    mocks.getReleaseNote.mockImplementation(async (version: string) => note(version, version === '2026.11.4'));
  });
  afterEach(() => { cleanup(); vi.clearAllMocks(); });

  it('opens on current notes, switches to exact history, and uses read-only public requests', async () => {
    render(<ReleaseNotesReader presentation="page" initialVersion="current" />);
    expect(await screen.findByRole('heading', { name: 'Notes 2026.11.4' })).toBeTruthy();
    expect(mocks.getReleaseNote).toHaveBeenCalledWith('2026.11.4', 'en', currentIndex.compatibility_aliases);
    expect(screen.getAllByText(/Current · 2026.11.4/).length).toBeGreaterThan(0);

    fireEvent.change(screen.getByRole('combobox', { name: 'Version' }), { target: { value: '2026.11.1' } });
    expect(await screen.findByRole('heading', { name: 'Notes 2026.11.1' })).toBeTruthy();
    expect(mocks.getReleaseNote).toHaveBeenLastCalledWith('2026.11.1', 'en', currentIndex.compatibility_aliases);
    expect(screen.getByText(/Historical release · 2026.11.1/)).toBeTruthy();
    expect(mocks).not.toHaveProperty('patchNudgeAttention');
  });

  it('keeps stale history unavailable as current and never auto-loads a historical row', async () => {
    mocks.getReleaseNotesIndex.mockResolvedValue({
      ...currentIndex, current_status: 'unavailable', current_product_version: undefined, current: undefined,
      component_details_status: 'unavailable', current_components: [],
    });
    render(<ReleaseNotesReader presentation="page" initialVersion="current" />);
    expect(await screen.findByRole('status')).toBeTruthy();
    expect(screen.getByText('Release notes are temporarily unavailable.')).toBeTruthy();
    expect(mocks.getReleaseNote).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole('combobox', { name: 'Version' }), { target: { value: '2026.11.1' } });
    expect(await screen.findByRole('heading', { name: 'Notes 2026.11.1' })).toBeTruthy();
    expect(screen.getByText(/Historical release · 2026.11.1/)).toBeTruthy();
  });

  it('keeps the old 1.0.0 URL bound to the exact canonical compatibility alias', async () => {
    mocks.getReleaseNote.mockResolvedValue(note('2026.11.4', true, '1.0.0', '2026.11.4', true));
    render(<ReleaseNotesReader presentation="page" initialVersion="1.0.0" />);
    expect(await screen.findByRole('heading', { name: 'Notes 2026.11.4' })).toBeTruthy();
    expect(mocks.getReleaseNote).toHaveBeenCalledWith('1.0.0', 'en', currentIndex.compatibility_aliases);
    expect(screen.getByText('This older version link opens the release notes for its compatible product release.')).toBeTruthy();
    expect(screen.getAllByText(/Current · 2026.11.4/).length).toBeGreaterThan(0);
  });

  it('dialog starts on Notes tab; direct view and copy do not create reading state', async () => {
    render(<ReleaseNotesReader presentation="dialog" initialVersion="2026.11.1" showBuildInformation buildInformation={<p>Build identity · BFF 1.0.0</p>} />);
    expect(await screen.findByRole('heading', { name: 'Notes 2026.11.1' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Notes' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.queryByRole('tabpanel', { name: 'Build info' })).toBeNull();
    await waitFor(() => expect(mocks.getReleaseNote).toHaveBeenCalledWith('2026.11.1', 'en', currentIndex.compatibility_aliases));
    fireEvent.click(screen.getByRole('tab', { name: 'Build info' }));
    expect(screen.getByText('Build identity · BFF 1.0.0')).toBeTruthy();
    expect(mocks).not.toHaveProperty('markRead');
  });
});
