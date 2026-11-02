import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { ProjectContentContextPanel } from '@/components/ProjectContentContextPanel';
import { ProjectSelect } from '@/components/ui/ProjectSelect';

const mocks = vi.hoisted(() => ({ getContext: vi.fn() }));
vi.mock('@/lib/projects', () => ({ getProjectContentContext: mocks.getContext }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function context(projectId: string, text: string) {
  return {
    project_id: projectId, status: 'ready', current_generation: 'generation-1',
    current_concepts_sha256: 'a'.repeat(64), stale: false,
    summary: {
      text, source_generation: 'generation-1', concepts_sha256: 'a'.repeat(64),
      generated_at: '2026-11-02T00:00:00Z', prompt_revision: 'legacy', profile_source: 'personal' as const,
      role: 'fast', requested_model: 'model', returned_model: 'model', input_digest: 'b'.repeat(64),
      selection_version: 'selection-v1',
    },
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it('drops a late content context response after switching projects', async () => {
  const requestA = deferred<ReturnType<typeof context>>();
  const requestB = deferred<ReturnType<typeof context>>();
  mocks.getContext.mockImplementation((projectId: string) => projectId === 'project-a' ? requestA.promise : requestB.promise);
  const mounted = render(<ProjectContentContextPanel projectId="project-a" />);
  mounted.rerender(<ProjectContentContextPanel projectId="project-b" />);

  await act(async () => { requestB.resolve(context('project-b', 'Project B summary')); });
  expect(await screen.findByText('Project B summary')).toBeTruthy();
  await act(async () => { requestA.resolve(context('project-a', 'Stale Project A summary')); });
  expect(screen.queryByText('Stale Project A summary')).toBeNull();
  expect(screen.getByText('Project B summary')).toBeTruthy();
});

it('refreshes the project list when a project selector opens', () => {
  const onOpen = vi.fn();
  render(<ProjectSelect projects={[{ id: 'project-a', name: 'Project A' }]} value="project-a" placeholder="Choose Wiki" onChange={vi.fn()} onOpen={onOpen} />);
  fireEvent.click(screen.getByRole('button', { name: 'Project A' }));
  expect(onOpen).toHaveBeenCalledTimes(1);
  expect(screen.getByRole('listbox')).toBeTruthy();
});

it('keeps every Wiki reachable in the Home selector while recent cards remain separately capped', async () => {
  render(<ProjectSelect projects={Array.from({ length: 6 }, (_, index) => ({ id: `project-${index + 1}`, name: `Project ${index + 1}` }))} value="" placeholder="Choose Wiki" onChange={vi.fn()} />);
  fireEvent.click(screen.getByRole('button', { name: 'Choose Wiki' }));
  expect(screen.getByRole('option', { name: 'Project 6' })).toBeTruthy();
  const source = await readFile(resolve(process.cwd(), 'src/components/HomeClient.tsx'), 'utf8');
  expect(source).toMatch(/\.slice\(0, 4\)\.map\(/);
  expect(source).not.toMatch(/maxResults=\{4\}/);
});

it('refreshes last-used metadata on Home return and when the Home tab regains focus', async () => {
  const source = await readFile(resolve(process.cwd(), 'src/components/HomeClient.tsx'), 'utf8');
  expect(source).toMatch(/pathname !== '\/' \|\| !token/);
  expect(source).toMatch(/window\.addEventListener\('focus', refreshWhenActive\)/);
  expect(source).toMatch(/ProjectSelect[\s\S]*?onOpen=\{\(\) => \{ void refreshProjects\(\); \}\}/);
  expect(source).toContain("t('Home.viewAllWikis')");
});
