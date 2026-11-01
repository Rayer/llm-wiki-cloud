import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { entryDate, selectEntries, selectSources } from '@/lib/list-controls';
import type { SourceListItem, WikiEntry } from '@/lib/api';
import { ListClient } from '@/components/ListClient';
import { SourceListClient } from '@/components/SourceListClient';

const workspace = vi.hoisted(() => ({ currentProject: { id: 'list-459-a' }, token: 'synthetic-session-a', sessionEpoch: 1, projects: [], projectsLoading: false }));
const api = vi.hoisted(() => ({ sources: vi.fn() }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/components/NavigationBlocker', () => ({ NavigationLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock('@/components/DocumentImportsPanel', () => ({ DocumentImportsPanel: () => null }));
vi.mock('next/navigation', () => ({ usePathname: () => '/sources', useSearchParams: () => new URLSearchParams() }));
vi.mock('@/lib/api', async (original) => ({ ...await original<typeof import('@/lib/api')>(), getSources: api.sources }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string, values?: Record<string, string | number>) => key === 'List.resultCount' ? `${values?.count}/${values?.total}` : key }) }));
const entries: WikiEntry[] = [
  { title: 'Alpha', slug: 'alpha', status: 'published', date: '2026-10-01', raw: {} },
  { title: 'Beta', slug: 'beta', status: 'draft', date: '2026-10-02', raw: {} },
  { title: 'Gamma', slug: 'gamma', status: 'published', raw: {} },
  { title: 'Delta', slug: 'delta', status: 'published', date: 'invalid', raw: {} },
];
const sources: SourceListItem[] = [
  { title: 'Alpha notes', slug: 'a', rawPath: 'raw/z.md', lifecycle: 'synced', annotationPresent: true, annotationAllowed: true, raw: {} },
  { title: 'Beta notes', slug: 'b', rawPath: 'raw/a.md', lifecycle: 'new', annotationPresent: false, annotationAllowed: true, raw: {} },
  { title: 'Gamma notes', slug: 'c', rawPath: 'raw/b.md', lifecycle: 'synced', annotationPresent: false, annotationAllowed: true, raw: {} },
];
afterEach(() => { cleanup(); vi.clearAllMocks(); });
beforeEach(() => { workspace.currentProject = { id: 'list-459-a' }; workspace.token = 'synthetic-session-a'; workspace.sessionEpoch += 1; api.sources.mockResolvedValue(sources); });

it('combines entry search, status, date and order without mutating input', () => {
  expect(selectEntries(entries, 'a', 'published', 'dated', 'date-desc').map((entry) => entry.title)).toEqual(['Alpha']);
  expect(selectEntries(entries, '', 'all', 'all', 'title-desc').map((entry) => entry.title)).toEqual(['Gamma', 'Delta', 'Beta', 'Alpha']);
  expect(entries.map((entry) => entry.title)).toEqual(['Alpha', 'Beta', 'Gamma', 'Delta']);
});
it('keeps absent and invalid dates last in both directions without inventing dates', () => {
  expect(entryDate(entries[2])).toBeNull();
  expect(entryDate(entries[3])).toBeNull();
  expect(selectEntries(entries, '', 'all', 'all', 'date-desc').slice(0, 2).map((entry) => entry.title)).toEqual(['Beta', 'Alpha']);
  expect(selectEntries(entries, '', 'all', 'all', 'date-asc').slice(0, 2).map((entry) => entry.title)).toEqual(['Alpha', 'Beta']);
});
it('combines real source fields and supports path ordering', () => {
  expect(selectSources(sources, 'notes', 'synced', 'present', 'title-desc').map((source) => source.title)).toEqual(['Alpha notes']);
  expect(selectSources(sources, '', 'all', 'all', 'path').map((source) => source.title)).toEqual(['Beta notes', 'Gamma notes', 'Alpha notes']);
});
it('operates the shared entry toolbar, updates counts and clears all conditions', async () => {
  render(<ListClient title="Entries" description="Knowledge" basePath="/concepts" entryType="concept" load={async () => entries} />);
  await screen.findByRole('heading', { name: 'Alpha' });
  fireEvent.change(screen.getByLabelText('List.filterStatus'), { target: { value: 'published' } });
  fireEvent.change(screen.getByLabelText('List.filterDate'), { target: { value: 'dated' } });
  fireEvent.change(screen.getByLabelText('List.orderBy'), { target: { value: 'date-desc' } });
  expect(screen.getByRole('status').textContent).toBe('1/4');
  expect(screen.queryByRole('heading', { name: 'Beta' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'List.clearControls' }));
  expect(screen.getByRole('status').textContent).toBe('4/4');
});
it('resets entry filters and cache across project and session changes', async () => {
  const load = vi.fn(async () => workspace.currentProject.id === 'list-459-b' ? [{ ...entries[0], title: 'Separate project' }] : entries);
  const props = { title: 'Entries', description: '', basePath: '/concepts', load };
  const view = render(<ListClient {...props} />);
  await screen.findByRole('heading', { name: 'Alpha' });
  fireEvent.change(screen.getByLabelText('List.searchPlaceholder'), { target: { value: 'Alpha' } });
  workspace.currentProject = { id: 'list-459-b' };
  view.rerender(<ListClient {...props} />);
  await screen.findByRole('heading', { name: 'Separate project' });
  expect((screen.getByLabelText('List.searchPlaceholder') as HTMLInputElement).value).toBe('');
  expect(screen.queryByRole('heading', { name: 'Alpha' })).toBeNull();
  workspace.token = 'synthetic-session-b';
  view.rerender(<ListClient {...props} />);
  await screen.findByRole('heading', { name: 'Separate project' });
  expect(load).toHaveBeenCalledTimes(3);
});
it('operates source filters, search, order, empty state and project reset', async () => {
  const view = render(<SourceListClient />);
  await screen.findByRole('heading', { name: 'Alpha notes' });
  fireEvent.change(screen.getByLabelText('List.filterStatus'), { target: { value: 'synced' } });
  fireEvent.change(screen.getByLabelText('List.filterAnnotation'), { target: { value: 'present' } });
  fireEvent.change(screen.getByLabelText('List.orderBy'), { target: { value: 'title-desc' } });
  expect(screen.getByRole('status').textContent).toBe('1/3');
  fireEvent.change(screen.getByLabelText('List.searchPlaceholder'), { target: { value: 'no match' } });
  expect(screen.getByText('List.noFilterMatches')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'List.clearControls' }));
  expect(screen.getByRole('status').textContent).toBe('3/3');
  api.sources.mockResolvedValue([{ ...sources[0], title: 'Separate source' }]);
  workspace.currentProject = { id: 'list-459-b' };
  view.rerender(<SourceListClient />);
  await screen.findByRole('heading', { name: 'Separate source' });
  expect(screen.queryByRole('heading', { name: 'Alpha notes' })).toBeNull();
});
