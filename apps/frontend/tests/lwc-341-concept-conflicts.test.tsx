import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  getConceptConflicts: vi.fn(),
  compareConceptConflict: vi.fn(),
  getConceptConflictOperation: vi.fn(),
  resolveConceptConflict: vi.fn(),
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  hydrated: true,
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getConceptConflicts: mocks.getConceptConflicts,
  compareConceptConflict: mocks.compareConceptConflict,
  getConceptConflictOperation: mocks.getConceptConflictOperation,
  resolveConceptConflict: mocks.resolveConceptConflict,
}));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: mocks.currentProject, hydrated: mocks.hydrated }),
}));

import ConceptConflictsPage from '@/app/(workspace)/concept-conflicts/page';

const conflict = (name: string) => ({
  conflict_id: `conflict-${name}`,
  published: { path: `wiki/${name}.md`, sha256: `published-${name}`, content: `published ${name}`, identity: { exists: true, status: 'published' } },
  candidate: { path: `wiki/.drafts/${name}.md`, sha256: `candidate-${name}`, content: `candidate ${name}`, identity: { exists: true, status: 'draft' } },
  index_sha256: 'index', confidence: 0.9, min_confidence: 0, is_new: true, eligible: true,
});

const inventory = (projectID: string, names: string[]) => ({
  project_id: projectID, storage_view: `observed-${projectID}`, pair_sha256: `pairs-${projectID}`,
  new_count: names.length, pending_count: names.length, held_back_count: 0,
  conflicts: names.map(conflict), held_back: [], count: names.length,
});

const comparison = (name: string, projectID = 'project-a') => ({
  project_id: projectID, storage_view: `observed-${name}`, pair_sha256: `pair-${name}`,
  conflict_id: `conflict-${name}`, index_sha256: 'index', confidence: 0.9, min_confidence: 0,
  published: { path: `wiki/${name}.md`, sha256: `published-${name}`, content: `published ${name}`, identity: { exists: true, status: 'published' } },
  candidate: { path: `wiki/.drafts/${name}.md`, sha256: `candidate-${name}`, content: `candidate ${name}`, identity: { exists: true, status: 'draft' } },
});

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.hydrated = true;
  mocks.getConceptConflicts.mockResolvedValue(inventory('project-a', ['a', 'b']));
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.restoreAllMocks();
});

it('ignores the older comparison response when a newer path finishes first', async () => {
  let resolveA!: (value: ReturnType<typeof comparison>) => void;
  let resolveB!: (value: ReturnType<typeof comparison>) => void;
  mocks.compareConceptConflict.mockImplementation((path: string, projectID: string) => new Promise((resolve) => {
    if (projectID !== 'project-a') throw new Error('unexpected project');
    if (path === 'wiki/a.md') resolveA = resolve;
    if (path === 'wiki/b.md') resolveB = resolve;
  }));
  render(<ConceptConflictsPage />);
  const a = await screen.findByRole('button', { name: /wiki\/a\.md/ });
  const b = screen.getByRole('button', { name: /wiki\/b\.md/ });
  fireEvent.click(a);
  fireEvent.click(b);

  await waitFor(() => expect(mocks.compareConceptConflict).toHaveBeenCalledTimes(2));
  await act(async () => { resolveB(comparison('b')); });
  expect(await screen.findByText('candidate b')).toBeDefined();
  await act(async () => { resolveA(comparison('a')); });
  expect(screen.getByText('candidate b')).toBeDefined();
  expect(screen.queryByText('candidate a')).toBeNull();
});

it('discards a pending comparison when the selected project changes', async () => {
  let resolveA!: (value: ReturnType<typeof comparison>) => void;
  mocks.getConceptConflicts
    .mockReset()
    .mockResolvedValueOnce(inventory('project-a', ['a']))
    .mockResolvedValueOnce(inventory('project-b', []));
  mocks.compareConceptConflict.mockImplementation(() => new Promise((resolve) => { resolveA = resolve; }));
  const view = render(<ConceptConflictsPage />);
  fireEvent.click(await screen.findByRole('button', { name: /wiki\/a\.md/ }));
  await waitFor(() => expect(mocks.compareConceptConflict).toHaveBeenCalledTimes(1));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ConceptConflictsPage />);
  await waitFor(() => expect(screen.getByText('There are no eligible same-path conflicts to review.')).toBeDefined());
  await act(async () => { resolveA(comparison('a')); });
  expect(screen.queryByText('candidate a')).toBeNull();
});

