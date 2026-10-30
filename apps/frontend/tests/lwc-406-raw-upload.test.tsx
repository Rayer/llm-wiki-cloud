import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { RawFile, RawUploadResult } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  token: 'session-a' as string | null,
  isDemoSession: false,
  getRawFiles: vi.fn(),
  getRawFilePreview: vi.fn(),
  uploadRawFile: vi.fn(),
  triggerPipeline: vi.fn(),
  refreshNavCounts: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getRawFiles: mocks.getRawFiles,
  getRawFilePreview: mocks.getRawFilePreview,
  uploadRawFile: mocks.uploadRawFile,
  triggerPipeline: mocks.triggerPipeline,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    token: mocks.token,
    isDemoSession: mocks.isDemoSession,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  const { i18nLaneTestTranslate } = await vi.importActual<typeof import('./i18n-lane-test-helper')>('./i18n-lane-test-helper');
  return { useT: () => ({ t: (key: string, params?: Record<string, string | number>) => i18nLaneTestTranslate(actual.translate, key, params) }) };
});
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams() }));

import { RawClient } from '@/components/RawClient';

const uploadedFile: RawFile = {
  name: 'onboarding.md',
  size: 42,
  updated: '2026-10-18T15:00:00Z',
  sha256: 'actual-fixture-digest',
  ingested: false,
  raw: {},
};

const createdResult: RawUploadResult = {
  filename: uploadedFile.name,
  path: 'raw/onboarding.md',
  bytes: uploadedFile.size,
  sha256: uploadedFile.sha256,
  status: 'created',
};

function uploadInput() {
  const input = document.querySelector<HTMLInputElement>('#raw-file-upload');
  if (!input) throw new Error('Raw upload file input is not mounted');
  return input;
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.token = 'session-a';
  mocks.isDemoSession = false;
  mocks.getRawFiles.mockResolvedValue([]);
  mocks.getRawFilePreview.mockResolvedValue('Synthetic content stored by the local fixture.');
  mocks.uploadRawFile.mockResolvedValue(createdResult);
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it('uploads through the shared queue, reads the new Raw entry back, and previews its content without running Pipeline', async () => {
  mocks.getRawFiles
    .mockResolvedValueOnce([])
    .mockResolvedValueOnce([uploadedFile]);
  render(<RawClient />);

  expect(await screen.findByRole('heading', { name: 'Raw.uploadTitle' })).toBeDefined();
  const file = new File(['Synthetic content stored by the local fixture.'], uploadedFile.name, { type: 'text/markdown' });
  await act(async () => {
    fireEvent.change(uploadInput(), { target: { files: [file] } });
  });

  await screen.findByText('Created');
  await waitFor(() => expect(mocks.getRawFiles).toHaveBeenCalledTimes(2));
  expect(mocks.uploadRawFile).toHaveBeenCalledTimes(1);
  expect(mocks.uploadRawFile).toHaveBeenCalledWith(file, expect.any(Function), 'project-a');
  expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(1);
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
  expect(await screen.findByRole('button', { name: 'Raw.previewAria' })).toBeDefined();
  expect(screen.getByText('42 B')).toBeDefined();
  expect(screen.getByText('actual-fixture-digest')).toBeDefined();
  expect(screen.getByText('raw/onboarding.md')).toBeDefined();

  fireEvent.click(screen.getByRole('button', { name: 'Raw.previewAria' }));
  expect(await screen.findByText('Synthetic content stored by the local fixture.')).toBeDefined();
  expect(mocks.getRawFilePreview).toHaveBeenCalledWith(uploadedFile.name, 'project-a');
});

it('keeps multiple-file selection and queues every file at the raw root without a destination control', async () => {
  const secondFile: RawFile = { ...uploadedFile, name: 'second.md', sha256: 'second-fixture-digest' };
  mocks.getRawFiles.mockResolvedValueOnce([]).mockResolvedValueOnce([uploadedFile, secondFile]);
  render(<RawClient />);
  const firstUpload = new File(['First root content'], uploadedFile.name, { type: 'text/markdown' });
  const secondUpload = new File(['Second root content'], secondFile.name, { type: 'text/markdown' });

  await act(async () => {
    fireEvent.change(uploadInput(), { target: { files: [firstUpload, secondUpload] } });
  });

  expect(await screen.findByText('2 created · 0 already exists · 0 replaced · 0 failed · 0 in progress')).toBeDefined();
  await waitFor(() => expect(mocks.uploadRawFile).toHaveBeenCalledTimes(2));
  expect(mocks.uploadRawFile).toHaveBeenNthCalledWith(1, firstUpload, expect.any(Function), 'project-a');
  expect(mocks.uploadRawFile).toHaveBeenNthCalledWith(2, secondUpload, expect.any(Function), 'project-a');
  expect(screen.queryByLabelText(/destination directory/i)).toBeNull();
});

it('keeps an upload conflict visible and retries the file through the same uploader', async () => {
  mocks.getRawFiles
    .mockResolvedValueOnce([])
    .mockResolvedValueOnce([uploadedFile]);
  mocks.uploadRawFile
    .mockRejectedValueOnce(new Error('filename already exists with different content'))
    .mockResolvedValueOnce(createdResult);
  render(<RawClient />);
  const file = new File(['new content'], uploadedFile.name, { type: 'text/markdown' });

  await act(async () => {
    fireEvent.change(uploadInput(), { target: { files: [file] } });
  });
  expect(await screen.findByText('filename already exists with different content')).toBeDefined();
  fireEvent.click(screen.getByRole('button', { name: `Retry ${uploadedFile.name}` }));

  await screen.findByText('Created');
  await waitFor(() => expect(mocks.getRawFiles).toHaveBeenCalledTimes(2));
  expect(mocks.uploadRawFile).toHaveBeenCalledTimes(2);
});

it('keeps a late Project A upload pinned and out of the Project B list after switching Projects', async () => {
  let resolveUpload!: (result: RawUploadResult) => void;
  const pendingUpload = new Promise<RawUploadResult>((resolve) => {
    resolveUpload = resolve;
  });
  mocks.uploadRawFile.mockReturnValue(pendingUpload);
  mocks.getRawFiles
    .mockResolvedValueOnce([])
    .mockResolvedValueOnce([{ ...uploadedFile, name: 'project-b.md' }]);
  const view = render(<RawClient />);
  const file = new File(['A content'], uploadedFile.name, { type: 'text/markdown' });
  await act(async () => {
    fireEvent.change(uploadInput(), { target: { files: [file] } });
  });
  await waitFor(() => expect(mocks.uploadRawFile).toHaveBeenCalledTimes(1));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<RawClient />);
  expect(await screen.findByRole('button', { name: 'Raw.previewAria' })).toBeDefined();
  expect(screen.getByText('project-b.md')).toBeDefined();

  await act(async () => {
    resolveUpload(createdResult);
    await pendingUpload;
  });
  expect(screen.getByText('project-b.md')).toBeDefined();
  expect(screen.getByText('Created')).toBeDefined();
  expect(screen.getByText('RawUpload.projectLabel')).toBeDefined();
  expect(screen.queryByRole('row', { name: new RegExp(uploadedFile.name) })).toBeNull();
  expect(mocks.getRawFiles).toHaveBeenCalledTimes(2);
  expect(mocks.refreshNavCounts).not.toHaveBeenCalled();
});

