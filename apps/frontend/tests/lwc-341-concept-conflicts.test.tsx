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

const comparison = (name: string) => ({
  project_id: 'project-a', storage_view: `observed-${name}`, pair_sha256: `pair-${name}`,
  conflict_id: `conflict-${name}`, index_sha256: 'index', confidence: 0.9, min_confidence: 0,
  published: { path: `wiki/${name}.md`, sha256: `published-${name}`, content: `published ${name}`, identity: { exists: true, status: 'published' } },
  candidate: { path: `wiki/.drafts/${name}.md`, sha256: `candidate-${name}`, content: `candidate ${name}`, identity: { exists: true, status: 'draft' } },
});

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.hydrated = true;
  mocks.getConceptConflicts.mockResolvedValue({ project_id: 'project-a', storage_view: 'observed', pair_sha256: 'pairs', new_count: 2, pending_count: 2, held_back_count: 0, conflicts: [conflict('a'), conflict('b')], held_back: [], count: 2 });
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
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
    .mockResolvedValueOnce({ project_id: 'project-a', storage_view: 'observed', pair_sha256: 'pairs', new_count: 1, pending_count: 1, held_back_count: 0, conflicts: [conflict('a')], held_back: [], count: 1 })
    .mockResolvedValueOnce({ project_id: 'project-b', storage_view: 'observed-b', pair_sha256: 'pairs-b', new_count: 0, pending_count: 0, held_back_count: 0, conflicts: [], held_back: [], count: 0 });
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