it('does not leave Project A content or actions visible when Project B inventory is refused', async () => {
  let rejectProjectB!: (reason: Error) => void;
  mocks.getConceptConflicts
    .mockReset()
    .mockResolvedValueOnce(inventory('project-a', ['a']))
    .mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectProjectB = reject; }));
  mocks.compareConceptConflict.mockResolvedValue(comparison('a'));
  const view = render(<ConceptConflictsPage />);

  fireEvent.click(await screen.findByRole('button', { name: /wiki\/a\.md/ }));
  expect(await screen.findByText('candidate a')).toBeDefined();

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ConceptConflictsPage />);
  await waitFor(() => expect(mocks.getConceptConflicts).toHaveBeenCalledTimes(2));
  expect(screen.getByText('Loading conflict inventory…')).toBeDefined();
  expect(screen.queryByText('candidate a')).toBeNull();
  expect(screen.queryByRole('button', { name: /Publish candidate over current version/ })).toBeNull();
  await act(async () => { rejectProjectB(new Error('Project B inventory unavailable')); });

  expect((await screen.findByRole('alert')).textContent).toContain('Project B inventory unavailable');
  expect(screen.queryByText('candidate a')).toBeNull();
  expect(screen.queryByRole('button', { name: /Publish candidate over current version/ })).toBeNull();
  expect(screen.queryByText('There are no eligible same-path conflicts to review.')).toBeNull();
});

it('opens Project B comparison content after switching from a loaded Project A comparison', async () => {
  mocks.getConceptConflicts
    .mockReset()
    .mockResolvedValueOnce(inventory('project-a', ['a']))
    .mockResolvedValueOnce(inventory('project-b', ['b']));
  mocks.compareConceptConflict.mockImplementation((path: string) => Promise.resolve(comparison(path.endsWith('b.md') ? 'b' : 'a', path.endsWith('b.md') ? 'project-b' : 'project-a')));
  const view = render(<ConceptConflictsPage />);

  fireEvent.click(await screen.findByRole('button', { name: /wiki\/a\.md/ }));
  expect(await screen.findByText('candidate a')).toBeDefined();

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ConceptConflictsPage />);
  fireEvent.click(await screen.findByRole('button', { name: /wiki\/b\.md/ }));
  expect(await screen.findByText('candidate b')).toBeDefined();
  expect(screen.queryByText('candidate a')).toBeNull();
});

it('ignores a delayed Project A comparison failure after Project B opens', async () => {
  let rejectA!: (reason: Error) => void;
  mocks.getConceptConflicts
    .mockReset()
    .mockResolvedValueOnce(inventory('project-a', ['a']))
    .mockResolvedValueOnce(inventory('project-b', ['b']));
  mocks.compareConceptConflict.mockImplementation((path: string) => path.endsWith('a.md')
    ? new Promise((_resolve, reject) => { rejectA = reject; })
    : Promise.resolve(comparison('b', 'project-b')));
  const view = render(<ConceptConflictsPage />);

  fireEvent.click(await screen.findByRole('button', { name: /wiki\/a\.md/ }));
  await waitFor(() => expect(mocks.compareConceptConflict).toHaveBeenCalledTimes(1));
  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ConceptConflictsPage />);
  fireEvent.click(await screen.findByRole('button', { name: /wiki\/b\.md/ }));
  expect(await screen.findByText('candidate b')).toBeDefined();

  await act(async () => { rejectA(new Error('Project A comparison failed')); });
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByText('candidate b')).toBeDefined();
});

it('does not reload Project A when its decision resolves after switching to Project B', async () => {
  let resolveDecision!: (value: { status: string }) => void;
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  mocks.getConceptConflicts
    .mockReset()
    .mockResolvedValueOnce(inventory('project-a', ['a']))
    .mockResolvedValueOnce(inventory('project-b', []));
  mocks.compareConceptConflict.mockResolvedValue(comparison('a'));
  mocks.resolveConceptConflict.mockImplementation(() => new Promise((resolve) => { resolveDecision = resolve; }));
  const view = render(<ConceptConflictsPage />);

  fireEvent.click(await screen.findByRole('button', { name: /wiki\/a\.md/ }));
  await screen.findByText('candidate a');
  fireEvent.click(screen.getByRole('button', { name: /Keep published and discard this candidate/ }));
  await waitFor(() => expect(mocks.resolveConceptConflict).toHaveBeenCalledTimes(1));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ConceptConflictsPage />);
  expect(await screen.findByText('There are no eligible same-path conflicts to review.')).toBeDefined();
  await act(async () => { resolveDecision({ status: 'committed' }); });

  expect(mocks.getConceptConflicts).toHaveBeenCalledTimes(2);
  expect(screen.getByText('There are no eligible same-path conflicts to review.')).toBeDefined();
});