it('clears queued upload state when the authenticated session changes', async () => {
  let resolveUpload!: (result: RawUploadResult) => void;
  const pendingUpload = new Promise<RawUploadResult>((resolve) => {
    resolveUpload = resolve;
  });
  mocks.uploadRawFile.mockReturnValue(pendingUpload);
  mocks.getRawFiles
    .mockResolvedValueOnce([])
    .mockResolvedValueOnce([{ ...uploadedFile, name: 'project-b.md' }]);
  const view = render(<RawClient />);
  const file = new File(['A content'], uploadedFile.name, { type: 'text/markdown' });
  await act(async () => fireEvent.change(uploadInput(), { target: { files: [file] } }));
  await waitFor(() => expect(mocks.uploadRawFile).toHaveBeenCalledTimes(1));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  mocks.token = 'session-b';
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<RawClient />);
  expect(await screen.findByText('project-b.md')).toBeDefined();

  await act(async () => {
    resolveUpload(createdResult);
    await pendingUpload;
  });
  expect(screen.queryByText(uploadedFile.name)).toBeNull();
  expect(screen.getByText('project-b.md')).toBeDefined();
  expect(mocks.getRawFiles).toHaveBeenCalledTimes(2);
  expect(mocks.refreshNavCounts).not.toHaveBeenCalled();
});

it('keeps uploads unavailable in Demo and hides the control when no project is selected', async () => {
  mocks.isDemoSession = true;
  const demoView = render(<RawClient />);
  const demoButton = await screen.findByRole('button', { name: 'Demo.restricted' });
  expect(demoButton.getAttribute('aria-disabled')).toBe('true');
  expect(uploadInput().disabled).toBe(true);
  fireEvent.click(demoButton);
  expect(mocks.uploadRawFile).not.toHaveBeenCalled();

  demoView.unmount();
  mocks.currentProject = null;
  mocks.isDemoSession = false;
  render(<RawClient />);
  expect(await screen.findByText('Raw.empty')).toBeDefined();
  expect(screen.queryByRole('heading', { name: 'Raw.uploadTitle' })).toBeNull();
});
